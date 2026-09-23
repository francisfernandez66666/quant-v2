// fill_amendments_test.go — §FILL-AMEND（2026-09-23）勘误 HTTP 通道与守恒自检端点的回归。
//
// 锁三组机械化不变量：
//  1. 写端点必须鉴权：无 token / 坏 token 一律非 2xx（401），非 admin 成员 403（§M-14 收权口径）；
//  2. 提交即影子态：pending 时 /api/qmt/trades 的方向与盈亏数字不变，批准后才变，撤销后回原值；
//  3. 守恒自检是只读口：GET 得到逐笔差异线索，POST 打到它身上必须 405（"诊断口"不得被误接成
//     写路径），且跑完账本数据一字不变。
//
// English: HTTP regression for §FILL-AMEND — write endpoints demand auth (401/403), a pending
// amendment is a pure shadow on /api/qmt/trades until approved (and snaps back on revoke), and the
// conservation endpoint is read-only.
package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"quant-trading-v2/internal/auth"
	"quant-trading-v2/internal/store"
)

// newAmendHTTPServer 带实盘账本的 admin 路由服务（realDB 走 researchDB 兜底位，同 qmt 族端点口径）。
func newAmendHTTPServer(t *testing.T) (*Server, *store.DB, *auth.User) {
	t.Helper()
	s, admin := newAdminTestServer(t)
	db, err := store.Open(filepath.Join(t.TempDir(), "live.db"))
	if err != nil {
		t.Fatalf("open live db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	s.researchDB = db
	return s, db, admin
}

// seedWrongSideSell 复现 09-22 事故形态：真实卖出 900 股 @22.55 被按买入入账。返回 fills.id。
// uid 必须传调用方账号自己的 user.ID：/api/qmt/trades 与勘误端点都按 userIDFor(r)=u.ID 过滤归属，
// 夹具里写死字符串（如 "admin"）会让成交行既查不到也改不动——那是"夹具自证失败"，不是产品缺陷。
func seedWrongSideSell(t *testing.T, db *store.DB, uid string) int64 {
	t.Helper()
	f := store.RealFill{OrderID: "OHTTP", Code: "603468.SH", Name: "风语筑", Side: "买入",
		Price: 22.55, Qty: 900, Amount: 20295, TradedAt: "2026-09-22 10:08:00",
		SignalID: "sell:603468.SH:止盈:2026-09-22:r900", TradeID: "THTTP", UserID: uid, Fee: 5}
	if err := db.ApplyRealFill(f); err != nil {
		t.Fatalf("seed fill: %v", err)
	}
	// ApplyRealFill 不回填结构体 ID；勘误入口的定位锚是 fills.id，按券商成交编号回查。
	fs, err := db.RealFills()
	if err != nil || len(fs) != 1 {
		t.Fatalf("read back fills: %v n=%d", err, len(fs))
	}
	return fs[0].ID
}

// tradesResp 只声明本文件用到的字段（其余字段由 §F1/§F12 的用例覆盖）。
type tradesResp struct {
	Summary struct {
		RealizedPnl float64 `json:"realized_pnl"`
		Wins        int     `json:"wins"`
		Losses      int     `json:"losses"`
		TradeCount  int     `json:"trade_count"`
	} `json:"summary"`
	Fills []struct {
		ID       int64   `json:"id"`
		Side     string  `json:"side"`
		OrigSide string  `json:"orig_side"`
		Amended  bool    `json:"amended"`
		AmendID  int64   `json:"amend_id"`
		Amount   float64 `json:"amount"`
		Strategy string  `json:"strategy"`
		AmendKey string  `json:"amend_key"`
	} `json:"fills"`
	ByStrategy []struct {
		Strategy string  `json:"strategy"`
		Buys     float64 `json:"buys"`
		Sells    float64 `json:"sells"`
		Realized float64 `json:"realized_pnl"`
		Count    int     `json:"trade_count"`
	} `json:"by_strategy"`
}

// getTrades 走完整路由取 /api/qmt/trades（第 4 个消费口径的对外形态）。
func getTrades(t *testing.T, s *Server, admin *auth.User) tradesResp {
	t.Helper()
	rr := adminDo(s, adminReq(s, admin, http.MethodGet, "/api/qmt/trades", ""))
	if rr.Code != 200 {
		t.Fatalf("trades HTTP %d: %s", rr.Code, rr.Body.String())
	}
	var out tradesResp
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode trades: %v body=%s", err, rr.Body.String())
	}
	return out
}

// listAmendments 走完整路由取勘误台账。
func listAmendments(t *testing.T, s *Server, admin *auth.User) []store.FillAmendment {
	t.Helper()
	rr := adminDo(s, adminReq(s, admin, http.MethodGet, "/api/qmt/fill-amendments", ""))
	if rr.Code != 200 {
		t.Fatalf("list amendments HTTP %d: %s", rr.Code, rr.Body.String())
	}
	var out struct {
		Amendments []store.FillAmendment `json:"amendments"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode list: %v body=%s", err, rr.Body.String())
	}
	return out.Amendments
}

// anonReq 不带任何凭据的请求（走完整 mux，因此真的经过 authMiddleware）。
func anonReq(method, path, body string) *http.Request {
	return httptest.NewRequest(method, path, strings.NewReader(body))
}

// TestFillAmendmentEndpointsRequireAuth §M-14 写端点收权：未鉴权访问勘误/守恒端点必须非 2xx。
// 走完整 mux（而不是直调 handler）——只有路由注册处挂了闸，这条断言才有意义。
func TestFillAmendmentEndpointsRequireAuth(t *testing.T) {
	s, db, admin := newAmendHTTPServer(t)
	fillID := seedWrongSideSell(t, db, admin.ID)

	cases := []struct{ method, path, body string }{
		{http.MethodPost, "/api/qmt/fill-amendments", `{"fill_id":` + strconv.FormatInt(fillID, 10) + `,"new_side":"卖出","reason":"柜台回单"}`},
		{http.MethodPost, "/api/qmt/fill-amendments/1/apply", ``},
		{http.MethodPost, "/api/qmt/fill-amendments/1/revoke", ``},
		{http.MethodGet, "/api/qmt/fill-amendments", ``},
		{http.MethodGet, "/api/qmt/fills/conservation?day=2026-09-22", ``},
	}
	for _, c := range cases {
		rr := adminDo(s, anonReq(c.method, c.path, c.body))
		if rr.Code < 400 {
			t.Fatalf("未鉴权访问 %s %s 必须非 2xx, got %d body=%s", c.method, c.path, rr.Code, rr.Body.String())
		}
	}

	// 坏 token 同样拒（authMiddleware 的 ValidateToken 分支）
	req := anonReq(http.MethodPost, "/api/qmt/fill-amendments", `{}`)
	req.Header.Set("Authorization", "Bearer not-a-real-token")
	if rr := adminDo(s, req); rr.Code < 400 {
		t.Fatalf("坏 token 访问勘误写端点必须非 2xx, got %d body=%s", rr.Code, rr.Body.String())
	}

	// 已鉴权的普通成员（非 admin）→ 403（实盘账本族 admin-only，§GAP1.8/1.10 口径）
	member, err := s.auth.CreateUser("amendmember", "pw", auth.RoleUser, nil, 0)
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	if rr := adminDo(s, adminReq(s, member, http.MethodPost, "/api/qmt/fill-amendments",
		`{"fill_id":`+strconv.FormatInt(fillID, 10)+`,"new_side":"卖出","reason":"成员不该能改判"}`)); rr.Code != http.StatusForbidden {
		t.Fatalf("成员提交勘误应 403, got %d body=%s", rr.Code, rr.Body.String())
	}
	// 成员读守恒自检同样 403（诊断口暴露的是全账户账本差异）
	if rr := adminDo(s, adminReq(s, member, http.MethodGet, "/api/qmt/fills/conservation", "")); rr.Code != http.StatusForbidden {
		t.Fatalf("成员读守恒自检应 403, got %d", rr.Code)
	}
	// 成员也不能读勘误台账
	if rr := adminDo(s, adminReq(s, member, http.MethodGet, "/api/qmt/fill-amendments", "")); rr.Code != http.StatusForbidden {
		t.Fatalf("成员读勘误台账应 403, got %d", rr.Code)
	}
}

// TestFillAmendmentShadowThenApplyThenRevoke 端到端：pending 不动 trades 数字 → 批准后方向/盈亏/
// 战法归因跟着变 → 撤销后回原值；并锁住"原始 fills 行一字未动"。
func TestFillAmendmentShadowThenApplyThenRevoke(t *testing.T) {
	s, db, admin := newAmendHTTPServer(t)
	fillID := seedWrongSideSell(t, db, admin.ID)

	before := getTrades(t, s, admin)
	if len(before.Fills) != 1 || before.Fills[0].Side != "买入" || before.Fills[0].Amended {
		t.Fatalf("夹具应为 1 笔未勘误的买入: %+v", before.Fills)
	}
	if before.Summary.Losses != 0 || before.Summary.RealizedPnl != 0 {
		t.Fatalf("改判前不应有已实现盈亏（成交簿里没有卖出腿）: %+v", before.Summary)
	}

	body := `{"fill_id":` + strconv.FormatInt(fillID, 10) +
		`,"new_side":"卖出","reason":"柜台回单 2026-09-22 10:08 实为卖出，方向被柜台枚举猜测污染"}`
	rr := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/qmt/fill-amendments", body))
	if rr.Code != http.StatusCreated {
		t.Fatalf("提交勘误应 201, got %d body=%s", rr.Code, rr.Body.String())
	}
	// ok 是字符串 "1"：qmt 族端点的既有响应口径（见 internal/server/qmt.go:885 等），
	// 声明成 bool 会在解码期就报 "cannot unmarshal string into Go value of type bool"。
	var created struct {
		OK        string              `json:"ok"`
		Amendment store.FillAmendment `json:"amendment"`
		Note      string              `json:"note"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if created.OK != "1" {
		t.Fatalf("提交勘误的 ok 标记应为 \"1\", got %q body=%s", created.OK, rr.Body.String())
	}
	if created.Amendment.Status != store.FillAmendPending {
		t.Fatalf("提交后必须是 pending 影子态: %+v", created.Amendment)
	}
	if created.Amendment.OrigSide != "买入" || created.Amendment.NewSide != "卖出" {
		t.Fatalf("方向对错位: %+v", created.Amendment)
	}
	if created.Amendment.OrigAmount != 20295 {
		t.Fatalf("金额快照应随勘误行留档: %+v", created.Amendment)
	}

	// 影子态：/api/qmt/trades 一字不变
	shadow := getTrades(t, s, admin)
	if shadow.Fills[0].Side != "买入" || shadow.Fills[0].Amended {
		t.Fatalf("pending 勘误改变了 trades 方向: %+v", shadow.Fills[0])
	}
	if shadow.Summary.RealizedPnl != before.Summary.RealizedPnl || shadow.Summary.Losses != before.Summary.Losses {
		t.Fatalf("pending 勘误改变了盈亏数字: before=%+v shadow=%+v", before.Summary, shadow.Summary)
	}

	amendPath := "/api/qmt/fill-amendments/" + strconv.FormatInt(created.Amendment.ID, 10) + "/apply"
	if rr = adminDo(s, adminReq(s, admin, http.MethodPost, amendPath, ``)); rr.Code != 200 {
		t.Fatalf("批准应 200, got %d body=%s", rr.Code, rr.Body.String())
	}

	after := getTrades(t, s, admin)
	if after.Fills[0].Side != "卖出" || !after.Fills[0].Amended || after.Fills[0].OrigSide != "买入" {
		t.Fatalf("批准后 trades 应回显生效方向与原始方向: %+v", after.Fills[0])
	}
	if after.Fills[0].Amount != 20295 {
		t.Fatalf("勘误只改方向，金额必须原样: %+v", after.Fills[0])
	}
	// 第 4 个消费口径：按战法归因的买卖额从 buys 挪到 sells
	if len(after.ByStrategy) != 1 || after.ByStrategy[0].Sells != 20295 || after.ByStrategy[0].Buys != 0 {
		t.Fatalf("战法归因买卖额未跟着改判: %+v", after.ByStrategy)
	}
	if after.Summary.Losses != 1 || after.Summary.Wins != 0 {
		t.Fatalf("卖出腿成立后胜负计数应变化: %+v", after.Summary)
	}

	// 原始行一字未动（设计前提）：RawFillForUser 读的是 fills 本体，不走视图。
	// 归属参数同样必须是 user.ID——夹具与产品口径不一致时这里会假红。
	raw, err := db.RawFillForUser(admin.ID, fillID)
	if err != nil {
		t.Fatalf("read raw fill: %v", err)
	}
	if raw.Side != "买入" || raw.Amount != 20295 {
		t.Fatalf("原始 fills 行被改写：side=%s amount=%.2f", raw.Side, raw.Amount)
	}

	// 撤销 → 全部回原值
	revokePath := "/api/qmt/fill-amendments/" + strconv.FormatInt(created.Amendment.ID, 10) + "/revoke"
	if rr = adminDo(s, adminReq(s, admin, http.MethodPost, revokePath, ``)); rr.Code != 200 {
		t.Fatalf("撤销应 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	back := getTrades(t, s, admin)
	if back.Fills[0].Side != "买入" || back.Fills[0].Amended {
		t.Fatalf("撤销后应回到原始方向: %+v", back.Fills[0])
	}
	if back.Summary.Losses != 0 || len(back.ByStrategy) != 1 || back.ByStrategy[0].Buys != 20295 {
		t.Fatalf("撤销后盈亏/归因未回原值: %+v %+v", back.Summary, back.ByStrategy)
	}
	// 撤销是终态：再批准必须被拒（409）
	if rr = adminDo(s, adminReq(s, admin, http.MethodPost, amendPath, ``)); rr.Code != http.StatusConflict {
		t.Fatalf("撤销后再批准应 409, got %d body=%s", rr.Code, rr.Body.String())
	}
}

// TestFillAmendmentValidationAndConservation 入参护栏（缺理由 400 且不落库）+ 守恒自检端点输出。
func TestFillAmendmentValidationAndConservation(t *testing.T) {
	s, db, admin := newAmendHTTPServer(t)
	fillID := seedWrongSideSell(t, db, admin.ID)

	// 缺理由 → 400，且不落库
	rr := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/qmt/fill-amendments",
		`{"fill_id":`+strconv.FormatInt(fillID, 10)+`,"new_side":"卖出"}`))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("缺理由应 400, got %d body=%s", rr.Code, rr.Body.String())
	}
	if list := listAmendments(t, s, admin); len(list) != 0 {
		t.Fatalf("校验失败的提交不得落库: %+v", list)
	}
	// 同向"勘误" → 400
	if rr := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/qmt/fill-amendments",
		`{"fill_id":`+strconv.FormatInt(fillID, 10)+`,"new_side":"买入","reason":"同向空操作"}`)); rr.Code != http.StatusBadRequest {
		t.Fatalf("与原始方向相同应 400, got %d body=%s", rr.Code, rr.Body.String())
	}
	// 不存在的成交 → 404
	if rr := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/qmt/fill-amendments",
		`{"fill_id":987654,"new_side":"卖出","reason":"越界定位"}`)); rr.Code != http.StatusNotFound {
		t.Fatalf("不存在的成交应 404, got %d body=%s", rr.Code, rr.Body.String())
	}
	// 非法 day → 400
	if rr := adminDo(s, adminReq(s, admin, http.MethodGet, "/api/qmt/fills/conservation?day=20260922", "")); rr.Code != http.StatusBadRequest {
		t.Fatalf("非法 day 应 400, got %d", rr.Code)
	}

	// 正常提交 + 批准 → 守恒自检必须报出持仓差额
	// （勘误按设计不回改历史写账，持仓账仍挂着那 900 股——这正是自检要暴露的残留位移）
	if rr := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/qmt/fill-amendments",
		`{"fill_id":`+strconv.FormatInt(fillID, 10)+`,"new_side":"卖出","reason":"柜台回单确认卖出"}`)); rr.Code != http.StatusCreated {
		t.Fatalf("提交应 201, got %d body=%s", rr.Code, rr.Body.String())
	}
	list := listAmendments(t, s, admin)
	if len(list) != 1 || list[0].Status != store.FillAmendPending {
		t.Fatalf("台账应含 1 条 pending: %+v", list)
	}
	if list[0].AmendKey == "" {
		t.Fatalf("台账必须自带匹配键（前端靠它把影子态挂到成交行）: %+v", list[0])
	}
	if rr := adminDo(s, adminReq(s, admin, http.MethodPost,
		"/api/qmt/fill-amendments/"+strconv.FormatInt(list[0].ID, 10)+"/apply", ``)); rr.Code != 200 {
		t.Fatalf("批准应 200, got %d body=%s", rr.Code, rr.Body.String())
	}

	rr = adminDo(s, adminReq(s, admin, http.MethodGet, "/api/qmt/fills/conservation?day=2026-09-22", ""))
	if rr.Code != 200 {
		t.Fatalf("守恒自检应 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	// 同上：ok 按仓库口径是字符串 "1"（报告体内部的 ok 才是真 bool）。
	var out struct {
		OK     string                   `json:"ok"`
		Report store.ConservationReport `json:"report"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode conservation: %v body=%s", err, rr.Body.String())
	}
	if out.OK != "1" {
		t.Fatalf("守恒自检 ok 标记应为 \"1\", got %q", out.OK)
	}
	if out.Report.PositionsOK || len(out.Report.PositionLines) != 1 {
		t.Fatalf("勘误生效后自检必须报出持仓差额: %+v", out.Report)
	}
	line := out.Report.PositionLines[0]
	if line.Code != "603468.SH" || line.ReplayedQty != 0 || line.BookQty != 900 || line.Diff != 900 || line.Note == "" {
		t.Fatalf("差额线索不完整: %+v", line)
	}
	if out.Report.AppliedAmendments != 1 {
		t.Fatalf("报告应带生效勘误数: %+v", out.Report)
	}
	if out.Report.Cash.Checked {
		t.Fatalf("夹具未配券商现金回报，现金条应标未检查: %+v", out.Report.Cash)
	}

	// 只读口拒绝写方法（诊断口不得被误接成写路径）
	if rr := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/qmt/fills/conservation", ``)); rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("守恒自检端点 POST 应 405, got %d", rr.Code)
	}
	// 自检不改账：跑完仍是 1 笔成交、1 条勘误
	if fs, err := db.RealFills(); err != nil || len(fs) != 1 {
		t.Fatalf("自检改动了成交簿: %v n=%d", err, len(fs))
	}
	if got := listAmendments(t, s, admin); len(got) != 1 {
		t.Fatalf("自检改动了勘误台账: %+v", got)
	}
}

// TestFillAmendmentScopedToOwner 越权面：另一账号的管理员不能凭 fills.id 改到别人的成交。
// 404 而非 403：不向非归属账号泄漏"这行存在与否"。
func TestFillAmendmentScopedToOwner(t *testing.T) {
	s, db, admin := newAmendHTTPServer(t)
	fillID := seedWrongSideSell(t, db, admin.ID) // UserID=admin

	other, err := s.auth.CreateUser("otheradmin", "pw", auth.RoleAdmin, nil, 0)
	if err != nil {
		t.Fatalf("create other admin: %v", err)
	}
	// 伪造上下文成另一账号（adminMiddleware 的产物形态），验证处理器内的归属判定
	req := anonReq(http.MethodPost, "/api/qmt/fill-amendments",
		`{"fill_id":`+strconv.FormatInt(fillID, 10)+`,"new_side":"卖出","reason":"跨账号尝试"}`)
	req.Header.Set("Authorization", "Bearer "+other.Token)
	ctxUser := s.auth.UserByID(other.ID)
	if ctxUser == nil {
		t.Fatal("另一管理员读不出来")
	}
	rr := adminDo(s, req.WithContext(context.WithValue(req.Context(), ctxUserKey{}, ctxUser)))
	if rr.Code == http.StatusCreated || rr.Code == http.StatusOK {
		t.Fatalf("跨账号提交勘误不得成功, got %d body=%s", rr.Code, rr.Body.String())
	}
	if rr.Code != http.StatusUnauthorized && rr.Code != http.StatusNotFound &&
		rr.Code != http.StatusForbidden {
		t.Fatalf("跨账号提交应回 401/403/404, got %d body=%s", rr.Code, rr.Body.String())
	}
	if len(listAmendments(t, s, admin)) != 0 {
		t.Fatal("被拒的跨账号提交不应落库")
	}
}
