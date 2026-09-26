// w1c_no_quote_confirm_test.go — §0926E2E-W1C（2026-09-26 全量审计批）手动单"现价不可得"
// 显式确认制行为用例。缺陷原文：handleExecuteAction 里行情拉取失败（q==nil）直接跳过
// ±15% 偏离校验放行——真钱路径上"行情故障=手滑错价可成交"，与闸1（盈亏吞 0）同族。
// 本文件锁三条语义：①行情链在场但取不到价且未确认 → 400 且不触柜台；②带 confirm_no_quote
// → 受理并落审计；③取到价时确认位形同虚设（偏离闸照常把关，防"确认绕过一切"）。
package server

import (
	"errors"
	"strings"
	"testing"

	"quant-trading-v2/internal/data"
)

// TestW1cQuoteUnavailableRequiresConfirm 取价报错时：未确认必 400（含固定前缀文案，前端据此
// 弹二次确认），显式 confirm_no_quote 后受理且真实触达柜台一次。
func TestW1cQuoteUnavailableRequiresConfirm(t *testing.T) {
	s, admin, exec := newExecuteTestServer(t)
	// 覆盖新浪桩：注入"行情链在场但取价失败"形态（生产同形态=四级 failover 全灭）
	s.quoteForOrderFn = func(string) (*data.StockInfo, error) {
		return nil, errors.New("全部行情源失败（w1c 注入）")
	}
	body := `{"code":"600000.SH","side":"买入","qty":100,"price":999.00,"client_id":"w1c-1"}`
	// Act①：价 999 对现价而言离谱到天际，但真正的防线现在只剩 confirm——未确认必须拒
	rr := adminDo(s, adminReq(s, admin, "POST", "/api/positions/execute", body))
	if rr.Code != 400 || !strings.Contains(rr.Body.String(), "无法获取实时现价") {
		t.Fatalf("现价不可得未确认应 400+固定前缀, got %d body=%s", rr.Code, rr.Body.String())
	}
	if exec.calls != 0 {
		t.Fatalf("被拒的单绝不能触达柜台, calls=%d", exec.calls)
	}
	// Act②：显式确认（模拟操作员在弹窗点"仍要下单"）→ 受理（行情故障时保留人工应急通道）
	rr = adminDo(s, adminReq(s, admin, "POST", "/api/positions/execute",
		`{"code":"600000.SH","side":"买入","qty":100,"price":999.00,"client_id":"w1c-1","confirm_no_quote":true}`))
	if rr.Code != 200 {
		t.Fatalf("显式确认后应受理, got %d body=%s", rr.Code, rr.Body.String())
	}
	if exec.calls != 1 {
		t.Fatalf("确认后应恰好触达柜台一次, calls=%d", exec.calls)
	}
}

// TestW1cConfirmDoesNotBypassDeviationGate 假绿反证的反向：带 confirm_no_quote 但**取到了价**，
// 偏离闸必须照常把关——确认位只豁免"现价不可得"，绝不豁免"现价可得且偏离超限"。
func TestW1cConfirmDoesNotBypassDeviationGate(t *testing.T) {
	s, admin, exec := newExecuteTestServer(t)
	s.quoteForOrderFn = func(string) (*data.StockInfo, error) {
		return &data.StockInfo{Price: 10.00}, nil // 现价 10.00
	}
	// 委托价 12 → +20% 超 ±15%，只带了 confirm_no_quote（未带 confirm_deviation）→ 必拒
	rr := adminDo(s, adminReq(s, admin, "POST", "/api/positions/execute",
		`{"code":"600000.SH","side":"买入","qty":100,"price":12,"client_id":"w1c-2","confirm_no_quote":true}`))
	if rr.Code != 400 || !strings.Contains(rr.Body.String(), "偏离") {
		t.Fatalf("取到价时 confirm_no_quote 不得豁免偏离闸, got %d body=%s", rr.Code, rr.Body.String())
	}
	if exec.calls != 0 {
		t.Fatalf("偏离拒单不得触柜台, calls=%d", exec.calls)
	}
}

// TestW1cHaltedKeepsKillSwitchFirstReason halted 短路：kill-switch 置位且现价不可得时，
// 拒单理由必须是权威的 "kill-switch engaged"，确认闸不得抢位掩盖紧急停止文案。
func TestW1cHaltedKeepsKillSwitchFirstReason(t *testing.T) {
	s, admin, _ := newExecuteTestServer(t)
	s.quoteForOrderFn = func(string) (*data.StockInfo, error) { return nil, errors.New("行情全灭（w1c 注入）") }
	// 置 halted（与生产 /api/qmt/halt 同字段；此处直接改控制器持有的配置快照）
	cfg := s.qmtCtrlFor(admin.ID).Config()
	cfg.Halted = true
	s.qmtCtrlFor(admin.ID).UpdateConfig(cfg)
	rr := adminDo(s, adminReq(s, admin, "POST", "/api/positions/execute",
		`{"code":"600000.SH","side":"买入","qty":100,"price":10,"client_id":"w1c-4"}`))
	body := rr.Body.String()
	if rr.Code < 400 || !strings.Contains(body, "kill-switch") {
		t.Fatalf("halted+无现价必须仍以 kill-switch 为首要拒单理由, got %d body=%s", rr.Code, body)
	}
}

// TestW1cNoMarketNoSeamKeepsOldShape 部署形态兜底：行情链整体缺席（s.market==nil 且无测试缝，
// 如纯纸面/单机部署）时不做确认制——与旧行为一致，避免把"没装配行情"误判成"行情故障"。
func TestW1cNoMarketNoSeamKeepsOldShape(t *testing.T) {
	s, admin, _ := newExecuteTestServer(t)
	s.market = nil
	s.quoteForOrderFn = nil
	rr := adminDo(s, adminReq(s, admin, "POST", "/api/positions/execute",
		`{"code":"600000.SH","side":"买入","qty":100,"price":999.00,"client_id":"w1c-3"}`))
	if rr.Code != 200 {
		t.Fatalf("无行情链部署应维持旧形态放行, got %d body=%s", rr.Code, rr.Body.String())
	}
}
