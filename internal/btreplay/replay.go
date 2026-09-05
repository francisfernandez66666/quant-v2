// Package btreplay 四大手写战法 + 战法库规则的历史回放回测（子系统统一改造二期：
// 自 cmd/backtest_strategy 并入 research 二进制，消除子系统内的第二套回测进程代码）。
// 对外入口：research [--db …] backtest-strategy …（run-task 的 backtest_strategy 类型进程内调用）。
// English: package btreplay — historical replay backtests for the four hand-written strategies plus
// applied factor/pattern library rules. Merged from the standalone bt_strategy binary into the
// research binary (phase 2), leaving one research subsystem with a single entry.
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
package btreplay

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/research"
	"quant-trading-v2/internal/store"
	"quant-trading-v2/internal/strategies/double_bump"
	"quant-trading-v2/internal/strategies/dragon"
	"quant-trading-v2/internal/strategies/dragon_return"
	"quant-trading-v2/internal/strategies/factor"
	"quant-trading-v2/internal/strategies/n_shape"
	"quant-trading-v2/internal/strategies/pattern"
	"quant-trading-v2/internal/strategy"
	"quant-trading-v2/internal/strategy_engine"
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

// Name 战法名（回测报告分组键）。
func (a *doubleBumpAdapter) Name() string { return "双响炮" }

// Trigger 纯日K完整回放：复用实盘 EvaluateReal 判定（≥10 根 K 线且总分≥70 触发），
// 入场评分明细带阶段最高价（移动止盈基准）。
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
	// 入场评分明细：阶段最高价（移动止盈基准）+ 总分（扫参门槛过滤）
	meta := map[string]float64{
		"highest_price": last.Close,
		"score":         eval.TotalScore,
	}
	return meta, true
}

// Exit 直接委托 double_bump.CheckExit（注入配置的移动止盈/破位/超期规则）。
func (a *doubleBumpAdapter) Exit(ctx *strategy.ExitContext, dailyK []strategy.KLine) (*strategy.ExitResult, bool) {
	res := double_bump.CheckExit(ctx, a.cfg)
	if res == nil {
		return nil, false
	}
	return res, true
}

// ── dragon_return 适配器（日K派生 StockData） ──

// dragonReturnAdapter 龙回头适配器：日K派生 StockData（MA/阶段高点/回撤等），
// 板块龙性（IsSectorTop2/SectorRPS20）无真实板块数据时按 -industry 开关放宽近似。
type dragonReturnAdapter struct {
	st  *dragon_return.DragonReturnStrategy
	cfg *config.DragonReturnConfig
	// 板块龙性近似：IsSectorTop2 与 SectorRPS20（回测无真实板块时放宽，可经 -sector 控制）
	forceLeader bool
}

// Name 战法名（回测报告分组键）。
func (a *dragonReturnAdapter) Name() string { return "龙回头" }

// Trigger 日K派生 StockData 后走实盘 Evaluate（≥30 根 K 线）；入场明细带阶段最高价。
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
	// 入场评分明细：阶段最高价（移动止盈/破位基准）+ 总分（扫参门槛过滤）
	meta := map[string]float64{
		"highest_price": sd.HighestPrice,
		"score":         eval.TotalScore,
	}
	return meta, true
}

// Exit 直接委托 dragon_return.CheckExit（回撤/破位/超期规则与实盘同源）。
func (a *dragonReturnAdapter) Exit(ctx *strategy.ExitContext, dailyK []strategy.KLine) (*strategy.ExitResult, bool) {
	res := dragon_return.CheckExit(ctx, a.cfg)
	if res == nil {
		return nil, false
	}
	return res, true
}

// ── n_shape 适配器（日K近似 WaveA/IntradayB，D1 用规则分） ──

// nShapeAdapter N 形适配器：高度依赖日内快照与 LLM D1，日K近似后准确性打折——
// WaveA=前一交易日、IntradayB=当日近似，D1 用可配置规则分；MACD 序列预计算避免逐日 O(n²)。
type nShapeAdapter struct {
	st         *n_shape.NShapeStrategy
	cfg        *config.NShapeConfig
	d1Score    float64     // 规则 D1 分（日K近似假设的中性事件分；0=不触发）
	macdSeries []data.MACD // 预计算的日线 MACD 序列（由 backtestStock 一次性填充，避免每日重复 O(n²)）
	curIdx     int         // 当前判定日在 macdSeries 中的索引（由 backtestStock 逐日设置）
}

// Name 战法名（回测报告分组键）。
func (a *nShapeAdapter) Name() string { return "N形" }

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
	// 走完整 n_shape 波评估链，仅 full_chain 通过才视为有效触发。
	ctx := &n_shape.Ctx{LLMD1Score: a.d1Score}
	eval, err := a.st.EvaluateWave(wa, ib, ctx)
	if err != nil || eval == nil || !eval.Pass || eval.Level != "full_chain" {
		return nil, false
	}
	meta := map[string]float64{
		"limit_price":   last.Close,
		"highest_price": last.High,
		"score":         eval.TotalScore,
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

// dragonAdapter 龙头战法适配器：板块共振（F2/F3）用所属行业当日涨幅近似，
// 无行业数据传 0 时自动降级忽略板块维度。
type dragonAdapter struct {
	st  *dragon.DragonStrategy
	cfg *config.DragonConfig
}

// Name 战法名（回测报告分组键）。
func (a *dragonAdapter) Name() string { return "龙头" }

// Trigger 复用实盘 EvaluateReal（≥5 根 K 线且总分≥70 触发）；行业涨幅近似板块共振，
// 入场明细带封板价（炸板回落基准）与阶段最高价。
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
	// 入场评分明细：封板价（炸板回落基准）+ 总分（扫参模式门槛过滤用）
	meta := map[string]float64{
		"limit_price":   last.Close,
		"highest_price": last.Close,
		"score":         eval.TotalScore,
	}
	return meta, true
}

// Exit 直接委托 dragon.CheckExit（封板/炸板/移动止盈规则与实盘同源）。
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

// Options 回放回测参数（数据库、日期区间、战法选择与近似开关）。
// English: Options configures a replay backtest.
type Options struct {
	DBPath    string  // 回放用数据库路径（daily K 等原始数据）
	Start     string  // 回测开始日期（YYYY-MM-DD）
	End       string  // 回测结束日期（YYYY-MM-DD）
	Strategy  string  // double_bump|dragon|dragon_return|n_shape|factor|pattern|all（战法选择）
	MaxStocks int     // 最多回测股票数（0=全部）
	D1Score   float64 // 外部注入的固定 D1 分（≥0 时使用）
	Industry  bool    // 是否启用行业过滤/分组
	DataDir   string  // 战法库目录（applied_factors.json / applied_patterns.json 所在）
	// Sweep 非 nil 时进入参数扫参模式（§P2）：全库战法 × 出场/门槛参数网格自动寻优，
	// 触发一次性预计算 + 逐组合廉价统一出场模拟，产出排名表与 SWEEP_JSON。
	// English: when set, run the parameter sweep optimizer (see sweep.go).
	Sweep *SweepConfig
	// CandidateID > 0 且 Strategy=pattern 时：直接从候选行构造单条形态规则回放，
	// 不依赖 applied_patterns.json（待审批候选也有回测通道，§8.6-B）。
	// English: when set with Strategy=pattern, build one rule from the candidate row itself —
	// proposed candidates get a replay path without requiring library approval.
	CandidateID int64
	// Screen 非空时以质控筛选股票池替代全部 StockCodes()（再叠加 MaxStocks 截断）：
	// 全量回测默认剔除 ST/退市/多年亏损/地量股，而非 maxstocks=300 的字母序傻截。
	// English: when set, run on the quality-screened universe instead of all StockCodes(),
	// so full-market replays drop ST/delisted/multi-year-loss/illiquid names.
	Screen *store.StockScreen
	// ThrottleMs 逐股节流（毫秒/只）：>0 时每处理完一只股票 sleep 该时长，摊平全量回放
	// 对服务器的瞬时 CPU/内存挤压（2 核 4G 机器全池回放会触发内存熔断抢占）。0=不节流。
	// English: per-stock throttle in ms — sleeps between stocks to flatten instantaneous CPU/mem
	// pressure during full-universe replay on small boxes. 0 = no throttling.
	ThrottleMs int
}

// DefaultDB 研究库默认路径：QUANT_DATA_DIR 优先，否则 ~/.quant-trading-v2/trading.db
// （与 research/scheduler 的 defaultDB 同一约定）。
func DefaultDB() string {
	if d := os.Getenv("QUANT_DATA_DIR"); d != "" {
		return filepath.Join(d, "trading.db")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".quant-trading-v2", "trading.db")
}

// defaultDataDir 战法库默认目录（applied_factors.json/applied_patterns.json 所在，与数据目录一致）。
func DefaultDataDir() string {
	if d := os.Getenv("QUANT_DATA_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".quant-trading-v2")
}

// strategyNeedsIndustry 判断战法是否依赖行业板块数据：dragon 的 F2 共振/溢价对标、
// dragon_return 的"板块前2+RPS"前提都吃板块输入。无板块数据时两者理论上限≈62~65，
// 恒低于 70/Pass 触发线——回放必须自动装配行业涨幅，否则这两法永远零触发。
// English: dragon/dragon_return need sector context; without it their max score stays under
// the trigger line, so replay auto-enables industry assembly whenever they are selected.
func strategyNeedsIndustry(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "dragon", "dragon_return":
		return true
	}
	return false
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

// ── 已应用战法回测适配器（阶段3.4：applied_factors.json / applied_patterns.json）──

// ruleEvalAdapter 因子/形态规则回测适配器：直接复用实盘 FactorStrategy / PatternStrategy 的
// Evaluate 逻辑（seriesFromKLines 由日K计算因子 → 时间序列分位×权重×方向 / [min,max) 条件解释），
// 与 8a/8b 实盘同一套打分口径；退出用通用移动止盈+超期（与实盘未知战法回退 genericTrailingExit 同口径）。
// 每条启用规则一个 adapter 实例，结果按规则名分组统计。
// English: factor/pattern rule backtest adapters — reuses the live FactorStrategy/PatternStrategy
// Evaluate (factors computed from daily bars; percentile×weight×direction scoring or [min,max) condition
// interpretation), identical to the 8a/8b live path. Exits use the generic trailing-stop + timeout,
// matching the live fallback for unknown strategies. One adapter per enabled rule; results group by name.
type ruleEvalAdapter struct {
	name   string // 规则显示名（如 "因子战法#1"）
	ruleID string // 规则唯一 ID（fac_<n>/pat_<n>；扫参审批回写参数覆盖的定位键）
	fs     *factor.FactorStrategy
	ps     *pattern.PatternStrategy
	// §P2-d 规则级出场覆盖（扫参审批写入 applied_*.json；nil=用全局默认 8%/15 天）。
	trailOverride *float64
	holdOverride  *int
}

// Name 返回规则显示名（如"因子战法#1"/"形态战法#2"），作为回测报告的分组键。
func (a *ruleEvalAdapter) Name() string { return a.name }

// kindProvider 可选接口：返回规则 ID（内置战法无此实现）。
type kindProvider interface{ Kind() string }

// Kind 返回规则唯一 ID（fac_<n>/pat_<n>），扫参排名落库与审批定位用。
func (a *ruleEvalAdapter) Kind() string { return a.ruleID }

// Trigger 用截止当日（含）的日K构造 StockMarketData，走实盘 Evaluate 判定是否触发买入。
// English: builds StockMarketData from bars up to the day and runs the live Evaluate as the trigger.
func (a *ruleEvalAdapter) Trigger(klines []data.KLine, prevClose, _ float64) (map[string]float64, bool) {
	if len(klines) < 30 {
		return nil, false // 与实盘同门槛：K线不足 30 根不打分
	}
	last := klines[len(klines)-1]
	md := &strategy_engine.StockMarketData{KLines: klines}
	var eval *strategy.Evaluation
	var err error
	if a.fs != nil {
		eval, err = a.fs.Evaluate("", md)
	} else {
		eval, err = a.ps.Evaluate("", md)
	}
	if err != nil || eval == nil || !eval.Pass {
		return nil, false
	}
	// 入场评分明细：阶段最高价基准（Exit 中逐日抬高）。
	// score：因子规则带复合总分供扫参门槛过滤；形态规则是区间命中、无连续分 → -1 标记跳过该维。
	sc := -1.0
	if a.fs != nil {
		sc = eval.TotalScore
	}
	return map[string]float64{"highest_price": last.Close, "score": sc}, true
}

// Exit 通用移动止盈 + 超期离场（与 combat_agent.genericTrailingExit 同口径）：
// 阶段高点逐日抬高（EntryMeta 复用同一 map），从高点回撤 ≥8% 且曾盈利 → 减仓级平仓；
// 持仓超 15 日未完成形态 → 超期平仓。
// English: generic trailing stop + timeout (same semantics as the live fallback): raises the stage high
// daily via the shared EntryMeta map, exits on an ≥8% drawdown from a profitable high or a 15-day timeout.
func (a *ruleEvalAdapter) Exit(ctx *strategy.ExitContext, dailyK []strategy.KLine) (*strategy.ExitResult, bool) {
	cost, price := ctx.CostPrice, ctx.CurPrice
	if cost <= 0 || price <= 0 {
		return nil, false
	}
	// §P2-d 规则级出场参数优先（扫参审批），缺省回退全局 8%/15 天。
	// §GAP2.2 修复：缺省 trailLimit 必须是负号语义（回撤达 -8% 才触发），与实盘
	// genericTrailingExitWith（combat_agent/position_exits.go: trail <= -trailPct）同口径。
	// 旧实现缺省 +8.0：stageHigh 先抬含现价 → trail 恒 ≤0 → 任何曾盈利持仓当日即被"移动止盈"平仓，
	// 未获扫参覆盖的库规则夜间回放胜率/持仓天数严重失真。
	trailLimit := -8.0
	if a.trailOverride != nil && *a.trailOverride > 0 {
		trailLimit = -*a.trailOverride
	}
	holdLimit := 15
	if a.holdOverride != nil && *a.holdOverride > 0 {
		holdLimit = *a.holdOverride
	}
	stageHigh := cost
	if h, ok := ctx.EntryMeta["highest_price"]; ok && h > stageHigh {
		stageHigh = h
	}
	if price > stageHigh {
		stageHigh = price
		ctx.EntryMeta["highest_price"] = stageHigh // 抬高并随 ctx 持续到后续交易日
	}
	// 移动止盈：阶段高点回撤达阈值（且曾盈利）→ 平仓保护利润
	if trailPct := (price - stageHigh) / stageHigh * 100; trailPct <= trailLimit && stageHigh > cost {
		return &strategy.ExitResult{Reason: "回撤止损(移动止盈)", Priority: strategy.P2}, true
	}
	// 超期：持仓超上限强制离场
	if ctx.EntryAt != "" {
		if entryDate, err := time.Parse("2006-01-02", ctx.EntryAt); err == nil {
			if days := int(ctx.Now.Sub(entryDate).Hours() / 24); days >= holdLimit {
				return &strategy.ExitResult{Reason: "持仓超期离场", Priority: strategy.P3}, true
			}
		}
	}
	return nil, false
}

// loadRuleAdapters 从战法库加载全部启用规则，每条规则一个 adapter（kind: factor|pattern）。
// English: loads every enabled library rule as one adapter (kind: factor|pattern).
func loadRuleAdapters(kind, dataDir string) ([]adapter, error) {
	switch strings.ToLower(kind) {
	case "factor", "factor_rules", "applied_factors":
		// §P2-d：直接读库条目以携带规则级出场覆盖（扫参审批后回测立即生效）；
		// English: read library entries directly so sweep-approved exit overrides apply to replays.
		entries, err := research.ListAppliedFactorRules(dataDir)
		if err != nil {
			return nil, err
		}
		out := make([]adapter, 0, len(entries))
		for i := range entries {
			e := &entries[i]
			if !e.Enabled || len(e.Factors) == 0 {
				continue
			}
			r := &factor.ActiveRule{
				ID: e.ID, Name: e.Name, CandID: e.CandID,
				Rule: factor.Rule{
					Factors: e.Factors, Weights: e.Weights, Directions: e.Directions,
					BuyThreshold: e.BuyThreshold,
				},
			}
			ad := &ruleEvalAdapter{name: r.Name, ruleID: r.ID,
				fs: func() *factor.FactorStrategy { f := factor.New(); f.SetRules([]*factor.ActiveRule{r}); return f }()}
			if e.ExitTrailPct > 0 {
				ad.trailOverride = &e.ExitTrailPct
			}
			if e.ExitMaxHoldDays > 0 {
				h := e.ExitMaxHoldDays
				ad.holdOverride = &h
			}
			out = append(out, ad)
		}
		return out, nil
	case "pattern", "pattern_rules", "applied_patterns":
		// §P2-d：同因子分支，直读条目以携带扫参审批的出场覆盖。
		pentries, perr := research.ListAppliedPatternRules(dataDir)
		if perr != nil {
			return nil, perr
		}
		out := make([]adapter, 0, len(pentries))
		for i := range pentries {
			e := &pentries[i]
			if !e.Enabled || len(e.Conds) == 0 {
				continue
			}
			ap := &pattern.ActivePattern{ID: e.ID, Name: e.Name, CandID: e.CandID}
			for _, cd := range e.Conds {
				ap.Conds = append(ap.Conds, pattern.Cond{Factor: cd.Factor, Min: cd.Min, Max: cd.Max})
			}
			ad := &ruleEvalAdapter{name: ap.Name, ruleID: ap.ID,
				ps: func() *pattern.PatternStrategy {
					p := pattern.New()
					p.SetRules([]*pattern.ActivePattern{ap})
					return p
				}()}
			if e.ExitTrailPct > 0 {
				ad.trailOverride = &e.ExitTrailPct
			}
			if e.ExitMaxHoldDays > 0 {
				h := e.ExitMaxHoldDays
				ad.holdOverride = &h
			}
			out = append(out, ad)
		}
		return out, nil
	}
	return nil, fmt.Errorf("未知战法库类型: %s", kind)
}

// buildAdapters 按 Options 构建回放适配器集合：形态候选直读 / all 全库（库规则+四大内置）/
// factor|pattern 单库类型 / 单战法。返回适配器列表与行业板块装配开关
// （显式 Industry 或任一入选战法依赖板块，见 strategyNeedsIndustry）。
// English: builds the adapter set per mode and reports whether sector data must be assembled.
func (o *Options) buildAdapters(db *store.DB) ([]adapter, bool, error) {
	var ads []adapter
	// 行业板块装配开关：显式 Industry 或任一入选战法依赖板块（见 strategyNeedsIndustry）。
	useIndustry := o.Industry
	needsInd := func(names ...string) {
		for _, n := range names {
			if strategyNeedsIndustry(n) {
				useIndustry = true
			}
		}
	}
	if o.CandidateID > 0 && strings.EqualFold(o.Strategy, "pattern") {
		// 候选直读模式（§8.6-B）：候选 Factors JSON 即 []PatternCond（与 ApplyPatternRule 同映射），
		// 构造单条规则走与实盘一致的 Evaluate 回放；战法库为空/未审批均不影响。
		// English: candidate-direct mode — build a single rule from the candidate row and replay it.
		c, cerr := db.CandidateByID(o.CandidateID)
		if cerr != nil {
			return nil, false, fmt.Errorf("读取候选 #%d 失败: %w", o.CandidateID, cerr)
		}
		var conds []research.PatternCond
		if jerr := json.Unmarshal([]byte(c.Factors), &conds); jerr != nil {
			return nil, false, fmt.Errorf("解析候选条件失败: %w", jerr)
		}
		if len(conds) == 0 {
			log.Printf("候选 #%d 无条件集，跳过回放", c.ID)
			return nil, false, nil
		}
		rule := &pattern.ActivePattern{
			ID:     "pat_" + strconv.FormatInt(c.ID, 10),
			Name:   "形态战法#" + strconv.FormatInt(c.ID, 10),
			CandID: c.ID,
		}
		for _, cd := range conds {
			rule.Conds = append(rule.Conds, pattern.Cond{Factor: cd.Factor, Min: cd.Min, Max: cd.Max})
		}
		ps := pattern.New()
		ps.SetRules([]*pattern.ActivePattern{rule})
		ads = []adapter{&ruleEvalAdapter{name: rule.Name, ruleID: rule.ID, ps: ps}}
		log.Printf("候选直读回放：%s 条件=%d", rule.Name, len(rule.Conds))
	} else if strings.EqualFold(o.Strategy, "all") {
		fa, ferr := loadRuleAdapters("factor", o.DataDir)
		if ferr != nil {
			return nil, false, ferr
		}
		pa, perr := loadRuleAdapters("pattern", o.DataDir)
		if perr != nil {
			return nil, false, perr
		}
		ads = append(fa, pa...)
		// 四大手写战法一并纳入 all 回放（dragon/double_bump/dragon_return/n_shape）：
		// "几个形态战法不进回测"的另一含义——它们此前只能手动逐个跑。
		// English: include the four hand-written strategies in the all-replay as well.
		builtins := []string{"double_bump", "dragon", "dragon_return", "n_shape"}
		needsInd(builtins...)
		for _, name := range builtins {
			ad, aerr := newAdapter(name, useIndustry, o.D1Score)
			if aerr != nil {
				return nil, false, aerr
			}
			ads = append(ads, ad)
		}
		if len(ads) == 0 {
			log.Printf("战法库无启用规则（%s 下 applied_*.json 为空或全部停用）", o.DataDir)
			return nil, false, nil
		}
		log.Printf("all 回放：%d 条库规则（factor=%d pattern=%d）+ 四大手写战法",
			len(fa)+len(pa), len(fa), len(pa))
	} else if strings.EqualFold(o.Strategy, "factor") || strings.EqualFold(o.Strategy, "pattern") {
		ra, rerr := loadRuleAdapters(o.Strategy, o.DataDir)
		if rerr != nil {
			return nil, false, rerr
		}
		ads = ra
		if len(ads) == 0 {
			log.Printf("战法库无启用规则（%s 下 applied_*.json 为空或全部停用）", o.DataDir)
			return nil, false, nil
		}
		log.Printf("战法库已加载 %d 条启用规则", len(ads))
	} else {
		needsInd(o.Strategy)
		ad, aerr := newAdapter(o.Strategy, useIndustry, o.D1Score)
		if aerr != nil {
			return nil, false, aerr
		}
		ads = []adapter{ad}
	}
	return ads, useIndustry, nil
}

// Run 执行回放回测主流程（汇总报告打印到 stdout，供 worker 解析 result_text）。
func (o *Options) Run() error {
	db, err := store.Open(o.DBPath)
	if err != nil {
		return fmt.Errorf("打开数据库: %w", err)
	}
	defer db.Close()

	// §质控筛选：Screen 非空时用质控池（剔 ST/退市/多年亏损/地量股）替代全量 StockCodes()，
	// 再叠加 MaxStocks 截断——全量回测不再是 maxstocks=300 的字母序傻截。
	// English: with a quality Screen set, build the universe from ScreenedCodes (drops ST/delisted/
	// multi-year-loss/illiquid names), then apply the MaxStocks cap on top.
	var codes []string
	if o.Screen != nil {
		sc := *o.Screen
		if sc.End == "" && o.End != "" {
			sc.End = o.End // 质控窗口结束日对齐回测区间，避免用"今天"跨出回测区间
		}
		codes, err = db.ScreenedCodes(sc)
	} else {
		codes, err = db.StockCodes()
	}
	if err != nil {
		return err
	}
	if o.MaxStocks > 0 && len(codes) > o.MaxStocks {
		codes = codes[:o.MaxStocks]
	}
	log.Printf("回放股票池 %d 只（质控筛选=%v）", len(codes), o.Screen != nil)

	// 阶段3.4：factor/pattern → 从战法库加载全部启用规则（每条规则一个 adapter，分组统计）。
	// "all"（子系统统一改造新增）：因子+形态启用规则一起回放——夜间 library_replay 步骤用，
	// 让自动研究每晚对现行战法做一次实盘口径的胜率/盈亏比回归验证。
	// English: "all" replays every enabled factor AND pattern rule in one pass — used by the nightly
	// library_replay step so auto-research regression-tests live strategies nightly.
	ads, useIndustry, berr := o.buildAdapters(db)
	if berr != nil {
		return berr
	}

	// 行业板块数据（仅 dragon 需要）：股票→行业映射，以及每个行业按日期的涨幅
	// 行业板块数据（仅 dragon 需要）：股票→行业映射，以及每个行业按日期的涨幅
	indMap := map[string]string{}
	industryChg := map[string]map[string]float64{} // code -> date -> ChangePct
	if useIndustry {
		if m, err := db.Industries(); err == nil {
			indMap = m
		}
		// 逐票按其行业取区间板块涨幅，转 代码→日期→涨幅 结构供 dragon 使用。
		for _, tsCode := range codes {
			ind, ok := indMap[tsCode]
			if !ok {
				continue
			}
			sectorDays, err := db.SectorHistory(ind, o.Start, o.End)
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

	// §P2 参数扫参模式：触发一次性预计算 + 逐组合廉价模拟统一出场（见 sweep.go）。
	// English: sweep mode — pre-compute triggers once, then cheaply simulate each param combo.
	if o.Sweep != nil {
		return o.runSweep(db, codes, ads, industryChg)
	}

	// 逐 adapter 回放（多规则时按规则分组统计；单战法仅一条）。
	// 进度输出：每 10% 打一行"回测进度 x%"（§8.6-A 同协议），队列 worker 解析回写，
	// 否则整轮回放只有结尾汇总、进度条全程空窗。
	// English: emit "回测进度 x%" every 10% of the stock loop so the queue worker can feed the bar.
	summaries := make([]*summary, 0, len(ads))
	for _, ad := range ads {
		var trades []trade
		lastPct := -10
		for ci2, tsCode := range codes {
			if pct := ci2 * 100 / len(codes); pct >= lastPct+10 && len(codes) > 0 {
				lastPct = pct
				fmt.Printf("回测进度 %d%%\n", pct)
			}
			code := strings.Split(tsCode, ".")[0] // 000001.SZ -> 000001
			// §GAP4 复权价回放：HfqBars 后复权序列——除权缺口不再被误判为真实暴跌
			// （RawBars 口径下移动止损/破位在除权日假触发、盈亏被扭曲）。
			bars, err := db.HfqBars(tsCode, o.Start, o.End)
			if err != nil {
				continue
			}
			if len(bars) < 15 {
				continue
			}
			klines := toDataKLine(bars)
			trades = append(trades, o.backtestStock(code, klines, ad, industryChg[code])...)
			// §节流：每处理完一只股票 sleep 指定毫秒，把全量回放对服务器的瞬时 CPU/内存
			// 挤压摊平到盘后十几个小时——2 核 4G 机器上全池回放曾把可用内存打到熔断线。
			// English: throttle per stock to flatten instantaneous CPU/mem pressure of a
			// full-universe replay over the long post-close window on small boxes.
			if o.ThrottleMs > 0 {
				time.Sleep(time.Duration(o.ThrottleMs) * time.Millisecond)
			}
		}
		sm := summarize(trades)
		if sm.Name == "" {
			// 零触发时 summarize 拿不到交易行，名字会空——报告头变成"战法历史回测: （N 只股票）"。
			// English: zero-trigger adapters have no trade row to carry the name; backfill it.
			sm.Name = ad.Name()
		}
		summaries = append(summaries, sm)
	}
	printReports(summaries, len(codes))
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
func (o *Options) backtestStock(code string, klines []data.KLine, ad adapter, industryChgByDate map[string]float64) []trade {
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
		// §GAP4.2 开盘即封板不可成交：一字板/秒板买单现实中排队无望，跳过该笔
		// （打板类战法此前默认必成交，产生系统性乐观偏差）。
		if costOpenAtLimitUp(code, klines[i].Close, entry) {
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
func (o *Options) simulateExit(code string, klines []data.KLine, entryIdx int, entry float64, meta map[string]float64, ad adapter) *trade {
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
				// §GAP4.1 净额口径：双边滑点+双边佣金+卖出印花税一次性计入收益率
				PnlPct: costRoundTripPnl(entry, cur), Reason: res.Reason,
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
		PnlPct: costRoundTripPnl(entry, last), Reason: "区间结束强制结算",
	}
}

// ── 汇总与输出 ──

// summary 按战法分组的回测统计结果。
type summary struct {
	Name         string  // 战法名称
	Count        int     // 触发/交易次数
	Win          int     // 盈利次数
	Loss         int     // 亏损次数
	WinRate      float64 // 胜率（%）
	AvgWinPct    float64 // 平均盈利百分比
	AvgLossPct   float64 // 平均亏损百分比
	ProfitFactor float64 // 盈亏比
	Expectancy   float64 // 每笔交易期望收益率%（正=正期望策略）
	AvgHold      float64 // 平均持仓天数

	// §GAP4.5 风险调整指标（此前全系统零实现）
	Sharpe          float64 `json:"sharpe"`            // 年化夏普（逐笔净额收益）
	MaxDrawdownPct  float64 `json:"max_drawdown_pct"`  // 复利净值最大回撤%（正数）
	AnnualReturnPct float64 `json:"annual_return_pct"` // 复利年化收益%
	Calmar          float64 `json:"calmar"`            // 卡玛 = |年化/MDD|
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
	// §期望收益：每笔交易的数学期望 E = P(赢)×均盈 + P(亏)×均亏（正=正期望策略）
	wr := s.WinRate / 100
	s.Expectancy = wr*s.AvgWinPct + (1-wr)*s.AvgLossPct
	s.AvgHold = float64(holdSum) / float64(s.Count)

	// §GAP4.5 风险调整指标：按入场日排序后计算（多股票交错入账，净值曲线需时间序）
	ord := make([]int, len(trades))
	for i := range ord {
		ord[i] = i
	}
	sort.Slice(ord, func(a, b int) bool { return trades[ord[a]].Date < trades[ord[b]].Date })
	pnls := make([]float64, len(trades))
	dates := make([]string, len(trades))
	for k, idx := range ord {
		pnls[k] = trades[idx].PnlPct
		dates[k] = trades[idx].Date
	}
	s.Sharpe, s.MaxDrawdownPct, s.AnnualReturnPct, s.Calmar = perfMetrics(pnls, dates)
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
	fmt.Printf("期望收益: %+.2f%%\n", s.Expectancy)
	fmt.Printf("平均持仓天数: %.1f\n", s.AvgHold)
	// §GAP4.5 风险调整指标
	fmt.Printf("夏普: %.2f | 最大回撤: %.2f%% | 年化: %+.2f%% | 卡玛: %.2f\n",
		s.Sharpe, s.MaxDrawdownPct, s.AnnualReturnPct, s.Calmar)
	fmt.Println("==============================================")
}

// printReports 打印多规则分组报告（阶段3.4 战法库回测：每条启用规则一组；单战法仅一组）。
// English: prints grouped reports — one per library rule (a single group for single-strategy runs).
func printReports(summaries []*summary, stockCount int) {
	for _, s := range summaries {
		printReport(s, s.Name, stockCount)
	}
}
