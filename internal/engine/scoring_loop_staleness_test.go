// scoring_loop_staleness_test.go — §UPDLINK（2026-09-22 H-4 / 审计 N-1）新鲜度量规喂养用例。
// 职责：锁死"告警规则必须有真实数据源"与"未知态一律写 0"两条口径——
// quote_stale 规则此前在全仓找不到对应 SetGauge（死规则），而 H-4 的上行停摆根本无规则可触发。
// English: §UPDLINK tests — the freshness gauges the alert rules consume must actually be written,
// and every unknown/not-applicable state must land on 0 (which never trips a gt rule).
package engine

import (
	"testing"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/llm"
	"quant-trading-v2/internal/metrics"
	"quant-trading-v2/internal/trading"
)

// TestRefreshStalenessGaugesUnknownIsZero 未接行情采集器、未接实盘控制器时两条量规都必须为 0：
// 未知既不能伪造"新鲜"（写 -1 会让规则永远看不到问题），也不能伪造"陈旧"（写巨大值每晚误报）。
func TestRefreshStalenessGaugesUnknownIsZero(t *testing.T) {
	e := &Engine{}
	e.refreshStalenessGauges()
	if v, ok := metrics.GetGauge("quote_staleness_sec"); !ok || v < 0 {
		t.Fatalf("未配置采集器时 quote_staleness_sec 应为 0（未知），got %d ok=%v", v, ok)
	}
	if v, ok := metrics.GetGauge("uplink_staleness_sec"); !ok || v != 0 {
		t.Fatalf("未接实盘控制器时 uplink_staleness_sec 应为 0，got %d ok=%v", v, ok)
	}
}

// TestRefreshStalenessGaugesUplinkFreshAndNeverReported 实盘已开但从未收到回报 → 0（无基线不判陈旧）；
// 刚收到回报 → 0（新鲜）。两者都不该让 uplink_stale 误触发。
func TestRefreshStalenessGaugesUplinkFreshAndNeverReported(t *testing.T) {
	c := trading.NewController(nil, nil, "u_1", config.QMTConfig{Enabled: true}, nil)
	e := &Engine{}
	e.mu.Lock()
	e.qmtCtrl = c
	e.mu.Unlock()

	e.refreshStalenessGauges()
	if v := mustGauge(t, "uplink_staleness_sec"); v != 0 {
		t.Fatalf("从未上报（零值时间）应写 0=未知，got %d", v)
	}
	c.SetLastReport("heartbeat")
	e.refreshStalenessGauges()
	if v := mustGauge(t, "uplink_staleness_sec"); v != 0 {
		t.Fatalf("刚收到回报应判新鲜（0），got %d", v)
	}
}

// TestUplinkStaleRuleRegistered H-4 的替代品：默认规则里必须有 uplink_stale（p1），
// 且它读的键与 refreshStalenessGauges 写的键同名——键名打错就是又一次"规则恒不触发"。
func TestUplinkStaleRuleRegistered(t *testing.T) {
	var uplink, skipped bool
	for _, r := range metrics.DefaultAlertRules() {
		switch r.Name {
		case "uplink_stale":
			uplink = r.Metric == "uplink_staleness_sec" && r.Level == "p1"
		case "sse_broadcast_skipped":
			skipped = r.Metric == "sse_broadcast_skipped_total"
		}
	}
	if !uplink {
		t.Fatalf("默认规则缺少可用的 uplink_stale（指标名/等级须为 p1 + uplink_staleness_sec）")
	}
	if !skipped {
		t.Fatalf("默认规则缺少 sse_broadcast_skipped")
	}
}

// TestRefreshFeedsLLMCooldownGauge §DEADGAUGE（2026-09-23 傍晚批收尾）：规则 llm_cooldown 的
// 量规 llm_cooldown_count 此前全仓无赋值点（第三次踩中"有规则、无数据源"形态）。现由打分链每轮
// 喂养，本用例锁两件事：①写的键名与规则读的键名逐字相同（打错键名 = 又一条永不触发的规则）；
// ②未接 LLM 客户端 / 池内无失败一律 0（不适用不伪造，和上面两条新鲜度量规同一口径）。
func TestRefreshFeedsLLMCooldownGauge(t *testing.T) {
	var metric string
	for _, r := range metrics.DefaultAlertRules() {
		if r.Name == "llm_cooldown" {
			metric = r.Metric
		}
	}
	if metric != "llm_cooldown_count" {
		t.Fatalf("规则 llm_cooldown 读的键名变了：%q（写端在 refreshStalenessGauges）", metric)
	}

	e := &Engine{}
	e.refreshStalenessGauges()
	if v := mustGauge(t, "llm_cooldown_count"); v != 0 {
		t.Fatalf("未接 LLM 客户端时应写 0, got %d", v)
	}
	// 直接置字段而非 e.SetLLMClient：后者会顺带把客户端转交给 newsAgent，裸 &Engine{} 下该字段为 nil
	// （与本文件上面那条用例给 qmtCtrl 加锁赋值的写法同源）。
	e.mu.Lock()
	e.llmClient = llm.New(llm.Config{APIKeys: []string{"A", "B"}})
	e.mu.Unlock()
	e.refreshStalenessGauges()
	if v := mustGauge(t, "llm_cooldown_count"); v != 0 {
		t.Fatalf("多 key 池无失败时应为 0, got %d", v)
	}
}

// mustGauge 读取量规值（缺失即失败）。
func mustGauge(t *testing.T, name string) int64 {
	t.Helper()
	v, ok := metrics.GetGauge(name)
	if !ok {
		t.Fatalf("量规 %s 未注册", name)
	}
	return v
}
