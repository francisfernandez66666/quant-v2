// §RFIX-2 回归测试：样本内方向拟合（IS 带符号 IR 裁决，翻转 dirs 收割反向制度；
// 防未来函数：IS正/OOS负 仍被带符号样本外门拒绝）。
package research

import (
	"fmt"
	"math"
	"testing"

	"quant-trading-v2/internal/factor"
	"quant-trading-v2/internal/store"
)

// mkNegStockPanel 与 mkStockPanel 相反制度：k 越大未来收益越差（因子值=k → IC 恒负）。
// 生产 09 月实录形态：dirOfCat 先验下带符号 IR 为负、|IR| 却很强。
func mkNegStockPanel(dates []string, k int) *Panel {
	idx := make(map[string]int, len(dates))
	for i, d := range dates {
		idx[d] = i
	}
	closes := make([]float64, len(dates))
	closes[0] = 100.0
	for j := 1; j < len(dates); j++ {
		// 负 slope 项（-0.003k）给出稳定的负向预测；(k+j)%3 抖动幅度大于 slope 步长，
		// 使逐日 IC 在若干取值间轮换而非恒等于 ±1（IC 恒定 → std=0 → IR=NaN，无法评估）。
		ret := -0.003*float64(k) + 0.012*float64((k+j)%3)
		closes[j] = closes[j-1] * (1 + ret)
	}
	return &Panel{
		Code:    fmt.Sprintf("%d", k),
		Series:  &factor.StockSeries{Dates: dates, CloseHfq: closes},
		DateIdx: idx,
		Factors: map[string][]float64{
			"f1": rep(float64(k), len(dates)),
			"f2": rep(float64(k)*0.5, len(dates)),
		},
	}
}

// TestDirFitFlipsNegativeRegime 全区间负 IC：方向应相对 dirOfCat 先验全量翻转，
// 样本内/样本外 IR 均为正（可收割），护栏正常放行。
func TestDirFitFlipsNegativeRegime(t *testing.T) {
	dates := makeDates(40)
	var panels []*Panel
	for k := 0; k < 5; k++ {
		panels = append(panels, mkNegStockPanel(dates, k))
	}
	res := DiscoverFactors(panels, DiscoverOpts{
		Factors: []string{"f1", "f2"},
		Horizon: 1, MinStocks: 3, MaxFactors: 2, SplitPct: 0.6,
		MinDays: 5, MinIR: 0.05,
	})
	if len(res.Factors) == 0 {
		t.Fatalf("负制度强信号不应被清空: %+v", res)
	}
	if res.InsampleIR <= 0 {
		t.Fatalf("方向拟合后样本内IR应>0，实际 %.4f", res.InsampleIR)
	}
	if res.OutsampleIR <= 0 {
		t.Fatalf("样本内方向在负制度下延续，拟合后样本外IR应>0，实际 %.4f", res.OutsampleIR)
	}
	for _, f := range res.Factors {
		// 测试因子未注册 → dirOfCat 兜底先验 +1；拟合应翻为 -1
		if res.Directions[f] != -1 {
			t.Fatalf("因子 %s 方向=%d，期望翻转后 -1（先验+1）", f, res.Directions[f])
		}
	}
	if !res.PassGuard {
		t.Fatalf("拟合后应通过护栏，Reason=%s", res.Reason)
	}
	// runner 语义自洽：翻转后的 dirs 参与反推泛化，高分组应跑赢
	if res.GenExcess <= 0 {
		t.Fatalf("翻转后反推超额=%.4f 应为正", res.GenExcess)
	}
}

// mkRegimeFlipPanel IS 正相关 / OOS 翻负（真方向反转，fit 不许救）。
func mkRegimeFlipPanel(dates []string, k int) *Panel {
	idx := make(map[string]int, len(dates))
	for i, d := range dates {
		idx[d] = i
	}
	closes := make([]float64, len(dates))
	closes[0] = 100.0
	for j := 1; j < len(dates); j++ {
		coef := 0.004
		if j >= 22 {
			coef = -0.008 // 样本外段制度反转：k 越大跌得越狠
		}
		ret := coef*float64(k) + 0.012*float64((k+j)%3)
		closes[j] = closes[j-1] * (1 + ret)
	}
	return &Panel{
		Code:    fmt.Sprintf("%d", k),
		Series:  &factor.StockSeries{Dates: dates, CloseHfq: closes},
		DateIdx: idx,
		Factors: map[string][]float64{
			"f1": rep(float64(k), len(dates)),
			"f2": rep(float64(k)*0.5, len(dates)),
		},
	}
}

// TestDirFitNoLookahead 防未来函数用例：样本内正、样本外翻负 → 不翻转、
// 拟合后 OutsampleIR 为负、护栏拒绝——方向拟合不得拯救真反转。
func TestDirFitNoLookahead(t *testing.T) {
	dates := makeDates(40)
	var panels []*Panel
	for k := 0; k < 5; k++ {
		panels = append(panels, mkRegimeFlipPanel(dates, k))
	}
	res := DiscoverFactors(panels, DiscoverOpts{
		Factors: []string{"f1", "f2"},
		Horizon: 1, MinStocks: 3, MaxFactors: 2, SplitPct: 0.6,
		MinDays: 5, MinIR: 0.05,
	})
	if len(res.Factors) == 0 {
		t.Fatalf("样本内仍有信号，不应空组合")
	}
	if res.OutsampleIR >= 0 {
		t.Fatalf("OOS 真反转应报负 IR（拟合未偷看样本外），实际 %.4f", res.OutsampleIR)
	}
	if res.PassGuard {
		t.Fatalf("OOS 反转必须被带符号护栏拒绝，Reason=%s", res.Reason)
	}
	for _, f := range res.Factors {
		if res.Directions[f] != 1 {
			t.Fatalf("样本内为正不应翻转方向，因子 %s=%d", f, res.Directions[f])
		}
	}
}

// TestFitDirsByInSampleSign 拟合裁决纯函数：负→全翻-1返回-1；正/零/NaN→不动返回+1。
func TestFitDirsByInSampleSign(t *testing.T) {
	dirs := map[string]int{"a": 1, "b": -1, "c": 1}
	if s := fitDirsByInSampleSign(math.NaN(), dirs); s != 1 {
		t.Fatalf("NaN 不判定应返回 +1，实际 %v", s)
	}
	if dirs["a"] != 1 || dirs["b"] != -1 {
		t.Fatalf("NaN 不应改动方向: %v", dirs)
	}
	if s := fitDirsByInSampleSign(0.2, dirs); s != 1 {
		t.Fatalf("正 IR 应返回 +1，实际 %v", s)
	}
	if s := fitDirsByInSampleSign(-0.8, dirs); s != -1 {
		t.Fatalf("负 IR 应返回 -1，实际 %v", s)
	}
	if dirs["a"] != -1 || dirs["b"] != 1 || dirs["c"] != -1 {
		t.Fatalf("负 IR 应全量翻转: %v", dirs)
	}
}

// seedReversalWindowDB 建临时库（窗口内核端到端用）：价格沿周期 40 日正弦振荡、
// 各股票相位错开 1/6 周期——20 日动量高的股票恰在回落段，Mom20 截面 IC 恒负，
// 复刻生产 09 月「|IR| 强但符号与先验相反」制度。
func seedReversalWindowDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(t.TempDir() + "/rev.db")
	if err != nil {
		t.Fatalf("打开临时库失败: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	var cal []map[string]any
	var dates []string
	d := "20230101"
	for i := 0; i < 200; i++ {
		dates = append(dates, d)
		cal = append(cal, map[string]any{"cal_date": d, "is_open": 1})
		d = nextDayStr(d)
	}
	if _, err := db.InsertRows("trade_cal", []string{"cal_date", "is_open"}, cal); err != nil {
		t.Fatalf("插入 trade_cal 失败: %v", err)
	}
	dailyCols := []string{"ts_code", "trade_date", "open", "high", "low", "close", "vol", "amount"}
	adjCols := []string{"ts_code", "trade_date", "adj_factor"}
	var dailyRows, adjRows, stockRows []map[string]any
	for k := 0; k < 6; k++ {
		code := fmt.Sprintf("00000%d.SZ", k)
		stockRows = append(stockRows, map[string]any{"ts_code": code, "name": "测试", "industry": "测试"})
		for i, dd := range dates {
			phase := float64(i+k*200/6) * 2 * math.Pi / 40
			close := 100.0 * (1 + 0.06*math.Sin(phase))
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
	if _, err := db.InsertRows("stocks", []string{"ts_code", "name", "industry"}, stockRows); err != nil {
		t.Fatalf("插入 stocks 失败: %v", err)
	}
	return db
}

// TestWindowedDirFitFlipsNegativeRegime 窗口内核端到端：负制度合成库下
// DiscoverFactorsWindowed 不再清空候选——产出组合、InsampleIR 非负、
// 方向相对 dirOfCat 先验整体翻转（旧实现此场景 C2 带符号门全灭）。
func TestWindowedDirFitFlipsNegativeRegime(t *testing.T) {
	db := seedReversalWindowDB(t)
	codes, _ := db.StockCodes()
	if len(codes) == 0 {
		t.Fatal("合成库无股票")
	}
	// 先锤实前提：Mom20 在该库上样本内带符号 IR 确实为负（否则本用例失效）
	raw := WindowFactorIC(db, codes, "20230101", datesEnd(db), []string{"Mom20"}, 5, 3)["Mom20"]
	if ir := IR(raw); !(ir < 0 && !isNaN(ir)) {
		t.Fatalf("合成库应构造出 Mom20 负 IC 制度，实际带符号IR=%.4f", ir)
	}
	opts := DiscoverOpts{
		Factors: []string{"Mom20", "STO20", "RSI14"},
		Horizon: 5, MinStocks: 3, MaxFactors: 3, SplitPct: 0.7,
		MinIR: 0.05, MinDays: 5,
	}
	res := DiscoverFactorsWindowed(db, codes, "20230101", datesEnd(db), opts)
	if len(res.Factors) == 0 {
		t.Fatalf("负制度不应再清空候选: %+v", res)
	}
	if res.InsampleIR < 0 {
		t.Fatalf("方向拟合后样本内IR应非负，实际 %.4f", res.InsampleIR)
	}
	// 方向相对类别先验的一致性：拟合是整体翻转，组合内所有因子相对各自先验同号翻转
	var flips map[string]int
	for _, f := range res.Factors {
		prior := 1
		if d, ok := factor.Get(f); ok {
			prior = dirOfCat(d.Cat)
		}
		ratio := res.Directions[f] / prior
		if flips == nil {
			flips = map[string]int{}
		}
		flips[f] = ratio
		if len(flips) > 1 {
			t.Fatalf("拟合必须整体翻转，出现不一致: factors=%v dirs=%v", res.Factors, res.Directions)
		}
	}
	if res.OutsampleIR <= 0 {
		t.Fatalf("负制度延续场景拟合后样本外IR应>0（旧口径被清零），实际 %.4f，Reason=%s", res.OutsampleIR, res.Reason)
	}
	if flips[res.Factors[0]] != -1 {
		t.Fatalf("负 IC 制度应触发翻转（相对先验比值=-1），实际 %v", res.Directions)
	}
}
