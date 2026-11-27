// internal/config/carrier_knobs_tp_20261124_test.go — §TP-LADDER 载体旋钮的单位与关系锁（2026-11-24）。
//
// 现场：载体只有一条 take_profit_ratio=1.05 的全平腿，002579/000678/000009 开盘三分钟
// 各被一刀清仓。owner 九条裁决把它改成两段（卖半 + 跟峰值）。本文件锁的不是"文案"，
// 是这条阶梯最容易悄悄坏掉的三件事：
//
//  1. **单位**：TakeProfitPct / GivebackPP / PeakArmPct 是百分点，RetrPct 是相对峰值价的
//     百分比。两者混用不会报错，只会让"30% 掉到 25% 清"永远够不到线（相对峰值价口径下
//     只有 3.85%，比 5% 小）；
//  2. **关系**：每一档板宽都必须 卖半 < 清剩余，且三档（10cm/20cm/30cm）必须单调不降
//     ——板越宽、回撤容忍越大。任何一条反过来，规则层会在同一个价位同时要求减半和清仓；
//  3. **真源**：龙头的载体止盈点（30）刻意不同于纸面出厂（10），而龙回头是转调账号级
//     出厂表的换算值 ⇒ 改纸面表那天载体跟着动，不会再出现第二条账。
//
// English: unit and ordering locks for the carrier-side take-profit knobs.
package config_test

import (
	"math"
	"testing"

	cfg "quant-trading-v2/internal/config"
	"quant-trading-v2/internal/strategies/double_bump"
)

// TestTPKnobsUnitsAndOrdering 逐档校验 卖半 < 清剩余，并校验三档之间的单调性。
func TestTPKnobsUnitsAndOrdering(t *testing.T) {
	ladders := []struct {
		name  string
		half  float64
		clear float64
	}{
		{"10cm", cfg.CarrierTPTrailHalfRetrPct(), cfg.CarrierTPTrailClearRetrPct()},
		{"20cm", cfg.CarrierTPTrailHalfRetrPct20(), cfg.CarrierTPTrailClearRetrPct20()},
		{"30cm", cfg.CarrierTPTrailHalfRetrPct30(), cfg.CarrierTPTrailClearRetrPct30()},
	}
	for _, lad := range ladders {
		if lad.half <= 0 || lad.clear <= 0 {
			t.Fatalf("%s: retrace thresholds must be positive, got half=%v clear=%v",
				lad.name, lad.half, lad.clear)
		}
		if lad.half >= lad.clear {
			t.Fatalf("%s: half (%v) must be strictly below clear (%v); otherwise one price "+
				"asks for both a trim and a full exit", lad.name, lad.half, lad.clear)
		}
	}
	// 板越宽，容忍越大（10cm <= 20cm <= 30cm）。相等是允许的，反过来不行。
	if !(cfg.CarrierTPTrailHalfRetrPct() <= cfg.CarrierTPTrailHalfRetrPct20() &&
		cfg.CarrierTPTrailHalfRetrPct20() <= cfg.CarrierTPTrailHalfRetrPct30()) {
		t.Fatalf("half-sale retrace must not shrink as the board widens: %v / %v / %v",
			cfg.CarrierTPTrailHalfRetrPct(), cfg.CarrierTPTrailHalfRetrPct20(), cfg.CarrierTPTrailHalfRetrPct30())
	}
	if !(cfg.CarrierTPTrailClearRetrPct() <= cfg.CarrierTPTrailClearRetrPct20() &&
		cfg.CarrierTPTrailClearRetrPct20() <= cfg.CarrierTPTrailClearRetrPct30()) {
		t.Fatalf("clear retrace must not shrink as the board widens: %v / %v / %v",
			cfg.CarrierTPTrailClearRetrPct(), cfg.CarrierTPTrailClearRetrPct20(), cfg.CarrierTPTrailClearRetrPct30())
	}
}

// TestTPKnobsOwnersRulings 钉死 owner 2026-11-24 复核过的三个实数。
func TestTPKnobsOwnersRulings(t *testing.T) {
	// 20cm 卖半 5% / 清 8%（owner："这个对"）
	if got := cfg.CarrierTPTrailClearRetrPct20(); got != 8.0 {
		t.Fatalf("ChiNext/STAR clear retrace must be 8%%, got %v", got)
	}
	// 30cm 卖半 6% / 清 10%（owner："这个也对"）
	if got := cfg.CarrierTPTrailClearRetrPct30(); got != 10.0 {
		t.Fatalf("BSE clear retrace must be 10%%, got %v", got)
	}
	// 卖半比例就是一半，且阶梯只有一个 Stage-1 常量。
	if got := cfg.CarrierTPHalfFraction(); math.Abs(got-0.5) > 1e-9 {
		t.Fatalf("stage-0 must sell half, got fraction %v", got)
	}
	if got := cfg.CarrierTPStageHalf(); got != 1 {
		t.Fatalf("stage marker must be 1, got %v", got)
	}
	// 龙头开板不回封判清仓是 15 分钟（秒为单位）。
	if got := cfg.CarrierTPDragonOpenNoResealSec(); got != 900.0 {
		t.Fatalf("dragon open-board window must be 900s, got %v", got)
	}
	// D3 2026-11-25（owner 裁决 ①）：没有战法止盈点的票也不是"站上成本就卖"，
	// 要先有 3 个百分点的浮盈才武装峰值回撤腿——止盈不是止损，水下交给 reduce。
	if got := cfg.CarrierTPPeakArmPct(); got != 3.0 {
		t.Fatalf("peak arm threshold must be 3 P&L points (owner ruling 2026-11-25), got %v", got)
	}
	// 2026-11-27：破位腿退役，其"浮盈只减半"旋钮随之下线；余下两条开关仍默认开。
	if !cfg.CarrierTPLadderEnabled() || !cfg.CarrierTPRebuyBlock() {
		t.Fatalf("ladder/rebuy-block must both default on: %v/%v",
			cfg.CarrierTPLadderEnabled(), cfg.CarrierTPRebuyBlock())
	}
}

// TestTPKnobsStrategyPoints 锁龙头/双凸/龙回头三个"战法自己的止盈点"的单位与出处。
func TestTPKnobsStrategyPoints(t *testing.T) {
	// 龙头：载体 30 点，而纸面出厂是 10 点。两者不同是 owner 主动裁的
	//（「龙头是超短战法，实盘这条腿要的是封板后别把利润全让回去」），
	// 所以这里既锁 30，也锁"它不等于纸面那个 10"——谁把它改成转调纸面，测试就红。
	if got := cfg.CarrierTPDragonTakeProfitPct(); got != 30.0 {
		t.Fatalf("carrier dragon take-profit must be 30 points, got %v", got)
	}
	paperDragon := cfg.DefaultStrategyConfig().Dragon.TakeProfitPct // 已是百分点
	if math.Abs(paperDragon-10.0) > 1e-9 {
		t.Fatalf("paper dragon take-profit is expected to stay at 10 points, got %v", paperDragon)
	}
	if cfg.CarrierTPDragonTakeProfitPct() == paperDragon {
		t.Fatal("carrier dragon leg must NOT be wired to the paper dragon table")
	}
	// 双凸：15 点是战法包自己的出厂常量（不是载体另写的数）。这里只锁"两处同源"
	// ——如果哪天载体把 15 写成自己的字面量，参数文件与会先红在 derive 的对账上。
	if double_bump.DefaultTakeProfitPct != 15.0 {
		t.Fatalf("double-bump factory take-profit must stay 15 points, got %v",
			double_bump.DefaultTakeProfitPct)
	}
	// 龙回头：转调账号级出厂表（0.25 ⇒ 25 点），这里只做单位换算。
	if got, want := cfg.CarrierTPDragonReturnTakeProfitPct(),
		cfg.DefaultStrategyConfig().DragonReturn.TakeProfitPct*100; math.Abs(got-want) > 1e-9 {
		t.Fatalf("dragon-return take-profit must be the account-level table ×100: want %v, got %v", want, got)
	}
	// 有止盈点的票 Stage 1 用**百分点**回吐（30→25）；5 点必须小于 30 点，
	// 否则 Stage 0 刚卖完一半就会立刻清掉另一半。
	if got := cfg.CarrierTPGivebackPP(); got <= 0 || got >= cfg.CarrierTPDragonTakeProfitPct() {
		t.Fatalf("give-back must be a positive point count below the dragon target: %v vs %v",
			got, cfg.CarrierTPDragonTakeProfitPct())
	}
}
