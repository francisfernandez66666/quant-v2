// tier.go — §B6 通知增强（2026-11-05）：四类消息的分级去向、降噪预算与三条"不许静默"判据。
//
// 本文件把施工图 §5A.1/§5A.2/§5A.3 落成可调用的出站器：
//   - 去向只有四种：必推（notify.LevelHigh）/ 普通推送（LevelMedium）/ 日汇总（不出站，只累计）/
//     新闻 tab（写进消息中心的当日「新闻」行，既不出站也不进信号流）；
//   - 预算与冷却写死成常量（NEWS_PUSH_PER_STOCK=2、NEWS_PUSH_DAILY=12、冷却 5min），
//     **不新增任何配置旋钮**——每加一个可调项就多一处漂移（§5A.2 第 4 条）；
//   - 阈值复用 Agent.MinScore，只在"命中持仓/自选"时下探一档；
//   - 冷却键按 (类型, 代码) 分格，全局冷却窗会让第二只票的利空被第一只的窗吃掉（§5A.2 第 3 条）；
//   - 超预算一律转日汇总并在新闻 tab 留一行"今日已折叠 N 条"——静默折叠＝把通知系统做成黑箱。
//
// 出站通道本身（notify 的三路扇出、宕机播报）一律复用现成实现，本文件不另造通道。
package newsagent

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/notify"
)

// §B6 降噪预算常量（§5A.2）：每票每日必推上限 / 全局每日上限 / 同键冷却窗。
// English: §B6 noise-budget constants: per-stock daily must-push cap, global cap, cooldown window.
const (
	NewsPushPerStockDaily = 2
	NewsPushDailyTotal    = 12
	NewsPushCooldown      = 5 * time.Minute
)

// 消息种类（冷却键的第一维）：冷却按 (类型, 代码) 分格，不同类型互不吃对方的窗。
// English: message kinds — the first dimension of the cooldown key.
const (
	KindNegative     = "news_negative"     // 利空（命中或未命中持仓）
	KindPositive     = "news_positive"     // 利多
	KindD1Tier       = "d1_tier"           // D1 跨档
	KindBuy          = "nshape_buy"        // n形买入（必推、不折叠）
	KindUnattributed = "news_unattributed" // 未归因高相关（LLM 不可用）
	KindChannel      = "news_channel"      // 通道级："今天没读到任何新闻"
)

// Tier 一条消息的去向（§5A.1 表的四个列值）。
// English: Tier is one message's destination, matching the four columns of the §5A.1 table.
type Tier string

const (
	TierMustPush    Tier = "必推"    // LevelHigh：jpush/ntfy/Webhook 现成映射
	TierNormalPush  Tier = "普通推送"  // LevelMedium：出站内置手机网关，但不占必推通道
	TierDailyDigest Tier = "日汇总"   // 不出站，当日累计后一条
	TierNewsTab     Tier = "新闻tab" // 只落消息中心当日「新闻」行
)

// Notice 一条待出站的消息要素（分级、文案与去重指纹都在这里定形，出站只做预算/冷却判定）。
// English: Notice is a ready-to-send message: tier, text and dedup fingerprint are fixed here.
type Notice struct {
	Kind        string // 冷却键第一维（KindNegative 等）
	Code        string // 主标的代码（冷却键第二维；可为空=无个股指向）
	Name        string // 主标的名称
	Tier        Tier   // 去向
	Title       string // 出站标题
	Text        string // 出站正文（§5A.1 的"文案要素缺一不可"就在这里拼）
	Fingerprint string // 多源去重指纹（沿用 NewsEvent 的截断标题口径）
	Sources     int    // 该事件覆盖到的来源数（>1 时文案标"来源 ×N"）
	Foldable    bool   // 是否受预算折叠（n形买入=false：必推且**不折叠**）
	When        time.Time
}

// BuyNotice 是战法侧交来的 n形买入通知要素。
// 这里刻意不 import 战法包（依赖方向保持 strategies → newsagent），
// 只约定"能自述代码/名称/文案 + 能不能算买入档"四件事，internal/strategies/n_shape 实现它。
// English: BuyNotice is the contract the strategy side fulfils; newsagent does not import strategies.
type BuyNotice interface {
	BuyCode() string
	BuyName() string
	BuyText() string
	BuyTier() bool // 是否落在买入档（priorityOf 的 strong + CanOpen）
}

// SourceState 一个新闻来源本轮的读数。OK=false 表示**这一轮取失败**（不是"没有新闻"）。
// §5A.3 第 2 条：三源全失败时必须出一条通道级告警，绝不呈现成"今天没有利空"。
// English: SourceState is one source's round reading; all-failed must surface as a channel alert.
type SourceState struct {
	Name  string
	OK    bool
	Count int
	Err   string
}

// LLMState 归因侧读数：不可用（未配置/连不通）与配额耗尽两种降级分开判——
// 前者逐条走"未归因"文案出站，后者只出一条"今日归因降级"日汇总，不逐条刷。
// English: attribution state; unavailable items still go out, quota exhaustion only yields one summary.
type LLMState struct {
	Available      bool
	QuotaExhausted bool
	Reason         string
}

// D1Change 一条 D1 跨档事件（新旧两值 + 触发它的事件标题；只报变化不报方向＝无法复核）。
// English: a D1 tier crossing with both values and the triggering event title.
type D1Change struct {
	Code    string
	Name    string
	Old     float64
	New     float64
	Trigger string
}

// HitSet 命中判定集合：持仓与自选分开记（利空命中两者都必推，但利多只有命中**持仓**才升普通推送）。
// English: HitSet keeps holdings and watchlist apart — bullish only leaves the tab when a holding hits.
type HitSet struct {
	Holdings map[string]bool
	Watch    map[string]bool
}

// NewHitSet 由代码列表构造命中集合（"名称|代码" 与裸代码都能吃，统一按代码索引）。
// English: NewHitSet builds the hit index from code lists, accepting both "name|code" and bare codes.
func NewHitSet(holdings, watch []string) HitSet {
	return HitSet{Holdings: codeIndex(holdings), Watch: codeIndex(watch)}
}

// Hit 判断一组关联个股是否命中持仓/自选。
// English: Hit reports whether any of the codes is a holding or on the watchlist.
func (h HitSet) Hit(codes []string) (holding, watch bool) {
	for _, c := range codes {
		c = normalizeCode(c)
		if c == "" {
			continue
		}
		if h.Holdings[c] {
			holding = true
		}
		if h.Watch[c] {
			watch = true
		}
	}
	return holding, watch
}

// codeIndex 把代码列表归一成裸代码集合（CleanedStocks 是 "名称|代码"，RelatedStocks 可能是 "名称(代码)"）。
// English: codeIndex normalizes a code list into bare codes ("name|code" / "name(code)" → "code").
func codeIndex(list []string) map[string]bool {
	out := make(map[string]bool, len(list))
	for _, one := range list {
		if c := normalizeCode(one); c != "" {
			out[c] = true
		}
	}
	return out
}

// normalizeCode 从 "名称|代码" / "名称(代码)" / 裸代码里取出 6 位代码；取不到返回空串。
// English: normalizeCode extracts the 6-digit code from the three shapes the pipeline carries.
func normalizeCode(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// "名称|代码"：清洗器产出的主形态，竖线右侧就是代码
	// English: "name|code" — the cleaner's main shape; the right side is the code.
	if i := strings.LastIndex(s, "|"); i >= 0 {
		s = strings.TrimSpace(s[i+1:])
	}
	// "名称(代码)"：LLM/直构事件里的括号形态
	// English: "name(code)" — the bracket shape produced by LLM/direct events.
	if i := strings.LastIndex(s, "("); i >= 0 && strings.HasSuffix(s, ")") {
		s = s[i+1 : len(s)-1]
	}
	// 只保留数字代码位；其余（纯名称、异常串）判为"没有个股指向"，不参与命中判定。
	// English: keep digit-only codes; a bare name means "no stock pointer" and never hits.
	s = strings.TrimSpace(s)
	if len(s) == 6 {
		digits := true
		for _, r := range s {
			if r < '0' || r > '9' {
				digits = false
				break
			}
		}
		if digits {
			return s
		}
	}
	return ""
}

// eventCodes 取一条事件的关联个股（清洗后优先，退回原始名称串）。
// English: eventCodes reads cleaned stocks first, falling back to the raw related-stock list.
func eventCodes(e NewsEvent) []string {
	if len(e.CleanedStocks) > 0 {
		return e.CleanedStocks
	}
	return e.RelatedStocks
}

// scoreLadder 事件强度档（types.go 的既定分档：±0.25 弱 / ±0.5 中 / ±0.75 强）。
// 命中持仓时的"下探一档"就沿这串台阶走，而不是新造一个阈值旋钮。
// English: the established strength bands; "step down one band on a holding hit" walks this ladder.
var scoreLadder = []float64{0.25, 0.5, 0.75}

// ThresholdFor 返回本轮接受阈值：命中持仓/自选时在 Agent.MinScore 基础上下探一档。
// 已在最低档（0.25）时不再下探——低于它的事件连落盘都不落，放行只会把噪音训练成告警。
// （§5A.2 第 4 条：阈值复用 Agent.MinScore，B6 只加这一条下探规则，不新增配置项。）
// English: ThresholdFor steps Agent.MinScore down one band when a holding/watchlist hit occurs.
func ThresholdFor(min float64, hit bool) float64 {
	if !hit || min <= 0 {
		return min // min<=0 = 前端"显示全部"（全收），没有可下探的档
		// English: min<=0 means "show all" — there is no band below the floor to step into.
	}
	for _, rung := range scoreLadder {
		if rung < min {
			// 台阶升序遍历，取严格低于当前档的最高一档 = 恰好低一档
			// English: ascending walk; the highest rung strictly below min is exactly one band down.
			best := rung
			for _, next := range scoreLadder {
				if next < min && next > best {
					best = next
				}
			}
			return best
		}
	}
	return min
}

// D1Tier 把 0~40 的 D1 分归到既有档位（负面过滤 / 低影响 / 中等影响 / 强影响）。
// 分档线取 d1_scorer 提示词里的 8 与 16 两个界（低影响 0~8、中等 8~20、顶级 16~40 的重叠段
// 按"高段优先"归并），跨不跨档只由这两条线判——档位定义只有这一处，改线要同批改文案。
// English: D1Tier buckets the 0~40 score into the existing bands using the 8/16 cut lines.
func D1Tier(v float64) string {
	switch {
	case v <= 0:
		return "负面过滤/无事件"
	case v < 8:
		return "低影响"
	case v < 16:
		return "中等影响"
	default:
		return "强影响"
	}
}

// CrossTier 判断两次 D1 读数是否跨过既有档位（跨档才出普通推送；同档内的抖动不打扰）。
// English: CrossTier reports whether two D1 readings crossed an existing band.
func CrossTier(oldV, newV float64) bool {
	return D1Tier(oldV) != D1Tier(newV)
}

// DiffD1 由新旧两张 D1 表算出跨档清单（triggerBy 提供"触发它的事件标题"，可缺）。
// English: DiffD1 turns two D1 maps into the cross-tier list (trigger titles optional).
func DiffD1(oldMap, newMap map[string]float64, triggerBy map[string]string, nameBy map[string]string) []D1Change {
	codes := make([]string, 0, len(newMap))
	for code, nv := range newMap {
		ov, ok := oldMap[code]
		if !ok || !CrossTier(ov, nv) {
			continue
		}
		codes = append(codes, code)
		_ = nv
	}
	sort.Strings(codes)
	out := make([]D1Change, 0, len(codes))
	for _, c := range codes {
		out = append(out, D1Change{
			Code:    c,
			Name:    nameBy[c],
			Old:     oldMap[c],
			New:     newMap[c],
			Trigger: triggerBy[c],
		})
	}
	return out
}

// PushSink 出站通道（生产实现是 *notify.Notifier：Push/PushLaneAlive/AnnounceChannelDown 三个方法现成）。
// 刻意只暴露这三件事——通道映射与宕机播报一律沿用 internal/notify 的既有逻辑，B6 不另造。
// English: the outbound lane; the production impl is *notify.Notifier, reusing its own
// surviving-channel announcement instead of a new mechanism.
type PushSink interface {
	Push(msg notify.Message)
	PushLaneAlive() bool
	AnnounceChannelDown(title, reason string)
}

// RoundInput 一轮出站的输入：本轮事件 + D1 变化 + n形买入 + 未归因条目 + 通道读数。
// English: one dispatch round's inputs.
type RoundInput struct {
	Events       []NewsEvent
	D1Changes    []D1Change
	Buys         []BuyNotice
	Unattributed []data.NewsItem
	Sources      []SourceState
	LLM          LLMState
	Now          time.Time
}

// RoundReport 一轮出站的读数（调用方据此落消息中心行、写日志、被单测逐条核对）。
// English: the round's readout: pushed messages, digest entries, news-tab rows, fold/suppress counts.
type RoundReport struct {
	Pushed           []notify.Message
	Digest           []string
	NewsRows         []data.MessageItem
	Folded           int // 本轮被折叠进日汇总的条数（当日累计值见 FoldedToday）
	FoldedToday      int
	Suppressed       int  // 冷却/去重压掉的条数（不是丢弃：事件表照旧落库）
	ChannelAnnounced bool // 本轮是否发了"今天没读到任何新闻"
	LaneAnnounced    bool // 本轮是否发了"必推通道不可用"
}

// Dispatcher 通知出站器：持有当日的预算/冷却/去重账，跨交易日随 day-key 一并清零。
// 非并发安全的问题由内部锁兜住（打分轮与手动补推可能同时到达）。
// English: Dispatcher holds the day's budget/cooldown/dedup ledger and resets on the trading day.
type Dispatcher struct {
	sink PushSink
	hits HitSet
	// minScore 直接取 Agent.MinScore（§5A.2：不新增旋钮，读数同源）
	minScore float64
	// uid 新闻行的归属作用域（""=公共）；朋友的持仓命中新闻不该打到 owner 手机上
	uid   string
	nowFn func() time.Time

	mu sync.Mutex
	// 以下都是"当日状态"，日切全部清零（与消息中心同一处 day-key 判据）
	// English: everything below is today's state and clears on the trading-day rollover.
	day            string
	perStock       map[string]int
	daily          int
	folded         int
	cooldown       map[string]time.Time
	sentFPR        map[string]int  // 指纹 → 已计入的来源数（多源去重）
	bearSeenCode   map[string]bool // 未命中持仓的利空：同代码当日只必推首条
	digest         []string
	srcAnnounced   bool // 当日是否已发"今天没读到任何新闻"
	quotaAnnounced bool // 当日是否已发"今日归因降级"
	laneAnnounced  bool // 当日是否已发"必推通道不可用"
}

// NewDispatcher 创建出站器。minScore 请传 Agent.MinScore()（同一把尺子，不要另配一个）。
// English: NewDispatcher builds the outbound dispatcher; pass Agent.MinScore() as the threshold.
func NewDispatcher(sink PushSink, hits HitSet, minScore float64, uid string) *Dispatcher {
	return &Dispatcher{
		sink:         sink,
		hits:         hits,
		minScore:     minScore,
		uid:          strings.TrimSpace(uid),
		nowFn:        time.Now,
		perStock:     make(map[string]int),
		cooldown:     make(map[string]time.Time),
		sentFPR:      make(map[string]int),
		bearSeenCode: make(map[string]bool),
	}
}

// SetMinScore 同步 Agent.MinScore 的热更值（读面与出站同源，不引入第二把尺子）。
// English: SetMinScore mirrors Agent.MinScore's hot-reloaded value so both read sides share one ruler.
func (d *Dispatcher) SetMinScore(v float64) {
	d.mu.Lock()
	d.minScore = v
	d.mu.Unlock()
}

// SetNowFunc 注入时钟（单测按固定时刻驱动冷却窗与日切；生产不调用，保持 time.Now）。
// English: SetNowFunc injects the clock so tests can drive the cooldown window and day rollover.
func (d *Dispatcher) SetNowFunc(f func() time.Time) {
	d.mu.Lock()
	d.nowFn = f
	d.mu.Unlock()
}

// FoldedToday 返回当日累计折叠条数（观测面与用例读它，不另存一份计数）。
// English: FoldedToday returns the day's accumulated fold count.
func (d *Dispatcher) FoldedToday() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.folded
}

// DayState 当日预算用度的读数（证明计数真的在走，不是每轮从 0 开始）。
// English: DayState is the day's budget ledger, proving the counters actually advance.
type DayState struct {
	Day      string
	Daily    int
	PerStock map[string]int
	Folded   int
	Digest   []string
}

// State 返回当日账的拷贝（调用方改动不许影响出站）。
// English: State returns a copy of the day's ledger.
func (d *Dispatcher) State() DayState {
	d.mu.Lock()
	defer d.mu.Unlock()
	per := make(map[string]int, len(d.perStock))
	for k, v := range d.perStock {
		per[k] = v
	}
	return DayState{Day: d.day, Daily: d.daily, PerStock: per, Folded: d.folded, Digest: append([]string(nil), d.digest...)}
}

// rollLocked 日切：与消息中心同一条判据（data.TradingDayDate），预算/冷却/去重/折叠计数一并清零。
// English: rollLocked resets the day's ledger on the same trading-day key the message center uses.
func (d *Dispatcher) rollLocked(now time.Time) {
	td := data.TradingDayDate(now)
	if d.day == td {
		return
	}
	d.day = td
	d.perStock = make(map[string]int)
	d.daily = 0
	d.cooldown = make(map[string]time.Time)
	d.sentFPR = make(map[string]int)
	d.bearSeenCode = make(map[string]bool)
	d.digest = nil
	d.folded = 0
	d.srcAnnounced = false
	d.quotaAnnounced = false
	d.laneAnnounced = false
}

// cooldownKey 冷却键：(类型, 代码) 两维分格。缺了代码那一维，第二只票的利空会被第一只的窗吃掉
// （§5A.2 第 3 条，也是本仓"分键不许全局窗"的一贯取向）。
// English: cooldownKey is (kind, code) — dropping the code dimension is exactly the §5A.2 defect.
func cooldownKey(kind, code string) string {
	return kind + "|" + code
}

// Round 一轮出站：先分级（§5A.1），再过预算/冷却/去重（§5A.2），最后落"不许静默"三条（§5A.3）。
// 返回的 NewsRows 由调用方交给消息中心（data.MessageStore.PublishNews）；本函数不直接碰存储，
// 让出站决策可以被单测单独驱动。
// English: Round classifies (§5A.1), applies budget/cooldown/dedup (§5A.2) and the never-silent
// rules (§5A.3); the caller persists the returned news-tab rows.
func (d *Dispatcher) Round(in RoundInput) RoundReport {
	d.mu.Lock()
	defer d.mu.Unlock()

	now := in.Now
	if now.IsZero() {
		now = d.nowFn()
	}
	d.rollLocked(now)

	rep := RoundReport{}
	// ① 通道级"不许静默"：三源全失败 ⇒ 出一条"今天没读到任何新闻"，当日只一条（不逐轮刷）。
	// 关键是措辞：这是通道故障，不是"今天没有利空"——把取不到伪装成没有是本仓反复出事的同一族错误。
	// English: all sources down yields one channel alert per day, worded as an outage, not as "no bad news".
	if allSourcesDown(in.Sources) && !d.srcAnnounced {
		d.srcAnnounced = true
		d.pushLocked(notify.Message{
			Level: notify.LevelHigh,
			Title: "【新闻通道】今天没读到任何新闻",
			// 正文自己也要把"读不到"说全：宕机播报/仅正文的通道（手机推送折叠、日志检索）拿不到标题时，
			// 只剩"三个来源取数失败"这句容易被读成"今天确实没新闻"。
			// English: the body repeats "nothing was read today" — body-only lanes must not be able to
			// read "sources failed" as "there was no news".
			Content: "今天没读到任何新闻：三个来源全部取数失败（" + sourceFailBrief(in.Sources) +
				"）。这是通道故障，不是「今天没有利空」：未取到的部分不会进事件表，也不构成无风险结论。",
			Alias: d.uid,
		}, &rep)
		rep.ChannelAnnounced = true
	}

	// ② 分级：把本轮输入按 §5A.1 表映射成 Notice 列表。
	notices := d.classifyLocked(in, now)

	// ③ 出站：新闻 tab 与日汇总都不出站；必推先过去重/冷却，再过预算，超限转日汇总并留折叠行。
	// English: the tab and the digest never go out; must-push runs dedup → cooldown → budget, then folds.
	for _, nt := range notices {
		row := d.newsRow(nt, now)
		switch nt.Tier {
		case TierNewsTab:
			rep.NewsRows = append(rep.NewsRows, row)
			continue
		case TierDailyDigest:
			d.foldIntoDigest(nt, &rep)
			rep.NewsRows = append(rep.NewsRows, row)
			continue
		}
		// 去重：同一事件被多源重复投递只发一次（指纹沿用 NewsEvent 的截断标题口径）。
		// English: duplicates across sources go out once, keyed by the existing truncated-title fingerprint.
		if nt.Fingerprint != "" {
			if _, done := d.sentFPR[nt.Fingerprint]; done {
				d.sentFPR[nt.Fingerprint]++
				rep.Suppressed++
				continue
			}
		}
		// 冷却：键按 (类型, 代码) 分格，同格窗口内不重复；不同票互不吃窗。
		key := cooldownKey(nt.Kind, nt.Code)
		if last, ok := d.cooldown[key]; ok && now.Sub(last) < NewsPushCooldown {
			rep.Suppressed++
			continue
		}
		d.cooldown[key] = now
		// 预算：只有必推吃额度；n形买入 Foldable=false 明确"不折叠"（§5A.1 最后一行）。
		// English: only the must-push lane consumes budget; n-shape buys are marked non-foldable.
		if nt.Tier == TierMustPush && nt.Foldable && d.overBudget(nt) {
			d.foldIntoDigest(nt, &rep)
			rep.NewsRows = append(rep.NewsRows, row)
			continue
		}
		if nt.Tier == TierMustPush {
			d.daily++
			if nt.Code != "" {
				d.perStock[nt.Code]++
			}
		}
		if nt.Fingerprint != "" {
			d.sentFPR[nt.Fingerprint] = 1
		}
		d.pushLocked(notify.Message{Level: levelOf(nt.Tier), Title: nt.Title, Content: nt.Text, Alias: d.uid}, &rep)
		rep.NewsRows = append(rep.NewsRows, row)
	}

	// ④ 归因配额耗尽 ⇒ 出一条"今日归因降级"日汇总（当日一次），不逐条刷。
	// English: quota exhaustion yields one daily "attribution degraded" summary, not per-item spam.
	if in.LLM.QuotaExhausted && !d.quotaAnnounced {
		d.quotaAnnounced = true
		d.digest = append(d.digest, "今日归因降级：LLM 配额耗尽，当日高相关新闻以未归因文案出站")
	}

	// ⑤ 折叠必须可见：当日累计折叠 N 条在新闻 tab 里留一行（跨日随消息中心 day-key 一起消失）。
	// English: folds stay visible — one "已折叠 N 条" row lives in the tab for today only.
	if d.folded > 0 {
		rep.NewsRows = append(rep.NewsRows, data.MessageItem{
			ID:          "news|folded|" + d.day,
			Level:       "新闻",
			Title:       fmt.Sprintf("今日已折叠 %d 条", d.folded),
			Body:        "超出每票/全局必推预算的新闻已转日汇总：" + strings.Join(trimList(d.digest, 5), "；"),
			Kind:        data.KindNews,
			GeneratedAt: now,
			Time:        now.Format("15:04:05"),
			Scope:       d.uid,
		})
		rep.FoldedToday = d.folded
	}

	// ⑥ 必推通道全 trip ⇒ 复用 notify 现成的"存活通道播报宕机"，不另造一套。
	// English: when no push channel is alive, reuse notify's surviving-channel announcement.
	if len(rep.Pushed) > 0 && d.sink != nil && !d.sink.PushLaneAlive() && !d.laneAnnounced {
		d.laneAnnounced = true
		d.sink.AnnounceChannelDown("必推通道不可用", "网关/ntfy/Webhook 均不在位，本轮必推消息只进了站内通道")
		rep.LaneAnnounced = true
	}
	rep.Digest = append([]string(nil), d.digest...)
	return rep
}

// pushLocked 经出站通道投递一条消息（sink 为 nil 时只记账不发——单测与"未配置推送器"同形）。
// English: pushLocked sends one message through the sink (nil sink = bookkeeping only).
func (d *Dispatcher) pushLocked(msg notify.Message, rep *RoundReport) {
	rep.Pushed = append(rep.Pushed, msg)
	if d.sink != nil {
		d.sink.Push(msg)
	}
}

// overBudget 必推预算判定：每票每日 2 条、全局每日 12 条（§5A.2 第 1 条）。
// English: overBudget applies the per-stock (2) and daily (12) must-push caps.
func (d *Dispatcher) overBudget(nt Notice) bool {
	if nt.Code != "" && d.perStock[nt.Code] >= NewsPushPerStockDaily {
		return true
	}
	return d.daily >= NewsPushDailyTotal
}

// foldIntoDigest 超预算 ⇒ 转日汇总 + 当日折叠计数（静默折叠＝通知变黑箱，所以计数必须留下）。
// English: over-budget items go to the daily digest and bump the visible fold counter.
func (d *Dispatcher) foldIntoDigest(nt Notice, rep *RoundReport) {
	d.folded++
	d.digest = append(d.digest, "["+nt.Kind+"] "+nt.Code+" "+nt.Title)
	rep.Folded++
}

// levelOf 去向 → notify 级别：必推=LevelHigh，普通推送=LevelMedium（不抢必推通道）。
// English: levelOf maps destinations to levels; the normal lane never takes LevelHigh.
func levelOf(t Tier) notify.AlertLevel {
	if t == TierMustPush {
		return notify.LevelHigh
	}
	return notify.LevelMedium
}

// classifyLocked 按 §5A.1 表把一轮输入映射成待出站消息列表。
// 顺序：新闻事件（同指纹先合并）→ D1 跨档 → n形买入 → 未归因高相关。
// English: classifyLocked applies the §5A.1 table: events (fingerprint-grouped) → D1 crossings →
// n-shape buys → unattributed high-relevance items.
func (d *Dispatcher) classifyLocked(in RoundInput, now time.Time) []Notice {
	var out []Notice
	// 同指纹先合并（多源同事件只发一条并标"来源 ×N"）：按指纹分组，保留首次出现顺序。
	// English: group by fingerprint first (keeping first-seen order) so one event from several sources goes out once.
	byFPR := make(map[string][]NewsEvent)
	order := make([]string, 0, len(in.Events))
	for _, e := range in.Events {
		f := truncTitle(e.Title)
		if _, ok := byFPR[f]; !ok {
			order = append(order, f)
		}
		byFPR[f] = append(byFPR[f], e)
	}
	for _, f := range order {
		out = append(out, d.noticesForGroup(byFPR[f], f, in, now)...)
	}
	// D1 跨档：普通推送，且**必须带新旧两个值 + 触发它的事件标题**（只报变化不报方向＝无法复核）。
	for _, ch := range in.D1Changes {
		if !CrossTier(ch.Old, ch.New) {
			continue
		}
		out = append(out, Notice{
			Kind:     KindD1Tier,
			Code:     normalizeCode(ch.Code),
			Name:     ch.Name,
			Tier:     TierNormalPush,
			Title:    "D1 跨档 " + ch.Code,
			Text:     d1Text(ch),
			Foldable: false, // 普通推送不吃必推额度，也就不存在"被折叠"
			When:     now,
		})
	}
	// n形买入：必推 + 多通道并发（notify.Push 已内聚 WS/Webhook/手机网关三路）+ 不折叠。
	// English: n-shape buys go to the must-push lane, fan out on all channels and skip folding.
	for _, b := range in.Buys {
		if b == nil || !b.BuyTier() {
			continue
		}
		code := normalizeCode(b.BuyCode())
		out = append(out, Notice{
			Kind:        KindBuy,
			Code:        code,
			Name:        b.BuyName(),
			Tier:        TierMustPush,
			Title:       "🚀 n形买入 " + b.BuyName() + "(" + code + ")",
			Text:        b.BuyText(),
			Fingerprint: "buy|" + code + "|" + now.Format("200601021504"),
			Foldable:    false,
			When:        now,
		})
	}
	// 未归因高相关（LLM 不可用）：仍出站，并明标"未归因（LLM 不可用）"——
	// 归因失败不许伪造成"没有消息"（§5A.3 第 1 条）。
	if !in.LLM.Available {
		for _, item := range in.Unattributed {
			codes := d.hitCodesInTitle(item.Title)
			holding, watch := d.hits.Hit(codes)
			if !holding && !watch {
				continue // 未归因只放行命中持仓/自选的：否则整条新闻流都会变成告警
				// English: unattributed items only go out when they hit a holding/watchlist stock.
			}
			out = append(out, Notice{
				Kind:        KindUnattributed,
				Code:        firstCode(codes),
				Name:        firstName(nil, item.Title),
				Tier:        TierMustPush,
				Title:       "【未归因】" + item.Title,
				Text:        unattributedText(item, codes, holding, watch),
				Fingerprint: truncTitle(item.Title),
				Foldable:    true,
				When:        now,
			})
		}
	}
	return out
}

// hitCodesInTitle 在标题里找在册代码（未归因条目只有标题，没有 LLM 给的关联个股）。
// English: hitCodesInTitle scans the title for known codes (unattributed items carry no stock list).
func (d *Dispatcher) hitCodesInTitle(title string) []string {
	var codes []string
	for c := range d.hits.Holdings {
		if strings.Contains(title, c) {
			codes = append(codes, c)
		}
	}
	for c := range d.hits.Watch {
		if strings.Contains(title, c) && !strings.Contains(title, "|"+c) {
			codes = append(codes, c)
		}
	}
	sort.Strings(codes)
	return codes
}

// noticesForGroup 一条事件（含同指纹的多源副本）按 §5A.1 决定去向与文案。
// English: noticesForGroup applies the table to one fingerprint group.
func (d *Dispatcher) noticesForGroup(group []NewsEvent, fpr string, in RoundInput, now time.Time) []Notice {
	if len(group) == 0 {
		return nil
	}
	e := group[0]
	codes := eventCodes(e)
	holding, watch := d.hits.Hit(codes)
	hit := holding || watch
	thr := ThresholdFor(d.minScore, hit)
	abs := e.Score
	if abs < 0 {
		abs = -abs
	}
	// 低于阈值不出站（事件表照旧落库：UI 不显示 ≠ 证据不留，§5A.3b 第 3 条）。
	// English: below threshold nothing goes out, while the NewsEvent table still keeps the evidence.
	if abs < thr {
		return nil
	}
	srcs := distinctSources(group)
	code := firstCode(codes)
	when := parseEventTime(e.Datetime, now)
	d1 := findD1Change(in.D1Changes, code)

	switch {
	case e.Direction == "利空":
		scope, action := "未命中持仓", "评估减仓并复核止损线"
		if holding {
			scope, action = "命中持仓", "暂停买入并评估减仓"
		} else if watch {
			scope, action = "命中自选", "暂停买入并评估减仓"
		}
		tier := TierMustPush
		// 利空未命中持仓：当日同代码只有首条必推，其余转日汇总（§5A.1 第 2 行）。
		// English: bearish news that misses the book pushes once per code per day; the rest go to the digest.
		if !hit {
			bkey := code
			if bkey == "" {
				bkey = fpr
			}
			if d.bearSeenCode[bkey] {
				tier = TierDailyDigest
			} else {
				d.bearSeenCode[bkey] = true
			}
		}
		return []Notice{{
			Kind:        KindNegative,
			Code:        code,
			Name:        firstName(codes, e.Title),
			Tier:        tier,
			Title:       fmt.Sprintf("【利空·%s】%s", scope, e.Title),
			Text:        negativeText(e, code, scope, action, d1, srcs, when),
			Fingerprint: fpr,
			Foldable:    true,
			When:        when,
		}}

	case e.Direction == "利好":
		if holding {
			// 利多命中持仓：普通推送，且文案写死"无需动作"——把它挤进必推就是在稀释利空。
			// English: bullish on a holding uses the normal lane and says "no action needed".
			return []Notice{{
				Kind:        KindPositive,
				Code:        code,
				Name:        firstName(codes, e.Title),
				Tier:        TierNormalPush,
				Title:       "【利好·命中持仓】" + e.Title,
				Text:        positiveText(e, code, "无需动作（利多不占必推通道）", srcs, when),
				Fingerprint: fpr,
				Foldable:    false,
				When:        when,
			}}
		}
		// 利多非持仓（含只命中自选的）：进「新闻」tab，不占必推、不混进信号流（owner 11-03 定）。
		// English: bullish off the book goes to the news tab only — no must-push lane, no signal stream.
		return []Notice{{
			Kind:        KindPositive,
			Code:        code,
			Name:        firstName(codes, e.Title),
			Tier:        TierNewsTab,
			Title:       e.Title,
			Text:        positiveText(e, code, "无需动作（利多不占必推通道）", srcs, when),
			Fingerprint: fpr,
			Foldable:    false,
			When:        when,
		}}
	}
	return nil
}

// newsRow 把一条 Notice 折成消息中心「新闻」行（利多非持仓 / 折叠条目都留在 tab 里可见）。
// English: newsRow renders a notice as a message-center news-tab row.
func (d *Dispatcher) newsRow(nt Notice, now time.Time) data.MessageItem {
	gen := nt.When
	if gen.IsZero() {
		gen = now
	}
	return data.MessageItem{
		ID:          "news|" + nt.Fingerprint,
		Code:        nt.Code,
		Name:        nt.Name,
		Level:       "新闻",
		Title:       nt.Title,
		Body:        nt.Text,
		Direction:   directionOfKind(nt.Kind),
		Time:        gen.Format("15:04:05"),
		GeneratedAt: gen,
		Scope:       d.uid,
		Kind:        data.KindNews,
	}
}

// directionOfKind 新闻行的方向串（前端按它着色；未知类型给中性）。
// English: directionOfKind maps a notice kind to the display direction string.
func directionOfKind(kind string) string {
	switch kind {
	case KindNegative, KindUnattributed:
		return "利空"
	case KindPositive:
		return "利好"
	default:
		return "中性"
	}
}

// negativeText 利空文案：标的 + 一句归因 + 建议动作 + D1 新旧值 + 源与时刻（§5A.1 缺一不可）。
// English: bearish text carries stock, one-line attribution, suggested action, D1 old/new, source and time.
func negativeText(e NewsEvent, code, scope, action string, d1 *D1Change, srcs int, when time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "标的：%s(%s)｜归因：%s", firstName(e.CleanedStocks, e.Title), code, reasonOf(e))
	fmt.Fprintf(&b, "｜建议动作：%s", action)
	// D1 新旧值：有跨档记录就带两个值；没有就如实写"本轮无 D1 读数"，不许留空冒充"没变"。
	// English: carry both D1 values when a crossing exists; say so explicitly when there is no reading.
	if d1 != nil {
		fmt.Fprintf(&b, "｜D1：%.1f→%.1f（%s→%s）", d1.Old, d1.New, D1Tier(d1.Old), D1Tier(d1.New))
	} else {
		b.WriteString("｜D1：本轮无读数")
	}
	fmt.Fprintf(&b, "｜来源：%s%s｜时刻：%s｜命中：%s",
		e.Source, sourceTag(srcs), when.Format("2006-01-02 15:04:05"), scope)
	return b.String()
}

// positiveText 利多文案：标的 + 归因 + "无需动作"（§5A.1 第 3 行）。
// English: bullish text carries the stock, attribution and "no action needed".
func positiveText(e NewsEvent, code, action string, srcs int, when time.Time) string {
	return fmt.Sprintf("标的：%s(%s)｜归因：%s｜动作：%s｜来源：%s%s｜时刻：%s",
		firstName(e.CleanedStocks, e.Title), code, reasonOf(e), action, e.Source, sourceTag(srcs),
		when.Format("2006-01-02 15:04:05"))
}

// d1Text D1 跨档文案：新旧两值 + 档位 + 触发它的事件标题。
// English: D1 crossing text carries both values, both bands and the triggering title.
func d1Text(ch D1Change) string {
	trig := ch.Trigger
	if trig == "" {
		trig = "（本轮无事件标题）"
	}
	return fmt.Sprintf("D1 %.1f→%.1f（%s→%s）｜标的：%s(%s)｜触发事件：%s",
		ch.Old, ch.New, D1Tier(ch.Old), D1Tier(ch.New), ch.Name, ch.Code, trig)
}

// unattributedText 未归因文案：标题原文 + 命中票 + "归因暂缺"，并明标"未归因（LLM 不可用）"。
// English: unattributed text keeps the raw title, the hit codes and the explicit "LLM unavailable" note.
func unattributedText(item data.NewsItem, codes []string, holding, watch bool) string {
	scope := "未命中持仓"
	if holding {
		scope = "命中持仓"
	} else if watch {
		scope = "命中自选"
	}
	return fmt.Sprintf("标题：%s｜命中：%s(%s)｜未归因（LLM 不可用）：归因暂缺｜来源：%s｜时刻：%s",
		item.Title, strings.Join(codes, ","), scope, item.Source, item.Datetime)
}

// reasonOf 取事件的归因说明；LLM 没给原因时如实写"未给归因"，不许用空串冒充"无风险"。
// English: reasonOf returns the attribution text or an explicit "no reason given".
func reasonOf(e NewsEvent) string {
	r := strings.TrimSpace(e.Reason)
	if r == "" {
		return "未给归因"
	}
	return r
}

// sourceTag 多源去重标记：来源数 >1 时文案标"来源 ×N"（只发一次、来源可见）。
// English: sourceTag appends " ×N" when several sources carried the same event.
func sourceTag(n int) string {
	if n > 1 {
		return fmt.Sprintf(" ×%d", n)
	}
	return ""
}

// distinctSources 同指纹分组内不同来源的个数（去重后"来源 ×N"的 N）。
// English: distinctSources counts the different sources inside one fingerprint group.
func distinctSources(group []NewsEvent) int {
	seen := make(map[string]bool, len(group))
	for _, e := range group {
		seen[blankTo(e.Source, "未知源")] = true
	}
	return len(seen)
}

// findD1Change 在跨档清单里按代码找那条 D1 变化（利空文案要带它的新旧值）。
// English: findD1Change looks the code up in the crossing list.
func findD1Change(list []D1Change, code string) *D1Change {
	if code == "" {
		return nil
	}
	for i := range list {
		if normalizeCode(list[i].Code) == code {
			return &list[i]
		}
	}
	return nil
}

// firstCode 取第一个可用代码作为主标的（文案与冷却键都用它）。
// English: firstCode picks the first usable code as the message's main subject.
func firstCode(codes []string) string {
	for _, c := range codes {
		if n := normalizeCode(c); n != "" {
			return n
		}
	}
	return ""
}

// firstName 从 "名称|代码" 串里取名称；取不到时退回标题前缀（标题里必然含标的线索）。
// English: firstName reads the name out of "name|code", falling back to the title prefix.
func firstName(codes []string, title string) string {
	for _, c := range codes {
		if i := strings.Index(c, "|"); i > 0 {
			return strings.TrimSpace(c[:i])
		}
	}
	t := strings.TrimSpace(title)
	if t == "" {
		return ""
	}
	r := []rune(t)
	if len(r) > 12 {
		return string(r[:12])
	}
	return t
}

// parseEventTime 解析事件时间串，失败退回本轮时刻（不许拿零值时间去格式化成一串 0001）。
// English: parseEventTime falls back to the round time so a bad timestamp never prints year 1.
func parseEventTime(dt string, fallback time.Time) time.Time {
	if t, err := time.ParseInLocation("2006-01-02 15:04:05", dt, time.Local); err == nil {
		return t
	}
	if t, err := time.ParseInLocation("2006-01-02", dt, time.Local); err == nil {
		return t
	}
	return fallback
}

// allSourcesDown 判"三源全失败"：至少要有两个来源读数，且**每一条**都取失败。
// 空读数（Sources 为 nil）不算全失败——那叫"本轮没做取数"，不是"通道坏了"。
// English: allSourcesDown requires ≥2 reported sources, all failed; an empty reading means
// "no fetch happened this round", not "the channel is down".
func allSourcesDown(list []SourceState) bool {
	if len(list) < 2 {
		return false
	}
	for _, s := range list {
		if s.OK {
			return false
		}
	}
	return true
}

// sourceFailBrief 通道告警里的失败摘要（源名 + 错误串，最多三个，便于值班一眼定位）。
// English: sourceFailBrief renders up to three source/error pairs for the channel alert.
func sourceFailBrief(list []SourceState) string {
	parts := make([]string, 0, len(list))
	for _, s := range list {
		if s.OK {
			continue
		}
		parts = append(parts, s.Name+"="+blankTo(s.Err, "未知错误"))
	}
	return strings.Join(trimList(parts, 3), "、")
}

// blankTo 空串兜底成可读文案（不许出现"来源=  "这种看起来像数据缺失的空值）。
// English: blankTo replaces an empty string with a readable placeholder.
func blankTo(s, placeholder string) string {
	if strings.TrimSpace(s) == "" {
		return placeholder
	}
	return s
}

// trimList 截断列表到最多 n 项（日志与文案都不许无限长）。
// English: trimList caps a slice to at most n items.
func trimList(list []string, n int) []string {
	if len(list) <= n {
		return list
	}
	return list[:n]
}

// DigestText 当日日汇总文本（调用方把它交给日汇总通道；本包不自己出站，避免双写者）。
// English: DigestText returns today's digest for the caller's daily-summary channel.
func (d *Dispatcher) DigestText() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.digest) == 0 {
		return ""
	}
	return fmt.Sprintf("新闻日汇总（%s，折叠 %d 条）：%s", d.day, d.folded, strings.Join(d.digest, "；"))
}
