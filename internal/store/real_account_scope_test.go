// real_account_scope_test.go — §MULTI-ADMIN P1：账户资金读取的 legacy/own 双口径。
package store

import (
	"math"
	"path/filepath"
	"testing"
)

func TestRealAccountScoped(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "live.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.UpsertRealAccount(RealAccount{UserID: "u1", AvailableCash: 100}); err != nil {
		t.Fatalf("seed u1: %v", err)
	}
	if err := db.UpsertRealAccount(RealAccount{UserID: "", AvailableCash: 777}); err != nil {
		t.Fatalf("seed global: %v", err)
	}

	// 有自有行：legacy/own 都读自己的 100。
	if acc, _ := db.GetRealAccountScoped("u1", true); math.Abs(acc.AvailableCash-100) > 0.005 {
		t.Fatalf("u1 legacy 应 100, got %.2f", acc.AvailableCash)
	}
	if acc, _ := db.GetRealAccountOwn("u1"); math.Abs(acc.AvailableCash-100) > 0.005 {
		t.Fatalf("u1 own 应 100, got %.2f", acc.AvailableCash)
	}
	// 无自有行：operators 允许 legacy 读到 777；非 operator 的 strict 口径必须 0。
	if acc, _ := db.GetRealAccountScoped("u2", true); math.Abs(acc.AvailableCash-777) > 0.005 {
		t.Fatalf("u2 legacy 应 777, got %.2f", acc.AvailableCash)
	}
	if acc, _ := db.GetRealAccountOwn("u2"); math.Abs(acc.AvailableCash) > 0.005 {
		t.Fatalf("u2 own 不应读全局行, got %.2f", acc.AvailableCash)
	}
	if v, _ := db.TotalAssetsScoped("u2", false); math.Abs(v) > 0.005 {
		t.Fatalf("u2 strict total 不应含全局现金, got %.2f", v)
	}
}
