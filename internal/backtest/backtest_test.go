// B4 全链路回测测试：合成库（含行业/涨停/行情/指数）验证事件合成与回测链。
package backtest

import (
	"math"
	"testing"

	"quant-trading-v2/internal/store"
)

// seedBT 建临时回测库：3 行业 × 每行业若干股票 × 8 个交易日。
// 每日第 1 行业（"芯片"）有 3 只涨停（合成事件触发源）；其余行业个别涨停。
// 行情 close 严格递增 → 前瞻收益恒为正；指数 000300.SH 递增 1%/日。
func seedBT(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(t.TempDir() + "/bt.db")
	if err != nil {
		t.Fatalf("打开临时库失败: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	// 交易日历
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

	// 行业: 芯片(6只), 军工(4只), 医药(4只)
	type stk struct {
		code, ind string
		base      float64 // 起始价
	}
	stocks := []stk{
		{"300001.SZ", "芯片", 10}, {"300002.SZ", "芯片", 12}, {"300003.SZ", "芯片", 15},
		{"300004.SZ", "芯片", 20}, {"300005.SZ", "芯片", 8}, {"300006.SZ", "芯片", 25},
		{"600001.SH", "军工", 18}, {"600002.SH", "军工", 22}, {"600003.SH", "军工", 30}, {"600004.SH", "军工", 16},
		{"000001.SZ", "医药", 40}, {"000002.SZ", "医药", 55}, {"000003.SZ", "医药", 33}, {"000004.SZ", "医药", 21},
	}
	// stocks 表
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

	// 每日行情：close 递增 2%/日；芯片 3 只在每天涨停（close = 上日*1.1 四舍五入到 0.01）
	dailyCols := []string{"ts_code", "trade_date", "open", "high", "low", "close", "vol", "amount"}
	limCols := []string{"ts_code", "trade_date", "up_limit", "down_limit"}
	var dailyRows, limRows []map[string]any
	for _, s := range stocks {
		prev := s.base
		isChip := s.ind == "芯片"
		for i, d := range dates {
			close := prev * 1.02
			up := prev * 1.10
			if isChip {
				// 前 4 天芯片 300001-300003 涨停，300004 涨停制造更明显事件
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
			dailyRows = append(dailyRows, map[string]any{
				"ts_code": s.code, "trade_date": d, "open": close * 0.99,
				"high": close * 1.01, "low": close * 0.98, "close": close,
				"vol": 10000, "amount": close * 10000 * 100,
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

	// daily_basic：pe_ttm/pb/turnover/is_st（供估值/流动性因子）
	basicCols := []string{"ts_code", "trade_date", "turnover_rate", "volume_ratio", "pe_ttm",
		"pb", "ps_ttm", "pcf_ttm", "dv_ttm", "total_share", "total_mv", "circ_mv", "is_st"}
	var basicRows []map[string]any
	for i, s := range stocks {
		for _, d := range dates {
			basicRows = append(basicRows, map[string]any{
				"ts_code": s.code, "trade_date": d, "turnover_rate": 2.0,
				"volume_ratio": 1.1, "pe_ttm": 10.0 + float64(i), "pb": 1.0 + float64(i)*0.05,
				"ps_ttm": 2.0, "pcf_ttm": 3.0, "dv_ttm": 0.5,
				"total_share": 1e9, "total_mv": 1e6, "circ_mv": 8e5, "is_st": 0,
			})
		}
	}
	if _, err := db.InsertRows("daily_basic", basicCols, basicRows); err != nil {
		t.Fatalf("插入 daily_basic 失败: %v", err)
	}

	// 财务：每票 2020 四季度 + 2021Q1（roe 随序号变化，ann_date 早于事件区间 → 可用）
	finaCols := []string{"ts_code", "end_date", "ann_date", "roe", "grossprofit_margin",
		"netprofit_margin", "debt_to_assets", "yoy_or", "yoy_net_profit"}
	incomeCols := []string{"ts_code", "end_date", "n_income_attr_p", "revenue"}
	var finaRows, incomeRows []map[string]any
	for i, s := range stocks {
		roe := 5.0 + float64(i)
		finaRows = append(finaRows,
			map[string]any{"ts_code": s.code, "end_date": "20200930", "ann_date": "20201031", "roe": roe, "grossprofit_margin": 30.0, "netprofit_margin": 10.0, "debt_to_assets": 40.0, "yoy_or": 5.0, "yoy_net_profit": 10.0},
			map[string]any{"ts_code": s.code, "end_date": "20201231", "ann_date": "20210430", "roe": roe + 2, "grossprofit_margin": 32.0, "netprofit_margin": 10.0, "debt_to_assets": 40.0, "yoy_or": 5.0, "yoy_net_profit": 15.0},
			map[string]any{"ts_code": s.code, "end_date": "20210331", "ann_date": "20210430", "roe": roe + 1, "grossprofit_margin": 31.0, "netprofit_margin": 10.0, "debt_to_assets": 40.0, "yoy_or": 5.0, "yoy_net_profit": 8.0},
		)
		incomeRows = append(incomeRows,
			map[string]any{"ts_code": s.code, "end_date": "20200930", "n_income_attr_p": 600.0, "revenue": 1800.0},
			map[string]any{"ts_code": s.code, "end_date": "20201231", "n_income_attr_p": 1000.0, "revenue": 2500.0},
			map[string]any{"ts_code": s.code, "end_date": "20210331", "n_income_attr_p": 110.0, "revenue": 520.0},
		)
	}
	if _, err := db.InsertRows("fina_indicator", finaCols, finaRows); err != nil {
		t.Fatalf("插入 fina_indicator 失败: %v", err)
	}
	if _, err := db.InsertRows("income", incomeCols, incomeRows); err != nil {
		t.Fatalf("插入 income 失败: %v", err)
	}

	// 指数：000300.SH 递增 1%/日
	idxCols := []string{"ts_code", "trade_date", "open", "high", "low", "close", "vol", "amount"}
	var idxRows []map[string]any
	ip := 4000.0
	for _, d := range dates {
		idxRows = append(idxRows, map[string]any{
			"ts_code": "000300.SH", "trade_date": d, "open": ip,
			"high": ip * 1.005, "low": ip * 0.995, "close": ip * 1.01,
			"vol": 0, "amount": 0,
		})
		ip = ip * 1.01
	}
	if _, err := db.InsertRows("index_daily", idxCols, idxRows); err != nil {
		t.Fatalf("插入 index_daily 失败: %v", err)
	}
	return db
}

// TestSynthesizeEvents 验证事件合成逻辑。
func TestSynthesizeEvents(t *testing.T) {
	db := seedBT(t)
	evs, err := SynthesizeEvents(db, "20230103", "20230112", 3, 3)
	if err != nil {
		t.Fatalf("合成事件失败: %v", err)
	}
	if len(evs) == 0 {
		t.Fatal("期望至少一个事件（芯片行业每日涨停≥3）")
	}
	for _, e := range evs {
		if e.Industry != "芯片" {
			t.Fatalf("期望事件行业为芯片，得 %s", e.Industry)
		}
		if e.LimitUpCount < 3 {
			t.Fatalf("期望涨停数≥3，得 %d", e.LimitUpCount)
		}
	}
}

// TestRunChain 验证回测链条完整跑通。
func TestRunChain(t *testing.T) {
	db := seedBT(t)
	opts := DefaultOptions()
	opts.Start, opts.End = "20230103", "20230109"
	opts.Rule.TopK = 2
	opts.Rule.MinStocks = 3
	rep, err := Run(db, opts)
	if err != nil {
		t.Fatalf("回测失败: %v", err)
	}
	if rep.TotalEvents == 0 {
		t.Fatal("期望有事件")
	}
	if rep.TotalPicks == 0 {
		t.Fatal("期望有选股")
	}
	// 所有 pick 的前瞻收益应为正（close 单调递增，且选股大概率命中芯片高动量股）
	for _, e := range rep.Events {
		for _, p := range e.Picks {
			for _, h := range opts.Horizons {
				if v, ok := p.Returns[h]; ok && v <= 0 {
					t.Fatalf("pick %s 期望正收益，得 %v", p.Code, v)
				}
			}
		}
	}
	js, err := rep.JSONReport()
	if err != nil {
		t.Fatalf("JSON 失败: %v", err)
	}
	if len(js) == 0 {
		t.Fatal("空 JSON")
	}
	html, err := rep.HTMLReport()
	if err != nil {
		t.Fatalf("HTML 失败: %v", err)
	}
	if !bytesContains(html, []byte("全链路回测报告")) {
		t.Fatal("HTML 缺标题")
	}
}

// bytesContains 手写字节子串包含判断（测试断言辅助）。
func bytesContains(b, sub []byte) bool {
	return stringContains(string(b), string(sub))
}

// stringContains 手写字符串子串包含判断（测试断言辅助）。
func stringContains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexStr(s, sub) >= 0)
}

// indexStr 手写子串首现下标（测试断言辅助）。
func indexStr(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// TestSignalRuleFingerprint §GAP 二.3#5 回归：规则参数指纹——
// 同参数同指纹；任一参数（权重/方向/TopK/因子集）变化指纹必变（缓存失效依据）。
func TestSignalRuleFingerprint(t *testing.T) {
	base := SignalRule{
		Factors:    []string{"EP_ttm", "ROE", "Mom20"},
		Directions: map[string]int{"EP_ttm": 1},
		Weights:    map[string]float64{"ROE": 1.5},
		TopK:       5, MinStocks: 10, MinCover: 0.5,
	}
	fp1 := base.Fingerprint()
	if fp1 != base.Fingerprint() {
		t.Fatal("同参数指纹应稳定")
	}
	// 因子顺序无关
	reordered := base
	reordered.Factors = []string{"Mom20", "EP_ttm", "ROE"}
	if reordered.Fingerprint() != fp1 {
		t.Fatal("因子集合相同（顺序不同）指纹应一致")
	}
	// 权重变化 → 指纹变化
	w := base
	w.Weights = map[string]float64{"ROE": 2.0}
	if w.Fingerprint() == fp1 {
		t.Fatal("权重变化指纹应变化")
	}
	// TopK 变化 → 指纹变化
	k := base
	k.TopK = 10
	if k.Fingerprint() == fp1 {
		t.Fatal("TopK 变化指纹应变化")
	}
	// 方向变化 → 指纹变化
	d := base
	d.Directions = map[string]int{"EP_ttm": -1}
	if d.Fingerprint() == fp1 {
		t.Fatal("方向变化指纹应变化")
	}
}
