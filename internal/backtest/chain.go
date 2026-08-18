// B4 全链路回测引擎：合成事件（板块涨停潮）→ 板块 → 个股 → 多因子信号 → 前瞻收益验证。
// 纯离线（读研究 SQLite 库），库化供 B5 优化器调用；回测参数由调用方/CLI 配置。
// （B4 full-chain backtest engine: synthesized sector limit-up events → sector → stocks →
// multi-factor signal → forward-return verification. Offline-only, library for B5.）
package backtest

import (
	"fmt"
	"math"
	"sort"
	"time"

	"quant-trading-v2/internal/factor"
	"quant-trading-v2/internal/research"
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
	Factors    []string       // 因子 ID（因子库注册名）
	Directions map[string]int // factorID → +1/-1（缺失按类别默认方向）
	Weights    map[string]float64 // factorID → 权重（缺失=1，B5 优化器产出）
	TopK       int            // 每事件选股数
	MinStocks  int            // 当日有效样本下限
	MinCover   float64        // 因子覆盖要求（0~1，缺失占比过高则剔除）
}

// DefaultRule 返回默认信号规则（7 大类精选 + 合理方向）。
// （DefaultRule returns the default signal rule.）
func DefaultRule() SignalRule {
	return SignalRule{
		Factors:   []string{"EP_ttm", "BP", "ROE", "YoyNetProfit", "SUE", "Mom20", "STO20"},
		Directions: nil, // 按类别默认
		TopK:      5,
		MinStocks: 10,
		MinCover:  0.5,
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
	Start        string // 事件区间起点 YYYYMMDD
	End          string // 事件区间终点 YYYYMMDD
	Horizons     []int  // 前瞻天数（默认 [1,5,10]）
	MinLimitUps  int    // 触发事件的行业涨停家数下限（默认 3）
	MaxPerDay    int    // 每日最多事件数（默认 3，取涨停家数最多）
	Benchmark    string // 基准指数（默认 000300.SH）
	Lookback     int    // 因子预热回看天数（默认 70）
	Rule         SignalRule
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

	// 一次性装配全部相关股票（含预热与前视窗口）
	lookStart, err := shiftDate(opts.Start, -opts.Lookback)
	if err != nil {
		return nil, err
	}
	fwdEnd, err := shiftDate(opts.End, maxH+3)
	if err != nil {
		return nil, err
	}
	panels, err := assembleAll(db, codes, lookStart, fwdEnd, opts.Rule)
	if err != nil {
		return nil, err
	}
	if len(panels) == 0 {
		return nil, fmt.Errorf("无装配面板（codes=%d）", len(codes))
	}
	// 基准指数
	bench, err := db.IndexBars(opts.Benchmark, lookStart, fwdEnd)
	if err != nil {
		return nil, err
	}
	benchIdx := make(map[string]int, len(bench))
	for i, b := range bench {
		benchIdx[b.Date] = i
	}

	rep := &ChainReport{
		Start: opts.Start, End: opts.End, Benchmark: opts.Benchmark,
		Rule: opts.Rule, Horizons: opts.Horizons,
	}
	for _, e := range events {
		er := evalEvent(panels, bench, benchIdx, e, opts.Rule, opts.Horizons)
		rep.Events = append(rep.Events, er)
		rep.TotalEvents++
		rep.TotalPicks += len(er.Picks)
	}
	rep.Summarize()
	return rep, nil
}

// evalEvent 对单个事件做"选股 → 前瞻收益 → 基准超额"。
// （evalEvent picks stocks for one event and validates forward excess returns.）
func evalEvent(panels map[string]*stockData, bench []store.Bar, benchIdx map[string]int,
	e SectorEvent, rule SignalRule, horizons []int) EventResult {
	er := EventResult{
		Date: e.Date, Industry: e.Industry,
		LimitUpCount: e.LimitUpCount, Constituents: e.Constituents,
		MeanExcess: map[int]float64{}, HitRate: map[int]float64{},
	}

	// 第一遍：收集事件日截面（每因子的当日值，过滤 ST/无价）
	vals := make(map[string]map[string]float64, len(rule.Factors)) // factorID → code → value
	for code, sd := range panels {
		idx, ok := sd.DateIdx[e.Date]
		if !ok || sd.Series.IsST[idx] == 1 || sd.Series.CloseHfq[idx] <= 0 {
			continue
		}
		for _, fid := range rule.Factors {
			fvals, ok := sd.FactorVals[fid]
			if !ok || idx >= len(fvals) || math.IsNaN(fvals[idx]) {
				continue
			}
			if vals[fid] == nil {
				vals[fid] = make(map[string]float64)
			}
			vals[fid][code] = fvals[idx]
		}
	}
	// 每因子截面均值/标准差
	meanStd := make(map[string]struct{ mean, std float64 }, len(rule.Factors))
	for fid, m := range vals {
		var sum, sum2 float64
		n := 0
		for _, v := range m {
			sum += v
			sum2 += v * v
			n++
		}
		if n < 2 {
			continue
		}
		mean := sum / float64(n)
		std := math.Sqrt(sum2/float64(n) - mean*mean)
		meanStd[fid] = struct{ mean, std float64 }{mean, std}
	}
	if len(meanStd) == 0 {
		return er
	}

	// 第二遍：z 分数复合 + 选股
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
			ms, has := meanStd[fid]
			if !has || ms.std == 0 {
				continue
			}
			v, ok := vals[fid][code]
			if !ok {
				continue
			}
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
			total += float64(dir) * w * (v - ms.mean) / ms.std
			used++
		}
		cover := 1.0
		// 覆盖度按"当日实际有截面变差的因子"计（无变差因子不算缺失）
		if len(meanStd) > 0 {
			cover = float64(used) / float64(len(meanStd))
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
		bj := firstAfter(benchIdx, e.Date)
		for _, h := range horizons {
			fi := c.next + h
			if fi >= sd.Series.Len() || sd.Series.CloseHfq[fi] <= 0 {
				continue
			}
			ret := sd.Series.CloseHfq[fi]/entryPrice - 1
			pick.Returns[h] = ret
			if bj >= 0 && bj+h < len(bench) && bench[bj].Open > 0 {
				bre := bench[bj+h].Close/bench[bj].Open - 1
				pick.Excess[h] = ret - bre
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

// firstAfter 返回日期 d 之后（含 d）第一个基准交易日下标；无则 -1。
func firstAfter(idx map[string]int, d string) int {
	best := ""
	for k := range idx {
		if k >= d && (best == "" || k < best) {
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
	Series     *factor.StockSeries
	DateIdx    map[string]int
	FactorVals map[string][]float64
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