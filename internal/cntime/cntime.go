// Package cntime §时区收口：全系统交易日/交易窗口判断的唯一时区源（Asia/Shanghai）。
//
// 此前全部时段判断依赖宿主机 Local 时区——部署到 UTC 主机（容器/云主机默认）时，
// 9:15 开盘门控漂移为 17:15、盘中反而放行夜间任务、交易日分桶整体漂移一天。
// 所有"现在几点/今天几号"的语义必须经本包转换，禁止直接 time.Now().Hour()。
//
// English: canonical Asia/Shanghai clock for every trading-day/window decision. Host-local
// timezone assumptions broke gates by 8h on UTC hosts; route all wall-clock semantics here.
package cntime

import "time"

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
