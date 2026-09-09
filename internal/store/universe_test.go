// universe_test.go — §WS-D D-2 时点股票池单测：消除幸存者偏差（含退市/未上市边界）。
// English: §WS-D D-2 point-in-time universe tests — survivorship-bias-free boundaries (delisted/not-yet-listed).
package store

import (
	"testing"
)

// seedStocks 写入 4 只边界样本：在市、已退市、未上市、退市日当天。
func seedStocks(t *testing.T, db *DB) {
	t.Helper()
	rows := []map[string]any{
		{"ts_code": "600001.SH", "name": "在市", "list_date": "20180101", "delist_date": ""},
		{"ts_code": "600002.SH", "name": "已退市", "list_date": "20150101", "delist_date": "20230630"},
		{"ts_code": "600003.SH", "name": "未上市", "list_date": "20250101", "delist_date": ""},
		{"ts_code": "600004.SH", "name": "当日退市", "list_date": "20160101", "delist_date": "20200101"},
	}
	n, err := db.InsertRows("stocks", TableColumns("stocks"), rows)
	if err != nil || n != 4 {
		t.Fatalf("seed: n=%d err=%v", n, err)
	}
}

// TestUniverseAtPointInTime 时点股票池边界。
func TestUniverseAtPointInTime(t *testing.T) {
	db := testDB(t)
	seedStocks(t, db)
	// 2019-06-30：在市(600001) + 已退市但当时在市(600002) + 当日退市前的 600004（退市日 2020>2019）
	// 未上市(600003, 2025) 排除。
	codes, err := db.UniverseAt("20190630")
	if err != nil {
		t.Fatalf("UniverseAt: %v", err)
	}
	want := map[string]bool{"600001.SH": true, "600002.SH": true, "600004.SH": true}
	if len(codes) != len(want) {
		t.Fatalf("2019 时点应 3 只, got %v", codes)
	}
	for _, c := range codes {
		if !want[c] {
			t.Fatalf("意外代码 %s", c)
		}
	}
	// 2024-06-30：600002 已退市(2023)、600004 已退市(2020) → 排除；仅在市 600001。
	codes, _ = db.UniverseAt("20240630")
	if len(codes) != 1 || codes[0] != "600001.SH" {
		t.Fatalf("2024 时点应仅在市 600001, got %v", codes)
	}
	// 2020-01-01 当天退市：delist_date=20200101 不满足 "> date"，排除 600004（退市日当天已不可交易）；
	// 600002（2023 退市）当时在市 → 包含。
	codes, _ = db.UniverseAt("20200101")
	want2 := map[string]bool{"600001.SH": true, "600002.SH": true}
	if len(codes) != len(want2) {
		t.Fatalf("20200101 应 2 只(600001/600002), got %v", codes)
	}
	for _, c := range codes {
		if !want2[c] {
			t.Fatalf("意外代码 %s", c)
		}
	}
	// 全量 StockCodes 含退市（元数据层面不丢样本）。
	all, _ := db.StockCodes()
	if len(all) != 4 {
		t.Fatalf("全量应 4 只, got %v", all)
	}
}
