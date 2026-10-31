// cash_scope_test.go — §MULTI-ADMIN P1：风控闸资金读取的 legacy/own 双口径。
package risk

import (
	"math"
	"path/filepath"
	"testing"

	"quant-trading-v2/internal/store"
)

func TestGateCashScope(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "live.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.UpsertRealAccount(store.RealAccount{UserID: "", AvailableCash: 777}); err != nil {
		t.Fatalf("seed global: %v", err)
	}
	g := NewGate(db, "u2", nil)
	if acc, err := g.cashAccount(); err != nil || math.Abs(acc.AvailableCash) > 0.005 {
		t.Fatalf("非 operator strict 不应读全局行, got %.2f err=%v", acc.AvailableCash, err)
	}
	g.SetLegacyCashGlobal(true)
	if acc, err := g.cashAccount(); err != nil || math.Abs(acc.AvailableCash-777) > 0.005 {
		t.Fatalf("legacy 开关应读全局行 777, got %.2f err=%v", acc.AvailableCash, err)
	}
}
