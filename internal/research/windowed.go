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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"sort"
	"strings"

	"quant-trading-v2/internal/factor"
	"quant-trading-v2/internal/store"
)

// winCkpt 窗口级断点助手（二期）：命中即跳过该窗装配，算完即落库。
// nil 接收者安全（所有方法判空直通），便于调用方无断点场景复用同一代码路径。
// English: per-window checkpoint helper — a hit skips that window's assembly; completion persists it.
// nil-receiver safe so callers without checkpoints share the same code path.
type winCkpt struct {
	db        *store.DB
	resumeKey string
	stage     string
}

// load 尝试命中窗口断点并反序列化到 dst：nil 接收者 / 未命中 / JSON 损坏一律返回 false
// （调用方走正常装配路径，断点只是加速而非正确性依赖）。
func (c *winCkpt) load(w [2]string, dst any) bool {
	if c == nil || c.db == nil {
		return false
	}
	js, ok, err := c.db.GetWindowCkpt(c.resumeKey, c.stage, w[0], w[1])
	if err != nil || !ok {
		return false
	}
	return json.Unmarshal([]byte(js), dst) == nil
}

// save 把当前窗口的产物落库（序列化失败静默跳过，不阻断发现主流程）。
func (c *winCkpt) save(w [2]string, v any) {
	if c == nil || c.db == nil {
		return
	}
	js, err := json.Marshal(v)
	if err == nil {
		_ = c.db.PutWindowCkpt(c.resumeKey, c.stage, w[0], w[1], string(js))
	}
}

// stageProgress 阶段进度：把窗口完成数映射到全局百分比带并打印"发现进度 xx%"
// （worker 按 (?:任务|回测|发现)进度 解析回写队列；同时喂看门狗）。
// English: maps finished windows into a global percentage band and prints "发现进度 xx%" for the worker.
type stageProgress struct {
	lo, hi int // 全局百分比带
	total  int // 窗口总数
	done   int
}

// newStageProgress 构造阶段进度器：total<=0 返回 nil（tick 对 nil 直通，无进度场景零开销）。
func newStageProgress(lo, hi, total int) *stageProgress {
	if total <= 0 {
		return nil
	}
	return &stageProgress{lo: lo, hi: hi, total: total}
}

// tick 每完成一个窗口调用一次：映射到 [lo,hi] 百分比带打印"发现进度 xx%"，
// worker 正则解析后回写队列并喂看门狗。
func (p *stageProgress) tick() {
	if p == nil {
		return
	}
	p.done++
	pct := p.lo + (p.hi-p.lo)*p.done/p.total
	log.Printf("发现进度 %d%%", pct)
}

// discoveryResumeKey 断点键：任何影响结果的参数（区间/前瞻/最小样本/窗口宽/因子池/股票池）
// 变更都会生成新 key，旧缓存自动失效。English: checkpoint key — any result-affecting change rolls a fresh key.
func discoveryResumeKey(start, end string, horizon, minStocks, winDays int, fids []string, codes []string) string {
	sorted := append([]string{}, fids...)
	sort.Strings(sorted)
	sum := sha256.Sum256([]byte(fmt.Sprintf("%v", codes)))
	return fmt.Sprintf("df|%s|%s|h%d|ms%d|w%d|%s|%s",
		start, end, horizon, minStocks, winDays,
		strings.Join(sorted, ","), hex.EncodeToString(sum[:])[:10])
}

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

// weightsTag 权重集的稳定短标识（断点 stage 用：不同权重 = 不同缓存槽）。
func weightsTag(w map[string]float64) string {
	keys := make([]string, 0, len(w))
	for k := range w {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	sum := sha256.Sum256([]byte(fmt.Sprintf("%v", keys)))
	return hex.EncodeToString(sum[:])[:8]
}

// windowCompositeIC 按窗口分块装配，累积 CompositeIC 的逐日 IC 行（全区间）。
// 每窗口：BuildPanels 装配 [winStart, endPlusH]（多算 h 天尾巴保证前瞻收益完整），
// 然后 CompositeICRange 只统计窗口内日期。窗口算完释放。
// ck 非 nil 时启用断点（stage 需含权重标识——行值依赖权重）；nil 直通无缓存。
// English: chunked CompositeIC rows over the full range; checkpoint-aware when ck is non-nil
// (its stage must embed the weights tag since row values depend on them); nil passes through.
func windowCompositeIC(db *store.DB, codes []string, factors []string, weights map[string]float64, h, min int, chunks [][2]string, dates []string, ck *winCkpt) []ICRow {
	defs := windowDefs(factors)
	var all []ICRow
	for _, w := range chunks {
		var rows []ICRow
		if ck.load(w, &rows) {
			all = append(all, rows...)
			continue
		}
		asmbEnd := w[1]
		for i := 0; i < h; i++ {
			asmbEnd = nextDayStr(asmbEnd)
		}
		panels, err := BuildPanels(db, codes, windowAsmStart(dates, w[0]), asmbEnd, defs)
		if err != nil {
			continue
		}
		rows = CompositeICRange(panels, factors, weights, h, min, w[0], w[1])
		ck.save(w, rows)
		all = append(all, rows...)
	}
	return all
}

// windowICByAllFactors 每窗口只装配一次（含全部候选因子），算出窗口内**所有**因子的
// 单因子 IC 行（预筛阶段，装配次数从「因子数×窗口数」降到「窗口数」）。
// 断点 stage="pre"；prog 上报窗口完成进度。English: single-factor pre-screen per window;
// checkpoint stage "pre", progress reported per window.
func windowICByAllFactors(db *store.DB, codes []string, fids []string, h, min int, chunks [][2]string, dates []string, ck *winCkpt, prog *stageProgress) map[string][]ICRow {
	out := make(map[string][]ICRow, len(fids))
	if len(fids) == 0 {
		return out
	}
	defs := windowDefs(fids)
	for _, w := range chunks {
		var winAll map[string][]ICRow
		if ck.load(w, &winAll) && len(winAll) > 0 {
			for fid, rows := range winAll {
				out[fid] = append(out[fid], rows...)
			}
			prog.tick()
			continue
		}
		asmbEnd := w[1]
		for i := 0; i < h; i++ {
			asmbEnd = nextDayStr(asmbEnd)
		}
		panels, err := BuildPanels(db, codes, windowAsmStart(dates, w[0]), asmbEnd, defs)
		if err != nil {
			prog.tick()
			continue
		}
		winAll = make(map[string][]ICRow, len(fids))
		for _, fid := range fids {
			rows := ICByDate(panels, fid, h, min)
			var kept []ICRow
			for _, r := range rows {
				if r.Date >= w[0] && r.Date <= w[1] {
					out[fid] = append(out[fid], r)
					kept = append(kept, r)
				}
			}
			winAll[fid] = kept
		}
		ck.save(w, winAll)
		prog.tick()
	}
	return out
}

// windowCompositeIR 返回全区间复合 |IR|（窗口分块，断点 stage 含权重标识）。
func windowCompositeIR(db *store.DB, codes []string, factors []string, weights map[string]float64, h, min int, chunks [][2]string, dates []string, rk string) float64 {
	ck := &winCkpt{db: db, resumeKey: rk, stage: "ir|" + weightsTag(weights)}
	rows := windowCompositeIC(db, codes, factors, weights, h, min, chunks, dates, ck)
	ir := IR(rows)
	if math.IsNaN(ir) {
		return 0
	}
	return math.Abs(ir)
}

// windowCompositeICForSubsets 贪心选择的提速版：每窗口只装配一次（含 base+全部候选因子），
// 在该窗口内对每个候选子集（base+每个 cand）各算 CompositeIC 行并累积。
// 断点 stage 含 base 标识（base 集不同 = 不同缓存槽）。English: greedy-step speedup with
// per-window checkpointing keyed by the base set.
func windowCompositeICForSubsets(db *store.DB, codes []string, base, cands []string, h, min int, chunks [][2]string, dates []string, ck *winCkpt) map[string][]ICRow {
	out := make(map[string][]ICRow, len(cands))
	if len(cands) == 0 {
		return out
	}
	// 装配用的因子 = base + 全部候选
	fids := append(append([]string{}, base...), cands...)
	defs := windowDefs(fids)
	for _, w := range chunks {
		var winOut map[string][]ICRow
		if ck.load(w, &winOut) && len(winOut) > 0 {
			for cand, rows := range winOut {
				out[cand] = append(out[cand], rows...)
			}
			continue
		}
		asmbEnd := w[1]
		for i := 0; i < h; i++ {
			asmbEnd = nextDayStr(asmbEnd)
		}
		panels, err := BuildPanels(db, codes, windowAsmStart(dates, w[0]), asmbEnd, defs)
		if err != nil {
			continue
		}
		winOut = make(map[string][]ICRow, len(cands))
		for _, cand := range cands {
			candFactors := append(append([]string{}, base...), cand)
			wm := map[string]float64{}
			for _, f := range candFactors {
				wm[f] = 1.0
			}
			rows := CompositeICRange(panels, candFactors, wm, h, min, w[0], w[1])
			out[cand] = append(out[cand], rows...)
			winOut[cand] = rows
		}
		ck.save(w, winOut)
	}
	return out
}

// windowReverseExtension 按窗口分块累积反推泛化的 top/rest 收益数组并做 Welch t 检验。
// 断点 stage="gen"（每窗缓存 top/rest 数组）。English: window-chunked reverse-extension with
// per-window checkpoints (stage "gen" caches top/rest arrays).
func windowReverseExtension(db *store.DB, codes []string, factors []string, dirs map[string]int, weights map[string]float64, opts DiscoverOpts, chunks [][2]string, dates []string, rk string) (float64, float64, float64, float64, float64) {
	defs := windowDefs(factors)
	ck := &winCkpt{db: db, resumeKey: rk, stage: "gen"}
	var topRets, restRets []float64
	for _, w := range chunks {
		var winTR struct {
			Top  []float64 `json:"top"`
			Rest []float64 `json:"rest"`
		}
		if ck.load(w, &winTR) {
			topRets = append(topRets, winTR.Top...)
			restRets = append(restRets, winTR.Rest...)
			continue
		}
		asmbEnd := w[1]
		for i := 0; i < opts.Horizon; i++ {
			asmbEnd = nextDayStr(asmbEnd)
		}
		panels, err := BuildPanels(db, codes, windowAsmStart(dates, w[0]), asmbEnd, defs)
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
					winTR.Top = append(winTR.Top, v.r)
				} else {
					restRets = append(restRets, v.r)
					winTR.Rest = append(winTR.Rest, v.r)
				}
			}
		}
		ck.save(w, winTR)
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
// 但内部用 windowCompositeIC 代替全量面板的 CompositeIC。权重候选是瞬态的（坐标上升每步
// 组合都不同），不做窗口断点——断点只覆盖输入确定的阶段（预筛/贪心/分段IR/反推）。
// English: window-chunked coordinate-ascent weight optimization. Candidate weights are transient
// (different every ascent step), so no window checkpoints here — checkpoints only cover
// deterministic stages (pre-screen / greedy / split-IR / reverse-extension).
func windowOptimizeWeights(db *store.DB, codes []string, opts OptimizeOpts, chunks [][2]string, dates []string) OptResult {
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
	best := windowEval(db, codes, opts, w, chunks, dates)
	for it := 0; it < opts.MaxIter; it++ {
		improved := false
		for _, f := range opts.Factors {
			for _, delta := range []float64{opts.Step, -opts.Step} {
				cand := cloneWeights(w)
				cand[f] += delta
				if cand[f] < 0 {
					cand[f] = 0
				}
				r := windowEval(db, codes, opts, cand, chunks, dates)
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

// windowEval 用窗口分块计算某权重下的 IC 统计（等价 evaluate，但走窗口内核；无断点）。
func windowEval(db *store.DB, codes []string, opts OptimizeOpts, w map[string]float64, chunks [][2]string, dates []string) OptResult {
	rows := windowCompositeIC(db, codes, opts.Factors, w, opts.Horizon, opts.MinStocks, chunks, dates, nil)
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
	winDays := windowDays
	chunks := windowChunks(dates, winDays)

	// 窗口级断点（二期）：resume_key 绑定区间+参数+股票池；被抢占后重入，
	// 预筛/贪心/分段IR/反推各阶段命中窗口直接复用，不再重装配。
	// English: window-level checkpoints — resume key binds range+params+pool; a preempted rerun
	// reuses finished windows across the pre-screen / greedy / split-IR / gen stages.
	rk := discoveryResumeKey(start, end, opts.Horizon, opts.MinStocks, winDays, opts.Factors, codes)
	log.Printf("[discover] 断点key=%s 窗口数=%d（中断续跑跳过已完成窗口）", rk, len(chunks))

	// 1) 单因子预筛：每窗口装配一次（含全部候选因子），一次性算所有因子的全区间 |IR|
	// （比逐因子重新装配窗口快约「因子数」倍）。进度带 5%–35%。
	// English: single-factor pre-screen (progress band 5–35%).
	var pre []string
	preCk := &winCkpt{db: db, resumeKey: rk, stage: "pre"}
	allIC := windowICByAllFactors(db, codes, opts.Factors, opts.Horizon, opts.MinStocks, chunks, dates, preCk, newStageProgress(5, 35, len(chunks)))
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
		// 断点 stage 含 base 标识：贪心每步 base 集不同，各自成槽（stage embeds the base set）。
		subsetIC := windowCompositeICForSubsets(db, codes, selected, cands, opts.Horizon, opts.MinStocks, chunks, dates,
			&winCkpt{db: db, resumeKey: rk, stage: "greedy|" + strings.Join(selected, "+")})
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
	}, chunks, dates)
	res.Factors = selected
	res.Directions = dirs
	res.Weights = opt.Weights
	res.ICMean = opt.ICMean
	res.IR = opt.IR
	res.NDays = opt.NDays
	res.PassGuard = opt.PassGuard
	res.Reason = opt.Reason

	// 4) E3 分段 + 反推验证（窗口分块）
	// 分段 IR 与反推验证：输入确定（selected+权重固定），窗口断点全量生效。
	splitChunks := windowChunks(dates[splitIdx:], winDays)
	headChunks := windowChunks(dates[:splitIdx+1], winDays)
	res.InsampleIR = windowCompositeIR(db, codes, selected, opt.Weights, opts.Horizon, opts.MinStocks, headChunks, dates, rk)
	res.OutsampleIR = windowCompositeIR(db, codes, selected, opt.Weights, opts.Horizon, opts.MinStocks, splitChunks, dates, rk)
	res.GenTopMean, res.GenAllMean, res.GenExcess, res.GenStdErr, res.GenT =
		windowReverseExtension(db, codes, selected, dirs, opt.Weights, opts, splitChunks, dates, rk)

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

// itoa4/itoa2 定宽零填充整数字符串（4 位年 / 2 位月日），供 storeNextDay 拼回 YYYYMMDD。
func itoa4(v int) string { return itoaN(v, 4) }

// itoa2 同 itoa4，宽度 2（月/日）。
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

// windowAsmStart 窗口装配起点：从 w0 左移 patternWarmupDays 个交易日补算子回看预热。
// 形态/动量类算子含 20 日级回看，若窗口头直接从 w0 装配，头部因子值为 NaN，
// 造成"窗口版 vs 全量版"系统性分歧；评估仍限定窗口内日期，口径与全量版对齐。
// English: per-window assembly start — shift back warm-up trade-days so operator lookbacks are
// fully warmed at every window head (evaluation stays restricted to the window).
func windowAsmStart(dates []string, w0 string) string {
	gi := sort.SearchStrings(dates, w0)
	lo := gi - patternWarmupDays
	if lo < 0 {
		lo = 0
	}
	return dates[lo]
}

// WindowChunks 导出版窗口切分：把交易日列表按 winDays 切成 [start,end] 窗口
// （winDays<=0 用默认 windowDays）。供 B4 全链路回测等大装配场景复用，
// 与 discover-factors 同一内存口径：峰值只装一个窗口。
// English: exported window splitter — chunks a trade-date list into [start,end] windows
// (winDays<=0 uses the default). Reused by the B4 chain backtest so peak memory stays at one window.
func WindowChunks(dates []string, winDays int) [][2]string {
	return windowChunks(dates, winDays)
}
