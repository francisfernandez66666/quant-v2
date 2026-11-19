// Package store — anchor_book.go：首买锚点簿（§ANCHOR-T1，owner 2026-11-19 裁决）。
//
// 为什么需要第四张表（不是"把锚点留在 real_positions 里"就够）
//
//	`real_positions` 的一行就是"当前持仓"，清仓时整行被删（`ApplyRealFill` 的 qty<=0 清理）。
//	锚点跟着这一行走，于是**同一个交易日内**「卖出清仓 → 同一天再买回来」会重新建行、把锚点
//	换成第二次买入的成本——锚点漂移。owner 明确要的不是这个：清仓的票当天再买回来，止盈/止损
//	的分母必须还是当天第一次建仓的那个价；只有跨到**下一个交易日**（T+1）才允许重新冻结。
//
// 判据只有一条：`anchor_day == 本笔成交的北京交易日` ⇒ 复用旧锚点，否则按本笔成交重新冻结。
//   - 清仓不删锚点簿（那正是它存在的理由）；
//   - 跨日不清也行：`anchor_day` 对不上就等于已过期，写入时顺手把更早的行清掉（有界增长）；
//   - 读面（`t_anchor_book` 烘参、T 闸门）仍只看 `real_positions`，本表只在建行/复建时被查一次，
//     免得两本账各判一次新鲜度。
//
// English: a per-(symbol, account) first-buy anchor that outlives the position row. Clearing a
// position deletes the row (and with it the anchor), so a same-day re-buy used to re-freeze the
// anchor at the new cost. Owner ruling 2026-11-19: keep the anchor until T+1. The only predicate
// is `anchor_day == the fill's Beijing trading day`; an older row is expired by definition and
// is pruned when a new anchor is frozen. Readers (the baked t_anchor_book / T gate) still read
// real_positions only — this table is consulted exactly once, when a position row is (re)created.
package store

import (
	"database/sql"
	"fmt"
	"regexp"
	"time"

	"quant-trading-v2/internal/cntime"
)

// AnchorBookEntry 锚点簿一行：某账号某代码在某个交易日冻结下来的首买锚点。
// （One frozen first-buy anchor for an account+symbol on a given trading day.）
type AnchorBookEntry struct {
	TsCode       string  `json:"ts_code"`
	UserID       string  `json:"user_id"`
	AnchorPrice  float64 `json:"anchor_price"`
	AnchorSource string  `json:"anchor_source"`
	// AnchorDay 冻结这笔锚点的北京交易日（yyyy-mm-dd）。T+1 判据就是它。
	// English: the Beijing trading day the anchor was frozen on — the whole T+1 predicate.
	AnchorDay string `json:"anchor_day"`
	UpdatedAt string `json:"updated_at"`
}

// anchorDayPattern 成交时间串里的日期段是否真的是日期。
// 为什么必须校验：历史成交行有一批 `traded_at` 只有时分秒（§REPORT-STAMP 修复前的产物），
// 直接取前 10 字符会得到 `10:46:00` 这种"日"，拿它当 T+1 键会把锚点判成永远同一天。
// 解析不出来就退回**当前北京交易日**：那是"继续沿用"的方向（多留一天），绝不会把旧锚点
// 冒充成新一天的锚点（那个方向会让止损线凭空抬起来）。
var anchorDayPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// anchorDayOf 从成交时间串推出锚点簿的日键（北京交易日）。
func anchorDayOf(tradedAt string) string {
	day := buyDateOf(tradedAt)
	if anchorDayPattern.MatchString(day) {
		return day
	}
	return cntime.DayOf(cntime.Now())
}

// ensureAnchorBook 锚点簿的幂等建表（惰性 DDL，与 ensureAnchorPriceColumn 同族：
// 不进集中迁移清单，老库第一次写入时自建）。
func (d *DB) ensureAnchorBook() error {
	_, err := d.db.Exec(`CREATE TABLE IF NOT EXISTS real_anchor_book (
		ts_code TEXT NOT NULL,
		user_id TEXT NOT NULL DEFAULT '',
		anchor_price REAL NOT NULL DEFAULT 0,
		anchor_source TEXT NOT NULL DEFAULT '',
		anchor_day TEXT NOT NULL DEFAULT '',
		updated_at TEXT NOT NULL DEFAULT '',
		PRIMARY KEY (ts_code, user_id)
	)`)
	if err != nil {
		return fmt.Errorf("store migrate real_anchor_book: %w", err)
	}
	return nil
}

// lookupAnchorBookTx 读某代码在某账号下的锚点行（含遗留全局行，账号自己的优先）。
// 返回的 ok 只表示"库里有一行"，是否可用由调用方按 anchor_day 判（T+1 判据）。
func lookupAnchorBookTx(tx *sql.Tx, tsCode, userID string) (AnchorBookEntry, bool, error) {
	var e AnchorBookEntry
	err := tx.QueryRow(`SELECT ts_code, user_id, anchor_price, COALESCE(anchor_source,''),
			COALESCE(anchor_day,''), COALESCE(updated_at,'')
		FROM real_anchor_book
		WHERE ts_code=? AND (user_id='' OR user_id=?)
		ORDER BY CASE WHEN user_id=? THEN 0 ELSE 1 END
		LIMIT 1`, tsCode, userID, userID).
		Scan(&e.TsCode, &e.UserID, &e.AnchorPrice, &e.AnchorSource, &e.AnchorDay, &e.UpdatedAt)
	if err == sql.ErrNoRows {
		return AnchorBookEntry{}, false, nil
	}
	if err != nil {
		return AnchorBookEntry{}, false, err
	}
	return e, true, nil
}

// freezeAnchorTx 把一条锚点写进簿（同代码同账号旧行整体替换，并清掉已过期的更早日子）。
// 为什么要先删再插：遗留全局行（user_id=”）与账号行可能同时存在，ON CONFLICT 只会命中
// 其中一行，剩下的那行在下一次查询里可能被读成"账号自己的"以外的那条，判据就活了两次。
func freezeAnchorTx(tx *sql.Tx, e AnchorBookEntry) error {
	if e.UserID == "" {
		// 遗留全局行只在"这笔成交确实没有账号"时才写；有账号的成交一律归账号。
		if _, err := tx.Exec(`DELETE FROM real_anchor_book WHERE ts_code=? AND user_id=''`, e.TsCode); err != nil {
			return err
		}
	} else if _, err := tx.Exec(`DELETE FROM real_anchor_book WHERE ts_code=? AND (user_id='' OR user_id=?)`,
		e.TsCode, e.UserID); err != nil {
		return err
	}
	if e.UpdatedAt == "" {
		e.UpdatedAt = time.Now().Format("2006-01-02 15:04:05")
	}
	// T+1 清理：比本行更早的日子已经被判过期（对不上 anchor_day 就用不上），顺手收掉，
	// 免得多账号长期运行后这张表只增不减。**只收本账号的行**：跨账号删别人的锚点是
	// 「顺手优化」里最不该做的一类——别人的持仓行还在用那个价，凭一张表的清理动作去碰它，
	// 坏掉的是止盈/止损线，而且只在"他今天也没成交"的那天才看得出来。
	if e.AnchorDay != "" {
		if _, err := tx.Exec(`DELETE FROM real_anchor_book
			WHERE (user_id='' OR user_id=?) AND anchor_day <> '' AND anchor_day < ?`,
			e.UserID, e.AnchorDay); err != nil {
			return err
		}
	}
	_, err := tx.Exec(`INSERT INTO real_anchor_book
		(ts_code, user_id, anchor_price, anchor_source, anchor_day, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		e.TsCode, e.UserID, e.AnchorPrice, e.AnchorSource, e.AnchorDay, e.UpdatedAt)
	if err != nil {
		return fmt.Errorf("freeze anchor book %s: %w", e.TsCode, err)
	}
	return nil
}

// resolveOpenAnchorTx 建仓（或复建）时该用哪个锚点——锚点簿的唯一消费点。
//
// 输入是本笔买入成交算出来的"新锚点"（含费每股成本 + first_buy_order 来源）；
// 若簿里有一行**同一交易日**的锚点，则沿用那一行（这正是 owner 要的"清仓当天买回来不漂移"）。
// 命中判据只看价不看来源：`anchor_price>0 且 anchor_source=”` 是历史存量行的合法形态
// （来源不可证、但风险线是真的），沿用它的**价**、来源照旧留空——拿本笔成本顶替它才是漂移。
// 无论命中与否都会把结果写回簿里（命中＝刷新时间戳即可，未命中＝冻结本笔）。
// English: the one place the anchor book is consumed — a same-day row wins over the new fill's
// cost, and the resolved value is always written back.
func resolveOpenAnchorTx(tx *sql.Tx, tsCode, userID string, freshPrice float64, freshSource, tradedAt string) (float64, string, error) {
	day := anchorDayOf(tradedAt)
	price, source := freshPrice, freshSource
	if old, ok, err := lookupAnchorBookTx(tx, tsCode, userID); err != nil {
		return 0, "", err
	} else if ok && old.AnchorDay == day && old.AnchorPrice > 0 {
		price, source = old.AnchorPrice, old.AnchorSource
	}
	err := freezeAnchorTx(tx, AnchorBookEntry{
		TsCode:       tsCode,
		UserID:       userID,
		AnchorPrice:  price,
		AnchorSource: source,
		AnchorDay:    day,
	})
	return price, source, err
}

// AnchorBookForUser 读某账号的锚点簿（诊断/对照用，只读）。
// 遗留全局行（user_id=”）对所有账号可见，与 real_positions 的读面口径一致。
func (d *DB) AnchorBookForUser(userID string) ([]AnchorBookEntry, error) {
	if err := d.ensureAnchorBook(); err != nil {
		return nil, err
	}
	rows, err := d.db.Query(`SELECT ts_code, user_id, anchor_price, COALESCE(anchor_source,''),
			COALESCE(anchor_day,''), COALESCE(updated_at,'')
		FROM real_anchor_book WHERE user_id='' OR user_id=? ORDER BY ts_code`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AnchorBookEntry
	for rows.Next() {
		var e AnchorBookEntry
		if err := rows.Scan(&e.TsCode, &e.UserID, &e.AnchorPrice, &e.AnchorSource, &e.AnchorDay, &e.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
