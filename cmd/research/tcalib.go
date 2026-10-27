// tcalib.go — §T-COST-CALIB **B2** 的只读子命令（`research tcalib`，2026-10-27）。
//
// 职责边界（三条，越界即事故）：
//  1. **只读**：只查 minute_klines 与配置，不写库、不写配置、不接交易进程；
//  2. **产物固定**：`tcalib_YYYYMMDD.json`（机读）+ `TCALIB_REPORT_YYYYMMDD.md`（人读），
//     文件名里的日期是**样本最后交易日**，不是运行时刻——这样重跑会覆盖同一份文件，
//     不会每次生成一个新文件把目录搞乱；
//  3. **不自动落配置**：阈值只写进报告，签字后由 owner 在设置页写 `sizing.t.calib`。
//
// 用法：
//
//	research tcalib [--codes 文件] [--limit N] [--min-days 60] [--tiers 4] [--min-sample 30]
//	                [--calib-days 60] [--oos-days 11] [--slip-bps 5] [--out ./research_out] [--dry-run]
//
// 典型调用顺序（§4.1「先跑再谈」）：
//
//  1. 先不带 --slip-bps 跑一次，看步骤 0 覆盖率够不够（不够就先回填分钟线）；
//  2. 覆盖率达标后，从 B1 的实盘账本取该档滑点中位数，带 --slip-bps 重跑；
//  3. 看报告"可签字"列，再决定签不签。
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"quant-trading-v2/internal/store"
	"quant-trading-v2/internal/tcalib"
)

// tcalibScale 分钟周期：本工具只标定 5 分钟口径（与实盘动量判据同源）。
const tcalibScale = 5

// tcalibMinBarsPerDay 一天至少这么多根才当成"完整交易日"喂进样本。
// 48 根是 5 分钟口径的整日根数；低于 30 根通常是上游窗口被截，
// 把它算成一天会拉低分位（"缺半天"不等于"波动小"）。
const tcalibMinBarsPerDay = 30

// cmdTCalib 子命令入口：解析参数 → 读分钟线 → 调 tcalib.Calibrate → 落两份产物。
// English: cmdTCalib is the read-only `research tcalib` subcommand.
func cmdTCalib(db *store.DB, args []string) {
	fs := flag.NewFlagSet("tcalib", flag.ExitOnError)
	codesFile := fs.String("codes", "", "研究池文件（每行一个 ts_code，# 注释）；空=库里所有有分钟线的票")
	limit := fs.Int("limit", 0, "最多标定多少只票（0=不限；分钟表可能上千只，先小样本验证再全量）")
	minDays := fs.Int("min-days", tcalib.DefaultMinDays, "步骤 0：每票最少交易日数")
	tiers := fs.Int("tiers", tcalib.DefaultTiers, "分档数（4 档=按滑窗日均成交额四分位）")
	minSample := fs.Int("min-sample", tcalib.DefaultMinSample, "单档最小样本数（不足则标『不替换』）")
	calibDays := fs.Int("calib-days", tcalib.DefaultCalibDays, "标定窗长度（交易日）")
	oosDays := fs.Int("oos-days", tcalib.DefaultOOSDays, "验证窗长度（交易日，T…T+10 ⇒ 11）")
	slipBps := fs.Float64("slip-bps", tcalib.DefaultSlipBps, "单边滑点（bp）：取 B1 实盘账本该档中位数；0=未计入（报告会标注偏乐观）")
	commBps := fs.Float64("comm-bps", tcalib.DefaultCommBps, "单边佣金（bp，出厂万 2.5）")
	stampBps := fs.Float64("stamp-bps", tcalib.DefaultStampBps, "卖出印花税（bp，出厂万 5）")
	safety := fs.Float64("safety-mul", tcalib.DefaultSafetyMul, "价差门槛的安全边际倍数")
	outDir := fs.String("out", "./research_out", "产物输出目录")
	dryRun := fs.Bool("dry-run", false, "只打印结论，不落盘（用于确认覆盖率）")
	if err := fs.Parse(args); err != nil {
		log.Fatalf("tcalib 参数解析失败: %v", err)
	}

	params := tcalib.Params{
		MinDays: *minDays, Tiers: *tiers, MinSample: *minSample,
		CalibDays: *calibDays, OOSDays: *oosDays,
		CommBps: *commBps, StampBps: *stampBps, SlipBps: *slipBps, SafetyMul: *safety,
	}

	// 取池：显式 --codes 优先（可复现的标定必须能钉死名单），否则用库里全部有分钟线的票。
	codes, err := tcalibUniverse(db, *codesFile, *limit)
	if err != nil {
		log.Fatalf("读取标定票池失败: %v", err)
	}
	if len(codes) == 0 {
		log.Fatalf("标定票池为空：分钟表没有数据（先跑分钟线回填），或用 --codes 指定名单")
	}
	log.Printf("§T-CALIB 票池 %d 只（周期 %d 分钟，天数门槛 %d）", len(codes), tcalibScale, params.MinDays)

	// 逐票读分钟线。天数不够的票**不读明细**（省掉绝大部分 IO），只把"天数"喂给覆盖率，
	// 这样报告里仍能逐条列出它们差多少天。
	samples := make([]tcalib.CodeDays, 0, len(codes))
	for i, code := range codes {
		cd, err := tcalibLoadCode(db, code, params.MinDays)
		if err != nil {
			// 单票读失败不中断整批：覆盖率报告里它会体现为"天数不足"，
			// 而整批中止会让一次跑了几分钟的任务白费（这是运维成本，不是严谨）。
			log.Printf("[tcalib] %s 读取失败，按缺数据处理: %v", code, err)
			continue
		}
		samples = append(samples, cd)
		if (i+1)%50 == 0 {
			log.Printf("[tcalib] 已读 %d/%d 只", i+1, len(codes))
		}
	}

	rep, err := tcalib.Calibrate(samples, params)
	if err != nil {
		// 窗口放不下不是"失败"，是"数据不足"：结论已写进 rep.Verdict，
		// 这里只提示，产物照常落盘（覆盖率报告本身就是本次交付物）。
		log.Printf("§T-CALIB 结论：%s", rep.Verdict)
		log.Printf("§T-CALIB 窗口不足：%v", err)
	} else {
		log.Printf("§T-CALIB 结论：%s", rep.Verdict)
	}

	tcalibPrintSummary(rep)
	if *dryRun {
		log.Printf("--dry-run：不落盘")
		return
	}
	jsonPath, mdPath, err := tcalibWrite(rep, *outDir)
	if err != nil {
		log.Fatalf("写产物失败: %v", err)
	}
	log.Printf("产物：%s", jsonPath)
	log.Printf("产物：%s", mdPath)
}

// tcalibUniverse 决定标定票池：--codes 文件优先，否则取库里所有有 5 分钟线的票（可按 limit 截断）。
// 排序一律升序，保证"同输入同顺序"，这是确定性锁的前置条件。
// English: tcalibUniverse resolves the code universe (explicit file first, else all minute codes).
func tcalibUniverse(db *store.DB, codesFile string, limit int) ([]string, error) {
	var codes []string
	if codesFile != "" {
		got, err := readCodesFile(codesFile)
		if err != nil {
			return nil, err
		}
		codes = got
	} else {
		got, err := db.MinuteCodes(tcalibScale)
		if err != nil {
			return nil, err
		}
		codes = got
	}
	// 去重 + 升序：名单里重复一行不该让同一只票被算两次（那会虚增样本数）。
	seen := make(map[string]bool, len(codes))
	uniq := make([]string, 0, len(codes))
	for _, c := range codes {
		c = strings.TrimSpace(c)
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		uniq = append(uniq, c)
	}
	sort.Strings(uniq)
	if limit > 0 && len(uniq) > limit {
		uniq = uniq[:limit]
	}
	return uniq, nil
}

// tcalibLoadCode 读一只票的分钟样本日。
//
// 两段式：先用 MinuteDayCounts 拿"每天多少根"，只对根数达标的日子取明细。
// 天数 < minDays 时**只回填占位日**（Day 有值、Bars 为空）：覆盖率照样能报"差几天"，
// 但省掉成百上千次按日查询——这是这个工具能在几千只票上跑完的关键。
// English: tcalibLoadCode loads one code's minute days; under-covered codes only get placeholder days.
func tcalibLoadCode(db *store.DB, code string, minDays int) (tcalib.CodeDays, error) {
	cd := tcalib.CodeDays{Code: code}
	counts, err := db.MinuteDayCounts(code, tcalibScale)
	if err != nil {
		return cd, err
	}
	// 先把"根数达标"的日子挑出来（顺序按 Day 升序，MinuteDayCounts 已排序）。
	full := make([]string, 0, len(counts))
	for _, c := range counts {
		if c.Count >= tcalibMinBarsPerDay {
			full = append(full, c.Day)
		}
	}
	if len(full) < minDays {
		// 不足门槛：用全部有数据的日子做占位（含根数不足的日子，它们同样是"覆盖天数"）。
		for _, c := range counts {
			cd.Days = append(cd.Days, tcalib.DayBars{Day: c.Day})
		}
		return cd, nil
	}
	// 达标：逐日取明细并转成 tcalib.Bar（store.MinuteBar → tcalib.Bar 是同形映射）。
	for _, day := range full {
		bars, err := db.MinuteBarsByDay(code, tcalibScale, day)
		if err != nil {
			return cd, err
		}
		if len(bars) == 0 {
			continue
		}
		conv := make([]tcalib.Bar, 0, len(bars))
		for _, b := range bars {
			conv = append(conv, tcalib.Bar{
				Ts: b.Ts, Open: b.Open, High: b.High, Low: b.Low,
				Close: b.Close, Vol: b.Vol, Amount: b.Amount,
			})
		}
		cd.Days = append(cd.Days, tcalib.DayBars{Day: day, Bars: conv})
	}
	return cd, nil
}

// tcalibPrintSummary 把结论打到日志（运维在远端只看日志，不能只依赖产物文件）。
// English: tcalibPrintSummary logs the verdict and per-tier headline numbers.
func tcalibPrintSummary(rep tcalib.Report) {
	log.Printf("§T-CALIB 覆盖率：票 %d / 合格 %d（≥%d 日），天数 P10/P50/max=%d/%d/%d",
		rep.Coverage.CodesTotal, rep.Coverage.CodesEligible, rep.Coverage.MinDays,
		rep.Coverage.DaysP10, rep.Coverage.DaysP50, rep.Coverage.DaysMax)
	if len(rep.Coverage.Excluded) > 0 {
		log.Printf("§T-CALIB 不合格票 %d 只（明细见报告，不静默丢）", len(rep.Coverage.Excluded))
	}
	if rep.Window.TotalN > 0 {
		log.Printf("§T-CALIB 窗口：标定 %s~%s（%d 日） 验证 %s~%s（%d 日）",
			rep.Window.CalibFrom, rep.Window.CalibTo, rep.Window.CalibN,
			rep.Window.OOSFrom, rep.Window.OOSTo, rep.Window.OOSN)
	}
	// 逐档一行：直接打"可签字/不替换"，避免读者再去翻 JSON。
	for _, st := range rep.Tiers {
		mark := "不替换"
		if st.Replaced {
			mark = "可签字"
		}
		log.Printf("§T-CALIB T%d n=%d 价差=%.4f%% 回踩=%.4f%% 振幅=%.4f%% 轮次=%d 样本外净=%.4f%% [%s]",
			st.Tier, st.N, st.MinSpreadPct, st.VwapDevPct, st.MinAmplitudePct,
			st.MaxRounds, st.OOSNetPct, mark)
	}
}

// tcalibWrite 落两份产物：机读 JSON + 人读 Markdown。文件名带样本最后交易日，
// 重跑覆盖同一份（不制造"每次一个新文件"的目录垃圾）。
// English: tcalibWrite persists the JSON + Markdown artifacts keyed by the sample end date.
func tcalibWrite(rep tcalib.Report, outDir string) (string, string, error) {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", "", err
	}
	suffix := rep.SampleEnd
	if suffix == "" {
		// 没有样本日（分钟表为空/全部不合格）时用 "nodata" 占位，
		// 这样产物仍然落盘（覆盖率报告本身就是交付物），且不会覆盖历史文件。
		suffix = "nodata"
	}
	js, err := rep.JSON()
	if err != nil {
		return "", "", err
	}
	jsonPath := filepath.Join(outDir, fmt.Sprintf("tcalib_%s.json", suffix))
	if err := os.WriteFile(jsonPath, append(js, '\n'), 0o644); err != nil {
		return "", "", err
	}
	mdPath := filepath.Join(outDir, fmt.Sprintf("TCALIB_REPORT_%s.md", suffix))
	if err := os.WriteFile(mdPath, []byte(rep.Markdown()), 0o644); err != nil {
		return "", "", err
	}
	return jsonPath, mdPath, nil
}
