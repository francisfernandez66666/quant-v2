// 窗口分块与全量一致性测试：验证 DiscoverFactorsWindowed 与 DiscoverFactors 结果一致
// English: Windowed-chunking vs. full consistency test: verifies DiscoverFactorsWindowed and DiscoverFactors produce consistent results
// （内存优化不应改变发现口径）。
// English: (memory optimization should not change the discovery semantics).
package research

import (
	"fmt"
	"path/filepath"
	"testing"

	"quant-trading-v2/internal/factor"
	"quant-trading-v2/internal/store"
)

// seedWindowDB 建临时库：多只股票 × 多个交易日（近3年窗口化所需）。
// English: seedWindowDB builds a temp DB: multiple stocks x multiple trading days (needed for ~3-year windowing).
// 构造使某因子与前瞻收益强相关（同 mkStockPanel 思路），用于发现。
// English: constructed so a factor strongly correlates with forward returns (same idea as mkStockPanel), for discovery.
func seedWindowDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(t.TempDir() + "/w.db")
	if err != nil {
		t.Fatalf("打开临时库失败: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	// 交易日历：200 个交易日（20230101 起逐日 +1）
	// English: trading calendar: 200 trading days (incrementing daily from 20230101)
	var cal []map[string]any
	dates := make([]string, 0, 200)
	d := "20230101"
	for i := 0; i < 200; i++ {
		dates = append(dates, d)
		cal = append(cal, map[string]any{"cal_date": d, "is_open": 1})
		d = nextDayStr(d)
	}
	if _, err := db.InsertRows("trade_cal", []string{"cal_date", "is_open"}, cal); err != nil {
		t.Fatalf("插入 trade_cal 失败: %v", err)
	}

	// 6 只股票，每只 k 不同（k 越大，收益越高，因子有效）
	// English: 6 stocks, each with a different k (larger k → higher returns, factor effective)
	dailyCols := []string{"ts_code", "trade_date", "open", "high", "low", "close", "vol", "amount"}
	adjCols := []string{"ts_code", "trade_date", "adj_factor"}
	var dailyRows, adjRows []map[string]any
	for k := 0; k < 6; k++ {
		code := fmt.Sprintf("00000%d.SZ", k)
		close := 100.0
		for i, dd := range dates {
			ret := 0.005*float64(i+1)*float64(k) + 0.012*float64((k+i)%3)
			close = close * (1 + ret)
			dailyRows = append(dailyRows, map[string]any{
				"ts_code": code, "trade_date": dd,
				"open": close - 0.1, "high": close + 0.5, "low": close - 0.2,
				"close": close, "vol": 1000, "amount": close * 1000 * 100,
			})
			adjRows = append(adjRows, map[string]any{"ts_code": code, "trade_date": dd, "adj_factor": 1.0})
		}
	}
	if _, err := db.InsertRows("daily", dailyCols, dailyRows); err != nil {
		t.Fatalf("插入 daily 失败: %v", err)
	}
	if _, err := db.InsertRows("adj_factor", adjCols, adjRows); err != nil {
		t.Fatalf("插入 adj_factor 失败: %v", err)
	}

	// stocks 表：StockCodes 从该表读取——此前缺失导致依赖它的测试空转（0 只股票 0==0 假通过）
	// English: seed the stocks table too — StockCodes reads it; without rows some tests passed vacuously.
	var stockRows []map[string]any
	for k := 0; k < 6; k++ {
		stockRows = append(stockRows, map[string]any{
			"ts_code": fmt.Sprintf("00000%d.SZ", k), "name": "测试", "industry": "测试",
		})
	}
	if _, err := db.InsertRows("stocks", []string{"ts_code", "name", "industry"}, stockRows); err != nil {
		t.Fatalf("插入 stocks 失败: %v", err)
	}
	return db
}

// TestTopNExclusiveReruns §S1 排他重跑：topN=2 应产出两条互异最优组合，且首轮不劣于次轮。
// English: §S1 exclusive reruns — topN=2 must yield two mutually-distinct combos, first no worse than second.
func TestTopNExclusiveReruns(t *testing.T) {
	db := seedWindowDB(t)
	codes, _ := db.StockCodes()

	opts := DiscoverOpts{
		Factors:    []string{"Mom20", "STO20", "Brk20", "RSI14", "AtrRatio14"},
		Horizon:    5,
		MinStocks:  3,
		MaxFactors: 4,
		SplitPct:   0.7,
		MinIR:      0.1,
		MinDays:    5,
	}
	results := DiscoverFactorsWindowedN(db, codes, "20230101", datesEnd(db), opts, 2)
	if len(results) == 0 || len(results[0].Factors) == 0 {
		t.Fatalf("应产出 ≥1 条非空结果, got %d (%v)", len(results), results)
	}
	t.Logf("topN=2 产出 %d 条：%v", len(results), results)
	// 排他重跑机制：当第二组合可行时两轮必须互异
	if len(results) >= 2 {
		jacc := jaccardSet(results[0].Factors, results[1].Factors)
		if jacc >= 1 {
			t.Fatalf("两轮组合应互异: 轮0=%v 轮1=%v（Jaccard=%.2f）", results[0].Factors, results[1].Factors, jacc)
		}
	}
}

// jaccardSet 两个因子集合的 Jaccard 相似度（交/并）。
func jaccardSet(a, b []string) float64 {
	am, bm := map[string]bool{}, map[string]bool{}
	for _, f := range a {
		am[f] = true
	}
	n := 0
	for _, f := range b {
		bm[f] = true
		if am[f] {
			n++
		}
	}
	union := len(am) + len(bm) - n
	if union == 0 {
		return 0
	}
	return float64(n) / float64(union)
}

// TestYearlySignConsistency §C3a 分年度 IR 符号一致统计。
func TestYearlySignConsistency(t *testing.T) {
	type attrYear func(year, j int) float64
	// IC 需有微小方差（否则 std=0 → IR=NaN → 判 0，无法做一致性判断）。
	// English: IC needs tiny variance (constant IC → std=0 → IR=NaN → treated 0, inconsistent).
	mk := func(years []int, ic attrYear) []ICRow {
		var rows []ICRow
		for _, y := range years {
			for j := 0; j < 3; j++ {
				rows = append(rows, ICRow{Date: fmt.Sprintf("%04d0101", y), N: 100, IC: ic(y, j)})
			}
		}
		return rows
	}
	pos := func(year, j int) float64 { return 0.02 + 0.001*float64((year+j)%3) }

	// 全部同号（每年 3 行≥2）→ 一致年份 = 非平凡年份
	yrs := []int{2023, 2024, 2025}
	c, tt := yearlySignConsistency(mk(yrs, pos), 2)
	if c != len(yrs) || tt != len(yrs) {
		t.Fatalf("全同号应一致=总=%d, got c=%d t=%d", len(yrs), c, tt)
	}
	// 2022 整体为负 → 一致 3 年 / 非平凡 4 年（总体仍为正）
	mixed := func(year, j int) float64 {
		if year == 2022 {
			return -(0.02 + 0.001*float64(j%3))
		}
		return pos(year, j)
	}
	c, tt = yearlySignConsistency(mk([]int{2022, 2023, 2024, 2025}, mixed), 2)
	if tt != 4 || c != 3 {
		t.Fatalf("一年反向应 c=3 t=4, got c=%d t=%d", c, tt)
	}
	// 空/样本不足
	if c, tt := yearlySignConsistency(nil, 2); c != 0 || tt != 0 {
		t.Fatalf("空输入应 0,0, got %d,%d", c, tt)
	}
}

// TestWindowedMatchesFull 窗口分块与全量发现结果一致。
// English: TestWindowedMatchesFull: windowed chunking and full discovery produce consistent results.
func TestWindowedMatchesFull(t *testing.T) {
	db := seedWindowDB(t)
	codes, _ := db.StockCodes()

	opts := DiscoverOpts{
		Factors:    []string{"Mom20", "STO20", "Brk20"},
		Horizon:    5,
		MinStocks:  3,
		MaxFactors: 3,
		SplitPct:   0.7,
		MinIR:      0.3,
		MinDays:    10,
	}
	// 全量版：BuildPanels + DiscoverFactors
	// English: full version: BuildPanels + DiscoverFactors
	defs := factor.All()
	panels, err := BuildPanels(db, codes, "20230101", datesEnd(db), defs)
	if err != nil {
		t.Fatalf("BuildPanels 失败: %v", err)
	}
	full := DiscoverFactors(panels, opts)
	// 窗口版
	// English: windowed version
	win := DiscoverFactorsWindowed(db, codes, "20230101", datesEnd(db), opts)

	// 选中的因子组合应一致（顺序可能不同则按集合比较）
	// English: the selected factor combination should match (compare as sets since order may differ)
	fset := map[string]bool{}
	for _, f := range full.Factors {
		fset[f] = true
	}
	for _, f := range win.Factors {
		if !fset[f] {
			t.Fatalf("窗口版多出因子 %s（全量=%v 窗口=%v）", f, full.Factors, win.Factors)
		}
	}
	if len(win.Factors) != len(full.Factors) {
		t.Fatalf("因子数不一致: 全量=%v 窗口=%v", full.Factors, win.Factors)
	}
	// IR 符号与通过性一致（绝对值可能因窗口边界略异）
	// English: IR sign and pass/fail status should match (absolute value may differ slightly due to window boundaries)
	if full.PassGuard != win.PassGuard {
		t.Fatalf("护栏判定不一致: 全量=%v(%s) 窗口=%v(%s)", full.PassGuard, full.Reason, win.PassGuard, win.Reason)
	}
}

// datesEnd 返回库中最后交易日。
// English: datesEnd returns the last trading day in the DB.
func datesEnd(db *store.DB) string {
	dates, err := db.TradeDates("20230101", "20231231")
	if err != nil || len(dates) == 0 {
		return "20230101"
	}
	return dates[len(dates)-1]
}

// TestDiscoveryResumeKeyAndCkpt 断点键稳定性/参数敏感性与窗口缓存往返。
func TestDiscoveryResumeKeyAndCkpt(t *testing.T) {
	base := discoveryResumeKey("20230101", "20260821", 5, 20, 60, []string{"B", "A"}, []string{"600000.SH"}, nil)
	same := discoveryResumeKey("20230101", "20260821", 5, 20, 60, []string{"A", "B"}, []string{"600000.SH"}, nil)
	if base != same {
		t.Fatal("因子池顺序不应影响 resume_key")
	}
	diff := discoveryResumeKey("20230101", "20260822", 5, 20, 60, []string{"A", "B"}, []string{"600000.SH"}, nil)
	if diff == base {
		t.Fatal("区间变更必须换 key（旧缓存自动失效）")
	}
	// §F4 已驳回组合参与 key：新增排除必须失效旧断点缓存（否则复用的贪心结果绕过排除）。
	// English: §F4 excluded combos participate in the key — adding exclusions must invalidate the cache.
	d2 := discoveryResumeKey("20230101", "20260821", 5, 20, 60, []string{"A", "B"}, []string{"600000.SH"}, [][]string{{"Brk60"}})
	if d2 == base {
		t.Fatal("已驳回组合加入必须换 key")
	}
	d3 := discoveryResumeKey("20230101", "20260821", 5, 20, 60, []string{"A", "B"}, []string{"600000.SH"}, [][]string{{"A"}, {"B"}})
	if d3 == d2 {
		t.Fatal("已驳回组合集合不同必须换 key")
	}

	dbPath := filepath.Join(t.TempDir(), "t.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	w := [2]string{"20230101", "20230331"}
	ck := &winCkpt{db: db, resumeKey: base, stage: "pre"}
	var got map[string][]ICRow
	if ck.load(w, &got) {
		t.Fatal("空库不应命中")
	}
	want := map[string][]ICRow{"A": {{Date: "20230105", N: 100, IC: 0.03}}}
	ck.save(w, want)
	ck2 := &winCkpt{db: db, resumeKey: base, stage: "pre"}
	if !ck2.load(w, &got) || got["A"][0].IC != 0.03 {
		t.Fatalf("断点往返失败: %+v", got)
	}
	// 不同 stage 不串槽
	ck3 := &winCkpt{db: db, resumeKey: base, stage: "gen"}
	if ck3.load(w, &got) {
		t.Fatal("stage 应相互隔离")
	}
}
