// Package ordbudget 实盘/模拟盘共享的买入预算纯函数（v3 定稿）。
//
// 口径（docs/DYN_SIZE_V3_PLAN_20261023.md §4）：
//
//	raw       = Base × Scale
//	if raw < OneLot: raw = OneLot
//	room      = min(raw, 行业剩余额度, 单票剩余额度)
//	if room < OneLot: 跳过
//	qty       = floor(room / Price / Lot) × Lot
//	qty       = min(qty, 现金可负担整手)
//
// 关键纪律：
//   - 1 手地板不能突破 35% 额度；额度不足 1 手就跳过；
//   - 行业未知时禁用行业额度，但单票额度始终参与；
//   - 所有金额向下取整到整手；
//   - 纯函数、无 IO，调用方负责把在途委托计入 IndustryValue/StockValue。
//
// English: shared pure buy-budget calculator for live and paper. Dynamic sizing is the sole
// budget authority; 35% industry/single-stock rooms shrink the order instead of rejecting it,
// and the one-lot floor may never break those rooms.
package ordbudget

import "math"

// DefaultLot A 股一手股数。
const DefaultLot = 100

// moneyEpsilon 金额比较容差（0.000001 元）。百分比×权益会产生 999.9999999999977 这类
// 浮点尾差；若按严格小于比较，会把“恰好剩 1 手”误判为不足 1 手而跳过。
// English: money comparison tolerance — percentage×equity yields tails like 999.9999999999977,
// and a strict comparison would wrongly skip a room that is exactly one lot.
const moneyEpsilon = 1e-6

// Inputs 预算输入。金额单位均为元，比例为百分数（35 = 35%）。
// English: budget inputs; amounts in CNY and percentages as whole numbers (35 = 35%).
type Inputs struct {
	// Base 预算基数：实盘=总资产；模拟盘=池权益（池现金+池持仓市值）。
	Base float64
	// CapEquity 集中度分母：实盘=总资产；模拟盘=整个模拟盘总权益。
	CapEquity float64
	// Scale 动态仓位缩放系数（中性 1.0）。
	Scale float64
	// Price 委托价（现价，不可得时用信号触发价）。
	Price float64
	// Cash 可用现金：实盘=可用资金；模拟盘=池现金。
	Cash float64
	// Lot 一手股数（<=0 时按 100）。
	Lot int
	// IndustryCapPct 行业上限百分数（35）；<=0 表示不启用。
	IndustryCapPct float64
	// SingleStockCapPct 单票上限百分数（35）；<=0 表示不启用。
	SingleStockCapPct float64
	// IndustryValue 该行业当前市值（含在途委托）。
	IndustryValue float64
	// StockValue 该股票当前市值（含在途委托）。
	StockValue float64
	// IndustryKnown 行业是否已知；未知时行业额度不参与，单票额度兜底。
	IndustryKnown bool
	// FeeBufferPct 现金费用缓冲百分数（实盘 0.6）；<=0 表示不预留。
	FeeBufferPct float64
}

// Result 预算结果。
// English: budget result.
type Result struct {
	// Raw 抬升 1 手地板后的原始预算（审计用）。
	Raw float64
	// OneLot 一手成本。
	OneLot float64
	// IndustryRoom 行业剩余额度（未启用/未知为 0）。
	IndustryRoom float64
	// StockRoom 单票剩余额度（未启用为 0）。
	StockRoom float64
	// Budget 最终预算（= Notional）。
	Budget float64
	// Qty 最终整手股数。
	Qty int
	// Notional 最终成交额（Qty×Price）。
	Notional float64
	// CappedBy 最终约束来源：dynamic / one_lot_floor / industry / stock / cash / none。
	CappedBy string
	// SkipReason 非空表示不下单（额度不足1手/现金不足/参数不可得）。
	SkipReason string
}

// Compute 计算最终预算与股数。任何不可得输入都返回 SkipReason，不伪造成可交易。
// English: computes the final budget and lot count; missing inputs return a skip reason instead
// of a fabricated trade.
func Compute(in Inputs) Result {
	lot := in.Lot
	if lot <= 0 {
		lot = DefaultLot
	}
	res := Result{CappedBy: "none"}
	if in.Base <= 0 || in.Scale <= 0 || in.Price <= 0 || in.CapEquity <= 0 || in.Cash <= 0 {
		res.SkipReason = "参数不可得"
		return res
	}
	oneLot := in.Price * float64(lot)
	res.OneLot = oneLot

	raw := in.Base * in.Scale
	floored := false
	if raw+moneyEpsilon < oneLot {
		raw = oneLot
		floored = true
	}
	res.Raw = raw

	budget := raw
	capped := "dynamic"
	if floored {
		capped = "one_lot_floor"
	}

	// 行业额度：仅行业已知且上限为正时参与；剩余额度更小即把预算压到额度内（减额而非拒买）。
	// English: the industry room applies only when the industry is known and the cap is positive;
	// a smaller room shrinks the budget instead of rejecting the buy.
	if in.IndustryKnown && in.IndustryCapPct > 0 {
		room := roomOf(in.IndustryCapPct, in.CapEquity, in.IndustryValue)
		res.IndustryRoom = room
		if room < budget {
			budget = room
			capped = "industry"
		}
	}
	// 单票额度：始终参与（行业未知时它就是唯一兜底），同样只减额不拒买。
	// English: the single-stock room always applies (it is the sole backstop when the industry is
	// unknown) and likewise shrinks rather than rejects.
	if in.SingleStockCapPct > 0 {
		room := roomOf(in.SingleStockCapPct, in.CapEquity, in.StockValue)
		res.StockRoom = room
		if room < budget {
			budget = room
			capped = "stock"
		}
	}

	// 额度不足 1 手 → 跳过：1 手地板只是抬升 raw 的手段，绝不允许反噬 35% 额度。
	// English: below one lot the buy is skipped — the one-lot floor may raise raw but can never
	// override the 35% rooms.
	if budget+moneyEpsilon < oneLot {
		res.Budget = budget
		res.CappedBy = capped
		res.SkipReason = "额度不足1手"
		return res
	}

	// 预算向下取整到整手股数。
	// English: round the budget down to whole lots.
	qty := int(math.Floor((budget+moneyEpsilon)/in.Price/float64(lot))) * lot
	if qty <= 0 {
		res.Budget = budget
		res.CappedBy = capped
		res.SkipReason = "额度不足1手"
		return res
	}

	// 费用缓冲：实盘预留佣金/印花税/过户费余量，避免"算得出买不起"。
	// English: fee buffer — live books reserve room for commission/stamp duty/transfer fees.
	fee := in.FeeBufferPct / 100
	if fee < 0 {
		fee = 0
	}
	// 现金可负担整手：按含费单价折算，压到 0 手即"现金不足"跳过。
	// English: affordable whole lots at the fee-inclusive unit price; zero means cash-short.
	affordable := int(math.Floor((in.Cash/(1+fee)+moneyEpsilon)/in.Price/float64(lot))) * lot
	if affordable < qty {
		qty = affordable
		capped = "cash"
	}
	if qty <= 0 {
		res.Budget = budget
		res.CappedBy = capped
		res.SkipReason = "现金不足"
		return res
	}

	// 成交额回写：Budget 与 Notional 恒等（便于审计对账）。
	// English: notional write-back — Budget and Notional are always identical for auditing.
	notional := float64(qty) * in.Price
	res.Qty = qty
	res.Budget = notional
	res.Notional = notional
	res.CappedBy = capped
	return res
}

// roomOf 计算某上限的剩余额度（负数按 0）。
// English: remaining room under a percentage cap (negative floored at zero).
func roomOf(capPct, equity, current float64) float64 {
	room := capPct/100*equity - current
	if room < 0 {
		return 0
	}
	// 金额取到分，抹掉百分比运算的浮点尾差（35%×100000−34000 = 999.9999999999977）。
	return math.Round(room*100) / 100
}
