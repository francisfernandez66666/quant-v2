// cost.go — B4 回测交易成本模型（修复「回测零成本假设、超额收益被高估」）。
//
// 成本参数来源：与模拟盘 §R11 默认值、internal/btreplay/cost.go 同源（paper.DefaultConfig）：
//   - 佣金：万分之 2.5（双边计），单笔最低 5 元；
//   - 滑点：成交额的固定基点（单边 5bp，买卖双边计）；
//   - 印花税：卖出单边 0.05%。
//
// 与 internal/btreplay/cost.go 的区别：本模型额外实现「最低佣金 5 元」下限
// （btreplay 为简化忽略之，因其为逐笔收益率口径、无名义额）。回测没有真实仓位规模，
// 故用 AssumeNotional（默认每笔名义额 10 万元）把最低佣金折算为费率下限，使小额单的成本不被低估。
package backtest

import "math"

// CostModel 回测交易成本模型。所有字段为比例/金额参数，Apply 时按名义额折算为收益拖累。
// English: backtest trading-cost model; parameters are amortized onto return via notional.
type CostModel struct {
	CommissionRate float64 // 佣金费率（单边，按成交额），如 0.00025 = 万分之 2.5（买卖双边计）
	MinCommission  float64 // 单笔最低佣金（元），如 5（买卖双边各自适用）
	SlippageBps    float64 // 单边滑点（基点 bp），如 5（买卖双边计）；1bp = 1e-4
	StampTaxRate   float64 // 印花税（卖出单边，按成交额），如 0.0005 = 0.05%
	AssumeNotional float64 // 回测假设的每笔名义成交额（元），用于折算最低佣金下限；无真实仓位规模时给默认值
}

// DefaultCostModel 返回与模拟盘/btreplay 同源的默认成本模型。
// 默认每笔名义额 100000 元：最低佣金 5 元折算为费率下限 5/100000 = 0.005%（0.5bp），
// 对常规规模单几乎不起作用，仅在小额单上兜住「最低 5 元」下限。
// English: default cost model, same rate set as paper/btreplay; AssumeNotional=100k for the min-commission floor.
func DefaultCostModel() CostModel {
	return CostModel{
		CommissionRate: 0.00025, // 万分之 2.5（与 btreplay/cost.go costCommissionRate 同源）
		MinCommission:  5,       // 单笔最低佣金 5 元
		SlippageBps:    5,       // 单边滑点 5bp（与 btreplay/cost.go costSlippageBps 同源）
		StampTaxRate:   0.0005,  // 卖出印花税 0.05%（与 btreplay/cost.go costStampTaxRate 同源）
		AssumeNotional: 100000,  // 默认每笔名义额 10 万元
	}
}

// CostFraction 返回一笔「买入→卖出」往返交易的总成本占入场名义额的比例（收益拖累，正值）。
// 计算口径：
//   - 滑点双边：2 * SlippageBps / 10000；
//   - 佣金双边：2 * max(CommissionRate, MinCommission/notional)（最低佣金折算为费率下限）；
//   - 印花税单边：StampTaxRate（仅卖出计）。
//
// 入参 notional：该笔交易的名义成交额（元）；<=0 时回退到 AssumeNotional。
// 出参：成本/名义额（比例，可直接从毛收益中扣减得到净收益）。
// English: total round-trip cost as a fraction of entry notional (return drag), amortizing the min-commission floor.
func (c CostModel) CostFraction(notional float64) float64 {
	if notional <= 0 {
		notional = c.AssumeNotional
	}
	// 佣金费率：取「比例费率」与「最低佣金折算费率」的较大者，保证小额单不被低估。
	commRate := c.CommissionRate
	if minFrac := c.MinCommission / notional; minFrac > commRate {
		commRate = minFrac
	}
	// 滑点双边 + 佣金双边 + 卖出印花税单边。
	slippage := 2 * c.SlippageBps / 10000
	commission := 2 * commRate
	stamp := c.StampTaxRate
	return slippage + commission + stamp
}

// NetReturn 把毛收益率折算为扣成本后的净收益率。
// 入参 gross：毛收益率（如 Close/Entry - 1）；notional：该笔名义额（元，<=0 用 AssumeNotional）。
// 出参：净收益率 = gross - CostFraction(notional)。
// English: net return after cost = gross return minus the cost fraction for the notional.
func (c CostModel) NetReturn(gross, notional float64) float64 {
	if math.IsNaN(gross) {
		return math.NaN()
	}
	return gross - c.CostFraction(notional)
}
