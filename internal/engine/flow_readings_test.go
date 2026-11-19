// flow_readings_test.go — §FLOW-FACTOR-DAILY 量比腿盘中接线锁（方案 B 2026-11-19）。
// 五条：① 指数日 K 注入 → 盘中折算算出放量 +1 并只写量比列（不碰资金流列）；
// ② 30s TTL 内不重取（当日累计量在变但缓存命中）；③ 日 K 断更（末根非今日）→ 弃权；
// ④ 盘前（折算会放大 240 倍）→ 弃权；⑤ 无取数源 → 安全跳过（不 panic、Regime 保持零值）。
package engine

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/signalctl"
	"quant-trading-v2/internal/store"
)

func newFlowTestEngine(t *testing.T) *Engine {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "trading.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	e := &Engine{}
	e.SetD1Store(db)
	return e
}

// indexSeries 构造指数日 K 序列：6 根，末尾=lastDate 累计 lastAmt，前 5 根完整日各 100 亿。
func indexSeries(lastDate string, lastAmt float64) []data.IndexAmountDay {
	return []data.IndexAmountDay{
		{TradeDate: "2026-11-13", Amount: 100e8},
		{TradeDate: "2026-11-16", Amount: 100e8},
		{TradeDate: "2026-11-17", Amount: 100e8},
		{TradeDate: "2026-11-18", Amount: 100e8},
		{TradeDate: "2026-11-19", Amount: 100e8},
		{TradeDate: lastDate, Amount: lastAmt},
	}
}

// 北京 11-20 14:00：elapsed=120+60=180 → 300e8×240/180=400e8；r_yest=r_ma5=4 → 放量。
func flowTestNow() time.Time { return time.Date(2026, 11, 20, 14, 0, 0, 0, cntime.Loc) }

func TestFillFlowReadingsIndexIntraday(t *testing.T) {
	e := newFlowTestEngine(t)
	e.indexAmountSeriesFn = func(n int, today string) ([]data.IndexAmountDay, string, error) {
		return indexSeries("2026-11-20", 300e8), "em:000001.SH", nil
	}
	r := signalctl.Regime{}
	e.fillFlowReadings(&r, flowTestNow())

	if r.FlowDay != "2026-11-20" {
		t.Fatalf("FlowDay 应为当日, got %q", r.FlowDay)
	}
	if r.RYest < 3.99 || r.RMa5 < 3.99 {
		t.Fatalf("盘中折算应得 ~4/4, got rYest=%v rMa5=%v", r.RYest, r.RMa5)
	}
	if r.VolSurgeDelta != 1.0 {
		t.Fatalf("放量应 +1, got %v", r.VolSurgeDelta)
	}
	// 只写量比列：source 带实际源标签，资金流四列仍 NULL。
	row, err := e.d1Store.GetFlowFactorDaily("2026-11-20")
	if err != nil || row == nil || !strings.HasPrefix(row.Source, "index_em:000001.SH") {
		t.Fatalf("量比应写库, row=%+v err=%v", row, err)
	}
	if row.FlowMarketDelta != nil || row.MarketFlowPct != nil {
		t.Fatalf("量比写库不得碰资金流列, got %+v", row)
	}
}

func TestFillFlowReadingsTTL(t *testing.T) {
	e := newFlowTestEngine(t)
	calls := 0
	e.indexAmountSeriesFn = func(n int, today string) ([]data.IndexAmountDay, string, error) {
		calls++
		return indexSeries("2026-11-20", 300e8), "em:000001.SH", nil
	}
	r1 := signalctl.Regime{}
	e.fillFlowReadings(&r1, flowTestNow())
	r2 := signalctl.Regime{}
	e.fillFlowReadings(&r2, flowTestNow().Add(10*time.Second)) // TTL 内
	if calls != 1 {
		t.Fatalf("30s TTL 内应只取数 1 次, got %d", calls)
	}
	if r2.RYest != r1.RYest || r2.VolSurgeDelta != r1.VolSurgeDelta {
		t.Fatalf("TTL 内读数应一致: %+v vs %+v", r1, r2)
	}
	// TTL 过后重取。
	r3 := signalctl.Regime{}
	e.fillFlowReadings(&r3, flowTestNow().Add(31*time.Second))
	if calls != 2 {
		t.Fatalf("TTL 过后应重取, got %d", calls)
	}
}

func TestFillFlowReadingsStaleIndexAbstain(t *testing.T) {
	e := newFlowTestEngine(t)
	// 日 K 末根停在 11-18（断更），今日 11-20 → 应弃权。
	e.indexAmountSeriesFn = func(n int, today string) ([]data.IndexAmountDay, string, error) {
		return indexSeries("2026-11-18", 300e8), "em:000001.SH", nil
	}
	r := signalctl.Regime{}
	e.fillFlowReadings(&r, flowTestNow())
	if r.RYest > 0 || r.VolSurgeDelta == 1.0 {
		t.Fatalf("断更应弃权（量比不激活）, got rYest=%v delta=%v", r.RYest, r.VolSurgeDelta)
	}
}

func TestFillFlowReadingsPreOpenAbstain(t *testing.T) {
	e := newFlowTestEngine(t)
	e.indexAmountSeriesFn = func(n int, today string) ([]data.IndexAmountDay, string, error) {
		return indexSeries("2026-11-20", 300e8), "em:000001.SH", nil
	}
	preOpen := time.Date(2026, 11, 20, 9, 20, 0, 0, cntime.Loc) // 盘前 elapsed=0
	r := signalctl.Regime{}
	e.fillFlowReadings(&r, preOpen)
	if r.RYest > 0 || r.VolSurgeDelta != 0 {
		t.Fatalf("盘前应弃权（不折算放大 240x）, got rYest=%v delta=%v", r.RYest, r.VolSurgeDelta)
	}
}

func TestFillFlowReadingsNoSourceSafe(t *testing.T) {
	e := &Engine{} // 无 marketAPI、无注入源：安全跳过
	r := signalctl.Regime{}
	e.fillFlowReadings(&r, time.Now())
	if r.FlowDay != "" || r.RYest != 0 {
		t.Fatalf("无源应保持弃权零值, got %+v", r)
	}
}
