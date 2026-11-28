// factor_scalar_test.go — §REPLAY-INPUTS 2026-11-28（档 2）：标量腿接进 scoreRule 的行为锁。
//
// 钉四件事，每件都对应一个"能跑但算错"的形态：
//
//	① 标量腿走 `ScalarScore`，**绝不进 percentile**——同一列数据按时序分位与按绝对带打分
//	   会给出完全不同的数（趋势市里分位天天接近 1，绝对带才反映"今天到底涨了多少"）；
//	② 缺带 / NaN ⇒ 该因子弃权（不进 used），引用它的规则 decline，而不是给 0 分；
//	③ 方向算术与载体逐字对齐（`dir<0 → w×(1-pct)`，否则 `w×dir×pct`，`used += w`），
//	   这条靠"手算期望值"锁死，不靠肉眼比对两份代码；
//	④ 位置锁的行为面：标量 ID 即使在 financial 判定为真时也不能被 `finaScore` 抢走
//	   （分支顺序 D1Event → 标量 → 财务 → 分位，门禁 §259 ③ 用 AST 锁顺序，这里锁结果）。
//
// English: locks the scalar leg inside scoreRule — band scoring (never percentile),
// abstention on NaN/missing bands, and direction arithmetic asserted against hand-computed
// values that mirror the carrier's rules_factor implementation.
package factor

import (
	"math"
	"testing"

	factorlib "quant-trading-v2/internal/factor"
)

// mkMarketSeries 造一条 n 根的序列，市场级列按给定尾值填充（其余 NaN）。
func mkMarketSeries(n int, fill func(s *factorlib.StockSeries, i int)) *factorlib.StockSeries {
	s := &factorlib.StockSeries{Dates: make([]string, n)}
	cols := []*[]float64{&s.CloseHfq, &s.CloseRaw, &s.Open, &s.High, &s.Low, &s.Vol, &s.Amount,
		&s.Turnover, &s.PeTTM, &s.Pb, &s.PsTTM, &s.PcfTTM, &s.DvTTM, &s.TotalShare, &s.IsST,
		&s.Eps, &s.D1, &s.Roe, &s.GrossMargin, &s.NetMargin, &s.DebtToAssets,
		&s.YoyNetProfit, &s.SingleQuarterNIYoy, &s.EmoLimitUp, &s.EmoBreakCnt, &s.EmoMaxBoard,
		&s.EmoBlastRate, &s.LimitBoard, &s.SealAmtRatio, &s.BreakCnt, &s.FirstSealMin,
		&s.IndexChange, &s.FlowToday, &s.Flow5d, &s.FlowCapToday, &s.FlowCap5d,
		&s.SectorFlowQuantile, &s.SectorGainRank}
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
		s.Dates[i] = "20260" + itoaNum(100+i)
		p := float64(10 + i)
		s.CloseHfq[i], s.CloseRaw[i] = p, p
		s.Open[i], s.High[i], s.Low[i] = p, p+0.5, p-0.5
		s.Vol[i], s.Amount[i] = float64(1000+i), float64(1000+i)*p
		if fill != nil {
			fill(s, i)
		}
	}
	return s
}

func itoaNum(v int) string {
	if v == 0 {
		return "0"
	}
	out := ""
	for v > 0 {
		out = string(rune('0'+v%10)) + out
		v /= 10
	}
	return out
}

func TestScalarLegUsesBandNotPercentile(t *testing.T) {
	fs := New()
	fs.SetRules([]*ActiveRule{{
		ID: "fac_idx", Name: "指数腿", CandID: 177,
		Rule: Rule{
			Factors:      []string{"IndexChange"},
			Weights:      map[string]float64{"IndexChange": 1.0},
			Directions:   map[string]int{"IndexChange": 1},
			BuyThreshold: 50,
		},
	}})
	// 序列里指数涨幅**一路上行**：时序分位会把最后一天读成 1.0（最高位），
	// 绝对带只给它 (0.8-(-2))/3.5 = 0.8——两者差得很远，用这一列就能分辨走了哪条路。
	s := mkMarketSeries(60, func(x *factorlib.StockSeries, i int) {
		x.IndexChange[i] = -1.0 + float64(i)*0.03 // 末值 = 0.77
	})
	eval, err := fs.EvaluateSeries("600000", s, 59)
	if err != nil || eval == nil {
		t.Fatalf("调用失败: %v", err)
	}
	got, ok := eval.Details["IndexChange"]
	if !ok {
		t.Fatalf("标量腿未参与打分，reasons=%v", eval.Reasons)
	}
	want := (0.77 - (-2.0)) / (1.5 - (-2.0)) // 线性带归一
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("标量腿必须按绝对带打分：得 %.6f 期望带值 %.6f（若期望接近 1.0 就是误走了时序分位）", got, want)
	}
	if math.Abs(got-1.0) < 1e-6 {
		t.Fatal("得分等于时序分位的满分＝这条腿落进了 percentile")
	}
	// 复合分与载体同式：(pct/1 + 1)/2*100
	wantScore := (want + 1) / 2 * 100
	if math.Abs(eval.TotalScore-wantScore) > 1e-6 {
		t.Fatalf("复合分与载体口径不符：得 %.6f 期望 %.6f", eval.TotalScore, wantScore)
	}
}

func TestScalarLegDirectionMatchesCarrierRule(t *testing.T) {
	// 载体 rules_factor.py:1475-1479 的形状是
	//   dir<0 ⇒ total += w*(1-pct)；否则 total += w*dir*pct；used += w
	// 这里用同一组 (值, 方向, 权重) 手算期望，把两侧钉在同一个数上。
	mk := func(dir int) *FactorStrategy {
		fs := New()
		fs.SetRules([]*ActiveRule{{
			ID: "fac_dir", Name: "方向用例", CandID: 177,
			Rule: Rule{
				Factors:      []string{"SectorGainRank"},
				Weights:      map[string]float64{"SectorGainRank": 2.0},
				Directions:   map[string]int{"SectorGainRank": dir},
				BuyThreshold: 50,
			},
		}})
		return fs
	}
	v := 0.75 // linear 带 0..1 ⇒ pct=0.75
	sPos := mk(1)
	sNeg := mk(-1)
	evPos, _ := sPos.EvaluateSeries("A", mkMarketSeries(40, func(x *factorlib.StockSeries, i int) {
		x.SectorGainRank[i] = v
	}), 39)
	evNeg, _ := sNeg.EvaluateSeries("B", mkMarketSeries(40, func(x *factorlib.StockSeries, i int) {
		x.SectorGainRank[i] = v
	}), 39)
	// 正向：total=2*1*0.75=1.5, used=2 ⇒ ratio 0.75 ⇒ score (0.75+1)/2*100 = 87.5
	if math.Abs(evPos.TotalScore-87.5) > 1e-6 {
		t.Fatalf("正向复合分应为 87.5，得 %.6f", evPos.TotalScore)
	}
	// 负向：total=2*(1-0.75)=0.5, used=2 ⇒ ratio 0.25 ⇒ score 62.5
	if math.Abs(evNeg.TotalScore-62.5) > 1e-6 {
		t.Fatalf("负向复合分应为 62.5（载体同式 w*(1-pct)），得 %.6f", evNeg.TotalScore)
	}
	if math.Abs(evPos.TotalScore+evNeg.TotalScore-150.0) > 1e-6 {
		t.Fatal("正反两向得分之和必须等于 2×score_domain 中点的对称关系（(p+1)/2 与 ((1-p)+1)/2）")
	}
}

func TestScalarLegAbstainsNotZeroScore(t *testing.T) {
	fs := New()
	fs.SetRules([]*ActiveRule{{
		ID: "fac_cap", Name: "未标定腿", CandID: 175,
		Rule: Rule{
			// FlowCapToday 刻意没有带值（待 owner 标定）⇒ 有读数也必须弃权
			Factors:      []string{"FlowCapToday"},
			Weights:      map[string]float64{"FlowCapToday": 1.0},
			Directions:   map[string]int{"FlowCapToday": 1},
			BuyThreshold: 50,
		},
	}})
	s := mkMarketSeries(40, func(x *factorlib.StockSeries, i int) { x.FlowCapToday[i] = 1.5 })
	eval, err := fs.EvaluateSeries("600000", s, 39)
	if err != nil || eval == nil {
		t.Fatalf("调用失败: %v", err)
	}
	if eval.Level != "nodata" || eval.Pass {
		t.Fatalf("未标定的标量腿必须整条弃权（nodata），不得给 0 分继续判，得 level=%s pass=%v",
			eval.Level, eval.Pass)
	}
	if _, ok := eval.Details["FlowCapToday"]; ok {
		t.Fatal("弃权不得写出 details 条目——写了就说明它参与了 used 的分母")
	}
}

func TestScalarNaNAbstainsVsZeroPctAreDifferent(t *testing.T) {
	// 这条用例钉的是本批最核心的一条区分：**"腿没读数"与"腿给了最低分"必须在数字上分得开**。
	// 期望值全部手算，不靠"跑出来是多少就是多少"：
	//
	//	IndexChange = 0.0   → pct = (0-(-2))/(1.5-(-2)) = 2/3.5 = 0.571428...
	//	FlowToday   = -0.5  → pct = (-0.5-(-0.5))/(1.2-(-0.5)) = 0（恰在带下界）
	//	复合分 = (total/used + 1)/2 × 100（与载体 score_domain 同式）
	//	A) FlowToday 全 NaN（弃权）：ratio = 0.571428 → score = 78.5714
	//	B) FlowToday 有读数且 pct=0：ratio = (0.571428 + 0)/2 = 0.285714 → score = 64.2857
	const idxWant = 2.0 / 3.5
	fsFor := func() *FactorStrategy {
		fs := New()
		fs.SetRules([]*ActiveRule{{
			ID: "fac_two", Name: "两腿用例", CandID: 175,
			Rule: Rule{
				Factors:      []string{"FlowToday", "IndexChange"},
				Weights:      map[string]float64{"FlowToday": 1.0, "IndexChange": 1.0},
				Directions:   map[string]int{"FlowToday": 1, "IndexChange": 1},
				BuyThreshold: 50,
			},
		}})
		return fs
	}

	// A) 资金流整天 NaN ⇒ 只有指数腿参与
	skip := mkMarketSeries(40, func(x *factorlib.StockSeries, i int) { x.IndexChange[i] = 0.0 })
	evSkip, err := fsFor().EvaluateSeries("A", skip, 39)
	if err != nil || evSkip == nil {
		t.Fatalf("调用失败: %v", err)
	}
	if _, ok := evSkip.Details["FlowToday"]; ok {
		t.Fatal("NaN 读数不得出现在 details（出现了就等于进了 used 分母）")
	}
	if want := (idxWant + 1) / 2 * 100; math.Abs(evSkip.TotalScore-want) > 1e-6 {
		t.Fatalf("单腿复合分应为 %.4f，得 %.4f", want, evSkip.TotalScore)
	}

	// B) 资金流有读数且恰在带下界（pct=0）⇒ 必须拖低复合分
	sZero := mkMarketSeries(40, func(x *factorlib.StockSeries, i int) {
		x.IndexChange[i] = 0.0
		x.FlowToday[i] = -0.5
	})
	evZero, err := fsFor().EvaluateSeries("B", sZero, 39)
	if err != nil || evZero == nil {
		t.Fatalf("调用失败: %v", err)
	}
	v, ok := evZero.Details["FlowToday"]
	if !ok || v != 0 {
		t.Fatalf("带下界的读数必须参与并得 0 分，得 ok=%v v=%v", ok, v)
	}
	want := ((idxWant+0)/2 + 1) / 2 * 100
	if math.Abs(evZero.TotalScore-want) > 1e-6 {
		t.Fatalf("双腿复合分应为 %.4f，得 %.4f", want, evZero.TotalScore)
	}
	if math.Abs(evSkip.TotalScore-evZero.TotalScore) < 1e-9 {
		t.Fatal("弃权与给 0 分得到同一个复合分＝两种状态在数字上无法区分（排障方向会被带错）")
	}
}

func TestScalarLegIsBandDrivenNotPercentileDriven(t *testing.T) {
	// 位置锁的结果面（AST 顺序锁在门禁 §259 ③）。
	// 做法：拿**同一个数值**分别喂"有带的 IndexChange"与"无带的 FlowCapToday"——
	// 如果打分真的由带值决定，前者会得分、后者必须整条弃权；
	// 若某处让它落进了 percentile/财务分支，无带那条也会照样得分（正是本批要防的形态）。
	//
	// 刻意**不给测试开"改带值"的后门**：那等于在生产包里留一条没人审的侧信道
	// （载体侧 g7e 拒绝 globals() 自省是同一条理由）。
	const v = 0.0
	s := mkMarketSeries(40, func(x *factorlib.StockSeries, i int) {
		x.IndexChange[i] = v
		x.FlowCapToday[i] = v
	})
	ruleFor := func(id string) []*ActiveRule {
		return []*ActiveRule{{
			ID: "fac_" + id, Name: id + " 用例", CandID: 177,
			Rule: Rule{
				Factors:      []string{id},
				Weights:      map[string]float64{id: 1.0},
				Directions:   map[string]int{id: 1},
				BuyThreshold: 50,
			},
		}}
	}
	fsBanded := New()
	fsBanded.SetRules(ruleFor("IndexChange"))
	withBand, err := fsBanded.EvaluateSeries("A", s, 39)
	if err != nil || withBand == nil {
		t.Fatalf("有带腿调用失败: %v", err)
	}
	if _, ok := withBand.Details["IndexChange"]; !ok {
		t.Fatalf("有带的标量腿必须参与打分，得 %+v", withBand)
	}

	fsUnbanded := New()
	fsUnbanded.SetRules(ruleFor("FlowCapToday"))
	off, err := fsUnbanded.EvaluateSeries("B", s, 39)
	if err != nil || off == nil {
		t.Fatalf("无带腿调用失败: %v", err)
	}
	if off.Level != "nodata" {
		t.Fatalf("无带时必须整条弃权（仍能打分＝它落进了 percentile 或财务分支），得 %+v", off)
	}
	if withBand.Details["IndexChange"] == 0 {
		t.Fatalf("带内 0%% 涨幅不应恰为 0 分（带下界是 -2%%，0%% 处在中偏上）")
	}
}
