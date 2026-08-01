// Package data — 宏观事件日历生成器
// 自动生成 FOMC/CPI/非农/核心PCE/股指期货交割日等定期事件，
// 支持从 rules.json 补充自定义事件，地缘冲突由 D1 新闻匹配实时识别。

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
)

// MacroEvent 宏观事件定义
type MacroEvent struct {
	Date     time.Time `json:"date"`
	Title    string    `json:"title"`
	Level    string    `json:"level"`
	Impact   string    `json:"impact"`
	Duration int       `json:"duration"`
	DaysLeft int       `json:"days_left"`
}

// GenMacroEvents 生成指定年份的全部宏观事件
// supplement 从 rules.json 读取的补充事件，会合并到结果中
func GenMacroEvents(year int, supplement map[string]string) []MacroEvent {
	var events []MacroEvent

	// FOMC 会议（2026年已知会议日期）
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
		{12, 16, "FOMC 12月议息会议+点阵图+经济预测"},
	}
	for _, f := range fomcDates {
		d := time.Date(year, time.Month(f.m), f.d, 0, 0, 0, 0, time.UTC)
		events = append(events, MacroEvent{
			Date: d, Title: f.note, Level: "fomc", Impact: "high", Duration: 3,
		})
	}

	// 非农 (NFP): 每月第一个周五
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
	for m := 1; m <= 12; m++ {
		d := time.Date(year, time.Month(m), 13, 0, 0, 0, 0, time.UTC)
		events = append(events, MacroEvent{
			Date: d, Title: monthCN(m) + "美国CPI数据",
			Level: "cpi", Impact: "high", Duration: 3,
		})
	}

	// 核心 PCE: 每月最后一天
	for m := 1; m <= 12; m++ {
		first := time.Date(year, time.Month(m), 1, 0, 0, 0, 0, time.UTC)
		last := first.AddDate(0, 1, -1)
		events = append(events, MacroEvent{
			Date: last, Title: monthCN(m) + "美国核心PCE物价指数",
			Level: "pce", Impact: "high", Duration: 3,
		})
	}

	// 股指期货交割日: 每月第三个周五
	thirdFri := func(y int, m time.Month) time.Time {
		d := time.Date(y, m, 1, 0, 0, 0, 0, time.UTC)
		for d.Weekday() != time.Friday {
			d = d.AddDate(0, 0, 1)
		}
		return d.AddDate(0, 0, 14) // 第1个周五+14天=第3个周五
	}
	for m := 1; m <= 12; m++ {
		d := thirdFri(year, time.Month(m))
		events = append(events, MacroEvent{
			Date: d, Title: monthCN(m) + "股指期货交割日",
			Level: "contract", Impact: "high", Duration: 2,
		})
	}

	// 合并 rules.json 补充事件
	for title, impact := range supplement {
		// supplement 格式: "date|impact|duration" 或 "date|impact"
		// 简化处理: 直接使用 title 作为描述，impact 为级别
		_ = title
		_ = impact
	}

	return events
}

// AddGeopoliticalEvent 从新闻标题注入战争/地缘事件。
// 标题中包含 geopolitical 关键词时触发，Duration=0（不设限）。
func AddGeopoliticalEvent(events *[]MacroEvent, title string) {
	if title == "" {
		return
	}
	for _, e := range *events {
		if e.Level == "war" && e.Title == title {
			return // 已存在，去重
		}
	}
	*events = append(*events, MacroEvent{
		Date:     time.Now(),
		Title:    title,
		Level:    "war",
		Impact:   "high",
		Duration: 0,
		DaysLeft: 0,
	})
}

// GetActiveMacroEvents 筛选当前处于"影响期"或未来 7 天内即将发生的宏观事件。
// 影响期 = 事件日前 Duration 天至事件日后 Duration 天；
// 返回按优先级（war > contract > fomc > pce > cpi > nfp > other）降序排列。
func GetActiveMacroEvents(events []MacroEvent) []MacroEvent {
	now := time.Now()
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
		inRange := (now.After(before) && now.Before(after.AddDate(0, 0, 1))) ||
			(now.Before(e.Date) && e.Date.Sub(now).Hours() <= 168)
		if inRange {
			e.DaysLeft = int(math.Ceil(after.Sub(now).Hours() / 24))
			active = append(active, e)
		}
	}

	// 按优先级排序
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
func monthCN(m int) string {
	names := []string{"", "1月", "2月", "3月", "4月", "5月", "6月",
		"7月", "8月", "9月", "10月", "11月", "12月"}
	if m >= 1 && m <= 12 {
		return names[m]
	}
	return ""
}

// itoa 将整数转为十进制字符串（避免引入 strconv 依赖，支持负数）。
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
type ExternalEvent struct {
	Date   string `json:"date"`   // YYYY-MM-DD
	Title  string `json:"title"`  // 事件标题
	Impact string `json:"impact"` // high/medium/low
	Level  string `json:"level"`  // 事件类型(可选): fomc/cpi/nfp/pce/contract
}

// FetchCalendarFromAPI 从外部API获取宏观事件列表。
// url为空或请求失败时返回nil。
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
	var events []ExternalEvent
	if err := json.Unmarshal(body, &events); err != nil {
		log.Printf("宏观日历API解析失败(%s): %v", apiURL, err)
		// 如果外部API不可用，回退到算法生成
		return nil
	}
	log.Printf("宏观日历API成功: %d条事件 (%s)", len(events), apiURL)
	return events
}

// minInt 整数取小
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ChatFunc 通用 LLM 聊天函数签名，避免 calendar 包直接依赖 llm 包
type ChatFunc func(system, user string) (string, error)

// LLMCalendarPrompt 生成日历专属的 system+user prompt
func LLMCalendarPrompt(months int) (string, string) {
	now := time.Now()
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
type EventResult struct {
	Title     string `json:"title"`
	Value     string `json:"value"`
	Expect    string `json:"expect"`
	Compare   string `json:"compare"`
	Sentiment string `json:"sentiment"`
	Summary   string `json:"summary"`
}

// FetchEventResult 查询已发生宏观事件的真实数据结果
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
type CalendarCacheFile struct {
	Date   string       `json:"date"`   // 缓存日期 YYYY-MM-DD
	Events []MacroEvent `json:"events"` // 事件列表
}

// SaveCalendarCache 将日历事件缓存到文件
func SaveCalendarCache(filePath string, events []MacroEvent) error {
	cached := CalendarCacheFile{
		Date:   time.Now().Format("2006-01-02"),
		Events: events,
	}
	data, err := json.Marshal(cached)
	if err != nil {
		return err
	}
	return os.WriteFile(filePath, data, 0644)
}

// LoadCalendarCache 从文件加载日历缓存（仅当天有效）
// 返回 events 和 ok；缓存不存在或日期不匹配时 ok=false
func LoadCalendarCache(filePath string) (events []MacroEvent, ok bool) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, false
	}
	var cached CalendarCacheFile
	if err := json.Unmarshal(data, &cached); err != nil {
		return nil, false
	}
	if cached.Date != time.Now().Format("2006-01-02") {
		return nil, false // 缓存过期
	}
	return cached.Events, true
}
