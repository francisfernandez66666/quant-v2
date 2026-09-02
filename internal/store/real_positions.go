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
	"log"
	"strings"
	"time"
)

// RealPosition 实盘持仓行。
// （RealPosition is one row of the live book.）
type RealPosition struct {
	TsCode       string  `json:"ts_code"`       // TS代码
	Name         string  `json:"name"`          // 名称
	Qty          int     `json:"qty"`           // 数量
	CostPrice    float64 `json:"cost_price"`    // 成本价
	Amount       float64 `json:"amount"`        // 成交额
	HighestPrice float64 `json:"highest_price"` // 持仓以来最高价（加仓/格局判定用）
	Strategy     string  `json:"strategy"`      // 战法
	SignalID     string  `json:"signal_id"`     // 信号ID
	UpdatedAt    string  `json:"updated_at"`    // 更新时间
	// CurPrice §前端实盘持仓现价/盈亏展示用：由 handleRealPositions 装配实时行情快照填充，
	// 网关回报本身不含实时价（仅 cost_price）。English: live price for the real-position table;
	// filled from the quote snapshot by handleRealPositions, not carried in gateway reports.
	CurPrice float64 `json:"cur_price"` // 实时现价（浮动盈亏计算用）
	// UserID §GAP1.10 多租户归属：网关回报 user_id 写入；空串=遗留全局行（对所有人可见，
	// 兼容单老板存量部署）。English: §GAP1.10 owner account; empty = legacy global row.
	UserID string `json:"user_id,omitempty"` // 归属用户
}

// RealOrder 实盘委托单行（signal_id 为幂等键）。
// （RealOrder is one live order ticket; signal_id is the idempotency key.）
type RealOrder struct {
	OrderID   string  `json:"order_id"`          // 订单ID
	SignalID  string  `json:"signal_id"`         // 信号ID
	Code      string  `json:"code"`              // 代码
	Side      string  `json:"side"`              // 方向
	Status    string  `json:"status"`            // 状态
	Price     float64 `json:"price"`             // 价格
	Qty       int     `json:"qty"`               // 数量
	CreatedAt string  `json:"created_at"`        // 创建时间
	UserID    string  `json:"user_id,omitempty"` // §W2-10 归属账号（空=遗留全局行）
}

// RealFill 实盘成交回报行。
// （RealFill is one live fill report.）
type RealFill struct {
	ID       int64   `json:"id"`                // 自增 ID
	OrderID  string  `json:"order_id"`          // 订单ID
	Code     string  `json:"code"`              // 代码
	Name     string  `json:"name"`              // 名称（成交回报携带，建仓回填）
	Side     string  `json:"side"`              // 方向
	Price    float64 `json:"price"`             // 价格
	Qty      int     `json:"qty"`               // 数量
	Amount   float64 `json:"amount"`            // 成交额
	TradedAt string  `json:"traded_at"`         // 成交时间
	SignalID string  `json:"signal_id"`         // 信号ID
	UserID   string  `json:"user_id,omitempty"` // §W2-10 归属账号
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
		// §P0-1 多租户：带归属账号的对账先声明遗留全局行，避免复合主键冲突产生重复行。
		if p.UserID != "" {
			if _, err := tx.Exec(`UPDATE real_positions SET user_id=? WHERE ts_code=? AND user_id=''`, p.UserID, p.TsCode); err != nil {
				return 0, fmt.Errorf("claim legacy position %s: %w", p.TsCode, err)
			}
		}
		_, err := tx.Exec(`INSERT INTO real_positions
			(ts_code, name, qty, cost_price, amount, highest_price, strategy, signal_id, updated_at, user_id)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(ts_code, user_id) DO UPDATE SET
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
	// §修复 R9（2026-08-29）：原 len(pos)==0 分支是 `DELETE FROM real_positions` 全表删除，
	// 多租户下任一账号推送空快照即清掉所有账号持仓（清库炸弹）。现按 user_id 作用域删除：
	// 仅当 pos 含归属账号时才限定到这些账号行；无归属账号(旧单租户/测试)保留原行为。
	userSet := make(map[string]bool)
	for _, p := range pos {
		if p.UserID != "" {
			userSet[p.UserID] = true
		}
	}
	if len(pos) == 0 {
		if len(userSet) == 0 {
			// 旧单租户 / 测试：保留原整表清空语义
			if _, err := tx.Exec(`DELETE FROM real_positions`); err != nil {
				return 0, err
			}
		} else {
			// 多租户空快照：仅清这些账号行，绝不动他人持仓
			args := make([]any, 0, len(userSet))
			ph := ""
			for u := range userSet {
				if ph != "" {
					ph += ","
				}
				ph += "?"
				args = append(args, u)
			}
			if _, err := tx.Exec(`DELETE FROM real_positions WHERE user_id IN (`+ph+`)`, args...); err != nil {
				return 0, err
			}
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
		if len(userSet) == 0 {
			// 旧单租户 / 测试：原整表 NOT IN 语义
			if _, err := tx.Exec(`DELETE FROM real_positions WHERE ts_code NOT IN (`+placeholders+`)`, codes...); err != nil {
				return 0, err
			}
		} else {
			// 多租户：仅删这些账号、且不在推送集合内的行
			args := make([]any, 0, len(codes)+len(userSet))
			ph := ""
			for u := range userSet {
				if ph != "" {
					ph += ","
				}
				ph += "?"
				args = append(args, u)
			}
			args = append(args, codes...)
			if _, err := tx.Exec(`DELETE FROM real_positions WHERE user_id IN (`+ph+`) AND ts_code NOT IN (`+placeholders+`)`, args...); err != nil {
				return 0, err
			}
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

// ReconcilePositionsForUser §R3-8 P1-G 用户隔离的全量对账写入（Controller.Reconcile 专用）：
//   - 每行打 UserID 归属后 upsert；
//   - 删除范围限定在 本账号行 ∪ 遗留全局行（user_id=”）——绝不动其他账号的 scoped 行
//     （旧 UpsertRealPositions 空集合分支是 DELETE FROM real_positions 全表，多租户下是清库炸弹）；
//   - pos 为空 = 网关全平，调用方必须已做「通道在线」守卫（网关 /state 断连时也返回空列表，
//     不可信快照禁止清账）。
//
// English: R3-8 P1-G — user-scoped full reconciliation write: stamps ownership on every row,
// deletes only this account's rows plus legacy global rows; never touches other accounts' rows.
// An empty pos means "gateway flat" — the caller must have verified the channel is connected.
func (d *DB) ReconcilePositionsForUser(userID string, pos []RealPosition) (int, error) {
	tx, err := d.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	for _, p := range pos {
		if p.UpdatedAt == "" {
			p.UpdatedAt = time.Now().Format("2006-01-02 15:04:05")
		}
		if userID != "" {
			p.UserID = userID
		}
		// §P0-1 多租户主键：同一股票先声明遗留全局行，避免复合主键冲突产生重复行。
		// English: claim any legacy global row for this account before upserting.
		if userID != "" {
			if _, err := tx.Exec(`UPDATE real_positions SET user_id=? WHERE ts_code=? AND user_id=''`, userID, p.TsCode); err != nil {
				return 0, fmt.Errorf("claim legacy position %s: %w", p.TsCode, err)
			}
		}
		_, err := tx.Exec(`INSERT INTO real_positions
			(ts_code, name, qty, cost_price, amount, highest_price, strategy, signal_id, updated_at, user_id)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(ts_code, user_id) DO UPDATE SET
				name=excluded.name, qty=excluded.qty, cost_price=excluded.cost_price,
				amount=excluded.amount, strategy=excluded.strategy, signal_id=excluded.signal_id,
				updated_at=excluded.updated_at, user_id=excluded.user_id,
				highest_price=CASE WHEN excluded.highest_price > real_positions.highest_price
					THEN excluded.highest_price ELSE real_positions.highest_price END`,
			p.TsCode, p.Name, p.Qty, p.CostPrice, p.Amount, p.HighestPrice, p.Strategy, p.SignalID, p.UpdatedAt, p.UserID)
		if err != nil {
			return 0, fmt.Errorf("reconcile real position %s: %w", p.TsCode, err)
		}
	}
	// 清除本账号范围内已不在网关快照中的行。
	if len(pos) == 0 {
		if _, err := tx.Exec(`DELETE FROM real_positions WHERE user_id = ?`, userID); err != nil {
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
		q := `DELETE FROM real_positions WHERE ts_code NOT IN (` + placeholders + `) AND user_id = ?`
		codes = append(codes, userID)
		if _, err := tx.Exec(q, codes...); err != nil {
			return 0, err
		}
	}
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM real_positions WHERE user_id = '' OR user_id = ?`, userID).Scan(&n); err != nil {
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
// ⚠️ 多账号部署下应使用 RealPositionByCodeForUser；本函数保留以兼容遗留单租户调用。
// （RealPositionByCode returns one live position, sql.ErrNoRows when absent.）
func (d *DB) RealPositionByCode(code string) (RealPosition, error) {
	var p RealPosition
	err := d.db.QueryRow(`SELECT ts_code, name, qty, cost_price, amount, highest_price,
		strategy, signal_id, updated_at, COALESCE(user_id,'') FROM real_positions WHERE ts_code=?`, code).
		Scan(&p.TsCode, &p.Name, &p.Qty, &p.CostPrice, &p.Amount,
			&p.HighestPrice, &p.Strategy, &p.SignalID, &p.UpdatedAt, &p.UserID)
	return p, err
}

// RealPositionByCodeForUser §P0-3 按账号返回单只实盘持仓；遗留全局行（user_id=”）对查询账号可见。
// English: user-scoped single position lookup; legacy global rows are visible to any caller.
func (d *DB) RealPositionByCodeForUser(userID, code string) (RealPosition, error) {
	var p RealPosition
	err := d.db.QueryRow(`SELECT ts_code, name, qty, cost_price, amount, highest_price,
		strategy, signal_id, updated_at, COALESCE(user_id,'') FROM real_positions
		WHERE ts_code=? AND (user_id = '' OR user_id = ?)`, code, userID).
		Scan(&p.TsCode, &p.Name, &p.Qty, &p.CostPrice, &p.Amount,
			&p.HighestPrice, &p.Strategy, &p.SignalID, &p.UpdatedAt, &p.UserID)
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

	// §P0-1 多租户：成交前先声明遗留全局行，防止后续 INSERT 产生重复。
	if f.UserID != "" {
		if _, err := tx.Exec(`UPDATE real_positions SET user_id=? WHERE ts_code=? AND user_id=''`, f.UserID, f.Code); err != nil {
			return fmt.Errorf("claim legacy position before fill %s: %w", f.Code, err)
		}
	}

	var p RealPosition
	err = tx.QueryRow(`SELECT ts_code, name, qty, cost_price, amount, highest_price,
		strategy, signal_id, user_id FROM real_positions WHERE ts_code=? AND (user_id='' OR user_id=?)`, f.Code, f.UserID).
		Scan(&p.TsCode, &p.Name, &p.Qty, &p.CostPrice, &p.Amount, &p.HighestPrice, &p.Strategy, &p.SignalID, &p.UserID)
	switch {
	case err == sql.ErrNoRows:
		// 首次成交：建仓（卖出空仓视为 no-op，仅记录 fills，不建行）。
		// §实盘账户隔离：INSERT 必须写入 user_id，将该持仓归属到来源成交的账号，
		// 否则该持仓会落入 user_id='' 的遗留全局行，对所有账号可见，造成跨账号持仓泄漏。
		err = nil
		if f.Side == "买入" {
			// §修复 R8（2026-08-29）：建仓时回填成交携带的 name（此前硬编码空串，
			// 导致实盘持仓页个股名称为空）。
			_, err = tx.Exec(`INSERT INTO real_positions
				(ts_code, name, qty, cost_price, amount, highest_price, strategy, signal_id, updated_at, user_id)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				f.Code, f.Name, f.Qty, f.Price, f.Price*float64(f.Qty), f.Price, "", f.SignalID,
				time.Now().Format("2006-01-02 15:04:05"), f.UserID)
		}
	case err == nil:
		now := time.Now().Format("2006-01-02 15:04:05")
		// 统一归属到本次成交账号（遗留空行被声明后 p.UserID 已等于 f.UserID）。
		ownerID := f.UserID
		if ownerID == "" {
			ownerID = p.UserID
		}
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
			// §实盘账户隔离：加仓时一并回写 user_id，确保归属字段不被旧行残值覆盖。
			// §修复 R8：加仓时若原持仓 name 为空，用本次成交的 name 回填。
			_, err = tx.Exec(`UPDATE real_positions SET qty=?, cost_price=?, amount=?,
				highest_price=?, updated_at=?, user_id=?, name=CASE WHEN name='' OR name IS NULL THEN ? ELSE name END
				WHERE ts_code=? AND (user_id='' OR user_id=?)`,
				newQty, newCost, newCost*float64(newQty), hi, now, ownerID, f.Name, f.Code, f.UserID)
		} else {
			newQty := p.Qty - f.Qty
			if newQty < 0 {
				newQty = 0
			}
			// §实盘账户隔离：减仓时同样回写 user_id（减仓不改变归属，但保持写路径一致，
			// 防止历史上 user_id 为空的持仓在减仓后仍以全局行形态存在）。
			_, err = tx.Exec(`UPDATE real_positions SET qty=?, amount=?, updated_at=?, user_id=? WHERE ts_code=? AND (user_id='' OR user_id=?)`,
				newQty, float64(newQty)*p.CostPrice, now, ownerID, f.Code, f.UserID)
		}
	}
	if err != nil {
		return fmt.Errorf("apply fill %s %s: %w", f.Code, f.Side, err)
	}
	if _, err := tx.Exec(`DELETE FROM real_positions WHERE qty <= 0`); err != nil {
		return err
	}
	// §W2-10 流水行打归属账号；§W3-b 幂等唯一键 (order_id,traded_at,price,qty)：
	// 网关 outbox 对同笔回报重试时，唯一索引冲突 → 视为已入账的重复投递，整体事务回滚并返回幂等成功，
	// 持仓数量不再被二次累加（此前首尔侧零幂等，响应丢失即双倍记账）。
	if _, err := tx.Exec(`INSERT INTO fills (order_id, code, side, price, qty, amount, traded_at, signal_id, user_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		f.OrderID, f.Code, f.Side, f.Price, f.Qty, f.Amount, f.TradedAt, f.SignalID, f.UserID); err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed: fills.order_id") {
			log.Printf("[store] fills 幂等命中(重复回报): order=%s traded_at=%s qty=%d", f.OrderID, f.TradedAt, f.Qty)
			return tx.Rollback()
		}
		return err
	}
	return tx.Commit()
}

// UpsertRealOrder 写入/更新委托单；signal_id 冲突时返回已存在（幂等，不重复下单）。
// 返回 (是否已存在, 错误)。English: upserts an order; a signal_id conflict means the order already
// exists (idempotent — never double-sends). Returns (alreadyExisted, err).
func (d *DB) UpsertRealOrder(o RealOrder) (bool, error) {
	// §W2-10 租户列：委托行打归属账号（存量行为空串=遗留全局）
	res, err := d.db.Exec(`INSERT OR IGNORE INTO orders
		(order_id, signal_id, code, side, status, price, qty, created_at, user_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		o.OrderID, o.SignalID, o.Code, o.Side, o.Status, o.Price, o.Qty, o.CreatedAt, o.UserID)
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

// SumFilledQty 汇总某账号某 signal_id 的累计成交数量（按 (user_id, signal_id) 过滤）。
// §修复 R6（2026-08-29）：自动卖出为日级幂等键，若首笔卖单仅部成（部分成交），同日同键重试会被
// 唯一键判 duplicate 而剩余仓位不再卖出。此处累计已成交数量，供补卖逻辑计算剩余可卖量。
func (d *DB) SumFilledQty(userID, signalID string) int {
	var total int
	if err := d.db.QueryRow(`SELECT COALESCE(SUM(qty),0) FROM fills WHERE user_id=? AND signal_id=?`,
		userID, signalID).Scan(&total); err != nil {
		return 0
	}
	return total
}

// UpdateRealOrderBySignalID 下单回填：把 pend:<signal_id> 占位行的 order_id 替换为网关真实委托号并更新状态。
// §GAP 修复：此前占位行 order_id 恒为空串，与 order_id 主键冲突导致第二笔起的新单被
// INSERT OR IGNORE 误判为重复（静默不下单），且按网关单号 UPDATE 也永不命中。
// §P0-2/P0-3 userID 为空时仅操作遗留全局行；非空时严格限定本账号，防止跨账号误更新。
// English: backfills the pending ticket (keyed by signal_id) with the gateway-assigned order id.
// English: §GAP fix — placeholder rows previously stored an empty order_id, colliding on the primary
// key so every later new order was swallowed as a "duplicate", and the status update by gateway id
// never matched.
func (d *DB) UpdateRealOrderBySignalID(userID, signalID, orderID, status string) error {
	if userID == "" {
		// 遗留全局行（user_id=''）兼容路径
		_, err := d.db.Exec(`UPDATE orders SET order_id=?, status=? WHERE signal_id=? AND user_id=''`, orderID, status, signalID)
		return err
	}
	_, err := d.db.Exec(`UPDATE orders SET order_id=?, status=? WHERE signal_id=? AND user_id=?`, orderID, status, signalID, userID)
	return err
}

// MarkRealOrderSendFailed §GAP2-W1 占位行降级：下单请求发送失败（网关超时/5xx/断连）时把占位行
// 从"已报"改为"发送失败"。带 status='已报' 守卫——若回报线程已把该单推进到 部分成交/已成 等状态
// （客户端超时但券商实际受理的场景），绝不回退真实进度，只降级仍停留在"已报"假象的行。
// 效果：①买入纪律统计不再把幽灵单计入当日预算/笔数；②同一 signal_id 重试可经
// ResetFailedRealOrder 放行，止损类自动单不会因首次网络抖动被封死一整天。
// §P0-3 userID 限定本账号作用域。
// English: §GAP2-W1 demotes a placeholder order from 已报 to 发送失败 after a send failure
// (gateway timeout / 5xx / disconnect). Guarded on status='已报' so fills that already arrived via
// report callbacks (broker accepted despite our timeout) are never rolled back. Effects: failed sends
// no longer pollute daily budget/count, and retrying the same signal_id is allowed via
// ResetFailedRealOrder — auto stop-losses can no longer be bricked for the whole day by one network blip.
func (d *DB) MarkRealOrderSendFailed(userID, signalID string) error {
	var res sql.Result
	var err error
	if userID == "" {
		res, err = d.db.Exec(`UPDATE orders SET status='发送失败' WHERE signal_id=? AND status='已报' AND user_id=''`, signalID)
	} else {
		res, err = d.db.Exec(`UPDATE orders SET status='发送失败' WHERE signal_id=? AND status='已报' AND user_id=?`, signalID, userID)
	}
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		log.Printf("[store] 委托 %s 发送失败，占位行已降级（可重试）", signalID)
	}
	return nil
}

// ResetFailedRealOrder §GAP2-W1 失败重试放行：把指定 signal_id 的"发送失败"行重置为"已报"。
// 仅当行存在且状态恰为"发送失败"时生效并返回 true；其余状态（已报/部分成交/已成/已撤）原样保留
// 并返回 false——即真正的重复下单仍然被唯一键拦截，只有确认失败过的单才允许再发一次。
// §P0-3 userID 限定本账号作用域。
// English: §GAP2-W1 retry gate — resets a 发送失败 ticket back to 已报 so the same signal_id may be
// re-sent. Returns true only when a failed row was actually reset; genuine duplicates (already
// reported/filled/cancelled) stay untouched and return false.
func (d *DB) ResetFailedRealOrder(userID, signalID string) (bool, error) {
	var res sql.Result
	var err error
	if userID == "" {
		res, err = d.db.Exec(`UPDATE orders SET status='已报' WHERE signal_id=? AND status='发送失败' AND user_id=''`, signalID)
	} else {
		res, err = d.db.Exec(`UPDATE orders SET status='已报' WHERE signal_id=? AND status='发送失败' AND user_id=?`, signalID, userID)
	}
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// RealOrders 返回全部实盘委托单（倒序）。
// （RealOrders returns all live orders, newest first.）
func (d *DB) RealOrders() ([]RealOrder, error) {
	rows, err := d.db.Query(`SELECT order_id, signal_id, code, side, status, price, qty, created_at,
		COALESCE(user_id,'') FROM orders ORDER BY created_at DESC, order_id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RealOrder
	for rows.Next() {
		var o RealOrder
		if err := rows.Scan(&o.OrderID, &o.SignalID, &o.Code, &o.Side, &o.Status, &o.Price, &o.Qty, &o.CreatedAt, &o.UserID); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// RealOrdersForUser §P0-3 按账号返回实盘委托单；遗留全局行（user_id=”）对查询账号可见。
// English: user-scoped live orders; legacy global rows are visible to any caller.
func (d *DB) RealOrdersForUser(userID string) ([]RealOrder, error) {
	rows, err := d.db.Query(`SELECT order_id, signal_id, code, side, status, price, qty, created_at,
		COALESCE(user_id,'') FROM orders WHERE user_id = '' OR user_id = ?
		ORDER BY created_at DESC, order_id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RealOrder
	for rows.Next() {
		var o RealOrder
		if err := rows.Scan(&o.OrderID, &o.SignalID, &o.Code, &o.Side, &o.Status, &o.Price, &o.Qty, &o.CreatedAt, &o.UserID); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// orderStatusRank §R4-4 委托状态单调推进秩：回报乱序/重放时绝不回退真实进度。
// 终态（已成/已撤/部撤/废单）秩最高；未报/待报视同初始已报。
// English: §R4-4 monotonic rank for order-report statuses so out-of-order/replayed reports
// never roll real progress back; terminal states rank highest.
func orderStatusRank(status string) int {
	switch status {
	case "已成", "已撤", "部撤", "废单":
		return 6
	case "部成待撤":
		return 4
	case "部成":
		return 3
	case "已报待撤":
		return 2
	case "已报":
		return 1
	default: // 未报/待报/未知 → 初始档
		return 1
	}
}

// AdvanceRealOrderStatus §R4-4 带单调守卫的委托状态推进（按 signal_id 定位）：
// 仅当回报状态的秩高于本地当前秩时才更新——修掉"回报 部成/已成/已撤 被 INSERT OR IGNORE
// 静默吞掉、本地永远停留已报"的状态机断链（该断链会让自动撤单误撤已成交单，资损级前置）。
// §P0-3 userID 限定本账号作用域；userID 为空时仅操作遗留全局行。
// 返回 (是否实际更新, 错误)。
// English: §R4-4 guarded status advance keyed by signal_id — updates only when the reported
// status outranks the local one, fixing the silent-swallow hole where 部成/已成/已撤 reports
// never landed (a prerequisite before any auto-cancel can be trusted).
func (d *DB) AdvanceRealOrderStatus(userID, signalID, status string) (bool, error) {
	var cur string
	var err error
	if userID == "" {
		err = d.db.QueryRow(`SELECT status FROM orders WHERE signal_id=? AND user_id=''`, signalID).Scan(&cur)
	} else {
		err = d.db.QueryRow(`SELECT status FROM orders WHERE signal_id=? AND user_id=?`, signalID, userID).Scan(&cur)
	}
	if err == sql.ErrNoRows {
		return false, nil // 本地无此单（如网侧重放的历史单）：交由调用方决定是否补插
	}
	if err != nil {
		return false, err
	}
	if orderStatusRank(status) <= orderStatusRank(cur) {
		return false, nil // 乱序/重放/回退：忽略
	}
	var res sql.Result
	if userID == "" {
		res, err = d.db.Exec(`UPDATE orders SET status=? WHERE signal_id=? AND user_id=''`, status, signalID)
	} else {
		res, err = d.db.Exec(`UPDATE orders SET status=? WHERE signal_id=? AND user_id=?`, status, signalID, userID)
	}
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// UpdateRealOrderStatusMonotonic §P0-4 撤单路径单调状态机：仅当目标状态秩高于当前秩时才更新。
// 防止"网关撤单响应晚于成交回报"时把 已成/已撤 回退为 已撤，或把部成回退为已报。
// userID 为空时仅操作遗留全局行。
// English: P0-4 monotonic cancel path — only updates when the target status outranks the current one,
// preventing a late cancel response from downgrading a filled/cancelled order.
func (d *DB) UpdateRealOrderStatusMonotonic(userID, orderID, status string) (bool, error) {
	var cur string
	var err error
	if userID == "" {
		err = d.db.QueryRow(`SELECT status FROM orders WHERE order_id=? AND user_id=''`, orderID).Scan(&cur)
	} else {
		err = d.db.QueryRow(`SELECT status FROM orders WHERE order_id=? AND user_id=?`, orderID, userID).Scan(&cur)
	}
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if orderStatusRank(status) <= orderStatusRank(cur) {
		return false, nil
	}
	var res sql.Result
	if userID == "" {
		res, err = d.db.Exec(`UPDATE orders SET status=? WHERE order_id=? AND user_id=''`, status, orderID)
	} else {
		res, err = d.db.Exec(`UPDATE orders SET status=? WHERE order_id=? AND user_id=?`, status, orderID, userID)
	}
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
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

// MigrateRealTablesIfEmpty 仅在 dst 实盘账本为空时，从 src（通常是旧 trading.db）整表拷贝
// real_positions / orders / fills / real_account，避免拆分 live.db 后存量实盘数据丢失。
// 幂等：dst 已有数据则直接跳过，返回 (false, nil)。English: one-time copy of the live book
// from src into dst when dst is empty (idempotent; skips when dst already holds rows).
func MigrateRealTablesIfEmpty(dst, src *DB) (bool, error) {
	if dst == nil || src == nil {
		return false, nil
	}
	var n int
	if err := dst.db.QueryRow(`SELECT COUNT(*) FROM real_positions`).Scan(&n); err != nil {
		return false, err
	}
	if n > 0 {
		return false, nil
	}

	tx, err := dst.db.Begin()
	if err != nil {
		return false, err
	}
	defer func() {
		if tx != nil {
			tx.Rollback()
		}
	}()

	// real_positions
	if rows, rerr := src.db.Query(`SELECT ts_code,name,qty,cost_price,amount,highest_price,strategy,signal_id,updated_at,user_id FROM real_positions`); rerr == nil {
		for rows.Next() {
			var p RealPosition
			if err := rows.Scan(&p.TsCode, &p.Name, &p.Qty, &p.CostPrice, &p.Amount, &p.HighestPrice, &p.Strategy, &p.SignalID, &p.UpdatedAt, &p.UserID); err != nil {
				rows.Close()
				return false, err
			}
			if _, err := tx.Exec(`INSERT OR REPLACE INTO real_positions(ts_code,name,qty,cost_price,amount,highest_price,strategy,signal_id,updated_at,user_id) VALUES(?,?,?,?,?,?,?,?,?,?)`,
				p.TsCode, p.Name, p.Qty, p.CostPrice, p.Amount, p.HighestPrice, p.Strategy, p.SignalID, p.UpdatedAt, p.UserID); err != nil {
				rows.Close()
				return false, err
			}
		}
		rows.Close()
	}

	// orders
	if oRows, oerr := src.db.Query(`SELECT order_id,signal_id,code,side,status,price,qty,created_at,user_id FROM orders`); oerr == nil {
		for oRows.Next() {
			var o RealOrder
			if err := oRows.Scan(&o.OrderID, &o.SignalID, &o.Code, &o.Side, &o.Status, &o.Price, &o.Qty, &o.CreatedAt, &o.UserID); err != nil {
				oRows.Close()
				return false, err
			}
			if _, err := tx.Exec(`INSERT OR REPLACE INTO orders(order_id,signal_id,code,side,status,price,qty,created_at,user_id) VALUES(?,?,?,?,?,?,?,?,?)`,
				o.OrderID, o.SignalID, o.Code, o.Side, o.Status, o.Price, o.Qty, o.CreatedAt, o.UserID); err != nil {
				oRows.Close()
				return false, err
			}
		}
		oRows.Close()
	}

	// fills
	if fRows, ferr := src.db.Query(`SELECT order_id,code,side,price,qty,amount,traded_at,signal_id,user_id FROM fills`); ferr == nil {
		for fRows.Next() {
			var f RealFill
			if err := fRows.Scan(&f.OrderID, &f.Code, &f.Side, &f.Price, &f.Qty, &f.Amount, &f.TradedAt, &f.SignalID, &f.UserID); err != nil {
				fRows.Close()
				return false, err
			}
			if _, err := tx.Exec(`INSERT INTO fills(order_id,code,side,price,qty,amount,traded_at,signal_id,user_id) VALUES(?,?,?,?,?,?,?,?,?)`,
				f.OrderID, f.Code, f.Side, f.Price, f.Qty, f.Amount, f.TradedAt, f.SignalID, f.UserID); err != nil {
				fRows.Close()
				return false, err
			}
		}
		fRows.Close()
	}

	// real_account（惰性建表，一并迁移）
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS real_account (
		user_id TEXT PRIMARY KEY, available_cash REAL NOT NULL DEFAULT 0,
		frozen_cash REAL NOT NULL DEFAULT 0, total_asset REAL NOT NULL DEFAULT 0,
		market_value REAL NOT NULL DEFAULT 0, updated_at TEXT NOT NULL)`); err != nil {
		return false, err
	}
	if aRows, aerr := src.db.Query(`SELECT user_id,available_cash,frozen_cash,total_asset,market_value,updated_at FROM real_account`); aerr == nil {
		for aRows.Next() {
			var a RealAccount
			if err := aRows.Scan(&a.UserID, &a.AvailableCash, &a.FrozenCash, &a.TotalAsset, &a.MarketValue, &a.UpdatedAt); err != nil {
				aRows.Close()
				return false, err
			}
			if _, err := tx.Exec(`INSERT OR REPLACE INTO real_account(user_id,available_cash,frozen_cash,total_asset,market_value,updated_at) VALUES(?,?,?,?,?,?)`,
				a.UserID, a.AvailableCash, a.FrozenCash, a.TotalAsset, a.MarketValue, a.UpdatedAt); err != nil {
				aRows.Close()
				return false, err
			}
		}
		aRows.Close()
	}

	if err := tx.Commit(); err != nil {
		return false, err
	}
	tx = nil
	return true, nil
}
