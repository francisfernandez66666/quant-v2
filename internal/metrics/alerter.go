// alerter.go 阈值告警（§WS-L 维5）：对量规/计数器做规则评估，支持
// 「持续 for 时长才触发」（防毛刺）、去重（触发后不重复推送）、恢复通知。
// 复用 opslog 留痕；评估器为纯函数可单测，scoreCycle 周期性调用。
// §高-3（2026-09-22 傍晚批）：评估出的事件不再只落日志——出站路由/限频/日汇总见
// alert_routing.go（同包），本文件保持"纯评估器"职责。
//
// English: threshold alerting (WS-L 维5). Rules over gauges/counters with a "sustained for"
// state machine (noise-free), dedup (fire once), and recovery notifications. Pure evaluator for
// tests; the engine score cycle invokes it periodically. Egress/routing lives in alert_routing.go.
package metrics

import (
	"fmt"
	"time"

	"quant-trading-v2/internal/opslog"
)

// AlertRule 一条阈值告警规则。
// English: one threshold alert rule.
type AlertRule struct {
	Name      string  `json:"name"`      // 规则名（唯一，状态键）
	Metric    string  `json:"metric"`    // 指标名（gauge 名）
	Op        string  `json:"op"`        // gt | ge | lt | le
	Threshold float64 `json:"threshold"` // 阈值
	For       string  `json:"for"`       // 持续触发时长（如 "60s"），空=立即
	Level     string  `json:"level"`     // p1 | p2（推送等级）
	Message   string  `json:"message"`   // 告警文案
}

// DefaultAlertRules 出厂默认规则（WS-L）。
// English: factory-default alert rules.
func DefaultAlertRules() []AlertRule {
	return []AlertRule{
		{Name: "breaker_open", Metric: "breaker_active", Op: "gt", Threshold: 0, For: "0s", Level: "p1", Message: "实盘网关熔断中"},
		// §DEADGAUGE（2026-09-23 傍晚批收尾）：下面三条规则 09-15 注册时都只有规则没有数据源
		// （全仓无 SetGauge 赋值点 = 永不触发，audit N-1 同族），现各自接上真实来源：
		//   order_fail_rate_milli ← 本包 order_rate.go（§R4-9 累计计数器做 5 分钟窗增量换算）
		//   settlement_diff_count ← trading/settlement.go（三方对账三类差异条数之和）
		//   llm_cooldown_count    ← engine/scoring_loop.go（llm.Client.KeysInCooldown，与 pickKey 同判据）
		{Name: "order_fail_rate", Metric: "order_fail_rate_milli", Op: "gt", Threshold: 50, For: "300s", Level: "p1", Message: "下单失败率 >5%（连续5分钟）"},
		{Name: "quote_stale", Metric: "quote_staleness_sec", Op: "gt", Threshold: 60, For: "60s", Level: "p2", Message: "行情报价陈旧 >60s"},
		// §UPDLINK（2026-09-22 H-4）：网关→引擎上行回报链停摆。网关心跳 60s 一发，连续 5 个周期
		// 无入账即为异常（生产实录：SSE 广播锁被 double-close panic 永久占用，回报挂死 1h45m、
		// 实盘账冻结，而引擎日志看起来完全正常——这条规则就是把那段静默期变成 p1 告警）。
		{Name: "uplink_stale", Metric: "uplink_staleness_sec", Op: "gt", Threshold: 300, For: "120s", Level: "p1", Message: "网关上行回报停摆 >5 分钟（实盘账不再更新，查 SSE/回报端点）"},
		// §UPDLINK 兜底自监控：有界广播放弃推送 = SSE 侧存在持锁阻塞（正常持锁仅微秒级，
		// 一次超预算即异常，不等第二十五次）。
		{Name: "sse_broadcast_skipped", Metric: "sse_broadcast_skipped_total", Op: "gt", Threshold: 0, For: "0s", Level: "p1", Message: "SSE 广播锁超预算丢推送（上行回报入口曾被堵住的同族形态）"},
		{Name: "settlement_diff", Metric: "settlement_diff_count", Op: "gt", Threshold: 0, For: "0s", Level: "p1", Message: "交割单对账出现差异"},
		// §D4（2026-09-22 PM 修复批）：日终结算失败当日会按 10 分钟节流自动重试，本规则盯"当日
		// 连续失败次数"（settlement.go 失败抬升、成功归零，是真实赋值不是 N-1 那种死规则）。
		// 触发即说明三方对账这道日终安全网今天到目前为止没跑成——差异/漏单不会被发现。
		{Name: "settle_failed", Metric: "settle_fail_streak", Op: "gt", Threshold: 0, For: "0s", Level: "p2", Message: "交割单三方对账失败（当日自动重试中，成功后自动恢复）"},
		{Name: "llm_cooldown", Metric: "llm_cooldown_count", Op: "gt", Threshold: 2, For: "60s", Level: "p2", Message: "LLM 冷却数超阈值"},
		// §AUDIT-PM 2026-09-15 buyCh 深度预警：容量 64，过半仍在排 = 下单风暴或网关变慢（P2）。
		{Name: "buy_queue_high", Metric: "buy_queue_depth", Op: "gt", Threshold: 32, For: "60s", Level: "p2", Message: "自动买入队列积压 >32（网关变慢或信号风暴）"},
		// §ADJ-BASIS-2（2026-09-23）：已应用因子战法的复权口径基线失效条数 >0 即 p1。
		// 赋值点：internal/research/apply.go markStaleAdjBasis（每次读战法库都刷新，含"没有库"清零）。
		// 语义是"这些战法的 weights/buy_threshold 是在 §ADJ 修正前的复权面板上拟合的，历史依据已失效"，
		// 缺省只告警不停投（处置权在 owner），持续触发由评估器 Firing 态 + 路由冷却窗去重，
		// 每个轮询节拍不会再刷一条。
		{Name: "applied_factor_stale_basis", Metric: "applied_factor_stale_basis_count", Op: "gt", Threshold: 0, For: "0s", Level: "p1", Message: "已应用因子战法复权基线已失效（参数缺历史依据，需重跑寻优+审批）"},
		// §ADJ-BASIS-2P（2026-09-23）：形态战法同一条链的第二侧。刻意**不复用**上面那条指标——
		// 因子库与形态库由不同调用点各自读库，共用一个 gauge 就会"后读者覆盖前读者"，
		// 报出来的条数不是任何一侧的真值（§DEADGAUGE 的反面形态：值在、但失真）。
		// 赋值点：internal/research/apply.go markStaleAdjBasisPatterns（含"没有库"清零）。
		{Name: "applied_pattern_stale_basis", Metric: "applied_pattern_stale_basis_count", Op: "gt", Threshold: 0, For: "0s", Level: "p1", Message: "已应用形态战法复权基线已失效（条件阈值缺历史依据，需重跑寻优+审批）"},
		// §CAL-GATE（2026-09-25 D-25-1）：交易日历未加载即告警——时段判据的缺省方向是 fail-open
		// （日历没加载时法定节假日按周末口径当交易日），这方向是刻意的（宁可多跑不漏跑真交易日），
		// 但"今天其实在 fail-open"必须说出来：D-25-1 的实录就是中秋休市日被整天当盘中出信号。
		// 赋值点：engine/scoring_loop.go refreshStalenessGauges（每 30s、会话门禁之前，休市日也在喂）。
		// For=300s：给启动后首个 API 刷新/磁盘缓存读取留重试余量；触发即 p1——引擎对"休市"失明期间
		// 信号、实盘建议、熔断健康判定全部按盘中口径跑。
		{Name: "trading_calendar_not_loaded", Metric: "trading_calendar_loaded", Op: "lt", Threshold: 1, For: "300s", Level: "p1", Message: "交易日历未加载：法定节假日正被当交易日（fail-open），休市日会照常出信号，查 hithink 日历接口/磁盘缓存"},
		// §0925EVE-A2（2026-09-25）：kill-switch 按下后有委托撤不掉。撤单失败的单仍挂在网关侧，
		// 而操作者界面若只显示成功数就是「降级报成功」——紧急停止语义下这是资损面，p1 不降级。
		// 赋值点：trading/controller.go HaltAll（每轮以失败笔数覆写，全成功写 0 供 resolved 销案）。
		// For=0s：HaltAll 是人工动作触发的瞬时批量结果，失败即事实成立，不存在"毛刺需要持续观察"。
		// 接法与上面 trading_calendar_not_loaded 同款：量规 + 规则 + 路由表条目，不引入新机制。
		{Name: "halt_cancel_failed", Metric: "halt_cancel_fail_count", Op: "gt", Threshold: 0, For: "0s", Level: "p1", Message: "kill-switch 批量撤单存在失败：紧急停止下仍有在途未成交委托挂网，按 /api/qmt/halt 响应 failed 明细逐单人工处置"},
		// §0925EVE-C1（2026-09-25）：实盘腿战法库闸（§95/LIB-GATE 回放侧判红的对偶）。两条规则、两份
		// 文案严格分家——"读库失败"与"库里真没启用规则"是两种处置（查文件可读性 vs 查启用状态），
		// 不许混成一条。赋值点：engine/registry.go gateLiveStrategyLibrary（每次账号引擎装配刷新）。
		// 量规缺省（进程还没装配过引擎）读到 0，恰好落"不触发"侧——不会把"没人登录"误报成"库挂了"。
		// fail-close 语义=当轮不出新建议（runner 置空），不熔断资金：与 §CB 防误熔同一方向。
		// For=0s：装配事件是离散事实（这一次读失败/零条就是发生了），不存在需要持续观察的毛刺。
		{Name: "live_strategy_library_not_loaded", Metric: "live_strategy_library_load_errors", Op: "gt", Threshold: 0, For: "0s", Level: "p1", Message: "实盘战法库读取失败（当轮已 fail-close 不出新建议，未熔断资金）：applied_factors.json/applied_patterns.json 存在但不可读/损坏，查数据目录挂载与文件权限——这是读库故障，不是库里没规则"},
		{Name: "live_strategy_no_enabled_rules", Metric: "live_strategy_no_enabled", Op: "gt", Threshold: 0, For: "0s", Level: "p1", Message: "实盘战法库读取成功但零条启用规则（当轮已 fail-close 不出新建议，未熔断资金）：库里真没有启用的因子/形态战法（缺失/为空/全停用），查战法库启用状态——这不是读库故障"},
	}
}

// AlertState 单条规则在评估器内的状态。
// English: per-rule state inside the evaluator.
type AlertState struct {
	Rule    AlertRule
	Since   time.Time // 连续低于/高于阈值窗口起点
	Firing  bool      // 当前是否处于触发态（已推送）
	FiredAt time.Time
}

// Alerter 进程内告警评估器。
// English: in-process alert evaluator.
type Alerter struct {
	states map[string]*AlertState
	now    func() time.Time
}

// NewAlerter 创建告警评估器。
func NewAlerter() *Alerter {
	return &Alerter{states: map[string]*AlertState{}, now: time.Now}
}

// Evaluate 对全部规则做一轮评估：输入规则表+当前量规值，输出本轮新触发与恢复的事件。
// 语义：指标越界持续满 For 时长才 fire；已 fire 后不再重复；回落恢复时发 recover。
// English: evaluates all rules against current gauge values, returning this round's fire/recover
// events. Fires only after the condition holds for the For duration; fires once; recovers on return.
func (a *Alerter) Evaluate(rules []AlertRule, values map[string]int64) []AlertEvent {
	if a.states == nil {
		a.states = map[string]*AlertState{}
	}
	if a.now == nil {
		a.now = time.Now
	}
	now := a.now()
	var out []AlertEvent
	for _, rule := range rules {
		val := float64(values[rule.Metric])
		below := !matchesOp(rule.Op, val, rule.Threshold)
		st := a.states[rule.Name]
		if st == nil {
			st = &AlertState{Rule: rule}
			a.states[rule.Name] = st
		}
		if below {
			st.Since = now
			if st.Firing {
				st.Firing = false
				opslog.Logf("quant", "告警恢复 [%s/%s] %s 值=%.2f", rule.Level, rule.Name, rule.Message, val)
				out = append(out, AlertEvent{Name: rule.Name, Level: rule.Level, Kind: "recover", Value: val, Message: rule.Message})
			}
			continue
		}
		if st.Since.IsZero() {
			st.Since = now
		}
		dur, _ := time.ParseDuration(rule.For)
		if !st.Firing && now.Sub(st.Since) >= dur {
			st.Firing = true
			st.FiredAt = now
			out = append(out, AlertEvent{Name: rule.Name, Level: rule.Level, Kind: "fire", Value: val, Message: rule.Message})
			opslog.Logf("quant", "告警触发 [%s/%s] %s 值=%.2f", rule.Level, rule.Name, rule.Message, val)
		}
	}
	return out
}

// matchesOp 数值比较。
func matchesOp(op string, v, t float64) bool {
	switch op {
	case "gt":
		return v > t
	case "ge":
		return v >= t
	case "lt":
		return v < t
	case "le":
		return v <= t
	}
	return false
}

// AlertEvent 一次告警事件（触发/恢复）。
// English: one alert event (fire/recover).
type AlertEvent struct {
	Name    string  `json:"name"`
	Level   string  `json:"level"`
	Kind    string  `json:"kind"` // fire | recover
	Value   float64 `json:"value"`
	Message string  `json:"message"`
}

// String 告警事件的单行可读格式（级别/类型/消息/触发值）。
func (e AlertEvent) String() string {
	return fmt.Sprintf("[%s/%s] %s 值=%.2f", e.Level, e.Kind, e.Message, e.Value)
}

// EvaluateNow 用当前注册量规对默认规则做一轮评估（供 scoreCycle 调用）。
// English: evaluates the default rules against the currently registered gauges (for the score cycle).
func EvaluateNow(a *Alerter) []AlertEvent {
	vals := gaugeSnapshot()
	f64 := map[string]int64{}
	for k, v := range vals {
		f64[k] = v
	}
	return a.Evaluate(DefaultAlertRules(), f64)
}

// globalAlerter 进程级共享告警评估器（scoreCycle 周期调用）。
var globalAlerter = NewAlerter()

// GlobalAlerter 返回进程级告警评估器。
// English: returns the process-wide alert evaluator.
func GlobalAlerter() *Alerter { return globalAlerter }

// RunAlertEvaluation 用当前量规跑一轮告警评估，并把事件按路由表送出站（供 scoreCycle 节流调用）。
// §高-3（2026-09-22 傍晚批）：这里以前只做 log.Printf——规则触发但事件从不出站（"有评估、无出口"
// 的静默失效）。现在必推类（熔断/降级）走注入的 AlertSink（带冷却限频 + alert/resolved 成对），
// 日汇总类当天聚合、跨日补发。签名保持不变：调用点是 engine 的 30s 无参节流调用。
// 即使本轮没有任何事件也要调 Route()：冷却窗后悬置的销案、以及跨日的日汇总都靠这个节拍放行。
// English: runs one evaluation round and routes the resulting events out (push-class rules go to
// the injected AlertSink with cooldown + paired alert/resolved; the rest are aggregated daily).
// Signature is unchanged — the engine calls it with no arguments every ~30s.
func RunAlertEvaluation() {
	// §DEADGAUGE（2026-09-23）：派生量规先于评估刷新。order_fail_rate_milli 的数据源是本包内的
	// 累计计数器（§R4-9），由评估节拍换算成窗口失败率——不先刷新一轮，快照里永远是上一轮的值。
	refreshOrderFailRateGauge()
	events := globalAlerter.Evaluate(DefaultAlertRules(), gaugeSnapshot())
	for _, d := range globalAlertRouter.Route(events) {
		alertOutput.emit(d)
	}
}
