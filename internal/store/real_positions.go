// Package store — SQLite 历史数据存储层。
// real_positions.go：实盘账本（AUTO_TRADING_PLAN M1）的真实持仓/委托/成交存取。
// 数据源为国内 QMT 网关回报（全量对账 positions 事件 + 增量 trade/order 事件），
// 与纸面账本（report.Report JSON）完全独立（双账本并存）。
// English: real-book persistence for AUTO_TRADING_PLAN M1 — real positions/orders/fills written from
// the domestic QMT gateway reports (full reconciliation + incremental trade/order events), fully
// independent of the paper book (report.Report JSON). Dual ledgers coexist.
package store

import (
	"database/sql"
	"fmt"
	"time"
)

// RealPosition 实盘持仓行。
// （RealPosition is one row of the live book.）
type RealPosition struct {
	TsCode       string  `json:"ts_code"`
	Name         string  `json:"name"`
	Qty          int     `json:"qty"`
	CostPrice    float64 `json:"cost_price"`
	Amount       float64 `json:"amount"`
	HighestPrice float64 `json:"highest_price"`
	Strategy     string  `json:"strategy"`
	SignalID     string  `json:"signal_id"`
	UpdatedAt    string  `json:"updated_at"`
	// UserID §GAP1.10 多租户归属：网关回报 user_id 写入；空串=遗留全局行（对所有人可见，
	// 兼容单老板存量部署）。English: §GAP1.10 owner account; empty = legacy global row.
	UserID string `json:"user_id,omitempty"`
}

// RealOrder 实盘委托单行（signal_id 为幂等键）。
// （RealOrder is one live order ticket; signal_id is the idempotency key.）
type RealOrder struct {
	OrderID   string  `json:"order_id"`
	SignalID  string  `json:"signal_id"`
	Code      string  `json:"code"`
	Side      string  `json:"side"`
	Status    string  `json:"status"`
	Price     float64 `json:"price"`
	Qty       int     `json:"qty"`
	CreatedAt string  `json:"created_at"`
}

// RealFill 实盘成交回报行。
// （RealFill is one live fill report.）
type RealFill struct {
	ID       int64   `json:"id"`
	OrderID  string  `json:"order_id"`
	Code     string  `json:"code"`
	Side     string  `json:"side"`
	Price    float64 `json:"price"`
	Qty      int     `json:"qty"`
	Amount   float64 `json:"amount"`
	TradedAt string  `json:"traded_at"`
	SignalID string  `json:"signal_id"`
}

// UpsertRealPositions 全量对账写入：以网关推送的持仓集合为准，逐条 upsert 并移除已不在集合内的旧持仓。
// 返回替换后的持仓数量。English: full-reconciliation write — upserts every gateway position and drops
// rows absent from the push; returns the resulting position count.
func (d *DB) UpsertRealPositions(pos []RealPosition) (int, error) {
	tx, err := d.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	for _, p := range pos {
		if p.UpdatedAt == "" {
			p.UpdatedAt = time.Now().Format("2006-01-02 15:04:05")
		}
		_, err := tx.Exec(`INSERT INTO real_positions
			(ts_code, name, qty, cost_price, amount, highest_price, strategy, signal_id, updated_at, user_id)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(ts_code) DO UPDATE SET
				name=excluded.name, qty=excluded.qty, cost_price=excluded.cost_price,
				amount=excluded.amount, strategy=excluded.strategy, updated_at=excluded.updated_at,
				user_id=excluded.user_id,
				highest_price=CASE WHEN excluded.highest_price > real_positions.highest_price
					THEN excluded.highest_price ELSE real_positions.highest_price END`,
			p.TsCode, p.Name, p.Qty, p.CostPrice, p.Amount, p.HighestPrice, p.Strategy, p.SignalID, p.UpdatedAt, p.UserID)
		if err != nil {
			return 0, fmt.Errorf("upsert real position %s: %w", p.TsCode, err)
		}
	}
	// 移除网关推送中已不存在的持仓（全量对账语义）。
	if len(pos) == 0 {
		if _, err := tx.Exec(`DELETE FROM real_positions`); err != nil {
			return 0, err
		}
	} else {
		codes := make([]any, 0, len(pos))
		placeholders := ""
		for i, p := range pos {
			codes = append(codes, p.TsCode)
			if i > 0 {
				placeholders += ","
			}
			placeholders += "?"
		}
		if _, err := tx.Exec(`DELETE FROM real_positions WHERE ts_code NOT IN (`+placeholders+`)`, codes...); err != nil {
			return 0, err
		}
	}
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM real_positions`).Scan(&n); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return n, nil
}

// RealPositions 返回全部实盘持仓（含成本/最高价），供决策层读取。
// （RealPositions returns every live position for the decision layer.）
func (d *DB) RealPositions() ([]RealPosition, error) {
	rows, err := d.db.Query(`SELECT ts_code, name, qty, cost_price, amount, highest_price,
		strategy, signal_id, updated_at, COALESCE(user_id,'') FROM real_positions ORDER BY ts_code`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RealPosition
	for rows.Next() {
		var p RealPosition
		if err := rows.Scan(&p.TsCode, &p.Name, &p.Qty, &p.CostPrice, &p.Amount,
			&p.HighestPrice, &p.Strategy, &p.SignalID, &p.UpdatedAt, &p.UserID); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// RealPositionsForUser §GAP1.10 按账号过滤实盘持仓：返回 user_id 匹配或遗留全局行（user_id=”）。
// English: §GAP1.10 — positions owned by the account plus legacy global (empty user_id) rows.
func (d *DB) RealPositionsForUser(userID string) ([]RealPosition, error) {
	rows, err := d.db.Query(`SELECT ts_code, name, qty, cost_price, amount, highest_price,
		strategy, signal_id, updated_at, COALESCE(user_id,'') FROM real_positions
		WHERE user_id = '' OR user_id = ? ORDER BY ts_code`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RealPosition
	for rows.Next() {
		var p RealPosition
		if err := rows.Scan(&p.TsCode, &p.Name, &p.Qty, &p.CostPrice, &p.Amount,
			&p.HighestPrice, &p.Strategy, &p.SignalID, &p.UpdatedAt, &p.UserID); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// RealPositionByCode 返回单只实盘持仓（不存在返回 sql.ErrNoRows）。
// （RealPositionByCode returns one live position, sql.ErrNoRows when absent.）
func (d *DB) RealPositionByCode(code string) (RealPosition, error) {
	var p RealPosition
	err := d.db.QueryRow(`SELECT ts_code, name, qty, cost_price, amount, highest_price,
		strategy, signal_id, updated_at FROM real_positions WHERE ts_code=?`, code).
		Scan(&p.TsCode, &p.Name, &p.Qty, &p.CostPrice, &p.Amount,
			&p.HighestPrice, &p.Strategy, &p.SignalID, &p.UpdatedAt)
	return p, err
}

// ApplyRealFill 成交回报应用到持仓：
//   - 买入：首次建仓（成本=成交价）或加仓（合并加权平均成本），并更新最高价；
//   - 卖出：减仓 qty；全部卖出后 qty<=0（下次全量对账/查询时清除）。
//
// 成交回报同时写 fills 表。English: applies a gateway fill to the book: buys open/add with weighted
// average cost, sells reduce qty (cleared when qty<=0); the fill row is also persisted.
func (d *DB) ApplyRealFill(f RealFill) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var p RealPosition
	err = tx.QueryRow(`SELECT ts_code, name, qty, cost_price, amount, highest_price,
		strategy, signal_id FROM real_positions WHERE ts_code=?`, f.Code).
		Scan(&p.TsCode, &p.Name, &p.Qty, &p.CostPrice, &p.Amount, &p.HighestPrice, &p.Strategy, &p.SignalID)
	switch {
	case err == sql.ErrNoRows:
		// 首次成交：建仓（卖出空仓视为 no-op，仅记录 fills，不建行）
		err = nil
		if f.Side == "买入" {
			_, err = tx.Exec(`INSERT INTO real_positions
				(ts_code, name, qty, cost_price, amount, highest_price, strategy, signal_id, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				f.Code, "", f.Qty, f.Price, f.Price*float64(f.Qty), f.Price, "", f.SignalID,
				time.Now().Format("2006-01-02 15:04:05"))
		}
	case err == nil:
		now := time.Now().Format("2006-01-02 15:04:05")
		if f.Side == "买入" {
			newQty := p.Qty + f.Qty
			var newCost float64
			if newQty > 0 {
				newCost = (p.Amount + f.Price*float64(f.Qty)) / float64(newQty)
			}
			hi := p.HighestPrice
			if f.Price > hi {
				hi = f.Price
			}
			_, err = tx.Exec(`UPDATE real_positions SET qty=?, cost_price=?, amount=?,
				highest_price=?, updated_at=? WHERE ts_code=?`,
				newQty, newCost, newCost*float64(newQty), hi, now, f.Code)
		} else {
			newQty := p.Qty - f.Qty
			if newQty < 0 {
				newQty = 0
			}
			_, err = tx.Exec(`UPDATE real_positions SET qty=?, amount=?, updated_at=? WHERE ts_code=?`,
				newQty, float64(newQty)*p.CostPrice, now, f.Code)
		}
	}
	if err != nil {
		return fmt.Errorf("apply fill %s %s: %w", f.Code, f.Side, err)
	}
	if _, err := tx.Exec(`DELETE FROM real_positions WHERE qty <= 0`); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO fills (order_id, code, side, price, qty, amount, traded_at, signal_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		f.OrderID, f.Code, f.Side, f.Price, f.Qty, f.Amount, f.TradedAt, f.SignalID); err != nil {
		return err
	}
	return tx.Commit()
}

// UpsertRealOrder 写入/更新委托单；signal_id 冲突时返回已存在（幂等，不重复下单）。
// 返回 (是否已存在, 错误)。English: upserts an order; a signal_id conflict means the order already
// exists (idempotent — never double-sends). Returns (alreadyExisted, err).
func (d *DB) UpsertRealOrder(o RealOrder) (bool, error) {
	res, err := d.db.Exec(`INSERT OR IGNORE INTO orders
		(order_id, signal_id, code, side, status, price, qty, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		o.OrderID, o.SignalID, o.Code, o.Side, o.Status, o.Price, o.Qty, o.CreatedAt)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if n == 0 {
		return true, nil
	}
	return false, nil
}

// UpdateRealOrderStatus 更新委托单状态（已报/已撤/已成）。
// （UpdateRealOrderStatus updates an order's status.）
func (d *DB) UpdateRealOrderStatus(orderID, status string) error {
	_, err := d.db.Exec(`UPDATE orders SET status=? WHERE order_id=?`, status, orderID)
	return err
}

// UpdateRealOrderBySignalID 下单回填：把 pend:<signal_id> 占位行的 order_id 替换为网关真实委托号并更新状态。
// §GAP 修复：此前占位行 order_id 恒为空串，与 order_id 主键冲突导致第二笔起的新单被
// INSERT OR IGNORE 误判为重复（静默不下单），且按网关单号 UPDATE 也永不命中。
// English: backfills the pending ticket (keyed by signal_id) with the gateway-assigned order id.
// English: §GAP fix — placeholder rows previously stored an empty order_id, colliding on the primary
// key so every later new order was swallowed as a "duplicate", and the status update by gateway id
// never matched.
func (d *DB) UpdateRealOrderBySignalID(signalID, orderID, status string) error {
	_, err := d.db.Exec(`UPDATE orders SET order_id=?, status=? WHERE signal_id=?`, orderID, status, signalID)
	return err
}

// RealOrders 返回全部实盘委托单（倒序）。
// （RealOrders returns all live orders, newest first.）
func (d *DB) RealOrders() ([]RealOrder, error) {
	rows, err := d.db.Query(`SELECT order_id, signal_id, code, side, status, price, qty, created_at
		FROM orders ORDER BY created_at DESC, order_id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RealOrder
	for rows.Next() {
		var o RealOrder
		if err := rows.Scan(&o.OrderID, &o.SignalID, &o.Code, &o.Side, &o.Status, &o.Price, &o.Qty, &o.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// RealFills 返回全部实盘成交回报（倒序）。
// （RealFills returns all live fills, newest first.）
func (d *DB) RealFills() ([]RealFill, error) {
	rows, err := d.db.Query(`SELECT id, order_id, code, side, price, qty, amount, traded_at, signal_id
		FROM fills ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RealFill
	for rows.Next() {
		var f RealFill
		if err := rows.Scan(&f.ID, &f.OrderID, &f.Code, &f.Side, &f.Price, &f.Qty, &f.Amount, &f.TradedAt, &f.SignalID); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// ClearRealBook 清空实盘账本（对账重建/演示用）。
// （ClearRealBook wipes the live book.）
func (d *DB) ClearRealBook() error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, t := range []string{"real_positions", "orders", "fills"} {
		if _, err := tx.Exec("DELETE FROM " + t); err != nil {
			return err
		}
	}
	return tx.Commit()
}
