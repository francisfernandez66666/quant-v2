// t0.go — 底仓 T+0（§SIGNAL_EDGE_ENHANCEMENT_PLAN P3）。
// A 股 T+1：当日买入次日方可卖。持有昨日底仓时，可"当日低吸加仓（机动仓）→ 当日卖出等量
// 底仓"锁日内价差摊薄成本。本模块核算：
//   - 层拆分：Base 底仓（昨日及以前买入，可卖）与 Intraday 机动仓（当日买入，当日不可卖）；
//   - 机动仓上限（默认总仓 30%，不隔夜——尾盘须平掉等价量）；
//   - 结算：价差与成本摊薄核算。
// 纯核算（不读 config）。§T1-PARTIAL（2026-10-24 裁决三 W4）改变了本文件的地位：
//   - 🆕 `LayersOfPosition`（t1_partial.go）把这份数学接进了生产——它按**批次层账**汇总底仓/机动仓，
//     成为模拟盘两个卖出入口的可卖量判据；无层账的存量持仓仍回落本文件的整仓二值口径。
//   - 因此 `AllocLayers` 不再是"已设计未接线"，而是**存量兼容路径 + 单测基准**。
//   - 仍未接线的是"做 T 的编排"：机动仓上限、配平、回转结算这三件事没有调度方，配置开关
//     Enhance.T0 依旧无消费方（方案 §4.4 第 11 行要求 W6 要么接上它、要么由新 TConfig 取代，
//     不许留第三个 T 开关）。接线前置条件也仍挂着：先向柜台确认支持"当日卖旧+当日买新"申报
//     顺序与两腿幂等（Q13，详见 docs/BUGFIX_WATCHLIST_20260917.md #1 决策记录）。
// English: pure math. §T1-PARTIAL changed its status — LayersOfPosition now feeds the production sellable
// qty from the per-batch lot ledger (this file's AllocLayers remains the legacy fallback + unit baseline),
// so it is no longer "designed but unwired". What is still unwired is the T-round orchestration (intraday
// cap, rebalancing, round settlement) and the Enhance.T0 switch, whose broker prerequisite (same-day
// sell-old/buy-new ordering + two-leg idempotency, Q13) is still unconfirmed.

package paper

import (
	"time"
)

// PositionLayers 单一持仓的层拆分。
type PositionLayers struct {
	BaseQty      int     // 底仓数量（昨日及以前买入，可卖）
	IntradayQty  int     // 机动仓数量（当日低吸，当日不可卖）
	Sellable     int     // 可卖量（= BaseQty，T+1 语义下当日买入不可卖）
	TotalQty     int     // 总持仓
	WeightedCost float64 // 摊薄后成本价
}

// AllocLayers 按 T+1 语义拆分单笔持仓：FilledAt 为当日 → 机动仓，否则 → 底仓。
// 使用与现有 canSellToday 相同日历口径（北京时区 day 判定）。
// English: splits one position into base/intraday by T+1: filled today → intraday, else base.
func AllocLayers(qty int, costPrice float64, filledAt, now time.Time) PositionLayers {
	if qty <= 0 {
		return PositionLayers{}
	}
	layers := PositionLayers{TotalQty: qty, WeightedCost: costPrice}
	if canSellToday(filledAt, now) {
		layers.BaseQty = qty
		layers.Sellable = qty
	} else {
		layers.IntradayQty = qty
		layers.Sellable = 0
	}
	return layers
}

// AllocLayersAcross 对同一 code 的多笔持仓（数组）汇总拆分（底仓/机动仓汇总 + 加权摊薄成本）。
// English: aggregates layer split and weighted cost across multiple lots of one code.
func AllocLayersAcross(positions []*Position, now time.Time) PositionLayers {
	var out PositionLayers
	var costSum, baseQty, intQty float64
	for _, p := range positions {
		if p == nil {
			continue
		}
		l := AllocLayers(p.Qty, p.CostPrice, p.FilledAt, now)
		out.TotalQty += l.TotalQty
		out.BaseQty += l.BaseQty
		out.IntradayQty += l.IntradayQty
		out.Sellable += l.Sellable
		baseQty += float64(l.BaseQty)
		intQty += float64(l.IntradayQty)
		if l.TotalQty > 0 {
			costSum += p.CostPrice * float64(l.TotalQty)
		}
	}
	if out.TotalQty > 0 {
		out.WeightedCost = costSum / float64(out.TotalQty)
	}
	return out
}

// T0Cap 机动仓上限：min(totalQty, totalQty*maxPct)（maxPct 默认 0.30）。
// English: intraday-layer cap = min(total, total*maxPct) with maxPct defaulting to 30%.
func T0Cap(totalQty int, maxPct float64) int {
	if totalQty <= 0 {
		return 0
	}
	if maxPct <= 0 {
		maxPct = 0.30
	}
	if maxPct > 1 {
		maxPct = 1
	}
	cap := int(float64(totalQty) * maxPct)
	if cap > totalQty {
		cap = totalQty
	}
	return cap
}

// T0Settle 结算一条 T+0 操作：当日低吸 buyQty@buyPrice，随后卖出等量底仓 sellQty@sellPrice。
// 返回锁定价差、成本摊薄（元）与摊薄后成本价。要求 buyQty<=T0Cap 且 sellQty<=Sellable 且
// sellQty <= buyQty（卖出量不超当日机动仓量，避免留空隔夜）。English: settles one T+0 round:
// intraday buy (buyQty@buyPrice) followed by selling an equal base amount (sellQty@sellPrice);
// returns locked spread, cost improvement (yuan) and the new average cost. Keep sellQty<=buyQty so
// no overnight intraday layer remains.
func T0Settle(layers PositionLayers, buyQty int, buyPrice, sellPrice float64) (spread, costImprove float64, newCost float64) {
	if buyQty <= 0 || sellPrice-buyPrice < 0 {
		// 低吸需成交于卖价之下才有价差；否则不记账（保本/亏损不做 T+0）
		return 0, 0, layers.WeightedCost
	}
	sellQty := buyQty
	if layers.Sellable < sellQty {
		sellQty = layers.Sellable
	}
	if sellQty <= 0 {
		return 0, 0, layers.WeightedCost
	}
	perShare := sellPrice - buyPrice
	spread = perShare * float64(sellQty)
	improvePerShare := (layers.WeightedCost - buyPrice) / 2        // 摊薄近似（买卖等价量）
	costImprove = improvePerShare * float64(sellQty) * (1 - 0.005) // 印花税/佣金粗扣
	remain := layers.TotalQty
	if remain <= 0 {
		return spread, costImprove, layers.WeightedCost
	}
	newCost = layers.WeightedCost - improvePerShare
	if newCost < 0 {
		newCost = 0
	}
	return spread, costImprove, newCost
}
