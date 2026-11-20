// turnover_tier_20261120_test.go — §T-TURNOVER-TIER（owner 2026-11-20）回归锁。
//
// 背景（现网读数，不是假设）：盒上策略日志整天打
// `t_round skips=step0.5 turnover 0.69% < 1.50%` —— 一只正常活跃的大票被一条按小票
// 标定的平铺线永远挡在门外，T 腿等于只对小票开放。分档后门槛随流通市值走。
//
// 这组用例锁四件事：
//  1. 出厂三档的边界（500 / 100 亿，含端点）与"小票仍是 1.50%"；
//  2. 市值读不到 ⇒ 退回平铺值（最严档），**绝不因为看不见而放宽**；
//  3. 显式配了 min_turnover_pct ⇒ 分档整块不生效（存量手调账号行为不变）；
//  4. 分档表不合法（倒序/负数/越界）⇒ 整表回落出厂档，绝不半张表生效。
package config

import (
	"math"
	"testing"
)

// TestTurnoverTierFactoryBoundaries 出厂三档的边界。
func TestTurnoverTierFactoryBoundaries(t *testing.T) {
	tc := &TConfig{}
	cases := []struct {
		mcapYi float64
		want   float64
	}{
		{600, 0.40},
		{500, 0.40}, // 含端点
		{499.99, 0.60},
		{200, 0.60},
		{100, 0.60},   // 含端点
		{99.99, 1.50}, // 小票：与旧平铺值一个字节都不差
		{50, 1.50},
		{0, 1.50},  // 读不到市值 ⇒ 最严档
		{-1, 1.50}, // 脏读数同样按读不到处理
		{math.NaN(), 1.50},
	}
	for _, c := range cases {
		if got := tc.TMinTurnoverPctFor(c.mcapYi); got != c.want {
			t.Fatalf("TMinTurnoverPctFor(%v)=%v want %v", c.mcapYi, got, c.want)
		}
	}
	// 出厂档本身必须与平铺值同源：最小一档就是 defaultTMinTurnoverPct。
	if defaultTMinTurnoverTiers[len(defaultTMinTurnoverTiers)-1].Pct != defaultTMinTurnoverPct {
		t.Fatalf("ladder tail %v != defaultTMinTurnoverPct %v",
			defaultTMinTurnoverTiers[len(defaultTMinTurnoverTiers)-1].Pct, defaultTMinTurnoverPct)
	}
}

// TestTurnoverTierExplicitFlatWins 显式平铺值优先，分档整块不生效（含 0＝关掉这条门）。
func TestTurnoverTierExplicitFlatWins(t *testing.T) {
	flat := 0.9
	tc := &TConfig{MinTurnoverPct: &flat}
	if got := tc.TMinTurnoverTiersOf(); got != nil {
		t.Fatalf("explicit flat should disable the ladder, got %v", got)
	}
	for _, mcap := range []float64{600, 200, 50, 0} {
		if got := tc.TMinTurnoverPctFor(mcap); got != flat {
			t.Fatalf("mcap=%v got %v want flat %v", mcap, got, flat)
		}
	}
	zero := 0.0
	off := &TConfig{MinTurnoverPct: &zero}
	if got := off.TMinTurnoverPctFor(600); got != 0 {
		t.Fatalf("explicit 0 should disable the gate, got %v", got)
	}
}

// TestTurnoverTierCustomTable 合法自定义分档表照用（不改出厂档，只换表）。
func TestTurnoverTierCustomTable(t *testing.T) {
	tc := &TConfig{MinTurnoverTiers: []TTurnoverTier{
		{MinCapYi: 1000, Pct: 0.3},
		{MinCapYi: 0, Pct: 1.2},
	}}
	if got := tc.TMinTurnoverPctFor(1200); got != 0.3 {
		t.Fatalf("custom top tier got %v want 0.3", got)
	}
	if got := tc.TMinTurnoverPctFor(300); got != 1.2 {
		t.Fatalf("custom bottom tier got %v want 1.2", got)
	}
}

// TestTurnoverTierInvalidTableFallsBack 半张表比没有表更危险 ⇒ 整表回落出厂档。
func TestTurnoverTierInvalidTableFallsBack(t *testing.T) {
	bad := [][]TTurnoverTier{
		{{MinCapYi: 100, Pct: 0.6}, {MinCapYi: 500, Pct: 0.4}}, // 倒序
		{{MinCapYi: -1, Pct: 1.5}},                             // 负市值
		{{MinCapYi: 500, Pct: 26}},                             // 换手率越界
		{{MinCapYi: 500, Pct: math.NaN()}},                     // NaN
		{{MinCapYi: 500, Pct: 0.4}, {MinCapYi: 500, Pct: 0.6}}, // 重复边界
	}
	for i, table := range bad {
		tc := &TConfig{MinTurnoverTiers: table}
		got := tc.TMinTurnoverTiersOf()
		if len(got) != len(defaultTMinTurnoverTiers) || got[0].MinCapYi != 500 {
			t.Fatalf("case %d: invalid table not rejected: %v", i, got)
		}
		if pct := tc.TMinTurnoverPctFor(600); pct != 0.40 {
			t.Fatalf("case %d: fallback tier got %v want 0.40", i, pct)
		}
	}
}
