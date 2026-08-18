// E5 板块历史测试：合成库重建板块日线，验证涨停数/平均涨跌幅/领涨股聚合正确。
package store

import (
	"math"
	"testing"
)

// seedSectorBT 建临时库：3 行业 × 14 只股票 × 8 个交易日（与 backtest 测试同构）。
// 芯片 300001-300004 每日涨停（close = 上日×1.10），其余股票每日 +2%。
func seedSectorBT(t *testing.T) *DB {
	t.Helper()
	db, err := Open(t.TempDir() + "/sector.db")
	if err != nil {
		t.Fatalf("打开临时库失败: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	dates := []string{
		"20230103", "20230104", "20230105", "20230106", "20230109",
		"20230110", "20230111", "20230112",
	}
	var cal []map[string]any
	for _, d := range dates {
		cal = append(cal, map[string]any{"cal_date": d, "is_open": 1})
	}
	if _, err := db.InsertRows("trade_cal", []string{"cal_date", "is_open"}, cal); err != nil {
		t.Fatalf("插入 trade_cal 失败: %v", err)
	}

	type stk struct {
		code, ind string
		base      float64
	}
	stocks := []stk{
		{"300001.SZ", "芯片", 10}, {"300002.SZ", "芯片", 12}, {"300003.SZ", "芯片", 15},
		{"300004.SZ", "芯片", 20}, {"300005.SZ", "芯片", 8}, {"300006.SZ", "芯片", 25},
		{"600001.SH", "军工", 18}, {"600002.SH", "军工", 22}, {"600003.SH", "军工", 30}, {"600004.SH", "军工", 16},
		{"000001.SZ", "医药", 40}, {"000002.SZ", "医药", 55}, {"000003.SZ", "医药", 33}, {"000004.SZ", "医药", 21},
	}
	stkCols := []string{"ts_code", "name", "area", "industry", "market", "list_date", "delist_date"}
	var stkRows []map[string]any
	for _, s := range stocks {
		stkRows = append(stkRows, map[string]any{
			"ts_code": s.code, "name": s.code, "area": "", "industry": s.ind,
			"market": "主板", "list_date": "20150101", "delist_date": nil,
		})
	}
	if _, err := db.InsertRows("stocks", stkCols, stkRows); err != nil {
		t.Fatalf("插入 stocks 失败: %v", err)
	}

	dailyCols := []string{"ts_code", "trade_date", "open", "high", "low", "close", "pct_chg", "vol", "amount"}
	limCols := []string{"ts_code", "trade_date", "up_limit", "down_limit"}
	var dailyRows, limRows []map[string]any
	for _, s := range stocks {
		prev := s.base
		isChip := s.ind == "芯片"
		// 芯片中 300001-300004 涨停（4 只），300005/300006 平盘（不涨停）
		isLimit := isChip && (s.code == "300001.SZ" || s.code == "300002.SZ" ||
			s.code == "300003.SZ" || s.code == "300004.SZ")
		for i, d := range dates {
			close := prev * 1.02
			up := prev * 1.10
			if isLimit {
				close = prev * 1.10
			}
			close = math.Round(close*100) / 100
			up = math.Round(up*100) / 100
			if close <= 0 {
				close = 1
			}
			if i == 0 {
				close = s.base
			}
			pct := 0.0
			if prev > 0 {
				pct = (close/prev - 1) * 100
			}
			dailyRows = append(dailyRows, map[string]any{
				"ts_code": s.code, "trade_date": d, "open": close * 0.99,
				"high": close * 1.01, "low": close * 0.98, "close": close,
				"pct_chg": pct, "vol": 10000, "amount": close * 10000 * 100,
			})
			limRows = append(limRows, map[string]any{
				"ts_code": s.code, "trade_date": d, "up_limit": up, "down_limit": up * 0.8,
			})
			prev = close
		}
	}
	if _, err := db.InsertRows("daily", dailyCols, dailyRows); err != nil {
		t.Fatalf("插入 daily 失败: %v", err)
	}
	if _, err := db.InsertRows("stk_limit", limCols, limRows); err != nil {
		t.Fatalf("插入 stk_limit 失败: %v", err)
	}
	return db
}

// TestRebuildSectorHistory 重建板块历史：芯片每日 4 涨停、军工/医药 0 涨停；
// 平均涨跌幅符号与行情一致；领涨股含涨停股。
func TestRebuildSectorHistory(t *testing.T) {
	db := seedSectorBT(t)
	n, err := db.RebuildSectorHistory("20230103", "20230112")
	if err != nil {
		t.Fatalf("重建板块历史失败: %v", err)
	}
	// 8 天 × 3 行业 = 24 行
	if n != 24 {
		t.Fatalf("写入行数=%d 期望 24", n)
	}

	// 查询芯片板块某日：涨停 4 家
	rows, err := db.SectorHistory("芯片", "20230103", "20230112")
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(rows) != 8 {
		t.Fatalf("芯片板块天数=%d 期望 8", len(rows))
	}
	for _, r := range rows {
		// 首日（20230103）为基准价不涨停；其余 7 天芯片 4 只涨停
		expectLU := 0
		if r.TradeDate != "20230103" {
			expectLU = 4
		}
		if r.LimitupCnt != expectLU {
			t.Fatalf("%s 芯片涨停数=%d 期望 %d", r.TradeDate, r.LimitupCnt, expectLU)
		}
		if r.MemberCount != 6 {
			t.Fatalf("%s 芯片成员数=%d 期望 6", r.TradeDate, r.MemberCount)
		}
		if r.TradeDate != "20230103" && r.ChangePct <= 0 {
			t.Fatalf("%s 芯片平均涨跌幅应 >0（涨停拉动），实际=%f", r.TradeDate, r.ChangePct)
		}
		if len(r.TopStocks) == 0 {
			t.Fatalf("%s 芯片领涨股为空", r.TradeDate)
		}
	}

	// 军工：0 涨停，平均涨跌幅 +2%
	mil, err := db.SectorHistory("军工", "20230103", "20230112")
	if err != nil {
		t.Fatalf("查询军工失败: %v", err)
	}
	if len(mil) != 8 {
		t.Fatalf("军工天数=%d 期望 8", len(mil))
	}
	for _, r := range mil {
		if r.LimitupCnt != 0 {
			t.Fatalf("%s 军工涨停数=%d 期望 0", r.TradeDate, r.LimitupCnt)
		}
	}

	// SectorLimitUpCounts 单日查询（非首日：芯片 4 涨停）
	lus, err := db.SectorLimitUpCounts("20230104")
	if err != nil {
		t.Fatalf("查询涨停数失败: %v", err)
	}
	if lus["芯片"] != 4 {
		t.Fatalf("20230104 芯片涨停数=%d 期望 4", lus["芯片"])
	}
	if lus["医药"] != 0 {
		t.Fatalf("20230104 医药涨停数=%d 期望 0", lus["医药"])
	}
}
