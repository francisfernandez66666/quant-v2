// Package server HTTP API 服务器：为前端/网关提供 REST 接口、SSE 推送、量化研究、模拟盘、QMT 回报等路由。
package server

import (
	"testing"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/data"
)

// fixSignal helper 生成一条做多/做空信号。
func mkSig(code, dir string) combat_agent.Signal {
	return combat_agent.Signal{Code: code, Name: "测试", Direction: dir, Action: "buy", Confidence: 1.0}
}

// quote 构造测试行情快照。
func quote(chg float64) *data.StockInfo { return &data.StockInfo{Price: 10, ChangePct: chg} }

// TestFilterStaleSignals_LongRed 做多信号当日转绿 → 从当前信号展示中撤下。
func TestFilterStaleSignals_LongRed(t *testing.T) {
	quotes := map[string]*data.StockInfo{"001376": quote(-0.08)}
	live, pruned := filterStaleSignals([]combat_agent.Signal{mkSig("001376", "做多")}, quotes)
	if pruned != 1 || len(live) != 0 {
		t.Fatalf("做多信号当日 -0.08%% 应撤下, got live=%d pruned=%d", len(live), pruned)
	}
}

// TestFilterStaleSignals_LongGreen 做多信号当日仍为红盘 → 保留。
func TestFilterStaleSignals_LongGreen(t *testing.T) {
	quotes := map[string]*data.StockInfo{"600000": quote(1.5)}
	live, pruned := filterStaleSignals([]combat_agent.Signal{mkSig("600000", "做多")}, quotes)
	if pruned != 0 || len(live) != 1 {
		t.Fatalf("做多信号当日 +1.5%% 应保留, got live=%d pruned=%d", len(live), pruned)
	}
}

// TestFilterStaleSignals_ShortGreen 做空信号当日转红 → 撤下。
func TestFilterStaleSignals_ShortGreen(t *testing.T) {
	quotes := map[string]*data.StockInfo{"600000": quote(0.5)}
	live, pruned := filterStaleSignals([]combat_agent.Signal{mkSig("600000", "做空")}, quotes)
	if pruned != 1 || len(live) != 0 {
		t.Fatalf("做空信号当日转红应撤下, got live=%d pruned=%d", len(live), pruned)
	}
}

// TestFilterStaleSignals_ShortFalling 做空信号当日下跌 → 保留（对做空是顺势）。
func TestFilterStaleSignals_ShortFalling(t *testing.T) {
	quotes := map[string]*data.StockInfo{"600000": quote(-3.2)}
	live, pruned := filterStaleSignals([]combat_agent.Signal{mkSig("600000", "做空")}, quotes)
	if pruned != 0 || len(live) != 1 {
		t.Fatalf("做空信号当日下跌应保留, got live=%d pruned=%d", len(live), pruned)
	}
}

// TestFilterStaleSignals_NoQuote 行情缺失 → fail-open 保留信号。
func TestFilterStaleSignals_NoQuote(t *testing.T) {
	live, pruned := filterStaleSignals([]combat_agent.Signal{mkSig("600000", "做多")}, nil)
	if pruned != 0 || len(live) != 1 {
		t.Fatalf("行情缺失应 fail-open 保留, got live=%d pruned=%d", len(live), pruned)
	}
}

// TestFilterStaleSignals_Mixed 混合清单只撤下失效项。
func TestFilterStaleSignals_Mixed(t *testing.T) {
	sigs := []combat_agent.Signal{
		mkSig("001376", "做多"), // 当日 -0.08% → 撤
		mkSig("600000", "做多"), // 当日 +1.5% → 留
		mkSig("300175", "做多"), // 无行情 → 留
	}
	quotes := map[string]*data.StockInfo{
		"001376": quote(-0.08),
		"600000": quote(1.5),
	}
	live, pruned := filterStaleSignals(sigs, quotes)
	if pruned != 1 || len(live) != 2 {
		t.Fatalf("应撤 1 留 2, got live=%d pruned=%d", len(live), pruned)
	}
}

// TestFilterStaleSignals_ST 屏蔽个股：ST/*ST/退市整理 信号即使行情上涨也一律撤下。
func TestFilterStaleSignals_ST(t *testing.T) {
	quotes := map[string]*data.StockInfo{
		"002586": quote(0.5),
		"000999": quote(2.1),
		"600123": quote(3.0),
	}
	sigs := []combat_agent.Signal{
		{Code: "002586", Name: "ST围海", Action: "buy", Confidence: 1.0},   // ST → 撤
		{Code: "000999", Name: "*ST某", Action: "buy", Confidence: 1.0},   // *ST → 撤
		{Code: "600123", Name: "退市整理股", Action: "buy", Confidence: 1.0},  // 退 → 撤
		{Code: "300175", Name: "正常股票", Direction: "做多", Confidence: 1.0}, // 正常 → 留
	}
	live, pruned := filterStaleSignals(sigs, quotes)
	if pruned != 3 || len(live) != 1 {
		t.Fatalf("应撤 3 留 1 (仅正常股票), got live=%d pruned=%d", len(live), pruned)
	}
	if len(live) > 0 && live[0].Code != "300175" {
		t.Fatalf("剩余应为正常股票, got %s", live[0].Code)
	}
}
