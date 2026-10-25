// runner_test.go — §SIZING-EVO 运行器单测。
// 钉死：开关/冷启动不动配置、无达标候选不留痕、灰度入口幂等（同组合不重复进）、
// L2 自动晋升 vs 人工闸、回拒进已驳回集、衰退降级回默认并留记录。
package sizing_evo

import (
	"sort"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/signalctl"
)

// evoBaseOn 出厂配置 + evo 显式开启（evo 默认关，必须 owner 显式开）。
func evoBaseOn() config.SizingConfig {
	b := evoBase()
	on := true
	b.Evo.Enabled = &on
	return b
}

// seedEvoStore 把快照按日写入临时影子库（Samples 只返回可用样本）。
func seedEvoStore(t *testing.T, snaps []SizingSnapshot) *SnapshotStore {
	t.Helper()
	st := NewSnapshotStore(t.TempDir())
	byDay := map[string][]SizingSnapshot{}
	var order []string
	for _, s := range snaps {
		if _, ok := byDay[s.Day]; !ok {
			order = append(order, s.Day)
		}
		byDay[s.Day] = append(byDay[s.Day], s)
	}
	sort.Strings(order)
	for _, d := range order {
		if err := st.Append(d, byDay[d]...); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

// TestRunL1WeeklyDisabled 开关关闭 → 零动作。
func TestRunL1WeeklyDisabled(t *testing.T) {
	st := NewEvoStateStore(t.TempDir())
	store := seedEvoStore(t, evoSynthetic(5, 10, "20260901"))
	rep := RunL1Weekly(store, st, evoBase(), time.Now()) // Evo.Enabled=nil=关
	if rep.Enabled {
		t.Fatal("未启用时 Enabled 应为 false")
	}
	if got := st.GrayscaleCombos(); len(got) != 0 {
		t.Fatalf("未启用不得进灰度，得 %d", len(got))
	}
}

// TestRunL1WeeklyColdStart 样本不足 → 冷启动跳过，不动任何配置。
func TestRunL1WeeklyColdStart(t *testing.T) {
	st := NewEvoStateStore(t.TempDir())
	store := seedEvoStore(t, evoSynthetic(5, 10, "20260901")) // 50 < 200
	rep := RunL1Weekly(store, st, evoBaseOn(), time.Now())
	if !rep.Enabled {
		t.Fatal("已启用时 Enabled 应为 true")
	}
	if !strings.Contains(rep.Skipped, "冷启动") {
		t.Fatalf("应冷启动跳过，得 %q", rep.Skipped)
	}
	if rep.Entered != "" || len(st.GrayscaleCombos()) != 0 {
		t.Fatal("冷启动不得进灰度")
	}
}

// TestRunL1WeeklyEntersGrayscale 合成数据上坐标上升能找到过护栏的非基线候选 →
// 进灰度（带稳定 ID + 本交易日）；同组合再进幂等（不重复写）。
func TestRunL1WeeklyEntersGrayscale(t *testing.T) {
	st := NewEvoStateStore(t.TempDir())
	store := seedEvoStore(t, evoSynthetic(35, 10, "20260901"))
	now := time.Now()
	rep := RunL1Weekly(store, st, evoBaseOn(), now)
	if !rep.Enabled {
		t.Fatal("应启用")
	}
	if rep.Entered == "" {
		t.Fatalf("应有候选进灰度，得 %+v", rep)
	}
	got, _ := st.Load()
	if len(got.Grayscale) != 1 || got.Grayscale[0].CandID != rep.Entered {
		t.Fatalf("灰度条目错：%+v", got.Grayscale)
	}
	if got.Grayscale[0].EnteredAt != data.TradingDayDate(now) {
		t.Fatalf("进入日应为本交易日 %s，得 %s", data.TradingDayDate(now), got.Grayscale[0].EnteredAt)
	}
	// 同组合再进：幂等（返回空 ID + 跳过原因，不重复写）。
	if id, why, err := enterGrayscale(st, *rep.Best, now); err != nil || id != "" || !strings.Contains(why, "已在灰度") {
		t.Fatalf("同组合应幂等：id=%q why=%q err=%v", id, why, err)
	}
	if got, _ := st.Load(); len(got.Grayscale) != 1 {
		t.Fatalf("灰度条目应仍为 1，得 %d", len(got.Grayscale))
	}
}

// TestEnterGrayscaleIdempotent 灰度入口幂等：同组合第二次返回空 ID；已驳回组合也拒绝；
// 队列满时不再收新（探索成本有界）。
func TestEnterGrayscaleIdempotent(t *testing.T) {
	st := NewEvoStateStore(t.TempDir())
	best := L1Result{CandID: "coord-ascent", Params: map[string]float64{"kelly_lambda": 0.45}, InSample: ICStats{IR: 0.9}}
	now := time.Date(2026, 10, 25, 15, 0, 0, 0, time.Local)

	id1, why, err := enterGrayscale(st, best, now)
	if err != nil || id1 == "" || why != "" {
		t.Fatalf("首次应进灰度：id=%q why=%q err=%v", id1, why, err)
	}
	if id1 != (Candidate{Params: best.Params}).StableID() {
		t.Fatalf("ID 应为组合稳定 ID，得 %q", id1)
	}
	got, _ := st.Load()
	wantDay := data.TradingDayDate(now) // 周日跑 → 回落到上周五（交易日口径）
	if len(got.Grayscale) != 1 || got.Grayscale[0].CandID != id1 || got.Grayscale[0].EnteredAt != wantDay {
		t.Fatalf("灰度条目错：%+v（want EnteredAt=%s）", got.Grayscale, wantDay)
	}
	// 第二次：同组合已在灰度 → 空 ID 且不重复写。
	if id2, why2, err := enterGrayscale(st, best, now); err != nil || id2 != "" || !strings.Contains(why2, "已在灰度") {
		t.Fatalf("重复进灰度应幂等：id=%q why=%q err=%v", id2, why2, err)
	}
	if got, _ := st.Load(); len(got.Grayscale) != 1 {
		t.Fatalf("重复进灰度后条目 = %d, want 1", len(got.Grayscale))
	}
	// 队列满：再多的新组合也不收（已有 1 条，补到上限）。
	for i := 0; i < GrayscaleMax-1; i++ {
		other := L1Result{Params: map[string]float64{"cap": 1.0 + 0.05*float64(i)}}
		if id, _, err := enterGrayscale(st, other, now); err != nil || id == "" {
			t.Fatalf("队列未满应能进：i=%d id=%q err=%v", i, id, err)
		}
	}
	if got, _ := st.Load(); len(got.Grayscale) != GrayscaleMax {
		t.Fatalf("队列应满 %d，得 %d", GrayscaleMax, len(got.Grayscale))
	}
	full := L1Result{Params: map[string]float64{"floor": 0.1}}
	if id, why, err := enterGrayscale(st, full, now); err != nil || id != "" || !strings.Contains(why, "队列已满") {
		t.Fatalf("队列满应拒收：id=%q why=%q err=%v", id, why, err)
	}
	// 移除并驳回后：同组合不得再进（已判死）。
	if err := st.RemoveGrayscale(id1); err != nil {
		t.Fatal(err)
	}
	if err := st.Reject(ComboKey(best.Params)); err != nil {
		t.Fatal(err)
	}
	if id3, why3, err := enterGrayscale(st, best, now); err != nil || id3 != "" || !strings.Contains(why3, "驳回") {
		t.Fatalf("已驳回组合不得再进灰度：id=%q why=%q err=%v", id3, why3, err)
	}
}

// grayscaleState 造一个含单条灰度候选的状态库（L2 用例共用）。
func grayscaleState(t *testing.T, candID string, params map[string]float64) *EvoStateStore {
	t.Helper()
	st := NewEvoStateStore(t.TempDir())
	if err := st.AddGrayscale(GrayscaleEntry{CandID: candID, Params: params, EnteredAt: "20260901"}); err != nil {
		t.Fatal(err)
	}
	return st
}

// TestRunL2NightlyAutoPromote auto_promote=true：达标 → 写生效条目 + 移出灰度。
func TestRunL2NightlyAutoPromote(t *testing.T) {
	candID := "evo1234"
	params := map[string]float64{"kelly_lambda": 0.45}
	state := grayscaleState(t, candID, params)
	store := seedEvoStore(t, evoShadowSet(candID, 22, 10, "20260901", 1))
	base := evoBaseOn()
	auto := true
	base.Evo.AutoPromote = &auto
	rep := RunL2Nightly(store, state, base, time.Now())
	if len(rep.Promoted) != 1 || rep.Promoted[0] != candID {
		t.Fatalf("应自动晋升，得 %+v", rep)
	}
	applied := state.AppliedOf()
	if applied == nil || applied.CandID != candID || applied.CfgVer != "global#"+candID || !applied.Auto {
		t.Fatalf("生效条目错：%+v", applied)
	}
	if got := state.GrayscaleCombos(); len(got) != 0 {
		t.Fatalf("晋升后应移出灰度，得 %d", len(got))
	}
}

// TestRunL2NightlyAwaitingHuman auto_promote=false（首期默认）：达标只留痕，不动生效配置。
func TestRunL2NightlyAwaitingHuman(t *testing.T) {
	candID := "evo1234"
	state := grayscaleState(t, candID, map[string]float64{"kelly_lambda": 0.45})
	store := seedEvoStore(t, evoShadowSet(candID, 22, 10, "20260901", 1))
	rep := RunL2Nightly(store, state, evoBaseOn(), time.Now())
	if len(rep.AwaitingHuman) != 1 || rep.AwaitingHuman[0] != candID {
		t.Fatalf("应待人工审批，得 %+v", rep)
	}
	if state.AppliedOf() != nil {
		t.Fatal("人工闸下不得写生效配置")
	}
	got, _ := state.Load()
	if len(got.Grayscale) != 1 || !strings.Contains(got.Grayscale[0].Note, "待人工审批") {
		t.Fatalf("应保留灰度并标注待审批，得 %+v", got.Grayscale)
	}
}

// TestRunL2NightlyReject 反向加权 → 回拒 + 进已驳回集 + 移出灰度。
func TestRunL2NightlyReject(t *testing.T) {
	candID := "evo9999"
	params := map[string]float64{"cap": 1.2}
	state := grayscaleState(t, candID, params)
	store := seedEvoStore(t, evoShadowSet(candID, 22, 10, "20260901", -1))
	rep := RunL2Nightly(store, state, evoBaseOn(), time.Now())
	if len(rep.Rejected) != 1 || rep.Rejected[0] != candID {
		t.Fatalf("应回拒，得 %+v", rep)
	}
	if got := state.GrayscaleCombos(); len(got) != 0 {
		t.Fatalf("回拒后应移出灰度，得 %d", len(got))
	}
	rej := state.RejectedCombos()
	if len(rej) != 1 || !comboEqual(rej[0], ComboKey(params)) {
		t.Fatalf("应进已驳回集，得 %+v", rej)
	}
}

// TestRunL2NightlyDemote 生效配置连续 3 日衰退 → 回默认 + 降级记录。
func TestRunL2NightlyDemote(t *testing.T) {
	candID := "evo1234"
	state := NewEvoStateStore(t.TempDir())
	if err := state.SetApplied(&AppliedEntry{CandID: candID, Params: map[string]float64{"kelly_lambda": 0.45}, CfgVer: "global#" + candID, Auto: true}); err != nil {
		t.Fatal(err)
	}
	// 5 日 × 5 只、全负收益（胜率 0 < 35%）→ 连续低位。
	snaps := evoShadowSet("global#"+candID, 5, 5, "20260901", 1)
	for i := range snaps {
		snaps[i].Channel = "live"
		neg := -3.0
		snaps[i].FwdRet = &neg
	}
	store := seedEvoStore(t, snaps)
	rep := RunL2Nightly(store, state, evoBaseOn(), time.Now())
	if rep.Demoted == nil || rep.Demoted.Verdict != "disable" {
		t.Fatalf("应衰退降级，得 %+v", rep.Demoted)
	}
	if state.AppliedOf() != nil {
		t.Fatal("降级后应回默认配置（生效条目清空）")
	}
	got, _ := state.Load()
	if len(got.Demoted) != 1 || got.Demoted[0].CandID != candID {
		t.Fatalf("应留降级记录，得 %+v", got.Demoted)
	}
}

// TestRunL2NightlyDisabled 开关关闭 → 零动作（连灰度也不判）。
func TestRunL2NightlyDisabled(t *testing.T) {
	candID := "evo1234"
	state := grayscaleState(t, candID, map[string]float64{"kelly_lambda": 0.45})
	store := seedEvoStore(t, evoShadowSet(candID, 22, 10, "20260901", 1))
	rep := RunL2Nightly(store, state, evoBase(), time.Now())
	if len(rep.Promoted)+len(rep.AwaitingHuman)+len(rep.Rejected)+len(rep.Pending) != 0 {
		t.Fatalf("未启用不得判任何候选，得 %+v", rep)
	}
	if len(state.GrayscaleCombos()) != 1 {
		t.Fatal("未启用不得动灰度库")
	}
}

// TestRunL2NightlyDefaultIsSafeHarbor 无生效条目（出厂默认）时不判降级——默认配置是安全港。
func TestRunL2NightlyDefaultIsSafeHarbor(t *testing.T) {
	state := NewEvoStateStore(t.TempDir())
	snaps := evoShadowSet("global#base", 5, 5, "20260901", 1)
	for i := range snaps {
		snaps[i].Channel = "live"
		neg := -3.0
		snaps[i].FwdRet = &neg
	}
	store := seedEvoStore(t, snaps)
	rep := RunL2Nightly(store, state, evoBaseOn(), time.Now())
	if rep.Demoted != nil {
		t.Fatalf("默认配置不应被降级，得 %+v", rep.Demoted)
	}
}

// TestRunL1SummaryAndL2Summary 摘要文案含关键计数（审计日志可读）。
func TestRunL1SummaryAndL2Summary(t *testing.T) {
	if s := (L1RunReport{Enabled: false}).Summary(); !strings.Contains(s, "未启用") {
		t.Fatalf("未启用摘要错：%q", s)
	}
	s1 := L1RunReport{Enabled: true, Evaluated: 5, Passed: 2, Entered: "evo1", Best: &L1Result{InSample: ICStats{IR: 0.8}}}.Summary()
	if !strings.Contains(s1, "evo1") || !strings.Contains(s1, "0.800") {
		t.Fatalf("进灰度摘要错：%q", s1)
	}
	v := DemoteVerdict{RuleID: "evo1", Verdict: "disable", Reason: "连续3日"}
	s2 := L2RunReport{Promoted: []string{"a"}, Rejected: []string{"b", "c"}, Demoted: &v}.Summary()
	if !strings.Contains(s2, "晋升 1") || !strings.Contains(s2, "回拒 2") || !strings.Contains(s2, "衰退降级") {
		t.Fatalf("L2 摘要错：%q", s2)
	}
}

// TestEvoShadowUsesCandidateConfig 引擎侧 shadow 照算依赖 Candidate.Apply——这里锁死
// "候选配置真的会改变 scale"（否则 shadow 观测与 live 同值，灰度毫无意义）。
func TestEvoShadowUsesCandidateConfig(t *testing.T) {
	base := evoBase()
	ps := DefaultParamSpace()
	cand := Candidate{ID: "x", Params: map[string]float64{"kelly_lambda": 0.2}}
	cfg := cand.Apply(base, ps)
	reg := signalctl.Regime{SizeCfg: base, EmotionPhase: "发酵", MarketState: "bull", BreadthUpRatio: 0.6,
		Holiday: signalctl.HolidayWindow{CalendarLoaded: true}}
	sig := combat_agent.Signal{Code: "600519", Price: 10, ATR: 0.2, Confidence: 0.8, QualityWeight: 1.0}
	live := signalctl.ComputeSizing(reg, sig)
	reg.SizeCfg = cfg
	shadow := signalctl.ComputeSizing(reg, sig)
	if live.Scale == shadow.Scale {
		t.Fatalf("候选配置未改变 scale：%v vs %v", live.Scale, shadow.Scale)
	}
}
