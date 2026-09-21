// qmt_feed.go — §ENH-5 批E：QMT Level-1 全推行情注入器。
// 生产决策机上由 qmt_gateway(/quotes, xtdata) 提供 L1 tick；本文件按 1~3s 轮询
// QuoteFeedSource（由 internal/trading.QMTClient 实现、经接口注入避免 data→trading 反向依赖），
// 命中代码合并覆盖 Fetcher 快照（Source="QMT-L1"），未命中/失败保持静默——
// 既有 5s 新浪批量链自兜底，Staleness 自然增长，绝不伪造新鲜度解除 §WS-C 陈旧行情闸。
// English: batch-E Level-1 quote feed — polls the gateway /quotes (xtdata-backed) every 1-3s and
// merge-injects hits into the Fetcher snapshot with Source="QMT-L1"; misses/failures stay silent so
// the existing 5s Sina chain keeps working and staleness keeps growing (never fakes freshness).
package data

import (
	"context"
	"log"
	"math"
	"sync"
	"sync/atomic"
	"time"
)

// QMTTick 网关 /quotes 单只 tick（Level-1 全推快照字段子集）。
// 字段与 qmt_gateway/quote_feed.py 透传的 xtdata get_full_tick 对齐。
// English: one Level-1 tick from gateway /quotes, aligned with xtdata get_full_tick passthrough.
type QMTTick struct {
	LastPrice float64 `json:"lastPrice"` // 最新价（元）
	Open      float64 `json:"open"`      // 今开（元）
	High      float64 `json:"high"`      // 最高（元）
	Low       float64 `json:"low"`       // 最低（元）
	PrevClose float64 `json:"prevClose"` // 昨收（元，§P1-5 口径统一用 PrevClose）
	Volume    float64 `json:"volume"`    // 成交量（网关原样透传，单位换算见 QMTFeed.volumeToShares）
	Amount    float64 `json:"amount"`    // 成交额（元）
	TickTime  int64   `json:"tickTime"`  // tick 时间戳（毫秒；0=网关未提供）
}

// QuoteFeedSource 行情 feed 数据源接口（唯一实现：trading.QMTClient.Quotes）。
// 定义在 data 侧并以接口注入：internal/trading 已依赖 internal/data，反向 import 会成环。
// codes 为裸 6 位码，返回 map key 同为裸码；无命中返回空 map（非错误）。
// English: feed source interface (implemented by trading.QMTClient.Quotes); defined in data to
// avoid an import cycle. Codes and returned keys are bare 6-digit codes.
type QuoteFeedSource interface {
	Quotes(ctx context.Context, codes []string) (map[string]QMTTick, error)
}

// QMTFeed L1 行情轮询注入器。生命周期与 Fetcher 平行：Start 阻塞、Stop 停。
// English: L1 feed poller with the same lifecycle shape as Fetcher (blocking Start, Stop to end).
type QMTFeed struct {
	fetcher  *Fetcher        // 注入目标快照的采集器
	src      QuoteFeedSource // tick 数据源（网关客户端）
	interval time.Duration   // 轮询间隔（1~3s 量级）
	maxAge   time.Duration   // tick 新鲜度上限：超龄 tick 丢弃（默认 30s）
	// volumeToShares tick.volume→StockInfo.Volume（股）的换算系数。
	// ⚠ 单位口径（§FIX-1 教训）：仓库内不存在 xtdata volume 字段的权威单位证据，
	// 部署 Windows 决策机时须先跑 scripts/probe_xtquant.py 打印原始 volume 并与新浪同日成交量比对，
	// 确认后用 SetVolumeToShares 固化（默认 1，即按"volume 单位=股"处理，与新浪链一致）。
	volumeToShares atomic.Uint64 // float64 位模式存储
	volFallback    float64       // 未显式设置时的回退系数
	loggedFirst    atomic.Bool   // 首轮命中只打一条 INFO，之后降为静默
	errorAt        atomic.Int64  // 错误日志节流（unix 秒）
	stopCh         chan struct{}
	stopOnce       sync.Once
}

// NewQMTFeed 构造 feed：interval<=0 取 3s，maxAge<=0 取 30s，volToShares<=0 取 1。
// English: constructs the feed (defaults interval 3s / maxAge 30s / volToShares 1).
func NewQMTFeed(fetcher *Fetcher, src QuoteFeedSource, interval, maxAge time.Duration, volToShares float64) *QMTFeed {
	if interval <= 0 {
		interval = 3 * time.Second
	}
	if maxAge <= 0 {
		maxAge = 30 * time.Second
	}
	if volToShares <= 0 {
		volToShares = 1
	}
	f := &QMTFeed{
		fetcher:     fetcher,
		src:         src,
		interval:    interval,
		maxAge:      maxAge,
		volFallback: volToShares,
		stopCh:      make(chan struct{}),
	}
	f.volumeToShares.Store(math.Float64bits(volToShares))
	return f
}

// SetVolumeToShares 单位校准后固化 tick.volume→股 的换算系数（0 忽略）。
// English: pins the tick.volume → shares factor once verified on the production machine.
func (f *QMTFeed) SetVolumeToShares(v float64) {
	if v > 0 {
		f.volumeToShares.Store(math.Float64bits(v))
	}
}

// volFactor 读取当前换算系数（位模式还原，无浮点原子则回退构造值）。
func (f *QMTFeed) volFactor() float64 {
	if bits := f.volumeToShares.Load(); bits != 0 {
		return math.Float64frombits(bits)
	}
	return f.volFallback
}

// Stop 幂等停止轮询循环。
// English: Stop ends the polling loop (idempotent).
func (f *QMTFeed) Stop() {
	f.stopOnce.Do(func() { close(f.stopCh) })
}

// Start 阻塞轮询：仅活跃交易时段拉取（休市用 DurationToNextActiveSession 长睡，
// 省网关与 xtdata 负载）；每轮 WatchCodes() 动态取监控池，命中则合并注入。
// English: blocking poll loop — only during active sessions (idle sleeps until the next one),
// refreshing the watch pool each round and merge-injecting hits.
func (f *QMTFeed) Start() {
	ticker := time.NewTicker(f.interval)
	defer ticker.Stop()
	for {
		select {
		case <-f.stopCh:
			return
		case <-ticker.C:
		}
		if !IsActiveSession(time.Now()) {
			d := DurationToNextActiveSession(time.Now())
			if d < f.interval {
				d = f.interval
			}
			// 休眠至下个活跃窗口（或 Stop），期间不产生任何请求
			select {
			case <-f.stopCh:
				return
			case <-time.After(d):
			}
			ticker.Reset(f.interval)
			continue
		}
		f.pollOnce()
	}
}

// pollOnce 单轮：取池→拉 tick→合并注入；任何失败仅记日志（节流），绝不 panic。
// English: one round: pool -> fetch ticks -> merge inject; failures are throttled logs only.
func (f *QMTFeed) pollOnce() {
	codes := f.fetcher.WatchCodes()
	if len(codes) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*f.interval)
	defer cancel()
	ticks, err := f.src.Quotes(ctx, codes)
	if err != nil {
		f.logThrottled("qmt_feed: /quotes 拉取失败（新浪链继续兜底，不影响行情可用性）: %v", err)
		return
	}
	if n := f.applyTicks(ticks); n > 0 && !f.loggedFirst.Swap(true) {
		log.Printf("[qmt_feed] §ENH-5 L1 行情已生效: 命中 %d/%d 只, Source=QMT-L1", n, len(codes))
	}
}

// applyTicks 把有效 tick 合并进当前快照并整体注入。
// 三个关键约束：
//  1. Fetcher.Snapshot() 只做浅拷贝，*StockInfo 与内部快照共享指针——必须复制新 struct 再改，
//     否则与 5s 采集循环并发写同一指针（-race 可抓到）；
//  2. 仅命中 >=1 只时才调 IngestSnapshot——该调用会刷新 lastOK，无条件注入会掩盖
//     新浪链断流并误解除 §WS-C 陈旧行情闸（反向污染，比熔断污染更危险）；
//  3. Name/Sector/NetInflow/HasFlow 等 tick 不携带的字段一律保留原值，不清零。
//
// English: merges valid ticks into a snapshot copy and injects once. Only injects on >=1 hit
// (injection refreshes lastOK, so unconditional use would mask Sina outages and disarm the
// staleness gate); StockInfo entries are copied before mutation because Snapshot shares pointers.
func (f *QMTFeed) applyTicks(ticks map[string]QMTTick) int {
	if len(ticks) == 0 {
		return 0
	}
	base := f.fetcher.Snapshot()
	if base == nil {
		return 0 // 首轮采集还没跑：不注入空壳快照，等新浪链建立基线
	}
	if base.Stocks == nil {
		base.Stocks = make(map[string]*StockInfo, len(ticks))
	}
	now := time.Now()
	factor := f.volFactor()
	hits := 0
	for code, tk := range ticks {
		if tk.LastPrice <= 0 {
			continue // 停牌/无 tick：lastPrice=0 是常态，跳过保留新浪值
		}
		if tk.TickTime > 0 && now.Sub(time.UnixMilli(tk.TickTime)) > f.maxAge {
			continue // 超龄 tick：feed 断流时网关仍会回缓存，宁可退回新浪
		}
		old := base.Stocks[code]
		cp := StockInfo{Code: code}
		if old != nil {
			cp = *old // 保留 Name/Sector/资金流等 tick 没有的字段
		}
		cp.Price = tk.LastPrice
		cp.Open = tk.Open
		cp.High = tk.High
		cp.Low = tk.Low
		cp.PrevClose = tk.PrevClose
		cp.Volume = tk.Volume * factor
		cp.Amount = tk.Amount
		if tk.PrevClose > 0 {
			cp.ChangePct = (tk.LastPrice/tk.PrevClose - 1) * 100
		}
		base.Stocks[code] = &cp
		hits++
	}
	if hits == 0 {
		return 0
	}
	base.Source = QuoteSourceQMTL1 // §M1 枚举常量（/api/status quote_source 契约单源化）
	base.Time = now
	f.fetcher.IngestSnapshot(base)
	return hits
}

// logThrottled feed 专属错误日志节流（60s 一条），不复用采集链的 lastStaleWarn。
// English: feed-local 60s-throttled error logging, separate from the fetch chain's warn state.
func (f *QMTFeed) logThrottled(format string, args ...any) {
	now := time.Now().Unix()
	prev := f.errorAt.Swap(now)
	if now-prev >= 60 {
		log.Printf("[qmt_feed] "+format, args...)
	}
}
