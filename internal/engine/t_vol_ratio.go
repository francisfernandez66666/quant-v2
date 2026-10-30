package engine

import (
	"sync"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/data"
)

// ── §W-09 个股量能门读数：折算量比 ─────────────────────────────────────────────
//
// 为什么必须复用 combat_agent.IntradayVolumeRatio 而不是就地写 `q.Volume / avg20`：
// 同一指标在本仓已有三处消费（momentum.go / sell_side.go / nwave.go），三处都依赖
// "按已流逝交易分钟折算"这个修正。自己写一份不折算的，结果是**上午的放量永远测不出来**
// （09:35 的累计量只有全日的零头，同一放量强度测出的量比约为 14:55 的 1/5）⇒ 门形同不设，
// 而且同一分钟两个读数互相矛盾，排障时无法判断哪个是真的。

const (
	// tVolRatioTTLOk 成功缓存 30 分钟：20 日均量盘中不变，5s 循环绝不逐轮打上游。
	tVolRatioTTLOk = 30 * time.Minute
	// tVolRatioTTLErr 失败缓存 60 秒。两个 TTL 必须不同，且**都要缓存**：
	// 失败也缓存 30 分钟 ⇒ 一次瞬时抖动让该票半天做不了 T；
	// 失败完全不缓存 ⇒ 源挂了就是 20 只票 × 每 5 秒一次 ≈ 每秒 4 次打上游。
	tVolRatioTTLErr = 60 * time.Second
	// tVolRatioAvgDays 均量取前 20 个交易日。
	tVolRatioAvgDays = 20
	// tVolRatioNeed 日 K 最少根数 = 均量窗口 + 1（最后一根可能是今日未完成 K，要剔掉）。
	tVolRatioNeed = tVolRatioAvgDays + 1
)

// tVolEntry 单票量比缓存条目。day 用于跨日丢弃——昨天的量比不能用来关今天的 T。
type tVolEntry struct {
	v   float64
	at  time.Time
	day string
	ok  bool
}

// tVolCache 量比缓存（按代码，成功/失败两个 TTL）。
type tVolCache struct {
	mu sync.Mutex
	m  map[string]tVolEntry
}

func newTVolCache() *tVolCache { return &tVolCache{m: map[string]tVolEntry{}} }

// get 命中返回 (值, 成功与否, true)；未命中返回 (0,false,false)。
func (c *tVolCache) get(code, day string, now time.Time) (float64, bool, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, has := c.m[code]
	if !has || e.day != day {
		return 0, false, false
	}
	ttl := tVolRatioTTLErr
	if e.ok {
		ttl = tVolRatioTTLOk
	}
	if now.Sub(e.at) >= ttl {
		return 0, false, false
	}
	return e.v, e.ok, true
}

func (c *tVolCache) put(code, day string, now time.Time, v float64, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[code] = tVolEntry{v: v, at: now, day: day, ok: ok}
}

// tVolRatioOf 逐票折算量比：前 20 日均量（**剔除今日未完成 K 线**）→ IntradayVolumeRatio。
//
// ok=false 表示"读不到"（K 线链失败 / 根数不足 / 当日无量），调用方按**不过**处理，不当 0 用
// （volumeBlock 的 fail-closed 纪律：判据缺了就不能判"通过"）。
//
// 缓存按 code 落、跨日丢弃。成功与失败分开缓存（两个 TTL），理由见常量处注释。
// English: per-name time-prorated volume ratio with a split-TTL cache; ok=false means "unreadable".
func (e *Engine) tVolRatioOf(code string) (float64, bool) {
	// 走引擎统一时钟（SetClock），测试才能"跨日"而不依赖真实日期；生产默认 time.Now。
	now := e.nowTime()
	day := data.TradingDayDate(now)
	if e.tVolCache != nil {
		if v, ok, hit := e.tVolCache.get(code, day, now); hit {
			return v, ok
		}
	}
	v, ok := e.tVolRatioCompute(code, now, day)
	if e.tVolCache != nil {
		e.tVolCache.put(code, day, now, v, ok)
	}
	return v, ok
}

// tVolRatioCompute 真正取数（K 线是网络调用，绝不在持 e.mu 时做）。
func (e *Engine) tVolRatioCompute(code string, now time.Time, day string) (float64, bool) {
	e.mu.RLock()
	dc := e.coordinator
	f := e.fetcher
	klineFn := e.tKLineFn
	cumVolFn := e.tCumVolFn
	e.mu.RUnlock()
	if dc == nil && klineFn == nil {
		return 0, false
	}
	var (
		kl  []data.KLine
		err error
	)
	if klineFn != nil {
		// 测试注入点：与 dc.GetKLine 同一签名、同一"101=日线"口径，生产路径不走这里。
		kl, err = klineFn(code, "101", tVolRatioNeed+4)
	} else {
		kl, err = dc.GetKLine(code, "101", tVolRatioNeed+4) // 101=日线；多取几根留余量
	}
	if err != nil || len(kl) < tVolRatioNeed {
		// 新股/停牌复牌/源失败 ⇒ 不拿 10 根冒充"20 日均量"。
		return 0, false
	}
	// 剔除今日未完成 K 线（与 nshape_input.go 同口径）：不剔除会让"今天已经放了大量"
	// 把分母抬高，反而测不出放量——门就反过来成了"放量之后才允许做 T"。
	if len(kl) > 0 && cntime.DayOf(kl[len(kl)-1].Date) == cntime.DayOf(now) {
		kl = kl[:len(kl)-1]
	}
	if len(kl) < tVolRatioAvgDays {
		return 0, false
	}
	bars := kl[len(kl)-tVolRatioAvgDays:]
	var sum float64
	for _, b := range bars {
		sum += b.Volume
	}
	avg := sum / float64(len(bars))
	if avg <= 0 {
		return 0, false
	}
	// 当日累计量取本轮快照（与 T 的其他读数同源，不另取一次快照）。
	var cumVol float64
	if cumVolFn != nil {
		cumVol, _ = cumVolFn(code)
	} else if f != nil {
		if q := f.SnapshotQuote(code); q != nil {
			cumVol = q.Volume
		}
	}
	if cumVol <= 0 {
		return 0, false
	}
	return combat_agent.IntradayVolumeRatio(now, cumVol, avg), true
}
