// 板块历史（E5）：按行业聚合的板块日线，离线从 daily/stk_limit/stocks 重建，
// 供形态战法回测的"板块共振"与因子环境分组使用；实盘板块快照可增量落盘。
// English: sector history (E5) — per-industry aggregated board daily bars, rebuilt offline from
// daily/stk_limit/stocks, consumed by pattern backtests (板块共振) and factor environment grouping;
// live board snapshots can also be written incrementally.
package store

import (
	"encoding/json"
	"sort"
)

// SectorDay 单个行业在单个交易日的板块聚合结果。
// English: one industry's board aggregation on one trade date.
type SectorDay struct {
	TradeDate   string   // 交易日 YYYYMMDD
	Industry    string   // 行业名（stocks.industry）
	LimitupCnt  int      // 当日板块内涨停家数（板块共振强度）
	ChangePct   float64  // 当日板块平均涨跌幅（%）
	MemberCount int      // 当日板块内有成交的股票数
	TopStocks   []string // 当日涨幅居前的股票（前 5）
}

// RebuildSectorHistory 全量重建板块历史：按 trade_date 逐日聚合各行业
// 涨停家数/平均涨跌幅/成分数/领涨股。使用 INSERT OR REPLACE 幂等覆盖。
// 复杂度可控：按日期索引（idx_daily_date）逐日扫描。返回写入的行数。
// English: rebuilds the full sector history — per trade date, aggregates each industry's limit-up
// count, mean change %, member count and top gainers. Idempotent via INSERT OR REPLACE. Scans one
// date at a time using the trade_date index. Returns rows written.
func (d *DB) RebuildSectorHistory(start, end string) (int, error) {
	dates, err := d.TradeDates(start, end)
	if err != nil {
		return 0, err
	}
	total := 0
	for _, date := range dates {
		days, err := d.aggregateSectorDay(date)
		if err != nil {
			return total, err
		}
		for _, sd := range days {
			top, _ := json.Marshal(sd.TopStocks)
			n, err := d.InsertRows("sector_history", TableColumns("sector_history"), []map[string]any{{
				"trade_date": sd.TradeDate, "industry": sd.Industry,
				"limitup_cnt": sd.LimitupCnt, "change_pct": sd.ChangePct,
				"member_count": sd.MemberCount, "top_stocks": string(top),
			}})
			if err != nil {
				return total, err
			}
			total += int(n)
		}
	}
	return total, nil
}

// aggregateSectorDay 聚合单个交易日的全部行业板块数据（涨停数/平均涨跌幅/领涨股）。
// English: aggregates every industry's board data for one trade date.
func (d *DB) aggregateSectorDay(date string) ([]SectorDay, error) {
	return d.aggregateSectorDayFull(date)
}

// aggregateSectorDayFull 通过三组专用查询聚合单日板块数据（涨停数/平均涨跌幅/领涨股）。
// English: aggregates one day's sector data via three dedicated queries (limit-up count, mean
// change, top gainers).
func (d *DB) aggregateSectorDayFull(date string) ([]SectorDay, error) {
	// 行业清单 + 成员数 + 平均涨跌幅（一次聚合）
	aggQ := `SELECT s.industry, COUNT(*), COALESCE(AVG(dv.pct_chg),0)
		FROM daily dv JOIN stocks s ON s.ts_code = dv.ts_code
		WHERE dv.trade_date = ? AND s.industry IS NOT NULL AND s.industry != '' AND dv.close > 0
		GROUP BY s.industry`
	rows, err := d.db.Query(aggQ, date)
	if err != nil {
		return nil, err
	}
	// agg 板块聚合行：板块名 + 股票数 + 涨跌幅。
	type agg struct {
		ind   string
		count int
		pct   float64
	}
	var aggs []agg
	for rows.Next() {
		var a agg
		if err := rows.Scan(&a.ind, &a.count, &a.pct); err != nil {
			rows.Close()
			return nil, err
		}
		aggs = append(aggs, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// 涨停家数（板块共振强度）
	luQ := `SELECT s.industry, COUNT(*)
		FROM daily dv
		JOIN stk_limit l ON l.ts_code = dv.ts_code AND l.trade_date = dv.trade_date
		JOIN stocks s ON s.ts_code = dv.ts_code
		WHERE dv.trade_date = ? AND s.industry IS NOT NULL AND s.industry != ''
		  AND dv.close >= l.up_limit * 0.995 AND dv.close > 0
		GROUP BY s.industry`
	luRows, err := d.db.Query(luQ, date)
	if err != nil {
		return nil, err
	}
	lu := make(map[string]int)
	for luRows.Next() {
		var ind string
		var n int
		if err := luRows.Scan(&ind, &n); err != nil {
			luRows.Close()
			return nil, err
		}
		lu[ind] = n
	}
	luRows.Close()

	// 领涨股（各行业当日涨幅前 5）
	top, err := d.topStocksByIndustry(date)
	if err != nil {
		return nil, err
	}

	out := make([]SectorDay, 0, len(aggs))
	for _, a := range aggs {
		out = append(out, SectorDay{
			TradeDate: date, Industry: a.ind,
			LimitupCnt: lu[a.ind], ChangePct: a.pct,
			MemberCount: a.count, TopStocks: top[a.ind],
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Industry < out[j].Industry })
	return out, nil
}

// topStocksByIndustry 返回每个行业当日涨幅居前 5 的股票（并列按代码序）。
// English: returns each industry's top-5 gainers for a date (ties broken by code).
func (d *DB) topStocksByIndustry(date string) (map[string][]string, error) {
	query := `SELECT s.industry, s.ts_code, COALESCE(dv.pct_chg,0) FROM daily dv
		JOIN stocks s ON s.ts_code = dv.ts_code
		WHERE dv.trade_date = ? AND s.industry IS NOT NULL AND s.industry != '' AND dv.close > 0`
	rows, err := d.db.Query(query, date)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// pick 板块内个股候选：代码 + 涨跌幅。
	type pick struct {
		code string
		pct  float64
	}
	byInd := make(map[string][]pick)
	for rows.Next() {
		var ind, code string
		var pct float64
		if err := rows.Scan(&ind, &code, &pct); err != nil {
			return nil, err
		}
		byInd[ind] = append(byInd[ind], pick{code, pct})
	}
	out := make(map[string][]string, len(byInd))
	for ind, picks := range byInd {
		sort.SliceStable(picks, func(i, j int) bool {
			if picks[i].pct != picks[j].pct {
				return picks[i].pct > picks[j].pct
			}
			return picks[i].code < picks[j].code
		})
		n := len(picks)
		if n > 5 {
			n = 5
		}
		codes := make([]string, n)
		for i := 0; i < n; i++ {
			codes[i] = picks[i].code
		}
		out[ind] = codes
	}
	return out, nil
}

// SectorHistory 查询 [start,end] 区间内某行业的板块历史（升序）。
// industry 为空串时返回全部行业（按 trade_date, industry 排序）。
// English: queries sector history for one industry over [start,end] ascending; an empty industry
// returns all (ordered by trade_date, industry).
func (d *DB) SectorHistory(industry, start, end string) ([]SectorDay, error) {
	query := `SELECT trade_date, industry, COALESCE(limitup_cnt,0), COALESCE(change_pct,0),
		COALESCE(member_count,0), COALESCE(top_stocks,'[]')
		FROM sector_history WHERE trade_date >= ? AND trade_date <= ?`
	args := []any{start, end}
	if industry != "" {
		query += ` AND industry = ?`
		args = append(args, industry)
	}
	query += ` ORDER BY trade_date, industry`
	rows, err := d.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SectorDay
	for rows.Next() {
		var sd SectorDay
		var top string
		if err := rows.Scan(&sd.TradeDate, &sd.Industry, &sd.LimitupCnt, &sd.ChangePct, &sd.MemberCount, &top); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(top), &sd.TopStocks)
		out = append(out, sd)
	}
	return out, rows.Err()
}

// SectorLimitUpCounts 返回某日期各行业涨停家数（板块共振强度，供形态回测）。
// English: per-industry limit-up counts on a date (board-resonance strength for pattern backtests).
func (d *DB) SectorLimitUpCounts(date string) (map[string]int, error) {
	rows, err := d.db.Query(`SELECT industry, COALESCE(limitup_cnt,0) FROM sector_history WHERE trade_date = ?`, date)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]int)
	for rows.Next() {
		var ind string
		var n int
		if err := rows.Scan(&ind, &n); err != nil {
			return nil, err
		}
		out[ind] = n
	}
	return out, rows.Err()
}
