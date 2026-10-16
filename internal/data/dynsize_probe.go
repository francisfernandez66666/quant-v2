// dynsize_probe.go — §DYN-SIZE 引擎侧读数探针（节前窗口 / 交易日距离）。
//
// 这些读数是"每轮装配一次、注入 signalctl"的输入，不属于逐信号路径（§SIGBATCH 教训：
// 绝不逐信号打上游）。放在 data 包是因为它们要问交易日历（isClosedDay / IsTradingDay），
// 而日历状态是本包的私有全局。
//
// English: §DYN-SIZE engine-side probes (pre-holiday window, trading-day distance). Assembled
// once per round and injected into signalctl — never fetched per signal.
package data

import (
	"strings"
	"time"

	"quant-trading-v2/internal/cntime"
)

// HolidayWindowProbe 节前窗口读数（供 signalctl.Regime.Holiday 映射）。
//
//   - CalendarLoaded=false：交易日历未加载 → 调用方应让节前因子**弃权**（绝不 fail-open 当普通日）；
//   - ClosedRun：下一段连续休市天数（2=周末，≥3=小长假，≥5=长假）；
//   - PreHolidayN：节前第 N 个交易日（1=节前最后一日；0=不在节前窗口——本探针恒 ≥1）；
//   - DaysToNextOpen：距下一开市日的自然日跨度。
//
// English: the pre-holiday window reading consumed by signalctl.Regime.Holiday.
type HolidayWindowProbe struct {
	CalendarLoaded bool
	DaysToNextOpen int
	ClosedRun      int
	PreHolidayN    int
}

// ProbeHolidayWindow §DYN-SIZE：给定时刻的节前窗口读数。
//
// 口径：先归一到"基准交易日"（今日若非交易日则回退到最近一个交易日），再向后数：
//   - PreHolidayN = 从基准日起连续可交易的天数（1=基准日就是节前最后一日）；
//   - ClosedRun   = 基准日之后第一段连续休市的天数；
//   - DaysToNextOpen = 基准日到下一开市日的自然日跨度。
//
// 全部循环有 20 天硬上限（跨国庆长假足够），日历未加载时仍按周末口径计算（仅周末可识别），
// 但 CalendarLoaded=false 会让调用方弃权——所以这里的数值只是读数，不是决策。
//
// English: normalizes to a base trading day (today, or the last trading day if today is closed),
// then counts forward: PreHolidayN = consecutive tradable days from base (1 = base is the last one);
// ClosedRun = length of the next closed run; DaysToNextOpen = calendar days to the next open.
func ProbeHolidayWindow(now time.Time) HolidayWindowProbe {
	now = cntime.In(now)
	base := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	// 今日若非交易日（正处于休市中），回退到最近一个交易日作为计数基准。
	for i := 0; i < 20 && !IsTradingDay(base); i++ {
		base = base.AddDate(0, 0, -1)
	}

	// PreHolidayN：从基准日起连续可交易的天数（含基准日）。
	preN := 1
	d := base
	for i := 0; i < 20; i++ {
		d = d.AddDate(0, 0, 1)
		if !IsTradingDay(d) {
			break
		}
		preN++
	}

	// ClosedRun：基准日之后第一段连续休市的天数。
	d = base
	for i := 0; i < 20; i++ {
		d = d.AddDate(0, 0, 1)
		if !IsTradingDay(d) {
			break
		}
	}
	closedRun := 0
	for i := 0; i < 20 && !IsTradingDay(d); i++ {
		closedRun++
		d = d.AddDate(0, 0, 1)
	}

	// DaysToNextOpen：基准日到下一开市日的自然日跨度。
	d = base
	for i := 0; i < 20; i++ {
		d = d.AddDate(0, 0, 1)
		if IsTradingDay(d) {
			break
		}
	}
	daysToOpen := int(d.Sub(base).Hours() / 24)

	return HolidayWindowProbe{
		CalendarLoaded: CalendarLoaded(),
		DaysToNextOpen: daysToOpen,
		ClosedRun:      closedRun,
		PreHolidayN:    preN,
	}
}

// TradingDaysUntil §DYN-SIZE：从 today 到 target 之间的**交易日**数（不含 today，含 target）。
//
//	返回 0  = 事件就在今天（T日）
//	返回 >0 = 事件在未来第 N 个交易日
//	返回 -1 = 事件日已过（调用方应只按"影响期"处理，不再算前瞻）
//
// 必须按交易日而非自然日：跨周末/长假用自然日会把国庆 8 天算成 8 个"交易日"（方案 §5 测试锁）。
// 解析失败按 -1（弃权方向）返回。
//
// English: trading-day distance from today to target (excluding today, including target);
// 0 = today, >0 = N trading days ahead, -1 = past or unparsable.
func TradingDaysUntil(today, target string) int {
	t0, err0 := time.ParseInLocation("20060102", today, cntime.Loc)
	t1, err1 := time.ParseInLocation("20060102", target, cntime.Loc)
	if err0 != nil || err1 != nil {
		return -1
	}
	if t1.Before(t0) {
		return -1
	}
	if t1.Equal(t0) {
		return 0
	}
	n := 0
	// 硬上限 60 个自然日：前瞻窗最长 5 个交易日，超出必属配置异常，避免长循环。
	for d, i := t0.AddDate(0, 0, 1), 0; !d.After(t1) && i < 60; d, i = d.AddDate(0, 0, 1), i+1 {
		if IsTradingDay(d) {
			n++
		}
	}
	return n
}

// TradingDaySpan 从 today 起前进 n 个**交易日**所跨的**自然日**数（含节假日；n<=0 或解析失败 → 0）。
//
// §DYN-SIZE P4：把"事件前 N 个交易日"的前瞻窗折算成自然日跨度，供宏观事件列表的取数窗口使用。
// 为什么必须走日历：5 个交易日在平常是 7 个自然日，跨一次国庆长假却是 ~14 个——按自然日硬编码
// 的前瞻窗在长假期前会取不到事件，前瞻曲线形同虚设。
// English: natural-day span covering n trading days from today (holiday-aware); 0 when n<=0 or the
// date is unparsable. Used to size the macro-event fetch window from a trading-day lead.
func TradingDaySpan(today string, n int) int {
	if n <= 0 {
		return 0
	}
	base, err := time.ParseInLocation("20060102", today, cntime.Loc)
	if err != nil {
		return 0
	}
	target := AddTradingDays(today, n)
	t, err := time.ParseInLocation("20060102", target, cntime.Loc)
	if err != nil {
		return 0
	}
	d := int(t.Sub(base).Hours() / 24)
	if d < 0 {
		return 0
	}
	return d
}

// CalendarCoverage §DYN-SIZE P4：交易日历的**覆盖窗口**读数（min, max, loaded）。
//
// 为什么需要它：`RefreshTradingCalendar` **不外推 API 窗口外**，且未加载时 `isClosedDay` 恒 false
// （fail-open 把法定节假日当交易日）。节前因子与 P4 的前瞻窗折算都建立在"未来假期可知"之上——
// 覆盖到哪天必须能被看见，否则长假因子会静默退化成"只能识别周末"而无人知晓。
// English: §DYN-SIZE P4 — the calendar's coverage window. The holiday factor and the P4 lead-window
// conversion both depend on knowing future holidays, so the coverage horizon must be observable.
func CalendarCoverage() (min, max string, loaded bool) {
	calMu.RLock()
	defer calMu.RUnlock()
	if !calLoaded || calWindow == "" {
		return "", "", calLoaded
	}
	minD, maxD := calWindow, calWindow
	if i := strings.IndexByte(calWindow, '~'); i >= 0 {
		minD = calWindow[:i]
		if i+1 <= len(calWindow) {
			maxD = calWindow[i+1:]
		}
	}
	return minD, maxD, true
}
