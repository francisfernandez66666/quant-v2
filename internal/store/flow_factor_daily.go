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
		  market_flow_pct=COALESCE(excluded.market_flow_pct, flow_factor_daily.market_flow_pct),
		  sector_flow_pct=COALESCE(excluded.sector_flow_pct, flow_factor_daily.sector_flow_pct),
		  r_yest=COALESCE(excluded.r_yest, flow_factor_daily.r_yest),
		  r_ma5=COALESCE(excluded.r_ma5, flow_factor_daily.r_ma5),
		  flow_market_delta=COALESCE(excluded.flow_market_delta, flow_factor_daily.flow_market_delta),
		  flow_sector_delta=COALESCE(excluded.flow_sector_delta, flow_factor_daily.flow_sector_delta),
		  vol_surge_delta=COALESCE(excluded.vol_surge_delta, flow_factor_daily.vol_surge_delta),
		  source=excluded.source, updated_at=excluded.updated_at`,
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

// UpsertFlowVolumeOnly 只写「量比腿」三列（r_yest/r_ma5/vol_surge_delta）+ source/updated_at，
// **不碰** market_flow_pct/sector_flow_pct/flow_market_delta/flow_sector_delta 四列。
//
// 为什么单独一个写口（§FLOW-FACTOR-DAILY 2026-11-19）：
//   - 数据源分两块——量比腿走云端东财指数日 K（盘中每 30s 折算刷新），资金流腿等 QMT/云端资金流
//     写入。两块各自更新，全字段 UpsertFlowFactorDaily 会互相把对方的值盖成 NULL。
//   - 写入方是引擎 lazy seed（当日无行时插入，有行时只刷量比列），资金流列保持探针/资金流写入的原值。
//
// English: upsert only the volume-leg columns; never clobber the money-flow columns written by the other source.
func (d *DB) UpsertFlowVolumeOnly(r FlowFactorDailyRow) error {
	if r.TradeDate == "" {
		return fmt.Errorf("flow_factor_daily: empty trade_date")
	}
	_, err := d.db.Exec(`INSERT INTO flow_factor_daily
		(trade_date, r_yest, r_ma5, vol_surge_delta, source, updated_at)
		VALUES (?,?,?,?,?,?)
		ON CONFLICT(trade_date) DO UPDATE SET
		  r_yest=COALESCE(excluded.r_yest, flow_factor_daily.r_yest),
		  r_ma5=COALESCE(excluded.r_ma5, flow_factor_daily.r_ma5),
		  vol_surge_delta=COALESCE(excluded.vol_surge_delta, flow_factor_daily.vol_surge_delta),
		  source=excluded.source,
		  updated_at=excluded.updated_at`,
		r.TradeDate, nnz(r.RYest), nnz(r.RMa5), nnz(r.VolSurgeDelta), r.Source,
		time.Now().Format("2006-01-02 15:04:05"))
	if err != nil {
		return fmt.Errorf("flow_factor_daily volume-only upsert: %w", err)
	}
	return nil
}

// UpsertFlowMoneyOnly 只写「资金流腿」四列（market_flow_pct/sector_flow_pct/flow_market_delta/
// flow_sector_delta）+ source/updated_at，**不碰**量比三列（r_yest/r_ma5/vol_surge_delta）。
// 与 UpsertFlowVolumeOnly 对称：资金流腿（云端东财 fflow，60s TTL）与量比腿（指数日 K，30s TTL）
// 是两块独立数据源，各自 UPSERT 互不盖 NULL——全字段 Upsert 会把对方刚写的列冲掉。
// English: upsert only the money-flow columns; never clobber the volume-leg columns.
func (d *DB) UpsertFlowMoneyOnly(r FlowFactorDailyRow) error {
	if r.TradeDate == "" {
		return fmt.Errorf("flow_factor_daily: empty trade_date")
	}
	_, err := d.db.Exec(`INSERT INTO flow_factor_daily
		(trade_date, market_flow_pct, sector_flow_pct, flow_market_delta, flow_sector_delta, source, updated_at)
		VALUES (?,?,?,?,?,?,?)
		ON CONFLICT(trade_date) DO UPDATE SET
		  market_flow_pct=COALESCE(excluded.market_flow_pct, flow_factor_daily.market_flow_pct),
		  sector_flow_pct=COALESCE(excluded.sector_flow_pct, flow_factor_daily.sector_flow_pct),
		  flow_market_delta=COALESCE(excluded.flow_market_delta, flow_factor_daily.flow_market_delta),
		  flow_sector_delta=COALESCE(excluded.flow_sector_delta, flow_factor_daily.flow_sector_delta),
		  source=excluded.source, updated_at=excluded.updated_at`,
		r.TradeDate, nnz(r.MarketFlowPct), nnz(r.SectorFlowPct),
		nnz(r.FlowMarketDelta), nnz(r.FlowSectorDelta), r.Source,
		time.Now().Format("2006-01-02 15:04:05"))
	if err != nil {
		return fmt.Errorf("flow_factor_daily money-only upsert: %w", err)
	}
	return nil
}

// SectorFlowDailyRow 一行板块日级资金流（按板块名对齐 GetStockIndustry 的行业名）。
type SectorFlowDailyRow struct {
	TradeDate string  // 北京日 YYYY-MM-DD
	Sector    string  // 板块名（东财行业板块名，与 GetStockIndustry 返回同串）
	NetInflow float64 // 主力净流入（元，当日累计）
	Amount    float64 // 板块当日成交额（元，f6）——归一分母
	Delta     float64 // 归一强度 ∈[-1,1]（净额/成交额，3% 满档）
}

// UpsertSectorFlowDaily 幂等写一行板块读数（主键 trade_date+sector）。
func (d *DB) UpsertSectorFlowDaily(r SectorFlowDailyRow) error {
	if r.TradeDate == "" || r.Sector == "" {
		return fmt.Errorf("sector_flow_daily: empty trade_date/sector")
	}
	_, err := d.db.Exec(`INSERT INTO sector_flow_daily
		(trade_date, sector, net_inflow, amount, delta, updated_at)
		VALUES (?,?,?,?,?,?)
		ON CONFLICT(trade_date, sector) DO UPDATE SET
		  net_inflow=excluded.net_inflow, amount=excluded.amount, delta=excluded.delta,
		  updated_at=excluded.updated_at`,
		r.TradeDate, r.Sector, r.NetInflow, r.Amount, r.Delta,
		time.Now().Format("2006-01-02 15:04:05"))
	if err != nil {
		return fmt.Errorf("sector_flow_daily upsert: %w", err)
	}
	return nil
}

// GetSectorFlowDaily 读指定交易日某板块的读数；无行返回 (nil, nil)。
func (d *DB) GetSectorFlowDaily(tradeDate, sector string) (*SectorFlowDailyRow, error) {
	if tradeDate == "" || sector == "" {
		return nil, fmt.Errorf("sector_flow_daily: empty key")
	}
	var r SectorFlowDailyRow
	var ni, amt, dl sql.NullFloat64
	err := d.db.QueryRow(`SELECT trade_date, sector, net_inflow, amount, delta
		FROM sector_flow_daily WHERE trade_date=? AND sector=?`, tradeDate, sector).
		Scan(&r.TradeDate, &r.Sector, &ni, &amt, &dl)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("sector_flow_daily get: %w", err)
	}
	r.NetInflow, r.Amount, r.Delta = ni.Float64, amt.Float64, dl.Float64
	return &r, nil
}

// CountSectorFlowDaily 统计某交易日已烘入的板块读数行数（读面用，不参与判定）。
//
// 为什么需要它：逐票板块腿的命中率在**载体**侧算（sector_hit=N/M），而云端这一侧的
// "今天到底烘了几个板块"只在库里。仪表盘要能区分「板块腿今天有读数」与「今天一个都没有」——
// 0 行是如实读数（现网因缺 sector_map 生产者就是 0），不是错误，但也绝不能画成"中性"。
// English: count today's baked sector rows; 0 is an honest reading (no sector_map producer yet),
// not an error and definitely not "neutral".
func (d *DB) CountSectorFlowDaily(tradeDate string) (int, error) {
	if tradeDate == "" {
		return 0, fmt.Errorf("sector_flow_daily: empty trade_date")
	}
	var n int
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM sector_flow_daily WHERE trade_date=?`, tradeDate).Scan(&n); err != nil {
		return 0, fmt.Errorf("sector_flow_daily count: %w", err)
	}
	return n, nil
}
