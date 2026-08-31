// Package store — 股票池质控筛选（全市场回测前置过滤）。
// 剔除 ST/风险警示、退市整理期、多年连续亏损、长期地量股，并保证近期有足够行情，
// 让全量回测建立在可用交易标的池上而非简单的 maxstocks=300 字母序截断。
// English: quality screening for the full-market backtest universe.
package store

import (
	"fmt"
	"strings"
	"time"
)

// StockScreen 股票池质控筛选条件。
// zero-value ExcludeST/ExcludeDelist 为 false 时不剔除；其余 0 值自然禁用。
type StockScreen struct {
	// ExcludeST 剔除名称含 ST/退 的股票（含 *ST/风险警示/退市整理）。默认建议 true。
	ExcludeST bool
	// ExcludeDelist 剔除已退市/濒临退市（stocks.delist_date 非空）。默认建议 true。
	ExcludeDelist bool
	// MaxLossYears 连续年度净利亏损≥N 年剔除（按 income 年度报告 end_date LIKE %1231、
	// n_income_attr_p<0 计年数）。0=不查。默认建议 2。
	MaxLossYears int
	// MinRecentBars 近 WindowDays 个交易日内最少日线根数（0=不设，默认建议 20）。
	MinRecentBars int
	// WindowDays 流动性统计窗口（交易日，默认 60；<1 视为 60）。
	WindowDays int
	// MinAvgAmount 近 WindowDays 交易日日均成交额下限（元，0=不设，默认建议 3000万=3e7）。
	MinAvgAmount float64
	// End 筛选窗口结束日 YYYYMMDD（空=最近交易日/今天）。
	End string
}

// DefaultQualityScreen 全市场回测默认质控口径：剔 ST/退市、连续两年以上亏损、地量股，
// 并要求近 60 个交易日至少有 20 根日线（滤掉长期停牌/新上市无行情标的）。
// English: default quality screen — drop ST/delisted, multi-year loss, illiquid names.
func DefaultQualityScreen() StockScreen {
	return StockScreen{
		ExcludeST:     true,
		ExcludeDelist: true,
		MaxLossYears:  2,
		MinRecentBars: 20,
		WindowDays:    60,
		MinAvgAmount:  3e7, // 3000 万元
	}
}

// ScreenedCodes 返回通过质控筛选的股票代码（ORDER BY ts_code，与 StockCodes 同为全量口径）。
// 任一 SQL 查询失败即返回错误；筛选是交集语义：剔除集合用多层 EXISTS/NOT EXISTS 合并。
// English: returns the quality-screened stock universe (sorted); intersection of all active filters.
func (d *DB) ScreenedCodes(s StockScreen) ([]string, error) {
	end := strings.TrimSpace(s.End)
	if end == "" {
		end = time.Now().Format("20060102")
	}
	// 统计窗口结束日：有 trade_cal 用最近一个开盘日，否则用 End 本身。
	if d := d.nearBy(end, 0); d != "" {
		end = d
	}
	// 流动性窗口起点：从 trade_cal 往前取 WindowDays 个开盘日（无数据时兜底 End）。
	wd := s.WindowDays
	if wd < 1 {
		wd = 60
	}
	cutoff := d.nearBy(end, wd)
	if cutoff == "" {
		cutoff = end
	}

	// 基础剔除集：ST/风险警示/退市整理名称 + 已退市。
	var conds []string
	if s.ExcludeST {
		conds = append(conds, "(s.name NOT LIKE '%ST%' AND s.name NOT LIKE '%退%' AND s.name IS NOT NULL)")
	}
	if s.ExcludeDelist {
		conds = append(conds, "(COALESCE(s.delist_date,'') = '' OR s.delist_date > ?)")
	}

	// 连续亏损股票集（仅统计有年度利润表数据的标的）。
	lose := map[string]bool{}
	if s.MaxLossYears > 0 {
		rows, err := d.db.Query(`SELECT ts_code, COUNT(*) FROM income
			WHERE end_date LIKE '%1231' AND n_income_attr_p < 0
			GROUP BY ts_code HAVING COUNT(*) >= ?`, s.MaxLossYears)
		if err != nil {
			return nil, fmt.Errorf("亏损筛选: %w", err)
		}
		for rows.Next() {
			var code string
			var n int
			if err := rows.Scan(&code, &n); err != nil {
				rows.Close()
				return nil, err
			}
			lose[code] = true
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}

	// 流动性/近期行情：近 cutoff 起各票根数与日均额。
	liq := map[string]struct{ Bars int; Amt float64 }{}
	if s.MinRecentBars > 0 || s.MinAvgAmount > 0 {
		rows, err := d.db.Query(`SELECT ts_code, COUNT(*), AVG(amount) FROM daily
			WHERE trade_date >= ? GROUP BY ts_code`, cutoff)
		if err != nil {
			return nil, fmt.Errorf("流动性筛选: %w", err)
		}
		for rows.Next() {
			var code string
			var bars int
			var amt float64
			if err := rows.Scan(&code, &bars, &amt); err != nil {
				rows.Close()
				return nil, err
			}
			liq[code] = struct{ Bars int; Amt float64 }{bars, amt}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}

	sql := `SELECT s.ts_code FROM stocks s`
	if len(conds) > 0 {
		sql += ` WHERE ` + strings.Join(conds, " AND ")
	}
	sql += ` ORDER BY s.ts_code`
	var args []any
	if s.ExcludeDelist {
		args = append(args, end)
	}
	rows, err := d.db.Query(sql, args...)
	if err != nil {
		return nil, fmt.Errorf("质控股票池: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var code string
		if err := rows.Scan(&code); err != nil {
			return nil, err
		}
		if lose[code] {
			continue
		}
		if s.MinRecentBars > 0 || s.MinAvgAmount > 0 {
			l, ok := liq[code]
			if !ok {
				continue
			}
			if s.MinRecentBars > 0 && l.Bars < s.MinRecentBars {
				continue
			}
			if s.MinAvgAmount > 0 && l.Amt < s.MinAvgAmount {
				continue
			}
		}
		out = append(out, code)
	}
	return out, rows.Err()
}

// nearBy 返回以 ref 为基准第 offset 个历史开盘日（offset=0 返回最近开盘日）；
// trade_cal 无对应行时返回 ""。offset 从 0 开始：0=最新开盘日，1=上一个开盘日，以此类推。
// English: returns the offset-th open trading day at-or-before ref (0 = most recent);
// "" when no such calendar row exists.
func (d *DB) nearBy(ref string, offset int) string {
	var cal string
	err := d.db.QueryRow(`SELECT cal_date FROM trade_cal
		WHERE is_open=1 AND cal_date <= ? ORDER BY cal_date DESC LIMIT 1 OFFSET ?`, ref, offset).Scan(&cal)
	if err != nil {
		return ""
	}
	return cal
}