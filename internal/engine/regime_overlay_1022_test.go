// regime_overlay_1022_test.go — §A1 Regime 动态字段覆盖锁。
//
// 反证靶心：3 秒缓存命中时，主循环刚更新的情绪/风险档不得被缓存里的旧值盖回去。
// 同时要求静态昂贵读数（指数涨跌幅）仍来自缓存，不被覆盖逻辑误改。
//
// English: §A1 locks — within the 3s cache window the freshly updated dynamic fields
// (emotion/risk tier/breadth/reasons) must win, while expensive static readings stay cached.
package engine

import (
	"testing"
	"time"

	"quant-trading-v2/internal/signalctl"
)

func TestOverlayDynamicRegimeRefreshesWithinCacheWindow(t *testing.T) {
	e := &Engine{
		lastRegime: signalctl.Regime{
			AsOf:           time.Now(),
			IndexChangePct: 1.23, // 静态/昂贵读数：必须保留
			MarketState:    "bull",
		},
		lastEmotionPhase: "启动",
		lastMarketState:  "bull",
		lastRiskTier:     "Yellow",
		lastUpRatio:      0.55,
		lastMaxPosPct:    0.50,
		lastRiskReasons:  []string{"旧原因"},
	}
	r := e.regimeForAccount("u")
	if r.IndexChangePct != 1.23 {
		t.Fatalf("静态指数读数应保留, got %v", r.IndexChangePct)
	}
	if r.EmotionPhase != "启动" || r.RiskTier != "Yellow" || r.BreadthUpRatio != 0.55 || r.MaxPosPct != 0.50 {
		t.Fatalf("首次覆盖值不符: %+v", r)
	}

	// 主循环更新动态字段（3 秒缓存尚未过期）。
	e.mu.Lock()
	e.lastEmotionPhase = "退潮"
	e.lastMarketState = "bear"
	e.lastRiskTier = "Red"
	e.lastUpRatio = 0.20
	e.lastMaxPosPct = 0.20
	e.lastRiskReasons = []string{"新原因"}
	e.mu.Unlock()

	r2 := e.regimeForAccount("u")
	if r2.EmotionPhase != "退潮" || r2.MarketState != "bear" || r2.RiskTier != "Red" {
		t.Fatalf("3 秒窗口内应返回最新情绪/状态/风险档, got %+v", r2)
	}
	if r2.BreadthUpRatio != 0.20 || r2.MaxPosPct != 0.20 {
		t.Fatalf("广度/仓位档未刷新: %+v", r2)
	}
	if len(r2.RiskReasons) != 1 || r2.RiskReasons[0] != "新原因" {
		t.Fatalf("风险原因应替换为最新值, got %v", r2.RiskReasons)
	}
	if r2.IndexChangePct != 1.23 {
		t.Fatalf("覆盖动态字段时改动了静态指数读数, got %v", r2.IndexChangePct)
	}
}
