// perm_surface_test.go — §F-6（2026-11-01）：权限面收口与「角色≠权限」显式化。
//
// 背景：22 条路由原挂 adminMiddleware（任何 role=admin 可进）。这有两层问题：
//
//	① 收口粒度错：`funds_admin` 该管的事被"是不是管理员"代替，多租户化时无法表达"这个人被授予了
//	   哪些能力"；
//	② `role=admin` 极易被误读成"能看一切"，而实际上 HasExplicitPerm 只认 perms 数组——
//	   迁移只给**首个** admin 发五权限位，第二个 admin 可以零权限位却仍叫 admin。
//
// 本文件锁三件事：路由面已无 adminMiddleware 注册、角色不再隐式放行、auth/me 暴露 effective_perms。
//
// English: §F-6 — no role-level admin gate left on the route surface; the role grants nothing
// implicitly; and /api/auth/me exposes effective_perms so the UI stops treating role as capability.
package server

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"

	"quant-trading-v2/internal/auth"
)

// I1：注册面**零** adminMiddleware。元断言——新加一条就会红。
func TestAdminMiddlewareRoutesHaveExplicitPerm(t *testing.T) {
	b, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatalf("读取 server.go 失败: %v", err)
	}
	// 只数"注册"形态（adminMiddleware 包住某个 handler），定义处不算。
	re := regexp.MustCompile(`s\.adminMiddleware\(s\.handle`)
	if m := re.FindAllString(string(b), -1); len(m) != 0 {
		t.Fatalf("仍有 %d 条路由挂在角色级 adminMiddleware 上（应为 0）：%v", len(m), m)
	}
	// 反向确认：五权限位都还在用，别把收口做成"全删"。
	for _, p := range []string{"PermFundsAdmin", "PermTradingAdmin", "PermUserAdmin", "PermConfigAdmin", "PermOpsAdmin"} {
		if !strings.Contains(string(b), "strictPermMiddleware(auth."+p) {
			t.Fatalf("权限位 %s 在注册面消失了——收口不该把能力删掉", p)
		}
	}
}

// I2：role=admin 但 perms 为空 ⇒ 敏感端点必须 403（角色不再隐式放行）。
//
// 这条正是 NEW-3 的机器化：`role=admin` + 零权限位是真实存在的状态，不是理论假设。
func TestRoleAdminWithoutPermsGets403OnSensitiveRoutes(t *testing.T) {
	s, _ := newAdminTestServer(t)
	// 建一个 role=admin、但**不给任何权限位**的账号
	bare, err := s.auth.CreateUser("bareadmin", "pw123456", auth.RoleAdmin, nil, 0)
	if err != nil {
		t.Fatalf("create bare admin: %v", err)
	}
	if len(bare.Perms) != 0 {
		t.Fatalf("前提被破坏：该账号应零权限位，实得 %v", bare.Perms)
	}
	for _, path := range []string{"/api/funds/today", "/api/usage", "/api/opslog"} {
		rr := adminDo(s, adminReq(s, bare, "GET", path, ""))
		if rr.Code != 403 {
			t.Fatalf("%s：role=admin 且零权限位应 403，实得 %d body=%s", path, rr.Code, rr.Body.String())
		}
	}
}

// I3：/api/auth/me 必须带 effective_perms，且与 perms 一致（契约：角色不增权）。
func TestAuthMeReturnsEffectivePerms(t *testing.T) {
	s, admin := newAdminTestServer(t)
	rr := adminDo(s, adminReq(s, admin, "GET", "/api/auth/me", ""))
	if rr.Code != 200 {
		t.Fatalf("/api/auth/me 应 200，实得 %d", rr.Code)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是 JSON: %v", err)
	}
	eff, ok := body["effective_perms"]
	if !ok {
		t.Fatalf("缺 effective_perms 字段（前端无法区分『role=admin 但无权限』），实得 %v", body)
	}
	// 与 perms 同值：角色不隐式增权
	perms, _ := body["perms"].([]interface{})
	effList, _ := eff.([]interface{})
	if len(effList) != len(perms) {
		t.Fatalf("effective_perms 应与 perms 等长，实得 %d vs %d", len(effList), len(perms))
	}
}

// I4：持 trading_admin 的成员必须能读 /api/t-live/rounds —— 防"收口过严把自家运维挡在外面"。
// 断言用"不是 403"而非"是 200"：本用例只验权限层，不依赖引擎是否装配（无引擎时 handler 自会回空）。
func TestTradingAdminCanReadTLiveRounds(t *testing.T) {
	s, _ := newAdminTestServer(t)
	op, err := s.auth.CreateUser("trader", "pw123456", auth.RoleUser, []string{auth.PermTradingAdmin}, 0)
	if err != nil {
		t.Fatalf("create trader: %v", err)
	}
	rr := adminDo(s, adminReq(s, op, "GET", "/api/t-live/rounds", ""))
	if rr.Code == 403 {
		t.Fatalf("持 trading_admin 却被 403 —— 收口过严（body=%s）", rr.Body.String())
	}
}

// I5：无该权限位的成员访问同一端点必须 403（对照组，防"全员放行"糊过去）。
func TestMemberWithoutTradingAdminBlockedOnTLiveRounds(t *testing.T) {
	s, _ := newAdminTestServer(t)
	plain, err := s.auth.CreateUser("plain", "pw123456", auth.RoleUser, nil, 0)
	if err != nil {
		t.Fatalf("create plain: %v", err)
	}
	rr := adminDo(s, adminReq(s, plain, "GET", "/api/t-live/rounds", ""))
	if rr.Code != 403 {
		t.Fatalf("无 trading_admin 的成员应 403，实得 %d body=%s", rr.Code, rr.Body.String())
	}
}
