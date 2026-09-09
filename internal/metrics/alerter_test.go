package metrics

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestAlerterFireAndRecover 越界持续满 For 才 fire；回落触发 recover；未满窗口不 fire。
func TestAlerterFireAndRecover(t *testing.T) {
	a := NewAlerter()
	base := time.Now()
	a.now = func() time.Time { return base }

	rules := []AlertRule{
		{Name: "quote_stale", Metric: "quote_staleness_sec", Op: "gt", Threshold: 60, For: "60s", Level: "p2", Message: "报价陈旧"},
	}
	vals := map[string]int64{"quote_staleness_sec": 0}

	// 未越界
	if evs := a.Evaluate(rules, vals); len(evs) != 0 {
		t.Fatalf("未越界不应触发: %v", evs)
	}
	// 越界但不足 60s
	a.now = func() time.Time { return base.Add(30 * time.Second) }
	vals["quote_staleness_sec"] = 100
	if evs := a.Evaluate(rules, vals); len(evs) != 0 {
		t.Fatalf("持续不足 For 不应 fire: %v", evs)
	}
	// 满 60s → fire
	a.now = func() time.Time { return base.Add(61 * time.Second) }
	evs := a.Evaluate(rules, vals)
	if len(evs) != 1 || evs[0].Kind != "fire" {
		t.Fatalf("应 fire 一次: %v", evs)
	}
	// 去重：继续越界不再重复
	if evs := a.Evaluate(rules, vals); len(evs) != 0 {
		t.Fatalf("已 fire 不应重复: %v", evs)
	}
	// 回落 → recover
	vals["quote_staleness_sec"] = 0
	evs = a.Evaluate(rules, vals)
	if len(evs) != 1 || evs[0].Kind != "recover" {
		t.Fatalf("回落应 recover: %v", evs)
	}
}

// TestAlerterImmediateZeroFor For=0 立即 fire。
func TestAlerterImmediateZeroFor(t *testing.T) {
	a := NewAlerter()
	rules := []AlertRule{
		{Name: "breaker_open", Metric: "breaker_active", Op: "gt", Threshold: 0, For: "0s", Level: "p1", Message: "熔断"},
	}
	if evs := a.Evaluate(rules, map[string]int64{"breaker_active": 1}); len(evs) != 1 || evs[0].Kind != "fire" {
		t.Fatalf("For=0 应立即 fire: %v", evs)
	}
}

// TestPrometheusExposition promtool 兼容文本：含类型行/帮助行、指标名合法。
func TestPrometheusExposition(t *testing.T) {
	s := PrometheusExposition()
	if !strings.Contains(s, "# TYPE quant_orders_placed_total counter") {
		t.Errorf("缺少 counter 类型行:\n%s", s)
	}
	if !strings.Contains(s, "quant_orders_placed_total 0") {
		t.Errorf("缺少计数值:\n%s", s)
	}
	// 量规
	SetGauge("breaker_active", 1)
	SetGauge("quote_staleness_sec", 42)
	s = PrometheusExposition()
	if !strings.Contains(s, "# TYPE quant_gauge_breaker_active gauge") ||
		!strings.Contains(s, "quant_gauge_breaker_active 1") {
		t.Errorf("缺少量规行: %s", s)
	}
	if !strings.Contains(s, "quant_gauge_quote_staleness_sec 42") {
		t.Errorf("缺少陈旧度量规:\n%s", s)
	}
}

// TestSLOCompute 可用性/延迟 p95/LLM 成功率。
func TestSLOCompute(t *testing.T) {
	in := SLOInput{
		UptimeSec:      39600, // 11 小时在线
		SessionSec:     39600,
		OrderLatencyMs: []float64{100, 200, 300, 400, 500, 600, 700, 800, 900, 1000},
		LLMOk:          95,
		LLMFail:        5,
	}
	r := SLOCompute(in)
	if r.AvailabilityPct != 100 || !r.SLOMet {
		t.Errorf("满时段在线可用性应 100 达成: %.2f met=%v", r.AvailabilityPct, r.SLOMet)
	}
	if r.OrderLatencyP95Ms != 900 {
		t.Errorf("p95 应 900（floor((n-1)*0.95) 下标=8）, got %.0f", r.OrderLatencyP95Ms)
	}
	if r.OrderLatencyP50Ms != 500 {
		t.Errorf("p50 应 500, got %.0f", r.OrderLatencyP50Ms)
	}
	if r.LLMSuccessRatePct != 95 {
		t.Errorf("LLM 成功率应 95, got %.1f", r.LLMSuccessRatePct)
	}
	// 可用性不达标 → SLOMet=false
	in.UptimeSec = 39000
	r = SLOCompute(in)
	if r.SLOMet {
		t.Errorf("98.5 可用性应未达成")
	}
}

// TestWriteSLODaily 落盘 slo_daily.json 且含 opslog 记录（无 panic）。
func TestWriteSLODaily(t *testing.T) {
	dir := t.TempDir()
	r := SLOCompute(SLOInput{UptimeSec: 39600, SessionSec: 39600, LLMOk: 10, LLMFail: 0})
	if err := WriteSLODaily(dir, r); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "slo_daily.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"availability_pct"`) || !strings.Contains(string(b), `"slo_met"`) {
		t.Errorf("slo_daily.json 结构不符: %s", b)
	}
}
