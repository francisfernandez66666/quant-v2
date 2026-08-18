// 存储层往返测试：建库 → upsert → hfq 换算读取。
package store

import (
	"os"
	"path/filepath"
	"testing"
)

// testDB 临时建库并在结束后清理。
func testDB(t *testing.T) *DB {
	t.Helper()
	p := filepath.Join(t.TempDir(), "t.db")
	db, err := Open(p)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close(); os.Remove(p) })
	return db
}

// TestInsertAndHfq 验证 daily/adj_factor 装载后 HfqBars 正确换算。
func TestInsertAndHfq(t *testing.T) {
	db := testDB(t)

	daily := []map[string]any{
		{"ts_code": "600000.SH", "trade_date": "20250101", "open": 10.0, "high": 11.0, "low": 9.5, "close": 10.5, "vol": 1000, "amount": 1e6},
		{"ts_code": "600000.SH", "trade_date": "20250102", "open": 10.6, "high": 12.0, "low": 10.4, "close": 11.8, "vol": 1200, "amount": 1.2e6},
	}
	if n, err := db.InsertRows("daily", TableColumns("daily"), daily); err != nil || n != 2 {
		t.Fatalf("InsertRows daily: n=%d err=%v", n, err)
	}
	adj := []map[string]any{
		{"ts_code": "600000.SH", "trade_date": "20250101", "adj_factor": 2.0},
		{"ts_code": "600000.SH", "trade_date": "20250102", "adj_factor": 2.5},
	}
	if _, err := db.InsertRows("adj_factor", TableColumns("adj_factor"), adj); err != nil {
		t.Fatalf("InsertRows adj: %v", err)
	}

	bars, err := db.HfqBars("600000.SH", "20250101", "20250102")
	if err != nil {
		t.Fatalf("HfqBars: %v", err)
	}
	if len(bars) != 2 {
		t.Fatalf("HfqBars 条数=%d，期望 2", len(bars))
	}
	// hfq_close = close*adj_factor
	if bars[0].Close != 21.0 || bars[1].Close != 29.5 {
		t.Fatalf("hfq close 错误: %v %v", bars[0].Close, bars[1].Close)
	}
	if bars[0].Open != 20.0 {
		t.Fatalf("hfq open 错误: %v", bars[0].Open)
	}
}

// TestResume 验证断点续传的 Max 查询（daily 全局/单票、财务 end_date）。
func TestResume(t *testing.T) {
	db := testDB(t)

	if _, err := db.InsertRows("daily", TableColumns("daily"), []map[string]any{
		{"ts_code": "600000.SH", "trade_date": "20250101", "close": 1.0},
		{"ts_code": "600000.SH", "trade_date": "20250103", "close": 1.1},
		{"ts_code": "000001.SZ", "trade_date": "20250102", "close": 2.0},
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	if v, _ := db.MaxTradeDateAll("daily"); v != "20250103" {
		t.Fatalf("MaxTradeDateAll=%q", v)
	}
	if v, _ := db.MaxTradeDate("daily", "600000.SH"); v != "20250103" {
		t.Fatalf("MaxTradeDate(600000)=%q", v)
	}
	if v, _ := db.MaxTradeDate("daily", "000001.SZ"); v != "20250102" {
		t.Fatalf("MaxTradeDate(000001)=%q", v)
	}

	// INSERT OR REPLACE 幂等：重插同一主键应覆盖不增行
	if _, err := db.InsertRows("daily", TableColumns("daily"), []map[string]any{
		{"ts_code": "600000.SH", "trade_date": "20250101", "close": 9.9},
	}); err != nil {
		t.Fatalf("reinsert: %v", err)
	}
	if n, _ := db.Count("daily", ""); n != 3 {
		t.Fatalf("Count=%d，期望 3（UPSERT 幂等）", n)
	}

	// 财务表按 end_date 续传
	if _, err := db.InsertRows("fina_indicator", TableColumns("fina_indicator"), []map[string]any{
		{"ts_code": "600000.SH", "end_date": "20250331", "ann_date": "20250425", "roe": 2.5},
	}); err != nil {
		t.Fatalf("fina insert: %v", err)
	}
	if v, _ := db.MaxEndDate("fina_indicator", "600000.SH"); v != "20250331" {
		t.Fatalf("MaxEndDate=%q", v)
	}
	fh, err := db.FinaHistory("600000.SH")
	if err != nil || len(fh) != 1 || fh[0].AnnDate != "20250425" || fh[0].ROE != 2.5 {
		t.Fatalf("FinaHistory 异常: %+v err=%v", fh, err)
	}
}
