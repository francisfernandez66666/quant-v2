// qmt_feed.go — §ENH-5 批E：QMT Level-1 全推行情注入器（§SUBSCRIPTION-A 起改为 overlay 语料生产者）。
// 生产决策机上由 qmt_gateway(/quotes, xtdata) 提供 L1 tick；本文件按 1~3s 轮询
// QuoteFeedSource（由 internal/trading.QMTClient 实现、经接口注入避免 data→trading 反向依赖），
// 把命中的 tick 交给 Fetcher 的行情装配层（quote_feed_hub.go），**不再自己整份塞快照**。
//
// 为什么必须改这一刀（2026-11-01，docs/IMPL_QMT_DATA_SUBSCRIPTION_20261101.md A4/A5）：
//
//	① 旧实现命中 ≥1 只就整份注入并刷新全局 lastOK，而那个基线喂的是发单前的行情新鲜度硬闸
//	   ⇒ 一只票的新鲜替全池 200 只作了保（伪造新鲜，比熔断污染更危险）；
//	② 5s 采集链每轮重建整份快照，feed 事后注入的券商价下一轮就被外部源顶掉
//	   ⇒ "主源"名不副实，价格每 5s 来回抖（双写者谁都没赢，只有抖）。
//
// 现在：feed 只产 tick + 每轮读数，合并由采集链在写快照前单点完成（applyFeedOverlay），
// 全局基线只在覆盖率达标时才刷（coverageGateOK）。
//
// 三档行为（2026-11-01 owner 改判后，出厂档＝第三档「主源模式」；前两档都保留为显式关掉时的回滚位）：
//
//	qmt_feed_enabled=false      → 本文件根本不启动（显式写 false 才到这档）
//	enabled=true, primary=false → 观察模式：轮询、统计覆盖率/降级读数，**一个价格都不改写**
//	enabled=true, primary=true  → 主源模式（出厂档）：写 overlay，覆盖率达标才刷全局新鲜度
//
// English: L1 feed poller turned into an overlay producer; observe mode records numbers without
// touching a single price, primary mode merges through the single-writer path in Fetcher.
package data

import (
	"context"
	"errors"
	"log"
	"math"
	"runtime/debug"
	"sort"
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
	Volume    float64 `json:"volume"`    // 成交量（网关原样透传，单位换算见 FeedPolicy.VolumeToShares）
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

// QMTFeed L1 行情轮询器。生命周期与 Fetcher 平行：Start 阻塞、Stop 停。
// English: L1 feed poller with the same lifecycle shape as Fetcher (blocking Start, Stop to end).
type QMTFeed struct {
	fetcher  *Fetcher        // 装配目标（overlay/覆盖率/降级读数都落在它身上）
	src      QuoteFeedSource // tick 数据源（网关客户端）
	interval time.Duration   // 轮询间隔（1~3s 量级）
	maxAge   time.Duration   // tick 新鲜度上限：超龄 tick 丢弃（默认 30s）
	// 量纲系数 tick.volume→股。⚠ 单位口径（§FIX-1 教训）：仓库内不存在 xtdata volume 字段的
	// 权威单位证据，必须由盘中探针（scripts/probe_xtquant.py 的 amount/(price*volume) 判读）
	// 确认后固化；错 100 倍会把 VWAP（=Amount/Volume）算成 730 元这种量级，直接进钱的路径。
	// English: volume→shares factor; must be pinned by the intraday probe — a 100x error lands
	// straight on VWAP and therefore on order assembly and T slippage.
	volFallback float64
	volAtomic   atomic.Uint64 // float64 位模式存储，热更换不重开 goroutine
	loggedFirst atomic.Bool   // 首轮命中只打一条 INFO，之后降为静默
	observedAt  atomic.Int64  // 观察模式关态读数（feed_observed）的 60s 节流位（与错误日志各自的位点）
	errorAt     atomic.Int64  // 错误日志节流（unix 秒）
	failStreak  atomic.Int64  // 连续失败轮次（J1 通道不通的判据输入）
	stopCh      chan struct{}
	stopOnce    sync.Once
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
	f.volAtomic.Store(math.Float64bits(volToShares))
	return f
}

// SetVolumeToShares 单位校准后固化 tick.volume→股 的换算系数（0 忽略）。
// 同时写进 Fetcher 的策略，采集链合并 overlay 时读的是同一份数——两处必须同源，
// 否则会出现"feed 按 100 折、快照按 1 折"那种同一条链两个口径的形状（VWAP 直接差 100 倍）。
// English: pins the tick.volume → shares factor in BOTH the feed and the fetcher policy so the
// merge path and the producer path can never disagree about VWAP's denominator.
func (f *QMTFeed) SetVolumeToShares(v float64) {
	if v <= 0 {
		return
	}
	f.volAtomic.Store(math.Float64bits(v))
	p := f.fetcher.feedPolicyNow()
	p.VolumeToShares = v
	f.fetcher.SetFeedPolicy(p)
}

// SetPrimary 决定券商 tick 是否可以改写快照价格（false＝观察模式，一个价都不动）。
// 这里零值是 false 是"结构体未装配"的安全态；生产走的是配置层出厂开（RuntimeConfig.QMTFeedPrimaryOn，
// 缺键=开），装配点 main.go 将生效值灌进来——两者不是一回事，别在这里改默认去"表达"出厂档。
// English: promote the feed from observe-only to price-authoritative; the zero value here is the
// un-assembled safe state, while the factory-on ruling lives in the config accessor wired at startup.
func (f *QMTFeed) SetPrimary(on bool) {
	p := f.fetcher.feedPolicyNow()
	p.Primary = on
	if p.MaxAge <= 0 {
		p.MaxAge = f.maxAge
	}
	if p.VolumeToShares <= 0 {
		p.VolumeToShares = f.volFactor()
	}
	f.fetcher.SetFeedPolicy(p)
	if on {
		thr := p.CoverageMinPct
		if thr == 0 {
			thr = feedCoverageDefault // 日志必须打生效值：打 0 会被读成"没设阈值"，而实际按 80 判
		}
		log.Printf("[qmt_feed] §SUBSCRIPTION-A 券商 L1 已提升为主源（覆盖率阈值 %d%%，量纲系数 %.0f）",
			thr, p.VolumeToShares)
	}
}

// volFactor 读取当前换算系数（位模式还原，无浮点原子则回退构造值）。
func (f *QMTFeed) volFactor() float64 {
	if bits := f.volAtomic.Load(); bits != 0 {
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
// 省网关与 xtdata 负载）；每轮 WatchCodes() 动态取监控池，命中则交给装配层。
// English: blocking poll loop — only during active sessions (idle sleeps until the next one),
// refreshing the watch pool each round.
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
		f.pollOnceGuarded()
	}
}

// pollOnceGuarded 每轮包一层 recover：行情腿**不许成为交易链的停机原因**。
// 2026-11-02 的教训是同形状的——资金心跳（也是只读观测腿）一次 nil 解引用把整个进程打死，
// 现网 131 次重启、整天零买入评估。pollOnce 的注释本来就写了"绝不 panic"，但那是意图不是强制；
// 这里补上强制，并把栈打出来（吞掉异常文本等于把下次排障退回盲猜）。
// 守卫按**每一轮**收：挂在 Start 顶部只兜第一次，之后轮询静默停摆——那正是
// FeedStats.Rounds 要防的"冒充在跑"形态，不能由我们的兜底手段自己造出来。
func (f *QMTFeed) pollOnceGuarded() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[qmt-feed] 本轮行情轮询 panic（跳过本轮，不影响交易链）: %v\n%s", r, debug.Stack())
		}
	}()
	f.pollOnce()
}

// pollOnce 单轮：取池→拉 tick→交给装配层；任何失败只记日志（节流），绝不 panic。
// 失败轮必须把 J1 计数递上去并**立刻收回主源档**：后备链一直在跑，缩池是安全方向。
// English: one round. Failures feed the J1 counter and drop the pool tier immediately (shrinking
// is the safe direction); they never refresh freshness.
func (f *QMTFeed) pollOnce() {
	codes := f.fetcher.WatchCodes()
	if len(codes) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*f.interval)
	defer cancel()
	ticks, err := f.src.Quotes(ctx, codes)
	if err != nil {
		n := f.failStreak.Add(1)
		// 钥匙被撤/无效（401/403）与"网关挂了"必须分形：前者要补凭证、后者要查进程。
		// 判据走哨兵 errors.Is，不猜状态码字符串（§0925EVE-W3-E 的口径）。
		authFailed := errors.Is(err, ErrQuoteUnauthorized)
		if authFailed {
			f.logThrottled("qmt_feed: /quotes 被鉴权拒绝（第 %d 轮）——**行情主源没在用**，"+
				"请确认运营账号在册凭证；行情照旧走现行链，不影响交易链路: %v", n, err)
		} else {
			f.logThrottled("qmt_feed: /quotes 拉取失败（第 %d 轮，新浪链继续兜底，不影响行情可用性）: %v", n, err)
		}
		f.fetcher.NoteFeedFailure(int(n), authFailed)
		return
	}
	f.failStreak.Store(0)
	n := f.applyTicks(ticks, len(codes))
	if n > 0 && !f.loggedFirst.Swap(true) {
		log.Printf("[qmt_feed] §SUBSCRIPTION-A L1 首次命中: %d/%d 只（primary=%v）", n, len(codes), f.isPrimary())
	}
	// 观察模式每 60s 打一行 feed_observed：覆盖率、本可以改写多少只、是否已连续达标可开 primary。
	// 没有这行，"能不能开"就只能靠人翻日志数轮次——那是感觉式上线，正是本批要换掉的。
	if !f.isPrimary() {
		st := f.fetcher.FeedStatsNow()
		if st.Hits > 0 {
			f.logObservedThrottled("qmt_feed: feed_observed hits=%d/%d coverage=%.1f%% denom_src=%s "+
				"would_override=%d ready_to_promote=%d rounds=%d（观察模式，未改写任何价格）",
				st.Hits, st.Denominator, st.CoveragePct, st.DenominatorSource,
				boolInt(st.WouldOverride), boolInt(st.ReadyToPromote), st.CoverageRounds)
		}
	}
}

// isPrimary 当前是否主源模式（读数与日志用，不改行为）。
func (f *QMTFeed) isPrimary() bool { return f.fetcher.feedPolicyNow().Primary }

// applyTicks 把一轮 tick 变成 overlay 语料 + 本轮读数，交给 Fetcher 的唯一入口。
//
// 四个关键约束（前两条是 §ENH-5 原班约束，后两条是本批新增）：
//  1. 只把 lastPrice>0 的 tick 计为命中：停牌/无 tick 时券商回 0 是常态，必须保留外部源的值，
//     也绝不能因为"拿到了回包"就把这些票算进覆盖率分子（那是自欺）；
//  2. 超龄 tick（TickTime 比 now 老过 maxAge）丢弃：feed 断流时网关仍会回缓存，
//     宁可退回新浪值，不许拿陈旧 tick 冒充新鲜；
//  3. 覆盖率分母**不是池总数**，而是"当日可交易标的数"的近似＝本轮任一源给出有效价的只数；
//     分母不可信（0 只）才退回池总数并标 denominator=pool。
//     为什么：池一扩（400 只）停牌/无 tick 的占比自然抬头，按池总数算会把正常交易日误判成
//     "feed 没达标"，进而让全池显示陈旧、把发单闸拦死——收紧方向误伤正常状态是更坏的选择；
//  4. 读数不自洽（J3）：用券商自己的数验自己——amount/(lastPrice*volume) 的中位数应当落在
//     1（股）或 100（手）附近。既不≈1 也不≈100 ⇒ 单位/口径不成立 ⇒ 判 J3。
//     注意这**不是**"两源价格对不上"：owner 裁决 5 明确多源不一致以券商为准，只有券商
//     完全不通才降级；J3 抓的是"这个数根本不成立"，与行情差异是两件事。
//
// English: turns one poll into overlay material plus round statistics. Coverage denominator is the
// tradable universe (not the raw pool) so suspensions cannot fake a failure; J3 self-validates the
// broker's own numbers instead of comparing against Sina.
func (f *QMTFeed) applyTicks(ticks map[string]QMTTick, pool int) int {
	now := time.Now()
	base := f.fetcher.Snapshot()

	entries := make(map[string]QMTTick, len(ticks))
	merged := f.copySnapshotForMerge(base) // 主源模式下才用得到（浅拷贝 + 逐条复制条目）
	missingPrice := 0
	ratios := make([]float64, 0, len(ticks))
	for code, tk := range ticks {
		if tk.LastPrice <= 0 {
			missingPrice++
			continue // 约束 ①
		}
		if tk.TickTime > 0 && now.Sub(time.UnixMilli(tk.TickTime)) > f.maxAge {
			continue // 约束 ②
		}
		entries[code] = tk
		// amount/(price*volume)：单位是股时≈1，是手时≈100
		if tk.Volume > 0 && tk.Amount > 0 {
			ratios = append(ratios, tk.Amount/(tk.LastPrice*tk.Volume))
		}
	}

	st := FeedStats{
		Hits:            len(entries),
		Pool:            pool,
		Degrade:         "",
		MissingPricePct: 0,
	}
	if len(ticks) > 0 {
		st.MissingPricePct = float64(missingPrice) / float64(len(ticks)) * 100
	}

	// 约束 ③：分母取"任一源本轮给出有效价的只数"∪命中数（命中数是券商给的证据，也算有价）。
	denom, denomSource := 0, "tradable"
	if base != nil {
		for _, si := range base.Stocks {
			if si != nil && si.Price > 0 {
				denom++
			}
		}
	}
	for code := range entries {
		if base == nil || base.Stocks[code] == nil || base.Stocks[code].Price <= 0 {
			denom++ // 命中但外部源没有有效价 ⇒ 这条证据只有券商有，计入分母也计入分子
		}
	}
	if denom <= 0 {
		denom, denomSource = pool, "pool" // 分母不可信：退回池总数并如实标注，不静默用旧口径
	}
	st.Denominator = denom
	st.DenominatorSource = denomSource
	if denom > 0 {
		st.CoveragePct = float64(st.Hits) / float64(denom) * 100
	}

	// J2：一条都没命中但回包非空 ⇒ 拿到的全是超龄/零价 tick ＝通道在读旧数据，判降级。
	if len(ticks) > 0 && st.Hits == 0 {
		st.Degrade = "J2"
	}
	// J3：中位数既不≈1 也不≈100 ⇒ 单位/口径不成立。合法单位档（1 或 100）绝不触发——
	// 那种情形该做的是配系数（SetVolumeToShares），不是判通道坏。
	observed := float64(0)
	if r := medianRatio(ratios); r > 0 {
		switch {
		case within(r, 1, 0.15):
			observed = 1
		case within(r, 100, 15):
			observed = 100
		default:
			st.Degrade = "J3"
		}
	}
	// 量纲自动对齐（**券商源默认开的安全前提**）：观测档与配置系数不一致时，
	// 这一轮抹掉量/额、只并价格类字段 ⇒ VWAP（成交额÷成交量）继续用外部源的自洽值。
	// 为什么必须有这段：J3 天生抓不到"系数配错一档"——≈1 与 ≈100 都是合法单位档，
	// 于是"以券商为准 + 系数留 1"会把 VWAP 整体放大/缩小 100 倍，直接进委托装配、
	// 做 T 成本分解与那条"VWAP 不合理就不买"的 fail-closed 闸。
	st.VolumeObservedUnit = observed
	factor := f.volFactor()
	st.VolumeAligned = observed > 0 && observed == factor
	if p := f.fetcher.feedPolicyNow(); p.VolumeAutoAlign && !st.VolumeAligned && observed > 0 && factor == 1 {
		for code, tk := range entries {
			tk.Volume, tk.Amount = 0, 0 // applyTickTo 见 0 即保留外部源的量/额
			entries[code] = tk
		}
		f.logObservedThrottled("qmt_feed: §SUBSCRIPTION-A 量纲未对齐（观测=%d 配置系数=%.0f）⇒ "+
			"本轮只并价格、不接管量/额（VWAP 保持外部源口径），请把 runtime.qmt_feed_volume_to_shares 配成 %d",
			int(observed), factor, int(observed))
	}

	if st.Degrade != "" {
		// 本轮不产 overlay、不刷基线；池档即刻收回（与 J1 同方向）。
		f.fetcher.NoteFeedDegrade(st)
		f.logThrottled("qmt_feed: §SUBSCRIPTION-A 判定券商读数不可用 degrade=%s hits=%d/%d median_ratio=%.2f missing_price_pct=%.1f",
			st.Degrade, st.Hits, denom, medianRatio(ratios), st.MissingPricePct)
		return 0
	}

	// 主源模式才做逐票合并（观察模式一个价都不改）；合并后的副本交给装配层落。
	if f.isPrimary() && merged != nil && st.Hits > 0 {
		merged.Source = QuoteSourceQMTL1 // §M1 枚举常量：本轮由券商价当家（整份单值 Source 的每票化是下一批 A3）
		factor := f.volFactor()
		p := f.fetcher.feedPolicyNow()
		p.VolumeToShares = factor
		for code, tk := range entries {
			si := merged.Stocks[code]
			if si == nil {
				si = &StockInfo{Code: code} // 池里有但外部源没给价的票：由券商补上这一条
				if merged.Stocks == nil {
					merged.Stocks = map[string]*StockInfo{}
				}
			}
			cp := *si // 约束：复制后再改（Snapshot 是浅拷贝，条目指针与内部快照共享）
			if applyTickTo(&cp, tk, p) {
				if p.PerCodeSource {
					cp.Src = QuoteSourceQMTL1
					cp.AsOfMs = tk.TickTime // 0＝网关没给时间戳：留空，绝不折成"现在"（消费端按未知处理）
				}
				merged.Stocks[code] = &cp
			}
		}
	}
	f.fetcher.IngestFeedRound(merged, entries, st)
	return st.Hits
}

// copySnapshotForMerge 取当前快照的"可安全改写"副本：map 本身新建，条目指针沿用
// （真正的写在 applyTicks 里逐条复制 StockInfo，绝不改共享指针）。base==nil 时返回 nil——
// 首轮采集还没跑：不注入空壳快照，等现行链建立基线（§ENH-5 原约束保持）。
func (f *QMTFeed) copySnapshotForMerge(base *MarketSnapshot) *MarketSnapshot {
	if base == nil {
		return nil
	}
	out := &MarketSnapshot{Source: base.Source, Time: base.Time, Stocks: make(map[string]*StockInfo, len(base.Stocks))}
	for code, si := range base.Stocks {
		out.Stocks[code] = si
	}
	if base.Sector != nil {
		out.Sector = base.Sector
	}
	return out
}

// medianRatio 取比值中位数（偶数取中间两数均值）；空集回 0（＝不判 J3）。
// 为什么用中位数而不是均值：一两只异常票不该把整条通道判死。
func medianRatio(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	cp := append([]float64(nil), xs...)
	sort.Float64s(cp)
	n := len(cp)
	if n%2 == 1 {
		return cp[n/2]
	}
	return (cp[n/2-1] + cp[n/2]) / 2
}

// within 判断 v 是否落在 [target±tol]。
func within(v, target, tol float64) bool {
	return v >= target-tol && v <= target+tol
}

// boolInt 把布尔打成 0/1（日志里用数字，便于 grep 统计，不用 true/false 混着读）。
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// logObservedThrottled 观察模式关态读数的节流出口（60s 一条，与错误日志各自的位点）。
func (f *QMTFeed) logObservedThrottled(format string, args ...any) {
	now := time.Now().Unix()
	prev := f.observedAt.Swap(now)
	if now-prev >= 60 {
		log.Printf("[qmt_feed] "+format, args...)
	}
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
