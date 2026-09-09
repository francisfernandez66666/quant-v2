// alerter.go 阈值告警（§WS-L 维5）：对量规/计数器做规则评估，支持
// 「持续 for 时长才触发」（防毛刺）、去重（触发后不重复推送）、恢复通知。
// 复用 opslog/通知链路；评估器为纯函数可单测，scoreCycle 周期性调用。
//
// English: threshold alerting (WS-L 维5). Rules over gauges/counters with a "sustained for"
// state machine (noise-free), dedup (fire once), and recovery notifications. Pure evaluator for
// tests; the engine score cycle invokes it periodically.
package metrics

import (
	"fmt"
	"log"
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
		{Name: "settlement_diff", Metric: "settlement_diff_count", Op: "gt", Threshold: 0, For: "0s", Level: "p1", Message: "交割单对账出现差异"},
		{Name: "llm_cooldown", Metric: "llm_cooldown_count", Op: "gt", Threshold: 2, For: "60s", Level: "p2", Message: "LLM 冷却数超阈值"},
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

// RunAlertEvaluation 用当前量规跑一轮告警评估并把事件打到标准日志（供 scoreCycle 节流调用）。
// English: runs one alert-evaluation round against registered gauges and logs events (throttled by
// the score cycle).
func RunAlertEvaluation() {
	for _, e := range globalAlerter.Evaluate(DefaultAlertRules(), gaugeSnapshot()) {
		log.Printf("[metrics] 告警事件 %s", e)
	}
}
