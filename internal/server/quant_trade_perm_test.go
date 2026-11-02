// quant_trade_perm_test.go — §MULTI-ACCT-B 批5（2026-11-02）：第六枚权限位 quant_trade「量化交易」。
//
// 这枚位的语义是"只看/只动**自己的**实盘账"，它是 trading_admin 的**子集**而不是别名，
// 所以路由侧的放行判据从「持有 trading_admin」变成「持有 trading_admin 或 quant_trade 之一」
// （strictAnyPermsMiddleware）。本文件锁三面：
//
//	① 只持 quant_trade 的成员：11 条"自己的实盘"路由不再被权限层 403，
//	   而勘误/通道切换/人工改判/结算/风险闸/advice/execute/运维读面**照旧 403**；
//	② 只持 trading_admin 的账号：行为与换中间件之前**逐条相等**（equals-legacy），
//	   也就是"给运营账号发新位"这件事没有悄悄扩大或缩小任何人的可达面；
//	③ 零权限位成员：11 条仍 403——放行是**按位**的，不是按角色的（role 不授予任何能力，§F-6）。
//
// English: quant_trade opens the self-scoped live surface as an ALTERNATIVE to trading_admin;
// the operator bit keeps exactly its legacy shape, and a bitless member is still denied.
package server

import (
	"net/http"
	"path/filepath"
	"testing"

	"quant-trading-v2/internal/auth"
	"quant-trading-v2/internal/store"
)

// openedRoute 一条「本批对 quant_trade 开放」的路由（method + path + 可选 body）。
type openedRoute struct {
	method string
	path   string
	body   string
}

// openedRoutes 批5 放行的 11 条：全部按**请求者**取账/动作（handler 内部 userIDFor(r) 过滤），
// 所以"能进"不等于"能看别人的"。顺序与 server.go 注册面一致，方便逐条对照行号。
var openedRoutes = []openedRoute{
	{http.MethodGet, "/api/config/qmt", ""},
	{http.MethodPost, "/api/config/qmt", `{}`},
	{http.MethodGet, "/api/t-live/rounds", ""},
	{http.MethodGet, "/api/funds/today", ""},
	{http.MethodGet, "/api/signals/decisions", ""},
	{http.MethodGet, "/api/positions/real", ""},
	{http.MethodGet, "/api/qmt/state", ""},
	{http.MethodGet, "/api/qmt/trades", ""},
	{http.MethodGet, "/api/qmt/orders", ""},
	{http.MethodPost, "/api/qmt/halt", `{"halted":false}`},
	{http.MethodPost, "/api/qmt/cancel/9001", ""},
}

// lockedRoutes 本批**刻意不放**的路由：语义都是"改判别人的账 / 动全局通道 / 跨账号"，
// 与 quant_trade 的"只管自己"冲突，必须继续只认 trading_admin（或 ops_admin）。
// 每条带期望码 403（权限层先于 handler，未装配引擎也照样先拒）。
var lockedRoutes = []openedRoute{
	{http.MethodGet, "/api/positions/advice", ""},
	{http.MethodPost, "/api/positions/execute", `{}`},
	{http.MethodPost, "/api/qmt/broker", `{}`},
	{http.MethodGet, "/api/qmt/pending-review", ""},
	{http.MethodPost, "/api/qmt/order-confirm", `{}`},
	{http.MethodGet, "/api/opslog", ""},
	{http.MethodGet, "/api/risk/gates", ""},
}

// newQuantTradePermServer 装配一台带实盘库与数据目录的测试服务，并建三个成员账号：
// friend（仅 quant_trade）、trader（仅 trading_admin，equals-legacy 对照组）、plain（零权限位）。
// 三个都是 role=user——本批的放行只认显式权限位，用非 admin 账号才测得出这一点。
func newQuantTradePermServer(t *testing.T) (*Server, *auth.User, *auth.User, *auth.User) {
	t.Helper()
	s, owner := newAdminTestServer(t)
	db, err := store.Open(filepath.Join(t.TempDir(), "live.db"))
	if err != nil {
		t.Fatalf("open live db: %v", err)
	}
	s.SetLiveDB(db)
	s.cacheDir = t.TempDir()
	// 运营账号（owner）本批不留 quant_trade：迁移不给任何人自动发新位（见 auth 侧同名用例）。
	if err := db.UpsertRealAccount(store.RealAccount{UserID: owner.ID, AvailableCash: 1000}); err != nil {
		t.Fatalf("seed owner cash: %v", err)
	}
	friend, err := s.auth.CreateUser("friend", "pw-friend", auth.RoleUser, []string{auth.PermQuantTrade}, 0)
	if err != nil {
		t.Fatalf("create friend: %v", err)
	}
	trader, err := s.auth.CreateUser("trader", "pw-trader", auth.RoleUser, []string{auth.PermTradingAdmin}, 0)
	if err != nil {
		t.Fatalf("create trader: %v", err)
	}
	plain, err := s.auth.CreateUser("plain", "pw-plain", auth.RoleUser, nil, 0)
	if err != nil {
		t.Fatalf("create plain: %v", err)
	}
	if !friend.HasExplicitPerm(auth.PermQuantTrade) || friend.HasExplicitPerm(auth.PermTradingAdmin) {
		t.Fatalf("前提被破坏：friend 应只持 quant_trade，实得 %v", friend.Perms)
	}
	return s, friend, trader, plain
}

// doAll 依次打一批路由，返回"路径 → HTTP 状态码"。
func doAll(t *testing.T, s *Server, u *auth.User, routes []openedRoute) map[string]int {
	t.Helper()
	out := make(map[string]int, len(routes))
	for _, rt := range routes {
		key := rt.method + " " + rt.path
		if _, dup := out[key]; dup {
			t.Fatalf("用例路由重复: %s", key)
		}
		out[key] = adminDo(s, adminReq(s, u, rt.method, rt.path, rt.body)).Code
	}
	return out
}

// Q1：只持 quant_trade 的成员能进 11 条"自己的实盘"路由（权限层不再 403）。
// 断言用"不是 403"而不是"一定是 200"：本用例只验权限层，handler 在没装配控制器/引擎的
// 测试服里会自行回 503/409（与 perm_surface_test.go I4 同口径）；紧随其后的 Q2 再逐条点名
// 哪些路由在这台裸服里确实能拿到 200。
func TestQuantTradeMemberOpensSelfLiveRoutes(t *testing.T) {
	s, friend, _, _ := newQuantTradePermServer(t)
	for _, rt := range openedRoutes {
		rr := adminDo(s, adminReq(s, friend, rt.method, rt.path, rt.body))
		if rr.Code == http.StatusForbidden {
			t.Fatalf("%s %s：持 quant_trade 却被权限层 403（body=%s）", rt.method, rt.path, rr.Body.String())
		}
		if rr.Code == http.StatusUnauthorized {
			t.Fatalf("%s %s：应过鉴权（403/401 混了就说明 token 注入坏了）", rt.method, rt.path)
		}
	}
}

// Q2：同一批路由在裸测试服里的**实际**状态码——能拿到 200 的逐条钉死 200，
// 拿不到 200 的（撤单：无控制器 503）钉死它当前的非 403 码，
// 这样"以后有人把权限层改回单位"会从 200/503 变成 403，用例必红而不是被 handler 噪声掩盖。
func TestQuantTradeRouteStatusCodes(t *testing.T) {
	s, friend, _, _ := newQuantTradePermServer(t)
	got := doAll(t, s, friend, openedRoutes)
	want := map[string]int{
		"GET /api/config/qmt":        http.StatusOK,
		"POST /api/config/qmt":       http.StatusOK,
		"GET /api/t-live/rounds":     http.StatusOK,
		"GET /api/funds/today":       http.StatusOK,
		"GET /api/signals/decisions": http.StatusOK,
		"GET /api/positions/real":    http.StatusOK,
		"GET /api/qmt/state":         http.StatusOK,
		"GET /api/qmt/trades":        http.StatusOK,
		"GET /api/qmt/orders":        http.StatusOK,
		"POST /api/qmt/halt":         http.StatusOK,
		"POST /api/qmt/cancel/9001":  http.StatusServiceUnavailable, // 未接实盘控制器
	}
	for k, w := range want {
		if got[k] != w {
			t.Fatalf("%s 期望 %d，实得 %d", k, w, got[k])
		}
	}
	if len(got) != len(openedRoutes) {
		t.Fatalf("路由条数应为 %d，实得 %d", len(openedRoutes), len(got))
	}
}

// Q3：quant_trade **不放行**勘误/通道切换/人工改判/结算/风险闸/execute/运维读面。
// 这一条是本批的边界：新位只扩"自己的账"，不扩任何改判与跨账号能力。
func TestQuantTradeMemberStillBlockedOnPrivilegedRoutes(t *testing.T) {
	s, friend, _, _ := newQuantTradePermServer(t)
	for _, rt := range lockedRoutes {
		rr := adminDo(s, adminReq(s, friend, rt.method, rt.path, rt.body))
		if rr.Code != http.StatusForbidden {
			t.Fatalf("%s %s：quant_trade 不该进特权面，应 403，实得 %d body=%s",
				rt.method, rt.path, rr.Code, rr.Body.String())
		}
	}
}

// Q4：equals-legacy——只持 trading_admin 的账号在**本批从 trading_admin 换成 any** 的 10 条上
// 状态码与 quant_trade 成员逐条相等（同 handler、同"按请求者取账"口径，只是权限判据换了写法）。
// GET /api/funds/today 单独判：它本批前挂的是 funds_admin，trading_admin 从来进不去，
// 本批换成「funds_admin 或 quant_trade」后 trading_admin **仍然** 403——这条的 equals-legacy
// 是"运营位没被顺发放宽"，而不是"和 quant_trade 同码"。
func TestTradingAdminCodesUnchangedOnOpenedRoutes(t *testing.T) {
	s, friend, trader, _ := newQuantTradePermServer(t)
	q := doAll(t, s, friend, openedRoutes)
	o := doAll(t, s, trader, openedRoutes)
	for _, rt := range openedRoutes {
		key := rt.method + " " + rt.path
		if key == "GET /api/funds/today" {
			if o[key] != http.StatusForbidden {
				t.Fatalf("%s：trading_admin 不该被本批顺带放行（legacy 是 funds_admin），实得 %d", key, o[key])
			}
			continue
		}
		if q[key] != o[key] {
			t.Fatalf("%s：trading_admin 应与此前一致并与 quant_trade 同码，实得 %d vs %d",
				key, o[key], q[key])
		}
	}
}

// Q5：equals-legacy 的另一半——trading_admin 在 7 条特权面上照旧可进（除 /api/opslog 属 ops_admin）。
// 漏了这条的话，"换成 any"写反成"换成 quant_trade only"也能让 Q1/Q3 全绿。
func TestTradingAdminStillHoldsPrivilegedRoutes(t *testing.T) {
	s, _, trader, _ := newQuantTradePermServer(t)
	for _, rt := range lockedRoutes {
		rr := adminDo(s, adminReq(s, trader, rt.method, rt.path, rt.body))
		if rt.path == "/api/opslog" {
			// 运维读面从来就不在 trading_admin 名下（属 ops_admin），本批也没动它。
			if rr.Code != http.StatusForbidden {
				t.Fatalf("GET /api/opslog 无 ops_admin 应 403，实得 %d", rr.Code)
			}
			continue
		}
		if rr.Code == http.StatusForbidden {
			t.Fatalf("%s %s：持 trading_admin 却被 403 —— 本批把路由换成 any 时写错位了（body=%s）",
				rt.method, rt.path, rr.Body.String())
		}
	}
}

// Q6：零权限位成员对 11 条"自己的实盘"路由全部 403——放行按位、不按角色。
func TestBitlessMemberBlockedOnOpenedRoutes(t *testing.T) {
	s, _, _, plain := newQuantTradePermServer(t)
	for _, rt := range openedRoutes {
		rr := adminDo(s, adminReq(s, plain, rt.method, rt.path, rt.body))
		if rr.Code != http.StatusForbidden {
			t.Fatalf("%s %s：零权限位应 403，实得 %d body=%s", rt.method, rt.path, rr.Code, rr.Body.String())
		}
	}
}
