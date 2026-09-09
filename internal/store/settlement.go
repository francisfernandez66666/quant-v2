// Package store — settlement.go §WS-B 券商交割单三方对账存储。
// 与实盘账本（real_positions/orders/fills）同库：
//   - ListFillsByDay：按交易日拉取某账号成交（对账源 A=本地账本）；
//   - 对账差异（SettlementDiff）写入 settlement_diff 表（report_only 落账 / sync_fills 纠偏留痕）。
//
// English: §WS-B settlement three-way reconciliation storage — per-day fills lookup for the local
// leg, and persistence of reconciliation diffs for audit/alerting.
package store

import (
	"database/sql"
	"encoding/json"
	"time"
)

// SettlementDiff 单日三方对账差异摘要（JSON 存 settlement_diff.diff_json）。
// English: one day's three-way reconciliation diff summary (JSON in settlement_diff.diff_json).
type SettlementDiff struct {
	Day            string   `json:"day"`              // 交易日
	MissingInLocal []string `json:"missing_in_local"` // 券商有、本地无的成交（可 sync_fills 补记）
	ExtraInLocal   []string `json:"extra_in_local"`   // 本地有、券商无的成交（标 phantom 待人工）
	Mismatch       []string `json:"mismatch"`         // 数量/价格/费用不符
	FeeDiff        float64  `json:"fee_diff"`         // 券商费用合计 − 本地费用合计
	CashDiff       float64  `json:"cash_diff"`        // 券商期末现金 − 本地 real_account
	LocalFeeTotal  float64  `json:"local_fee_total"`
	BrokerFeeTotal float64  `json:"broker_fee_total"`
	Mode           string   `json:"mode"` // report_only | sync_fills
}

// ListFillsByDay 返回某账号指定交易日（traded_at 前缀 yyyy-MM-dd）的全部成交，按 traded_at 升序。
// English: lists a user's fills for one trading day (traded_at prefix), ascending by time.
func (d *DB) ListFillsByDay(userID, day string) ([]RealFill, error) {
	rows, err := d.db.Query(`SELECT id, order_id, code, side, price, qty, amount, traded_at,
		COALESCE(signal_id,''), COALESCE(user_id,''), COALESCE(fee,0), COALESCE(stamp_tax,0), COALESCE(serial,'')
		FROM fills WHERE user_id=? AND substr(traded_at,1,10)=? ORDER BY traded_at ASC`, userID, day)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RealFill
	for rows.Next() {
		var f RealFill
		if err := rows.Scan(&f.ID, &f.OrderID, &f.Code, &f.Side, &f.Price, &f.Qty, &f.Amount,
			&f.TradedAt, &f.SignalID, &f.UserID, &f.Fee, &f.StampTax, &f.Serial); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// FillKey 成交三方对账的关联键（券商交割流水号优先，缺则退回 order_id+traded_at+side）。
// English: cross-leg fill correlation key — broker serial when present, else order_id+time+side.
func FillKey(f RealFill) string {
	if f.Serial != "" {
		return "s:" + f.Serial
	}
	return "f:" + f.OrderID + "@" + f.TradedAt + "@" + f.Side
}

// SaveSettlementDiff 落一条对账差异（同 (user_id,day) 覆盖）。
// English: persists one day's reconciliation diff (upsert by user+day).
func (d *DB) SaveSettlementDiff(userID string, sd SettlementDiff) error {
	raw, err := json.Marshal(sd)
	if err != nil {
		return err
	}
	_, err = d.db.Exec(`INSERT INTO settlement_diff (user_id, day, diff_json, fee_diff, cash_diff, mode, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id, day) DO UPDATE SET diff_json=excluded.diff_json,
			fee_diff=excluded.fee_diff, cash_diff=excluded.cash_diff, mode=excluded.mode, created_at=excluded.created_at`,
		userID, sd.Day, string(raw), sd.FeeDiff, sd.CashDiff, sd.Mode, time.Now().Format("2006-01-02 15:04:05"))
	if err != nil {
		// 无 (user_id,day) 唯一键的旧库：退化为插入（幂等弱，仅在极端迁移期出现）
		if _, err2 := d.db.Exec(`INSERT INTO settlement_diff (user_id, day, diff_json, fee_diff, cash_diff, mode, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, userID, sd.Day, string(raw), sd.FeeDiff, sd.CashDiff, sd.Mode,
			time.Now().Format("2006-01-02 15:04:05")); err2 != nil {
			return err2
		}
	}
	return nil
}

// ListSettlementDiffs 返回最近 N 天对账差异（倒序）。
// English: lists recent settlement diffs, newest first.
func (d *DB) ListSettlementDiffs(limit int) ([]SettlementDiff, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := d.db.Query(`SELECT diff_json FROM settlement_diff ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SettlementDiff
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var sd SettlementDiff
		if json.Unmarshal([]byte(raw), &sd) == nil {
			out = append(out, sd)
		}
	}
	return out, rows.Err()
}

// ApplySettlementFill §WS-B sync_fills 纠偏：补记券商有、本地无的成交（幂等）。
// English: sync_fills reconciliation — backfills a broker-only fill idempotently.
func (d *DB) ApplySettlementFill(f RealFill) error {
	return d.ApplyRealFill(f)
}

// SettlementForeignFills §WS-B 供 sync_fills 判断券商成交是否已在本地：按 (order_id,traded_at,price,qty) 判重。
// English: reports whether a broker fill already exists locally (used by sync_fills).
func (d *DB) FillExists(f RealFill) (bool, error) {
	var n int
	err := d.db.QueryRow(`SELECT COUNT(*) FROM fills WHERE order_id=? AND traded_at=? AND price=? AND qty=?`,
		f.OrderID, f.TradedAt, f.Price, f.Qty).Scan(&n)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// _ 哨兵：确保 settlement_diff 的 (user_id,day) 在旧库无唯一索引时仍可工作（编译期注释）。
// English: compile-time no-op sentinel.
var _ = sql.ErrNoRows
