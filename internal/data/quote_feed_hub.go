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
// 本批四枚开关都是**出厂开**的指针位（2026-11-01 owner 改判「默认把券商信息源开关打开，
// 要保证券商的数据源通畅、功能如预期上线」；见 docs/IMPL_QMT_DATA_SUBSCRIPTION_20261101.md A9/§0.2）：
//
//	runtime.qmt_feed_enabled          —— feed 是否轮询（缺键=开；显式 false 停）
//	runtime.qmt_feed_primary          —— 券商 tick 是否允许改写快照价格（缺键=开；显式 false＝观察模式：
//	                                      只统计覆盖率/降级状态，一个价格都不动，这是现场回滚位）
//	runtime.qmt_feed_coverage_min_pct —— 覆盖率阈值（0→出厂 80；负数→关闭判断＝旧行为）
//	runtime.monitor_pool_hot_max      —— 券商主源档的热点池上限（0→出厂 400；降级档仍是常数 60）
//
// "默认开"必须有自证腿，否则开在一个连不通的通道上等于静默无效：FeedPolicy.Enabled 记录
// "这条通道本该在跑"，FeedAlertCode 据此把"从没轮询/被鉴权拒/自身读数不可用"编码成量规值，
// 由 metrics 的 broker_feed_unavailable 规则在盘中持续破线时告警（见 internal/metrics/alerter.go）。
package data

import (
	"errors"
	"strings"
	"sync"
	"time"

	"quant-trading-v2/internal/config"
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
	Enabled         bool // 这条通道本该在跑（runtime.qmt_feed_enabled 的生效值）：告警自证腿的前提，缺省 false＝不告警
	Primary         bool // 券商 tick 是否可改写快照价格（出厂开；关＝观察模式，现场回滚位）
	PerCodeSource   bool // 是否逐票标 Src/AsOfMs（出厂开；关时字段留空，落盘/SSE 与旧形状同形）
	VolumeAutoAlign bool // 量纲自动对齐（默认开）：观测单位与配置系数不一致时不接管量/额，见 VolumeObservedUnit
	Consult         bool // 裁决 13：咨询与自选补价是否"快照优先"（出厂开；关＝每问一次打一次外部源，回滚位）
	// IndexEnabled §A7-3（2026-11-02 owner 裁决 4「白名单与用例先写好、开关打开，等 QMT」）：
	// 是否向券商侧请求指数点位（独立后缀白名单，绝不吃个股后缀函数）。
	// 关掉＝一次都不请求；开着而通道不通＝每轮多一次空响应，外部三条腿照常当家（不影响行情可用性）。
	IndexEnabled   bool
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
	Hits               int     // 本轮有效 tick 只数
	Denominator        int     // 覆盖率分母（当日可交易标的数，取不到时退回池总数）
	Pool               int     // 监控池只数
	CoveragePct        float64 // Hits/Denominator*100
	DenominatorSource  string  // tradable / pool（pool＝分母不可信，退回池总数）
	RefreshedGlobal    bool    // 本轮是否刷新了全局新鲜度基线（覆盖率达标才有）
	Degrade            string  // 空＝正常；J1 通道不通 / J2 集体陈旧 / J3 读数不自洽
	AuthFailed         bool    // 最近一轮被 401/403 拒（钥匙被撤/无效）——与"网关挂了"必须分形
	WouldOverride      bool    // 观察模式下：本轮覆盖率已达标，**本可以**改写价格（但开关没开）
	VolumeObservedUnit float64 // 本轮由"成交额/(价×量)"观测到的单位档（1=股、100=手、0=不成立）
	VolumeAligned      bool    // 观测档与配置系数一致 ⇒ 量/额可以接管
	ReadyToPromote     bool    // 连续达标轮次 >=3 ⇒ "可以开 primary 了"的现场依据，不靠人盯日志
	CoverageRounds     int     // 连续达标轮数（ReadyToPromote 的输入）
	ConsecutiveFail    int     // 连续失败轮次（J1 判据输入）
	MissingPricePct    float64 // 本轮 lastPrice<=0 的占比（J3 判据输入）
	Rounds             int64   // 进程内累计轮询轮次（0＝这条通道从没跑过）：区分"开着但静默无效"与"没开"的唯一依据
	// Returned 本轮网关**回包**里的 tick 只数（与 Hits 不同：Hits 还要求价>0 且不超龄）。
	// 单列它是为了把"回包为空"与"回包非空但都是旧价/零价"分开——后者是 J2，前者今天没有任何一档。
	Returned int
	// EmptyRounds 连续空回包轮数（池非空、HTTP 成功、回包零条）。
	// 现网 2026-11-02 的形态就是它：网关在、/quotes 回 200、`ticks:{}`，因为上游 xtdata 根本没有
	// 行情服务可连 ⇒ Rounds 一直涨、Hits 恒 0、Degrade 恒空 ⇒ 告警编码器判成"健康"。
	EmptyRounds int
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
	// idxEnts §A7-3：**带后缀**的指数代码 → 最近一次有效 tick。
	// 为什么单独一本账并且键带后缀：券商侧 000001.SH 是上证指数、000001.SZ 是平安银行，
	// 而 feed 主链的键在客户端就被剥成裸码（qmt_client.go:511）——同一个键装两只，
	// 谁赢取决于回包顺序，那正是 §BUG-INDEX 的形状。指数因此走独立的请求与独立的键空间，
	// 并且**不进 ents、不进覆盖率分母、不进混源账**（见 QMTFeed 的指数段注释）。
	idxEnts map[string]QMTTick
	stats   FeedStats       // 最近一轮读数
	pinned  map[string]bool // 被引用的裸码（持仓/在途委托/在途 T 腿）：收池与热点轮换都不许踢掉

	// 迟滞状态：healthy 连续区间的起点，与当前是否处于主源档
	healthySince time.Time
	live         bool
	okRounds     int   // 连续覆盖率达标轮数（ReadyToPromote 的输入）
	rounds       int64 // 累计轮询轮次（FeedStats.Rounds 的来源；只在锁内自增）
	// emptyRounds 连续"池非空、HTTP 成功、回包零条"的轮数（只在锁内改）。
	emptyRounds int
}

func (f *Fetcher) hub() *feedHubState {
	// 用 sync.Once 而不是 f.mu：UpdateHotStocks / StalenessMs 这些调用方自己持 f.mu，
	// 借同一把锁会当场自锁死（本仓 §R3-2 那类"锁内取撕裂切片头"的同族坑）。
	f.hubOnce.Do(func() {
		f.hubRef = &feedHubState{ents: map[string]feedEntry{}, pinned: map[string]bool{}, idxEnts: map[string]QMTTick{}}
	})
	return f.hubRef
}

// bumpRound 把本轮计入进程内总轮次，并把累计值写进读数（调用方必须持 h.mu）。
// 为什么需要一个总数而不只看 Hits/失败位：Stats 里每个零值都有两种成因——
// "一轮都没跑"（feed 没装配/钥匙为空被显式停用）与"跑了但什么都没拿到"。
// 出厂开之后这两种在现场的处置完全不同（查装配 vs 查通道），必须能分开。
// English: counts polled rounds so "never polled" is distinguishable from "polled, got nothing".
// bumpRound 是"一轮跑完了"的唯一记账点（观察档与主源档都走它，否则两档的轮数会各自漂移）。
// 顺带在此维护连续空回包计数：判据放在这里而不是 qmt_feed 里，是因为只有这里同时拿得到
// 池只数与回包只数——空回包必须是"该有票、请求成功、却一条都没回"，三者缺一都不算。
func (h *feedHubState) bumpRound(st *FeedStats) {
	h.rounds++
	st.Rounds = h.rounds
	// st.Pool <= 0 是"没东西可问"（收池/盘前），不是故障；回包非零条也不是（那是 J2 的领地）。
	if st.Pool > 0 && st.Returned == 0 {
		h.emptyRounds++
	} else {
		h.emptyRounds = 0
	}
	st.EmptyRounds = h.emptyRounds
}

// FeedPolicyFromRuntime 是 runtime 配置 → 装配策略的**唯一构建器**（启动装配与设置页热更共用）。
// 为什么必须单点：这两条路各写一份兜底时，"改设置页保存后生效值与重启后不一样"是必然结局
// （AGENTS 里纸面配置那条教训的同族）。取值一律走 Effective*——存量 config.json 缺键反序列化得 0，
// 0 的含义是"没填"，不是"阈值 0＝谁都达标"。
// Enabled 直接取 qmt_feed_enabled 的生效值：它是 broker_feed_unavailable 告警的前提——
// 「本该在跑却一轮都没跑」与「没启用」必须能分开，否则出厂开之后静默无效没人知道。
// English: the single builder from runtime config to the assembly policy (startup + hot-update share it),
// so the two paths can no longer drift; Enabled carries the effective feed switch the alert leg needs.
func FeedPolicyFromRuntime(rt config.RuntimeConfig) FeedPolicy {
	return FeedPolicy{
		Enabled:         rt.QMTFeedOn(),
		Primary:         rt.QMTFeedPrimaryOn(),
		CoverageMinPct:  rt.EffectiveFeedCoverageMinPct(),
		HotPoolCap:      rt.EffectiveMonitorPoolHotMax(),
		PerCodeSource:   rt.QuotePerCodeSourceOn(),
		Consult:         rt.QuoteHubConsultOn(),
		IndexEnabled:    rt.QuoteFeedIndexOn(),
		VolumeAutoAlign: true, // 量纲未对齐时不接管量/额（默认开）：见本文件量纲对齐段
		MaxAge:          30 * time.Second,
		VolumeToShares:  rt.EffectiveFeedVolumeToShares(),
	}
}

// SetFeedPolicy 装配/热更换行情策略（main.go 启动与设置页热更都走这里，单点入口）。
// English: SetFeedPolicy installs the feed policy from the single assembly point.
func (f *Fetcher) SetFeedPolicy(p FeedPolicy) {
	h := f.hub()
	h.mu.Lock()
	// §A7-3：关掉券商指数时**同批清空指数分账**，而不是等下一次拉取才发现没清。
	// 残留的后果很具体：用户拨回开关→重启→第一次拉取之前，卡片仍会报 index_source=QMT-L1
	// 并拿着旧点位定权——"刚做的回滚在读数上看不见"正是本批要消灭的那类形状。
	if !p.IndexEnabled {
		for k := range h.idxEnts {
			delete(h.idxEnts, k)
		}
	}
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
		h.bumpRound(&st)
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
	h.bumpRound(&st)
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
	h.bumpRound(&st)
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
	h.bumpRound(&st)
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
		return nil // 观察模式（显式关 primary 的回滚位）：一个价格都不改写，与旧形状逐字节一致
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
	// 量与额只在"单位已对齐"时才接管：applyTickTo 拿不到整轮观测，故约定
	// **volume==0 表示调用方已按未对齐把这两项抹掉**，此处保留外部源的值（见 qmt_feed.go 的对齐段）。
	if tk.Volume > 0 {
		dst.Volume = tk.Volume * factor
	}
	if tk.Amount > 0 {
		dst.Amount = tk.Amount
	}
	if tk.PrevClose > 0 {
		dst.ChangePct = (tk.LastPrice/tk.PrevClose - 1) * 100
	}
	return true
}

// QuoteFeedState 给观测面用：把"这条通道现在是什么状态"折成一个字符串读数。
// 取值 not_polled / auth_failed / degraded:J1 / degraded:J2 / degraded:J3 / ok。
// 为什么 auth_failed 优先于 J1：钥匙被撤在 HTTP 层也是"通道不通"，但对运维来说
// 两者的处置动作完全不同（补钥匙 vs 查网关进程），混成一个值就是让人盲操作。
// 为什么要有 not_polled：一轮都没跑时"ok"是**编的**——它和"跑过且健康"在读数上无法区分，
// 而这正是出厂开之后最需要看见的那一态（同 §M2 的"没数据不许冒充新鲜"）。
// English: single observable state; auth_failed stays distinct from a generic channel failure and a
// feed that never polled says so instead of claiming ok.
func (f *Fetcher) QuoteFeedState() string {
	st := f.FeedStatsNow()
	if st.Rounds <= 0 {
		return "not_polled"
	}
	if st.AuthFailed {
		return "auth_failed"
	}
	if d := feedDegradeView(st); d != "" {
		return "degraded:" + d
	}
	return "ok"
}

// feedEmptyAlertRounds 连续多少轮空回包才判不可用（3s 轮询 ⇒ 约 60 秒）。
// 为什么不是一轮就报：网关刚重启、池刚轮换、收盘前最后一轮都可能出现单轮空回包，
// 一轮就报会把正常运维动作刷成告警（与 ReadyToPromote 要连续 3 轮同一条取向）。
const feedEmptyAlertRounds = 20

// feedDegradeView 给出**只供读数与告警编码**的降级档：本体 Degrade（J1/J2/J3）优先，
// 本体为空但连续空回包成档时给 J0。
//
// 关键取舍（owner 令：券商不通必须自动退回现有源、不许推翻现有链路规则）：
// J0 **不写进 st.Degrade** ——那个字段会带动主源健康线（setFeedHealth）与池分档迟滞，
// 属于数据面行为。空回包时数据面本来就已经在正确地退回：命中才改写、覆盖率闸要求 Hits>0
// 所以全局新鲜度基线根本不推进、连续失败即刻缩池。缺的只是"看得见"，不是"退回去"。
// English: J0 is an observability-only tier; the fallback behaviour it describes already happens,
// so we surface it without touching the data-path fields that drive pool tiers and health.
func feedDegradeView(st FeedStats) string {
	if st.Degrade != "" {
		return st.Degrade
	}
	if st.EmptyRounds >= feedEmptyAlertRounds {
		return "J0"
	}
	return ""
}

// 行情通道告警编码（broker_feed_unavailable 规则的唯一数据源）。
// 口径与仓内其他"应有值缺失"型心跳一致：**不适用一律写 0**，而规则用 ge 2 判定，
// 所以 0 与 1 都在阈值之下——启动后、盘前、休市、通道没启用都不会假红
// （§CAL-GATE/§P2-E 的判例：必须回落的判据若把"还没开始"算成异常，第一天就刷屏）。
const (
	FeedAlertNotApplicable = 0 // 通道未启用 / 采集器未装配 / 不在盘中：不参与判定
	FeedAlertAlive         = 1 // 至少跑过一轮且自身读数可用（观察模式也算活着，那是刻意档）
	FeedAlertAuthFailed    = 2 // 被 401/403 拒：钥匙被撤或无效，处置＝补钥匙
	FeedAlertDegraded      = 3 // J1/J2/J3/J0：通道或自身读数不可用，处置＝查网关进程与 xtdata
	FeedAlertNeverPolled   = 4 // 启用了却一轮都没跑：装配断了或每轮 panic，最难发现的一种静默无效
)

// FeedAlertCode 把通道状态折成告警量规值（盘中由引擎每 30s 喂一次，见 engine.refreshStalenessGauges）。
// 为什么在 data 侧算而不是引擎侧判字符串：取值集合（auth_failed / degraded:Jx）是本包的定义，
// 引擎再 Contains 一遍就是"靠猜状态码"那类判据（§0925EVE-W3-E）。
//
// 2026-11-02 补的 J0 档：`Returned==0`（回包为空）**过去任何一档都不沾**——J2 的判据写的是
// "一条都没命中但回包非空"。于是现网"网关活着、每 3 秒回 200、`ticks:{}`、因为上游 xtdata
// 根本没有行情服务可连"这一形被编码成 Alive=1：必推规则一次都没响
// （alert_router_state.json 里 broker_feed_unavailable 连键都没有，同期 order_fail_rate 10:57 已 fire）。
// English: an empty reply matched no tier, so a gateway answering 200 with zero ticks while its
// upstream quote service is dead was encoded as healthy; it now reports 3 → "check gateway/xtdata".
func (f *Fetcher) FeedAlertCode() int64 {
	p := f.feedPolicyNow()
	if !p.Enabled {
		return FeedAlertNotApplicable
	}
	st := f.FeedStatsNow()
	switch {
	case st.Rounds <= 0:
		return FeedAlertNeverPolled
	case st.AuthFailed:
		return FeedAlertAuthFailed
	case feedDegradeView(st) != "":
		return FeedAlertDegraded
	default:
		return FeedAlertAlive
	}
}

// HubQuoteTTL 快照优先的可用窗口：超过它就当"没拿到"，走原链现拉。
// 为什么不是"有价就用"：咨询语义一直是"照此刻的价格回答"，快照最快也是 3~5 秒前；
// 给了窗口又如实带 Src/AsOfMs，用户看到的才是一个**有时刻的价**，而不是"看起来实时"。
const HubQuoteTTL = 5 * time.Second

// HubQuote 快照优先读法（裁决 13）：命中且逐票新鲜才返回**深拷贝**。
// 返回拷贝是必须的——快照里的 *StockInfo 与在用的快照共享指针，调用方（咨询装配、自选补价）
// 拿到后往往就地改字段，那就等于从读路径写坏了采集器自己的账。
// English: snapshot-first lookup; returns a deep copy only when the per-symbol evidence is fresh.
func (f *Fetcher) HubQuote(code string, ttl time.Duration) (*StockInfo, bool) {
	p := f.feedPolicyNow()
	if !p.Consult {
		return nil, false
	}
	if ttl <= 0 {
		ttl = HubQuoteTTL
	}
	bare := code
	if i := strings.IndexByte(bare, '.'); i > 0 {
		bare = bare[:i]
	}
	snap := f.Snapshot()
	if snap == nil {
		return nil, false
	}
	si := snap.Stocks[bare]
	if si == nil || si.Price <= 0 {
		return nil, false
	}
	// 新鲜度按这一只票算（overlay 时刻优先，其次全局基线），不看整份快照——混源时整份时间是错的
	asOf := f.feedFreshMs(bare)
	if asOf <= 0 {
		if ts := f.lastOK.Load(); ts != 0 {
			asOf = ts * 1000
		}
	}
	if asOf <= 0 || time.Since(time.UnixMilli(asOf)) > ttl {
		return nil, false
	}
	cp := *si
	if cp.Src == "" {
		cp.Src = snap.Source
	}
	if cp.AsOfMs == 0 {
		cp.AsOfMs = asOf
	}
	return &cp, true
}

// ApplyHubPrice 把 hub（快照/券商）给出的**价格类**字段并到 dst 上，其余字段一律不动。
// 这是裁决 13 与底线 8 的交叉点：咨询要资金流（东财 f62），而券商没有资金流——
// 于是"现价来自 hub、净流入来自原链"必须允许分家，但绝不能因为合并把 dst 的
// NetInflow/HasFlow/Name/Sector/FloatShares/Turnover 清成零值（那等于为了换价格而丢数据）。
// English: merges only price-class fields; flow/name/sector fields survive untouched —
// the broker has no net-inflow, so mixing sources must never zero out what the other source provided.
func ApplyHubPrice(dst, hub *StockInfo) bool {
	if dst == nil || hub == nil || hub.Price <= 0 {
		return false
	}
	dst.Price = hub.Price
	dst.Open = hub.Open
	dst.High = hub.High
	dst.Low = hub.Low
	if hub.PrevClose > 0 {
		dst.PrevClose = hub.PrevClose
	}
	if hub.Volume > 0 {
		dst.Volume = hub.Volume
	}
	if hub.Amount > 0 {
		dst.Amount = hub.Amount
	}
	if hub.PrevClose > 0 {
		dst.ChangePct = (hub.Price/hub.PrevClose - 1) * 100
	}
	dst.Src = hub.Src
	dst.AsOfMs = hub.AsOfMs
	return true
}
