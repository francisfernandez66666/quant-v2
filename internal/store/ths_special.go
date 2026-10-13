// ths_special.go 同花顺（新）盘口特色数据表：涨停/跌停/炸板三池、连板天梯、个股异动
// （§HITHINK_DATA_SOURCE_PLAN D3 数据层）。全部按 (trade_date, ts_code[, board/tag]) 幂等 upsert。
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// thsLimitUpInsertSQL 涨停池幂等写入语句（列序唯一定义处：upsert 与整日替换两条路径共用，
// 两处各抄一份就是下一个"改了一处漏一处"的坑）。
const thsLimitUpInsertSQL = `INSERT OR REPLACE INTO ths_limit_up_daily
	(trade_date, ts_code, name, is_st, is_new, price, pct_chg, first_seal_time,
	 continue_cnt, continue_text, limit_reason, seal_money, max_seal_money)
	VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`

// bindThsLimitUp 按 thsLimitUpInsertSQL 的列序写入一行，返回受影响行数。
// 抽出成函数的唯一理由就是"列序只有一处"——两条写入口径必须同形。
func bindThsLimitUp(stmt *sql.Stmt, r ThsLimitUpRow) (int64, error) {
	res, err := stmt.Exec(r.TradeDate, r.TsCode, r.Name, b2i(r.IsST), b2i(r.IsNew),
		r.Price, r.PctChg, r.FirstSealTime, r.ContinueCnt, r.ContinueText,
		r.LimitReason, r.SealMoney, r.MaxSealMoney)
	if err != nil {
		return 0, err
	}
	aff, _ := res.RowsAffected()
	return aff, nil
}

// ErrPoolSnapshotEmpty 空回包禁止整日替换：那会把"上游这次没给数据"变成"当日数据消失"。
// 同 §降级链会兜死分支的口径——失败不许伪装成产物为空。
// English: an empty pool payload must never trigger a whole-day replace (absence of data
// is not evidence of an empty trading day).
var ErrPoolSnapshotEmpty = errors.New("盘口整日替换被拒：回包为空（先保数据，缺数按缺数判）")

// ErrPoolSnapshotMultiDate 一次整日替换只允许一个交易日：删的是整日，写的若跨多日＝删与写不同天。
var ErrPoolSnapshotMultiDate = errors.New("盘口整日替换被拒：一批行跨多个交易日")

// ErrPoolSnapshotNoDate 行集缺 trade_date（无法确定要替换哪一天，宁可不删）。
var ErrPoolSnapshotNoDate = errors.New("盘口整日替换被拒：行集缺 trade_date")

// PoolReplaceResult 整日替换的两侧读数：只报"写了多少"就分不清"池子缩了"与"根本没动"。
type PoolReplaceResult struct {
	TradeDate string `json:"trade_date"` // 被替换的交易日
	Deleted   int64  `json:"deleted"`    // 删除的旧行数（真值为 0 才算"首次落库"）
	Written   int64  `json:"written"`    // 本次写入的行数
}

// ReplaceThsLimitUps 涨停池整日快照替换：单事务内先删该交易日全部行，再写入本次行集。
//
// 为什么必须"先删后写"而不是幂等 upsert：主键是 (trade_date, ts_code)，INSERT OR REPLACE 只会
// 覆盖本次回包里出现的股票，**上一批在池、这一批不在池的那些股会一直留在同一天**。于是池子只增
// 不减，涨停家数/最高连板/炸板率全部朝一个方向漂，且第二次同步"看起来成功"（rc=0、写入行数>0）。
// 这与 §0926 的"幂等 ≠ 快照"同族：幂等解决重跑，解决不了集合收缩。
//
// 拦停顺序是刻意的：空回包/跨日/缺日三型都在 DELETE 之前返回错误，库一个字节都不动。
// English: whole-day snapshot replace (delete then insert in one tx); empty or multi-date
// batches abort before any delete so a failed fetch can never erase the day.
func (d *DB) ReplaceThsLimitUps(rows []ThsLimitUpRow) (PoolReplaceResult, error) {
	res := PoolReplaceResult{}
	if len(rows) == 0 {
		return res, ErrPoolSnapshotEmpty
	}
	date := rows[0].TradeDate
	if date == "" {
		return res, ErrPoolSnapshotNoDate
	}
	for _, r := range rows {
		if r.TradeDate != date {
			return res, ErrPoolSnapshotMultiDate
		}
	}
	res.TradeDate = date
	tx, err := d.db.Begin()
	if err != nil {
		return res, err
	}
	defer func() { _ = tx.Rollback() }()
	del, err := tx.Exec(`DELETE FROM ths_limit_up_daily WHERE trade_date=?`, date)
	if err != nil {
		return res, fmt.Errorf("涨停池整日删除失败(%s): %w", date, err)
	}
	res.Deleted, _ = del.RowsAffected()
	stmt, err := tx.Prepare(thsLimitUpInsertSQL)
	if err != nil {
		return res, err
	}
	defer stmt.Close()
	for _, r := range rows {
		aff, err := bindThsLimitUp(stmt, r)
		if err != nil {
			return res, fmt.Errorf("涨停池写入失败(%s %s): %w", date, r.TsCode, err)
		}
		res.Written += aff
	}
	return res, tx.Commit()
}

// LimitUpLatestDateBefore 涨停池里严格早于 date 的最近有数据日（无则 ""）。
// §POOL-STAMP 幻影日判据用它取"上一天的池子"逐行比对：上游把缺失日兜底成最近一个有数据的交易日，
// 落库侧只看请求日就会把那一包写成从没发生过的日期。
// English: the newest pool date strictly before `date` — the shadow source for the phantom-day guard.
func (d *DB) LimitUpLatestDateBefore(date string) (string, error) {
	var out string
	err := d.db.QueryRow(`SELECT COALESCE(MAX(trade_date),'') FROM ths_limit_up_daily WHERE trade_date<?`, date).Scan(&out)
	return out, err
}

// PoolTextNorm 池内自由文本的**比较用**归一形（去全部空白，上游的 "粤 传 媒" 与库里的 "粤传媒" 同形）。
//
// 只用于影子日比对，绝不用于落库——库里必须保持上游字节原样，否则"归一"会变成第二次写入的口径差。
// English: whitespace-stripped form used ONLY for comparison; the DB keeps upstream bytes verbatim.
func PoolTextNorm(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\u3000' || r == '\u00a0' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// LimitUpRowSig 涨停池一行的比较签名：代码 + 六个可判"同一天"的数值/文本字段。
//
// 为什么这些字段：价格、涨跌幅、首封时间、连板数、峰值封单额、名称。真换一个交易日，六个字段
// 全体相等的概率在 A 股几乎为零（0926 现网实测那 72 行是逐字节相同＝同一包数据）。
// 封单额 seal_money 故意不进签名：它是盘中实时值，同一日两次抓取都可能不同，放进签名只会漏判。
// English: per-row comparison signature; seal_money is excluded because it is an intraday value.
func LimitUpRowSig(r ThsLimitUpRow) string {
	return strings.Join([]string{
		strings.TrimSpace(r.TsCode),
		PoolTextNorm(r.Name),
		strconv.FormatFloat(r.Price, 'f', -1, 64),
		strconv.FormatFloat(r.PctChg, 'f', -1, 64),
		strings.TrimSpace(r.FirstSealTime),
		strconv.Itoa(r.ContinueCnt),
		strconv.FormatFloat(r.MaxSealMoney, 'f', -1, 64),
	}, "|")
}

// limitUpSigSet 一批行的有序签名集合（比较用；排序后与库里同日集合可直接等值比对）。
func limitUpSigSet(rows []ThsLimitUpRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, LimitUpRowSig(r))
	}
	sort.Strings(out)
	return out
}

// LimitUpShadowOf 判断"这一批涨停池回包"是否与库里 date 之前最近一日的行集逐签名相同（影子日／幻影日）。
//
// 返回 (影子日, 是否相同, 错误)：影子日为 "" 时说明此前无数据可比，identical 恒 false——
// 这不是"安全"，是"无从判定"，调用方要把这层意思留在读数里，不能当成通过。
// English: compares a candidate batch's row signatures against the nearest earlier stored day;
// identical means the payload is that earlier day's snapshot mislabeled as `date`.
func (d *DB) LimitUpShadowOf(date string, rows []ThsLimitUpRow) (string, bool, error) {
	if len(rows) == 0 {
		return "", false, nil
	}
	shadow, err := d.LimitUpLatestDateBefore(date)
	if err != nil {
		return "", false, err
	}
	if shadow == "" {
		return "", false, nil
	}
	stored, err := d.LimitUpsOnDate(shadow)
	if err != nil {
		return shadow, false, err
	}
	a, b := limitUpSigSet(rows), limitUpSigSet(stored)
	if len(a) != len(b) {
		return shadow, false, nil
	}
	for i := range a {
		if a[i] != b[i] {
			return shadow, false, nil
		}
	}
	return shadow, true, nil
}

// ThsLimitUpRow 涨停池行。
type ThsLimitUpRow struct {
	TradeDate     string  // yyyyMMdd（交易日）
	TsCode        string  // TS代码
	Name          string  // 名称
	IsST          bool    // 是否ST
	IsNew         bool    // 未开板新股
	Price         float64 // 价格
	PctChg        float64 // 涨跌幅（已×100）
	FirstSealTime string  // 首次封板 HH:MM
	ContinueCnt   int     // 连板数
	ContinueText  string  // "5天4板"
	LimitReason   string  // 涨停原因（可空）
	SealMoney     float64 // 当前封单额
	MaxSealMoney  float64 // 峰值封单额
}

// UpsertThsLimitUps 批量幂等写入涨停池（只增改、不删）。
// §POOL-SNAPSHOT（2026-10-12）：盘后同步链已改用 ReplaceThsLimitUps（整日快照替换）；
// 本函数保留给"逐条补写不动其它行"的消费面（研究侧回填/测试夹具），尺寸不删。
func (d *DB) UpsertThsLimitUps(rows []ThsLimitUpRow) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	tx, err := d.db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.Prepare(thsLimitUpInsertSQL)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	var n int64
	for _, r := range rows {
		aff, err := bindThsLimitUp(stmt, r)
		if err != nil {
			return n, err
		}
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

// ── §ENH-A 单票涨停微结构查询（因子面板装配用）──

// LuEventRow 涨停池个股事件行（面板装配消费的最小字段集）。
type LuEventRow struct {
	TradeDate     string  // 交易日 yyyyMMdd
	ContinueCnt   int     // 连板数
	FirstSealTime string  // 首封时间 HH:MM
	MaxSealMoney  float64 // 峰值封单额（元）
}

// LimitUpsForCode 某标的区间内的涨停池事件行（升序）。索引 (ts_code, trade_date) 支持
// 面板逐股装配 O(事件数) 读取（生产 5000 股×逐窗调用，不能全表扫）。
// English: per-code limit-up events over a range, ascending — index-backed for panel assembly.
func (d *DB) LimitUpsForCode(code, from, to string) ([]LuEventRow, error) {
	rows, err := d.db.Query(`SELECT trade_date, continue_cnt, first_seal_time, max_seal_money
		FROM ths_limit_up_daily WHERE ts_code=? AND trade_date BETWEEN ? AND ? ORDER BY trade_date`,
		code, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LuEventRow
	for rows.Next() {
		var r LuEventRow
		var fts string
		if err := rows.Scan(&r.TradeDate, &r.ContinueCnt, &fts, &r.MaxSealMoney); err != nil {
			return nil, err
		}
		r.FirstSealTime = fts
		out = append(out, r)
	}
	return out, rows.Err()
}

// BreakCntForCode 某标的区间内逐日开板（炸板）次数：date → open_times。
// English: per-code daily blast (re-open) counts over a range.
func (d *DB) BreakCntForCode(code, from, to string) (map[string]int, error) {
	rows, err := d.db.Query(`SELECT trade_date, open_times FROM ths_break_pool_daily
		WHERE ts_code=? AND trade_date BETWEEN ? AND ?`, code, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var dt string
		var n int
		if err := rows.Scan(&dt, &n); err == nil {
			out[dt] = n
		}
	}
	return out, rows.Err()
}

// ── 跌停池 / 炸板池（结构对称，字段较少）──

// ThsBreakRow 炸板池单行（开板次数 = 炸板证据）。
type ThsBreakRow struct {
	TradeDate string  // yyyyMMdd
	TsCode    string  // TS代码
	Name      string  // 名称
	Price     float64 // 价格
	PctChg    float64 // 涨跌幅（%）
	OpenTimes int     // 开板次数
	Turnover  float64 // 成交额（元）
}

// BreakPoolOnDate 某交易日炸板池（情绪因子/炸板率消费）。
func (d *DB) BreakPoolOnDate(date string) ([]ThsBreakRow, error) {
	rows, err := d.db.Query(`SELECT trade_date, ts_code, name, price, pct_chg, open_times, turnover
		FROM ths_break_pool_daily WHERE trade_date=?`, date)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ThsBreakRow
	for rows.Next() {
		var r ThsBreakRow
		if err := rows.Scan(&r.TradeDate, &r.TsCode, &r.Name, &r.Price, &r.PctChg,
			&r.OpenTimes, &r.Turnover); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// EmotionStat 单日市场情绪统计（涨停池 + 炸板池合成）。
type EmotionStat struct {
	Date      string  // yyyyMMdd
	LimitUp   int     // 涨停家数
	BreakCnt  int     // 炸板家数
	MaxBoard  int     // 最高连板高度
	BlastRate float64 // 炸板率（%）：炸板/(涨停+炸板)*100；分母为 0 时 = 0
}

// EmotionStatsRange 区间内逐日市场情绪统计（按日期升序）。
// English: per-day market-sentiment stats over [from,to], ascending by date.
func (d *DB) EmotionStatsRange(from, to string) ([]EmotionStat, error) {
	var out []EmotionStat
	dates, err := d.ThsDatesBetween(from, to)
	if err != nil {
		return nil, err
	}
	// 涨停池按日计数 + 最高连板
	q1 := `SELECT trade_date, COUNT(*), MAX(continue_cnt) FROM ths_limit_up_daily
		WHERE trade_date BETWEEN ? AND ? GROUP BY trade_date`
	rows, err := d.db.Query(q1, from, to)
	if err != nil {
		return nil, err
	}
	limitByDate := make(map[string][2]int, 64)
	for rows.Next() {
		var dt string
		var cnt, maxBoard int
		if err := rows.Scan(&dt, &cnt, &maxBoard); err != nil {
			rows.Close()
			return nil, err
		}
		limitByDate[dt] = [2]int{cnt, maxBoard}
	}
	rows.Close()
	// 炸板池按日计数
	q2 := `SELECT trade_date, COUNT(*) FROM ths_break_pool_daily
		WHERE trade_date BETWEEN ? AND ? GROUP BY trade_date`
	rows, err = d.db.Query(q2, from, to)
	if err != nil {
		return nil, err
	}
	breakByDate := make(map[string]int, 64)
	for rows.Next() {
		var dt string
		var cnt int
		if err := rows.Scan(&dt, &cnt); err != nil {
			rows.Close()
			return nil, err
		}
		breakByDate[dt] = cnt
	}
	rows.Close()
	for _, dt := range dates {
		lu := limitByDate[dt]
		bc := breakByDate[dt]
		total := lu[0] + bc
		rate := 0.0
		if total > 0 {
			rate = float64(bc) / float64(total) * 100
		}
		out = append(out, EmotionStat{
			Date: dt, LimitUp: lu[0], MaxBoard: lu[1], BreakCnt: bc, BlastRate: rate,
		})
	}
	return out, nil
}

// ThsDatesBetween 区间内已收录行情/涨停数据的交易日（升序去重）。
// English: trading dates in [from,to] from the THS tables (ascending, deduped).
func (d *DB) ThsDatesBetween(from, to string) ([]string, error) {
	rows, err := d.db.Query(`SELECT DISTINCT trade_date FROM ths_limit_up_daily
		WHERE trade_date BETWEEN ? AND ? ORDER BY trade_date`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var dt string
		if err := rows.Scan(&dt); err == nil {
			out = append(out, dt)
		}
	}
	return out, rows.Err()
}

// thsSimplePoolTables 通用池表的拼接白名单（防注入；新增池表只在这里加一处）。
// 白名单单独成变量而不是散在各函数的 switch：upsert 与整日替换两条路径必须认同一张表清单，
// 两处各写一份时，第二处漏项会让"表名合法却被当成非法"变成静默失败（返回 sql.ErrNoRows 那种）。
var thsSimplePoolTables = map[string]bool{
	"ths_limit_down_daily": true,
	"ths_break_pool_daily": true,
}

// thsSimplePoolInsertTail 通用池写入语句的列序部分（表名由白名单校验后拼接；唯一定义处）。
const thsSimplePoolInsertTail = `(trade_date, ts_code, name, price, pct_chg, open_times, turnover_ratio_pct, turnover)
		VALUES (?,?,?,?,?,?,?,?)`

// bindThsPoolSimple 按 thsSimplePoolInsertTail 的列序写入一行通用池。
func bindThsPoolSimple(stmt *sql.Stmt, tradeDate, code string, r ThsPoolSimple) (int64, error) {
	res, err := stmt.Exec(tradeDate, code, r.Name, r.Price, r.PctChg,
		r.OpenTimes, r.TurnoverRatioPct, r.Turnover)
	if err != nil {
		return 0, err
	}
	aff, _ := res.RowsAffected()
	return aff, nil
}

// UpsertThsSimplePool 通用三池写入（跌停/炸板共用简化列集，只增改、不删）。
// table 仅允许白名单值，防拼接注入。§POOL-SNAPSHOT：盘后同步链改用 ReplaceThsSimplePool。
func (d *DB) UpsertThsSimplePool(table, tradeDate string, rows map[string]ThsPoolSimple) (int64, error) {
	if !thsSimplePoolTables[table] {
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
		` + thsSimplePoolInsertTail)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	var n int64
	for code, r := range rows {
		aff, err := bindThsPoolSimple(stmt, tradeDate, code, r)
		if err != nil {
			return n, err
		}
		n += aff
	}
	return n, tx.Commit()
}

// ReplaceThsSimplePool 跌停/炸板池整日快照替换（语义与拦停顺序同 ReplaceThsLimitUps：
// 空回包/缺日在 DELETE 之前返回错误，库不动）。
// English: whole-day snapshot replace for the limit-down / break pools; same abort-before-delete rules.
func (d *DB) ReplaceThsSimplePool(table, tradeDate string, rows map[string]ThsPoolSimple) (PoolReplaceResult, error) {
	res := PoolReplaceResult{TradeDate: tradeDate}
	if !thsSimplePoolTables[table] {
		return res, fmt.Errorf("盘口整日替换被拒：表名 %q 不在白名单", table)
	}
	if tradeDate == "" {
		return res, ErrPoolSnapshotNoDate
	}
	if len(rows) == 0 {
		return res, ErrPoolSnapshotEmpty
	}
	tx, err := d.db.Begin()
	if err != nil {
		return res, err
	}
	defer func() { _ = tx.Rollback() }()
	del, err := tx.Exec(`DELETE FROM `+table+` WHERE trade_date=?`, tradeDate)
	if err != nil {
		return res, fmt.Errorf("%s 整日删除失败(%s): %w", table, tradeDate, err)
	}
	res.Deleted, _ = del.RowsAffected()
	stmt, err := tx.Prepare(`INSERT OR REPLACE INTO ` + table + `
		` + thsSimplePoolInsertTail)
	if err != nil {
		return res, err
	}
	defer stmt.Close()
	for code, r := range rows {
		aff, err := bindThsPoolSimple(stmt, tradeDate, code, r)
		if err != nil {
			return res, fmt.Errorf("%s 写入失败(%s %s): %w", table, tradeDate, code, err)
		}
		res.Written += aff
	}
	return res, tx.Commit()
}

// ThsPoolSimple 三池简化行（跌停：open_times=0；炸板：open_times=开板次数）。
type ThsPoolSimple struct {
	Name             string  // 名称
	Price            float64 // 价格
	PctChg           float64 // 涨跌幅
	OpenTimes        int     // 开板次数
	TurnoverRatioPct float64 // 换手率
	Turnover         float64 // 成交额
}

// SimplePoolCountOnDate 白名单简化池表某交易日的行数（表名非法直接报错，不降级成 0）。
//
// §POOL-STAMP 的缺数归因要用"按日计数"而不是"表内最新日"：latest(TABLE)=MAX(trade_date) 只说
// 最后一天到哪，中间漏掉的日子看不见（10-12 那批 index_daily/日历盲窗同族的第三种形态——
// 有读数、读数没错，但读的是 MAX 而不是逐日覆盖）。
// English: per-day row count for the simple pool tables — the gap readout needs day-by-day counts,
// not MAX(trade_date), because a hole in the middle is invisible to the newest-date reading.
func (d *DB) SimplePoolCountOnDate(table, date string) (int, error) {
	if !thsSimplePoolTables[table] {
		return 0, fmt.Errorf("行数读取被拒：表名 %q 不在简化池表白名单", table)
	}
	var n int
	err := d.db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE trade_date=?`, date).Scan(&n)
	return n, err
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
	TradeDate   string // 交易日期
	BoardNum    int    // 涨停家数
	TsCode      string // TS代码
	Name        string // 名称
	SealNextDay *bool  // 次日是否封板
	SignLevel   int    // 信号级别
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

// ── 估值快照 / 财务指标（§E 因子输入扩充）──

// ThsValuationRow 估值快照行。
type ThsValuationRow struct {
	TradeDate string   // 交易日期
	TsCode    string   // TS代码
	PeTtm     *float64 // 市盈率 TTM
	PeMrq     *float64 // 市盈率 MRQ
	PbMrq     *float64 // 市净率 MRQ
	PsTtm     *float64 // 市销率 TTM
	PcfTtm    *float64 // 市现率 TTM
}

// UpsertThsValuations 批量幂等写入估值快照。
func (d *DB) UpsertThsValuations(rows []ThsValuationRow) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	tx, err := d.db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.Prepare(`INSERT OR REPLACE INTO ths_valuations_daily
		(trade_date, ts_code, pe_ttm, pe_mrq, pb_mrq, ps_ttm, pcf_ttm) VALUES (?,?,?,?,?,?,?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	var n int64
	for _, r := range rows {
		res, err := stmt.Exec(r.TradeDate, r.TsCode, r.PeTtm, r.PeMrq, r.PbMrq, r.PsTtm, r.PcfTtm)
		if err != nil {
			return n, err
		}
		aff, _ := res.RowsAffected()
		n += aff
	}
	return n, tx.Commit()
}

// ThsFinIndicatorRow 财务指标单行。
type ThsFinIndicatorRow struct {
	TsCode  string  // TS代码
	Report  string  // "2024-4"（报告期）
	Ability string  // 能力维度：growth/profitability/solvency/operation/cash-flow
	IndexID string  // 同花顺指标 ID
	Value   *string // 上游原始精度字符串；null=未披露
}

// UpsertThsFinIndicators 批量幂等写入财务指标。
func (d *DB) UpsertThsFinIndicators(rows []ThsFinIndicatorRow) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	tx, err := d.db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.Prepare(`INSERT OR REPLACE INTO ths_fin_indicators
		(ts_code, report, ability, index_id, value) VALUES (?,?,?,?,?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	var n int64
	for _, r := range rows {
		res, err := stmt.Exec(r.TsCode, r.Report, r.Ability, r.IndexID, r.Value)
		if err != nil {
			return n, err
		}
		aff, _ := res.RowsAffected()
		n += aff
	}
	return n, tx.Commit()
}
