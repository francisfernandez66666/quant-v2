// flow_scale_test.go — §FLOW-FACTOR / §DYN-SIZE 执行层锁：BuyScale 是否真的落到模拟盘成交股数上。
//
// 为什么这条必须单独钉：净流入因子的产出物是 `ComputeSizing` 的 scale，而 scale 一路要经过
// `Decision.BuyScale → Signal.SizeScale → paper.fillLocked → dynamicPaperQtyLocked` 才能变成
// 真实股数。中间任何一环断掉，**回测与灰度观测都会看着"因子生效"而账面一动不动**——
// 因子层单测全绿也发现不了。方案 §2.10 的「执行层」一行就是要求这条链路有断言。
//
// 口径（与 §DYN-SIZE-v3 一致）：
//   - dynamic 模式下 scale 直接乘在**池权益基数**上（不是乘最终股数），所以
//     0.5 → 权益基数 10 万×0.5 = 5 万 → 受 35% 单票上限 → 1.75 万 → 1750 股；
//   - sizeScale=0 或 1.0 都表示"不缩放"（0 是 Signal 的零值，绝不能理解成"买 0 股"）；
//   - SizingShadow=true 时只留痕不下发 → 股数与 1.0 基线完全一致。
//
// English: execution-layer lock — the factor's scale must actually change the paper fill size.
// In dynamic mode the scale multiplies the pool-equity base (not the final quantity); zero and
// one both mean "no scaling"; shadow mode must leave the quantity identical to the baseline.
package paper

import (
	"testing"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/data"
)

// flowBuySignal 造一条最小的买入信号（SizeScale 由调用方给）。
// English: builds a minimal buy signal carrying the given size scale.
func flowBuySignal(code string, sizeScale float64) combat_agent.Signal {
	return combat_agent.Signal{
		Code: code, Name: "测试股", Strategy: "龙头", StrategyType: "dragon",
		Direction: "做多", Action: "buy", Price: 10, GeneratedAt: time.Now(), SizeScale: sizeScale,
	}
}

// flowFillQty 用给定 sizeScale 跑一遍"新引擎 + 单条信号"，返回成交股数。
// 每次都用全新引擎：同一引擎第二次买入会走加仓路径，股数口径不同（会把比例断言搅浑）。
// English: runs one signal through a fresh engine and returns the filled quantity.
func flowFillQty(t *testing.T, cfg Config, sizeScale float64) int {
	t.Helper()
	e := New(cfg, "")
	e.SetStrategyPools([]string{"dragon"}) // OnSignals 按战法池入账：不设池则无池可买
	quotes := map[string]*data.StockInfo{"600000.SH": {Code: "600000.SH", Price: 10, ChangePct: 1}}
	e.OnSignals([]combat_agent.Signal{flowBuySignal("600000.SH", sizeScale)}, quotes)
	p := e.positions["600000.SH"]
	if p == nil {
		t.Fatalf("sizeScale=%v 未成交（应至少买入一手）", sizeScale)
	}
	return p.Qty
}

// TestDynamicBuyScaleChangesFillQty dynamic 模式下 scale 真的改变成交股数。
//
// 读数的正确解释（数字本身就是口径说明，改动前先看懂这段）：
//   - 单池 dragon 分到 5 万池权益，全盘 10 万 → 35% 单票上限 = 3.5 万 = 3500 股；
//   - scale=1.0 时基数 5 万 > 上限 3.5 万 ⇒ **上限生效**，成交 3500 股；
//   - scale=0.5 时基数 5 万×0.5 = 2.5 万 < 上限 ⇒ **基数生效**，成交 2500 股；
//   - scale=0.25 时 1.25 万 ⇒ 1250 股（与 0.5 恰好成 2:1，证明缩放是乘在基数上的）；
//   - scale=0（Signal 零值）必须与 1.0 同义 —— 绝不能理解成"买 0 股"。
//
// 反证靶心：若 dynamicPaperQtyLocked 丢掉 scale 参数，上面四条会全部读成 3500。
// English: in dynamic mode the scale multiplies the pool-equity base (the 35% single-stock cap
// binds at scale=1 but not below), so 0.5 → 2500 and 0.25 → 1250 prove the multiplication is real.
func TestDynamicBuyScaleChangesFillQty(t *testing.T) {
	cfg := dynPaperConfig(100000)
	if got := flowFillQty(t, cfg, 1.0); got != 3500 {
		t.Fatalf("scale=1.0 受 35%% 单票上限约束应买 3500 股, got %d", got)
	}
	if got := flowFillQty(t, cfg, 0.5); got != 2500 {
		t.Fatalf("scale=0.5 应按池权益基数（5万×0.5）买 2500 股, got %d（scale 未落到执行层）", got)
	}
	// 0.25 档：理论 1250 股，预算器按整手向下取整并预留费用缓冲后落在 1200 股附近。
	// 断言用**区间**而不是精确值：这里要证明的是"scale 乘在基数上"（忽略 scale 会读成 3500），
	// 不是把 ordbudget 的取整细节焊死在用例里。
	// English: assert a range — the point is that the scale multiplies the base (ignoring it would
	// read 3500), not to freeze the budget calculator's lot-rounding detail.
	if got := flowFillQty(t, cfg, 0.25); got < 1150 || got > 1250 {
		t.Fatalf("scale=0.25 应买约 1250 股（整手取整后 1150~1250）, got %d", got)
	}
	// 零值与 1.0 同义：Signal 的零值字段绝不能理解成"买 0 股"。
	if zero := flowFillQty(t, cfg, 0); zero != 3500 {
		t.Fatalf("sizeScale=0 应与 1.0 同义（3500 股）, got %d", zero)
	}
}

// TestDynamicBuyScaleShadowKeepsQty 影子模式下 scale 只留痕、不改股数（灰度期的安全前提）。
// English: in shadow mode the scale is recorded only — the quantity must equal the baseline.
func TestDynamicBuyScaleShadowKeepsQty(t *testing.T) {
	cfg := dynPaperConfig(100000)
	cfg.SizingShadow = true
	if got := flowFillQty(t, cfg, 0.5); got != 3500 {
		t.Fatalf("影子模式不得改变成交股数（应 3500）, got %d", got)
	}
}

// TestLegacyBuyScaleChangesAmount 旧 product/legacy 路径：scale 乘在金额上（10000×0.5 → 500 股 @10）。
// 这条路径保留供回滚对照，方案 §2.4 明确要求"逐字保留"，因此也必须被锁。
// English: the legacy path scales the amount (10000×0.5 → 500 shares at 10) and must stay locked.
func TestLegacyBuyScaleChangesAmount(t *testing.T) {
	cfg := dynPaperConfig(100000)
	cfg.BudgetMode = "legacy" // 走 FixedAmount 路径（dynPaperConfig 的 FixedAmount=9999）
	cfg.FixedAmount = 10000
	if got := flowFillQty(t, cfg, 1.0); got != 1000 {
		t.Fatalf("legacy 基线（scale=1.0）应买 1000 股, got %d", got)
	}
	if got := flowFillQty(t, cfg, 0.5); got != 500 {
		t.Fatalf("legacy scale=0.5 应买 500 股, got %d", got)
	}
}
