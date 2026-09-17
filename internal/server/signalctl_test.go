// signalctl_test.go — §SIGNAL_CONTROLLER HTTP 端点测试：模拟盘战法白名单读写 + 裁定留痕查询。
// English: endpoint tests for the paper strategy whitelist (read/save) and the verdict audit feed.
package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"quant-trading-v2/internal/config"
)

// TestPaperStrategiesEndpoints GET 返回 known_strategies 全集（含动量，§SIGNAL_CONTROLLER 后
// 动量有显式开关位）；POST 白名单+黑名单落库后 GET 回读一致；未知战法拒绝。
// 裁定端点无引擎时返回空列表（200 形状稳定，前端零判空）。
func TestPaperStrategiesEndpoints(t *testing.T) {
	s := &Server{cfg: config.NewManager("")}

	// GET 初始：空白名单（默认全集语义）+ shadow 默认开
	rr := httptest.NewRecorder()
	s.handleGetPaperStrategies(rr, httptest.NewRequest(http.MethodGet, "/api/paper/strategies", nil))
	if rr.Code != 200 {
		t.Fatalf("GET strategies: %d", rr.Code)
	}
	var view struct {
		Strategies      []string `json:"strategies"`
		Blacklist       []string `json:"blacklist"`
		ShadowBlacklist bool     `json:"shadow_blacklist"`
		Known           []struct {
			ID string `json:"id"`
		} `json:"known_strategies"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	hasMom := false
	for _, k := range view.Known {
		if k.ID == "momentum" {
			hasMom = true
		}
	}
	if !hasMom {
		t.Fatal("known_strategies 必须含 momentum（动量自此有开关）")
	}
	if len(view.Strategies) != 0 || !view.ShadowBlacklist {
		t.Fatalf("初始态应为空白名单+影子开: %v shadow=%v", view.Strategies, view.ShadowBlacklist)
	}

	// POST 保存：动量显式开启 + 黑名单
	body := `{"strategies":["dragon","momentum"],"blacklist":["600519"]}`
	rr = httptest.NewRecorder()
	s.handleSetPaperStrategies(rr, httptest.NewRequest(http.MethodPost, "/api/paper/strategies", bytes.NewBufferString(body)))
	if rr.Code != 200 {
		t.Fatalf("POST strategies: %d %s", rr.Code, rr.Body.String())
	}

	// GET 回读一致
	rr = httptest.NewRecorder()
	s.handleGetPaperStrategies(rr, httptest.NewRequest(http.MethodGet, "/api/paper/strategies", nil))
	if err := json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if len(view.Strategies) != 2 || view.Strategies[0] != "dragon" || view.Strategies[1] != "momentum" {
		t.Fatalf("白名单回读异常: %v", view.Strategies)
	}
	if len(view.Blacklist) != 1 {
		t.Fatalf("黑名单回读异常")
	}

	// 未知战法 ID 拒绝（400）
	rr = httptest.NewRecorder()
	s.handleSetPaperStrategies(rr, httptest.NewRequest(http.MethodPost, "/api/paper/strategies", bytes.NewBufferString(`{"strategies":["不存在的战法"]}`)))
	if rr.Code != 400 {
		t.Fatalf("未知战法应 400, got %d", rr.Code)
	}

	// 裁定端点：无注册表返回空数组形状（200）
	rr = httptest.NewRecorder()
	s.handleSignalVerdicts(rr, httptest.NewRequest(http.MethodGet, "/api/signalctl/verdicts", nil))
	if rr.Code != 200 {
		t.Fatalf("verdicts: %d", rr.Code)
	}
	var v struct {
		Verdicts []json.RawMessage `json:"verdicts"`
		Count    int               `json:"count"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &v); err != nil || v.Verdicts == nil {
		t.Fatalf("verdicts 形状异常: %s", rr.Body.String())
	}
}
