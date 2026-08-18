// Package combat_agent 战法适配层：把引擎传入的 *strategy_engine.StockMarketData
// 转换成各战法真实评分接口需要的结构化输入，让 8a/8b 战法在标准扫描路径上真正产出信号。
// English: strategy adapter layer — converts the engine's *strategy_engine.StockMarketData into the
// structured inputs each strategy's real scoring interface needs, so the 8a/8b strategies produce real
// signals on the standard scan path.
package combat_agent

import (
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/indicator"
	"quant-trading-v2/internal/sector_agent"
	"quant-trading-v2/internal/strategies/double_bump"
	"quant-trading-v2/internal/strategies/dragon"
	"quant-trading-v2/internal/strategies/dragon_return"
	"quant-trading-v2/internal/strategies/n_shape"
	"quant-trading-v2/internal/strategy"
	"quant-trading-v2/internal/strategy_engine"
)

// stockInfoFromMarketData 从 StockMarketData 构造战法需要的个股实时信息。
// 成交量/成交额取自日K最后一根（近似当日量能）。
// 基础字段（代码/名称/现价/涨跌幅）直接取自引擎行情；OHLC/量额回填最后一根日K。
// English: builds the realtime StockInfo a strategy needs from StockMarketData. Base fields come from
// engine quotes; OHLC and volume/amount are back-filled from the last daily K-line as an intraday proxy.
func stockInfoFromMarketData(md *strategy_engine.StockMarketData) *data.StockInfo {
	si := &data.StockInfo{
		Code:      md.Code,
		Name:      md.Name,
		Price:     md.Price,
		ChangePct: md.ChangePct,
	}
	if n := len(md.KLines); n > 0 {
		// 以最后一根日K作为当日盘口近似数据
		last := md.KLines[n-1]
		si.Open = last.Open
		si.High = last.High
		si.Low = last.Low
		si.Close = last.Close
		si.Volume = last.Volume
		si.Amount = last.Amount
	}
	return si
}

// ma 计算最近 n 根K线的收盘均线（委托指标库 indicator.SMA，末尾值）。
// K线数量不足 n 根时按实际数量回退计算（SMA 预热期为 NaN，此处回退简单平均）；n<=0 或空列表返回 0。
// English: last simple moving average of the n K-line closes, delegating to indicator.SMA. Falls back to
// the available-count average when K-lines are fewer than n (SMA warm-up is NaN), and returns 0 for
// n<=0 or an empty list.
func ma(kl []data.KLine, n int) float64 {
	if len(kl) == 0 || n <= 0 {
		return 0
	}
	closes := make([]float64, len(kl))
	for i, k := range kl {
		closes[i] = k.Close
	}
	smas := indicator.SMA(closes, n)
	if v := smas[len(smas)-1]; v == v { // 非 NaN：末值即最近 n 根均值
		return v
	}
	// 预热期不足：按实际数量简单平均回退（与原 ma 语义一致）
	s := 0.0
	for _, c := range closes {
		s += c
	}
	return s / float64(len(closes))
}

// avgVol 计算最近 n 根K线的平均成交量。
// 逻辑与 ma 一致，仅统计维度为成交量；K线不足或空列表时返回 0。
// English: average volume of the last n K-lines, same semantics as ma but on Volume; returns 0 when
// insufficient or empty.
func avgVol(kl []data.KLine, n int) float64 {
	if len(kl) < n {
		n = len(kl)
	}
	if n <= 0 {
		return 0
	}
	s := 0.0
	// 取倒数 n 根K线的成交量累加求平均
	for _, k := range kl[len(kl)-n:] {
		s += k.Volume
	}
	return s / float64(n)
}

// dragonReturnDataFromMarketData 从日K线派生龙回头战法的输入：
// 首轮涨幅(近40日主升高点)、回调幅度/天数、缩量比、均线；板块龙性由已验证板块填充。
// K线不足30根时返回零值结构（龙性硬性条件不满足 → 0分，安全降级）。
// English: derives the dragon-return strategy input from daily K-lines — first-leg rise (40-day peak),
// pullback depth/days, volume-shrink ratio, MAs; sector leadership comes from the verified sector.
// Returns the zero-valued struct (safe 0-score degrade) when fewer than 30 K-lines are available.
func dragonReturnDataFromMarketData(code string, md *strategy_engine.StockMarketData, sector *sector_agent.VerifiedSector) *dragon_return.StockData {
	sd := &dragon_return.StockData{
		Code:         code,
		Name:         md.Name,
		CurrentPrice: md.Price,
	}
	kl := md.KLines
	// 历史K线不足30根时缺少均线/主升段数据，返回零值安全降级（0分）
	// English: fewer than 30 K-lines → no MA/main-rally data, return zero struct as a safe 0-score degrade.
	if len(kl) < 30 {
		return sd
	}

	// 均线族：MA5/MA10/MA20
	// English: MA series: MA5/MA10/MA20.
	sd.MA5 = ma(kl, 5)
	sd.MA10 = ma(kl, 10)
	sd.MA20 = ma(kl, 20)

	// 近40根内最高收盘作为主升高点
	// English: lowest index in the last 40 bars is the window start for the main rally peak.
	start := len(kl) - 40
	if start < 0 {
		start = 0
	}
	// 线性扫描窗口内最高收盘价所在位置
	// English: linearly scan the window for the bar with the highest close.
	hiIdx := start
	for i := start; i < len(kl); i++ {
		if kl[i].Close > kl[hiIdx].Close {
			hiIdx = i
		}
	}
	sd.HighestPrice = kl[hiIdx].Close
	if sd.HighestPrice > 0 {
		// 首轮涨幅 = 主升高点相对窗口起点收盘的涨幅
		// English: first-leg rise = peak vs the window-start close.
		sd.FirstRisePct = (sd.HighestPrice - kl[start].Close) / kl[start].Close
		// 回调幅度 = 主升高点相对现价的回撤
		// English: pullback depth = retracement of the peak against the current price.
		sd.PullbackPct = (sd.HighestPrice - md.Price) / sd.HighestPrice
	}
	// 回调天数 = 主升高点之后的K线根数，并做下界保护
	// English: pullback days = bars after the peak index, with a lower-bound guard.
	sd.PullbackDays = len(kl) - 1 - hiIdx
	if sd.PullbackDays < 0 {
		sd.PullbackDays = 0
	}

	// 缩量比近似：近5日均量 / 20日均量（回调缩量 vs 主升量能）
	// English: volume-shrink ratio ≈ 5-day avg volume / 20-day avg volume (pullback vs rally energy).
	vol20 := avgVol(kl, 20)
	if vol20 > 0 {
		sd.VolumeRatio = avgVol(kl, 5) / vol20
	}

	// 板块龙性由已验证板块填充：RPS 排名前2视为板块龙头
	// English: fill sector leadership from the verified sector — RPS rank 1-2 counts as leader.
	if sector != nil {
		sd.IsSectorTop2 = sector.RPSRank > 0 && sector.RPSRank <= 2
		sd.SectorRPS20 = sector.RPS20
		sd.SectorRPS60 = sector.RPS20
	}
	return sd
}

// evalFor 按战法类型分发到真实评分逻辑：
//   - Dragon/DoubleBump 走 EvaluateReal（K线+板块共振）
//   - DragonReturn 从K线派生 StockData 走 Evaluate
//   - NShape 从日K+实时量价+分钟MACD构造 WaveA/IntradayB/Ctx 走 EvaluateWave
//
// sector 传 nil 表示无板块上下文（个股直入 8a/8b 场景）；emotionPhase 供 N 形情绪硬闸；
// d1 为 D1Scorer 对该股的评分（含 LLM 打分与负面阻断），eventDesc 为个股关联新闻标题（供 D1 YAML 兜底），
// pe 为个股PE（供 D3 超跌评分），两者仅 N 形战法消费，其他战法忽略。
// English: dispatches to each strategy's real scoring by concrete type — Dragon/DoubleBump via
// EvaluateReal, DragonReturn via a derived StockData Evaluate, NShape via constructed WaveA/IntradayB/Ctx
// EvaluateWave. sector nil means no sector context (direct 8a/8b input); d1/eventDesc/pe are consumed by
// the N-shape strategy only.
func evalFor(runner StrategyRunner, code string, md *strategy_engine.StockMarketData, sector *sector_agent.VerifiedSector, emotionPhase string, d1 *D1Score, eventDesc string, pe float64) (*strategy.Evaluation, error) {
	// 行情数据缺失时回退到策略的默认评估接口（各战法自行处理 nil 行情）
	// English: when market data is missing fall back to the strategy's default Evaluate (strategies
	// handle nil data themselves).
	if md == nil {
		return runner.Strategy.Evaluate(code, md)
	}
	// 按策略具体类型分发到真实评分逻辑
	// English: dispatch by the concrete strategy type.
	switch st := runner.Strategy.(type) {
	case *dragon.DragonStrategy:
		// 龙头战法：K线 + 板块共振（把已验证板块折叠成 SectorInfo 列表）
		// English: dragon — K-lines + sector resonance (verified sector folded into a SectorInfo list).
		var sectors []data.SectorInfo
		if sector != nil {
			sectors = []data.SectorInfo{{Name: sector.Name, ChangePct: sector.ChangePct}}
		}
		return st.EvaluateReal(code, stockInfoFromMarketData(md), md.KLines, sectors), nil
	case *double_bump.DoubleBumpStrategy:
		// 双响炮：实时信息 + K线
		// English: double-bump — realtime info + K-lines.
		return st.EvaluateReal(code, stockInfoFromMarketData(md), md.KLines), nil
	case *dragon_return.DragonReturnStrategy:
		// 龙回头：从日K派生首轮涨幅/回调/缩量输入
		// English: dragon-return — derive first-rise/pullback/shrink inputs from daily K-lines.
		return st.Evaluate(code, dragonReturnDataFromMarketData(code, md, sector))
	case *n_shape.NShapeStrategy:
		// N形：日K A波 + 日内快照 B段 + 上下文（含情绪硬闸 + D1 事件 + PE）
		// English: N-shape — daily A-wave + intraday B-snapshot + context (emotion gate, D1 event, PE).
		return st.EvaluateWave(buildWaveA(md, sector), buildIntradayB(md), buildCtx(md, emotionPhase, d1, eventDesc, pe))
	default:
		// 未知/未特化策略 → 回退到策略默认评估接口
		// English: unknown/non-specialized strategy → fall back to the default Evaluate.
		return runner.Strategy.Evaluate(code, md)
	}
}
