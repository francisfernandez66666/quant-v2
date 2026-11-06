// manual_book_test.go — §MANUAL-BOOK-UNIFY（2026-11-06 晚批）手动账 HTTP 面的行为锁。
//
// 这里锁七件事（存储层的四条见 internal/store/manual_book_test.go，改账流水见 manual_adjust_test.go，两边刻意不重复）：
//
//	A. **路由档位**：账号级 authMiddleware，不落严格位——一个没有任何权限位的普通成员
//	   必须能录自己的账（挂了 quant_trade 才让录，就等于把桥断后的止血通道锁死）；
//	   同时未登录的裸请求必须 401，不是"落到运营账号"。
//	B. **归属**：只认登录态；请求体里自报的 user_id 一律不信（§安全 T1 同口径）。
//	C. **读面**：必须出现「手动维护，更新于 <asof>」，并且是 additive 键
//	   ——既有 positions/account 两个键的语义一个字都不能变（变的是**值选谁**，不是键的形状）。
//	D. **柜台为准**（裁决 1 的前半句）：柜台在报数时，手动账哪怕写着 99 万也不许进 §M12-A；
//	   asof 停在旧日的手动账同样不许解开资金闸（这是上午那批的旧主题，改判后它变成一条前置判据的测试）。
//	E. **增量改账**（裁决 2/3/4）：adjust 与全量互斥、一次一条、缺数量拒收；回显带来源。
//	F. **读侧合成一本账**（裁决 1/2）：柜台不报数 + 手动当日 ⇒ positions 换成手动行且 book_source 点名；
//	   柜台报数 ⇒ 逐字柜台，book_source 标 counter。
//	G. **接管指针**（裁决 5）：柜台回报成功落库后打标；回报失败路径**不打**——
//	   把没回来的账说成回来了，等于在唯一兜底通道上自己拔线。
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
	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/store"
	"quant-trading-v2/internal/trading"
)

// newManualTestServer 账号级测试栈：一个普通成员（无任何权限位）+ 一个实盘账本库。
func newManualTestServer(t *testing.T) (*Server, *auth.User, *store.DB) {
	t.Helper()
	s, _ := newAdminTestServer(t)
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "live.db"))
	if err != nil {
		t.Fatalf("open live db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	s.SetLiveDB(db)
	member, err := s.auth.CreateUser("member1", "pw", auth.RoleUser, nil, 0)
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	return s, member, db
}

// manualPost 以指定用户身份 POST（走完整 mux，因此路由上挂的中间件真的会跑）。
func manualPost(s *Server, u *auth.User, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/qmt/manual-positions", strings.NewReader(body))
	if u != nil {
		req.Header.Set("Authorization", "Bearer "+u.Token)
		ctx := context.WithValue(req.Context(), ctxUserKey{}, s.auth.UserByID(u.ID))
		req = req.WithContext(ctx)
	}
	rr := httptest.NewRecorder()
	s.mux.ServeHTTP(rr, req)
	return rr
}

// TestManualPositionsMemberLevelRoute A：普通成员（零权限位）可录；未登录 401。
// 这一条同时是"档位"锁：把它改成 strictPermMiddleware(任何一枚位) 会让本用例第一条就 403 变红。
func TestManualPositionsMemberLevelRoute(t *testing.T) {
	s, member, _ := newManualTestServer(t)
	rr := manualPost(s, member, `{"positions":[{"ts_code":"600000.SH","qty":1000}],"available_cash":50000,"asof":"2026-11-04 09:30:00"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("普通成员应能录自己的手动账（§2.12 不落严格位），got %d body=%s", rr.Code, rr.Body.String())
	}
	// 未登录：不注入身份的裸请求走真实 authMiddleware ⇒ 401，且绝不能顺手写进运营账号的账
	anon := httptest.NewRequest(http.MethodPost, "/api/qmt/manual-positions", strings.NewReader(`{"available_cash":1}`))
	anonRR := httptest.NewRecorder()
	s.mux.ServeHTTP(anonRR, anon)
	if anonRR.Code != http.StatusUnauthorized {
		t.Fatalf("未登录应 401，got %d", anonRR.Code)
	}
}

// TestManualPositionsAttribution B：归属只认登录态，请求体自报 user_id 不采信。
func TestManualPositionsAttribution(t *testing.T) {
	s, member, db := newManualTestServer(t)
	operator := s.operatorID()
	body := `{"user_id":"` + operator + `","positions":[{"ts_code":"600519.SH","qty":200}],"available_cash":12345}`
	rr := manualPost(s, member, body)
	if rr.Code != http.StatusOK {
		t.Fatalf("应 200，got %d body=%s", rr.Code, rr.Body.String())
	}
	// 写进了成员自己的账
	if got, _ := db.ManualPositionsForUser(member.ID); len(got) != 1 || got[0].Qty != 200 {
		t.Fatalf("手动持仓没落到登录账号名下：%+v", got)
	}
	// 冒名者没写到运营账号名下
	if got, _ := db.ManualPositionsForUser(operator); len(got) != 0 {
		t.Fatalf("请求体 user_id 被采信了（§安全 T1）：%+v", got)
	}
	if st, _ := db.ManualAccountForUser(operator); st.Present {
		t.Fatalf("运营账号被冒名写入手动资金：%+v", st)
	}
}

// TestManualPositionsIdempotentViaHTTP A/B 之外的幂等面：同一份清单提交两次，读数不变、不长行。
func TestManualPositionsIdempotentViaHTTP(t *testing.T) {
	s, member, db := newManualTestServer(t)
	body := `{"positions":[{"ts_code":"600000.SH","qty":1000,"cost_price":8.5},{"ts_code":"000001.SZ","qty":500}],"available_cash":50000,"asof":"2026-11-04 09:30:00"}`
	for i := 0; i < 2; i++ {
		if rr := manualPost(s, member, body); rr.Code != http.StatusOK {
			t.Fatalf("第 %d 次提交应 200，got %d body=%s", i+1, rr.Code, rr.Body.String())
		}
	}
	got, _ := db.ManualPositionsForUser(member.ID)
	if len(got) != 2 {
		t.Fatalf("重放长行了：n=%d %+v", len(got), got)
	}
}

// TestManualPositionsValidation 校验面：非法代码/缺 qty/未知字段/空提交/只带总资产 全部 400。
// 每一条都对应一种"静默错位"的形状，宁可拒在门口。
func TestManualPositionsValidation(t *testing.T) {
	s, member, db := newManualTestServer(t)
	cases := map[string]string{
		"缺交易所后缀的代码":      `{"positions":[{"ts_code":"600000","qty":100}]}`,
		"缺 qty（指针未给）":    `{"positions":[{"ts_code":"600000.SH"}]}`,
		"qty 为 0（清仓请删行）": `{"positions":[{"ts_code":"600000.SH","qty":0}]}`,
		"未知字段（拼错键名）":     `{"position":[{"ts_code":"600000.SH","qty":100}]}`,
		"空提交（一项都没带）":     `{}`,
		"只带总资产":          `{"total_asset":99999}`,
		"asof 认不出":       `{"available_cash":100,"asof":"11/04/2026"}`,
		"资金为负":           `{"available_cash":-1}`,
	}
	for name, body := range cases {
		rr := manualPost(s, member, body)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("%s：应 400，got %d body=%s", name, rr.Code, rr.Body.String())
		}
	}
	// 全部失败之后，账上必须一行都没留下（半批落库是最难查的形态）
	if got, _ := db.ManualPositionsForUser(member.ID); len(got) != 0 {
		t.Fatalf("校验失败的批次留下了行：%+v", got)
	}
	if st, _ := db.ManualAccountForUser(member.ID); st.Present {
		t.Fatalf("校验失败的批次留下了资金行：%+v", st)
	}
}

// TestManualPositionsReadoutLabelsSource C：读面必须说清"这是手动维护的、更新于什么时候"。
func TestManualPositionsReadoutLabelsSource(t *testing.T) {
	s, member, _ := newManualTestServer(t)
	rr := manualPost(s, member, `{"positions":[{"ts_code":"600000.SH","qty":1000}],"available_cash":50000,"asof":"2026-11-04 09:30:00"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("应 200，got %d", rr.Code)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out["source"] != store.ManualSourceLabel {
		t.Fatalf("回显必须带 source=manual，got %v", out["source"])
	}
	note, _ := out["note"].(string)
	if !strings.HasPrefix(note, "手动维护，更新于 ") || !strings.Contains(note, "2026-11-04 09:30:00") {
		t.Fatalf("回显必须带「手动维护，更新于 <asof>」，got %q", note)
	}
	// 三态：填了可用资金 ⇒ positive（"没录"与"录了 0"不能都显示成 0）
	if out["cash_state"] != "positive" {
		t.Fatalf("cash_state 应为 positive，got %v", out["cash_state"])
	}
	// §MANUAL-BOOK-UNIFY 裁决 1 之后，读面必须说清两件事：**参与**哪些判据、在什么情况下**不参与**。
	// 只写"不参与哪些"是上午那批的口径，已经作废；但 used_by / unavailable_when 都必须逐条点名——
	// 一个不知道边界的调用方才会把"手动资金"当"柜台余额"，也才会把"今天没用它"读成"这本账是空的"。
	if v, ok := out["used_by"].([]interface{}); !ok || len(v) < 4 {
		t.Fatalf("读面必须点名这条读数参与哪些判据（至少四条），got %#v", out["used_by"])
	}
	if v, ok := out["unavailable_when"].([]interface{}); !ok || len(v) != 3 {
		t.Fatalf("读面必须点名三条「不参与」情形（柜台在报数 / asof 非今日 / 已被接管），got %#v", out["unavailable_when"])
	}
	// VWAP 硬过滤吃的是行情成交额/量，与账本无关——这条必须仍然留在 not_used_by 里（改判不等于全改）。
	if v, ok := out["not_used_by"].([]interface{}); !ok || len(v) != 1 ||
		!strings.Contains(v[0].(string), "VWAP") {
		t.Fatalf("not_used_by 必须只剩 VWAP 那一条（其余三条已按裁决 1 改判参与），got %#v", out["not_used_by"])
	}
}

// TestManualPositionsStaleAsofNeverUnlocksCashGate D：asof 停在旧日的手动账不许解开 §M12-A。
//
// 上午那批这条叫"手动读数绝不进硬闸"（本批主题）；owner 2026-11-06 裁决 1 把它改判成
// "柜台连不上就以手工为准"之后，它变成那条改判的**前置判据**——几天前手敲的余额不许在下次桥断时顶上。
// 断言一个字没放松：判的还是 Controller.AvailableCash 的第二个返回值，也就是自动买入腿的 fail-close 开关。
func TestManualPositionsStaleAsofNeverUnlocksCashGate(t *testing.T) {
	s, member, db := newManualTestServer(t)
	rr := manualPost(s, member, `{"positions":[{"ts_code":"600000.SH","qty":1000}],"available_cash":999999,"total_asset":999999,"asof":"2026-11-04 09:30:00"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("应 200，got %d body=%s", rr.Code, rr.Body.String())
	}
	ctrl := trading.NewController(nil, db, member.ID, config.DefaultQMTConfig(), nil)
	cash, fresh := ctrl.AvailableCash()
	if fresh {
		t.Fatalf("asof 停在 11-04 的手动账解开了 §M12-A 资金新鲜度（cash=%.2f fresh=%v）——自动买入腿从此可被一笔旧账放行", cash, fresh)
	}
	if cash != 0 {
		t.Fatalf("AvailableCash 读到了过期手动值：%.2f", cash)
	}
	// T+1 可卖量：过期手动那 1000 股不许变成"可卖"。
	if q := db.BuyableQtyForUserSell(member.ID, "600000.SH", "2026-11-04"); q != 0 {
		t.Fatalf("手动持仓造出了可卖量：%d 股", q)
	}
}

// TestManualPositionsTodayAsofDoesFeedCashGate D 的正腿（裁决 1 真正要的效果）：
// 柜台不报数 + 手动账是**今天**维护的 ⇒ §M12-A 从此按手动余额判，且来源标 manual。
// 这一条是"改判"的验收面——只测过期不测当日，等于把功能测成永远不生效。
func TestManualPositionsTodayAsofDoesFeedCashGate(t *testing.T) {
	s, member, db := newManualTestServer(t)
	today := cntime.DayOf(cntime.Now())
	body := `{"positions":[{"ts_code":"600000.SH","qty":1000}],"available_cash":88000,"asof":"` + today + ` 09:30:00"}`
	if rr := manualPost(s, member, body); rr.Code != http.StatusOK {
		t.Fatalf("应 200，got %d body=%s", rr.Code, rr.Body.String())
	}
	ctrl := trading.NewController(nil, db, member.ID, config.DefaultQMTConfig(), nil)
	cash, fresh, source := ctrl.AvailableCashBook()
	if !fresh || source != "manual" || cash != 88000 {
		t.Fatalf("柜台未报数 + 当日手动账必须喂进资金闸，got (%.0f,%v,%q)", cash, fresh, source)
	}
	// 仍然没有把任何值写进柜台输入表（裁决 1 的前提）。
	acc, err := db.GetRealAccountOwn(member.ID)
	if err != nil || acc.AvailableCash != 0 || acc.UpdatedAt != "" {
		t.Fatalf("手动值被写进 real_account 了（柜台为准从此无法判定）：%+v err=%v", acc, err)
	}
}

// TestManualPositionsRealBookReadAdditive C 的另一半：GET /api/positions/real 新增 manual 键，
// 而 positions/account 两个既有键一个字节都没变（手动行绝不混进柜台行里）。
func TestManualPositionsRealBookReadAdditive(t *testing.T) {
	s, member, db := newManualTestServer(t)
	// 柜台账先来一笔真实持仓（网关回报路径的等价写法）
	if _, err := db.ReconcilePositionsForUser(member.ID, []store.RealPosition{
		{TsCode: "600000.SH", Name: "柜台山", Qty: 300, CostPrice: 7.7},
	}); err != nil {
		t.Fatalf("seed broker position: %v", err)
	}
	if rr := manualPost(s, member, `{"positions":[{"ts_code":"600111.SH","qty":1000}],"available_cash":50000,"asof":"2026-11-04 09:30:00"}`); rr.Code != http.StatusOK {
		t.Fatalf("manual post 应 200，got %d body=%s", rr.Code, rr.Body.String())
	}
	req := httptest.NewRequest(http.MethodGet, "/api/positions/real", nil)
	ctx := context.WithValue(req.Context(), ctxUserKey{}, s.auth.UserByID(member.ID))
	rr := httptest.NewRecorder()
	s.handleRealPositions(rr, req.WithContext(ctx))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET real positions 应 200，got %d body=%s", rr.Code, rr.Body.String())
	}
	var out struct {
		Positions []store.RealPosition `json:"positions"`
		Manual    struct {
			Present  bool   `json:"present"`
			Source   string `json:"source"`
			Note     string `json:"note"`
			AsOf     string `json:"asof"`
			Position []struct {
				TsCode string `json:"ts_code"`
				Qty    int    `json:"qty"`
			} `json:"positions"`
		} `json:"manual"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v body=%s", err, rr.Body.String())
	}
	if len(out.Positions) != 1 || out.Positions[0].TsCode != "600000.SH" {
		t.Fatalf("柜台持仓键被手动行污染（additive 要求既有 positions 不变）：%+v", out.Positions)
	}
	if !out.Manual.Present || out.Manual.Source != "manual" {
		t.Fatalf("manual 键缺失或没标来源：%+v", out.Manual)
	}
	if !strings.HasPrefix(out.Manual.Note, "手动维护，更新于 ") {
		t.Fatalf("manual.note 不如实：%q", out.Manual.Note)
	}
	if len(out.Manual.Position) != 1 || out.Manual.Position[0].TsCode != "600111.SH" {
		t.Fatalf("manual.positions 装配不对：%+v", out.Manual.Position)
	}
}

// manualGetReal 走完整 handler 读实仓账（GET /api/positions/real）。
func manualGetReal(t *testing.T, s *Server, u *auth.User) map[string]interface{} {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/positions/real", nil)
	ctx := context.WithValue(req.Context(), ctxUserKey{}, s.auth.UserByID(u.ID))
	rr := httptest.NewRecorder()
	s.handleRealPositions(rr, req.WithContext(ctx))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/positions/real 应 200，got %d body=%s", rr.Code, rr.Body.String())
	}
	var out map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

// qmtReportTokenFor 给该成员落一把自己的网关钥匙并返回——接管触发点的两格必须走
// 真实的 qmtReportMiddleware 鉴权腿（回报的归属只认钥匙，普通会话 token 会被 401）。
// 姿势与 carrier_test.go 的 seedCarrierKey 同源：钥匙必须真正落盘，
// userForQMTToken 的反查走 ListUsers 遍历，编一个不在册的 uid 只会得到空归属的假绿。
func qmtReportTokenFor(t *testing.T, s *Server, uid string) string {
	t.Helper()
	key := "GKEY-" + uid
	cfg := *(s.cfg.GetQMTConfigFor(uid))
	cfg.Token = key
	cfg.GatewayURL = "http://127.0.0.1:18789"
	if err := s.cfg.SetQMTConfigFor(uid, &cfg); err != nil {
		t.Fatalf("给 %s 落网关钥匙: %v", uid, err)
	}
	if got := s.userForQMTToken(key); got != uid {
		t.Fatalf("钥匙应反查到 %s，实得 %q", uid, got)
	}
	return key
}

// TestManualAdjustEndpoints E：增量改账的三条入口约束——
// 与全量互斥、一次一条、缺数量拒收；成功时回显带来源与改后值。
func TestManualAdjustEndpoints(t *testing.T) {
	s, member, db := newManualTestServer(t)
	today := cntime.DayOf(cntime.Now())
	// ① 两种语义同时提交 ⇒ 400（被吞的那一个恰好是"我刚点的那笔减仓"）。
	if rr := manualPost(s, member, `{"positions":[{"ts_code":"600000.SH","qty":100}],"adjust":[{"op":"add","ts_code":"600000.SH","qty":100,"price":9}]}`); rr.Code != http.StatusBadRequest {
		t.Fatalf("adjust 与 positions 同时提交必须 400，got %d body=%s", rr.Code, rr.Body.String())
	}
	// ② 多条 ⇒ 400（一次一条，不做"部分成功"的批次）。
	if rr := manualPost(s, member, `{"adjust":[{"op":"add","ts_code":"600000.SH","qty":100,"price":9},{"op":"add","ts_code":"000001.SZ","qty":100,"price":9}]}`); rr.Code != http.StatusBadRequest {
		t.Fatalf("adjust 多条必须 400，got %d body=%s", rr.Code, rr.Body.String())
	}
	// ③ 缺 qty ⇒ 400（缺数量与数量 0 在钱路径上是两件事）。
	if rr := manualPost(s, member, `{"adjust":[{"op":"add","ts_code":"600000.SH","price":9}]}`); rr.Code != http.StatusBadRequest {
		t.Fatalf("adjust 缺 qty 必须 400，got %d body=%s", rr.Code, rr.Body.String())
	}
	// ④ 未知动作 ⇒ 400，且文案把合法动作说清楚。
	rr := manualPost(s, member, `{"adjust":[{"op":"halve","ts_code":"600000.SH","qty":100,"price":9}]}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("未知 op 必须 400，got %d body=%s", rr.Code, rr.Body.String())
	}
	// ⑤ 正常加仓（先有资金基线，否则 store 会拒并指路「直接改资金」）。
	if rr := manualPost(s, member, `{"available_cash":50000,"asof":"`+today+` 09:30:00"}`); rr.Code != http.StatusOK {
		t.Fatalf("先录资金基线应 200，got %d body=%s", rr.Code, rr.Body.String())
	}
	if rr := manualPost(s, member, `{"adjust":[{"op":"new","ts_code":"600000.SH","qty":1000,"price":8.5}]}`); rr.Code != http.StatusOK {
		t.Fatalf("改账·新增应 200，got %d body=%s", rr.Code, rr.Body.String())
	} else {
		var out map[string]interface{}
		_ = json.Unmarshal(rr.Body.Bytes(), &out)
		adj, _ := out["adjust"].(map[string]interface{})
		if adj == nil || adj["op"] != "new" {
			t.Fatalf("回显必须带本次改账结果，got %#v", out["adjust"])
		}
		if adj["seeded_from"] != "none" {
			t.Fatalf("全新建仓的基线来源必须是 none（柜台与手动都没有这一码），got %v", adj["seeded_from"])
		}
		if cashAfter, _ := adj["cash_after"].(float64); cashAfter != 41500 {
			t.Fatalf("买扣资金应 50000-8500=41500，got %v", adj["cash_after"])
		}
	}
	// 流水当场可回看（这是 T+1 当日买入扣减与人工对账的凭据）。
	if n, err := db.ManualTodayBoughtQty(member.ID, "600000.SH", today); err != nil || n != 1000 {
		t.Fatalf("改账·新增必须落当日买入流水，got %d err=%v", n, err)
	}
}

// TestManualRealBookMergesBySource F：读侧合成一本账。
// 柜台不报数 + 手动当日 ⇒ positions 换成手动行、book_source.positions=manual；
// 柜台报数 ⇒ positions 逐字柜台、book_source 标 counter（裁决 1 前半句"以柜台为准"在屏上的样子）。
func TestManualRealBookMergesBySource(t *testing.T) {
	s, member, db := newManualTestServer(t)
	today := cntime.DayOf(cntime.Now())
	yesterday := cntime.DayOf(cntime.Now().AddDate(0, 0, -1))
	// linkLive 必须真成立：resolver 把"控制器在场"当显式入参，只看库里的时间戳不够——
	// 测试夹具种库的时刻再新，控制器不在场也不算"柜台在报数"（manual_book_source.go 头注的同一条判据）。
	// 姿势与 execute_action_test.go 同源：真实 trading.Controller + fakeCtrl 暴露 QMTController。
	ctrl := trading.NewController(nil, db, member.ID, config.DefaultQMTConfig(), nil)
	s.SetEngineController(fakeCtrl{qmt: ctrl})

	// 场景一：柜台持仓停在昨天（桥断当天冻结的样子），手动账今天维护 ⇒ 表里应是手动行。
	if _, err := db.ReconcilePositionsForUser(member.ID, []store.RealPosition{
		{TsCode: "600999.SH", Name: "柜台旧", Qty: 300, CostPrice: 7.7, UpdatedAt: yesterday + " 14:55:00"},
	}); err != nil {
		t.Fatalf("seed stale counter: %v", err)
	}
	if err := db.UpsertRealAccount(store.RealAccount{UserID: member.ID, AvailableCash: 1234,
		UpdatedAt: yesterday + " 14:55:00"}); err != nil {
		t.Fatalf("seed stale account: %v", err)
	}
	if rr := manualPost(s, member, `{"positions":[{"ts_code":"600000.SH","qty":1000,"cost_price":8.5}],"available_cash":88000,"asof":"`+today+` 09:30:00"}`); rr.Code != http.StatusOK {
		t.Fatalf("manual post 应 200，got %d body=%s", rr.Code, rr.Body.String())
	}
	out := manualGetReal(t, s, member)
	src, _ := out["book_source"].(map[string]interface{})
	if src == nil {
		t.Fatalf("响应必须带 book_source（前端唯一的来源判据）：%#v", out["book_source"])
	}
	if src["positions"] != "manual" || src["cash"] != "manual" {
		t.Fatalf("两维都应换成手动，got %#v", src)
	}
	poses, _ := out["positions"].([]interface{})
	if len(poses) != 1 {
		t.Fatalf("表里应只剩手动那一行，got %#v", poses)
	}
	first := poses[0].(map[string]interface{})
	if first["ts_code"] != "600000.SH" {
		t.Fatalf("持仓行应来自手动账，got %v", first["ts_code"])
	}
	// updated_at 必须跟着换成手动 asof：右上角那句"持仓数据截至"不能继续显示柜台回报时刻。
	if first["updated_at"] != today+" 09:30:00" {
		t.Fatalf("手动行的 updated_at 应等于手动 asof，got %v", first["updated_at"])
	}
	acc := out["account"].(map[string]interface{})
	if acc["available_cash"].(float64) != 88000 {
		t.Fatalf("可用资金应取手动账 88000，got %v", acc["available_cash"])
	}
	// manual 明细键仍在（改账流水与可用性三态的回看面）。
	if m, ok := out["manual"].(map[string]interface{}); !ok || m["present"] != true {
		t.Fatalf("manual 明细键缺失：%#v", out["manual"])
	}

	// 场景二：柜台恢复报数 ⇒ 表逐字回到柜台，book_source 标 counter（手动账一个字都不参与）。
	if _, err := db.ReconcilePositionsForUser(member.ID, []store.RealPosition{
		{TsCode: "600999.SH", Name: "柜台新", Qty: 500, CostPrice: 7.7, UpdatedAt: today + " 10:00:00"},
	}); err != nil {
		t.Fatalf("seed fresh counter: %v", err)
	}
	if err := db.UpsertRealAccount(store.RealAccount{UserID: member.ID, AvailableCash: 4321,
		UpdatedAt: cntime.Now().Format("2006-01-02 15:04:05")}); err != nil {
		t.Fatalf("seed fresh account: %v", err)
	}
	out = manualGetReal(t, s, member)
	src, _ = out["book_source"].(map[string]interface{})
	if src["positions"] != "counter" || src["cash"] != "counter" {
		t.Fatalf("柜台在报数时两维都必须信柜台，got %#v", src)
	}
	poses, _ = out["positions"].([]interface{})
	if len(poses) != 1 || poses[0].(map[string]interface{})["ts_code"] != "600999.SH" {
		t.Fatalf("持仓表应逐字回到柜台行，got %#v", poses)
	}
}

// TestManualTakenOverMarkedOnReportSuccess G：接管指针只在回报**成功落库**后打。
// 这里打的是 positions 事件那条腿；失败腿由"坏快照整批拒收 ⇒ 不打标"这一格锁。
func TestManualTakenOverMarkedOnReportSuccess(t *testing.T) {
	s, member, db := newManualTestServer(t)
	today := cntime.DayOf(cntime.Now())
	if rr := manualPost(s, member, `{"positions":[{"ts_code":"600000.SH","qty":1000}],"available_cash":88000,"asof":"`+today+` 09:30:00"}`); rr.Code != http.StatusOK {
		t.Fatalf("manual post 应 200，got %d", rr.Code)
	}
	// 回报失败的一格：positions 里塞一行非法代码 ⇒ 整批 400，接管标记**不许**打。
	// 回报体是**单事件**形状（信封即事件本身，qmtReportEvent 直接解码），没有 events 数组。
	// 且必须走真实的 qmtReportMiddleware 归属腿：uid 来自那把钥匙反查（§安全 T1），直调 handler 拿不到归属。
	bad := httptest.NewRequest(http.MethodPost, "/api/qmt/report", strings.NewReader(
		`{"type":"positions","positions":[{"ts_code":"","qty":100}]}`))
	bad.Header.Set("Authorization", "Bearer "+qmtReportTokenFor(t, s, member.ID))
	badRR := httptest.NewRecorder()
	s.qmtReportMiddleware(s.handleQMTReport)(badRR, bad)
	if badRR.Code == http.StatusOK {
		t.Fatalf("非法持仓快照应被拒（本用例的前提），got %d body=%s", badRR.Code, badRR.Body.String())
	}
	v, err := db.ManualBookViewFor(member.ID, today)
	if err != nil {
		t.Fatalf("读可用性: %v", err)
	}
	if v.TakenOverAt != "" {
		t.Fatalf("回报失败却打了接管指针（等于把唯一的兜底通道自己关掉）：%q", v.TakenOverAt)
	}

	// 成功的一格：合法持仓快照落库 ⇒ 打标，且行与流水都还在。
	good := httptest.NewRequest(http.MethodPost, "/api/qmt/report", strings.NewReader(
		`{"type":"positions","positions":[{"ts_code":"600000.SH","qty":1000,"cost_price":8.5}]}`))
	good.Header.Set("Authorization", "Bearer "+qmtReportTokenFor(t, s, member.ID))
	goodRR := httptest.NewRecorder()
	s.qmtReportMiddleware(s.handleQMTReport)(goodRR, good)
	if goodRR.Code != http.StatusOK {
		t.Fatalf("合法持仓回报应 200，got %d body=%s", goodRR.Code, goodRR.Body.String())
	}
	v, _ = db.ManualBookViewFor(member.ID, today)
	if v.TakenOverAt == "" {
		t.Fatal("柜台回报成功落库后必须打接管指针（裁决 5）")
	}
	rows, _ := db.ManualPositionsForUser(member.ID)
	if len(rows) == 0 {
		t.Fatal("接管不许删手动行（证据必须留着）")
	}
}

// TestManualTakenOverMarkedOnAccountReport H：接管指针的**第二条**真源——资金腿回报成功落库。
// G 组只锁了 positions 腿；资金与持仓两维独立报数，只打一条会出现
// "资金已接管、持仓还在用旧手动账"。不带 synced/data_ts 的旧桥信封按旧口径判新鲜，
// 走的是 qmt.go account 分支最平常的那条路。
func TestManualTakenOverMarkedOnAccountReport(t *testing.T) {
	s, member, db := newManualTestServer(t)
	today := cntime.DayOf(cntime.Now())
	if rr := manualPost(s, member, `{"positions":[{"ts_code":"600000.SH","qty":1000}],"available_cash":88000,"asof":"`+today+` 09:30:00"}`); rr.Code != http.StatusOK {
		t.Fatalf("manual post 应 200，got %d body=%s", rr.Code, rr.Body.String())
	}
	// 失败腿：asset 为空 ⇒ 400，不许打标。
	empty := httptest.NewRequest(http.MethodPost, "/api/qmt/report", strings.NewReader(`{"type":"account","asset":{}}`))
	empty.Header.Set("Authorization", "Bearer "+qmtReportTokenFor(t, s, member.ID))
	emptyRR := httptest.NewRecorder()
	s.qmtReportMiddleware(s.handleQMTReport)(emptyRR, empty)
	if emptyRR.Code == http.StatusOK {
		t.Fatalf("空资产快照应被拒（本用例的前提），got %d body=%s", emptyRR.Code, emptyRR.Body.String())
	}
	v, err := db.ManualBookViewFor(member.ID, today)
	if err != nil {
		t.Fatalf("读可用性: %v", err)
	}
	if v.TakenOverAt != "" {
		t.Fatalf("资金回报失败却打了接管指针：%q", v.TakenOverAt)
	}
	// 成功腿：合法资产快照落库 ⇒ 打标。
	good := httptest.NewRequest(http.MethodPost, "/api/qmt/report", strings.NewReader(
		`{"type":"account","asset":{"cash":4321,"frozen_cash":0,"total_asset":4321,"market_value":0}}`))
	good.Header.Set("Authorization", "Bearer "+qmtReportTokenFor(t, s, member.ID))
	goodRR := httptest.NewRecorder()
	s.qmtReportMiddleware(s.handleQMTReport)(goodRR, good)
	if goodRR.Code != http.StatusOK {
		t.Fatalf("合法资产回报应 200，got %d body=%s", goodRR.Code, goodRR.Body.String())
	}
	v, _ = db.ManualBookViewFor(member.ID, today)
	if v.TakenOverAt == "" {
		t.Fatal("柜台资金腿成功落库后必须打接管指针（裁决 5，两维各自独立）")
	}
}

// TestManualTakenOverNotMarkedByManualBalanceEdit I：**负锁**——纸面页人工改余额
// （POST /api/holdings/balance，写的是 real_account 但钱是人敲的）不许打接管指针。
// 方案 §5 曾把这个落库点误标为"对账写账户的第三处触发点"；照它加一行
// markManualTakenOver 的后果是：人改一次余额 ⇒ 当日手动账被判成已被柜台接管
// ⇒ 下次桥断 asof 早于接管时刻 ⇒ 读不到，唯一的止血通道被自己关掉。
// 故意破坏的方式：在 handlers_fix.go 的 UpsertRealAccount 成功分支后补那一行，本用例必红。
func TestManualTakenOverNotMarkedByManualBalanceEdit(t *testing.T) {
	s, admin := newAdminTestServer(t)
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "live.db"))
	if err != nil {
		t.Fatalf("open live db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	s.SetLiveDB(db)
	today := cntime.DayOf(cntime.Now())
	if rr := manualPost(s, admin, `{"positions":[{"ts_code":"600000.SH","qty":1000}],"available_cash":88000,"asof":"`+today+` 09:30:00"}`); rr.Code != http.StatusOK {
		t.Fatalf("录当日手动账应 200，got %d body=%s", rr.Code, rr.Body.String())
	}
	req := adminReq(s, admin, "POST", "/api/holdings/balance", `{"available_balance":12345.67}`)
	if rr := adminDo(s, req); rr.Code != http.StatusOK {
		t.Fatalf("手工改余额应 200，got %d body=%s", rr.Code, rr.Body.String())
	}
	v, err := db.ManualBookViewFor(admin.ID, today)
	if err != nil {
		t.Fatalf("读可用性: %v", err)
	}
	if v.TakenOverAt != "" {
		t.Fatalf("人敲余额不是柜台回报，不许打接管指针：%q", v.TakenOverAt)
	}
	if !v.CashUsable || !v.PositionsUsable {
		t.Fatalf("人工改余额后这本当日手动账仍须两维可用（cash=%v pos=%v）", v.CashUsable, v.PositionsUsable)
	}
}
