// risk_gates.go — §WS-C 风控闸口命中记录与日内已实现盈亏口径。
// 提供 risk_gates 表的命中计数（每 user+日+闸 一行，命中自增，供 SLO/审计/UI 卡片），
// 以及日内已实现盈亏（今日卖出成交 vs 成本）与总资产口径，供 RiskGate 熔断/集中度闸消费。
// English: §WS-C risk-gate hit recording + intraday realized-P&L / total-assets accounting for the
// risk gate (realized-loss circuit breaker and single-stock concentration).
package store

import (
	"fmt"
	"time"
)

// RiskGateHit 单日单闸命中计数行（API/UI 展示用）。
// English: one (user, day, gate) hit-count row.
type RiskGateHit struct {
	UserID     string `json:"user_id"`
	TradeDate  string `json:"trade_date"`
	Gate       string `json:"gate"`
	Hits       int    `json:"hits"`
	LastReason string `json:"last_reason"`
	UpdatedAt  string `json:"updated_at"`
}

// RecordRiskGate 记录一次风控闸命中（幂等自增：每 user+日+闸 一行）。
// English: records one risk-gate hit, incrementing the per (user, day, gate) counter idempotently.
func (d *DB) RecordRiskGate(userID, tradeDate, gate, reason string) error {
	now := time.Now().Format("2006-01-02 15:04:05")
	_, err := d.db.Exec(`INSERT INTO risk_gates (user_id, trade_date, gate, hits, last_reason, updated_at)
		VALUES (?, ?, ?, 1, ?, ?)
		ON CONFLICT(user_id, trade_date, gate) DO UPDATE SET
			hits = hits + 1,
			last_reason = excluded.last_reason,
			updated_at = excluded.updated_at`,
		userID, tradeDate, gate, reason, now)
	return err
}

// RiskGateHits 返回某用户某交易日的全部闸口命中计数（gate → hits）。
// English: returns all risk-gate hit counts for one user/day as gate → hits.
func (d *DB) RiskGateHits(userID, tradeDate string) (map[string]int, error) {
	rows, err := d.db.Query(`SELECT gate, hits FROM risk_gates WHERE user_id = ? AND trade_date = ?`, userID, tradeDate)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var g string
		var h int
		if err := rows.Scan(&g, &h); err != nil {
			return nil, err
		}
		out[g] = h
	}
	return out, rows.Err()
}

// RiskGateDay 返回某交易日的全量命中明细（限行，最新在前）。
// English: returns all risk-gate hit rows for a day, newest first, capped by limit.
func (d *DB) RiskGateDay(tradeDate string, limit int) ([]RiskGateHit, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := d.db.Query(`SELECT user_id, trade_date, gate, hits, last_reason, updated_at
		FROM risk_gates WHERE trade_date = ? ORDER BY id DESC LIMIT ?`, tradeDate, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RiskGateHit
	for rows.Next() {
		var h RiskGateHit
		if err := rows.Scan(&h.UserID, &h.TradeDate, &h.Gate, &h.Hits, &h.LastReason, &h.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// TodayRealizedPnl 日内已实现盈亏（元，正=盈利，负=亏损）：
// Σ 今日卖出成交 (fillPrice − 成本) × 数量。成本取该 code 当前持仓 CostPrice（已清仓则回落
// 到今日买入均价兜底）；成本不可知（无持仓且无买入成交）时该笔 fail-open 不计入——
// 熔断闸宁可漏计也不因数据缺口误熔断。English: intraday realized P&L in yuan — Σ today's sell fills
// (fillPrice − cost) × qty; cost = the current position's CostPrice, falling back to today's average
// buy price when the position is gone; unknowable cost fails open (not counted) so the breaker never
// trips on a data gap.
func (d *DB) TodayRealizedPnl(userID, day string) (float64, error) {
	fills, err := d.ListFillsByDay(userID, day)
	if err != nil {
		return 0, err
	}
	var pnl float64
	for _, f := range fills {
		if f.Side != "卖出" || f.Qty <= 0 {
			continue
		}
		cost := d.costBasisFor(userID, f.Code, day)
		if cost <= 0 {
			continue // fail-open：成本不可知不计入
		}
		pnl += (f.Price - cost) * float64(f.Qty)
	}
	return pnl, nil
}

// costBasisFor 某 code 的成本价：优先当前持仓 CostPrice；持仓已清时回落今日该 code 买入成交均价。
// English: cost basis for a code — current position CostPrice first; falls back to today's average buy
// fill price when the position has been fully closed.
func (d *DB) costBasisFor(userID, code, day string) float64 {
	if p, err := d.RealPositionByCodeForUser(userID, code); err == nil && p.Qty > 0 && p.CostPrice > 0 {
		return p.CostPrice
	}
	fills, err := d.ListFillsByDay(userID, day)
	if err != nil {
		return 0
	}
	var buyAmt, buyQty float64
	for _, f := range fills {
		if f.Side == "买入" && f.Code == code && f.Qty > 0 {
			buyAmt += f.Price * float64(f.Qty)
			buyQty += float64(f.Qty)
		}
	}
	if buyQty <= 0 {
		return 0
	}
	return buyAmt / buyQty
}

// TotalAssets 当前总资产（元）：可用现金（券商已回报时）+ Σ持仓市值（现价优先，缺失回落成本价）。
// 券商可用资金未回报/不可信时仅计持仓市值（现金口径缺失），由调用方决定是否跳过集中度闸。
// English: current total assets (yuan) — available broker cash (when reported) + Σ position market
// value (live price preferred, cost price fallback). When broker cash is unreported only the held
// value is returned, letting callers skip the concentration gate.
func (d *DB) TotalAssets(userID string) (float64, error) {
	total := 0.0
	if acc, err := d.GetRealAccount(userID); err == nil && acc.AvailableCash > 0 {
		total += acc.AvailableCash
	}
	poses, err := d.RealPositionsForUser(userID)
	if err != nil {
		return 0, fmt.Errorf("read real positions: %w", err)
	}
	for _, p := range poses {
		price := p.CurPrice
		if price <= 0 {
			price = p.CostPrice
		}
		if price > 0 {
			total += price * float64(p.Qty)
		}
	}
	return total, nil
}
