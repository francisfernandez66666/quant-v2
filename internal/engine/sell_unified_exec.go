// sell_unified_exec.go — §SELLPOINT-UNIFY P2 切闸：统一卖出裁决 → 展示/执行投影。
//
// mode=on 时，卖出卡片的处置结论**只**来自 signalctl 裁决层（sellRoundVerdict）：
//   - pass 处置 → 可执行卡（Source=unified）：close+止损线/深破=「止损/高」，close+止盈/移动=「止盈/高」，
//     trim=「减仓/高」；autoExecuteRealSells 只放行该来源（旧「止损任意来源直放」与 short_tactic 后门关闭）；
//   - hold（观察窗/延持中）→「持有/低」预警卡（Source=unified-watch），不可执行；
//   - 未触线利空 / 非双源验证利空 → 只出「持有/中」预警卡（owner 语义①：降级为预警，不再自动卖）。
//
// T+1 当日锁定持仓的处置卡降为预警（卖不动的指令即误导，§PROD-T1 同口径）。
// 卡片仍走既有 SSE/消息中心通道，JSON 形状不变（前端零改动）。
package engine

import (
	"log"

	"quant-trading-v2/internal/signalctl"
	"quant-trading-v2/internal/store"
	"quant-trading-v2/internal/trading"
)

// unifiedSellViews 把本轮裁决视图投影为 trading 层的统一卖出卡输入（mode=on 专用）。
// sellable=T+1 可卖量（键=ts_code；缺 key=未知不锁定，与 §PROD-T1 放行口径一致）。
func (e *Engine) unifiedSellViews(positions []store.RealPosition, verdicts []sellRoundVerdict, sellable map[string]int) []trading.UnifiedSellView {
	if len(verdicts) == 0 {
		return nil
	}
	byCode := make(map[string]*sellRoundVerdict, len(verdicts))
	for i := range verdicts {
		byCode[verdicts[i].TsCode] = &verdicts[i]
	}
	locked := t1LockedCodes(positions, sellable)
	out := make([]trading.UnifiedSellView, 0, len(verdicts))
	for _, p := range positions {
		v, ok := byCode[p.TsCode]
		if !ok {
			continue
		}
		view := projectSellView(v)
		if view == nil {
			continue
		}
		// §PROD-T1 同口径：当日买入锁定（可卖量≤0）的处置不可执行——降为预警卡并写明原因，
		// 绝不推"止盈/止损请手动处理"这种今天根本做不到的指令。
		if view.Source == trading.UnifiedSellSourceAction && locked[p.TsCode] {
			log.Printf("[sell-unify] %s(%s) 裁决处置 %s 因 T+1 当日锁定降级为预警", p.Name, p.TsCode, view.Action)
			view = &trading.UnifiedSellView{
				TsCode: p.TsCode, Action: "持有", Level: "中", RefPrice: v.Price,
				Reason: "统一卖出裁决=" + view.Action + "，但当日买入 T+1 锁定不可卖（解锁后按裁决窗重放处置）",
				Source: trading.UnifiedSellSourceWatch,
			}
		}
		out = append(out, *view)
	}
	return out
}

// projectSellView 单条裁决 → 展示/执行视图映射（纯函数，便于回归锁）。
// 返回 nil=无需出卡（未触线、无利空且无处置的常规轮次）。
func projectSellView(v *sellRoundVerdict) *trading.UnifiedSellView {
	if d := v.Verdict.Disposal; d != nil {
		action, level := "止损", "高"
		switch {
		case d.Action == signalctl.SellActionTrim:
			action, level = "减仓", "高"
		case d.Line == signalctl.SellLineTakeProfit || d.Line == signalctl.SellLineTrail:
			action, level = "止盈", "高"
		}
		return &trading.UnifiedSellView{
			TsCode: v.TsCode, Action: action, Level: level, RefPrice: v.Price,
			Reason: "[统一裁决] " + d.Reason, Source: trading.UnifiedSellSourceAction,
		}
	}
	if v.Verdict.Line != signalctl.SellLineNone {
		// 观察窗/延持中：留痕即预警卡（缺陷 7 根治——同码单裁决单卡，"观察中"不再被误报卡遮蔽）。
		return &trading.UnifiedSellView{
			TsCode: v.TsCode, Action: "持有", Level: "低", RefPrice: v.Price,
			Reason: "[卖出观察中] " + v.Verdict.Line.String() + "：" + v.Verdict.HoldReason,
			Source: trading.UnifiedSellSourceWatch,
		}
	}
	if v.BearHit {
		// owner 语义①：未触线（或非双源验证）利空=只预警，无处置资格。
		return &trading.UnifiedSellView{
			TsCode: v.TsCode, Action: "持有", Level: "中", RefPrice: v.Price,
			Reason: "[利空预警] 验证等级=" + v.Verified + "（未触线/非双源，不自动卖）",
			Source: trading.UnifiedSellSourceWatch,
		}
	}
	return nil
}

// t1LockedCodes §PROD-T1 口径：可卖量已知且 ≤0 的持仓代码集合（当日买入全锁）。
func t1LockedCodes(positions []store.RealPosition, sellable map[string]int) map[string]bool {
	out := map[string]bool{}
	if len(sellable) == 0 {
		return out
	}
	for _, p := range positions {
		if q, ok := sellable[p.TsCode]; ok && q <= 0 {
			out[p.TsCode] = true
		}
	}
	return out
}
