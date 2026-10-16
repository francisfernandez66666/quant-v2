// account_sizing_test.go — §DYN-SIZE P6 账户级读数回归（止损次数 / 权益回撤 / 总仓位占比）。
//
// 三条锁：
//
//	① 止损次数与 TodayRealizedPnl **同源**（fills_effective 卖出腿），按 (代码, 交易日) 去重——
//	   同一笔止损被拆成多次部分成交仍算一次清仓；止盈/减仓/买入/他账号/窗外一律不计；
//	   冒号分段语义（`:止损:`）而不是子串——`止损提示` 这类新类别不得误命中。
//	② 权益回撤取窗口内峰值 vs 最新点；无快照 → 弃权（ok=false），绝不当作"无回撤"；
//	   <=0 的权益读数拒收（"未回报"与"真爆仓"无法区分，不落伪零）。
//	③ 总仓位占比直接取券商回报的 real_account（分子分母同刻同源）。
//
// English: P6 account-reading regression — the stop-loss count shares TodayRealizedPnl's sell-leg
// caliber and dedups per (code, day); the drawdown reads peak vs latest and abstains without
// snapshots; the total-position ratio comes from the broker-reported account row.
package store

import (
	"testing"
)

// slFill 构造一笔带 signal_id 的卖出成交（止损清仓口径）。
func slFill(orderID, code, signalID, tradedAt string) RealFill {
	f := fill(orderID, "", code, "卖出", tradedAt)
	f.SignalID = signalID
	return f
}

// TestStopLossExitCountDedupsPartialFills 同一笔止损的部分成交算一次；代码/日期不同各算一次。
func TestStopLossExitCountDedupsPartialFills(t *testing.T) {
	db := newRiskTestDB(t)
	seed := []RealFill{
		// 600001 当日止损，拆成两笔部分成交 → 1 次
		slFill("S1", "600001.SH", "sell:600001:止损:2026-10-16:r500", "2026-10-16 09:35:00"),
		slFill("S1", "600001.SH", "sell:600001:止损:2026-10-16:r500", "2026-10-16 09:35:04"),
		// 600002 当日止损 → 第 2 次
		slFill("S2", "600002.SH", "sell:600002:止损:2026-10-16", "2026-10-16 10:00:00"),
		// 600001 次日又止损 → 第 3 次（同码不同日不并）
		slFill("S3", "600001.SH", "sell:600001:止损:2026-10-15", "2026-10-15 09:40:00"),
	}
	for i, f := range seed {
		if err := db.ApplyRealFill(f); err != nil {
			t.Fatalf("ApplyRealFill #%d: %v", i, err)
		}
	}
	// 窗 [10-15, 10-16] → 3 次
	n, err := db.StopLossExitCount("u_rg", "2026-10-15", "2026-10-16")
	if err != nil {
		t.Fatalf("StopLossExitCount: %v", err)
	}
	if n != 3 {
		t.Fatalf("止损清仓应为 3 次（同码同日两笔部分成交算 1）got=%d", n)
	}
	// 只收当日 → 2 次
	if n, err = db.StopLossExitCount("u_rg", "2026-10-16", "2026-10-16"); err != nil || n != 2 {
		t.Fatalf("10-16 应为 2 次, got %d err=%v", n, err)
	}
	// 他账号 → 0
	if n, err = db.StopLossExitCount("u_none", "2026-10-15", "2026-10-16"); err != nil || n != 0 {
		t.Fatalf("他账号应为 0, got %d err=%v", n, err)
	}
}

// TestStopLossExitCountIgnoresOtherClasses 止盈/减仓/买入/非止损类卖出一律不计；
// 且「止损提示」不得被子串命中（冒号分段语义）。
func TestStopLossExitCountIgnoresOtherClasses(t *testing.T) {
	db := newRiskTestDB(t)
	seed := []RealFill{
		slFill("A1", "600011.SH", "sell:600011:止盈:2026-10-16", "2026-10-16 09:35:00"),
		slFill("A2", "600012.SH", "sell:600012:减仓:2026-10-16", "2026-10-16 09:36:00"),
		slFill("A3", "600013.SH", "sell:600013:m8:2026-10-16", "2026-10-16 09:37:00"),
		// 冒号分段语义：整段必须等于「止损」，含「止损」的其它段不得命中
		slFill("A4", "600014.SH", "sell:600014:止损提示:2026-10-16", "2026-10-16 09:38:00"),
		// 无 signal_id（券商手工单）：不是我们触发的止损，不计
		slFill("A5", "600015.SH", "", "2026-10-16 09:39:00"),
		// 买入方向即便 signal_id 像止损也不计（口径是卖出腿）
		func() RealFill {
			f := fill("A6", "", "600016.SH", "买入", "2026-10-16 09:40:00")
			f.SignalID = "buy:600016:止损:2026-10-16"
			return f
		}(),
		// 真·止损一条：反证腿（证明上面的 0 不是"查询坏了"）
		slFill("A7", "600017.SH", "sell:600017:止损:2026-10-16", "2026-10-16 09:41:00"),
	}
	for i, f := range seed {
		if err := db.ApplyRealFill(f); err != nil {
			t.Fatalf("ApplyRealFill #%d: %v", i, err)
		}
	}
	n, err := db.StopLossExitCount("u_rg", "2026-10-16", "2026-10-16")
	if err != nil {
		t.Fatalf("StopLossExitCount: %v", err)
	}
	if n != 1 {
		t.Fatalf("只有一条真止损应计入, got=%d", n)
	}
}

// TestStopLossExitCountTruncatedSignalID 柜台把 signal_id 截到 24 位时「止损」段仍在头部，仍可命中。
func TestStopLossExitCountTruncatedSignalID(t *testing.T) {
	db := newRiskTestDB(t)
	// 完整键 sell:600519:止损:2026-10-16 共 25 字符 → 截到 24 = ...2026-10-1
	if err := db.ApplyRealFill(slFill("T1", "600519.SH", "sell:600519:止损:2026-10-1", "2026-10-16 10:00:00")); err != nil {
		t.Fatalf("ApplyRealFill: %v", err)
	}
	n, err := db.StopLossExitCount("u_rg", "2026-10-16", "2026-10-16")
	if err != nil || n != 1 {
		t.Fatalf("截断键应仍命中止损段, got=%d err=%v", n, err)
	}
}

// TestEquityDrawdownPeakToTrough 峰谷回撤：窗口内峰值 vs 最新点；单点=0；空窗=弃权。
func TestEquityDrawdownPeakToTrough(t *testing.T) {
	db := newRiskTestDB(t)
	for day, eq := range map[string]float64{
		"2026-10-13": 100000,
		"2026-10-14": 120000, // 峰值
		"2026-10-15": 108000,
		"2026-10-16": 90000, // 最新
	} {
		if err := db.RecordEquitySnapshot("u_rg", day, eq); err != nil {
			t.Fatalf("RecordEquitySnapshot %s: %v", day, err)
		}
	}
	dd, ok, err := db.EquityDrawdown("u_rg", "2026-10-13", "2026-10-16")
	if err != nil || !ok {
		t.Fatalf("EquityDrawdown ok/err = %v/%v", ok, err)
	}
	// (120000-90000)/120000 = 0.25
	if dd < 0.2499 || dd > 0.2501 {
		t.Fatalf("回撤应为 0.25, got=%.4f", dd)
	}
	// 窗口只含峰值那一天 → 单点 0（合法读数）
	if dd, ok, err = db.EquityDrawdown("u_rg", "2026-10-14", "2026-10-14"); err != nil || !ok || dd != 0 {
		t.Fatalf("单点应 0/true, got %.4f/%v/%v", dd, ok, err)
	}
	// 窗口内无快照 → 弃权
	if dd, ok, err = db.EquityDrawdown("u_rg", "2026-01-01", "2026-01-31"); err != nil || ok || dd != 0 {
		t.Fatalf("空窗应弃权, got %.4f/%v/%v", dd, ok, err)
	}
	// 无快照账号 → 弃权
	if _, ok, err = db.EquityDrawdown("u_none", "2026-01-01", "2026-12-31"); err != nil || ok {
		t.Fatalf("无快照账号应弃权, ok=%v err=%v", ok, err)
	}
}

// TestRecordEquitySnapshotRejectsNonPositive <=0 的权益读数不落库（"未回报"与"真爆仓"不可区分）。
func TestRecordEquitySnapshotRejectsNonPositive(t *testing.T) {
	db := newRiskTestDB(t)
	for _, eq := range []float64{0, -1} {
		if err := db.RecordEquitySnapshot("u_rg", "2026-10-16", eq); err != nil {
			t.Fatalf("RecordEquitySnapshot(%v): %v", eq, err)
		}
	}
	if _, ok, err := db.EquityDrawdown("u_rg", "", "2026-10-16"); err != nil || ok {
		t.Fatalf("非正权益不应落库, ok=%v err=%v", ok, err)
	}
	// 同日覆盖：后写覆盖先写（幂等 upsert）
	_ = db.RecordEquitySnapshot("u_rg", "2026-10-16", 50000)
	_ = db.RecordEquitySnapshot("u_rg", "2026-10-16", 80000)
	var n int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM real_equity_daily WHERE user_id='u_rg'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("同日应只有一行, got=%d", n)
	}
}

// TestAccountSizingReadingsTotalPosPct 总仓位占比取券商回报的 real_account（市值/总资产）。
func TestAccountSizingReadingsTotalPosPct(t *testing.T) {
	db := newRiskTestDB(t)
	if err := db.UpsertRealAccount(RealAccount{
		UserID: "u_rg", AvailableCash: 20000, TotalAsset: 100000, MarketValue: 80000,
	}); err != nil {
		t.Fatalf("UpsertRealAccount: %v", err)
	}
	sl, dd, tp, ok := db.AccountSizingReadings("u_rg", "2026-10-01", "2026-10-16")
	if !ok {
		t.Fatal("账本可用应 ok=true")
	}
	if tp < 0.7999 || tp > 0.8001 {
		t.Fatalf("总仓位占比应为 0.80, got=%.4f", tp)
	}
	if sl != 0 || dd != 0 {
		t.Fatalf("无止损/无权益序列应保持零值, got sl=%d dd=%.4f", sl, dd)
	}
}

// TestAccountSizingReadingsAbstains 无任何读数时仍 ok=true（账本可用），三个读数保持零值——
// 由 signalctl 侧的上限因子逐个弃权；只有 d==nil 才整组弃权。
func TestAccountSizingReadingsAbstains(t *testing.T) {
	db := newRiskTestDB(t)
	sl, dd, tp, ok := db.AccountSizingReadings("u_empty", "2026-10-01", "2026-10-16")
	if !ok || sl != 0 || dd != 0 || tp != 0 {
		t.Fatalf("空账本应零值且 ok=true, got %d/%.4f/%.4f/%v", sl, dd, tp, ok)
	}
	var nilDB *DB
	if _, _, _, ok := nilDB.AccountSizingReadings("u_empty", "", "2026-10-16"); ok {
		t.Fatal("nil DB 应整组弃权")
	}
}
