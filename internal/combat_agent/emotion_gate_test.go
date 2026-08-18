// C5 情绪周期过滤扩展到四战法测试：禁止开仓阶段下买入降级为 watch，允许阶段保持买入。
package combat_agent

import (
	"testing"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/strategy"
	"quant-trading-v2/internal/strategy_engine"
)

// TestEmotionGateBlocksAllStrategies 情绪"衰退"阶段下四战法 buy 一律降级为 watch。
func TestEmotionGateBlocksAllStrategies(t *testing.T) {
	cfg := config.NewManager("")
	a := New(cfg.GetStrategyConfig())
	a.SetEmotionBlockPhases([]string{"衰退"})
	a.SetRunners([]StrategyRunner{{Type: strategy.SignalDragon, Strategy: &fakeAlwaysPass{}}})

	pool := map[string]*strategy_engine.StockMarketData{"600100": d1BoostDragonMD()}
	_, sigs := a.ScorePool([]string{"600100"}, pool, map[string]D1Score{}, "衰退")
	for _, s := range sigs {
		if s.Code != "600100" {
			continue
		}
		if s.Action == "buy" {
			t.Fatalf("衰退期不应产出 buy, got %+v", s)
		}
		if s.Action != "watch" {
			t.Fatalf("衰退期应降级为 watch, got %+v", s)
		}
	}
}

// TestEmotionGateDefaultOnlyRecession 默认配置（未注入）仅"衰退"禁止开仓，其他阶段放行。
func TestEmotionGateDefaultOnlyRecession(t *testing.T) {
	cfg := config.NewManager("")
	a := New(cfg.GetStrategyConfig())
	a.SetRunners([]StrategyRunner{{Type: strategy.SignalDragon, Strategy: &fakeAlwaysPass{}}})

	pool := map[string]*strategy_engine.StockMarketData{"600100": d1BoostDragonMD()}

	// 未注入阶段列表 + 允许阶段（高潮）→ buy 保持
	_, sigsClimax := a.ScorePool([]string{"600100"}, pool, map[string]D1Score{}, "高潮")
	if !hasDragonAction(sigsClimax, "600100", "buy") {
		t.Fatalf("高潮期应保持 buy, got %+v", sigsClimax)
	}

	// 空阶段列表 → 默认"衰退"仍拦截
	_, sigsRecession := a.ScorePool([]string{"600100"}, pool, map[string]D1Score{}, "衰退")
	if hasDragonAction(sigsRecession, "600100", "buy") {
		t.Fatalf("默认衰退期应拦截 buy, got %+v", sigsRecession)
	}
}

// TestEmotionGateCustomPhases 自定义禁止阶段列表生效（如"冰点"）且未列入的阶段放行。
func TestEmotionGateCustomPhases(t *testing.T) {
	cfg := config.NewManager("")
	a := New(cfg.GetStrategyConfig())
	a.SetEmotionBlockPhases([]string{"冰点"})
	a.SetRunners([]StrategyRunner{{Type: strategy.SignalDragon, Strategy: &fakeAlwaysPass{}}})

	pool := map[string]*strategy_engine.StockMarketData{"600100": d1BoostDragonMD()}

	if _, sigs := a.ScorePool([]string{"600100"}, pool, map[string]D1Score{}, "冰点"); hasDragonAction(sigs, "600100", "buy") {
		t.Fatalf("自定义冰点期应拦截 buy, got %+v", sigs)
	}
	if _, sigs := a.ScorePool([]string{"600100"}, pool, map[string]D1Score{}, "衰退"); !hasDragonAction(sigs, "600100", "buy") {
		t.Fatalf("未列入自定义列表的衰退期应放行 buy, got %+v", sigs)
	}
}