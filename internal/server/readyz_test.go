// 文件概述（internal/server/readyz_test.go）
// §F3-20261114：GET /readyz 就绪探针的分支契约测试（此前全仓没有任何 /readyz 用例，
// 公网又被 Caddy SPA 兜底成恒 200 的假探针——本文件三例锁的是 handler 自身的语义）：
//
//	B1 db 坏        → 503 且 body.db=false（依赖不可用必须诚实报，不许冒充 200）
//	B2 gateway 坏   → 503 且 body.gateway=false（未接入实盘/探测失败 ≠ ok）
//	B3 全好         → 200 且 body.ok=true（就绪态正确放行）
//
// 红线：响应只含 ok/build/db/market/gateway 五个键，不出网关地址/token/业务数据。
// English: §F3-20261114 — contract tests for the /readyz readiness handler: unhealthy
// dependencies must answer 503 with the offending boolean false; fully-ready answers 200.
package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/store"
	"quant-trading-v2/internal/trading"
)

// readyzStubRegistry 最小 EngineRegistry/EngineController 桩：只为把一枚可控的
// trading.Controller 塞进 qmtCtrlFor 路径（两个内嵌 nil 接口顶住其余方法，不会被拨到）。
type readyzStubRegistry struct {
	EngineRegistry
	EngineController
	ctrl *trading.Controller
}

func (r *readyzStubRegistry) GetController(string) EngineController { return r }

func (r *readyzStubRegistry) QMTController() *trading.Controller { return r.ctrl }

// readyzParse 统一断言：状态码 + JSON 解码 + 键集合恒为 5（§185 A3 契约）。
func readyzParse(t *testing.T, rr *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var out map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应非合法 JSON: %v（body=%s）", err, rr.Body.String())
	}
	keys := make([]string, 0, len(out))
	for k := range out {
		keys = append(keys, k)
	}
	if len(out) != 5 {
		t.Fatalf("readyz 键集合应恒为 5 个（ok/build/db/market/gateway），实际 %v", keys)
	}
	return out
}

// TestReadyz_DBFalse §185 B1：库不可用 → 503 + db:false。
func TestReadyz_DBFalse(t *testing.T) {
	db, _ := store.Open(filepath.Join(t.TempDir(), "t.db"))
	db.Close() // 关闭后 Ping 必失败——构造「db 坏」的最小形态
	s := &Server{researchDB: db, dc: &data.DataCoordinator{}}
	rr := httptest.NewRecorder()
	s.handleReadyz(rr, httptest.NewRequest("GET", "/readyz", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("db 坏应回 503，实际 %d", rr.Code)
	}
	out := readyzParse(t, rr)
	if v, _ := out["db"].(bool); v {
		t.Fatalf("db 坏时 body.db 应为 false")
	}
	if v, _ := out["ok"].(bool); v {
		t.Fatalf("db 坏时 body.ok 应为 false")
	}
}

// TestReadyz_GatewayFalse §185 B2：未接入实盘（无控制器）→ 503 + gateway:false。
// 这正是生产运营账号 qmt.enabled=false 时的常态读数——「诚实 503」是设计态不是故障。
func TestReadyz_GatewayFalse(t *testing.T) {
	db, _ := store.Open(filepath.Join(t.TempDir(), "t.db"))
	defer db.Close()
	s := &Server{researchDB: db, dc: &data.DataCoordinator{}}
	rr := httptest.NewRecorder()
	s.handleReadyz(rr, httptest.NewRequest("GET", "/readyz", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("gateway 缺席应回 503，实际 %d", rr.Code)
	}
	out := readyzParse(t, rr)
	if v, _ := out["gateway"].(bool); v {
		t.Fatalf("gateway 缺席时 body.gateway 应为 false")
	}
	if v, _ := out["db"].(bool); !v {
		t.Fatalf("db 健康时 body.db 应为 true（本例只红 gateway 一项）")
	}
}

// TestReadyz_OK §185 B3：db/market/gateway 全好 → 200 + ok:true。
// gateway 好的构造：控制器快照 LastProbeOK=false 但带 GatewayURL，handler 会按第二权威
// 直拨 <GatewayURL>/health 的 live_ready——用 httptest 假网关返回 live_ready=true。
func TestReadyz_OK(t *testing.T) {
	db, _ := store.Open(filepath.Join(t.TempDir(), "t.db"))
	defer db.Close()
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"live_ready":true}`))
	}))
	defer gw.Close()
	ctrl := trading.NewController(trading.NoopExecutor{}, nil, "u_readyz_test",
		config.QMTConfig{GatewayURL: gw.URL}, nil)
	s := &Server{
		researchDB: db,
		dc:         &data.DataCoordinator{},
		registry:   &readyzStubRegistry{ctrl: ctrl},
	}
	rr := httptest.NewRecorder()
	s.handleReadyz(rr, httptest.NewRequest("GET", "/readyz", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("全好应回 200，实际 %d（body=%s）", rr.Code, rr.Body.String())
	}
	out := readyzParse(t, rr)
	if v, _ := out["ok"].(bool); !v {
		t.Fatalf("全好时 body.ok 应为 true")
	}
	for _, k := range []string{"db", "market", "gateway"} {
		if v, _ := out[k].(bool); !v {
			t.Fatalf("全好时 body.%s 应为 true", k)
		}
	}
}
