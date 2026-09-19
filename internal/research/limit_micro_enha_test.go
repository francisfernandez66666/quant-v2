// §ENH-A 回归测试：同花顺三池 → StockSeries 涨停微结构列 → CatLimit 因子值。
// 覆盖：装配对齐（事件日/非事件日语义、封单比流通市值缺失=NaN）、首封时间解析、
// 因子事件日掩码（非事件 NaN）与 EarlySeal/SealStrength 组合语义。
package research

import (
	"math"
	"path/filepath"
	"testing"

	"quant-trading-v2/internal/factor"
	"quant-trading-v2/internal/store"
)

// seedLuDB 建含三池事件的临时库：3 个交易日，000001.SZ 第 2 日二板（早封、有封单），
// 000002.SZ 第 3 日首板（晚封、当日炸板 2 次、无 daily_basic 流通市值→封单比 NaN）。
func seedLuDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "lu.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	dates := []string{"20230103", "20230104", "20230105"}
	var cal []map[string]any
	for _, d := range dates {
		cal = append(cal, map[string]any{"cal_date": d, "is_open": 1})
	}
	if _, err := db.InsertRows("trade_cal", []string{"cal_date", "is_open"}, cal); err != nil {
		t.Fatal(err)
	}
	var daily, adj, stockRows []map[string]any
	for k, code := range []string{"000001.SZ", "000002.SZ"} {
		for i, d := range dates {
			close := 10.0 + float64(i+k)
			daily = append(daily, map[string]any{
				"ts_code": code, "trade_date": d,
				"open": close, "high": close + 0.1, "low": close - 0.1,
				"close": close, "vol": 10000, "amount": close * 10000,
			})
			adj = append(adj, map[string]any{"ts_code": code, "trade_date": d, "adj_factor": 1.0})
		}
		stockRows = append(stockRows, map[string]any{"ts_code": code, "name": "测试", "industry": "测试"})
	}
	if _, err := db.InsertRows("daily", []string{"ts_code", "trade_date", "open", "high", "low", "close", "vol", "amount"}, daily); err != nil {
		t.Fatal(err)
	}
	if _, err := db.InsertRows("adj_factor", []string{"ts_code", "trade_date", "adj_factor"}, adj); err != nil {
		t.Fatal(err)
	}
	if _, err := db.InsertRows("stocks", []string{"ts_code", "name", "industry"}, stockRows); err != nil {
		t.Fatal(err)
	}
	// daily_basic 仅覆盖 000001（000002 缺流通市值 → 封单比 NaN 语义验证）。
	if _, err := db.InsertRows("daily_basic",
		[]string{"ts_code", "trade_date", "circ_mv", "total_share"},
		[]map[string]any{
			{"ts_code": "000001.SZ", "trade_date": "20230104", "circ_mv": 100000.0, "total_share": 1e8}, // 10亿元
		}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.UpsertThsLimitUps([]store.ThsLimitUpRow{
		{TradeDate: "20230104", TsCode: "000001.SZ", ContinueCnt: 2, FirstSealTime: "09:41", MaxSealMoney: 5e7},
		{TradeDate: "20230105", TsCode: "000002.SZ", ContinueCnt: 1, FirstSealTime: "14:20", MaxSealMoney: 2e7},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.UpsertThsSimplePool("ths_break_pool_daily", "20230105",
		map[string]store.ThsPoolSimple{"000002.SZ": {OpenTimes: 2}}); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestAssembleLimitMicroColumns(t *testing.T) {
	db := seedLuDB(t)
	s, err := Assemble(db, "000001.SZ", "20230103", "20230105")
	if err != nil {
		t.Fatal(err)
	}
	// 非事件日：连板/封单比/炸板=0，首封 NaN；事件日取池值。
	if s.LimitBoard[0] != 0 || s.LimitBoard[1] != 2 || !math.IsNaN(s.FirstSealMin[0]) {
		t.Fatalf("连板/首封装配错误: board=%v seal=%v", s.LimitBoard, s.FirstSealMin)
	}
	if s.FirstSealMin[1] != 581 { // 09:41 = 9*60+41
		t.Fatalf("首封分钟解析错误: %v", s.FirstSealMin[1])
	}
	// 封单比 = 5e7 / (10亿元=100000万×1e4) = 0.05
	if math.Abs(s.SealAmtRatio[1]-0.05) > 1e-9 {
		t.Fatalf("封单比错误: %v", s.SealAmtRatio[1])
	}
	s2, err := Assemble(db, "000002.SZ", "20230103", "20230105")
	if err != nil {
		t.Fatal(err)
	}
	if s2.BreakCnt[2] != 2 {
		t.Fatalf("炸板次数装配错误: %v", s2.BreakCnt)
	}
	if !math.IsNaN(s2.SealAmtRatio[2]) {
		t.Fatalf("流通市值缺失应 NaN（不得折算 0）: %v", s2.SealAmtRatio[2])
	}
}

func TestLimitMicroFactorsOnPanel(t *testing.T) {
	db := seedLuDB(t)
	defs := []factor.Def{}
	for _, id := range []string{"lu_board", "lu_board3", "lu_seal_strength", "lu_early_seal", "lu_reblast_low"} {
		d, ok := factor.Get(id)
		if !ok {
			t.Fatalf("CatLimit 因子 %s 未注册", id)
		}
		defs = append(defs, d)
	}
	p, err := BuildPanel(db, "000001.SZ", "20230103", "20230105", defs)
	if err != nil {
		t.Fatal(err)
	}
	// lu_board：非事件日 NaN、事件日=2
	if !math.IsNaN(p.Factors["lu_board"][0]) || p.Factors["lu_board"][1] != 2 || !math.IsNaN(p.Factors["lu_board"][2]) {
		t.Fatalf("lu_board 事件日掩码错误: %v", p.Factors["lu_board"])
	}
	// lu_early_seal：09:41 早封且未炸 → 1（事件日）
	if p.Factors["lu_early_seal"][1] != 1 {
		t.Fatalf("早封坚决应为 1: %v", p.Factors["lu_early_seal"])
	}
	// lu_seal_strength：窗口内事件日外延（第 2、3 日均应含 0.05 均值）
	if math.Abs(p.Factors["lu_seal_strength"][2]-0.05) > 1e-9 {
		t.Fatalf("封单强度 5 日窗口错误: %v", p.Factors["lu_seal_strength"])
	}
	// lu_reblast_low：市场炸板率历史 <10 观测 → 事件日也 NaN（样本不足不判定）
	if !math.IsNaN(p.Factors["lu_reblast_low"][1]) {
		t.Fatalf("历史不足应 NaN: %v", p.Factors["lu_reblast_low"])
	}
	p2, err := BuildPanel(db, "000002.SZ", "20230103", "20230105", defs)
	if err != nil {
		t.Fatal(err)
	}
	// 晚封 + 当日炸板 2 次 → early_seal=0；board3（首板）=0
	if p2.Factors["lu_early_seal"][2] != 0 || p2.Factors["lu_board3"][2] != 0 {
		t.Fatalf("晚封炸板日应 0: early=%v b3=%v", p2.Factors["lu_early_seal"], p2.Factors["lu_board3"])
	}
}
