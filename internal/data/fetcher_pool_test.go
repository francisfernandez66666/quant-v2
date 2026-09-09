package data

import "testing"

// TestFetcherMonitoringBaseHot 验证 Monitoring 反映 base/hot 两池（持仓池/监控池分离的前置判定）：
// base（自选+持仓，无上限）与 hot（热点，随轮换/上限淘汰）任一命中即视为在监控。
func TestFetcherMonitoringBaseHot(t *testing.T) {
	f := NewFetcher(nil, nil, nil)
	f.SetBaseStocks([]string{"600000.SH", "000001.SZ"})
	f.UpdateHotStocks([]string{"600519.SH", "300750.SZ"})
	for _, code := range []string{"600000.SH", "000001.SZ", "600519.SH", "300750.SZ"} {
		if !f.Monitoring(code) {
			t.Fatalf("Monitoring(%s) 应为 true（base/hot 内）", code)
		}
	}
	if f.Monitoring("999999.SZ") {
		t.Fatal("Monitoring(999999.SZ) 应为 false（池外代码）")
	}
	// 热点轮换剔除后不再监控（持仓池不受影响——separate pool 语义）
	f.UpdateHotStocks([]string{"300750.SZ"})
	if f.Monitoring("600519.SH") {
		t.Fatal("Monitoring(600519.SH) 热点剔除后应为 false")
	}
	if !f.Monitoring("600000.SH") {
		t.Fatal("Monitoring(600000.SH) base 持仓不应受热点轮换影响")
	}
}
