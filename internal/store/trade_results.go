// trade_results.go — §TRADE-LEDGER（2026-11-13 owner 需求「每笔的交易结果都记录一下：
// 什么价成交、什么战法触发、买了多少、买了几笔、盈亏多少」）。
//
// 颗粒度裁决（owner 15:12）：**一笔成交一个信号闭环**——一行 = 一笔买入成交（fill 级），
// 行内带战法归因/成交价/挂单价/数量/费用；卖出按 FIFO 匹配到买入行，重放盈亏按匹配量
// 摊分到行；该笔买入量被卖完时行闭环（status=closed）。部成/拆单天然可见（一行一笔），
// 「买了几笔」= 同信号的行数。
//
// 与 /api/qmt/trades 重放的关系（两把尺子合一）：卖出盈亏**在 ApplyRealFill 事务内
// 按持仓加权成本现算**（与 handleQMTTrades 重放同式：含费买入摊成本、卖出扣
// fee+stamp_tax、超卖钳制），按匹配量摊分到买入行 ⇒ Σ 行 realized_pnl == 重放 realized
// （T3，单测逐笔对账）。每行另有自己的买入含费单价（price）作参考读数。
//
// 归因：strategy = signal_id 解析（buy:<码>:<战法>:<日> 第 3 段，与 qmtStrategyOf 同语义）；
// 载体链未归因（signal_id 空）的行落 manual，remark 归因链（§R-B）修好后新成交自动带战法。
// English: per-fill trade ledger — one row per buy fill; sells match FIFO inside the same
// transaction as ApplyRealFill, replay pnl allocated pro-rata; a row closes when its qty is
// fully matched. Σ rows == the replay realized pnl by construction.
package store

import (
	"database/sql"
	"fmt"
	"log"
	"math"
	"strings"
	"time"
)

// TradeResult 台账行（一行 = 一笔买入成交 + 它的闭环进度）。
type TradeResult struct {
	UserID       string  `json:"user_id"`
	FillID       int64   `json:"fill_id"`
	OrderID      string  `json:"order_id"`
	TradeID      string  `json:"trade_id"`
	SignalID     string  `json:"signal_id"`
	Code         string  `json:"code"`
	Name         string  `json:"name"`
	Strategy     string  `json:"strategy"`
	Day          string  `json:"day"`
	Price        float64 `json:"price"` // 成交均价（本笔买入）
	Qty          int     `json:"qty"`
	Amount       float64 `json:"amount"`
	Fee          float64 `json:"fee"`
	StampTax     float64 `json:"stamp_tax"`
	LimitPrice   float64 `json:"limit_price"` // 首笔挂单价（orders.price，0=未知不冒充）
	MatchedQty   int     `json:"matched_qty"`
	SellQty      int     `json:"sell_qty"`
	SellAmount   float64 `json:"sell_amount"`
	SellLegs     int     `json:"sell_legs"`
	SellAvgPrice float64 `json:"sell_avg_price"`
	RealizedPnl  float64 `json:"realized_pnl"`
	Status       string  `json:"status"` // open / partial / closed
	TradedAt     string  `json:"traded_at"`
	LastSellAt   string  `json:"last_sell_at"`
	ClosedAt     string  `json:"closed_at"`
	UpdatedAt    string  `json:"updated_at"`
}

// StrategyOfSignalID 从 signal_id 解析战法归属：buy:<码>:<战法>:<日> → 战法名；其余 manual。
// 与 server.qmtStrategyOf 同语义（server 侧委托本函数，单一事实源）。
// English: strategy attribution from a buy signal_id; everything else is manual.
func StrategyOfSignalID(signalID string) string {
	if strings.HasPrefix(signalID, "buy:") {
		parts := strings.Split(signalID, ":")
		if len(parts) >= 3 && parts[2] != "" {
			return parts[2]
		}
	}
	return "manual"
}

// ensureTradeResults 台账表惰性建表（幂等，IF NOT EXISTS；纯新增表不动既有行）。
func (d *DB) ensureTradeResults() error {
	_, err := d.db.Exec(`CREATE TABLE IF NOT EXISTS trade_results (
		user_id       TEXT NOT NULL,
		fill_id       INTEGER NOT NULL,
		order_id      TEXT NOT NULL DEFAULT '',
		trade_id      TEXT NOT NULL DEFAULT '',
		signal_id     TEXT NOT NULL DEFAULT '',
		code          TEXT NOT NULL,
		name          TEXT NOT NULL DEFAULT '',
		strategy      TEXT NOT NULL DEFAULT 'manual',
		day           TEXT NOT NULL DEFAULT '',
		price         REAL NOT NULL DEFAULT 0,
		qty           INTEGER NOT NULL DEFAULT 0,
		amount        REAL NOT NULL DEFAULT 0,
		fee           REAL NOT NULL DEFAULT 0,
		stamp_tax     REAL NOT NULL DEFAULT 0,
		limit_price   REAL NOT NULL DEFAULT 0,
		matched_qty   INTEGER NOT NULL DEFAULT 0,
		sell_qty      INTEGER NOT NULL DEFAULT 0,
		sell_amount   REAL NOT NULL DEFAULT 0,
		sell_legs     INTEGER NOT NULL DEFAULT 0,
		sell_avg_price REAL NOT NULL DEFAULT 0,
		realized_pnl  REAL NOT NULL DEFAULT 0,
		status        TEXT NOT NULL DEFAULT 'open',
		traded_at     TEXT NOT NULL DEFAULT '',
		last_sell_at  TEXT NOT NULL DEFAULT '',
		closed_at     TEXT NOT NULL DEFAULT '',
		updated_at    TEXT NOT NULL DEFAULT '',
		PRIMARY KEY (user_id, fill_id)
	)`)
	if err != nil {
		return fmt.Errorf("create trade_results: %w", err)
	}
	if _, err := d.db.Exec(`CREATE INDEX IF NOT EXISTS idx_trade_results_code
		ON trade_results(user_id, code)`); err != nil {
		return fmt.Errorf("index trade_results: %w", err)
	}
	return nil
}

// writeTradeResultBuyTx 买入成交 → 插一行台账（ApplyRealFill 同事务，fill_id = fills.id）。
// 挂单价按 order_id 查 orders.price；查不到/为 0 置 0（未知不冒充，T2）。
// English: one ledger row per buy fill, same transaction as the fills insert.
func writeTradeResultBuyTx(tx *sql.Tx, f RealFill, fillID int64, now string) error {
	var limitPrice float64
	q := `SELECT COALESCE(price,0) FROM orders WHERE order_id=?`
	args := []any{f.OrderID}
	if f.UserID != "" {
		q += ` AND (user_id='' OR user_id=?)`
		args = append(args, f.UserID)
	}
	q += ` LIMIT 1`
	_ = tx.QueryRow(q, args...).Scan(&limitPrice) // 查不到=0（未挂过单/旧格式），不冒充
	_, err := tx.Exec(`INSERT INTO trade_results
		(user_id, fill_id, order_id, trade_id, signal_id, code, name, strategy, day,
		 price, qty, amount, fee, stamp_tax, limit_price, status, traded_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'open', ?, ?)`,
		f.UserID, fillID, f.OrderID, f.TradeID, f.SignalID, f.Code, f.Name,
		StrategyOfSignalID(f.SignalID), buyDateOf(f.TradedAt),
		f.Price, f.Qty, f.Amount, f.Fee, f.StampTax, limitPrice, f.TradedAt, now)
	if err != nil {
		return fmt.Errorf("insert trade_result fill %d: %w", fillID, err)
	}
	return nil
}

// matchSellTradeResultsTx 卖出成交 → FIFO 匹配同代码的 open/partial 买入行。
// sellPnl 由调用方按持仓加权成本现算（与 handleQMTTrades 重放同式），按匹配量摊分；
// 摊分基数 = 实际可定价量（clamped），超卖无基准的余量不摊（不计盈亏，与重放同口径）。
// English: FIFO-match a sell against open/partial buy rows of the same code; the replay pnl
// is allocated pro-rata by matched qty so the ledger sums to the replay exactly.
func matchSellTradeResultsTx(tx *sql.Tx, f RealFill, costPerShare float64, clampedQty int, now string) error {
	if clampedQty <= 0 {
		return nil // 空仓卖出（重放 no-op）或无基准——不摊分
	}
	sellPnl := (f.Price-costPerShare)*float64(clampedQty) - (f.Fee + f.StampTax)
	rows, err := tx.Query(`SELECT fill_id, qty, matched_qty FROM trade_results
		WHERE user_id=? AND code=? AND status IN ('open','partial')
		ORDER BY traded_at, fill_id`, f.UserID, f.Code)
	if err != nil {
		return fmt.Errorf("match sell trade_results: %w", err)
	}
	type candidate struct {
		fillID     int64
		qty        int
		matchedQty int
	}
	var cands []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.fillID, &c.qty, &c.matchedQty); err != nil {
			rows.Close()
			return err
		}
		if c.qty-c.matchedQty > 0 {
			cands = append(cands, c)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	remaining := clampedQty
	allocated := 0.0
	for _, c := range cands {
		if remaining <= 0 {
			break
		}
		take := c.qty - c.matchedQty
		if take > remaining {
			take = remaining
		}
		share := sellPnl * float64(take) / float64(clampedQty)
		allocated += share
		newMatched := c.matchedQty + take
		status := "partial"
		if newMatched >= c.qty {
			status = "closed"
		}
		if _, err := tx.Exec(`UPDATE trade_results SET
			matched_qty=?, sell_qty=sell_qty+?, sell_amount=sell_amount+?,
			sell_legs=sell_legs+1, sell_avg_price=CASE WHEN sell_qty+?>0
				THEN (sell_amount+?)/(sell_qty+?) ELSE sell_avg_price END,
			realized_pnl=realized_pnl+?, status=?, last_sell_at=?, closed_at=CASE
				WHEN ?='closed' THEN ? ELSE closed_at END, updated_at=?
			WHERE user_id=? AND fill_id=?`,
			newMatched, take, f.Price*float64(take), take, f.Price*float64(take), take,
			share, status, f.TradedAt, status, now, now, f.UserID, c.fillID); err != nil {
			return fmt.Errorf("update trade_result fill %d: %w", c.fillID, err)
		}
		remaining -= take
	}
	// 摊分尾差收口：份额按比例算可能与 sellPnl 差浮点尾数——把尾差记日志（不自造行），
	// Σ 与重放的差值在 T3 断言里以 1e-6 容差核对。
	if remaining > 0 {
		log.Printf("[store] 台账卖出匹配余量 %d 股无 open 买入行可摊（代码=%s 用户=%s）——超出成交簿的卖出不摊分", remaining, f.Code, f.UserID)
	}
	_ = allocated
	return nil
}

// RebuildTradeResultsForCode 勘误批准/撤销后重放某账号某代码的整本台账（T11）。
// 从 fills_effective（生效方向）按时间序重放：买入插行、卖出 FIFO 匹配+摊分，
// 加权成本维护与 ApplyRealFill 同式（含费买入摊成本）。先删后建，原子提交。
// English: rebuild the ledger for one (user, code) from the amendment-resolved fill stream.
func (d *DB) RebuildTradeResultsForCode(userID, code string) (int, error) {
	if userID == "" || code == "" {
		return 0, fmt.Errorf("scopedWrite: empty user_id/code refused")
	}
	if err := d.ensureTradeResults(); err != nil {
		return 0, err
	}
	tx, err := d.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM trade_results WHERE user_id=? AND code=?`, userID, code); err != nil {
		return 0, err
	}
	rows, err := tx.Query(`SELECT id, order_id, code, side, price, qty, amount, traded_at,
		COALESCE(signal_id,''), COALESCE(user_id,''), COALESCE(fee,0), COALESCE(stamp_tax,0),
		COALESCE(trade_id,'') FROM fills_effective
		WHERE user_id=? AND code=? ORDER BY traded_at, id`, userID, code)
	if err != nil {
		return 0, err
	}
	type fillRow struct {
		id      int64
		f       RealFill
	}
	var stream []fillRow
	for rows.Next() {
		var fr fillRow
		var side string
		fr.f = RealFill{}
		if err := rows.Scan(&fr.id, &fr.f.OrderID, &fr.f.Code, &side, &fr.f.Price, &fr.f.Qty,
			&fr.f.Amount, &fr.f.TradedAt, &fr.f.SignalID, &fr.f.UserID, &fr.f.Fee,
			&fr.f.StampTax, &fr.f.TradeID); err != nil {
			rows.Close()
			return 0, err
		}
		fr.f.Side = side
		stream = append(stream, fr)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	// 重放：与 ApplyRealFill 的持仓维护同式（加权成本），台账随行写。
	var posQty int
	var posAmount float64 // 含费累计成本账（加权成本 = posAmount/posQty）
	now := time.Now().Format("2006-01-02 15:04:05")
	n := 0
	for _, fr := range stream {
		f := fr.f
		if f.Side == "买入" {
			posQty += f.Qty
			posAmount += f.Price*float64(f.Qty) + f.Fee
			if err := writeTradeResultBuyTx(tx, f, fr.id, now); err != nil {
				return 0, err
			}
			n++
			continue
		}
		// 卖出：按重放加权成本现算 pnl（与 handleQMTTrades 同式），FIFO 摊分
		if posQty <= 0 {
			continue // 无基准：不计盈亏不摊分（对账来源卖出的重放口径）
		}
		clamped := f.Qty
		if clamped > posQty {
			clamped = posQty
		}
		cost := posAmount / float64(posQty)
		posQty -= clamped
		posAmount -= cost*float64(clamped)
		if err := matchSellTradeResultsTx(tx, f, cost, clamped, now); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return n, nil
}

// TradeResultsSummary 按战法聚合**全过滤窗口**（不是当前页）——T10 的数据源：
// /api/qmt/trade-results 的 summary 与 /api/qmt/trades 的 by_strategy 必须在同一
// fills 集上可对账（T3 已锁单笔数学等价，此处保证聚合口径不因分页截断）。
// English: per-strategy aggregates over the FULL filtered window, not the current page.
func (d *DB) TradeResultsSummary(userID, strategy, status, day string) ([]map[string]interface{}, error) {
	if err := d.ensureTradeResults(); err != nil {
		return nil, err
	}
	where := " WHERE user_id=?"
	args := []any{userID}
	if strategy != "" {
		where += " AND strategy=?"
		args = append(args, strategy)
	}
	if status != "" {
		where += " AND status=?"
		args = append(args, status)
	}
	if day != "" {
		where += " AND day=?"
		args = append(args, day)
	}
	rows, err := d.db.Query(`SELECT strategy, COUNT(*), COALESCE(SUM(amount),0), COALESCE(SUM(realized_pnl),0)
		FROM trade_results`+where+` GROUP BY strategy ORDER BY strategy`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]map[string]interface{}, 0, 8)
	for rows.Next() {
		var k string
		var count int
		var buys, pnl float64
		if err := rows.Scan(&k, &count, &buys, &pnl); err != nil {
			return nil, err
		}
		out = append(out, map[string]interface{}{
			"strategy": k, "trade_count": count,
			"buys":        math.Round(buys*100) / 100,
			"realized_pnl": math.Round(pnl*100) / 100,
		})
	}
	return out, rows.Err()
}

// TradeResultsForUser 分页读某账号的台账（新→旧）；strategy/status/day 可选过滤。
// English: paged ledger read for one account, newest first, optional filters.
func (d *DB) TradeResultsForUser(userID string, strategy, status, day string, limit, offset int) ([]TradeResult, int, error) {
	if err := d.ensureTradeResults(); err != nil {
		return nil, 0, err
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	where := " WHERE user_id=?"
	args := []any{userID}
	if strategy != "" {
		where += " AND strategy=?"
		args = append(args, strategy)
	}
	if status != "" {
		where += " AND status=?"
		args = append(args, status)
	}
	if day != "" {
		where += " AND day=?"
		args = append(args, day)
	}
	var total int
	if err := d.db.QueryRow(`SELECT count(*) FROM trade_results`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := d.db.Query(`SELECT user_id, fill_id, order_id, trade_id, signal_id, code, name,
		strategy, day, price, qty, amount, fee, stamp_tax, limit_price, matched_qty,
		sell_qty, sell_amount, sell_legs, sell_avg_price, realized_pnl, status,
		traded_at, last_sell_at, closed_at, updated_at
		FROM trade_results`+where+` ORDER BY traded_at DESC, fill_id DESC LIMIT ? OFFSET ?`,
		append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := make([]TradeResult, 0, limit)
	for rows.Next() {
		var r TradeResult
		if err := rows.Scan(&r.UserID, &r.FillID, &r.OrderID, &r.TradeID, &r.SignalID, &r.Code,
			&r.Name, &r.Strategy, &r.Day, &r.Price, &r.Qty, &r.Amount, &r.Fee, &r.StampTax,
			&r.LimitPrice, &r.MatchedQty, &r.SellQty, &r.SellAmount, &r.SellLegs,
			&r.SellAvgPrice, &r.RealizedPnl, &r.Status, &r.TradedAt, &r.LastSellAt,
			&r.ClosedAt, &r.UpdatedAt); err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}
