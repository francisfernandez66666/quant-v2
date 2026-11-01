// dynbudget_v3_test.go — 模拟盘 dynamic 预算锁（v3 §9.2）。
//
// 验证：池权益为基数、全盘权益为 35% 分母、持仓数量上限让位、手动卖出按清仓处理。
// §F-4（2026-11-01）起"手动买入忽略手填股数"这条口径已作废：手填股数改为**意图上限**
// （只压不抬），见 TestPaperDynamicManualBuyRespectsTypedQty 与 f4_manual_qty_cap_test.go。
//
// English: paper-book locks for the v3 dynamic budget — pool equity is the base, whole-book equity
// is the 35% denominator, position caps yield, manual sells close. Since §F-4 a typed share count is
// an upper bound instead of being ignored.
package paper

import (
	"testing"
	"time"

	"quant-trading-v2/internal/data"
)

func dynPaperConfig(capital float64) Config {
	return Config{
		Enabled: true, InitialCapital: capital, FixedAmount: 9999, MaxPositions: 1,
		BudgetMode: "dynamic", SizingEnabled: true, SizingShadow: false,
		SingleStockCapPct: 35,
	}
}

func dynQuotes(codes ...string) map[string]*data.StockInfo {
	out := make(map[string]*data.StockInfo, len(codes))
	for _, c := range codes {
		out[c] = &data.StockInfo{Code: c, Price: 10, ChangePct: 1}
	}
	return out
}

// TestPaperDynamicManualBuyRespectsTypedQty §F-4（2026-11-01）改口径：手填股数从"被忽略"
// 改为"意图上限（只压不抬）"。
//
// 旧行为（本文件原断言，现已作废）：dynamic 下手填 100 股被整个丢掉，引擎按 10 万×50% 再被
// 35% 单票额度截到 3500 股 —— 用户点"买 100 股"，账上多出 35 倍，页面显示与实际成交对不上，
// 且没有任何提示。新行为：100 股就是 100 股（F4 用例），填得比额度大时按额度截断（F5 用例）。
//
// 本条只保留"持仓数量上限让位"这半：MaxPositions=1 在 dynamic 下仍然退场。
// English: §F-4 — the typed share count is now an upper bound (shrunk by the budget, never
// inflated). Only the "position-count cap yields" half of the old case survives here.
func TestPaperDynamicManualBuyRespectsTypedQty(t *testing.T) {
	pe := New(dynPaperConfig(100000), "")
	pe.SetSizingContext(1.0, 1.0, 1.0, 1.0, false)
	quotes := dynQuotes("600000.SH")
	// 显式传 100 股：dynamic 下按 100 股成交（旧行为是 3500 股）。
	if err := pe.BuyExInPool("600000.SH", "浦发", "龙头", "", 10, 0, 100, quotes); err != nil {
		t.Fatalf("BuyExInPool: %v", err)
	}
	poses := pe.Positions()
	if len(poses) != 1 || poses[0].Qty != 100 {
		t.Fatalf("dynamic 下手填 100 股应原样成交 100 股（只压不抬），got %+v", poses)
	}
	// MaxPositions=1：dynamic 下继续开第二只不受数量上限限制。
	if err := pe.BuyExInPool("600001.SH", "华夏", "龙头", "", 10, 0, 100, dynQuotes("600001.SH")); err != nil {
		t.Fatalf("dynamic 下持仓数量上限应让位: %v", err)
	}
	if len(pe.Positions()) != 2 {
		t.Fatalf("应允许第二只持仓，got %d", len(pe.Positions()))
	}
}

// TestPaperDynamicPoolBaseAndWholeBookDenominator 基数=池权益，35% 分母=全盘权益。
func TestPaperDynamicPoolBaseAndWholeBookDenominator(t *testing.T) {
	pe := New(dynPaperConfig(300000), "")
	// 同 v4 说明：单笔基准放大到 100%，本例才考得到"集中度分母是**全盘权益**而不是池权益"。
	pe.SetSizingContext(1.0, 1.0, 1.0, 1.0, false)
	pe.SetStrategyPools([]string{"dragon"}) // 现金均分：dragon=150000，其他池=150000
	quotes := dynQuotes("600000.SH")
	if err := pe.BuyInPool("600000.SH", "浦发", "龙头", "dragon", 10, quotes); err != nil {
		t.Fatalf("BuyInPool: %v", err)
	}
	poses := pe.Positions()
	if len(poses) != 1 {
		t.Fatalf("应有 1 笔持仓, got %d", len(poses))
	}
	// 池权益 15 万 × eff(0.50) = 7.5 万 → 7500 股；全盘 35% = 10.5 万不参与（7.5 万 < 10.5 万）。
	// 若错误地用池权益做分母（15万×35%=5.25万）会得到 5250 股 —— 那才是本条要抓的形态。
	if poses[0].Qty != 7500 {
		t.Fatalf("单笔基准应按池权益计（7500 股），got %d", poses[0].Qty)
	}
}

// TestPaperDynamicManualSellClosesAll dynamic 手动卖出按清仓处理（不保留尾仓）。
func TestPaperDynamicManualSellClosesAll(t *testing.T) {
	pe := New(dynPaperConfig(100000), "")
	quotes := dynQuotes("600000.SH")
	if err := pe.Buy("600000.SH", "浦发", "龙头", 10, quotes); err != nil {
		t.Fatalf("Buy: %v", err)
	}
	// T+1 是保留的合规闸：把成交日改到昨日（层账批次一并回拨），再验证 dynamic 手动卖出全平。
	pe.mu.Lock()
	for _, p := range pe.positions {
		t1Backdate(p, time.Now().AddDate(0, 0, -1))
	}
	pe.mu.Unlock()
	if err := pe.Sell("600000.SH", quotes); err != nil {
		t.Fatalf("Sell: %v", err)
	}
	if len(pe.Positions()) != 0 {
		t.Fatalf("卖出后不应有持仓，got %+v", pe.Positions())
	}
}

// TestPaperDynamicConcentrationShrinksThenSkips §9.2：连续买入第二笔被减额、第三笔额度不足 1 手跳过。
// 全盘权益 10 万 ⇒ 单票 35% = 3.5 万；股价 10 元 ⇒ 上限 3500 股。
// English: consecutive buys shrink to the remaining 35% room, and the third skips below one lot.
func TestPaperDynamicConcentrationShrinksThenSkips(t *testing.T) {
	pe := New(dynPaperConfig(100000), "")
	pe.SetSizingContext(1.0, 1.0, 1.0, 1.0, false) // 单笔基准放大（lo=hi=1.0→硬夹 0.50），让单票 35% 额度成为约束
	// 第一笔：3500 股（吃满单票额度）。
	if err := pe.Buy("600000.SH", "浦发", "龙头", 10, dynQuotes("600000.SH")); err != nil {
		t.Fatalf("第一笔 Buy: %v", err)
	}
	if got := pe.Positions()[0].Qty; got != 3500 {
		t.Fatalf("第一笔应为 3500 股，got %d", got)
	}
	// 第二笔（加仓，dynamic 下允许）：剩余额度 0 → 必须跳过而不是再买 1 手。
	if err := pe.Buy("600000.SH", "浦发", "龙头", 10, dynQuotes("600000.SH")); err == nil {
		t.Fatalf("额度耗尽时应跳过（1 手地板不得突破 35%%），got %+v", pe.Positions())
	}
	if got := pe.Positions()[0].Qty; got != 3500 {
		t.Fatalf("被拒后持仓不得变化，got %d", got)
	}
}

// TestPaperLegacyStillUsesFixedAmount legacy 路径保持固定金额（证明 dynamic 是显式开关）。
func TestPaperLegacyStillUsesFixedAmount(t *testing.T) {
	pe := New(Config{Enabled: true, InitialCapital: 100000, FixedAmount: 20000}, "")
	if err := pe.Buy("600000.SH", "浦发", "龙头", 10, dynQuotes("600000.SH")); err != nil {
		t.Fatalf("Buy: %v", err)
	}
	poses := pe.Positions()
	if len(poses) != 1 || poses[0].Qty != 2000 {
		t.Fatalf("legacy 固定金额 20000/10 = 2000 股，got %+v", poses)
	}
}
