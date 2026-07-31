package engine

import (
	"math"
	"testing"
	"time"

	"quant-trading-v2/internal/newsagent"
)

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

func TestApplyEventDecay(t *testing.T) {
	e := &Engine{sectorEventTimes: make(map[string]time.Time)}
	events := []newsagent.NewsEvent{
		{Title: "半导体利好", Sectors: []string{"半导体"}, Direction: "利好", Score: 0.8},
	}
	// 首次：无衰减
	e.applyEventDecay(events)
	if math.Abs(events[0].Score-0.8) > 1e-9 {
		t.Fatalf("首次不应衰减: %v", events[0].Score)
	}
	// 4 小时后同板块同方向：score *= 0.5
	e.sectorEventTimes["半导体|利好"] = time.Now().Add(-4 * time.Hour)
	e.applyEventDecay(events)
	want := 0.8 * 0.5
	if math.Abs(events[0].Score-want) > 1e-9 {
		t.Errorf("4h后 score = %v, want %v", events[0].Score, want)
	}
	// 个股事件不衰减
	stockEv := []newsagent.NewsEvent{{Title: "个股利好", Level: "个股", Score: 0.8}}
	e.sectorEventTimes["股|利好"] = time.Now().Add(-4 * time.Hour)
	e.applyEventDecay(stockEv)
	if math.Abs(stockEv[0].Score-0.8) > 1e-9 {
		t.Errorf("个股事件不应衰减: %v", stockEv[0].Score)
	}
}

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
