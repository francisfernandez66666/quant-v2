// Package store — SQLite 历史数据存储层。
// real_account.go：实盘账户资产（AUTO_TRADING_PLAN M1 补充）——由广州 QMT 网关
// 在对账时上报的账户级资产（可用资金/冻结资金/总资产/持仓市值）落库，供前端展示。
// 数据源为网关 /api/qmt/report 的 account 事件（broker.query_asset 产出）。
// English: real_account.go — live account assets (available cash / frozen cash / total asset /
// market value) pushed by the Guangzhou QMT gateway's account report event, persisted for display.
package store

import (
	"time"
)

// RealAccount 实盘账户资产行（账户级，非持仓级）。
// （RealAccount is one row of live account assets — account-level, not position-level.）
type RealAccount struct {
	UserID        string  `json:"user_id"`        // 归属用户（空=遗留全局行）
	AvailableCash float64 `json:"available_cash"` // 可用资金（可买新股的钱）
	FrozenCash    float64 `json:"frozen_cash"`    // 冻结资金
	TotalAsset    float64 `json:"total_asset"`    // 总资产
	MarketValue   float64 `json:"market_value"`   // 持仓市值
	UpdatedAt     string  `json:"updated_at"`     // 更新时间
}

// ensureRealAccountTable 幂等建表（首次上报时创建）。
func (d *DB) ensureRealAccountTable() error {
	_, err := d.db.Exec(`CREATE TABLE IF NOT EXISTS real_account (
		user_id       TEXT PRIMARY KEY,
		available_cash REAL NOT NULL DEFAULT 0,
		frozen_cash  REAL NOT NULL DEFAULT 0,
		total_asset  REAL NOT NULL DEFAULT 0,
		market_value REAL NOT NULL DEFAULT 0,
		updated_at   TEXT NOT NULL
	)`)
	return err
}

// UpsertRealAccount 写入/更新账户资产（按 user_id 幂等）。
// （UpsertRealAccount writes/updates account assets, idempotent by user_id.）
func (d *DB) UpsertRealAccount(acc RealAccount) error {
	if err := d.ensureRealAccountTable(); err != nil {
		return err
	}
	if acc.UpdatedAt == "" {
		acc.UpdatedAt = time.Now().Format("2006-01-02 15:04:05")
	}
	_, err := d.db.Exec(`INSERT INTO real_account
		(user_id, available_cash, frozen_cash, total_asset, market_value, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET
			available_cash=excluded.available_cash, frozen_cash=excluded.frozen_cash,
			total_asset=excluded.total_asset, market_value=excluded.market_value,
			updated_at=excluded.updated_at`,
		acc.UserID, acc.AvailableCash, acc.FrozenCash, acc.TotalAsset, acc.MarketValue, acc.UpdatedAt)
	return err
}

// GetRealAccount 返回指定账号的账户资产（含遗留全局行兜底）；不存在返回零值行。
// （GetRealAccount returns the account assets for a user (legacy global row as fallback);
// a zero row when absent.）
func (d *DB) GetRealAccount(userID string) (RealAccount, error) {
	var acc RealAccount
	acc.UserID = userID
	err := d.db.QueryRow(`SELECT available_cash, frozen_cash, total_asset, market_value, updated_at
		FROM real_account WHERE user_id = ? OR user_id = '' ORDER BY user_id DESC LIMIT 1`, userID).
		Scan(&acc.AvailableCash, &acc.FrozenCash, &acc.TotalAsset, &acc.MarketValue, &acc.UpdatedAt)
	if err != nil {
		// 表不存在或查不到：返回零值（不报错，前端显示 0/—）
		return acc, nil
	}
	return acc, nil
}
