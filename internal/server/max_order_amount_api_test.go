// max_order_amount_api_test.go §AUDIT-PM 2026-09-15：单笔金额绝对帽在配置 API 面的契约——
// POST /api/config/qmt 往返读回、非法值 400（负数/超上限）、落库持久，以及最关键的一条：
// **保存即生效**（携带 max_order_amount 的请求同步刷 controller 的 cfg，不等休市开关队列；
// 与 kill-switch 同口径，收盘设置的上限次日开盘前不能形同虚设）。
// English: config-API contract for the absolute per-order cap: round-trip, 400 on invalid values,
// persistence, and the core guarantee — saving applies to the live controller immediately
// (same fail-stop semantics as the kill-switch), not via the closed-hours switch queue.
package server

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/store"
	"quant-trading-v2/internal/trading"
)

// capFakeEngine 只覆写 QMTController 的假引擎控制面（其余方法嵌入 nil 接口，测试不触达）。
// 存在意义：Server.ctrl 是 EngineController 大接口（30+ 方法），手写完整假实现无价值。
type capFakeEngine struct {
	EngineController
	qmt *trading.Controller
}

func (f *capFakeEngine) QMTController() *trading.Controller { return f.qmt }

func TestMaxOrderAmountConfigAPI(t *testing.T) {
	s, admin := newAdminTestServer(t)

	db, err := store.Open(filepath.Join(t.TempDir(), "cap.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	exec := trading.NewQMTClient("http://127.0.0.1:1", "", time.Second, 0) // 测试不下单，仅占位
	ctrl := trading.NewController(exec, db, admin.ID, config.DefaultQMTConfig(), nil)
	s.ctrl = &capFakeEngine{qmt: ctrl}

	// 1) 非法值拒绝：负数与超上限（0-1e9 之外）
	for _, body := range []string{`{"max_order_amount":-1}`, `{"max_order_amount":2000000000}`} {
		rr := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/config/qmt", body))
		if rr.Code != 400 {
			t.Fatalf("body=%s 期望 400, got %d body=%s", body, rr.Code, rr.Body.String())
		}
	}
	// 2) 合法值保存 → 200
	if rr := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/config/qmt", `{"max_order_amount":150000}`)); rr.Code != 200 {
		t.Fatalf("合法帽值期望 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	// 3) GET 视图读回 150000（前端回填依赖）
	rr := adminDo(s, adminReq(s, admin, http.MethodGet, "/api/config/qmt", ""))
	var view map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
		t.Fatalf("GET decode: %v", err)
	}
	if v, _ := view["max_order_amount"].(float64); v != 150000 {
		t.Fatalf("GET 视图期望 max_order_amount=150000, got %v", view["max_order_amount"])
	}
	// 4) 落库持久（Manager 读回，同值断言的是持久层而非内存缓存巧合）
	if got := s.cfg.GetQMTConfigFor(admin.ID).RiskGate.MaxOrderAmount; got != 150000 {
		t.Fatalf("期望持久化 150000, got %v", got)
	}
	// 5) 保存即生效：controller 内存 cfg 同步刷新（不经开关队列、不等休市）
	if got := ctrl.Config().RiskGate.MaxOrderAmount; got != 150000 {
		t.Fatalf("UpdateConfig 应即时生效：期望 ctrl 读到 150000, got %v（若滞留队列此值为 0）", got)
	}
	// 6) 帽=0 显式关闭同样往返生效
	if rr := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/config/qmt", `{"max_order_amount":0}`)); rr.Code != 200 {
		t.Fatalf("关闭帽期望 200, got %d", rr.Code)
	}
	if got := ctrl.Config().RiskGate.MaxOrderAmount; got != 0 {
		t.Fatalf("显式关闭应即时生效, got %v", got)
	}
}
