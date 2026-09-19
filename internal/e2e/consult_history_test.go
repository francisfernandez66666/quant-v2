// consult_history_test.go — §ENH-3(20260919 批B) AI 顾问历史事件注入的端到端用例：
// 归档 JSONL 里早于本交易日、命中本轮个股代码的事件必须进入 ⟦DATA⟧ 上下文，
// 且当日事件不得混入历史段（rig 引擎时钟固定=2026-08-04 交易日）。
package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/newsagent"
)

// writeConsultHistory 往 rig 数据目录写 news_event_history.jsonl（与 newsagent 归档口径一致的 JSON 行）。
func writeConsultHistory(t *testing.T, rig *testRig, entries []struct {
	Day   string
	Event newsagent.NewsEvent
}) {
	t.Helper()
	var sb strings.Builder
	for _, e := range entries {
		raw, err := json.Marshal(map[string]any{"trading_day": e.Day, "event": e.Event})
		if err != nil {
			t.Fatal(err)
		}
		sb.Write(raw)
		sb.WriteString("\n")
	}
	path := filepath.Join(rig.tmp, "news_event_history.jsonl")
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestConsultHistoryEventsInjected(t *testing.T) {
	data.DisableAll = true
	defer func() { data.DisableAll = false }()

	rig := newTestEngine(t, loadTodayFixture(t))
	writeConsultHistory(t, rig, []struct {
		Day   string
		Event newsagent.NewsEvent
	}{
		{"20260728", newsagent.NewsEvent{Title: "卧龙电驱中标机器人电机大单", Datetime: "2026-07-28 10:00:00", Source: "财联社", Level: "个股", Direction: "利好", Score: 0.62, CleanedStocks: []string{"卧龙电驱|600580"}}},
		{"20260804", newsagent.NewsEvent{Title: "当日事件不得进历史段", Datetime: "2026-08-04 09:40:00", Source: "新浪", Level: "个股", Direction: "利空", Score: -0.7, CleanedStocks: []string{"卧龙电驱|600580"}}},
		{"20260701", newsagent.NewsEvent{Title: "无关股票旧事件", Datetime: "2026-07-01 09:40:00", Source: "新浪", Level: "个股", Direction: "利好", Score: 0.9, CleanedStocks: []string{"贵州茅台|600519"}}},
	})

	ctx := todayConsult(t, rig, "卧龙电驱(600580) 上次也放量冲高回落，后来怎么走的？", true)
	if !strings.Contains(ctx, "历史同类事件") {
		t.Fatalf("上下文应含历史同类事件段\n---context---\n%s", ctx)
	}
	if !strings.Contains(ctx, "2026-07-28") || !strings.Contains(ctx, "卧龙电驱中标机器人电机大单") {
		t.Fatalf("历史事件（日期/标题）应注入\n---context---\n%s", ctx)
	}
	if strings.Contains(ctx, "当日事件不得进历史段") {
		t.Fatal("本交易日事件不得混入历史段（会与实时数据口径混淆）")
	}
	if strings.Contains(ctx, "无关股票旧事件") {
		t.Fatal("未命中本轮代码的历史事件不应注入")
	}
	// 头部要求必须同时声明历史段的引用纪律（防模型把往日数字说成今天）
	if !strings.Contains(ctx, "引用必须带发生日期") {
		t.Fatalf("【要求】应含历史事件引用纪律\n---context---\n%s", ctx)
	}
}

func TestConsultNoHistoryFileSilent(t *testing.T) {
	data.DisableAll = true
	defer func() { data.DisableAll = false }()

	// 冷启动（无归档文件）：咨询链路必须与增强前完全一致，不报错、不出现历史段
	// （注：【要求】行本身会提及"『历史同类事件』段"的引用纪律，故断言取段标题独有字串）。
	rig := newTestEngine(t, loadTodayFixture(t))
	ctx := todayConsult(t, rig, "卧龙电驱(600580) 今天怎么样？", true)
	if strings.Contains(ctx, "往日发生，引用必须带日期") {
		t.Fatalf("无归档时不应出现历史段\n---context---\n%s", ctx)
	}
}
