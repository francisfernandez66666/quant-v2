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
	h := buildHandler(b, "t0", 20*time.Millisecond, rec.record)
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
