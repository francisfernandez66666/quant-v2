// 文件概述（internal/server/uat_fix_20261114_test.go）
// §F2-20261114 写面门禁的行为腿（§184 A1/A5/E4/E7 的进程内验证）：
//   - DELETE /api/alerts：缺 confirm → 400；confirm+无账号 → 401（confirm 不是免鉴权钥匙）；
//   - POST /api/holdings/balance：键缺失（{"_probe":true}）→ 400；显式 {"available_balance":0}
//     → 200 且落库 0（「键缺失」与「显式 0」必须可分——F2 v3 订正的核心）；
//   - POST /api/admin/users/cleanup：空体 → 400；dry_run:true → 预览档；dry_run:false → 真删档。
//
// English: §F2-20261114 write-surface gate behaviors — confirm/auth on destructive DELETEs,
// pointer-based balance key-presence semantics, explicit dry_run for user cleanup.
package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"quant-trading-v2/internal/auth"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/store"
	"quant-trading-v2/internal/trading"
)

func f2WithUser(r *http.Request, uid string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), ctxUserKey{}, &auth.User{ID: uid}))
}

func TestF2ClearAlertsConfirmAndAuth(t *testing.T) {
	s := &Server{} // ctrlFor 返回 nil → confirm+账号齐全时走 no_engine 分支（本测只验闸，不验清空）
	// ① 缺 confirm → 400
	rr := httptest.NewRecorder()
	r1 := httptest.NewRequest(http.MethodDelete, "/api/alerts", nil)
	s.handleClearAlerts(rr, r1)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("缺 confirm 应 400, got %d body=%s", rr.Code, rr.Body.String())
	}
	// ② confirm + 无账号 → 401（confirm 不是免鉴权钥匙，§184 E7）
	rr2 := httptest.NewRecorder()
	r2 := httptest.NewRequest(http.MethodDelete, "/api/alerts?confirm=CLEAR_ALERTS", nil)
	s.handleClearAlerts(rr2, r2)
	if rr2.Code != http.StatusUnauthorized {
		t.Fatalf("confirm+匿名应 401, got %d body=%s", rr2.Code, rr2.Body.String())
	}
	// ③ confirm + 账号 + 无引擎 → 200 no_engine（闸放行，清空走 no_engine 诚实护栏）
	rr3 := httptest.NewRecorder()
	r3 := f2WithUser(httptest.NewRequest(http.MethodDelete, "/api/alerts?confirm=CLEAR_ALERTS", nil), "u_f2")
	s.handleClearAlerts(rr3, r3)
	if rr3.Code != http.StatusOK || !strings.Contains(rr3.Body.String(), "no_engine") {
		t.Fatalf("confirm+账号+无引擎应 200 no_engine, got %d body=%s", rr3.Code, rr3.Body.String())
	}
}

func TestF2SetBalanceKeyPresence(t *testing.T) {
	db, _ := store.Open(filepath.Join(t.TempDir(), "t.db"))
	defer db.Close()
	s := &Server{liveDB: db}
	uid := "u_f2bal"
	// ① 键缺失（{"_probe":true}）→ 400（旧 float64 零值语义会把可用资金写成 0）
	rr := httptest.NewRecorder()
	r1 := f2WithUser(httptest.NewRequest(http.MethodPost, "/api/holdings/balance", strings.NewReader(`{"_probe":true}`)), uid)
	s.handleFixSetBalance(rr, r1)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("键缺失应 400, got %d body=%s", rr.Code, rr.Body.String())
	}
	// ② 显式 0 → 200（清零是正当业务动作），且读回 available_cash=0
	rr2 := httptest.NewRecorder()
	r2 := f2WithUser(httptest.NewRequest(http.MethodPost, "/api/holdings/balance", strings.NewReader(`{"available_balance":0}`)), uid)
	s.handleFixSetBalance(rr2, r2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("显式 0 应 200, got %d body=%s", rr2.Code, rr2.Body.String())
	}
	acc, _ := s.realAccountFor(uid)
	if acc.AvailableCash != 0 {
		t.Fatalf("显式 0 应落库 available_cash=0, got %v", acc.AvailableCash)
	}
}

func TestF2CleanupExplicitDryRun(t *testing.T) {
	s, admin := newAdminTestServer(t)
	// ① 空体 → 400（owner 裁决路 B：dry_run 必须显式携带）
	rr := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/admin/users/cleanup", ``))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("空体应 400, got %d body=%s", rr.Code, rr.Body.String())
	}
	// ② dry_run:true → 预览档（dry_run 回 true）
	rr2 := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/admin/users/cleanup", `{"dry_run":true}`))
	var out struct {
		Count  int  `json:"count"`
		DryRun bool `json:"dry_run"`
	}
	if err := json.Unmarshal(rr2.Body.Bytes(), &out); err != nil {
		t.Fatalf("解析 dry_run:true: %v body=%s", err, rr2.Body.String())
	}
	if rr2.Code != 200 || !out.DryRun {
		t.Fatalf("dry_run:true 应 200 预览档, got %d body=%s", rr2.Code, rr2.Body.String())
	}
	// ③ dry_run:false → 真删档（本夹具无僵尸号 → count=0，但 dry_run 回 false）
	rr3 := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/admin/users/cleanup", `{"dry_run":false}`))
	var out3 struct {
		Count  int  `json:"count"`
		DryRun bool `json:"dry_run"`
	}
	if err := json.Unmarshal(rr3.Body.Bytes(), &out3); err != nil {
		t.Fatalf("解析 dry_run:false: %v body=%s", err, rr3.Body.String())
	}
	if rr3.Code != 200 || out3.DryRun {
		t.Fatalf("dry_run:false 应 200 真删档, got %d body=%s", rr3.Code, rr3.Body.String())
	}
}

// TestF8GatewayNotConfigured409 §189 B1：未启用实盘（哨兵 ErrGatewayNotConfigured）→ 409
// gateway_not_configured，不再把配置态报成 502 网关故障。
func TestF8GatewayNotConfigured409(t *testing.T) {
	db, _ := store.Open(filepath.Join(t.TempDir(), "t.db"))
	defer db.Close()
	ctrl := trading.NewController(trading.NoopExecutor{}, nil, "u_f8",
		config.QMTConfig{Enabled: false, GatewayURL: ""}, nil)
	s := &Server{
		researchDB: db,
		registry:   &readyzStubRegistry{ctrl: ctrl},
	}
	rr := httptest.NewRecorder()
	r := f2WithUser(httptest.NewRequest(http.MethodGet, "/api/qmt/pending-review", nil), "u_f8")
	s.handleQMTPendingReview(rr, r)
	if rr.Code != http.StatusConflict {
		t.Fatalf("未配置应 409, got %d body=%s", rr.Code, rr.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil || body["error"] != "gateway_not_configured" {
		t.Fatalf("409 应带 error=gateway_not_configured, got %s", rr.Body.String())
	}
}
