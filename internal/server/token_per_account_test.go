// token_per_account_test.go — §TOKEN-PERAACCT（2026-10-30）Go 侧那一刀：账号保存配置时，
// 绝不把**继承来的**实盘凭证写进自己的快照；同时给"撤销一把已扩散的凭证"开出显式通道。
//
// 缺陷原文（现网 2026-10-30 只读盘查锤实）：七个 App 账号的有效 token 全是同一把生产钥匙——
// admin/test01 各自快照里写着同一把（applySetQMTConfig 整份克隆"有效配置"），其余五个账号
// 靠"无账号覆盖 ⇒ 回退全局 rules.qmt"也拿到同一把。于是网关回报只能按 token 反查账号、
// 命中 users 顺序里第一个（=admin），下单方自己那行永远没人推进，同一资金账号被记成两本账。
// 本文件锁的三条不变量：
//
//	N4 成员账号保存别的字段时，继承来的凭证不落它的快照（一次保存就把钥匙扩散给一个新账号）；
//	N2 显式携带 token = owner 在签发，必须落盘；此后它有自己的钥匙，普通保存不许把它擦掉；
//	N1 `"token":""` 是**显式撤销**而不是"没带这个键"——旧语义把两者混为一谈，导致
//	   没有任何产品通道能把一把已经扩散出去的实盘凭证摘掉（只能手改 auth.json）。
package server

import (
	"net/http"
	"testing"

	"quant-trading-v2/internal/config"
)

// seedGlobalQMTKey 把全局兜底位写成一把"生产钥匙"，复现现网形态（成员账号无覆盖 ⇒ 继承它）。
func seedGlobalQMTKey(t *testing.T, s *Server, key string) {
	t.Helper()
	q := s.cfg.Get().QMT
	q.Token = key
	q.GatewayURL = "http://127.0.0.1:8789"
	if err := s.cfg.SetQMTConfigFor("", &q); err != nil {
		t.Fatalf("写全局 rules.qmt 失败: %v", err)
	}
}

// maskSample 产出会被 isMaskedSecret 认成脱敏哨兵的串（GET /api/config/qmt 回显就是这三颗星）。
func maskSample() string { return "***" }

// TestMemberSaveDoesNotCloneInheritedToken N4：成员账号点一次保存只落自己的行为参数。
func TestMemberSaveDoesNotCloneInheritedToken(t *testing.T) {
	s, admin := newAdminTestServer(t)
	seedGlobalQMTKey(t, s, "PROD-KEY")
	friend, err := s.auth.CreateUser("friend", "pw-friend", "user", nil, 0)
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	// 前置自证：此刻它的有效 token 确实是继承来的生产钥匙（与现网五个朋友号同形）
	if got := s.cfg.GetQMTConfigFor(friend.ID).Token; got != "PROD-KEY" {
		t.Fatalf("成员有效 token 应为继承值 PROD-KEY, got %q", got)
	}
	req := adminReq(s, admin, http.MethodPost, "/api/admin/users/"+friend.ID+"/config/qmt", `{"mode":"manual"}`)
	if rr := adminDo(s, req); rr.Code != 200 {
		t.Fatalf("保存期望 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if got := s.cfg.GetQMTConfigFor(friend.ID).Token; got != "" {
		t.Fatalf("§TOKEN-PERAACCT N4 违例：继承来的凭证被写进了成员快照（token=%q）", got)
	}
	// 一刀切的风险是"顺手把别的配置也清了"——其余字段必须照常落盘
	if s.cfg.GetQMTConfigFor(friend.ID).Mode != "manual" {
		t.Fatalf("成员快照的 mode 应照常落 manual")
	}
}

// TestExplicitIssuedTokenPersists N2：显式签发是 owner 的授权动作，必须落盘且此后不被擦。
func TestExplicitIssuedTokenPersists(t *testing.T) {
	s, admin := newAdminTestServer(t)
	seedGlobalQMTKey(t, s, "PROD-KEY")
	member, err := s.auth.CreateUser("member2", "pw-member2", "user", nil, 0)
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	req := adminReq(s, admin, http.MethodPost, "/api/admin/users/"+member.ID+"/config/qmt", `{"token":"KEY-FOR-MEMBER2"}`)
	if rr := adminDo(s, req); rr.Code != 200 {
		t.Fatalf("签发保存期望 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if got := s.cfg.GetQMTConfigFor(member.ID).Token; got != "KEY-FOR-MEMBER2" {
		t.Fatalf("显式签发的 token 应落盘, got %q", got)
	}
	// 再保存别的字段：这把是它**自己的**钥匙（不再算继承），不许被擦——擦了就等于自锁实盘
	req2 := adminReq(s, admin, http.MethodPost, "/api/admin/users/"+member.ID+"/config/qmt", `{"mode":"auto"}`)
	if rr := adminDo(s, req2); rr.Code != 200 {
		t.Fatalf("二次保存期望 200, got %d", rr.Code)
	}
	if got := s.cfg.GetQMTConfigFor(member.ID).Token; got != "KEY-FOR-MEMBER2" {
		t.Fatalf("自有钥匙不该被擦, got %q", got)
	}
	if s.cfg.GetQMTConfigFor(member.ID).Mode != "auto" {
		t.Fatalf("二次保存的 mode 应落 auto")
	}
}

// TestOwnerKeepsItsOwnTokenAcrossSaves owner（现网=admin）保存其它字段不许打断实盘链路。
func TestOwnerKeepsItsOwnTokenAcrossSaves(t *testing.T) {
	s, admin := newAdminTestServer(t)
	seedGlobalQMTKey(t, s, "PROD-KEY")
	if rr := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/config/qmt",
		`{"token":"OWNER-KEY","enabled":true}`)); rr.Code != 200 {
		t.Fatalf("owner 首次保存期望 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if got := s.cfg.GetQMTConfigFor(admin.ID).Token; got != "OWNER-KEY" {
		t.Fatalf("owner 自有钥匙应落盘, got %q", got)
	}
	if rr := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/config/qmt",
		`{"price_type":"limit"}`)); rr.Code != 200 {
		t.Fatalf("owner 二次保存期望 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	after := s.cfg.GetQMTConfigFor(admin.ID)
	if after.Token != "OWNER-KEY" {
		t.Fatalf("§TOKEN-PERAACCT：自有钥匙被擦=实盘链路当场断, got %q", after.Token)
	}
	if !after.Enabled {
		t.Fatalf("二次保存不许回退 enabled")
	}
}

// TestExplicitEmptyTokenRevokesCredential N1：撤销必须是显式动作，且不连带清掉别的字段。
func TestExplicitEmptyTokenRevokesCredential(t *testing.T) {
	s, admin := newAdminTestServer(t)
	member, err := s.auth.CreateUser("member3", "pw-member3", "user", nil, 0)
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	if err := s.cfg.SetQMTConfigFor(member.ID, &config.QMTConfig{
		Token: "KEY-TO-REVOKE", GatewayURL: "http://127.0.0.1:8789", Enabled: true}); err != nil {
		t.Fatalf("seed member config: %v", err)
	}
	if got := s.cfg.GetQMTConfigFor(member.ID).Token; got != "KEY-TO-REVOKE" {
		t.Fatalf("前置条件不成立（钥匙没落上）, got %q", got)
	}
	if rr := adminDo(s, adminReq(s, admin, http.MethodPost,
		"/api/admin/users/"+member.ID+"/config/qmt", `{"token":""}`)); rr.Code != 200 {
		t.Fatalf("撤销期望 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if got := s.cfg.GetQMTConfigFor(member.ID).Token; got != "" {
		t.Fatalf("§TOKEN-PERAACCT：显式空 token 应撤销凭证, got %q", got)
	}
	if s.cfg.GetQMTConfigFor(member.ID).GatewayURL != "http://127.0.0.1:8789" {
		t.Fatalf("撤销凭证不该连带清掉 gateway_url")
	}
	// 缺键（没带 token）不许被当成撤销——否则前端一次普通保存就把人锁死
	if rr := adminDo(s, adminReq(s, admin, http.MethodPost,
		"/api/admin/users/"+member.ID+"/config/qmt", `{"mode":"manual"}`)); rr.Code != 200 {
		t.Fatalf("普通保存期望 200, got %d", rr.Code)
	}
	if s.cfg.GetQMTConfigFor(member.ID).Mode != "manual" {
		t.Fatalf("普通保存应照常落 mode")
	}
}

// TestMaskedTokenEchoDoesNotWipeOwnKey 前端回显的掩码串既不改写真值，也不触发撤销。
func TestMaskedTokenEchoDoesNotWipeOwnKey(t *testing.T) {
	s, admin := newAdminTestServer(t)
	if err := s.cfg.SetQMTConfigFor(admin.ID, &config.QMTConfig{
		Token: "REAL-KEY", GatewayURL: "http://127.0.0.1:8789"}); err != nil {
		t.Fatalf("seed owner config: %v", err)
	}
	body := `{"token":"` + maskSample() + `"}`
	if rr := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/config/qmt", body)); rr.Code != 200 {
		t.Fatalf("掩码保存期望 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if got := s.cfg.GetQMTConfigFor(admin.ID).Token; got != "REAL-KEY" {
		t.Fatalf("掩码回显不得改动真值, got %q", got)
	}
}
