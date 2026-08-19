// 本文件：事件聚合相关单元测试——事件聚簇去重（clusterEvents）、重复事件时间衰减（applyEventDecay）
// 与字符串切片合并（mergeStr）的正确性验证。
// English: This file: unit tests for event aggregation — event cluster dedup (clusterEvents), time decay for repeated events (applyEventDecay),
// and correctness of string-slice merging (mergeStr).
package engine

import (
	"math"
	"testing"
	"time"

	"quant-trading-v2/internal/newsagent"
)

// TestClusterEvents 验证同板块同方向事件被合并为单条：Score 取 |score| 最大者，RelatedStocks 去重合并。
// English: TestClusterEvents verifies events in the same sector with the same direction are merged into one: Score takes the max |score|, RelatedStocks are dedup-merged.
func TestClusterEvents(t *testing.T) {
	events := []newsagent.NewsEvent{
		{Title: "半导体政策利好1", Sectors: []string{"半导体"}, Score: 0.6, Direction: "利好", RelatedStocks: []string{"600001"}},
		{Title: "半导体政策利好2", Sectors: []string{"半导体"}, Score: 0.8, Direction: "利好", RelatedStocks: []string{"600002"}},
		{Title: "白酒利空", Sectors: []string{"白酒"}, Score: -0.7, Direction: "利空"},
	}
	out := clusterEvents(events)
	if len(out) != 2 {
		t.Fatalf("clusterEvents = %d, want 2", len(out))
	}
	// 半导体两事件应合并为一条，Score 取最大 0.8
	// English: The two semiconductor events should merge into one, Score taking the max 0.8.
	foundSemi := false
	for _, ev := range out {
		if ev.Sectors[0] == "半导体" {
			foundSemi = true
			if math.Abs(ev.Score-0.8) > 1e-9 {
				t.Errorf("合并事件 score = %v, want 0.8", ev.Score)
			}
			if len(ev.RelatedStocks) != 2 {
				t.Errorf("合并事件个股 = %v, want 2 只", ev.RelatedStocks)
			}
		}
	}
	if !foundSemi {
		t.Error("半导体簇缺失")
	}
}

// TestClusterEventsSameSectorDiffDirection 同板块不同方向的事件不得合并：
// 对抗制裁型上游利好/下游利空拆分事件共享"光通信"板块时，方向相反必须各自独立保留。
// English: TestClusterEventsSameSectorDiffDirection events with the same sector but different directions must not merge:
// when counter-sanction style split events (upstream bullish / downstream bearish) share the "optical communications" sector, opposite directions must each be kept independently.
func TestClusterEventsSameSectorDiffDirection(t *testing.T) {
	events := []newsagent.NewsEvent{
		{Title: "上游利好", Sectors: []string{"光通信"}, Score: 0.75, Direction: "利好"},
		{Title: "下游利空", Sectors: []string{"光通信"}, Score: -0.5, Direction: "利空"},
	}
	out := clusterEvents(events)
	if len(out) != 2 {
		t.Fatalf("同板块不同方向事件不应合并: %d 条, want 2", len(out))
	}
	directions := map[string]bool{}
	for _, ev := range out {
		directions[ev.Direction] = true
	}
	if !directions["利好"] || !directions["利空"] {
		t.Fatalf("应同时保留利好与利空两个方向, 实际 %v", directions)
	}
}

// TestApplyEventDecay 验证重复事件衰减规则：首次不衰减；4 小时后同板块同方向事件 score ×0.5；
// 个股级事件不参与衰减。
// English: TestApplyEventDecay verifies the repeated-event decay rule: no decay on first occurrence; after 4 hours, same-sector same-direction events have score ×0.5;
// stock-level events do not participate in decay.
func TestApplyEventDecay(t *testing.T) {
	e := &Engine{sectorEventTimes: make(map[string]time.Time)}
	events := []newsagent.NewsEvent{
		{Title: "半导体利好", Sectors: []string{"半导体"}, Direction: "利好", Score: 0.8},
	}
	// 首次：无衰减
	// English: First occurrence: no decay.
	e.applyEventDecay(events)
	if math.Abs(events[0].Score-0.8) > 1e-9 {
		t.Fatalf("首次不应衰减: %v", events[0].Score)
	}
	// 4 小时后同板块同方向：score *= 0.5
	// English: Same sector and direction 4 hours later: score *= 0.5.
	e.sectorEventTimes["半导体|利好"] = time.Now().Add(-4 * time.Hour)
	e.applyEventDecay(events)
	want := 0.8 * 0.5
	if math.Abs(events[0].Score-want) > 1e-9 {
		t.Errorf("4h后 score = %v, want %v", events[0].Score, want)
	}
	// 个股事件不衰减
	// English: Stock-level events do not decay.
	stockEv := []newsagent.NewsEvent{{Title: "个股利好", Level: "个股", Score: 0.8}}
	e.sectorEventTimes["股|利好"] = time.Now().Add(-4 * time.Hour)
	e.applyEventDecay(stockEv)
	if math.Abs(stockEv[0].Score-0.8) > 1e-9 {
		t.Errorf("个股事件不应衰减: %v", stockEv[0].Score)
	}
}

// TestMergeStr 验证字符串切片合并：去重且保持首次出现顺序。
// English: TestMergeStr verifies string-slice merging: dedup and preserve first-occurrence order.
func TestMergeStr(t *testing.T) {
	got := mergeStr([]string{"600001", "600002"}, []string{"600002", "600003"})
	want := []string{"600001", "600002", "600003"}
	if len(got) != len(want) {
		t.Fatalf("mergeStr = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("mergeStr = %v, want %v", got, want)
		}
	}
}
