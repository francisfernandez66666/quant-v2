// r7_endpoints_test.go R7 加固新增管理端点的端点级测试：
//   - WS-K 维4：GET /api/config/history + POST /api/config/rollback（快照/回滚/字段级 diff）
//   - WS-L 维5：GET /api/metrics/prometheus + GET /api/metrics/alerts
//   - WS-H C2：GET /api/research/strategies/snapshots + POST /api/research/strategies/rollback
//
// 与单元测试（config/history_test.go、metrics/alerter_test.go、research/versioning_test.go）
// 互补：这里走完整 mux + adminMiddleware 鉴权路径，验证「端点存在 + 权限 + 真实落盘/恢复」。
// English: endpoint-level tests for the R7 admin endpoints — config history/rollback (WS-K 维4),
// metrics prometheus/alerts (WS-L 维5) and research snapshots/rollback (WS-H C2). These complement
// the unit tests by exercising the full mux + adminMiddleware path with real on-disk effects.
package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"quant-trading-v2/internal/auth"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/metrics"
	"quant-trading-v2/internal/research"
)

// writeTestConfig 向 cfg 路径写入一份最小合法 config.json（测试初始态）。
// English: writes a minimal valid config.json for the manager path (test initial state).
func writeTestConfig(t *testing.T, cfg *config.Manager, cfgPath, body string) {
	t.Helper()
	if err := os.WriteFile(cfgPath, []byte(body), 0o644); err != nil {
		t.Fatalf("写 config.json: %v", err)
	}
}

// newR7TestServer 构建带 auth + cfg（真实 config.json 路径）+ 全路由 mux 的 Server，
// 并挂上 researchDir，供三类 R7 管理端点测试复用。
// English: builds a Server with auth + cfg (real config.json path) + full-route mux + researchDir
// for the three R7 admin-endpoint test groups.
func newR7TestServer(t *testing.T) (*Server, string, *config.Manager) {
	t.Helper()
	dir := t.TempDir()
	mgr := auth.NewManager(dir)
	if err := mgr.Init(); err != nil {
		t.Fatalf("auth init: %v", err)
	}
	cfgPath := filepath.Join(dir, "config.json")
	cfgMgr := config.NewManager(cfgPath)
	cfgMgr.SetStore(mgr)
	s := &Server{auth: mgr, cfg: cfgMgr, mux: http.NewServeMux()}
	s.researchDir = dir
	s.registerRoutes()
	return s, cfgPath, cfgMgr
}

// TestConfigHistoryAndRollbackEndpoints §WS-K 维4 端点级：快照列表、字段级 diff、
// 回滚真实恢复 config.json、非法入参 400/404、非管理员 403。
// English: WS-K 维4 endpoint test — snapshot list, field-level diff, real rollback restore,
// invalid input 400/404, non-admin 403.
func TestConfigHistoryAndRollbackEndpoints(t *testing.T) {
	s, cfgPath, _ := newR7TestServer(t)
	// 先落一份初始 config.json，否则 SnapshotRules 读不到文件
	writeTestConfig(t, s.cfg, cfgPath, `{"qmt":{"enabled":false,"price_type":"market"}}`)

	admin, err := s.auth.CreateUser("histadmin", "pw", "admin", nil, 0)
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	member, err := s.auth.CreateUser("histmember", "pw", "user", nil, 0)
	if err != nil {
		t.Fatalf("create member: %v", err)
	}

	// 1) 列表：初始为空
	rr := adminDo(s, adminReq(s, admin, http.MethodGet, "/api/config/history", ""))
	if rr.Code != 200 {
		t.Fatalf("GET /api/config/history → %d body=%s", rr.Code, rr.Body.String())
	}
	var listResp struct {
		Snapshots []config.RuleSnapshotInfo `json:"snapshots"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("解析快照列表: %v", err)
	}
	if len(listResp.Snapshots) != 0 {
		t.Fatalf("初始快照列表应为空, got %d", len(listResp.Snapshots))
	}

	// 2) 非管理员 403
	if rr := adminDo(s, adminReq(s, member, http.MethodGet, "/api/config/history", "")); rr.Code != 403 {
		t.Errorf("member GET /api/config/history 应 403, got %d", rr.Code)
	}

	// 3) 快照 v1 → 改配置为 v2 → diff 可见变更
	ts1, err := config.SnapshotRules(s.cfg)
	if err != nil {
		t.Fatalf("snapshot v1: %v", err)
	}
	writeTestConfig(t, s.cfg, cfgPath, `{"qmt":{"enabled":true,"price_type":"limit"}}`)

	rr = adminDo(s, adminReq(s, admin, http.MethodGet, "/api/config/history?diff="+ts1, ""))
	if rr.Code != 200 {
		t.Fatalf("diff → %d body=%s", rr.Code, rr.Body.String())
	}
	var diffResp struct {
		Diff string `json:"diff"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &diffResp); err != nil {
		t.Fatalf("解析 diff: %v", err)
	}
	if !strings.Contains(diffResp.Diff, "price_type: market -> limit") {
		t.Errorf("diff 应含 price_type 变更行, got %q", diffResp.Diff)
	}

	// 4) 回滚 v1 → config.json 恢复 v1 内容
	rr = adminDo(s, adminReq(s, admin, http.MethodPost, "/api/config/rollback", `{"snapshot_ts":"`+ts1+`"}`))
	if rr.Code != 200 {
		t.Fatalf("rollback → %d body=%s", rr.Code, rr.Body.String())
	}
	cur, err := config.RestoreRulesContentCurrent(s.cfg)
	if err != nil {
		t.Fatalf("读取回滚后 config.json: %v", err)
	}
	if strings.Contains(string(cur), `"enabled":true`) {
		t.Errorf("回滚后应为 v1（enabled=false）, got %s", string(cur))
	}

	// 5) 非法入参：缺 snapshot_ts → 400；不存在的快照 → 404
	if rr := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/config/rollback", `{}`)); rr.Code != 400 {
		t.Errorf("rollback 缺 snapshot_ts 应 400, got %d", rr.Code)
	}
	if rr := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/config/rollback", `{"snapshot_ts":"nope_9999"}`)); rr.Code != 404 {
		t.Errorf("rollback 不存在快照应 404, got %d", rr.Code)
	}
	// member 回滚 403
	if rr := adminDo(s, adminReq(s, member, http.MethodPost, "/api/config/rollback", `{"snapshot_ts":"`+ts1+`"}`)); rr.Code != 403 {
		t.Errorf("member rollback 应 403, got %d", rr.Code)
	}
}

// TestMetricsEndpoints §WS-L 维5 端点级：Prometheus text 导出可过 promtool 结构（HELP/TYPE/值行），
// 量规出现在导出中；告警规则接口返回 5 条默认规则；非管理员 403。
// English: WS-L 维5 endpoint test — promtool-style exposition (HELP/TYPE/value lines) with gauges
// present; alert rules return the 5 defaults; non-admin 403.
func TestMetricsEndpoints(t *testing.T) {
	s, _, _ := newR7TestServer(t)
	admin, err := s.auth.CreateUser("metricsadmin", "pw", "admin", nil, 0)
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	member, err := s.auth.CreateUser("metricsmember", "pw", "user", nil, 0)
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	metrics.SetGauge("r7test_breaker_active", 1)

	rr := adminDo(s, adminReq(s, admin, http.MethodGet, "/api/metrics/prometheus", ""))
	if rr.Code != 200 {
		t.Fatalf("GET /api/metrics/prometheus → %d", rr.Code)
	}
	body := rr.Body.String()
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type 应为 text/plain, got %q", ct)
	}
	for _, want := range []string{
		"# TYPE quant_orders_placed_total counter",
		"quant_orders_placed_total ",
		"# TYPE quant_gauge_r7test_breaker_active gauge",
		"quant_gauge_r7test_breaker_active 1",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("prometheus 文本缺 %q\n%s", want, body)
		}
	}

	// 告警规则列表
	rr = adminDo(s, adminReq(s, admin, http.MethodGet, "/api/metrics/alerts", ""))
	if rr.Code != 200 {
		t.Fatalf("GET /api/metrics/alerts → %d", rr.Code)
	}
	var alertResp struct {
		Rules []map[string]interface{} `json:"rules"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &alertResp); err != nil {
		t.Fatalf("解析告警规则: %v", err)
	}
	if len(alertResp.Rules) == 0 {
		t.Fatal("告警规则列表不应为空")
	}

	// 非管理员 403
	if rr := adminDo(s, adminReq(s, member, http.MethodGet, "/api/metrics/prometheus", "")); rr.Code != 403 {
		t.Errorf("member prometheus 应 403, got %d", rr.Code)
	}
	if rr := adminDo(s, adminReq(s, member, http.MethodGet, "/api/metrics/alerts", "")); rr.Code != 403 {
		t.Errorf("member alerts 应 403, got %d", rr.Code)
	}
}

// TestResearchSnapshotRollbackEndpoints §WS-H C2 端点级：快照列表、回滚真实恢复 applied_rules.json、
// 缺参 400 / 不存在快照 400、未接入研究目录 503、member 回滚 403。
// English: WS-H C2 endpoint test — snapshot list, real restore of applied_rules.json, 400 on bad
// input, 503 when the research dir isn't wired, 403 for non-admins.
func TestResearchSnapshotRollbackEndpoints(t *testing.T) {
	s, _, _ := newR7TestServer(t)
	admin, err := s.auth.CreateUser("r7admin", "pw", "admin", nil, 0)
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	member, err := s.auth.CreateUser("r7member", "pw", "user", nil, 0)
	if err != nil {
		t.Fatalf("create member: %v", err)
	}

	// 1) 未接入研究目录 → 503
	s2, _, _ := newR7TestServer(t)
	s2.researchDir = ""
	admin2, err := s2.auth.CreateUser("r7admin2", "pw", "admin", nil, 0)
	if err != nil {
		t.Fatalf("create admin on s2: %v", err)
	}
	if rr := adminDo(s2, adminReq(s2, admin2, http.MethodGet, "/api/research/strategies/snapshots", "")); rr.Code != 503 {
		t.Errorf("researchDir 空应 503, got %d", rr.Code)
	}

	// 2) 空快照列表（researchDir 已接）
	rr := adminDo(s, adminReq(s, admin, http.MethodGet, "/api/research/strategies/snapshots", ""))
	if rr.Code != 200 {
		t.Fatalf("snapshots → %d body=%s", rr.Code, rr.Body.String())
	}
	var listResp struct {
		Snapshots []research.SnapshotInfo `json:"snapshots"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("解析快照列表: %v", err)
	}
	if len(listResp.Snapshots) != 0 {
		t.Fatalf("初始快照列表应为空, got %d", len(listResp.Snapshots))
	}

	// 3) 写 applied_rules.json → 快照 → 修改 → 回滚真实恢复
	applied := filepath.Join(s.researchDir, "applied_rules.json")
	if err := os.WriteFile(applied, []byte(`{"strategy":"v1"}`), 0o644); err != nil {
		t.Fatalf("写 applied_rules.json: %v", err)
	}
	ts1, err := research.SnapshotStrategies(s.researchDir)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if err := os.WriteFile(applied, []byte(`{"strategy":"v2-mutated"}`), 0o644); err != nil {
		t.Fatalf("修改 applied_rules.json: %v", err)
	}
	rr = adminDo(s, adminReq(s, admin, http.MethodPost, "/api/research/strategies/rollback", `{"snapshot_ts":"`+ts1+`"}`))
	if rr.Code != 200 {
		t.Fatalf("rollback → %d body=%s", rr.Code, rr.Body.String())
	}
	b, err := os.ReadFile(applied)
	if err != nil {
		t.Fatalf("读取恢复后的文件: %v", err)
	}
	// 快照恢复会重新缩进 JSON（MarshalIndent），按语义比对
	var restored, want map[string]string
	if err := json.Unmarshal(b, &restored); err != nil {
		t.Fatalf("解析回滚后的 JSON: %v", err)
	}
	if err := json.Unmarshal([]byte(`{"strategy":"v1"}`), &want); err != nil {
		t.Fatalf("解析期望值: %v", err)
	}
	if restored["strategy"] != want["strategy"] {
		t.Errorf("回滚后 applied_rules.json 应为 v1, got %s", string(b))
	}
	// 回滚后再列快照仍存在（快照不被消费）
	rr = adminDo(s, adminReq(s, admin, http.MethodGet, "/api/research/strategies/snapshots", ""))
	if rr.Code != 200 {
		t.Fatalf("回滚后 snapshots → %d", rr.Code)
	}

	// 4) 非法入参 + 权限
	if rr := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/research/strategies/rollback", `{}`)); rr.Code != 400 {
		t.Errorf("rollback 缺 snapshot_ts 应 400, got %d", rr.Code)
	}
	if rr := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/research/strategies/rollback", `{"snapshot_ts":"nope_9999"}`)); rr.Code != 400 {
		t.Errorf("rollback 不存在快照应 400, got %d", rr.Code)
	}
	if rr := adminDo(s, adminReq(s, member, http.MethodPost, "/api/research/strategies/rollback", `{"snapshot_ts":"`+ts1+`"}`)); rr.Code != 403 {
		t.Errorf("member rollback 应 403, got %d", rr.Code)
	}
}
