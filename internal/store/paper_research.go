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
	UserID       string  `json:"user_id"`                 // 归属用户
	Code         string  `json:"code"`                    // 代码
	Name         string  `json:"name"`                    // 名称
	Strategy     string  `json:"strategy"`                // 战法
	StrategyType string  `json:"strategy_type,omitempty"` // 战法类型
	Side         string  `json:"side"`                    // buy / sell（买入/卖出）
	Price        float64 `json:"price"`                   // 价格
	SignalPrice  float64 `json:"signal_price,omitempty"`  // 信号价
	LatencySec   float64 `json:"latency_sec,omitempty"`   // 信号→成交延迟（秒）
	Qty          int     `json:"qty"`                     // 数量
	Amount       float64 `json:"amount"`                  // 成交额
	FilledAt     string  `json:"filled_at"`               // 成交时间
	Reason       string  `json:"reason,omitempty"`        // 原因
}

// PaperDailyRecord 一条模拟盘每日快照（研究库版本）。
// English: one paper daily snapshot (research-DB flavor).
type PaperDailyRecord struct {
	UserID      string  `json:"user_id"`      // 归属用户
	Date        string  `json:"date"`         // YYYY-MM-DD（基准日）
	Cash        float64 `json:"cash"`         // 现金
	MarketValue float64 `json:"market_value"` // 市值
	TotalValue  float64 `json:"total_value"`  // 总资产
	Realized    float64 `json:"realized"`     // 已实现盈亏
	Positions   int     `json:"positions"`    // 持仓数
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
	StrategyType string  `json:"strategy_type"` // 战法类型
	Side         string  `json:"side"`          // 方向
	Count        int     `json:"count"`         // 数量
	TotalAmount  float64 `json:"total_amount"`  // Total成交额
	AvgPrice     float64 `json:"avg_price"`     // Avg价格
	AvgSlippage  float64 `json:"avg_slippage"`  // 平均滑点 %（成交价 vs 信号价）
	AvgLatency   float64 `json:"avg_latency"`   // 平均延迟 秒
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

// PaperAttribution 归因喂回（§Phase4）：按 用户+战法 分组统计盘中信号→成交的
// 承接质量（笔数/成交额/平均滑点/平均延迟），并把每战法当日最新净值差还原为增量收益。
// 供研究侧判断：哪些战法信号的成交兑现好（滑点小、成交快），喂回优化排序与失败聚类。
// English: PaperAttribution (Phase 4) — groups signal-to-fill quality by user+strategy (count/amount/
// avg slippage/avg latency) plus the latest realized delta per strategy, so the research side can rank
// which strategy signals fill well (low slippage, fast fills) and feed that back into optimization and
// failure clustering.
type PaperAttribution struct {
	UserID, Strategy string  // 用户 + 战法
	Count            int     // 成交笔数
	TotalAmount      float64 // 成交额
	AvgSlippage      float64 // 平均滑点 %
	AvgLatency       float64 // 平均延迟 秒
	BuyCount         int     // 买入笔数
	SellCount        int     // 卖出笔数
}

// PaperAttributions 归因查询（按 用户+战法，含方向计数）。
// English: attribution query (grouped by user+strategy with per-side counts).
func (d *DB) PaperAttributions() ([]PaperAttribution, error) {
	rows, err := d.db.Query(`SELECT user_id, strategy, COUNT(*), SUM(amount),
		AVG(CASE WHEN signal_price > 0 AND price > 0 THEN (price - signal_price) / signal_price * 100 ELSE 0 END),
		AVG(latency_sec),
		SUM(CASE WHEN side='buy' THEN 1 ELSE 0 END),
		SUM(CASE WHEN side='sell' THEN 1 ELSE 0 END)
		FROM paper_trades GROUP BY user_id, strategy ORDER BY COUNT(*) DESC, SUM(amount) DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PaperAttribution
	for rows.Next() {
		var a PaperAttribution
		if err := rows.Scan(&a.UserID, &a.Strategy, &a.Count, &a.TotalAmount,
			&a.AvgSlippage, &a.AvgLatency, &a.BuyCount, &a.SellCount); err != nil {
			return nil, err
		}
		out = append(out, a)
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
