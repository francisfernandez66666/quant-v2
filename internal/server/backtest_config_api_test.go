// backtest_config_api_test.go — §回测自动增强 A0 API 测试：GET 无记录/有记录、
// PUT 越界 400、合法保存后读回、注入函数四路回退。
// English: settings endpoint tests + payload-injection gating on the server side.
package server

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/store"
)

// newBtCfgServer 构造带研究库与配置管理器的最小 server。
func newBtCfgServer(t *testing.T) (*Server, *store.DB) {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "trading.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &Server{researchDB: db, cfg: config.NewManager("")}, db
}

// TestBacktestConfigEndpoints GET 空态 → PUT 合法 → GET 读回；PUT 非法 400。
func TestBacktestConfigEndpoints(t *testing.T) {
	s, db := newBtCfgServer(t)

	// GET 无记录
	rr := httptest.NewRecorder()
	s.handleBacktestConfigGet(rr, httptest.NewRequest("GET", "/api/research/backtest-config", nil))
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"enabled":false`) {
		t.Fatalf("空态 GET: %d %s", rr.Code, rr.Body.String())
	}

	// PUT 非法（base_bps 越界）→ 400 且不落库
	rr = httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/research/backtest-config",
		strings.NewReader(`{"enabled":true,"slippage":{"base_bps":99}}`))
	s.handleBacktestConfigPut(rr, req)
	if rr.Code != 400 {
		t.Fatalf("非法配置应 400, got %d: %s", rr.Code, rr.Body.String())
	}
	if _, ok, _ := db.GetBacktestSettings(); ok {
		t.Fatal("非法保存不应落库")
	}

	// PUT 合法 → 200；GET 读回 enabled=true
	rr = httptest.NewRecorder()
	req = httptest.NewRequest("PUT", "/api/research/backtest-config",
		strings.NewReader(`{"enabled":true,"slippage":{"base_bps":3,"auto_calibrate":true,"calib_min_sample":30}}`))
	s.handleBacktestConfigPut(rr, req)
	if rr.Code != 200 {
		t.Fatalf("合法保存失败: %d %s", rr.Code, rr.Body.String())
	}
	rr = httptest.NewRecorder()
	s.handleBacktestConfigGet(rr, httptest.NewRequest("GET", "/api/research/backtest-config", nil))
	var out struct {
		Config  map[string]any `json:"config"`
		Enabled bool           `json:"enabled"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.Enabled || out.Config["enabled"] != true {
		t.Fatalf("GET 读回异常: %s", rr.Body.String())
	}
}

// TestInjectBacktestPayloadServerSide server 注入函数：无记录不注入、enabled=false 不注入、
// enabled 注入完整结构体（名义额缺省走引擎兜底，s.cfg 出厂默认 fixed_amount=10000）。
func TestInjectBacktestPayloadServerSide(t *testing.T) {
	s, db := newBtCfgServer(t)
	p := map[string]any{"kind": "optimize"}

	// 1) 无记录
	s.injectBacktestPayload(p)
	if _, ok := p["backtest"]; ok {
		t.Fatal("无记录不应注入")
	}
	// 2) enabled=false
	_ = db.SetBacktestSettings(`{"enabled":false}`)
	s.injectBacktestPayload(p)
	if _, ok := p["backtest"]; ok {
		t.Fatal("停用不应注入")
	}
	// 3) 坏 JSON（防御：不 panic、不注入）
	_ = db.SetBacktestSettings(`{broken`)
	s.injectBacktestPayload(p)
	if _, ok := p["backtest"]; ok {
		t.Fatal("坏 JSON 不应注入")
	}
	// 4) enabled → 注入且名义额解析为出厂 fixed_amount=10000
	_ = db.SetBacktestSettings(`{"enabled":true}`)
	s.injectBacktestPayload(p)
	bt, ok := p["backtest"].(config.BacktestConfig)
	if !ok || !bt.Enabled {
		t.Fatalf("enabled 应注入: %T", p["backtest"])
	}
	if bt.OrderValueYuan != 10000 {
		t.Fatalf("名义额未从配置解析: %f", bt.OrderValueYuan)
	}
	// 显式 order_value_yuan 优先于配置解析
	_ = db.SetBacktestSettings(`{"enabled":true,"order_value_yuan":50000}`)
	p2 := map[string]any{}
	s.injectBacktestPayload(p2)
	if bt2 := p2["backtest"].(config.BacktestConfig); bt2.OrderValueYuan != 50000 {
		t.Fatalf("显式名义额应优先: %f", bt2.OrderValueYuan)
	}
}
