// 跨账号读面收口回归（§FIX-20261028-F01，2026-10-29）。
//
// 缺陷本体（UAT 实弹实录）：成员号 GET /api/positions 收到的行里带着别人的 user_id；
// /api/holdings 与 admin 的读数逐字节同指纹；/api/paper/positions 直接是运营账号的纸面账。
// 根因三处：paperEngineFor 忽略入参恒取 operatorID；handleListPositions 固定 ListFor("")；
// paperPnlTotals 读全量后不按账号过滤。
//
// 本文件锁三件事（读面，不涉及写面）：
//  1. /api/positions 默认只回本账号；admin 带 ?scope=all 才看全量；非 admin 带该参数不放行；
//  2. 遗留 user_id=<空串> 的旧行归属运营账号（读时归一，不迁移数据）；
//  3. /api/holdings 同样按账号过滤。
//
// English: regression for the cross-account read scoping — self by default, operator-wide only
// for admins via ?scope=all, and legacy empty-user_id rows attributed to the operator account.
package server

import (
	"encoding/json"
	"net/http"
	"testing"

	"quant-trading-v2/internal/auth"
	"quant-trading-v2/internal/display"
	"quant-trading-v2/internal/report"
	"quant-trading-v2/internal/store"
)

// positionsResp /api/positions 的响应形状（只取本测试关心的字段）。
type positionsResp struct {
	Positions []report.ExecLog `json:"positions"`
	Scope     string           `json:"scope"`
}

// seedTwoAccounts 造三行运营账本：admin 一行、member 一行、遗留空 user_id 一行。
func seedTwoAccounts(t *testing.T, s *Server, admin, member *auth.User) {
	t.Helper()
	s.rpt = report.NewFromLogs([]report.ExecLog{
		{SignalID: "pap-admin", UserID: admin.ID, Code: "600000", Name: "浦发银行", Direction: "做多", Strategy: "手动", Status: "持仓中", Quantity: 100, EntryPrice: 10},
		{SignalID: "pap-member", UserID: member.ID, Code: "000001", Name: "平安银行", Direction: "做多", Strategy: "手动", Status: "持仓中", Quantity: 200, EntryPrice: 11},
		{SignalID: "pap-legacy", UserID: "", Code: "600519", Name: "贵州茅台", Direction: "做多", Strategy: "手动", Status: "持仓中", Quantity: 100, EntryPrice: 1500},
	})
}

// TestPositionsScopedToRequester /api/positions 默认只看自己；admin 显式 ?scope=all 才全量。
func TestPositionsScopedToRequester(t *testing.T) {
	s, admin := newAdminTestServer(t)
	member, err := s.auth.CreateUser("member1", "memberpw", auth.RoleUser, nil, 0)
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	seedTwoAccounts(t, s, admin, member)

	get := func(u *auth.User, path string) positionsResp {
		t.Helper()
		rr := adminDo(s, adminReq(s, u, http.MethodGet, path, ""))
		if rr.Code != http.StatusOK {
			t.Fatalf("GET %s → %d body=%s", path, rr.Code, rr.Body.String())
		}
		var out positionsResp
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatalf("解析 %s 失败: %v body=%s", path, err, rr.Body.String())
		}
		return out
	}
	codes := func(r positionsResp) map[string]bool {
		m := map[string]bool{}
		for _, p := range r.Positions {
			m[p.Code] = true
		}
		return m
	}

	// ① 成员只看自己
	m := codes(get(member, "/api/positions"))
	if !m["000001"] || m["600000"] || m["600519"] {
		t.Fatalf("成员应只看到自己的行，实到 %v", m)
	}
	// ② 成员带 ?scope=all 不得放行（非 admin 忽略参数）
	m = codes(get(member, "/api/positions?scope=all"))
	if !m["000001"] || m["600000"] {
		t.Fatalf("非 admin 带 scope=all 不得看全量，实到 %v", m)
	}
	// ③ admin 默认：自己的行 + 遗留空 user_id 行（归属运营账号）
	ra := get(admin, "/api/positions")
	m = codes(ra)
	if !m["600000"] || !m["600519"] || m["000001"] {
		t.Fatalf("admin 默认视角应为「自己 + 遗留行」，实到 %v", m)
	}
	if ra.Scope != "self" {
		t.Fatalf("admin 默认 scope 标记应为 self，实到 %q", ra.Scope)
	}
	// ④ admin 显式 ?scope=all：全量三行
	ra = get(admin, "/api/positions?scope=all")
	m = codes(ra)
	if !m["600000"] || !m["600519"] || !m["000001"] {
		t.Fatalf("admin scope=all 应看到全部三行，实到 %v", m)
	}
	if ra.Scope != "all" {
		t.Fatalf("scope 标记应为 all，实到 %q", ra.Scope)
	}
}

// TestHoldingsScopedToRequester /api/holdings 同样按账号过滤（含遗留空行的归属）。
func TestHoldingsScopedToRequester(t *testing.T) {
	s, admin := newAdminTestServer(t)
	member, err := s.auth.CreateUser("member2", "memberpw", auth.RoleUser, nil, 0)
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	seedTwoAccounts(t, s, admin, member)
	// /api/holdings 需要实盘账本（可用资金/校准值）与实时聚合器（打分快照）；
	// 测试里各注入一个空的，避免 nil 解引用（不是被测逻辑的一部分）。
	s.agg = display.New()
	db, err := store.Open(t.TempDir() + "/live.db")
	if err != nil {
		t.Fatalf("open live db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	s.SetLiveDB(db)

	type holdingsResp struct {
		Holdings []struct {
			Code string `json:"code"`
		} `json:"holdings"`
	}
	get := func(u *auth.User) map[string]bool {
		t.Helper()
		rr := adminDo(s, adminReq(s, u, http.MethodGet, "/api/holdings", ""))
		if rr.Code != http.StatusOK {
			t.Fatalf("GET /api/holdings → %d body=%s", rr.Code, rr.Body.String())
		}
		var out holdingsResp
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatalf("解析失败: %v body=%s", err, rr.Body.String())
		}
		m := map[string]bool{}
		for _, h := range out.Holdings {
			m[h.Code] = true
		}
		return m
	}
	if m := get(member); !m["000001"] || m["600000"] || m["600519"] {
		t.Fatalf("成员 holdings 应只含自己的行，实到 %v", m)
	}
	if m := get(admin); !m["600000"] || !m["600519"] || m["000001"] {
		t.Fatalf("admin holdings 应为「自己 + 遗留行」，实到 %v", m)
	}
}
