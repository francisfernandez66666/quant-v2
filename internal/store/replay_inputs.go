// replay_inputs.go — §REPLAY-INPUTS 2026-11-28：回放输入面四张新表的读写单点。
//
// 方案：docs/PLAN_FACTOR_REPLAY_INPUTS_PIT_20261128.md（档 2/档 3）。
// 本文件存在的意义是把三类容易散落的东西钉在一个地方：
//
//  1. **日尺**：本批四张表一律紧凑 `YYYYMMDD`（与 `daily/index_daily` 同形），上游带横线的
//     日期只在**写入边界**经 cntime.CompactDay 归一一次。库里同一张表绝不两种形态并存——
//     "查错形状"在本仓已经造成三次"永远今天没数、且不报错"（flow_factor_daily 横线 /
//     sector_map.day 紧凑 / daily.trade_date 紧凑，三处各有实录）。
//  2. **NULL 与弃权的分界**：读不到用 `ok=false` 返回，绝不用 0 顶班。资金流金额缺失、
//     分母为 0、股本缺行都走 ok=false；把"没有读数"写成 0 会把它变成一条**方向可能相反**的
//     真读数（0 在净流入语义里是"中性"，另一回事）。
//  3. **幂等**：四张表都是 INSERT OR REPLACE（见 InsertRows），重复跑行数不涨。
//     断点续跑靠调用方的状态文件，不靠"查一下有没有写过"。
//
// English: single home for the four new replay-input tables — date-shape normalization at the
// write boundary only, (value, ok) abstention instead of fabricated zeros, idempotent upserts.
package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"quant-trading-v2/internal/cntime"
)

// StockFlowRow 一(票,日)的主力净流入**原子量**（方案 §3.2：只存原子量，比率一律不进表）。
//
// 指针字段＝上游这一列确实没有（NULL），与"值是 0"是两件事：
//   - MainPct NULL：上游 f57 缺失/非数 ⇒ 该日不可参与 Flow5d 分母；
//   - Amount NULL：由 pct=0/缺 反推不出来 ⇒ 同上。
//
// 一旦把"没有"折叠成 0，5 日累计就会算出一个看着正常、方向却可能错一整档的比率。
type StockFlowRow struct {
	TsCode        string  // 带后缀（600519.SH），与 daily 同写法
	TradeDate     string  // 紧凑 YYYYMMDD
	MainNet       float64 // 主力净额（元）
	HasMainNet    bool    // 上游给了这一列
	MainPct       float64 // f57 主力净占比＝净额/成交额×100（%）
	HasMainPct    bool
	Amount        float64 // 当日成交额（元）＝MainNet/(MainPct/100)，推不出为 NULL
	HasAmount     bool
	SmallNet      float64 // 小单净额（元），留作后续资金结构因子
	MediumNet     float64 // 中单净额（元）
	LargeNet      float64 // 大单净额（元）
	SuperLargeNet float64 // 超大单净额（元）
	Source        string  // eastmoney_fflow_relay_seed / eastmoney_fflow_relay_live
	FetchedAt     string  // 落库时刻（北京时间墙钟，排障用）
}

// FloatShareRow 一(票,日)的流通股本（股）。**按日**存而不是"一票一行 + asof"——
// 用今天的股本去折算三个月前的净流入率，遇到解禁/定增/回购就是污染，且事后无从复核。
type FloatShareRow struct {
	TsCode    string
	TradeDate string // 紧凑 YYYYMMDD
	Share     float64
	Source    string // precalc_seed_v1 / tushare_daily_basic / manual
}

// LeaderRankRow 一(票,日)的同板块涨幅分位（0..1，龙头代理 §SectorGainRank 的输入）。
// Sector 一起存：审计要能回答"这个分位是在哪个板块里算出来的"，只留一个 float 事后没法复核。
type LeaderRankRow struct {
	TsCode    string
	TradeDate string // 紧凑 YYYYMMDD
	Sector    string
	PctChg    float64
	HasPctChg bool
	GainRank  float64 // 同板块内涨幅分位 0..1
	HasRank   bool
}

// UpsertStockMoneyFlow 批量幂等写入个股资金流日频档。
// 返回写入行数；TradeDate 形状不认识整批拒绝（不是跳过坏行——半批落库会让"今天写了几只"
// 这个数从此对不上，而它正是覆盖率读数的分子）。
func (d *DB) UpsertStockMoneyFlow(rows []StockFlowRow) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	cols := TableColumns("stock_moneyflow_daily")
	out := make([]map[string]any, 0, len(rows))
	for i, r := range rows {
		day := cntime.CompactDay(r.TradeDate)
		if day == "" {
			return 0, fmt.Errorf("store: stock_moneyflow_daily 第 %d 行 trade_date 形状不可认 %q（要求 YYYYMMDD 或 YYYY-MM-DD）", i, r.TradeDate)
		}
		if strings.TrimSpace(r.TsCode) == "" {
			return 0, fmt.Errorf("store: stock_moneyflow_daily 第 %d 行 ts_code 为空", i)
		}
		rec := map[string]any{
			"ts_code":         r.TsCode,
			"trade_date":      day,
			"source":          r.Source,
			"fetched_at":      r.FetchedAt,
			"small_net":       r.SmallNet,
			"medium_net":      r.MediumNet,
			"large_net":       r.LargeNet,
			"super_large_net": r.SuperLargeNet,
		}
		// 三个可空列按 Has* 决定写值还是写 NULL——0 与"没读数"在这里必须分得开。
		if r.HasMainNet {
			rec["main_net"] = r.MainNet
		}
		if r.HasMainPct {
			rec["main_pct"] = r.MainPct
		}
		if r.HasAmount {
			rec["amount"] = r.Amount
		}
		out = append(out, rec)
	}
	n, err := d.InsertRows("stock_moneyflow_daily", cols, out)
	if err != nil {
		return 0, fmt.Errorf("写入个股资金流失败: %w", err)
	}
	return n, nil
}

// StockFlowSeries 取某票 ≤endDay 的最近 limit 个交易日资金流，**返回升序**（最旧在前）。
// 调用方要"连续交易日"的判据请拿 `daily` 的交易日序对（本函数只管取数，不做完整性判定），
// 完整性判定放在装配层——那里才知道"5 日"这条腿的语义是什么。
func (d *DB) StockFlowSeries(code, endDay string, limit int) ([]StockFlowRow, error) {
	if limit <= 0 {
		limit = 5
	}
	end := cntime.CompactDay(endDay)
	if end == "" {
		return nil, fmt.Errorf("store: StockFlowSeries endDay 形状不可认 %q", endDay)
	}
	// 子查询取"最近 limit 行"再外层升序排：直接 ORDER BY trade_date LIMIT 会给出倒序，
	// 装配层按 [0]=最早理解时会把窗口整体错位一天（载体侧 _series_on_bars 栽过同型坑）。
	rows, err := d.db.Query(`SELECT ts_code, trade_date, main_net, main_pct, amount,
			small_net, medium_net, large_net, super_large_net, source, fetched_at
		FROM (SELECT * FROM stock_moneyflow_daily
			WHERE ts_code=? AND trade_date<=? ORDER BY trade_date DESC LIMIT ?)
		ORDER BY trade_date ASC`, code, end, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StockFlowRow
	for rows.Next() {
		var (
			r                    StockFlowRow
			mainNet, mainPct     sql.NullFloat64
			amount               sql.NullFloat64
			small, medium, large sql.NullFloat64
			super                sql.NullFloat64
		)
		if err := rows.Scan(&r.TsCode, &r.TradeDate, &mainNet, &mainPct, &amount,
			&small, &medium, &large, &super, &r.Source, &r.FetchedAt); err != nil {
			return nil, err
		}
		r.MainNet, r.HasMainNet = mainNet.Float64, mainNet.Valid
		r.MainPct, r.HasMainPct = mainPct.Float64, mainPct.Valid
		r.Amount, r.HasAmount = amount.Float64, amount.Valid
		r.SmallNet = small.Float64
		r.MediumNet = medium.Float64
		r.LargeNet = large.Float64
		r.SuperLargeNet = super.Float64
		out = append(out, r)
	}
	return out, rows.Err()
}

// MaxStockFlowDay 全表最新数据日（紧凑 YYYYMMDD）；空表返回 ""（不是 0 天前的某个日期）。
func (d *DB) MaxStockFlowDay() (string, error) {
	var v sql.NullString
	if err := d.db.QueryRow(`SELECT MAX(trade_date) FROM stock_moneyflow_daily`).Scan(&v); err != nil {
		return "", err
	}
	if !v.Valid {
		return "", nil
	}
	return v.String, nil
}

// CountStockFlowCodes 某数据日有多少只票有行（覆盖率分子）。
// 注意判的是"行数"而不是"MAX(date)"——只看末行会把"今天只补了三成"读成"全部到齐"。
func (d *DB) CountStockFlowCodes(day string) (int, error) {
	var n int
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM stock_moneyflow_daily WHERE trade_date=?`,
		cntime.CompactDay(day)).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// StockFlowDepth 每票平均行数与最深行数（首采是否真拿满上游 120 根的自证读数）。
func (d *DB) StockFlowDepth() (codes, rowsTotal int, maxRowsPerCode int, err error) {
	if err = d.db.QueryRow(`SELECT COUNT(DISTINCT ts_code), COUNT(*) FROM stock_moneyflow_daily`).
		Scan(&codes, &rowsTotal); err != nil {
		return 0, 0, 0, err
	}
	if err = d.db.QueryRow(`SELECT COALESCE(MAX(n),0) FROM (SELECT COUNT(*) n FROM stock_moneyflow_daily GROUP BY ts_code)`).
		Scan(&maxRowsPerCode); err != nil {
		return 0, 0, 0, err
	}
	return codes, rowsTotal, maxRowsPerCode, nil
}

// UpsertFloatShareDaily 幂等写入流通股本日频档；Share<=0 整批拒绝（0 股本会让占流通市值
// 那条腿除出天文数字或直接 NaN，写进去就是把坏数存档）。
func (d *DB) UpsertFloatShareDaily(rows []FloatShareRow) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	out := make([]map[string]any, 0, len(rows))
	for i, r := range rows {
		day := cntime.CompactDay(r.TradeDate)
		if day == "" {
			return 0, fmt.Errorf("store: stock_float_share_daily 第 %d 行 trade_date 形状不可认 %q", i, r.TradeDate)
		}
		if r.Share <= 0 {
			return 0, fmt.Errorf("store: stock_float_share_daily 第 %d 行 float_share<=0（缺股本请**不要写这一行**，缺行＝弃权）", i)
		}
		out = append(out, map[string]any{
			"ts_code": r.TsCode, "trade_date": day, "float_share": r.Share, "source": r.Source,
		})
	}
	n, err := d.InsertRows("stock_float_share_daily", TableColumns("stock_float_share_daily"), out)
	if err != nil {
		return 0, fmt.Errorf("写入流通股本失败: %w", err)
	}
	return n, nil
}

// FloatShareOn 取某票当日流通股本。只认**当日精确命中**，不做"最近的凑"——
// 股本是会变的东西，拿上一周的值当今天用，等于给占流通市值分母偷偷换一个分母。
func (d *DB) FloatShareOn(code, day string) (float64, bool) {
	var v sql.NullFloat64
	err := d.db.QueryRow(`SELECT float_share FROM stock_float_share_daily WHERE ts_code=? AND trade_date=?`,
		code, cntime.CompactDay(day)).Scan(&v)
	if err != nil || !v.Valid {
		return 0, false
	}
	return v.Float64, true
}

// UpsertSectorMapDay 写某一日的逐票板块归属快照（生产者已产出一份 json，这里只是把它存档，
// 让"板块归属"从单日快照变成可回溯的历史——否则龙头/板块分位永远只能在快照当天之后有效）。
// 空表整批拒绝：写一份"当日没人有板块"的表比不写更坏（它会让命中率读数看起来正常而为 0）。
func (d *DB) UpsertSectorMapDay(day string, entries map[string]string, typeOf func(string) string) (int64, error) {
	if len(entries) == 0 {
		return 0, fmt.Errorf("store: sector_map_daily 当日映射为空（全量或弃权，绝不落空表）")
	}
	compact := cntime.CompactDay(day)
	if compact == "" {
		return 0, fmt.Errorf("store: sector_map_daily day 形状不可认 %q", day)
	}
	out := make([]map[string]any, 0, len(entries))
	for code, sector := range entries {
		sector = strings.TrimSpace(sector)
		if code == "" || sector == "" {
			continue // 归属空的行不进表：连接键缺失就是"没有板块腿"，不是"板块名叫空串"
		}
		typ := ""
		if typeOf != nil {
			typ = typeOf(code)
		}
		out = append(out, map[string]any{
			"trade_date": compact, "ts_code": code, "sector": sector, "sector_type": typ,
		})
	}
	if len(out) == 0 {
		return 0, fmt.Errorf("store: sector_map_daily 当日映射全为空板块名（拒绝落库）")
	}
	n, err := d.InsertRows("sector_map_daily", TableColumns("sector_map_daily"), out)
	if err != nil {
		return 0, fmt.Errorf("写入板块归属历史失败: %w", err)
	}
	return n, nil
}

// SectorMapAsOf 取 ≤day 的最近一份板块归属，账龄超过 maxAgeDays 个**日历日**则整表弃权。
// 返回值第三项 false＝"没有可用归属表"，调用方必须让相关腿 NaN，绝不许退回"当日快照"或空串。
func (d *DB) SectorMapAsOf(day string, maxAgeDays int) (map[string]string, string, bool) {
	end := cntime.CompactDay(day)
	if end == "" || maxAgeDays <= 0 {
		return nil, "", false
	}
	var got string
	if err := d.db.QueryRow(`SELECT MAX(trade_date) FROM sector_map_daily WHERE trade_date<=?`, end).Scan(&got); err != nil {
		return nil, "", false
	}
	if got == "" {
		return nil, "", false
	}
	// 账龄用日期差而不是字符串差（"20261101" 与 "20261031" 差 1 但字典序差 31）。
	age, ok := dayAgeCompact(got, end)
	if !ok || age > maxAgeDays {
		return nil, got, false
	}
	rows, err := d.db.Query(`SELECT ts_code, sector FROM sector_map_daily WHERE trade_date=?`, got)
	if err != nil {
		return nil, got, false
	}
	defer rows.Close()
	out := make(map[string]string)
	for rows.Next() {
		var c, s string
		if err := rows.Scan(&c, &s); err == nil && strings.TrimSpace(s) != "" {
			out[c] = s
		}
	}
	if len(out) == 0 {
		return nil, got, false
	}
	return out, got, true
}

// dayAgeCompact 两个紧凑日期之间的日历日差（解析失败 ok=false＝"算不出账龄"，不是 0 天）。
func dayAgeCompact(from, to string) (int, bool) {
	a, err1 := parseCompactDay(from)
	b, err2 := parseCompactDay(to)
	if err1 != nil || err2 != nil {
		return 0, false
	}
	days := int(b.Sub(a).Hours() / 24)
	if days < 0 {
		return 0, false
	}
	return days, true
}

// parseCompactDay 解析紧凑日期为北京时区时刻。时区必须显式给（cntime.Loc）——
// 用 time.Parse 会落到 UTC，跨零点时账龄会差一天（本仓时区收口就是为这类漂移做的）。
func parseCompactDay(s string) (time.Time, error) {
	return time.ParseInLocation("20060102", s, cntime.Loc)
}

// UpsertLeaderRankDay 幂等写入当日龙头代理分位（由预计算生产者算，不在逐股装配里算横截面）。
func (d *DB) UpsertLeaderRankDay(rows []LeaderRankRow) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	out := make([]map[string]any, 0, len(rows))
	for i, r := range rows {
		day := cntime.CompactDay(r.TradeDate)
		if day == "" {
			return 0, fmt.Errorf("store: leader_rank_daily 第 %d 行 trade_date 形状不可认 %q", i, r.TradeDate)
		}
		rec := map[string]any{"trade_date": day, "ts_code": r.TsCode, "sector": r.Sector}
		if r.HasPctChg {
			rec["pct_chg"] = r.PctChg
		}
		if r.HasRank {
			rec["gain_rank"] = r.GainRank
		}
		out = append(out, rec)
	}
	n, err := d.InsertRows("leader_rank_daily", TableColumns("leader_rank_daily"), out)
	if err != nil {
		return 0, fmt.Errorf("写入龙头分位失败: %w", err)
	}
	return n, nil
}

// LeaderRankOn 取某票当日龙头代理分位；缺行/缺值都返回 ok=false（NaN 弃权，不写 0——
// 0 在"分位"语义里是"同板块最弱"，是一条**方向相反**的真读数）。
func (d *DB) LeaderRankOn(code, day string) (float64, bool) {
	var v sql.NullFloat64
	err := d.db.QueryRow(`SELECT gain_rank FROM leader_rank_daily WHERE ts_code=? AND trade_date=?`,
		code, cntime.CompactDay(day)).Scan(&v)
	if err != nil || !v.Valid {
		return 0, false
	}
	return v.Float64, true
}

// IndexDailyCoverage 各指数在库里的 (行数, 末行日期)——回填前后各读一次自证。
// 返回空 map 不报错（表可能刚建还没数据），但调用方必须把"读不到"如实报成 unknown。
func (d *DB) IndexDailyCoverage() (map[string][2]any, error) {
	rows, err := d.db.Query(`SELECT ts_code, COUNT(*), MAX(trade_date) FROM index_daily GROUP BY ts_code`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string][2]any)
	for rows.Next() {
		var code string
		var n int
		var maxd sql.NullString
		if err := rows.Scan(&code, &n, &maxd); err != nil {
			return nil, err
		}
		out[code] = [2]any{n, maxd.String}
	}
	return out, rows.Err()
}

// PureCode 去掉交易所后缀（600519.SH → 600519；430047.BJ → 430047）。
//
// 口径刻意保持"只剥已知后缀"（.SH/.SZ/.BJ），而不是"切到第一个点"：
// 本函数的一个直接消费者是实盘幂等键（signal_id）的构造，把 `600519.XX` 这种异常形态
// 悄悄切成 `600519` 会让两把键不再是同一把——原 engine 实现就是这么防的，收口时不改语义。
//
// 为什么放在 store：本仓此前有三份同职能实现（engine.pureTsCode / data.stripSuffix /
// 各处字符串切分），`internal/data/exchange.go` 文件头自记"三处各自实现交易所归属且口径矛盾"
// 就是历史缺陷的成因。本函数是对外唯一口径，`engine.pureTsCode` 已改为转调它。
func PureCode(tsCode string) string {
	for _, suf := range []string{".SH", ".SZ", ".BJ"} {
		if i := strings.LastIndex(tsCode, suf); i > 0 {
			return tsCode[:i]
		}
	}
	return tsCode
}

// D1ScoresRange 一次性把区间内的 D1 事件分读成 map[紧凑日]map[6位码]分。
//
// 为什么整表读而不是逐票查（方案 §1.2 第 2 条）：现网实测 d1_scores 全表只有
// 60681 行 / 67 个日期，整表进内存约 10 MB；逐票查会变成 4309 次查询、
// 回放凭空多十几分钟，而且 d1_scores 的索引只建在 date 上（按 code 查是全表扫）。
//
// 同 (日,票) 多行取 created_at 最大的一条：D1 是会被复核改分的，
// 取第一条等于把"最早那次的判断"当成最终判断。
func (d *DB) D1ScoresRange(start, end string) (map[string]map[string]float64, error) {
	s, e := cntime.CompactDay(start), cntime.CompactDay(end)
	if s == "" || e == "" {
		return nil, fmt.Errorf("store: D1ScoresRange 日尺不可认 start=%q end=%q", start, end)
	}
	// ★ 边界必须是横线形态：d1_scores.date 存的是 `2026-11-27`，而本函数返回的是紧凑日。
	// 拿紧凑串直接 BETWEEN 会**静默查空**（'2026-11-27' < '20261101' 因为 '-'(0x2D)<'0'(0x30)），
	// 表现是"D1 腿一整天没读数而分数看着正常"——正是本批文档 §3.2 点名的那个坑，
	// 写到这里自己踩了一次，所以转换必须写在这一处并留注释。
	rows, err := d.db.Query(`SELECT date, code, score, created_at FROM d1_scores
		WHERE date BETWEEN ? AND ? ORDER BY created_at ASC`, cntime.DayOf(mustDay(s)), cntime.DayOf(mustDay(e)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]map[string]float64)
	for rows.Next() {
		var date, code string
		var score float64
		var createdAt sql.NullString
		if err := rows.Scan(&date, &code, &score, &createdAt); err != nil {
			return nil, err
		}
		// d1_scores.date 是横线形态（与 daily.trade_date 的紧凑不同），这里统一成紧凑再返回，
		// 让装配层只面对一种日尺；形状认不出就整行丢弃（不是当 0 塞进去）。
		day := cntime.CompactDay(date)
		if day == "" {
			continue
		}
		bucket, ok := out[day]
		if !ok {
			bucket = make(map[string]float64)
			out[day] = bucket
		}
		bucket[code] = score // ORDER BY created_at ASC ⇒ 后写的总是最新一次判定
	}
	return out, rows.Err()
}

// LeaderRanksForCode 取某票区间内的龙头代理分位，key＝紧凑日期。
// 缺日就是没有键（调用方按"该日 NaN 弃权"处理），绝不返回 0——
// 0 在分位语义里是"同板块最弱"，是一条方向相反的真读数。
func (d *DB) LeaderRanksForCode(code, start, end string) (map[string]float64, error) {
	s, e := cntime.CompactDay(start), cntime.CompactDay(end)
	if s == "" || e == "" {
		return nil, fmt.Errorf("store: LeaderRanksForCode 日尺不可认 %q..%q", start, end)
	}
	rows, err := d.db.Query(`SELECT trade_date, gain_rank FROM leader_rank_daily
		WHERE ts_code=? AND trade_date BETWEEN ? AND ?`, code, s, e)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]float64)
	for rows.Next() {
		var day string
		var v sql.NullFloat64
		if err := rows.Scan(&day, &v); err != nil {
			return nil, err
		}
		if v.Valid {
			out[day] = v.Float64
		}
	}
	return out, rows.Err()
}

// FloatSharesForCodeRange 取某票区间内的流通股本，key＝紧凑日期（逐股一次查，
// 与 StockFlowSeries 同形状；主键 (ts_code, trade_date) 正好走前缀）。
func (d *DB) FloatSharesForCodeRange(code, start, end string) (map[string]float64, error) {
	s, e := cntime.CompactDay(start), cntime.CompactDay(end)
	if s == "" || e == "" {
		return nil, fmt.Errorf("store: FloatSharesForCodeRange 日尺不可认 %q..%q", start, end)
	}
	rows, err := d.db.Query(`SELECT trade_date, float_share FROM stock_float_share_daily
		WHERE ts_code=? AND trade_date BETWEEN ? AND ?`, code, s, e)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]float64)
	for rows.Next() {
		var day string
		var v sql.NullFloat64
		if err := rows.Scan(&day, &v); err != nil {
			return nil, err
		}
		if v.Valid && v.Float64 > 0 {
			out[day] = v.Float64
		}
	}
	return out, rows.Err()
}

// SectorMapDaysBetween 库里有哪些归属快照日（升序、紧凑）。
// 只回日期清单，装配层据此决定"as-of 到哪一份"——把选择权留在读侧，
// 生产者的职责只是每天存一份。
func (d *DB) SectorMapDaysBetween(start, end string) ([]string, error) {
	s, e := cntime.CompactDay(start), cntime.CompactDay(end)
	if s == "" || e == "" {
		return nil, fmt.Errorf("store: SectorMapDaysBetween 日尺不可认 %q..%q", start, end)
	}
	rows, err := d.db.Query(`SELECT DISTINCT trade_date FROM sector_map_daily
		WHERE trade_date BETWEEN ? AND ? ORDER BY trade_date ASC`, s, e)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var day string
		if err := rows.Scan(&day); err != nil {
			return nil, err
		}
		out = append(out, day)
	}
	return out, rows.Err()
}

// SectorMapRowsOn 取某一日快照的全部 (ts_code → 板块名)。
// 供"当日截面"类生产者用（如龙头分位预计算）；逐票装配请走 SectorMapForCodeRange（走索引）。
func (d *DB) SectorMapRowsOn(day string) (map[string]string, error) {
	compact := cntime.CompactDay(day)
	if compact == "" {
		return nil, fmt.Errorf("store: SectorMapRowsOn 日尺不可认 %q", day)
	}
	rows, err := d.db.Query(`SELECT ts_code, sector FROM sector_map_daily WHERE trade_date=?`, compact)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]string)
	for rows.Next() {
		var c, s string
		if err := rows.Scan(&c, &s); err != nil {
			return nil, err
		}
		if strings.TrimSpace(s) != "" {
			out[c] = s
		}
	}
	return out, rows.Err()
}

// DailyPctChgOn 取某交易日全市场的涨跌幅（%），供"当日横截面"类预计算用（如龙头代理分位）。
//
// 只取有 pct_chg 的行；NULL/0 分不清"平盘"与"没数"，而 baostock 腿的降级行是靠 source 列标的
// （§P2-F），所以这里连带把 source 带出来，由调用方决定是否排除降级行——
// 在 SQL 里替调用方做这个决定，等于把降级口径藏进存储层。
func (d *DB) DailyPctChgOn(day string) (map[string][2]float64, error) {
	compact := cntime.CompactDay(day)
	if compact == "" {
		return nil, fmt.Errorf("store: DailyPctChgOn 日尺不可认 %q", day)
	}
	rows, err := d.db.Query(`SELECT ts_code, pct_chg, COALESCE(source,'') FROM daily
		WHERE trade_date=? AND pct_chg IS NOT NULL`, compact)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string][2]float64)
	for rows.Next() {
		var code, src string
		var pct sql.NullFloat64
		if err := rows.Scan(&code, &pct, &src); err != nil {
			return nil, err
		}
		if !pct.Valid {
			continue
		}
		// 第二列存降级标记（1=降级行），调用方按需排除；降级行没有可信涨跌幅时上游已写 NULL，
		// 走不到这里，但来源标记仍然要能读出，否则"平盘"与"来源不可信"在预计算里无法区分。
		flag := 0.0
		if src != "" && src != "baostock" && src != "tushare" {
			flag = 1.0
		}
		out[code] = [2]float64{pct.Float64, flag}
	}
	return out, rows.Err()
}

// SectorMapOnDay 取某一日的逐票板块归属（横截面预计算用；行式查询走 trade_date 前缀索引）。
func (d *DB) SectorMapOnDay(day string) (map[string]string, error) {
	return d.SectorMapRowsOn(day)
}

// 为什么逐票查而不是把全表读进内存：一年快照 ≈ 250 日 × 5662 只 ≈ 140 万条，
// 在 2 核 4G、researchd 只有 500M 配额（MemoryHigh）的机器上，
// 让回放进程为了一张连接键表吃 200 MB 是把别的腿挤死。
// 主键前缀 (ts_code, trade_date) 已建索引（idx_sectormap_code），单票一次查询只回几十~几百行。
func (d *DB) SectorMapForCodeRange(code, start, end string) (map[string]string, error) {
	s, e := cntime.CompactDay(start), cntime.CompactDay(end)
	if s == "" || e == "" {
		return nil, fmt.Errorf("store: SectorMapForCodeRange 日尺不可认 %q..%q", start, end)
	}
	rows, err := d.db.Query(`SELECT trade_date, sector FROM sector_map_daily
		WHERE ts_code=? AND trade_date BETWEEN ? AND ?`, code, s, e)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]string)
	for rows.Next() {
		var day, sector string
		if err := rows.Scan(&day, &sector); err != nil {
			return nil, err
		}
		if strings.TrimSpace(sector) != "" {
			out[day] = sector
		}
	}
	return out, rows.Err()
}

// SectorFlowRange 读区间内逐 (日, 板块) 的净流入与成交额，供装配层算**横截面分位**。
//
// 为什么不预先建一张"板块分位表"：sector_flow_daily 现网只有百余个交易日 × ~496 板块
// ≈ 6 万行，分位是"当日全板块内的位次"，读侧一次算完比多存一张表更不容易漂
// （多存一张表就多一个写点，写点与算点分家时两边都可能各自漂移）。
// trade_date 在这一张表里是**横线**形态（与 daily 不同），返回时统一成紧凑日尺。
func (d *DB) SectorFlowRange(start, end string) (map[string]map[string][2]float64, error) {
	s, e := cntime.CompactDay(start), cntime.CompactDay(end)
	if s == "" || e == "" {
		return nil, fmt.Errorf("store: SectorFlowRange 日尺不可认 %q..%q", start, end)
	}
	// 库里是 YYYY-MM-DD， BETWEEN 用紧凑会一路查空且不报错（§FLOW 烘参那次的同型坑），
	// 所以这里把边界转回横线，读出来再归一成紧凑——转换只在这一个函数里发生。
	rows, err := d.db.Query(`SELECT trade_date, sector, net_inflow, amount FROM sector_flow_daily
		WHERE trade_date BETWEEN ? AND ?`, cntime.DayOf(mustDay(s)), cntime.DayOf(mustDay(e)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]map[string][2]float64)
	for rows.Next() {
		var dayRaw, sector string
		var net, amount sql.NullFloat64
		if err := rows.Scan(&dayRaw, &sector, &net, &amount); err != nil {
			return nil, err
		}
		day := cntime.CompactDay(dayRaw)
		if day == "" {
			continue
		}
		bucket, ok := out[day]
		if !ok {
			bucket = make(map[string][2]float64)
			out[day] = bucket
		}
		bucket[sector] = [2]float64{net.Float64, amount.Float64}
	}
	return out, rows.Err()
}

// mustDay 紧凑日期 → time.Time（解析不动退回零值，由调用方的形状校验兜住）。
func mustDay(compact string) time.Time {
	t, err := time.ParseInLocation("20060102", compact, cntime.Loc)
	if err != nil {
		return time.Time{}
	}
	return t
}

// CountCollectableCodes 库里可采资金流的票数（排除北交所——上游无该档，实测 rc=100）。
// 覆盖率读数的分母必须与采集器的目标集**同一条判据**，否则覆盖率凭空虚高或虚低。
func (d *DB) CountCollectableCodes() (int, error) {
	var n int
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM stocks WHERE ts_code NOT LIKE '%.BJ'`).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}
