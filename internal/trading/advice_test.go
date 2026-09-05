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

// testDB 打开临时目录下的测试库并注册清理。
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
			// §GAP2-W1 契约对齐：Health() 现要求 ok && broker_connected（与真实网关一致）。
			w.Write([]byte(`{"ok":true,"broker_connected":true,"ts":"now"}`))
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
	ctrl.exec.Store(execHolder{failingExecutor{}})
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
	ctrl.exec.Store(execHolder{exec})
	ctrl.mu.Lock()
	ctrl.lastFailAt = time.Time{}
	ctrl.lastHealthAt = time.Time{}
	ctrl.mu.Unlock()
	ctrl.HealthCheck()
	if ctrl.Tripped() {
		t.Fatal("expected circuit breaker un-tripped after recovery")
	}
}

// TestFromSignalNoLivePriceNoFakeRef P2#25 回归：行情缺失时 fromSignal 不得用成本价顶替现价——
// RefPrice 保持 0（自动卖出守卫据此拦截，避免按成本价挂错单），显示估值仍按成本价（ProfitPct=0）。
// English: P2#25 regression — a missing live quote must not be faked with cost price in fromSignal:
// RefPrice stays 0 (the auto-sell guard skips it, so no order at the wrong cost price) while the display
// estimate still uses cost (ProfitPct=0).
func TestFromSignalNoLivePriceNoFakeRef(t *testing.T) {
	pos := store.RealPosition{TsCode: "600000.SH", Name: "浦发", Qty: 100, CostPrice: 10, Amount: 1000}
	in := AdviceInput{Positions: []store.RealPosition{pos}}
	sig := combat_agent.Signal{
		Code: "600000", AlertType: "止损", Direction: "提醒", Action: "止损",
		Price:  0, // 行情缺失（触发价无效）
		Reason: "行情缺失止损",
	}
	pa := fromSignal(sig, in, time.Now(), "")
	if pa == nil {
		t.Fatal("fromSignal 不应返回 nil")
	}
	if pa.RefPrice != 0 {
		t.Fatalf("行情缺失时 RefPrice 应为 0（不可用做挂单价）, got %.2f", pa.RefPrice)
	}
	if pa.Action != "止损" || pa.Level != "高" {
		t.Fatalf("止损信号应映射为 止损/高, got %s/%s", pa.Action, pa.Level)
	}
	if pa.Amount != 1000 || pa.ProfitPct != 0 {
		t.Fatalf("展示估值应按成本价兜底 (Amount=1000, ProfitPct=0), got Amount=%.0f ProfitPct=%.2f", pa.Amount, pa.ProfitPct)
	}
}

// stringsContains 手写子串包含判断（测试断言辅助，避免依赖标准库 strings 之外行为）。
func stringsContains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

// indexOf 手写子串首现下标（-1=未找到），供 stringsContains 使用。
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

// PlaceBuy 模拟网关失联：返回 DeadlineExceeded。
func (failingExecutor) PlaceBuy(req OrderRequest) (*OrderResult, error) {
	return nil, context.DeadlineExceeded
}

// PlaceSell PlaceSell。
func (failingExecutor) PlaceSell(req OrderRequest) (*OrderResult, error) {
	return nil, context.DeadlineExceeded
}

// Cancel 模拟网关失联：返回 DeadlineExceeded。
func (failingExecutor) Cancel(orderID string) error { return context.DeadlineExceeded }

// State 模拟网关失联：返回 DeadlineExceeded。
func (failingExecutor) State() (*GatewayState, error) { return nil, context.DeadlineExceeded }

// Health Health。
func (failingExecutor) Health() (bool, error) { return false, context.DeadlineExceeded }
