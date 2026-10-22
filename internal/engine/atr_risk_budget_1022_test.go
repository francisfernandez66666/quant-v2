// atr_risk_budget_1022_test.go — §C3 ATR 风险预算股数推导锁。
//
// 公式：qtyRisk = floor(equity × risk% / (ATR × ATRStopMult) / 100) × 100。
// ATR 越高、权益越小，股数越小；ATR/权益缺失时整条弃权（ok=false）。
//
// English: §C3 locks the risk-budget lot formula; higher ATR or lower equity shrinks the qty,
// and missing ATR/equity abstains.
package engine

import (
	"testing"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/store"
)

func TestATRRiskBudgetQty(t *testing.T) {
	db := newProbeTestDB(t)
	if err := db.UpsertRealAccount(store.RealAccount{UserID: "u", TotalAsset: 100000, AvailableCash: 100000}); err != nil {
		t.Fatalf("UpsertRealAccount: %v", err)
	}
	cm := config.NewManagerWithRules(&config.Rules{
		Position: config.PositionConfig{ATRStopMult: 2.5},
	})
	e := &Engine{userID: "u", realStore: db, cfgMgr: cm}

	// 100000 × 0.5% = 500 元风险预算；ATR=1 × 2.5 = 2.5 元止损距离 → 200 股。
	qty, equity, ok := e.atrRiskBudgetQty(combat_agent.Signal{ATR: 1}, 10, 0.5)
	if !ok || equity != 100000 || qty != 200 {
		t.Fatalf("ATR=1 应为 200 股, got qty=%d equity=%v ok=%v", qty, equity, ok)
	}
	// ATR 翻倍 → 止损距离翻倍 → 股数减半到 100。
	qty, _, ok = e.atrRiskBudgetQty(combat_agent.Signal{ATR: 2}, 10, 0.5)
	if !ok || qty != 100 {
		t.Fatalf("ATR=2 应为 100 股, got qty=%d ok=%v", qty, ok)
	}
	// ATR 缺失 → 弃权（不得伪造成 0 股）。
	if _, _, ok := e.atrRiskBudgetQty(combat_agent.Signal{ATR: 0}, 10, 0.5); ok {
		t.Fatal("ATR=0 应弃权")
	}
	// 无账本 → 弃权。
	eNoStore := &Engine{userID: "u", cfgMgr: cm}
	if _, _, ok := eNoStore.atrRiskBudgetQty(combat_agent.Signal{ATR: 1}, 10, 0.5); ok {
		t.Fatal("无实盘账本应弃权")
	}
}
