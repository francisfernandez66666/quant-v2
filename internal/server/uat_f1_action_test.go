// ── §F-1/F-3（20260917 缺陷修复批）POST /api/action 白名单回归测试 ──
// 锁定资金安全契约：①ignore/未知 action 绝不触达实盘下单通道（旧实现把 ignore 静默归为
// 买入侧，enabled+manual+admin 下点"忽略"= 真买 100 股——P0）；②buy/sell 正常路由；
// ③链路未启用时返回 noop+原因而非伪装成功；④墓碑忽略走引擎 signalStore 失效机制且不依赖
// QMT 开关状态。
// English: §F-1/F-3 regression — "ignore" can NEVER reach the order path; unknown actions are
// rejected; the disabled-mode stub now reports status=noop explicitly.
package server

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestActionWhitelistIgnoreNeverOrders enabled+manual+admin（真实 trading.Controller+计数柜台桩）下
// 发 action=ignore：必须 200 ignored 且柜台零触达。
func TestActionWhitelistIgnoreNeverOrders(t *testing.T) {
	// Arrange：装配 enabled+manual+admin 全开的真实链路（trading.Controller + 计数柜台桩），
	// 构造一个带合法 code/strategy 的 ignore 请求——这是旧缺陷触发"真买 100 股"的完整前置。
	s, admin, exec := newExecuteTestServer(t)
	// Act：以 admin 身份 POST /api/action，action=ignore
	req := adminReq(s, admin, "POST", "/api/action", `{"code":"600000.SH","action":"ignore","strategy":"龙头首板"}`)
	rr := adminDo(s, req)
	// Assert 1：HTTP 200（ignore 是合法白名单动作，不应报错）
	if rr.Code != 200 {
		t.Fatalf("ignore 应 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	var res map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &res); err != nil {
		t.Fatalf("响应非 JSON: %v", err)
	}
	// Assert 2：status=ignored（前端按钮据此回显"已忽略"）
	if res["status"] != "ignored" {
		t.Fatalf("status 应为 ignored, got %v", res)
	}
	// Assert 3：柜台零触达——ignore 走墓碑失效而非下单通道，exec.calls 必须 == 0（P0 资损回归点）
	if exec.calls != 0 {
		t.Fatalf("ignore 绝不允许触达柜台, calls=%d", exec.calls)
	}
}

// TestActionWhitelistUnknownRejected 非 买入/卖出/忽略 的任何 action → 400（旧实现默认归买入）。
func TestActionWhitelistUnknownRejected(t *testing.T) {
	// Arrange：装配真实链路，准备一批白名单外的 action 样本——大小写变体/空串/语义近义词都算：
	// 覆盖"大小写不敏感误匹配""空 action 默认归买入"两类历史漏洞形态。
	s, admin, exec := newExecuteTestServer(t)
	// Act + Assert：逐个提交，全部期待 400 且柜台零触达（旧实现把未知 action 默认归买入=真买）。
	// 用例故意附 price=10/qty=100，验证"即使带齐下单参数，白名单外仍拒绝"。
	for _, a := range []string{"foobar", "BUY_AND_HOLD", "", "watch", "close"} {
		req := adminReq(s, admin, "POST", "/api/action", `{"code":"600000.SH","action":"`+a+`","price":10,"qty":100}`)
		rr := adminDo(s, req)
		if rr.Code != 400 {
			t.Fatalf("action=%q 应 400, got %d body=%s", a, rr.Code, rr.Body.String())
		}
	}
	if exec.calls != 0 {
		t.Fatalf("白名单外动作不得触达柜台, calls=%d", exec.calls)
	}
}

// TestActionBuyLiveRouted 白名单内 buy 在 enabled+manual+admin 下正常进下单通道。
func TestActionBuyLiveRouted(t *testing.T) {
	// Arrange：与 ignore 用例同款全开链路；附 signal_id 以走真实幂等键路径（buy:code:strategy:交易日）。
	s, admin, exec := newExecuteTestServer(t)
	// Act：POST 合法 buy 请求（price=10 qty=100 现价附近，不触发偏离闸）
	req := adminReq(s, admin, "POST", "/api/action", `{"code":"600000.SH","action":"buy","price":10,"qty":100,"signal_id":"f1-buy-test"}`)
	rr := adminDo(s, req)
	// Assert 1：HTTP 200 且响应含 ok 字段（柜台受理回执透传给前端）
	if rr.Code != 200 {
		t.Fatalf("buy 应 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"ok"`) {
		t.Fatalf("buy 响应应含 ok 字段: %s", rr.Body.String())
	}
	// Assert 2：柜台恰好被触达一次——证明 buy 走的是真实下单通道（与 ignore 的 calls==0 互为镜像）
	if exec.calls != 1 {
		t.Fatalf("buy 应触达柜台一次, calls=%d", exec.calls)
	}
}

// TestActionStubNoopNotFakeOK 链路未启用（无 QMT 控制器）时 buy 必须回 status=noop+原因，
// 不再伪装 {"status":"ok"} 成功（旧 F-3 假反馈）。
func TestActionStubNoopNotFakeOK(t *testing.T) {
	// Arrange：不装配引擎/QMT 控制器的最简 admin 服务——模拟"链路未启用"（旧 F-3 假反馈环境）。
	s, admin := newAdminTestServer(t)
	// Act：在未启用环境提交合法 buy（白名单内），看端点的降级路径
	req := adminReq(s, admin, "POST", "/api/action", `{"code":"600000.SH","action":"buy","price":10,"qty":100}`)
	rr := adminDo(s, req)
	// Assert 1：HTTP 200（端点本身可达），但 status=noop+reason 而非伪装 {"status":"ok"} 成功
	if rr.Code != 200 {
		t.Fatalf("noop 应 200, got %d", rr.Code)
	}
	var res map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &res); err != nil {
		t.Fatalf("响应非 JSON: %v", err)
	}
	if res["status"] != "noop" || res["reason"] == "" {
		t.Fatalf("未启用时应 status=noop+reason, got %v", res)
	}
	// ignore 在无引擎/无控制器环境也要 200（无信号可墓碑时 removed=0，不报错）
	// Act 2：同环境提交 ignore——空环境下无固化信号可墓碑，回归点是"不报错不 500"。
	// Assert 2：200 + ignored 文案（removed=0 透传而非失败）
	req = adminReq(s, admin, "POST", "/api/action", `{"code":"600000.SH","action":"ignore"}`)
	if rr := adminDo(s, req); rr.Code != 200 || !strings.Contains(rr.Body.String(), "ignored") {
		t.Fatalf("ignore 空环境应 200 ignored, got %d body=%s", rr.Code, rr.Body.String())
	}
}
