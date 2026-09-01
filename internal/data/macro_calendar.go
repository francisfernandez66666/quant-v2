// Package data — 宏观事件日历生成器
// 自动生成 FOMC/CPI/非农/核心PCE/股指期货交割日等定期事件，
// 支持从 rules.json 补充自定义事件，地缘冲突由 D1 新闻匹配实时识别。
// English: Package data — macro-event calendar generator.
// English: Auto-generates recurring events such as FOMC/CPI/NFP/core-PCE/index-futures delivery days;
// English: supports extra custom events from rules.json; geopolitical conflicts are identified live via D1 news matching.

package data

import (
	"encoding/json"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"strings"
	"time"

	"quant-trading-v2/internal/cntime"
)

// MacroEvent 宏观事件定义
// English: MacroEvent defines a macro event.
type MacroEvent struct {
	Date     time.Time `json:"date"`      // 事件日期
	Title    string    `json:"title"`     // 事件标题
	Level    string    `json:"level"`     // 事件类型（fomc/cpi/nfp/pce/contract/war）
	Impact   string    `json:"impact"`    // 影响程度（high/medium/low）
	Duration int       `json:"duration"`  // 影响期天数（事件日前/后各 Duration 天为影响期）
	DaysLeft int       `json:"days_left"` // 距离事件结束的剩余天数（由筛选逻辑计算）
	// English: Date: event date; Title: event title; Level: event type (fomc/cpi/nfp/pce/contract/war);
	// English: Impact: impact level (high/medium/low); Duration: impact-period days (Duration days before/after the event date);
	// English: DaysLeft: remaining days until the event ends (computed by the filter logic).
}

// GenMacroEvents 生成指定年份的全部宏观事件
// supplement 自定义补充事件（§R3-8 P1-J 真实接线）：key=事件标题，
// value="YYYY-MM-DD|impact[|duration]"（如 "2026-09-18|high|2"）；非法条目记日志跳过。
// English: GenMacroEvents generates all macro events for a given year.
// English: supplement holds custom events (key=title, value="date|impact[|duration]"); invalid
// entries are logged and skipped.
func GenMacroEvents(year int, supplement map[string]string) []MacroEvent {
	var events []MacroEvent

	// FOMC 会议：仅 2026 有经核实的会议日期表。§R3-8 P1-J 年份门控——此前对任意 year
	// 套用同一组月日，2027 起生成的日历全是错误日期。未知年份跳过 FOMC 并告警一次
	// （宁可缺事件不可造事件）；NFP/交割日/PCE 为公式推导不受影响。
	// English: R3-8 P1-J — the FOMC table is verified for 2026 only; other years skip FOMC with a
	// warning instead of fabricating wrong dates.
	if year == 2026 {
		fomcDates := []struct {
			m, d int
			note string
		}{
			{1, 28, "FOMC 1月议息会议"},
			{3, 18, "FOMC 3月议息会议+点阵图+经济预测"},
			{5, 6, "FOMC 5月议息会议"},
			{6, 17, "FOMC 6月议息会议+点阵图+经济预测"},
			{7, 29, "FOMC 7月议息会议"},
			{9, 16, "FOMC 9月议息会议+点阵图+经济预测"},
			{11, 5, "FOMC 11月议息会议"},
			{12, 16, "FOMC 12月议息会议+经济预测"},
		}
		for _, f := range fomcDates {
			d := time.Date(year, time.Month(f.m), f.d, 0, 0, 0, 0, time.UTC)
			events = append(events, MacroEvent{
				Date: d, Title: f.note, Level: "fomc", Impact: "high", Duration: 3,
			})
		}
	} else if !macroFomcWarned {
		macroFomcWarned = true
		log.Printf("[macro] %d 年无已核实 FOMC 日期表，本年跳过 FOMC 事件（NFP/交割日等公式事件不受影响）", year)
	}

	// 非农 (NFP): 每月第一个周五
	// English: Nonfarm payrolls (NFP): the first Friday of each month.
	firstFri := func(y int, m time.Month) time.Time {
		d := time.Date(y, m, 1, 0, 0, 0, 0, time.UTC)
		for d.Weekday() != time.Friday {
			d = d.AddDate(0, 0, 1)
		}
		return d
	}
	for m := 1; m <= 12; m++ {
		d := firstFri(year, time.Month(m))
		events = append(events, MacroEvent{
			Date: d, Title: monthCN(m) + "美国非农就业数据",
			Level: "nfp", Impact: "high", Duration: 3,
		})
	}

	// CPI: 每月 10-15 日之间发布（取 13 日为估计值）
	// English: CPI: released between the 10th and 15th of each month (day 13 used as estimate).
	for m := 1; m <= 12; m++ {
		d := time.Date(year, time.Month(m), 13, 0, 0, 0, 0, time.UTC)
		events = append(events, MacroEvent{
			Date: d, Title: monthCN(m) + "美国CPI数据",
			Level: "cpi", Impact: "high", Duration: 3,
		})
	}

	// 核心 PCE: 每月最后一天
	// English: Core PCE: the last day of each month.
	for m := 1; m <= 12; m++ {
		first := time.Date(year, time.Month(m), 1, 0, 0, 0, 0, time.UTC)
		last := first.AddDate(0, 1, -1)
		events = append(events, MacroEvent{
			Date: last, Title: monthCN(m) + "美国核心PCE物价指数",
			Level: "pce", Impact: "high", Duration: 3,
		})
	}

	// 股指期货交割日: 每月第三个周五
	// English: Index-futures delivery day: the third Friday of each month.
	thirdFri := func(y int, m time.Month) time.Time {
		d := time.Date(y, m, 1, 0, 0, 0, 0, time.UTC)
		for d.Weekday() != time.Friday {
			d = d.AddDate(0, 0, 1)
		}
		return d.AddDate(0, 0, 14) // 第1个周五+14天=第3个周五
		// English: 1st Friday + 14 days = 3rd Friday.
	}
	for m := 1; m <= 12; m++ {
		d := thirdFri(year, time.Month(m))
		events = append(events, MacroEvent{
			Date: d, Title: monthCN(m) + "股指期货交割日",
			Level: "contract", Impact: "high", Duration: 2,
		})
	}

	// 合并自定义补充事件（§R3-8 P1-J 真实解析，此前入参被显式丢弃）：
	// key=标题，value="YYYY-MM-DD|impact[|duration]"；level 统一记 custom。
	// English: R3-8 P1-J — actually parse the supplement now: key=title,
	// value="YYYY-MM-DD|impact[|duration]"; invalid entries are skipped with a log.
	for title, spec := range supplement {
		parts := strings.Split(spec, "|")
		if len(parts) < 2 || parts[0] == "" || title == "" {
			log.Printf("[macro] 补充事件格式非法（应为 标题→日期|impact[|duration]），跳过: %q → %q", title, spec)
			continue
		}
		// §修复 D4（2026-08-29）：事件日期按北京时区解析，避免 UTC 主机上日期边界错 8 小时
		// （交割日/高影响门控可能早/晚一天触发）。
		d, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(parts[0]), cntime.Loc)
		if err != nil {
			log.Printf("[macro] 补充事件日期解析失败，跳过: %q → %q (%v)", title, spec, err)
			continue
		}
		impact := strings.ToLower(strings.TrimSpace(parts[1]))
		switch impact {
		case "high", "medium", "low":
		default:
			impact = "medium"
		}
		duration := 2
		if len(parts) >= 3 {
			if n := atoiDefault(parts[2], 2); n > 0 && n <= 30 {
				duration = n
			}
		}
		events = append(events, MacroEvent{
			Date: d, Title: title, Level: "custom", Impact: impact, Duration: duration,
		})
	}

	return events
}

// macroFomcWarned 非核实年份的 FOMC 缺失告警只打一次（进程级节流）。
var macroFomcWarned = false

// atoiDefault 整数解析失败时返回默认值。
func atoiDefault(s string, def int) int {
	n := 0
	for _, c := range strings.TrimSpace(s) {
		if c < '0' || c > '9' {
			return def
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// AddGeopoliticalEvent 从新闻标题注入战争/地缘事件。
// 标题中包含 geopolitical 关键词时触发，Duration=0（不设限）。
// English: AddGeopoliticalEvent injects war/geopolitical events from news titles.
// English: Triggered when the title contains geopolitical keywords; Duration=0 (no window).
func AddGeopoliticalEvent(events *[]MacroEvent, title string) {
	if title == "" {
		return
	}
	for _, e := range *events {
		if e.Level == "war" && e.Title == title {
			return // 已存在，去重
			// English: Already exists, dedup.
		}
	}
	*events = append(*events, MacroEvent{
		Date:     cntime.Now(),
		Title:    title,
		Level:    "war",
		Impact:   "high",
		Duration: 0,
		DaysLeft: 0,
	})
}

// GetActiveMacroEvents 筛选 now 时刻处于"影响期"或未来 7 天内即将发生的宏观事件。
// 影响期 = 事件日前 Duration 天至事件日后 Duration 天；
// 返回按优先级（war > contract > fomc > pce > cpi > nfp > other）降序排列。
// English: GetActiveMacroEvents filters events within their "impact period" at now or due within the next 7 days.
// English: Impact period = Duration days before to Duration days after the event date.
// English: Returns sorted by priority (war > contract > fomc > pce > cpi > nfp > other) descending.
func GetActiveMacroEvents(events []MacroEvent, now time.Time) []MacroEvent {
	var active []MacroEvent

	priority := map[string]int{
		"war":      100,
		"contract": 90,
		"fomc":     80,
		"pce":      70,
		"cpi":      60,
		"nfp":      50,
		"other":    10,
	}

	for _, e := range events {
		before := e.Date.AddDate(0, 0, -e.Duration)
		after := e.Date.AddDate(0, 0, e.Duration)
		// 当前在影响期内，或未来7天内即将发生
		// English: Currently in the impact period, or due within the next 7 days.
		inRange := (now.After(before) && now.Before(after.AddDate(0, 0, 1))) ||
			(now.Before(e.Date) && e.Date.Sub(now).Hours() <= 168)
		if inRange {
			e.DaysLeft = int(math.Ceil(after.Sub(now).Hours() / 24))
			active = append(active, e)
		}
	}

	// 按优先级排序
	// English: Sort by priority.
	for i := 0; i < len(active); i++ {
		for j := i + 1; j < len(active); j++ {
			if priority[active[i].Level] < priority[active[j].Level] {
				active[i], active[j] = active[j], active[i]
			}
		}
	}
	return active
}

// MacroEventDesc 生成宏观事件描述文本（用于嵌入信号消息）
// 例如："背景：美联储7月议息会议影响期(剩余2天) | 股指期货交割日(进行中)"
// English: MacroEventDesc builds macro-event description text (for embedding in signal messages).
// English: E.g. "Background: Fed July FOMC meeting impact period (2 days left) | Index-futures delivery day (ongoing)".
func MacroEventDesc(events []MacroEvent) string {
	if len(events) == 0 {
		return ""
	}
	s := "背景："
	for i, e := range events {
		if i > 0 {
			s += " | "
		}
		s += e.Title
		if e.Level == "war" || e.Level == "geopolitical" {
			s += "(地缘冲突/不可抗力)"
		} else if e.DaysLeft > 0 {
			s += "(剩余" + itoa(e.DaysLeft) + "天)"
		} else {
			s += "(进行中)"
		}
	}
	return s
}

// monthCN 将月份数字（1-12）转为中文月名（如 1 → "1月"），越界返回空串。
// English: monthCN converts a month number (1-12) to a Chinese month name (e.g. 1 → "1月"); returns empty string out of range.
func monthCN(m int) string {
	names := []string{"", "1月", "2月", "3月", "4月", "5月", "6月",
		"7月", "8月", "9月", "10月", "11月", "12月"}
	if m >= 1 && m <= 12 {
		return names[m]
	}
	return ""
}

// itoa 将整数转为十进制字符串（避免引入 strconv 依赖，支持负数）。
// English: itoa converts an integer to a decimal string (avoids a strconv dependency, supports negatives).
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	r := ""
	t := i
	if t < 0 {
		t = -t
	}
	for t > 0 {
		r = string(rune('0'+t%10)) + r
		t /= 10
	}
	if i < 0 {
		r = "-" + r
	}
	return r
}

// ExternalEvent 外部日历API返回的单条事件（标准JSON格式）
// English: ExternalEvent is a single event returned by an external calendar API (standard JSON format).
type ExternalEvent struct {
	Date   string `json:"date"`   // 事件日期 YYYY-MM-DD
	Title  string `json:"title"`  // 事件标题
	Impact string `json:"impact"` // 影响程度：high/medium/low（高/中/低）
	Level  string `json:"level"`  // 事件类型(可选): fomc/cpi/nfp/pce/contract
	// English: Date: YYYY-MM-DD; Title: event title; Impact: high/medium/low; Level: event type (optional): fomc/cpi/nfp/pce/contract.
}

// FetchCalendarFromAPI 从外部API获取宏观事件列表。
// url为空或请求失败时返回nil。
// English: FetchCalendarFromAPI fetches the macro-event list from an external API.
// English: Returns nil when url is empty or the request fails.
func FetchCalendarFromAPI(apiURL string) []ExternalEvent {
	if apiURL == "" {
		return nil
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(apiURL)
	if err != nil {
		log.Printf("宏观日历API请求失败(%s): %v", apiURL, err)
		return nil
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("宏观日历API读取失败(%s): %v", apiURL, err)
		return nil
	}
	// 尝试解析为JSON数组
	// English: Try to parse as a JSON array.
	var events []ExternalEvent
	if err := json.Unmarshal(body, &events); err != nil {
		log.Printf("宏观日历API解析失败(%s): %v", apiURL, err)
		// 如果外部API不可用，回退到算法生成
		// English: If the external API is unavailable, fall back to algorithmic generation.
		return nil
	}
	log.Printf("宏观日历API成功: %d条事件 (%s)", len(events), apiURL)
	return events
}

// minInt 整数取小
// English: minInt returns the smaller of two integers.
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ChatFunc 通用 LLM 聊天函数签名，避免 calendar 包直接依赖 llm 包
// English: ChatFunc is a generic LLM chat function signature, avoiding a direct dependency of the calendar package on the llm package.
type ChatFunc func(system, user string) (string, error)

// LLMCalendarPrompt 生成日历专属的 system+user prompt
// English: LLMCalendarPrompt generates the calendar-specific system+user prompt.
func LLMCalendarPrompt(months int) (string, string) {
	now := cntime.Now()
	end := now.AddDate(0, months, 0)
	system := "你是一个A股宏观日历助手。返回纯JSON数组，不要任何其他文字。事件类型以下划线命名：cpi/nfp/pce/fomc/contract/geo/other"
	user := "生成" + now.Format("2006-01") + "至" + end.Format("2006-01") + "间的宏观事件。具体要求：\n" +
		"1. FOMC议息会议日期（带点阵图的标注）\n" +
		"2. 美国CPI数据发布日期（每月10-15日左右）\n" +
		"3. 美国非农就业数据发布日期（每月第一个周五）\n" +
		"4. 美国核心PCE物价指数发布日期（每月底）\n" +
		"5. A股股指期货交割日（每月第三个周五）\n" +
		"6. 已知的战争/地缘冲突事件（如果有）\n" +
		"JSON格式：[{\"date\":\"2026-08-12\",\"title\":\"美国7月CPI数据\",\"impact\":\"high\",\"level\":\"cpi\"}]\n" +
		"只输出JSON数组。"
	return system, user
}

// FetchCalendarFromLLM 用LLM生成宏观事件日历
// English: FetchCalendarFromLLM generates the macro-event calendar using an LLM.
func FetchCalendarFromLLM(chat ChatFunc, months int) []ExternalEvent {
	system, user := LLMCalendarPrompt(months)
	reply, err := chat(system, user)
	if err != nil {
		log.Printf("LLM日历请求失败: %v", err)
		return nil
	}
	reply = cleanJSONBlock(reply)
	var events []ExternalEvent
	if err := json.Unmarshal([]byte(reply), &events); err != nil {
		log.Printf("LLM日历解析失败: %v\nreply=%s", err, reply[:minInt(len(reply), 300)])
		return nil
	}
	log.Printf("LLM日历: 生成%d条事件", len(events))
	return events
}

// EventResult 宏观事件发布结果
// English: EventResult is the published result of a macro event.
type EventResult struct {
	Title     string `json:"title"`     // 事件标题
	Value     string `json:"value"`     // 实际公布值
	Expect    string `json:"expect"`    // 预期值
	Compare   string `json:"compare"`   // 对比结果（高于预期/低于预期/符合预期）
	Sentiment string `json:"sentiment"` // 市场影响（利好/利空/中性）
	Summary   string `json:"summary"`   // 一句话总结
	// English: Title: event title; Value: actual published value; Expect: expected value;
	// English: Compare: comparison result (above/below/in line with expectations);
	// English: Sentiment: market impact (bullish/bearish/neutral); Summary: one-line summary.
}

// FetchEventResult 查询已发生宏观事件的真实数据结果
// English: FetchEventResult queries the real published result of an already-occurred macro event.
func FetchEventResult(chat ChatFunc, title, date string) *EventResult {
	system := "你是一个宏观数据助手。查询指定事件的真实发布结果。返回纯JSON。"
	user := "查询以下宏观事件的实际公布结果：\n事件：" + title + "\n日期：" + date + "\n\n" +
		"请返回JSON格式：\n{\"title\":\"事件标题\",\"value\":\"实际值\",\"expect\":\"预期值\"," +
		"\"compare\":\"高于预期|低于预期|符合预期\",\"sentiment\":\"利好|利空|中性\",\"summary\":\"一句话总结\"}\n" +
		"只输出JSON。如果不确定请填\"数据待查\"。"
	reply, err := chat(system, user)
	if err != nil {
		log.Printf("LLM事件结果查询失败(%s): %v", title, err)
		return nil
	}
	reply = cleanJSONBlock(reply)
	var result EventResult
	if err := json.Unmarshal([]byte(reply), &result); err != nil {
		log.Printf("LLM事件结果解析失败(%s): %v", title, err)
		return nil
	}
	return &result
}

// cleanJSONBlock 从LLM回复中提取纯JSON（去除markdown代码块等）
// English: cleanJSONBlock extracts pure JSON from an LLM reply (strips markdown code fences etc.).
func cleanJSONBlock(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		lines := strings.SplitN(s, "\n", 2)
		if len(lines) >= 2 {
			s = lines[1]
		}
	}
	if idx := strings.LastIndex(s, "```"); idx >= 0 {
		s = s[:idx]
	}
	return strings.TrimSpace(s)
}

// CalendarCacheFile 日历缓存文件格式
// English: CalendarCacheFile is the calendar cache file format.
type CalendarCacheFile struct {
	Date   string       `json:"date"`   // 缓存日期 YYYY-MM-DD
	Events []MacroEvent `json:"events"` // 事件列表
	// English: Date: cache date YYYY-MM-DD; Events: event list.
}

// SaveCalendarCache 将日历事件缓存到文件
// English: SaveCalendarCache writes the calendar events to a cache file.
func SaveCalendarCache(filePath string, events []MacroEvent) error {
	cached := CalendarCacheFile{
		Date:   cntime.DayOf(time.Now()),
		Events: events,
	}
	data, err := json.Marshal(cached)
	if err != nil {
		return err
	}
	return atomicWrite(filePath, data, 0644)
}

// LoadCalendarCache 从文件加载日历缓存（仅当天有效）
// 返回 events 和 ok；缓存不存在或日期不匹配时 ok=false
// English: LoadCalendarCache loads the calendar cache from a file (valid only for the current day).
// English: Returns events and ok; ok=false when the cache is missing or the date does not match.
func LoadCalendarCache(filePath string) (events []MacroEvent, ok bool) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, false
	}
	var cached CalendarCacheFile
	if err := json.Unmarshal(data, &cached); err != nil {
		return nil, false
	}
	if cached.Date != cntime.DayOf(time.Now()) {
		return nil, false // 缓存过期
		// English: Cache expired.
	}
	return cached.Events, true
}
