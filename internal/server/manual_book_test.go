// manual_book_test.go — §2.12 手动维护账 HTTP 面的行为锁（2026-11-04，外脑化 B1' 批）。
//
// 这里锁四件事（存储层的四条见 internal/store/manual_book_test.go，两边刻意不重复）：
//
//	A. **路由档位**：账号级 authMiddleware，不落严格位——一个没有任何权限位的普通成员
//	   必须能录自己的账（挂了 quant_trade 才让录，就等于把桥断后的止血通道锁死）；
//	   同时未登录的裸请求必须 401，不是"落到运营账号"。
//	B. **归属**：只认登录态；请求体里自报的 user_id 一律不信（§安全 T1 同口径）。
//	C. **读面**：必须出现「手动维护，更新于 <asof>」，并且是 additive 键
//	   ——既有 positions/account 两个键的语义一个字都不能变。
//	D. **不进硬闸**（本批主题）：写完手动账之后，§M12-A 的 Controller.AvailableCash
//	   仍必须是 (0, false)＝口径不可得、自动买入照旧 fail-close。
//	   这条之所以要打成行为测试而不是写在注释里：它唯一的失效方式是"某天有人图省事
//	   把手动值写进 real_account"，而那不会报错，只会让一笔手敲的钱解开真金白银的闸。
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
	if v, ok := out["not_used_by"].([]interface{}); !ok || len(v) != 3 {
		t.Fatalf("读面必须点名这条读数不参与哪三条硬闸，got %#v", out["not_used_by"])
	}
}

// TestManualPositionsNeverUnlocksCashGate D（本批主题）：写完手动资金，§M12-A 仍判"口径不可得"。
// 断的是 Controller.AvailableCash 本身——自动买入腿 fail-close 的判据就是它的第二个返回值。
func TestManualPositionsNeverUnlocksCashGate(t *testing.T) {
	s, member, db := newManualTestServer(t)
	rr := manualPost(s, member, `{"positions":[{"ts_code":"600000.SH","qty":1000}],"available_cash":999999,"total_asset":999999,"asof":"2026-11-04 09:30:00"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("应 200，got %d body=%s", rr.Code, rr.Body.String())
	}
	ctrl := trading.NewController(nil, db, member.ID, config.DefaultQMTConfig(), nil)
	cash, fresh := ctrl.AvailableCash()
	if fresh {
		t.Fatalf("手动读数解开了 §M12-A 资金新鲜度（cash=%.2f fresh=%v）——自动买入腿从此可被手敲放行", cash, fresh)
	}
	if cash != 0 {
		t.Fatalf("AvailableCash 读到了手动值：%.2f", cash)
	}
	// T+1 可卖量：手动那 1000 股不许变成"可卖"
	if q := db.BuyableQtyForUserSell(member.ID, "600000.SH", "2026-11-04"); q != 0 {
		t.Fatalf("手动持仓造出了可卖量：%d 股", q)
	}
	// 风控闸的资金输入仍是零值行（集中度/买入纪律都以它为分母）
	gate := trading.NewController(nil, db, member.ID, config.DefaultQMTConfig(), nil)
	if acc, _ := gate.AvailableCash(); acc != 0 {
		t.Fatalf("risk.Gate 同源资金读数被手动值污染：%v", acc)
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
