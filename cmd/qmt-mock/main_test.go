package main

// §U-4（2026-09-14 UAT 修复）mock 委托生命周期单测：
// 真实网关在 xtquant 回调里同时推 order 状态事件（已报/已成/已撤）与 trade 成交事件，
// 旧 mock 只有 trade——本地联调永远测不到委托状态机（引擎侧委托行卡"已报"）。
// 本测试驱动 buildHandler 验证四件事：受理推"已报"、成交先"已成"后 trade、
// 撤单推"已撤"且撤单竞态下不产生幻影成交、终态撤单按实柜台契约回 409/未知单 404。
// English: unit tests for the §U-4 order lifecycle events of the mock gateway
// (submit/fill/cancel pushes, cancel-race guard, 409/404 contract).

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// eventRecorder 线程安全地收集 mock 推送的回报事件（push 回调注入用）。
// （eventRecorder collects pushed report payloads thread-safely via the injected push func.）
type eventRecorder struct {
	mu     sync.Mutex
	events []map[string]interface{}
}

// record 追加一条回报；done 通道由调用方在期望事件到达时读取（异步成交用）。
func (e *eventRecorder) record(p map[string]interface{}) {
	e.mu.Lock()
	e.events = append(e.events, p)
	e.mu.Unlock()
}

// find 按 type+status 过滤已收集事件（status 传空串表示只匹配 type）。
func (e *eventRecorder) find(typ, status string) []map[string]interface{} {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []map[string]interface{}
	for _, ev := range e.events {
		if ev["type"] == typ && (status == "" || ev["status"] == status) {
			out = append(out, ev)
		}
	}
	return out
}

// waitFor 轮询等待谓词满足（异步成交协程完成后事件才可见），超时返回 false。
func (e *eventRecorder) waitFor(pred func() bool, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if pred() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

// newTestGateway 构造带小延时的 mock 服务：返回 handler、账本与事件收集器。
// token 固定 "t0"，成交延时 20ms——测试里撤单窗口与成交窗口都可控。
func newTestGateway() (http.Handler, *book, *eventRecorder) {
	b := newBook("MOCK0001")
	rec := &eventRecorder{}
	h := buildHandler(b, "t0", 20*time.Millisecond, rec.record, false)
	return h, b, rec
}

// post 向 mock 发 JSON 请求，返回状态码与解析后的响应体。
func post(t *testing.T, h http.Handler, path, body string) (int, map[string]interface{}) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer t0")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// TestMockSubmitPushesAcceptedEvent 验证受理即推"已报" order 事件（不再只有 trade）。
func TestMockSubmitPushesAcceptedEvent(t *testing.T) {
	h, b, rec := newTestGateway()
	code, resp := post(t, h, "/order", `{"signal_id":"S1","code":"600519.SH","side":"buy","price":1500,"qty":100}`)
	if code != 200 || resp["ok"] != true {
		t.Fatalf("下单应 200 ok, got %v %v", code, resp)
	}
	oid, _ := resp["order_id"].(string)
	if oid == "" {
		t.Fatal("响应缺 order_id")
	}
	// 受理事件必须立刻可见（同步 push），且携带 signal_id 供引擎侧按信号回填
	evs := rec.find("order", "已报")
	if len(evs) != 1 || evs[0]["signal_id"] != "S1" || evs[0]["order_id"] != oid {
		t.Fatalf("受理应推且只推一条 已报 order 事件: %+v", evs)
	}
	_ = b
}

// TestMockFillPushesDoneThenTrade 验证成交时序：先"已成" order 事件、后 trade 记账事件，
// 与实网关契约一致（引擎委托状态机依赖 order 推进，trade 只入账）。
func TestMockFillPushesDoneThenTrade(t *testing.T) {
	h, _, rec := newTestGateway()
	_, resp := post(t, h, "/order", `{"signal_id":"S2","code":"600519.SH","side":"buy","price":1500,"qty":100}`)
	oid := resp["order_id"].(string)
	// 等待异步成交协程把"已成"+trade 推完
	if !rec.waitFor(func() bool { return len(rec.find("trade", "")) == 1 }, 2*time.Second) {
		t.Fatal("延时后未收到 trade 事件")
	}
	// 顺序断言：已成 order 必须在同单 trade 之前入列
	idxDone, idxTrade := -1, -1
	for i, ev := range rec.events {
		if ev["order_id"] != oid {
			continue
		}
		if ev["type"] == "order" && ev["status"] == "已成" && idxDone < 0 {
			idxDone = i
		}
		if ev["type"] == "trade" && idxTrade < 0 {
			idxTrade = i
		}
	}
	if idxDone < 0 || idxTrade < 0 || idxDone > idxTrade {
		t.Fatalf("应先 已成 后 trade: done=%d trade=%d", idxDone, idxTrade)
	}
}

// TestMockCancelRaceNoPhantomFill 验证撤单竞态守卫：受理→立即撤单→成交延时到点，
// 不得回填成交（无"已成"无 trade、持仓不变），撤单须推"已撤"事件。
// （§U-4 实测缺陷 MOCK000002：旧 mock 撤单仍幻影成交入持仓。）
func TestMockCancelRaceNoPhantomFill(t *testing.T) {
	h, b, rec := newTestGateway()
	_, resp := post(t, h, "/order", `{"signal_id":"S3","code":"600519.SH","side":"buy","price":1500,"qty":100}`)
	oid := resp["order_id"].(string)
	// 成交延时 20ms，此刻抢在其前撤单
	code, cr := post(t, h, "/cancel", `{"order_id":"`+oid+`"}`)
	if code != 200 || cr["ok"] != true {
		t.Fatalf("在途单撤单应成功: %v %v", code, cr)
	}
	if len(rec.find("order", "已撤")) != 1 {
		t.Fatal("撤单成功必须推 已撤 order 事件")
	}
	// 等过成交窗口，确认竞态守卫生效：无幻影成交
	time.Sleep(150 * time.Millisecond)
	if len(rec.find("order", "已成")) != 0 || len(rec.find("trade", "")) != 0 {
		t.Fatalf("已撤单绝不能再有 成交事件/trade: %v", rec.events)
	}
	b.mu.Lock()
	st := b.orders[oid].Status
	b.mu.Unlock()
	if st != "已撤" {
		t.Fatalf("终态应保持 已撤, got %s", st)
	}
	if len(b.snapshotPositions()) != 0 {
		t.Fatal("撤单后不得流入持仓")
	}
}

// TestMockCancelTerminalCodes 验证实柜台契约：终态（已成）撤单回 409，未知单回 404。
func TestMockCancelTerminalCodes(t *testing.T) {
	h, _, rec := newTestGateway()
	_, resp := post(t, h, "/order", `{"signal_id":"S4","code":"600519.SH","side":"buy","price":1500,"qty":100}`)
	oid := resp["order_id"].(string)
	if !rec.waitFor(func() bool { return len(rec.find("trade", "")) == 1 }, 2*time.Second) {
		t.Fatal("等待成交超时")
	}
	if code, _ := post(t, h, "/cancel", `{"order_id":"`+oid+`"}`); code != http.StatusConflict {
		t.Fatalf("已成单撤单应 409, got %d", code)
	}
	if code, _ := post(t, h, "/cancel", `{"order_id":"NOPE"}`); code != http.StatusNotFound {
		t.Fatalf("未知单撤单应 404, got %d", code)
	}
}

// TestMockIdempotentSignalID 验证 signal_id 幂等：重复提交返回原 order_id，不二次下单。
func TestMockIdempotentSignalID(t *testing.T) {
	h, _, _ := newTestGateway()
	_, r1 := post(t, h, "/order", `{"signal_id":"S5","code":"600519.SH","side":"buy","price":1500,"qty":100}`)
	_, r2 := post(t, h, "/order", `{"signal_id":"S5","code":"600519.SH","side":"buy","price":1500,"qty":100}`)
	if r1["order_id"] != r2["order_id"] {
		t.Fatalf("同 signal_id 应返回原单: %v vs %v", r1["order_id"], r2["order_id"])
	}
}

// TestMockAuthRequired 验证 Bearer 鉴权面：/health 豁免，其余缺/错 token 一律 401。
func TestMockAuthRequired(t *testing.T) {
	h, _, _ := newTestGateway()
	if rec := httptest.NewRecorder(); func() bool {
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
		return rec.Code == 200
	}() == false {
		t.Fatal("/health 应免鉴权")
	}
	req := httptest.NewRequest(http.MethodGet, "/state", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("无 token 访问 /state 应 401, got %d", rec.Code)
	}
}

// posOf 从持仓快照（切片）中按 ts_code 取单只持仓（§P2-14 断言辅助）。
func posOf(b *book, code string) map[string]interface{} {
	for _, p := range b.snapshotPositions() {
		if p["ts_code"] == code {
			return p
		}
	}
	return nil
}

// newModeGateway 构造指定 fill-mode 的 mock 服务（§P2-14 部成/废单回归用）。
func newModeGateway(mode string, chaos bool) (http.Handler, *book, *eventRecorder) {
	b := newBook("MOCK0001")
	b.fillMode = mode
	rec := &eventRecorder{}
	h := buildHandler(b, "t0", 20*time.Millisecond, rec.record, chaos)
	return h, b, rec
}

// TestMockPartialFillLifecycle §P2-14：partial 模式下委托生命周期为
// 已报→部成（半笔 trade）→已成（余笔 trade），两笔 trade 各带唯一 trade_id，
// 持仓只入账一次总量（qty 精确累加，不重不漏）。
func TestMockPartialFillLifecycle(t *testing.T) {
	h, b, rec := newModeGateway("partial", false)
	_, resp := post(t, h, "/order", `{"signal_id":"P1","code":"600519.SH","side":"买入","price":1500,"qty":400}`)
	oid := resp["order_id"].(string)
	if !rec.waitFor(func() bool { return len(rec.find("trade", "")) == 2 }, 3*time.Second) {
		t.Fatalf("部成模式应推两笔 trade, got %d", len(rec.find("trade", "")))
	}
	// 生命周期顺序：已报 → 部成 → 已成
	var seq []string
	for _, ev := range rec.events {
		if ev["type"] == "order" && ev["order_id"] == oid {
			seq = append(seq, ev["status"].(string))
		}
	}
	want := []string{"已报", "部成", "已成"}
	if len(seq) != 3 || seq[0] != want[0] || seq[1] != want[1] || seq[2] != want[2] {
		t.Fatalf("生命周期应 %v, got %v", want, seq)
	}
	// 两笔 trade 的 trade_id 唯一（serial 不塌缩，对齐 §P0-1b 幂等口径）
	tid1, _ := rec.find("trade", "")[0]["trade_id"].(string)
	tid2, _ := rec.find("trade", "")[1]["trade_id"].(string)
	if tid1 == "" || tid2 == "" || tid1 == tid2 {
		t.Fatalf("两笔 trade 的 trade_id 应唯一非空: %q vs %q", tid1, tid2)
	}
	// 持仓精确入账 400 股（半笔+余笔，不重复记账）
	p := posOf(b, "600519.SH")
	if p == nil || p["qty"].(int) != 400 {
		t.Fatalf("持仓应精确 400 股, got %+v", p)
	}
}

// TestMockRejectNoFill §P2-14：reject 模式下推 order 废单事件（带 reason），
// 不推 trade、不建持仓——对齐实网关拒单链路（拒因回传/重报禁用）。
func TestMockRejectNoFill(t *testing.T) {
	h, b, rec := newModeGateway("reject", false)
	_, resp := post(t, h, "/order", `{"signal_id":"R1","code":"600519.SH","side":"买入","price":1500,"qty":100}`)
	oid := resp["order_id"].(string)
	if !rec.waitFor(func() bool { return len(rec.find("order", "废单")) == 1 }, 2*time.Second) {
		t.Fatal("reject 模式应推 废单 order 事件")
	}
	evt := rec.find("order", "废单")[0]
	if evt["order_id"] != oid || evt["reason"] == nil {
		t.Fatalf("废单事件缺 order_id/reason: %+v", evt)
	}
	if len(rec.find("trade", "")) != 0 {
		t.Fatal("废单单绝不应产生 trade 成交")
	}
	if p := posOf(b, "600519.SH"); p != nil {
		t.Fatalf("废单单不应建仓: %+v", p)
	}
	// 废单为终态：撤单 409（同已成）
	if code, _ := post(t, h, "/cancel", `{"order_id":"`+oid+`"}`); code != http.StatusConflict {
		t.Fatalf("废单单撤单应 409, got %d", code)
	}
}

// TestMockSettlementEndpoint §P2-14：/settlement 返回当日成交流水（serial 唯一）
// 与现金快照——引擎 SettleDay 对账权威源在 mock 下的 e2e 覆盖（§P0-1a 配套）。
func TestMockSettlementEndpoint(t *testing.T) {
	h, _, _ := newModeGateway("full", false)
	post(t, h, "/order", `{"signal_id":"ST1","code":"600519.SH","side":"buy","price":1500,"qty":100}`)
	get := func(path string) (int, map[string]interface{}) {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer t0")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var out map[string]interface{}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	// 等待成交入账（成交流水在异步成交协程里落账）
	day := time.Now().Format("2006-01-02")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, body := get("/settlement?date=" + day); len(body["trades"].([]interface{})) == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	code, body := get("/settlement?date=" + day)
	if code != 200 {
		t.Fatalf("/settlement 应 200, got %d", code)
	}
	trades, _ := body["trades"].([]interface{})
	if len(trades) != 1 {
		t.Fatalf("应返回 1 笔成交, got %d", len(trades))
	}
	tr := trades[0].(map[string]interface{})
	if tr["serial"] == nil || tr["ts_code"] != "600519.SH" || tr["qty"].(float64) != 100 {
		t.Fatalf("成交流水缺 serial/字段错: %+v", tr)
	}
	// §P2-FEE 20260918：mock 交割流水带模拟费用腿（150000×万2.5=37.5，最低 5 元），
	// 与成交回报同源，供引擎三方对账的费用差腿在 UAT 下可观测。
	if fee, _ := tr["fee"].(float64); fee <= 0 {
		t.Fatalf("/settlement 成交应带非零 fee, got %+v", tr)
	}
	cashMap, _ := body["cash"].(map[string]interface{})
	if cashMap == nil || cashMap["cash"] == nil {
		t.Fatalf("/settlement 缺 cash 快照: %+v", body)
	}
	// 缺/错 date 参数 400
	if c, _ := get("/settlement"); c != 400 {
		t.Fatalf("缺 date 应 400, got %d", c)
	}
}

// TestMockChaosTradeBeforeOrder §P2-14：chaos 模式 trade 先于 order 已成推送——
// 引擎单调状态机的乱序守卫回归（成交先到不阻断委托状态推进，不重复记账）。
func TestMockChaosTradeBeforeOrder(t *testing.T) {
	h, b, rec := newModeGateway("full", true)
	_, resp := post(t, h, "/order", `{"signal_id":"C1","code":"600519.SH","side":"buy","price":1500,"qty":100}`)
	oid := resp["order_id"].(string)
	if !rec.waitFor(func() bool { return len(rec.find("trade", "")) == 1 }, 2*time.Second) {
		t.Fatal("chaos 模式应收到 trade")
	}
	idxTrade, idxDone := -1, -1
	for i, ev := range rec.events {
		if ev["type"] == "trade" && idxTrade < 0 {
			idxTrade = i
		}
		if ev["type"] == "order" && ev["order_id"] == oid && ev["status"] == "已成" && idxDone < 0 {
			idxDone = i
		}
	}
	if idxTrade < 0 || idxDone < 0 || idxTrade > idxDone {
		t.Fatalf("chaos 下 trade 应先于 order 已成: trade=%d done=%d", idxTrade, idxDone)
	}
	if p := posOf(b, "600519.SH"); p == nil || p["qty"].(int) != 100 {
		t.Fatalf("乱序回报不应影响记账: %+v", p)
	}
}

// TestMockOversellReject §UAT-D5（2026-09-16）：卖出超仓必须整笔废单——
// 旧 mock 对持仓不足的销售单要么静默不成交（委托永挂"已报"、无废单事件、无拒因），
// 要么（partial/清仓）删整仓却按全部卖出量回补现金，制造幻影现金。
// 真柜台"证券不足"→整笔废单、持仓/现金不动、拒因回传。本用例锁死该语义。
// English: §UAT-D5 — a sell exceeding holdings must be a whole-ticket reject: no trade,
// position/cash untouched, reject reason pushed (matching the real counter's insufficient-securities).
func TestMockOversellReject(t *testing.T) {
	h, b, rec := newModeGateway("full", false)
	// 预置持仓 600519.SH 100 股 @1500，并记现金/持仓基线
	b.mu.Lock()
	b.positions["600519.SH"] = &pos{TsCode: "600519.SH", Name: "贵州茅台", Qty: 100, CostPrice: 1500, Amount: 150000, HighestPrice: 1500}
	b.mu.Unlock()
	cashBefore := b.snapshotCash()

	// 卖 200（超仓 100）→ 期望整笔废单
	_, resp := post(t, h, "/order", `{"signal_id":"OV1","code":"600519.SH","side":"卖出","price":1500,"qty":200}`)
	oid := resp["order_id"].(string)
	if !rec.waitFor(func() bool { return len(rec.find("order", "废单")) == 1 }, 2*time.Second) {
		t.Fatalf("卖超仓应推 废单 事件, got orders=%+v", rec.find("order", ""))
	}
	evt := rec.find("order", "废单")[0]
	if evt["order_id"] != oid || evt["reason"] == nil || evt["reason"] == "" {
		t.Fatalf("废单事件应带 order_id+拒因: %+v", evt)
	}
	if len(rec.find("trade", "")) != 0 {
		t.Fatal("超仓废单绝不应产生 trade 成交")
	}
	// 持仓与现金原封不动（幻影现金回归锁）
	if p := posOf(b, "600519.SH"); p == nil || p["qty"].(int) != 100 {
		t.Fatalf("超仓废单不应改动持仓: %+v", p)
	}
	if got := b.snapshotCash(); got != cashBefore {
		t.Fatalf("超仓废单不应回补现金（幻影现金）: got %.2f want %.2f", got, cashBefore)
	}
	// 部分超仓（持 100，卖 150）同样整笔废单
	if _, resp2 := post(t, h, "/order", `{"signal_id":"OV2","code":"600519.SH","side":"卖出","price":1500,"qty":150}`); true {
		oid2 := resp2["order_id"].(string)
		if !rec.waitFor(func() bool { return len(rec.find("order", "废单")) == 2 }, 2*time.Second) {
			t.Fatalf("部分超仓也应整笔废单, oid=%s", oid2)
		}
	}
	if len(rec.find("trade", "")) != 0 || posOf(b, "600519.SH")["qty"].(int) != 100 {
		t.Fatal("部分超仓后仍不应有成交/改仓/回现金")
	}
	// 合法卖出（持 100 卖 100）→ 正常成交清仓（确保没把正常路径也拦死）
	if _, _ = post(t, h, "/order", `{"signal_id":"OK1","code":"600519.SH","side":"卖出","price":1500,"qty":100}`); true {
		if !rec.waitFor(func() bool { return len(rec.find("trade", "")) == 1 }, 2*time.Second) {
			t.Fatal("合法全额卖出应正常成交")
		}
	}
	if p := posOf(b, "600519.SH"); p != nil {
		t.Fatalf("全额卖出应清仓, got %+v", p)
	}
	if got, want := b.snapshotCash(), cashBefore+100*1500.0; got != want {
		t.Fatalf("合法卖出回补现金应 %.2f, got %.2f", want, got)
	}
}

// TestMockAdminBrokerSwitch §FIX-9j：/admin/broker 切换 + /health 回读对齐实网关契约。
// 此前 mock 无该路由（404），双通道切换链路在 e2e/演练栈完全测不到。
func TestMockAdminBrokerSwitch(t *testing.T) {
	h, b, _ := newTestGateway()
	// /health 免鉴权，默认 active=xt 且带 BrokerStatus() 解析所需字段
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	var hs map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &hs); err != nil {
		t.Fatalf("/health 非法 json: %v", err)
	}
	if hs["broker"] != "xt" || hs["broker_mode"] != "xt" || hs["xt_connected"] != true || hs["queued_connected"] != true {
		t.Fatalf("/health 初始应报 xt 双通道在线, got %+v", hs)
	}
	// 切到 queued（携带鉴权，与实网关 admin 面一致）
	code, resp := post(t, h, "/admin/broker", `{"broker":"queued"}`)
	if code != 200 || resp["ok"] != true || resp["broker"] != "queued" {
		t.Fatalf("切换 queued 应 200 ok, got %d %+v", code, resp)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	_ = json.Unmarshal(rec.Body.Bytes(), &hs)
	if hs["broker"] != "queued" {
		t.Fatalf("切换后 /health 应回读 queued, got %+v", hs)
	}
	// 非法值 400（对齐网关 "broker must be one of"），且不改状态
	code, resp = post(t, h, "/admin/broker", `{"broker":"nope"}`)
	if code != http.StatusBadRequest || resp["ok"] != false {
		t.Fatalf("非法通道应 400 ok=false, got %d %+v", code, resp)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.activeBroker != "queued" {
		t.Fatalf("非法切换不得改状态, got %s", b.activeBroker)
	}
}

// TestMockQuotesEndpoint §ENH-5 批E：/quotes 契约回归——鉴权面（无 token 401）、
// 缺参 400、正常回 ticks（字段齐、价格为正、逐秒可变、tickTime 毫秒）。
// mock 的 /quotes 是 nightly/本地 e2e 里 QMT-L1 feed 的数据源，字段必须与
// 真实网关 quote_feed 及 Go 侧 data.QMTTick 解析对齐。
func TestMockQuotesEndpoint(t *testing.T) {
	h, _, _ := newTestGateway()
	get := func(path, token string) (int, map[string]interface{}) {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var out map[string]interface{}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	if code, _ := get("/quotes?codes=600000.SH", ""); code != http.StatusUnauthorized {
		t.Fatalf("/quotes 无 token 应 401, got %d", code)
	}
	if code, out := get("/quotes", "t0"); code != http.StatusBadRequest || out["ok"] != false {
		t.Fatalf("缺 codes 应 400+ok:false, got %d %v", code, out)
	}
	code, out := get("/quotes?codes=600000.SH,000001.SZ", "t0")
	if code != http.StatusOK || out["ok"] != true {
		t.Fatalf("/quotes 应 200, got %d %v", code, out)
	}
	ticks, _ := out["ticks"].(map[string]interface{})
	if len(ticks) != 2 {
		t.Fatalf("应回 2 只 tick, got %v", out["ticks"])
	}
	for _, c := range []string{"600000.SH", "000001.SZ"} {
		tk, _ := ticks[c].(map[string]interface{})
		if tk == nil {
			t.Fatalf("缺 %s tick", c)
		}
		for _, f := range []string{"lastPrice", "open", "high", "low", "prevClose", "volume", "amount"} {
			v, ok := tk[f].(float64)
			if !ok || v <= 0 {
				t.Fatalf("%s.%s 应为正数, got %v", c, f, tk[f])
			}
		}
		if ms, _ := tk["tickTime"].(float64); ms < 1e12 {
			t.Fatalf("tickTime 应为毫秒时间戳, got %v", tk["tickTime"])
		}
	}
	// 确定性伪 tick 可重复拉取：二次请求结构一致（tick 随秒推进由真实网关保证，这里锁接口稳定）。
	code2, out2 := get("/quotes?codes=600000.SH", "t0")
	if code2 != http.StatusOK || out2["ok"] != true {
		t.Fatalf("二次请求应稳定 200, got %d %v", code2, out2)
	}
	if ticks2, _ := out2["ticks"].(map[string]interface{}); len(ticks2) != 1 {
		t.Fatalf("二次 ticks 结构错误: %v", out2["ticks"])
	}
	// /health 观察字段：feed_connected 存在（Go 侧 Health 判定不解析它，仅回显）
	if _, hb := get("/health", ""); hb["feed_connected"] != true {
		t.Fatalf("/health 应带 feed_connected=true 观察字段, got %v", hb["feed_connected"])
	}
}
