// Package btreplay 五个手写战法（四形态 + 动量）+ 战法库规则的历史回放回测（子系统统一改造二期：
// 自 cmd/backtest_strategy 并入 research 二进制，消除子系统内的第二套回测进程代码）。
// 对外入口：research [--db …] backtest-strategy …（run-task 的 backtest_strategy 类型进程内调用）。
// English: package btreplay — historical replay backtests for the five hand-written strategies plus
// applied factor/pattern library rules. Merged from the standalone bt_strategy binary into the
// research binary (phase 2), leaving one research subsystem with a single entry.
// 从离线研究库（trading.db）读取历史日K，逐交易日回放 dragon/double_bump/dragon_return/n_shape
// 四个内置形态战法与 momentum（动量）共五个内置战法的触发信号，模拟入场后逐日跑各战法的
// CheckExit 平仓并结算盈亏，输出按战法分组的胜率/平均盈亏/盈亏比，以及 1/5/10 日前瞻收益，
// 用于验证与调参。入场时点按战法各自声明：缺省次日开盘，动量按实盘当日撮合（触发当日收盘）。
//
// 说明（近似口径）：板块/日内/LLM 依赖按如下方式近似——
//   - double_bump：纯日K完整回放，最接近实盘。
//   - dragon_return：从日K派生 StockData，板块龙性（IsSectorTop2/SectorRPS20）可配置近似。
//   - dragon：板块共振（F2/F3）用所属行业板块当日涨幅近似；无行业数据时降级忽略板块维度。
//   - n_shape：高度依赖日内快照与 LLM D1，日K近似后准确性打折；D1 用可配置规则分（默认 0，
//     此时仅统计其他维度，几乎不触发，需配合 -d1 提供规则分才有信号）。
//   - momentum（动量）：判据按实盘语义重写（2026-09-24 owner 令）——同标的当日被任一兄弟战法
//     出信号即不入场（实盘 `len(sigs)==0` 兜底档）、触发当日收盘入场（实盘当日撮合）、
//     只计实盘买入档（观察档不算可交易）。日K粒度仍量不到的三件事（5 分钟 MACD、盘中多轮、
//     跨轮"动量提升"门）逐条写在 momentumAdapter 注释与 ReplayApproxNote("momentum") 里，
//     数字出门时带标签。
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

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/combat_agent"
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

// fallbackTierAdapter 声明"实盘兜底档"的战法。动量在实盘只在**同标的当日其它战法一个信号都没出**
// 时才轮到它下单（combat_agent/agent.go 的 `len(sigs) == 0 &&` 分支）。回放框架按战法各自独立跑，
// 天生没有这个跨战法状态——不补就会把"实盘根本轮不到动量"的那些日子也算成动量交易，
// 量出来的胜率属于一批实盘不存在的单子。放行这类战法前必须先由 collect() 预扫出
// "该标的当日已被出手"的日索引（Options.fallbackBlocks），见 backtestStock 的兜底门控段。
// English: a strategy that only trades in live when no other strategy signaled the same stock/day;
// replay must therefore consult a pre-scoped set of days occupied by its sibling adapters.
type fallbackTierAdapter interface{ FallbackTier() bool }

// sameDayEntryAdapter 声明"实盘当日撮合"的战法：信号产生的那一刻就以现价进动量池成交，
// 而回放缺省口径是"次日开盘入场"——那等于让回测替实盘承担了一个实盘没有的隔夜跳空。
// 声明为真的适配器入场时点改为**触发当日收盘**：日K粒度下唯一不需要自造日内路径的当日成交价。
// English: a strategy whose live fills happen the same day the signal is produced, so replay enters
// at the signal day's close instead of the next day's open (the only same-day price that does not
// require inventing an intraday path we do not have).
type sameDayEntryAdapter interface{ SameDayEntry() bool }

// dayScoped 有状态适配器的取数装配点：逐股一次预计算 + 逐日推进游标。
// §RFIX-1 的教训（MACD 序列按池内第一只股票算一次 → 后续股票跨股污染、更长序列游标越界 panic）
// 之所以会复发，就是因为这套装配散落在 backtestStock / sweepTriggersOf 两处手写类型分支里。
// 收敛成一个接口：三处调用点（含兜底互斥预扫）共用，新增有状态适配器只在这里登记一次。
// English: the single place where stateful adapters get their per-stock precomputation and
// per-day cursor, shared by replay, sweep and the fallback-exclusivity pre-scan.
type dayScoped interface {
	prepareStock(klines []data.KLine)
	setDay(i int)
}

// applyStockScope / applyDayScope 对可选接口做装配（非 dayScoped 的适配器零开销跳过）。
func applyStockScope(ad adapter, klines []data.KLine) {
	if ds, ok := ad.(dayScoped); ok {
		ds.prepareStock(klines)
	}
}

func applyDayScope(ad adapter, i int) {
	if ds, ok := ad.(dayScoped); ok {
		ds.setDay(i)
	}
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
	macdSeries []data.MACD // 预计算的日线 MACD 序列（由 backtestStock 逐股重算填充，见 §RFIX-1）
	curIdx     int         // 当前判定日在 macdSeries 中的索引（由 backtestStock 逐日设置）
}

// Name 战法名（回测报告分组键）。
func (a *nShapeAdapter) Name() string { return "N形" }

// prepareStock / setDay 实现 dayScoped：逐股重算日线 MACD 序列（§RFIX-1 的口径不变），逐日推进游标。
func (a *nShapeAdapter) prepareStock(klines []data.KLine) {
	a.macdSeries = data.CalcMACDSeries(klines)
}

func (a *nShapeAdapter) setDay(i int) { a.curIdx = i }

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
	// §RFIX-1 索引钳位兜底：macdSeries 由 backtestStock 逐股重算，长度恒与该股 K 线一致；
	// 任何未来错位（如复用旧适配器）退化为逐日重算而非越界崩溃。
	if a.macdSeries != nil && a.curIdx >= 0 && a.curIdx < len(a.macdSeries) {
		macd = a.macdSeries[a.curIdx]
	} else {
		macd = data.CalcMACD(klines)
	}

	// 把日线摊成 n_shape 期望的两段输入：WaveA 取前一交易日的 OHLCV，
	// A 浪涨幅以再前一根收盘为基准（chgPct(prev, k[-3])），与实盘 A 浪定义一致。
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

// ── momentum 适配器（动量分：日K + 日线 MACD 近似盘中量价/分钟 MACD） ──

// momentumAdapter 动量战法适配器：**按实盘语义重写的可回放判据**（2026-09-24 owner 令：
// "动量判据按实盘语义重写"，此前它是"适配器在位但 Trigger 恒不触发"的排摸盲区）。
//
// 重写落地的三条实盘语义（逐条对应 agent.go 的实现，缺一条量出来的就不是"实盘那批单子"）：
//  1. **兜底互斥**（agent.go:1208 `len(sigs) == 0 &&`）：动量只在**同标的当日四形态战法一个信号都没出**
//     时才轮到下单。由 FallbackTier() 声明，backtestStock 在动量自身达买入档的那一刻回查兄弟适配器
//     （fallbackBlockedByPeer），兄弟当日出过信号即丢弃这条动量信号——与实盘同一条 if 的短路顺序一致。
//  2. **当日撮合**（实盘在分数产生的那一刻就以现价进动量池，md.Price＝当时现价）：由 SameDayEntry()
//     声明，入场时点从"次日开盘"改成**触发当日收盘**。日K粒度下收盘是唯一不需要自造日内路径的
//     当日成交价；用次日开盘等于让回测替实盘承担一个实盘没有的隔夜跳空。
//  3. **买入档才计交易**：实盘 [观察档 60, 买入档 75) 只发 watch 不动单（≥ 买入档才 Action=buy），
//     故 Trigger 用 buyThreshold()，与实盘 momentumBuySignalThreshold 同一对阈值和同一个
//     "买入档不低于观察档"的夹子。
//
// 四条**数据/粒度差异**（前三条不可重建，只能如实标注、不能编出来；第四条是必须主动抵消的口径差）：
//  1. 分钟 MACD → 日线 MACD（**仅在该日没有分钟落库时**）：实盘 macdRatio 读 md.MinuteMACD
//     （5 分钟 K 线算出的 DIF/DEA/Bar）。§MINUTE-K 起研究库有 minute_klines，判档日优先用真
//     5 分钟口径（当日最近 48 根，与实盘 fetchMinuteKLine 同尺寸）；只有该股该日没回填或
//     根数不足 48 时才退回 data.CalcMACDSeries 的**日线** MACD（与 n_shape 的 D4 资金确认同一
//     先例）。方向性偏差只在退回的那部分日子里成立：日线 MACD 比 5 分钟迟钝，金叉/水上通常
//     滞后一日。整轮"有多少日子用了真分钟"随近似说明一起出门（approxNote 拼覆盖率读数）。
//  2. 盘中轮次不可重建：实盘一轮一轮用实时行情打分，日内可能多轮跨越阈值；回放每天只判一次
//     （收盘口径），因此**只会漏掉盘中那一刻的触发**，不会凭空多造触发。
//  3. "动量提升才提醒"门不可重放：实盘该门（combat_agent/agent.go momentumImproved）是**跨轮盘中
//     状态**，且 momentumPrev 每日重置——盘后按日粒度既拿不到"上一轮"，也就无从复现。
//     注意这条门管的是**四形态信号**（N 形豁免），不是动量自己，所以它对动量行的影响是间接的：
//     回放里兄弟用裸 Trigger（不过提升门/板块二次确认/情绪闸），被占掉的日偏多 →
//     **动量入场数偏少**，属保守方向的偏差。
//  4. 盘中量比的时间窗折算：实盘 volumePriceRatio 用 time.Now() 把"当日累计量"折算成全天等值
//     （§P2#26）。回放喂的是已收盘的全日量，故 momentumQuoteVolume 先按同一系数把它缩小，
//     让实盘函数内部的折算正好抵消——**触发结果与回测在几点运行无关**（否则上午跑一次会把
//     全市场量比放大数倍、人人触发）。
//
// English: momentum adapter rewritten to live semantics (owner directive 2026-09-24) — same-stock
// fallback exclusivity, same-day entry at the signal day's close, trade only at the live BUY
// threshold. What daily bars cannot reproduce (minute MACD, intraday rounds, the round-to-round
// improvement gate) is declared as a residual approximation, not faked.
type momentumAdapter struct {
	cfg config.MomentumConfig // 动量权重与双阈值（出厂默认 40/30/30 + 60/75）
	// 日线 MACD 序列 + 当前判定日游标：由 dayScoped 装配点逐股 CalcMACDSeries、逐日推进
	// （§RFIX-1 口径，与 nShapeAdapter 同一手法；逐日重算会退化成 O(n²)）。
	macdSeries []data.MACD
	curIdx     int
	// §MINUTE-K（2026-09-24）真 5 分钟 MACD 口径：装配点逐股注入 src+code，scoreDay 优先用它，
	// 取不到（表为空/当日根数不足/该股未回填）才退回上面的日线序列。覆盖率计数在 src 上
	// （looks/hits，整轮一份），换了口径必须看得见换了多大一部分，否则"升级"和"没数据所以
	// 还是老数字"在报告上长得一模一样。
	minuteSrc  MinuteMACDSource
	minuteCode string
	// §P2-d 出场参数扫参覆盖（nil=通用移动止盈缺省 8%/15 天，动量实盘本就无专属 CheckExit）。
	trailOverride *float64
	holdOverride  *int
}

// MinuteMACDSource 判定日 → 真 5 分钟 MACD 的取值器（§MINUTE-K）。
// 实现方负责"当日根数不足即返回 false"，调用方据此退回日线近似口径——这条边界不能含糊：
// 半天数据算出来的 MACD 与实盘那 48 根不是同一个东西，宁可退回"已声明的日线近似"，
// 也不要拿一个没声明过的第三种近似冒充升级。
// （MinuteMACDSource resolves the live-equivalent 5-minute MACD for one judgement day;
// a day with too few bars must report "not available" rather than a half-day series.）
type MinuteMACDSource interface {
	MinuteMACDAt(tsCode, day string) (data.MACD, bool)
}

// minuteMACDScoped 装配点对动量适配器的可选注入（与 dayScoped 同一手法：能接的接、不能接的跳过）。
type minuteMACDScoped interface {
	setMinuteScope(src MinuteMACDSource, tsCode string)
}

// setMinuteScope 逐股装配分钟口径来源（装配点每次换股都调，src 为 nil 表示本轮没有分钟数据）。
func (a *momentumAdapter) setMinuteScope(src MinuteMACDSource, tsCode string) {
	a.minuteSrc = src
	a.minuteCode = tsCode
}

// storeMinuteMACD 用研究库 minute_klines 复刻实盘那一刻的分钟 MACD：
// 取判定日的全部 5 分钟根 → **截最近 windowToBars(48) 根**（与 strategy_engine.fetchMinuteKLine
// 的 count=48 同尺寸）→ data.CalcMACD（与引擎同一函数、同一 12/26/9 口径）。
// 逐(股,日)缓存：同一轮回放里兄弟战法回查与主循环可能重复问同一天，缓存顺带把 SQL 次数
// 从"每股判档日数"压到不重复的量。looks/hits 是观测计数（不参与判红）。
type storeMinuteMACD struct {
	db     *store.DB
	scale  int
	window int
	cache  map[string]minuteMACDEntry
	looks  int
	hits   int
	// queries 真正落到 SQL 的次数（looks-queries 就是缓存省掉的查询数）：没有这一项，
	// "缓存有没有生效"在测试里完全不可观测，只能靠断言计数器的自嗨。
	queries int
	// byBare 裸 6 位代码 → 库内 ts_code 的一次性反查表（见 normalizeCode）。
	// 为什么需要：回放内部到处用裸代码（klines 映射的键、扫参的 code 参数），而分钟表主键是
	// ts_code；在装配点建一次表（一次 DISTINCT 查询），比在每个调用点透传 ts_code 改动面小。
	byBare map[string]string
}

// normalizeCode 把裸代码还原成库内 ts_code；已是 ts_code 形态（带点）则原样返回。
// 查不到（该股没回填分钟数据）返回原串——后续按日查询自然零行，报"不可用"退回日线近似。
func (s *storeMinuteMACD) normalizeCode(code string) string {
	if strings.Contains(code, ".") || s.byBare == nil {
		return code
	}
	if ts, ok := s.byBare[code]; ok {
		return ts
	}
	return code
}

type minuteMACDEntry struct {
	macd data.MACD
	ok   bool
}

// newStoreMinuteMACD 按周期装配来源（scale 传 5；window 固定 48 根＝实盘一日的尺寸）。
// codes 是本轮回放股票池的 ts_code 清单，用来建裸代码反查表。
func newStoreMinuteMACD(db *store.DB, scale int, codes []string) *storeMinuteMACD {
	if scale <= 0 {
		scale = 5
	}
	s := &storeMinuteMACD{db: db, scale: scale, window: 48, cache: map[string]minuteMACDEntry{},
		byBare: make(map[string]string, len(codes))}
	for _, ts := range codes {
		if i := strings.Index(ts, "."); i > 0 {
			s.byBare[ts[:i]] = ts
		}
	}
	return s
}

// MinuteMACDAt 实现 MinuteMACDSource：查该 (股,周期,判定日) 的分钟根 → 不足 window 根即报
// "不可用"（口径边界，见接口注释）→ 截尾 window 根 → data.CalcMACD。
// 根数门槛为什么硬卡在 window：实盘那 48 根是"整个交易日"，半日只有 12 根时 EMA26 预热根本
// 不够，算出来的 DIF/DEA 与实盘不同源，宁可用已声明的日线近似也不能冒充升级。
func (s *storeMinuteMACD) MinuteMACDAt(tsCode, day string) (data.MACD, bool) {
	if s == nil || s.db == nil || tsCode == "" || len(day) < 10 {
		return data.MACD{}, false
	}
	s.looks++
	tsCode = s.normalizeCode(tsCode)
	key := tsCode + "|" + day
	if e, ok := s.cache[key]; ok {
		if e.ok {
			s.hits++
		}
		return e.macd, e.ok
	}
	s.queries++
	bars, err := s.db.MinuteBarsByDay(tsCode, s.scale, day)
	entry := minuteMACDEntry{}
	// 查询失败（表不存在/旧库未迁移）与"根数不足"归同一类：报不可用、让调用方退回日线口径。
	// 不返回 error 是因为回放的判档循环里没有任何一处能处理它，往上抛只会把整轮回测打断。
	//
	// 门槛只有**一处**（转换之后的 len(mkl) >= window）：先按窗口截尾、再剔除零价占位根，
	// 剩下的有效根数才是"能不能算实盘同口径 MACD"的唯一依据。两处判据（截尾前一次、转换后
	// 一次）会让 47 根被剔除后的序列仍带着"原始行数够 48"的错觉通过，也会让改窗口的人漏改一处。
	if err == nil {
		if len(bars) > s.window {
			bars = bars[len(bars)-s.window:] // 截尾：只留最近 window 根（与实盘 count=48 同尺寸）
		}
		mkl := make([]data.KLine, 0, len(bars))
		for _, b := range bars {
			if b.Close <= 0 {
				continue // 零价根＝停牌/半根，喂进 EMA 会把序列拉歪，直接丢弃
			}
			mkl = append(mkl, data.KLine{
				Date:   minuteBarTime(b.Ts),
				Open:   b.Open,
				High:   b.High,
				Low:    b.Low,
				Close:  b.Close,
				Volume: b.Vol,
				Amount: b.Amount,
			})
		}
		if len(mkl) >= s.window {
			entry = minuteMACDEntry{macd: data.CalcMACD(mkl), ok: true}
		}
	}
	s.cache[key] = entry
	if entry.ok {
		s.hits++
	}
	return entry.macd, entry.ok
}

// minuteBarTime 把落库的北京时间墙钟字符串解析成北京时间 time.Time（cntime.Loc，不读本机时区）。
// 解析失败退到零值——MACD 只用收盘价，日期字段只是结构占位，绝不允许在这里"猜"一个时区把
// 一根不属于当日的根混进来（ts 已由 MinuteBarsByDay 的日范围过滤过）。
func minuteBarTime(ts string) time.Time {
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02 15:04"} {
		if t, err := time.ParseInLocation(layout, ts, cntime.Loc); err == nil {
			return t
		}
	}
	return time.Time{}
}

// coverage 返回整轮观测计数（命中率 = hits/looks），供装配点打日志与近似说明用。
// 口径：looks＝动量判档被问了几次"这天的 5 分钟 MACD"，hits＝其中答得出（根数够窗口）的次数。
func (s *storeMinuteMACD) coverage() (looks, hits int, pct float64) {
	l, h, _, p := s.coverageStats()
	return l, h, p
}

// coverageStats 返回全部三个观测计数，供出门文本用。为什么非要多带一个 queries：
// 2026-09-25 全量重算里两次 all 回放的动量触发数差 2.1×，日线口径下的"判档日"分母也同步漂了约 22%，
// 只报 hits/looks 分不清"判的次数真变了"还是"缓存命中结构变了"（looks 含缓存命中、queries 只数
// 真正查库那一次）。两个数一起出门，读者就能判断漂移发生在判档层还是取数层。
// 观测计数**不参与判红**（纯读数）。
// English: exposes looks/hits/queries so a coverage shift can be attributed to the judgement layer
// or the query layer; read-only, never used in a pass/fail decision.
func (s *storeMinuteMACD) coverageStats() (looks, hits, queries int, pct float64) {
	if s == nil || s.looks == 0 {
		return 0, 0, 0, 0
	}
	return s.looks, s.hits, s.queries, float64(s.hits) / float64(s.looks) * 100
}

// Name 战法名（回测报告分组键；与实盘 strategy.SignalMomentum 的显示名同一字面）。
func (a *momentumAdapter) Name() string { return "动量" }

// FallbackTier 声明实盘兜底档身份：回放必须过 backtestStock 的兜底互斥门控（见 fallbackTierAdapter）。
func (a *momentumAdapter) FallbackTier() bool { return true }

// SameDayEntry 声明实盘当日撮合：入场价取触发当日收盘而非次日开盘（见 sameDayEntryAdapter）。
func (a *momentumAdapter) SameDayEntry() bool { return true }

// prepareStock / setDay 实现 dayScoped：逐股重算日线 MACD 序列、逐日推进游标（§RFIX-1 口径，
// 与 nShapeAdapter 同一对方法；游标不在此预置，缺 -1 时 scoreDay 的钳位判据会退化成逐日重算）。
func (a *momentumAdapter) prepareStock(klines []data.KLine) {
	a.macdSeries = data.CalcMACDSeries(klines)
}

func (a *momentumAdapter) setDay(i int) { a.curIdx = i }

// watchThreshold 观察级阈值（实盘 momentumSignalThreshold 同口径：≤0 回退 60）。
func (a *momentumAdapter) watchThreshold() float64 {
	if a.cfg.SignalThreshold <= 0 {
		return 60
	}
	return a.cfg.SignalThreshold
}

// buyThreshold 买入级阈值（实盘 momentumBuySignalThreshold 同口径：≤0 回退 75，且不低于观察阈值）。
func (a *momentumAdapter) buyThreshold() float64 {
	buy := a.cfg.BuySignalThreshold
	if buy <= 0 {
		buy = 75
	}
	if w := a.watchThreshold(); buy < w {
		buy = w
	}
	return buy
}

// Trigger 当日是否出"可交易的动量信号"：实盘同一打分函数 + 实盘买入档阈值（近似口径与三条
// 实盘语义见 momentumAdapter 注释）。判据本体在 scoreDay，Trigger 只做委托——**兜底互斥门控
// 不在这里**：那是跨战法状态，适配器自己看不到兄弟战法，由 backtestStock 的兜底段裁决。
func (a *momentumAdapter) Trigger(klines []data.KLine, prevClose float64, _ float64) (map[string]float64, bool) {
	return a.scoreDay(klines, prevClose)
}

// scoreDay 动量判档本体：用截止当日的日K构造实盘同结构的 StockMarketData，跑
// combat_agent.MomentumScore，按实盘买入档给结论。
// 门槛：日K ≥30 根（日线 MACD 的 EMA26+DEA9 预热需要，比实盘的 ≥5 有效数据门槛更严——
// 近似口径 1 的必然代价：预热不足的 MACD 全是 0，动量分会白丢 30 分权重）。
// 返回值 meta 带 score（动量分，供扫参 min_score 维度同构复用）；bool=分数是否达买入档
// （实盘此档才发 buy；[观察档, 买入档) 只发 watch、不下单，故不算可交易信号）。
func (a *momentumAdapter) scoreDay(klines []data.KLine, prevClose float64) (map[string]float64, bool) {
	if len(klines) < 30 {
		return nil, false
	}
	last := klines[len(klines)-1]
	if prevClose <= 0 && len(klines) >= 2 {
		prevClose = klines[len(klines)-2].Close
	}
	chg := chgPct(last.Close, prevClose)
	// MACD 口径选择（§MINUTE-K）：**优先真 5 分钟 MACD**（研究库 minute_klines 当日最近 48 根，
	// 与实盘 fetchMinuteKLine(count=48)→CalcMACD 同源）；当日没回填/根数不足才退回日线序列
	// （近似口径 1，已声明）。覆盖率计数在 src 上，随近似说明一起出门。
	var macd data.MACD
	haveMinute := false
	if a.minuteSrc != nil && a.minuteCode != "" {
		if m, ok := a.minuteSrc.MinuteMACDAt(a.minuteCode, cntime.DayOf(last.Date)); ok {
			macd = m
			haveMinute = true
		}
	}
	if !haveMinute {
		// 未命中分钟口径 → 日线近似：序列缺失/游标错位时退化为逐日重算（不越界）。
		if a.macdSeries != nil && a.curIdx >= 0 && a.curIdx < len(a.macdSeries) {
			macd = a.macdSeries[a.curIdx]
		} else {
			macd = data.CalcMACD(klines)
		}
	}
	md := &strategy_engine.StockMarketData{
		Price:     last.Close,
		ChangePct: chg,
		KLines:    klines,
		// 实盘动量分量读 Quote 的当日量价：这里用当日日K + 收盘涨跌幅喂一份等价快照，
		// 成交量按近似口径 3 折算，抵消 MomentumScore 内部的盘中时间窗放大。
		Quote: &data.StockInfo{
			Price:  last.Close,
			Open:   last.Open,
			High:   last.High,
			Low:    last.Low,
			Close:  last.Close,
			Volume: momentumQuoteVolume(time.Now(), last.Volume),
			Amount: last.Amount,

			ChangePct: chg,
		},
		MinuteMACD: macd,
	}
	// 与实盘 momentumDataValid 同一有效性判定（无有效量价/MACD 时不出信号，避免拿全零序列
	// 凑出一个"0 分但可交易"的假信号）；这条路径里唯一会挂的是 MACD 预热不足。
	if last.Close <= 0 || last.Volume <= 0 || (macd.DIF == 0 && macd.DEA == 0 && macd.Bar == 0) {
		return nil, false
	}
	score := combat_agent.MomentumScore(md, a.cfg)
	meta := map[string]float64{
		"highest_price": last.Close, // 移动止盈基准（Exit 逐日抬高）
		"score":         score,
	}
	return meta, score >= a.buyThreshold()
}

// Exit 动量实盘没有专属 CheckExit（持仓走 combat_agent 的通用移动止盈回退，见
// position_exits.go），回放同口径：8% 移动止盈 + 15 日超期，缺省值与扫参覆盖都由
// genericReplayExit 统一实现，绝不在这里另写一套出场。
func (a *momentumAdapter) Exit(ctx *strategy.ExitContext, dailyK []strategy.KLine) (*strategy.ExitResult, bool) {
	trailLimit, holdLimit := -8.0, 15
	if a.trailOverride != nil && *a.trailOverride > 0 {
		trailLimit = -*a.trailOverride
	}
	if a.holdOverride != nil && *a.holdOverride > 0 {
		holdLimit = *a.holdOverride
	}
	return genericReplayExit(ctx, trailLimit, holdLimit)
}

// replayElapsedTradeMinutes A股当日已流逝交易分钟数（北京时区，240 分钟制＝上午 120 + 下午 120）。
// 这是 combat_agent.tradingMinutesElapsed 的**本地副本**，唯一用途是让回放喂进去的量在
// 实盘函数里被同一个系数乘回来（见 momentumQuoteVolume）——两边口径必须同步，实盘若改
// 折算窗口，这里不改就会让动量回放的量比系统性失真。
// English: local mirror of the live elapsed-trading-minute proration, used only to pre-compensate
// the full-day volume so MomentumScore's intraday normalization cancels out exactly.
func replayElapsedTradeMinutes(now time.Time) float64 {
	n := cntime.In(now)
	m := n.Hour()*60 + n.Minute()
	const (
		amStart = 9*60 + 30  // 09:30
		amEnd   = 11*60 + 30 // 11:30
		pmStart = 13 * 60    // 13:00
		pmEnd   = 15 * 60    // 15:00
	)
	switch {
	case m < amStart:
		return 0
	case m <= amEnd:
		return float64(m - amStart)
	case m <= pmStart: // 午休：上午已走完
		return 120
	case m <= pmEnd:
		return 120 + float64(m-pmStart)
	default: // 盘后：全日 240
		return 240
	}
}

// momentumQuoteVolume 把"当日全日成交量"折算成 now 时刻的盘中累计量等价值（近似口径 3）。
// 实盘量比 = 累计量 × (240/已流逝分钟) ÷ 前 20 日均量；回放喂全日量会被再放大一次，
// 这里先按同系数缩小把它还原，运行时刻不再影响触发结果。盘前（流逝 0 分钟）与实盘
// 同一兜底（按 1 分钟），保证折算系数有限且两侧一致。
func momentumQuoteVolume(now time.Time, fullDayVol float64) float64 {
	if fullDayVol <= 0 {
		return 0
	}
	elapsed := replayElapsedTradeMinutes(now)
	if elapsed <= 0 {
		elapsed = 1
	}
	return fullDayVol * elapsed / 240
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
	Strategy  string  // double_bump|dragon|dragon_return|n_shape|momentum|factor|pattern|all（战法选择）
	MaxStocks int     // 最多回测股票数（0=全部）
	D1Score   float64 // 外部注入的固定 D1 分（≥0 时使用）
	Industry  bool    // 是否启用行业过滤/分组
	DataDir   string  // 战法库目录（applied_factors.json / applied_patterns.json 所在）
	// AllowEmptyLibrary 显式声明"战法库一条启用规则都没有也要照跑"。缺省 false ⇒ 只要 Strategy
	// 声明要跑库规则（all|factor|pattern），而库里一条都没加载到，collect 就**直接报错、非 0 退出**，
	// 不再静默按"0 条线上战法"跑完整轮。（§LIB-GATE 2026-09-25，起因：全量重算时 -strategy all
	// 未带库副本，日志首行写了 0 条库规则却照常出门一张表，读者把"线上没有战法时动量能拿多少单"
	// 当成了"线上跑完一轮"的数字——与本仓反复出事的"降级报成功"同型。）
	// English: opt out of the empty-library hard failure; default off so a replay that claims to
	// cover the live rule library cannot silently run with zero rules.
	AllowEmptyLibrary bool
	// Library 运行期出门事实（collect 装配时写；非配置项）：库目录、目录来源、加载到的因子/形态
	// 规则条数、零条时的成因。它随报告头一行与排摸产物 JSON 一起出门，让"这轮用的是哪一版战法库"
	// 跟着数字走，而不是只留在没人读的日志里。
	Library LibraryLoad
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
	// RiskFreeRate §WS-D D3 无风险利率（年化，小数）：Sharpe 日频口径 (mean(R_daily)−rf/252)/std×√252。
	// 默认 0（A 股保守口径）；参考可配国债利率如 0.02。English: §WS-D D3 annual risk-free rate used in
	// the daily-frequency Sharpe formula; default 0, a reasonable A-share reference is ~0.02.
	RiskFreeRate float64
	// PointInTime §WS-D D-2 时点股票池：**缺省开**（owner 裁决 2026-09-26「幸存者偏差开关：
	// 开，默认打开」）。nil=开；显式 false 才退回"今天在市的票"旧口径。置真时以回测起始日
	// "当时点已上市且未退市"的股票池（store.UniverseAt(o.Start)，含退市样本消除幸存者偏差）
	// 替代全量 StockCodes()——数据前提是 dataload §B4-META 已回填 list_date/delist_date；
	// 元数据覆盖为 0 时本闸把降级抬成**可见读数**（报告首行池口径声明），不静默空跑。
	// English: §WS-D D-2 point-in-time universe — ON by default (owner ruling 2026-09-26);
	// explicit false opts back into the survivorship-biased "today-listed" pool. Empty metadata
	// coverage degrades loudly (pool-basis line in the report), never silently.
	PointInTime *bool
	// Codes 非空时直接作为回放股票池（跳过 StockCodes/质控/时点三条池解析路径）。
	// strategy-survey 用它把排摸窗口/池与研究面板装配钉在同一份清单上，保证成分因子健康度
	// 与回放结果口径一致。English: explicit universe, bypassing DB pool resolution (survey uses this).
	Codes []string
	// IncludeDisabled 为真时战法库**停用**条目也建适配器参与回放。
	// 仅供 strategy-survey 排摸全库；生产 backtest-strategy/library_replay 保持默认 false，
	// 行为与旧版逐字节一致（只回放启用规则）。
	// English: when true, disabled library rules are replayed too (survey-only; default false keeps
	// the production behavior unchanged).
	IncludeDisabled bool
	// DB 调用方已持有的研究库连接：非 nil 时直接复用、不再按 DBPath 二次 Open，
	// 避免同进程双连接抢锁（strategy-survey 在 main 里已 Open）。
	// English: reuse a caller-owned connection instead of opening DBPath again.
	DB *store.DB
	// Backtest §回测自动增强 A0：动态滑点/流动性约束/Pareto 寻优配置（payload.backtest 注入）。
	// nil 或 Enabled=false = 行为与增强前完全一致（固定 5bp、无流动性门控、无 Pareto 段）。
	// English: backtest enhancement config injected via task payload; nil/disabled keeps the
	// legacy fixed-5bp behavior byte-for-byte.
	Backtest *config.BacktestConfig

	// slip 运行期滑点上下文（Run/runSweep 每战法装配一次，非配置项；nil=旧行为）。
	// English: runtime slippage context assembled per strategy during a run (not a config field).
	slip *slipCtx
	// fallbackPeers 运行期兜底互斥的"兄弟战法"清单（collect/runSweep 各装配一次，非配置项）：
	// 实盘 agent.go 的 `len(sigs) == 0 &&` 是**同标的同一轮**跨战法状态，逐战法独立跑的回放
	// 天生看不到它。这里把同批次构建的其它适配器交给兜底档适配器回查，量出的才是"实盘真由
	// 动量下单的那批票"。清单来自同一份 ads（排摸 IncludeDisabled=true 时停用库规则也算兄弟），
	// 偏差方向是动量入场数偏少——保守侧，见 momentumAdapter 残余近似 3。
	fallbackPeers []adapter
	// minuteSrc §MINUTE-K 运行期真 5 分钟 MACD 来源（collect/runSweep 各装配一次，非配置项）：
	// nil = 研究库没有 minute_klines（旧库/未回填）⇒ 动量判档整体退回日线近似，输出与升级前
	// 逐字节一致。装配点见 applyMinuteScope。
	minuteSrc *storeMinuteMACD
	// poolNote §B4-PIT 运行期池口径读数（collect 装配一次，非配置项）：本轮股票池到底是不是
	// 时点口径、降级时为什么降级——随报告首行出门，引用数字的人与前提同屏。
	poolNote string
	// finaSrc §B2 运行期财务输入源（collect/runSweep 各装配一次，非配置项）：
	// 因子规则回放逐股喂"判定日可见"的财报（与实盘 finaCache 同一份裁决函数）。
	// nil＝不喂（财务腿按缺失计入），§B2-FINA 读数行会如实说"未装配"。装配点见 applyFinaScope。
	finaSrc *finaProvider
}

// PITEnabled §B4-PIT（owner 裁决 2026-09-26）：时点股票池是否生效——nil（未配置）→ 默认开；
// 显式 false 才关。判定姿势与 config.EnforceT1Enabled 同款。
// English: §B4-PIT — point-in-time universe is enabled unless explicitly set false (nil = on).
func (o *Options) PITEnabled() bool {
	return o.PointInTime == nil || *o.PointInTime
}

// minuteMACDScale 动量口径使用的分钟周期：与实盘 fetchMinuteKLine 的 scale=5 同一数字。
const minuteMACDScale = 5

// applyMinuteScope 逐股把分钟口径来源注入动量适配器（非动量适配器静默跳过）。
// tsCode 空串＝本轮没有分钟来源（src 为 nil 时同样传空），适配器据此判定"只能走日线近似"。
func (o *Options) applyMinuteScope(ad adapter, tsCode string) {
	ms, ok := ad.(minuteMACDScoped)
	if !ok {
		return
	}
	if o.minuteSrc == nil {
		ms.setMinuteScope(nil, "")
		return
	}
	ms.setMinuteScope(o.minuteSrc, tsCode)
}

// minuteCoverageNote 装配收尾的覆盖率读数（nil 来源 = 整轮都是日线近似）。
func (o *Options) minuteCoverageNote() string {
	if o.minuteSrc == nil {
		return "动量 MACD 口径：日线近似（研究库无 minute_klines，未回填）"
	}
	looks, hits, queries, pct := o.minuteSrc.coverageStats()
	return fmt.Sprintf("动量 MACD 口径：真 5 分钟 %d/%d 判档日（%.1f%%），其余退回日线近似；查库 %d 次（缓存命中 %d 次）",
		hits, looks, pct, queries, looks-queries)
}

// setFallbackPeers 装配兜底互斥的兄弟清单：同一批适配器里**非兜底档**的那些。
// 兜底档之间不互为兄弟（实盘由同一条 if 分支产出，互不占用名额）。
func (o *Options) setFallbackPeers(ads []adapter) {
	peers := make([]adapter, 0, len(ads))
	for _, ad := range ads {
		if fb, ok := ad.(fallbackTierAdapter); ok && fb.FallbackTier() {
			continue
		}
		peers = append(peers, ad)
	}
	o.fallbackPeers = peers
}

// hasFallbackTier 本批适配器里是否存在兜底档战法（决定要不要为它装配兄弟清单）。
func hasFallbackTier(ads []adapter) bool {
	for _, ad := range ads {
		if fb, ok := ad.(fallbackTierAdapter); ok && fb.FallbackTier() {
			return true
		}
	}
	return false
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
	case "momentum":
		// 动量：无策略对象，直接喂实盘打分函数 MomentumScore（近似口径见 momentumAdapter 注释）。
		return &momentumAdapter{cfg: sc.Momentum, curIdx: -1}, nil
	default:
		return nil, fmt.Errorf("未知战法: %s", name)
	}
}

// ── 已应用战法回测适配器（阶段3.4：applied_factors.json / applied_patterns.json）──

// ruleEvalAdapter 因子/形态规则回测适配器：直接复用实盘 FactorStrategy / PatternStrategy 的
// Evaluate 逻辑（seriesFromKLines 由日K计算因子 → 时间序列分位×权重×方向 / [min,max) 条件解释），
// 与 8a/8b 实盘同一套打分口径；§B2 起因子侧还把"判定日可见"的财报喂进 md.Fina（与实盘 finaCache
// 共享同一份可见性/新鲜度裁决），"同一套打分口径"从"只对价量成立"补齐到含财务成分的规则。
// 退出用通用移动止盈+超期（与实盘未知战法回退 genericTrailingExit 同口径）。
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
	// §B2 财务输入注入态（逐股由 applyFinaScope 装配）：finaSrc=nil 或 finaCode="" 时不喂，
	// Evaluate 拿到的 md.Fina 与升级前逐字节一致（Fina 恒 nil → 财务因子按 NaN 跳过成分）。
	finaSrc  *finaProvider
	finaCode string
}

// setFinaScope 实现 finaScoped：逐股装配财务输入源与当前票代码（装配点每次换票都调）。
func (a *ruleEvalAdapter) setFinaScope(p *finaProvider, tsCode string) {
	a.finaSrc, a.finaCode = p, tsCode
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
	// §B2（owner 裁决 2026-09-26「因子回放要不要喂财务数据：要」）：因子规则补上"判定日可见"
	// 的财务腿——裁决全走与实盘 finaCache 共享的 strategy_engine 函数（ann_date≤判定日、
	// 过旧停用、查库失败按缺失），回放数字从此回答"实盘机制那天会怎么打分"。
	// 未装配（finaSrc=nil）时 Fina 保持 nil，与升级前行为逐字节一致。
	// 形态规则不喂：pattern.seriesFromKLines 只有价量字段，Fina 无人消费。
	// English: §B2 — feed the factor rule the report visible on the judgment day via the same
	// shared predicates the live cache uses; unassembled stays byte-identical to pre-upgrade.
	if a.fs != nil && a.finaSrc != nil && a.finaCode != "" {
		md.Fina = a.finaSrc.visibleFina(a.finaCode, last.Date)
	}
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
	return genericReplayExit(ctx, trailLimit, holdLimit)
}

// genericReplayExit 通用移动止盈 + 超期离场的**唯一实现**（实盘 combat_agent.genericTrailingExit
// 同口径），供没有专属 CheckExit 的战法复用：库规则（ruleEvalAdapter）与动量（momentumAdapter）。
// trailLimit 传**负号语义**（-8 ＝ 从阶段高点回撤 8% 触发），holdLimit 为超期天数。
// English: the single generic trailing-stop + timeout exit reused by adapters without their own
// CheckExit; trailLimit keeps the live negative-sign semantics.
func genericReplayExit(ctx *strategy.ExitContext, trailLimit float64, holdLimit int) (*strategy.ExitResult, bool) {
	cost, price := ctx.CostPrice, ctx.CurPrice
	if cost <= 0 || price <= 0 {
		return nil, false
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

// LibraryLoad 一次回放对「战法库」这条输入的实际读数（§LIB-GATE 出门事实）。
// 为什么要单独有这一个结构：库里几条规则、这些条数是从哪个目录读来的、读不到时到底为什么读不到，
// 过去只出现在日志首行的一句话里——而引用数字的人读的是结果表，不是日志。2026-09-25 全量重算就
// 因此把"这台机器没有线上战法库时动量能拿多少单"（144,420）当成了"线上跑完一轮"的数字（真值
// 67,925，差 2.1×）。读数随报告头一行与排摸产物 JSON 一起出门，读者拿不到"没带库"这条前提就没法
// 误用数字。
// English: what the library input actually resolved to (dir, dir source, rule counts, and — when
// zero — *why*). Numbers must travel with the premise they were produced under.
type LibraryLoad struct {
	Dir      string `json:"dir"`             // 解析后的库目录（空=未指定）
	DirFrom  string `json:"dir_from"`        // 目录来源：explicit（调用方给的）| env（QUANT_DATA_DIR）| home_default（机器缺省）| unset
	EntriesF int    `json:"entries_factor"`  // 因子库文件里的条目数（含停用，过滤前）
	EntriesP int    `json:"entries_pattern"` // 形态库文件里的条目数（含停用，过滤前）
	// EnabledF/EnabledP＝文件里**标记为启用**的条目数。与 Entries（有没有东西）和 Rules（建成几条
	// 适配器）三档一起，才把"零条"的四种成因区分开（见 ruleFileReason）。
	EnabledF int `json:"enabled_factor"`
	EnabledP int `json:"enabled_pattern"`
	RulesF   int `json:"factor_rules"`  // 真正建成回放适配器的因子规则数（过滤后）
	RulesP   int `json:"pattern_rules"` // 真正建成回放适配器的形态规则数（过滤后）
	// ZeroReason 非空＝本轮声明要跑库规则却一条都没加载到，成因逐字点名（各态不得压成一个"空"），
	// 并**带侧名前缀**（factor: / pattern:，all 模式两侧都空时两条拼在一起）：
	// dir_unset / file_missing / file_unreadable / file_blank / no_entries / all_disabled / no_usable_rule。
	ZeroReason string `json:"zero_reason,omitempty"`
	// Gate 出门时的门状态：enforced（零条即判红）/ waived（显式放行）/ ok（有条目，门未触发）/
	// not_applicable（单内置战法回放，本来就不读库）/ candidate_direct_exempt（候选直读，与库无关）。
	Gate string `json:"gate"`
}

// String 把读数压成一行可 grep 的出门文本（报告头与夜间任务 result_text 共用一份，不两处各写）。
func (l LibraryLoad) String() string {
	if l.ZeroReason != "" {
		return fmt.Sprintf("战法库读数：dir=%s 来源=%s 因子=%d 形态=%d 成因=%s 门=%s",
			l.Dir, l.DirFrom, l.RulesF, l.RulesP, l.ZeroReason, l.Gate)
	}
	return fmt.Sprintf("战法库读数：dir=%s 来源=%s 因子=%d 形态=%d（文件条目 %d/%d，其中启用 %d/%d）门=%s",
		l.Dir, l.DirFrom, l.RulesF, l.RulesP, l.EntriesF, l.EntriesP, l.EnabledF, l.EnabledP, l.Gate)
}

// resolveDirFrom 判定库目录的来源（"读到了什么"必须连"从哪读的"一起出门）。
// Options.DataDir 由调用方装配，命令行/单子命令的缺省值是空串，故这里只能按"非空即调用方给的"
// 判第一档，其余两档按环境变量与机器缺省目录逐字比对。
func resolveDirFrom(dataDir string) string {
	if dataDir == "" {
		return "unset"
	}
	if env := os.Getenv("QUANT_DATA_DIR"); env != "" && filepath.Clean(env) == filepath.Clean(dataDir) {
		return "env"
	}
	if filepath.Clean(DefaultDataDir()) == filepath.Clean(dataDir) {
		return "home_default"
	}
	return "explicit"
}

// ruleFileReason 在"一条规则都没加载到"时点名字面成因。四种"空"是**四件不同的事**，修法各异：
//   - dir_unset / file_missing / file_unreadable：目录没给或指错（研究机上最常见的形态＝根本没带上
//     线上库副本，正是 2026-09-25 那次数值误用的现场）→ 换目录或走 survey_live_rules.sh 只读副本；
//   - file_blank：文件存在但零字节（写入被截断／部署链只建了空壳）→ 查落库那一腿；
//   - no_entries：文件是合法 JSON 数组但里面没条目 → 等审批链产出；
//   - all_disabled：有条目但全部标记停用 → 有人手工停用了战法，是**业务事实**不是故障；
//   - no_usable_rule：有启用条目却一条都建不出适配器（条目自身因子集为空＝历史遗留脏数据）→ 修数据。
//
// 压成一句"库为空"等于没报：五种情况下该跑的命令完全不同。
// English: names the literal cause of a zero-rule load; the five cases need different remedies,
// so they must not collapse into one "empty library" string.
func ruleFileReason(dataDir, fileName string, entries, enabled, built int) string {
	if dataDir == "" {
		return "dir_unset"
	}
	st, err := os.Stat(filepath.Join(dataDir, fileName))
	if err != nil {
		if os.IsNotExist(err) {
			return "file_missing"
		}
		return "file_unreadable"
	}
	if st.Size() == 0 {
		return "file_blank"
	}
	switch {
	case entries == 0:
		return "no_entries"
	case enabled == 0:
		return "all_disabled"
	case built == 0:
		return "no_usable_rule"
	}
	return "unknown" // 到不了：三档读数都非零时门在上一行就放行了。到得了就是读数与装配脱钩，如实报
}

// libraryGate §LIB-GATE 的唯一判红口：声明要跑库规则却零条即错误，除非显式放行。
// wantF/wantP 由调用分支决定（all=两侧都要、factor 只要因子、pattern 只要形态），
// 缺省即红＝**放行必须是显式动作**，与本仓所有运维脚本"缺省只预览"同一方向。
func (o *Options) libraryGate(ll *LibraryLoad, wantF, wantP bool) error {
	ll.DirFrom = resolveDirFrom(o.DataDir)
	if ll.RulesF+ll.RulesP > 0 {
		ll.Gate = "ok"
		return nil
	}
	// 成因按声明要的两侧各判一次，**各带侧名前缀**拼起来——只跑 factor 时不许被形态库文件缺失带红
	// （反之亦然）；而 all 模式两侧都空时，去掉前缀去重会把"两侧都判过"这件事抹掉（读者以为只有一侧空）。
	why := ""
	if wantF && ll.RulesF == 0 {
		why = "factor:" + ruleFileReason(o.DataDir, "applied_factors.json", ll.EntriesF, ll.EnabledF, ll.RulesF)
	}
	if wantP && ll.RulesP == 0 {
		r := "pattern:" + ruleFileReason(o.DataDir, "applied_patterns.json", ll.EntriesP, ll.EnabledP, ll.RulesP)
		if why == "" {
			why = r
		} else {
			why = why + "+" + r
		}
	}
	ll.ZeroReason = why
	if o.AllowEmptyLibrary {
		ll.Gate = "waived"
		return nil
	}
	ll.Gate = "enforced"
	return fmt.Errorf("战法库零条启用规则，回放判红（%s）：目录 %s（来源 %s）下按实盘口径加载到 因子 %d 条 / 形态 %d 条。"+
		"绝不静默按「0 条线上战法」跑完整轮——那样出门的数字回答的是「线上没有战法时会怎样」，不是「线上跑完一轮」。"+
		"修法是显式表态：要跑线上战法就 --datadir 指到真有 applied_factors.json/applied_patterns.json 的目录"+
		"（研究机走 scripts/survey_live_rules.sh 那份只读副本）；确实要在空库上跑就加 --allow-empty-library",
		why, o.DataDir, ll.DirFrom, ll.RulesF, ll.RulesP)
}

// libScan loadRuleAdapters 的三档读数：文件里的条目数 / 其中启用数 / 真正建成适配器数。
// 为什么要三档而不是"成功条数"一个数：判红要说清"为什么零条"，一档分不清"没文件""有文件没条目"
// "有条目全停用""启用条目自身是脏数据"（见 ruleFileReason）。
type libScan struct {
	Entries int
	Enabled int
	Built   int
}

// loadRuleAdapters 从战法库加载规则，每条规则一个 adapter（kind: factor|pattern）。
// includeDisabled=true 时停用条目也建适配器（strategy-survey 全库排摸用）；
// 生产回放路径恒传 false = 只加载启用规则，与旧行为一致。
// 第二个返回值 libScan 是**过滤前后各档读数**（条目 / 启用 / 建成）：§LIB-GATE 要靠它把"没文件"
// "文件是空的""有条目但全停用""启用条目自身没带因子集"分成四种成因点名，只回一个切片就得不出区分。
// English: also returns per-stage counts (entries / enabled / built) so a zero-rule load can be
// reported with its actual cause instead of a bare "empty library".
func loadRuleAdapters(kind, dataDir string, includeDisabled bool) ([]adapter, libScan, error) {
	switch strings.ToLower(kind) {
	case "factor", "factor_rules", "applied_factors":
		// §P2-d：直接读库条目以携带规则级出场覆盖（扫参审批后回测立即生效）；
		// English: read library entries directly so sweep-approved exit overrides apply to replays.
		entries, err := research.ListAppliedFactorRules(dataDir)
		if err != nil {
			return nil, libScan{}, err
		}
		scan := libScan{Entries: len(entries)}
		out := make([]adapter, 0, len(entries))
		for i := range entries {
			e := &entries[i]
			if e.Enabled {
				scan.Enabled++
			}
			if (!e.Enabled && !includeDisabled) || len(e.Factors) == 0 {
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
		scan.Built = len(out)
		return out, scan, nil
	case "pattern", "pattern_rules", "applied_patterns":
		// §P2-d：同因子分支，直读条目以携带扫参审批的出场覆盖。
		pentries, perr := research.ListAppliedPatternRules(dataDir)
		if perr != nil {
			return nil, libScan{}, perr
		}
		pscan := libScan{Entries: len(pentries)}
		out := make([]adapter, 0, len(pentries))
		for i := range pentries {
			e := &pentries[i]
			if e.Enabled {
				pscan.Enabled++
			}
			if (!e.Enabled && !includeDisabled) || len(e.Conds) == 0 {
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
		pscan.Built = len(out)
		return out, pscan, nil
	}
	return nil, libScan{}, fmt.Errorf("未知战法库类型: %s", kind)
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
		// 候选直读**不受库门约束**（它回放的是一条未审批候选，与线上战法库有没有内容无关），
		// 但门状态照样在分支入口就落定——下面的"无条件集跳过"提前返回也要带着它出门，
		// 否则读者拿到的是一个门状态空白、无从判断与库的关系的读数。
		o.Library = LibraryLoad{Gate: "candidate_direct_exempt", Dir: o.DataDir, DirFrom: resolveDirFrom(o.DataDir)}
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
		fa, fsc, ferr := loadRuleAdapters("factor", o.DataDir, o.IncludeDisabled)
		if ferr != nil {
			return nil, false, ferr
		}
		pa, psc, perr := loadRuleAdapters("pattern", o.DataDir, o.IncludeDisabled)
		if perr != nil {
			return nil, false, perr
		}
		// §LIB-GATE：all 模式声明"跑库规则 + 内置五形态"，库侧读数必须先落进出门事实再过判红门。
		// 判据用库侧两档之和——**不是**下面拼完内置战法后的 len(ads)：旧代码在 append 之后
		// 才判 `len(ads)==0`，而 builtins 恒有 5 条，那个分支永远走不到，库空时既不进日志也不报错，
		// 这正是"静默按零条线上战法跑完整轮"能存在的结构原因。
		// English: the gate reads the library-side counts, not the post-builtin aggregate — the old
		// `len(ads)==0` check sat after five builtins were appended and could never fire.
		o.Library = LibraryLoad{
			Dir:      o.DataDir,
			EntriesF: fsc.Entries, EnabledF: fsc.Enabled, RulesF: len(fa),
			EntriesP: psc.Entries, EnabledP: psc.Enabled, RulesP: len(pa),
		}
		if gerr := o.libraryGate(&o.Library, true, true); gerr != nil {
			return nil, false, gerr
		}
		ads = append(fa, pa...)
		// 四大手写战法 + 动量一并纳入 all 回放（dragon/double_bump/dragon_return/n_shape/momentum）：
		// "几个形态战法不进回测"的另一含义——它们此前只能手动逐个跑。momentum 的判据已按实盘语义
		// 重写（2026-09-24），在 all 模式下是**真产出数字的第五个战法**：它作为兜底档，兄弟（含此处
		// 先装载的 fac_*/pat_* 库规则）当日出过信号就不入场，与实盘 sigs 的口径一致。
		// 枚举唯一出处 = BuiltinStrategies()（strategy-survey 排摸同一集合，不再各写一份）。
		// English: the built-in list has exactly one source of truth — BuiltinStrategies().
		builtins := BuiltinStrategies()
		needsInd(builtins...)
		for _, name := range builtins {
			ad, aerr := newAdapter(name, useIndustry, o.D1Score)
			if aerr != nil {
				return nil, false, aerr
			}
			ads = append(ads, ad)
		}
		log.Printf("all 回放：%d 条库规则（factor=%d pattern=%d）+ 五形态内置战法（含动量近似回放）",
			len(fa)+len(pa), len(fa), len(pa))
	} else if strings.EqualFold(o.Strategy, "factor") || strings.EqualFold(o.Strategy, "pattern") {
		wantP := strings.EqualFold(o.Strategy, "pattern")
		ra, sc, rerr := loadRuleAdapters(o.Strategy, o.DataDir, o.IncludeDisabled)
		if rerr != nil {
			return nil, false, rerr
		}
		// §LIB-GATE 单侧版：只声明一侧时，另一侧库文件存不存在与本轮回放无关（成因判定由
		// libraryGate 的 wantF/wantP 参数负责，不许被对侧文件缺失带红）。
		o.Library = LibraryLoad{Dir: o.DataDir}
		if wantP {
			o.Library.EntriesP, o.Library.EnabledP, o.Library.RulesP = sc.Entries, sc.Enabled, len(ra)
		} else {
			o.Library.EntriesF, o.Library.EnabledF, o.Library.RulesF = sc.Entries, sc.Enabled, len(ra)
		}
		if gerr := o.libraryGate(&o.Library, !wantP, wantP); gerr != nil {
			return nil, false, gerr
		}
		ads = ra
		log.Printf("战法库已加载 %d 条启用规则", len(ads))
	} else {
		needsInd(o.Strategy)
		ad, aerr := newAdapter(o.Strategy, useIndustry, o.D1Score)
		if aerr != nil {
			return nil, false, aerr
		}
		ads = []adapter{ad}
		// 单内置战法回放本来就不读库：门状态记 not_applicable，出门时读者一眼看到"这轮与库无关"。
		o.Library = LibraryLoad{Gate: "not_applicable", Dir: o.DataDir, DirFrom: resolveDirFrom(o.DataDir)}
	}
	return ads, useIndustry, nil
}

// BuiltinStrategies 内置形态战法枚举（**写了回放适配器**的五个）：buildAdapters 的 all 模式与
// strategy-survey 排摸共用这一份清单——排摸覆盖面必须与生产回测同源，不许两处各写各的。
// 2026-09-24 起五个都真跑数字：动量此前是"适配器在位但 Trigger 恒不触发"的排摸盲区，已按实盘
// 语义（兜底互斥 + 当日撮合 + 买入档才计交易）重写判据，残余近似由 ReplayApproxNote("momentum")
// 随数字一起出门。
// English: built-in strategies that HAVE a replay adapter — all five now produce numbers; momentum's
// criteria were rewritten to live semantics on 2026-09-24 (see momentumAdapter).
func BuiltinStrategies() []string {
	return []string{"double_bump", "dragon", "dragon_return", "momentum", "n_shape"}
}

// DefaultDisabledBuiltins 返回"适配器已实现、但按实盘语义默认不参与回放"的内置战法 ID。
// **当前为空**（momentum 于 2026-09-24 按实盘语义重写判据后回到默认可回放集合）。
// 机制必须保留而不是删掉：将来再有"实盘能下单、回放框架对不上"的战法，正确处置是登记进这份
// 清单并让 UnsurveyedLiveFormStatus 报成 "adapter_disabled_by_default"，而不是从 BuiltinStrategies
// 里删掉——删掉＝"没有适配器"，会把"写好了但按语义停用"和"根本没写"这两种运维处置完全不同的
// 状态压成同一个信号。
// English: built-ins whose adapters exist but do not replay by default. Currently empty — the
// mechanism stays so a future intraday-only strategy is reported as "implemented but disabled"
// rather than "not implemented".
func DefaultDisabledBuiltins() []string {
	return nil
}

// UnsurveyedLiveFormStatus 返回"实盘白名单战法未被排摸"的原因状态（ASCII，供锚点行与产物 notes 用）：
//   - "no_replay_adapter"                 适配器根本不存在（缺代码，要补实现）
//   - "adapter_disabled_by_default"       适配器已实现但默认停用（缺的是可重放的实盘语义，不是代码）
//   - ""                                  该战法默认就被排摸覆盖（不该出现在差集里）
//
// 两句话的区别很重要：前者要写代码，后者要么按实盘兜底语义重写判据、要么就承认量不了——
// 混成一个计数会把"已经尽力近似"的战法读成"还没人管"。
// English: why a live form strategy is not surveyed — missing adapter vs. implemented-but-disabled.
func UnsurveyedLiveFormStatus(id string) string {
	isBuiltin := false
	for _, b := range BuiltinStrategies() {
		if b == id {
			isBuiltin = true
			break
		}
	}
	if !isBuiltin {
		return "no_replay_adapter"
	}
	for _, b := range DefaultDisabledBuiltins() {
		if b == id {
			return "adapter_disabled_by_default"
		}
	}
	return ""
}

// LiveFormStrategies 实盘白名单里的**形态战法全集**（内置四形态 + 动量）。
// 与 internal/server/qmt.go 的 knownStrategyList 同源——那边是带中文显示名的 UI 视图，
// 这里是"可交易 ID"的机器口径；两边一致性由 internal/server 的等值测试钉住
// （server 测试引用本包，生产依赖方向不变）。
// **两份清单各自保留**：前者是"能不能实盘下单"，后者是"有没有回放适配器"，
// 谁先动都会由下面的差集锚点行报出来。
// English: the canonical set of form-strategy IDs that may trade live (four builtins + momentum).
// Kept in step with server's knownStrategyList by an equality test in internal/server.
func LiveFormStrategies() []string {
	return []string{"double_bump", "dragon", "dragon_return", "n_shape", "momentum"}
}

// UnsurveyedLiveForms 返回「实盘白名单里有、但 btreplay 默认不回放因而量不到」的形态战法 ID：
// 覆盖集 = BuiltinStrategies 扣掉 DefaultDisabledBuiltins。
// 这些战法在排摸表里**拿不出可比的数字**——不显式计数的话，"没查到"会被读成"没问题"，
// 正是本轮 §ADJ 口径漂移最想要人看见的那类盲区。2026-09-24 动量判据按实盘语义重写后本函数
// 返回空集（**这不是可以删掉它的理由**）：下一个进实盘白名单却没有回放适配器的战法、或适配器
// 又因语义对不上被停用的战法，仍必须由它显形，本函数与 survey 侧的差集锚点行不得删除。
// English: live form strategies that btreplay does not replay by default (missing adapter OR
// adapter disabled) — surfaced as a non-zero survey anchor so "not measured" can never be read
// as "not a problem"; each id carries its own status via UnsurveyedLiveFormStatus.
func UnsurveyedLiveForms() []string {
	covered := map[string]bool{}
	for _, id := range BuiltinStrategies() {
		covered[id] = true
	}
	for _, id := range DefaultDisabledBuiltins() {
		delete(covered, id)
	}
	var out []string
	for _, id := range LiveFormStrategies() {
		if !covered[id] {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// adapterID 返回 adapter 的稳定英文 ID：库规则取 ruleID（fac_*/pat_*），
// 内置战法按类型映射（double_bump/dragon/dragon_return/n_shape）。
// English: stable ASCII ID per adapter (rule ID for library entries, registry name for builtins).
func adapterID(ad adapter) string {
	if kp, ok := ad.(kindProvider); ok {
		if k := kp.Kind(); k != "" {
			return k
		}
	}
	switch ad.(type) {
	case *doubleBumpAdapter:
		return "double_bump"
	case *dragonAdapter:
		return "dragon"
	case *dragonReturnAdapter:
		return "dragon_return"
	case *nShapeAdapter:
		return "n_shape"
	case *momentumAdapter:
		return "momentum"
	}
	return ""
}

// BuiltinDisplayName 返回内置战法的显示名（"动量"/"双响炮"…），取处就是适配器自己的 Name()——
// 同一字面，不存在第二份名字清单。
// 为什么单独导出：排摸记录行的名称不能依赖"这一轮回放跑出了交易"（区间内 0 笔的战法很多，
// 名字若从交易行反查就会是空串，一行没有名字的记录会被读成坏数据而不是"这轮没机会"）。
// English: display name straight from the adapter (the single source of the label), so a strategy
// that replays zero trades still gets a correctly named row.
func BuiltinDisplayName(id string) string {
	ad, err := newAdapter(id, false, 0)
	if err != nil {
		return ""
	}
	return ad.Name()
}

// approxNote 出门文本 = 静态近似说明 + 本轮**实测**的分钟口径覆盖率 + 本轮**实测**的兜底互斥兄弟集。
// 为什么必须拼实测值而不是把那句话改写成"已经用真 5 分钟了"：同一句声明在"回填跑完"和
// "库是空的"两种运行里长得不一样，但报告上看起来一样——本仓反复出事的"降级报成功"形态。
// §LIB-GATE 追加兄弟集尺寸：动量是兜底档，兄弟集（同批非兜底档战法，含库规则 fac_*/pat_*）当日
// 出过信号它就不入场，所以**兄弟越多动量越少**。2026-09-25 两次 all 回放的动量触发数差 2.1×，
// 根因就在这条判据吃到的战法集合不同（0 条库规则 vs 3 条）。数字出门口径必须自带"当时有几条兄弟"，
// 否则两张表的动量行会被当成同一件事。覆盖率读数来自 storeMinuteMACD 的观测计数（不参与判红）。
func (o *Options) approxNote(id string) string {
	note := ReplayApproxNote(id)
	if id != "momentum" || note == "" {
		return note
	}
	// 兄弟集实测尺寸（运行期装配，非配置项）：库规则条数 + 内置非兜底档数一起写，读者能反推口径。
	note += fmt.Sprintf("; sibling set actually loaded this run: %d non-fallback adapter(s) = library {factor=%d pattern=%d} + builtins (%s gate=%s)",
		len(o.fallbackPeers), o.Library.RulesF, o.Library.RulesP, o.Library.Dir, o.Library.Gate)
	if o.minuteSrc == nil {
		return note + "; minute-basis coverage: NONE (research DB has no minute_klines rows -> every judgement day used the daily MACD approximation above)"
	}
	looks, hits, queries, pct := o.minuteSrc.coverageStats()
	return fmt.Sprintf("%s; minute-basis coverage: %d/%d judgement days used real 5-minute MACD (%.1f%%, %d DB queries), the rest fell back to the daily approximation above",
		note, hits, looks, pct, queries)
}

// ReplayApproxNote 返回某内置战法回放适配器的**近似口径说明**（空串＝无近似/非内置）。
// 用途：排摸/回测读者必须能看见"这一行是近似量出来的"——纯日K完整回放与靠日内快照的
// 近似回放混在一张表里不加标注，读出来同名的数字其实不是一回事。
// survey 产物侧的挂法是把本说明写进 records[].notes（cmd/research/survey.go 内置战法分支），
// 生产回测报告侧由 printReport 直接打在战法名下。
// English: per-strategy approximation note so a survey reader can tell a full-daily replay apart
// from an approximated one (momentum/n_shape/dragon/dragon_return are not exact).
func ReplayApproxNote(id string) string {
	switch id {
	case "momentum":
		// 动量数字出门时必须带着这四句：判据已按实盘语义重写（兜底互斥/当日收盘撮合/买入档），
		// 剩下量不了的只有"日K粒度看不到盘中"这一件事。读者若把这一行当成"5 分钟动量的精确回放"
		// 就会高估它的可比性。
		return "approx replay (criteria rewritten to live semantics 2026-09-24): fallback exclusivity honored (blocked whenever a sibling strategy signaled the same stock the same day, mirroring agent.go's `len(sigs)==0`) and entry at the SIGNAL DAY CLOSE (live matches the momentum pool the same tick), trade only at the live BUY threshold; still approximated -- 5-minute MACD replaced by daily MACD on the days the research DB has no minute bars (see the coverage figure appended at runtime), one judgement per day instead of N intraday rounds (can only miss triggers, not invent them), and the intraday round-to-round momentum-improvement gate is not reproducible (peers are probed with raw triggers, so the blocked-day set is larger than live's -> momentum entries skew LOW)"
	case "n_shape":
		return "approx replay: intraday snapshot approximated from daily bars; D1 injected via -d1 rule score (no LLM/event context)"
	case "dragon":
		return "approx replay: sector resonance (F2/F3) approximated by the stock's industry change; degraded when industry data is missing"
	case "dragon_return":
		return "approx replay: sector leadership (IsSectorTop2/SectorRPS20) relaxed by the -industry switch"
	case "double_bump":
		return "" // 纯日K完整回放，与实盘同源，无近似口径
	}
	return ""
}

// ReplayStat 单策略回放结果（strategy-survey 的对外契约）。字段与内部 summary 同源，
// 刻意**不含**夏普/年化/卡玛——逐笔采样口径下这些风险调整指标无意义（排摸输出曾因此
// 出现"年化 -1e14%"级别的噪声列）。
// English: per-strategy replay stats for the survey; intentionally excludes Sharpe/annual/Calmar,
// which are meaningless under the sampled-trade basis.
type ReplayStat struct {
	ID            string  // 稳定 ASCII ID（内置名 / fac_* / pat_*）
	Name          string  // 显示名（中文，仅供人看）
	Approx        string  // 近似回放说明（空＝完整回放；见 ReplayApproxNote）
	Signals       int     // 触发信号数
	Win           int     // 盈利笔数
	Loss          int     // 亏损笔数
	WinRate       float64 // 胜率%
	AvgWinPct     float64 // 平均盈利%
	AvgLossPct    float64 // 平均亏损%
	ProfitFactor  float64 // 盈亏比
	ExpectancyPct float64 // 期望收益%（每笔）
	AvgHoldDays   float64 // 平均持仓天数
}

// RunCollect 执行回放但以结构化结果返回（不打印汇总报告）——strategy-survey 专用。
// 与 Run 共用 collect() 同一执行路径：排摸数字与生产回测逐字同源，杜绝第二套口径。
// English: runs the same pipeline as Run but returns structured per-adapter stats.
func (o *Options) RunCollect() ([]ReplayStat, error) {
	sums, ids, _, err := o.collect()
	if err != nil {
		return nil, err
	}
	out := make([]ReplayStat, 0, len(sums))
	for i, s := range sums {
		id := ""
		if i < len(ids) {
			id = ids[i]
		}
		out = append(out, ReplayStat{
			ID: id, Name: s.Name, Approx: s.Approx, Signals: s.Count, Win: s.Win, Loss: s.Loss,
			WinRate: s.WinRate, AvgWinPct: s.AvgWinPct, AvgLossPct: s.AvgLossPct,
			ProfitFactor: s.ProfitFactor, ExpectancyPct: s.Expectancy, AvgHoldDays: s.AvgHold,
		})
	}
	return out, nil
}

// Run 执行回放回测主流程（汇总报告打印到 stdout，供 worker 解析 result_text）。
// §LIB-GATE：战法库读数打在报告**第一行**——引用数字的人读的是这份出门文本，不是 stderr 日志；
// 前提不进 stdout 就等于前提没出门（2026-09-25 那次误用的正是只写在日志首行的一句话）。
func (o *Options) Run() error {
	summaries, _, stockCount, err := o.collect()
	if err != nil {
		return err
	}
	fmt.Printf("%s\n", o.Library.String())
	// §B4-PIT：池口径读数与库读数同出口出门——"这组数字跑在什么股票池上"是引用数字的人
	// 必须先看到的前提（缺它=幸存者偏差静默在体）。
	if o.poolNote != "" {
		fmt.Printf("%s\n", o.poolNote)
	}
	printReports(summaries, stockCount)
	return nil
}

// collect 回放主流程本体（不含打印）：返回按 adapter 分组的 summary、对应的稳定 ID 列表
// 与股票池规模。Run 与 RunCollect 都走这里（见 RunCollect 注释：单一口径）。
func (o *Options) collect() ([]*summary, []string, int, error) {
	db := o.DB
	if db == nil {
		var err error
		db, err = store.Open(o.DBPath)
		if err != nil {
			return nil, nil, 0, fmt.Errorf("打开数据库: %w", err)
		}
		defer db.Close()
	}

	// §质控筛选：Screen 非空时用质控池（剔 ST/退市/多年亏损/地量股）替代全量 StockCodes()，
	// 再叠加 MaxStocks 截断——全量回测不再是 maxstocks=300 的字母序傻截。
	// §B4-PIT（owner 裁决 2026-09-26「幸存者偏差开关：开，默认打开」）：时点股票池缺省生效，
	// 池=回测起始日"已上市且未退市"（store.UniverseAt(Start)，含退市样本消除幸存者偏差）。
	// 与质控叠加时两轴正交：退市时点统一交给时点池裁决（质控腿的 ExcludeDelist 置假——它按
	// "今天还退市与否"一刀切，正是幸存者偏差的来源；Start 之后才退市的票必须在池内）。
	// 降级可见：元数据覆盖为 0（dataload §B4-META 未回填）或 Start 为空时建不出时点池，
	// 不静默按旧口径跑——poolNote 抬出"本轮非时点口径（幸存者偏差在体）"读数，随报告首行出门
	// （与 minuteCoverageNote 同姿势：前提与数字同屏）。
	// English: §B4-PIT — point-in-time universe is the default; screen and PIT compose orthogonally
	// (delisting timing owned by PIT, quality by screen); unbuildable PIT degrades LOUDLY via the
	// report's first-line pool-basis note, never silently.
	var codes []string
	var err error
	pitOn := o.PITEnabled()
	var pitList []string
	var pitSet map[string]bool
	switch {
	case pitOn && len(o.Codes) > 0:
		// 显式池（strategy-survey 面板清单）：时点裁决归清单作者，本处只把读数抬出来不越权代办。
		o.poolNote = fmt.Sprintf("池口径=显式清单 %d 只（§B4-PIT 不接管——清单若不是时点名单，幸存者偏差风险随清单出门）", len(o.Codes))
		log.Printf("回放 %s", o.poolNote)
		codes = o.Codes
	case pitOn:
		stats, serr := db.StockMetaStats()
		if serr != nil {
			return nil, nil, 0, fmt.Errorf("§B4-PIT 覆盖率探针: %w", serr)
		}
		switch {
		case o.Start == "":
			pitOn = false
			o.poolNote = "池口径=非时点（§B4-PIT 降级：回放起始日为空，时点池无从建起）——幸存者偏差在体"
		case stats.WithList == 0:
			pitOn = false
			o.poolNote = fmt.Sprintf("池口径=非时点（§B4-PIT 降级：stocks.list_date 覆盖 0/%d，未跑 dataload meta-dates 回填）——幸存者偏差在体", stats.Total)
		default:
			pitList, err = db.UniverseAt(o.Start)
			if err != nil {
				return nil, nil, 0, fmt.Errorf("时点股票池(%s): %w", o.Start, err)
			}
			pitSet = make(map[string]bool, len(pitList))
			for _, ts := range pitList {
				pitSet[ts] = true
			}
			o.poolNote = fmt.Sprintf("池口径=时点（§B4-PIT）：截至 %s 在市 %d 只；元数据 list_date 覆盖 %d/%d、退市样本 %d",
				o.Start, len(pitList), stats.WithList, stats.Total, stats.WithDelist)
		}
		if o.poolNote != "" {
			log.Printf("回放 %s", o.poolNote)
		}
		switch {
		case pitOn && o.Screen != nil:
			sc := *o.Screen
			if sc.End == "" && o.End != "" {
				sc.End = o.End // 质控窗口结束日对齐回测区间，避免用"今天"跨出回测区间
			}
			sc.ExcludeDelist = false // 退市时点由时点池裁决（见上：按今天一刀切正是偏差来源）
			codes, err = db.ScreenedCodes(sc)
			if err == nil {
				before := len(codes)
				kept := make([]string, 0, before)
				for _, ts := range codes {
					if pitSet[ts] {
						kept = append(kept, ts)
					}
				}
				codes = kept
				log.Printf("回放股票池：质控 %d → ∩时点池 %d 只（退市按 %s 时点裁决）", before, len(codes), o.Start)
			}
		case pitOn:
			codes = pitList
		case o.Screen != nil:
			sc := *o.Screen
			if sc.End == "" && o.End != "" {
				sc.End = o.End
			}
			codes, err = db.ScreenedCodes(sc)
		default:
			codes, err = db.StockCodes()
		}
	case len(o.Codes) > 0:
		// 显式池（strategy-survey）：与调用方研究面板用同一份清单。
		codes = o.Codes
	case o.Screen != nil:
		sc := *o.Screen
		if sc.End == "" && o.End != "" {
			sc.End = o.End // 质控窗口结束日对齐回测区间，避免用"今天"跨出回测区间
		}
		codes, err = db.ScreenedCodes(sc)
	default:
		o.poolNote = "池口径=非时点（§B4-PIT 显式关闭 PointInTime=false）——按今天在市名单回测，幸存者偏差在体"
		log.Printf("回放 %s", o.poolNote)
		codes, err = db.StockCodes()
	}
	if err != nil {
		return nil, nil, 0, err
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
		return nil, nil, 0, berr
	}
	// §LIB-GATE：库读数随日志出门（Run 另外把它打进 stdout 报告首行，survey 打进产物 JSON 的
	// library 字段——三条出口共用同一个 String()，不各处重写一句话）。
	log.Printf("§LIB-GATE %s", o.Library.String())
	// 兜底档战法（动量）在场时装配跨战法互斥清单——没有兜底档时保持 nil，
	// 其余战法的回放路径一次判断都不会多走。
	if hasFallbackTier(ads) {
		o.setFallbackPeers(ads)
		// 只单独跑动量（-strategy momentum）时清单为空：兜底互斥这道门在实盘是"同标的当日
		// 四形态均未出信号"，没有兄弟适配器可回查就等于没有这道门，量出的会是一批实盘轮不到
		// 下单的单子。数字仍然出门（比不跑有用），但必须在日志里显式声明口径与 all 不可比。
		if len(o.fallbackPeers) == 0 {
			log.Printf("⚠️ 兜底档战法单独回放：本批没有非兜底档兄弟可回查，兜底互斥门未生效（-strategy all 才有）——动量入场数会偏高，与 all/排摸口径不可比")
		}
	}

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

	// §MINUTE-K 动量 MACD 口径升级的装配点（一次）：研究库有分钟落库就把真 5 分钟来源接上，
	// 没有（旧库没这张表 / 表是空的）就保持 nil —— 判档整体退回"已声明的日线近似"，
	// 数字与升级前逐字节一致。这里刻意不把"表存在但零行"当成错误：那正是回填还没跑的态。
	if st, err := db.MinuteTableStats(minuteMACDScale); err == nil && st.Rows > 0 {
		o.minuteSrc = newStoreMinuteMACD(db, minuteMACDScale, codes)
		log.Printf("§MINUTE-K 动量 MACD 用真 5 分钟口径：minute_klines %d 行 / %d 只（%s ~ %s，平均 %.1f 根每票每日）",
			st.Rows, st.Codes, st.FirstTs, st.LastTs, st.AvgBars)
	}

	// §B2 财务输入装配点（一次）：因子规则回放逐股喂"判定日可见"的财报（owner 裁决 2026-09-26
	// 「因子回放要不要喂财务数据：要」）。库里一行业绩没有也照样装配——那是"回填还没跑的态"
	// （与 §MINUTE-K 同一姿势），裸奔规模由收尾的 §B2-FINA 读数行点名，不判红也不静默。
	// English: §B2 — attach the per-day point-in-time financial source once per collect; an empty
	// fina_indicator is a legitimate not-yet-loaded state surfaced via the readout line, never silent.
	o.finaSrc = newFinaProvider(db)

	// §P2 参数扫参模式：触发一次性预计算 + 逐组合廉价模拟统一出场（见 sweep.go）。
	// English: sweep mode — pre-compute triggers once, then cheaply simulate each param combo.
	if o.Sweep != nil {
		return nil, nil, len(codes), o.runSweep(db, codes, ads, industryChg)
	}

	// 逐 adapter 回放（多规则时按规则分组统计；单战法仅一条）。
	// 进度输出：每 10% 打一行"回测进度 x%"（§8.6-A 同协议），队列 worker 解析回写，
	// 否则整轮回放只有结尾汇总、进度条全程空窗。
	// English: emit "回测进度 x%" every 10% of the stock loop so the queue worker can feed the bar.
	summaries := make([]*summary, 0, len(ads))
	ids := make([]string, 0, len(ads)) // 与 summaries 平行：strategy-survey 定位每条结果归属
	amountFixed := 0                   // Risk-1 千元口径归一的股票计数（收尾日志）
	for _, ad := range ads {
		// §回测自动增强：每战法装配一次动态滑点上下文（含 paper_trades 校准合并；
		// 增强关闭 = nil = 全部旧行为）。
		kind := ""
		if kp, ok := ad.(kindProvider); ok {
			kind = kp.Kind()
		}
		o.slip, _ = o.buildSlipCtx(db, ad.Name(), kind)
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
			// §MINUTE-K 逐股注入分钟口径来源（动量适配器认它，其它适配器跳过）。
			o.applyMinuteScope(ad, tsCode)
			// §B2 财务输入逐股注入（含兜底兄弟，见 applyFinaScope 的 B6 教训注释）。
			o.applyFinaScope(ad, tsCode)
			// §Risk-1 单位自校：tushare 口径库 amount=千元，均价带判定后归一（仅增强模式）
			if o.slip != nil && fixAmountScale(klines) {
				amountFixed++
			}
			trades = append(trades, o.backtestStock(code, klines, ad, industryChg[code])...)
			// §节流：每处理完一只股票 sleep 指定毫秒，把全量回放对服务器的瞬时 CPU/内存
			// 挤压摊平到盘后十几个小时——2 核 4G 机器上全池回放曾把可用内存打到熔断线。
			// English: throttle per stock to flatten instantaneous CPU/mem pressure of a
			// full-universe replay over the long post-close window on small boxes.
			if o.ThrottleMs > 0 {
				time.Sleep(time.Duration(o.ThrottleMs) * time.Millisecond)
			}
		}
		sm := summarize(trades, o.RiskFreeRate)
		if sm.Name == "" {
			// 零触发时 summarize 拿不到交易行，名字会空——报告头变成"战法历史回测: （N 只股票）"。
			// English: zero-trigger adapters have no trade row to carry the name; backfill it.
			sm.Name = ad.Name()
		}
		// 近似口径随结果一起带出：报告正文（printReport）与 survey 侧（ReplayStat.Approx）
		// 都能看到"这一行不是精确回放"，不会只剩代码注释里才知道。
		adID := adapterID(ad)
		sm.Approx = o.approxNote(adID)
		summaries = append(summaries, sm)
		ids = append(ids, adID)
	}
	if amountFixed > 0 {
		log.Printf("Risk-1 单位自校：%d 只股票 amount 按千元口径归一（×1000）", amountFixed)
	}
	// §MINUTE-K 收尾读数：队列 worker 抓的就是这里的日志行，报告之外也要看得见"这一轮
	// 动量到底用了多少真分钟"（只回显、不参与判红）。
	log.Printf("%s", o.minuteCoverageNote())
	// §B2-FINA 收尾读数：这一轮因子判定到底有多少票吃到了"判定日可见"的财报（只回显、不判红）。
	log.Printf("%s", o.finaSrc.String())
	return summaries, ids, len(codes), nil
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

// backtestStock 对单只股票回放指定战法：逐日判定触发，缺省在**次日开盘**入场并逐日模拟平仓；
// 声明 SameDayEntry 的战法（实盘当日撮合的动量）在**触发当日收盘**入场（入场时点见下面的 entryIdx）。
// o.slip 非空 = 回测增强模式：入场一字板判定升级为 封死跳过/打开加滑点，
// 滑点按流动性/名义额逐笔定档，出场 walk 集成跌停封死不可卖与部分成交（模块 A/B）。
// English: per-stock replay. When slip context is set, entry gating upgrades to sealed/openable
// distinction, slippage is resolved per trade, and the exit walk honors limit-down sealing + partial fills.
func (o *Options) backtestStock(code string, klines []data.KLine, ad adapter, industryChgByDate map[string]float64) []trade {
	var trades []trade
	// §RFIX-1 有状态适配器的取数装配（见 dayScoped）：MACD 序列必须**逐股**重算。旧实现
	// `macdSeries == nil` 守卫使序列只按池内第一只股票计算一次——后续股票全部跨股污染 D4 资金
	// 确认（结果失真），且更长序列股票的 curIdx 直接越界 panic（生产 09-18/19 实录）。
	// 装配从"这里手写一个 n_shape 分支"收敛到 applyStockScope/applyDayScope 两个装配点，
	// 兜底互斥回查兄弟时共用同一套（否则兄弟拿不到自己那只股票的序列）。
	applyStockScope(ad, klines)
	// 兜底档战法（动量）要回查兄弟：兄弟的逐股预计算同样要装配一次，否则跨股污染兄弟的判据。
	needPeers := isFallbackTier(ad) && len(o.fallbackPeers) > 0
	if needPeers {
		for _, p := range o.fallbackPeers {
			applyStockScope(p, klines)
		}
	}
	// 从第 30 根起才有足够前视窗（MA/主升段）
	for i := 29; i < len(klines)-1; i++ {
		applyDayScope(ad, i)
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
		// 兜底互斥门控（实盘 agent.go 的 `len(sigs) == 0 &&`）：同标的当日只要有任何一个兄弟战法
		// 出了信号，实盘就轮不到动量下单——**在动量自己达档的那一刻**才回查（短路顺序与实盘一致，
		// 也让回查开销只落在候选笔数上而不是每个交易日）。
		if needPeers && o.fallbackBlockedByPeer(klines, i, prevClose, industryChg) {
			continue
		}
		// 入场时点：缺省=次日开盘；声明 SameDayEntry 的战法（实盘当日撮合）=触发当日收盘。
		entryIdx, entry := i+1, klines[i+1].Open
		if sd, ok2 := ad.(sameDayEntryAdapter); ok2 && sd.SameDayEntry() {
			entryIdx, entry = i, klines[i].Close
		}
		if entry <= 0 {
			continue
		}
		// 入场滑点/成交比例定档：旧路径 = 固定 5bp + 开盘一字板不可成交（§GAP4.2）；
		// 增强路径 = 动态滑点 + 涨停打开可成交加罚分（一字封死仍不可成交）+ 部分成交比例。
		buySlip, sellSlip, fill := costSlippageBps, costSlippageBps, 1.0
		if o.slip != nil {
			var can bool
			buySlip, sellSlip, fill, can = o.slip.entrySlipAt(code, klines, i, entryIdx)
			if !can {
				continue
			}
		} else if entryIdx == i+1 && costOpenAtLimitUp(code, klines[i].Close, entry) {
			// §GAP4.2 开盘即封板不可成交：一字板/秒板买单现实中排队无望，跳过该笔
			// （打板类战法此前默认必成交，产生系统性乐观偏差）。
			// 当日收盘入场的战法走下面那条同义判定（收盘贴涨停＝买不到）。
			continue
		} else if entryIdx == i && i > 0 && costOpenAtLimitUp(code, prevClose, entry) {
			continue
		}
		// 逐日平仓模拟：从入场次日（entryIdx+1）起跑 CheckExit
		t := o.simulateExit(code, klines, entryIdx, entry, meta, ad, buySlip, sellSlip, fill)
		if t != nil {
			trades = append(trades, *t)
			// 入场后跳到该笔交易结束（平仓日）之后，避免同一标的在同一时段重复入场
			i = entryIdx + t.HoldDays
		}
	}
	return trades
}

// isFallbackTier 该适配器是否声明"实盘兜底档"。
func isFallbackTier(ad adapter) bool {
	fb, ok := ad.(fallbackTierAdapter)
	return ok && fb.FallbackTier()
}

// fallbackBlockedByPeer 兜底互斥回查：同标的当日（索引 i）是否已被任一兄弟战法出信号占掉。
// 用兄弟的**裸 Trigger**（不带入场可成交性判定），因为实盘的 `len(sigs) == 0` 数的是信号、
// 不是成交——兄弟那天出了信号但回放里买不进（一字板），实盘同样不会轮到动量。
// English: the live fallback branch counts sibling *signals* (not fills), so peers are probed with
// their raw trigger at the same day index.
func (o *Options) fallbackBlockedByPeer(klines []data.KLine, i int, prevClose, industryChg float64) bool {
	for _, p := range o.fallbackPeers {
		applyDayScope(p, i)
		if _, fired := p.Trigger(klines[:i+1], prevClose, industryChg); fired {
			return true
		}
	}
	return false
}

// simulateExit 从入场日 index 起逐日跑 CheckExit，返回平仓结果；到序列末尾仍未平仓则按末日收盘强制结算。
// buySlip/sellSlip/fill 为该笔入场日定档的动态滑点（bp）与成交比例（旧路径恒 5/5/1）。
// 跌停封死门控（模块 B，o.slip 开启时）：封死日不可卖出、跳过出场判定；打开日以当日
// 收盘卖出并追加 LimitDownSealedExtraBps 滑点。部分成交口径：pnl × fillRate
// （未成交部分留现金属零收益，B.5-5 近似）。
// English: daily exit walk with per-trade dynamic slippage; honors limit-down sealing
// (no sell while sealed, extra slippage on the opening day) and partial fills.
func (o *Options) simulateExit(code string, klines []data.KLine, entryIdx int, entry float64, meta map[string]float64,
	ad adapter, buySlip, sellSlip, fill float64) *trade {
	sealedExtra := o.slip.sellSealedExtra() // 0 = 跌停封死门控未启用
	wasSealed := false                      // 上一日是否封死（打开日加罚卖出滑点）
	for j := entryIdx + 1; j < len(klines); j++ {
		cur := klines[j].Close
		if cur <= 0 {
			continue
		}
		// 跌停封死判定：封死日不可卖出（强制持有），记录状态待打开日加罚
		if sealedExtra > 0 && costLimitDownSealedDay(code, klines[j-1].Close, klines[j].Low, cur) {
			wasSealed = true
			continue
		}
		slipSell := sellSlip
		if wasSealed {
			slipSell += sealedExtra
			wasSealed = false
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
				// §GAP4.1 净额口径：双边滑点+双边佣金+卖出印花税一次性计入收益率；
				// 增强路径下滑点逐笔定档、盈亏按成交比例折算（近似，B.5-5）。
				PnlPct: costRoundTripPnlEx(entry, cur, buySlip, slipSell) * fill, Reason: res.Reason,
			}
		}
	}
	// 未平仓：按末日收盘强制结算（即使末日仍封死——文档口径：持有到末日按末日收盘结算）
	last := klines[len(klines)-1].Close
	if last <= 0 {
		return nil
	}
	return &trade{
		Strategy: ad.Name(), Code: code, Date: klines[entryIdx].Date.Format("20060102"),
		HoldDays: len(klines) - 1 - entryIdx, Entry: entry, Exit: last,
		PnlPct: costRoundTripPnlEx(entry, last, buySlip, sellSlip+sealedExtra*boolF(wasSealed)) * fill,
		Reason: "区间结束强制结算",
	}
}

// boolF 布尔转 0/1 系数（封死状态参与加罚计算的可读写法）。
func boolF(b bool) float64 {
	if b {
		return 1
	}
	return 0
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
	// Approx 近似回放口径说明（空=纯日K完整回放）；由 collect() 按 adapterID 查 ReplayApproxNote 填，
	// printReport 会把它打在战法名下——近似数字必须带着"我是近似"的标签出门。
	Approx string

	// §GAP4.5 风险调整指标（此前全系统零实现）
	Sharpe          float64 `json:"sharpe"`            // 年化夏普（逐笔净额收益）
	MaxDrawdownPct  float64 `json:"max_drawdown_pct"`  // 复利净值最大回撤%（正数）
	AnnualReturnPct float64 `json:"annual_return_pct"` // 复利年化收益%
	Calmar          float64 `json:"calmar"`            // 卡玛 = |年化/MDD|
}

// summarize 汇总所有交易的胜率/盈亏指标。
func summarize(trades []trade, rf float64) *summary {
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
	s.Sharpe, s.MaxDrawdownPct, s.AnnualReturnPct, s.Calmar = perfMetricsRF(pnls, dates, rf)
	return s
}

// printReport 打印回测汇总报告。
func printReport(s *summary, name string, stockCount int) {
	fmt.Println("==============================================")
	fmt.Printf("战法历史回测: %s（%d 只股票）\n", name, stockCount)
	// 近似口径先于数字出场：读报告的人（含 worker 的 result_text 消费端）第一眼就知道
	// 下面的胜率是"日K 近似回放"量出来的，而不是精确回放。
	if s.Approx != "" {
		fmt.Printf("近似口径: %s\n", s.Approx)
	}
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
