// guards_test.go — §GAP1.3/1.4/1.6 下单前置守卫测试：
// ST/退市风险股拒绝、单日买入笔数上限、单日买入预算、近似可用资金预检；卖出不受纪律限制。
// English: order-guard tests — ST rejection, daily buy-count cap, daily budget, estimated-cash
// precheck; sells are exempt from the buy discipline.
package trading

import (
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/store"
)

// guardServer 构造始终受理的假网关。
// English: guardServer builds a fake gateway that always accepts orders.
func guardServer() *guardStub {
	return &guardStub{}
}

type guardStub struct {
	calls int
}

// PlaceBuy PlaceBuy（Stub方法）。
func (g *guardStub) PlaceBuy(req OrderRequest) (*OrderResult, error) {
	g.calls++
	return &OrderResult{OK: true, OrderID: "GW-X"}, nil
}

// PlaceSell PlaceSell（Stub方法）。
func (g *guardStub) PlaceSell(req OrderRequest) (*OrderResult, error) {
	g.calls++
	return &OrderResult{OK: true, OrderID: "GW-S"}, nil
}

// Cancel Cancel（Stub方法）。
func (g *guardStub) Cancel(orderID string) error { return nil }

// State 测试桩：返回已连接网关状态。
func (g *guardStub) State() (*GatewayState, error) { return &GatewayState{Connected: true}, nil }

// Health 测试桩：返回健康。
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

// PlaceBuy PlaceBuy（Stub方法）。
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
	if err := db.UpdateRealOrderBySignalID("u_sf", "R2", "GW-R", "已成"); err != nil {
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

// ── §R3-1 P0-A/P0-B/P0-C 回归 ──────────────────────────────────────────────────

// rejectStub 业务拒单假网关：仅首次下单返回 (OK=false, err=nil)，模拟网关 200+ok:false
// （券商侧拒绝：资金不足/废单等）；此后放行（模拟资金腾挪后重试成功）。
// English: business-rejection stub — the first PlaceBuy returns 200+ok:false with nil error,
// later calls succeed (simulating funds freeing up for the retry).
type rejectStub struct {
	guardStub
	callsSeen int
}

// PlaceBuy PlaceBuy（Stub方法）。
func (rj *rejectStub) PlaceBuy(req OrderRequest) (*OrderResult, error) {
	rj.calls++
	rj.callsSeen++
	if rj.callsSeen == 1 {
		return &OrderResult{OK: false, Err: "资金不足"}, nil
	}
	return &OrderResult{OK: true, OrderID: "GW-" + req.SignalID}, nil
}

// TestBusinessRejectDemotesPlaceholder §R3-1 P0-A 回归：
// ①业务拒单（200+ok:false）后占位行降级"发送失败"，不再以"已报"虚耗当日预算；
// ②同 signal_id 重试经 ResetFailedRealOrder 放行；
// ③拒单结果原样透传（res.OK=false + 原始 Err），调用方语义不变。
func TestBusinessRejectDemotesPlaceholder(t *testing.T) {
	db := testDB(t)
	stub := &rejectStub{}
	cfg := config.DefaultQMTConfig()
	cfg.Enabled = true
	cfg.DailyBudgetAmount = 3000 // 三笔 1000：幽灵已报若计入预算（R1 已报+R2 成功），BR1 重试必被拒
	ctrl := NewController(stub, db, "u_br", cfg, nil)

	mk := func(id string) OrderRequest {
		return OrderRequest{SignalID: id, Code: "600000.SH", Name: "浦发", Strategy: "龙头",
			Side: SideBuy, PriceType: "market", Price: 10, Qty: 100, Amount: 1000,
			CreatedAt: time.Now().Format(time.RFC3339)}
	}

	// ① 首次业务拒单：无 err、res.OK=false、占位行降级发送失败
	res, err := ctrl.PlaceOrder(mk("BR1"))
	if err != nil || res == nil || res.OK || res.Err != "资金不足" {
		t.Fatalf("业务拒单应原样透传: res=%+v err=%v", res, err)
	}
	os, err := db.RealOrders()
	if err != nil || len(os) != 1 || os[0].Status != "发送失败" {
		t.Fatalf("占位行应降级为发送失败而非滞留已报: %+v err=%v", os, err)
	}

	// ② 幽灵行不占预算：第二笔 1000 应放行（旧实现 spent=1000 已报+1000 > 1500 被拒）
	res2, err := ctrl.PlaceOrder(mk("BR2"))
	if err == nil && res2.OK {
		// 放行即证明降级生效
	} else if err == nil && !res2.OK {
		t.Fatalf("第二笔意外被业务拒单 stub 拒绝: %+v", res2)
	} else {
		t.Fatalf("降级后的占位行不应占用预算，第二笔应放行: %v", err)
	}

	// ③ 同键重试放行（ResetFailedRealOrder）
	calls := stub.calls
	res3, err := ctrl.PlaceOrder(mk("BR1"))
	if err != nil || res3 == nil || !res3.OK {
		t.Fatalf("发送失败降级后同键重试应放行: %+v err=%v", res3, err)
	}
	if stub.calls != calls+1 {
		t.Fatalf("重试应真实到达网关")
	}
}

// blockingStub 并发探针假网关：记录同时在 PlaceBuy 内的并发数。
// English: blocking stub that tracks concurrent entries into PlaceBuy.
type blockingStub struct {
	guardStub
	inFlight int32
	maxSeen  int32
	release  chan struct{} // 第二个请求在此等待，直到第一个完成
}

// PlaceBuy PlaceBuy（Stub方法）。
func (b *blockingStub) PlaceBuy(req OrderRequest) (*OrderResult, error) {
	n := atomic.AddInt32(&b.inFlight, 1)
	for {
		old := atomic.LoadInt32(&b.maxSeen)
		if n <= old || atomic.CompareAndSwapInt32(&b.maxSeen, old, n) {
			break
		}
	}
	if b.release != nil && req.SignalID == "SER-2" {
		<-b.release // SER-2 等 SER-1 出来才能进（串行化断言点）
	}
	time.Sleep(50 * time.Millisecond) // 制造重叠窗口
	atomic.AddInt32(&b.inFlight, -1)
	return &OrderResult{OK: true, OrderID: "GW-" + req.SignalID}, nil
}

// TestPlaceOrderSerialized §R3-1 P0-C 回归：并发下单串行化——
// SER-1 在网关内停留期间 SER-2 不得进入 PlaceBuy（守卫→落库→下单 TOCTOU 窗口封死）。
func TestPlaceOrderSerialized(t *testing.T) {
	stub := &blockingStub{release: make(chan struct{})}
	cfg := config.DefaultQMTConfig()
	cfg.Enabled = true
	cfg.DailyBudgetAmount = 100000
	ctrl := NewController(stub, testDB(t), "u_ser", cfg, nil)

	mk := func(id string) OrderRequest {
		return OrderRequest{SignalID: id, Code: "600000.SH", Name: "浦发", Strategy: "龙头",
			Side: SideBuy, PriceType: "market", Price: 10, Qty: 100, Amount: 1000,
			CreatedAt: time.Now().Format(time.RFC3339)}
	}

	done := make(chan struct{}, 2)
	go func() { ctrl.PlaceOrder(mk("SER-1")); done <- struct{}{} }()
	time.Sleep(20 * time.Millisecond) // 确保 SER-1 已先进入
	go func() { ctrl.PlaceOrder(mk("SER-2")); done <- struct{}{} }()
	close(stub.release) // SER-1 完成后放行 SER-2
	<-done
	<-done
	if m := atomic.LoadInt32(&stub.maxSeen); m != 1 {
		t.Fatalf("下单必须串行化: PlaceBuy 最大并发 %d, 期望 1", m)
	}
}

// TestMaxPositionsFilteredByUser §R3-1 P0-B 回归：仓位上限按账号过滤——
// 他账号持仓不占本账号 max_positions 名额。
func TestMaxPositionsFilteredByUser(t *testing.T) {
	db := testDB(t)
	if _, err := db.UpsertRealPositions([]store.RealPosition{
		{TsCode: "000001.SZ", Name: "他人持仓", Qty: 500, CostPrice: 10, Amount: 5000, UserID: "user_other"},
	}); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultQMTConfig()
	cfg.Enabled = true
	cfg.MaxPositions = 1
	ctrl := NewController(guardServer(), db, "u_me", cfg, nil)
	// user_other 已持 1 只，但过滤后本账号 0 持仓 → 应放行
	if _, err := ctrl.PlaceOrder(buyReq("MP-1", 1000)); err != nil {
		t.Fatalf("他账号持仓不应占用本账号上限名额: %v", err)
	}
	// 模拟 MP-1 成交回报落库（本账号持仓 1 只）
	if _, err := db.UpsertRealPositions([]store.RealPosition{
		{TsCode: "600000.SH", Name: "浦发", Qty: 100, CostPrice: 10, Amount: 1000, UserID: "u_me"},
	}); err != nil {
		t.Fatal(err)
	}
	// 本账号再买第 2 只 → 达上限被拒
	if _, err := ctrl.PlaceOrder(buyReq("MP-2", 1000)); err == nil || !strings.Contains(err.Error(), "max_positions") {
		t.Fatalf("本账号达 max_positions 应被拒: %v", err)
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
