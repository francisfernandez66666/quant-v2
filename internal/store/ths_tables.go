// ths_tables.go 同花顺（新）数据源表：ths_daily 日K（docs/HITHINK_DATA_SOURCE_PLAN.md §4）。
//
// 与旧 daily(baostock) 物理分离——读路由按 data.primary_source 优先 ths_ 表（THS 口径优先），
// 缺口回退旧表并登记重试队列。字段与 fuyao dump 一一对应。
package store

import (
	"database/sql"
	"time"
)

// ThsDailyRow ths_daily 单行。
type ThsDailyRow struct {
	TsCode    string  // 完整代码 600519.SH
	TradeDate string  // yyyyMMdd（交易日）
	Open      float64 // 开盘价
	High      float64 // 最高价
	Low       float64 // 最低价
	Close     float64 // 收盘价
	Vol       float64 // 成交量（股）
	Amount    float64 // 成交额（元）
}

// thsDailyColumns ths_daily 表插入列清单（与 ThsDailyRow 字段顺序一致）。
const thsDailyColumns = `ts_code, trade_date, open, high, low, close, vol, amount`

// UpsertThsDailyRows 批量幂等写入（事务，INSERT OR REPLACE）。
func (d *DB) UpsertThsDailyRows(rows []ThsDailyRow) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	tx, err := d.db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.Prepare(`INSERT OR REPLACE INTO ths_daily
		(ts_code, trade_date, open, high, low, close, vol, amount)
		VALUES (?,?,?,?,?,?,?,?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	var n int64
	for _, r := range rows {
		res, err := stmt.Exec(r.TsCode, r.TradeDate, r.Open, r.High, r.Low, r.Close, r.Vol, r.Amount)
		if err != nil {
			return n, err
		}
		aff, _ := res.RowsAffected()
		n += aff
	}
	return n, tx.Commit()
}

// ThsDailyMaxDate 某标的在 ths 表的最新交易日（空串=无数据；全市场对账用 MaxTradeDate 语义一致）。
func (d *DB) ThsDailyMaxDate(tsCode string) (string, error) {
	var q string
	var args []any
	if tsCode == "" {
		q = `SELECT COALESCE(MAX(trade_date),'') FROM ths_daily`
		args = nil
	} else {
		q = `SELECT COALESCE(MAX(trade_date),'') FROM ths_daily WHERE ts_code = ?`
		args = []any{tsCode}
	}
	var out sql.NullString
	err := d.db.QueryRow(q, args...).Scan(&out)
	if err != nil {
		return "", err
	}
	return out.String, nil
}

// ThsDailyCount 统计行数（对账用；可选 since 过滤）。
func (d *DB) ThsDailyCount(since string) (int, error) {
	q := `SELECT COUNT(*) FROM ths_daily`
	var args []any
	if since != "" {
		q += ` WHERE trade_date >= ?`
		args = append(args, since)
	}
	var n int
	err := d.db.QueryRow(q, args...).Scan(&n)
	return n, err
}

var _ = time.Now // 保持 time 引用（时间戳列扩展备用）

// ThsAdjFactorRow ths_adj_factor 单行：某标的某交易日的累计后复权因子。
type ThsAdjFactorRow struct {
	TsCode    string  // TS代码
	TradeDate string  // 交易日期
	Factor    float64 // 复权因子
}

// UpsertThsAdjFactorRows 批量幂等写入因子行。
func (d *DB) UpsertThsAdjFactorRows(rows []ThsAdjFactorRow) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	tx, err := d.db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.Prepare(`INSERT OR REPLACE INTO ths_adj_factor (ts_code, trade_date, factor) VALUES (?,?,?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	var n int64
	for _, r := range rows {
		res, err := stmt.Exec(r.TsCode, r.TradeDate, r.Factor)
		if err != nil {
			return n, err
		}
		aff, _ := res.RowsAffected()
		n += aff
	}
	return n, tx.Commit()
}

// ThsCloseBefore 取某标的小于 date 的最近收盘价（复权乘数计算用）；无数据返回 ok=false。
func (d *DB) ThsCloseBefore(tsCode, date string) (float64, bool, error) {
	var c sql.NullFloat64
	err := d.db.QueryRow(`SELECT close FROM ths_daily WHERE ts_code=? AND trade_date<? ORDER BY trade_date DESC LIMIT 1`,
		tsCode, date).Scan(&c)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	if !c.Valid || c.Float64 <= 0 {
		return 0, false, nil
	}
	return c.Float64, true, nil
}

// ThsDatesSince 某标的自 since 起的全部交易日（升序，逐日展开因子用）。
func (d *DB) ThsDatesSince(tsCode, since string) ([]string, error) {
	rows, err := d.db.Query(`SELECT trade_date FROM ths_daily WHERE ts_code=? AND trade_date>=? ORDER BY trade_date`, tsCode, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var dt string
		if err := rows.Scan(&dt); err == nil {
			out = append(out, dt)
		}
	}
	return out, rows.Err()
}

// LegacyAdjFactorAt 旧 baostock 表在某标的≤date 的最近累计因子（窗口基线衔接用）。
func (d *DB) LegacyAdjFactorAt(tsCode, date string) (float64, bool, error) {
	var f sql.NullFloat64
	err := d.db.QueryRow(`SELECT adj_factor FROM adj_factor WHERE ts_code=? AND trade_date<=? ORDER BY trade_date DESC LIMIT 1`, tsCode, date).Scan(&f)
	if err == sql.ErrNoRows || (err == nil && (!f.Valid || f.Float64 <= 0)) {
		return 1, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return f.Float64, true, nil
}

// ThsAllCodes ths_daily 全部去重标的（复权因子逐日物化需遍历全代码——无事件标的也要
// 物化恒等基线行，保证引擎级原子切换时任一代码都有完整因子覆盖）。
func (d *DB) ThsAllCodes() ([]string, error) {
	rows, err := d.db.Query(`SELECT DISTINCT ts_code FROM ths_daily ORDER BY ts_code`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err == nil {
			out = append(out, c)
		}
	}
	return out, rows.Err()
}

// TestRouting 验证用（同包测试辅助）：构造双源数据。
func setupRoutingFixture(d *DB) error {
	if err := d.migrate(); err != nil {
		return err
	}
	rows := []ThsDailyRow{
		{TsCode: "000001.SZ", TradeDate: "20260820", Open: 10, High: 11, Low: 9.5, Close: 10.5, Vol: 100, Amount: 1050},
		{TsCode: "000001.SZ", TradeDate: "20260821", Open: 10.5, High: 12, Low: 10.4, Close: 11.8, Vol: 120, Amount: 1380},
	}
	if _, err := d.UpsertThsDailyRows(rows); err != nil {
		return err
	}
	// 旧表写入不同价格（可区分来源）
	if _, err := d.db.Exec(`INSERT OR REPLACE INTO daily (ts_code, trade_date, open, high, low, close, vol, amount)
		VALUES ('000001.SZ','20260820',99,99,99,99,999,999),
		       ('000001.SZ','20260821',98,98,98,98,998,998)`); err != nil {
		return err
	}
	// 因子逐日全覆盖（与物化保证一致：每个 ths_daily 日期都有因子行）
	if _, err := d.UpsertThsAdjFactorRows([]ThsAdjFactorRow{
		{TsCode: "000001.SZ", TradeDate: "20260820", Factor: 1.5},
		{TsCode: "000001.SZ", TradeDate: "20260821", Factor: 1.5},
	}); err != nil {
		return err
	}
	return nil
}
