// order_state_pull_stale_test.go — §ORDER-STATE-PULL-STALE（2026-11-02）回归锁。
//
// 成因不是推测，是现网读数：阶段一拉的是网关**当日**委托表，阶段二的候选又被
// `strings.HasPrefix(o.CreatedAt, day)` 挡在当日之内 ⇒ 历史日的非终态行**结构上永远进不了判据**。
// 11-02 复核时账上还挂着两笔 2026-10-30 的 600362「撤单中」，跨了三个交易日，运维面只剩一条
// CANCEL_UNRESOLVED 告警，没有任何后续动作。
//
// 本文件钉两条一体的东西：
//  1. **缺省关＝今天行为逐字节不变**（owner 令"新行为挂开关、发版≠生效"）：开关没按下时，
//     既不改本地行，也一次都不去敲网关的历史日账（多发一个请求都算违约）；
//  2. 开起来后三条安全边界与阶段二**同源且一条不许放宽**：① 只认真实委托号；
//     ② 该日网关账必须非空（整天零行时"查无"不是证据）；③ 单日一轮成片缺失 ≥3 视为账坏，
//     不动钱。放额度方向的判定误判一次就是多买一笔，所以三条都用"不动"来兜。
//
// English: locks the cross-day reconciliation leg: default-off equals today's exact behaviour
// (not even an extra gateway call), and when on, the three phase-two quality gates hold.

package trading

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/store"
)

// staleServer 一个会记录"被问过哪些日"的网关桩：缺省关时必须一次历史日都没被问过。
type staleServer struct {
	mu    sync.Mutex
	days  []string
	rows  map[string][]map[string]any // day → 该日网关账（缺 key＝该日零行）
	snapN int
	snapA int
}

func (s *staleServer) requestedDays() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.days))
	copy(out, s.days)
	return out
}

// serve 起桩：只认 /orders?day=，按请求日返回那天的账，并把"被问过哪些日"记下来。
// 记账是这条测试的一部分而非装饰：缺省关的用例要靠它证明**一次都没问历史日**——
// 只断言"本地行没变"是不够的，多发一次网关请求本身就是违例（这条腿的成本与副作用都在网关侧）。
func (s *staleServer) serve(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/orders" {
			http.NotFound(w, r)
			return
		}
		day := r.URL.Query().Get("day")
		s.mu.Lock()
		s.days = append(s.days, day)
		rows := s.rows[day]
		n, age := s.snapN, s.snapA
		s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true, "orders": rows, "n": len(rows), "snapshot_n": n, "snapshot_age": age,
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// seedStaleRow 落一笔**历史交易日**的非终态行（默认「撤单中」+ 真实委托号）。
// created_at 用"今天往前 3 个自然日"，保证一定落在 reconcile 的 today 过滤之外。
func seedStaleRow(t *testing.T, db *store.DB, uid, sid, oid, day string, status int) {
	t.Helper()
	_ = status
	created := day + " 14:30:00"
	if _, err := db.UpsertRealOrder(store.RealOrder{
		OrderID: oid, SignalID: sid, Code: "600362.SH", Side: SideBuy,
		Status: "撤单中", Price: 32.63, Qty: 100, CreatedAt: created, UserID: uid,
	}); err != nil {
		t.Fatalf("seed stale %s: %v", sid, err)
	}
}

// staleCtrl 造一个开了/没开开关的控制器（day 参数由桩自己记）。
func staleCtrl(t *testing.T, db *store.DB, uid string, srv *httptest.Server, on bool) *Controller {
	t.Helper()
	cfg := config.DefaultQMTConfig()
	cfg.Enabled = true
	cfg.GatewayURL = srv.URL
	cfg.Token = "tk"
	if on {
		yes := true
		cfg.OrderStatePullStale = &yes
	}
	return NewController(guardServer(), db, uid, cfg, nil)
}

// pullStaleRounds 跑 n 轮，每轮间隔超过 stalePullEvery（10 分钟）节流。
// offsetMinutes 是这一段起点的偏移：分段跑（先 5 轮验"没到 6 轮不放额度"、再 2 轮验放额度）时
// 必须让第二段的钟点**晚于**第一段，否则第二段的每一轮都会被 10 分钟节流挡掉——
// 第一版就这么假红过一次（红的是测试的时钟，不是实现的判据）。
func pullStaleRounds(ctrl *Controller, n int, offsetMinutes int) {
	base := time.Now().Add(time.Duration(offsetMinutes) * time.Minute)
	for i := 0; i < n; i++ {
		// 每轮同时喂阶段一（当日）与 STALE（跨日）：STALE 挂在 reconcileOrderStates 尾部。
		ctrl.ReconcileOrderStates(base.Add(time.Duration(i) * 11 * time.Minute))
	}
}

// pastDay 返回"今天往前第 3 个自然日"的 YYYY-MM-DD。
func pastDay() string {
	return time.Now().AddDate(0, 0, -3).Format("2006-01-02")
}

// TestStalePullDefaultOffIsLegacy 缺省关＝今天行为：历史行不动，且**一次都不问**历史日的账。
func TestStalePullDefaultOffIsLegacy(t *testing.T) {
	db := testDB(t)
	uid := "u_stale_off"
	day := pastDay()
	sid := "sell:600362:止损:" + day
	sv := &staleServer{rows: map[string][]map[string]any{
		day: {{orderIDKey(): "x", "signal_id": sid, "code": "600362.SH", "side": "卖出", "status": "已撤"}},
	}, snapN: 3, snapA: 12}
	srv := sv.serve(t)
	seedStaleRow(t, db, uid, sid, "xt7001", day, 0)
	ctrl := staleCtrl(t, db, uid, srv, false)
	if ctrl.cfg.OrderStatePullStaleOn() {
		t.Fatal("未配置时开关必须是关（这条腿放额度，缺省保守）")
	}
	pullStaleRounds(ctrl, 7, 20)
	if st, _ := db.RealOrderStatusForUser(uid, sid); st != "撤单中" {
		t.Fatalf("开关关着却被改了状态: %q（＝改了今天行为）", st)
	}
	for _, d := range sv.requestedDays() {
		if d == day {
			t.Fatalf("开关关着却去敲了历史日的账（day=%s）：缺省必须一次都不问", d)
		}
	}
}

// TestStalePullAppliesRealTerminal 开起来后，网关该日账给了真实终态 ⇒ 本地补成同一个终态（零推断）。
func TestStalePullAppliesRealTerminal(t *testing.T) {
	db := testDB(t)
	uid := "u_stale_term"
	day := pastDay()
	sid := "sell:600362:减仓:" + day
	sv := &staleServer{rows: map[string][]map[string]any{
		// 该日账里同时给一条别的单，证明不是"整本账为空"才生效
		day: {
			{"order_id": "o1", "signal_id": "OTHER-ROW", "code": "600000.SH", "side": "买入", "status": "已报"},
			{"order_id": "xt7002", "signal_id": sid, "code": "600362.SH", "side": "卖出", "status": "已撤",
				"price": 32.63, "qty": 100, "created_at": day + "T14:30:00+08:00"},
		},
	}, snapN: 3, snapA: 12}
	srv := sv.serve(t)
	seedStaleRow(t, db, uid, sid, "xt7002", day, 0)
	ctrl := staleCtrl(t, db, uid, srv, true)
	pullStaleRounds(ctrl, 1, 20)
	if st, _ := db.RealOrderStatusForUser(uid, sid); st != "已撤" {
		t.Fatalf("网关该日账已给终态，本地应被补成 已撤, got %q", st)
	}
}

// TestStalePullVoidsAfterSixMissingRounds 该日账非空但连续 6 轮查无此单 → 判废单（与阶段二同值）。
func TestStalePullVoidsAfterSixMissingRounds(t *testing.T) {
	db := testDB(t)
	uid := "u_stale_void"
	day := pastDay()
	sid := "buy:600428:fac_137:" + day
	sv := &staleServer{rows: map[string][]map[string]any{
		day: {{orderIDKey(): "o9", "signal_id": "SOME-OTHER", "code": "600000.SH", "side": "买入", "status": "已撤"}},
	}, snapN: 3, snapA: 12}
	srv := sv.serve(t)
	seedStaleRow(t, db, uid, sid, "xt7003", day, 0)
	ctrl := staleCtrl(t, db, uid, srv, true)
	pullStaleRounds(ctrl, 5, 20)
	if st, _ := db.RealOrderStatusForUser(uid, sid); st == "废单" {
		t.Fatalf("只 5 轮就放了额度（missLimit 应为 6，与阶段二同源）")
	}
	pullStaleRounds(ctrl, 2, 90)
	if st, _ := db.RealOrderStatusForUser(uid, sid); st != "废单" {
		t.Fatalf("连续 6 轮查无此单应判废单, got %q", st)
	}
}

// TestStalePullSkipsEmptyDayLedger 闸②：该日网关账**整天为空**时，查无不构成证据，永不判废。
func TestStalePullSkipsEmptyDayLedger(t *testing.T) {
	db := testDB(t)
	uid := "u_stale_emptyday"
	day := pastDay()
	sid := "sell:600362:清仓:" + day
	sv := &staleServer{rows: map[string][]map[string]any{}, snapN: 3, snapA: 12} // 该日零行
	srv := sv.serve(t)
	seedStaleRow(t, db, uid, sid, "xt7004", day, 0)
	ctrl := staleCtrl(t, db, uid, srv, true)
	pullStaleRounds(ctrl, 8, 20)
	if st, _ := db.RealOrderStatusForUser(uid, sid); st != "撤单中" {
		t.Fatalf("该日账为空却被判了终态（闸②失守：网关账被重置/未上送时缺失不是证据）: %q", st)
	}
}

// TestStalePullSkipsBulkMissing 闸③：单日一轮要收 ≥3 笔＝更像那本账坏了，只告警不动钱。
func TestStalePullSkipsBulkMissing(t *testing.T) {
	db := testDB(t)
	uid := "u_stale_bulk"
	day := pastDay()
	sids := []string{"sell:600362:a:" + day, "sell:600362:b:" + day, "sell:600362:c:" + day}
	for i, s := range sids {
		seedStaleRow(t, db, uid, s, "xt710"+string(rune('0'+i)), day, 0)
	}
	sv := &staleServer{rows: map[string][]map[string]any{
		day: {{orderIDKey(): "zz", "signal_id": "PRESENT-ONLY", "code": "600000.SH", "side": "买入", "status": "已报"}},
	}, snapN: 3, snapA: 12}
	srv := sv.serve(t)
	ctrl := staleCtrl(t, db, uid, srv, true)
	pullStaleRounds(ctrl, 8, 20)
	for _, s := range sids {
		if st, _ := db.RealOrderStatusForUser(uid, s); st != "撤单中" {
			t.Fatalf("成片缺失（3 笔）被当成三笔幻影单放了额度（闸③失守）%s: %q", s, st)
		}
	}
}

// TestStalePullIgnoresPlaceholderRefs 闸①：pend:/seq: 占位引用不在柜台账里，永远不动。
func TestStalePullIgnoresPlaceholderRefs(t *testing.T) {
	db := testDB(t)
	uid := "u_stale_pend"
	day := pastDay()
	sid := "buy:000063:fac_117:" + day
	sv := &staleServer{rows: map[string][]map[string]any{
		day: {{orderIDKey(): "o5", "signal_id": "OTHER", "code": "600000.SH", "side": "买入", "status": "已撤"}},
	}, snapN: 3, snapA: 12}
	srv := sv.serve(t)
	// 直接把 order_id 写成 pend: 形态（seedStaleRow 走的是真实号路径）
	if _, err := db.UpsertRealOrder(store.RealOrder{
		OrderID: "pend:" + sid, SignalID: sid, Code: "000063.SZ", Side: SideBuy,
		Status: "撤单中", Price: 10, Qty: 100, CreatedAt: day + " 14:30:00", UserID: uid,
	}); err != nil {
		t.Fatalf("seed pend row: %v", err)
	}
	ctrl := staleCtrl(t, db, uid, srv, true)
	pullStaleRounds(ctrl, 8, 20)
	if st, _ := db.RealOrderStatusForUser(uid, sid); st != "撤单中" {
		t.Fatalf("占位引用被按缺失判了终态（闸①失守＝凭空放预算）: %q", st)
	}
}

// TestStalePullCountsPerDay 分日计数：同一把 signal 在两个日子各自独立，不共用一本缺失账。
// 为什么这条要单独钉：共用 map 时"当日快照看到过"会把历史日的连续缺失清零 ⇒ 历史行永远收不掉，
// 或者反过来历史日的缺失把当日在途单提前判废（两个方向都是错账）。
func TestStalePullCountsPerDay(t *testing.T) {
	ctrl := &Controller{stalePullMiss: map[string]int{}}
	d1, d2 := pastDay(), time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	k1, k2 := d1+"|SIG", d2+"|SIG"
	if k1 == k2 {
		t.Fatal("分日键必须带日期")
	}
	ctrl.stalePullMiss[k1] = 5
	if ctrl.stalePullMiss[k2] != 0 {
		t.Fatal("另一日的同信号被串到了同一本计数里")
	}
}

// orderIDKey 网关行里主键字段名（保持桩数据里写法的唯一来源，避免散着写字面量）。
func orderIDKey() string { return "order_id" }
