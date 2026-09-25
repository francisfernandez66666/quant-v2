// Package store — SQLite 历史数据存储层。
// pnl_offset.go：§E1 盈亏单轨化（owner 裁决 2026-09-26「盈亏前端自算与后端两套账：要做」）——
// 纸面账户「清零」显示偏移量从浏览器 localStorage 收编入库。
// 旧账本：偏移量只存在各台电脑浏览器里（换设备/清缓存即凭空消失，且无任何痕迹），
// 前端拿它把总盈亏"校"成想要的读数，后端 summary 与前端展示因此对不上（两套账）。
// 现在：**只追加、不覆盖**的历史表（每次清零一行，带时间与备注）——留痕本身就是审计；
// 当前生效值 = 最新一行；总盈亏的算式收敛到后端 handleFixGetHoldings 一处，前端只展示。
// English: §E1 — the paper account's "reset-to-zero" display offset moves from browser localStorage
// into an append-only SQLite table (one row per reset, with time and note); the effective offset is
// the newest row, and the total-P&L arithmetic now lives solely in the backend summary.
package store

import (
	"database/sql"
	"errors"
	"time"
)

// PnlOffsetRecord 一条清零校准记录（pnl_offset_history 行）。
// English: one reset calibration row.
type PnlOffsetRecord struct {
	// ID 自增主键（回显用，表内定位这一笔留痕；新插入行由 LastInsertId 回填）
	ID int64 `json:"id"`
	// UserID 归属操作账号（与 real_account 同款口径，运营数据归属管理员）
	UserID string `json:"user_id"`
	// Offset 校准后的显示偏移量（元）：展示总盈亏 = 已实现 + 浮动 − Offset
	Offset float64 `json:"offset"`
	// Note 备注（谁点的、为什么清零；入库留痕的可读上下文）
	Note string `json:"note"`
	// CreatedAt 入库时间（本地时间串，与全仓时间字段口径一致）
	CreatedAt string `json:"created_at"`
}

// ensurePnlOffsetTable 幂等建表（首次读写时创建）。
// 表只追加：不提供 UPDATE/DELETE 入口——"当时的校准值有没有被改过"正是这套账要回答的问题。
func (d *DB) ensurePnlOffsetTable() error {
	_, err := d.db.Exec(`CREATE TABLE IF NOT EXISTS pnl_offset_history (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id     TEXT NOT NULL DEFAULT '',
		offset      REAL NOT NULL,
		note        TEXT NOT NULL DEFAULT '',
		created_at  TEXT NOT NULL
	)`)
	return err
}

// AddPnlOffset 追加一条清零校准记录，回填自增 ID 后返回。
// English: appends one calibration row (idempotent create); there is deliberately no update/delete API.
func (d *DB) AddPnlOffset(rec PnlOffsetRecord) (PnlOffsetRecord, error) {
	if err := d.ensurePnlOffsetTable(); err != nil {
		return rec, err
	}
	if rec.CreatedAt == "" {
		rec.CreatedAt = time.Now().Format("2006-01-02 15:04:05")
	}
	res, err := d.db.Exec(`INSERT INTO pnl_offset_history (user_id, offset, note, created_at)
		VALUES (?, ?, ?, ?)`, rec.UserID, rec.Offset, rec.Note, rec.CreatedAt)
	if err != nil {
		return rec, err
	}
	if id, e := res.LastInsertId(); e == nil {
		rec.ID = id
	}
	return rec, nil
}

// LatestPnlOffset 返回当前生效的显示偏移量（元）：最新一行；无记录=0（从未做过校准）。
// 查库失败原样上报——§E1 的口径是「读数不可得就明确失败」，绝不静默把错误折成 0
// 让前端照用旧账（与 §N-5「错误≠没有」同一姿势），调用方拿到 err 应把总盈亏显示为"—"。
// English: the effective offset = newest row (absent = 0). A DB error is returned as-is and must
// never be flattened into a silent 0.
func (d *DB) LatestPnlOffset(userID string) (float64, error) {
	if err := d.ensurePnlOffsetTable(); err != nil {
		return 0, err
	}
	var off float64
	err := d.db.QueryRow(`SELECT offset FROM pnl_offset_history
		WHERE user_id = ? OR user_id = '' ORDER BY id DESC LIMIT 1`, userID).Scan(&off)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return off, nil
}
