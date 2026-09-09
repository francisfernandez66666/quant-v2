// shadow_test.go — §WS-G ShadowExecutor 单测：决策落 shadow_orders、回执受理、永不真下、幂等去重。
// English: §WS-G ShadowExecutor tests — decisions persisted to shadow_orders, echoed as accepted,
// never placed for real, deduped on signal_id.
package trading

import (
	"testing"

	"quant-trading-v2/internal/store"
)

// TestShadowExecutorRecordsAndEchoes 影子下单落账 + 回执受理成功。
func TestShadowExecutorRecordsAndEchoes(t *testing.T) {
	db := testDB(t)
	s := NewShadowExecutor(db, "u_shadow")
	req := OrderRequest{
		SignalID: "buy:600000.SH:n_shape:2026-09-08", Code: "600000.SH", Name: "浦发",
		Strategy: "N形", StrategyID: "n_shape", Side: SideBuy,
		Price: 10, Qty: 100, Amount: 1000, CreatedAt: "2026-09-08 09:35:00",
	}
	res, err := s.PlaceBuy(req)
	if err != nil || res == nil || !res.OK || res.OrderID == "" {
		t.Fatalf("影子买单应回执受理, got res=%+v err=%v", res, err)
	}
	day := "2026-09-08"
	rows, err := db.ShadowOrdersForDay("u_shadow", day, 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("应落账 1 条, got %d err=%v", len(rows), err)
	}
	if rows[0].Code != "600000.SH" || rows[0].Side != SideBuy || rows[0].Qty != 100 {
		t.Fatalf("影子记录字段不符: %+v", rows[0])
	}
}

// TestShadowExecutorIdempotent 同 signal_id 影子决策只记一次（幂等）。
func TestShadowExecutorIdempotent(t *testing.T) {
	db := testDB(t)
	s := NewShadowExecutor(db, "u_shadow")
	req := OrderRequest{
		SignalID: "sell:600519.SH:清仓:2026-09-08", Code: "600519.SH", Name: "茅台",
		Side: SideSell, Price: 1500, Qty: 100, Amount: 150000, CreatedAt: "2026-09-08 10:00:00",
	}
	if _, err := s.PlaceSell(req); err != nil {
		t.Fatalf("sell: %v", err)
	}
	if _, err := s.PlaceSell(req); err != nil {
		t.Fatalf("sell2: %v", err)
	}
	rows, _ := db.ShadowOrdersForDay("u_shadow", "2026-09-08", 10)
	if len(rows) != 1 {
		t.Fatalf("重复 signal_id 应只记 1 条, got %d", len(rows))
	}
}

// TestShadowExecutorNeverReal 影子执行器 State/Health 恒健康、恒空持仓（永不触达真实网关）。
func TestShadowExecutorNeverReal(t *testing.T) {
	s := NewShadowExecutor(nil, "u_shadow")
	st, err := s.State()
	if err != nil || st == nil || !st.Connected || len(st.Positions) != 0 {
		t.Fatalf("State 应恒连接+空持仓, got %+v err=%v", st, err)
	}
	ok, err := s.Health()
	if err != nil || !ok {
		t.Fatalf("Health 应恒健康, got ok=%v err=%v", ok, err)
	}
	if err := s.Cancel("anything"); err != nil {
		t.Fatalf("Cancel 应恒成功, got %v", err)
	}
	_ = store.ShadowOrder{}
}
