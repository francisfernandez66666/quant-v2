// C4 ATR 动态止损测试：ATR 止损距离计算、双凸 ATR 硬止损、CheckPositionsExits 端到端激活。
package combat_agent

import (
	"path/filepath"
	"testing"
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/report"
	"quant-trading-v2/internal/strategy"
)

// TestATRStopPctUnit ATR 动态止损距离：启用时 ATR×mult/成本×100，否则回退固定百分比。
func TestATRStopPctUnit(t *testing.T) {
	// 未启用（mult=0）→ 回退固定百分比
	if got := (&strategy.ExitContext{CostPrice: 10, ATR: 0.4, ATRStopMult: 0}).ATRStopPct(8); got != 8 {
		t.Fatalf("mult=0 应回退 8, got %.2f", got)
	}
	// ATR=0（日K不足）→ 回退固定百分比
	if got := (&strategy.ExitContext{CostPrice: 10, ATR: 0, ATRStopMult: 2.5}).ATRStopPct(8); got != 8 {
		t.Fatalf("ATR=0 应回退 8, got %.2f", got)
	}
	// 启用：ATR=0.4 × 2.5 / 10 × 100 = 10%
	if got := (&strategy.ExitContext{CostPrice: 10, ATR: 0.4, ATRStopMult: 2.5}).ATRStopPct(8); got != 10 {
		t.Fatalf("ATR×2.5/10×100 应为 10, got %.2f", got)
	}
}

// atrKlines 构造 20 根振幅约 1.6% 的日K（ATR≈0.16 → 2.5×ATR/10≈4% 止损距离）。
func atrKlines(base float64) []data.KLine {
	now := time.Now()
	ks := make([]data.KLine, 20)
	c := base
	for i := 0; i < 20; i++ {
		ks[i] = data.KLine{
			Date: now.AddDate(0, 0, i-19), Open: c, Close: c,
			High: c + 0.08, Low: c - 0.08, Volume: 10000,
		}
		c += 0.01
	}
	return ks
}

// TestDoubleBumpATRHardStop 双凸 ATR 硬止损：ATR≈0.2、mult=2.5 → 止损距离≈5%；
// 跌 6% 触发双凸ATR止损；未启用时仅固定 8% 阈值（跌 6% 不触发 ATR 分支）。
func TestDoubleBumpATRHardStop(t *testing.T) {
	cfg := config.NewManager("")
	a := New(cfg.GetStrategyConfig())
	a.SetATRStop(true, 2.5)

	rpt := report.New(filepath.Join(t.TempDir(), "rpt.json"))
	rpt.LogSignalWithMeta("p1", "600100", "双凸", "做多", "double_bump", 10, 15, 8, nil)
	if rpt.HeldPositions()[0].EntryAt.IsZero() {
		t.Fatal("持仓记录缺失入场时间")
	}
	quotes := map[string]*data.StockInfo{"600100": {Price: 9.4}} // -6%
	dayK := map[string][]data.KLine{"600100": atrKlines(10)}

	sigs := a.CheckPositionsExits(rpt, quotes, dayK, time.Now())
	found := false
	for _, s := range sigs {
		if s.Code == "600100" && s.Reason == "双凸ATR止损" {
			found = true
		}
	}
	if !found {
		t.Fatalf("ATR 启用时跌6%%应触发双凸ATR止损, got %+v", sigs)
	}

	// 未启用：跌 6% 未破固定 8% → 无 ATR 止损（双凸其他分支可能触发，过滤掉即视为通过）
	a2 := New(cfg.GetStrategyConfig())
	a2.SetATRStop(false, 2.5)
	rpt2 := report.New(filepath.Join(t.TempDir(), "rpt2.json"))
	rpt2.LogSignalWithMeta("p1", "600100", "双凸", "做多", "double_bump", 10, 15, 8, nil)
	sigs2 := a2.CheckPositionsExits(rpt2, quotes, dayK, time.Now())
	for _, s := range sigs2 {
		if s.Reason == "双凸ATR止损" {
			t.Fatalf("ATR 未启用不应出现双凸ATR止损, got %+v", sigs2)
		}
	}
}

// TestDragonATRStopNarrowerThanFixed 龙头 ATR 止损：ATR≈0.2、mult=2.5 → 止损≈5%，
// 比固定 8% 更紧：跌 6% 时 ATR 模式触发"买入回撤全出"，固定模式不触发。
func TestDragonATRStopNarrowerThanFixed(t *testing.T) {
	cfg := config.NewManager("")
	dc := cfg.GetStrategyConfig().Dragon
	dc.BuyPullbackSellAllPct = 0.08

	rpt := report.New(filepath.Join(t.TempDir(), "dragon.json"))
	rpt.LogSignalWithMeta("p1", "600101", "龙头", "做多", "dragon", 10, 10, 8, map[string]float64{"limit_price": 10})

	quotes := map[string]*data.StockInfo{"600101": {Price: 9.4}} // -6%
	dayK := map[string][]data.KLine{"600101": atrKlines(10)}

	a := New(cfg.GetStrategyConfig())
	a.SetATRStop(true, 2.5)
	sigs := a.CheckPositionsExits(rpt, quotes, dayK, time.Now())
	found := false
	for _, s := range sigs {
		if s.Code == "600101" && s.Reason == "买入回撤全出" {
			found = true
		}
	}
	if !found {
		t.Fatalf("ATR≈5%% 止损应比固定8%%更早触发全出, got %+v", sigs)
	}

	// 固定模式（ATR 关闭）：跌 6% 未破 8% → 无"买入回撤全出"
	a2 := New(cfg.GetStrategyConfig())
	a2.SetATRStop(false, 2.5)
	rpt2 := report.New(filepath.Join(t.TempDir(), "dragon2.json"))
	rpt2.LogSignalWithMeta("p1", "600101", "龙头", "做多", "dragon", 10, 10, 8, map[string]float64{"limit_price": 10})
	sigs2 := a2.CheckPositionsExits(rpt2, quotes, dayK, time.Now())
	for _, s := range sigs2 {
		if s.Reason == "买入回撤全出" {
			t.Fatalf("固定8%%止损下跌6%%不应全出, got %+v", sigs2)
		}
	}
}