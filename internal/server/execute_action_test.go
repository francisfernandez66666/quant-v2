// §P1-7（2026-09-15）POST /api/positions/execute 端点回归测试：
// 覆盖本轮补上的三条链路——
//  1. 限价偏离闸：委托价偏离实时价 ±15% 且未显式 confirm_deviation → 400 拒单；
//  2. client_id 幂等：同一次确认（同一 client_id）重复提交只落一笔委托；
//  3. 非法 client_id 回退旧时间戳键（不因脏输入 panic/拒单）。
//
// 此前该端点零单测（仅 e2e 探针覆盖"qmt 未启用时拒绝"），本轮新增的幂等/偏离闸无回归保护。
// English: regression tests for the manual execute endpoint (P1-7): the ±15% limit-price
// deviation gate, client_id idempotency (double-click / retry dedupe), and dirty client_id fallback.
package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"

	"quant-trading-v2/internal/auth"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/store"
	"quant-trading-v2/internal/trading"
)

// sinaStubTransport 测试桩：把任意 hq.sinajs.cn 请求替换为固定新浪行情应答（600000.SH 现价 10.00 昨收 9.90），
// 其余请求一律报错——deviation 闸依赖实时行情，该桩让 GetRealtimeQuote 走新浪主源拿到确定报价。
type sinaStubTransport struct{}

// 桩 transport：Sina 域名直供固定行情，隔离外网。
func (sinaStubTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !strings.Contains(req.URL.Host, "sinajs.cn") {
		return nil, fmt.Errorf("stub: unexpected host %s", req.URL.Host)
	}
	// 新浪行情 CSV 字段索引：0名称 1今开 2昨收 3现价 4最高 5最低 …（与 getSinaQuotes 解析一致）
	csv := "浦发银行,10.00,9.90,10.00,10.10,9.85,9.99,10.00,100000,1000000"
	utf := []byte(`var hq_str_sh600000="` + csv + `";`)
	enc, _ := simplifiedchinese.GBK.NewEncoder().Bytes(utf) // GBK 失败时 enc 为空，解码端容错跳过
	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(bytes.NewReader(enc)),
		Header:     http.Header{"Content-Type": []string{"text/html; charset=GBK"}},
	}, nil
}

// execCountStub 下单执行器测试桩：永远受理并自增计数（断言实际触达柜台的下单次数）。
type execCountStub struct{ calls int }

// 买/卖两条路径共用「自增计数 + 直接受理」的实现，用例只看 calls 是否变化，
// 以此判定 /api/action 有没有真的把单子推到柜台。
func (e *execCountStub) PlaceBuy(req trading.OrderRequest) (*trading.OrderResult, error) {
	e.calls++
	return &trading.OrderResult{OK: true, OrderID: fmt.Sprintf("GW-%d", e.calls)}, nil
}

// 桩：记录卖出调用次数。
func (e *execCountStub) PlaceSell(req trading.OrderRequest) (*trading.OrderResult, error) {
	e.calls++
	return &trading.OrderResult{OK: true, OrderID: fmt.Sprintf("GW-%d", e.calls)}, nil
}

// 桩：撤单恒成功。
func (e *execCountStub) Cancel(orderID string) error { return nil }

// 桩：返回已连接的网关状态。
func (e *execCountStub) State() (*trading.GatewayState, error) {
	return &trading.GatewayState{Connected: true}, nil
}

// 桩：健康检查恒通过。
func (e *execCountStub) Health() (bool, error) { return true, nil }

// fakeCtrl 只覆盖 QMTController（handleExecuteAction 唯一用到的控制面能力）；
// 内嵌接口占位其余方法（handler 不会触碰，触碰即显式 panic 暴露契约漂移）。
type fakeCtrl struct {
	EngineController
	qmt *trading.Controller
}

// 桩：向调用方暴露注入的 QMT 控制器。
func (f fakeCtrl) QMTController() *trading.Controller { return f.qmt }

// IgnoreSignal §F-1：/api/action 的 ignore 分支会触达该桩——返回 0（无可墓碑信号）；
// 断言重点是"绝不触达 QMT 柜台"（exec.calls 保持 0），墓碑真实行为由 engine 层测试覆盖。
func (f fakeCtrl) IgnoreSignal(string, string) int { return 0 }

// newExecuteTestServer 装配带实盘控制器的测试服务：真实 trading.Controller + 计数执行器 + 新浪行情桩。
func newExecuteTestServer(t *testing.T) (*Server, *auth.User, *execCountStub) {
	t.Helper()
	s, admin := newAdminTestServer(t)
	db, err := store.Open(filepath.Join(t.TempDir(), "live.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	exec := &execCountStub{}
	cfg := config.DefaultQMTConfig()
	cfg.Enabled = true
	ctrl := trading.NewController(exec, db, admin.Username, cfg, nil)
	s.SetEngineController(fakeCtrl{qmt: ctrl})
	s.market = data.NewMarketAPI()
	s.market.SetTransport(sinaStubTransport{})
	return s, admin, exec
}

// TestExecuteDeviationGate 委托价偏离现价 ±15% 未确认 → 400；显式 confirm_deviation → 放行。
func TestExecuteDeviationGate(t *testing.T) {
	// Arrange：全开链路 + 新浪行情桩（现价 10.00，±15% 偏离闸以此为基准）
	s, admin, exec := newExecuteTestServer(t)
	// Act：现价 10.00，委托价 12 → 偏离 +20% 超 ±15%，未确认应拒
	// 为什么这样构造：+20% 恰好越过闸门并远离现价，能固定复现"委托价偏离现价"路径。
	req := adminReq(s, admin, "POST", "/api/positions/execute",
		`{"code":"600000.SH","side":"买入","qty":100,"price":12,"client_id":"dev-test-1"}`)
	if rr := adminDo(s, req); rr.Code != 400 || !strings.Contains(rr.Body.String(), "偏离") {
		t.Fatalf("偏离未确认应 400+偏离文案, got %d body=%s", rr.Code, rr.Body.String())
	}
	// Assert：拒单绝不能触达柜台（曾出现闸门放行后才拒、造成幽灵委托的缺陷形态）
	if exec.calls != 0 {
		t.Fatalf("拒单不应触达柜台, calls=%d", exec.calls)
	}
	// Act：显式 confirm_deviation=true → 用户已知偏离，闸门放行
	req = adminReq(s, admin, "POST", "/api/positions/execute",
		`{"code":"600000.SH","side":"买入","qty":100,"price":12,"client_id":"dev-test-1","confirm_deviation":true}`)
	if rr := adminDo(s, req); rr.Code != 200 {
		t.Fatalf("确认偏离后应放行, got %d body=%s", rr.Code, rr.Body.String())
	}
}

// TestExecuteClientIDIdempotent 同一 client_id 重复提交（模拟双击/网络重试）只下一笔委托：
// 第二次命中 signal_id 唯一键返回 duplicate（OK:false，柜台词绝不再触）——前端会收到明确提示
// 而非第二笔真实委托，符合 §P1-7"一次确认最多一笔单"的幂等目标。
func TestExecuteClientIDIdempotent(t *testing.T) {
	// Arrange：全开链路；委托价 10 与桩现价 10.00 一致（只走幂等路径，不触发偏离闸）
	s, admin, exec := newExecuteTestServer(t)
	// Act ①：首次提交，构造同一请求体供两次复用（同 client_id=idem-abc）
	body := `{"code":"600000.SH","side":"买入","qty":100,"price":10,"client_id":"idem-abc"}`
	req := adminReq(s, admin, "POST", "/api/positions/execute", body)
	rr := adminDo(s, req)
	// Assert ①：首次 200 + ok=true + order_id 回执
	if rr.Code != 200 {
		t.Fatalf("首次提交应 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	var res map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &res); err != nil {
		t.Fatalf("响应非 JSON: %v", err)
	}
	if res["ok"] != true || res["order_id"] == "" {
		t.Fatalf("首次应受理成功: %v", res)
	}
	// 第二次同键提交：命中幂等键，不再触达柜台，返回 duplicate 提示
	// Act ②：完全相同的请求再提交一次（模拟前端双击/网络层重试）
	req = adminReq(s, admin, "POST", "/api/positions/execute", body)
	rr = adminDo(s, req)
	// Assert ②：HTTP 仍是 200（幂等命中不算错误），但 ok=false + err 含 duplicate，
	// 且 exec.calls 保持 1——一次确认最多产生一笔真实委托（P1-7 契约）。
	if rr.Code != 200 {
		t.Fatalf("重复提交应 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &res)
	if res["ok"] != false || !strings.Contains(fmt.Sprint(res["err"]), "duplicate") {
		t.Fatalf("重复提交应返回 duplicate: %v", res)
	}
	if exec.calls != 1 {
		t.Fatalf("柜台应只被调一次, calls=%d", exec.calls)
	}
}

// TestExecuteDirtyClientIDFallsBack 非法 client_id（超长/特殊字符）不拒单、不 panic，回退旧键。
func TestExecuteDirtyClientIDFallsBack(t *testing.T) {
	// Arrange：全开链路；样本覆盖"超长（80 字符，超 immortal 键长度约束）"与"含空格/特殊字符"两类脏输入
	s, admin, _ := newExecuteTestServer(t)
	// Act + Assert：非法 client_id 不拒单、不 panic——handler 回退到旧的"时间戳幂等键"路径放行 200。
	// 回退而非拒绝的理由：幂等键是防重的增强手段，脏输入不应让用户的真实下单意图丢失。
	for _, bad := range []string{strings.Repeat("x", 80), "bad id!@#"} {
		req := adminReq(s, admin, "POST", "/api/positions/execute",
			fmt.Sprintf(`{"code":"600000.SH","side":"买入","qty":100,"price":10,"client_id":%q}`, bad))
		if rr := adminDo(s, req); rr.Code != 200 {
			t.Fatalf("脏 client_id %q 应回退旧键放行, got %d body=%s", bad, rr.Code, rr.Body.String())
		}
	}
}
