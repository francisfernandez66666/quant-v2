// §U-2/§U-3/§U-5（2026-09-14 像素级 UAT 修复批）端点回归：
//   - GET /api/qmt/orders 当日过滤与 503 分支（撤单 UI 的数据源）；
//   - POST /api/config/qmt 携带 halted 的持久化与即时生效挂点（kill-switch 不再滞留开关队列）；
//   - POST /api/admin/users/cleanup 脏账号清理（过期/temp 禁用），dry_run 预览不误删。
// English: regression tests for the §U-2/§U-3/§U-5 fixes — the today-filtered orders endpoint
// (data source for the new cancel UI), the halted-carrying config save (immediate kill switch),
// and the stale-account cleanup endpoint with dry-run preview.
package server

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"quant-trading-v2/internal/auth"
	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/store"
)

// TestQMTOrdersTodayOnly GET /api/qmt/orders 只回北京时间当日委托（历史日不混入撤单列表）。
func TestQMTOrdersTodayOnly(t *testing.T) {
	s, admin := newAdminTestServer(t)
	// 未接实盘库 → 503（与 trades 同口径，前端走失败分支不白屏）
	if rr := adminDo(s, adminReq(s, admin, http.MethodGet, "/api/qmt/orders", "")); rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("无实盘库应 503, got %d", rr.Code)
	}
	db, err := store.Open(t.TempDir() + "/live.db")
	if err != nil {
		t.Fatalf("open live db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	s.SetLiveDB(db)
	today := cntime.In(time.Now()).Format("2006-01-02T10:00:00+08:00")
	yday := cntime.In(time.Now().AddDate(0, 0, -1)).Format("2006-01-02T10:00:00+08:00")
	for _, o := range []store.RealOrder{
		{OrderID: "GW-T1", SignalID: "sig-t1", Code: "600519.SH", Side: "买入", Status: "已报", Price: 1500, Qty: 100, CreatedAt: today, UserID: admin.ID},
		{OrderID: "GW-Y1", SignalID: "sig-y1", Code: "600519.SH", Side: "买入", Status: "已撤", Price: 1500, Qty: 100, CreatedAt: yday, UserID: admin.ID},
	} {
		if _, err := db.UpsertRealOrder(o); err != nil {
			t.Fatalf("upsert order: %v", err)
		}
	}
	rr := adminDo(s, adminReq(s, admin, http.MethodGet, "/api/qmt/orders", ""))
	if rr.Code != 200 {
		t.Fatalf("GET orders → %d body=%s", rr.Code, rr.Body.String())
	}
	var orders []store.RealOrder
	if err := json.Unmarshal(rr.Body.Bytes(), &orders); err != nil {
		t.Fatalf("解析失败: %v body=%s", err, rr.Body.String())
	}
	if len(orders) != 1 || orders[0].OrderID != "GW-T1" {
		t.Fatalf("应仅含当日单, got %+v", orders)
	}
}

// TestConfigSaveCarriesHalted POST /api/config/qmt 带 halted 持久化成功（即时同步挂点不报错，
// 控制器缺席时安全空转）——kill-switch 语义与 /api/qmt/halt 同口径，不再滞留开关队列。
func TestConfigSaveCarriesHalted(t *testing.T) {
	s, admin := newAdminTestServer(t)
	rr := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/config/qmt", `{"enabled":true,"mode":"manual","halted":true}`))
	if rr.Code != 200 {
		t.Fatalf("POST qmt(halted=true) → %d body=%s", rr.Code, rr.Body.String())
	}
	rr = adminDo(s, adminReq(s, admin, http.MethodGet, "/api/config/qmt", ""))
	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("GET qmt 解析失败: %v", err)
	}
	if resp["halted"] != true {
		t.Fatalf("halted 应持久化为 true, got %v", resp["halted"])
	}
	// 解除路径同样可用
	rr = adminDo(s, adminReq(s, admin, http.MethodPost, "/api/config/qmt", `{"halted":false}`))
	if rr.Code != 200 {
		t.Fatalf("POST qmt(halted=false) → %d", rr.Code)
	}
}

// TestCleanupUsersDryRunAndDelete 清理端点：过期 temp 与已禁用 temp 被清理；
// 在用 temp / 正式用户 / admin 绝不误删；dry_run 只预览不落刀。
func TestCleanupUsersDryRunAndDelete(t *testing.T) {
	s, admin := newAdminTestServer(t)
	mgr := s.auth
	if _, err := mgr.CreateUser("alice", "pw-alice", auth.RoleUser, nil, 0); err != nil {
		t.Fatalf("create alice: %v", err)
	}
	// temp 账号需邀请码：逐号现取现用（CreateInvite 单次消耗）
	newTemp := func(d time.Duration) *auth.User {
		t.Helper()
		code, err := mgr.CreateInvite()
		if err != nil {
			t.Fatalf("create invite: %v", err)
		}
		u, err := mgr.CreateTemp(d, code)
		if err != nil {
			t.Fatalf("create temp(%v): %v", d, err)
		}
		return u
	}
	expired := newTemp(time.Nanosecond) // 创建即过期
	disabled := newTemp(24 * time.Hour)
	if err := mgr.SetEnabled(disabled.ID, false); err != nil {
		t.Fatalf("disable temp: %v", err)
	}
	live := newTemp(24 * time.Hour) // 在用临时号：不得清理

	// 1) dry_run：命中 2 条（expired、temp_disabled），但都不删
	rr := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/admin/users/cleanup", `{"dry_run":true}`))
	if rr.Code != 200 {
		t.Fatalf("dry_run → %d body=%s", rr.Code, rr.Body.String())
	}
	var preview struct {
		Deleted []struct {
			ID     string `json:"id"`
			Reason string `json:"reason"`
		} `json:"deleted"`
		Count  int  `json:"count"`
		DryRun bool `json:"dry_run"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &preview); err != nil {
		t.Fatalf("解析 dry_run: %v", err)
	}
	if preview.Count != 2 || !preview.DryRun {
		t.Fatalf("dry_run 应预览 2 条, got %+v", preview)
	}
	if len(mgr.ListUsers()) != 5 {
		t.Fatalf("dry_run 不得删除任何账号, got %d", len(mgr.ListUsers()))
	}
	// 2) 真删：只剩 admin/alice/live temp
	rr = adminDo(s, adminReq(s, admin, http.MethodPost, "/api/admin/users/cleanup", `{}`))
	if err := json.Unmarshal(rr.Body.Bytes(), &preview); err != nil {
		t.Fatalf("解析 cleanup: %v", err)
	}
	if preview.Count != 2 || preview.DryRun {
		t.Fatalf("cleanup 应删 2 条, got %+v", preview)
	}
	left := map[string]bool{}
	for _, u := range mgr.ListUsers() {
		left[u.ID] = true
	}
	for id, want := range map[string]bool{admin.ID: true, live.ID: true, expired.ID: false, disabled.ID: false} {
		if left[id] != want {
			t.Fatalf("账号 %s 存续=%v 期望=%v", id, left[id], want)
		}
	}
	var alice bool
	for _, u := range mgr.ListUsers() {
		if u.Username == "alice" {
			alice = true
		}
	}
	if !alice {
		t.Fatal("正式用户在用不得被清理")
	}
}
