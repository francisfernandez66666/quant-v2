// budget_error.go — §F-4（2026-11-01）：动态预算拒绝的**结构化**错误。
//
// 缺陷原文：`POST /api/paper/buy` 在动态仓位下被拒时只回一句
// `{"error":"动态仓位: 额度不足1手"}`。而同一套裁定在 `/api/signals/decisions` 里
// 明明带着 capped_by / room / cash / per_trade_pct / notional 全套上下文——
// 用户看到的是"系统坏了"，运维拿到的是"没有可查的数"。
//
// 本文件把拒绝原因做成可机读的结构，由 server 侧展开成
// `{error, detail:{...}}`（保留 error 键以兼容既有前端）。
//
// English: §F-4 — a structured rejection for the dynamic budget so the write path can explain itself
// the same way /api/signals/decisions already does.
package paper

import (
	"fmt"

	"quant-trading-v2/internal/ordbudget"
)

// BudgetError 动态仓位拒绝的结构化原因。
//
// 字段口径与 ordbudget.Result 同源，不另算一套：
//   - CappedBy  最终约束来源（one_lot_floor / target / stock / industry / cluster / cash）
//   - Budget    本次裁定给出的预算（元）
//   - OneLot    一手成本（元）；Budget < OneLot 就是"额度不足1手"的量化解释
//   - Cash      可用现金
//   - Room      目标仓位剩余空间
//   - PerTradePct 本轮单笔占比（已含 MS×SS 缩放后的生效值）
//
// English: mirrors ordbudget.Result — no second implementation of the same numbers.
type BudgetError struct {
	Reason      string  `json:"reason"`
	CappedBy    string  `json:"capped_by"`
	Budget      float64 `json:"budget"`
	OneLotCost  float64 `json:"one_lot_cost"`
	Price       float64 `json:"price"`
	Cash        float64 `json:"cash"`
	Room        float64 `json:"room"`
	PerTradePct float64 `json:"per_trade_pct"`
	// Prefix 人类可读文案的前缀；空＝"动态仓位"（历史口径）。
	// §F-4（2026-11-01）：单笔金额绝对帽不是动态预算裁出来的（它是 risk_gate 的绝对阈值），
	// 套上"动态仓位:"会把排障引到错的地方——用户会去调动态仓位开关，而该调的是绝对帽。
	// 默认空值保持既有前缀不变，消费点按 "动态仓位:" 分流的历史行为不受影响。
	// English: overridable human prefix; empty keeps the historical "动态仓位" so existing
	// consumers keep working, while a non-budget cap can label itself honestly.
	Prefix string `json:"-"`
}

// Error 保持人类可读，且**保留原来的前缀**——既有前端/日志按 "动态仓位:" 分流，不能悄悄改口径。
func (e *BudgetError) Error() string {
	reason := e.Reason
	if reason == "" {
		reason = "动态预算不足"
	}
	prefix := e.Prefix
	if prefix == "" {
		prefix = "动态仓位"
	}
	// 带上"差多少"：只报"额度不足1手"等于让用户自己猜该改哪个参数。
	if e.OneLotCost > 0 && e.Budget < e.OneLotCost {
		return fmt.Sprintf("%s: %s（单笔额度 %.2f 元 < 一手成本 %.2f 元）", prefix, reason, e.Budget, e.OneLotCost)
	}
	return fmt.Sprintf("%s: %s", prefix, reason)
}

// newBudgetError 从 ordbudget.Result 组装结构化原因（不重算任何数）。
func newBudgetError(res ordbudget.Result, price, perTradePct, cash, room float64) *BudgetError {
	be := &BudgetError{
		Reason:      res.SkipReason,
		CappedBy:    res.CappedBy,
		Budget:      res.Budget,
		OneLotCost:  res.OneLot,
		Price:       price,
		Cash:        cash,
		Room:        room,
		PerTradePct: perTradePct,
	}
	// §F-4：绝对帽不是动态预算裁出来的（它是 risk_gate 的绝对阈值），套上"动态仓位:"会把排障
	// 引到错的方向——用户会去调动态仓位开关，而该调的是单笔绝对帽。前缀在这里统一改写，
	// 与 fill 层的 maxOrderAmountErrLocked 用同一句话，两条路径的文案不会漂移。
	// English: the notional cap is not a dynamic-budget decision, so it relabels its own prefix —
	// same sentence as the fill-layer path, so the two reject paths cannot drift apart.
	if res.CappedBy == "max_order_amount" {
		be.Prefix = "单笔金额超限"
	}
	return be
}
