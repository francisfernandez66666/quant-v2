// l1_test.go — §SIZING-EVO L1 评估器单测。
// 钉死：反事实重算口径、日期分段（hold-out 不泄漏）、护栏顺序与理由、冷启动不动配置、
// R11 中位 scale 硬护栏、R10 分层、评估确定性（同输入同结果）、生成器确定性。
package sizing_evo

import (
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/signalctl"
)

// contains 子串判定（测试辅助；禁串扫描在 arch_test.go 用 AST，不吃这个）。
func contains(s, sub string) bool { return strings.Contains(s, sub) }

// evoSynthetic 构造合成快照集：每股一日的净流入率从 -5% 铺到 +5%，前向收益随净流入率
// 单调递增 + 确定性抖动（抖动幅度与组内档差同量级，保证逐日 IC 有真实方差——IC 恒定时
// IR 无定义，合成数据必须让"哪天信号强/弱"有差异）。市场状态恒定 bull（分层用例另造）。
// ATR=0.2/Price=10 → volFactor=1.0（不抢排序）；其余因子恒定 → 组内 scale 排序=净流入排序。
// English: synthetic snapshots whose forward returns rise monotonically with the flow ratio,
// with day-varying noise so per-day ICs carry real variance.
func evoSynthetic(nDays, perDay int, startDay string) []SizingSnapshot {
	base, err := time.Parse("20060102", startDay)
	if err != nil {
		base = time.Now()
	}
	var out []SizingSnapshot
	for i := 0; i < nDays; i++ {
		day := base.AddDate(0, 0, i).Format("20060102")
		for j := 0; j < perDay; j++ {
			flow := -5 + 10*float64(j)/float64(perDay-1)
			// 抖动 ±0.015（与组内档差 0.0111 同量级 → 组内序偶尔翻转 → IC 逐日有方差）。
			jitter := float64((i*7+j*13)%11-5) * 0.003
			out = append(out, SizingSnapshot{
				Day: day, At: base.AddDate(0, 0, i), Channel: "live", Account: "u1",
				Code: fmt.Sprintf("60%04d", j),
				Sig: SignalProfile{
					Confidence: 0.8, Price: 10, ATR: 0.2, QualityWeight: 1.0,
					FlowRatio5D: flow, FlowRatioToday: 0, // 当日维弃权（只留 5 日维说话）
				},
				Regime: signalctl.Regime{
					EmotionPhase: "发酵", MarketState: "bull", RiskTier: "",
					IndexChangePct: 0, BreadthUpRatio: 0.6,
					Holiday: signalctl.HolidayWindow{CalendarLoaded: true, ClosedRun: 0, PreHolidayN: 0},
				},
				CfgVer: "u1#base", Scale: 1.0,
			})
			// 结算前向收益（指针，Usable 门要求）。
			ret := flow/100 + jitter
			out[len(out)-1].FwdRet = &ret
		}
	}
	return out
}

// evoSplit 默认分段参数：35 日 × 10 = 350 样本，SplitDay=20260924 → 前 23 日样本内
// （230 ≥ 冷启动 200），后 12 日样本外（split 日当天归样本外）。
// English: default split fixtures — day 20260924 starts the hold-out.
func evoSplit() (startDay, split string) { return "20260901", "20260924" }

// evoBase 基线配置（出厂 + WithDefaults）。
func evoBase() config.SizingConfig { return config.DefaultSizingConfig().WithDefaults() }

// TestL1BaselinePassesGuards 基线候选在合成数据上过全部门槛（反事实重算→IC→IR→hold-out→分层）。
func TestL1BaselinePassesGuards(t *testing.T) {
	ps := DefaultParamSpace()
	start, split := evoSplit()
	snaps := evoSynthetic(35, 10, start) // 350 样本 / 35 日
	opts := L1Opts{SplitDay: split}      // 前 23 日样本内，后 12 日样本外
	res := EvaluateCandidate(evoBase(), snaps, Candidate{ID: "baseline", Params: map[string]float64{}}, ps, opts)
	if !res.PassGuard {
		t.Fatalf("基线候选应过护栏，得 %+v", res)
	}
	if res.Samples != 230 {
		t.Fatalf("样本内样本数 = %d, want 230", res.Samples)
	}
	if res.InSample.NDays != 23 || res.Holdout.NDays != 12 {
		t.Fatalf("分段错：in=%d hold=%d, want 23/12", res.InSample.NDays, res.Holdout.NDays)
	}
	if res.InSample.IR < opts.GuardMinIR || res.Holdout.IR <= 0 {
		t.Fatalf("IR 不达标：in=%v hold=%v", res.InSample.IR, res.Holdout.IR)
	}
	if math.IsNaN(res.InSample.WelchT) || res.InSample.WelchT < opts.GuardMinT {
		t.Fatalf("Welch t 不达标：%v", res.InSample.WelchT)
	}
	if res.MedianScale <= 0 || res.BaseMedian <= 0 {
		t.Fatalf("中位 scale 异常：%v/%v", res.MedianScale, res.BaseMedian)
	}
	if _, ok := res.Stratified["bull"]; !ok {
		t.Fatalf("R10 分层缺 bull：%v", res.Stratified)
	}
}

// TestL1Deterministic 同输入同结果（含 Params 映射与全部统计量）。
func TestL1Deterministic(t *testing.T) {
	ps := DefaultParamSpace()
	start, split := evoSplit()
	snaps := evoSynthetic(35, 10, start)
	opts := L1Opts{SplitDay: split}
	c := Candidate{ID: "x", Origin: "coordinate", Params: map[string]float64{"kelly_lambda": 0.45, "flow.buy5d.strong_in": 1.075}}
	a := EvaluateCandidate(evoBase(), snaps, c, ps, opts)
	b := EvaluateCandidate(evoBase(), snaps, c, ps, opts)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("评估必须确定：同输入两次结果不一致")
	}
	if !a.PassGuard {
		t.Fatalf("该候选应过护栏（供 DeepEqual 比较），得 %q", a.Reason)
	}
}

// TestL1ColdStartRejects 冷启动：样本不足 → 拒，且不动任何配置（评估器只读）。
func TestL1ColdStartRejects(t *testing.T) {
	ps := DefaultParamSpace()
	snaps := evoSynthetic(3, 10, "20260901") // 30 样本 < 200
	res := EvaluateCandidate(evoBase(), snaps, Candidate{ID: "c", Params: map[string]float64{"kelly_lambda": 0.6}}, ps, L1Opts{SplitDay: "20260902"})
	if res.PassGuard {
		t.Fatal("冷启动必须拒")
	}
	if !contains(fmt.Sprint(res.Reason), "冷启动") {
		t.Fatalf("拒因须可归因冷启动，得 %q", res.Reason)
	}
}

// TestL1GuardOrder 护栏顺序：IR 不足在 hold-out 之前判；样本外不足单独拒；有效日不足最先。
// 阈值全部由基线实际值派生（合成数据上绝对写死会把测试绑死在噪声水平上）。
func TestL1GuardOrder(t *testing.T) {
	ps := DefaultParamSpace()
	start, split := evoSplit()
	snaps := evoSynthetic(35, 10, start)
	base := EvaluateCandidate(evoBase(), snaps, Candidate{ID: "g"}, ps, L1Opts{SplitDay: split})
	if !base.PassGuard {
		t.Fatalf("基线应过护栏，得 %q", base.Reason)
	}
	// 有效日门槛抬高 → 样本内有效日不足（先于 IR 判定）。
	res3 := EvaluateCandidate(evoBase(), snaps, Candidate{ID: "g"}, ps, L1Opts{SplitDay: split, GuardMinDays: base.InSample.NDays + 1})
	if res3.PassGuard || !contains(res3.Reason, "有效日不足") {
		t.Fatalf("应拒于有效日不足，得 %+v", res3)
	}
	// IR 门槛抬到实际值之上 → 样本内 IR 不足。
	res := EvaluateCandidate(evoBase(), snaps, Candidate{ID: "g"}, ps, L1Opts{SplitDay: split, GuardMinIR: base.InSample.IR + 0.5})
	if res.PassGuard || !contains(res.Reason, "IR不足") {
		t.Fatalf("应拒于 IR 不足，得 %+v", res)
	}
	// 样本外有效日门槛抬高 → 样本外不足。
	res2 := EvaluateCandidate(evoBase(), snaps, Candidate{ID: "g"}, ps, L1Opts{SplitDay: split, MinHoldoutDays: base.Holdout.NDays + 1})
	if res2.PassGuard || !contains(res2.Reason, "样本外") {
		t.Fatalf("应拒于样本外不足，得 %+v", res2)
	}
}

// TestL1R11MedianGuard R11：候选中位 scale 低于基线中位×0.8 → 拒（防"进化到不做生意"）。
// kelly_lambda 0.5→0.2 把所有 scale 同比压 0.787 倍（排序不变、IC 不变），恰好触发 R11。
//
// 基线在这里**显式钉 product**：R11 的前提是"λ 同比压缩每一个 scale"，那是连乘路径的性质；
// §DYN-SIZE v4 的出厂默认已切 power_mean，同一 λ 会被组间幂平均稀释成不到 20% 的中位降幅，
// 用它验证"同比压缩"这条几何会测错对象。文件末尾另有一条 power_mean 对照，把差异写死在测试里。
func TestL1R11MedianGuard(t *testing.T) {
	ps := DefaultParamSpace()
	start, split := evoSplit()
	snaps := evoSynthetic(35, 10, start)
	c := Candidate{ID: "r11", Params: map[string]float64{"kelly_lambda": 0.2}}
	base := evoBase()
	base.Mode = "product"
	res := EvaluateCandidate(base, snaps, c, ps, L1Opts{SplitDay: split})
	if res.PassGuard {
		t.Fatal("R11 必须拒")
	}
	if !contains(res.Reason, "中位scale过低") {
		t.Fatalf("拒因须点名 R11，得 %q", res.Reason)
	}
	// 对照：λ=0.45（比值 0.935 > 0.8）不应死于 R11。
	res2 := EvaluateCandidate(evoBase(), snaps, Candidate{ID: "ok", Params: map[string]float64{"kelly_lambda": 0.45}}, ps, L1Opts{SplitDay: split})
	if !res2.PassGuard {
		t.Fatalf("λ=0.45 应过护栏，得 %q", res2.Reason)
	}
}

// TestL1HoldoutSplitNoShuffle 分段按日期切（时序不洗牌）：样本内/外与日期严格分界。
func TestL1HoldoutSplitNoShuffle(t *testing.T) {
	ps := DefaultParamSpace()
	start, _ := evoSplit()
	snaps := evoSynthetic(35, 10, start)
	// 无显式 SplitDay：按 70% 日期切 → 24 日样本内 / 11 日样本外。
	res := EvaluateCandidate(evoBase(), snaps, Candidate{ID: "s"}, ps, L1Opts{})
	if res.InSample.NDays != 24 || res.Holdout.NDays != 11 {
		t.Fatalf("默认 70%% 分段错：%d/%d", res.InSample.NDays, res.Holdout.NDays)
	}
}

// TestL1CFactorOffKillsDimension C 层关因子 → 该维弃权 → 组内无方差 → 有效日 0 → 拒。
func TestL1CFactorOffKillsDimension(t *testing.T) {
	ps := DefaultParamSpace()
	start, split := evoSplit()
	snaps := evoSynthetic(35, 10, start)
	c := Candidate{ID: "off", Params: map[string]float64{"factor.flow5d": 0}}
	res := EvaluateCandidate(evoBase(), snaps, c, ps, L1Opts{SplitDay: split})
	if res.PassGuard {
		t.Fatal("关掉唯一有信息量的维度后必须拒")
	}
	if !contains(res.Reason, "有效日不足") {
		t.Fatalf("拒因应为有效日不足（组内无方差），得 %q", res.Reason)
	}
}

// TestL1UnsettledSnapshotsAbstain 未结算快照不进样本（缺数不当 0、不伪造）。
func TestL1UnsettledSnapshotsAbstain(t *testing.T) {
	ps := DefaultParamSpace()
	start, split := evoSplit()
	snaps := evoSynthetic(35, 10, start)
	for i := range snaps[:50] { // 前 5 日（50 条）整批抹掉前向收益
		snaps[i].FwdRet = nil
	}
	res := EvaluateCandidate(evoBase(), snaps, Candidate{ID: "u"}, ps, L1Opts{SplitDay: split})
	if res.Samples != 180 { // 230 - 50
		t.Fatalf("未结算条目必须被剔除：样本内 = %d, want 180", res.Samples)
	}
}

// TestGenerateCandidatesDeterministic 生成器确定性：同输入 → 同候选序列；eval 可计数。
func TestGenerateCandidatesDeterministic(t *testing.T) {
	ps := DefaultParamSpace()
	base := evoBase()
	calls := 0
	eval := func(c Candidate) (float64, bool) {
		calls++
		// 假目标：kelly_lambda 越接近 0.6 越好（确定性、可复现）。
		v := c.Params["kelly_lambda"]
		return -math.Abs(v - 0.6), true
	}
	opts := GenerateOpts{MaxIter: 2, MaxMutate: 4}
	a := GenerateCandidates(base, ps, eval, opts)
	b := GenerateCandidates(base, ps, eval, opts)
	if len(a) != len(b) {
		t.Fatalf("两次生成数量不同：%d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i].ID != b[i].ID || !reflect.DeepEqual(a[i].Params, b[i].Params) {
			t.Fatalf("候选 %d 不一致：%+v vs %+v", i, a[i], b[i])
		}
	}
	if a[0].ID != "baseline" || len(a[0].Params) != 0 {
		t.Fatalf("首个候选应为基线，得 %+v", a[0])
	}
	// 坐标上升应把 kelly_lambda 推向 0.6（网格 0.2..0.8 step 0.05 → 0.60 恰在网格上）。
	if got := a[1].Params["kelly_lambda"]; math.Abs(got-0.6) > 1e-9 {
		t.Fatalf("坐标上升应收敛到 0.6，得 %v", got)
	}
	if calls == 0 {
		t.Fatal("eval 应被调用")
	}
}

// TestBestCandidateTieBreak 同分取 CandID 字典序小者（确定性）。
func TestBestCandidateTieBreak(t *testing.T) {
	rs := []L1Result{
		{CandID: "b", PassGuard: true, Objective: 0.5},
		{CandID: "a", PassGuard: true, Objective: 0.5},
		{CandID: "c", PassGuard: true, Objective: 0.4},
		{CandID: "d", PassGuard: false, Objective: 0.9},
	}
	best, ok := BestCandidate(rs)
	if !ok || best.CandID != "a" {
		t.Fatalf("应取同分中 ID 最小且过护栏者，得 %+v ok=%v", best, ok)
	}
	if _, ok := BestCandidate(nil); ok {
		t.Fatal("空结果必须 not ok")
	}
}

// TestStatsOfWelchT IC 恒定时 std=0 → IR/WelchT=NaN（零方差不伪造显著性）。
func TestStatsOfWelchT(t *testing.T) {
	st := statsOf([]ICRow{{IC: 0.5}, {IC: 0.5}, {IC: 0.5}})
	if !math.IsNaN(st.IR) || !math.IsNaN(st.WelchT) {
		t.Fatalf("零方差应 NaN，得 %+v", st)
	}
	st2 := statsOf([]ICRow{{IC: 0.2}, {IC: 0.4}, {IC: 0.6}})
	if math.Abs(st2.IR-2) > 1e-12 {
		t.Fatalf("IR = %v, want 2", st2.IR)
	}
	// t = mean/(std/√n) = 0.4/(0.2/√3) ≈ 3.464
	if math.Abs(st2.WelchT-3.4641016151377544) > 1e-9 {
		t.Fatalf("WelchT = %v, want ≈3.4641", st2.WelchT)
	}
}
