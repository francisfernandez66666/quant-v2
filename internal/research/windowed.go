// 窗口分块计算（内存优化）：把 discover_factors 的"全量面板常驻（~2.8GB）"改为
// 按交易日窗口分块，每窗口只装配该区间股票面板、算完即释放，从而把峰值内存压到
// 单窗口（约 340MB，900M 内）而保持全局口径（每窗口内是完整截面）。
//
// 适用场景：服务器 1.6G 内存小 VPS 跑全市场（5545 只 × 近3年 × 全因子含财务）自动研究时
// 避免 OOM/拖垮系统；代价是每次评估动作需重扫所有窗口（CPU 高、跑得慢，但可接受）。
//
// 核心复用：窗口内仍用现有的 CompositeICRange / ICByDate / SpearmanIC / forwardReturn，
// 只是把"一次性全量 panels"替换为"逐窗口装配 panels → 累积该窗口的 ICRow → 释放"。
//
// English: window-chunked computation (memory optimization) — turns discover_factors' "all panels
// resident in memory (~2.8GB)" into per-trading-day-window chunks: each window only assembles that
// interval's stock panels, computes, then releases, dropping peak memory to a single window
// (~340MB, within 900M) while keeping the global cross-section (each window holds the full stock
// cross-section). Aimed at the 1.6G VPS running full-universe auto research without OOM/starving the
// system; the cost is that each evaluation re-scans every window (slower, but acceptable).
package research

import (
	"math"
	"sort"

	"quant-trading-v2/internal/factor"
	"quant-trading-v2/internal/store"
)

// windowDays 每个窗口包含的交易日数。越小峰值内存越低、但装配次数越多（越慢）。
// 90→60：2026-08-20 起调小以进一步压低研究峰值内存（~716MB→~450MB），
// 配合 quant 盘后释放 + MemoryMax 1500M，让 1.6G 小 VPS 的夜间作业不再叠加 OOM。
// English: trading days per window. Smaller → lower peak memory, more assembles (slower).
// 90→60 (since 2026-08-20): shrinks the research peak (~716MB→~450MB) so the nightly job no longer
// stacks with quant on the 1.6G box (alongside quant's after-hours release and MemoryMax 1500M).
const windowDays = 60

// windowDefs 把因子 ID 列表解析为装配用的 Def 列表（缺省全部已注册）。
// English: resolves factor IDs into Defs for assembly (defaults to all registered factors).
func windowDefs(ids []string) []factor.Def {
	if len(ids) == 0 {
		return factor.All()
	}
	var defs []factor.Def
	for _, id := range ids {
		if d, ok := factor.Get(id); ok {
			defs = append(defs, d)
		}
	}
	return defs
}

// windowChunks 把交易日列表切成若干窗口（每窗口最多 winDays 天）。
// 返回各窗口的 [start,end]（YYYYMMDD）。窗口按区间首日切分。
// English: splits the trade-date list into windows of at most winDays days, returning each
// window's inclusive [start,end] (YYYYMMDD).
func windowChunks(dates []string, winDays int) [][2]string {
	if winDays <= 0 {
		winDays = windowDays
	}
	if len(dates) == 0 {
		return nil
	}
	var out [][2]string
	for i := 0; i < len(dates); i += winDays {
		j := i + winDays
		if j > len(dates) {
			j = len(dates)
		}
		out = append(out, [2]string{dates[i], dates[j-1]})
	}
	return out
}

// nextDayStr 返回 YYYYMMDD 的次日（用于把窗口尾巴多算 h 天以补足前瞻收益）。
// English: returns the day after a YYYYMMDD (to extend a window's tail by h days for forward returns).
func nextDayStr(yyyymmdd string) string {
	if len(yyyymmdd) != 8 {
		return yyyymmdd
	}
	return storeNextDay(yyyymmdd)
}

// windowCompositeIC 按窗口分块装配，累积 CompositeIC 的逐日 IC 行（全区间）。
// 每窗口：BuildPanels 装配 [winStart, endPlusH]（多算 h 天尾巴保证前瞻收益完整），
// 然后 CompositeICRange 只统计窗口内日期。窗口算完释放。
// English: chunked assembly of CompositeIC rows over the full range — per window it assembles
// panels over [winStart, endPlusH] (extra h days so forward returns are complete) then keeps only
// in-window rows via CompositeICRange; the window is released afterwards.
func windowCompositeIC(db *store.DB, codes []string, factors []string, weights map[string]float64, h, min int, winDays int, start, end string) []ICRow {
	defs := windowDefs(factors)
	dates, err := db.TradeDates(start, end)
	if err != nil || len(dates) == 0 {
		return nil
	}
	// 统计用的全区间边界
	statStart, statEnd := dates[0], dates[len(dates)-1]
	var all []ICRow
	for _, w := range windowChunks(dates, winDays) {
		// 窗口统计区间 + 尾部多算 h 天（保证窗口末日的前瞻收益可用）
		asmbEnd := w[1]
		for i := 0; i < h; i++ {
			asmbEnd = nextDayStr(asmbEnd)
		}
		panels, err := BuildPanels(db, codes, w[0], asmbEnd, defs)
		if err != nil {
			continue
		}
		rows := CompositeICRange(panels, factors, weights, h, min, w[0], w[1])
		all = append(all, rows...)
	}
	_ = statStart
	_ = statEnd
	return all
}

// windowICByAllFactors 每窗口只装配一次（含全部候选因子），算出窗口内**所有**因子的
// 单因子 IC 行。用于预筛阶段，把装配次数从「因子数×窗口数」降到「窗口数」，大幅提速。
// 返回 map[fid][]ICRow（全区间，按窗口合并）。
// English: assembles each window once (with all candidate factors) and computes every factor's
// single-factor IC rows in that window. Used by the pre-screen, cutting assembles from
// factors×windows down to windows (a big speedup). Returns map[fid][]ICRow merged over windows.
func windowICByAllFactors(db *store.DB, codes []string, fids []string, h, min int, winDays int, start, end string) map[string][]ICRow {
	out := make(map[string][]ICRow, len(fids))
	if len(fids) == 0 {
		return out
	}
	defs := windowDefs(fids)
	dates, err := db.TradeDates(start, end)
	if err != nil || len(dates) == 0 {
		return out
	}
	for _, w := range windowChunks(dates, winDays) {
		asmbEnd := w[1]
		for i := 0; i < h; i++ {
			asmbEnd = nextDayStr(asmbEnd)
		}
		panels, err := BuildPanels(db, codes, w[0], asmbEnd, defs)
		if err != nil {
			continue
		}
		// 对每个候选因子，用同一批窗口面板算单因子 IC
		for _, fid := range fids {
			rows := ICByDate(panels, fid, h, min)
			for _, r := range rows {
				if r.Date >= w[0] && r.Date <= w[1] {
					out[fid] = append(out[fid], r)
				}
			}
		}
	}
	return out
}

// windowCompositeIR 返回全区间复合 |IR|（窗口分块）。
// English: composite |IR| over the full range via window chunking.
func windowCompositeIR(db *store.DB, codes []string, factors []string, weights map[string]float64, h, min int, winDays int, start, end string) float64 {
	rows := windowCompositeIC(db, codes, factors, weights, h, min, winDays, start, end)
	ir := IR(rows)
	if math.IsNaN(ir) {
		return 0
	}
	return math.Abs(ir)
}

// windowCompositeICForSubsets 贪心选择的提速版：每窗口只装配一次（含 base+全部候选因子），
// 在该窗口内对每个候选子集（base+每个 cand）各算 CompositeIC 行并累积。
// 把贪心每步的装配次数从「候选数×窗口数」降到「窗口数」。返回 map[cand][]ICRow。
// English: greedy-selection speedup — assemble each window once (base + all candidate factors) and
// compute CompositeIC rows for every candidate subset (base+cand) in that window, cutting a greedy
// step's assembles from candidates×windows down to windows. Returns map[cand][]ICRow.
func windowCompositeICForSubsets(db *store.DB, codes []string, base, cands []string, h, min int, winDays int, start, end string) map[string][]ICRow {
	out := make(map[string][]ICRow, len(cands))
	if len(cands) == 0 {
		return out
	}
	// 装配用的因子 = base + 全部候选
	fids := append(append([]string{}, base...), cands...)
	defs := windowDefs(fids)
	dates, err := db.TradeDates(start, end)
	if err != nil || len(dates) == 0 {
		return out
	}
	for _, w := range windowChunks(dates, winDays) {
		asmbEnd := w[1]
		for i := 0; i < h; i++ {
			asmbEnd = nextDayStr(asmbEnd)
		}
		panels, err := BuildPanels(db, codes, w[0], asmbEnd, defs)
		if err != nil {
			continue
		}
		for _, cand := range cands {
			candFactors := append(append([]string{}, base...), cand)
			wm := map[string]float64{}
			for _, f := range candFactors {
				wm[f] = 1.0
			}
			rows := CompositeICRange(panels, candFactors, wm, h, min, w[0], w[1])
			out[cand] = append(out[cand], rows...)
		}
	}
	return out
}

// windowReverseExtension 按窗口分块累积反推泛化的 top/rest 收益数组并做 Welch t 检验。
// 逻辑与 reverseExtension 一致（每日截面 z 化复合分 → top20% vs 其余），只是逐窗口装配、
// 累积收益数组而非一次性聚合。
// English: window-chunked reverse-extension — accumulates top/rest forward returns per window
// (same daily z-scored composite → top-quintile vs rest logic as reverseExtension) and runs a
// Welch t-test on the merged arrays.
func windowReverseExtension(db *store.DB, codes []string, factors []string, dirs map[string]int, weights map[string]float64, opts DiscoverOpts, winDays int, start, end string) (float64, float64, float64, float64, float64) {
	defs := windowDefs(factors)
	dates, err := db.TradeDates(start, end)
	if err != nil || len(dates) == 0 {
		return 0, 0, 0, 0, nan()
	}
	var topRets, restRets []float64
	for _, w := range windowChunks(dates, winDays) {
		asmbEnd := w[1]
		for i := 0; i < opts.Horizon; i++ {
			asmbEnd = nextDayStr(asmbEnd)
		}
		panels, err := BuildPanels(db, codes, w[0], asmbEnd, defs)
		if err != nil {
			continue
		}
		// 逐日截面（限定窗口内日期），累积 top/rest 收益
		type kv struct {
			sc float64
			r  float64
		}
		for _, d := range unionDates(panels) {
			if d < w[0] || d > w[1] {
				continue
			}
			var day []kv
			for _, p := range panels {
				idx, ok := p.DateIdx[d]
				if !ok {
					continue
				}
				r := forwardReturn(p.Series, idx, opts.Horizon)
				if isNaN(r) {
					continue
				}
				sc := compositeScore(p, factors, dirs, weights, d)
				if isNaN(sc) {
					continue
				}
				day = append(day, kv{sc, r})
			}
			if len(day) < opts.MinStocks {
				continue
			}
			var sum, sum2 float64
			for _, v := range day {
				sum += v.sc
				sum2 += v.sc * v.sc
			}
			mean := sum / float64(len(day))
			std := math.Sqrt(sum2/float64(len(day)) - mean*mean)
			if std <= 0 {
				continue
			}
			for i := range day {
				day[i].sc = (day[i].sc - mean) / std
			}
			sort.Slice(day, func(i, j int) bool { return day[i].sc > day[j].sc })
			nTop := len(day) / 5
			if nTop < 1 {
				nTop = 1
			}
			for i, v := range day {
				if i < nTop {
					topRets = append(topRets, v.r)
				} else {
					restRets = append(restRets, v.r)
				}
			}
		}
	}
	if len(topRets) == 0 || len(restRets) == 0 {
		return 0, 0, 0, 0, nan()
	}
	topMean := meanOf(topRets)
	restMean := meanOf(restRets)
	excess := topMean - restMean
	varTop := varianceOf(topRets, topMean)
	varRest := varianceOf(restRets, restMean)
	stdErr := math.Sqrt(varTop/float64(len(topRets)) + varRest/float64(len(restRets)))
	var t float64
	if stdErr > 0 {
		t = excess / stdErr
	} else {
		t = nan()
	}
	return topMean, restMean, excess, stdErr, t
}

// windowOptimizeWeights 坐标上升权重优化（窗口分块版）。复刻 OptimizeWeights 的算法，
// 但内部用 windowCompositeIC 代替全量面板的 CompositeIC。
// English: window-chunked coordinate-ascent weight optimization, mirroring OptimizeWeights but
// using windowCompositeIC instead of full-panel CompositeIC.
func windowOptimizeWeights(db *store.DB, codes []string, opts OptimizeOpts, winDays int, start, end string) OptResult {
	if len(opts.Factors) == 0 {
		return OptResult{Reason: "因子池为空"}
	}
	if opts.Horizon <= 0 {
		opts.Horizon = 5
	}
	if opts.MinStocks <= 0 {
		opts.MinStocks = 10
	}
	if opts.MaxIter <= 0 {
		opts.MaxIter = 6
	}
	if opts.Step <= 0 {
		opts.Step = 0.1
	}
	w := make(map[string]float64, len(opts.Factors))
	for _, f := range opts.Factors {
		w[f] = 1.0
	}
	w = cloneWeights(w)
	best := windowEval(db, codes, opts, w, winDays, start, end)
	for it := 0; it < opts.MaxIter; it++ {
		improved := false
		for _, f := range opts.Factors {
			for _, delta := range []float64{opts.Step, -opts.Step} {
				cand := cloneWeights(w)
				cand[f] += delta
				if cand[f] < 0 {
					cand[f] = 0
				}
				r := windowEval(db, codes, opts, cand, winDays, start, end)
				if better(r, best, opts.Metric) {
					best = r
					w = cand
					improved = true
				}
			}
		}
		if !improved {
			break
		}
	}
	best.Weights = cloneWeights(w)
	ir := math.Abs(best.IR)
	switch {
	case len(best.Weights) == 0:
		best.PassGuard, best.Reason = false, "无有效因子"
	case best.NDays < opts.GuardMinDays:
		best.PassGuard, best.Reason = false, "有效日不足"
	case ir < opts.GuardMinIR:
		best.PassGuard, best.Reason = false, "|IR| 低于护栏"
	default:
		best.PassGuard, best.Reason = true, "通过护栏"
	}
	return best
}

// windowEval 用窗口分块计算某权重下的 IC 统计（等价 evaluate，但走窗口内核）。
// English: window-chunked IC stats for a given weight set (equivalent to evaluate).
func windowEval(db *store.DB, codes []string, opts OptimizeOpts, w map[string]float64, winDays int, start, end string) OptResult {
	rows := windowCompositeIC(db, codes, opts.Factors, w, opts.Horizon, opts.MinStocks, winDays, start, end)
	return OptResult{
		ICMean: meanIC(rows), ICStd: stdIC(rows), IR: IR(rows), NDays: len(rows),
	}
}

// DiscoverFactorsWindowed 内存可控的因子发现（窗口分块版）。
// 等价于 DiscoverFactors，但接收 db+codes+区间，内部按窗口装配，避免全量面板常驻
// （~2.8GB），峰值内存压到单窗口（900M 内）。口径与全量版一致。
// English: memory-bounded factor discovery (window-chunked). Equivalent to DiscoverFactors but takes
// db+codes+range and assembles per window internally, avoiding the ~2.8GB full-panel residency so
// peak memory stays within a single window (inside 900M). Same semantics as the full version.
func DiscoverFactorsWindowed(db *store.DB, codes []string, start, end string, opts DiscoverOpts) DiscoverResult {
	if opts.Horizon <= 0 {
		opts.Horizon = 5
	}
	if opts.MinStocks <= 0 {
		opts.MinStocks = 10
	}
	if opts.MaxFactors <= 0 {
		opts.MaxFactors = 8
	}
	if opts.Step <= 0 {
		opts.Step = 0.1
	}
	if opts.MinIR <= 0 {
		opts.MinIR = 0.3
	}
	if opts.MinDays <= 0 {
		opts.MinDays = 20
	}
	if opts.SplitPct <= 0 || opts.SplitPct >= 1 {
		opts.SplitPct = 0.7
	}
	if opts.MinGenT >= 0 {
		opts.MinGenT = -2
	}
	if len(opts.Factors) == 0 {
		for _, d := range factor.All() {
			opts.Factors = append(opts.Factors, d.ID)
		}
	}
	res := DiscoverResult{Directions: map[string]int{}, Weights: map[string]float64{}}
	if len(codes) == 0 {
		res.Reason = "无有效面板"
		return res
	}
	// 全局交易日列表 + 分段边界
	dates, err := db.TradeDates(start, end)
	if err != nil || len(dates) < 10 {
		res.Reason = "日期过少"
		return res
	}
	splitIdx := int(float64(len(dates)) * opts.SplitPct)
	splitDate := dates[splitIdx]
	winDays := windowDays

	// 1) 单因子预筛：每窗口装配一次（含全部候选因子），一次性算所有因子的全区间 |IR|
	// （比逐因子重新装配窗口快约「因子数」倍）。
	// English: single-factor pre-screen — assemble each window once (all candidate factors) and compute
	// every factor's full-range |IR| in one pass (~factors× faster than re-assembling per factor).
	var pre []string
	allIC := windowICByAllFactors(db, codes, opts.Factors, opts.Horizon, opts.MinStocks, winDays, start, end)
	for _, fid := range opts.Factors {
		rows := allIC[fid]
		if len(rows) < opts.MinDays {
			continue
		}
		ir := absf(IR(rows))
		if isNaN(ir) || ir < 0.05 {
			continue
		}
		pre = append(pre, fid)
	}
	if len(pre) == 0 {
		res.Reason = "预筛后无有效因子"
		return res
	}

	// 2) 贪心前向选择（等权）
	selected := make([]string, 0, opts.MaxFactors)
	selectedSet := map[string]bool{}
	bestIR := -1e9
	dirs := map[string]int{}
	for len(selected) < opts.MaxFactors {
		// 候选因子（未选中的）
		var cands []string
		for _, fid := range pre {
			if !selectedSet[fid] {
				cands = append(cands, fid)
			}
		}
		if len(cands) == 0 {
			break
		}
		// 每窗口装配一次，算所有候选子集（base+各 cand）的复合 IC（提速）
		bestFid := ""
		bestCandIR := bestIR
		subsetIC := windowCompositeICForSubsets(db, codes, selected, cands, opts.Horizon, opts.MinStocks, winDays, start, end)
		for _, fid := range cands {
			ir := absf(IR(subsetIC[fid]))
			if isNaN(ir) {
				continue
			}
			if ir > bestCandIR {
				bestCandIR = ir
				bestFid = fid
			}
		}
		if bestFid == "" || bestCandIR <= bestIR {
			break
		}
		selected = append(selected, bestFid)
		selectedSet[bestFid] = true
		bestIR = bestCandIR
	}
	if len(selected) == 0 {
		res.Reason = "前向选择未选出因子"
		return res
	}

	// 3) 方向 + 权重优化
	for _, fid := range selected {
		if d, ok := factor.Get(fid); ok {
			dirs[fid] = dirOfCat(d.Cat)
		} else {
			dirs[fid] = 1
		}
	}
	opt := windowOptimizeWeights(db, codes, OptimizeOpts{
		Factors: selected, Horizon: opts.Horizon, MinStocks: opts.MinStocks,
		Metric: opts.Metric, Step: opts.Step, MaxIter: 6,
		GuardMinIR: opts.MinIR, GuardMinDays: opts.MinDays,
	}, winDays, start, end)
	res.Factors = selected
	res.Directions = dirs
	res.Weights = opt.Weights
	res.ICMean = opt.ICMean
	res.IR = opt.IR
	res.NDays = opt.NDays
	res.PassGuard = opt.PassGuard
	res.Reason = opt.Reason

	// 4) E3 分段 + 反推验证（窗口分块）
	res.InsampleIR = windowCompositeIR(db, codes, selected, opt.Weights, opts.Horizon, opts.MinStocks, winDays, start, splitDate)
	res.OutsampleIR = windowCompositeIR(db, codes, selected, opt.Weights, opts.Horizon, opts.MinStocks, winDays, splitDate, end)
	res.GenTopMean, res.GenAllMean, res.GenExcess, res.GenStdErr, res.GenT = windowReverseExtension(db, codes, selected, dirs, opt.Weights, opts, winDays, splitDate, end)

	if res.OutsampleIR < opts.MinIR {
		if res.PassGuard {
			res.Reason = "样本内过护栏但样本外IR不足(" + trimFloat(res.OutsampleIR) + ")"
			res.PassGuard = false
		}
	}
	if res.PassGuard && !isNaN(res.GenT) && res.GenT < opts.MinGenT {
		res.Reason = "反推泛化不足（高分组超额" + trimFloat(res.GenExcess) + "，t=" + trimFloat(res.GenT) + "显著为负）"
		res.PassGuard = false
	}
	return res
}

// storeNextDay 返回 YYYYMMDD 的次日（跨月跨年）。
// English: returns the next calendar day of a YYYYMMDD (handles month/year rollover).
func storeNextDay(yyyymmdd string) string {
	y := atoi8(yyyymmdd[0:4])
	m := atoi8(yyyymmdd[4:6])
	d := atoi8(yyyymmdd[6:8])
	// 用简单的日推进
	d++
	dim := daysInMonth(y, m)
	if d > dim {
		d = 1
		m++
		if m > 12 {
			m = 1
			y++
		}
	}
	return itoa4(y) + itoa2(m) + itoa2(d)
}

// atoi8 手写 8 位数字字符串 → int（YYYYMMDD 日期解析，避免引入 strconv 依赖）。
// English: hand-rolled 8-digit string→int (YYYYMMDD date parsing, no strconv dependency).
func atoi8(s string) int {
	v := 0
	for i := 0; i < len(s); i++ {
		v = v*10 + int(s[i]-'0')
	}
	return v
}

// daysInMonth 返回某年某月的天数（含闰年 2 月）。
// English: returns the number of days in a month (leap-year aware).
func daysInMonth(y, m int) int {
	switch m {
	case 1, 3, 5, 7, 8, 10, 12:
		return 31
	case 4, 6, 9, 11:
		return 30
	case 2:
		if (y%4 == 0 && y%100 != 0) || y%400 == 0 {
			return 29
		}
		return 28
	}
	return 30
}

func itoa4(v int) string { return itoaN(v, 4) }
func itoa2(v int) string { return itoaN(v, 2) }

// itoaN 手写整数 → 定宽数字字符串（高位补零），供 storeNextDay 拼 YYYYMMDD。
// English: hand-rolled int→zero-padded fixed-width decimal string (for storeNextDay).
func itoaN(v, w int) string {
	s := ""
	for v > 0 {
		s = string(rune('0'+v%10)) + s
		v /= 10
	}
	for len(s) < w {
		s = "0" + s
	}
	return s
}
