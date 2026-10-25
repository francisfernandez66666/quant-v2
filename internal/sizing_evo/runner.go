// runner.go — §SIZING-EVO 运行器：周度 L1 离线快筛 + 夜间 L2 灰度/降级。
//
// 调度形态（§2.7.4，与 research 周例行/夜间链同构但独立）：
//   - 周度 RunL1Weekly：冷启动门 → 候选生成（坐标上升+变异，去重：已驳回+已在灰度）
//     → 护栏评估 → 最优进**灰度**（只留痕，不动生效配置）；
//   - 夜间 RunL2Nightly：① 灰度候选逐个判 promote/reject/pending——auto_promote=false
//     （首期默认）时候选留痕待人工审批；true 时写生效条目热加载 + opslog 告警；
//     ② 已生效配置逐日滚动指标判衰退 → 连续 N 日低于阈值**自动回默认配置** + 告警。
//
// 纪律（§2.7.5）：每次进化运行落一行 opslog（候选数/达标数/结论），连续失明可告警；
// evo.enabled=false 一键冻结（两个入口都先查开关）；任何演化产出不突破 §2.7.1 围栏
// （Clamp 在 Candidate.Apply 内强制执行）。
//
// English: the sizing_evo runner — weekly L1 screening (candidates enter grayscale only)
// and the nightly L2 pass (promote/reject + auto-demote back to default).
package sizing_evo

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/opslog"
)

// evoRunAccount 首期全局单回路（R7）：晋升/降级都按这一个回路归组。
// English: the single global loop for the first phase (R7).
const evoRunAccount = "global"

// GrayscaleMax 灰度队列上限：每条灰度候选每轮都要为每个买入信号多算一次 scale
// （引擎侧 shadow 照算），无上限会把"探索"变成"成本放大"。3 条足够覆盖一周的探索。
// English: grayscale queue cap — each candidate costs one extra ComputeSizing per signal
// per round on the engine side, so exploration must stay bounded.
const GrayscaleMax = 3

// L1RunReport 周度 L1 运行报告（opslog 与任务输出消费）。
// English: the weekly L1 run report (for opslog/task output).
type L1RunReport struct {
	Enabled   bool      `json:"enabled"`
	Counts    Counts    `json:"counts"`
	Samples   int       `json:"samples"`
	Evaluated int       `json:"evaluated"` // 参与护栏评估的候选数（基线除外）
	Passed    int       `json:"passed"`    // 过护栏候选数
	Best      *L1Result `json:"best,omitempty"`
	Entered   string    `json:"entered,omitempty"` // 新进灰度候选 ID（空=无新进）
	Skipped   string    `json:"skipped,omitempty"` // 跳过原因（冷启动/无达标/已在灰度）
}

// Summary 一行式运行摘要（审计日志）。
// English: one-line run summary for audit logs.
func (r L1RunReport) Summary() string {
	if !r.Enabled {
		return "sizing_evo 未启用（跳过）"
	}
	if r.Skipped != "" {
		return fmt.Sprintf("周度L1：%s（库内 %d 快照/%d 日）", r.Skipped, r.Counts.Total, r.Counts.Days)
	}
	if r.Entered == "" {
		return fmt.Sprintf("周度L1：评估 %d 候选 %d 达标，无新进灰度", r.Evaluated, r.Passed)
	}
	ir := 0.0
	if r.Best != nil {
		ir = r.Best.InSample.IR
	}
	return fmt.Sprintf("周度L1：评估 %d 候选 %d 达标，最优 %s 进灰度（样本内IR=%.3f）", r.Evaluated, r.Passed, r.Entered, ir)
}

// saveRunReport 把一次运行报告落盘（<evoDir>/report_weekly.json / report_nightly.json）。
// 前端"仓位进化"面板与运维巡检都读这份文件——报告是观测面，写失败不影响运行结论。
// English: persists one run report for the frontend panel and ops inspection.
func saveRunReport(dir, kind string, v any) {
	if strings.TrimSpace(dir) == "" {
		return
	}
	b, err := json.MarshalIndent(map[string]any{
		"kind": kind, "at": time.Now().Format("2006-01-02 15:04:05"), "report": v,
	}, "", "  ")
	if err != nil {
		return
	}
	path := filepath.Join(dir, "report_"+kind+".json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
	}
}

// RunL1Weekly 周度 L1 主入口（确定性：同库同配置同结果）。
//
// 冷启动纪律：可用样本 < EvoConfig.MinSamples → 不动任何配置、不进灰度、只留痕。
// 去重：已驳回组合（RejectedCombos）+ 已在灰度组合（GrayscaleCombos）不重复生成——
// "进化不能把同一个错误每周重犯"，也不对同一组合反复开观测。
// English: the weekly L1 entry — cold-start gate, deduped candidate generation, guard
// evaluation, and the best candidate entering grayscale (never the live config).
func RunL1Weekly(store *SnapshotStore, state *EvoStateStore, base config.SizingConfig, now time.Time) L1RunReport {
	rep := L1RunReport{}
	defer func() { saveRunReport(store.Dir(), "weekly", rep) }()
	if !base.EvoOn() {
		rep.Skipped = "evo 未启用"
		return rep
	}
	rep.Enabled = true
	evo := base.Evo.WithDefaults()
	if counts, err := store.Counts(); err == nil {
		rep.Counts = counts
	}
	snaps, err := store.Samples()
	if err != nil {
		rep.Skipped = "影子库读取失败: " + err.Error()
		opslog.Logf("sizing_evo", "周度L1异常：%s", rep.Skipped)
		return rep
	}
	rep.Samples = len(snaps)
	if len(snaps) < evo.MinSamples {
		rep.Skipped = fmt.Sprintf("冷启动：可用样本 %d < %d，不动配置", len(snaps), evo.MinSamples)
		opslog.Logf("sizing_evo", "周度L1跳过（%s）", rep.Skipped)
		return rep
	}
	ps := DefaultParamSpace().WithBounds(base.Evo.SearchBounds)
	exclude := append(state.RejectedCombos(), state.GrayscaleCombos()...)
	l1opts := L1Opts{MinSamples: evo.MinSamples}
	eval := func(c Candidate) (float64, bool) {
		r := EvaluateCandidate(base, snaps, c, ps, l1opts)
		return r.Objective, r.PassGuard
	}
	cands := Dedup(GenerateCandidates(base, ps, eval, GenerateOpts{}), exclude)
	var results []L1Result
	for _, c := range cands {
		if c.Origin == "baseline" {
			continue // 基线不是进化候选（它是"什么都不做"的对照）
		}
		results = append(results, EvaluateCandidate(base, snaps, c, ps, l1opts))
	}
	rep.Evaluated = len(results)
	for _, r := range results {
		if r.PassGuard {
			rep.Passed++
		}
	}
	best, ok := BestCandidate(results)
	if !ok {
		rep.Skipped = "本周无达标候选（护栏全拒）"
		opslog.Logf("sizing_evo", "周度L1：评估 %d 候选，无一过护栏（库内 %d 快照）", len(results), rep.Counts.Total)
		return rep
	}
	rep.Best = &best
	// 铸稳定 ID（同组合跨周同 ID）后进灰度——只留痕，生效配置一律等 L2 灰度判定。
	id, why, err := enterGrayscale(state, best, now)
	if err != nil {
		rep.Skipped = "灰度入库失败: " + err.Error()
		opslog.Logf("sizing_evo", "周度L1异常：%s", rep.Skipped)
		return rep
	}
	if id == "" {
		rep.Skipped = why
		opslog.Logf("sizing_evo", "周度L1：评估 %d 候选 %d 达标，%s", len(results), rep.Passed, why)
		return rep
	}
	rep.Entered = id
	opslog.Logf("sizing_evo", "周度L1：评估 %d 候选 %d 达标，最优 %s 进灰度（样本内IR=%.3f hold-outIR=%.3f 样本=%d）",
		len(results), rep.Passed, id, best.InSample.IR, best.Holdout.IR, best.Samples)
	// 顺手修剪影子库（只增不删的唯一例外，防无界增长）。
	if n, err := store.Prune(0); err == nil && n > 0 {
		opslog.Logf("sizing_evo", "影子库修剪 %d 个旧日文件", n)
	}
	return rep
}

// enterGrayscale 把 L1 最优候选写进灰度库（幂等 + 队列上限）。
//
// 稳定 ID = 参数组合指纹派生（同组合跨周同 ID）；同组合已在灰度或已驳回、或灰度队列
// 已满 → 返回空 ID + 跳过原因且不写库——避免对同一组合反复开观测、把已判死的组合重新
// 拉回，或让探索把引擎的 shadow 照算成本放大到无界。
// English: writes the L1 winner into grayscale (idempotent, queue-capped); an empty ID
// plus a reason means nothing was written.
func enterGrayscale(state *EvoStateStore, best L1Result, now time.Time) (string, string, error) {
	fp := ComboKey(best.Params)
	for _, e := range state.RejectedCombos() {
		if comboEqual(fp, e) {
			return "", "最优组合已被驳回（不再重复进灰度）", nil
		}
	}
	combos := state.GrayscaleCombos()
	for _, e := range combos {
		if comboEqual(fp, e) {
			return "", "最优组合已在灰度观测中（不重复进）", nil
		}
	}
	if len(combos) >= GrayscaleMax {
		return "", fmt.Sprintf("灰度队列已满（%d/%d），等 L2 判定消化后再进", len(combos), GrayscaleMax), nil
	}
	cand := Candidate{Params: best.Params}
	id := cand.StableID()
	err := state.AddGrayscale(GrayscaleEntry{
		CandID:    id,
		Params:    best.Params,
		EnteredAt: data.TradingDayDate(now),
		Note:      fmt.Sprintf("L1最优（样本内IR=%.3f，hold-out IR=%.3f）", best.InSample.IR, best.Holdout.IR),
	})
	if err != nil {
		return "", "", err
	}
	return id, "", nil
}

// L2RunReport 夜间 L2 运行报告。
// English: the nightly L2 run report.
type L2RunReport struct {
	Promoted      []string       `json:"promoted,omitempty"`       // 自动晋升并生效
	AwaitingHuman []string       `json:"awaiting_human,omitempty"` // 达标但 auto_promote=false，待人工审批
	Rejected      []string       `json:"rejected,omitempty"`       // 回拒（进已驳回集）
	Pending       []string       `json:"pending,omitempty"`        // 观察期未满/无观测
	Demoted       *DemoteVerdict `json:"demoted,omitempty"`        // 非空=发生衰退降级（已回默认）
}

// Summary 一行式运行摘要（审计日志）。
// English: one-line run summary for audit logs.
func (r L2RunReport) Summary() string {
	s := fmt.Sprintf("夜间L2：晋升 %d / 待人工 %d / 回拒 %d / 观测中 %d",
		len(r.Promoted), len(r.AwaitingHuman), len(r.Rejected), len(r.Pending))
	if r.Demoted != nil {
		s += "；" + DemoteSummary(*r.Demoted)
	}
	return s
}

// RunL2Nightly 夜间链主入口（确定性）。
//
// 灰度判定用 shadow 通道快照（引擎按候选配置照算留痕的那批）；晋升写生效条目
// （引擎 TTL 内热加载）；回拒进已驳回集。衰退判定用 live/paper 通道快照的逐日
// 滚动指标，连续 N 日低于阈值 → 生效条目清空（回出厂默认）+ 降级记录 + 告警。
//
// 人工闸（§2.7.5）：auto_promote=false 时 promote 判定只留痕（Note 标注待审批），
// 不碰生效配置——owner 随时可据此人工审批或继续冻结。
// English: the nightly entry — grayscale verdicts (auto-promote or hold for human
// approval) plus rolling-decline demotion back to the factory default.
func RunL2Nightly(store *SnapshotStore, state *EvoStateStore, base config.SizingConfig, now time.Time) L2RunReport {
	rep := L2RunReport{}
	defer func() { saveRunReport(store.Dir(), "nightly", rep) }()
	if !base.EvoOn() {
		return rep
	}
	evo := base.Evo.WithDefaults()
	st, err := state.Load()
	if err != nil {
		opslog.Logf("sizing_evo", "夜间L2异常：状态库读取失败 %v", err)
		return rep
	}
	snaps, err := store.Samples()
	if err != nil {
		opslog.Logf("sizing_evo", "夜间L2异常：影子库读取失败 %v", err)
		return rep
	}
	l2 := L2Opts{ObservationDays: evo.ObservationDays}
	auto := base.EvoAutoPromoteOn()
	day := data.TradingDayDate(now)
	for _, g := range st.Grayscale {
		v := EvaluateGrayscale(snaps, g.CandID, l2)
		switch v.Verdict {
		case "promote":
			if !auto {
				// 首期默认：只留痕待人工审批，不动生效配置（人工闸）。
				_ = state.AddGrayscale(GrayscaleEntry{
					CandID: g.CandID, Params: g.Params, EnteredAt: g.EnteredAt,
					Note: "灰度达标待人工审批：" + v.Reason,
				})
				rep.AwaitingHuman = append(rep.AwaitingHuman, g.CandID)
				opslog.Logf("sizing_evo", "灰度达标待人工审批 %s（auto_promote=false 不自动生效）：%s", g.CandID, v.Reason)
				continue
			}
			entry := &AppliedEntry{
				CandID: g.CandID, Params: g.Params, AppliedAt: day,
				CfgVer: CfgVerOf(evoRunAccount, g.CandID), Auto: true, Account: evoRunAccount,
			}
			if err := state.SetApplied(entry); err != nil {
				opslog.Logf("sizing_evo", "自动晋升落库失败 %s：%v", g.CandID, err)
				continue
			}
			_ = state.RemoveGrayscale(g.CandID)
			rep.Promoted = append(rep.Promoted, g.CandID)
			opslog.Logf("sizing_evo", "⚠️ 自动晋升 %s 已生效（引擎 TTL 内热加载）：%s", g.CandID, v.Reason)
		case "reject":
			_ = state.RemoveGrayscale(g.CandID)
			_ = state.Reject(ComboKey(g.Params))
			rep.Rejected = append(rep.Rejected, g.CandID)
			opslog.Logf("sizing_evo", "灰度回拒 %s（进已驳回集）：%s", g.CandID, v.Reason)
		default:
			rep.Pending = append(rep.Pending, g.CandID)
		}
	}
	// —— 衰退自动降级：已生效配置逐日滚动指标，连续衰退 → 回默认 + 告警 ——
	// Applied=nil（出厂默认）时不判——默认配置是安全港，没有"可降级的对象"。
	if st.Applied != nil {
		days := LiveDailyStats(snaps, st.Applied.CandID)
		v := EvaluateDemote(days, DemoteOpts{})
		v.RuleID = st.Applied.CandID
		if v.Verdict == "disable" {
			if err := state.SetApplied(nil); err != nil {
				opslog.Logf("sizing_evo", "衰退降级落库失败 %s：%v", v.RuleID, err)
			} else {
				_ = state.AddDemote(DemoteRecord{CandID: v.RuleID, At: day, Reason: v.Reason})
				rep.Demoted = &v
				opslog.Logf("sizing_evo", "⚠️ 衰退自动降级 %s 已回默认配置：%s", v.RuleID, v.Reason)
			}
		}
	}
	return rep
}
