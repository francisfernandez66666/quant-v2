// qmt_feed_test.go — §ENH-5 批E 回归：L1 tick 合并注入语义。
// 锁四条关键约束：命中才注入（防掩盖断流）、共享指针复制（防 -race）、
// tick 缺失字段保留（Name/资金流）、超龄/停牌 tick 丢弃。
package data

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeFeedSource QuoteFeedSource 测试替身。
type fakeFeedSource struct {
	ticks map[string]QMTTick
	err   error
	calls int
	got   [][]string
}

// Quotes 记录每轮请求代码并返回预置结果。
func (s *fakeFeedSource) Quotes(_ context.Context, codes []string) (map[string]QMTTick, error) {
	s.calls++
	s.got = append(s.got, codes)
	if s.err != nil {
		return nil, s.err
	}
	return s.ticks, nil
}

// seedBase 构造带基线快照的 Fetcher：600000 已有 Name/资金流，300750 不在快照（新命中）。
func seedBase(t *testing.T) *Fetcher {
	t.Helper()
	f := NewFetcher([]string{"600000", "300750"}, &MarketAPI{}, nil)
	f.IngestSnapshot(&MarketSnapshot{
		Stocks: map[string]*StockInfo{
			"600000": {Code: "600000", Name: "浦发银行", Price: 9.9, Sector: "银行",
				NetInflow: 1234.5, HasFlow: true},
		},
		Time:   time.Now().Add(-time.Minute),
		Source: "新浪",
	})
	return f
}

// freshTick 生成指定价格、tickTime=当前时刻的新鲜 QMT tick。
func freshTick(px float64) QMTTick {
	return QMTTick{LastPrice: px, Open: px - 0.1, High: px + 0.2, Low: px - 0.2,
		PrevClose: 10, Volume: 200000, Amount: 200000 * px,
		TickTime: time.Now().UnixMilli()}
}

// TestQMTFeedMergeInject 命中注入：Source=QMT-L1、tick 字段覆盖、缺失字段保留、新码入表。
func TestQMTFeedMergeInject(t *testing.T) {
	f := seedBase(t)
	src := &fakeFeedSource{ticks: map[string]QMTTick{
		"600000": freshTick(10.5),
		"300750": freshTick(200.5),
	}}
	feed := NewQMTFeed(f, src, time.Second, 30*time.Second, 1)
	if n := feed.applyTicks(src.ticks); n != 2 {
		t.Fatalf("应命中 2 只, got %d", n)
	}
	snap := f.Snapshot()
	if snap.Source != "QMT-L1" {
		t.Fatalf("注入后 Source 应为 QMT-L1, got %q", snap.Source)
	}
	got := snap.Stocks["600000"]
	if got == nil || got.Price != 10.5 || got.High != 10.7 || got.PrevClose != 10 {
		t.Fatalf("tick 字段应覆盖: %+v", got)
	}
	if got.ChangePct < 4.9 || got.ChangePct > 5.1 {
		t.Fatalf("涨跌幅应按昨收计算, got %v", got.ChangePct)
	}
	// tick 不携带的字段必须保留（资金流第二源不能被行情冲掉）
	if got.Name != "浦发银行" || got.Sector != "银行" || !got.HasFlow || got.NetInflow != 1234.5 {
		t.Fatalf("Name/Sector/资金流字段应保留: %+v", got)
	}
	if snap.Stocks["300750"] == nil || snap.Stocks["300750"].Price != 200.5 {
		t.Fatal("未监控于快照的新码应入表")
	}
}

// TestQMTFeedNoBlindInjection 无命中/失败/基线缺失时绝不注入——lastOK 不被伪造刷新。
func TestQMTFeedNoBlindInjection(t *testing.T) {
	f := seedBase(t)
	before := f.Staleness()
	cases := []struct {
		name string
		src  *fakeFeedSource
	}{
		{"源错误", &fakeFeedSource{err: errors.New("dial tcp: refused")}},
		{"空 ticks", &fakeFeedSource{ticks: map[string]QMTTick{}}},
		{"全停牌", &fakeFeedSource{ticks: map[string]QMTTick{"600000": {LastPrice: 0}}}},
		{"超龄tick", &fakeFeedSource{ticks: map[string]QMTTick{"600000": {
			LastPrice: 10, TickTime: time.Now().Add(-time.Hour).UnixMilli()}}}},
	}
	for _, tc := range cases {
		feed := NewQMTFeed(f, tc.src, time.Second, 30*time.Second, 1)
		feed.pollOnce()
		if f.Snapshot().Source != "新浪" {
			t.Fatalf("%s: 不应覆盖快照 Source", tc.name)
		}
		if f.Staleness() < before {
			t.Fatalf("%s: 注入会刷新 lastOK，禁止", tc.name)
		}
	}
}

// TestQMTFeedPointerIsolation 注入不得原地改动旧快照共享的 *StockInfo（§R3-2 P0-D2 浅拷贝约束）。
func TestQMTFeedPointerIsolation(t *testing.T) {
	f := seedBase(t)
	old := f.Snapshot().Stocks["600000"] // 与内部快照共享指针的旧对象
	feed := NewQMTFeed(f, &fakeFeedSource{}, time.Second, 30*time.Second, 1)
	feed.applyTicks(map[string]QMTTick{"600000": freshTick(11.1)})
	if old.Price != 9.9 {
		t.Fatalf("旧 StockInfo 被原地改写（并发竞态隐患）: %v", old.Price)
	}
	if f.Snapshot().Stocks["600000"].Price != 11.1 {
		t.Fatal("新快照应携带 tick 价")
	}
}

// TestQMTFeedVolumeUnit 换算系数生效：默认 1（股），SetVolumeToShares 后按系数放大。
func TestQMTFeedVolumeUnit(t *testing.T) {
	f := seedBase(t)
	feed := NewQMTFeed(f, &fakeFeedSource{}, time.Second, 30*time.Second, 1)
	feed.applyTicks(map[string]QMTTick{"600000": freshTick(10)})
	if v := f.Snapshot().Stocks["600000"].Volume; v != 200000 {
		t.Fatalf("默认系数应为 1, got %v", v)
	}
	f2 := seedBase(t)
	feed2 := NewQMTFeed(f2, &fakeFeedSource{}, time.Second, 30*time.Second, 0) // 0 → 回退 1
	feed2.SetVolumeToShares(100)
	tk := freshTick(10)
	feed2.applyTicks(map[string]QMTTick{"600000": tk})
	if v := f2.Snapshot().Stocks["600000"].Volume; v != tk.Volume*100 {
		t.Fatalf("手→股系数应生效, got %v", v)
	}
}

// TestQMTFeedPoolAndFirstLog 轮询取 WatchCodes 全池并把错误日志节流吞掉（不 panic）。
func TestQMTFeedPoolAndFirstLog(t *testing.T) {
	f := seedBase(t)
	src := &fakeFeedSource{err: errors.New("boom")}
	feed := NewQMTFeed(f, src, time.Second, 30*time.Second, 1)
	feed.pollOnce()
	feed.pollOnce() // 第二次应被 60s 节流吞掉
	if src.calls != 2 {
		t.Fatalf("每轮都应请求, got %d", src.calls)
	}
	if len(src.got[0]) != 2 {
		t.Fatalf("应携带全部监控码, got %v", src.got[0])
	}
	if got := f.WatchCodes(); len(got) != 2 {
		t.Fatalf("WatchCodes 应为 base+hot 去重池, got %v", got)
	}
}
