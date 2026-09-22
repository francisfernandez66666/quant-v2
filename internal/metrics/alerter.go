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
	events := globalAlerter.Evaluate(DefaultAlertRules(), gaugeSnapshot())
	for _, d := range globalAlertRouter.Route(events) {
		alertOutput.emit(d)
	}
}
