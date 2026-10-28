// 终轮 UAT 修复的端点级回归（§FIX-20261028-F05/F-06/F-08/F-18/F-19a，2026-10-29）。
//
// 本文件集中锁五个"产品面可见性"修复：
//
//	F-05 /api/config/qmt 必须回显**生效的**禁撤/禁挂窗口（此前视图里根本没这两个键，
//	     客户 14:57 点撤单只能收到事后 409）；
//	F-06 收盘对账透传三态（无控制器 503 / 网关无该键 available:false / 有读数 available:true），
//	     任何一态都不许回空对象或伪造 0 笔；
//	F-08 实盘 T 账本空账必须给中立 reason（不从 t_at 推断"已收摊"）；
//	F-18 删号只清私有数据，**orders 资金流水必须保留**；
//	F-19a 手动直发单落 channel=manual 快照（MS/SS/目标留空，不伪造）。
//
// English: endpoint-level regression for the readout/visibility fixes and the delete/manual-
// decision contracts (audit rows must survive account deletion; manual rows must not fake MS/SS).
package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/auth"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/report"
	"quant-trading-v2/internal/store"
)

// TestConfigQMTEchoesEffectiveWindows F-05：回包必须带两个窗口键，且是生效值（非空）。
func TestConfigQMTEchoesEffectiveWindows(t *testing.T) {
	s, admin := newAdminTestServer(t)
	rr := adminDo(s, adminReq(s, admin, http.MethodGet, "/api/config/qmt", ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/config/qmt → %d body=%s", rr.Code, rr.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	cancel, _ := out["no_cancel_windows"].(string)
	newOrder, _ := out["no_new_order_windows"].(string)
	if strings.TrimSpace(cancel) == "" || strings.TrimSpace(newOrder) == "" {
		t.Fatalf("两个窗口键必须非空，实到 cancel=%q newOrder=%q", cancel, newOrder)
	}
	// 未接控制器时回的是出厂默认（与 trading.DefaultNoCancelWindows 同值）
	if !strings.Contains(cancel, "14:57") {
		t.Fatalf("禁撤窗口应含收盘集合竞价段 14:57，实到 %q", cancel)
	}
}

// TestCloseSnapshotNoControllerIs503 F-06：未接实盘 ⇒ 503（不得回 200 空对象）。
func TestCloseSnapshotNoControllerIs503(t *testing.T) {
	s, admin := newAdminTestServer(t)
	rr := adminDo(s, adminReq(s, admin, http.MethodGet, "/api/qmt/close-snapshot", ""))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("未接实盘应 503（空读数不代表无成交），实到 %d body=%s", rr.Code, rr.Body.String())
	}
}

// TestTLiveRoundsEmptyGivesReason F-08：空账本必须带中立 reason，且不得出现"收摊"这类推断。
func TestTLiveRoundsEmptyGivesReason(t *testing.T) {
	s, admin := newAdminTestServer(t)
	// 数据目录接入 + 该账号没有 t_rounds_live.json ⇒ missing 分支
	s.SetCacheDir(t.TempDir())
	rr := adminDo(s, adminReq(s, admin, http.MethodGet, "/api/t-live/rounds", ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/t-live/rounds → %d body=%s", rr.Code, rr.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	reason, _ := out["reason"].(string)
	if strings.TrimSpace(reason) == "" {
		t.Fatalf("空账本必须给 reason，实到 %+v", out)
	}
	if strings.Contains(reason, "收摊") {
		t.Fatalf("不得从 t_at 推断轮次状态（凭空断言）：%q", reason)
	}
}

// TestDeleteUserKeepsMoneyAuditRows F-18：删号只清私有数据，orders 流水必须保留。
func TestDeleteUserKeepsMoneyAuditRows(t *testing.T) {
	s, admin := newAdminTestServer(t)
	s.SetCacheDir(t.TempDir())
	victim, err := s.auth.CreateUser("victim1", "pw123456", auth.RoleUser, nil, 0)
	if err != nil {
		t.Fatalf("create victim: %v", err)
	}
	db, err := store.Open(t.TempDir() + "/live.db")
	if err != nil {
		t.Fatalf("open live db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	s.SetLiveDB(db)
	// 一条资金流水（必须保留）+ 一条决策快照（必须清）
	if _, err := db.UpsertRealOrder(store.RealOrder{
		OrderID: "GW-DEL-1", SignalID: "sig-del-1", Code: "600519.SH", Side: "买入",
		Status: "已撤", Price: 1500, Qty: 100, CreatedAt: "2026-10-29T09:31:00+08:00", UserID: victim.ID,
	}); err != nil {
		t.Fatalf("upsert order: %v", err)
	}
	if err := db.UpsertSignalDecision(store.SignalDecision{
		UserID: victim.ID, Day: "20261029", Channel: "live", SignalID: "sig-del-1",
		Code: "600519.SH", Strategy: "fac_1", At: "2026-10-29T09:31:00+08:00", Stage: "evaluated",
	}); err != nil {
		t.Fatalf("upsert decision: %v", err)
	}

	rr := adminDo(s, adminReq(s, admin, http.MethodDelete, "/api/admin/users/"+victim.ID, ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("删号应 200，实到 %d body=%s", rr.Code, rr.Body.String())
	}
	// ① 资金流水保留
	orders, err := db.RealOrdersForUser(victim.ID)
	if err != nil {
		t.Fatalf("read orders: %v", err)
	}
	if len(orders) != 1 {
		t.Fatalf("orders 资金流水必须保留，实到 %d 行", len(orders))
	}
	// ② 决策快照清空
	decs, err := db.SignalDecisionsForDay(victim.ID, "20261029")
	if err != nil {
		t.Fatalf("read decisions: %v", err)
	}
	if len(decs) != 0 {
		t.Fatalf("signal_decisions 应随删号清空，实到 %d 行", len(decs))
	}
	// ③ 结果串必须自证保留了审计
	if !strings.Contains(rr.Body.String(), "audit_kept=true") {
		t.Fatalf("响应应自证 audit_kept=true，实到 %s", rr.Body.String())
	}
}

// TestManualDecisionRecorded F-19a：手动直发落 channel=manual 快照，且不伪造 MS/SS。
func TestManualDecisionRecorded(t *testing.T) {
	s, _ := newAdminTestServer(t)
	db, err := store.Open(t.TempDir() + "/live.db")
	if err != nil {
		t.Fatalf("open live db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	s.SetLiveDB(db)
	s.rpt = report.NewFromLogs(nil)
	u := s.auth.UserByID(s.operatorID())
	if u == nil {
		t.Fatal("operator 账号不存在")
	}
	// 直接调内部方法（HTTP 路径需要 QMT 控制器，本用例只锁落库口径）
	s.persistManualDecision(u, "600519", 200, 1500.5, "买入", "manual@600519@1", "GW-M1")

	// 用当天日键查（与 persistManualDecision 内部同一口径：data.TradingDayDate）
	day := data.TradingDayDate(time.Now())
	rows, err := db.SignalDecisionsForDay(u.ID, day)
	if err != nil {
		t.Fatalf("read decisions: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("手动单应落 1 条决策快照，实到 %d", len(rows))
	}
	r := rows[0]
	if r.Channel != "manual" || r.Stage != "manual" {
		t.Fatalf("通道/阶段应为 manual，实到 %+v", r)
	}
	if r.Qty != 200 {
		t.Fatalf("数量应取实际下出的 200，实到 %d", r.Qty)
	}
	if r.MarketScale != 0 || r.Target != 0 {
		t.Fatalf("手动路径没有动态预算 ⇒ MS/目标必须留 0（不得伪造），实到 %+v", r)
	}
	if !strings.Contains(r.Reason, "不经动态预算") {
		t.Fatalf("reason 必须说明不经动态预算，实到 %q", r.Reason)
	}
}
