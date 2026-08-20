// 模拟盘研究落库测试：盘后导出的成交与每日快照写入、幂等去重、覆盖更新。
package store

import (
	"testing"
)

// TestSavePaperTradesIdempotent 验证同一笔成交重复导出不产生重复行（INSERT OR IGNORE）。
func TestSavePaperTradesIdempotent(t *testing.T) {
	db := testDB(t)
	rec := PaperTradeRecord{
		UserID: "u_1", Code: "600000.SH", Name: "浦发", Strategy: "N形",
		StrategyType: "n_shape", Side: "buy", Price: 9.5, SignalPrice: 9.4,
		Qty: 300, Amount: 2850, FilledAt: "2026-08-20 10:00:00", Reason: "手动模拟买入",
	}
	if err := db.SavePaperTrades([]PaperTradeRecord{rec}); err != nil {
		t.Fatalf("first save: %v", err)
	}
	// 同一天盘后重复导出（如周末重跑）不得重复插入
	if err := db.SavePaperTrades([]PaperTradeRecord{rec}); err != nil {
		t.Fatalf("second save: %v", err)
	}
	var n int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM paper_trades`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected 1 row after duplicate export, got %d", n)
	}
}

// TestSavePaperDailyUpsert 验证每日快照按（user_id,date）覆盖更新，不产生重复行。
func TestSavePaperDailyUpsert(t *testing.T) {
	db := testDB(t)
	d := PaperDailyRecord{UserID: "u_1", Date: "2026-08-20", Cash: 4000, MarketValue: 3000, TotalValue: 7000, Realized: 120, Positions: 3}
	if err := db.SavePaperDaily(d); err != nil {
		t.Fatalf("first save: %v", err)
	}
	d2 := d
	d2.TotalValue = 7600 // 同一天净值变化（收盘后再次导出）应覆盖
	if err := db.SavePaperDaily(d2); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	var n int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM paper_daily`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected 1 row after upsert, got %d", n)
	}
	var tv float64
	if err := db.db.QueryRow(`SELECT total_value FROM paper_daily WHERE user_id='u_1' AND date='2026-08-20'`).Scan(&tv); err != nil {
		t.Fatal(err)
	}
	if tv != 7600 {
		t.Fatalf("expected overwritten total_value 7600, got %.0f", tv)
	}
}

// TestSavePaperTradesEmpty 空切片不得报错。
func TestSavePaperTradesEmpty(t *testing.T) {
	db := testDB(t)
	if err := db.SavePaperTrades(nil); err != nil {
		t.Fatalf("empty save: %v", err)
	}
}
