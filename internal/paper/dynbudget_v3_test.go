// dynbudget_v3_test.go — 模拟盘 dynamic 预算锁（v3 §9.2）。
//
// 验证：手动买入忽略手填股数、池权益为基数、全盘权益为 35% 分母、持仓数量上限让位、
// 手动卖出按清仓处理。
//
// English: paper-book locks for the v3 dynamic budget — manual buys ignore typed qty, pool equity
// is the base, whole-book equity is the 35% denominator, position caps yield, manual sells close.
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

// TestPaperDynamicManualBuyIgnoresQtyAndCaps 手填股数与持仓上限都不再生效，预算由池权益/35% 决定。
func TestPaperDynamicManualBuyIgnoresQtyAndCaps(t *testing.T) {
	pe := New(dynPaperConfig(100000), "")
	quotes := dynQuotes("600000.SH")
	// 显式传 100 股：dynamic 下必须忽略，按 10 万×1.0 再受 35% 单票额度 → 3500 股。
	if err := pe.BuyExInPool("600000.SH", "浦发", "龙头", "", 10, 0, 100, quotes); err != nil {
		t.Fatalf("BuyExInPool: %v", err)
	}
	poses := pe.Positions()
	if len(poses) != 1 || poses[0].Qty != 3500 {
		t.Fatalf("dynamic 应忽略手填 100 股并按 35%% 额度买 3500 股，got %+v", poses)
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
	pe.SetStrategyPools([]string{"dragon"}) // 现金均分：dragon=150000，其他池=150000
	quotes := dynQuotes("600000.SH")
	if err := pe.BuyInPool("600000.SH", "浦发", "龙头", "dragon", 10, quotes); err != nil {
		t.Fatalf("BuyInPool: %v", err)
	}
	poses := pe.Positions()
	if len(poses) != 1 {
		t.Fatalf("应有 1 笔持仓, got %d", len(poses))
	}
	// 池权益 15 万 → raw 15 万；全盘 35% = 10.5 万 → 10500 股。
	// 若错误地用池权益做分母会得到 15万×35%=5.25万（5250 股）。
	if poses[0].Qty != 10500 {
		t.Fatalf("35%% 分母应为全盘权益（10500 股），got %d", poses[0].Qty)
	}
}

// TestPaperDynamicManualSellClosesAll dynamic 手动卖出按清仓处理（不保留尾仓）。
func TestPaperDynamicManualSellClosesAll(t *testing.T) {
	pe := New(dynPaperConfig(100000), "")
	quotes := dynQuotes("600000.SH")
	if err := pe.Buy("600000.SH", "浦发", "龙头", 10, quotes); err != nil {
		t.Fatalf("Buy: %v", err)
	}
	// T+1 是保留的合规闸：把成交日改到昨日，再验证 dynamic 手动卖出全平。
	pe.mu.Lock()
	for _, p := range pe.positions {
		p.FilledAt = time.Now().AddDate(0, 0, -1)
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
