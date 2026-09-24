// 分钟 K 线落库层（§MINUTE-K，2026-09-24 owner 裁决「分钟 K 落库升级：建表 + 回填 + 动量回放换真 5 分钟」）。
//
// 为什么要这一张表：动量战法的实盘判据读的是 **5 分钟 K 线算出来的 MACD**
// （strategy_engine/engine.go fetchMinuteKLine：scale=5、count=48 → data.CalcMACD），而研究库此前
// 只有日线，回放只能拿日线 MACD 顶替（btreplay/replay.go momentumAdapter 近似口径 1）。日线 MACD
// 比 5 分钟 MACD 迟钝，金叉/水上平均滞后一日——量出来的动量数字和实盘那批单子不是同一件事，
// 这正是"回放可信度"上最大的一块欠账。
//
// 口径三条（都是有意为之，改任何一条都会让这张表失去意义）：
//  1. **不复权**：分钟上游（新浪/腾讯/东财）给的就是真实成交价的不复权价，与实盘 md.MinuteMACD
//     同一口径；日线侧的不复权口径另有 RawBars/adj_basis 体系，两者**不得互相换算混用**
//     （分钟表里没有复权因子列，就是想让人无法顺手混用）。
//  2. **ts 是北京时间墙钟字符串** "YYYY-MM-DD HH:MM:SS"（上游原样）：字符串可直接字典序排序，
//     且"同一交易日的 48 根"就是 ts 前缀相同的一段——回放按日切片时不必再做时区换算。
//  3. **主键 (ts_code, scale, ts)**：写入一律 INSERT OR REPLACE，幂等由主键保证；
//     上游只有"最近 N 根"窗口（无分页），所以收盘后增量续拉与回填走同一条 upsert 路径，
//     重复拉同一段时间不会造出双行，也不会覆盖成半根。
package store

import (
	"database/sql"
	"fmt"
	"strings"
)

// MinuteBar 一根分钟 K 线（未复权）。
// （MinuteBar is one unadjusted minute K-line row; Ts is Beijing wall clock "YYYY-MM-DD HH:MM:SS".）
type MinuteBar struct {
	TsCode string  // 证券代码（600000.SH 形态，与 daily.ts_code 同一写法）
	Scale  int     // 周期分钟数（本表只写 5，列留着以便日后加 15/30/60 不需改表）
	Ts     string  // 北京时间墙钟 "2026-09-24 14:35:00"
	Open   float64 // 开盘
	High   float64 // 最高
	Low    float64 // 最低
	Close  float64 // 收盘
	Vol    float64 // 成交量（与日线 vol 同单位：上游给什么存什么，不做手/股换算）
	Amount float64 // 成交额（元）
}

// UpsertMinuteBars 幂等批量写入分钟 K（单事务 INSERT OR REPLACE）。
// 空切片直接返回 0（不建事务）；列清单取自 TableColumns，未知列会被 validateInsertSurface 拦下。
// （UpsertMinuteBars idempotently bulk-writes minute bars in one transaction.）
func (d *DB) UpsertMinuteBars(bars []MinuteBar) (int64, error) {
	if len(bars) == 0 {
		return 0, nil
	}
	cols := TableColumns("minute_klines")
	rows := make([]map[string]any, 0, len(bars))
	for _, b := range bars {
		if strings.TrimSpace(b.TsCode) == "" || b.Ts == "" || b.Scale <= 0 {
			return 0, fmt.Errorf("store minute_klines: 主键三件套缺一（ts_code=%q scale=%d ts=%q）", b.TsCode, b.Scale, b.Ts)
		}
		rows = append(rows, map[string]any{
			"ts_code": b.TsCode, "scale": b.Scale, "ts": b.Ts,
			"open": b.Open, "high": b.High, "low": b.Low, "close": b.Close,
			"vol": b.Vol, "amount": b.Amount,
		})
	}
	return d.InsertRows("minute_klines", cols, rows)
}

// MinuteBarsByDay 取某股某周期在某一交易日（"YYYY-MM-DD"）的全部分钟根，按 ts 升序。
// 回放动量用的就是这一条：实盘喂 CalcMACD 的是"当日最近 48 根 5 分钟"，日粒度收盘时点
// 恰好等于当日全部 5 分钟根，故调用方拿到后仍要**自己截尾 48 根**（本函数不代做裁剪，
// 免得有人以为这里已经裁过、把半天数据当成全天口径）。
// （MinuteBarsByDay returns one day's minute bars ascending; trimming to the live 48-bar window
// is intentionally left to the caller.）
func (d *DB) MinuteBarsByDay(tsCode string, scale int, day string) ([]MinuteBar, error) {
	// 右边界用 "day 24:00:00"：ts 的时分秒恒为两位、日内最大 "23:55:00"，字典序上"24:"必然大于
	// 任何真实时刻，于是"前缀范围"等价于"这一天的全部行"，且能吃到 (ts_code,scale,ts) 主键索引
	// （substr(ts,1,10)=? 的写法要全表扫，回放按日取数会退化成几千次扫描）。
	rows, err := d.db.Query(`SELECT ts_code, scale, ts,
		COALESCE(open,0), COALESCE(high,0), COALESCE(low,0), COALESCE(close,0),
		COALESCE(vol,0), COALESCE(amount,0)
		FROM minute_klines WHERE ts_code=? AND scale=? AND ts>=? AND ts<? ORDER BY ts`,
		tsCode, scale, day+" 00:00:00", day+" 24:00:00")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMinuteRows(rows)
}

// MinuteCodeDay 一格的分钟覆盖情况（某股某周期在某交易日有多少根）。
type MinuteCodeDay struct {
	TsCode string
	Scale  int
	Day    string
	Count  int
}

// MinuteDayCounts 一次查出一批交易日各自的根数（按日分组），供回放侧统计覆盖率与
// dataload 打印"哪些日子数据不全"。day 形如 "2026-09-24"。
// （MinuteDayCounts returns per-day bar counts for one code/scale — coverage accounting.）
func (d *DB) MinuteDayCounts(tsCode string, scale int) ([]MinuteCodeDay, error) {
	rows, err := d.db.Query(`SELECT ts_code, scale, substr(ts,1,10), COUNT(*)
		FROM minute_klines WHERE ts_code=? AND scale=? GROUP BY substr(ts,1,10) ORDER BY substr(ts,1,10)`,
		tsCode, scale)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MinuteCodeDay
	for rows.Next() {
		var v MinuteCodeDay
		if err := rows.Scan(&v.TsCode, &v.Scale, &v.Day, &v.Count); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// MinuteCodes 列出库里已存分钟数据的代码（去重、升序）。收盘后增量续拉的默认清单就是它——
// 自维护：回填过的票继续补，没人手工维护名单，也不会因为某天新增了股票就把老票漏掉。
// （MinuteCodes lists codes already stored for a scale — the nightly incremental's work list.）
func (d *DB) MinuteCodes(scale int) ([]string, error) {
	rows, err := d.db.Query(`SELECT DISTINCT ts_code FROM minute_klines WHERE scale=? ORDER BY ts_code`, scale)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// MinuteHasBars 该周期表内是否已有任意一行（O(1) 探针，走 (scale, ts) 索引取一行即停）。
// 调度器每 30s tick 都要判一次"分钟日增该不该入队"，所以这里绝不能用 COUNT(*)——
// 250 万行的整表计数会把例行 tick 拖成秒级扫描（要读数用 MinuteTableStats）。
// （MinuteHasBars is the constant-time "any minute rows?" probe for the scheduler gate.）
func (d *DB) MinuteHasBars(scale int) (bool, error) {
	var one int
	err := d.db.QueryRow(`SELECT 1 FROM minute_klines WHERE scale=? LIMIT 1`, scale).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// MinuteMaxTs 某股某周期已存的最早/最新 ts（空表返回空串）。增量续拉按"是否已覆盖最近交易日"
// 判断要不要拉，回填断点也用它（上游只有最近 N 根窗口，一旦拉到过就不必反复重刷整段）。
// （MinuteMaxTs returns the stored min/max ts for a code/scale, empty strings when absent.）
func (d *DB) MinuteMaxTs(tsCode string, scale int) (minTs, maxTs string, err error) {
	q := `SELECT COALESCE(MIN(ts),''), COALESCE(MAX(ts),'') FROM minute_klines WHERE ts_code=? AND scale=?`
	if err = d.db.QueryRow(q, tsCode, scale).Scan(&minTs, &maxTs); err != nil {
		return "", "", err
	}
	return minTs, maxTs, nil
}

// MinuteStats 分钟表总览（行数、去重股票数、覆盖日期区间、按周期分组）。
// 供 dataload 出门打印与 verify 探针读数——**没有这张表时就如实返回 0 行**，
// 调用方据此区分"没数据"和"取数失败"（本仓反复出事的"降级报成功"形态，这里不留口子）。
// （MinuteStats summarizes the minute table for loaders and probes.）
type MinuteStats struct {
	Rows    int     `json:"rows"`
	Codes   int     `json:"codes"`
	Scale   int     `json:"scale"`
	FirstTs string  `json:"first_ts"`
	LastTs  string  `json:"last_ts"`
	AvgBars float64 `json:"avg_bars_per_code_day"` // 平均每(票,日)根数：远低于 48 说明上游窗口被截
}

func (s MinuteStats) String() string {
	return fmt.Sprintf("rows=%d codes=%d span=%s..%s avg_bars/code_day=%.1f",
		s.Rows, s.Codes, s.FirstTs, s.LastTs, s.AvgBars)
}

// MinuteTableStats 按周期统计整表。
func (d *DB) MinuteTableStats(scale int) (MinuteStats, error) {
	var st MinuteStats
	st.Scale = scale
	q := `SELECT COUNT(*), COUNT(DISTINCT ts_code), COALESCE(MIN(ts),''), COALESCE(MAX(ts),''),
		COUNT(DISTINCT ts_code || '|' || substr(ts,1,10))
		FROM minute_klines WHERE scale=?`
	var codeDays int
	if err := d.db.QueryRow(q, scale).Scan(&st.Rows, &st.Codes, &st.FirstTs, &st.LastTs, &codeDays); err != nil {
		if err == sql.ErrNoRows {
			return st, nil
		}
		return st, err
	}
	if codeDays > 0 {
		st.AvgBars = float64(st.Rows) / float64(codeDays)
	}
	return st, nil
}

// MinutePoolUniverse 回填的缺省取数清单：**打过板/炸过板的票**（ths_limit_up_daily ∪
// ths_break_pool_daily 在 [since, ~] 内出现过的 ts_code），按出现次数降序取前 limit 只。
// 为什么用池而不用全市场：分钟线上游只有"最近 5025 根"这一扇窗口（≈5 个月），全市场 5000+ 只
// 一次性灌进来是 2500 万行、2GB 量级，而动量判据真正需要分钟口径的是这些日内有波动的票；
// owner 批准的规模也是"≈250 万行"。limit 上限由调用方把关（本函数只负责排序与截断）。
// （MinutePoolUniverse: default backfill universe = limit-up/break-pool codes in the window,
// ranked by appearance count and capped at limit — bounded on purpose, not the whole market.）
func (d *DB) MinutePoolUniverse(since string, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 500
	}
	rows, err := d.db.Query(`SELECT ts_code, COUNT(*) c FROM (
		SELECT ts_code FROM ths_limit_up_daily WHERE trade_date>=?
		UNION ALL
		SELECT ts_code FROM ths_break_pool_daily WHERE trade_date>=?
	) GROUP BY ts_code ORDER BY c DESC, ts_code LIMIT ?`, since, since, limit)
	if err == nil {
		defer rows.Close()
		var out []string
		for rows.Next() {
			var s string
			var c int
			if err := rows.Scan(&s, &c); err != nil {
				return nil, err
			}
			out = append(out, s)
		}
		return out, rows.Err()
	}
	// 两张池表都还没数据（全新库/池同步未跑）时不能"降级成全市场"——那会把 2GB 量级的写入
	// 悄悄塞进一次例行回填。这里如实报错，让调用方显式给 --codes 文件。
	return nil, fmt.Errorf("分钟回填取池失败（池表可能为空）: %w", err)
}

// scanMinuteRows 把查询结果行装成 MinuteBar 切片（两处读法共用，避免列序写歪）。
func scanMinuteRows(rows *sql.Rows) ([]MinuteBar, error) {
	var out []MinuteBar
	for rows.Next() {
		var b MinuteBar
		if err := rows.Scan(&b.TsCode, &b.Scale, &b.Ts, &b.Open, &b.High, &b.Low, &b.Close, &b.Vol, &b.Amount); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
