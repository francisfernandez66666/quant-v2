package paper

import "time"

// ── §W-02 纸面通道的"今日动态仓位"决策快照 ───────────────────────────────────────
//
// 为什么必须补：`/api/signals/decisions` 只读实盘账本，唯一写入点原本是**实盘自动买入**路径
// 且 Channel 硬编码 "live"。纸面的动态预算走的是 internal/paper 内部一条完全独立的算式，
// 于是纸面自动交易跑再多，「今日动态仓位」卡恒空——灰度期最需要对照的那本账，恰恰没有读数。
//
// 设计约束（改这里前先看这三条）：
//  1. **不改账本语义**：复用既有的 store.SignalDecision 与 signal_decisions 表，
//     只多一个 Channel="paper" 的取值。加列会牵动 schemaVersion（那是 W-05 的领地）。
//  2. **回调式注入，不引 store**：paper 包不依赖 store，由 registry 注入写库函数。
//     这样纸面引擎在纯内存场景（单测/无账本）下照样跑，onDecision 为 nil 即全链路 no-op。
//  3. **回调里绝不取 e.mu**：dynamicPaperQtyLocked 的调用方已持锁，回调内再取就是自锁（B3 用例盯这个）。

// DecisionRecord 一次纸面动态预算计算的快照。字段与实盘 dynBuyReadout 同构，
// 缺项一律留 0 并由 MissingKeyInput 标出——**不许**拿 0 冒充真读数。
type DecisionRecord struct {
	Day string // YYYYMMDD（cntime，与实盘日键同口径）
	// At 该快照的时刻。实盘侧这列是 d.At，两通道必须同义，否则「今日动态仓位」按时间排序时
	// 纸面行会全挤在最前（零值时间看起来像"最早发生"）。
	At          time.Time
	Code        string
	Strategy    string // poolKey 或战法显示名
	Stage       string // placed / skipped
	MarketScale float64
	StockScale  float64
	PerTradePct float64
	Total       float64 // 池权益
	Cash        float64 // 池现金
	Target      float64 // 目标金额
	CurStrat    float64 // 战略当前
	Room        float64 // 剩余空间
	// IndustryRoom / StockRoom / ClusterRoom 行业、单票、相关性簇三帽的剩余额度。
	//
	// §F-3-E5（2026-11-01）：这三个字段**此前从未被赋值**（恒 0），而 DB 表 signal_decisions 与
	// 前端「今日动态仓位」都按列读它们 ⇒ 面板上"行业/单票剩余"永远是 0，是**静默的假读数**。
	// 现改为从 ordbudget.Compute 的结果逐项透传（见 dynamicPaperQtyLocked）。
	// 簇帽仍会是 0：纸面当前没有把簇上限传进 Compute（行业/单票传了，簇没传），
	// 属"未接线"而非"没有限制"——这一点由 UI 的 missing_key_input 提示，不许当 0 解释成"无上限"。
	// English: these three were never assigned (always 0) while both the DB row and the UI read them
	// as real numbers. Now passed through from ordbudget.Compute; the cluster cap is still not wired
	// into the paper path, which is "not connected" rather than "no limit".
	IndustryRoom    float64
	StockRoom       float64
	ClusterRoom     float64
	CappedBy        string
	Reason          string
	MissingKeyInput string
	Qty             int
	Notional        float64
}

// SetDecisionRecorder 注入决策快照的记录器（nil = 关闭）。
// 与既有的 SetOnOpen/SetOnClose 同款回调风格：纸面引擎只负责"算出什么就播什么"，
// 落库是调用方（registry）的事。
func (e *Engine) SetDecisionRecorder(fn func(DecisionRecord)) {
	e.mu.Lock()
	e.onDecision = fn
	e.mu.Unlock()
}
