// Package paper 独立模拟盘（纸面交易）引擎：把策略信号按实时价撮合成虚拟持仓，产出净值曲线并记录滑点/延迟，与真实持仓完全隔离。
package paper

import (
	"errors"
	"testing"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/data"
)

// TestManualAddOnHonorsPoolDiscipline §P1-I：手动加仓此前绕过冷却/日限/预算前置检查，
// 只记账不把关。这里设池中已有一次买入且处于冷却，手动加仓必须被纪律拒绝。
func TestManualAddOnHonorsPoolDiscipline(t *testing.T) {
	e := New(testCfg(), "")
	e.SetStrategyPools([]string{"dragon"})
	rule := &PoolBuyRule{CooldownMinutes: 60, MaxDailyBuys: 5, BudgetPctPerDay: 100}
	e.SetPoolBuyRule("dragon", rule)

	// 先通过信号路径正常建仓一次（走 fillLocked，纪律放行）
	now := time.Now()
	e.OnSignals([]combat_agent.Signal{
		{Code: "600000.SH", Name: "龙头股", Strategy: "龙头", StrategyType: "dragon",
			Direction: "做多", Action: "buy", Price: 10, Confidence: 0.9, GeneratedAt: now},
	}, map[string]*data.StockInfo{"600000.SH": {Price: 10}})
	if len(e.positions) != 1 {
		t.Fatal("首笔建仓应成功")
	}

	// 冷却 60 分钟内：手动加仓应被纪律挡下（修复前会直接加仓成功）
	err := e.BuyExInPool("600000.SH", "龙头股", "龙头", "dragon", 10, 10, 100,
		map[string]*data.StockInfo{"600000.SH": {Price: 10}})
	if err == nil {
		t.Fatal("冷却期内手动加仓不应放行")
	}
	if !errors.Is(err, errPoolDiscipline) {
		t.Fatalf("手动加仓拒绝应属分仓纪律错误, got %v", err)
	}
}

// TestResetPoolClearsDiscipline §P1-I：单池清盘后，该池当日纪律计数（次数/花费/冷却）
// 应被清空；否则同日清盘再买会被陈旧计数误拒。
func TestResetPoolClearsDiscipline(t *testing.T) {
	e := New(testCfg(), "")
	e.SetStrategyPools([]string{"dragon"})
	e.SetPoolBuyRule("dragon", &PoolBuyRule{CooldownMinutes: 60, MaxDailyBuys: 1, BudgetPctPerDay: 100})

	// 用掉当日唯一名额
	e.OnSignals([]combat_agent.Signal{
		{Code: "600000.SH", Name: "龙头股", Strategy: "龙头", StrategyType: "dragon",
			Direction: "做多", Action: "buy", Price: 10, Confidence: 0.9, GeneratedAt: time.Now()},
	}, map[string]*data.StockInfo{"600000.SH": {Price: 10}})
	if len(e.positions) != 1 {
		t.Fatal("首笔建仓应成功")
	}
	// 日限已满 → 再加仓被拒
	if err := e.BuyExInPool("600000.SH", "龙头股", "龙头", "dragon", 10, 10, 100,
		map[string]*data.StockInfo{"600000.SH": {Price: 10}}); err == nil {
		t.Fatal("日限已满时加仓应被拒")
	}

	// 清盘该池：纪律计数应随之清空
	e.ResetPool("dragon")
	if len(e.positions) != 0 {
		t.Fatal("清盘后该池持仓应归零")
	}

	// 清盘同一池后再次建仓：不应被同日陈旧计数误拒（纪律已重置）
	e.OnSignals([]combat_agent.Signal{
		{Code: "600001.SH", Name: "新龙头", Strategy: "龙头", StrategyType: "dragon",
			Direction: "做多", Action: "buy", Price: 10, Confidence: 0.9, GeneratedAt: time.Now()},
	}, map[string]*data.StockInfo{"600001.SH": {Price: 10}})
	if len(e.positions) != 1 {
		t.Fatal("清盘后纪律应重置，允许重新建仓")
	}
}
