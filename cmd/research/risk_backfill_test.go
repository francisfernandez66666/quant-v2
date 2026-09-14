// risk_backfill_test.go — §DATA-OUTAGE 历史回放子命令的行为测试：
// ths 池统计→回补行装配、daily 广度占比、引擎权威行跳过/--force 覆盖、无 daily 行弃权。
// （Behavior tests for the outage-window market_risk_daily backfill planner.）
package main

import (
	"path/filepath"
	"testing"

	"quant-trading-v2/internal/store"
)

// newBackfillDB 建临时研究库并写入两日样本：
// D1=20260824 涨停2只(连板3/5)+日线3只2涨；D2=20260825 涨停1只+炸板1只+日线无。
func newBackfillDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "trading.db"))
	if err != nil {
		t.Fatalf("打开临时库失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.UpsertThsLimitUps([]store.ThsLimitUpRow{
		{TradeDate: "20260824", TsCode: "600000.SH", Name: "测试A", ContinueCnt: 3},
		{TradeDate: "20260824", TsCode: "600001.SH", Name: "测试B", ContinueCnt: 5},
		{TradeDate: "20260825", TsCode: "600002.SH", Name: "测试C", ContinueCnt: 1},
	}); err != nil {
		t.Fatalf("写涨停池失败: %v", err)
	}
	if _, err := db.InsertRows("ths_break_pool_daily", []string{"trade_date", "ts_code", "name"},
		[]map[string]any{{"trade_date": "20260825", "ts_code": "600009.SH", "name": "炸板D"}}); err != nil {
		t.Fatalf("写炸板池失败: %v", err)
	}
	if _, err := db.InsertRows("daily", store.TableColumns("daily"), []map[string]any{
		{"ts_code": "600000.SH", "trade_date": "20260824", "close": 10.0, "pct_chg": 1.2},
		{"ts_code": "600001.SH", "trade_date": "20260824", "close": 9.0, "pct_chg": -2.0},
		{"ts_code": "600002.SH", "trade_date": "20260824", "close": 8.0, "pct_chg": 0.5},
	}); err != nil {
		t.Fatalf("写日线失败: %v", err)
	}
	return db
}

// TestRiskBackfillPlanBasics 两日均入计划、字段口径正确（连板高度/炸板率百分转小数/上涨占比）。
func TestRiskBackfillPlanBasics(t *testing.T) {
	db := newBackfillDB(t)
	rows, skipped, err := riskBackfillPlan(db, "20260820", "20260831", false)
	if err != nil {
		t.Fatalf("回放计划失败: %v", err)
	}
	if skipped != 0 || len(rows) != 2 {
		t.Fatalf("期望 2 行 0 跳过，得 %d/%d", len(rows), skipped)
	}
	d1, d2 := rows[0], rows[1]
	if d1.TradeDate != "20260824" || d1.LimitUpCount != 2 || d1.LadderHeight != 5 {
		t.Fatalf("D1 统计错误: %+v", d1)
	}
	if d1.UpRatio == nil || *d1.UpRatio < 0.65 || *d1.UpRatio > 0.68 {
		t.Fatalf("D1 上涨占比应为 2/3≈0.667，得 %v", d1.UpRatio)
	}
	if d1.BreakRate == nil || *d1.BreakRate != 0 {
		t.Fatalf("D1 炸板率应为 0，得 %v", d1.BreakRate)
	}
	// D2 涨停1+炸板1 → 炸板率 0.5；无当日日线 → UpRatio 弃权 nil。
	if d2.BreakRate == nil || *d2.BreakRate != 0.5 {
		t.Fatalf("D2 炸板率应为 0.5，得 %v", d2.BreakRate)
	}
	if d2.UpRatio != nil {
		t.Fatalf("D2 无日线应弃权 nil，得 %v", *d2.UpRatio)
	}
	if d1.Emotion == "" || d2.Emotion == "" {
		t.Fatal("回补行必须带情绪相位标注")
	}
}

// TestRiskBackfillSkipsAuthoritative 引擎已写权威行（tier 非空）默认跳过，--force 覆盖。
func TestRiskBackfillSkipsAuthoritative(t *testing.T) {
	db := newBackfillDB(t)
	br := 0.0
	if err := db.UpsertMarketRiskDaily(store.MarketRiskDailyRow{
		TradeDate: "20260824", Emotion: "高潮", RiskTier: "Green", Reasons: "engine", BreakRate: &br,
	}); err != nil {
		t.Fatalf("写权威行失败: %v", err)
	}
	rows, skipped, _ := riskBackfillPlan(db, "20260820", "20260831", false)
	if len(rows) != 1 || rows[0].TradeDate != "20260825" || skipped != 1 {
		t.Fatalf("默认应只回补 D2 并跳过 D1，得 %d 行 skip=%d", len(rows), skipped)
	}
	rows, skipped, _ = riskBackfillPlan(db, "20260820", "20260831", true)
	if len(rows) != 2 || skipped != 0 {
		t.Fatalf("force 应全量回补，得 %d 行 skip=%d", len(rows), skipped)
	}
}

// TestRiskBackfillWritesRows 端到端落库：回补行可读回（幂等重跑行数不增）。
func TestRiskBackfillWritesRows(t *testing.T) {
	db := newBackfillDB(t)
	for pass := 0; pass < 2; pass++ {
		rows, _, err := riskBackfillPlan(db, "20260820", "20260831", false)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			if err := db.UpsertMarketRiskDaily(r); err != nil {
				t.Fatalf("落库失败: %v", err)
			}
		}
	}
	got, err := db.ListMarketRiskDaily("20260101", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("幂等重跑后应恰 2 行，得 %d", len(got))
	}
	if got[0].LimitUpCount != 2 || got[0].LadderHeight != 5 {
		t.Fatalf("读回字段错误: %+v", got[0])
	}
}
