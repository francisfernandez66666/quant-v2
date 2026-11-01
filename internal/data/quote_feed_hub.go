// quote_feed_hub.go — §SUBSCRIPTION-A（2026-11-01 裁决 1/4/5/12）：券商 L1 主源的装配状态单点。
//
// 为什么单开一个文件（owner 裁决 1：所有行情逻辑收进一个 Go 文件）：券商源与现行链是**两个写者**，
// 旧实现里两边都往 Fetcher 里"整份塞快照"——5s 链每轮重建整份（fetcher.go fetch），feed 每 3s
// 命中 ≥1 只也塞一份（qmt_feed.go applyTicks）。两个后果都真实存在：
//
//	① 新鲜度被伪造：全局只有一个 lastOK，feed 命中 1 只就把它归零，可那 1 只的新鲜替全池 200 只
//	   作了保，而它喂的是发单前的行情新鲜度硬闸（StalenessMs → executor.StaleQuoteGuard）；
//	② 价格互相覆盖：feed 刚写进来的券商价，下一轮 5s 链重建快照时又被外部源的值顶掉
//	   （"主源"名不副实，且每 5s 抖一次）。
//
// 本文件把这两件事收成一套状态机：feed 只写 **overlay**（带时刻的 tick 补丁），由 5s 链在写快照前
// 统一合并（单写者）；全局 lastOK 只在**覆盖率达标**时刷新，每只票另有自己的新鲜时刻。
//
// 三个开关决定这里的分支（缺省全部关＝与今天字节一致，见 docs/IMPL_QMT_DATA_SUBSCRIPTION_20261101.md A9）：
//
//	runtime.qmt_feed_enabled          —— feed 是否轮询（既有键）
//	runtime.qmt_feed_primary          —— 券商 tick 是否允许改写快照价格（本批新增，缺省 false＝观察模式：
//	                                      只统计覆盖率/降级状态，一个价格都不动）
//	runtime.qmt_feed_coverage_min_pct —— 覆盖率阈值（0→出厂 80；负数→关闭判断＝旧行为）
//	runtime.monitor_pool_hot_max      —— 券商主源档的热点池上限（0→出厂 400；降级档仍是常数 60）
package data

import (
	"errors"
	"strings"
	"sync"
	"time"
)

// ErrQuoteUnauthorized 是"行情通道被鉴权拒绝"的可判定错误哨兵（401/403）。
// 为什么必须由 data 侧定义、trading 侧包装：feed 只能看见接口返回的 error，而 §0925EVE-W3-E
// 早就定过"判据不许靠 Contains 猜状态码"；网关的 gatewayHTTPError 是 trading 包内未导出类型，
// data 侧既不能命名它、也不该去 import trading（会成环）。哨兵让"钥匙被撤/无效"与
// "网关挂了/网络抖"成为两个不同读数——owner 裁决 14 要的就是这个区分。
// English: sentinel so "credential revoked/invalid" is distinguishable from "gateway down"
// without string sniffing and without an import cycle.
var ErrQuoteUnauthorized = errors.New("quote feed unauthorized")

// feedHubTickInterval 是迟滞判定的最小时间粒度（只做时间比较，不做定时器）。
// feedSteadyExpand 升档（扩容/接管）需要的连续稳定时长；降档（退回后备源）**即刻生效**。
// 为什么不对称：源在两个档位之间抖会让热点池每轮换血、缺价代码进进出出（§132 池预热那枚锁最怕这个），
// 而"出问题时立刻缩回"是安全方向，不需要观察窗。
const feedSteadyExpand = 5 * time.Minute

// feedCoverageDefault 覆盖率阈值的出厂值（80%，owner 裁决 4）。
// ⚠ 与 config.RuntimeConfig 里的 FeedCoverageDefault **必须是同一个数**：装配点兜一次、
// 装配缺省（直接构造 Fetcher 的测试/内嵌场景）再兜一次，两处不一致就会出现
// "设置页显示 80、实际按别的数判达标"。门禁 §161 有等值锁盯这两个字面量。
// English: factory default coverage threshold; must equal config.FeedCoverageDefault (gate-locked).
const feedCoverageDefault = 80

// FeedPolicy 行情装配的生效策略（由 config.RuntimeConfig 的 Effective* 读数装配，整体替换、无锁读旧值）。
// English: effective feed policy assembled from RuntimeConfig's Effective* readings.
type FeedPolicy struct {
	Primary        bool          // 券商 tick 是否可改写快照价格
	PerCodeSource  bool          // 是否逐票标 Src/AsOfMs（缺省 false ⇒ 字段留空，落盘/SSE 与今天同形）
	CoverageMinPct int           // 覆盖率阈值：-1＝关闭判断（旧行为）；0/缺省由装配点兜成 80
	HotPoolCap     int           // 券商主源档的热点池上限
	MaxAge         time.Duration // 单票 tick 超龄上限（超龄退回后备源值）
	VolumeToShares float64       // 量纲系数：券商 tick 的 volume → 股（0→1；由 Effective* 兜底）
	SteadyWindow   time.Duration // 升档所需连续稳定时长（<=0 → feedSteadyExpand）
}

// FeedStats 一轮 feed 的读数（观测面：覆盖率、分母来源、降级状态、连续失败轮次）。
// 这些数字是"能不能把主源交给券商"的唯一现场依据——不许用日志感觉代替它们。
// English: per-round feed observations; the only现场 basis for promoting broker quotes to primary.
type FeedStats struct {
	Hits              int     // 本轮有效 tick 只数
	Denominator       int     // 覆盖率分母（当日可交易标的数，取不到时退回池总数）
	Pool              int     // 监控池只数
	CoveragePct       float64 // Hits/Denominator*100
	DenominatorSource string  // tradable / pool（pool＝分母不可信，退回池总数）
	RefreshedGlobal   bool    // 本轮是否刷新了全局新鲜度基线（覆盖率达标才有）
	Degrade           string  // 空＝正常；J1 通道不通 / J2 集体陈旧 / J3 读数不自洽
	AuthFailed        bool    // 最近一轮被 401/403 拒（钥匙被撤/无效）——与"网关挂了"必须分形
	WouldOverride     bool    // 观察模式下：本轮覆盖率已达标，**本可以**改写价格（但开关没开）
	ReadyToPromote    bool    // 连续达标轮次 >=3 ⇒ "可以开 primary 了"的现场依据，不靠人盯日志
	CoverageRounds    int     // 连续达标轮数（ReadyToPromote 的输入）
	ConsecutiveFail   int     // 连续失败轮次（J1 判据输入）
	MissingPricePct   float64 // 本轮 lastPrice<=0 的占比（J3 判据输入）
}

// feedEntry 单票 overlay 条目：原始 tick + 到达时刻。
type feedEntry struct {
	tk QMTTick
	at time.Time
}

// feedHubState 挂在 Fetcher 上的行情装配状态。全部方法内部自带锁，调用方不需要再持锁。
type feedHubState struct {
	mu sync.Mutex

	policy FeedPolicy
	ents   map[string]feedEntry // 裸码 → 最近一次有效 tick
	stats  FeedStats            // 最近一轮读数
	pinned map[string]bool      // 被引用的裸码（持仓/在途委托/在途 T 腿）：收池与热点轮换都不许踢掉

	// 迟滞状态：healthy 连续区间的起点，与当前是否处于主源档
	healthySince time.Time
	live         bool
	okRounds     int // 连续覆盖率达标轮数（ReadyToPromote 的输入）
}

func (f *Fetcher) hub() *feedHubState {
	// 用 sync.Once 而不是 f.mu：UpdateHotStocks / StalenessMs 这些调用方自己持 f.mu，
	// 借同一把锁会当场自锁死（本仓 §R3-2 那类"锁内取撕裂切片头"的同族坑）。
	f.hubOnce.Do(func() {
		f.hubRef = &feedHubState{ents: map[string]feedEntry{}, pinned: map[string]bool{}}
	})
	return f.hubRef
}

// SetFeedPolicy 装配/热更换行情策略（main.go 启动与设置页热更都走这里，单点入口）。
// English: SetFeedPolicy installs the feed policy from the single assembly point.
func (f *Fetcher) SetFeedPolicy(p FeedPolicy) {
	h := f.hub()
	h.mu.Lock()
	h.policy = p
	h.mu.Unlock()
}

// feedPolicyNow 读当前策略（值拷贝，避免调用方拿到可变的共享状态）。
func (f *Fetcher) feedPolicyNow() FeedPolicy {
	h := f.hub()
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.policy
}

// FeedPolicyNow 导出版读数（观测面测试与装配热改用；内部一律走 feedPolicyNow）。
func (f *Fetcher) FeedPolicyNow() FeedPolicy { return f.feedPolicyNow() }

// FeedStatsNow 返回最近一轮 feed 读数（观测面）。
func (f *Fetcher) FeedStatsNow() FeedStats {
	h := f.hub()
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.stats
}

// coverageGateOK 判断本轮是否允许刷新**全局**新鲜度基线。
// 阈值 <0＝关闭判断（旧行为，命中即刷，只用于排障对比）；
// 分母 <=0＝没有可信分母 ⇒ 不刷新（宁保守：把"不知道覆盖多少"当成没达标）。
// English: global freshness may only be refreshed when coverage of the tradable universe meets the
// threshold; a missing denominator means NOT refreshed (never let "unknown coverage" fake freshness).
func coverageGateOK(p FeedPolicy, st FeedStats) bool {
	thr := p.CoverageMinPct
	if thr == 0 {
		thr = feedCoverageDefault // 装配缺省也要兜出厂值：0 绝不允许意味着"谁都达标"
	}
	if thr < 0 {
		return true // 显式关闭阈值判断（旧行为，只用于排障对比）
	}
	if st.Denominator <= 0 || st.Hits <= 0 {
		return false
	}
	return st.CoveragePct >= float64(thr)
}

// IngestFeedRound 是一轮 feed 的唯一入口（替代旧的"整份注入"）：
//   - 观察模式（Primary=false）：只登记读数，**不动快照、不动 lastOK、不写 overlay**；
//   - 主源模式：写 overlay（每票时刻）+ 按覆盖率决定是否刷新全局基线，并把合并后的快照落下去。
//
// merged 是调用方（qmt_feed）以当前快照为底、把本轮 tick 合并后的副本——
// 复用 qmt_feed 既有的"复制 struct 再改"约束（Snapshot() 是浅拷贝，与 5s 链共享指针，-race 会抓到）。
//
// English: single entry point for one feed round. Observe mode records numbers without touching the
// snapshot; primary mode records per-code freshness and refreshes the GLOBAL baseline only when
// coverage passes the threshold — that is the whole point of the gate.
func (f *Fetcher) IngestFeedRound(merged *MarketSnapshot, entries map[string]QMTTick, st FeedStats) {
	h := f.hub()
	p := f.feedPolicyNow()
	if !p.Primary {
		// 观察模式：不写 overlay、不动快照、不刷全局基线，但 L2 读数（本可以改写多少只、
		// 是否已连续达标可开主源）照样算——"能不能开"必须由读数回答，不能靠人翻日志。
		h.mu.Lock()
		st.WouldOverride = coverageGateOK(p, st)
		if coverageGateOK(p, st) {
			h.okRounds++
		} else {
			h.okRounds = 0
		}
		st.CoverageRounds = h.okRounds
		st.ReadyToPromote = h.okRounds >= 3
		st.RefreshedGlobal = false // 观察模式绝不刷新基线（这是它与主源档唯一的行为差）
		h.stats = st
		h.mu.Unlock()
		return
	}
	now := time.Now()
	h.mu.Lock()
	if h.ents == nil {
		h.ents = map[string]feedEntry{}
	}
	for code, tk := range entries {
		h.ents[code] = feedEntry{tk: tk, at: now}
	}
	st.RefreshedGlobal = coverageGateOK(p, st)
	// 追加裁决 15 的 L2 半边（观察模式）：关着也要把"本可以做什么"记成读数。
	// 没有它，"能不能开 primary"就只能靠人翻日志数轮次——那正是我们要消灭的"感觉式"上线。
	st.WouldOverride = !p.Primary && coverageGateOK(p, st)
	if coverageGateOK(p, st) {
		h.okRounds++
	} else {
		h.okRounds = 0
	}
	st.CoverageRounds = h.okRounds
	st.ReadyToPromote = h.okRounds >= 3 // 连续 3 轮达标（≈9 秒）才算"可开"，单轮尖峰不算
	h.stats = st
	h.mu.Unlock()
	// 健康位：主源档（扩池）只在"确实由券商当家且本轮没判降级"时才升；
	// 观察模式不动档——扩池的前提是"用券商源"，不是"能连上券商"。
	f.setFeedHealth(p.Primary && st.Degrade == "")

	if merged != nil {
		f.mu.Lock()
		f.snapshot = merged
		f.mu.Unlock()
		f.emitSink(merged)
	}
	if st.RefreshedGlobal {
		f.lastOK.Store(now.Unix())
	}
}

// NoteFeedFailure 记录一轮失败（J1 通道不通的输入），并按需把主源档切回降级档。
// 失败轮**绝不**刷新 lastOK——否则"什么都拿不到"也会让全池显示新鲜（§M2 同族方向）。
// English: records a failed round for the J1 counter and drops the pool tier; never refreshes
// the global baseline, so "nothing arrived" can never masquerade as "everything is fresh".
func (f *Fetcher) NoteFeedFailure(consecutiveFail int, authFailed bool) {
	h := f.hub()
	h.mu.Lock()
	st := h.stats
	st.ConsecutiveFail = consecutiveFail
	st.Degrade = "J1"
	st.AuthFailed = authFailed // 钥匙问题与网关问题必须分形（裁决 14）
	st.CoverageRounds = 0
	st.ReadyToPromote = false
	h.okRounds = 0
	h.stats = st
	healthy := consecutiveFail == 0
	h.mu.Unlock()
	f.setFeedHealth(healthy)
}

// NoteFeedDegrade 记录一轮"读数不可用"（J2 集体陈旧 / J3 自身不自洽）：
// 本轮不产 overlay、不刷全局基线，并且**立刻**收回主源档（缩池是安全方向）。
// English: records a round whose broker numbers are unusable (J2/J3) — no overlay, no global
// refresh, and the pool tier drops immediately.
func (f *Fetcher) NoteFeedDegrade(st FeedStats) {
	h := f.hub()
	h.mu.Lock()
	st.ConsecutiveFail = 0 // 不是通道失败，J1 计数归零
	st.AuthFailed = false
	st.CoverageRounds = 0
	st.ReadyToPromote = false
	h.okRounds = 0
	h.stats = st
	h.mu.Unlock()
	f.setFeedHealth(false)
}

// setFeedHealth 驱动热点池分档的迟滞：升档需连续稳定 >=SteadyWindow，降档即刻。
// English: hysteresis for the hot-pool tier — expansion needs a continuous healthy window,
// shrinking is immediate.
func (f *Fetcher) setFeedHealth(healthy bool) {
	h := f.hub()
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	win := h.policy.SteadyWindow
	if win <= 0 {
		win = feedSteadyExpand
	}
	if !healthy {
		h.live = false
		h.healthySince = time.Time{}
		return
	}
	if h.healthySince.IsZero() {
		h.healthySince = now
	}
	if !h.live && now.Sub(h.healthySince) >= win {
		h.live = true
	}
}

// feedLive 当前是否处于券商主源档（升档条件见 setFeedHealth）。
func (f *Fetcher) feedLive() bool {
	h := f.hub()
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.live
}

// hotPoolCap 热点池的生效上限：降级档＝既有常数（60），主源档＝策略值。
// 既有常数不许消失（门禁有负锁）：这里的语义是"分档"，不是"取消截断"。
// English: hot-pool cap by tier; the historical fallback constant stays the default tier.
func (f *Fetcher) hotPoolCap() int {
	cap := maxWatchStocks
	h := f.hub()
	h.mu.Lock()
	if h.live && h.policy.HotPoolCap > cap {
		cap = h.policy.HotPoolCap
	}
	h.mu.Unlock()
	return cap
}

// pinCode 把代码钉进监控池（被持仓/在途委托引用时调用）：热点轮换与收池都不许把它踢掉。
// 为什么必须有：做 T 的一只票若因为池缩而失去行情，那是最坏的一种降级——价都看不见，腿还挂着。
// English: pinned codes survive hot-pool rotation and tier shrink; an intraday-T leg must never
// lose its quote because the pool got smaller.
func (f *Fetcher) pinCode(code string) {
	h := f.hub()
	h.mu.Lock()
	h.pinned[code] = true
	h.mu.Unlock()
}

// pinnedCodes 返回钉住的裸码集合（UpdateHotStocks 收敛时用）。
func (f *Fetcher) pinnedCodes() map[string]bool {
	h := f.hub()
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make(map[string]bool, len(h.pinned))
	for k, v := range h.pinned {
		out[k] = v
	}
	return out
}

// feedFreshMs 返回该票最近一次有效 tick 的 unixMilli（0＝没有 overlay 记录）。
// 入参按裸码查：overlay 的键是 feed 那侧归一过的裸码，而调用方（引擎/T 腿）可能送
// "600000.SH" 这种带后缀写法——不归一就会永远查不到，表现为"券商明明很新鲜，闸照旧判陈旧"。
func (f *Fetcher) feedFreshMs(code string) int64 {
	key := code
	if i := strings.IndexByte(key, '.'); i > 0 {
		key = key[:i]
	}
	h := f.hub()
	h.mu.Lock()
	defer h.mu.Unlock()
	if e, ok := h.ents[key]; ok {
		return e.at.UnixMilli()
	}
	return 0
}

// applyFeedOverlay 由 5s 采集链在**写快照之前**调用：把仍新鲜且与本轮结果同码的 tick 覆盖到
// 快照的对应条目上（单写者合并）。旧形状是"feed 事后整份注入"——那样每 5s 会被外部源重建覆盖一次，
// "主源"名不副实且价格来回抖。
//
// 四条必须守住的细节（都是 qmt_feed.go 里既有条款的延续）：
//  1. 复制 StockInfo 再改：Snapshot() 是浅拷贝，条目指针与内部快照共享，直接改会与采集循环竞写；
//  2. 超龄条目跳过：feed 断流时网关仍会回缓存，宁退后备源也不留陈旧券商价；
//  3. lastPrice<=0 不动（停牌/无 tick 是常态，保留外部源的值）；
//  4. tick 不携带的字段（Name/Sector/NetInflow/HasFlow/FloatShares/Turnover…）一律保留原值，
//     绝不清零——资金流那些字段是东财/同花顺的账，券商没有。
//
// English: merges fresh broker ticks into the externally-sourced snapshot under ONE writer, before
// the snapshot is published. Entries are copied (Snapshot shares pointers), stale/zero-price ticks
// are skipped, and tick-absent fields are preserved.
func (f *Fetcher) applyFeedOverlay(snap *MarketSnapshot, now time.Time) map[string]int64 {
	if snap == nil {
		return nil
	}
	p := f.feedPolicyNow()
	if !p.Primary {
		return nil // 观察模式：一个价格都不改写（缺省档＝与今天字节一致）
	}
	maxAge := p.MaxAge
	if maxAge <= 0 {
		maxAge = 30 * time.Second
	}
	h := f.hub()
	h.mu.Lock()
	entries := make(map[string]feedEntry, len(h.ents))
	for code, e := range h.ents {
		if now.Sub(e.at) <= maxAge {
			entries[code] = e
		}
	}
	h.mu.Unlock()

	applied := map[string]int64{}
	f.mu.Lock()
	for code, e := range entries {
		old := snap.Stocks[code]
		if old == nil {
			continue // 本轮外部快照没有这只票：不凭空造一条只有价没有名字的行（保留下一轮再合并）
		}
		cp := *old // 复制后再改（约束 ①）
		if applyTickTo(&cp, e.tk, p) {
			if p.PerCodeSource {
				cp.Src = QuoteSourceQMTL1 // 这一只现在的价是券商给的（不是整份快照的标称源）
				cp.AsOfMs = e.at.UnixMilli()
			}
			snap.Stocks[code] = &cp
			applied[code] = cp.AsOfMs
		}
	}
	f.mu.Unlock()
	return applied
}

// stampSnapshotSources 在 overlay 之后给"本轮其余票"补上后备源标记（只有开关开着才做）。
// extSource 必须是**外部链本轮的源名**，不能读 snap.Source —— 那个字段在 overlay 之后会被改成
// "QMT-L1"（整份标称源），拿它去标后备票就把新浪兜的票也写成券商给的，混源计数恒 0，
// 而这两处恰恰必须互相矛盾着才能被发现。参数化就是为了堵这个。
// English: stamps the fallback tier with the round's *external* source name, never the
// post-overlay snapshot.Source (which by then reads QMT-L1 and would erase the mixing).
func (f *Fetcher) stampSnapshotSources(snap *MarketSnapshot, overlaid map[string]int64, extSource string) {
	if snap == nil || !f.feedPolicyNow().PerCodeSource {
		return
	}
	ms := snap.Time.UnixMilli()
	if ms <= 0 {
		ms = time.Now().UnixMilli()
	}
	for code, si := range snap.Stocks {
		if si == nil {
			continue
		}
		if _, ok := overlaid[code]; ok {
			continue // 已由券商给价，别拿整份的时间戳去冒充它的时刻
		}
		si.Src = extSource
		si.AsOfMs = ms
	}
}

// applyTickTo 把一个券商 tick 打到 StockInfo 副本上；未命中有效价格返回 false（不动原值）。
// 价格字段与涨跌幅按现行链同一口径重算，Volume 走量纲系数（见 config.EffectiveFeedVolumeToShares）。
func applyTickTo(dst *StockInfo, tk QMTTick, p FeedPolicy) bool {
	if dst == nil || tk.LastPrice <= 0 {
		return false
	}
	factor := p.VolumeToShares
	if factor <= 0 {
		factor = 1
	}
	dst.Price = tk.LastPrice
	dst.Open = tk.Open
	dst.High = tk.High
	dst.Low = tk.Low
	if tk.PrevClose > 0 {
		dst.PrevClose = tk.PrevClose
	}
	dst.Volume = tk.Volume * factor
	dst.Amount = tk.Amount
	if tk.PrevClose > 0 {
		dst.ChangePct = (tk.LastPrice/tk.PrevClose - 1) * 100
	}
	return true
}

// QuoteFeedState 给观测面用：把"这条通道现在是什么状态"折成一个字符串读数。
// 取值 ok / auth_failed / degraded:J1 / degraded:J2 / degraded:J3。
// 为什么 auth_failed 优先于 J1：钥匙被撤在 HTTP 层也是"通道不通"，但对运维来说
// 两者的处置动作完全不同（补钥匙 vs 查网关进程），混成一个值就是让人盲操作。
// English: single observable state; auth_failed is kept distinct from a generic channel failure
// because the operator's action differs (re-issue the key vs. restart the gateway).
func (f *Fetcher) QuoteFeedState() string {
	st := f.FeedStatsNow()
	if st.AuthFailed {
		return "auth_failed"
	}
	if st.Degrade != "" {
		return "degraded:" + st.Degrade
	}
	return "ok"
}
