// history_test.go — §ENH-3(20260919 批B) 历史事件归档/检索单测：
// 换日归档追加、坏行容忍、代码过滤、日期排除、去重与排序。
package newsagent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newHistoryAgent 建一个仅落盘依赖（dataDir=临时目录）的 Agent。
func newHistoryAgent(t *testing.T) *Agent {
	t.Helper()
	return New(nil, nil, nil, t.TempDir())
}

// TestRolloverArchivesOldEvents 换日清空前旧事件必须整批归档到 JSONL（带旧交易日标记）。
func TestRolloverArchivesOldEvents(t *testing.T) {
	a := newHistoryAgent(t)
	// 预置"上一个历史日"的 newsDB（两天前必然 != 今日交易日，触发换日分支）
	old := &newsDB{TradingDay: "20200101", Events: []NewsEvent{
		{Title: "旧事件甲", Datetime: "2020-01-01 09:30:00", Source: "财联社", Level: "个股", Direction: "利好", Score: 0.6, CleanedStocks: []string{"测试股|000001"}},
		{Title: "旧事件乙", Datetime: "2020-01-01 14:00:00", Source: "同花顺", Level: "板块", Direction: "利空", Score: -0.4, Sectors: []string{"半导体"}},
	}}
	raw, _ := json.Marshal(old)
	if err := os.WriteFile(a.newsDBPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	// 触发一次带新事件的保存（minScore 默认 0.25，用 0.5 通过过滤）
	a.saveNewsEvents([]NewsEvent{{Title: "今日新事件", Datetime: "2026-09-19 10:00:00", Score: 0.5, Level: "个股", Direction: "利好"}})

	f, err := os.ReadFile(a.historyPath)
	if err != nil {
		t.Fatalf("归档文件应已生成: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(f)), "\n")
	if len(lines) != 2 {
		t.Fatalf("应归档旧事件 2 条, got %d:\n%s", len(lines), f)
	}
	var e0 eventHistoryEntry
	if err := json.Unmarshal([]byte(lines[0]), &e0); err != nil {
		t.Fatal(err)
	}
	if e0.TradingDay != "20200101" || e0.Event.Title != "旧事件甲" {
		t.Fatalf("归档内容不符: %+v", e0)
	}
	// 今日事件不得混入归档（归档只发生在换日点）
	if strings.Contains(string(f), "今日新事件") {
		t.Fatal("当日事件不应进归档")
	}
	// 同日再次保存不重复归档
	a.saveNewsEvents([]NewsEvent{{Title: "今日新事件2", Datetime: "2026-09-19 10:05:00", Score: 0.5, Level: "个股", Direction: "利好"}})
	f2, _ := os.ReadFile(a.historyPath)
	if strings.Count(string(f2), "旧事件甲") != 1 {
		t.Fatalf("同日重复保存不应重复归档:\n%s", f2)
	}
}

// TestSearchEventHistory 检索语义：代码过滤 / 严格早于 excludeFrom / (day,标题) 去重 / |score| 优先。
func TestSearchEventHistory(t *testing.T) {
	a := newHistoryAgent(t)
	entries := []eventHistoryEntry{
		{TradingDay: "20260728", Event: NewsEvent{Title: "中标大单", Datetime: "2026-07-28 10:00:00", Score: 0.62, Level: "个股", Direction: "利好", Source: "财联社", CleanedStocks: []string{"卧龙电驱|600580"}}},
		{TradingDay: "20260710", Event: NewsEvent{Title: "股东减持", Datetime: "2026-07-10 15:00:00", Score: -0.8, Level: "个股", Direction: "利空", Source: "公告", RelatedStocks: []string{"卧龙电驱(600580)"}}},
		{TradingDay: "20260728", Event: NewsEvent{Title: "中标大单", Datetime: "2026-07-28 10:00:00", Score: 0.62, Level: "个股", Direction: "利好", CleanedStocks: []string{"卧龙电驱|600580"}}}, // 重复行→去重
		{TradingDay: "20260729", Event: NewsEvent{Title: "别的股票", Datetime: "2026-07-29 11:00:00", Score: 0.9, Level: "个股", Direction: "利好", CleanedStocks: []string{"中国平安|601318"}}},
		{TradingDay: "20260804", Event: NewsEvent{Title: "今日事件不应算历史", Datetime: "2026-08-04 09:30:00", Score: 0.9, Level: "个股", Direction: "利好", CleanedStocks: []string{"卧龙电驱|600580"}}},
	}
	var sb strings.Builder
	for _, e := range entries {
		raw, _ := json.Marshal(e)
		sb.Write(raw)
		sb.WriteString("\n")
	}
	// 坏行容忍（崩溃残行不拖垮检索）
	sb.WriteString("{trunc...\n")
	if err := os.WriteFile(a.historyPath, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	hits := a.SearchEventHistory([]string{"600580"}, "20260804", 5)
	if len(hits) != 2 {
		t.Fatalf("应命中 2 条（去重后，排除今日与它股）: %+v", hits)
	}
	if hits[0].Event.Title != "股东减持" || hits[1].Event.Title != "中标大单" {
		t.Fatalf("排序应 |score| 降序: %q %q", hits[0].Event.Title, hits[1].Event.Title)
	}
	if hits[1].Day != "20260728" {
		t.Fatalf("日期标记不符: %s", hits[1].Day)
	}
	// 无查询代码/空档
	if len(a.SearchEventHistory(nil, "20260804", 5)) != 0 {
		t.Fatal("空代码集应无命中")
	}
}

// TestHistoryPathWiring New() 必须把归档落在 dataDir 下的约定文件名。
func TestHistoryPathWiring(t *testing.T) {
	a := newHistoryAgent(t)
	if filepath.Base(a.historyPath) != "news_event_history.jsonl" {
		t.Fatalf("归档文件名口径不符: %s", a.historyPath)
	}
	if filepath.Dir(a.historyPath) != filepath.Dir(a.newsDBPath) {
		t.Fatal("归档应与 news_events.json 同目录")
	}
}
