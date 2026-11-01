// f4_manual_qty_cap_test.go — §F-4（2026-11-01）手动股数上限 + 单笔金额绝对帽。
//
// 这一批修的是两个"读数与成交对不上"的缺陷：
//
//	F3 ConfigFromRules 必须从账号 risk_gate 取 MaxOrderAmount（单一构建器，四个装配点同源）
//	F4 dynamic 下手动买入的显式股数是**意图上限**，只压不抬（旧行为是整个丢掉，差 35 倍）
//	F5 显式股数超过动态额度时按额度截断，且 capped_by=manual 要透传到决策快照
//	F6 单笔成交额绝对帽：与实盘同阈值、同语义（拒单），0=关；新开/加仓/自动三条路都过
//
// English: §F-4 — the manual share count becomes an upper bound, and the absolute notional cap is
// wired from the account's risk gate with live-parity reject semantics.
package paper

import (
	"errors"
	"strings"
	"testing"

	"quant-trading-v2/internal/config"
)

// paperRulesForTest §F-4 装配用例的最小 rules.paper（其余字段走零值归一路径）。
func paperRulesForTest() config.PaperConfig {
	return config.PaperConfig{Enabled: true, FixedAmount: 10000}
}

// riskGateWith 只开单笔绝对帽的 risk_gate（其余闸保持零值=关）。
func riskGateWith(cap float64) config.RiskGateConfig {
	return config.RiskGateConfig{MaxOrderAmount: cap}
}

// f4Gate 造一个只开单笔绝对帽的 risk_gate（其余闸保持零值=关）。
func f4Gate(cap float64) Config {
	c := dynPaperConfig(100000)
	c.MaxOrderAmount = cap
	return c
}

// F3 ConfigFromRules 从 RiskGate.MaxOrderAmount 取值，负值归零（0 与负数都表示"这道闸关着"）。
//
// 反证靶心：如果构建器不接 risk_gate（旧签名），这条会拿到 0——纸面永远不吃帽，
// 而实盘同一笔单被拒，两本账从此一个持仓一个没有。
func TestF3ConfigFromRulesTakesMaxOrderAmount(t *testing.T) {
	cfg := ConfigFromRules(
		paperRulesForTest(),
		riskGateWith(20000),
	)
	if cfg.MaxOrderAmount != 20000 {
		t.Fatalf("MaxOrderAmount 应从 risk_gate 取 20000，实得 %v", cfg.MaxOrderAmount)
	}
	// 未配置（零值）= 闸关，必须原样是 0，不能回落到任何出厂默认帽。
	if got := ConfigFromRules(paperRulesForTest(), riskGateWith(0)).MaxOrderAmount; got != 0 {
		t.Fatalf("risk_gate 未配置时 MaxOrderAmount 应为 0（闸关），实得 %v", got)
	}
	// 负阈值必须归零：-1 若被当成"极小的帽"，会把所有单子拒光（配置脏写不该变成停机）。
	if got := ConfigFromRules(paperRulesForTest(), riskGateWith(-1)).MaxOrderAmount; got != 0 {
		t.Fatalf("负阈值应归零（闸关），实得 %v", got)
	}
}

// F4 dynamic 下手动买入的显式股数必须被尊重：填 100 股就成交 100 股。
//
// 旧行为：100 股被整个丢掉，按池权益 50% 再被 35% 单票额度截到 3500 股（差 35 倍）。
// 决策快照的 capped_by 必须如实报 manual —— 是"用户要这么多"在约束，不是额度在约束。
func TestF4DynamicManualBuyHonoursTypedQty(t *testing.T) {
	pe := New(f4Gate(0), "")
	pe.SetSizingContext(1.0, 1.0, 1.0, 1.0, false) // 单笔基准放大，让额度本来允许 3500 股
	var recs []DecisionRecord
	pe.SetDecisionRecorder(func(r DecisionRecord) { recs = append(recs, r) })
	if err := pe.BuyExInPool("600000.SH", "浦发", "龙头", "", 10, 0, 100, dynQuotes("600000.SH")); err != nil {
		t.Fatalf("BuyExInPool: %v", err)
	}
	poses := pe.Positions()
	if len(poses) != 1 || poses[0].Qty != 100 {
		t.Fatalf("手填 100 股应成交 100 股（额度本来允许 3500），got %+v", poses)
	}
	if len(recs) == 0 {
		t.Fatal("未收到决策快照")
	}
	last := recs[len(recs)-1]
	if last.Qty != 100 {
		t.Fatalf("决策快照的 Qty 必须与实际成交一致（100），got %d", last.Qty)
	}
	if last.CappedBy != "manual" {
		t.Fatalf("显式股数生效时 capped_by 应为 manual，got %q", last.CappedBy)
	}
}

// F5 显式股数**大于**动态额度时，按额度截断（受帽），不能反过来把额度顶开。
//
// 这是"只压不抬"的另一半：若实现成"直接用显式股数"，手动入口就绕过了 35% 单票额度。
func TestF5TypedQtyCappedByDynamicRoom(t *testing.T) {
	pe := New(f4Gate(0), "")
	pe.SetSizingContext(1.0, 1.0, 1.0, 1.0, false) // 10 万×50% → 5 万，再被 35% 单票额度截到 3.5 万 = 3500 股
	if err := pe.BuyExInPool("600000.SH", "浦发", "龙头", "", 10, 0, 999900, dynQuotes("600000.SH")); err != nil {
		t.Fatalf("BuyExInPool: %v", err)
	}
	poses := pe.Positions()
	if len(poses) != 1 || poses[0].Qty != 3500 {
		t.Fatalf("填 999900 股应被 35%% 额度截到 3500 股，got %+v", poses)
	}
}

// F6 单笔成交额绝对帽：超帽拒单、恰好等于帽放行、0=关；新开/加仓/自动三条路都要过。
func TestF6MaxOrderAmountCapRejects(t *testing.T) {
	// ① 手动新开仓：300 股 × 10 元 = 3000 > 帽 2000 → 拒，且不得留下任何持仓。
	pe := New(f4Gate(2000), "")
	pe.SetSizingContext(1.0, 1.0, 1.0, 1.0, false)
	err := pe.BuyExInPool("600000.SH", "浦发", "龙头", "", 10, 0, 300, dynQuotes("600000.SH"))
	if err == nil {
		t.Fatal("3000 元 > 绝对帽 2000 元，应拒单")
	}
	var be *BudgetError
	if !errors.As(err, &be) {
		t.Fatalf("拒单必须是 *BudgetError（server 要展开 detail），got %T: %v", err, err)
	}
	if be.CappedBy != "max_order_amount" {
		t.Fatalf("capped_by 应为 max_order_amount，got %q", be.CappedBy)
	}
	if !strings.HasPrefix(err.Error(), "单笔金额超限") {
		t.Fatalf("文案前缀应为「单笔金额超限」（不能套「动态仓位:」把排障引偏），got %q", err.Error())
	}
	if len(pe.Positions()) != 0 {
		t.Fatalf("拒单后不得留下持仓，got %+v", pe.Positions())
	}

	// ② 恰好等于帽（200 股 × 10 = 2000）必须放行：帽是"不超过"，不是"小于"。
	if err := pe.BuyExInPool("600000.SH", "浦发", "龙头", "", 10, 0, 200, dynQuotes("600000.SH")); err != nil {
		t.Fatalf("恰好等于帽应放行: %v", err)
	}
	// ③ 加仓同样过帽：再加 300 股（3000 > 2000）必须拒，持仓不得变化。
	if err := pe.BuyExInPool("600000.SH", "浦发", "龙头", "", 10, 0, 300, dynQuotes("600000.SH")); err == nil {
		t.Fatal("加仓 3000 元 > 帽 2000 元，应拒单（否则可以先买一笔、再加一笔绕过）")
	}
	if got := pe.Positions()[0].Qty; got != 200 {
		t.Fatalf("加仓被拒后持仓不得变化，got %d", got)
	}

	// ④ 帽=0（未配置）时同一笔必须放行：零值语义是"这道闸关着"，不是"帽为 0 元"。
	open := New(f4Gate(0), "")
	open.SetSizingContext(1.0, 1.0, 1.0, 1.0, false)
	if err := open.BuyExInPool("600000.SH", "浦发", "龙头", "", 10, 0, 300, dynQuotes("600000.SH")); err != nil {
		t.Fatalf("帽未配置（0）时不应拦单: %v", err)
	}
}

// F6b 自动通道（legacy 固定金额）同样吃绝对帽：实盘闸门对自动单也是硬拒。
//
// 这条刻意放在 legacy 分支：dynamic 分支的帽在预算层判（见 ordbudget.MaxNotional），
// legacy 没有预算层，只能由 fillLocked 兜——两条路都要有，否则"关掉动态仓位"就绕过了帽。
func TestF6MaxOrderAmountCapAppliesToLegacyAuto(t *testing.T) {
	cfg := Config{Enabled: true, InitialCapital: 100000, FixedAmount: 20000, MaxOrderAmount: 5000}
	pe := New(cfg, "")
	err := pe.Buy("600000.SH", "浦发", "龙头", 10, dynQuotes("600000.SH"))
	if err == nil {
		t.Fatal("固定金额 20000 元 > 帽 5000 元，自动买入应拒单")
	}
	if len(pe.Positions()) != 0 {
		t.Fatalf("拒单后不得留下持仓，got %+v", pe.Positions())
	}
	// 把帽放开后同一笔必须能成交（证明上面拒的是帽，不是别的原因）。
	pe.UpdateConfig(Config{Enabled: true, InitialCapital: 100000, FixedAmount: 20000})
	if err := pe.Buy("600000.SH", "浦发", "龙头", 10, dynQuotes("600000.SH")); err != nil {
		t.Fatalf("帽放开后应能成交: %v", err)
	}
	if got := pe.Positions()[0].Qty; got != 2000 {
		t.Fatalf("20000/10 = 2000 股，got %d", got)
	}
}
