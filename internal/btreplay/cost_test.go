// cost_test.go — §GAP4 成本模型回归：净额收益率、开盘即封板不可成交判定。
package btreplay

import (
	"math"
	"testing"
)

// TestCostRoundTripPnl 净额口径：100 买 110 卖，毛收益 +10%；
// 扣双边滑点(5bp×2)+双边佣金(2.5bp×2)+印花税(5bp) ≈ -0.21% → 净 ≈ +9.79%。
func TestCostRoundTripPnl(t *testing.T) {
	got := costRoundTripPnl(100, 110)
	want := (110*0.9995-100*1.0005)/(100*1.0005)*100 - (2*0.00025+0.0005)*100
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("got %.6f want %.6f", got, want)
	}
	if got >= 10 {
		t.Fatalf("净收益应低于毛收益 10%%, got %.4f", got)
	}
	if costRoundTripPnl(0, 110) != 0 || costRoundTripPnl(100, 0) != 0 {
		t.Fatal("非法价格应返回 0")
	}
	// 亏损场景成本进一步放大亏损
	if l := costRoundTripPnl(100, 95); l > -5 {
		t.Fatalf("净亏损应深于毛亏损 -5%%, got %.4f", l)
	}
}

// TestCostOpenAtLimitUp 开盘即封板不可成交：
// 主板前收 10 → 涨停价 11；开盘 11.00/10.995 视为不可成交，10.90 可成交。
func TestCostOpenAtLimitUp(t *testing.T) {
	if !costOpenAtLimitUp("600000.SH", 10, 11.00) {
		t.Fatal("开盘一字涨停应判不可成交")
	}
	if !costOpenAtLimitUp("600000.SH", 10, 10.9995) { // 容差内
		t.Fatal("涨停价容差内应判不可成交")
	}
	if costOpenAtLimitUp("600000.SH", 10, 10.90) {
		t.Fatal("未封板开盘应可成交")
	}
	// 创业板 20cm：前收 10 开盘 11.5（+15%）不触发
	if costOpenAtLimitUp("300750.SZ", 10, 11.5) {
		t.Fatal("创业板 +15% 不应误判封板")
	}
	if costOpenAtLimitUp("600000.SH", 0, 11) || costOpenAtLimitUp("600000.SH", 10, 0) {
		t.Fatal("非法价格不应触发")
	}
}
