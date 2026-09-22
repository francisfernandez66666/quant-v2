// side_gate_qmt_test.go — §SIDEGATE-GO / §N-4 / §M13（2026-09-22 修复批）实盘 HTTP 面回归。
//
// 覆盖两条本轮修的口子：
//  1. POST /api/positions/execute 的 side 白名单（§SIDEGATE-GO，M-1 的 Go 层）：
//     旧实现只把空串缺省成买入，"buy"/"SELL"/带空格等非法串一路透传到网关与 broker，
//     而 broker 语义是「side 不等于 '买入' 就下卖单」→ 用户点买入、柜台落卖单（方向翻转）。
//     现在非法方向必须在 HTTP 入口 400 拒掉、零触达柜台、并落 opslog 审计留痕。
//  2. POST /api/qmt/report 的 qmt_report 广播载荷必须带 tripped（§M13）：
//     前端 Positions.jsx 用 `!!msg.tripped` 覆盖熔断位，旧载荷没有该字段 →
//     任意一笔回报都把「熔断中」徽标瞬清成「正常」（要靠下一个 RTT 的 REST 纠正）。
//
// English: §SIDEGATE-GO/§M13 HTTP-layer regressions — a non-canonical side is rejected at the
// manual-order entry (never reaching the broker, where "not 买入" means SELL), and the qmt_report
// SSE payload now carries the authoritative breaker flag so the frontend badge can't be cleared.
package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/auth"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/store"
	"quant-trading-v2/internal/trading"
)

// newLiveReportServer 装配「实盘控制器 + 实盘账本 + SSE」的最小可测服务。
// 与 newExecuteTestServer 的区别：这里额外接 researchDB（handleQMTReport 需要 realDB 才走到广播），
// 并按 tripped 参数决定是否预置熔断状态。
func newLiveReportServer(t *testing.T, tripped bool) (*Server, *auth.User, *trading.Controller) {
	t.Helper()
	s, admin := newAdminTestServer(t)
	db, err := store.Open(filepath.Join(t.TempDir(), "live.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	s.researchDB = db
	cfg := config.DefaultQMTConfig()
	cfg.Enabled = true
	ctrl := trading.NewController(&execCountStub{}, db, admin.Username, cfg, nil)
	s.SetEngineController(fakeCtrl{qmt: ctrl})
	s.sse = NewSSEBroker()
	if tripped {
		ctrl.SetTripped("测试用熔断：网关失联")
	}
	return s, admin, ctrl
}

// readSSE 在超时窗口内取一条广播（nil=没有推送，让用例失败而不是永久阻塞）。
func readSSE(t *testing.T, ch chan SSEEvent) map[string]interface{} {
	t.Helper()
	select {
	case ev := <-ch:
		var m map[string]interface{}
		if err := json.Unmarshal(ev.Data, &m); err != nil {
			t.Fatalf("SSE 载荷非 JSON: %v (%s)", err, ev.Data)
		}
		return m
	case <-time.After(2 * time.Second):
		t.Fatal("2s 内未收到 SSE 广播")
		return nil
	}
}

// TestExecuteRejectsNonCanonicalSide §SIDEGATE-GO：非法方向在 Go 入口即 400，绝不触达柜台。
func TestExecuteRejectsNonCanonicalSide(t *testing.T) {
	// Arrange：全开链路（复用 §P1-7 的行情桩 + 计数执行器）
	s, admin, exec := newExecuteTestServer(t)
	// Act + Assert：三形态非法值（英文/大小写混写/带空格/简写）全部 400
	for _, bad := range []string{"buy", "SELL", "买入 ", "买", "??? "} {
		req := adminReq(s, admin, "POST", "/api/positions/execute",
			`{"code":"600000.SH","qty":100,"price":10,"side":"`+bad+`"}`)
		rr := adminDo(s, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("非法方向 %q 应 400, got %d body=%s", bad, rr.Code, rr.Body.String())
		}
		// 错误文案自解释（前端直接把原因显示给用户，不能只说 "invalid request"）
		if !strings.Contains(rr.Body.String(), "方向") {
			t.Fatalf("400 文案需说明方向非法, got %s", rr.Body.String())
		}
	}
	// 关键反例：旧缺陷里非法串会被 broker 当成「卖出」下单——这里执行器必须一次都没被触达
	if exec.calls != 0 {
		t.Fatalf("非法方向绝不能触达柜台（方向翻转的落点）, calls=%d", exec.calls)
	}
	// 合法契约不得被白名单误伤：显式买入 + 空串缺省买入都照常受理
	for i, ok := range []string{"买入", ""} {
		req := adminReq(s, admin, "POST", "/api/positions/execute",
			`{"code":"600000.SH","qty":100,"price":10,"side":"`+ok+`","client_id":"side-ok-`+string(rune('0'+i))+`"}`)
		if rr := adminDo(s, req); rr.Code != http.StatusOK {
			t.Fatalf("方向 %q（合法/空串缺省）应放行, got %d body=%s", ok, rr.Code, rr.Body.String())
		}
	}
	if exec.calls != 2 {
		t.Fatalf("两笔合法买入应各触达柜台一次, calls=%d", exec.calls)
	}
}

// TestExecuteSellSideStillAccepted 白名单不误伤回归：合法"卖出"仍照常受理。
// 方向闸的目的是拦非法串，不能顺手把卖出通道（止损/清仓依赖它）拦死——
// 闸内 §N-4 的 fail-close 已由 internal/risk 同名测试覆盖，这里只锁 HTTP 面的放行面。
func TestExecuteSellSideStillAccepted(t *testing.T) {
	s, admin, exec := newExecuteTestServer(t)
	// 无持仓时卖出：qty 校验/持仓校验放行给下游（网关/闸），入口白名单不得成为新拦点
	req := adminReq(s, admin, "POST", "/api/positions/execute",
		`{"code":"600000.SH","qty":100,"price":10,"side":"卖出","client_id":"side-sell-ok"}`)
	rr := adminDo(s, req)
	if rr.Code == http.StatusBadRequest && strings.Contains(rr.Body.String(), "方向") {
		t.Fatalf("合法卖出被方向白名单误拦: %s", rr.Body.String())
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("合法卖出应受理, got %d body=%s", rr.Code, rr.Body.String())
	}
	if exec.calls != 1 {
		t.Fatalf("卖出应触达柜台一次, calls=%d", exec.calls)
	}
}

// TestQMTReportBroadcastCarriesTripped §M13：熔断中收到的任意回报，广播载荷必须仍报 tripped=true
// （旧载荷根本没有该字段 → 前端 `!!msg.tripped` 把徽标瞬清）。
func TestQMTReportBroadcastCarriesTripped(t *testing.T) {
	s, admin, ctrl := newLiveReportServer(t, true)
	ch := s.sse.SubscribeFor(admin.ID, 0)
	t.Cleanup(func() { s.sse.UnsubscribeFor(admin.ID, ch) })

	body := `{"type":"heartbeat"}`
	rr := httptest.NewRecorder()
	s.handleQMTReport(rr, adminReq(s, admin, http.MethodPost, "/api/qmt/report", body))
	if rr.Code != http.StatusOK {
		t.Fatalf("心跳回报应 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	msg := readSSE(t, ch)
	if msg["type"] != "qmt_report" || msg["event"] != "heartbeat" {
		t.Fatalf("广播形状（type/event）漂移: %v", msg)
	}
	if _, ok := msg["tripped"]; !ok {
		t.Fatal("§M13 反例：qmt_report 载荷缺 tripped 字段，前端熔断徽标会被任意回报瞬清")
	}
	if msg["tripped"] != true {
		t.Fatalf("熔断中广播应带 tripped=true, got %v", msg["tripped"])
	}
	if at, _ := msg["at"].(string); at == "" {
		t.Fatalf("载荷应带 RFC3339 时间戳 at（前端据此判新鲜度），got %v", msg["at"])
	}
	// 广播只读：不得改动熔断状态本身（语义只加字段）
	if !ctrl.Tripped() {
		t.Fatal("广播不得顺手解熔")
	}
}

// TestQMTReportBroadcastTrippedFalseWhenNormal §M13 反向：未熔断时 tripped=false（字段存在且语义准确）。
func TestQMTReportBroadcastTrippedFalseWhenNormal(t *testing.T) {
	s, admin, _ := newLiveReportServer(t, false)
	ch := s.sse.SubscribeFor(admin.ID, 0)
	t.Cleanup(func() { s.sse.UnsubscribeFor(admin.ID, ch) })

	body := `{"type":"heartbeat"}`
	rr := httptest.NewRecorder()
	s.handleQMTReport(rr, adminReq(s, admin, http.MethodPost, "/api/qmt/report", body))
	if rr.Code != http.StatusOK {
		t.Fatalf("心跳回报应 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	msg := readSSE(t, ch)
	v, ok := msg["tripped"]
	if !ok || v != false {
		t.Fatalf("未熔断时 tripped 应为 false（且字段必须存在）, got %v ok=%v", v, ok)
	}
}
