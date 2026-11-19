// flow_factor_daily.go — §FLOW-FACTOR-DAILY 资金流/相对放量字段的按日滚动留痕（2026-11-19）。
// 每个交易日一条（主键 trade_date，重复 UPSERT 为最新一轮），存大盘/板块净流入百分比、
// 相对放量 r_yest/r_ma5 与三项 delta。缺值存 NULL（NaN 弃权），聚合按 NULL 跳过，绝不当 0。
// 探针（今晚 QMT）回来后逐日写入，引擎装配 Regime 时按当日读取；跨日不复活、不累计——
// 与 market_risk_daily / signal_decisions 同一条按日纪律。
// English: daily rolling record of flow/volume fields; one row per trading day (PK trade_date,
// upsert), NULL for abstained inputs, read by Regime for the current Beijing day only.
package store

import (
	"database/sql"
	"fmt"
	"math"
	"time"
)

// FlowFactorDailyRow 一行日级资金流/放量字段。指针 nil→NULL（NaN 弃权）。
type FlowFactorDailyRow struct {
	TradeDate       string
	MarketFlowPct   *float64 // 大盘净流入百分比（原生 QMT zjbyNetInflow 归一）
	SectorFlowPct   *float64 // 板块净流入百分比
	RYest           *float64 // 昨日量比 v[-1]/v[-2]
	RMa5            *float64 // 五日均量比 v[-1]/mean(v[-6:-1])
	FlowMarketDelta *float64 // 大盘信号强度 ∈[-1,1]
	FlowSectorDelta *float64 // 板块信号强度 ∈[-1,1]
	VolSurgeDelta   *float64 // 相对放量强度 ∈[-1,1]
	Source          string   // 数据来源标记（qmt_native / eastmoney_approx / 空）
}

// UpsertFlowFactorDaily 幂等写入当日字段（主键 trade_date，重复即覆盖为最新一轮快照）。
func (d *DB) UpsertFlowFactorDaily(r FlowFactorDailyRow) error {
	if r.TradeDate == "" {
		return fmt.Errorf("flow_factor_daily: empty trade_date")
	}
	_, err := d.db.Exec(`INSERT INTO flow_factor_daily
		(trade_date, market_flow_pct, sector_flow_pct, r_yest, r_ma5,
		 flow_market_delta, flow_sector_delta, vol_surge_delta, source, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(trade_date) DO UPDATE SET
		  market_flow_pct=excluded.market_flow_pct, sector_flow_pct=excluded.sector_flow_pct,
		  r_yest=excluded.r_yest, r_ma5=excluded.r_ma5,
		  flow_market_delta=excluded.flow_market_delta, flow_sector_delta=excluded.flow_sector_delta,
		  vol_surge_delta=excluded.vol_surge_delta, source=excluded.source,
		  updated_at=excluded.updated_at`,
		r.TradeDate, nnz(r.MarketFlowPct), nnz(r.SectorFlowPct),
		nnz(r.RYest), nnz(r.RMa5), nnz(r.FlowMarketDelta), nnz(r.FlowSectorDelta),
		nnz(r.VolSurgeDelta), r.Source,
		time.Now().Format("2006-01-02 15:04:05"))
	if err != nil {
		return fmt.Errorf("flow_factor_daily upsert: %w", err)
	}
	return nil
}

// GetFlowFactorDaily 读取指定交易日的一条字段；无该行返回 (nil, nil)。
// 调用方按北京自然日取 trade_date——跨日不复活是调用方/装配层的纪律（本函数只按 key 取）。
func (d *DB) GetFlowFactorDaily(tradeDate string) (*FlowFactorDailyRow, error) {
	if tradeDate == "" {
		return nil, fmt.Errorf("flow_factor_daily: empty trade_date")
	}
	var r FlowFactorDailyRow
	var mf, sf, ry, rm, fmd, fsd, vsd sql.NullFloat64
	err := d.db.QueryRow(`SELECT trade_date, market_flow_pct, sector_flow_pct, r_yest, r_ma5,
		flow_market_delta, flow_sector_delta, vol_surge_delta, source
		FROM flow_factor_daily WHERE trade_date=?`, tradeDate).
		Scan(&r.TradeDate, &mf, &sf, &ry, &rm, &fmd, &fsd, &vsd, &r.Source)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("flow_factor_daily get: %w", err)
	}
	r.MarketFlowPct, r.SectorFlowPct = np(mf), np(sf)
	r.RYest, r.RMa5 = np(ry), np(rm)
	r.FlowMarketDelta, r.FlowSectorDelta, r.VolSurgeDelta = np(fmd), np(fsd), np(vsd)
	return &r, nil
}

// ListFlowFactorDaily 读取 [from,to] 区间的日级字段（to 空=不设上界），按日期升序。供按日滚动检查/复盘。
func (d *DB) ListFlowFactorDaily(from, to string) ([]FlowFactorDailyRow, error) {
	q := `SELECT trade_date, market_flow_pct, sector_flow_pct, r_yest, r_ma5,
		flow_market_delta, flow_sector_delta, vol_surge_delta, source
		FROM flow_factor_daily WHERE trade_date >= ?`
	args := []interface{}{from}
	if to != "" {
		q += " AND trade_date <= ?"
		args = append(args, to)
	}
	q += " ORDER BY trade_date ASC"
	rows, err := d.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("flow_factor_daily list: %w", err)
	}
	defer rows.Close()
	var out []FlowFactorDailyRow
	for rows.Next() {
		var r FlowFactorDailyRow
		var mf, sf, ry, rm, fmd, fsd, vsd sql.NullFloat64
		if err := rows.Scan(&r.TradeDate, &mf, &sf, &ry, &rm, &fmd, &fsd, &vsd, &r.Source); err != nil {
			return nil, fmt.Errorf("flow_factor_daily scan: %w", err)
		}
		r.MarketFlowPct, r.SectorFlowPct = np(mf), np(sf)
		r.RYest, r.RMa5 = np(ry), np(rm)
		r.FlowMarketDelta, r.FlowSectorDelta, r.VolSurgeDelta = np(fmd), np(fsd), np(vsd)
		out = append(out, r)
	}
	return out, rows.Err()
}

// np 把 sql.NullFloat64 转为 *float64（无效→nil）。English: NullFloat64 → *float64 (invalid → nil).
func np(v sql.NullFloat64) *float64 {
	if !v.Valid || math.IsNaN(v.Float64) {
		return nil
	}
	f := v.Float64
	return &f
}
