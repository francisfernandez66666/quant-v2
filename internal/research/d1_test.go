// D1 集成测试：新增 Alpha158/Alpha101 因子从装配→B3 IC 全链路有值（抽因子→IC 检验打通）。
// English: D1 integration test: the new Alpha158/Alpha101 factors produce values through the full pipeline from assembly to B3 IC (factor sampling to IC validation works end-to-end).
package research

import (
	"fmt"
	"math"
	"testing"

	"quant-trading-v2/internal/factor"
	"quant-trading-v2/internal/store"
)

// TestD1FactorsFlowThroughB3 验证 D1 新增因子在真实装配链路上产出非 NaN 值，并能进入 IC 汇总。
// English: TestD1FactorsFlowThroughB3 verifies that the new D1 factors produce non-NaN values on the real assembly pipeline and can feed into the IC aggregation.
// 用 40 个交易日、双股票临时库覆盖 20/24 日预热窗口（BBI/EMA10_20/HighLow20 等）。
// English: Uses a 40-trading-day, two-stock temp DB to cover the 20/24-day warmup windows (BBI/EMA10_20/HighLow20, etc.).
func TestD1FactorsFlowThroughB3(t *testing.T) {
	db := d1SeedDB(t)
	codes, err := db.StockCodes()
	if err != nil || len(codes) < 2 {
		t.Fatalf("研究池应≥3 只, got %d err=%v", len(codes), err)
	}
	defs := []factor.Def{
		mustFactor(t, "RSI14"), mustFactor(t, "BBI"), mustFactor(t, "EMA10_20"),
		mustFactor(t, "RealizedVol5"), mustFactor(t, "AtrRatio14"), mustFactor(t, "HighLow20"),
		mustFactor(t, "VolRatio5"), mustFactor(t, "VMA5"), mustFactor(t, "VSTD20"),
		mustFactor(t, "VMAX10"), mustFactor(t, "VMIN10"), mustFactor(t, "TurnoverStd20"),
		mustFactor(t, "Alpha1"), mustFactor(t, "Alpha4"), mustFactor(t, "Alpha12"),
		mustFactor(t, "Alpha101"),
	}
	start, end := "20210104", "20210301"
	panels, err := BuildPanels(db, codes, start, end, defs)
	if err != nil {
		t.Fatalf("装配面板失败: %v", err)
	}
	if len(panels) == 0 {
		t.Fatal("无有效面板")
	}
	for _, d := range defs {
		found := false
		for _, p := range panels {
			vals := p.Factors[d.ID]
			if len(vals) == 0 {
				continue
			}
			last := vals[len(vals)-1]
			if !math.IsNaN(last) && !math.IsInf(last, 0) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("因子 %s 在 B3 装配链路末值全为 NaN/Inf", d.ID)
		}
		// Summarize 不应崩（IC/分层消费该因子）
		// English: Summarize should not crash (IC/layering consume this factor)
		r := Summarize(panels, d, start, end, 5, 5, 2)
		if r == nil || len(r.IC) == 0 {
			t.Fatalf("因子 %s IC 汇总无有效日", d.ID)
		}
	}
}

// d1SeedDB 建含 2 只股票 × 40 交易日的临时库（波浪收盘，激活动量/波动/量能因子）。
// English: d1SeedDB builds a temp DB with 2 stocks x 40 trading days (wave closes, activating momentum/volatility/volume factors).
func d1SeedDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(t.TempDir() + "/d1.db")
	if err != nil {
		t.Fatalf("打开临时库失败: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	const n = 40
	dates := make([]string, n)
	for i := 0; i < n; i++ {
		// 20210104 + i 个交易日（简单推进日历，工作日近似即可；装配按 trade_date 字符串排序）
		// English: 20210104 + i trading days (simple calendar advance, weekdays suffice; assembly sorts by trade_date string)
		dates[i] = addTradeDay("20210104", i)
	}
	dailyCols := []string{"ts_code", "trade_date", "open", "high", "low", "close", "vol", "amount"}
	var rows []map[string]any
	phaseByCode := map[string]float64{"000001.SZ": 0.0, "600000.SH": 1.2, "000002.SZ": 2.4, "600001.SH": 3.1}
	for _, code := range []string{"000001.SZ", "600000.SH", "000002.SZ", "600001.SH"} {
		phase := phaseByCode[code]
		for i, d := range dates {
			// 波浪收盘：10 + 4·sin(i/4 + phase) + 0.05·i，价量同步波动（各股相位不同保证横截面有变差）
			// English: Wave closes: 10 + 4*sin(i/4 + phase) + 0.05*i, price and volume move in sync (different phases per stock ensure cross-sectional variation)
			c := 10 + 4*sin(float64(i)/4+phase) + 0.05*float64(i)
			vol := 1000 + 300*sin(float64(i)/3+phase/2)
			if vol <= 0 {
				vol = 500
			}
			rows = append(rows, map[string]any{
				"ts_code": code, "trade_date": d,
				"open": c - 0.2, "high": c + 0.6, "low": c - 0.5, "close": c,
				"vol": vol, "amount": vol * c * 100,
			})
		}
	}
	if _, err := db.InsertRows("daily", dailyCols, rows); err != nil {
		t.Fatalf("插入 daily 失败: %v", err)
	}
	// stocks 基础表（StockCodes 消费）
	// English: stocks base table (consumed by StockCodes)
	stockCols := []string{"ts_code", "name", "market"}
	var stockRows []map[string]any
	for _, code := range []string{"000001.SZ", "600000.SH", "000002.SZ", "600001.SH"} {
		stockRows = append(stockRows, map[string]any{"ts_code": code, "name": code, "market": "A"})
	}
	if _, err := db.InsertRows("stocks", stockCols, stockRows); err != nil {
		t.Fatalf("插入 stocks 失败: %v", err)
	}
	adjCols := []string{"ts_code", "trade_date", "adj_factor"}
	var adjRows []map[string]any
	for _, code := range []string{"000001.SZ", "600000.SH", "000002.SZ", "600001.SH"} {
		for _, d := range dates {
			adjRows = append(adjRows, map[string]any{"ts_code": code, "trade_date": d, "adj_factor": 1.0})
		}
	}
	if _, err := db.InsertRows("adj_factor", adjCols, adjRows); err != nil {
		t.Fatalf("插入 adj_factor 失败: %v", err)
	}
	basicCols := []string{"ts_code", "trade_date", "turnover_rate", "pe_ttm", "pb", "total_share", "is_st"}
	var basicRows []map[string]any
	for _, code := range []string{"000001.SZ", "600000.SH", "000002.SZ", "600001.SH"} {
		phase := phaseByCode[code]
		for i, d := range dates {
			// 换手率随时间与个股相位波动（TurnoverStd20 需要横截面/时序变差）
			// English: Turnover rate varies with time and per-stock phase (TurnoverStd20 needs cross-sectional/time-series variation)
			basicRows = append(basicRows, map[string]any{
				"ts_code": code, "trade_date": d, "turnover_rate": 2.0 + 0.5*sin(float64(i)/2+phase),
				"pe_ttm": 15.0 + float64(i%4), "pb": 1.5 + 0.1*float64(i%3), "total_share": 1e9, "is_st": 0,
			})
		}
	}
	if _, err := db.InsertRows("daily_basic", basicCols, basicRows); err != nil {
		t.Fatalf("插入 daily_basic 失败: %v", err)
	}
	return db
}

// mustFactor 按 ID 从注册表取因子定义，缺失即测试失败。
func mustFactor(t *testing.T, id string) factor.Def {
	t.Helper()
	d, ok := factor.Get(id)
	if !ok {
		t.Fatalf("因子 %s 未注册", id)
	}
	return d
}

// sin 测试用正弦别名。
func sin(v float64) float64 { return math.Sin(v) }

// addTradeDay 简单推进交易日历（跳过周末，忽略节假日——测试只需日期排序与预热窗口足够）。
// English: addTradeDay advances the trading calendar simply (skips weekends, ignores holidays — the test only needs date ordering and enough warmup window).
func addTradeDay(start string, offset int) string {
	_ = start
	// 以 20210104(周一) 为起点，仅跳过周末
	// English: Starting from 20210104 (Monday), only skipping weekends
	y, m, d := 2021, 1, 4
	count := 0
	for {
		if dayOfWeek(y, m, d) != 6 && dayOfWeek(y, m, d) != 0 {
			if count == offset {
				return fmtDate(y, m, d)
			}
			count++
		}
		y, m, d = nextDay(y, m, d)
	}
}

// dayOfWeek 计算某日期为周几（Zeller 或直接查表，测试用）。
func dayOfWeek(y, m, d int) int {
	// Sakamoto 算法：0=周日
	// English: Sakamoto's algorithm: 0=Sunday
	t := []int{0, 3, 2, 5, 0, 3, 5, 1, 4, 6, 2, 4}
	if m < 3 {
		y--
	}
	return (y + y/4 - y/100 + y/400 + t[m-1] + d) % 7
}

// nextDay 返回下一天 (y,m,d)。
func nextDay(y, m, d int) (int, int, int) {
	d++
	dim := []int{31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}
	if m == 2 && (y%4 == 0 && (y%100 != 0 || y%400 == 0)) {
		dim[1] = 29
	}
	if d > dim[m-1] {
		d = 1
		m++
		if m > 12 {
			m = 1
			y++
		}
	}
	return y, m, d
}

// fmtDate 格式化为 YYYYMMDD 字符串。
func fmtDate(y, m, d int) string {
	return fmt.Sprintf("%04d%02d%02d", y, m, d)
}
