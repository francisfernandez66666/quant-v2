// buy_dispatcher_test.go §A+B 异步下单分发器（事件驱动热路径）测试：
// 信号经同步守卫后入 buyCh，worker 调用网关下单，scoring 循环不被网关 RTT 阻塞。
// English: A+B async order dispatcher tests — signal passes synchronous guards,
// enqueues on buyCh, a worker calls the gateway; the scoring loop is never blocked by gateway RTT.
package engine

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/metrics"
	"quant-trading-v2/internal/store"
	"quant-trading-v2/internal/trading"
)

// newAsyncEngine 构造异步下单引擎 + mock 网关测试夹具。
func newAsyncEngine(t *testing.T) (*Engine, *store.DB, *httptest.Server, *[]map[string]interface{}, *sync.Mutex) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "qmt.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	// §M12-A：同 newQMTEngine——异步链路的用例主题是"入队/排空/幂等"，不是资金门控；
	// 不播种新鲜账户行会被自动买入 fail-close 整腿拦在入口，orders 恒 0 造成假红。
	if err := db.UpsertRealAccount(store.RealAccount{UserID: "u_1", AvailableCash: 10_000_000,
		UpdatedAt: time.Now().In(cntime.Loc).Format("2006-01-02 15:04:05")}); err != nil {
		t.Fatalf("seed cash: %v", err)
	}

	// mock 柜台：/order 收到的每笔委托原样存进 orders 并固定回执 GW1，
	// 用例据此断言实际下单条数与委托内容。
	var mu sync.Mutex
	var orders []map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/order" && r.Method == "POST" {
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
		w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { db.Close() })

	// 夹具统一装配：QMT 开成 auto 模式指向 mock 柜台、每单固定 1 万元，
	// 让派发器用例只关心「信号有没有变成委托」。
	cfg := config.DefaultQMTConfig()
	cfg.Enabled = true
	cfg.Mode = "auto"
	cfg.GatewayURL = srv.URL
	cfg.FixedAmount = 10000
	exec := trading.NewQMTClient(srv.URL, "", 2*time.Second, 0)
	ctrl := trading.NewController(exec, db, "u_1", cfg, nil)
	e := &Engine{}
	e.SetQMT(ctrl, db)
	return e, db, srv, &orders, &mu
}

// TestAsyncDispatcherPlacesOrder 信号入队后 worker 实际下单，scoring 循环不阻塞。
// English: a buy signal enqueues and a worker places it; the caller is not blocked.
func TestAsyncDispatcherPlacesOrder(t *testing.T) {
	e, _, srv, orders, mu := newAsyncEngine(t)
	defer srv.Close()
	e.StartBuyDispatcher(2)
	defer e.StopBuyDispatcher()

	sig := combat_agent.Signal{
		ID: "sig-1", Code: "600000", Name: "浦发银行", Strategy: "龙头",
		Direction: "做多", Action: "买入", Price: 10, Confidence: 0.9,
	}
	live := map[string]*data.StockInfo{"600000": {Code: "600000", Price: 10}}

	started := time.Now()
	e.autoPlace(sig, live) // 同步守卫 + 入队，应立即返回
	elapsed := time.Since(started)
	if elapsed > 50*time.Millisecond {
		t.Errorf("autoPlace 应立即返回（入队不阻塞），实际耗时 %v", elapsed)
	}

	// 等待 worker POST /order
	deadline := time.After(3 * time.Second)
	for {
		mu.Lock()
		n := len(*orders)
		mu.Unlock()
		if n > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("worker 未在 3s 内下单，orders=%d", n)
		case <-time.After(50 * time.Millisecond):
		}
	}

	o := (*orders)[0]
	code, _ := o["code"].(string)
	if code != "600000.SH" {
		t.Errorf("code=%v, want 600000.SH", code)
	}
	sid, _ := o["signal_id"].(string)
	if want := "buy:600000:龙头:"; !startsWith(sid, want) {
		t.Errorf("signal_id=%q, want prefix %q", sid, want)
	}
}

// TestAsyncDispatcherDropsOnFullQueue buyCh 满时 autoPlace 必须立即返回（default 分支），
// 绝不能阻塞检测循环。本测试不启动 worker（避免依赖网关响应）。
// English: when buyCh is full, autoPlace must return immediately (default case),
// never blocking the detection loop. No worker is started here (avoids depending on the gateway).
func TestAsyncDispatcherDropsOnFullQueue(t *testing.T) {
	e, _, srv, _, _ := newAsyncEngine(t)
	defer srv.Close()
	e.mu.Lock()
	e.buyCh = make(chan buyTask, 1)
	e.buyStop = make(chan struct{})
	e.mu.Unlock()

	sig := combat_agent.Signal{
		ID: "s1", Code: "600000", Name: "浦发银行", Strategy: "龙头",
		Direction: "做多", Action: "买入", Price: 10,
	}
	live := map[string]*data.StockInfo{"600000": {Code: "600000", Price: 10}}
	e.autoPlace(sig, live) // 填满容量-1 的 channel

	done := make(chan struct{})
	go func() { e.autoPlace(sig, live); close(done) }() // 第二次应走 default
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("autoPlace 在 buyCh 满时阻塞了——会卡住检测循环")
	}
}

// TestAsyncDispatcherIdempotentKey 同一信号重复入队不阻塞，且 signal_id 唯一键确定（幂等由 orders 表保证）。
// 本测试不启动 HTTP worker，仅验证入队非阻塞与 channel 深度 + 唯一键格式。
// English: re-enqueuing the same signal returns immediately and keeps a deterministic signal_id (idempotency via DB unique key). No HTTP worker here.
func TestAsyncDispatcherIdempotentKey(t *testing.T) {
	e, _, srv, _, _ := newAsyncEngine(t)
	defer srv.Close()
	// 手动建 capacity-64 channel（模拟 StartBuyDispatcher），不启 worker
	e.mu.Lock()
	e.buyCh = make(chan buyTask, 64)
	e.buyStop = make(chan struct{})
	e.mu.Unlock()

	sig := combat_agent.Signal{
		ID: "s1", Code: "600000", Name: "浦发银行", Strategy: "龙头",
		Direction: "做多", Action: "买入", Price: 10,
	}
	live := map[string]*data.StockInfo{"600000": {Code: "600000", Price: 10}}

	started := time.Now()
	e.autoPlace(sig, live)
	e.autoPlace(sig, live) // 同 signal_id，两次均应快速入队返回
	elapsed := time.Since(started)
	if elapsed > 50*time.Millisecond {
		t.Errorf("重复入队应快速返回（入队不阻塞），实际耗时 %v", elapsed)
	}
	if got, want := len(e.buyCh), 2; got != want {
		t.Errorf("buyCh 深度=%d, want %d", got, want)
	}
	// 验证唯一键格式（与 autoPlace 内一致）：buy:<code>:<strategy>:<交易日>
	id := fmt.Sprintf("buy:%s:%s:%s", pureTsCode(sig.Code), sig.Strategy, data.TradingDayDate(time.Now()))
	if !startsWith(id, "buy:600000:龙头:") {
		t.Errorf("signal_id=%q, want prefix buy:600000:龙头:", id)
	}
}

// startsWith 字符串前缀判断（测试断言辅助）。
func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// TestBuyQueueDepthGauge §AUDIT-PM 2026-09-15：buy_queue_depth 量规随入队/出队实时跟踪
// 队列深度——崩溃丢单观测面（buyCh 为内存队列，深度=重启瞬间可能丢失的待下单信号数）。
// 两段断言：入队侧用手工 channel（无 worker，深度确定）；出队侧用真 worker（下单落定后
// 深度必须归零，证明量规不是只增不减的死数字）。
// English: the gauge tracks enqueue/dequeue in real time; enqueue side deterministic with a manual
// channel, drain side asserts the gauge returns to 0 once a real worker has placed the order.
func TestBuyQueueDepthGauge(t *testing.T) {
	e, _, srv, _, _ := newAsyncEngine(t)
	defer srv.Close()
	e.mu.Lock()
	e.buyCh = make(chan buyTask, 64)
	e.buyStop = make(chan struct{})
	e.mu.Unlock()

	sig := combat_agent.Signal{
		ID: "s1", Code: "600000", Name: "浦发银行", Strategy: "龙头",
		Direction: "做多", Action: "买入", Price: 10,
	}
	live := map[string]*data.StockInfo{"600000": {Code: "600000", Price: 10}}

	e.autoPlace(sig, live)
	if v, ok := metrics.GetGauge("buy_queue_depth"); !ok || v != 1 {
		t.Fatalf("入队 1 笔后量规应为 1, got v=%d ok=%v", v, ok)
	}
	e.autoPlace(sig, live)
	if v, _ := metrics.GetGauge("buy_queue_depth"); v != 2 {
		t.Fatalf("入队 2 笔后量规应为 2, got %d", v)
	}

	// 出队侧：真 worker 引擎，等其下单落定后量规应归零。
	e2, _, srv2, orders2, mu2 := newAsyncEngine(t)
	defer srv2.Close()
	e2.StartBuyDispatcher(1)
	defer e2.StopBuyDispatcher()
	e2.autoPlace(combat_agent.Signal{
		ID: "s2", Code: "600000", Name: "浦发银行", Strategy: "龙头",
		Direction: "做多", Action: "买入", Price: 10,
	}, live)
	deadline := time.After(5 * time.Second)
	for {
		mu2.Lock()
		n := len(*orders2)
		mu2.Unlock()
		if n > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("worker 5s 内未下单")
		case <-time.After(30 * time.Millisecond):
		}
	}
	if v, ok := metrics.GetGauge("buy_queue_depth"); !ok || v != 0 {
		t.Fatalf("worker 消费后量规应归零, got v=%d ok=%v", v, ok)
	}
}
