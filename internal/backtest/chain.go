// B4 全链路回测引擎：合成事件（板块涨停潮）→ 板块 → 个股 → 多因子信号 → 前瞻收益验证。
// 纯离线（读研究 SQLite 库），库化供 B5 优化器调用；回测参数由调用方/CLI 配置。
// English: B4 full-chain backtest engine: synthesized events (sector limit-up surges) → sector → stocks
// → multi-factor signals → forward-return validation. Purely offline (reads the research SQLite DB),
// exposed as a library for the B5 optimizer; params come from the caller/CLI.
// （B4 full-chain backtest engine: synthesized sector limit-up events → sector → stocks →
// multi-factor signal → forward-return verification. Offline-only, library for B5.）
package backtest

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"log"
	"math"
	"sort"
	"time"

	"quant-trading-v2/internal/factor"
	"quant-trading-v2/internal/research"
	"quant-trading-v2/internal/research/scoring"
	"quant-trading-v2/internal/store"
)

// SectorEvent 合成事件：某行业在某交易日的"板块涨停潮"。
// （SectorEvent is a synthesized sector limit-up surge event.）
type SectorEvent struct {
	Date         string // 事件日 YYYYMMDD
	Industry     string // 行业
	LimitUpCount int    // 涨停家数
	Constituents int    // 当日成分股数
}

// SignalRule 多因子复合信号规则。
// （SignalRule is the composite multi-factor signal rule.）
type SignalRule struct {
	Factors    []string           // 因子 ID（因子库注册名）
	Directions map[string]int     // factorID → +1/-1（缺失按类别默认方向）
	Weights    map[string]float64 // factorID → 权重（缺失=1，B5 优化器产出）
	TopK       int                // 每事件选股数
	MinStocks  int                // 当日有效样本下限
	MinCover   float64            // 因子覆盖要求（0~1，缺失占比过高则剔除）
}

// Fingerprint 规则参数指纹（§GAP 二.3#5）：因子集合/方向/权重/TopK/MinStocks/MinCover 的
// 规范化序列化哈希。作为 backtest_event_results 断点缓存键的组成部分——此前缓存 key 不含参数，
// 同一候选改参重跑会命中旧结果，产出新旧混杂的报告。
// English: canonical fingerprint of the rule parameters, part of the event-result cache key so a
// parameter change on the same candidate can no longer serve stale cached results.
func (r SignalRule) Fingerprint() string {
	factors := append([]string(nil), r.Factors...)
	sort.Strings(factors)
	keys := make([]string, 0, len(r.Directions)+len(r.Weights))
	dirs := make(map[string]int, len(r.Directions))
	wts := make(map[string]float64, len(r.Weights))
	for k, v := range r.Directions {
		keys = append(keys, "d:"+k)
		dirs[k] = v
	}
	for k, v := range r.Weights {
		keys = append(keys, "w:"+k)
		wts[k] = v
	}
	sort.Strings(keys)
	h := fnv.New64a()
	fmt.Fprintf(h, "f=%v;", factors)
	for _, k := range keys {
		if k[0] == 'd' {
			fmt.Fprintf(h, "%s=%d;", k, dirs[k[2:]])
		} else {
			fmt.Fprintf(h, "%s=%.6f;", k, wts[k[2:]])
		}
	}
	fmt.Fprintf(h, "topk=%d;minstocks=%d;mincover=%.4f", r.TopK, r.MinStocks, r.MinCover)
	return fmt.Sprintf("%016x", h.Sum64())
}

// DefaultRule 返回默认信号规则（7 大类精选 + 合理方向）。
// （DefaultRule returns the default signal rule.）
func DefaultRule() SignalRule {
	return SignalRule{
		Factors:    []string{"EP_ttm", "BP", "ROE", "YoyNetProfit", "SUE", "Mom20", "STO20"},
		Directions: nil, // 按类别默认
		TopK:       5,
		MinStocks:  10,
		MinCover:   0.5,
	}
}

// dirOf 返回因子的方向：配置优先，否则按类别默认（价值/成长/质量/动量/流动性 看多，
// 规模/波动率 看空）。
func dirOf(fdef factor.Def, dirs map[string]int) int {
	if v, ok := dirs[fdef.ID]; ok {
		return v
	}
	switch fdef.Cat {
	case factor.CatValue, factor.CatGrowth, factor.CatQuality,
		factor.CatMomentum, factor.CatLiquidity:
		return +1
	default: // 规模/波动率
		return -1
	}
}

// Options 回测选项。
// （Options configures a backtest run.）
type Options struct {
	Start       string     // 事件区间起点 YYYYMMDD
	End         string     // 事件区间终点 YYYYMMDD
	Horizons    []int      // 前瞻天数（默认 [1,5,10]）
	MinLimitUps int        // 触发事件的行业涨停家数下限（默认 3）
	MaxPerDay   int        // 每日最多事件数（默认 3，取涨停家数最多）
	Benchmark   string     // 基准指数（默认 000300.SH）
	Lookback    int        // 因子预热回看天数（默认 70）
	Rule        SignalRule // 触发信号规则（决定事件归类与成本计算）
	// Cost 交易成本模型（B4 成本模型，修复零成本假设）：默认 DefaultCostModel（佣金万2.5+最低5元、
	// 滑点 5bp 双边、卖出印花税 0.05%，与模拟盘/btreplay 同源）。零值时 Run 自动填充默认值。
	// English: trading-cost model; Run fills the default when left zero-valued.
	Cost CostModel
	// OnProgress 可选进度回调（已处理事件数/总事件数）。供 CLI/HTTP 上报回测进度，
	// 前端据此渲染"全链路回测进度条"。nil 时不回调。
	// English: optional progress callback (events done / total). Lets the CLI/HTTP layer report
	// backtest progress so the frontend can render a "full-chain backtest" progress bar. nil = no-op.
	OnProgress func(done, total int)

	// CandidateID 候选 ID（>0 时启用断点续跑）：每个事件先读 backtest_event_results 缓存，
	// 命中则复用结果跳过重算，未命中才 evalEvent 并落库。中断/重启后续跑只重算剩余事件，
	// 且同一候选重跑覆盖旧缓存（规则参数变更后自动失效）。
	// English: candidate ID — when > 0, checkpoint-resume is enabled: each event first reads the
	// backtest_event_results cache, reusing a hit and skipping recomputation; a miss is computed and
	// persisted. A resumed run only recomputes events not yet cached; reruns overwrite stale rows so a
	// parameter change on the same candidate invalidates old cache automatically.
	CandidateID int64
}

// DefaultOptions 返回默认回测选项。
// （DefaultOptions returns default backtest options.）
func DefaultOptions() Options {
	return Options{
		Horizons:    []int{1, 5, 10},
		MinLimitUps: 3,
		MaxPerDay:   3,
		Benchmark:   "000300.SH",
		Lookback:    70,
		Rule:        DefaultRule(),
	}
}

// Run 执行全链路回测，返回汇总报告。
// （Run executes the full-chain backtest and returns the summary report.）
func Run(db *store.DB, opts Options) (*ChainReport, error) {
	if opts.Horizons == nil {
		opts.Horizons = []int{1, 5, 10}
	}
	if opts.MinLimitUps <= 0 {
		opts.MinLimitUps = 3
	}
	if opts.MaxPerDay <= 0 {
		opts.MaxPerDay = 3
	}
	if opts.Benchmark == "" {
		opts.Benchmark = "000300.SH"
	}
	if opts.Lookback <= 0 {
		opts.Lookback = 70
	}
	if opts.Rule.TopK <= 0 {
		opts.Rule.TopK = 5
	}
	if opts.Rule.MinStocks <= 0 {
		opts.Rule.MinStocks = 10
	}
	if opts.Rule.MinCover <= 0 {
		opts.Rule.MinCover = 0.5
	}
	// B4 成本模型：未显式配置时使用与模拟盘/btreplay 同源的默认成本（佣金万2.5+最低5元、滑点5bp、印花税0.05%）。
	if opts.Cost == (CostModel{}) {
		opts.Cost = DefaultCostModel()
	}
	maxH := 0
	for _, h := range opts.Horizons {
		if h > maxH {
			maxH = h
		}
	}

	events, err := SynthesizeEvents(db, opts.Start, opts.End, opts.MinLimitUps, opts.MaxPerDay)
	if err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return nil, fmt.Errorf("无合成事件（区间 %s-%s，minLimitUps=%d）", opts.Start, opts.End, opts.MinLimitUps)
	}
	// 进度反馈提前（§8.6-A）：合成完成即回报 0%——首个事件前还有整窗装配（分钟级），
	// 没有这一行前端进度条会全程空窗，观感即"卡死"。
	// English: emit an immediate 0% progress right after synthesis; the first window assembly takes
	// minutes and without this the frontend bar sits empty the whole time.
	if opts.OnProgress != nil && len(events) > 0 {
		opts.OnProgress(0, len(events))
	}

	// 收集事件相关股票代码
	codeSet := make(map[string]bool)
	for _, e := range events {
		codes, err := db.IndustryConstituents(e.Industry, e.Date)
		if err != nil {
			return nil, err
		}
		for _, c := range codes {
			codeSet[c] = true
		}
	}
	codes := make([]string, 0, len(codeSet))
	for c := range codeSet {
		codes = append(codes, c)
	}
	sort.Strings(codes)

	// 资源治理（docs/RESEARCH_TASK_QUEUE_PLAN.md §7）：窗口分块装配。
	// 旧实现把全部相关股票在完整区间一次性装配（全市场×3年可达数 GB）；
	// 现改为按交易日窗口（复用 discover-factors 的 windowDays 口径，60 日/窗）
	// 逐窗"装配-评估-释放"，峰值内存压到单窗口水平。窗口内仍是完整截面，
	// 评估口径与旧实现一致；断点缓存命中的窗口直接跳过装配。
	// English: resource governance — window-chunked assembly. The legacy path assembled every
	// related stock over the full range at once (multi-GB for the full universe); now each
	// trading-day window (same 60-day cadence as factor discovery) is assembled, evaluated and
	// released in turn, keeping peak memory at single-window scale with identical semantics.
	dates, err := db.TradeDates(opts.Start, opts.End)
	if err != nil {
		return nil, err
	}
	windows := research.WindowChunks(dates, 0)

	rep := &ChainReport{
		Start: opts.Start, End: opts.End, Benchmark: opts.Benchmark,
		Rule: opts.Rule, Horizons: opts.Horizons,
	}
	total := len(events)
	done := 0
	for _, w := range windows {
		winEvents := filterEvents(events, w[0], w[1])
		if len(winEvents) == 0 {
			continue
		}
		// 断点续跑：整窗命中缓存则跳过装配（省内存也省时间）。
		// §GAP 二.3#5：缓存键携带规则参数指纹，改参后旧缓存自动失效。
		ruleFP := opts.Rule.Fingerprint()
		if opts.CandidateID > 0 && allEventsCached(db, opts.CandidateID, ruleFP, winEvents) {
			for _, e := range winEvents {
				var er EventResult
				if js, ok, err := db.GetBacktestEventResult(opts.CandidateID, e.Date, e.Industry, ruleFP); err == nil && ok {
					if json.Unmarshal([]byte(js), &er) == nil {
						rep.Events = append(rep.Events, er)
						rep.TotalEvents++
						rep.TotalPicks += len(er.Picks)
					}
				}
				done++
				if opts.OnProgress != nil {
					opts.OnProgress(done, total)
				}
			}
			continue
		}
		assembleStart, err := shiftDate(w[0], -opts.Lookback)
		if err != nil {
			return nil, err
		}
		assembleEnd, err := shiftDate(w[1], maxH+3)
		if err != nil {
			return nil, err
		}
		panels, err := assembleAll(db, codes, assembleStart, assembleEnd, opts.Rule)
		if err != nil {
			return nil, err
		}
		bench, err := db.IndexBars(opts.Benchmark, assembleStart, assembleEnd)
		if err != nil {
			return nil, err
		}
		benchIdx := make(map[string]int, len(bench))
		for i, b := range bench {
			benchIdx[b.Date] = i
		}
		for _, e := range winEvents {
			var er EventResult
			cached := false
			if opts.CandidateID > 0 {
				if js, ok, err := db.GetBacktestEventResult(opts.CandidateID, e.Date, e.Industry, ruleFP); err == nil && ok {
					if json.Unmarshal([]byte(js), &er) == nil {
						cached = true
					}
				}
			}
			if !cached {
				er = evalEvent(panels, bench, benchIdx, e, opts.Rule, opts.Horizons, opts.Cost)
				if opts.CandidateID > 0 {
					if js, err := json.Marshal(er); err == nil {
						if err := db.UpsertBacktestEventResult(opts.CandidateID, e.Date, e.Industry, ruleFP, string(js)); err != nil {
							log.Printf("backtest: 缓存事件结果失败 cand=%d %s/%s: %v", opts.CandidateID, e.Date, e.Industry, err)
						}
					}
				}
			}
			rep.Events = append(rep.Events, er)
			rep.TotalEvents++
			rep.TotalPicks += len(er.Picks)
			done++
			if opts.OnProgress != nil {
				opts.OnProgress(done, total)
			}
		}
		panels = nil // 窗口算完即释放（release the window's panels）
	}
	rep.Summarize()
	return rep, nil
}

// filterEvents 取日期落在 [start,end] 的窗口内事件（保持原顺序）。
func filterEvents(events []SectorEvent, start, end string) []SectorEvent {
	out := make([]SectorEvent, 0, len(events))
	for _, e := range events {
		if e.Date >= start && e.Date <= end {
			out = append(out, e)
		}
	}
	return out
}

// allEventsCached 判断窗口内全部事件是否都有断点缓存（决定该窗是否可跳过装配）。
// ruleFP 参与键匹配：规则参数变更后窗口视为未缓存，强制重算（不再复用旧参结果）。
func allEventsCached(db *store.DB, candID int64, ruleFP string, winEvents []SectorEvent) bool {
	for _, e := range winEvents {
		if _, ok, err := db.GetBacktestEventResult(candID, e.Date, e.Industry, ruleFP); err != nil || !ok {
			return false
		}
	}
	return true
}

// evalEvent 对单个事件做"选股 → 前瞻收益 → 基准超额"。
// （evalEvent picks stocks for one event and validates forward excess returns.）
func evalEvent(panels map[string]*stockData, bench []store.Bar, benchIdx map[string]int,
	e SectorEvent, rule SignalRule, horizons []int, cost CostModel) EventResult {
	er := EventResult{
		Date: e.Date, Industry: e.Industry,
		LimitUpCount: e.LimitUpCount, Constituents: e.Constituents,
		MeanExcess: map[int]float64{}, HitRate: map[int]float64{},
	}

	// 选股打分：统一改用「单股时序分位」(scoring.ScoreValue)，与实盘 internal/strategies/factor 同口径，
	// 修复旧实现「截面 z 标准化」导致的「研究↔实盘打分语义断层」。每只股票逐因子取其事件日值
	// 在「自身因子历史（截至事件日，无未来泄漏）」中的分位（看多=分位，看空=1-分位），加权复合后排序选 TopK。
	type cand struct {
		code  string
		score float64
		next  int
	}
	var cands []cand
	for code, sd := range panels {
		idx, ok := sd.DateIdx[e.Date]
		if !ok {
			continue
		}
		if sd.Series.IsST[idx] == 1 || sd.Series.CloseHfq[idx] <= 0 {
			continue
		}
		next := idx + 1
		if next >= sd.Series.Len() {
			continue
		}
		total, used := 0.0, 0
		for _, fid := range rule.Factors {
			fvals, ok := sd.FactorVals[fid]
			if !ok || idx >= len(fvals) || math.IsNaN(fvals[idx]) {
				continue
			}
			// 时序分位：事件日值在该股自身因子历史（含当日）中的分位秩，与实盘同源。
			pct := scoring.ScoreValue(fvals[:idx+1], fvals[idx])
			if math.IsNaN(pct) {
				continue
			}
			// 方向与实盘一致：看多因子取分位（越高越好），看空因子取 1-分位（越低越好）。
			dir := 1
			if fd, ok := factor.Get(fid); ok {
				dir = dirOf(fd, rule.Directions)
			}
			w := 1.0
			if rule.Weights != nil {
				if wv, ok := rule.Weights[fid]; ok {
					w = wv
				}
			}
			contrib := w * pct
			if dir < 0 {
				contrib = w * (1 - pct)
			}
			total += contrib
			used++
		}
		cover := 1.0
		// 覆盖度按"当日实际可用的因子"计（与规则配置因子数对比）。
		if len(rule.Factors) > 0 {
			cover = float64(used) / float64(len(rule.Factors))
		}
		if used == 0 || cover < rule.MinCover {
			continue
		}
		cands = append(cands, cand{code: code, score: total, next: next})
	}
	if len(cands) < rule.MinStocks {
		return er
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].score > cands[j].score })
	if len(cands) > rule.TopK {
		cands = cands[:rule.TopK]
	}
	for _, c := range cands {
		sd := panels[c.code]
		entryPrice := sd.Series.Open[c.next]
		if entryPrice <= 0 {
			continue
		}
		pick := Pick{
			Code: c.code, Score: c.score,
			EntryDate: sd.Series.Dates[c.next], EntryPrice: entryPrice,
			Returns: map[int]float64{}, Excess: map[int]float64{},
		}
		// §GAP 二.3#3 基准窗对齐：个股收益窗从"事件次日开盘"起算（entry=c.next Open），
		// 基准窗必须同为"首个严格晚于事件日的交易日开盘"起算——旧 firstAfter 含事件日本身，
		// 基准多算一天行情，超额收益被系统性抬高/压低一天。
		// English: benchmark window must start at the first trading day STRICTLY after the event date,
		// matching the stock entry at next-day open (the old >=d semantics included the event day itself).
		bj := firstStrictlyAfter(benchIdx, e.Date)
		for _, h := range horizons {
			fi := c.next + h
			if fi >= sd.Series.Len() || sd.Series.CloseHfq[fi] <= 0 {
				continue
			}
			ret := sd.Series.CloseHfq[fi]/entryPrice - 1
			// B4 成本模型：从毛收益中扣除往返交易成本（滑点+佣金+印花税），得到可代表实盘机制的净收益。
			netRet := cost.NetReturn(ret, cost.AssumeNotional)
			pick.Returns[h] = netRet
			if bj >= 0 && bj+h < len(bench) && bench[bj].Open > 0 {
				bre := bench[bj+h].Close/bench[bj].Open - 1
				pick.Excess[h] = netRet - bre
			}
		}
		er.Picks = append(er.Picks, pick)
	}
	for _, h := range horizons {
		var sum float64
		var n, wins int
		for _, p := range er.Picks {
			if v, ok := p.Excess[h]; ok {
				sum += v
				n++
				if v > 0 {
					wins++
				}
			}
		}
		if n > 0 {
			er.MeanExcess[h] = sum / float64(n)
			er.HitRate[h] = float64(wins) / float64(n)
		}
	}
	return er
}

// firstStrictlyAfter 返回日期 d 之后（严格不含 d）第一个基准交易日下标；无则 -1。
// §GAP 二.3#3 基准窗对齐：个股从事件次日开盘入场，基准窗同口径（旧实现含事件日，错位一天）。
func firstStrictlyAfter(idx map[string]int, d string) int {
	best := ""
	for k := range idx {
		if k > d && (best == "" || k < best) {
			best = k
		}
	}
	if best == "" {
		return -1
	}
	return idx[best]
}

// stockData 装配后的单股数据：序列 + 因子值缓存。
type stockData struct {
	Series     *factor.StockSeries  // 装配后的单股序列
	DateIdx    map[string]int       // 日期 → 序列下标
	FactorVals map[string][]float64 // 因子名 → 逐日因子值缓存
}

// shiftDate 把 YYYYMMDD 平移 d 天（日历日，够用）。
func shiftDate(s string, d int) (string, error) {
	t, err := time.Parse("20060102", s)
	if err != nil {
		return "", err
	}
	return t.AddDate(0, 0, d).Format("20060102"), nil
}

// assembleAll 装配一批股票并预计算规则因子。
func assembleAll(db *store.DB, codes []string, start, end string, rule SignalRule) (map[string]*stockData, error) {
	out := make(map[string]*stockData, len(codes))
	for _, code := range codes {
		series, err := research.Assemble(db, code, start, end)
		if err != nil {
			continue
		}
		sd := &stockData{
			Series:     series,
			DateIdx:    make(map[string]int, len(series.Dates)),
			FactorVals: make(map[string][]float64, len(rule.Factors)),
		}
		for i, d := range series.Dates {
			sd.DateIdx[d] = i
		}
		for _, fid := range rule.Factors {
			fd, ok := factor.Get(fid)
			if !ok {
				continue
			}
			sd.FactorVals[fid] = fd.Compute(series)
		}
		out[code] = sd
	}
	return out, nil
}
