// settlement_gauge_test.go — §DEADGAUGE（2026-09-23 傍晚批收尾）行为锁：
// 告警规则 settlement_diff（量规 settlement_diff_count）从 09-15 注册起全仓无赋值点，
// p1「交割单对账出现差异」永不触发。本文件把"三条出口都要喂量规"钉成行为用例：
//  1. 对账出差异 → 量规 = 缺失+多余+不符 三类条数之和（不是布尔、不是落库行数）；
//  2. 对账干净 → 必须归 0（否则昨天的差异会一直冒充今天的风警）；
//  3. 执行器不支持交割单 / 网关未连接（静默跳过）→ 写 0 = 不适用，不伪造也不留残值。
//
// English: §DEADGAUGE behavior locks — the settlement_diff alert rule had no gauge writer at all.
// These cases pin all three exits of SettleDay to feed settlement_diff_count: diff count on a real
// diff, zero on a clean reconciliation, and zero (not-applicable) on the silent-skip branches.
package trading

import (
	"testing"

	"quant-trading-v2/internal/metrics"
	"quant-trading-v2/internal/store"
)

// gaugeOf 读取量规当前值（不存在视为 0，并断言其已被注册过）。
func gaugeOf(t *testing.T, name string) int64 {
	t.Helper()
	v, ok := metrics.GetGauge(name)
	if !ok {
		t.Fatalf("量规 %s 从未被赋值（死规则形态复活）", name)
	}
	return v
}

// TestSettleDayFeedsDiffGauge 差异条数必须逐条进量规，且下一次干净对账能把它清零。
func TestSettleDayFeedsDiffGauge(t *testing.T) {
	db := testDB(t)
	cfg := configDefault()
	cfg.Enabled = true
	// 本地一笔成交（与券商侧同键可配对）
	if err := db.ApplyRealFill(store.RealFill{OrderID: "GW-1", Code: "600000.SH", Side: "买入",
		Price: 10, Qty: 100, Amount: 1000, TradedAt: "2026-09-08 09:35:00",
		SignalID: "SIG1", UserID: "u_st", Fee: 2.5, Serial: "SER-1"}); err != nil {
		t.Fatalf("local fill: %v", err)
	}
	// 券商侧：1 笔可配对 + 1 笔本地没有 → MissingInLocal=1
	src := &settleExecutor{src: &mockSettle{resp: &SettlementResponse{
		Date: "2026-09-08", Connected: true,
		Trades: []SettlementTrade{
			{OrderID: "GW-1", TsCode: "600000.SH", Side: "买入", Price: 10, Qty: 100, Fee: 2.5, Serial: "SER-1", TradedAt: "09:35:00"},
			{OrderID: "GW-2", TsCode: "000001.SZ", Side: "卖出", Price: 12, Qty: 200, Fee: 3, Serial: "SER-2", TradedAt: "10:00:00"},
		},
	}}}
	ctrl := NewController(src, db, "u_st", cfg, nil)
	if _, err := ctrl.SettleDay("2026-09-08", SettleModeReportOnly); err != nil {
		t.Fatalf("settle: %v", err)
	}
	if got := gaugeOf(t, "settlement_diff_count"); got != 1 {
		t.Fatalf("有 1 条缺失时量规应为 1, got %d", got)
	}

	// 同一天改成完全配对（本地补上第二笔）→ 量规必须归 0
	if err := db.ApplyRealFill(store.RealFill{OrderID: "GW-2", Code: "000001.SZ", Side: "卖出",
		Price: 12, Qty: 200, Amount: 2400, TradedAt: "2026-09-08 10:00:00",
		SignalID: "SIG2", UserID: "u_st", Fee: 3}); err != nil {
		t.Fatalf("second fill: %v", err)
	}
	if _, err := ctrl.SettleDay("2026-09-08", SettleModeReportOnly); err != nil {
		t.Fatalf("settle 2: %v", err)
	}
	if got := gaugeOf(t, "settlement_diff_count"); got != 0 {
		t.Fatalf("配对齐平后量规必须归 0, got %d", got)
	}
}

// TestSettleDaySkipBranchesWriteZero 不支持交割单的执行器（Noop/桩）与网关未连接两条静默跳过
// 出口：量规写 0（不适用），且必须覆盖掉上一轮留下的非 0 值。
func TestSettleDaySkipBranchesWriteZero(t *testing.T) {
	db := testDB(t)
	cfg := configDefault()
	cfg.Enabled = true

	// 先造一次非 0（网关未连接分支之前的基线，验证"清零"而不是"不写"）
	metrics.SetGauge("settlement_diff_count", 7)

	// 出口 A：执行器不支持交割单
	noopCtrl := NewController(&guardStub{}, db, "u_st", cfg, nil)
	diff, err := noopCtrl.SettleDay("2026-09-09", SettleModeReportOnly)
	if err != nil || diff != nil {
		t.Fatalf("不支持交割单应静默跳过 (nil,nil)，got diff=%v err=%v", diff, err)
	}
	if got := gaugeOf(t, "settlement_diff_count"); got != 0 {
		t.Fatalf("执行器不支持交割单时量规应写 0（不适用），got %d", got)
	}

	// 出口 B：网关未连接（交割单不可信）
	metrics.SetGauge("settlement_diff_count", 5)
	offCtrl := NewController(&settleExecutor{src: &mockSettle{resp: &SettlementResponse{
		Date: "2026-09-09", Connected: false,
	}}}, db, "u_st", cfg, nil)
	diff, err = offCtrl.SettleDay("2026-09-09", SettleModeReportOnly)
	if err != nil || diff != nil {
		t.Fatalf("网关未连接应静默跳过 (nil,nil)，got diff=%v err=%v", diff, err)
	}
	if got := gaugeOf(t, "settlement_diff_count"); got != 0 {
		t.Fatalf("网关未连接时量规应写 0（不适用），got %d", got)
	}
}
