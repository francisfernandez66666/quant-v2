// order_state_pull_test.go — §ORDER-STATE-PULL（2026-10-30）回归锁。
//
// 钉住三件事：
//  1. 网关 409 + code=terminal 与普通 4xx 拒单的区分（前者必须"当日终结"，后者才可重试）；
//  2. 拉取对账只应用终态、不补行、不改写本地终态；
//  3. 终态拒单后置重发禁令：同 signal_id 再下单不再打网关。
package trading

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/store"
)

// TestGatewayTerminalRejectionClassification 409+code=terminal 才算终态拒单。
func TestGatewayTerminalRejectionClassification(t *testing.T) {
	yes := &gatewayHTTPError{Method: "POST", Path: "/order", Status: http.StatusConflict,
		Body: `{"ok": false, "code": "terminal", "status": "已撤", "err": "already terminal"}`}
	if st, ok := GatewayTerminalRejection(yes); !ok || st != "已撤" {
		t.Fatalf("终态 409 应识别成功, got ok=%v status=%q", ok, st)
	}
	plain := &gatewayHTTPError{Method: "POST", Path: "/order", Status: http.StatusConflict,
		Body: `{"ok": false, "err": "duplicate signal_id in-flight"}`}
	if _, ok := GatewayTerminalRejection(plain); ok {
		t.Fatal("普通 in-flight 409 不得被当成终态")
	}
	bad := &gatewayHTTPError{Method: "POST", Path: "/order", Status: http.StatusBadRequest,
		Body: `{"ok": false, "code": "terminal"}`}
	if _, ok := GatewayTerminalRejection(bad); ok {
		t.Fatal("非 409 不得被当成终态")
	}
}

// TestReconcileOrderStatesAppliesTerminalOnly 只收终态；本地终态/未知信号/在途行都不动。
func TestReconcileOrderStatesAppliesTerminalOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/orders" {
			http.NotFound(w, r)
			return
		}
		if got := r.URL.Query().Get("day"); len(got) != 10 {
			t.Errorf("day 参数格式不对: %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"orders": []map[string]any{
				{"order_id": "a1", "signal_id": "SIG-A", "code": "600000.SH", "side": "买入", "status": "已撤"},
				{"order_id": "b1", "signal_id": "SIG-B", "code": "600001.SH", "side": "买入", "status": "已报"},
				{"order_id": "c1", "signal_id": "SIG-C", "code": "600002.SH", "side": "买入", "status": "已撤"},
				{"order_id": "d1", "signal_id": "SIG-D", "code": "600003.SH", "side": "买入", "status": "废单"},
			},
		})
	}))
	defer srv.Close()

	db := testDB(t)
	uid := "u_recon"
	seed := func(sid, status string) {
		if _, err := db.UpsertRealOrder(store.RealOrder{
			OrderID: "pend:" + sid, SignalID: sid, Code: "600000.SH", Side: SideBuy,
			Status: status, Price: 10, Qty: 100, CreatedAt: time.Now().Format(time.RFC3339), UserID: uid,
		}); err != nil {
			t.Fatalf("seed %s: %v", sid, err)
		}
	}
	seed("SIG-A", "撤单中")
	seed("SIG-B", "撤单中")
	seed("SIG-C", "已成") // 本地已是终态：不得改写

	cfg := config.DefaultQMTConfig()
	cfg.Enabled = true
	cfg.GatewayURL = srv.URL
	cfg.Token = "tk"
	ctrl := NewController(guardServer(), db, uid, cfg, nil)

	ctrl.ReconcileOrderStates(time.Now())

	if st, _ := db.RealOrderStatusForUser(uid, "SIG-A"); st != "已撤" {
		t.Fatalf("停滞的撤单中应被收敛为已撤, got %q", st)
	}
	if st, _ := db.RealOrderStatusForUser(uid, "SIG-B"); st != "撤单中" {
		t.Fatalf("在途状态不得被对账改写, got %q", st)
	}
	if st, _ := db.RealOrderStatusForUser(uid, "SIG-C"); st != "已成" {
		t.Fatalf("本地终态不得被网关旧状态覆盖, got %q", st)
	}
	if st, _ := db.RealOrderStatusForUser(uid, "SIG-D"); st != "" {
		t.Fatalf("对账不得为未知 signal 补行, got %q", st)
	}
}

// terminalStub 让下单直接拿到网关「已终态」的 409。
type terminalStub struct {
	*guardStub
	calls int
}

// PlaceBuy PlaceBuy（Stub方法）。
func (s *terminalStub) PlaceBuy(req OrderRequest) (*OrderResult, error) {
	s.calls++
	return nil, &gatewayHTTPError{Method: "POST", Path: "/order", Status: http.StatusConflict,
		Body: `{"ok": false, "code": "terminal", "status": "已撤", "err": "already terminal"}`}
}

// TestPlaceOrderTerminalRejectionRetiresSignal 终态拒单：不降级发送失败、落终态、当日不再打网关。
func TestPlaceOrderTerminalRejectionRetiresSignal(t *testing.T) {
	db := testDB(t)
	uid := "u_term"
	stub := &terminalStub{guardStub: guardServer()}
	cfg := config.DefaultQMTConfig()
	cfg.Enabled = true
	ctrl := NewController(stub, db, uid, cfg, nil)

	sid := "buy:300502:fac_150:20261030"
	res, err := ctrl.PlaceOrder(buyReq(sid, 1000))
	if err != nil {
		t.Fatalf("终态拒单不应升级为发送错误: %v", err)
	}
	if res == nil || !res.Retired || res.OK {
		t.Fatalf("应返回 Retired 结果, got %+v", res)
	}
	if st, _ := db.RealOrderStatusForUser(uid, sid); st != "已撤" {
		t.Fatalf("本地行应落到网关终态 已撤, got %q", st)
	}
	if blocked, _ := db.IsOrderRetryBlocked(uid, sid); !blocked {
		t.Fatal("终态后必须置当日重发禁令")
	}
	// 连发 5 轮：只允许第一轮打到网关（方案 §8.1 的"重发 5 轮只发一次"）。
	for i := 0; i < 5; i++ {
		if _, err := ctrl.PlaceOrder(buyReq(sid, 1000)); err != nil {
			t.Fatalf("重复调用不应报错: %v", err)
		}
	}
	if stub.calls != 1 {
		t.Fatalf("置禁令后不得再打网关, calls=%d", stub.calls)
	}
}

// pullServer 造一个网关：返回固定 rows + 指定的委托表快照可信度（阶段二的信任前提）。
func pullServer(t *testing.T, rows []map[string]any, snapshotN, snapshotAge int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true, "orders": rows, "n": len(rows),
			"snapshot_n": snapshotN, "snapshot_age": snapshotAge,
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// seedStuckBuy 落一笔「撤单中 + 真实委托号」的买单（阶段二的唯一作用对象）。
func seedStuckBuy(t *testing.T, db interface {
	UpsertRealOrder(store.RealOrder) (bool, error)
}, uid, sid, oid string) {
	t.Helper()
	if _, err := db.UpsertRealOrder(store.RealOrder{
		OrderID: oid, SignalID: sid, Code: "300502.SZ", Side: SideBuy,
		Status: "撤单中", Price: 195.01, Qty: 100,
		CreatedAt: time.Now().Format("2006-01-02 15:04:05"), UserID: uid,
	}); err != nil {
		t.Fatalf("seed %s: %v", sid, err)
	}
}

// pullSixRounds 跑满 6 轮拉取（每轮间隔超过 30s 节流）。
func pullSixRounds(ctrl *Controller) {
	base := time.Now().Add(10 * time.Minute)
	for i := 0; i < 6; i++ {
		ctrl.ReconcileOrderStates(base.Add(time.Duration(i) * 31 * time.Second))
	}
}

// TestReconcileMissingOrdersPhase2 阶段二（owner 2026-10-30 裁决**默认开**）：
// 可信快照下连续 6 轮查无此单 → 判废单。
func TestReconcileMissingOrdersPhase2(t *testing.T) {
	srv := pullServer(t, []map[string]any{
		{"order_id": "other1", "signal_id": "OTHER", "code": "600000.SH", "side": "买入", "status": "已报"},
	}, 3, 12)
	db := testDB(t)
	uid := "u_p2"
	sid := "buy:300502:fac_150:20261030"
	seedStuckBuy(t, db, uid, sid, "xt999")
	cfg := config.DefaultQMTConfig()
	cfg.Enabled = true
	cfg.GatewayURL = srv.URL
	cfg.Token = "tk"
	ctrl := NewController(guardServer(), db, uid, cfg, nil)
	if !cfg.OrderStatePullCloseMissingOn() {
		t.Fatal("阶段二必须默认开（未配置=生效）")
	}
	pullSixRounds(ctrl)
	if st, _ := db.RealOrderStatusForUser(uid, sid); st != "废单" {
		t.Fatalf("连续 6 轮查无此单应判废单, got %q", st)
	}
}

// TestReconcileMissingOrdersPhase2Off 显式关掉时同样的输入一笔都不动。
func TestReconcileMissingOrdersPhase2Off(t *testing.T) {
	srv := pullServer(t, []map[string]any{
		{"order_id": "other1", "signal_id": "OTHER", "code": "600000.SH", "side": "买入", "status": "已报"},
	}, 3, 12)
	db := testDB(t)
	uid := "u_p2off"
	sid := "buy:300502:fac_150:20261030"
	seedStuckBuy(t, db, uid, sid, "xt999")
	off := false
	cfg := config.DefaultQMTConfig()
	cfg.Enabled = true
	cfg.GatewayURL = srv.URL
	cfg.Token = "tk"
	cfg.OrderStatePullCloseMissing = &off
	ctrl := NewController(guardServer(), db, uid, cfg, nil)
	pullSixRounds(ctrl)
	if st, _ := db.RealOrderStatusForUser(uid, sid); st != "撤单中" {
		t.Fatalf("显式关闭后不得动状态, got %q", st)
	}
}

// TestReconcileMissingOrdersNeedsTrustedSnapshot 质量闸②：快照为空/陈旧时"缺失"不是证据。
func TestReconcileMissingOrdersNeedsTrustedSnapshot(t *testing.T) {
	for i, tc := range []struct {
		name   string
		n, age int
	}{
		{"无快照", 0, -1},
		{"快照陈旧", 3, 900},
	} {
		srv := pullServer(t, []map[string]any{
			{"order_id": "other1", "signal_id": "OTHER", "code": "600000.SH", "side": "买入", "status": "已报"},
		}, tc.n, tc.age)
		db := testDB(t)
		uid := "u_p2gate"
		sid := "buy:300502:fac_150:20261030"
		seedStuckBuy(t, db, uid, sid, "xt999")
		cfg := config.DefaultQMTConfig()
		cfg.Enabled = true
		cfg.GatewayURL = srv.URL
		cfg.Token = "tk"
		ctrl := NewController(guardServer(), db, uid, cfg, nil)
		pullSixRounds(ctrl)
		if st, _ := db.RealOrderStatusForUser(uid, sid); st != "撤单中" {
			t.Fatalf("用例 %d（%s）快照不可信时不得判死, got %q", i, tc.name, st)
		}
	}
}

// TestReconcileMissingOrdersMassMissingSkips 质量闸③：单轮成片缺失按"网关账异常"处理，只告警不动钱。
func TestReconcileMissingOrdersMassMissingSkips(t *testing.T) {
	srv := pullServer(t, []map[string]any{
		{"order_id": "other1", "signal_id": "OTHER", "code": "600000.SH", "side": "买入", "status": "已报"},
	}, 5, 10)
	db := testDB(t)
	uid := "u_p2mass"
	sids := []string{"buy:300502:fac_150:20261030", "buy:002415:fac_150:20261030", "buy:002594:fac_150:20261030"}
	for i, sid := range sids {
		seedStuckBuy(t, db, uid, sid, fmt.Sprintf("xt900%d", i))
	}
	cfg := config.DefaultQMTConfig()
	cfg.Enabled = true
	cfg.GatewayURL = srv.URL
	cfg.Token = "tk"
	ctrl := NewController(guardServer(), db, uid, cfg, nil)
	pullSixRounds(ctrl)
	for _, sid := range sids {
		if st, _ := db.RealOrderStatusForUser(uid, sid); st != "撤单中" {
			t.Fatalf("成片缺失（3 笔）时不得动钱, %s got %q", sid, st)
		}
	}
}
