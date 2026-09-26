package main

// §0926E2E-MX1（2026-09-27 四波·矩阵补位#1）mock 派发队列（QMT 桥通道镜像）行为用例：
// 真实网关的兜底通道是「/order 入队 → 策略桥 GET /dispatch/pending 取单 → 柜台执行 →
// POST /dispatch/result 回报 → 网关结算推首尔」；此前 mock 三腿整体缺失，这条链路在
// Go 级用例/盘内 UAT 完全测不到（REVIEW_20260926E2E 矩阵缺口 #1）。本组用例驱动
// buildHandler 锤四件事：①桥腿全周期（取单→回报→已报→已成 状态推进 + inflight 原子标记）；
// ②派发队列自身的同 signal 在途纵深防线（镜像 §CLAIMRELEASE）；③撤单腿两分支
// （ok→已撤且柜台成交被守卫拦截 / 失败→回已报可成交态）；④整手/金额帽校验开关
// （默认与实网关同口径，-relax-order-check 显式放宽的正反两证）。
// English: bridge-leg lifecycle tests for the mock's new /dispatch trio + order-gate
// (board lot / amount cap) switch, mirroring the real gateway's queued-broker contract.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// tGet 带鉴权 GET（main_test.go 只有 post helper，本文件补只读腿）。
func tGet(t *testing.T, h http.Handler, path string) (int, map[string]interface{}) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer t0")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// mustSwitchBroker 经正规面 POST /admin/broker 切换通道（e2e 同腿，不吃内部字段）。
func mustSwitchBroker(t *testing.T, h http.Handler, broker string) {
	t.Helper()
	code, resp := post(t, h, "/admin/broker", `{"broker":"`+broker+`"}`)
	if code != 200 || resp["ok"] != true {
		t.Fatalf("切换通道 %s 应 200 ok，实得 %v %v", broker, code, resp)
	}
}

// takePending 取单一次并返回 items（桥消费面）。
func takePending(t *testing.T, h http.Handler) []map[string]interface{} {
	t.Helper()
	code, resp := tGet(t, h, "/dispatch/pending")
	if code != 200 || resp["ok"] != true {
		t.Fatalf("取单应 200 ok，实得 %v %v", code, resp)
	}
	raw, _ := resp["items"].([]interface{})
	out := make([]map[string]interface{}, 0, len(raw))
	for _, it := range raw {
		if m, ok := it.(map[string]interface{}); ok {
			out = append(out, m)
		}
	}
	return out
}

// TestMX1BridgeLegFullCycle 桥腿全周期：queued 受理只入队（不推已报、不落委托），
// 取单原子置 inflight（二次取单为空），order_result ok 结算后 已报→已成 自动推进，
// signal 锚回填真实委托号，成交现金入账。
func TestMX1BridgeLegFullCycle(t *testing.T) {
	h, b, rec := newTestGateway()
	mustSwitchBroker(t, h, "queued")
	code, resp := post(t, h, "/order", `{"signal_id":"MX1-A","code":"600519.SH","side":"买入","price":1500,"qty":100}`)
	if code != 200 || resp["ok"] != true {
		t.Fatalf("queued 受理应 200 ok：%v %v", code, resp)
	}
	seq, _ := resp["order_id"].(string)
	if seq != "seq:1" {
		t.Fatalf("queued 受理应回派发引用 seq:1（对齐实网关 seq:<n> 契约），实得 %q", seq)
	}
	// 入队≠受理成交：不得推任何 order 事件、账本无委托行
	if len(rec.find("order", "")) != 0 {
		t.Fatalf("入队阶段绝不允许提前推 order 事件: %+v", rec.events)
	}
	b.mu.Lock()
	nOrders := len(b.orders)
	b.mu.Unlock()
	if nOrders != 0 {
		t.Fatalf("入队阶段账本必须为空（等桥执行）: %d 行", nOrders)
	}
	items := takePending(t, h)
	if len(items) != 1 || items[0]["seq"] != "seq:1" || items[0]["kind"] != "order" ||
		items[0]["signal_id"] != "MX1-A" || items[0]["code"] != "600519.SH" {
		t.Fatalf("取单形状错误（须 pending 形态的 order 行）: %+v", items)
	}
	if again := takePending(t, h); len(again) != 0 {
		t.Fatalf("pending→inflight 必须原子标记，二次取单不许重放: %+v", again)
	}
	// 桥回报成交前置：order_result ok → 网关侧建委托、推"已报"、延时成交推"已成"+trade
	code, resp = post(t, h, "/dispatch/result", `{"type":"order_result","seq":"seq:1","ok":true,"order_id":"900001"}`)
	if code != 200 || resp["ok"] != true {
		t.Fatalf("order_result 结算应 200 ok：%v %v", code, resp)
	}
	evs := rec.find("order", "已报")
	if len(evs) != 1 || evs[0]["order_id"] != "900001" || evs[0]["signal_id"] != "MX1-A" {
		t.Fatalf("结算后必须且只推一条带真实委托号的 已报: %+v", evs)
	}
	if !rec.waitFor(func() bool { return len(rec.find("trade", "")) == 1 }, 2*time.Second) {
		t.Fatal("order_result 后成交推进器未把状态推到 已成+trade")
	}
	if done := rec.find("order", "已成"); len(done) != 1 {
		t.Fatalf("桥腿成交必须推且只推一条 已成: %+v", done)
	}
	b.mu.Lock()
	st := b.orders["900001"].Status
	sig := b.signal["MX1-A"]
	cash := b.cash
	b.mu.Unlock()
	if st != "已成" || sig != "900001" {
		t.Fatalf("终态/幂等锚回填错误: status=%s signal=%s", st, sig)
	}
	if cash != 1000000-150000 {
		t.Fatalf("桥腿成交现金未入账: %v", cash)
	}
}

// TestMX1BridgeLegReject 负回报：order_result ok=false → 已废终态 + 拒因透出，无成交。
func TestMX1BridgeLegReject(t *testing.T) {
	h, b, rec := newTestGateway()
	mustSwitchBroker(t, h, "queued")
	if _, resp := post(t, h, "/order", `{"signal_id":"MX1-R","code":"600519.SH","side":"买入","price":1500,"qty":100}`); resp["order_id"] != "seq:1" {
		t.Fatalf("受理引用错误: %+v", resp)
	}
	takePending(t, h)
	code, resp := post(t, h, "/dispatch/result", `{"type":"order_result","seq":"seq:1","ok":false,"err":"资金不足"}`)
	if code != 200 || resp["ok"] != true {
		t.Fatalf("负回报本身应 200（结算成功）：%v %v", code, resp)
	}
	evs := rec.find("order", "已废")
	if len(evs) != 1 || evs[0]["reason"] != "资金不足" || evs[0]["signal_id"] != "MX1-R" {
		t.Fatalf("ok=false 必须推 已废+拒因: %+v", rec.events)
	}
	time.Sleep(80 * time.Millisecond) // 越过成交延时窗口做负证：废单绝不产生成交
	if len(rec.find("trade", "")) != 0 {
		t.Fatalf("已废单出现幻影成交: %+v", rec.find("trade", ""))
	}
	b.mu.Lock()
	fills := len(b.fills)
	b.mu.Unlock()
	if fills != 0 {
		t.Fatalf("已废单不得落成交流水: %d", fills)
	}
}

// TestMX1DispatchDuplicateGuard 派发队列纵深防线：同 signal_id 在途（pending/inflight）
// 行存在时第二次入队必须被拒（镜像 §CLAIMRELEASE 防线①的判重语义）。
func TestMX1DispatchDuplicateGuard(t *testing.T) {
	_, b, _ := newTestGateway()
	b.activeBroker = "queued"
	seq, err := b.dispatchEnqueueOrder("MX1-D", "600000.SH", "买入", "s", 10, 100)
	if err != nil || seq != "seq:1" {
		t.Fatalf("首笔入队应成功: seq=%s err=%v", seq, err)
	}
	// HTTP 正规面同 sid 重发：被 signal 幂等索引先挡（返回原引用，不产生第二行）
	// ——这里直接二次调 enqueue 证明**队列自身**那道防线独立存在（占位被释放/旁路场景）。
	if _, err2 := b.dispatchEnqueueOrder("MX1-D", "600000.SH", "买入", "s", 10, 100); err2 == nil {
		t.Fatal("同 signal_id 在途二次入队必须被派发队列拦下（§CLAIMRELEASE 镜像防线失效）")
	}
	// 结算成 已废 后不再算在途：同 sid 允许重新入队（与实网关 done 行不参与判重同口径）
	if _, ok := b.dispatchSettle("seq:1", map[string]interface{}{"ok": false}); !ok {
		t.Fatal("结算 seq:1 应成功")
	}
	if _, err3 := b.dispatchEnqueueOrder("MX1-D", "600000.SH", "买入", "s", 10, 100); err3 != nil {
		t.Fatalf("done 行不应挡住新的下单（误伤重试）: %v", err3)
	}
}

// TestMX1CancelLeg 撤单腿：queued 下 /cancel 入队不即时生效（受理≠已撤），
// cancel_result ok → 已撤且延时成交被状态守卫拦截；失败分支回"已报"可成交态。
func TestMX1CancelLeg(t *testing.T) {
	b := newBook("MOCK0001")
	rec := &eventRecorder{}
	h := buildHandler(b, "t0", 300*time.Millisecond, rec.record, false) // 长延时留出撤单窗口
	mustSwitchBroker(t, h, "queued")
	if _, resp := post(t, h, "/order", `{"signal_id":"MX1-C","code":"600519.SH","side":"卖出","price":1500,"qty":100}`); resp["order_id"] != "seq:1" {
		t.Fatalf("受理引用错误: %+v", resp)
	}
	b.seedPositions([]string{"600519.SH,贵州茅台,200,1400"})
	takePending(t, h)
	if _, resp := post(t, h, "/dispatch/result", `{"type":"order_result","seq":"seq:1","ok":true,"order_id":"900100"}`); resp["ok"] != true {
		t.Fatalf("order_result 结算失败: %+v", resp)
	}
	// 撤单入队（queued）：状态先挂中间态，不得推"已撤"
	code, resp := post(t, h, "/cancel", `{"order_id":"900100"}`)
	if code != 200 || resp["ok"] != true {
		t.Fatalf("queued 撤单受理应 200 ok：%v %v", code, resp)
	}
	if len(rec.find("order", "已撤")) != 0 {
		t.Fatal("撤单刚入队就推 已撤＝吞掉桥执行环节（假成功形态）")
	}
	cancels := takePending(t, h)
	if len(cancels) != 1 || cancels[0]["kind"] != "cancel" || cancels[0]["order_id"] != "900100" {
		t.Fatalf("撤单派发项形状错误: %+v", cancels)
	}
	// 分支一：失败 → 回"已报"，委托仍可成交（real: _apply_cancel_result ok=False 推 已报）
	if code, _ = post(t, h, "/dispatch/result", `{"type":"cancel_result","seq":"seq:2","ok":false,"err":"桥撤单失败"}`); code != 200 {
		t.Fatalf("cancel_result(失败) 应 200: %v", code)
	}
	b.mu.Lock()
	mid := b.orders["900100"].Status
	b.mu.Unlock()
	if mid != "已报" {
		t.Fatalf("撤单失败必须回可成交态 已报，实得 %s", mid)
	}
	// 分支二：成功 → 已撤，且成交窗口到点后不产生幻影成交
	post(t, h, "/cancel", `{"order_id":"900100"}`)
	if c2 := takePending(t, h); len(c2) != 1 || c2[0]["kind"] != "cancel" {
		t.Fatalf("第二次撤单未入队: %+v", c2)
	}
	if code, _ = post(t, h, "/dispatch/result", `{"type":"cancel_result","seq":"seq:3","ok":true}`); code != 200 {
		t.Fatalf("cancel_result(成功) 应 200: %v", code)
	}
	if done := rec.find("order", "已撤"); len(done) != 1 || done[0]["order_id"] != "900100" {
		t.Fatalf("撤单成功必须推一条 已撤: %+v", rec.events)
	}
	time.Sleep(500 * time.Millisecond) // 越过 300ms 成交延时：已撤单绝不回填成交
	if len(rec.find("trade", "")) != 0 {
		t.Fatalf("已撤单出现幻影成交（撤单竞态守卫在桥腿失效）: %+v", rec.find("trade", ""))
	}
}

// TestMX1ManualTradeLeg 桥成交腿：trade 回报对指定委托做人工成交推进（部成→已成），
// 与 order_result 自动挂的 simulateFill 互不双计（终态/部成后自动腿被守卫拦截）。
func TestMX1ManualTradeLeg(t *testing.T) {
	b := newBook("MOCK0001")
	rec := &eventRecorder{}
	h := buildHandler(b, "t0", 150*time.Millisecond, rec.record, false)
	// xt 直发建委托（已报挂账），随后立刻用 trade 腿吃掉全部数量：
	// 150ms 后 simulateFill 到点时状态已是 已成 → 守卫跳过，无第二笔成交。
	_, resp := post(t, h, "/order", `{"signal_id":"MX1-T","code":"600519.SH","side":"买入","price":1500,"qty":400}`)
	oid, _ := resp["order_id"].(string)
	if code, r := post(t, h, "/dispatch/result", `{"type":"trade","seq":"","order_id":"`+oid+`","qty":200,"price":1500}`); code != 200 || r["ok"] != true {
		t.Fatalf("trade 半笔应 200 ok: %v %v", code, r)
	}
	b.mu.Lock()
	st1 := b.orders[oid].Status
	qf1 := b.orders[oid].QtyFilled
	b.mu.Unlock()
	if st1 != "部成" || qf1 != 200 {
		t.Fatalf("半笔成交后应 部成/累计200，实得 %s/%d", st1, qf1)
	}
	if code, _ := post(t, h, "/dispatch/result", `{"type":"trade","order_id":"`+oid+`","qty":200,"price":1500}`); code != 200 {
		t.Fatalf("trade 余量应 200: %v", code)
	}
	time.Sleep(300 * time.Millisecond) // 越过 simulateFill 延时做负证
	b.mu.Lock()
	st2 := b.orders[oid].Status
	fills := len(b.fills)
	b.mu.Unlock()
	if st2 != "已成" || fills != 2 {
		t.Fatalf("人工成交腿终态/流水数错误（双计嫌疑）: status=%s fills=%d", st2, fills)
	}
	if len(rec.find("trade", "")) != 2 {
		t.Fatalf("trade 事件应恰为人工两笔（自动腿必须被守卫拦截）: %+v", rec.find("trade", ""))
	}
}

// TestMX1DispatchErrorsAndHeartbeat 契约负锁：unknown seq→404、未知类型→400、
// enqueue 非 diag→400、diag 正常往返、heartbeat/positions 观察腿。
func TestMX1DispatchErrorsAndHeartbeat(t *testing.T) {
	h, b, rec := newTestGateway()
	if code, _ := post(t, h, "/dispatch/result", `{"type":"order_result","seq":"seq:404","ok":true}`); code != 404 {
		t.Fatalf("unknown seq 必须 404，实得 %d", code)
	}
	if code, _ := post(t, h, "/dispatch/result", `{"type":"teleport","seq":"seq:1"}`); code != 400 {
		t.Fatalf("未知回报类型必须 400（桥升级事件要能被发现），实得 %d", code)
	}
	if code, _ := post(t, h, "/dispatch/enqueue", `{"kind":"order","signal_id":"backdoor"}`); code != 400 {
		t.Fatalf("运维注入口只许 diag，order 注入必须 400，实得 %d", code)
	}
	code, resp := post(t, h, "/dispatch/enqueue", `{"kind":"diag","signal_id":"MX1-DIAG"}`)
	if code != 200 || resp["ok"] != true {
		t.Fatalf("diag 注入应 200 ok: %v %v", code, resp)
	}
	dseq, _ := resp["seq"].(string)
	items := takePending(t, h)
	if len(items) != 1 || items[0]["kind"] != "diag" || items[0]["seq"] != dseq {
		t.Fatalf("diag 项未被取到: %+v", items)
	}
	if code, _ = post(t, h, "/dispatch/result", `{"type":"diag","seq":"`+dseq+`","dump":{"rows":1}}`); code != 200 {
		t.Fatalf("diag 回报应 200: %d", code)
	}
	b.mu.Lock()
	doneStatus := b.dispatch[len(b.dispatch)-1].Status
	b.mu.Unlock()
	if doneStatus != "done" {
		t.Fatalf("diag 结算后应 done: %s", doneStatus)
	}
	if code, r := post(t, h, "/dispatch/result", `{"type":"heartbeat","seq":""}`); code != 200 || r["ok"] != true {
		t.Fatalf("heartbeat 应 200 ok: %v %v", code, r)
	}
	b.mu.Lock()
	beatSet := !b.bridgeBeat.IsZero()
	b.mu.Unlock()
	if !beatSet {
		t.Fatal("heartbeat 未落桥心跳观察位")
	}
	if code, _ := post(t, h, "/dispatch/result", `{"type":"positions","positions":[{"ts_code":"600000.SH","qty":100}]}`); code != 200 {
		t.Fatalf("positions 快照腿应 200: %d", code)
	}
	if len(rec.find("positions", "")) != 1 {
		t.Fatalf("positions 快照必须原样转发首尔（对账权威腿被吞）: %+v", rec.events)
	}
}

// TestMX1OrderGateBoardLotAndCap 整手/金额帽开关正反两证：默认（与实网关一致）拒非整手、
// 拒超限金额、放行板块合法量；-relax-order-check 显式放宽回到旧宽松行为。
func TestMX1OrderGateBoardLotAndCap(t *testing.T) {
	h, b, _ := newTestGateway()
	// 主板非整手 → 400（board lot 文案对齐实网关）
	code, resp := post(t, h, "/order", `{"signal_id":"G1","code":"600519.SH","side":"买入","price":10,"qty":150}`)
	if code != 400 || !strings.Contains(toStr(resp["err"]), "board lot") {
		t.Fatalf("主板买入 150 股必须 400 board lot: %v %v", code, resp)
	}
	// 科创板最低 200、1 股递增
	if code, _ = post(t, h, "/order", `{"signal_id":"G2","code":"688111.SH","side":"买入","price":10,"qty":100}`); code != 400 {
		t.Fatalf("科创板 100 股必须 400: %d", code)
	}
	if code, _ = post(t, h, "/order", `{"signal_id":"G3","code":"688111.SH","side":"买入","price":10,"qty":201}`); code != 200 {
		t.Fatalf("科创板 201 股（200 起 1 股递增）必须放行: %d", code)
	}
	// 创业板 100 起 1 股递增
	if code, _ = post(t, h, "/order", `{"signal_id":"G4","code":"300750.SZ","side":"买入","price":10,"qty":101}`); code != 200 {
		t.Fatalf("创业板 101 股必须放行: %d", code)
	}
	// 卖方向不限整手（零股清仓）
	if code, _ = post(t, h, "/order", `{"signal_id":"G5","code":"600519.SH","side":"卖出","price":10,"qty":1}`); code != 200 {
		t.Fatalf("卖出 1 股（零股）必须放行: %d", code)
	}
	// 金额帽：0=关闭（默认），>0 买卖双向拒超
	b.mu.Lock()
	b.maxOrderAmount = 1000
	b.mu.Unlock()
	if code, _ = post(t, h, "/order", `{"signal_id":"G6","code":"600519.SH","side":"买入","price":1500,"qty":100}`); code != 400 {
		t.Fatalf("金额 150000 超帽 1000 必须 400: %d", code)
	}
	if code, _ = post(t, h, "/order", `{"signal_id":"G7","code":"600519.SH","side":"卖出","price":1500,"qty":100}`); code != 400 {
		t.Fatalf("金额帽为买卖双向，卖出超帽同样必须 400: %d", code)
	}
	// 显式放宽：非整手与超金额一律放行（旧 mock 行为，联调专用）
	b.mu.Lock()
	b.relaxOrderCheck = true
	b.mu.Unlock()
	if code, _ = post(t, h, "/order", `{"signal_id":"G8","code":"600519.SH","side":"买入","price":1500,"qty":150}`); code != 200 {
		t.Fatalf("-relax-order-check 后非整手+超金额必须放行: %d", code)
	}
}

// TestMX1DispatchRequiresAuth 负锁：/dispatch 三腿全部在 Bearer 鉴权之内——
// 无鉴权的取单/回报口等于把"桥"的位置让给任何网络访客（§0926E2E-W2C 同族防线）。
func TestMX1DispatchRequiresAuth(t *testing.T) {
	h, _, _ := newTestGateway()
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/dispatch/pending", ""},
		{http.MethodPost, "/dispatch/result", `{"type":"heartbeat"}`},
		{http.MethodPost, "/dispatch/enqueue", `{"kind":"diag"}`},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s 裸访问必须 401，实得 %d", tc.method, tc.path, rec.Code)
		}
	}
}

// toStr 任意值转字符串（断言错误文案用）。
func toStr(v interface{}) string {
	s, _ := v.(string)
	return s
}
