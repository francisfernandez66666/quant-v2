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
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"quant-trading-v2/internal/auth"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/store"
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

// TestReportAttributionFollowsTheToken 方案 §5：多把在册钥匙下，回报归属必须由钥匙本身决定。
// 旧形态的坑不在"按 token 反查账号"这件事，而在**所有账号的 token 是同一把**：
// 遍历 users 时命中的是顺序里的第一个（现网=admin），于是 test01 下的单、admin 收的回报，
// 两个账号各记一本账，谁也推不动谁那行。钥匙逐账号唯一之后，first-match 才等价于唯一命中——
// 所以这里锁的是"第二把钥匙不会解析成第一个账号"，而不只是"能解析出账号"。
// 顺带锁住两条边界：未登记的钥匙必须解析为空（宁可 401，不许蒙一个账号）；
// 请求不带 token 时不许匹配到"快照里 token 为空"的账号（否则匿名请求就变成某个人的账）。
func TestReportAttributionFollowsTheToken(t *testing.T) {
	s, admin := newAdminTestServer(t)
	seedGlobalQMTKey(t, s, "PROD-KEY")
	u2, err := s.auth.CreateUser("u2", "pw-u2", "user", nil, 0)
	if err != nil {
		t.Fatalf("create u2: %v", err)
	}
	u3, err := s.auth.CreateUser("u3", "pw-u3", "user", nil, 0)
	if err != nil {
		t.Fatalf("create u3: %v", err)
	}
	if rr := adminDo(s, adminReq(s, admin, http.MethodPost,
		"/api/admin/users/"+u2.ID+"/config/qmt", `{"token":"KEY-U2","enabled":true}`)); rr.Code != 200 {
		t.Fatalf("签发 u2 期望 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if rr := adminDo(s, adminReq(s, admin, http.MethodPost,
		"/api/admin/users/"+u3.ID+"/config/qmt", `{"token":"KEY-U3","enabled":true}`)); rr.Code != 200 {
		t.Fatalf("签发 u3 期望 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	// 自证前提：两把钥匙各自唯一落进两个账号快照（不落在 admin 名下——admin 排在 users 第一个，
	// 若这里也能抄到继承值，下面的断言就退化成"命中第一个"了）
	if got := s.cfg.GetQMTConfigFor(u2.ID).Token; got != "KEY-U2" {
		t.Fatalf("前置条件不成立：u2 快照 token=%q", got)
	}
	if got := s.cfg.GetQMTConfigFor(u3.ID).Token; got != "KEY-U3" {
		t.Fatalf("前置条件不成立：u3 快照 token=%q", got)
	}
	if got := s.userForQMTToken("KEY-U2"); got != u2.ID {
		t.Fatalf("§TOKEN-PERAACCT：u2 的钥匙解析成 %q（应为 u2；解析成 admin 就是双本账的成因）", got)
	}
	if got := s.userForQMTToken("KEY-U3"); got != u3.ID {
		t.Fatalf("§TOKEN-PERAACCT：u3 的钥匙解析成 %q（first-match 退回了 users 顺序）", got)
	}
	// 没登记的钥匙一律空：让中间件回 401，而不是"猜一个账号"
	if got := s.userForQMTToken("KEY-NOBODY"); got != "" {
		t.Fatalf("未登记的钥匙必须无匹配, got %q", got)
	}
	// 请求不带 token 时不许匹配任何人：没配钥匙的账号一堆，它们的 token 都是空串，命中谁全凭顺序
	if got := s.userForQMTToken(""); got != "" {
		t.Fatalf("空 token 不许匹配到账号, got %q", got)
	}
	// 残留形状（如实标注，不当已修）：全局兜底位一旦有值，**没有自有覆盖的账号全部继承同一把**，
	// 反查仍是 first-match。本用例不锁"它该解析成谁"（那是顺序决定的，锁了就是把缺陷认成规格），
	// 只锁它不许串到已有自有钥匙的账号头上——自有钥匙必须赢过继承。
	if got := s.userForQMTToken("PROD-KEY"); got == u2.ID || got == u3.ID {
		t.Fatalf("§TOKEN-PERAACCT：全局继承钥匙解析成了持有自有钥匙的账号 %q（两本账会互相串）", got)
	}
}

// TestForgedBodyUidCannotMoveTheBook §安全 T1 的行为腿（此前只有文字锁）。
// §TOKEN-PERAACCT 之后网关注册表里可以有很多把钥匙，每个账号敲进来的是**自己那把**；
// 于是"回报体自带的 user_id 算不算数"从理论问题变成日常问题：A 号（或任何持有 A 号钥匙的人）
// 只要把 body 里的 user_id 写成 B，就能把自己的成交记到 B 名下、或把空持仓快照砸到 B 头上。
// 本用例锁两件事：委托行的归属只认 token 解析出的账号（body 自报被丢），
// 以及持仓对账的作用范围同理——被冒名的那个账号一行都不许动。
func TestForgedBodyUidCannotMoveTheBook(t *testing.T) {
	s, db, _ := newTestResearchServer(t)
	// 归属来自 qmtReportMiddleware 用钥匙反查后写进上下文的那个 uid，这里直接扮演它
	const holder = "u_holder" // 持有这把实盘钥匙的账号
	const victim = "u_victim" // 回报体想冒用的另一个账号
	sid := "buy:600519:fac_1:20261031"
	// 先给被冒名账号落一行委托 + 一手持仓，作为"没被动过"的对照
	if _, err := db.ApplyOrderReportTx(store.RealOrder{
		OrderID: "XT-VICTIM", SignalID: sid, Code: "600519.SH", Side: "买入",
		Status: "已报", Price: 10, Qty: 100, CreatedAt: "2026-10-31T09:30:00+08:00", UserID: victim}); err != nil {
		t.Fatalf("seed victim order: %v", err)
	}
	if _, err := db.UpsertRealPositions([]store.RealPosition{
		{TsCode: "600519.SH", Name: "贵州茅台", Qty: 100, CostPrice: 10, UserID: victim}}); err != nil {
		t.Fatalf("seed victim position: %v", err)
	}

	// ① 委托回报：钥匙属于 holder，body 却自报 victim ⇒ 归属必须是 holder
	reqBody := `{"type":"order","order_id":"XT-NEW","signal_id":"` + sid + `","code":"600519.SH",` +
		`"side":"买入","status":"已成","price":10,"qty":100,"at":"2026-10-31T09:31:00+08:00",` +
		`"created_at":"2026-10-31T09:30:00+08:00","user_id":"` + victim + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/qmt/report", bytes.NewBufferString(reqBody))
	req = req.WithContext(context.WithValue(req.Context(), ctxUserKey{}, &auth.User{ID: holder}))
	rr := httptest.NewRecorder()
	s.handleQMTReport(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("回报应 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	stHolder, err := db.RealOrderStatusForUser(holder, sid)
	if err != nil {
		t.Fatalf("读 holder 侧委托: %v", err)
	}
	if stHolder == "" {
		t.Fatalf("§安全 T1 违例：token 账号名下没有落这条委托（回报被记到了自报的 %s？）", victim)
	}
	stVictim, err := db.RealOrderStatusForUser(victim, sid)
	if err != nil {
		t.Fatalf("读 victim 侧委托: %v", err)
	}
	if stVictim != "已报" {
		t.Fatalf("§安全 T1 违例：自报 victim 的回报推进了被冒名账号的委托行, got %q", stVictim)
	}

	// ② 持仓对账：同一条冒名 body 的 user_id 不许把作用范围切到 victim
	posBody := `{"type":"positions","user_id":"` + victim + `","positions":[{"ts_code":"000001.SZ",` +
		`"name":"平安银行","qty":200,"cost_price":12}]}`
	req2 := httptest.NewRequest(http.MethodPost, "/api/qmt/report", bytes.NewBufferString(posBody))
	req2 = req2.WithContext(context.WithValue(req2.Context(), ctxUserKey{}, &auth.User{ID: holder}))
	rr2 := httptest.NewRecorder()
	s.handleQMTReport(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("持仓回报应 200, got %d body=%s", rr2.Code, rr2.Body.String())
	}
	all, err := db.RealPositions()
	if err != nil {
		t.Fatalf("读持仓: %v", err)
	}
	var victimQty, holderQty int
	for _, p := range all {
		if p.UserID == victim {
			victimQty += p.Qty
		}
		if p.UserID == holder {
			holderQty += p.Qty
		}
	}
	if victimQty != 100 {
		t.Fatalf("§安全 T1 违例：被冒名账号的持仓被动过（应仍是 100 股, got %v）", victimQty)
	}
	if holderQty != 200 {
		t.Fatalf("对账应落在持钥匙的账号名下（holder 应 200 股, got %v）", holderQty)
	}
}

// TestOwnedTokenWinsOverInheritedMatch §TOKEN-PERAACCT 拆 first-match 那半颗雷（2026-10-31）。
// 旧实现只有一趟：`GetRulesFor()` 对没落过覆盖的账号回退全局兜底，所以全局有值时
// **所有无覆盖账号都"持有"同一把**，归属命中谁全看 users 顺序。现分两趟：
// ① 这把钥匙写在谁的自有快照里 → 归谁（不受顺序影响）；
// ② 谁都没有这把、只有兜底位有 → 仍按老形态解析（单账号旧部署不许因为这次改动变成 401）。
func TestOwnedTokenWinsOverInheritedMatch(t *testing.T) {
	s, admin := newAdminTestServer(t)
	seedGlobalQMTKey(t, s, "PROD-KEY")
	member, err := s.auth.CreateUser("member4", "pw-member4", "user", nil, 0)
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	// admin 排在 users 第一个且不落自己的快照 ⇒ 旧实现里它靠继承就能抢走归属
	if got := s.cfg.GetQMTConfigFor(admin.ID).Token; got != "PROD-KEY" {
		t.Fatalf("前置条件不成立：admin 应仍是继承形态（读得到 PROD-KEY）, got %q", got)
	}
	if rr := adminDo(s, adminReq(s, admin, http.MethodPost,
		"/api/admin/users/"+member.ID+"/config/qmt", `{"token":"PROD-KEY"}`)); rr.Code != 200 {
		t.Fatalf("显式签发 PROD-KEY 给 member 期望 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if got := s.userForQMTToken("PROD-KEY"); got != member.ID {
		t.Fatalf("§TOKEN-PERAACCT：自有这把钥匙的账号被继承账号抢走归属（got=%q want=%q）", got, member.ID)
	}

	// ② 纯继承形态：把 member 那把摘掉，此刻只有全局兜底持有它 ⇒ 仍必须能解析出某个账号
	if rr := adminDo(s, adminReq(s, admin, http.MethodPost,
		"/api/admin/users/"+member.ID+"/config/qmt", `{"token":""}`)); rr.Code != 200 {
		t.Fatalf("撤销 member 期望 200, got %d", rr.Code)
	}
	if got := s.userForQMTToken("PROD-KEY"); got == "" {
		t.Fatalf("兼容腿破了：旧形态（只有全局兜底有值）现在解析不出账号 ⇒ 现网升级即所有回报 401")
	}
	if got := s.userForQMTToken("KEY-NOBODY"); got != "" {
		t.Fatalf("未登记的钥匙必须无匹配, got %q", got)
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
