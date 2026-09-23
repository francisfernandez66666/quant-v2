// day_bars_lookup_test.go —— §KLINE-CHAIN-3 库内日K兜底腿【提供方】的单测。
//
// 锁的是"提供方只负责取数与形状"这一半契约：读出来必须是**后复权价**、**量纲为手**、
// 按日期升序、并截断到请求的根数；归一与新鲜度守卫在 strategy_engine（那边另有用例）。
// 这三条一旦在本包"顺手"做了，两处各写一遍就会改一处漏一处。
// （Provider contract: hfq prices, volume in lots, ascending, trimmed to count — normalization and
// freshness belong to strategy_engine.）
package engine

import (
	"math"
	"path/filepath"
	"testing"
	"time"

	"quant-trading-v2/internal/store"
)

// openDayBarsDB 建一个只装了日线与复权因子的研究库，写入 5 行 close=10、vol=100（手）的行情，
// 并在最后一个交易日之前落一个 2.0 的因子事件（后复权价即实际价的 2 倍）。
func openDayBarsDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "trading.db"))
	if err != nil {
		t.Fatalf("open research db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// 日期轴与生产同构（连续交易日）：兜底链的「丢当日行」「昨收锚」判据都依赖倒数第二行，
	// 日期散成不连续区间会让守卫误判成通过。
	dates := dayBarsTestDates()
	rows := make([]map[string]any, 0, len(dates))
	for _, d := range dates {
		rows = append(rows, map[string]any{
			"ts_code": "600519.SH", "trade_date": d,
			"open": 10.0, "high": 11.0, "low": 9.0, "close": 10.0, "vol": 100.0, "amount": 1000.0,
		})
	}
	if _, err := db.InsertRows("daily", store.TableColumns("daily"), rows); err != nil {
		t.Fatalf("InsertRows daily: %v", err)
	}
	if _, err := db.InsertRows("adj_factor", store.TableColumns("adj_factor"), []map[string]any{
		{"ts_code": "600519.SH", "trade_date": dates[0], "adj_factor": 2.0},
	}); err != nil {
		t.Fatalf("InsertRows adj_factor: %v", err)
	}
	return db
}

// dayBarsTestDates 最近 5 个日历日（升序，均早于今天，避免把周末当成今日 bar）。
func dayBarsTestDates() []string {
	out := make([]string, 0, 5)
	for i := 5; i >= 1; i-- {
		out = append(out, time.Now().AddDate(0, 0, -i).Format("20060102"))
	}
	return out
}

// TestDayBarsLookupShape 取数形状：6 位代码归一到 ts_code、后复权价透传（不在这层归一）、
// 成交量保持库内的**手**口径（×100 由 strategy_engine 做）、升序。
func TestDayBarsLookupShape(t *testing.T) {
	l := NewDayBarsLookup(openDayBarsDB(t))

	bars, ok := l.Lookup("600519", 120)
	if !ok || len(bars) != 5 {
		t.Fatalf("应取到 5 根库内日K, got ok=%v n=%d", ok, len(bars))
	}
	last := bars[len(bars)-1]
	if math.Abs(last.Close-20) > 1e-9 {
		t.Errorf("库内价必须是后复权原值（close 10 × 因子 2）, got %.4f", last.Close)
	}
	if math.Abs(last.Open-20) > 1e-9 || math.Abs(last.High-22) > 1e-9 || math.Abs(last.Low-18) > 1e-9 {
		t.Errorf("OHLC 应整体按因子等比（未归一口径透传）: %+v", last)
	}
	if last.Volume != 100 {
		t.Errorf("量纲要保持库内的手口径（换算在 strategy_engine）, got %v", last.Volume)
	}
	for i := 1; i < len(bars); i++ {
		if !bars[i-1].Date.Before(bars[i].Date) {
			t.Fatalf("序列必须升序，否则末根日期/新鲜度守卫全失效")
		}
	}
}

// TestDayBarsLookupTrimsToCount count 只取最近 N 根：打分要 120 根，取数窗口给的是 500 天日历区间，
// 尾部截断必须在提供方做掉，免得把半年的历史带进 MA。
func TestDayBarsLookupTrimsToCount(t *testing.T) {
	l := NewDayBarsLookup(openDayBarsDB(t))

	bars, ok := l.Lookup("600519.SH", 2)
	if !ok || len(bars) != 2 {
		t.Fatalf("count=2 应只留末两根, got ok=%v n=%d", ok, len(bars))
	}
	if math.Abs(bars[1].Close-20) > 1e-9 {
		t.Errorf("末根应是库里最新一天, got %+v", bars[1])
	}
}

// TestDayBarsLookupMissAndCache 库里没有这只票 ⇒ 返回 false（让链继续往下走而不是就地报错）；
// 有数据时结果进 TTL 缓存——把库关掉后仍能取到，证明确实没有每轮重扫 SQLite。
func TestDayBarsLookupMissAndCache(t *testing.T) {
	db := openDayBarsDB(t)
	l := NewDayBarsLookup(db)

	if bars, ok := l.Lookup("000001", 120); ok || len(bars) != 0 {
		t.Fatalf("无此票应返回 false, got ok=%v n=%d", ok, len(bars))
	}
	if _, ok := l.Lookup("600519", 120); !ok {
		t.Fatalf("有数据应返回 true")
	}
	// 关掉句柄再取：命中缓存则不碰 DB（故障期 5s 循环不该把库扫描打爆）
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	if bars, ok := l.Lookup("600519", 120); !ok || len(bars) != 5 {
		t.Fatalf("第二次应命中缓存, got ok=%v n=%d", ok, len(bars))
	}
}

// TestDayBarsLookupNilDBDisablesLeg 研究库没接上（dbErr）时装配的读取器恒不可用，
// 日K链行为回到改造前，不会因为注入点存在而凭空多一条腿。
func TestDayBarsLookupNilDBDisablesLeg(t *testing.T) {
	if _, ok := NewDayBarsLookup(nil).Lookup("600519", 120); ok {
		t.Fatal("db=nil 必须报不可用")
	}
}
