package paper

import (
	"path/filepath"
	"testing"
)

// ── §OPTIMIZE_POOL_INTEGRATION_PLAN A1：战法→池 key 映射 + 门槛合并 + 纪律持久化 ──

func TestPoolKeyForStrategy(t *testing.T) {
	// 库规则前缀优先（kind 权威，显示名只是辅助）
	cases := []struct{ strategy, kind, want string }{
		{"因子战法#1", "fac_1", "factor"},
		{"形态战法#2", "pat_2", "pattern"},
		{"双响炮", "", "double_bump"},
		{"龙头", "", "dragon"},
		{"N形", "", "n_shape"},
		{"龙回头", "", "dragon_return"},
		{"未知战法", "", ""},        // 无法识别 → 其他池（调用方不得下发）
		{"随便什么名", "fac_99", "factor"}, // kind 前缀压过显示名
	}
	for _, c := range cases {
		if got := PoolKeyForStrategy(c.strategy, c.kind); got != c.want {
			t.Errorf("PoolKeyForStrategy(%q,%q)=%q want %q", c.strategy, c.kind, got, c.want)
		}
	}
}

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
