// l2_test.go — §SIZING-EVO L2 灰度/降级评估器单测。
// 钉死：观察期/无观测的 pending 语义、阈值逐项拒因、指标口径（IC-IR/胜率/盈亏比/回撤）、
// 通道与配置戳过滤（live 快照不能冒充 shadow 观测）、降级连续天数与中断复位。
package sizing_evo

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/signalctl"
)

// evoShadowSet 造 shadow 通道快照：ret = 2 + slope×flow/100 + 抖动。
// slope>0 = 加权与前向收益同向（应晋升）；slope<0 = 反向（应回拒）。
// Scale 与 flow 同向、恒正（Usable 门要求 Scale>0）。
// English: synthetic shadow snapshots whose forward return moves with the flow ratio
// (slope>0 promotes, slope<0 rejects).
func evoShadowSet(candID string, nDays, perDay int, start string, slope float64) []SizingSnapshot {
	base, err := time.Parse("20060102", start)
	if err != nil {
		base = time.Now()
	}
	var out []SizingSnapshot
	for i := 0; i < nDays; i++ {
		day := base.AddDate(0, 0, i).Format("20060102")
		for j := 0; j < perDay; j++ {
			flow := -5 + 10*float64(j)/float64(perDay-1)
			jitter := float64((i*7+j*13)%11-5) * 0.003
			ret := 2 + slope*flow/100 + jitter
			out = append(out, SizingSnapshot{
				Day: day, At: base.AddDate(0, 0, i), Channel: "shadow", Account: "u1",
				Code: fmt.Sprintf("60%04d", j),
				Sig: SignalProfile{
					Confidence: 0.8, Price: 10, ATR: 0.2, QualityWeight: 1.0,
					FlowRatio5D: flow,
				},
				Regime: signalctl.Regime{
					EmotionPhase: "发酵", MarketState: "bull", BreadthUpRatio: 0.6,
					Holiday: signalctl.HolidayWindow{CalendarLoaded: true},
				},
				CfgVer: candID, Scale: 1 + slope*flow/100,
				FwdRet: &ret,
			})
		}
	}
	return out
}

// TestEvaluateGrayscalePending 观察期未满 → pending（按影子库实际观测日数计）。
func TestEvaluateGrayscalePending(t *testing.T) {
	snaps := evoShadowSet("evo1234", 5, 10, "20260901", 1)
	v := EvaluateGrayscale(snaps, "evo1234", L2Opts{})
	if v.Verdict != "pending" {
		t.Fatalf("5 日观测应 pending，得 %q（%s）", v.Verdict, v.Reason)
	}
	if v.ObservedDays != 5 || v.EnteredDay != "20260901" {
		t.Fatalf("观测日/起始日错：%d/%s", v.ObservedDays, v.EnteredDay)
	}
	if !strings.Contains(v.Reason, "观察期未满") {
		t.Fatalf("拒因应点名观察期，得 %q", v.Reason)
	}
}

// TestEvaluateGrayscaleNoObservation 从未留痕 → pending（拒因点名无 shadow 观测）。
func TestEvaluateGrayscaleNoObservation(t *testing.T) {
	v := EvaluateGrayscale(nil, "evo1234", L2Opts{ObservationDays: 1})
	if v.Verdict != "pending" {
		t.Fatalf("无观测应 pending，得 %q", v.Verdict)
	}
	if !strings.Contains(v.Reason, "无 shadow 观测") {
		t.Fatalf("拒因应点名无观测，得 %q", v.Reason)
	}
}

// TestEvaluateGrayscaleUnsettledPending 有留痕但前向收益未结算 → 观察期按已结算日计，
// 拒因须点出"待结算"（与"根本没留痕"可区分）。
func TestEvaluateGrayscaleUnsettledPending(t *testing.T) {
	snaps := evoShadowSet("evo1234", 22, 10, "20260901", 1)
	for i := range snaps {
		snaps[i].FwdRet = nil // 全部未结算
	}
	v := EvaluateGrayscale(snaps, "evo1234", L2Opts{})
	if v.Verdict != "pending" {
		t.Fatalf("未结算应 pending，得 %q", v.Verdict)
	}
	if !strings.Contains(v.Reason, "已结算0日") || !strings.Contains(v.Reason, "待结算") {
		t.Fatalf("拒因应点出已结算/待结算，得 %q", v.Reason)
	}
}

// TestEvaluateGrayscalePromote 22 日观测 + 加权与收益同向 → 全部指标达标 → promote。
func TestEvaluateGrayscalePromote(t *testing.T) {
	snaps := evoShadowSet("evo1234", 22, 10, "20260901", 1)
	v := EvaluateGrayscale(snaps, "evo1234", L2Opts{})
	if v.Verdict != "promote" {
		t.Fatalf("应晋升，得 %q（%s）", v.Verdict, v.Reason)
	}
	if v.Trades != 220 {
		t.Fatalf("观测样本 = %d, want 220", v.Trades)
	}
	if v.IR < 0.3 || math.IsNaN(v.IR) {
		t.Fatalf("IC-IR 应显著为正，得 %v", v.IR)
	}
	if v.WinRate < 40 || v.ProfitFactor < 1.2 || v.MaxDrawdownPct > 15 {
		t.Fatalf("指标异常：胜率=%v 盈亏比=%v 回撤=%v", v.WinRate, v.ProfitFactor, v.MaxDrawdownPct)
	}
}

// TestEvaluateGrayscaleReject 加权与收益反向 → IC-IR 为负 → reject（拒因点名 IR）。
func TestEvaluateGrayscaleReject(t *testing.T) {
	snaps := evoShadowSet("evo1234", 22, 10, "20260901", -1)
	v := EvaluateGrayscale(snaps, "evo1234", L2Opts{})
	if v.Verdict != "reject" {
		t.Fatalf("反向加权应回拒，得 %q（%s）", v.Verdict, v.Reason)
	}
	if !strings.Contains(v.Reason, "IR不足") {
		t.Fatalf("拒因应点名 IR，得 %q", v.Reason)
	}
}

// TestEvaluateGrayscaleChannelAndVerFilter live 通道/其他配置戳的快照不能冒充 shadow 观测。
func TestEvaluateGrayscaleChannelAndVerFilter(t *testing.T) {
	snaps := evoShadowSet("evo1234", 22, 10, "20260901", 1)
	for i := range snaps {
		snaps[i].Channel = "live" // 全部改成 live 通道
	}
	if v := EvaluateGrayscale(snaps, "evo1234", L2Opts{ObservationDays: 1}); v.Verdict != "pending" {
		t.Fatalf("live 快照不得计入灰度观测，得 %q", v.Verdict)
	}
	snaps = evoShadowSet("evo1234", 22, 10, "20260901", 1)
	if v := EvaluateGrayscale(snaps, "other-id", L2Opts{ObservationDays: 1}); v.Verdict != "pending" {
		t.Fatalf("别的配置戳不得计入本候选，得 %q", v.Verdict)
	}
}

// TestLiveDailyStatsShape 逐日聚合：样本数/胜率/滚动 IR（首日单 IC → IR=0）。
func TestLiveDailyStatsShape(t *testing.T) {
	snaps := evoShadowSet("u1#evo1234", 4, 5, "20260901", 1)
	for i := range snaps {
		snaps[i].Channel = "live"
	}
	days := LiveDailyStats(snaps, "evo1234") // 按候选 ID 后缀匹配（账号前缀不参与分组）
	if len(days) != 4 {
		t.Fatalf("应有 4 个观测日，得 %d", len(days))
	}
	if days[0].Trades != 5 {
		t.Fatalf("首日样本 = %d, want 5", days[0].Trades)
	}
	if days[0].IR != 0 {
		t.Fatalf("首日单 IC 的滚动 IR 应为 0，得 %v", days[0].IR)
	}
	if days[3].IR == 0 {
		t.Fatalf("第 4 日应有多日 IC 的滚动 IR，得 0")
	}
	if days[0].WinRate < 0 || days[0].WinRate > 100 {
		t.Fatalf("胜率越界：%v", days[0].WinRate)
	}
	// 其他配置戳不混入。
	if got := LiveDailyStats(snaps, "base"); len(got) != 0 {
		t.Fatalf("别的配置戳不得混入，得 %d 天", len(got))
	}
}

// TestEvaluateDemoteDisable 连续 3 日低于衰退阈值 → disable。
func TestEvaluateDemoteDisable(t *testing.T) {
	days := []DailyStat{
		{IR: 0.5, WinRate: 60, Trades: 8},
		{IR: -0.2, WinRate: 20, Trades: 8},
		{IR: -0.3, WinRate: 10, Trades: 8},
		{IR: -0.1, WinRate: 25, Trades: 8},
	}
	v := EvaluateDemote(days, DemoteOpts{})
	if v.Verdict != "disable" {
		t.Fatalf("连续 3 日衰退应降级，得 %q（%s）", v.Verdict, v.Reason)
	}
	if v.ConsecLow != 3 {
		t.Fatalf("连续低位天数 = %d, want 3", v.ConsecLow)
	}
}

// TestEvaluateDemoteKeep 中断复位：2 低 + 1 好 + 2 低 → 最长连续 2 < 3 → keep。
func TestEvaluateDemoteKeep(t *testing.T) {
	days := []DailyStat{
		{IR: -0.2, WinRate: 20, Trades: 8},
		{IR: -0.2, WinRate: 20, Trades: 8},
		{IR: 0.6, WinRate: 70, Trades: 8},
		{IR: -0.2, WinRate: 20, Trades: 8},
		{IR: -0.2, WinRate: 20, Trades: 8},
	}
	v := EvaluateDemote(days, DemoteOpts{})
	if v.Verdict != "keep" {
		t.Fatalf("中断后不应降级，得 %q（%s）", v.Verdict, v.Reason)
	}
	if v.ConsecLow != 2 {
		t.Fatalf("最长连续低位 = %d, want 2", v.ConsecLow)
	}
}

// TestEvaluateDemoteThinDays 样本不足的日子同样计入低位（无统计意义=不达标）。
func TestEvaluateDemoteThinDays(t *testing.T) {
	days := []DailyStat{
		{IR: 0.9, WinRate: 90, Trades: 1},
		{IR: 0.9, WinRate: 90, Trades: 2},
		{IR: 0.9, WinRate: 90, Trades: 2},
	}
	if v := EvaluateDemote(days, DemoteOpts{}); v.Verdict != "disable" {
		t.Fatalf("连续薄样本应降级，得 %q", v.Verdict)
	}
}

// TestEvaluateDemoteNoData 无观测 → keep（不判定，不误杀）。
func TestEvaluateDemoteNoData(t *testing.T) {
	v := EvaluateDemote(nil, DemoteOpts{})
	if v.Verdict != "keep" || !strings.Contains(v.Reason, "无滚动指标") {
		t.Fatalf("无观测应 keep，得 %q/%q", v.Verdict, v.Reason)
	}
}

// TestCfgVerCandID 配置戳的候选 ID 段提取。
func TestCfgVerCandID(t *testing.T) {
	cases := map[string]string{
		"u1#evo1234": "evo1234",
		"u1#base":    "base",
		"evo1234":    "evo1234",
		"":           "",
	}
	for in, want := range cases {
		if got := cfgVerCandID(in); got != want {
			t.Fatalf("cfgVerCandID(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestDailyEquityDrawdown 净值曲线回撤：先涨后跌 10% → 回撤≈10%。
func TestDailyEquityDrawdown(t *testing.T) {
	// 两日：+10% → 净值 1.10；-10% → 0.99；回撤 = (1.10-0.99)/1.10 ≈ 10%。
	obs := []SizingSnapshot{
		{Day: "20260901", FwdRet: floatPtr(10)},
		{Day: "20260902", FwdRet: floatPtr(-10)},
	}
	if dd := dailyEquityDrawdown(obs); math.Abs(dd-10) > 0.01 {
		t.Fatalf("回撤 = %v, want ≈10", dd)
	}
}

func floatPtr(v float64) *float64 { return &v }
