// Package cntime §时区收口：全系统交易日/交易窗口判断的唯一时区源（Asia/Shanghai）。
//
// 此前全部时段判断依赖宿主机 Local 时区——部署到 UTC 主机（容器/云主机默认）时，
// 9:15 开盘门控漂移为 17:15、盘中反而放行夜间任务、交易日分桶整体漂移一天。
// 所有"现在几点/今天几号"的语义必须经本包转换，禁止直接 time.Now().Hour()。
//
// English: canonical Asia/Shanghai clock for every trading-day/window decision. Host-local
// timezone assumptions broke gates by 8h on UTC hosts; route all wall-clock semantics here.
package cntime

import (
	"strings"
	"time"
)

// Loc 北京时间（Asia/Shanghai；tzdata 缺失时退回固定 +08:00，语义不变）。
var Loc = func() *time.Location {
	if l, err := time.LoadLocation("Asia/Shanghai"); err == nil {
		return l
	}
	return time.FixedZone("CST", 8*3600)
}()

// Now 当前北京时间。
func Now() time.Time { return time.Now().In(Loc) }

// In 把任意时刻转换为北京时区视图（不改变时刻本身）。
func In(t time.Time) time.Time { return t.In(Loc) }

// DayOf 北京日期 YYYY-MM-DD（T+1 日界/跨日清空等按北京日历）。
func DayOf(t time.Time) string { return In(t).Format("2006-01-02") }

// DayCompactOf 北京日期 YYYYMMDD（交易日分桶/chain_day）。
func DayCompactOf(t time.Time) string { return In(t).Format("20060102") }

// CompactDay 把一个**日期字符串**归一成 YYYYMMDD；形状不认识就返回空串（绝不猜）。
//
// §REPLAY-INPUTS 2026-11-28：本仓同时存在两种日尺——`daily/index_daily/stk_limit/stocks.list_date`
// 是紧凑 `20261127`，而 `flow_factor_daily/sector_flow_daily/d1_scores` 与东财上游 f51 是横线
// `2026-11-27`。这个差异已经造成过三次"查错形状 ⇒ 永远今天没数、且不报错"的事故
// （AGENTS 里 §FLOW-SECTOR-JOIN、§FACTOR-FIX、§BOX-FIX 各一处）。取向：
//   - **只在写入边界调用一次**，库里同一张表绝不两种形态并存；
//   - 空串是"读不懂"而不是"等于 0"——调用方必须显式弃权，不许拿零值继续算。
//
// English: normalizes a date string to YYYYMMDD; returns "" for anything unrecognized so
// callers abstain instead of silently comparing against a zero-shaped key.
func CompactDay(s string) string {
	t := strings.TrimSpace(s)
	if len(t) == 8 {
		for i := 0; i < 8; i++ {
			if t[i] < '0' || t[i] > '9' {
				return ""
			}
		}
		return t
	}
	if len(t) == 10 && t[4] == '-' && t[7] == '-' {
		digits := t[:4] + t[5:7] + t[8:10]
		for i := 0; i < 8; i++ {
			if digits[i] < '0' || digits[i] > '9' {
				return ""
			}
		}
		return digits
	}
	return ""
}

// CalendarLookaheadDays 交易日历回写窗口的未来延伸天数。
//
// §CAL-COVER-GO（2026-10-12）：日历写回右边界一旦等于"今日"，明天这张表的末行就落在昨天，
// 而任何按「≤今日的最近开市日」倒推的判据都会把"行情与日历一起停在昨天"读成满分——
// 09-28~09-30、10-08~10-09 两段真实交易日就是这么被整段跳过的（现网实录见 RUNBOOK §4.1b.26）。
// 因此日历回写必须往未来延伸，且**全部生产者共用这一个尺度**：Go 侧 cmd/dataload 的两条
// 写入口、Python 侧 scripts/dataload_keepalive.py 的 CALENDAR_LOOKAHEAD_DAYS 都是 45；
// 一旦某个源用了别的数字（或退回 today），覆盖检查会在判据上先失明而不是先报错。
// 45 天的下界来自"长假 + 未发布年度日历"：国庆/春节连休加数据源只给当年行时，窗口不足以
// 让末行跨过整个假期（现网 sidecar 实测 baostock 日历含全年未来日期）。
// English: the calendar write window must extend into the future; every producer shares this
// single number, otherwise the coverage assertion goes blind instead of failing loudly.
const CalendarLookaheadDays = 45

// CalendarEndDayCompact 日历回写窗口右边界（今日 + CalendarLookaheadDays 天），YYYYMMDD。
// 供 tushare 口径的 TradeCal(start,end) 使用。
func CalendarEndDayCompact(t time.Time) string {
	return DayCompactOf(In(t).AddDate(0, 0, CalendarLookaheadDays))
}

// CalendarEndDay dashed 版（YYYY-MM-DD），供 baostock / pydata 口径的 TradeDays(start,end) 使用。
// 与 CalendarEndDayCompact 必须是同一个常量算出来的，否则两个源写同一张表会互相把末行拖回去。
func CalendarEndDay(t time.Time) string {
	return DayOf(In(t).AddDate(0, 0, CalendarLookaheadDays))
}
