// shadow.go — §WS-G Shadow 执行器落账：staging 影子引擎的决策留痕（shadow_orders 表）。
// signal_id 唯一键幂等，重复决策不重复计；按日可查，供"与线上同输入同输出"回放对比。
// English: §WS-G shadow-executor bookkeeping — staging decisions recorded in shadow_orders, deduped on
// signal_id, queryable by day for replay parity checks against production.
package store

import "time"

// ShadowOrder 影子下单记录（staging 引擎的决策快照，永不发往券商）。
// English: a shadow order record (the staging engine's decision snapshot, never sent to a broker).
type ShadowOrder struct {
	SignalID   string  `json:"signal_id"`
	Code       string  `json:"code"`
	Name       string  `json:"name"`
	Strategy   string  `json:"strategy"`
	StrategyID string  `json:"strategy_id,omitempty"`
	Side       string  `json:"side"`
	Price      float64 `json:"price"`
	Qty        int     `json:"qty"`
	Amount     float64 `json:"amount"`
	UserID     string  `json:"user_id,omitempty"`
	CreatedAt  string  `json:"created_at"`
}

// InsertShadowOrder 写入影子下单（signal_id 幂等：已存在返回 existed=true，不重复计）。
// English: inserts a shadow order, deduped on signal_id (returns existed=true when already recorded).
func (d *DB) InsertShadowOrder(o ShadowOrder) (existed bool, err error) {
	if o.CreatedAt == "" {
		o.CreatedAt = time.Now().Format("2006-01-02 15:04:05")
	}
	res, err := d.db.Exec(`INSERT OR IGNORE INTO shadow_orders
		(user_id, signal_id, code, name, strategy, strategy_id, side, price, qty, amount, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		o.UserID, o.SignalID, o.Code, o.Name, o.Strategy, o.StrategyID,
		o.Side, o.Price, o.Qty, o.Amount, o.CreatedAt)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 0, nil
}

// ShadowOrdersForDay 返回某用户某日的影子下单明细（最新在前，limit 上限）。
// English: returns a user's shadow orders for a day, newest first, capped by limit.
func (d *DB) ShadowOrdersForDay(userID, day string, limit int) ([]ShadowOrder, error) {
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	rows, err := d.db.Query(`SELECT signal_id, code, name, strategy, COALESCE(strategy_id,''), side,
		price, qty, amount, COALESCE(user_id,''), created_at
		FROM shadow_orders
		WHERE (user_id = ? OR user_id = '') AND created_at LIKE ?
		ORDER BY id DESC LIMIT ?`, userID, day+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ShadowOrder
	for rows.Next() {
		var o ShadowOrder
		if err := rows.Scan(&o.SignalID, &o.Code, &o.Name, &o.Strategy, &o.StrategyID,
			&o.Side, &o.Price, &o.Qty, &o.Amount, &o.UserID, &o.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}
