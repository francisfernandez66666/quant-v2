// guards_test.go — §GAP1.3/1.4/1.6 下单前置守卫测试：
// ST/退市风险股拒绝、单日买入笔数上限、单日买入预算、近似可用资金预检；卖出不受纪律限制。
// English: order-guard tests — ST rejection, daily buy-count cap, daily budget, estimated-cash
// precheck; sells are exempt from the buy discipline.
package trading

import (
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/config"
)

// guardServer 构造始终受理的假网关。
// English: guardServer builds a fake gateway that always accepts orders.
func guardServer() *guardStub {
	return &guardStub{}
}

type guardStub struct {
	calls int
}

func (g *guardStub) PlaceBuy(req OrderRequest) (*OrderResult, error) {
	g.calls++
	return &OrderResult{OK: true, OrderID: "GW-X"}, nil
}

func (g *guardStub) PlaceSell(req OrderRequest) (*OrderResult, error) {
	g.calls++
	return &OrderResult{OK: true, OrderID: "GW-S"}, nil
}

func (g *guardStub) Cancel(orderID string) error { return nil }

func (g *guardStub) State() (*GatewayState, error) { return &GatewayState{Connected: true}, nil }

func (g *guardStub) Health() (bool, error) { return true, nil }

// buyReq 构造一笔标准买单。
func buyReq(id string, amount float64) OrderRequest {
	return OrderRequest{
		SignalID: id, Code: "600000.SH", Name: "浦发", Strategy: "龙头",
		Side: SideBuy, PriceType: "market", Price: 10,
		Qty:       int(amount / 10),
		Amount:    amount,
		CreatedAt: time.Now().Format(time.RFC3339),
	}
}

// TestGuardSTRejected §GAP1.6：ST/退市风险股任何路径都拒绝下单。
func TestGuardSTRejected(t *testing.T) {
	ctrl := NewController(guardServer(), testDB(t), "u_g", func() config.QMTConfig {
		c := config.DefaultQMTConfig()
		c.Enabled = true
		return c
	}(), nil)
	for i, name := range []string{"*ST海工", "ST板块", "S*ST前沿", "退市锐电"} {
		id := "SIG-ST-" + string(rune('A'+i)) // 每例独立幂等键：重复键会先命中 duplicate 短路而测不到守卫
		_, err := ctrl.PlaceOrder(OrderRequest{SignalID: id, Code: "600000.SH", Name: name,
			Side: SideBuy, Price: 10, Qty: 100, Amount: 1000, CreatedAt: time.Now().Format(time.RFC3339)})
		if err == nil || !strings.Contains(err.Error(), "ST") && !strings.Contains(err.Error(), "退") {
			t.Fatalf("%s 应被拒绝, got %v", name, err)
		}
	}
	// 正常名称放行（非 ST 判定边界："浦发银行" 含字母 S/T 但非 ST 前缀）
	if _, err := ctrl.PlaceOrder(buyReq("SIG-OK", 1000)); err != nil {
		t.Fatalf("正常股不应被拒: %v", err)
	}
}

// TestGuardDailyBuysCap §GAP1.4：单日买入笔数达上限后拒绝新买入，卖出不受限。
func TestGuardDailyBuysCap(t *testing.T) {
	db := testDB(t)
	cfg := config.DefaultQMTConfig()
	cfg.Enabled = true
	cfg.DailyMaxBuys = 2
	ctrl := NewController(guardServer(), db, "u_g", cfg, nil)
	for i := 0; i < 2; i++ {
		if _, err := ctrl.PlaceOrder(buyReq("SIG-B"+string(rune('A'+i)), 1000)); err != nil {
			t.Fatalf("第 %d 笔应放行: %v", i+1, err)
		}
	}
	if _, err := ctrl.PlaceOrder(buyReq("SIG-C", 1000)); err == nil || !strings.Contains(err.Error(), "单日买入笔数达上限") {
		t.Fatalf("第 3 笔应被笔数上限拦截, got %v", err)
	}
	// 卖出不受限
	if _, err := ctrl.PlaceOrder(OrderRequest{SignalID: "SIG-S", Code: "600000.SH", Name: "浦发",
		Side: SideSell, Price: 10, Qty: 100, Amount: 1000, CreatedAt: time.Now().Format(time.RFC3339)}); err != nil {
		t.Fatalf("卖出不受买入纪律限制: %v", err)
	}
}

// TestGuardDailyBudget §GAP1.4：单日买入金额超预算后拒绝。
func TestGuardDailyBudget(t *testing.T) {
	cfg := config.DefaultQMTConfig()
	cfg.Enabled = true
	cfg.DailyBudgetAmount = 1500
	ctrl := NewController(guardServer(), testDB(t), "u_g", cfg, nil)
	if _, err := ctrl.PlaceOrder(buyReq("SIG-B1", 1000)); err != nil {
		t.Fatalf("首笔应放行: %v", err)
	}
	if _, err := ctrl.PlaceOrder(buyReq("SIG-B2", 600)); err == nil || !strings.Contains(err.Error(), "预算不足") {
		t.Fatalf("累计 1600 > 预算 1500 应拦截, got %v", err)
	}
	// 恰好不超：600+900=1500 ≤ 预算 → 放行
	if _, err := ctrl.PlaceOrder(buyReq("SIG-B3", 500)); err != nil {
		t.Fatalf("累计 1500 = 预算应放行: %v", err)
	}
}

// TestGuardCashPrecheck §GAP1.3：近似可用资金不足时拒绝买入
// （本金 − Σ持仓成本市值 − 当日已报买单 < 本次金额）。
func TestGuardCashPrecheck(t *testing.T) {
	cfg := config.DefaultQMTConfig()
	cfg.Enabled = true
	cfg.InitialCapital = 10000
	ctrl := NewController(guardServer(), testDB(t), "u_g", cfg, nil)
	if _, err := ctrl.PlaceOrder(buyReq("SIG-B1", 9000)); err != nil {
		t.Fatalf("首笔 9000 ≤ 本金应放行: %v", err)
	}
	_, err := ctrl.PlaceOrder(buyReq("SIG-B2", 2000))
	if err == nil || !strings.Contains(err.Error(), "可用资金不足") {
		t.Fatalf("预估可用 1000 < 本次 2000 应拦截, got %v", err)
	}
	// 剩余额度内放行
	if _, err := ctrl.PlaceOrder(buyReq("SIG-B3", 1000)); err != nil {
		t.Fatalf("剩余额度内应放行: %v", err)
	}
}

// TestGuardBlacklist §GAP1.7 回归：黑名单股票拒绝下单（纯数字与带后缀双向归一比对）。
func TestGuardBlacklist(t *testing.T) {
	cfg := config.DefaultQMTConfig()
	cfg.Enabled = true
	cfg.Blacklist = []string{"600519.SH", "000001"}
	ctrl := NewController(guardServer(), testDB(t), "u_g", cfg, nil)
	for _, code := range []string{"600519", "600519.SH", "000001.SZ"} {
		_, err := ctrl.PlaceOrder(OrderRequest{SignalID: "BL-" + code, Code: code, Name: "普通名",
			Side: SideBuy, Price: 10, Qty: 100, Amount: 1000, CreatedAt: time.Now().Format(time.RFC3339)})
		if err == nil || !strings.Contains(err.Error(), "黑名单") {
			t.Fatalf("%s 应被黑名单拦截, got %v", code, err)
		}
	}
	// 非黑名单放行
	if _, err := ctrl.PlaceOrder(OrderRequest{SignalID: "BL-OK", Code: "600000.SH", Name: "浦发",
		Side: SideBuy, Price: 10, Qty: 100, Amount: 1000, CreatedAt: time.Now().Format(time.RFC3339)}); err != nil {
		t.Fatalf("非黑名单不应被拒: %v", err)
	}
}
