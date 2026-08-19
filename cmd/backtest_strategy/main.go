// Package main 四大手写战法历史胜率回测命令。
// 从离线研究库（trading.db）读取历史日K，逐交易日回放 dragon/double_bump/dragon_return/n_shape
// 四个战法的触发信号，次日开盘入场，用各战法的 CheckExit 逐日模拟平仓并结算盈亏，
// 输出按战法分组的胜率/平均盈亏/盈亏比，以及 1/5/10 日前瞻收益，用于验证与调参。
//
// 说明（近似口径）：板块/日内/LLM 依赖按如下方式近似——
//   - double_bump：纯日K完整回放，最接近实盘。
//   - dragon_return：从日K派生 StockData，板块龙性（IsSectorTop2/SectorRPS20）可配置近似。
//   - dragon：板块共振（F2/F3）用所属行业板块当日涨幅近似；无行业数据时降级忽略板块维度。
//   - n_shape：高度依赖日内快照与 LLM D1，日K近似后准确性打折；D1 用可配置规则分（默认 0，
//     此时仅统计其他维度，几乎不触发，需配合 -d1 提供规则分才有信号）。
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/store"
	"quant-trading-v2/internal/strategies/double_bump"
	"quant-trading-v2/internal/strategies/dragon"
	"quant-trading-v2/internal/strategies/dragon_return"
	"quant-trading-v2/internal/strategies/n_shape"
	"quant-trading-v2/internal/strategy"
)

// signal 一次回测触发的入场信号记录。
type signal struct {
	Strategy string             // 战法名
	Code     string             // 股票代码（纯数字）
	Date     string             // 触发交易日 YYYYMMDD
	Entry    float64            // 入场价（次日开盘）
	Meta     map[string]float64 // 入场评分明细（供 CheckExit 使用）
}

// trade 一次完整入场→平仓的交易结果。
type trade struct {
	Strategy string  // 战法名
	Code     string  // 股票代码
	Date     string  // 入场日期
	HoldDays int     // 持仓天数
	Entry    float64 // 入场价
	Exit     float64 // 平仓价
	PnlPct   float64 // 盈亏百分比
	Reason   string  // 平仓理由
}

// adapter 战法适配器：从日K序列判定触发，并在入场后逐日跑 CheckExit。
// klines 为截止当日（含当日）的完整日K，prevClose 为当日之前一交易日收盘（用于当日涨跌幅）。
type adapter interface {
	// Name 返回战法名。
	Name() string
	// Trigger 判定 klines 最后一根（当日）是否触发买入信号；触发返回入场评分明细，否则返回 nil。
	// industryChg 为当日行业板块涨幅（dragon 板块共振用；无数据传 0）。
	Trigger(klines []data.KLine, prevClose float64, industryChg float64) (map[string]float64, bool)
	// Exit 用当日行情判定是否平仓；返回平仓理由与是否平仓。非 nil 且 Exit==true 时按 CurPrice 结算。
	Exit(ctx *strategy.ExitContext, dailyK []strategy.KLine) (*strategy.ExitResult, bool)
}

// ── double_bump 适配器（纯日K完整回放） ──

// doubleBumpAdapter 双凸战法适配器：直接复用 double_bump.EvaluateReal 用日K完整回放，
// 仅需注入配置（权重/倍数必须非零，否则永不触发）。
type doubleBumpAdapter struct {
	st  *double_bump.DoubleBumpStrategy
	cfg *config.DoubleBumpConfig
}

func (a *doubleBumpAdapter) Name() string { return "DoubleBump" }

func (a *doubleBumpAdapter) Trigger(klines []data.KLine, prevClose, _ float64) (map[string]float64, bool) {
	if len(klines) < 10 {
		return nil, false
	}
	last := klines[len(klines)-1]
	si := &data.StockInfo{
		Code:      "",
		Price:     last.Close,
		Open:      last.Open,
		High:      last.High,
		Low:       last.Low,
		Close:     last.Close,
		Volume:    last.Volume,
		Amount:    last.Amount,
		ChangePct: chgPct(last.Close, prevClose),
	}
	eval := a.st.EvaluateReal(si.Code, si, klines)
	if eval == nil || !eval.Pass || eval.TotalScore < 70 {
		return nil, false
	}
	// 入场评分明细：阶段最高价（移动止盈基准）
	meta := map[string]float64{
		"highest_price": last.Close,
	}
	return meta, true
}

func (a *doubleBumpAdapter) Exit(ctx *strategy.ExitContext, dailyK []strategy.KLine) (*strategy.ExitResult, bool) {
	res := double_bump.CheckExit(ctx, a.cfg)
	if res == nil {
		return nil, false
	}
	return res, true
}

// ── dragon_return 适配器（日K派生 StockData） ──

type dragonReturnAdapter struct {
	st  *dragon_return.DragonReturnStrategy
	cfg *config.DragonReturnConfig
	// 板块龙性近似：IsSectorTop2 与 SectorRPS20（回测无真实板块时放宽，可经 -sector 控制）
	forceLeader bool
}

func (a *dragonReturnAdapter) Name() string { return "DragonReturn" }

func (a *dragonReturnAdapter) Trigger(klines []data.KLine, prevClose, _ float64) (map[string]float64, bool) {
	if len(klines) < 30 {
		return nil, false
	}
	sd := buildDragonReturnStockData(klines)
	if a.forceLeader {
		sd.IsSectorTop2 = true
		sd.SectorRPS20 = 90
	}
	eval, err := a.st.Evaluate("", sd)
	if err != nil || eval == nil || !eval.Pass {
		return nil, false
	}
	// 入场评分明细：阶段最高价（移动止盈/破位基准）
	meta := map[string]float64{
		"highest_price": sd.HighestPrice,
	}
	return meta, true
}

func (a *dragonReturnAdapter) Exit(ctx *strategy.ExitContext, dailyK []strategy.KLine) (*strategy.ExitResult, bool) {
	res := dragon_return.CheckExit(ctx, a.cfg)
	if res == nil {
		return nil, false
	}
	return res, true
}

// ── n_shape 适配器（日K近似 WaveA/IntradayB，D1 用规则分） ──

type nShapeAdapter struct {
	st         *n_shape.NShapeStrategy
	cfg        *config.NShapeConfig
	d1Score    float64     // 规则 D1 分（日K近似假设的中性事件分；0=不触发）
	macdSeries []data.MACD // 预计算的日线 MACD 序列（由 backtestStock 一次性填充，避免每日重复 O(n²)）
	curIdx     int         // 当前判定日在 macdSeries 中的索引（由 backtestStock 逐日设置）
}

func (a *nShapeAdapter) Name() string { return "NShape" }

// Trigger 用日K近似构造 n_shape 的评分输入：WaveA=前一交易日，IntradayB=当日近似，
// Ctx 只注入规则 D1 分（无 LLM/板块/事件数据）。仅 full_chain（D1>0 且总分≥60）触发。
func (a *nShapeAdapter) Trigger(klines []data.KLine, prevClose, _ float64) (map[string]float64, bool) {
	if a.d1Score <= 0 || len(klines) < 62 {
		return nil, false
	}
	last := klines[len(klines)-1]
	prev := klines[len(klines)-2]
	avgVol := avgVolK(klines, len(klines)-1, 20)
	// 日线 MACD 近似分钟 MACD（D4 资金确认：DIF>DEA 且 DIF>0）
	var macd data.MACD
	if a.macdSeries != nil {
		macd = a.macdSeries[a.curIdx]
	} else {
		macd = data.CalcMACD(klines)
	}

	wa := &n_shape.WaveA{
		ADate: prev.Date.Format("2006-01-02"),
		AOpen: prev.Open, AHigh: prev.High, ALow: prev.Low, AClose: prev.Close,
		AVol:    prev.Volume,
		AChgPct: chgPct(prev.Close, klines[len(klines)-3].Close),
	}
	ib := &n_shape.IntradayB{
		TTime:         1500,
		CurPrice:      last.Close,
		CumVol:        last.Volume,
		AuctionVol:    last.Volume,
		AuctionHigh:   last.High,
		AuctionLow:    last.Low,
		AuctionChgPct: chgPct(last.Open, prev.Close),
		AuctionTrend:  "平开",
		PrevClose:     prev.Close,
		PrevHigh:      prev.High,
		PrevLow:       prev.Low,
		AvgDailyVol:   avgVol,
		MinuteMACDDIF: macd.DIF,
		MinuteMACDDEA: macd.DEA,
		MinuteMACDBar: macd.Bar,
	}
	ctx := &n_shape.Ctx{LLMD1Score: a.d1Score}
	eval, err := a.st.EvaluateWave(wa, ib, ctx)
	if err != nil || eval == nil || !eval.Pass || eval.Level != "full_chain" {
		return nil, false
	}
	meta := map[string]float64{
		"limit_price":   last.Close,
		"highest_price": last.High,
	}
	return meta, true
}

// Exit n_shape 为日内超短策略，日K回测无尾盘（14:57）信号，无法用真实 CheckExit 的尾盘门控。
// 近似：入场次日收盘即平仓（视为"日内了结、不留隔夜"）。
func (a *nShapeAdapter) Exit(ctx *strategy.ExitContext, dailyK []strategy.KLine) (*strategy.ExitResult, bool) {
	res := n_shape.CheckExit(ctx, a.cfg)
	if res != nil {
		return res, true
	}
	// 无硬止损/量能衰竭时，次日起一律按收盘强平（超短不留隔夜）
	if len(dailyK) >= 2 {
		return &strategy.ExitResult{Reason: "N形收盘强平", Priority: strategy.P2}, true
	}
	return nil, false
}

// ── dragon 适配器（板块用行业涨幅近似） ──

type dragonAdapter struct {
	st  *dragon.DragonStrategy
	cfg *config.DragonConfig
}

func (a *dragonAdapter) Name() string { return "Dragon" }

func (a *dragonAdapter) Trigger(klines []data.KLine, prevClose, industryChg float64) (map[string]float64, bool) {
	if len(klines) < 5 {
		return nil, false
	}
	last := klines[len(klines)-1]
	si := &data.StockInfo{
		Code:      "",
		Price:     last.Close,
		Open:      last.Open,
		High:      last.High,
		Low:       last.Low,
		Close:     last.Close,
		Volume:    last.Volume,
		Amount:    last.Amount,
		ChangePct: chgPct(last.Close, prevClose),
	}
	// 板块共振：用行业板块当日涨幅近似（无数据时传 0，F2/F3 降级）
	sectors := []data.SectorInfo{}
	if industryChg > 0 {
		sectors = append(sectors, data.SectorInfo{Name: "industry", ChangePct: industryChg})
	}
	eval := a.st.EvaluateReal("", si, klines, sectors)
	if eval == nil || !eval.Pass || eval.TotalScore < 70 {
		return nil, false
	}
	// 入场评分明细：封板价（炸板回落基准）
	meta := map[string]float64{
		"limit_price":   last.Close,
		"highest_price": last.Close,
	}
	return meta, true
}

func (a *dragonAdapter) Exit(ctx *strategy.ExitContext, dailyK []strategy.KLine) (*strategy.ExitResult, bool) {
	res := dragon.CheckExit(ctx, a.cfg)
	if res == nil {
		return nil, false
	}
	return res, true
}

// ── 工具函数 ──

// chgPct 计算相对前收盘的涨跌幅（%）；prev<=0 返回 0。
func chgPct(close, prev float64) float64 {
	if prev <= 0 {
		return 0
	}
	return (close - prev) / prev * 100
}

// buildDragonReturnStockData 从日K派生龙回头 StockData（复现实盘 adapter 逻辑）。
func buildDragonReturnStockData(klines []data.KLine) *dragon_return.StockData {
	sd := &dragon_return.StockData{
		CurrentPrice: klines[len(klines)-1].Close,
	}
	n := len(klines)
	if n < 30 {
		return sd
	}
	sd.MA5 = maClose(klines, n, 5)
	sd.MA10 = maClose(klines, n, 10)
	sd.MA20 = maClose(klines, n, 20)
	start := n - 40
	if start < 0 {
		start = 0
	}
	hiIdx := start
	for i := start; i < n; i++ {
		if klines[i].Close > klines[hiIdx].Close {
			hiIdx = i
		}
	}
	sd.HighestPrice = klines[hiIdx].Close
	if sd.HighestPrice > 0 {
		sd.FirstRisePct = (sd.HighestPrice - klines[start].Close) / klines[start].Close
		sd.PullbackPct = (sd.HighestPrice - sd.CurrentPrice) / sd.HighestPrice
	}
	sd.PullbackDays = n - 1 - hiIdx
	if sd.PullbackDays < 0 {
		sd.PullbackDays = 0
	}
	vol20 := avgVolK(klines, n, 20)
	if vol20 > 0 {
		sd.VolumeRatio = avgVolK(klines, n, 5) / vol20
	}
	return sd
}

// maClose 计算最近 lookback 根收盘均线。
func maClose(klines []data.KLine, n, lookback int) float64 {
	if n < lookback {
		return 0
	}
	var s float64
	for i := n - lookback; i < n; i++ {
		s += klines[i].Close
	}
	return s / float64(lookback)
}

// avgVolK 计算最近 lookback 根成交量均值。
// avgVolK 计算最近 lookback 根（截止第 n 根）成交量均值；总根数不足 lookback 时返回 0。
func avgVolK(klines []data.KLine, n, lookback int) float64 {
	if n < lookback {
		return 0
	}
	var s float64
	for i := n - lookback; i < n; i++ {
		s += klines[i].Volume
	}
	return s / float64(lookback)
}

// toStrategyKLine 把 data.KLine 序列转成退出评估用的简化 strategy.KLine（去 Date/Amount）。
func toStrategyKLine(klines []data.KLine) []strategy.KLine {
	out := make([]strategy.KLine, 0, len(klines))
	for _, k := range klines {
		out = append(out, strategy.KLine{
			Open: k.Open, High: k.High, Low: k.Low, Close: k.Close, Volume: k.Volume,
		})
	}
	return out
}

// ── 回测运行 ──

// options 回测命令行参数（数据库、日期区间、战法选择与近似开关）。
type options struct {
	dbPath    string
	start     string
	end       string
	strategy  string
	maxStocks int
	d1Score   float64
	industry  bool
}

func parseFlags() *options {
	o := &options{}
	flag.StringVar(&o.dbPath, "db", defaultDB(), "离线研究库 SQLite 路径")
	flag.StringVar(&o.start, "start", "20230101", "回测起始日 YYYYMMDD")
	flag.StringVar(&o.end, "end", "20260101", "回测结束日 YYYYMMDD")
	flag.StringVar(&o.strategy, "strategy", "double_bump", "战法: double_bump|dragon|dragon_return|n_shape")
	flag.IntVar(&o.maxStocks, "maxstocks", 500, "最多回测股票数（0=全部）")
	flag.Float64Var(&o.d1Score, "d1", 20, "n_shape 的规则 D1 分（日K近似假设的中性事件分；0=不触发 n_shape）")
	flag.BoolVar(&o.industry, "industry", false, "dragon 是否用行业板块涨幅近似板块共振")
	flag.Parse()
	return o
}

func defaultDB() string {
	if d := os.Getenv("QUANT_DATA_DIR"); d != "" {
		return filepath.Join(d, "trading.db")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".quant-trading-v2", "trading.db")
}

// newAdapter 根据参数构建对应战法适配器。
// 关键：必须给策略注入 config.Manager（否则 strategyCfg() 返回全零配置，
// 所有权重/倍数为 0，导致 total 恒 0、永不触发）。这里用 NewManager("") 取得出厂默认配置。
func newAdapter(name string, industry bool, d1 float64) (adapter, error) {
	cfgMgr := config.NewManager("") // 空路径=仅内存默认配置，不写配置文件
	sc := cfgMgr.Get().Strategy
	switch strings.ToLower(name) {
	case "double_bump":
		return &doubleBumpAdapter{st: double_bump.New(cfgMgr), cfg: &sc.DoubleBump}, nil
	case "dragon":
		return &dragonAdapter{st: dragon.New(cfgMgr), cfg: &sc.Dragon}, nil
	case "dragon_return":
		return &dragonReturnAdapter{st: dragon_return.New(cfgMgr), cfg: &sc.DragonReturn, forceLeader: industry}, nil
	case "n_shape":
		return &nShapeAdapter{st: n_shape.New(cfgMgr, nil), cfg: &sc.NShape, d1Score: d1}, nil
	default:
		return nil, fmt.Errorf("未知战法: %s", name)
	}
}

// run 执行回测主流程。
func (o *options) run() error {
	db, err := store.Open(o.dbPath)
	if err != nil {
		return fmt.Errorf("打开数据库: %w", err)
	}
	defer db.Close()

	codes, err := db.StockCodes()
	if err != nil {
		return err
	}
	if o.maxStocks > 0 && len(codes) > o.maxStocks {
		codes = codes[:o.maxStocks]
	}

	ad, err := newAdapter(o.strategy, o.industry, o.d1Score)
	if err != nil {
		return err
	}

	// 行业板块数据（仅 dragon 需要）：股票→行业映射，以及每个行业按日期的涨幅
	indMap := map[string]string{}
	industryChg := map[string]map[string]float64{} // code -> date -> ChangePct
	if o.industry {
		if m, err := db.Industries(); err == nil {
			indMap = m
		}
		for _, tsCode := range codes {
			ind, ok := indMap[tsCode]
			if !ok {
				continue
			}
			sectorDays, err := db.SectorHistory(ind, o.start, o.end)
			if err != nil {
				continue
			}
			byDate := make(map[string]float64, len(sectorDays))
			for _, sd := range sectorDays {
				byDate[sd.TradeDate] = sd.ChangePct
			}
			code := strings.Split(tsCode, ".")[0]
			industryChg[code] = byDate
		}
	}

	var trades []trade
	for _, tsCode := range codes {
		code := strings.Split(tsCode, ".")[0] // 000001.SZ -> 000001
		bars, err := db.RawBars(tsCode, o.start, o.end)
		if err != nil {
			continue
		}
		if len(bars) < 15 {
			continue
		}
		klines := toDataKLine(bars)
		trades = append(trades, o.backtestStock(code, klines, ad, industryChg[code])...)
	}

	report := summarize(trades)
	printReport(report, ad.Name(), len(codes))
	return nil
}

// toDataKLine 把 store.Bar 序列转成 data.KLine（Date 解析为 time.Time）。
func toDataKLine(bars []store.Bar) []data.KLine {
	out := make([]data.KLine, 0, len(bars))
	for _, b := range bars {
		t, _ := time.Parse("20060102", b.Date)
		out = append(out, data.KLine{
			Date: t, Open: b.Open, High: b.High, Low: b.Low, Close: b.Close,
			Volume: b.Vol, Amount: b.Amount,
		})
	}
	return out
}

// backtestStock 对单只股票回放指定战法：逐日判定触发，触发后次日开盘入场并逐日模拟平仓。
func (o *options) backtestStock(code string, klines []data.KLine, ad adapter, industryChgByDate map[string]float64) []trade {
	var trades []trade
	// n_shape：预计算整条日线 MACD 序列（一次性，避免逐日重复 O(n²)）
	if na, ok := ad.(*nShapeAdapter); ok && na.macdSeries == nil {
		na.macdSeries = data.CalcMACDSeries(klines)
	}
	// 从第 30 根起才有足够前视窗（MA/主升段）
	for i := 29; i < len(klines)-1; i++ {
		if na, ok := ad.(*nShapeAdapter); ok {
			na.curIdx = i
		}
		// 当日触发判定：用截止当日的 K 线 + 当日相对前收的涨幅
		prevClose := 0.0
		if i > 0 {
			prevClose = klines[i-1].Close
		}
		// 行业板块涨幅近似（dragon 需要；无数据时为 0，F2/F3 降级）
		industryChg := 0.0
		if industryChgByDate != nil {
			industryChg = industryChgByDate[klines[i].Date.Format("20060102")]
		}
		meta, ok := ad.Trigger(klines[:i+1], prevClose, industryChg)
		if !ok {
			continue
		}
		// 次日开盘入场
		entry := klines[i+1].Open
		if entry <= 0 {
			continue
		}
		// 逐日平仓模拟：从入场次日（i+2）起跑 CheckExit
		t := o.simulateExit(code, klines, i+1, entry, meta, ad)
		if t != nil {
			trades = append(trades, *t)
			// 入场后跳到该笔交易结束（平仓日）之后，避免同一标的在同一时段重复入场
			i += t.HoldDays + 1
		}
	}
	return trades
}

// simulateExit 从入场日 index 起逐日跑 CheckExit，返回平仓结果；到序列末尾仍未平仓则按末日收盘强制结算。
func (o *options) simulateExit(code string, klines []data.KLine, entryIdx int, entry float64, meta map[string]float64, ad adapter) *trade {
	for j := entryIdx + 1; j < len(klines); j++ {
		cur := klines[j].Close
		if cur <= 0 {
			continue
		}
		// 用回测当天日期作为 Now（避免 time.Since 用真实时间导致历史入场立即判"调整超期"）
		now := klines[j].Date
		ctx := &strategy.ExitContext{
			Code:      code,
			CostPrice: entry,
			CurPrice:  cur,
			EntryAt:   klines[entryIdx].Date.Format("2006-01-02"),
			EntryMeta: meta,
			DailyK:    toStrategyKLine(klines[:j+1]),
			Now:       now,
		}
		res, exit := ad.Exit(ctx, ctx.DailyK)
		if exit && res != nil {
			return &trade{
				Strategy: ad.Name(), Code: code, Date: klines[entryIdx].Date.Format("20060102"),
				HoldDays: j - entryIdx, Entry: entry, Exit: cur,
				PnlPct: (cur - entry) / entry * 100, Reason: res.Reason,
			}
		}
	}
	// 未平仓：按末日收盘强制结算
	last := klines[len(klines)-1].Close
	if last <= 0 {
		return nil
	}
	return &trade{
		Strategy: ad.Name(), Code: code, Date: klines[entryIdx].Date.Format("20060102"),
		HoldDays: len(klines) - 1 - entryIdx, Entry: entry, Exit: last,
		PnlPct: (last - entry) / entry * 100, Reason: "区间结束强制结算",
	}
}

// ── 汇总与输出 ──

// summary 按战法分组的回测统计结果。
type summary struct {
	Name         string
	Count        int
	Win          int
	Loss         int
	WinRate      float64
	AvgWinPct    float64
	AvgLossPct   float64
	ProfitFactor float64
	AvgHold      float64
}

// summarize 汇总所有交易的胜率/盈亏指标。
func summarize(trades []trade) *summary {
	s := &summary{}
	if len(trades) == 0 {
		return s
	}
	s.Name = trades[0].Strategy
	s.Count = len(trades)
	var winSum, lossSum float64
	var holdSum int
	for _, t := range trades {
		holdSum += t.HoldDays
		if t.PnlPct > 0 {
			s.Win++
			winSum += t.PnlPct
		} else {
			s.Loss++
			lossSum += t.PnlPct
		}
	}
	if s.Win+s.Loss > 0 {
		s.WinRate = float64(s.Win) / float64(s.Win+s.Loss) * 100
	}
	if s.Win > 0 {
		s.AvgWinPct = winSum / float64(s.Win)
	}
	if s.Loss > 0 {
		s.AvgLossPct = lossSum / float64(s.Loss)
	}
	if lossSum != 0 {
		s.ProfitFactor = winSum / -lossSum
	}
	s.AvgHold = float64(holdSum) / float64(s.Count)
	return s
}

// printReport 打印回测汇总报告。
func printReport(s *summary, name string, stockCount int) {
	fmt.Println("==============================================")
	fmt.Printf("战法历史回测: %s（%d 只股票）\n", name, stockCount)
	fmt.Println("----------------------------------------------")
	if s.Count == 0 {
		fmt.Println("无触发信号。")
		return
	}
	fmt.Printf("触发信号数: %d\n", s.Count)
	fmt.Printf("胜率: %.2f%% (%d 胜 / %d 负)\n", s.WinRate, s.Win, s.Loss)
	fmt.Printf("平均盈利: +%.2f%%\n", s.AvgWinPct)
	fmt.Printf("平均亏损: %.2f%%\n", s.AvgLossPct)
	fmt.Printf("盈亏比: %.2f\n", s.ProfitFactor)
	fmt.Printf("平均持仓天数: %.1f\n", s.AvgHold)
	fmt.Println("==============================================")
}

// config 包的默认战法参数（供适配器构造，见 defaultConfig）。

// main 回测入口：解析参数并运行，失败时打印日志退出。
func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	o := parseFlags()
	if err := o.run(); err != nil {
		log.Fatalf("回测失败: %v", err)
	}
}
