// cash_scope_test.go — §MULTI-ADMIN P1：控制器的账号资金读取口径。
package trading

// §SESSION-WINDOW：本文件 Controller 构造统一走 newPinnedController（见 testclock_test.go），钉到当日北京时 10:15；不固定会在 09:15–09:30 与 14:52 后假红。

import (
	"math"
	"path/filepath"
	"testing"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/store"
)

func TestControllerCashScope(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "live.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.UpsertRealAccount(store.RealAccount{UserID: "", AvailableCash: 777}); err != nil {
		t.Fatalf("seed global: %v", err)
	}
	c := newPinnedController(nil, db, "u2", config.QMTConfig{}, nil)
	if acc, err := c.GetRealAccountFor("u2"); err != nil || math.Abs(acc.AvailableCash) > 0.005 {
		t.Fatalf("非 operator strict 不应读全局行, got %.2f err=%v", acc.AvailableCash, err)
	}
	c.SetLegacyCashGlobal(true)
	if acc, err := c.GetRealAccountFor("u2"); err != nil || math.Abs(acc.AvailableCash-777) > 0.005 {
		t.Fatalf("legacy 开关应读全局行 777, got %.2f err=%v", acc.AvailableCash, err)
	}
	if v, err := c.TotalAssetsFor("u2"); err != nil || math.Abs(v-777) > 0.005 {
		t.Fatalf("legacy total 应 777, got %.2f err=%v", v, err)
	}
}
