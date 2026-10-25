// sizing_evo.go — §SIZING-EVO 命令行入口（周度 L1 快筛 / 夜间 L2 灰度+降级）。
//
// 调度形态（§2.7.4）：周度跑 `sizing-evo --weekly`，夜间链尾部跑 `sizing-evo --nightly`。
// 与 research 生命周期**完全独立**：不读战法库/灰度库/候选库，自己的状态与影子库都在
// <dataDir>/sizing_evo 下（§2.7.0 铁律；架构测试 internal/sizing_evo/arch_test.go 锁定）。
//
// 与引擎的关系：本进程只写状态文件与影子库；正在跑的引擎按 TTL(30s) 读到生效条目后
// 热加载（internal/engine/sizing_evo_track.go），与战法库轮询同一套"研究进程改库、
// 引擎进程读库"的分工。
//
// English: CLI entry for the sizing evolution loop — weekly L1 screening and the nightly
// L2/demotion pass; fully independent from the research lifecycle (own state + shadow store).
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/opslog"
	"quant-trading-v2/internal/sizing_evo"
	"quant-trading-v2/internal/store"
)

// cmdSizingEvo 动态仓位进化：周度 L1 / 夜间 L2。
// db 未使用（本回路只消费影子库），保留签名与其它子命令一致。
func cmdSizingEvo(db *store.DB, dbPath string, args []string) {
	fs := flag.NewFlagSet("sizing-evo", flag.ExitOnError)
	dataDir := fs.String("data-dir", "", "数据目录（缺省=研究库所在目录，即 config.json 与 sizing_evo/ 的父目录）")
	weekly := fs.Bool("weekly", false, "跑周度 L1 离线快筛（候选→护栏→进灰度）")
	nightly := fs.Bool("nightly", false, "跑夜间 L2（灰度晋升/回拒 + 生效配置衰退降级）")
	r4 := fs.Bool("r4", false, "R4 分布标定：读影子库真实横截面净流入率样本，做档位占比标定")
	r08 := fs.Bool("r08", false, "R-08 因子值取证：扫描影子库确认 annotations 已落因子值（flow_ratio_5d/today）")
	day := fs.String("day", "", "R4/R-08 限定交易日 YYYYMMDD（缺省=全库汇总）")
	dryRun := fs.Bool("dry-run", false, "只评估不落库（状态写入临时目录，跑完丢弃）")
	fs.Parse(args)
	if !*weekly && !*nightly && !*r4 && !*r08 {
		log.Fatalf("[sizing-evo] 需指定 --weekly / --nightly / --r4 / --r08 之一")
	}
	dir := *dataDir
	if dir == "" {
		dir = filepath.Dir(dbPath)
	}
	opslog.Init(dir, 0)

	// 生效配置 = 账号级 sizing（R7 首期全局单回路：空账号取全局键）。
	cm := config.NewManager(filepath.Join(dir, "config.json"))
	base, _ := cm.GetSizingConfigFor("")
	base = base.WithDefaults()
	if !base.EvoOn() {
		log.Printf("[sizing-evo] sizing.evo.enabled 未开启，跳过（这是出厂默认：必须显式开）")
		return
	}

	evoDir := filepath.Join(dir, "sizing_evo")
	// R4/R-08 取证始终读真实影子库（只读诊断，绝不写）；weekly/nightly 的状态落库按 dry-run 选真实或临时。
	snaps := sizing_evo.NewSnapshotStore(evoDir)
	storeDir := evoDir
	if *dryRun {
		tmp, err := os.MkdirTemp("", "sizing-evo-dry")
		if err != nil {
			log.Fatalf("[sizing-evo] 建临时目录失败: %v", err)
		}
		defer os.RemoveAll(tmp) // 尽力清理；清理失败不影响结论
		storeDir = tmp
		log.Printf("[sizing-evo] dry-run：状态写 %s（跑完丢弃）", tmp)
	}
	state := sizing_evo.NewEvoStateStore(storeDir)
	now := time.Now()

	if *r4 {
		runEvoR4(snaps, base, *day)
		return
	}
	if *r08 {
		runEvoR08(snaps, *day)
		return
	}

	if *weekly {
		rep := sizing_evo.RunL1Weekly(snaps, state, base, now)
		log.Printf("[sizing-evo] %s", rep.Summary())
	}
	if *nightly {
		rep := sizing_evo.RunL2Nightly(snaps, state, base, now)
		log.Printf("[sizing-evo] %s", rep.Summary())
		for _, id := range rep.Promoted {
			log.Printf("[sizing-evo] ✅ 已晋升生效：%s（引擎 30s 内热加载）", id)
		}
		for _, id := range rep.AwaitingHuman {
			log.Printf("[sizing-evo] ⏳ 灰度达标待人工审批：%s（auto_promote=false）", id)
		}
		for _, id := range rep.Rejected {
			log.Printf("[sizing-evo] ❌ 灰度回拒：%s", id)
		}
		if rep.Demoted != nil {
			log.Printf("[sizing-evo] ⚠️ %s", sizing_evo.DemoteSummary(*rep.Demoted))
		}
	}
	// 库规模观测（冷启动判定依据）。
	if c, err := snaps.Counts(); err == nil {
		log.Printf("[sizing-evo] 影子库：%d 快照 / %d 已结算 / %d 交易日", c.Total, c.Settled, c.Days)
	}
}

// runEvoR4 §R4 分布标定：读影子库真实横截面净流入率样本，按当前生效配置档位阈值做档位占比标定。
// 只读影子库，不落任何状态。样本取自全部交易日（或 --day 指定日）的非弃权（FlowRatio5D!=0）快照。
// English: R4 band calibration — reads the real cross-sectional net-flow sample from the shadow store
// and reports band occupancy against the deployed thresholds. Read-only.
func runEvoR4(snaps *sizing_evo.SnapshotStore, base config.SizingConfig, day string) {
	ratios, err := snaps.FlowRatios(day)
	if err != nil {
		log.Fatalf("[sizing-evo] R4 采样失败: %v", err)
	}
	scope := "全库汇总"
	if day != "" {
		scope = "交易日 " + day
	}
	if len(ratios) == 0 {
		log.Printf("[sizing-evo] R4 标定（%s）：样本为空——影子库暂无可定权快照（FlowRatio5D!=0）。"+
			"需等盘中产生买入信号后，于盘后由调度器自动重跑。", scope)
		return
	}
	report := calibrateBands(ratios, base.FlowFactor.Buy5DBands)
	log.Printf("[sizing-evo] R4 分布标定（%s，样本 %d 条）:\n%s", scope, len(ratios), report)
}

// runEvoR08 §R-08 因子值取证：扫描影子库前 N 条含因子值的记录，证明确认 annotations 已落因子值。
// 只读，不写库。English: R-08 forensic — lists the first N shadow-store records that actually carry a
// factor value, proving annotations landed the flow-ratio factors.
func runEvoR08(snaps *sizing_evo.SnapshotStore, day string) {
	hits, err := snaps.ForensicRecords(day, 20)
	if err != nil {
		log.Fatalf("[sizing-evo] R-08 取证失败: %v", err)
	}
	if len(hits) == 0 {
		log.Printf("[sizing-evo] R-08 取证：影子库暂无可因子值记录（FlowRatio5D!=0）。" +
			"需等盘中产生买入信号后，于盘后由调度器自动重跑。")
		return
	}
	log.Printf("[sizing-evo] R-08 因子值取证（前 %d 条，全库/指定日均可读）:", len(hits))
	for i, h := range hits {
		log.Printf("  [%d] day=%s code=%s channel=%s cfg_ver=%s flow5d=%+.3f%% flowToday=%+.3f%% scale=%.4f",
			i+1, h.Day, h.Code, h.Channel, h.CfgVer, h.FlowRatio5D, h.FlowRatioToday, h.Scale)
	}
}

// calibrateBands R4 分布标定：样本分位 + 按部署阈值（Buy5DBands）的档位占比。
// 阈值含端点归严（与 signalctl.flowBandOf 同口径："达到即入"）。English: R4 calibration — sample
// quantiles plus per-band occupancy against the deployed band thresholds.
func calibrateBands(ratios []float64, bands map[string]float64) string {
	strongIn := bands["strong_in_pct"]
	mildIn := bands["mild_in_pct"]
	mildOut := bands["mild_out_pct"]
	strongOut := bands["strong_out_pct"]

	sorted := make([]float64, len(ratios))
	copy(sorted, ratios)
	sort.Float64s(sorted)
	n := len(sorted)

	var b strings.Builder
	fmt.Fprintf(&b, "# R4 净流入率分布标定（样本 %d 条）\n\n", n)
	fmt.Fprintf(&b, "| 分位 | 净流入率 |\n|---|---|\n")
	for _, q := range []struct {
		name string
		p    float64
	}{{"min", 0}, {"p10", 0.10}, {"p25", 0.25}, {"median", 0.50}, {"p75", 0.75}, {"p90", 0.90}, {"max", 1}} {
		fmt.Fprintf(&b, "| %s | %+.3f%% |\n", q.name, quantile(sorted, q.p))
	}
	fmt.Fprintf(&b, "\n## 各档位占比（按部署阈值 strong_in≥%.0f%% / mild_in≥%.0f%% / mild_out<%.0f%% / strong_out<%.0f%%）\n\n",
		strongIn, mildIn, mildOut, strongOut)
	b.WriteString("| 档位 | 占比 |\n|---|---|\n")
	counts := map[string]int{}
	for _, r := range sorted {
		counts[bandOfRatio(r, strongIn, mildIn, mildOut, strongOut)]++
	}
	for _, name := range []string{"strong_in", "mild_in", "neutral", "mild_out", "strong_out"} {
		fmt.Fprintf(&b, "| %s | %.1f%% |\n", name, float64(counts[name])/float64(n)*100)
	}
	b.WriteString("\n判读：强档（strong_in/strong_out）合计占比 10%~30% 时档位区分度较好；" +
		"若 neutral 占比 >70%，说明阈值过宽、因子几乎不起作用（R4 需重新标定阈值）。\n")
	return b.String()
}

// bandOfRatio 按 5 日档位阈值归类（阈值含端点归严，与 signalctl.flowBandOf 同口径）。
func bandOfRatio(ratio, strongIn, mildIn, mildOut, strongOut float64) string {
	switch {
	case ratio >= strongIn:
		return "strong_in"
	case ratio >= mildIn:
		return "mild_in"
	case ratio > mildOut:
		return "neutral"
	case ratio > strongOut:
		return "mild_out"
	default:
		return "strong_out"
	}
}

// quantile 线性插值分位（0..1；样本已排序）。
func quantile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 1 {
		return sorted[len(sorted)-1]
	}
	pos := p * float64(len(sorted)-1)
	lo := int(pos)
	frac := pos - float64(lo)
	if lo+1 >= len(sorted) {
		return sorted[lo]
	}
	return sorted[lo]*(1-frac) + sorted[lo+1]*frac
}
