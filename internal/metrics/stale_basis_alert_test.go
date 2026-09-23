// stale_basis_alert_test.go §ADJ-BASIS-2（2026-09-23）：复权基线失效战法的告警出口回归。
// 三把锁：①规则已注册且是 p1（注入方据此走 notify.LevelHigh 高优通道，见 cmd/quant/main.go 的
// SetAlertSink——必须经 Push，不直调 PushGateway，否则复犯 M8 双发）②路由表显式列了它（漏列 = 悄悄
// 走默认分支，正是 §高-3 要消灭的形态）③计数 0→N 只推一条 alert：持续失效期间每个轮询节拍不刷屏，
// 归零才成对销案。
// English: regression for the stale-adjustment-basis alert: p1 rule registered + explicitly routed,
// fires exactly once on 0→N, and only resolves back to zero.
package metrics

import (
	"strings"
	"testing"
)

const staleBasisGauge = "applied_factor_stale_basis_count"

// staleRule 从默认规则表里取基线失效那条。
func staleRule(t *testing.T) AlertRule {
	t.Helper()
	for _, r := range DefaultAlertRules() {
		if r.Metric == staleBasisGauge {
			return r
		}
	}
	t.Fatalf("默认规则表里没有基线失效规则（metric=%s）——它会变成永不触发的死规则", staleBasisGauge)
	return AlertRule{}
}

// TestStaleBasisRuleIsP1AndRouted 规则必须是 p1 且在路由表里显式列为必推。
func TestStaleBasisRuleIsP1AndRouted(t *testing.T) {
	r := staleRule(t)
	if r.Level != "p1" {
		t.Errorf("基线失效应走 p1（前端 SetAlertSink 把 p1 映射成 notify.LevelHigh），got %q", r.Level)
	}
	if r.Op != "gt" || r.Threshold != 0 {
		t.Errorf("判据应为 >0（有条目失效就报），got %s%g", opText(r.Op), r.Threshold)
	}
	rt, ok := DefaultAlertRouting().Routes[r.Name]
	if !ok {
		t.Fatalf("规则 %s 未在路由表中显式列出", r.Name)
	}
	if rt != RoutePush {
		t.Errorf("基线失效应必推（RoutePush），got %s", rt)
	}
}

// TestStaleBasisAlertFiresOnceOnZeroToN 计数 0→N 只推一条，持续失效不刷屏，归零成对销案。
func TestStaleBasisAlertFiresOnceOnZeroToN(t *testing.T) {
	_, restore := captureLog(t)
	oldAlerter, oldRouter := globalAlerter, globalAlertRouter
	t.Cleanup(func() {
		SetAlertSink(nil)
		SetGauge(staleBasisGauge, 0)
		globalAlerter, globalAlertRouter = oldAlerter, oldRouter
		restore()
	})
	globalAlerter, globalAlertRouter = NewAlerter(), newAlertRouter(nil)
	var got []AlertDelivery
	SetAlertSink(func(d AlertDelivery) { got = append(got, d) })
	rule := staleRule(t)

	// 起点：无失效条目 → 不该有 alert。
	SetGauge(staleBasisGauge, 0)
	RunAlertEvaluation()
	if n := countKind(got, rule.Name, KindAlert); n != 0 {
		t.Fatalf("计数为 0 时不该告警，got %d", n)
	}
	// 0→2（§ADJ 修复后旧库条目第一次被读出来）：恰好一条 alert。
	SetGauge(staleBasisGauge, 2)
	RunAlertEvaluation()
	if n := countKind(got, rule.Name, KindAlert); n != 1 {
		t.Fatalf("0→N 应推 1 条 alert，got %d: %+v", n, got)
	}
	if got2 := findDelivery(got, rule.Name, KindAlert); got2 == nil || !strings.Contains(got2.Body, staleBasisGauge) {
		t.Fatalf("alert 正文应带上游指标名便于定位，got %+v", got2)
	}
	// 战法库每个轮询节拍都会重新读库并写同名量规（值不变）→ 评估器去重 + 路由冷却窗双保险，
	// 连跑 5 轮也只剩那一条。
	for i := 0; i < 5; i++ {
		SetGauge(staleBasisGauge, 2)
		RunAlertEvaluation()
	}
	if n := countKind(got, rule.Name, KindAlert); n != 1 {
		t.Fatalf("持续失效期间不得每节拍刷一条，got %d", n)
	}
	// 归零（旧战法删除或重跑寻优后重新盖章）→ 成对销案。
	SetGauge(staleBasisGauge, 0)
	RunAlertEvaluation()
	if n := countKind(got, rule.Name, KindResolved); n != 1 {
		t.Fatalf("归零应补 1 条 resolved，got %d: %+v", n, got)
	}
}

// findDelivery 取某规则某种类的首条投递。
func findDelivery(ds []AlertDelivery, rule, kind string) *AlertDelivery {
	for i := range ds {
		if ds[i].Rule == rule && ds[i].Kind == kind {
			return &ds[i]
		}
	}
	return nil
}
