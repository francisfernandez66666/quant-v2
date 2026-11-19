// sell_undelivered_alert_test.go — §SELL-UNDELIVERED（2026-11-19，第 25 条）规则层回归锁。
//
// 这条规则的存在理由就是"不能被静默"：通道断的那天，order_fail_rate 会被折进日汇总
// （2026-11-17 现场：引擎发了 6 笔卖出全部 发送失败，08:35 响过一次就沉了）。
// 所以这里钉三件与"能不能叫醒人"直接相关的事：
//   - 规则五元组（gt 0 / For 0s / p1）——阈值放高或给宽限，等于把裸敞口再开几分钟；
//   - 路由必须是 RoutePush，且 QuietAlertRules 白名单里**不许**出现它；
//   - 文案必须给出"先看哪里"的处置方向（live_ready / sell_blind / 人工处置），
//     否则半夜被叫醒的人不知道该先动哪一下。
package metrics

import (
	"strings"
	"testing"
	"time"
)

// sellUndeliveredGauge 与 engine/heartbeat.go 的写端逐字同名。
const sellUndeliveredGauge = "sell_undelivered_today"

// sellUndeliveredRule 从默认规则表里取这条规则；取不到直接判红（少一条＝通道断时无人被叫醒）。
func sellUndeliveredRule(t *testing.T) AlertRule {
	t.Helper()
	for _, r := range DefaultAlertRules() {
		if r.Name == "sell_undelivered" {
			return r
		}
	}
	t.Fatalf("默认规则表里没有 sell_undelivered（第 25 条）——通道断的那天没人会被叫醒")
	return AlertRule{}
}

// TestSellUndeliveredRuleShape 五元组 + 键名同源 + 文案处置方向。
func TestSellUndeliveredRuleShape(t *testing.T) {
	r := sellUndeliveredRule(t)
	if r.Metric != sellUndeliveredGauge {
		t.Errorf("规则读的键名是 %q，写端是 %q（打错就是一条永不触发的死规则）", r.Metric, sellUndeliveredGauge)
	}
	if r.Op != "gt" || r.Threshold != 0 {
		t.Errorf("判据应为 gt 0（正确状态只有一个值＝0），got %s %v", opText(r.Op), r.Threshold)
	}
	if r.For != "0s" {
		t.Errorf("For 应为 0s：这是已经落库的事实不是取数抖动，宽限只会让敞口多开，got %q", r.For)
	}
	if r.Level != "p1" {
		t.Errorf("Level 应为 p1（止损没执行属必推），got %q", r.Level)
	}
	for _, needle := range []string{"live_ready", "sell_blind", "人工处置"} {
		if !strings.Contains(r.Message, needle) {
			t.Fatalf("文案缺少处置方向 %q（被叫醒的人要知道先看哪一处）：%s", needle, r.Message)
		}
	}
}

// TestSellUndeliveredRoutedPushAndNeverQuieted 显式必推 + 不进静默域（这条规则的全部意义）。
func TestSellUndeliveredRoutedPushAndNeverQuieted(t *testing.T) {
	cfg := DefaultAlertRouting()
	rt, ok := cfg.Routes["sell_undelivered"]
	if !ok {
		t.Fatalf("路由表没有显式列出 sell_undelivered（会掉进 DefaultRoute，改默认路由时被悄悄挪道）")
	}
	if rt != RoutePush {
		t.Fatalf("sell_undelivered 必须是 RoutePush，got %v", rt)
	}
	// 负锁：静默域白名单里出现这个名字＝这条规则退回"响了也发不出去"。
	// QuietAlertRules 是写入口（无返回值），要核对只能走只读的 QuietAlertRuleNames；
	// 用完把它清回空集，别让本用例污染同包其它用例的静默域读数。
	QuietAlertRules([]string{"uplink_stale", "order_fail_rate"})
	for _, name := range QuietAlertRuleNames() {
		if name == "sell_undelivered" {
			t.Fatalf("sell_undelivered 被放进了静默域：通道断的那天它就被静音了，而这正是它唯一的存在理由")
		}
	}
	QuietAlertRules(nil)
}

// TestSellUndeliveredFiresImmediately 行为腿：值=1 在 For=0s 下首轮即触发；回 0 成对销案。
func TestSellUndeliveredFiresImmediately(t *testing.T) {
	a := NewAlerter()
	now := time.Date(2026, 11, 19, 9, 31, 0, 0, time.UTC)
	a.now = func() time.Time { return now }
	rule := sellUndeliveredRule(t)
	rules := []AlertRule{rule}

	ev := a.Evaluate(rules, map[string]int64{sellUndeliveredGauge: 1})
	if len(ev) != 1 || ev[0].Name != "sell_undelivered" || ev[0].Kind != "fire" {
		t.Fatalf("值=1 应首轮即 fire，got %+v", ev)
	}
	// 同值第二轮不重复推（Firing 态去重）。
	if ev := a.Evaluate(rules, map[string]int64{sellUndeliveredGauge: 1}); len(ev) != 0 {
		t.Fatalf("Firing 态不应重复推送，got %+v", ev)
	}
	now = now.Add(31 * time.Minute) // 越过触发冷却窗
	ev = a.Evaluate(rules, map[string]int64{sellUndeliveredGauge: 0})
	if len(ev) != 1 || ev[0].Kind != "recover" {
		t.Fatalf("回 0 应成对销案，got %+v", ev)
	}
}
