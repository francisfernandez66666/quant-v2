// 模拟盘研究落库（paper_trades / paper_daily）：
// 盘中模拟盘只在交易时段运行（省内存）；盘后把当日成交与每日净值快照导出到研究库，
// 供自动研究（夜间 scheduler / research CLI）做信号质量与绩效研究。
// English: paper-to-research persistence (paper_trades / paper_daily) — the paper book only runs during
// trading hours (memory friendly); after the close its fills and daily equity snapshot are exported to
// the research DB for auto-research to study signal quality and performance.
package store

import (
	"database/sql"
	"time"
)

// PaperTradeRecord 一条模拟盘成交（研究库版本，独立于 internal/paper 避免包依赖环）。
// English: one paper fill (research-DB flavor; keeps the store decoupled from internal/paper).
type PaperTradeRecord struct {
	UserID       string  `json:"user_id"`
	Code         string  `json:"code"`
	Name         string  `json:"name"`
	Strategy     string  `json:"strategy"`
	StrategyType string  `json:"strategy_type,omitempty"`
	Side         string  `json:"side"` // buy / sell
	Price        float64 `json:"price"`
	SignalPrice  float64 `json:"signal_price,omitempty"`
	LatencySec   float64 `json:"latency_sec,omitempty"`
	Qty          int     `json:"qty"`
	Amount       float64 `json:"amount"`
	FilledAt     string  `json:"filled_at"`
	Reason       string  `json:"reason,omitempty"`
}

// PaperDailyRecord 一条模拟盘每日快照（研究库版本）。
// English: one paper daily snapshot (research-DB flavor).
type PaperDailyRecord struct {
	UserID      string  `json:"user_id"`
	Date        string  `json:"date"` // YYYY-MM-DD
	Cash        float64 `json:"cash"`
	MarketValue float64 `json:"market_value"`
	TotalValue  float64 `json:"total_value"`
	Realized    float64 `json:"realized"`
	Positions   int     `json:"positions"`
}

// SavePaperTrades 批量写入模拟盘成交（盘后导出）。UNIQUE(user_id,code,side,filled_at) + INSERT OR
// IGNORE 保证幂等：同一笔成交重复导出不产生重复行，成交历史只增不改。
// English: bulk-inserts paper fills (post-close export). The UNIQUE(user_id,code,side,filled_at) key
// plus INSERT OR IGNORE keeps the export idempotent — re-exporting never duplicates a fill.
func (d *DB) SavePaperTrades(recs []PaperTradeRecord) error {
	if len(recs) == 0 {
		return nil
	}
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT OR IGNORE INTO paper_trades
		(user_id, code, name, strategy, strategy_type, side, price, signal_price, latency_sec, qty, amount, filled_at, reason)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, r := range recs {
		if _, err := stmt.Exec(r.UserID, r.Code, r.Name, r.Strategy, r.StrategyType, r.Side,
			r.Price, r.SignalPrice, r.LatencySec, r.Qty, r.Amount, r.FilledAt, r.Reason); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SavePaperDaily 写入/更新一条模拟盘每日快照（UPSERT：同一账号同一交易日覆盖）。
// English: upserts one paper daily snapshot (same account + trading day overwrites).
func (d *DB) SavePaperDaily(r PaperDailyRecord) error {
	_, err := d.db.Exec(`INSERT INTO paper_daily (user_id, date, cash, market_value, total_value, realized, positions)
		VALUES (?,?,?,?,?,?,?)
		ON CONFLICT(user_id, date) DO UPDATE SET
			cash=excluded.cash, market_value=excluded.market_value, total_value=excluded.total_value,
			realized=excluded.realized, positions=excluded.positions`,
		r.UserID, r.Date, r.Cash, r.MarketValue, r.TotalValue, r.Realized, r.Positions)
	return err
}

// PaperTradeSummary 模拟盘成交聚合（按战法池类型 + 方向分组），研究侧信号质量汇总用。
// English: aggregated paper fills (grouped by strategy-pool type + side) for research signal quality.
type PaperTradeSummary struct {
	StrategyType string  `json:"strategy_type"`
	Side         string  `json:"side"`
	Count        int     `json:"count"`
	TotalAmount  float64 `json:"total_amount"`
	AvgPrice     float64 `json:"avg_price"`
	AvgSlippage  float64 `json:"avg_slippage"` // 平均滑点 %（成交价 vs 信号价）
	AvgLatency   float64 `json:"avg_latency"`  // 平均延迟 秒
}

// PaperTradeSummaries 汇总 paper_trades（研究侧读取），按策略池类型+方向分组。
// English: summarizes paper_trades for the research side, grouped by pool type + side.
func (d *DB) PaperTradeSummaries() ([]PaperTradeSummary, error) {
	rows, err := d.db.Query(`SELECT strategy_type, side, COUNT(*), SUM(amount), AVG(price),
		AVG(CASE WHEN signal_price > 0 AND price > 0 THEN (price - signal_price) / signal_price * 100 ELSE 0 END),
		AVG(latency_sec) FROM paper_trades
		GROUP BY strategy_type, side ORDER BY strategy_type, side`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PaperTradeSummary
	for rows.Next() {
		var s PaperTradeSummary
		var st, side sql.NullString
		if err := rows.Scan(&st, &side, &s.Count, &s.TotalAmount, &s.AvgPrice, &s.AvgSlippage, &s.AvgLatency); err != nil {
			return nil, err
		}
		s.StrategyType = st.String
		s.Side = side.String
		out = append(out, s)
	}
	return out, rows.Err()
}

// PaperDailyAll 返回全部模拟盘每日快照（按用户+日期升序），研究侧净值序列用。
// English: returns all paper daily snapshots (ascending user/date) for research equity series.
func (d *DB) PaperDailyAll() ([]PaperDailyRecord, error) {
	rows, err := d.db.Query(`SELECT user_id, date, cash, market_value, total_value, realized, positions
		FROM paper_daily ORDER BY user_id, date`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PaperDailyRecord
	for rows.Next() {
		var r PaperDailyRecord
		if err := rows.Scan(&r.UserID, &r.Date, &r.Cash, &r.MarketValue, &r.TotalValue, &r.Realized, &r.Positions); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SavePaperResearchReport 保存一条模拟盘研究报告摘要（UPSERT：同一账号同一报告日期覆盖）。
// English: saves a paper-research report summary (UPSERT per account + report date).
func (d *DB) SavePaperResearchReport(date, userID, summaryJSON string) error {
	_, err := d.db.Exec(`INSERT INTO paper_research_reports (date, user_id, summary_json, created_at)
		VALUES (?,?,?,?)
		ON CONFLICT(date, user_id) DO UPDATE SET
			summary_json=excluded.summary_json, created_at=excluded.created_at`,
		date, userID, summaryJSON, time.Now().Format("2006-01-02 15:04:05"))
	return err
}
