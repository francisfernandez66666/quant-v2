// multi_admin_identity_test.go — §MULTI-ADMIN（2026-10-31）双 admin 身份/权限/数据修复回归。
// 覆盖：三处写面按请求者归属、strict 权限 403、funds/today 账户隔离、000683 adjust 端点守卫。
package server

import (
	"encoding/json"
	"math"
	"net/http"
	"path/filepath"
	"testing"

	"quant-trading-v2/internal/auth"
	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/paper"
	"quant-trading-v2/internal/report"
	"quant-trading-v2/internal/store"
)

// newMultiAdminTestServer owner=admin（首个，五权限）+ second=test01（第二个，仅 research_approve）。
// 两个账号各有自己的 real_account 行；report 库独立；不依赖现网/真实柜台。
func newMultiAdminTestServer(t *testing.T) (*Server, *auth.User, *auth.User, *store.DB) {
	t.Helper()
	s, owner := newAdminTestServer(t)
	db, err := store.Open(filepath.Join(t.TempDir(), "live.db"))
	if err != nil {
		t.Fatalf("open live db: %v", err)
	}
	s.SetLiveDB(db)
	s.rpt = report.New(filepath.Join(t.TempDir(), "report.json"))
	s.cacheDir = t.TempDir()
	second, err := s.auth.CreateUser("test01", "pw-test01", auth.RoleAdmin,
		[]string{auth.PermResearchApprove}, 0)
	if err != nil {
		t.Fatalf("create second admin: %v", err)
	}
	if err := db.UpsertRealAccount(store.RealAccount{UserID: owner.ID, AvailableCash: 50000}); err != nil {
		t.Fatalf("seed owner cash: %v", err)
	}
	if err := db.UpsertRealAccount(store.RealAccount{UserID: second.ID, AvailableCash: 0}); err != nil {
		t.Fatalf("seed second cash: %v", err)
	}
	for _, p := range []string{auth.PermFundsAdmin, auth.PermTradingAdmin, auth.PermUserAdmin, auth.PermConfigAdmin, auth.PermOpsAdmin} {
		if !s.auth.HasExplicitPerm(owner.ID, p) {
			t.Fatalf("首个 admin 缺少 %s", p)
		}
		if s.auth.HasExplicitPerm(second.ID, p) {
			t.Fatalf("第二个 admin 不应有 %s", p)
		}
	}
	return s, owner, second, db
}

func TestMultiAdminSetBalanceIsolation(t *testing.T) {
	s, owner, second, db := newMultiAdminTestServer(t)
	if err := s.auth.GrantPerm(second.ID, auth.PermFundsAdmin); err != nil {
		t.Fatalf("grant funds: %v", err)
	}
	rr := adminDo(s, adminReq(s, second, http.MethodPost, "/api/holdings/balance", `{"available_balance":1234.56}`))
	if rr.Code != 200 {
		t.Fatalf("test01 写自己的资金应 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	own, _ := db.GetRealAccountOwn(second.ID)
	if math.Abs(own.AvailableCash-1234.56) > 0.005 {
		t.Fatalf("test01 自己行应 1234.56, got %.2f", own.AvailableCash)
	}
	ownerRow, _ := db.GetRealAccountOwn(owner.ID)
	if math.Abs(ownerRow.AvailableCash-50000) > 0.005 {
		t.Fatalf("operator 行不得被写, got %.2f", ownerRow.AvailableCash)
	}

	// 读面也按请求者：test01 读到自己的 1234.56，不是 owner 的 50000。
	rr = adminDo(s, adminReq(s, second, http.MethodGet, "/api/holdings", ""))
	if rr.Code != 200 {
		t.Fatalf("test01 GET holdings 应 200, got %d", rr.Code)
	}
	var body struct {
		AvailableBalance float64 `json:"available_balance"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &body)
	if math.Abs(body.AvailableBalance-1234.56) > 0.005 {
		t.Fatalf("test01 回读应 1234.56, got %.2f", body.AvailableBalance)
	}
}

func TestMultiAdminSetBalanceRequiresFundsPerm(t *testing.T) {
	s, owner, second, db := newMultiAdminTestServer(t)
	rr := adminDo(s, adminReq(s, second, http.MethodPost, "/api/holdings/balance", `{"available_balance":1}`))
	if rr.Code != 403 {
		t.Fatalf("test01 无 funds_admin 应 403, got %d body=%s", rr.Code, rr.Body.String())
	}
	secondRow, _ := db.GetRealAccountOwn(second.ID)
	ownerRow, _ := db.GetRealAccountOwn(owner.ID)
	if secondRow.AvailableCash != 0 || math.Abs(ownerRow.AvailableCash-50000) > 0.005 {
		t.Fatalf("403 后两行都不应变: second=%.2f owner=%.2f", secondRow.AvailableCash, ownerRow.AvailableCash)
	}
}

func TestMultiAdminPnlOffsetIsolation(t *testing.T) {
	s, owner, second, db := newMultiAdminTestServer(t)
	if err := s.auth.GrantPerm(second.ID, auth.PermFundsAdmin); err != nil {
		t.Fatalf("grant funds: %v", err)
	}
	if _, err := db.AddPnlOffset(store.PnlOffsetRecord{UserID: owner.ID, Offset: 10}); err != nil {
		t.Fatalf("seed owner offset: %v", err)
	}
	if _, err := db.AddPnlOffset(store.PnlOffsetRecord{UserID: second.ID, Offset: 20}); err != nil {
		t.Fatalf("seed second offset: %v", err)
	}
	rr := adminDo(s, adminReq(s, second, http.MethodPost, "/api/holdings/pnl-offset", `{"offset":30}`))
	if rr.Code != 200 {
		t.Fatalf("test01 校准应 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	own, _ := db.LatestPnlOffset(second.ID)
	ownerOff, _ := db.LatestPnlOffset(owner.ID)
	if math.Abs(own-30) > 0.005 || math.Abs(ownerOff-10) > 0.005 {
		t.Fatalf("偏移归属错: second=%.2f owner=%.2f", own, ownerOff)
	}
}

func TestMultiAdminSetHoldingsIsolation(t *testing.T) {
	s, owner, second, _ := newMultiAdminTestServer(t)
	if err := s.auth.GrantPerm(second.ID, auth.PermFundsAdmin); err != nil {
		t.Fatalf("grant funds: %v", err)
	}
	// owner 有一条 600000 手动持仓；test01 提交自己的 000001，不能覆盖 owner 的表。
	ownerHoldingID := "600000_" + owner.ID + "_fix"
	s.rpt.LogSignal(ownerHoldingID, "600000", "浦发银行", "做多", "手动", 10, 0, 0)
	s.rpt.AddLot(ownerHoldingID, 10, 100)
	s.rpt.Update(ownerHoldingID, func(l *report.ExecLog) { l.UserID = owner.ID })

	body := `{"holdings":[{"code":"000001","name":"平安银行","quantity":100,"cost_price":11}]}`
	rr := adminDo(s, adminReq(s, second, http.MethodPost, "/api/holdings", body))
	if rr.Code != 200 {
		t.Fatalf("test01 写自己的持仓应 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	secondHeld := s.rpt.HeldPositionsFor(second.ID)
	if len(secondHeld) != 1 || secondHeld[0].Code != "000001" {
		t.Fatalf("test01 应只有 000001, got %+v", secondHeld)
	}
	ownerHeld := s.rpt.HeldPositionsFor(owner.ID)
	if len(ownerHeld) != 1 || ownerHeld[0].Code != "600000" {
		t.Fatalf("owner 仍应只有 600000, got %+v", ownerHeld)
	}
}

// TestFundsTodayAccountIsolation 验证 /api/funds/today 按请求者归属取账：
// 成员账号（即便被授予 funds_admin）读不到 owner 的资金，owner 自己读到自己的数。
func TestFundsTodayAccountIsolation(t *testing.T) {
	s, owner, second, _ := newMultiAdminTestServer(t)
	if err := s.auth.GrantPerm(second.ID, auth.PermFundsAdmin); err != nil {
		t.Fatalf("grant funds: %v", err)
	}
	readCash := func(u *auth.User) float64 {
		rr := adminDo(s, adminReq(s, u, http.MethodGet, "/api/funds/today", ""))
		if rr.Code != 200 {
			t.Fatalf("GET funds/today who=%s got %d body=%s", u.Username, rr.Code, rr.Body.String())
		}
		var body struct {
			Live struct {
				Account struct {
					Cash float64 `json:"cash"`
				} `json:"account"`
			} `json:"live"`
		}
		_ = json.Unmarshal(rr.Body.Bytes(), &body)
		return body.Live.Account.Cash
	}
	if got := readCash(second); math.Abs(got) > 0.005 {
		t.Fatalf("test01 的 funds/today 不应读到 owner 资金, got %.2f", got)
	}
	if got := readCash(owner); math.Abs(got-50000) > 0.005 {
		t.Fatalf("owner 的 funds/today 应 50000, got %.2f", got)
	}
}

// multiAdminPaperRegistry 双账号纸面引擎注册表：只实现 funds/today 需要的 PaperForUser。
type multiAdminPaperRegistry struct {
	ownerID, secondID string
	owner, second     *paper.Engine
}

// 以下为 EngineRegistry 的测试桩方法：本用例只关心 PaperForUser 的账号分流，
// 其余能力一律空实现（不触发真实引擎/复盘/热同步）。
func (r *multiAdminPaperRegistry) GetController(string) EngineController     { return nil }
func (r *multiAdminPaperRegistry) InitStatusJSON(string) map[string]any      { return nil }
func (r *multiAdminPaperRegistry) AllControllers() []EngineController        { return nil }
func (r *multiAdminPaperRegistry) Len() int                                  { return 2 }
func (r *multiAdminPaperRegistry) SetPaperPools([]string)                    {}
func (r *multiAdminPaperRegistry) SetPaperConfig(paper.Config)               {}
func (r *multiAdminPaperRegistry) SetPaperLabelResolver(func(string) string) {}
func (r *multiAdminPaperRegistry) TriggerPositionReview(string) (int, error) { return 0, nil }
func (r *multiAdminPaperRegistry) OpenPositionStrategyCounts() combat_agent.HeldStrategyKeys {
	return nil
}
func (r *multiAdminPaperRegistry) PaperForUser(uid string) *paper.Engine {
	switch uid {
	case r.ownerID:
		return r.owner
	case r.secondID:
		return r.second
	}
	return nil
}

// TestFundsTodayPaperIsolation /api/funds/today 的 paper 读数必须取请求者自己的引擎，
// 不能回落到运营账号（旧实现恒取 operatorID，成员会看到 owner 的纸面目标仓位）。
func TestFundsTodayPaperIsolation(t *testing.T) {
	s, owner, second, _ := newMultiAdminTestServer(t)
	if err := s.auth.GrantPerm(second.ID, auth.PermFundsAdmin); err != nil {
		t.Fatalf("grant funds: %v", err)
	}
	newPaper := func(capital float64, name string) *paper.Engine {
		cfg := paper.ConfigFromRules(s.cfg.Get().Paper)
		cfg.Enabled = true
		cfg.AutoSell = false
		cfg.InitialCapital = capital
		return paper.New(cfg, filepath.Join(t.TempDir(), name+".json"))
	}
	peOwner, peSecond := newPaper(100000, "owner"), newPaper(200000, "second")
	s.SetEngineRegistry(&multiAdminPaperRegistry{ownerID: owner.ID, secondID: second.ID, owner: peOwner, second: peSecond})

	// readPaperCash 用指定账号请求 funds/today，取 paper.readout.cash 断言拿到的是谁的账。
	readPaperCash := func(u *auth.User) float64 {
		rr := adminDo(s, adminReq(s, u, http.MethodGet, "/api/funds/today", ""))
		if rr.Code != 200 {
			t.Fatalf("GET funds/today who=%s got %d body=%s", u.Username, rr.Code, rr.Body.String())
		}
		var body struct {
			Paper struct {
				Available bool `json:"available"`
				Readout   struct {
					Cash float64 `json:"cash"`
				} `json:"readout"`
			} `json:"paper"`
		}
		_ = json.Unmarshal(rr.Body.Bytes(), &body)
		if !body.Paper.Available {
			t.Fatalf("%s 的 paper 读数应可用", u.Username)
		}
		return body.Paper.Readout.Cash
	}
	if got, want := readPaperCash(second), peSecond.Stats().Cash; math.Abs(got-want) > 0.005 {
		t.Fatalf("test01 应读自己的纸面账 %.2f, got %.2f", want, got)
	}
	if got, want := readPaperCash(owner), peOwner.Stats().Cash; math.Abs(got-want) > 0.005 {
		t.Fatalf("owner 应读自己的纸面账 %.2f, got %.2f", want, got)
	}
}

// TestQMTEnableRequiresOwnToken 非运营账号启用实盘必须持有自己的网关 token；
// 空凭证/继承凭证一律 400，显式带 token 才放行（防止一把钥匙被顺手共享）。
func TestQMTEnableRequiresOwnToken(t *testing.T) {
	s, owner, second, _ := newMultiAdminTestServer(t)
	path := "/api/admin/users/" + second.ID + "/config/qmt"
	// 无自有 token、也不显式带 token ⇒ 拒绝启用。
	rr := adminDo(s, adminReq(s, owner, http.MethodPost, path, `{"enabled":true}`))
	if rr.Code != 400 {
		t.Fatalf("无自有 token 启用实盘应 400, got %d body=%s", rr.Code, rr.Body.String())
	}
	// 显式带一把自己的 token ⇒ 允许启用（配置合法字段一并给全）。
	body := `{"enabled":true,"mode":"auto","gateway_url":"http://127.0.0.1:8790","token":"own-secret",` +
		`"price_type":"market","fixed_amount":50000,"max_positions":8,"initial_capital":200000}`
	rr = adminDo(s, adminReq(s, owner, http.MethodPost, path, body))
	if rr.Code != 200 {
		t.Fatalf("显式自有 token 启用应 200, got %d body=%s", rr.Code, rr.Body.String())
	}
}

// TestStrictPermissionSurface 验证敏感路由只认显式权限位：
// 无对应权限的 admin 一律 403，持有权限位的 owner 不被拦。
func TestStrictPermissionSurface(t *testing.T) {
	s, owner, second, _ := newMultiAdminTestServer(t)
	cases := []struct {
		name   string
		path   string
		method string
		body   string
	}{
		{"qmt broker", "/api/qmt/broker", http.MethodGet, ""},
		{"usage", "/api/usage", http.MethodGet, ""},
		{"config strategy", "/api/config/strategy", http.MethodGet, ""},
		{"admin users", "/api/admin/users", http.MethodGet, ""},
		{"paper adjust", "/api/paper/adjust", http.MethodPost, `{"account_id":"x","code":"000683","qty":100,"price":5.54,"confirm":"RESTORE-000683"}`},
	}
	for _, tc := range cases {
		if rr := adminDo(s, adminReq(s, second, tc.method, tc.path, tc.body)); rr.Code != 403 {
			t.Errorf("test01 访问 %s 应 403, got %d body=%s", tc.name, rr.Code, rr.Body.String())
		}
	}
	// owner 有显式权限位，敏感端点不应 403。
	for _, tc := range cases[:4] {
		if rr := adminDo(s, adminReq(s, owner, tc.method, tc.path, tc.body)); rr.Code == 403 {
			t.Errorf("owner 访问 %s 不应 403, got %d", tc.name, rr.Code)
		}
	}
	// paper adjust 的 confirm 守卫：owner + 错 confirm -> 400（在没有 paper engine 前先判 body）。
	rr := adminDo(s, adminReq(s, owner, http.MethodPost, "/api/paper/adjust",
		`{"account_id":"x","code":"000683","qty":100,"price":5.54,"confirm":"BAD"}`))
	if rr.Code != 400 {
		t.Fatalf("错误 confirm 应 400, got %d body=%s", rr.Code, rr.Body.String())
	}
}

// TestPaperAdjustRejectsNewPosition 修复端点只补已持仓：对未持有的代码必须 400，
// 不能借它新建仓位（避免把一次性数据修复变成通用开仓入口）。
func TestPaperAdjustRejectsNewPosition(t *testing.T) {
	s, owner, second, _ := newMultiAdminTestServer(t)
	cfg := paper.ConfigFromRules(s.cfg.Get().Paper)
	cfg.Enabled = true
	cfg.AutoSell = false
	s.SetPaper(paper.New(cfg, filepath.Join(t.TempDir(), "paper.json")))
	body := `{"account_id":"` + second.ID + `","code":"000001","qty":100,"price":10,"confirm":"RESTORE-000683"}`
	rr := adminDo(s, adminReq(s, owner, http.MethodPost, "/api/paper/adjust", body))
	if rr.Code != 400 {
		t.Fatalf("未持有代码应 400, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestPaperAdjustRequiresEngineAndConfirm(t *testing.T) {
	s, owner, _, _ := newMultiAdminTestServer(t)
	// 没有 paper engine：confirm 正确时应 503（不是 200 假成功）。
	rr := adminDo(s, adminReq(s, owner, http.MethodPost, "/api/paper/adjust",
		`{"account_id":"x","code":"000683","qty":100,"price":5.54,"reason":"test","confirm":"RESTORE-000683"}`))
	if rr.Code != 503 {
		t.Fatalf("无 paper engine 应 503, got %d body=%s", rr.Code, rr.Body.String())
	}
}

// TestPaperAdjustRestoresHeldPosition 000683 已持有 100 股时，受控端点只加仓、不新建仓位，
// 并按显式 qty/price 记账（dynamic 下普通买入会忽略 qty，故不能走 /api/paper/buy）。
func TestPaperAdjustRestoresHeldPosition(t *testing.T) {
	s, owner, second, _ := newMultiAdminTestServer(t)
	cfg := paper.ConfigFromRules(s.cfg.Get().Paper)
	cfg.Enabled = true
	cfg.AutoSell = false
	pe := paper.New(cfg, filepath.Join(t.TempDir(), "paper.json"))
	s.SetPaper(pe)
	// 夹具先按 R-01 残留形态建 000683 100 股（绕过 HTTP 买入，因为 dynamic 忽略 qty）。
	if err := pe.BuyExInPool("000683", "远兴能源", "手动", "", 5.54, 5.54, 100, nil); err != nil {
		t.Fatalf("种子建仓失败: %v", err)
	}
	cashBefore := pe.Stats().Cash

	body := `{"account_id":"` + second.ID + `","code":"000683","qty":100,"price":5.54,"reason":"R-01 残留补回","confirm":"RESTORE-000683"}`
	rr := adminDo(s, adminReq(s, owner, http.MethodPost, "/api/paper/adjust", body))
	if rr.Code != 200 {
		t.Fatalf("补回应 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	var got int
	for _, p := range pe.Positions() {
		if p.Code == "000683" {
			got = p.Qty
		}
	}
	if got != 200 {
		t.Fatalf("000683 应补到 200 股, got %d", got)
	}
	// 记账语义：现金必须按 100×5.54 + 费用减少，不是凭空加仓。
	if pe.Stats().Cash >= cashBefore {
		t.Fatalf("补回后现金未减少: before=%.2f after=%.2f", cashBefore, pe.Stats().Cash)
	}
}
