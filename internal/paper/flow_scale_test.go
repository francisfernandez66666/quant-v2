// flow_scale_test.go — §FLOW-FACTOR / §DYN-SIZE 执行层锁：BuyScale 是否真的落到模拟盘成交股数上。
//
// 为什么这条必须单独钉：净流入因子的产出物是 `ComputeSizing` 的 scale，而 scale 一路要经过
// `Decision.BuyScale → Signal.SizeScale → paper.fillLocked → dynamicPaperQtyLocked` 才能变成
// 真实股数。中间任何一环断掉，**回测与灰度观测都会看着"因子生效"而账面一动不动**——
// 因子层单测全绿也发现不了。方案 §2.10 的「执行层」一行就是要求这条链路有断言。
//
// 口径（与 §DYN-SIZE-v3 一致）：
//   - dynamic 模式下 k 进的是**单笔占比**（§B0：eff = lo+(hi−lo)×k），再乘池权益基数，
//     所以 0.5 → eff=0.30 → 5 万×0.30 = 1.5 万 → 1500 股（不再乘最终股数）；
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
	// §DYN-SIZE v4：出厂单笔基准 10%（1000 股上限），那样"scale 乘在基数上"与"35% 额度截断"
	// 两条约束会同时看不出差别。这里把基准临时放大到 100%，让本文件原有的 scale 敏感性对照继续有效
	// ——锁的靶心是"scale 有没有落到执行层"，不是"基准是多少"（基准本身由 §1.4 的用例锁）。
	// §B0 起单笔占比 = lo + (hi−lo)×k（k = SizeScale），所以这里必须给一个**有宽度**的区间
	// （10%~50%）才看得见 scale 的敏感性；lo=hi 会让 eff 变成常数、本文件全部用例同时失效。
	e.SetSizingContext(1.0, 0.10, 0.50, 1.0, false)
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
//   - 单池 dragon 分到 5 万池权益；单笔占比区间取 10%~50%（见 flowFillQty 的注入）；
//   - k=1.0 ⇒ eff=0.50 ⇒ 5 万×0.50 = 2.5 万 ⇒ **2500 股**（35% 单票上限 3.5 万不参与）；
//   - k=0.5 ⇒ eff=0.30 ⇒ 1.5 万 ⇒ 1500 股；
//   - k=0.25 ⇒ eff=0.20 ⇒ 1.0 万 ⇒ 1000 股（与 0.5 恰好成 3:2，证明 k 真的进了占比）；
//   - k=0（Signal 零值）必须与 1.0 同义 ⇒ 2500 股 —— 绝不能理解成"买 0 股"。
//
// 反证靶心：若 dynamicPaperQtyLocked 丢掉 k（或又把它乘一遍），上面四条会同时读到 2500 或全不成立。
// English: in dynamic mode the scale multiplies the pool-equity base (the 35% single-stock cap
// binds at scale=1 but not below), so 0.5 → 2500 and 0.25 → 1250 prove the multiplication is real.
func TestDynamicBuyScaleChangesFillQty(t *testing.T) {
	cfg := dynPaperConfig(100000)
	if got := flowFillQty(t, cfg, 1.0); got != 2500 {
		t.Fatalf("k=1.0 ⇒ eff=0.50 ⇒ 应买 2500 股, got %d", got)
	}
	if got := flowFillQty(t, cfg, 0.5); got != 1500 {
		t.Fatalf("k=0.5 ⇒ eff=0.30 ⇒ 应买 1500 股, got %d（k 未落到执行层）", got)
	}
	// 0.25 档：eff=0.20 ⇒ 1.0 万 ⇒ 1000 股（整手无取整损失，所以这里可以用精确值）。
	// English: k=0.25 → eff=0.20 → 10k → 1000 shares (no lot rounding loss, exact value ok).
	if got := flowFillQty(t, cfg, 0.25); got != 1000 {
		t.Fatalf("k=0.25 ⇒ eff=0.20 ⇒ 应买 1000 股, got %d", got)
	}
	// 零值与 1.0 同义：Signal 的零值字段绝不能理解成"买 0 股"。
	if zero := flowFillQty(t, cfg, 0); zero != 2500 {
		t.Fatalf("sizeScale=0 应与 1.0 同义（2500 股）, got %d", zero)
	}
}

// TestDynamicBuyScaleShadowKeepsQty 影子模式下 scale 只留痕、不改股数（灰度期的安全前提）。
// English: in shadow mode the scale is recorded only — the quantity must equal the baseline.
func TestDynamicBuyScaleShadowKeepsQty(t *testing.T) {
	cfg := dynPaperConfig(100000)
	cfg.SizingShadow = true
	// 影子模式下 k 恒按中性 1.0 计 ⇒ eff=0.50 ⇒ 与基线（k=1.0）完全一致。
	if got := flowFillQty(t, cfg, 0.5); got != 2500 {
		t.Fatalf("影子模式不得改变成交股数（应 2500）, got %d", got)
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
