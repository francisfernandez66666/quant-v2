// streamlog_test.go — §WS-G 快照流录制/回放单测：Write/Load 往返 + IngestSnapshot 注入。
// English: §WS-G stream-log tests — Write/Load round-trip + IngestSnapshot injection.
package data

import (
	"path/filepath"
	"testing"
	"time"
)

// TestStreamLogRoundTrip 录制 → 读取 → 逐帧比对一致（含损坏行跳过）。
func TestStreamLogRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "quote_stream.jsonl")
	sl, err := NewStreamLog(p)
	if err != nil {
		t.Fatalf("NewStreamLog: %v", err)
	}
	now := time.Now()
	frames := []MarketSnapshot{
		{Stocks: map[string]*StockInfo{"600000": {Code: "600000", Price: 10}}, Time: now, Source: "sina"},
		{Stocks: map[string]*StockInfo{"600519": {Code: "600519", Price: 1500}, "000001": {Code: "000001", Price: 12}}, Time: now.Add(5 * time.Second), Source: "sina"},
	}
	for i := range frames {
		if err := sl.Write(&frames[i]); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	if err := sl.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	// 追加一行损坏数据，验证读取跳过
	f, _ := NewStreamLog(p)
	_ = f.Write(&MarketSnapshot{Stocks: nil, Time: now.Add(10 * time.Second)})
	f.Close()

	got, err := LoadStreamLog(p)
	if err != nil || len(got) != 3 {
		t.Fatalf("应读回 3 帧, got %d err=%v", len(got), err)
	}
	if got[0].Stocks["600000"].Price != 10 || got[1].Stocks["600519"].Price != 1500 {
		t.Fatalf("字段往返不一致: %+v", got)
	}
	if got[1].Stocks == nil {
		t.Fatalf("回放快照 Stocks 不应为 nil（应初始化为空 map）")
	}
}

// TestIngestSnapshot 回放注入：fetcher 快照可被录制流驱动（打分循环同输入）。
func TestIngestSnapshot(t *testing.T) {
	f := NewFetcher(nil, nil, nil)
	snapped := 0
	f.SetSnapshotSink(func(s *MarketSnapshot) { snapped++ })
	snap := &MarketSnapshot{
		Stocks: map[string]*StockInfo{"600000": {Code: "600000", Price: 10}},
		Time:   time.Now(), Source: "replay",
	}
	f.IngestSnapshot(snap)
	if snapped != 1 {
		t.Fatalf("sink 应被调用 1 次, got %d", snapped)
	}
	got := f.Snapshot()
	if got == nil || got.Stocks["600000"] == nil || got.Stocks["600000"].Price != 10 {
		t.Fatalf("IngestSnapshot 后快照应可读, got %+v", got)
	}
	if f.StalenessMs("600000") < 0 {
		t.Fatalf("注入后 StalenessMs 应 >=0, got %d", f.StalenessMs("600000"))
	}
}
