// dynsize_whatif — §FLOW-FACTOR/§SIZING-EVO 人工试算器（owner Q2 交付物）。
//
// 用途（docs/MODPLAN_UAT_FLOWFACTOR_20261025.md §2.7.4）：
//  1. matrix  ：给定市场环境 + 信号画像，扫净流入率网格，看每个点最终 scale 与命中理由
//     （"这一档加权在实盘会长成什么样"）；
//  2. presets ：同一网格下对比多套加权预设（出厂/进攻/保守），看加权强弱对 scale 的影响；
//  3. bands   ：R4 分布标定——读真实/给定净流入率样本（CSV 或单列文本），统计各档位占比，
//     判断默认阈值（±3%/±1%）是否切在合理分位。
//
// 纪律：本工具只读配置与信号，不写库、不触发交易；输出全部来自 signalctl.ComputeSizing
// 的真实合成路径（与实盘同一函数），不存在"试算一套、实盘另一套"。
//
// English: the manual what-if calculator — scale matrix over the flow grid, preset comparison,
// and R4 band calibration from a real flow-ratio sample. Pure read-only, same ComputeSizing path
// as production.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/signalctl"
)

// preset 一套加权预设（相对出厂的覆盖）。
type preset struct {
	Name  string
	Desc  string
	Apply func(*config.SizingConfig)
}

// presets 三套对照预设：出厂 / 进攻（强化净流入与情绪）/ 保守（全面压舱）。
func presets() []preset {
	return []preset{
		{Name: "default", Desc: "出厂默认（§2.4/§2.5 档位）", Apply: func(c *config.SizingConfig) {}},
		{Name: "aggressive", Desc: "进攻：净流入档差放大 + 阈值收紧 + 情绪进攻档上调", Apply: func(c *config.SizingConfig) {
			c.FlowFactor.Buy5DScales["strong_in"] = 1.15
			c.FlowFactor.Buy5DScales["strong_out"] = 0.65
			c.FlowFactor.BuyTodayScales["strong_in"] = 1.10
			c.FlowFactor.Buy5DBands["strong_in_pct"] = 2
			c.FlowFactor.Buy5DBands["mild_in_pct"] = 0.5
			c.FlowFactor.Buy5DBands["mild_out_pct"] = -0.5
			c.FlowFactor.Buy5DBands["strong_out_pct"] = -2
			c.EmotionScales["发酵"] = 1.10
			c.KellyLambda = 0.60
		}},
		{Name: "conservative", Desc: "保守：净流入档差收窄 + 阈值放宽 + 全局上限下调", Apply: func(c *config.SizingConfig) {
			c.FlowFactor.Buy5DScales["strong_in"] = 1.05
			c.FlowFactor.Buy5DScales["strong_out"] = 0.85
			c.FlowFactor.BuyTodayScales["strong_in"] = 1.02
			c.FlowFactor.BuyTodayScales["strong_out"] = 0.95
			c.FlowFactor.Buy5DBands["strong_in_pct"] = 4
			c.FlowFactor.Buy5DBands["mild_in_pct"] = 2
			c.FlowFactor.Buy5DBands["mild_out_pct"] = -2
			c.FlowFactor.Buy5DBands["strong_out_pct"] = -4
			c.Cap = 1.00
			c.KellyLambda = 0.40
		}},
	}
}

func main() {
	mode := flag.String("mode", "matrix", "matrix|presets|bands")
	flowMin := flag.Float64("flow-min", -6, "净流入率网格下限（%）")
	flowMax := flag.Float64("flow-max", 6, "净流入率网格上限（%）")
	flowStep := flag.Float64("flow-step", 1, "净流入率网格步长（%）")
	todayRatio := flag.Float64("today", 0, "当日净流入率（%；0=弃权）")
	conf := flag.Float64("conf", 0.8, "信号置信度 0..1")
	atr := flag.Float64("atr", 0.2, "ATR（元）")
	price := flag.Float64("price", 10, "现价（元）")
	quality := flag.Float64("quality", 1.0, "信号质量权重（1=未加权）")
	emotion := flag.String("emotion", "发酵", "情绪相位（退潮/背离/高潮/发酵/启动）")
	state := flag.String("state", "bull", "市场状态（bull/range/bear）")
	tier := flag.String("tier", "", "风险档（空/Yellow/Red）")
	breadth := flag.Float64("breadth", 0.6, "上涨广度 0..1")
	indexPct := flag.Float64("index", 0, "大盘涨跌幅（%）")
	csvPath := flag.String("csv", "", "bands 模式：净流入率样本文件（每行一个数，或逗号/空白分隔）")
	presetName := flag.String("preset", "default", "matrix 模式使用的预设名")
	outPath := flag.String("out", "", "输出文件（缺省 stdout；.json 结尾输出 JSON）")
	flag.Parse()

	base := config.DefaultSizingConfig().WithDefaults()
	var p preset
	for _, pp := range presets() {
		if pp.Name == *presetName {
			p = pp
		}
	}
	if p.Name == "" {
		fatalf("未知预设 %q（可选 default/aggressive/conservative）", *presetName)
	}
	cfg := base
	p.Apply(&cfg)

	reg := signalctl.Regime{
		SizeCfg: cfg, EmotionPhase: *emotion, MarketState: *state, RiskTier: *tier,
		BreadthUpRatio: *breadth, IndexChangePct: *indexPct,
		Holiday: signalctl.HolidayWindow{CalendarLoaded: true},
	}
	sig := combat_agent.Signal{
		Code: "600519", Price: *price, ATR: *atr, Confidence: *conf, QualityWeight: *quality,
	}

	var out strings.Builder
	switch *mode {
	case "matrix":
		renderMatrix(&out, reg, sig, *flowMin, *flowMax, *flowStep, *todayRatio, p)
	case "presets":
		renderPresets(&out, reg, sig, *flowMin, *flowMax, *flowStep, *todayRatio)
	case "bands":
		if *csvPath == "" {
			fatalf("bands 模式需要 --csv <样本文件>")
		}
		ratios, err := loadRatios(*csvPath)
		if err != nil {
			fatalf("读样本失败: %v", err)
		}
		renderBands(&out, ratios)
	default:
		fatalf("未知 mode %q", *mode)
	}

	if *outPath == "" {
		fmt.Print(out.String())
		return
	}
	if err := os.WriteFile(*outPath, []byte(out.String()), 0o644); err != nil {
		fatalf("写输出失败: %v", err)
	}
	fmt.Printf("已写入 %s\n", *outPath)
}

// renderMatrix 单预设：净流入率网格 → scale/理由表。
func renderMatrix(out *strings.Builder, reg signalctl.Regime, sig combat_agent.Signal, lo, hi, step, today float64, p preset) {
	fmt.Fprintf(out, "# 仓位试算矩阵（预设 %s：%s）\n\n", p.Name, p.Desc)
	fmt.Fprintf(out, "环境：情绪=%s 状态=%s 风险档=%s 广度=%.2f 大盘=%+.2f%%；信号：置信=%.2f ATR=%.2f 价=%.2f 质量=%.2f 当日流入=%.2f%%\n\n",
		reg.EmotionPhase, reg.MarketState, orDash(reg.RiskTier), reg.BreadthUpRatio, reg.IndexChangePct,
		sig.Confidence, sig.ATR, sig.Price, sig.QualityWeight, today)
	fmt.Fprintln(out, "| 5日净流入率 | 命中档位 | scale | 触地板/上限 | 命中因子 |")
	fmt.Fprintln(out, "|---|---|---|---|---|")
	for r := lo; r <= hi+1e-9; r += step {
		s := sig
		s.FlowRatio5D = r
		s.FlowRatioToday = today
		res := signalctl.ComputeSizing(reg, s)
		band := "弃权"
		if r != 0 {
			band = flowBandLabel(reg.SizeCfg, r)
		}
		hit := "—"
		if res.Downgrade {
			hit = "硬拦降级"
		} else if len(res.Reasons) > 0 {
			hit = strings.Join(res.Reasons, "；")
		}
		fmt.Fprintf(out, "| %+.1f%% | %s | %.4f | %s | %s |\n", r, band, res.Scale, floorCapMark(reg.SizeCfg, res.Scale), hit)
	}
}

// renderPresets 多预设对比：同网格下三套加权的 scale。
func renderPresets(out *strings.Builder, reg signalctl.Regime, sig combat_agent.Signal, lo, hi, step, today float64) {
	ps := presets()
	fmt.Fprintln(out, "# 多加权预设对比（同一信号/环境下 scale 差异）")
	fmt.Fprintf(out, "\n环境：情绪=%s 状态=%s 风险档=%s 广度=%.2f；信号：置信=%.2f ATR=%.2f 价=%.2f 当日流入=%.2f%%\n\n",
		reg.EmotionPhase, reg.MarketState, orDash(reg.RiskTier), reg.BreadthUpRatio, sig.Confidence, sig.ATR, sig.Price, today)
	out.WriteString("| 5日净流入率 |")
	for _, p := range ps {
		fmt.Fprintf(out, " %s |", p.Name)
	}
	out.WriteString("\n|---|")
	for range ps {
		out.WriteString("---|")
	}
	out.WriteString("\n")
	for r := lo; r <= hi+1e-9; r += step {
		fmt.Fprintf(out, "| %+.1f%% |", r)
		for _, p := range ps {
			cfg := config.DefaultSizingConfig().WithDefaults()
			p.Apply(&cfg)
			reg.SizeCfg = cfg
			s := sig
			s.FlowRatio5D = r
			s.FlowRatioToday = today
			res := signalctl.ComputeSizing(reg, s)
			cell := fmt.Sprintf("%.4f", res.Scale)
			if res.Downgrade {
				cell = "降级"
			}
			fmt.Fprintf(out, " %s |", cell)
		}
		out.WriteString("\n")
	}
	fmt.Fprintf(out, "\n说明：%s\n", presetLegend(ps))
}

// renderBands R4 分布标定：样本分位 + 各预设档位占比。
func renderBands(out *strings.Builder, ratios []float64) {
	sort.Float64s(ratios)
	n := len(ratios)
	fmt.Fprintf(out, "# 净流入率分布标定（样本 %d 条）\n\n", n)
	fmt.Fprintf(out, "| 分位 | 净流入率 |\n|---|---|\n")
	for _, q := range []struct {
		name string
		p    float64
	}{{"min", 0}, {"p10", 0.10}, {"p25", 0.25}, {"median", 0.50}, {"p75", 0.75}, {"p90", 0.90}, {"max", 1}} {
		fmt.Fprintf(out, "| %s | %+.3f%% |\n", q.name, quantile(ratios, q.p))
	}
	out.WriteString("\n## 各预设档位占比（按默认阈值 ±3%/±1% 与预设阈值）\n\n")
	out.WriteString("| 预设 | strong_in | mild_in | neutral | mild_out | strong_out |\n|---|---|---|---|---|---|\n")
	for _, p := range presets() {
		cfg := config.DefaultSizingConfig().WithDefaults()
		p.Apply(&cfg)
		counts := map[string]int{}
		for _, r := range ratios {
			counts[bandOf(cfg, r)]++
		}
		fmt.Fprintf(out, "| %s |", p.Name)
		for _, b := range []string{"strong_in", "mild_in", "neutral", "mild_out", "strong_out"} {
			fmt.Fprintf(out, " %.1f%% |", float64(counts[b])/float64(n)*100)
		}
		out.WriteString("\n")
	}
	out.WriteString("\n判读：强档（strong_in/strong_out）合计占比 10%~30% 时档位区分度较好；" +
		"若 neutral 占比 >70%，说明阈值过宽、因子几乎不起作用（R4 需标定）。\n")
}

// bandOf 按 5 日档位阈值归类（与 signalctl.flowBandOf 同口径：阈值含端点归严）。
func bandOf(cfg config.SizingConfig, ratio float64) string {
	b := cfg.FlowFactor.Buy5DBands
	if ratio >= b["strong_in_pct"] {
		return "strong_in"
	}
	if ratio >= b["mild_in_pct"] {
		return "mild_in"
	}
	if ratio > b["mild_out_pct"] {
		return "neutral"
	}
	if ratio > b["strong_out_pct"] {
		return "mild_out"
	}
	return "strong_out"
}

// flowBandLabel 档位中文名（矩阵可读）。
func flowBandLabel(cfg config.SizingConfig, ratio float64) string {
	switch bandOf(cfg, ratio) {
	case "strong_in":
		return "强流入"
	case "mild_in":
		return "弱流入"
	case "neutral":
		return "中性"
	case "mild_out":
		return "弱流出"
	default:
		return "强流出"
	}
}

// floorCapMark 标记触地板/触上限（一眼看出是不是被夹持）。
func floorCapMark(cfg config.SizingConfig, scale float64) string {
	var marks []string
	if scale <= cfg.Floor+1e-9 {
		marks = append(marks, "地板")
	}
	if scale >= cfg.Cap-1e-9 {
		marks = append(marks, "上限")
	}
	if len(marks) == 0 {
		return "—"
	}
	return strings.Join(marks, "+")
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

// loadRatios 读样本：每行一个数，或逗号/空白分隔；空行与 # 注释跳过。
func loadRatios(path string) ([]float64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []float64
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		for _, tok := range strings.FieldsFunc(line, func(r rune) bool { return r == ',' || r == ';' || r == ' ' || r == '\t' }) {
			tok = strings.TrimSpace(tok)
			if tok == "" {
				continue
			}
			v, err := strconv.ParseFloat(tok, 64)
			if err != nil {
				return nil, fmt.Errorf("无法解析 %q: %w", tok, err)
			}
			out = append(out, v)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("样本为空")
	}
	return out, nil
}

func presetLegend(ps []preset) string {
	var parts []string
	for _, p := range ps {
		parts = append(parts, p.Name+"="+p.Desc)
	}
	return strings.Join(parts, "；")
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "dynsize_whatif: "+format+"\n", args...)
	os.Exit(2)
}
