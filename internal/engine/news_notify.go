// news_notify.go — §NEWS-TIER（生产接线批）：把 newsagent/tier.go 的分级出站器真正接进新闻流水线。
//
// 这个文件为什么存在：§B6（2026-11-05）把利空必推 / 利多不占必推 / 预算折叠 / 冷却分格 /
// 三条"不许静默"全部写成了 newsagent.Dispatcher，但 `NewDispatcher` 在整仓**零生产调用点**
// （唯一引用是它自己的单测）——实盘每一轮新闻只完成了"算出分级并落事件表"，
// 必推/普通推送/日汇总/新闻 tab 四条去向一条都没打开过。这与本仓反复出现的
// "实现孤儿腿"家族同形（report_uplink 曾只有 import 没有调用点、资金心跳曾漏装配 ctrl）。
// 本文件补的就是那最后一跳：
//   - 每个账号一个 Dispatcher（引擎侧按 uid 懒建）：**当日账（预算/冷却/去重/折叠计数）按 uid 独立**，
//     一个账号的预算绝不许把另一个账号的必推噤声——那是账目规则，不是体验优化；
//   - 归属身份只取引擎在册账号集（memberIDs，由 registry 按注册表注入），
//     永不读请求体或任何自述字段（§TOKEN-PERAACCT / §安全 T1 同口径：只认平台持有的身份）；
//   - 命中集合（持仓+自选）按账号向 registry 注入的只读回调取，与 allPaperHeldCodes 同装配形状；
//   - 每轮 produceNews 事件定稿后**恰好一次** Round；RoundReport 要求调用方做的
//     （新闻 tab 行落消息中心、计数落日志、读数上屏）在这里全部做完；
//   - 构造不了分级器（没接推送器 / 没有账号身份 / 没有持仓来源）时，新闻轮照旧跑完，
//     并把"分级被跳过"记成**显式读数与日志**——"没分级"永远不许被读成"今天没有新闻"
//     （本仓重复次数最多的缺陷：J0 零回包、空快照 vs 没取到、QS 系列，全是这一族）。
package engine

import (
	"log"
	"sort"
	"strings"
	"time"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/newsagent"
	"quant-trading-v2/internal/notify"
)

// NewsTierReadout 一个账号最近一轮分级出站的读数（只读观测面）。
// Constructed=false 时 SkipReason 必非空——这两件事合起来就是"分级腿在不在工作"的判据，
// 缺任何一个都等于回到"读不到就当没有"的老形状。
// English: per-account readout of the latest tiering round; SkipReason is mandatory when
// Constructed is false, so "tiering skipped" can never be read as "no news today".
type NewsTierReadout struct {
	UID              string    `json:"uid"`
	Constructed      bool      `json:"constructed"`       // 分级器是否在位
	SkipReason       string    `json:"skip_reason"`       // 不在位的原因（在位时为空串占位，不省略字段）
	Events           int       `json:"events"`            // 本轮定稿事件数（与分级是否发生无关，先记下来）
	Pushed           int       `json:"pushed"`            // 本轮出站总条数
	HighPushed       int       `json:"high_pushed"`       // 本轮必推（LevelHigh）条数
	Suppressed       int       `json:"suppressed"`        // 冷却/去重压掉条数（不是丢弃：事件表照旧落库）
	Folded           int       `json:"folded"`            // 本轮超预算转日汇总条数
	FoldedToday      int       `json:"folded_today"`      // 当日累计折叠（预算在走，不是每轮从 0 开始）
	NewsRows         int       `json:"news_rows"`         // 写入消息中心「新闻」tab 的行数
	ChannelAnnounced bool      `json:"channel_announced"` // 本轮是否发了"今天没读到任何新闻"
	LaneAnnounced    bool      `json:"lane_announced"`    // 本轮是否发了"必推通道不可用"
	At               time.Time `json:"at"`                // 本轮分级时刻
}

// newsD1Snap 某账号上一轮的 D1 读数快照（跨档 diff 的基准）。
// 按交易日 keyed：昨日分数与今日分数不互相比对（与消息中心/Dispatcher 同一处日界线）。
// English: one account's previous-round D1 snapshot, day-keyed so yesterday's readings never
// diff against today's (the repo's single day-line rule).
type newsD1Snap struct {
	day    string
	scores map[string]float64
}

// newsDispEntry 在册分级器 + 重建判据（日切或命中集变化时重建）。
// English: a live dispatcher plus its rebuild predicates (day rollover / hit-set change).
type newsDispEntry struct {
	disp *newsagent.Dispatcher
	day  string // 装配时的交易日（跨日重建，与 Dispatcher 自己的日切同判据）
	sig  string // 命中集签名（变了就重建，否则"上午买入的持仓"永远进不了下午的命中判定）
}

// SetNewsHitProvider 注入"某账号的持仓+自选"只读回调（registry 装配；测试注入桩）。
// 刻意做成回调而不是快照：命中集必须是**这一轮**的持仓，不能是启动那一刻的。
// English: injects the per-account holdings/watchlist provider (wired by the registry).
// A callback rather than a snapshot — hits must reflect *this round's* book.
func (e *Engine) SetNewsHitProvider(fn func(uid string) (holdings, watch []string)) {
	e.mu.Lock()
	e.newsHitProvider = fn
	e.mu.Unlock()
}

// syncNewsTierMinScore 把 Agent.MinScore 的热更值同步到全部在册分级器。
// 调用点是"资讯显示全部"开关（SetNewsShowAll）——读面与出站必须同一把尺子；
// 另外 dispatchNewsTier 每轮还会再同步一次，双保险，漏任何一处都不会漂出第二把尺子。
// English: mirrors Agent.MinScore's hot-reloaded value onto every live dispatcher; also
// re-synced per round inside dispatchNewsTier as a belt-and-braces against ruler drift.
func (e *Engine) syncNewsTierMinScore(v float64) {
	e.newsDispMu.Lock()
	defer e.newsDispMu.Unlock()
	for uid, ent := range e.newsDisps {
		if ent.disp != nil {
			ent.disp.SetMinScore(v)
			log.Printf("[news-tier] 阈值热更同步 uid=%q minScore=%.2f", uid, v)
		}
	}
}

// newsDispatcher 取（或懒建）某账号的分级器；不可构造时返回 nil + 跳过原因。
// 三道构造前提按"缺了会算错账"排序，不是按报错方便排序：
//   - newsAgent 缺失 → 输入侧根本没有（阈值/源读数/未归因队列都从它取）；
//   - notifier 缺失 → 必推消息会"只进 report 不出站"，读起来像发了但其实没发（nil 接收器
//     的 Push 还会直接 panic，这里必须挡住）；
//   - uid 为空 → 没有账号身份就没有归属，推送别名与账本都无从谈起（§TOKEN-PERAACCT：
//     归属只认平台在册身份）；
//   - 命中集回调缺失 → 分级器会把**所有**利空当"未命中持仓"逐码必推，那就是把整条新闻流做成告警。
//
// English: fetches or lazily builds one account's dispatcher; returns nil + skip reason when a
// construction precondition is missing (preconditions are ordered by "wrong accounting", not by
// logging convenience).
func (e *Engine) newsDispatcher(uid string, na *newsagent.Agent, now time.Time) (*newsagent.Dispatcher, string) {
	if na == nil {
		return nil, "无新闻代理(newsAgent=nil)"
	}
	e.mu.RLock()
	nt := e.notifier
	provider := e.newsHitProvider
	e.mu.RUnlock()
	if nt == nil {
		return nil, "无推送通知器(notifier=nil)——必推只进读数不出站，不许冒充已送达"
	}
	uid = strings.TrimSpace(uid)
	if uid == "" {
		return nil, "无账号身份(memberIDs 为空串)——归属与账本都无从谈起（身份只认在册，不信自报）"
	}
	if provider == nil {
		return nil, "无持仓/自选来源(newsHitProvider=nil)——缺它会把整条新闻流当利空告警"
	}
	// 锁外取命中集：回调会进注册表/纸面引擎的锁，绝不把分级器锁压在别人家锁下面。
	// English: the provider runs outside our lock — it reaches into registry/paper locks; no lock nesting.
	holdings, watch := provider(uid)
	sig := newsHitSig(holdings, watch)
	td := data.TradingDayDate(now)

	e.newsDispMu.Lock()
	defer e.newsDispMu.Unlock()
	if e.newsDisps == nil {
		e.newsDisps = make(map[string]newsDispEntry)
	}
	ent, ok := e.newsDisps[uid]
	// 重建条件两条，都写在注释里防止日后被人"顺手优化"成永不重建：
	//   ① 跨交易日——Dispatcher 自己也会清当日账，整台重建与它的日切同判据，不产生第二套日界线；
	//   ② 命中集变化——分级器把 HitSet 定死在构造期，持仓/自选当天变了就必须换一台，
	//      代价是当日预算/冷却账重开（最坏情形是同一条新闻多推一次），
	//      比"上午买入的票下午爆雷却按非持仓口径走"这笔账划算。
	// English: two rebuild predicates — day rollover (same TradingDayDate judgement as the
	// dispatcher itself) and hit-set change (worst case one re-push, far cheaper than a stale book).
	if ok && ent.disp != nil && ent.day == td && ent.sig == sig {
		return ent.disp, ""
	}
	d := newsagent.NewDispatcher(nt, newsagent.NewHitSet(holdings, watch), na.MinScore(), uid)
	e.newsDisps[uid] = newsDispEntry{disp: d, day: td, sig: sig}
	log.Printf("[news-tier] 分级器装配 uid=%q 持仓=%d 自选=%d 交易日=%s（重建=%v）",
		uid, len(holdings), len(watch), td, ok)
	return d, ""
}

// newsHitSig 命中集签名：两份列表各自去重排序后拼接。
// 只用于"变了没有"的判定，不进任何文案或持久化。
// English: a stability-only signature of the hit set (deduped + sorted), never user-visible.
func newsHitSig(holdings, watch []string) string {
	dedupe := func(list []string) string {
		seen := make(map[string]bool, len(list))
		clean := make([]string, 0, len(list))
		for _, c := range list {
			c = newsBareCode(c)
			if c == "" || seen[c] {
				continue
			}
			seen[c] = true
			clean = append(clean, c)
		}
		sort.Strings(clean)
		return strings.Join(clean, ",")
	}
	return "h:" + dedupe(holdings) + "|w:" + dedupe(watch)
}

// newsBareCode 从 "600519.SH" / "名称|600519" / 裸 6 位码里取 6 位代码；取不到返回空串。
// tier.go 的 normalizeCode 是包内私有且只认后两种形态，实盘持仓表存的是带市场后缀的
// ts_code，所以这里多剥一层后缀，别把 "600519.SH" 原样喂进去判成"没有个股指向"。
// English: extracts the bare 6-digit code from ts_code / "name|code" / bare forms — tier.go's
// normalizer does not strip the ".SH" suffix that the live book carries.
func newsBareCode(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if i := strings.LastIndexByte(s, '|'); i >= 0 {
		s = strings.TrimSpace(s[i+1:])
	}
	if i := strings.IndexByte(s, '.'); i >= 0 {
		s = s[:i]
	}
	if len(s) != 6 {
		return ""
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return s
}

// newsTierD1Changes 算某账号视角的 D1 跨档清单，并把该账号的快照推进到本轮读数。
// 基准是引擎最近一轮主循环 D1 评分（LastD1Scores）：
//   - RetryPending（LLM 失败占位 0 分）不进读数——否则重试队列会把分数打回 0 冒充"跨档下跌"；
//   - 快照按 uid 独立：共享引擎服务的多个账号各算各的跨档，一台推过另一台不会替它吞掉；
//   - 快照按交易日 keyed：新交易日第一轮只立基准不比对（昨日与今日不同一天，不是"变了"）。
//
// English: per-account D1 crossings diffed against this engine's latest D1 round; retry-pending
// zeros are excluded, snapshots are uid-keyed and day-keyed so a new day only re-baselines.
func (e *Engine) newsTierD1Changes(uid string, events []newsagent.NewsEvent, now time.Time) []newsagent.D1Change {
	cur := make(map[string]float64)
	for code, d := range e.LastD1Scores() {
		if d.RetryPending {
			continue // LLM 失败的本轮占位 0 分不是读数（重试队列下轮真值回来再比）
		}
		c := newsBareCode(code)
		if c == "" {
			continue
		}
		cur[c] = d.Score
	}
	// 触发标题与名称从本轮定稿事件里取（§5A.1：D1 跨档必须带"触发它的事件标题"）。
	// English: trigger titles/names come from this round's finalized events (§5A.1 requires both).
	triggerBy := make(map[string]string)
	nameBy := make(map[string]string)
	for _, ev := range events {
		lists := ev.CleanedStocks
		if len(lists) == 0 {
			lists = ev.RelatedStocks
		}
		for _, sc := range lists {
			c := newsBareCode(sc)
			if c == "" {
				continue
			}
			if _, done := triggerBy[c]; !done {
				triggerBy[c] = ev.Title
			}
			if i := strings.IndexByte(sc, '|'); i > 0 {
				nameBy[c] = strings.TrimSpace(sc[:i])
			}
		}
	}

	e.newsDispMu.Lock()
	defer e.newsDispMu.Unlock()
	if e.newsD1Snaps == nil {
		e.newsD1Snaps = make(map[string]newsD1Snap)
	}
	td := data.TradingDayDate(now)
	prev := e.newsD1Snaps[uid]
	var changes []newsagent.D1Change
	if prev.day == td && prev.scores != nil {
		changes = newsagent.DiffD1(prev.scores, cur, triggerBy, nameBy)
	}
	// 无论是否跨档都推进快照：同一次变化只报一次（与 Dispatcher 的当日去重同一取向）。
	// English: the snapshot always advances, so one crossing is reported exactly once.
	e.newsD1Snaps[uid] = newsD1Snap{day: td, scores: cur}
	return changes
}

// dispatchNewsTier 一轮新闻出站：对本引擎服务的**每个账号**各跑一次 Round。
// 调用点是 produceNews 的两个 return（事件定稿之后）；这里是观测腿，
// 整体套 recover——分级出了任何问题都不许把新闻流水线带下去（§OBS-GUARD 同规：
// 观测腿既没有停机权限，也不许有把主链路拖死的权力）。
// English: runs one Round per account served by this engine at the two produceNews return
// points; fully panic-guarded because an observation leg must never take the pipeline down.
func (e *Engine) dispatchNewsTier(out produceOut) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[news-tier] 观测腿 panic 已恢复（本轮分级跳过，新闻流水线不受影响）: %v", r)
		}
	}()
	e.mu.RLock()
	na := e.newsAgent
	e.mu.RUnlock()
	targets := e.memberIDs()
	for _, uid := range targets {
		e.dispatchNewsTierFor(uid, na, out)
	}
}

// dispatchNewsTierFor 单账号一轮：构造（或跳过）→ Round → 落新闻行 → 记读数与日志。
// English: one account's round: construct-or-skip, Round, tab rows, readout and log.
func (e *Engine) dispatchNewsTierFor(uid string, na *newsagent.Agent, out produceOut) {
	now := e.nowTime()
	ro := NewsTierReadout{UID: uid, Events: len(out.valid), At: now}
	disp, skip := e.newsDispatcher(uid, na, now)
	if disp == nil {
		// 跳过必须留下"本轮其实有 N 条事件"这个数：把"没分级"伪装成"没新闻"
		// 正是本仓反复出事的同一形状（J0 零回包、空快照、指数盲区全是一族）。
		// English: the skip record keeps "this round had N events" — a skipped tiering must
		// never be readable as a newsless day.
		ro.SkipReason = skip
		e.recordNewsTierReadout(ro)
		log.Printf("[news-tier] 分级跳过 uid=%q reason=%s 本轮定稿事件=%d（这不是「今天没有新闻」）",
			uid, skip, ro.Events)
		return
	}
	// 每轮把尺子对齐 Agent.MinScore（热更跟随；SetNewsShowAll 那处同步是同一值的另一条腿）。
	// English: re-sync the threshold every round so the read side and the send side share one ruler.
	disp.SetMinScore(na.MinScore())

	in := newsagent.RoundInput{
		Events:    out.valid, // 本轮定稿事件（含固化回填）
		D1Changes: e.newsTierD1Changes(uid, out.valid, now),
		// Buys：n形买入通知的生产装配点（n_shape.BuyNoticeFrom）在本仓还没有战法侧调用方，
		// 那是与 tier.go 同族的另一条孤儿腿，接线不在本批范围；分级器对空列表零动作。
		// English: the n-shape buy producer is itself not wired in the strategy layer yet
		// (a separate orphan leg, out of this batch); an empty list is inert.
		Buys:         nil,
		Unattributed: na.UnattributedItems(), // 本轮归因后仍留队的条目（LLM 不可用时仍出站）
		Sources:      na.SourcesStatus(),     // 三源全坏 ⇒ "今天没读到任何新闻"那条通道告警的输入
		LLM:          na.LLMStatus(),
		Now:          now, // 引擎时钟（测试可注入），不另取真实时间，保证与冷却/日切同一时刻源
	}
	rep := disp.Round(in)

	// 新闻行落消息中心：ID 按既有私有消息口径加 "u<uid>|" 前缀——
	// 分级器给的 ID 只含指纹，两个账号同一条新闻会撞同一去重键、后写的把先写的顶掉。
	// English: prefix the row ID with the account (§GAP2-W2 convention), otherwise the same
	// headline's fingerprint would collide across accounts and one would erase the other.
	rows := make([]data.MessageItem, 0, len(rep.NewsRows))
	for _, row := range rep.NewsRows {
		if uid != "" && !strings.HasPrefix(row.ID, "u"+uid+"|") {
			row.ID = "u" + uid + "|" + row.ID
		}
		rows = append(rows, row)
	}
	if e.msgStore != nil && len(rows) > 0 {
		e.pushSSEMessages(rows) // 先按"首次出现"扇出再落库（与主循环消息同步同一形状）
		e.msgStore.PublishNews(rows)
	}

	// 必推计数只认 LevelHigh：普通推送（LevelMedium）与折叠行都出站在别处，
	// 把它们折进这一个数，读面上就看不出"必推通道到底响了几次"。
	// English: count LevelHigh only — merging normal pushes in would hide how often the
	// must-push lane actually fired.
	high := 0
	for _, m := range rep.Pushed {
		if m.Level == notify.LevelHigh {
			high++
		}
	}
	ro.Constructed = true
	ro.Pushed = len(rep.Pushed)
	ro.HighPushed = high
	ro.Suppressed = rep.Suppressed
	ro.Folded = rep.Folded
	ro.FoldedToday = rep.FoldedToday
	ro.NewsRows = len(rows)
	ro.ChannelAnnounced = rep.ChannelAnnounced
	ro.LaneAnnounced = rep.LaneAnnounced
	e.recordNewsTierReadout(ro)
	log.Printf("[news-tier] uid=%q 事件=%d 出站=%d(必推=%d) 冷却去重=%d 折叠=%d(当日累计=%d) 新闻行=%d 通道告警=%v 通道宕机=%v",
		uid, len(in.Events), ro.Pushed, ro.HighPushed, ro.Suppressed, ro.Folded, ro.FoldedToday,
		ro.NewsRows, ro.ChannelAnnounced, ro.LaneAnnounced)
}

// recordNewsTierReadout 留存单账号本轮读数（按 uid 覆盖；读面只给快照拷贝）。
func (e *Engine) recordNewsTierReadout(ro NewsTierReadout) {
	e.newsDispMu.Lock()
	defer e.newsDispMu.Unlock()
	if e.newsTierReadout == nil {
		e.newsTierReadout = make(map[string]NewsTierReadout)
	}
	e.newsTierReadout[ro.UID] = ro
}

// NewsTierStatus 返回各账号最近一轮分级读数（按 uid 排序，输出稳定）。
// 只读观测面：值本身不驱动任何交易判断（观测腿不许有停机权限，§OBS-GUARD 同规）。
// English: read-only per-account status of the latest tiering round, uid-sorted for stability.
func (e *Engine) NewsTierStatus() []NewsTierReadout {
	e.newsDispMu.Lock()
	defer e.newsDispMu.Unlock()
	uids := make([]string, 0, len(e.newsTierReadout))
	for uid := range e.newsTierReadout {
		uids = append(uids, uid)
	}
	sort.Strings(uids)
	out := make([]NewsTierReadout, 0, len(uids))
	for _, uid := range uids {
		out = append(out, e.newsTierReadout[uid])
	}
	return out
}
