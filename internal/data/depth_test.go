package data

import (
	"math"
	"testing"
)

// 构造五档盘口：买一到买五价格递减、卖一到卖五递增，委托量可配置。
func testDepth(price float64) *OrderBook {
	ob := newOrderBook("600519", "贵州茅台")
	ob.Price = price
	ob.PrevClose = price
	for i := 0; i < 5; i++ {
		ob.Bids[i] = OrderLevel{Price: price - 0.01*float64(i+1), Volume: 100 + float64(i)}
		ob.Asks[i] = OrderLevel{Price: price + 0.01*float64(i+1), Volume: 200 + float64(i)}
	}
	return ob
}

func TestOrderBookPreallocatedTenLevels(t *testing.T) {
	ob := newOrderBook("600519", "贵州茅台")
	if len(ob.Bids) != DepthLevels || len(ob.Asks) != DepthLevels {
		t.Fatalf("盘口需预分配 %d 档（十档预留），got bids=%d asks=%d", DepthLevels, len(ob.Bids), len(ob.Asks))
	}
	// 填充前五档后，6~10 档为零值。
	for i := 5; i < DepthLevels; i++ {
		if ob.Bids[i].Price != 0 || ob.Asks[i].Price != 0 {
			t.Fatalf("第 %d 档应为零值（未填充），got %+v/%+v", i+1, ob.Bids[i], ob.Asks[i])
		}
	}
}

func TestOrderBookFactors(t *testing.T) {
	price := 10.0
	ob := testDepth(price)
	// 买盘：100+101+102+103+104=510；卖盘：200+201+202+203+204=1010
	f := ob.Factors(5)
	if math.Abs(f.BidVol-510) > 1e-6 {
		t.Errorf("买量应=510, got %.2f", f.BidVol)
	}
	if math.Abs(f.AskVol-1010) > 1e-6 {
		t.Errorf("卖量应=1010, got %.2f", f.AskVol)
	}
	wantRatio := float64(510-1010) / float64(510+1010)
	if math.Abs(f.BidAskRatio-wantRatio) > 1e-6 {
		t.Errorf("委比应=%.4f, got %.4f", wantRatio, f.BidAskRatio)
	}
	if math.Abs(f.SealBid-100) > 1e-6 || math.Abs(f.SealAsk-200) > 1e-6 {
		t.Errorf("封单量应为 100/200, got %.1f/%.1f", f.SealBid, f.SealAsk)
	}
	// 买一 9.99 卖一 10.01 → 价差 0.2%
	if math.Abs(f.SpreadPct-0.2) > 1e-6 {
		t.Errorf("价差百分比应=0.2, got %.4f", f.SpreadPct)
	}
	// 买五 9.95 卖五 10.05 → 覆盖 1%
	if math.Abs(f.NearPct-1.0) > 1e-6 {
		t.Errorf("覆盖范围应=1.0%%, got %.4f", f.NearPct)
	}
	// 默认档位数=5（levels 传 0）
	f0 := ob.Factors(0)
	if math.Abs(f0.BidVol-510) > 1e-6 {
		t.Errorf("默认档位数应为5, got bid_vol=%.2f", f0.BidVol)
	}
}

func TestOrderBookValidate(t *testing.T) {
	ob := testDepth(10.0)
	if err := ob.Validate(); err != nil {
		t.Fatalf("合法盘口不应报错: %v", err)
	}
	bad := newOrderBook("000001", "平安银行")
	bad.Bids[0] = OrderLevel{Price: 0, Volume: 0}
	if err := bad.Validate(); err == nil {
		t.Fatal("缺少买一价应报错")
	}
}

func TestExtractTencentTime(t *testing.T) {
	fields := make([]string, 31)
	fields[30] = "20260818104710"
	got := extractTencentTime(fields)
	if got != "10:47:10" {
		t.Errorf("时间解析应=10:47:10, got %q", got)
	}
	if extractTencentTime(nil) != "" {
		t.Error("短字段应返回空串")
	}
}

// TestDetectBigOrders 验证托单/压单识别：单档量占同侧五档总量 ≥30% 判大单，
// 买盘大单→托单(support)、卖盘大单→压单(resistance)。
func TestDetectBigOrders(t *testing.T) {
	ob := newOrderBook("600519", "贵州茅台")
	ob.Price = 10
	// 买盘：买一 500 手占 80%，其余均摊 → 买一是托单(strong)
	ob.Bids = []OrderLevel{
		{Price: 9.99, Volume: 500},
		{Price: 9.98, Volume: 50},
		{Price: 9.97, Volume: 40},
		{Price: 9.96, Volume: 30},
		{Price: 9.95, Volume: 20},
	}
	// 卖盘：卖一 500 手占 56% → 压单(strong)；卖三 300 手占 34% → 压单(weak)
	ob.Asks = []OrderLevel{
		{Price: 10.01, Volume: 500},
		{Price: 10.02, Volume: 20},
		{Price: 10.03, Volume: 300},
		{Price: 10.04, Volume: 30},
		{Price: 10.05, Volume: 40},
	}
	orders := ob.DetectBigOrders(BigOrderConfig{})
	// 买盘总量=640，卖盘总量=890
	support := filterByKind(orders, BigOrderSupport)
	resist := filterByKind(orders, BigOrderResistance)
	if len(support) != 1 {
		t.Fatalf("应识别 1 个托单, got %d: %+v", len(support), support)
	}
	if support[0].Level != 1 || support[0].Volume != 500 || support[0].Strength != "strong" {
		t.Errorf("托单应为买一500手 strong, got %+v", support[0])
	}
	if math.Abs(support[0].SharePct-500.0/640.0) > 1e-6 {
		t.Errorf("托单占比应=500/640, got %.4f", support[0].SharePct)
	}
	if len(resist) != 2 {
		t.Fatalf("应识别 2 个压单, got %d: %+v", len(resist), resist)
	}
	if resist[0].Level != 1 || resist[0].Strength != "strong" {
		t.Errorf("压单应为卖一500手 strong, got %+v", resist[0])
	}
	if resist[1].Level != 3 || resist[1].Strength != "weak" {
		t.Errorf("压单应为卖三300手 weak, got %+v", resist[1])
	}
	// 空盘口 → 无大单
	if n := newOrderBook("000001", "平安银行").DetectBigOrders(BigOrderConfig{}); len(n) != 0 {
		t.Errorf("空盘口不应有大单, got %+v", n)
	}
	// 默认阈值=0.3；提高阈值到 0.7 后 500/640=0.78 仍命中，其余排除
	strict := ob.DetectBigOrders(BigOrderConfig{MinSharePct: 0.7})
	if len(strict) != 1 || strict[0].Level != 1 {
		t.Errorf("阈值0.7应只剩买一托单, got %+v", strict)
	}
}

func filterByKind(orders []BigOrder, kind BigOrderKind) []BigOrder {
	var out []BigOrder
	for _, o := range orders {
		if o.Kind == kind {
			out = append(out, o)
		}
	}
	return out
}