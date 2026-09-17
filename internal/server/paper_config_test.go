// paper_config_test.go — §F-4（20260917 缺陷修复批）模拟盘撮合配置端点回归：
// ①GET 形状稳定；②POST 总开关开→**引擎实时生效**（EngineEnabled=true，不再"必须重启"——
// 旧缺陷：SetPaperConfig 死代码+无端点）；③指针字段局部更新不误伤其他字段；④负值 400。
// English: §F-4 regression — config persists AND hot-applies to the running engine (the old
// restart-only path), pointer fields leave others intact, negative values are rejected.
package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/paper"
)

// newPaperConfigServer 构造最小 paper 配置测试服务：真实配置管理器 + 落盘到临时目录的 paper 引擎。
// 用真实 paper.Engine 而非桩，才能验证"端点热更 → 引擎 Enabled() 即时生效"的联动。
func newPaperConfigServer(t *testing.T) *Server {
	t.Helper()
	s := &Server{cfg: config.NewManager("")}
	// paper.json 放进 t.TempDir()：落盘路径随测试结束自动清理，用例间互不污染
	s.SetPaper(paper.New(paper.ConfigFromRules(s.cfg.Rules.Paper), filepath.Join(t.TempDir(), "paper.json")))
	return s
}

// getPaperConfig 便捷 Act：GET /api/paper/config 并断言 200，反序列化为 paperConfigView 返回。
func getPaperConfig(t *testing.T, s *Server) paperConfigView {
	t.Helper()
	rr := httptest.NewRecorder()
	s.handleGetPaperConfig(rr, httptest.NewRequest(http.MethodGet, "/api/paper/config", nil))
	if rr.Code != 200 {
		t.Fatalf("GET paper config: %d %s", rr.Code, rr.Body.String())
	}
	var v paperConfigView
	if err := json.Unmarshal(rr.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// TestPaperConfigGet 验证 GET 默认形状：未做过任何 POST 时，总开关与引擎侧开关都应回显未启用；
// auto_sell 缺省回显 true（nil=开的语义，防止"没配=自动卖出关闭"的误解读）。
func TestPaperConfigGet(t *testing.T) {
	s := newPaperConfigServer(t)      // Arrange：全新未配置的服务
	v := getPaperConfig(t, s)         // Act
	if v.Enabled || v.EngineEnabled { // Assert：默认双开关均为关
		t.Fatalf("默认应为未启用: %+v", v)
	}
	if v.AutoSell != true {
		t.Fatal("auto_sell 缺省应回显 true（nil=开语义）")
	}
}

// TestPaperConfigHotEnableAndPartialUpdate 验证 POST 配置的三段契约：①总开关开启后运行中的
// paper 引擎必须热生效（旧缺陷：SetPaperConfig 是死代码、改完必须重启）；②指针字段局部更新
// 不得误伤未提交字段；③关方向变更同样热同步。
func TestPaperConfigHotEnableAndPartialUpdate(t *testing.T) {
	s := newPaperConfigServer(t)
	// Act：POST 便捷闭包——每次提交一个 JSON 片段并断言 200
	post := func(body string) {
		t.Helper()
		rr := httptest.NewRecorder()
		s.handleSetPaperConfig(rr, httptest.NewRequest(http.MethodPost, "/api/paper/config", bytes.NewBufferString(body)))
		if rr.Code != 200 {
			t.Fatalf("POST paper config %s: %d %s", body, rr.Code, rr.Body.String())
		}
	}
	// 开总开关 + 单笔资金：引擎必须立即生效（旧缺陷=必须重启）
	// Act/Assert ①：enabled=true + fixed_amount=20000 → GET 回显一致，且 paper 引擎 Enabled() 即时为 true
	//（热生效是本批修复的核心契约——配置不仅要持久化，还必须驱动运行中的引擎）。
	post(`{"enabled":true,"fixed_amount":20000}`)
	v := getPaperConfig(t, s)
	if !v.Enabled || !v.EngineEnabled || v.FixedAmount != 20000 {
		t.Fatalf("开关联动失败: %+v", v)
	}
	if pe := s.paperEngine(); pe == nil || !pe.Enabled() {
		t.Fatal("引擎 Enabled() 应即时为 true")
	}
	// 局部关自动卖出：enabled/fixed_amount 不得被误伤
	// Act/Assert ②：只提交 {"auto_sell":false} 一个字段——指针语义局部更新，
	// 期待 enabled 保持 true、fixed_amount 保持 20000（旧缺陷会整份覆盖清掉其他字段）。
	post(`{"auto_sell":false}`)
	v = getPaperConfig(t, s)
	if v.Enabled != true || v.AutoSell != false || v.EngineAutoSell != false || v.FixedAmount != 20000 {
		t.Fatalf("指针局部更新误伤其他字段: %+v", v)
	}
	// 再关总开关：热同步同样生效
	// Act/Assert ③：关方向的变更也要热生效（Enabled() 双向联动，不只是开）。
	post(`{"enabled":false}`)
	if s.paperEngine().Enabled() {
		t.Fatal("关闭总开关应即时生效")
	}
}

// TestPaperConfigRejectNegative 验证数值参数负值一律 400：负的单笔资金/最大持仓/初始资金/做空资金
// 都会造成资损/切片越界类 bug，端点必须在入口拒绝而非落库生效。
func TestPaperConfigRejectNegative(t *testing.T) {
	// Arrange：从默认规则构造的服务（四个覆盖面各异的数值字段逐个取负提交）
	s := newPaperConfigServer(t)
	// Act + Assert：每个负值请求都应 400，配置不得落库生效
	for _, body := range []string{`{"fixed_amount":-1}`, `{"max_positions":-2}`, `{"initial_capital":-1}`, `{"short_capital":-1}`} {
		rr := httptest.NewRecorder()
		s.handleSetPaperConfig(rr, httptest.NewRequest(http.MethodPost, "/api/paper/config", bytes.NewBufferString(body)))
		if rr.Code != 400 {
			t.Fatalf("负值应 400: %s got %d", body, rr.Code)
		}
	}
}
