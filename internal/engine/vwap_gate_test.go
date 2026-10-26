// vwap_gate_test.go — §C2-VWAP-FAILCLOSED（2026-10-26）：
// VWAP 采集不到时，硬过滤开启必须 fail-closed（跳过并留痕），不能静默按无 VWAP 的价格下单；
// VWAP 采到时订单必须携带 market_vwap/vwap_source，供订单/成交读取面展示。
package engine

import (
	"math"
	"testing"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
)

func vwapTestSignal() combat_agent.Signal {
	return combat_agent.Signal{
		ID: "SIG-VWAP-1", Code: "603790", Name: "雅运股份",
		Strategy: "因子战法#118", StrategyID: "fac_118", Direction: "做多", Price: 24.31,
	}
}

func hardVWAPCfg(threshold float64, shadow bool) func(*config.QMTConfig) {
	return func(c *config.QMTConfig) {
		c.MaxSignalVWAPPremiumPct = threshold
		c.MaxSignalVWAPPremiumShadow = &shadow
	}
}

func TestAutoPlaceVWAPMissingHardFilterSkips(t *testing.T) {
	e, _, _, orders := newQMTEngine(t, hardVWAPCfg(5, false))
	// 有价、但快照没有 Amount/Volume → VWAPOf=false；engine 也没有 coordinator/marketAPI 可补取。
	live := map[string]*data.StockInfo{"603790": {Code: "603790", Price: 24.31}}
	e.autoPlace(vwapTestSignal(), live)
	if len(*orders) != 0 {
		t.Fatalf("VWAP 缺失 + 硬过滤开启时必须跳过，got %d 单", len(*orders))
	}
}

func TestAutoPlaceVWAPThresholdZeroKeepsLegacy(t *testing.T) {
	e, _, _, orders := newQMTEngine(t, hardVWAPCfg(0, false))
	live := map[string]*data.StockInfo{"603790": {Code: "603790", Price: 24.31}}
	e.autoPlace(vwapTestSignal(), live)
	if len(*orders) != 1 {
		t.Fatalf("阈值=0（闸关）时应保持旧行为下单，got %d 单", len(*orders))
	}
}

func TestAutoPlaceVWAPPresentCarriesFields(t *testing.T) {
	e, _, _, orders := newQMTEngine(t, hardVWAPCfg(5, false))
	// VWAP = 12155/500 = 24.31，与信号价相同 → 不触发硬过滤。
	live := map[string]*data.StockInfo{"603790": {Code: "603790", Price: 24.31, Amount: 12155, Volume: 500}}
	e.autoPlace(vwapTestSignal(), live)
	if len(*orders) != 1 {
		t.Fatalf("VWAP 在场且未超阈值应下单，got %d 单", len(*orders))
	}
	req := (*orders)[0]
	mv, _ := req["market_vwap"].(float64)
	if math.Abs(mv-24.31) > 1e-6 {
		t.Fatalf("订单应携带 market_vwap=24.31，got %v (%T)", req["market_vwap"], req["market_vwap"])
	}
	if src, _ := req["vwap_source"].(string); src != "quote" {
		t.Fatalf("订单应携带 vwap_source=quote，got %q", src)
	}
}

func TestAutoPlaceVWAPPremiumOverThresholdSkips(t *testing.T) {
	e, _, _, orders := newQMTEngine(t, hardVWAPCfg(5, false))
	// VWAP = 12155/1000 = 12.155；信号价 24.31 → 溢价 100% > 5% → 硬拦。
	live := map[string]*data.StockInfo{"603790": {Code: "603790", Price: 24.31, Amount: 12155, Volume: 1000}}
	e.autoPlace(vwapTestSignal(), live)
	if len(*orders) != 0 {
		t.Fatalf("溢价超阈值时必须跳过，got %d 单", len(*orders))
	}
}

func TestAutoPlaceVWAPPremiumShadowProceeds(t *testing.T) {
	e, _, _, orders := newQMTEngine(t, hardVWAPCfg(5, true))
	live := map[string]*data.StockInfo{"603790": {Code: "603790", Price: 24.31, Amount: 12155, Volume: 1000}}
	e.autoPlace(vwapTestSignal(), live)
	if len(*orders) != 1 {
		t.Fatalf("影子模式命中只记录、不拦截，got %d 单", len(*orders))
	}
}
