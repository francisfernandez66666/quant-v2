// flow_money_readings_test.go — §FLOW-MONEY 资金流腿装配接线锁（2026-11-19 云端实现）。
// 四条：① 大盘 fflow+成交额 → delta 写库（UpsertFlowMoneyOnly，不盖量比列）+ 板块 map 缓存；
// ② fflow 行日期非今日 → 弃权不写库；③ 板块名 → sectorDeltaFor 按行业查当日缓存；
// ④ 跨日缓存作废；⑤ 无 marketAPI 安全跳过。
package engine

import (
	"path/filepath"
	"testing"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/store"
)

func newFlowMoneyTestEngine(t *testing.T) *Engine {
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

func TestFillFlowMoneyWritesBothLegs(t *testing.T) {
	e := newFlowMoneyTestEngine(t)
	// sectorDeltaFor 内部按真实 time.Now() 判跨日，故桩日期必须与真实北京今天一致。
	nb := time.Now().In(cntime.Loc)
	today := nb.Format("2006-01-02")
	now := time.Date(nb.Year(), nb.Month(), nb.Day(), 10, 30, 0, 0, cntime.Loc)
	// 大盘桩：净 -1.2 亿 / 成交额 4000 亿 ≈ -0.03% → delta ≈ -0.01（轻微流出）。
	e.indexMoneyFlowFn = func() (*data.FlowDay, error) {
		return &data.FlowDay{Date: today + " 10:30", MainNet: -1.2e8}, nil
	}
	e.indexAmountSeriesFn = func(n int, today string) ([]data.IndexAmountDay, string, error) {
		return []data.IndexAmountDay{
			{TradeDate: "2026-11-18", Amount: 3800e8},
			{TradeDate: today, Amount: 4000e8},
		}, "em:000001.SH", nil
	}
	e.sectorListFn = func() ([]data.SectorInfo, error) {
		return []data.SectorInfo{
			{Name: "航空机场", NetInflow: 3e6, Turnover: 1e9},  // 0.3% → +0.1
			{Name: "公用事业", NetInflow: -3e8, Turnover: 1e9}, // -30% → -1（夹取）
			{Name: "坏板块", NetInflow: 1e6, Turnover: 0},     // 无成交 → 弃权
		}, nil
	}

	e.fillFlowMoney(now)

	// 大盘腿落库（资金流四列，source=eastmoney_fflow）。
	row, err := e.d1Store.GetFlowFactorDaily(today)
	if err != nil || row == nil || row.FlowMarketDelta == nil {
		t.Fatalf("大盘腿应写库, row=%+v err=%v", row, err)
	}
	if *row.FlowMarketDelta > -0.005 || *row.FlowMarketDelta < -0.02 {
		t.Fatalf("大盘 delta 应≈-0.01, got %v", *row.FlowMarketDelta)
	}
	if row.Source != "eastmoney_fflow" {
		t.Fatalf("source 应如实标记, got %q", row.Source)
	}
	// 板块 map 缓存：有效板块在、坏板块不在。
	e.flowMoneyMu.RLock()
	m, day := e.sectorDeltaMap, e.sectorDeltaDay
	e.flowMoneyMu.RUnlock()
	if day != today || m["航空机场"] <= 0 || m["公用事业"] != -1 || len(m) != 2 {
		t.Fatalf("板块缓存应只含 2 个有效板块, day=%s map=%+v", day, m)
	}
	// 逐票注入：按行业名查 delta（行业解析桩返回"航空机场"）。
	e.industryFn = func(code string) string { return "航空机场" }
	if d, ok := e.sectorDeltaFor("600000.SH"); !ok || d <= 0 {
		t.Fatalf("航空机场板块票应得正 delta, got %v ok=%v", d, ok)
	}
	// 板块表留痕。
	srow, err := e.d1Store.GetSectorFlowDaily(today, "航空机场")
	if err != nil || srow == nil || srow.Delta <= 0 {
		t.Fatalf("板块应留痕, got %+v err=%v", srow, err)
	}
}

func TestFillFlowMoneyStaleIndexAbstains(t *testing.T) {
	e := newFlowMoneyTestEngine(t)
	now := time.Date(2026, 11, 20, 10, 30, 0, 0, cntime.Loc)
	// fflow 行日期是昨日（盘前/断更形态）→ 弃权不写库。
	e.indexMoneyFlowFn = func() (*data.FlowDay, error) {
		return &data.FlowDay{Date: "2026-11-19 15:00", MainNet: -1e8}, nil
	}
	e.sectorListFn = func() ([]data.SectorInfo, error) { return nil, nil }
	e.fillFlowMoney(now)
	if row, _ := e.d1Store.GetFlowFactorDaily("2026-11-20"); row != nil && row.FlowMarketDelta != nil {
		t.Fatalf("断更 fflow 应弃权不写库, got %+v", row)
	}
	e.flowMoneyMu.RLock()
	m := e.sectorDeltaMap
	e.flowMoneyMu.RUnlock()
	if len(m) != 0 {
		t.Fatalf("无板块数据应空缓存, got %+v", m)
	}
}

func TestFillFlowMoneyNilMarketSafe(t *testing.T) {
	e := &Engine{} // 无 marketAPI：安全跳过
	e.fillFlowMoney(time.Now())
	if e.sectorDeltaMap != nil {
		t.Fatal("无行情源应保持空缓存")
	}
}
