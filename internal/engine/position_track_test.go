// C3 买入信号自动纸面开仓测试：开仓写盘激活离场路径、幂等去重、止盈/止损映射、开关控制。
// English: C3 tests for auto paper-opening on buy signals: writing positions on disk activates exit paths, idempotent dedup, take-profit/stop-loss mapping, and switch control.
package engine

import (
	"path/filepath"
	"testing"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/report"
)

func newTrackEngine(t *testing.T) (*Engine, *config.Manager) {
	t.Helper()
	cfgMgr := config.NewManager(filepath.Join(t.TempDir(), "config.json"))
	e := &Engine{cfgMgr: cfgMgr}
	e.rpt = report.New(filepath.Join(t.TempDir(), "rpt.json"))
	return e, cfgMgr
}

// TestPaperOpenTpSl 战法→止盈/止损百分比映射（比例源 ×100，默认值兜底）。
// English: TestPaperOpenTpSl strategy→take-profit/stop-loss percentage mapping (ratio source ×100, defaults as fallback).
func TestPaperOpenTpSl(t *testing.T) {
	sc := config.NewManager("").GetStrategyConfig()
	if tp, sl := paperOpenTpSl("dragon", sc); tp != 10 || sl != 8 {
		t.Errorf("dragon 应 10/8, got %.0f/%.0f", tp, sl)
	}
	if tp, sl := paperOpenTpSl("double_bump", sc); tp != 15 || sl != 8 {
		t.Errorf("double_bump 应 15/8, got %.0f/%.0f", tp, sl)
	}
	if tp, sl := paperOpenTpSl("n_shape", sc); tp != 10 || sl != 8 {
		t.Errorf("n_shape 应 10/8, got %.0f/%.0f", tp, sl)
	}
	if tp, sl := paperOpenTpSl("dragon_return", sc); tp != 25 || sl != 5 {
		t.Errorf("dragon_return 应 25/5, got %.0f/%.0f", tp, sl)
	}
	if tp, sl := paperOpenTpSl("手动", nil); tp != 10 || sl != 8 {
		t.Errorf("默认 应 10/8, got %.0f/%.0f", tp, sl)
	}
}

// TestPaperOpenBuyOpensAndIdempotent 纸面开仓写入持仓记录（dragon 补 limit_price 与止盈/止损），
// 且对同一代码幂等（不重复开仓）。
// English: TestPaperOpenBuyOpensAndIdempotent paper-opening writes a position record (dragon adds limit_price and take-profit/stop-loss),
// and is idempotent for the same code (no duplicate open).
func TestPaperOpenBuyOpensAndIdempotent(t *testing.T) {
	e, _ := newTrackEngine(t)
	sig := combat_agent.Signal{ID: "s1", Code: "600001", Name: "测试", Strategy: "dragon", Price: 11.2}
	if !e.paperOpenBuy(sig) {
		t.Fatal("首次应开仓成功")
	}
	held := e.rpt.HeldPositions()
	if len(held) != 1 || held[0].Code != "600001" || held[0].Strategy != "dragon" {
		t.Fatalf("应持有一笔 dragon 持仓, got %+v", held)
	}
	if held[0].TakeProfitPct != 10 || held[0].StopLossPct != 8 {
		t.Errorf("dragon 止盈/止损应为 10/8, got %.0f/%.0f", held[0].TakeProfitPct, held[0].StopLossPct)
	}
	if lp, ok := held[0].EntryMeta["limit_price"]; !ok || lp != 11.2 {
		t.Errorf("dragon 应记录 limit_price=11.2, got %v/%v", lp, ok)
	}
	if e.paperOpenBuy(sig) {
		t.Fatal("幂等逻辑：已持仓代码第二次不应开仓")
	}
	if n := len(e.rpt.HeldPositions()); n != 1 {
		t.Fatalf("重复开仓应被幂等拦截, held=%d", n)
	}
}

// TestPaperOpenBuySkipsInvalid 无现价/空代码时不开仓。
// English: TestPaperOpenBuySkipsInvalid does not open when there is no current price / empty code.
func TestPaperOpenBuySkipsInvalid(t *testing.T) {
	e, _ := newTrackEngine(t)
	if e.paperOpenBuy(combat_agent.Signal{Code: "600001", Strategy: "dragon", Price: 0}) {
		t.Fatal("现价无效不应开仓")
	}
	if e.paperOpenBuy(combat_agent.Signal{Code: "", Strategy: "dragon", Price: 10}) {
		t.Fatal("空代码不应开仓")
	}
}

// TestPaperOpenQty C6 仓位：置信度越高、止损越窄 → 数量越多；单位风险一致。
// English: TestPaperOpenQty C6 sizing: higher confidence, narrower stop-loss → more quantity; consistent unit risk.
func TestPaperOpenQty(t *testing.T) {
	if q := paperOpenQty(1.0, 8); q != 10 {
		t.Errorf("满置信 8%% 止损应为 10, got %.2f", q)
	}
	if q := paperOpenQty(0.6, 8); q != 6 {
		t.Errorf("0.6 置信 8%% 止损应为 6, got %.2f", q)
	}
	// 更窄止损 → 更多数量（单位风险一致）
	// English: Narrower stop-loss → more quantity (consistent unit risk).
	if q := paperOpenQty(0.6, 4); q != 12 {
		t.Errorf("0.6 置信 4%% 止损应为 12, got %.2f", q)
	}
	// 无效输入回退默认
	// English: Invalid input falls back to default.
	if q := paperOpenQty(0, 0); q != 5 {
		t.Errorf("无效置信度/止损应回退 5, got %.2f", q)
	}
}

// TestPaperOpenBuyATRSizing 纸面开仓数量按 ATR 止损距离赋值：ATR 有效时（止损更窄）
// 数量高于固定止损口径，且 Quantity 正确写入持仓记录。
// English: TestPaperOpenBuyATRSizing paper-open quantity is set from the ATR stop-loss distance: when ATR is valid (narrower stop)
// the quantity is higher than the fixed stop-loss basis, and Quantity is correctly written to the position record.
func TestPaperOpenBuyATRSizing(t *testing.T) {
	e, _ := newTrackEngine(t)
	sig := combat_agent.Signal{
		ID: "s2", Code: "600002", Name: "窄波动", Strategy: "dragon",
		Price: 10, Confidence: 0.8, ATR: 0.15, // 2.5×0.15/10×100 = 3.75% 止损
		// English: 2.5×0.15/10×100 = 3.75% stop-loss.
	}
	if !e.paperOpenBuy(sig) {
		t.Fatal("应开仓成功")
	}
	held := e.rpt.HeldPositions()
	if len(held) != 1 {
		t.Fatalf("应持有一笔持仓, got %d", len(held))
	}
	want := paperOpenQty(0.8, 0.15*2.5/10*100) // ≈17.07
	if held[0].Quantity != want {
		t.Errorf("数量应按 ATR 止损 3.75%% 计算: want %.2f, got %.2f", want, held[0].Quantity)
	}
	// ATR 缺失 → 回退固定 8% 止损口径
	// English: Missing ATR → falls back to the fixed 8% stop-loss basis.
	e2, _ := newTrackEngine(t)
	sig2 := sig
	sig2.ID = "s3"
	sig2.ATR = 0
	if !e2.paperOpenBuy(sig2) {
		t.Fatal("应开仓成功")
	}
	if got := e2.rpt.HeldPositions()[0].Quantity; got != paperOpenQty(0.8, 8) {
		t.Errorf("无 ATR 应回退固定 8%%: want %.2f, got %.2f", paperOpenQty(0.8, 8), got)
	}
}

// TestAutoTrackDisabled 开关关闭时不纸面开仓。
// English: TestAutoTrackDisabled does not paper-open when the switch is off.
func TestAutoTrackDisabled(t *testing.T) {
	e, cfgMgr := newTrackEngine(t)
	cfgMgr.SetStrategyConfigFor("", &config.StrategyConfig{})
	e.SetUserID("")
	cfg := cfgMgr.GetRulesFor("")
	cfg.Position.AutoTrackSignals = false
	cfgMgr.Save()
	if e.autoTrackEnabled() {
		t.Fatal("关闭 AutoTrackSignals 后不应自动开仓")
	}
}
