// carrier_shadow.go — §CARRIER-SHADOW（2026-11-05）载体影子决策的落库与按日读取。
//
// 这一层为什么必须存在（docs/IMPL_QMT_INTERNAL_LOOP_20261103.md：每枚移植的 P1 规则必须先与
// Go 引擎对照跑满 ≥5 个交易日才准碰钱）：载体的 shadow 命中以前**只有执行器本地一行日志**——
// 不落盘、不上行、广州侧没有任何读数能回答"载体那套判断与引擎离得多近"，于是 5 日窗口
// **根本无法计量**，owner 那枚"武装"按钮背后是零数据。本文件是这条测量腿的账本半边：
//
//	user_id   归属账号——**由上行那把钥匙反查得出**，回报体里自报的 user_id 一律不信
//	          （§安全 T1 / §TOKEN-PERAACCT 同口径，与 positions/account 事件一字不差）
//	day       北京时间交易日 YYYYMMDD，进主键：跨日天然不复活、不累计（§DECISION-TODAY 同取向）
//	short_code 载体自己的幂等键（<规则码><代码头>，与委托备注那把短码同源）——
//	          上行腿重启后重发同一份账本，落库还是同一行，5 日窗口的分子不会被灌水
//	market_scale / target_scale / amount 可空：NULL＝载体这一轮没算出来（判断腿没接线），
//	          写 0 会被对照表读成"定权给了零仓位"，那是把缺数据当成事实（missing≠empty）
//
// 三个查询面都是**只读观测**：只回答"有几条、引擎同意的有几条、5 日凑齐了几天"，
// 不参与下单、不参与健康线、不参与熔断（§OBS-GUARD 取向——观测腿不许有停机权限，
// 也不许有钱的动作）。保留期由调用方按天剪一次，与 signal_decisions 同一姿势。
//
// English: storage for the carrier's SHADOW decisions (day-keyed, per account, deduped on the
// carrier's own short code). Read-only surfaces only; nullable numerics stay NULL instead of 0.
package store

import (
	"fmt"
	"regexp"
	"strings"
)

// CarrierShadowDecision 一条载体影子决策（与 carrier_shadow_decisions 表一一对应）。
// JSON tag 集就是回报契约的"影子行"字段面：golden 在
// qmt_gateway/contract/report_fields.json 的 shadow_row_fields，反射锁在
// internal/server/report_contract_test.go（加/删字段漏改 golden 即红）。
// English: one carrier shadow decision; the JSON tag set IS the report contract for shadow rows.
type CarrierShadowDecision struct {
	UserID    string  `json:"user_id"` // 归属账号（钥匙反查，绝不信自报）
	Day       string  `json:"day"`     // YYYYMMDD（北京时间交易日，进主键）
	ShortCode string  `json:"short_code"`
	TsCode    string  `json:"ts_code"` // 600000.SH 形态（上行侧 normalize_code 那一处定义）
	Kind      string  `json:"kind"`    // 规则种类（dragon / factor_composite / ...）
	Side      string  `json:"side"`    // 载体口径的方向（buy/sell），对照时再映射到委托那两词
	Qty       int     `json:"qty"`
	Price     float64 `json:"price"`
	// Amount 意图金额（卖出信号常为空——与 make_signal 那边 amount=None 同源，不拿 0 冒充）。
	Amount *float64 `json:"amount"`
	// MarketScale/TargetScale 判断腿（rules_sizing）给出的两层系数；nil＝这一版载体没接线。
	MarketScale *float64 `json:"market_scale"`
	TargetScale *float64 `json:"target_scale"`
	SizingNote  string   `json:"sizing_note"` // ASCII 键值串（hard_block=... / capped_by=...）
	DecidedAt   string   `json:"decided_at"`  // 载体判定时刻（RFC3339 北京串）
	SeenAt      string   `json:"seen_at"`     // 广州收到这条上行的时刻（排查"谁先谁后"）
}

// carrierShadowDayPat 交易日形态：8 位数字（YYYYMMDD）。
// 为什么必须在写面判而不是读面：day 进主键，一行坏形态（"2026-11-05"）会自成一天，
// 5 日窗口就多一个永不存在的新交易日——那是把垃圾当成一天数据（本仓的 day-keyed 老坑）。
var carrierShadowDayPat = regexp.MustCompile(`^\d{8}$`)

// carrierShadowMaxNote 留痕列的字节帽：外部输入的一行文本不许把账本撑爆（§PAYLOAD-BOUND 同取向）。
const carrierShadowMaxNote = 200

// UpsertCarrierShadowRows 逐行写入（主键冲突＝同一意图重报，刷新最新判定、保留首次时刻）。
//
// 返回 (written, rejected)：
//
//	· written 真落库/被刷新的行数——这是"上行腿这次报了几条"的读数；
//	· rejected 是被**逐行拒收**的原因（坏 day / 缺 short_code / 缺 kind / 空 ts_code）。
//
// 为什么逐行拒收而不是整批 400：一行坏形态不该让这一天所有影子决策一起丢
// （§MANUAL-SYNC 的"部分失败不标 done"同取向——但也不许把坏行当好行写进去）。
// 空 uid 直接返回错误：那是"不知道这行是谁的"，写进去就是跨账号混淆账
// （§FIX-20261028-F01 读面纪律在写面的镜像）。
func (d *DB) UpsertCarrierShadowRows(userID, seenAt string, rows []CarrierShadowDecision) (int, []string, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return 0, nil, fmt.Errorf("carrier shadow: empty user id")
	}
	written := 0
	var rejected []string
	for i, r := range rows {
		reason := carrierShadowRowError(r)
		if reason != "" {
			rejected = append(rejected, fmt.Sprintf("#%d:%s", i, reason))
			continue
		}
		_, err := d.db.Exec(`INSERT INTO carrier_shadow_decisions
			(user_id, day, short_code, ts_code, kind, side, qty, price, amount,
			 market_scale, target_scale, sizing_note, decided_at, seen_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT(user_id, day, short_code) DO UPDATE SET
			 ts_code=excluded.ts_code, kind=excluded.kind, side=excluded.side,
			 qty=excluded.qty, price=excluded.price, amount=excluded.amount,
			 market_scale=excluded.market_scale, target_scale=excluded.target_scale,
			 sizing_note=excluded.sizing_note, decided_at=excluded.decided_at,
			 seen_at=excluded.seen_at`,
			userID, r.Day, r.ShortCode, r.TsCode, r.Kind, r.Side, r.Qty, r.Price,
			r.Amount, r.MarketScale, r.TargetScale,
			truncateCarrierText(r.SizingNote, carrierShadowMaxNote), r.DecidedAt, seenAt)
		if err != nil {
			return written, rejected, fmt.Errorf("store carrier shadow upsert %s/%s: %w", userID, r.Day, err)
		}
		written++
	}
	return written, rejected, nil
}

// carrierShadowRowError 单行的准入判据；返回空串＝可写。
// 四条必判（缺任一条，那行在对照表里就是不可解释的账）：
//
//	· day 必须 8 位数字（进主键，坏形态自成"新的一天"）；
//	· short_code 必须非空（它是幂等键，空键会让整批重报互相覆盖）；
//	· kind 必须非空（对照按种类分账，没种类的行进不了任何一格）；
//	· ts_code 必须是 6 位码+交易所后缀（与 positions 那侧同一形态要求：猜错后缀会把
//	  沪市的票落到深市账上，§BUG-INDEX 同族）。
func carrierShadowRowError(r CarrierShadowDecision) string {
	if !carrierShadowDayPat.MatchString(r.Day) {
		return "bad_day"
	}
	if strings.TrimSpace(r.ShortCode) == "" {
		return "empty_short_code"
	}
	if strings.TrimSpace(r.Kind) == "" {
		return "empty_kind"
	}
	if !carrierShadowCodePat.MatchString(r.TsCode) {
		return "bad_ts_code"
	}
	return ""
}

// carrierShadowCodePat 与 store 侧其它 ts_code 校验同形态（6 位数字 . SH/SZ/BJ，大小写都收）。
var carrierShadowCodePat = regexp.MustCompile(`^\d{6}\.(?:SH|SZ|BJ)$`)

// CarrierShadowRowsForDay 某账号某交易日的全部影子决策（按种类、短码排序，供对照逐行归因）。
// 无行返回**空切片**——"这一天没有"由调用方结合 CarrierShadowDayCounts 判（这一天有没有上报过），
// 本函数自己不猜（missing≠empty 在这一层的落形：读面只给事实，不给结论）。
func (d *DB) CarrierShadowRowsForDay(userID, day string) ([]CarrierShadowDecision, error) {
	if !carrierShadowDayPat.MatchString(day) {
		return nil, fmt.Errorf("store carrier shadow: bad day %q", day)
	}
	rows, err := d.db.Query(`SELECT user_id, day, short_code, ts_code, kind, side, qty, price,
		amount, market_scale, target_scale, sizing_note, decided_at, seen_at
		FROM carrier_shadow_decisions WHERE user_id = ? AND day = ?
		ORDER BY kind ASC, short_code ASC`, userID, day)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]CarrierShadowDecision, 0, 16)
	for rows.Next() {
		var r CarrierShadowDecision
		if err := rows.Scan(&r.UserID, &r.Day, &r.ShortCode, &r.TsCode, &r.Kind, &r.Side,
			&r.Qty, &r.Price, &r.Amount, &r.MarketScale, &r.TargetScale,
			&r.SizingNote, &r.DecidedAt, &r.SeenAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CarrierShadowDayCounts 区间内**有上报**的日子各有几行（含零行的日子不在 map 里）。
// 这一张账是"三种状态"的第一半：某天在 map 里没有＝那天盒子一个字节都没报（读数未知），
// 与"报了但这一类没命中"（有行、但某 kind 计数为 0）是两件事，折成一件就把静默说成没信号。
// 为什么用 BETWEEN 而不是逐日查：窗口最多十来天，一次拿全比 N 次查询少一个"中途库变了"的裂缝。
func (d *DB) CarrierShadowDayCounts(userID, fromDay, toDay string) (map[string]int, error) {
	if !carrierShadowDayPat.MatchString(fromDay) || !carrierShadowDayPat.MatchString(toDay) {
		return nil, fmt.Errorf("store carrier shadow: bad window %q..%q", fromDay, toDay)
	}
	rows, err := d.db.Query(`SELECT day, COUNT(*) FROM carrier_shadow_decisions
		WHERE user_id = ? AND day >= ? AND day <= ? GROUP BY day`, userID, fromDay, toDay)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var day string
		var n int
		if err := rows.Scan(&day, &n); err != nil {
			return nil, err
		}
		out[day] = n
	}
	return out, rows.Err()
}

// CarrierShadowKindDayCounts 区间内 (kind, day) 各命中几行——对照表的分母来源。
// 与 DayCounts 分两张账是刻意的：某类一天 0 行**不会出现在这张 map 里**，
// 调用方必须能区分"这一天没上报"（DayCounts 缺）与"这一天上报了但这类没命中"（DayCounts 有、这里缺）。
func (d *DB) CarrierShadowKindDayCounts(userID, fromDay, toDay string) (map[string]map[string]int, error) {
	if !carrierShadowDayPat.MatchString(fromDay) || !carrierShadowDayPat.MatchString(toDay) {
		return nil, fmt.Errorf("store carrier shadow: bad window %q..%q", fromDay, toDay)
	}
	rows, err := d.db.Query(`SELECT kind, day, COUNT(*) FROM carrier_shadow_decisions
		WHERE user_id = ? AND day >= ? AND day <= ? GROUP BY kind, day`, userID, fromDay, toDay)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]int{}
	for rows.Next() {
		var kind, day string
		var n int
		if err := rows.Scan(&kind, &day, &n); err != nil {
			return nil, err
		}
		if out[kind] == nil {
			out[kind] = map[string]int{}
		}
		out[kind][day] = n
	}
	return out, rows.Err()
}

// PruneCarrierShadow 删除早于 beforeDay 的影子行（只做保留期收缩，不参与任何判定）。
// 空串直接返回 0：那等价于"全删"，而全删是把 5 日窗口的证据自己抹掉。
func (d *DB) PruneCarrierShadow(beforeDay string) (int64, error) {
	if strings.TrimSpace(beforeDay) == "" {
		return 0, nil
	}
	res, err := d.db.Exec(`DELETE FROM carrier_shadow_decisions WHERE day < ?`, beforeDay)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteCarrierShadowForUser 删号时清该账号的私有观测行（orders/fills 一律不碰，§F18 证据链）。
func (d *DB) DeleteCarrierShadowForUser(userID string) (int64, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return 0, fmt.Errorf("refuse to delete carrier shadow for empty user id")
	}
	res, err := d.db.Exec(`DELETE FROM carrier_shadow_decisions WHERE user_id = ?`, userID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// truncateCarrierText 外部文本入库前的长度收口（超长按字节截，不返回错误——
// 留痕列被截不该让一整批决策丢掉，那正是"证据比格式重要"的那条取舍）。
func truncateCarrierText(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max]
}
