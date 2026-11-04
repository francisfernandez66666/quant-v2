// handlers_qmt_receipt_test.go — §4.2 人工回执 HTTP 面的行为锁（2026-11-04，外脑化 B1 第二腿）。
//
// 六条判据逐条对应 docs/IMPL_QMT_INTERNAL_LOOP_20261103.md §4.2/§4.3/§7④：
//
//  1. 档位：账号级 authMiddleware，普通成员（零权限位）可回执；未登录 401；空身份 403。
//     并静态钉注册行不许被"顺手严格化"（挂任何严格位＝桥断后朋友自己收不了账）。
//  2. 归属：别人的 signal_id 一律 403 且他人行一字不改；遗留全局行只有运营账号可回执。
//  3. 幂等：同键第二次回执 409，成交行/持仓不重复落账（网关 §CLAIMRELEASE「待核对」取向）。
//  4. 金额红线（本文件主题）：回执前后账户资产逐值不变（行为锁），
//     且 handler 函数体内不得出现任何账户资产写标识符（静态负锁，带"验牙"自证）。
//  5. 审计：每笔被接受的回执落 opslog.Audit(qmt_order_receipt) 一行，带 before→after。
//  6. 可辨识：回执过的委托行带 manual_receipt 来源标记、成交行带合成券商号前缀——
//     全部复用既有列（不加列、不动 schemaVersion）。
//
// 另外三条"如实"面：未知/查无 signal_id 404（不许静默 200）、body 拼错 400、
// 状态冲突 409 且零写入。
package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/auth"
	"quant-trading-v2/internal/opslog"
	"quant-trading-v2/internal/store"
)

// receiptPost 以指定用户身份 POST /api/qmt/order-receipt（走完整 mux，路由中间件真的会跑）。
func receiptPost(s *Server, u *auth.User, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/qmt/order-receipt", strings.NewReader(body))
	if u != nil {
		req.Header.Set("Authorization", "Bearer "+u.Token)
		ctx := context.WithValue(req.Context(), ctxUserKey{}, s.auth.UserByID(u.ID))
		req = req.WithContext(ctx)
	}
	rr := httptest.NewRecorder()
	s.mux.ServeHTTP(rr, req)
	return rr
}

// seedReceiptOrder 在实盘账里放一条在途委托行（回执的靶子），返回落库后的行。
func seedReceiptOrder(t *testing.T, db *store.DB, uid, sid, status string) store.RealOrder {
	t.Helper()
	o := store.RealOrder{
		OrderID: "pend:RCPT-" + sid, SignalID: sid, Code: "600000.SH", Side: "买入",
		Status: status, Price: 10, Qty: 1000,
		CreatedAt: time.Now().Format("2006-01-02 15:04:05"), UserID: uid,
	}
	if _, err := db.UpsertRealOrder(o); err != nil {
		t.Fatalf("seed order: %v", err)
	}
	return o
}

// findOrderRow 按 signal_id 在本账号账里取回委托行（读标记用）。
func findOrderRow(t *testing.T, db *store.DB, uid, sid string) (store.RealOrder, bool) {
	t.Helper()
	rows, err := db.RealOrdersForUser(uid)
	if err != nil {
		t.Fatalf("read orders: %v", err)
	}
	for _, o := range rows {
		if o.SignalID == sid && o.UserID == uid {
			return o, true
		}
	}
	return store.RealOrder{}, false
}

// countManualFills 数成交行里合成回执号的条数（幂等红线的直接读数：>1 就是重复落账）。
func countManualFills(t *testing.T, db *store.DB, sid string) int {
	t.Helper()
	fills, err := db.RealFills()
	if err != nil {
		t.Fatalf("read fills: %v", err)
	}
	n := 0
	key := manualReceiptFillKey(sid)
	for _, f := range fills {
		if f.TradeID == key {
			n++
		}
	}
	return n
}

// TestOrderReceiptMemberLevelRoute 判据 1：档位与身份。
// 零权限位成员可回执；未登录裸请求 401（不落到运营账号）；空身份直调 handler 403。
func TestOrderReceiptMemberLevelRoute(t *testing.T) {
	s, member, db := newManualTestServer(t)
	sid := "buy:600000:rcpt:20261104"
	seedReceiptOrder(t, db, member.ID, sid, "已报")
	rr := receiptPost(s, member, `{"signal_id":"`+sid+`","status":"filled","qty":600,"price":10.2}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("普通成员应能回执自己的单（§4.2 不落严格位），got %d body=%s", rr.Code, rr.Body.String())
	}
	// 未登录走真实中间件：401。
	anon := httptest.NewRequest(http.MethodPost, "/api/qmt/order-receipt", strings.NewReader(`{"signal_id":"x","status":"filled"}`))
	anonRR := httptest.NewRecorder()
	s.mux.ServeHTTP(anonRR, anon)
	if anonRR.Code != http.StatusUnauthorized {
		t.Fatalf("未登录应 401，got %d", anonRR.Code)
	}
	// 空身份（中间件被摘的假想态）：handler 自己必须 403 而不是落到运营账号。
	direct := httptest.NewRequest(http.MethodPost, "/api/qmt/order-receipt", strings.NewReader(`{"signal_id":"`+sid+`","status":"filled","qty":100,"price":10}`))
	drr := httptest.NewRecorder()
	s.handleQMTOrderReceipt(drr, direct)
	if drr.Code != http.StatusForbidden {
		t.Fatalf("空身份应 403，got %d", drr.Code)
	}
}

// TestOrderReceiptRouteNotStrictlyMounted 判据 1 的静态腿：注册行必须挂账号级 authMiddleware，
// 不许出现任何 strictPerm 包装（§7④「不挂严格位负锁」的 Go 侧等价物；挂上=朋友收不了自己的账）。
func TestOrderReceiptRouteNotStrictlyMounted(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("server.go"))
	if err != nil {
		t.Fatalf("读 server.go 失败: %v", err)
	}
	var line string
	for _, l := range strings.Split(string(src), "\n") {
		if strings.Contains(l, `"POST /api/qmt/order-receipt"`) && strings.Contains(l, "HandleFunc") {
			line = l
			break
		}
	}
	if line == "" {
		t.Fatalf("路由 POST /api/qmt/order-receipt 没在 server.go 注册（回执腿断链）")
	}
	if !strings.Contains(line, "s.authMiddleware(") {
		t.Fatalf("注册行必须挂账号级 authMiddleware：%q", line)
	}
	if strings.Contains(line, "strictPerm") || strings.Contains(line, "strictAllPerms") || strings.Contains(line, "strictAnyPerms") {
		t.Fatalf("回执路由被挂上严格位了（§4.2 刻意不落严格位）：%q", line)
	}
}

// TestOrderReceiptScopedToRequester 判据 2：越权 403 且他人行一字不改；查无 404 且不补插。
func TestOrderReceiptScopedToRequester(t *testing.T) {
	s, member, db := newManualTestServer(t)
	other, err := s.auth.CreateUser("member2", "pw", auth.RoleUser, nil, 0)
	if err != nil {
		t.Fatalf("create member2: %v", err)
	}
	sid := "buy:600000:other:20261104"
	seedReceiptOrder(t, db, other.ID, sid, "已报")

	rr := receiptPost(s, member, `{"signal_id":"`+sid+`","status":"filled","qty":100,"price":10}`)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("回执别人的单应 403，got %d body=%s", rr.Code, rr.Body.String())
	}
	if row, ok := findOrderRow(t, db, other.ID, sid); !ok || row.Status != "已报" {
		t.Fatalf("越权回执改动了他人行：%+v ok=%v", row, ok)
	}
	if n := countManualFills(t, db, sid); n != 0 {
		t.Fatalf("越权回执落成了成交行：%d 条", n)
	}

	// 查无此单：404，且**不许**顺手补插一条新委托（回执不是下单）。
	ghost := "buy:600000:none:20261104"
	rr2 := receiptPost(s, member, `{"signal_id":"`+ghost+`","status":"cancelled"}`)
	if rr2.Code != http.StatusNotFound {
		t.Fatalf("查无应如实 404（不许静默 200），got %d body=%s", rr2.Code, rr2.Body.String())
	}
	if _, ok := findOrderRow(t, db, member.ID, ghost); ok {
		t.Fatalf("404 之后账里多出了幽灵行")
	}
}

// TestOrderReceiptLegacyGlobalRowNeedsOperator 判据 2 的边界腿：遗留全局行（user_id=”）
// 按 §FIX-20261028-F01 归运营账号——成员回执它算越权 403，运营自己可以。
func TestOrderReceiptLegacyGlobalRowNeedsOperator(t *testing.T) {
	s, member, db := newManualTestServer(t)
	sid := "buy:600000:legacy:20261104"
	if _, err := db.UpsertRealOrder(store.RealOrder{
		OrderID: "pend:RCPT-L", SignalID: sid, Code: "600000.SH", Side: "买入",
		Status: "已报", Price: 10, Qty: 100, CreatedAt: "2026-11-04 09:30:00", UserID: "",
	}); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}
	if rr := receiptPost(s, member, `{"signal_id":"`+sid+`","status":"filled","qty":100,"price":10}`); rr.Code != http.StatusForbidden {
		t.Fatalf("成员回执遗留全局行应 403，got %d body=%s", rr.Code, rr.Body.String())
	}
	// 运营账号直接调 handler（本断言的对象是归属判定，不是中间件；token 是哈希态取不到原文）。
	opReq := httptest.NewRequest(http.MethodPost, "/api/qmt/order-receipt", strings.NewReader(`{"signal_id":"`+sid+`","status":"filled","qty":100,"price":10}`))
	opCtx := context.WithValue(opReq.Context(), ctxUserKey{}, s.auth.UserByID(s.operatorID()))
	orr := httptest.NewRecorder()
	s.handleQMTOrderReceipt(orr, opReq.WithContext(opCtx))
	if orr.Code != http.StatusOK {
		t.Fatalf("运营账号回执遗留全局行应 200，got %d body=%s", orr.Code, orr.Body.String())
	}
}

// TestOrderReceiptIdempotentSameSignal 判据 3：同 signal_id 第二次回执 409，
// 成交行/持仓/资金都不再落第二次；重放里的"差异字段"只允许单调状态补进。
func TestOrderReceiptIdempotentSameSignal(t *testing.T) {
	s, member, db := newManualTestServer(t)
	sid := "buy:600000:idem:20261104"
	seedReceiptOrder(t, db, member.ID, sid, "已报")
	body := `{"signal_id":"` + sid + `","status":"filled","qty":600,"price":10.2,"note":"券商App成交"}`
	if rr := receiptPost(s, member, body); rr.Code != http.StatusOK {
		t.Fatalf("首次回执应 200，got %d body=%s", rr.Code, rr.Body.String())
	}
	// 第二次：逐字相同 → 409，成交行仍只有 1 条、持仓仍只有 600 股。
	rr2 := receiptPost(s, member, body)
	if rr2.Code != http.StatusConflict {
		t.Fatalf("同键重放应 409（待核对取向），got %d body=%s", rr2.Code, rr2.Body.String())
	}
	if n := countManualFills(t, db, sid); n != 1 {
		t.Fatalf("重放重复落了成交行：%d 条", n)
	}
	// 持仓走柜台那条腿：ApplyRealFill 落 real_positions——数量必须仍是单笔。
	if ps, err := db.RealPositionsForUser(member.ID); err != nil || len(ps) != 1 || ps[0].Qty != 600 {
		t.Fatalf("重放改动了持仓量：ps=%+v err=%v", ps, err)
	}
	// 状态不许被重放回退：已成 行收到 partial 重放 → 409 且状态仍是 已成。
	rr3 := receiptPost(s, member, `{"signal_id":"`+sid+`","status":"partial","qty":100,"price":10.1}`)
	if rr3.Code != http.StatusConflict {
		t.Fatalf("已回执过的单再发 partial 应 409，got %d", rr3.Code)
	}
	if row, _ := findOrderRow(t, db, member.ID, sid); row.Status != "已成" {
		t.Fatalf("重放把状态改坏了：%+v", row)
	}
	if n := countManualFills(t, db, sid); n != 1 {
		t.Fatalf("partial 重放又落了成交行：%d 条", n)
	}
}

// TestOrderReceiptReplayAdvancesStatusOnly 判据 3 的"只更新差异字段"腿：
// partial 首次回执后 filled 重放 ⇒ 409，状态按秩守卫补进成 已成，但成交行**不补第二条**
// （数量纠错走待核对勘误通道，回执面不制造双记）。
func TestOrderReceiptReplayAdvancesStatusOnly(t *testing.T) {
	s, member, db := newManualTestServer(t)
	sid := "buy:600000:advance:20261104"
	seedReceiptOrder(t, db, member.ID, sid, "已报")
	if rr := receiptPost(s, member, `{"signal_id":"`+sid+`","status":"partial","qty":300,"price":10}`); rr.Code != http.StatusOK {
		t.Fatalf("partial 首次应 200，got %d body=%s", rr.Code, rr.Body.String())
	}
	rr := receiptPost(s, member, `{"signal_id":"`+sid+`","status":"filled","qty":700,"price":10.1}`)
	if rr.Code != http.StatusConflict {
		t.Fatalf("filled 重放应 409，got %d body=%s", rr.Code, rr.Body.String())
	}
	if row, _ := findOrderRow(t, db, member.ID, sid); row.Status != "已成" {
		t.Fatalf("重放里唯一允许动的状态字段没补进：%+v", row)
	}
	if n := countManualFills(t, db, sid); n != 1 {
		t.Fatalf("重放补进了第二条成交行：%d 条", n)
	}
}

// TestOrderReceiptNeverMovesCash 判据 4（金额红线·行为腿）：回执推进成交后，
// 账户资产行（总资产/可用资金/冻结）逐值不变——那是柜台/手动账的真值域，回执碰不得。
func TestOrderReceiptNeverMovesCash(t *testing.T) {
	s, member, db := newManualTestServer(t)
	acc := store.RealAccount{UserID: member.ID, AvailableCash: 50000, FrozenCash: 3000, TotalAsset: 80000, MarketValue: 27000}
	if err := db.UpsertRealAccount(acc); err != nil {
		t.Fatalf("seed real_account: %v", err)
	}
	sid := "buy:600000:cash:20261104"
	seedReceiptOrder(t, db, member.ID, sid, "已报")
	for _, body := range []string{
		`{"signal_id":"` + sid + `","status":"filled","qty":500,"price":10.5,"note":"全额成交"}`,
		`{"signal_id":"` + sid + `","status":"cancelled"}`, // 重放路径也要过一遍红线
	} {
		rr := receiptPost(s, member, body)
		if rr.Code != http.StatusOK && rr.Code != http.StatusConflict {
			t.Fatalf("回执不该 5xx/4xx（除 409 重放），got %d body=%s", rr.Code, rr.Body.String())
		}
	}
	got, err := db.GetRealAccountOwn(member.ID)
	if err != nil {
		t.Fatalf("read real_account: %v", err)
	}
	if got.AvailableCash != 50000 || got.TotalAsset != 80000 || got.FrozenCash != 3000 || got.MarketValue != 27000 {
		t.Fatalf("人工回执动了账户资金（金额红线）：%+v", got)
	}
}

// TestOrderReceiptHandlerHasNoAssetWrites 判据 4（金额红线·静态负锁腿）：
// handler 函数体内不得出现任何账户资产写标识符——§8 R4 说"顺手更新可用资金"必须让锁变红，
// 红的方式就红在这里：注入 `UpsertRealAccount(...)` 或引用资产字段名，本用例当场失败。
//
// 三条防"摆设锁"自证（11-02 晚批 §165 的教训：永不匹配的负锁比没有更危险）：
//   - 提取必须成功且非空（函数名漂了＝整把锁失明）；
//   - 提取出的函数体必须真的含已知锚串 AdvanceRealOrderStatus（证明取到的是正文不是空气）；
//   - 用一份注入过的假函数体走同一个判据函数，必须判红（证明尺子会咬）。
func TestOrderReceiptHandlerHasNoAssetWrites(t *testing.T) {
	src, err := os.ReadFile("handlers_qmt_receipt.go")
	if err != nil {
		t.Fatalf("读 handler 源文件失败: %v", err)
	}
	body := extractFuncBody(t, string(src), "func (s *Server) handleQMTOrderReceipt(")
	if !strings.Contains(body, "AdvanceRealOrderStatus") {
		t.Fatalf("函数体提取失败（锚串不在位＝这把负锁是摆设）")
	}
	if v := receiptAssetWriteViolations(body); len(v) != 0 {
		t.Fatalf("回执 handler 函数体出现账户资产写标识符（金额红线）：%v", v)
	}
	// 验牙：把 §8 R4 的那次"好心注入"原样喂给同一把尺子，必须抓到。
	trap := body + "\n\t_ = db.UpsertRealAccount(store.RealAccount{AvailableCash: 1, TotalAsset: 2})\n"
	if v := receiptAssetWriteViolations(trap); len(v) == 0 {
		t.Fatalf("负锁不会咬：注入「顺手更新资金」后仍判绿——这把锁是摆设")
	}
}

// receiptAssetWriteViolations 金额红线静态判据的单一实现（正向断言与验牙共用同一把尺子）。
// 只扫标识符不扫中文：注释里合法出现「可用资金」三个字（说明红线在哪），
// 按中文判会把自家说明文本当雷（10-29 §140 缩进坑的同族错误）。
func receiptAssetWriteViolations(body string) []string {
	forbidden := []string{"UpsertRealAccount", "PutManualAccount", "AvailableCash", "TotalAsset", "FrozenCash", "real_account"}
	var hits []string
	for _, tok := range forbidden {
		if strings.Contains(body, tok) {
			hits = append(hits, tok)
		}
	}
	return hits
}

// extractFuncBody 从 from 标记处取到下一个行首 "}"（Go 顶层函数收尾），取不到直接失败。
func extractFuncBody(t *testing.T, src, from string) string {
	t.Helper()
	i := strings.Index(src, from)
	if i < 0 {
		t.Fatalf("找不到函数起点 %q（函数改名了而锁没跟着改＝锁已失明）", from)
	}
	rest := src[i:]
	j := strings.Index(rest, "\n}\n")
	if j < 0 {
		t.Fatalf("函数体收尾取不到 %q", from)
	}
	return rest[:j]
}

// TestOrderReceiptAudited 判据 5：每笔被接受的回执在 opslog 审计文件落一行
// （event/actor/target 齐、result 带 before→after——事后能回答"谁把哪笔单改成了什么"）。
func TestOrderReceiptAudited(t *testing.T) {
	s, member, db := newManualTestServer(t)
	logDir := filepath.Join(t.TempDir(), "opslog")
	opslog.Init(logDir, 0)
	sid := "buy:600000:audit:20261104"
	seedReceiptOrder(t, db, member.ID, sid, "已报")
	rr := receiptPost(s, member, `{"signal_id":"`+sid+`","status":"cancelled","note":"券商App撤单"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("应 200，got %d body=%s", rr.Code, rr.Body.String())
	}
	auditPath := filepath.Join(logDir, "audit-"+time.Now().Format("20060102")+".log")
	data, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatalf("回执没有落审计文件: %v", err)
	}
	line := string(data)
	if !strings.Contains(line, "event=qmt_order_receipt") || !strings.Contains(line, "actor="+member.ID) || !strings.Contains(line, "target="+sid) {
		t.Fatalf("审计行四字段不齐：%s", line)
	}
	if !strings.Contains(line, "before=已报 after=已撤") {
		t.Fatalf("审计行没带 before→after 迁移：%s", line)
	}
}

// TestOrderReceiptRowsAreMarkedManual 判据 6：回执过的行**在既有列上**可与柜台回报区分——
// 委托行来源标记 + 成交行合成券商号；没有新列、没有 schemaVersion 变动。
func TestOrderReceiptRowsAreMarkedManual(t *testing.T) {
	s, member, db := newManualTestServer(t)
	sid := "buy:600000:mark:20261104"
	seedReceiptOrder(t, db, member.ID, sid, "已报")
	if rr := receiptPost(s, member, `{"signal_id":"`+sid+`","status":"filled","qty":200,"price":9.9}`); rr.Code != http.StatusOK {
		t.Fatalf("应 200，got %d body=%s", rr.Code, rr.Body.String())
	}
	row, ok := findOrderRow(t, db, member.ID, sid)
	if !ok {
		t.Fatalf("回执后委托行丢了")
	}
	if row.CancelSource != manualReceiptSource {
		t.Fatalf("委托行必须可辨识为人工回执（cancel_source=%s），got %q", manualReceiptSource, row.CancelSource)
	}
	fills, _ := db.RealFills()
	found := false
	for _, f := range fills {
		if f.TradeID == manualReceiptFillKey(sid) && f.UserID == member.ID && f.Qty == 200 {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("成交行没带合成回执号（与柜台成交区分不开）：%+v", fills)
	}
}

// TestOrderReceiptValidation 校验面：拼错键名/坏 JSON/缺 signal_id/未知 status/
// 成交缺量价 全部 400，且一条数据都不落。
func TestOrderReceiptValidation(t *testing.T) {
	s, member, db := newManualTestServer(t)
	sid := "buy:600000:valid:20261104"
	seedReceiptOrder(t, db, member.ID, sid, "已报")
	cases := map[string]string{
		"未知键（拼错）":      `{"signal_idd":"` + sid + `","status":"filled","qty":100,"price":10}`,
		"坏 JSON":       `{`,
		"缺 signal_id":  `{"status":"cancelled"}`,
		"未知 status":    `{"signal_id":"` + sid + `","status":"partially"}`,
		"filled 缺 qty": `{"signal_id":"` + sid + `","status":"filled","price":10}`,
		"filled 缺价":    `{"signal_id":"` + sid + `","status":"partial","qty":100}`,
		"qty 为负":       `{"signal_id":"` + sid + `","status":"filled","qty":-1,"price":10}`,
	}
	for name, body := range cases {
		rr := receiptPost(s, member, body)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("%s：应 400，got %d body=%s", name, rr.Code, rr.Body.String())
		}
	}
	row, _ := findOrderRow(t, db, member.ID, sid)
	if row.Status != "已报" {
		t.Fatalf("校验失败的回执改动了委托行：%+v", row)
	}
	if n := countManualFills(t, db, sid); n != 0 {
		t.Fatalf("校验失败的回执落成了成交行：%d 条", n)
	}
}

// TestOrderReceiptStatusConflictIsHonest 冲突如实 409：
// 本地已是终态（网关先撤了、还没标记过）再回 filled ⇒ 拒绝、零写入、不静默 200。
func TestOrderReceiptStatusConflictIsHonest(t *testing.T) {
	s, member, db := newManualTestServer(t)
	sid := "buy:600000:conflict:20261104"
	seedReceiptOrder(t, db, member.ID, sid, "已撤")
	rr := receiptPost(s, member, `{"signal_id":"`+sid+`","status":"filled","qty":100,"price":10}`)
	if rr.Code != http.StatusConflict {
		t.Fatalf("终态行收成交回执应如实 409，got %d body=%s", rr.Code, rr.Body.String())
	}
	row, _ := findOrderRow(t, db, member.ID, sid)
	if row.Status != "已撤" {
		t.Fatalf("冲突回执改动了状态：%+v", row)
	}
	if n := countManualFills(t, db, sid); n != 0 {
		t.Fatalf("冲突回执落成了成交行：%d 条", n)
	}
}

// TestOrderReceiptNotPlacedMapsToSendFailed not_placed 的口径腿：
// 落「发送失败」（同键可重放、不占额度语义），不是「废单」（券商受理过）。
func TestOrderReceiptNotPlacedMapsToSendFailed(t *testing.T) {
	s, member, db := newManualTestServer(t)
	sid := "sell:600000:notp:20261104"
	seedReceiptOrder(t, db, member.ID, sid, "已报")
	rr := receiptPost(s, member, `{"signal_id":"`+sid+`","status":"not_placed","note":"App里没挂出去"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("应 200，got %d body=%s", rr.Code, rr.Body.String())
	}
	row, _ := findOrderRow(t, db, member.ID, sid)
	if row.Status != "发送失败" {
		t.Fatalf("not_placed 应映射「发送失败」（§GAP2-W1 同语义），got %q", row.Status)
	}
}
