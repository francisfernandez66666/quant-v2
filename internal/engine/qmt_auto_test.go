// AUTO_TRADING_PLAN M1：autoPlace 自动下单测试。
// auto 模式做多信号直连网关下单（金额按 fixed_amount 折算整手、code 补后缀、白名单/熔断/手动模式跳过）。
// English: M1 autoPlace auto-order tests — in auto mode a long signal is placed straight to the gateway
// (qty from fixed_amount as whole lots, code exchange suffix, strategy whitelist / tripped / manual-skip).
package engine

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/store"
	"quant-trading-v2/internal/trading"
)

// newQMTEngine 构建带 QMT 控制器的引擎（httptest 网关记录 /order 请求）。
func newQMTEngine(t *testing.T, mutate func(*config.QMTConfig)) (*Engine, *store.DB, *httptest.Server, *[]map[string]interface{}) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "qmt.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	var mu sync.Mutex
	orders := []map[string]interface{}{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.Write([]byte(`{"ok":true,"ts":"now"}`))
			return
		}
		if r.URL.Path == "/order" {
			var req map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, `{"ok":false,"err":"bad body"}`, 400)
				return
			}
			mu.Lock()
			orders = append(orders, req)
			mu.Unlock()
			w.Write([]byte(`{"ok":true,"order_id":"GW1"}`))
			return
		}
		w.Write([]byte(`{"ok":false,"err":"not found"}`))
	}))
	t.Cleanup(srv.Close)

	cfg := config.DefaultQMTConfig()
	cfg.Enabled = true
	cfg.Mode = "auto"
	cfg.GatewayURL = srv.URL
	cfg.FixedAmount = 10000
	cfg.MissHeartbeatSec = 5
	if mutate != nil {
		mutate(&cfg)
	}
	exec := trading.NewQMTClient(srv.URL, "", 2*time.Second, 0)
	ctrl := trading.NewController(exec, db, "u_1", cfg, nil)
	e := &Engine{}
	e.SetQMT(ctrl, db)
	return e, db, srv, &orders
}

// TestAutoPlacePlacesOrder 验证 auto 模式做多信号直下网关：金额折算整手、code 补后缀、信号 ID 幂等键。
func TestAutoPlacePlacesOrder(t *testing.T) {
	e, _, _, orders := newQMTEngine(t, nil)
	sig := combat_agent.Signal{ID: "SIG1", Code: "600000", Name: "浦发", Strategy: "龙头", Direction: "做多", Price: 10}
	e.autoPlace(sig, map[string]*data.StockInfo{"600000": {Code: "600000", Price: 10}})
	if len(*orders) != 1 {
		t.Fatalf("expected 1 order, got %d", len(*orders))
	}
	o := (*orders)[0]
	if o["signal_id"] != "SIG1" {
		t.Fatalf("signal_id: %v", o["signal_id"])
	}
	if o["code"] != "600000.SH" {
		t.Fatalf("code should get .SH suffix, got %v", o["code"])
	}
	if o["side"] != "买入" {
		t.Fatalf("side: %v", o["side"])
	}
	// fixed_amount=10000 / price=10 → 1000 股（整手）
	if o["qty"].(float64) != 1000 {
		t.Fatalf("qty from fixed_amount: %v", o["qty"])
	}
	if o["price"].(float64) != 10 {
		t.Fatalf("price: %v", o["price"])
	}
}

// TestAutoPlaceSkipsWhenManual 手动模式不下单。
func TestAutoPlaceSkipsWhenManual(t *testing.T) {
	e, _, _, orders := newQMTEngine(t, func(c *config.QMTConfig) { c.Mode = "manual" })
	e.autoPlace(combat_agent.Signal{ID: "S1", Code: "000001", Strategy: "龙头", Direction: "做多", Price: 10}, nil)
	if len(*orders) != 0 {
		t.Fatalf("manual mode should not place, got %d", len(*orders))
	}
}

// TestAutoPlaceSkipsWhitelist 战法白名单外不下单。
func TestAutoPlaceSkipsWhitelist(t *testing.T) {
	e, _, _, orders := newQMTEngine(t, func(c *config.QMTConfig) { c.Strategies = []string{"N形"} })
	e.autoPlace(combat_agent.Signal{ID: "S1", Code: "000001", Strategy: "龙头", Direction: "做多", Price: 10}, nil)
	if len(*orders) != 0 {
		t.Fatalf("whitelist excludes 龙头, should skip, got %d", len(*orders))
	}
	// 白名单命中 → 下单
	e.autoPlace(combat_agent.Signal{ID: "S2", Code: "000002", Strategy: "N形", Direction: "做多", Price: 10}, nil)
	if len(*orders) != 1 {
		t.Fatalf("whitelist includes N形, should place, got %d", len(*orders))
	}
}

// TestAutoPlaceSkipsTripped 熔断中跳过。
func TestAutoPlaceSkipsTripped(t *testing.T) {
	e, _, _, orders := newQMTEngine(t, nil)
	// 直接置熔断（网关不可达模拟由 controller 负责；此处验证 autoPlace 熔断前置）
	ctrl := e.QMTController()
	ctrl.SetTripped("test")
	e.autoPlace(combat_agent.Signal{ID: "S1", Code: "000001", Strategy: "龙头", Direction: "做多", Price: 10}, nil)
	if len(*orders) != 0 {
		t.Fatalf("tripped should skip, got %d", len(*orders))
	}
}

// TestAutoPlacePriceFromLive 现价优先于信号触发价。
func TestAutoPlacePriceFromLive(t *testing.T) {
	e, _, _, orders := newQMTEngine(t, nil)
	sig := combat_agent.Signal{ID: "S1", Code: "300750", Name: "宁德", Strategy: "龙头", Direction: "做多", Price: 100}
	e.autoPlace(sig, map[string]*data.StockInfo{"300750": {Code: "300750", Price: 105}})
	if len(*orders) != 1 {
		t.Fatalf("expected 1 order, got %d", len(*orders))
	}
	o := (*orders)[0]
	if o["code"] != "300750.SZ" {
		t.Fatalf("code: %v", o["code"])
	}
	if o["price"].(float64) != 105 {
		t.Fatalf("should use live price 105, got %v", o["price"])
	}
	if o["qty"].(float64) != 100 { // 10000/105 → 95 → 不足一手兜底 100
		t.Fatalf("qty floor to one lot: %v", o["qty"])
	}
}

// TestAutoPlaceIdempotent 同一 signal_id 重复触发不重复下单（orders 表唯一键）。
func TestAutoPlaceIdempotent(t *testing.T) {
	e, _, _, orders := newQMTEngine(t, nil)
	sig := combat_agent.Signal{ID: "S1", Code: "600000", Name: "浦发", Strategy: "龙头", Direction: "做多", Price: 10}
	e.autoPlace(sig, nil)
	e.autoPlace(sig, nil)
	if len(*orders) != 1 {
		t.Fatalf("idempotent: expected 1 gateway call, got %d", len(*orders))
	}
}