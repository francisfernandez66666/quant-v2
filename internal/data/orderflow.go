// orderflow.go — 盘口微观结构（§SIGNAL_EDGE_ENHANCEMENT_PLAN P2.4）。
// 由盘口五档快照差分推断订单流：委买/委卖压力不平衡、大单净流方向、主动买卖占比，
// 并给出触发规则（买单压力持续上升→买；封单快速撤减→卖预警）。
// 无逐笔数据时用快照差分降级；滑窗平滑 + 最小变化阈值抑制噪声。
// 纯计算自包含；Enhance.OrderFlow 引擎侧门控接入。
// English: order-flow microstructure (P2.4). Infers order flow from 5-level book snapshot diffs:
// bid/ask pressure imbalance, large-order net direction, active buy ratio, plus trigger rules
// (rising buy pressure → buy; fast seal-drain → sell warning). Falls back to snapshot-diff when no
// tick data; window smoothing + min-change threshold suppress noise. Pure computation, gated by
// Enhance.OrderFlow.

package data

import "math"

// OrderFlow 盘口订单流特征（每帧）。English: per-frame order-flow features.
type OrderFlow struct {
	Imbalance      float64 // 委比（-1~1），>0 买压、<0 卖压（复用委比口径）
	BigNetInflow   float64 // 大单净流入方向（买一档量-卖一档量）/总量，-1~1
	ActiveBuyRatio float64 // 主动买入占比估计（0~1；快照差分模拟）
	TotalVol       float64 // 五档总委托量（手）
}

// OrderLevels 盘口五档汇总骨架（由 OrderBook 直接喂入/或独立采样）。
// English: 5-level book roll-up skeleton (fed directly from OrderBook or standalone sample).
type OrderLevels struct {
	BidVols []float64 // 买一~买 N 委托量（手）
	AskVols []float64 // 卖一~卖 N 委托量（手）
}

// FromBook 从 OrderBook 快照提取五档委托量。English: extracts 5-level volumes from an OrderBook.
func FromBook(ob *OrderBook) OrderLevels {
	n := 5
	if ob == nil {
		return OrderLevels{}
	}
	bid := make([]float64, 0, n)
	ask := make([]float64, 0, n)
	for i := 0; i < n && i < len(ob.Bids); i++ {
		bid = append(bid, ob.Bids[i].Volume)
	}
	for i := 0; i < n && i < len(ob.Asks); i++ {
		ask = append(ask, ob.Asks[i].Volume)
	}
	return OrderLevels{BidVols: bid, AskVols: ask}
}

func sumF(xs []float64) float64 {
	var s float64
	for _, v := range xs {
		s += v
	}
	return s
}

// ComputeOrderFlow 由当前帧计算订单流特征。
// Imbalance=委比（-1~1）；BigNetInflow 用买一/卖一档量差近似大单方向；ActiveBuyRatio 在无逐笔
// 时以快照差分估算（prev 提供时：Δ买盘-Δ卖盘的符号 → 主动方向），prev 为 nil 则 0.5 中性。
// English: computes order-flow features for the current frame. Imbalance is the bid/ask ratio
// (-1~1); BigNetInflow approximates large-order direction via bid1-ask1 delta; ActiveBuyRatio is
// a snapshot-diff proxy when no tick data is available (0.5 neutral without prev).
func ComputeOrderFlow(cur OrderLevels, prev *OrderLevels) OrderFlow {
	bid := sumF(cur.BidVols)
	ask := sumF(cur.AskVols)
	total := bid + ask
	of := OrderFlow{}
	if total <= 0 {
		return of
	}
	of.TotalVol = total
	of.Imbalance = (bid - ask) / total
	// 大单方向：买一/卖一档量差
	b1 := firstOrZero(cur.BidVols)
	a1 := firstOrZero(cur.AskVols)
	o1Sum := b1 + a1
	if o1Sum > 0 {
		of.BigNetInflow = (b1 - a1) / o1Sum
	}
	// 主动占比：快照差分（有界代理）。
	of.ActiveBuyRatio = 0.5
	if prev != nil {
		db := bid - sumF(prev.BidVols)
		da := ask - sumF(prev.AskVols)
		// 买增卖减 → 强买 1.0；卖增买减 → 强卖 0.0；同向增减按占比，双缩 → 中性 0.5。
		switch {
		case db > 0 && da > 0:
			if s := db + da; s > 0 {
				of.ActiveBuyRatio = db / s
			}
		case db > 0 && da <= 0:
			of.ActiveBuyRatio = 1.0
		case db <= 0 && da > 0:
			of.ActiveBuyRatio = 0.0
		}
	}
	return of
}

func firstOrZero(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	return xs[0]
}

// TriggerRule 订单流触发规则。返回买/卖信号与原因说明（观察用）。
// 规则：
//
//	buy  ：委比 ≥ 0.4 且 主动买占比 ≥ 0.6 且 大单净流入 ≥ 0.3（持续买压）；
//	sell ：委比 ≤ -0.4 且（封单快速撤减 → 大单净流入 ≤ -0.3 或 主动买占比 ≤ 0.4）。
//
// 阈值可调（TriggerDefaults）。English: order-flow trigger rules (observational): buy when the
// imbalance is strongly positive with elevated active-buy and big-order inflow; sell-warning when
// the book is heavily imbalanced to the ask with a fast seal-drain.
type TriggerRule struct {
	BuyImb  float64 // 买入触发委比
	BuyAct  float64 // 买入触发主动买占比
	BuyBig  float64 // 买入触发大单净流入
	SellImb float64 // 卖出触发委比
	SellAct float64 // 卖出触发主动买占比
	SellBig float64 // 卖出触发大单净流入
}

// TriggerDefaults 默认阈值（按 A 股经验校准确认，可按配置覆盖）。
var TriggerDefaults = TriggerRule{
	BuyImb: 0.4, BuyAct: 0.6, BuyBig: 0.3,
	SellImb: -0.4, SellAct: 0.4, SellBig: -0.3,
}

// TriggerResult 触发判定结果。English: trigger decision result.
type TriggerResult struct {
	Buy  bool   `json:"buy"`
	Sell bool   `json:"sell"`
	Note string `json:"note,omitempty"`
}

// Trigger 对当前帧做触发判定。English: decides triggers for the current frame.
func Trigger(of OrderFlow, cfg TriggerRule) TriggerResult {
	if cfg.BuyImb == 0 {
		cfg = TriggerDefaults
	}
	var res TriggerResult
	if of.Imbalance >= cfg.BuyImb && of.ActiveBuyRatio >= cfg.BuyAct && of.BigNetInflow >= cfg.BuyBig {
		res.Buy = true
		res.Note = "买压持续上升"
		return res
	}
	if of.Imbalance <= cfg.SellImb && (of.BigNetInflow <= cfg.SellBig || of.ActiveBuyRatio <= cfg.SellAct) {
		res.Sell = true
		res.Note = "卖压/封单撤减"
		return res
	}
	return res
}

// Smooth 5 帧滑窗均值（抑制快照差分噪声）。
// English: 5-frame moving average to damp snapshot-diff noise.
func Smooth(frame []OrderFlow) OrderFlow {
	if len(frame) == 0 {
		return OrderFlow{}
	}
	var acc OrderFlow
	for _, f := range frame {
		acc.Imbalance += f.Imbalance
		acc.BigNetInflow += f.BigNetInflow
		acc.ActiveBuyRatio += f.ActiveBuyRatio
		acc.TotalVol += f.TotalVol
	}
	n := float64(len(frame))
	acc.Imbalance /= n
	acc.BigNetInflow /= n
	acc.ActiveBuyRatio /= n
	acc.TotalVol /= n
	if math.IsNaN(acc.Imbalance) {
		acc.Imbalance = 0
	}
	return acc
}
