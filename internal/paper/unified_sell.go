// unified_sell.go — §REFACTOR_UNIFIED_SELL P3 模拟盘并轨：统一卖出裁决层的 paper 侧只读探针与处置执行口。
//
// 架构定位（docs/REFACTOR_UNIFIED_SELL_20260921.md §二/§四-P3）：模拟盘卖出与实盘共用
// signalctl 卖出裁决通道（键=(ChannelPaper, 账号, 代码)），探测器（CheckPositionsExits/
// CheckPositionAlerts 等）在 sell_unified_mode=on 后对 paper 不再是执行指令，只有本文件的
// ApplyUnifiedSell 一个出口能触发自动离场——双账一口径，止盈/止损/减仓语义与 live 完全对称。
// 做空账本（融券 shortOpen/shortCover）不在本次并轨范围：做空方向信号维持原分发路径。
// English: P3 paper-side probes + the single execution entry for unified sell disposals.
package paper

import (
	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/data"
)

// SellProbe 统一卖出裁决层的单持仓探针输入（paper 账本快照）。
// HighPrice 由 engine 侧 §M10 锚点文件注入（paper 账本本身不存持仓期最高价）：
// runPaperUnifiedJudge 裁决前取 <acctDir>/paper_sell_anchors.json 里的持久化锚点填进探针，
// 因此"重启后高点退回成本价"的旧形态已被收口（见 internal/engine/sell_anchor.go 头注）。
// EntryPrice 是**摊薄成本**（盈亏口径，裁决内核当前用它算涨跌幅）；AnchorPrice 是
// §ANCHOR-FIRST 的**首次买入价**（风险线口径，W2 影子期只随行对照、不参与判定）。
// English: probe input for the unified sell judge. HighPrice comes from the engine-side §M10
// anchor file (the paper book itself stores no holding-period high). EntryPrice is the diluted cost
// (P&L basis, still what the kernel measures against); AnchorPrice is the §ANCHOR-FIRST first-buy
// basis — carried alongside for the W2 shadow comparison only.
type SellProbe struct {
	Code        string
	Name        string
	EntryPrice  float64
	AnchorPrice float64 // §ANCHOR-FIRST 首次买入价锚（0=存量未回填）
	Qty         int
}

// SellProbes 当前全部有效持仓的裁决探针快照（Qty>0；现金/池状态不参与判定）。
func (e *Engine) SellProbes() []SellProbe {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]SellProbe, 0, len(e.positions))
	for _, p := range e.positions {
		if p == nil || p.Qty <= 0 {
			continue
		}
		out = append(out, SellProbe{Code: p.Code, Name: p.Name, EntryPrice: p.CostPrice,
			AnchorPrice: p.AnchorPrice, Qty: p.Qty})
	}
	return out
}

// ApplyUnifiedSell 执行统一裁决层的一张处置单（P3 切闸后 paper 自动卖出的唯一入口）：
//   - action="close" 全平；"trim" 半仓（autoSellLocked 内 trimDone 每码每日一次去重）；
//   - price=本轮裁决现价（≤0 不成交——宁可不卖也不按错价记账，与 autoSellLocked 行情守卫同口径）；
//   - reason 进订单留痕（Kind/Reason 可见"[统一裁决]"原文，事后核"为什么卖了"）。
//
// 复用 autoSellLocked 全部既有资产（AutoSell 开关、T+1 拒绝留痕、收益回池、report 镜像关闭）。
// 返回 false=本单未执行（未持仓/AutoSell 关/无有效价），调用方按未处置处理（状态机下轮重放）。
// English: applies one unified-adjudicator disposal through the legacy autoSellLocked machinery
// (AutoSell switch / T+1 reject audit / pool proceeds / report mirror all preserved); false = not
// executed this round, the state machine replays next round.
func (e *Engine) ApplyUnifiedSell(code, action string, price float64, reason string) bool {
	if price <= 0 || (action != "close" && action != "trim") {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	p, held := e.positions[code]
	if !held || p == nil || !e.cfg.AutoSell {
		return false
	}
	sig := &combat_agent.Signal{
		Code:      code,
		Name:      p.Name,
		Strategy:  p.Strategy,
		Direction: "做多",
		AlertType: "统一裁决",
		Price:     price,
		Reason:    reason,
	}
	quotes := map[string]*data.StockInfo{code: {Code: code, Price: price}}
	e.autoSellLocked(sig, action, quotes)
	e.persist()
	return true
}
