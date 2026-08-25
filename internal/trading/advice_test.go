// 持仓处理分析层测试（AUTO_TRADING_PLAN M1）：加仓/格局判定、卖出侧映射、熔断、幂等下单。
package trading

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/store"
)

func testDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// TestAdviseAddAndHold 验证加仓/格局判定：
//   - 有信号且回撤可控 → 加仓；
//   - 盈利但无信号（加仓要求信号活跃）→ 只格局。
func TestAdviseAddAndHold(t *testing.T) {
	db := testDB(t)
	// 两个持仓：600000 有信号且回撤小 → 加仓；000001 盈利无信号 → 格局
	if _, err := db.UpsertRealPositions([]store.RealPosition{
		{TsCode: "600000.SH", Name: "浦发", Qty: 100, CostPrice: 10, Amount: 1000, HighestPrice: 11, Strategy: "龙头"},
		{TsCode: "000001.SZ", Name: "平安", Qty: 100, CostPrice: 50, Amount: 5000, HighestPrice: 55, Strategy: "N形"},
	}); err != nil {
		t.Fatal(err)
	}
	positions, err := db.RealPositions()
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultQMTConfig()
	cfg.Advice.AddSignalActive = true
	cfg.Advice.AddReopenDrawdownPct = -5
	cfg.Advice.HoldMinProfitPct = 0

	scores := map[string]combat_agent.StockScores{
		"600000": {Code: "600000", SignalActive: true}, // 有信号
		"000001": {Code: "000001", SignalActive: false},
	}
	in := AdviceInput{
		Agent:     nil, // 卖出侧函数需真实 Agent，加仓/格局路径不依赖（nil 时直接跳过卖出侧）
		MarketAPI: nil,
		Positions: positions,
		Quotes: map[string]*data.StockInfo{
			"600000": {Code: "600000", Price: 10.8}, // 回撤 (10.8-11)/11 = -1.8% 可控
			"000001": {Code: "000001", Price: 52},   // 盈利 4%
		},
		DayKLines:    nil,
		Scores:       scores,
		MD:           nil,
		D1Scores:     nil,
		EmotionPhase: "高潮",
		Cfg:          cfg,
	}
	advices := Advise(in)
	if len(advices) != 2 {
		t.Fatalf("expected 2 advices, got %d: %+v", len(advices), advices)
	}
	got := map[string]string{}
	for _, a := range advices {
		got[a.Code] = a.Action
	}
	if got["600000"] != "加仓" {
		t.Fatalf("600000 应加仓，got %s", got["600000"])
	}
	if got["000001"] != "格局" {
		t.Fatalf("000001 应格局，got %s", got["000001"])
	}
}

// TestAdviseSkipAddWhenDrawdownExceeded 验证回撤超阈值时不再加仓（有信号也不加）。
func TestAdviseSkipAddWhenDrawdownExceeded(t *testing.T) {
	db := testDB(t)
	if _, err := db.UpsertRealPositions([]store.RealPosition{
		{TsCode: "600000.SH", Name: "浦发", Qty: 100, CostPrice: 10, Amount: 1000, HighestPrice: 11, Strategy: "龙头"},
	}); err != nil {
		t.Fatal(err)
	}
	positions, _ := db.RealPositions()
	cfg := config.DefaultQMTConfig()
	cfg.Advice.AddReopenDrawdownPct = -5
	in := AdviceInput{
		Positions: positions,
		Quotes:    map[string]*data.StockInfo{"600000": {Code: "600000", Price: 10.2}}, // 回撤 -7.3% 超 -5
		Scores:    map[string]combat_agent.StockScores{"600000": {Code: "600000", SignalActive: true}},
		Cfg:       cfg,
	}
	advices := Advise(in)
	// 盈利 (10.2-10)/10=2%>0 → 会走格局
	if len(advices) != 1 || advices[0].Action != "格局" {
		t.Fatalf("回撤超阈值应只格局，got %+v", advices)
	}
}

// TestControllerTripAndIdempotent 验证熔断与 signal_id 幂等。
func TestControllerTripAndIdempotent(t *testing.T) {
	db := testDB(t)
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch r.URL.Path {
		case "/health":
			w.Write([]byte(`{"ok":true,"ts":"now"}`))
		case "/order":
			w.Write([]byte(`{"ok":true,"order_id":"GW1"}`))
		case "/state":
			w.Write([]byte(`{"connected":true,"positions":[{"ts_code":"600000.SH","qty":100,"cost_price":10}]}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	cfg := config.DefaultQMTConfig()
	cfg.Enabled = true
	cfg.GatewayURL = srv.URL
	cfg.MissHeartbeatSec = 2

	exec := NewQMTClient(srv.URL, "", 2*time.Second, 0)
	ctrl := NewController(exec, db, "u_1", cfg, nil)

	// 正常下单
	res, err := ctrl.PlaceOrder(OrderRequest{
		SignalID: "SIG1", Code: "600000.SH", Name: "浦发", Strategy: "龙头",
		Side: SideBuy, PriceType: "market", Price: 10, Qty: 100, Amount: 1000,
		CreatedAt: time.Now().Format(time.RFC3339),
	})
	if err != nil || res == nil || !res.OK {
		t.Fatalf("place order: %+v err=%v", res, err)
	}
	// 幂等：同一 signal_id 不重复下单
	res, err = ctrl.PlaceOrder(OrderRequest{
		SignalID: "SIG1", Code: "600000.SH", Name: "浦发", Strategy: "龙头",
		Side: SideBuy, PriceType: "market", Price: 10, Qty: 100, Amount: 1000,
		CreatedAt: time.Now().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("idempotent order should not error, got %v", err)
	}
	if res.OK || !stringsContains(res.Err, "duplicate") {
		t.Fatalf("expected duplicate rejection, got %+v", res)
	}
	// 熔断：HealthCheck 失败超时后拒绝下单
	ctrl.exec = failingExecutor{}
	ctrl.mu.Lock()
	ctrl.lastHealthAt = time.Time{}
	ctrl.lastFailAt = time.Now().Add(-3 * time.Second)
	ctrl.mu.Unlock()
	ctrl.HealthCheck()
	if !ctrl.Tripped() {
		t.Fatal("expected circuit breaker tripped")
	}
	if _, err := ctrl.PlaceOrder(OrderRequest{SignalID: "SIG2", Code: "600000.SH", Side: SideBuy, Price: 10, Qty: 100, Amount: 1000, CreatedAt: time.Now().Format(time.RFC3339)}); err == nil {
		t.Fatal("expected order rejected while tripped")
	}
	// 恢复：健康恢复解熔
	ctrl.exec = exec
	ctrl.mu.Lock()
	ctrl.lastFailAt = time.Time{}
	ctrl.lastHealthAt = time.Time{}
	ctrl.mu.Unlock()
	ctrl.HealthCheck()
	if ctrl.Tripped() {
		t.Fatal("expected circuit breaker un-tripped after recovery")
	}
}

func stringsContains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// failingExecutor 模拟网关失联。
// （failingExecutor simulates an unreachable gateway.）
type failingExecutor struct{}

func (failingExecutor) PlaceBuy(req OrderRequest) (*OrderResult, error) {
	return nil, context.DeadlineExceeded
}
func (failingExecutor) PlaceSell(req OrderRequest) (*OrderResult, error) {
	return nil, context.DeadlineExceeded
}
func (failingExecutor) Cancel(orderID string) error   { return context.DeadlineExceeded }
func (failingExecutor) State() (*GatewayState, error) { return nil, context.DeadlineExceeded }
func (failingExecutor) Health() (bool, error)         { return false, context.DeadlineExceeded }
