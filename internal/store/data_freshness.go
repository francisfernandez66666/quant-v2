// 数据新鲜度自检（§D-4 GAP_VERIFY_20260917_PM）：行情库"最新交易日覆盖"断言。
// 背景：免费源改反爬/同步断供时，回测与情绪链会静默吃旧数据（历史同型缺陷：复盘断供）。
// 本文件只做查询判定，告警投递由调用方（quant 进程每日一次钩子）负责。
// §CAL-COVER-GO（2026-10-12）：判据加第二条独立腿——日历自身的覆盖度。此前它只读
// 「≤今日的最近开市日」，而写日历的生产者把窗口右边界写成了"今日"，于是末行天天停在昨天，
// 行情只要跟到同一天就恒判「滞后 0＝全部到齐」：判据和它检验的对象共用同一张失明的表。
// English: §D-4 — DB-side freshness assertion. Source outages silently degrade backtest/sentiment
// pipelines to stale data; the quant process runs this once a day and alerts on lag.
package store

import (
	"fmt"
	"strconv"
)

// DataFreshness 行情库新鲜度判定结果。
// （Freshness verdict: whether daily/ths_daily cover the most recent trading day,
// and whether the calendar itself is still being maintained.）
type DataFreshness struct {
	OK         bool   `json:"ok"`           // true=覆盖到位（或无从判定：日历/数据表为空）
	Latest     string `json:"latest"`       // 行情库最新交易日（daily ∪ ths_daily 取大）
	LastOpen   string `json:"last_open"`    // 交易日历中 ≤今日 的最近交易日
	LagDays    int    `json:"lag_days"`     // 滞后交易日数（0=最新即最近交易日；-1=算不了，见 Reason）
	TradeCalOK bool   `json:"trade_cal_ok"` // 日历表是否可用（false=无从判定，OK=true 不误报）
	// CalMax trade_cal 末行（不加 is_open、不加 ≤今日 过滤）——停更时读数直接指到"停在哪天"。
	CalMax string `json:"cal_max"`
	// CoversToday 日历末行是否已到今日。false＝日历停更，此时 LastOpen/LagDays 全部不可信：
	// 「≤今日的最近开市日」会等于日历末行本身，行情只要跟到同一天就恒判「滞后 0＝全部到齐」。
	// 这正是 §CAL-COVER 的现网事故形态（09-28~30、10-08/09 两段真实交易日整段被跳过且全闸门绿），
	// 所以覆盖检查必须独立于日龄检查存在——判据不能读它要检验的那张表的可信度。
	CoversToday bool `json:"covers_today"`
	// Reason 失败归因（""=新鲜/未判定，"lag"=行情落后，"calendar_stalled"=日历不覆盖今日）。
	// 调用方按它出不同告警文案：两者修法不同（补行情 vs 修日历回写窗口），混成一条必然误归因。
	Reason string `json:"reason"`
}

// FreshReasonLag 行情滞后判因。
const FreshReasonLag = "lag"

// FreshReasonCalendarStalled 日历停更判因（行情判据不可信的根因型）。
const FreshReasonCalendarStalled = "calendar_stalled"

// String 生成人读告警文案（调用方直接进 opslog/推送）。
// 覆盖态用 `covers_today=yes/no` 这个键与夜间验收那条只读数腿（verify_nightly_guangzhou.sh
// 的 cal_coverage 腿）同名，跨面一条 grep 能同时捞到引擎侧与验收侧的读数。
// English: human-readable alert line; the covers_today key is intentionally identical to the
// nightly acceptance leg's readout so one grep spans both sides.
func (f DataFreshness) String() string {
	if !f.TradeCalOK || f.CalMax == "" {
		return fmt.Sprintf("行情库最新 %s，最近交易日 %s，滞后 %d 个交易日", f.Latest, f.LastOpen, f.LagDays)
	}
	cov := "no"
	if f.CoversToday {
		cov = "yes"
	}
	lag := "unknown"
	if f.LagDays >= 0 {
		lag = strconv.Itoa(f.LagDays)
	}
	return fmt.Sprintf("行情库最新 %s，最近交易日 %s，滞后 %s 个交易日，日历末行 %s covers_today=%s",
		f.Latest, f.LastOpen, lag, f.CalMax, cov)
}

// CheckDataFreshness 判定行情库是否覆盖最近交易日：latest = MAX(daily.trade_date, ths_daily.trade_date)，
// lastOpen = trade_cal 中 ≤ today 的最大开市日，lag = (latest, lastOpen] 区间开市日数，
// coversToday = MAX(cal_date) ≥ today（§CAL-COVER-GO：日历停更时 lastOpen/lag 一律不可信，直接判不新鲜）。
// 口径：日历表空（全新部署/未同步）→ TradeCalOK=false，OK=true 不误报；行情两表全空视同未启动
// 采集（全新部署合法态）→ OK=true 但 Latest 为空，调用方可自行提示。滞后 >maxLag 判不新鲜；
// 日历不覆盖今日同样判不新鲜，但 Reason 与滞后分开——修法不同。
// English: compares the DB's newest trade_date against the newest open calendar day ≤ today;
// empty calendar or empty quotes are treated as "not started" (no false alarm). A calendar whose
// last row is before today is itself a fault (staleness made the lag judgement always read zero).
func (d *DB) CheckDataFreshness(today string, maxLag int) (DataFreshness, error) {
	f := DataFreshness{OK: true}
	var calRows int
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM trade_cal`).Scan(&calRows); err != nil {
		return f, err
	}
	if calRows == 0 {
		return f, nil // 无日历：无从判定（避免全新部署误报）
	}
	f.TradeCalOK = true
	// 覆盖检查按「全表末行」算，不带 is_open 也不带 ≤today——今日行哪怕写着休市也必须存在，
	// 不存在就是日历没在维护（除节假日外它本该有未来 45 天；见 cntime.CalendarLookaheadDays）。
	if err := d.db.QueryRow(`SELECT COALESCE(MAX(cal_date),'') FROM trade_cal`).Scan(&f.CalMax); err != nil {
		return f, err
	}
	f.CoversToday = f.CalMax >= today
	if err := d.db.QueryRow(`SELECT COALESCE(MAX(cal_date),'') FROM trade_cal WHERE is_open=1 AND cal_date<=?`, today).Scan(&f.LastOpen); err != nil {
		return f, err
	}
	if f.LastOpen == "" {
		return f, nil // 日历不含今日以前的开市日（异常数据）——不误报
	}
	// 行情最新交易日：daily ∪ ths_daily 取大（YYYYMMDD 字符串同格式可比）
	var dailyMax, thsMax string
	if err := d.db.QueryRow(`SELECT COALESCE(MAX(trade_date),'') FROM daily`).Scan(&dailyMax); err != nil {
		return f, err
	}
	if err := d.db.QueryRow(`SELECT COALESCE(MAX(trade_date),'') FROM ths_daily`).Scan(&thsMax); err != nil {
		return f, err
	}
	f.Latest = dailyMax
	if thsMax > f.Latest {
		f.Latest = thsMax
	}
	if f.Latest == "" {
		return f, nil // 行情两表全空：未启动采集（全新部署合法态）
	}
	if !f.CoversToday {
		// 日历停更 ⇒ 日龄算不出来（能算出来的那个 0 是假的），按"算不了"报而不是冒充 0 滞后。
		f.LagDays = -1
		f.OK = false
		f.Reason = FreshReasonCalendarStalled
		return f, nil
	}
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM trade_cal WHERE is_open=1 AND cal_date>? AND cal_date<=?`, f.Latest, f.LastOpen).Scan(&f.LagDays); err != nil {
		return f, err
	}
	f.OK = f.LagDays <= maxLag
	if !f.OK {
		f.Reason = FreshReasonLag
	}
	return f, nil
}
