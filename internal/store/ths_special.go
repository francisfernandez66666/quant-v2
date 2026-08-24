// ths_special.go 同花顺（新）盘口特色数据表：涨停/跌停/炸板三池、连板天梯、个股异动
// （§HITHINK_DATA_SOURCE_PLAN D3 数据层）。全部按 (trade_date, ts_code[, board/tag]) 幂等 upsert。
package store

import (
	"database/sql"
	"encoding/json"
)

// ThsLimitUpRow 涨停池行。
type ThsLimitUpRow struct {
	TradeDate     string // yyyyMMdd
	TsCode        string
	Name          string
	IsST          bool
	IsNew         bool // 未开板新股
	Price         float64
	PctChg        float64 // 涨跌幅（已×100）
	FirstSealTime string  // 首次封板 HH:MM
	ContinueCnt   int     // 连板数
	ContinueText  string  // "5天4板"
	LimitReason   string  // 涨停原因（可空）
	SealMoney     float64 // 当前封单额
	MaxSealMoney  float64 // 峰值封单额
}

// UpsertThsLimitUps 批量幂等写入涨停池。
func (d *DB) UpsertThsLimitUps(rows []ThsLimitUpRow) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	tx, err := d.db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.Prepare(`INSERT OR REPLACE INTO ths_limit_up_daily
		(trade_date, ts_code, name, is_st, is_new, price, pct_chg, first_seal_time,
		 continue_cnt, continue_text, limit_reason, seal_money, max_seal_money)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	var n int64
	for _, r := range rows {
		res, err := stmt.Exec(r.TradeDate, r.TsCode, r.Name, b2i(r.IsST), b2i(r.IsNew),
			r.Price, r.PctChg, r.FirstSealTime, r.ContinueCnt, r.ContinueText,
			r.LimitReason, r.SealMoney, r.MaxSealMoney)
		if err != nil {
			return n, err
		}
		aff, _ := res.RowsAffected()
		n += aff
	}
	return n, tx.Commit()
}

// LimitUpsOnDate 某交易日涨停池（B4 事件合成 / 龙头识别 / 情绪周期消费）。
func (d *DB) LimitUpsOnDate(date string) ([]ThsLimitUpRow, error) {
	rows, err := d.db.Query(`SELECT trade_date, ts_code, name, is_st, is_new, price, pct_chg,
		first_seal_time, continue_cnt, continue_text, limit_reason, seal_money, max_seal_money
		FROM ths_limit_up_daily WHERE trade_date=? ORDER BY continue_cnt DESC`, date)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ThsLimitUpRow
	for rows.Next() {
		var r ThsLimitUpRow
		var st, nw int
		var reason sql.NullString
		if err := rows.Scan(&r.TradeDate, &r.TsCode, &r.Name, &st, &nw, &r.Price, &r.PctChg,
			&r.FirstSealTime, &r.ContinueCnt, &r.ContinueText, &reason, &r.SealMoney, &r.MaxSealMoney); err != nil {
			return nil, err
		}
		r.IsST, r.IsNew = st == 1, nw == 1
		r.LimitReason = reason.String
		out = append(out, r)
	}
	return out, rows.Err()
}

// LimitUpCountOnDate 某日涨停家数（情绪周期输入）。
func (d *DB) LimitUpCountOnDate(date string) (int, error) {
	var n int
	err := d.db.QueryRow(`SELECT COUNT(*) FROM ths_limit_up_daily WHERE trade_date=?`, date).Scan(&n)
	return n, err
}

// ── 跌停池 / 炸板池（结构对称，字段较少）──

// UpsertThsSimplePool 通用三池写入（跌停/炸板共用简化列集）。
// table 仅允许白名单值，防拼接注入。
func (d *DB) UpsertThsSimplePool(table, tradeDate string, rows map[string]ThsPoolSimple) (int64, error) {
	switch table {
	case "ths_limit_down_daily", "ths_break_pool_daily":
	default:
		return 0, sql.ErrNoRows
	}
	if len(rows) == 0 {
		return 0, nil
	}
	tx, err := d.db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.Prepare(`INSERT OR REPLACE INTO ` + table + `
		(trade_date, ts_code, name, price, pct_chg, open_times, turnover_ratio_pct, turnover)
		VALUES (?,?,?,?,?,?,?,?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	var n int64
	for code, r := range rows {
		res, err := stmt.Exec(tradeDate, code, r.Name, r.Price, r.PctChg,
			r.OpenTimes, r.TurnoverRatioPct, r.Turnover)
		if err != nil {
			return n, err
		}
		aff, _ := res.RowsAffected()
		n += aff
	}
	return n, tx.Commit()
}

// ThsPoolSimple 三池简化行（跌停：open_times=0；炸板：open_times=开板次数）。
type ThsPoolSimple struct {
	Name             string
	Price            float64
	PctChg           float64
	OpenTimes        int
	TurnoverRatioPct float64
	Turnover         float64
}

// ThsLadderRows 连板天梯批量写入。
func (d *DB) UpsertThsLadder(rows []ThsLadderRow) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	tx, err := d.db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.Prepare(`INSERT OR REPLACE INTO ths_ladder_daily
		(trade_date, board_num, ts_code, name, seal_nextday, sign_level) VALUES (?,?,?,?,?,?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	var n int64
	for _, r := range rows {
		var sn any
		if r.SealNextDay != nil {
			sn = b2i(*r.SealNextDay)
		}
		res, err := stmt.Exec(r.TradeDate, r.BoardNum, r.TsCode, r.Name, sn, r.SignLevel)
		if err != nil {
			return n, err
		}
		aff, _ := res.RowsAffected()
		n += aff
	}
	return n, tx.Commit()
}

// ThsLadderRow 连板天梯物化行。
type ThsLadderRow struct {
	TradeDate   string
	BoardNum    int
	TsCode      string
	Name        string
	SealNextDay *bool
	SignLevel   int
}

// UpsertThsAnomalies 异动原因批量写入（keywords 序列化为 JSON 数组）。
func (d *DB) UpsertThsAnomalies(tradeDate string, items []struct {
	ThsCode         string
	Name            string
	TagName         string
	AnalysisContent string
	KeywordList     []string
}) (int64, error) {
	if len(items) == 0 {
		return 0, nil
	}
	tx, err := d.db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.Prepare(`INSERT OR REPLACE INTO ths_anomaly_daily
		(trade_date, ts_code, tag_name, name, analysis_content, keywords)
		VALUES (?,?,?,?,?,?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	var n int64
	for _, it := range items {
		kw, _ := json.Marshal(it.KeywordList)
		res, err := stmt.Exec(tradeDate, it.ThsCode, it.TagName, it.Name, it.AnalysisContent, string(kw))
		if err != nil {
			return n, err
		}
		aff, _ := res.RowsAffected()
		n += aff
	}
	return n, tx.Commit()
}

// AnomalyForCode 某标的某日异动原因（D1 归因辅证查询）。
func (d *DB) AnomalyForCode(tsCode, date string) ([]map[string]string, error) {
	rows, err := d.db.Query(`SELECT tag_name, analysis_content, keywords FROM ths_anomaly_daily
		WHERE trade_date=? AND ts_code=?`, date, tsCode)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]string
	for rows.Next() {
		var tag, content, kw string
		if err := rows.Scan(&tag, &content, &kw); err == nil {
			out = append(out, map[string]string{"tag": tag, "analysis": content, "keywords": kw})
		}
	}
	return out, rows.Err()
}

// b2i 布尔转整数（SQLite 存储）。
func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
