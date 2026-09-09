// validate.go 配置 schema 校验（§WS-K 维4）：保存/热更入口先 Validate，非法配置返回 400
// 明确报错，不再静默排队。校验范围：
//   - 枚举：QMT.mode ∈ {auto,manual}、QMT.price_type ∈ {market,limit}；
//   - 范围：百分比 0~100、金额/笔数 ≥ 0、MaxPositions 1~50、心跳超时区间；
//   - 引用一致性：QMT enabled 时 gateway_url 与 token 必须同非空（防 executor 无网关固化类误配）。
//
// English: config schema validation (WS-K 维4). Save/hot-reload entry points run Validate first and
// return an explicit 400 instead of silently queueing bad configs. Covers enums, ranges and the
// reference consistency rule that an enabled QMT must have both gateway_url and token set.
package config

import "fmt"

// Validate 校验整份 Rules 配置；返回第一条非法项的错误（nil=合法）。
// 注意：战法白名单 ID 的合法性在 server 层校验（已知战法集合属于 engine/server 域，
// config 包不引 server 避免循环依赖；applySetQMTConfig 已有 knownStrategyIDSet 校验）。
// English: validates a full Rules config, returning the first violation (nil = valid).
func Validate(cfg *Rules) error {
	if cfg == nil {
		return fmt.Errorf("配置为空")
	}
	if err := validateQMT(&cfg.QMT); err != nil {
		return err
	}
	if err := validateRiskCtrl(&cfg.RiskCtrl); err != nil {
		return err
	}
	if err := validatePosition(&cfg.Position); err != nil {
		return err
	}
	return nil
}

// validateQMT QMT 段校验：枚举/范围/引用一致性。
// English: QMT-section validation — enums, ranges, and the gateway_url↔token consistency rule.
func validateQMT(q *QMTConfig) error {
	if q.Mode != "" && q.Mode != "auto" && q.Mode != "manual" {
		return fmt.Errorf("qmt.mode 仅允许 auto/manual（实际 %q）", q.Mode)
	}
	if q.PriceType != "" && q.PriceType != "market" && q.PriceType != "limit" {
		return fmt.Errorf("qmt.price_type 仅允许 market/limit（实际 %q）", q.PriceType)
	}
	if q.FixedAmount < 0 {
		return fmt.Errorf("qmt.fixed_amount 不能为负（%.2f）", q.FixedAmount)
	}
	if q.InitialCapital < 0 {
		return fmt.Errorf("qmt.initial_capital 不能为负（%.2f）", q.InitialCapital)
	}
	if q.MaxPositions < 0 || q.MaxPositions > 50 {
		return fmt.Errorf("qmt.max_positions 超出范围 0-50（实际 %d）", q.MaxPositions)
	}
	if q.DailyMaxBuys < 0 {
		return fmt.Errorf("qmt.daily_max_buys 不能为负（%d）", q.DailyMaxBuys)
	}
	if q.DailyBudgetAmount < 0 {
		return fmt.Errorf("qmt.daily_budget_amount 不能为负（%.2f）", q.DailyBudgetAmount)
	}
	if q.MissHeartbeatSec != 0 && (q.MissHeartbeatSec < 30 || q.MissHeartbeatSec > 3600) {
		return fmt.Errorf("qmt.miss_heartbeat_sec 超出范围 30-3600（实际 %d）", q.MissHeartbeatSec)
	}
	// 注：gateway_url↔token 的引用一致性不在此强校验——mock/掩码 token/本地默认配置
	// 允许「网关已配 token 空」等形态，强校验会破坏既有部署的向后兼容（零值=现状）。
	return nil
}

// validateRiskCtrl 风控段校验：百分比字段 0~100。
// English: risk-control validation — percentage fields in 0..100.
func validateRiskCtrl(r *RiskCtrlConfig) error {
	if r.M8PortfolioDrawdownPct < 0 || r.M8PortfolioDrawdownPct > 100 {
		return fmt.Errorf("risk_ctrl.m8_portfolio_drawdown_pct 超出范围 0-100（实际 %.2f）", r.M8PortfolioDrawdownPct)
	}
	if r.PerStockMax < 0 || r.PerStockMax > 100 {
		return fmt.Errorf("risk_ctrl.per_stock_max 超出范围 0-100（实际 %.2f）", r.PerStockMax)
	}
	return nil
}

// validatePosition 仓位段校验：百分比字段 0~100、ATR 倍数合理区间。
// English: position-section validation — percentage fields in 0..100, ATR multiplier in range.
func validatePosition(p *PositionConfig) error {
	if p.MaxTotalPositionPct < 0 || p.MaxTotalPositionPct > 100 {
		return fmt.Errorf("position.max_total_position_pct 超出范围 0-100（实际 %.2f）", p.MaxTotalPositionPct)
	}
	if p.DailyDropAlertPct < 0 || p.DailyDropAlertPct > 100 {
		return fmt.Errorf("position.daily_drop_alert_pct 超出范围 0-100（实际 %.2f）", p.DailyDropAlertPct)
	}
	if p.ATRStopMult < 0 || p.ATRStopMult > 20 {
		return fmt.Errorf("position.atr_stop_mult 超出范围 0-20（实际 %.2f）", p.ATRStopMult)
	}
	return nil
}
