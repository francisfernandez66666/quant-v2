// paper_reports_test.go — §D-1（GAP_VERIFY_20260917_PM）夜间研究报告读端回归：
// GET /api/research/paper-reports 空表回 []（非 null）、倒序、正文解回对象、
// 普通用户 403（正文跨账号聚合，admin-only 口径）、研究库未接入 503。
package server

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"quant-trading-v2/internal/auth"
	"quant-trading-v2/internal/store"
)

// newPaperReportsTestServer 组装带研究库的测试服务（admin + 普通用户）。
func newPaperReportsTestServer(t *testing.T) (*Server, *auth.User, *auth.User, *store.DB) {
	t.Helper()
	s, admin := newAdminTestServer(t)
	user, err := s.auth.CreateUser("member", "pw", auth.RoleUser, nil, 0)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	db, err := store.Open(filepath.Join(t.TempDir(), "trading.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	s.SetResearch(db, t.TempDir())
	return s, admin, user, db
}

func TestPaperResearchReportsEndpoint(t *testing.T) {
	s, admin, user, db := newPaperReportsTestServer(t)

	// Arrange：空表先断言 []（前端渲染契约，null 会炸 map）
	rr := adminDo(s, adminReq(s, admin, http.MethodGet, "/api/research/paper-reports", ""))
	if rr.Code != 200 {
		t.Fatalf("admin 空表应 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	var empty struct {
		Reports []store.PaperResearchReport `json:"reports"`
		Count   int                         `json:"count"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &empty); err != nil {
		t.Fatalf("解空响应: %v", err)
	}
	if empty.Reports == nil || empty.Count != 0 {
		t.Fatalf("空表应 reports=[] count=0, got %s", rr.Body.String())
	}

	// Act：落两条不同日期报告（模拟 researchd 连续两晚产出）
	if err := db.SavePaperResearchReport("2026-09-16", admin.ID, `{"generated_at":"g1","trades":[]}`); err != nil {
		t.Fatalf("save r1: %v", err)
	}
	if err := db.SavePaperResearchReport("2026-09-17", admin.ID, `{"generated_at":"g2","trades":[{"count":3}]}`); err != nil {
		t.Fatalf("save r2: %v", err)
	}

	// Assert：倒序（最新在前）+ summary 解回对象
	rr = adminDo(s, adminReq(s, admin, http.MethodGet, "/api/research/paper-reports", ""))
	if rr.Code != 200 {
		t.Fatalf("应 200, got %d", rr.Code)
	}
	var resp struct {
		Reports []struct {
			Date    string          `json:"date"`
			Summary json.RawMessage `json:"summary"`
		} `json:"reports"`
		Count int `json:"count"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解响应: %v body=%s", err, rr.Body.String())
	}
	if resp.Count != 2 || resp.Reports[0].Date != "2026-09-17" || resp.Reports[1].Date != "2026-09-16" {
		t.Fatalf("应 2 条且 09-17 在前, got %s", rr.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(resp.Reports[0].Summary, &body); err != nil || body["generated_at"] != "g2" {
		t.Fatalf("正文应为解回对象, got %s", string(resp.Reports[0].Summary))
	}

	// Assert：普通用户 403（跨账号正文，admin-only）
	rr = adminDo(s, adminReq(s, user, http.MethodGet, "/api/research/paper-reports", ""))
	if rr.Code != 403 {
		t.Fatalf("普通用户应 403, got %d", rr.Code)
	}
}

func TestPaperResearchReportsNoDB(t *testing.T) {
	// Arrange：未 SetResearch 的服务 → 503（研究进程分离部署时 quant 可无研究库）
	s, admin := newAdminTestServer(t)
	rr := adminDo(s, adminReq(s, admin, http.MethodGet, "/api/research/paper-reports", ""))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("研究库未接入应 503, got %d", rr.Code)
	}
}
