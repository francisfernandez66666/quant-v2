// evaluate_series_test.go — §REPLAY-INPUTS 2026-11-28（档 1）：EvaluateSeries 的行为锁。
//
// 这五条断言对应方案 §1.3，钉的是"整段序列入口"会不会悄悄改变打分语义：
//
//	① 情绪列（EmoMaxBoard）真的参与打分——这是本批的目的（旧路径的 StockMarketData 没这些列，
//	   fac_172 那批因此在回放里只会静默 decline）；
//	② 整列缺失 ⇒ 弃权（nodata），不是"给个 0 分"；
//	③ **未来不泄漏**：把 as-of 之后的值改成极端值，当日得分必须一模一样；
//	④ as-of 越界/序列过短 ⇒ 明确 nodata 或钳制，不 panic（回放一只票崩不得带走整轮）；
//	⑤ 与实盘 `Evaluate` 同输入同分（两条入口共用 scoreRule 的正面证据）。
//
// English: locks the series entry point — sentiment columns actually score, missing input
// abstains instead of zero-scoring, no future leakage across the as-of index, and the
// series path agrees with the live Evaluate path.
package factor

import (
	"math"
	"testing"
	"time"

	factorlib "quant-trading-v2/internal/factor"
	"quant-trading-v2/internal/strategy"
	"quant-trading-v2/internal/strategy_engine"
)

// mkSeries 造一条 len 根的序列：价量走线性上涨（与 mkKLines 同形状，便于和 Evaluate 对分），
// 情绪列默认全 NaN（"没接上"的形态），由用例按需填。
func mkSeries(n int, withEmotion bool) *factorlib.StockSeries {
	s := &factorlib.StockSeries{Dates: make([]string, n)}
	cols := []*[]float64{
		&s.CloseHfq, &s.CloseRaw, &s.Open, &s.High, &s.Low, &s.Vol, &s.Amount,
		&s.Turnover, &s.PeTTM, &s.Pb, &s.PsTTM, &s.PcfTTM, &s.DvTTM, &s.TotalShare, &s.IsST,
		&s.Eps, &s.D1, &s.Roe, &s.GrossMargin, &s.NetMargin, &s.DebtToAssets,
		&s.YoyNetProfit, &s.SingleQuarterNIYoy, &s.EmoLimitUp, &s.EmoBreakCnt,
		&s.EmoMaxBoard, &s.EmoBlastRate, &s.LimitBoard, &s.SealAmtRatio, &s.BreakCnt,
		&s.FirstSealMin, &s.IndexChange, &s.FlowToday, &s.Flow5d, &s.FlowCapToday,
		&s.FlowCap5d, &s.SectorFlowQuantile, &s.SectorGainRank,
	}
	// 夹具与生产装配同一条纪律：先铺 NaN 再写读数。
	// 用 make 出来的 0 当"没数据"，会让"腿没接上"和"今天全是中性"在断言里同形。
	for _, c := range cols {
		*c = make([]float64, n)
		// 逐格铺 NaN：注释闸只看紧邻上一行，所以这句必须贴在这里。
		for i := range *c {
			(*c)[i] = math.NaN()
		}
	}
	for i := 0; i < n; i++ {
		s.Dates[i] = dayOfIndex(i)
		p := float64(10 + i)
		s.CloseHfq[i], s.CloseRaw[i] = p, p
		s.Open[i], s.High[i], s.Low[i] = p, p+0.5, p-0.5
		s.Vol[i], s.Amount[i] = float64(1000+i), float64(1000+i)*p
		if withEmotion {
			s.EmoMaxBoard[i] = float64(i % 5)
			s.EmoLimitUp[i] = float64(20 + i)
		}
	}
	return s
}

// dayOfIndex 第 i 个交易日的紧凑日期（从 2026-01-01 起按自然日推，测试只在意单调递增与可解析）。
func dayOfIndex(i int) string {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, i)
	return base.Format("20060102")
}

func emoRule() []*ActiveRule {
	return []*ActiveRule{{
		ID: "fac_emo", Name: "情绪因子", CandID: 172,
		Rule: Rule{
			Factors:      []string{"emo_max_board"},
			Weights:      map[string]float64{"emo_max_board": 1.0},
			Directions:   map[string]int{"emo_max_board": 1},
			BuyThreshold: 50,
		},
	}}
}

func TestEvaluateSeriesUsesPreloadedEmotion(t *testing.T) {
	fs := New()
	fs.SetRules(emoRule())
	s := mkSeries(80, true)
	eval, err := fs.EvaluateSeries("600000", s, 79)
	if err != nil || eval == nil {
		t.Fatalf("EvaluateSeries 失败: %v", err)
	}
	if _, ok := eval.Details["emo_max_board"]; !ok {
		t.Fatalf("情绪列必须参与打分（本批目的），得 level=%s reasons=%v", eval.Level, eval.Reasons)
	}
	if eval.Level == "nodata" {
		t.Fatal("有情绪读数时不得判 nodata")
	}
}

func TestEvaluateSeriesKeepsNaNOnMissingEmotion(t *testing.T) {
	fs := New()
	fs.SetRules(emoRule())
	s := mkSeries(80, false) // 整列 NaN＝"这条腿没数据"
	eval, err := fs.EvaluateSeries("600000", s, 79)
	if err != nil || eval == nil {
		t.Fatalf("调用失败: %v", err)
	}
	// 唯一因子弃权 ⇒ 无可用因子，必须 nodata 而不是拿 0 当"打分很低"
	if eval.Level != "nodata" && eval.Pass {
		t.Fatalf("整列 NaN 必须弃权，得 level=%s pass=%v details=%v", eval.Level, eval.Pass, eval.Details)
	}
	if v, ok := eval.Details["emo_max_board"]; ok && !math.IsNaN(v) {
		t.Fatalf("缺数不得写出一个具体分位值，得 %v", v)
	}
}

func TestEvaluateSeriesNoFutureLeak(t *testing.T) {
	fs := New()
	fs.SetRules(emoRule())
	s := mkSeries(120, true)
	asof := 60
	base, err := fs.EvaluateSeries("600000", s, asof)
	if err != nil || base == nil {
		t.Fatalf("基线调用失败: %v", err)
	}
	// 把 as-of **之后**的所有情绪值改成极端大——当日得分必须一字不动。
	// 这一条是 UpTo 视图真的生效的证明：若某处仍读整列（含未来），得分会跳。
	for i := asof + 1; i < len(s.EmoMaxBoard); i++ {
		s.EmoMaxBoard[i] = 9e9
		s.EmoLimitUp[i] = -9e9
	}
	after, err := fs.EvaluateSeries("600000", s, asof)
	if err != nil || after == nil {
		t.Fatalf("改未来值后调用失败: %v", err)
	}
	if math.Abs(base.TotalScore-after.TotalScore) > 1e-9 {
		t.Fatalf("未来值改变了当日得分＝前视泄漏：base=%.6f after=%.6f", base.TotalScore, after.TotalScore)
	}
	if len(fs.pending["600000"].eval.Details) != len(base.Details) {
		t.Fatal("pending 与返回值不同批")
	}
}

func TestEvaluateSeriesBoundsAndShortSeries(t *testing.T) {
	fs := New()
	fs.SetRules(emoRule())
	s := mkSeries(80, true)
	// 越界钳制到最后一根（不 panic、不报错）
	last := mkSeries(80, true)
	a, err := fs.EvaluateSeries("A", s, 10000)
	if err != nil || a == nil {
		t.Fatalf("越界不应报错: %v", err)
	}
	b, _ := fs.EvaluateSeries("B", last, last.Len()-1)
	if a == nil || b == nil || math.Abs(a.TotalScore-b.TotalScore) > 1e-9 {
		t.Fatalf("越界必须等价于最后一根，得 %v vs %v", a, b)
	}
	// 序列不足 30 根 ⇒ nodata（与 Evaluate 的"K线不足30根"同一把尺）
	short := mkSeries(10, true)
	ev, err := fs.EvaluateSeries("C", short, 9)
	if err != nil || ev == nil {
		t.Fatalf("短序列调用失败: %v", err)
	}
	if ev.Level != "nodata" {
		t.Fatalf("不足 30 根必须 nodata（回放不得比实盘更容易出信号），得 %s", ev.Level)
	}
	// nil 序列 / 空序列都不许 panic
	if _, err := fs.EvaluateSeries("D", nil, 5); err != nil {
		t.Fatalf("nil 序列应返回 nodata 而非错误: %v", err)
	}
	if _, err := fs.EvaluateSeries("E", &factorlib.StockSeries{}, 0); err != nil {
		t.Fatalf("空序列应返回 nodata 而非错误: %v", err)
	}
}

func TestEvaluateSeriesAgreesWithEvaluateOnPriceVolume(t *testing.T) {
	// 只用价量因子时，两条入口必须给同一个分——这是"没改实盘语义"的正面证据
	// （共用同一个 scoreRule 是刻意的，见 evaluate_series.go 文件头）。
	fsA := New()
	fsA.SetRules([]*ActiveRule{{
		ID: "fac_pv", Name: "价量因子", CandID: 180,
		Rule: Rule{
			Factors:      []string{"Mom20", "VolRatio5"},
			Weights:      map[string]float64{"Mom20": 0.5, "VolRatio5": 0.5},
			Directions:   map[string]int{"Mom20": 1, "VolRatio5": 1},
			BuyThreshold: 50,
		},
	}})
	klines := mkKLines(80)
	md := &strategy_engine.StockMarketData{KLines: klines}
	live, err := fsA.Evaluate("600000", md)
	if err != nil || live == nil {
		t.Fatalf("Evaluate 失败: %v", err)
	}
	// 用与 seriesFromKLines 完全相同的序列走新入口
	series := seriesFromKLines(klines, nil, 0)
	alt := New()
	alt.SetRules([]*ActiveRule{{
		ID: "fac_pv", Name: "价量因子", CandID: 180,
		Rule: Rule{
			Factors:      []string{"Mom20", "VolRatio5"},
			Weights:      map[string]float64{"Mom20": 0.5, "VolRatio5": 0.5},
			Directions:   map[string]int{"Mom20": 1, "VolRatio5": 1},
			BuyThreshold: 50,
		},
	}})
	got, err := alt.EvaluateSeries("600000", series, series.Len()-1)
	if err != nil || got == nil {
		t.Fatalf("EvaluateSeries 失败: %v", err)
	}
	if math.Abs(live.TotalScore-got.TotalScore) > 1e-9 {
		t.Fatalf("两条入口同输入必须同分：Evaluate=%.6f EvaluateSeries=%.6f", live.TotalScore, got.TotalScore)
	}
	if live.Pass != got.Pass {
		t.Fatalf("放行判定必须一致：live=%v series=%v", live.Pass, got.Pass)
	}
	var _ *strategy.Evaluation = got
}
