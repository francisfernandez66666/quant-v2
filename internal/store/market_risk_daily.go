// market_risk_daily.go — §MARKET_RISK_GATE P8 市场风险档日级留痕读写。
// 引擎每交易日落一条当日风险档快照（情绪/市场状态/合成档 + 关键输入），供按风险档分组回测（看信号在不同
// 风险档下的胜率/收益差）与事后复盘。缺失输入（涨跌家数/炸板率取数失败）存 NULL，聚合按 NULL 跳过，绝不当 0。
// English: P8 daily risk-tier record. The Engine writes one row per trading day (emotion / market state /
// synthesized tier + key inputs) for tier-grouped backtests (signal win-rate / return spread across tiers)
// and review. Missing inputs (breadth/break-rate fetch failures) are stored NULL so aggregation skips them
// rather than treating them as 0.
package store

import (
	"database/sql"
	"fmt"
	"math"
	"time"
)

// MarketRiskDailyRow 一行日级风险档留痕。UpRatio/BreakRate/MaxPosPct 用指针：nil→NULL（NaN 弃权）。
// English: one daily risk-tier record; the nullable float pointers map to NULL (NaN abstain).
type MarketRiskDailyRow struct {
	TradeDate    string
	Emotion      string
	MarketState  string
	RiskTier     string
	Reasons      string
	UpRatio      *float64
	BreakRate    *float64
	MaxPosPct    *float64
	LimitUpCount int
	LadderHeight int
}

// UpsertMarketRiskDaily 幂等写入当日风险档（主键 trade_date，重复即覆盖为最新一轮快照）。
// English: idempotently upserts today's risk-tier row (PK trade_date; a repeat overwrites with the latest).
func (d *DB) UpsertMarketRiskDaily(r MarketRiskDailyRow) error {
	if r.TradeDate == "" {
		return fmt.Errorf("market_risk_daily: empty trade_date")
	}
	_, err := d.db.Exec(`INSERT INTO market_risk_daily
		(trade_date, emotion, market_state, risk_tier, reasons, up_ratio, break_rate, max_pos_pct, limit_up_count, ladder_height, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(trade_date) DO UPDATE SET
		  emotion=excluded.emotion, market_state=excluded.market_state, risk_tier=excluded.risk_tier,
		  reasons=excluded.reasons, up_ratio=excluded.up_ratio, break_rate=excluded.break_rate,
		  max_pos_pct=excluded.max_pos_pct, limit_up_count=excluded.limit_up_count,
		  ladder_height=excluded.ladder_height, updated_at=excluded.updated_at`,
		r.TradeDate, r.Emotion, r.MarketState, r.RiskTier, r.Reasons,
		nnz(r.UpRatio), nnz(r.BreakRate), nnz(r.MaxPosPct),
		r.LimitUpCount, r.LadderHeight, time.Now().Format("2006-01-02 15:04:05"))
	if err != nil {
		return fmt.Errorf("market_risk_daily upsert: %w", err)
	}
	return nil
}

// ListMarketRiskDaily 读取 [from,to] 区间的日级风险档（to 为空=不设上界），按日期升序。
// 供分组回测/复盘消费。English: reads the daily risk-tier rows in [from,to] (empty to = open upper bound), ascending.
func (d *DB) ListMarketRiskDaily(from, to string) ([]MarketRiskDailyRow, error) {
	q := `SELECT trade_date, emotion, market_state, risk_tier, reasons, up_ratio, break_rate, max_pos_pct, limit_up_count, ladder_height
		FROM market_risk_daily WHERE trade_date >= ?`
	args := []interface{}{from}
	if to != "" {
		q += " AND trade_date <= ?"
		args = append(args, to)
	}
	q += " ORDER BY trade_date ASC"
	rows, err := d.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("market_risk_daily list: %w", err)
	}
	defer rows.Close()
	var out []MarketRiskDailyRow
	for rows.Next() {
		var r MarketRiskDailyRow
		var upR, br, mp sql.NullFloat64
		if err := rows.Scan(&r.TradeDate, &r.Emotion, &r.MarketState, &r.RiskTier, &r.Reasons,
			&upR, &br, &mp, &r.LimitUpCount, &r.LadderHeight); err != nil {
			return nil, fmt.Errorf("market_risk_daily scan: %w", err)
		}
		if upR.Valid {
			v := upR.Float64
			r.UpRatio = &v
		}
		if br.Valid {
			v := br.Float64
			r.BreakRate = &v
		}
		if mp.Valid {
			v := mp.Float64
			r.MaxPosPct = &v
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// MarketRiskTierSummary 按风险档分组聚合日数（None/Yellow/Red），供回测概览/看板。
// English: groups the daily rows by tier (None/Yellow/Red) with day counts for a backtest overview.
type MarketRiskTierSummary struct {
	Tier    string
	Days    int
	AvgUp   *float64 // 该档平均上涨占比（全 NULL 时 nil）
	AvgBrk  *float64 // 该档平均炸板率
}

// SummarizeMarketRiskDaily 统计 [from,to] 各风险档的日数 + 平均广度/炸板率（NULL 跳过）。
// English: summarizes day counts + mean up-ratio/break-rate per tier over [from,to], skipping NULLs.
func (d *DB) SummarizeMarketRiskDaily(from, to string) ([]MarketRiskTierSummary, error) {
	list, err := d.ListMarketRiskDaily(from, to)
	if err != nil {
		return nil, err
	}
	type agg struct {
		days          int
		upSum, upN    float64
		brSum, brN    float64
	}
	byTier := map[string]*agg{}
	order := []string{"", "Yellow", "Red"}
	for _, r := range list {
		a := byTier[r.RiskTier]
		if a == nil {
			a = &agg{}
			byTier[r.RiskTier] = a
		}
		a.days++
		if r.UpRatio != nil {
			a.upSum += *r.UpRatio
			a.upN++
		}
		if r.BreakRate != nil {
			a.brSum += *r.BreakRate
			a.brN++
		}
	}
	var out []MarketRiskTierSummary
	for _, t := range order {
		if a := byTier[t]; a != nil && a.days > 0 {
			s := MarketRiskTierSummary{Tier: tierLabel(t), Days: a.days}
			if a.upN > 0 {
				v := a.upSum / a.upN
				s.AvgUp = &v
			}
			if a.brN > 0 {
				v := a.brSum / a.brN
				s.AvgBrk = &v
			}
			out = append(out, s)
		}
	}
	return out, nil
}

// tierLabel 空档展示为 "None"。English: the empty tier is labeled "None" for display.
func tierLabel(t string) string {
	if t == "" {
		return "None"
	}
	return t
}

// nnz 把 NaN 指针转为 nil（→NULL），非 NaN 原样。English: maps a NaN pointer to nil (→NULL).
func nnz(p *float64) interface{} {
	if p == nil || math.IsNaN(*p) {
		return nil
	}
	return *p
}
