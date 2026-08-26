// guards_test.go — §GAP1.3/1.4/1.6 下单前置守卫测试：
// ST/退市风险股拒绝、单日买入笔数上限、单日买入预算、近似可用资金预检；卖出不受纪律限制。
// English: order-guard tests — ST rejection, daily buy-count cap, daily budget, estimated-cash
// precheck; sells are exempt from the buy discipline.
package trading

import (
	"fmt"
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

// ── §GAP2-W1 发送失败降级与重试放行 ─────────────────────────────────────────────

// flakyStub 可控失败的假网关：fail=true 时 PlaceBuy 返回网络错误，模拟网关超时/断连。
// English: flakyStub is a controllable fake gateway: with fail=true PlaceBuy returns a network
// error, simulating a gateway timeout / disconnect.
type flakyStub struct {
	guardStub
	fail bool
}

func (f *flakyStub) PlaceBuy(req OrderRequest) (*OrderResult, error) {
	f.calls++
	if f.fail {
		return nil, fmt.Errorf("gateway timeout")
	}
	return &OrderResult{OK: true, OrderID: "GW-R"}, nil
}

// TestSendFailedRetryAndBudgetExclusion §GAP2-W1 回归：
// ①下单发送失败 → 占位行降级"发送失败"，不再计入当日预算（幽灵单不再虚耗额度）；
// ②同一 signal_id 在发送失败后重试放行并成功；③成功后再发同键仍被 duplicate 拦截；
// ④状态守卫：已成/已报等真实进度绝不会被误降级。
func TestSendFailedRetryAndBudgetExclusion(t *testing.T) {
	db := testDB(t)
	stub := &flakyStub{}
	cfg := config.DefaultQMTConfig()
	cfg.Enabled = true
	cfg.DailyBudgetAmount = 3000 // 预算两笔：幽灵单若被计入（R1 失败 1000 + R2 成功 1000），R3 重试的 1000 会超限被拒
	ctrl := NewController(stub, db, "u_sf", cfg, nil)

	mk := func(id string) OrderRequest {
		return OrderRequest{SignalID: id, Code: "600000.SH", Name: "浦发", Strategy: "龙头",
			Side: SideBuy, PriceType: "market", Price: 10, Qty: 100, Amount: 1000,
			CreatedAt: time.Now().Format(time.RFC3339)}
	}

	// ① 首次发送失败：返回 err 且占位行降级为"发送失败"
	stub.fail = true
	if _, err := ctrl.PlaceOrder(mk("R1")); err == nil {
		t.Fatalf("网关故障时下单应报错")
	}
	os, err := db.RealOrders()
	if err != nil || len(os) != 1 || os[0].Status != "发送失败" {
		t.Fatalf("占位行应降级为发送失败: %+v err=%v", os, err)
	}

	// ② 幽灵单不计入预算：同日第二笔 1000 元应放行（旧实现 spent=1000+1000>1500 会被拒）
	stub.fail = false
	res, err := ctrl.PlaceOrder(mk("R2"))
	if err != nil || res == nil || !res.OK {
		t.Fatalf("发送失败的单不应占用预算，第二笔应成功: %+v err=%v", res, err)
	}

	// ④ 状态守卫：把 R2 手工推进为"已成"，再制造一次同键请求不应把它回退为已报/重发
	if err := db.UpdateRealOrderBySignalID("R2", "GW-R", "已成"); err != nil {
		t.Fatal(err)
	}
	stub.calls = 0
	res, err = ctrl.PlaceOrder(mk("R2"))
	if err != nil || res.OK || !strings.Contains(res.Err, "duplicate") {
		t.Fatalf("已成订单同键重发应 duplicate: %+v err=%v", res, err)
	}
	if stub.calls != 0 {
		t.Fatalf("已成订单不应触发真实发送, calls=%d", stub.calls)
	}
	if os, _ = db.RealOrders(); os[0].SignalID == "R2" && os[0].Status != "已成" {
		t.Fatalf("已成状态不应被降级: %+v", os[0])
	}

	// ③ 失败后同键重试放行：R3 首发失败 → 重置 → 同键重试成功
	stub.fail = true
	if _, err := ctrl.PlaceOrder(mk("R3")); err == nil {
		t.Fatalf("R3 首次应失败")
	}
	stub.fail = false
	calls := stub.calls
	res, err = ctrl.PlaceOrder(mk("R3"))
	if err != nil || !res.OK {
		t.Fatalf("发送失败后同键重试应放行: %+v err=%v", res, err)
	}
	if stub.calls != calls+1 {
		t.Fatalf("重试应真实到达网关")
	}
}

// TestGuardSellExemptsSTAndBlacklist §GAP2-W1 回归：ST/黑名单守卫仅拦买入方向——
// 已持仓股盘中戴帽 ST 或进黑名单后，卖出（止损/清仓）必须照常放行；买入仍被拒。
func TestGuardSellExemptsSTAndBlacklist(t *testing.T) {
	db := testDB(t)
	g := &guardStub{}
	cfg := config.DefaultQMTConfig()
	cfg.Enabled = true
	cfg.Blacklist = []string{"000001"}
	ctrl := NewController(g, db, "u_se", cfg, nil)

	sellReq := func(name, code string) OrderRequest {
		return OrderRequest{SignalID: "SELL-" + code, Code: code, Name: name, Strategy: "龙头",
			Side: SideSell, PriceType: "market", Price: 10, Qty: 100, Amount: 1000,
			CreatedAt: time.Now().Format(time.RFC3339)}
	}
	buyReqNamed := func(name, code, id string) OrderRequest {
		return OrderRequest{SignalID: id, Code: code, Name: name, Strategy: "龙头",
			Side: SideBuy, PriceType: "market", Price: 10, Qty: 100, Amount: 1000,
			CreatedAt: time.Now().Format(time.RFC3339)}
	}

	// ST 股：买拒 / 卖放行
	if _, err := ctrl.PlaceOrder(buyReqNamed("*ST海工", "600601.SH", "B-ST")); err == nil {
		t.Fatalf("ST 股买入应被拒")
	}
	if g.calls != 0 {
		t.Fatalf("ST 买入被拒不应触达网关, calls=%d", g.calls)
	}
	if _, err := ctrl.PlaceOrder(sellReq("*ST海工", "600601.SH")); err != nil {
		t.Fatalf("ST 股卖出应豁免守卫放行: %v", err)
	}
	// 黑名单股：买拒 / 卖放行
	if _, err := ctrl.PlaceOrder(buyReqNamed("平安银行", "000001.SZ", "B-BL")); err == nil {
		t.Fatalf("黑名单股买入应被拒")
	}
	if _, err := ctrl.PlaceOrder(sellReq("平安银行", "000001.SZ")); err != nil {
		t.Fatalf("黑名单股卖出应豁免守卫放行: %v", err)
	}
}
