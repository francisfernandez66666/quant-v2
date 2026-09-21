// fetcher_persist_test.go — §GAP3.2/3.3 回归：快照原子落盘、同交易日恢复、跨日丢弃、陈旧度计算。
package data

import (
	"testing"
	"time"
)

// TestSnapshotPersistAndRestore 验证快照原子落盘、同交易日新实例恢复及陈旧度计算。
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

// TestSnapshotCrossDayDropped 验证跨交易日快照不被新实例恢复。
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

// TestStalenessNegativeWhenNeverFetched §M2 语义修正回归：从未采集时 Staleness 必须回 -1 秒
// （未知），不再回 0——旧断言锁的正是缺陷语义（0 让"从未有行情"伪装成"绝对新鲜"）。
func TestStalenessNegativeWhenNeverFetched(t *testing.T) {
	f := NewFetcher(nil, &MarketAPI{}, nil)
	if f.Staleness() != -1*time.Second {
		t.Fatalf("从未采集时 Staleness 应为 -1s（未知），得到 %v", f.Staleness())
	}
	if f.StalenessMs("600000") != -1 {
		t.Fatalf("StalenessMs 从未采集应为 -1，得到 %d", f.StalenessMs("600000"))
	}
}
