// cost.go — 回测交易成本模型（§GAP4.1/4.2 回测真实性）：
// 费率与模拟盘 §R11 默认值同源（paper.DefaultConfig）：佣金万2.5 双边、印花税 0.05%
// 卖出单边、滑点 5bp 单边。按名义额比例计费，忽略最低佣金 5 元——回测为逐笔收益率
// 口径、无固定仓位规模，万 2.5 费率下 5 元下限仅对不足 2 万元的小额单有意义。
// 另含"开盘即封板不可成交"判定（打板类战法的次日开盘买入假设对一字板不成立）。
package btreplay

import (
	"quant-trading-v2/internal/paper"
)

// 回测交易成本费率常量（与模拟盘同源）。
const (
	costSlippageBps    = 5.0     // 单边滑点（bp）
	costCommissionRate = 0.00025 // 佣金万2.5（双边）
	costStampTaxRate   = 0.0005  // 印花税（卖出单边）
)

// costRoundTripPnl 净额口径收益率(%)：买价上浮滑点、卖价下浮滑点，
// 扣双边佣金 + 卖出印花税。raw 入参为未加滑点的原始价格序列取值。
// English: net round-trip return (%) — slippage on both sides plus two-way commission and sell stamp tax.
func costRoundTripPnl(rawEntry, rawExit float64) float64 {
	if rawEntry <= 0 || rawExit <= 0 {
		return 0
	}
	buy := rawEntry * (1 + costSlippageBps/10000)
	sell := rawExit * (1 - costSlippageBps/10000)
	feeFrac := 2*costCommissionRate + costStampTaxRate
	return (sell-buy)/buy*100 - feeFrac*100
}

// costOpenAtLimitUp 开盘即封板不可成交判定（§GAP4.2）：开盘价 ≥ 前收×(1+板块涨停幅) − 容差。
// 一字板/秒板买单现实中几乎排队无望；容差 0.001 吸收复权价缩放后的舍入误差。
// 局限：bars 不带名称，ST 档（5%）不在此判定，按代码板块幅度近似。
// English: unfillable-at-open check — an open already at the board limit cannot realistically be filled.
func costOpenAtLimitUp(code string, prevClose, open float64) bool {
	if prevClose <= 0 || open <= 0 {
		return false
	}
	limit := prevClose * (1 + paper.LimitUpPct(code, "")/100)
	return open >= limit-0.001
}
