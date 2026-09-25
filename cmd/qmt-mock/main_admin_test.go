package main

// §0925EVE-W3-G mock 第三态人工收敛契约面单测：
// 实网关 gateway.py 有 GET /admin/status（unresolved_orders 观察位）与
// POST /admin/order-confirm（released/settled 人工改判），Go 侧 qmt_admin.go
// 已按该形状解码并透传 4xx 结论——但 mock 此前没有这两个路由，导致「待核对清单 +
// 人工收敛」全链路在本地 UAT 栈完全测不到（又是「有防线没接线」同族）。
// 本测试钉四件事：①注入→清单回显→released 删行 的闭环；②settled 终态口径
// （缺省「已撤」、可指定）与 400/404 业务拒绝不洗 200；③[:20] 截断与全量计数
// （truncated 显式化的数据源头）；④mock-force-status 不推回报地把单改成终态后，
// /cancel 按 §R4-1 契约回 409——这是 kill-switch 撤单失败明细（§0925EVE-A2）
// 在 E2E 里可复现的前提。
// English: contract tests for the mock's third-state admin surface (unresolved list,
// manual confirm, [:20] truncation, forced-terminal-status → cancel 409 race).

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// getJSON 向 mock 发带 Bearer 的 GET，返回状态码与解析后的响应体。
// （getJSON issues an authenticated GET and decodes the JSON body.）
func getJSON(t *testing.T, h http.Handler, path string) (int, map[string]interface{}) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer t0")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// TestAdminUnresolveReleaseFlow 注入第三态行 → /admin/status 回显 → released 收敛删行闭环。
func TestAdminUnresolveReleaseFlow(t *testing.T) {
	h, _, _ := newTestGateway()
	code, _ := post(t, h, "/admin/mock-unresolve", `{"signal_id":"sig-U1","code":"600000.SH","side":"买入","qty":100,"dispatch_in_flight":false}`)
	if code != 200 {
		t.Fatalf("注入待核对行应 200，得 %d", code)
	}
	code, body := getJSON(t, h, "/admin/status")
	if code != 200 {
		t.Fatalf("/admin/status 应 200，得 %d", code)
	}
	if cnt, _ := body["unresolved_count"].(float64); cnt != 1 {
		t.Fatalf("unresolved_count 应为 1，得 %v", body["unresolved_count"])
	}
	rows, _ := body["unresolved_orders"].([]interface{})
	if len(rows) != 1 {
		t.Fatalf("清单应 1 行，得 %d", len(rows))
	}
	row, _ := rows[0].(map[string]interface{})
	if row["signal_id"] != "sig-U1" || row["code"] != "600000.SH" {
		t.Fatalf("行内容错配: %v", row)
	}
	// 派发中位必须如实回显（true 会被面板渲染成「可自动收敛」，语义反了就是误导）
	if row["dispatch_in_flight"] != false {
		t.Fatalf("dispatch_in_flight 应回显 false，得 %v", row["dispatch_in_flight"])
	}
	code, conf := post(t, h, "/admin/order-confirm", `{"signal_id":"sig-U1","decision":"released"}`)
	if code != 200 || conf["ok"] != true || conf["released"] != true {
		t.Fatalf("released 应 200+ok+released，得 %d %v", code, conf)
	}
	_, body2 := getJSON(t, h, "/admin/status")
	if cnt, _ := body2["unresolved_count"].(float64); cnt != 0 {
		t.Fatalf("released 后计数应归零，得 %v", body2["unresolved_count"])
	}
}

// TestAdminConfirmSettledAndRejections settled 终态口径（缺省已撤/可指定）+ 400/404 业务拒绝原样回传。
func TestAdminConfirmSettledAndRejections(t *testing.T) {
	h, _, _ := newTestGateway()
	if code, _ := post(t, h, "/admin/mock-unresolve", `{"signal_id":"sig-S1","code":"000001.SZ","side":"卖出","qty":200}`); code != 200 {
		t.Fatalf("注入应 200，得 %d", code)
	}
	// 非法 decision → 400（Go 侧 adminDo 按 4xx 体结构化透传，不得洗成 200）
	if code, _ := post(t, h, "/admin/order-confirm", `{"signal_id":"sig-S1","decision":"ignore"}`); code != http.StatusBadRequest {
		t.Fatalf("非法 decision 应 400，得 %d", code)
	}
	// 无此单 → 404
	if code, _ := post(t, h, "/admin/order-confirm", `{"signal_id":"sig-NOPE","decision":"released"}`); code != http.StatusNotFound {
		t.Fatalf("无此单应 404，得 %d", code)
	}
	// settled 缺省终态「已撤」（真实柜台有此单时人工回填 order_id，网关记终态解锁占位）
	code, conf := post(t, h, "/admin/order-confirm", `{"signal_id":"sig-S1","decision":"settled","order_id":"REAL001"}`)
	if code != 200 || conf["ok"] != true || conf["status"] != "已撤" {
		t.Fatalf("settled 缺省应 200+已撤，得 %d %v", code, conf)
	}
	// 指定 status 时如实回传（面板按网关结论显示，不自行改写）
	if code, _ := post(t, h, "/admin/mock-unresolve", `{"signal_id":"sig-S2","code":"000002.SZ","side":"买入","qty":100}`); code != 200 {
		t.Fatalf("注入 sig-S2 应 200，得 %d", code)
	}
	_, conf2 := post(t, h, "/admin/order-confirm", `{"signal_id":"sig-S2","decision":"settled","status":"已成"}`)
	if conf2["status"] != "已成" {
		t.Fatalf("settled 指定终态应回显 已成，得 %v", conf2["status"])
	}
	// Bearer 缺失 → 401（管理面同样在鉴权中间件之后，不存在免鉴权观察位）
	req := httptest.NewRequest(http.MethodGet, "/admin/status", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("无 Bearer 应 401，得 %d", rec.Code)
	}
}

// TestAdminStatusTruncation20 21 行注入 → 清单只回 20 条、count 报全量 21（truncated 显式化的数据源头）。
// 旧网关兼容位：unresolved_count 缺失时 Go 侧以清单长度为准——本 mock 恒带计数键，
// 该兼容分支由 Go 侧 qmt_admin_test 的合成响应覆盖，不在这里重复。
func TestAdminStatusTruncation20(t *testing.T) {
	h, _, _ := newTestGateway()
	for i := 0; i < 21; i++ {
		body := fmt.Sprintf(`{"signal_id":"sig-T%02d","code":"600000.SH","side":"买入","qty":100}`, i)
		if code, _ := post(t, h, "/admin/mock-unresolve", body); code != 200 {
			t.Fatalf("批量注入失败 i=%d -> %d", i, code)
		}
	}
	_, body := getJSON(t, h, "/admin/status")
	rows, _ := body["unresolved_orders"].([]interface{})
	if len(rows) != 20 {
		t.Fatalf("清单应截断到 20 行，得 %d", len(rows))
	}
	if cnt, _ := body["unresolved_count"].(float64); cnt != 21 {
		t.Fatalf("count 应为全量 21，得 %v", body["unresolved_count"])
	}
}

// TestAdminForceStatusDrivesCancel409 mock-force-status 不推回报地把在挂单改成已成，
// /cancel 必须按 §R4-1 契约回 409——E2E 用它造「引擎认为已报、网关已终态」的撤单失败，
// 从数据源头把 HaltAllResult.Failed 明细（§0925EVE-A2）变成可测形态。
func TestAdminForceStatusDrivesCancel409(t *testing.T) {
	// 成交延时给到 500ms：强改必须发生在成交协程唤醒之前的窗口内，20ms 默认档
	// 在 CI 慢机上可能被吃掉窗口（等值断言不靠"goroutine 刚好来得及"，§并发用例分相口径）。
	b := newBook("MOCK0001")
	rec := &eventRecorder{}
	h := buildHandler(b, "t0", 500*time.Millisecond, rec.record, false)
	code, o := post(t, h, "/order", `{"code":"600000.SH","name":"浦发银行","side":"买入","price":10,"qty":100,"signal_id":"sig-F1"}`)
	if code != 200 {
		t.Fatalf("下单应 200，得 %d", code)
	}
	oid, _ := o["order_id"].(string)
	if oid == "" {
		t.Fatalf("下单响应缺 order_id: %v", o)
	}
	// 未知单强改 → 404（注入面同样不吃「静默成功」）
	if code, _ := post(t, h, "/admin/mock-force-status", `{"order_id":"NOPE","status":"已成"}`); code != http.StatusNotFound {
		t.Fatalf("未知单强改应 404，得 %d", code)
	}
	// 立刻强改成终态（不等 20ms 成交协程；强改路径**不推回报**，引擎侧无从得知）
	if code, _ := post(t, h, "/admin/mock-force-status", `{"order_id":"`+oid+`","status":"已成"}`); code != 200 {
		t.Fatalf("强改状态应 200，得 %d", code)
	}
	if code, conf := post(t, h, "/cancel", `{"order_id":"`+oid+`"}`); code != http.StatusConflict {
		t.Fatalf("终态撤单应 409，得 %d %v", code, conf)
	}
	// 强改自身不得偷推 order 事件（推了引擎就会跟到终态，竞态形态消失，测试失真）
	if evs := rec.find("order", ""); len(evs) != 1 {
		t.Fatalf("应只有下单受理 1 条 order 事件（强改不推），实得 %d: %v", len(evs), evs)
	}
}
