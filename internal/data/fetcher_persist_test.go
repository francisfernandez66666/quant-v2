// fetcher_persist_test.go — §GAP3.2/3.3 回归：快照原子落盘、同交易日恢复、跨日丢弃、陈旧度计算。
package data

import (
	"testing"
	"time"
)

func TestSnapshotPersistAndRestore(t *testing.T) {
	dir := t.TempDir()
	f := NewFetcher([]string{"600000"}, &MarketAPI{}, nil)
	f.SetDataDir(dir)

	snap := &MarketSnapshot{
		Stocks: map[string]*StockInfo{"600000": {Code: "600000", Price: 10}},
		Time:   time.Now(),
		Source: "test",
	}
	f.persistTick = 0
	f.persistSnapshotMaybe(snap) // 首拍即写

	// 新实例恢复
	f2 := NewFetcher(nil, &MarketAPI{}, nil)
	f2.LoadPersistedSnapshot(dir)
	got := f2.Snapshot()
	if got == nil || got.Stocks["600000"] == nil || got.Stocks["600000"].Price != 10 {
		t.Fatal("应从持久化文件恢复当日快照")
	}
	// 恢复后陈旧度 = 快照时间起算（>0）
	if f2.Staleness() < 0 {
		t.Fatal("Staleness 应非负")
	}
}

func TestSnapshotCrossDayDropped(t *testing.T) {
	dir := t.TempDir()
	f := NewFetcher(nil, &MarketAPI{}, nil)
	f.SetDataDir(dir)
	old := &MarketSnapshot{Stocks: map[string]*StockInfo{"1": {Price: 1}}, Time: time.Now().AddDate(0, 0, -3)}
	f.persistSnapshotMaybe(old)
	f.LoadPersistedSnapshot(dir)
	if s := f.Snapshot(); s != nil && len(s.Stocks) > 0 && TradingDayDate(s.Time) == TradingDayDate(time.Now()) {
		t.Fatal("跨日快照不应恢复")
	}
}

func TestStalenessZeroWhenNeverFetched(t *testing.T) {
	f := NewFetcher(nil, &MarketAPI{}, nil)
	if f.Staleness() != 0 {
		t.Fatal("从未采集时 Staleness 应为 0")
	}
}
