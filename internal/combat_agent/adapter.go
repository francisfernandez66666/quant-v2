// Package combat_agent 战法适配层：把引擎传入的 *strategy_engine.StockMarketData
// 转换成各战法真实评分接口需要的结构化输入，让 8a/8b 战法在标准扫描路径上真正产出信号。
package combat_agent

import (
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/sector_agent"
	"quant-trading-v2/internal/strategy"
	"quant-trading-v2/internal/strategy_engine"
	"quant-trading-v2/internal/strategies/double_bump"
	"quant-trading-v2/internal/strategies/dragon"
	"quant-trading-v2/internal/strategies/dragon_return"
)

// stockInfoFromMarketData 从 StockMarketData 构造战法需要的个股实时信息。
// 成交量/成交额取自日K最后一根（近似当日量能）。
func stockInfoFromMarketData(md *strategy_engine.StockMarketData) *data.StockInfo {
	si := &data.StockInfo{
		Code:      md.Code,
		Name:      md.Name,
		Price:     md.Price,
		ChangePct: md.ChangePct,
	}
	if n := len(md.KLines); n > 0 {
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

// ma 计算最近 n 根K线的收盘均线。
func ma(kl []data.KLine, n int) float64 {
	if len(kl) < n {
		n = len(kl)
	}
	if n <= 0 {
		return 0
	}
	s := 0.0
	for _, k := range kl[len(kl)-n:] {
		s += k.Close
	}
	return s / float64(n)
}

// avgVol 计算最近 n 根K线的平均成交量。
func avgVol(kl []data.KLine, n int) float64 {
	if len(kl) < n {
		n = len(kl)
	}
	if n <= 0 {
		return 0
	}
	s := 0.0
	for _, k := range kl[len(kl)-n:] {
		s += k.Volume
	}
	return s / float64(n)
}

// dragonReturnDataFromMarketData 从日K线派生龙回头战法的输入：
// 首轮涨幅(近40日主升高点)、回调幅度/天数、缩量比、均线；板块龙性由已验证板块填充。
// K线不足30根时返回零值结构（龙性硬性条件不满足 → 0分，安全降级）。
func dragonReturnDataFromMarketData(code string, md *strategy_engine.StockMarketData, sector *sector_agent.VerifiedSector) *dragon_return.StockData {
	sd := &dragon_return.StockData{
		Code:         code,
		Name:         md.Name,
		CurrentPrice: md.Price,
	}
	kl := md.KLines
	if len(kl) < 30 {
		return sd
	}

	sd.MA5 = ma(kl, 5)
	sd.MA10 = ma(kl, 10)
	sd.MA20 = ma(kl, 20)

	// 近40根内最高收盘作为主升高点
	start := len(kl) - 40
	if start < 0 {
		start = 0
	}
	hiIdx := start
	for i := start; i < len(kl); i++ {
		if kl[i].Close > kl[hiIdx].Close {
			hiIdx = i
		}
	}
	sd.HighestPrice = kl[hiIdx].Close
	if sd.HighestPrice > 0 {
		sd.FirstRisePct = (sd.HighestPrice - kl[start].Close) / kl[start].Close
		sd.PullbackPct = (sd.HighestPrice - md.Price) / sd.HighestPrice
	}
	sd.PullbackDays = len(kl) - 1 - hiIdx
	if sd.PullbackDays < 0 {
		sd.PullbackDays = 0
	}

	// 缩量比近似：近5日均量 / 20日均量（回调缩量 vs 主升量能）
	vol20 := avgVol(kl, 20)
	if vol20 > 0 {
		sd.VolumeRatio = avgVol(kl, 5) / vol20
	}

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
//   - 其余（NShape 需日内快照，实时触发阶段接入）保持标准接口
//
// sector 传 nil 表示无板块上下文（个股直入 8a/8b 场景）。
func evalFor(runner StrategyRunner, code string, md *strategy_engine.StockMarketData, sector *sector_agent.VerifiedSector) (*strategy.Evaluation, error) {
	if md == nil {
		return runner.Strategy.Evaluate(code, md)
	}
	switch st := runner.Strategy.(type) {
	case *dragon.DragonStrategy:
		var sectors []data.SectorInfo
		if sector != nil {
			sectors = []data.SectorInfo{{Name: sector.Name, ChangePct: sector.ChangePct}}
		}
		return st.EvaluateReal(code, stockInfoFromMarketData(md), md.KLines, sectors), nil
	case *double_bump.DoubleBumpStrategy:
		return st.EvaluateReal(code, stockInfoFromMarketData(md), md.KLines), nil
	case *dragon_return.DragonReturnStrategy:
		return st.Evaluate(code, dragonReturnDataFromMarketData(code, md, sector))
	default:
		return runner.Strategy.Evaluate(code, md)
	}
}
