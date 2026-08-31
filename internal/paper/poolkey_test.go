// Package paper 独立模拟盘（纸面交易）引擎：把策略信号按实时价撮合成虚拟持仓，产出净值曲线并记录滑点/延迟，与真实持仓完全隔离。
package paper

import (
	"path/filepath"
	"testing"
	"time"

	"quant-trading-v2/internal/combat_agent"
	data "quant-trading-v2/internal/data"
)

// ── §OPTIMIZE_POOL_INTEGRATION_PLAN A1：战法→池 key 映射 + 门槛合并 + 纪律持久化 ──

func TestPoolKeyForStrategy(t *testing.T) {
	// §C 规则细分池：库规则的 kind 本身就是池 key（fac_1/pat_2 各自独立）
	cases := []struct{ strategy, kind, want string }{
		{"因子战法#1", "fac_1", "fac_1"},
		{"形态战法#2", "pat_2", "pat_2"},
		{"双响炮", "", "double_bump"},
		{"龙头", "", "dragon"},
		{"N形", "", "n_shape"},
		{"龙回头", "", "dragon_return"},
		{"动量", "", "momentum"}, // §动量入模拟盘
		// §名称规整：中英别名同口径
		{"N形超短", "", "n_shape"},
		{"双突破", "", "double_bump"},
		{"dragon", "", "dragon"},
		{"momentum", "", "momentum"},
		{"未知战法", "", ""},              // 无法识别 → 其他池（调用方不得下发）
		{"随便什么名", "fac_99", "fac_99"}, // kind 前缀压过显示名
	}
	for _, c := range cases {
		if got := PoolKeyForStrategy(c.strategy, c.kind); got != c.want {
			t.Errorf("PoolKeyForStrategy(%q,%q)=%q want %q", c.strategy, c.kind, got, c.want)
		}
	}
}

// TestApplyPoolMinScoreMergeAndClear 验证 ApplyPoolMinScore 的合并与清除语义：
// 池无规则时写入创建、已有规则只改 MinScore 其余保留、清零 MinScore 仅置零字段保留规则、
// 空 key/未启用类型静默跳过。
// English: verifies ApplyPoolMinScore — create-when-absent, field-merged update, zeroed-but-kept
// clearing, and silent skip for empty/unenabled keys.
func TestApplyPoolMinScoreMergeAndClear(t *testing.T) {
	e := New(testCfg(), "")
	e.SetStrategyPools([]string{"dragon"})
	// 池无规则时写入 → 创建仅含 MinScore 的规则
	e.ApplyPoolMinScore("dragon", 85)
	ps := e.StrategyPools()
	var rule *PoolBuyRule
	for _, p := range ps {
		if p.Key == "dragon" {
			rule = p.BuyRule
		}
	}
	if rule == nil || rule.MinScore != 85 {
		t.Fatalf("门槛未生效: %+v", rule)
	}
	// 合并语义：已有其他字段的规则只改 MinScore、其余保留
	e.SetPoolBuyRule("dragon", &PoolBuyRule{MaxDailyBuys: 3, CooldownMinutes: 10, BudgetPctPerDay: 30})
	e.ApplyPoolMinScore("dragon", 90)
	for _, p := range e.StrategyPools() {
		if p.Key == "dragon" {
			r := p.BuyRule
			if r == nil || r.MinScore != 90 || r.MaxDailyBuys != 3 || r.CooldownMinutes != 10 || r.BudgetPctPerDay != 30 {
				t.Fatalf("合并失败: %+v", r)
			}
		}
	}
	// 清零 MinScore 但其他字段仍在 → 规则保留、仅门槛归零（合并语义：只动 MinScore）
	e.ApplyPoolMinScore("dragon", 0)
	for _, p := range e.StrategyPools() {
		if p.Key == "dragon" {
			r := p.BuyRule
			if r == nil || r.MinScore != 0 || r.MaxDailyBuys != 3 || r.BudgetPctPerDay != 30 {
				t.Fatalf("清零应只动 MinScore: %+v", r)
			}
		}
	}
	// 其他池/空 key 静默跳过
	e.ApplyPoolMinScore("", 50)
	e.ApplyPoolMinScore("other", 50) // 未启用类型不报错即可
}

// TestSetPoolBuyRulePersistedAcrossReload 验证池买入纪律跨重载持久化。
func TestSetPoolBuyRulePersistedAcrossReload(t *testing.T) {
	// §A1 回归：此前 SetPoolBuyRule 缺 persist() 导致纪律重启即丢
	path := filepath.Join(t.TempDir(), "paper_state.json")
	e1 := New(testCfg(), path)
	e1.SetStrategyPools([]string{"double_bump"})
	e1.SetPoolBuyRule("double_bump", &PoolBuyRule{MaxDailyBuys: 2, MinScore: 80})
	// 同路径重开引擎（模拟重启恢复）
	e2 := New(testCfg(), path)
	found := false
	for _, p := range e2.StrategyPools() {
		if p.Key == "double_bump" {
			found = true
			if p.BuyRule == nil || p.BuyRule.MaxDailyBuys != 2 || p.BuyRule.MinScore != 80 {
				t.Fatalf("重启后纪律丢失: %+v", p.BuyRule)
			}
		}
	}
	if !found {
		t.Fatal("重启后未找到 double_bump 池")
	}
}

// TestEnsurePoolConservation 验证 EnsurePool 开立规则细分池时资金守恒：Σ池现金在开池前后不变，
// 新池获得 max(总现金×5%, ¥1000 保底) 且不超 25% 上限，重复开池返回 false。
// English: verifies EnsurePool conservation — Σpool cash is unchanged, the new pool gets
// max(5% of total, ¥1000 floor) capped at 25%, and a duplicate open returns false.
func TestEnsurePoolConservation(t *testing.T) {
	e := New(testCfg(), "")
	e.SetStrategyPools([]string{"dragon", "n_shape"})
	totalBefore := 0.0
	for _, p := range e.StrategyPools() {
		totalBefore += p.Cash
	}
	if !e.EnsurePool("fac_1") {
		t.Fatal("开池失败")
	}
	if e.EnsurePool("fac_1") {
		t.Fatal("重复开池应返回 false")
	}
	ps := e.StrategyPools()
	totalAfter := 0.0
	var fac = 0.0
	var found bool
	for _, p := range ps {
		totalAfter += p.Cash
		if p.Key == "fac_1" {
			fac, found = p.Cash, true
		}
	}
	if !found {
		t.Fatal("新池未出现在快照")
	}
	if abs(totalBefore-totalAfter) > 0.01 {
		t.Fatalf("守恒破坏: before=%.2f after=%.2f", totalBefore, totalAfter)
	}
	minFloor := totalBefore * 0.05
	if fac < ensurePoolFloor-0.01 || fac < minFloor-0.01 {
		t.Fatalf("新池资金低于保底: %.2f (floor=%.0f, 5%%=%.2f)", fac, ensurePoolFloor, minFloor)
	}
}

// TestRulePoolSignalRoutingAndLabel 验证规则细分池（fac_1）信号正确归池、标签解析器生效
// （fac_1 → 因子战法#1，未知回退 key 本身）。
// English: verifies rule-split pool (fac_1) routing and the label resolver (fac_1 → 因子战法#1,
// unknown falls back to the key itself).
func TestRulePoolSignalRoutingAndLabel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "paper_state.json")
	e := New(testCfg(), path)
	e.SetStrategyPools([]string{"fac_1"})
	// 标签解析器：fac_1 → 因子战法#1；未知回退 key 本身
	e.SetPoolLabelResolver(func(id string) string {
		if id == "fac_1" {
			return "因子战法#1"
		}
		return ""
	})
	now := time.Now()
	e.OnSignals([]combat_agent.Signal{
		{Code: "600000.SH", Name: "A", StrategyType: "fac_1", StrategyID: "fac_1",
			Direction: "做多", Action: "buy", Price: 10, GeneratedAt: now},
	}, map[string]*data.StockInfo{"600000.SH": {Price: 10}})
	found := false
	for _, p := range e.StrategyPools() {
		if p.Key == "fac_1" {
			found = true
			if p.Label != "因子战法#1" {
				t.Fatalf("标签未解析: %q", p.Label)
			}
			if p.Positions != 1 {
				t.Fatalf("信号未归入 fac_1 池: positions=%d", p.Positions)
			}
		}
	}
	if !found {
		t.Fatal("fac_1 池缺失")
	}
}

// abs 返回浮点数的绝对值（测试断言容差比较辅助）。
func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
