// signoff_api_test.go — §DYN-SIZE v4 / §ANCHOR-FIRST 签字面的行为锁（2026-10-26）。
//
// 锁四件事，全部是"写资金参数的那条路不能骗人"：
//  1. GET 报告永不写盘：查完 plan 之后读侧必须逐字不变（报告会被反复点，不能每点一次改一遍配置）；
//  2. POST 缺 authorize / authorize 错 → 400 且**什么都没写**（"缺确认就按默认执行"是本项目的老坑）；
//  3. POST 带正确 authorize → 真落盘，响应里的 residual 必须为 0，
//     否则就是"写了一半但接口回 ok"；再点一次必须判无待迁（幂等）；
//  4. 非管理员一律 403（这四个端点改的是判线基准与合成口径，不是展示层配置）。
//
// 锚点回填那一组还要多锁一条：回填完的 t_eligible 只数**首买价可证**的行——
// cost_fallback 档有锚点也没资格开 T 腿，把两者混进一个数就是 §8.4.5 那道门被读数面绕过。
//
// English: locks for the sign-off endpoints. GET never writes; a missing or wrong authorize token
// writes nothing and says so; a signed apply leaves zero residual and is idempotent; non-admins 403.
package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"quant-trading-v2/internal/auth"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/paper"
)

// newSignoffTestServer 独立配置文件 + 独立账本目录的 Server（迁移与回填都写盘，必须互不干扰）。
// config.json 预置成"旧物化默认"形状：mode=product、max_total_position_pct=0.80、已带 v1 标记。
// English: a server with its own config file seeded to the old materialized-default shape.
func newSignoffTestServer(t *testing.T) (*Server, *auth.User, string) {
	t.Helper()
	dir := t.TempDir()
	mgr := auth.NewManager(dir)
	if err := mgr.Init(); err != nil {
		t.Fatalf("auth init: %v", err)
	}
	admin, err := mgr.CreateUser("admin_signoff", "adminpw", auth.RoleAdmin, nil, 0)
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	cfgPath := filepath.Join(dir, "config.json")
	seed := map[string]any{"rules": map[string]any{"sizing": map[string]any{
		"enabled": true, "budget_mode": "dynamic", "mode": "product",
		"max_total_position_pct": 0.8, "policy_version": 1,
	}}}
	raw, err := json.Marshal(seed)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	cfgMgr := config.NewManager(cfgPath)
	cfgMgr.SetStore(mgr)
	s := &Server{auth: mgr, cfg: cfgMgr, mux: http.NewServeMux(), cacheDir: dir}
	s.registerRoutes()
	return s, admin, cfgPath
}

// v4ApplyView POST /api/config/sizing-v4-apply 的响应（只解断言需要的字段）。
type v4ApplyView struct {
	Applied v4Report `json:"applied"`
	Recheck struct {
		Changed int `json:"changed"`
	} `json:"recheck"`
	Residual int    `json:"residual"`
	AuthKey  string `json:"authorize_key"`
}

// v4Report 迁移总账（GET 的 report 与 POST 的 applied 同结构，共用一份解码）。
type v4Report struct {
	Apply   bool `json:"apply"`
	Changed int  `json:"changed"`
	Written int  `json:"written"`
	Failed  int  `json:"failed"`
	Rows    []struct {
		Layer      string  `json:"layer"`
		ModeBefore string  `json:"mode_before"`
		ModeAfter  string  `json:"mode_after"`
		CapBefore  float64 `json:"cap_before"`
		CapAfter   float64 `json:"cap_after"`
		Written    bool    `json:"written"`
		Changed    bool    `json:"changed"`
	} `json:"rows"`
}

func TestSizingV4SignoffAPI(t *testing.T) {
	s, admin, _ := newSignoffTestServer(t)

	// GET 的载荷把报告放在 report 键（POST 放在 applied 键），解码时对齐成同一个视图。
	type v4PlanView struct {
		Report  v4Report `json:"report"`
		AuthKey string   `json:"authorize_key"`
	}
	get := func() (int, v4ApplyView) {
		rr := adminDo(s, adminReq(s, admin, http.MethodGet, "/api/config/sizing-v4-plan", ""))
		var p v4PlanView
		_ = json.Unmarshal(rr.Body.Bytes(), &p)
		return rr.Code, v4ApplyView{Applied: p.Report, AuthKey: p.AuthKey}
	}
	post := func(body string) (int, v4ApplyView) {
		rr := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/config/sizing-v4-apply", body))
		var out v4ApplyView
		_ = json.Unmarshal(rr.Body.Bytes(), &out)
		return rr.Code, out
	}

	// ① GET：报告有前后值，且读侧一个字没变。
	code, plan := get()
	if code != http.StatusOK {
		t.Fatalf("GET 报告应 200，got %d", code)
	}
	if plan.Applied.Changed != 1 || len(plan.Applied.Rows) < 1 {
		t.Fatalf("报告应给出 1 处待迁，got %+v", plan.Applied)
	}
	// 报告里除全局行外还会有"自动跟随"的账号行（无账号级文档，本次不建键），故按层取。
	var row = struct {
		Layer      string  `json:"layer"`
		ModeBefore string  `json:"mode_before"`
		ModeAfter  string  `json:"mode_after"`
		CapBefore  float64 `json:"cap_before"`
		CapAfter   float64 `json:"cap_after"`
		Written    bool    `json:"written"`
		Changed    bool    `json:"changed"`
	}{}
	for _, r := range plan.Applied.Rows {
		if r.Layer == "global" {
			row = r
		}
	}
	if row.ModeBefore != "product" || row.ModeAfter != "power_mean" ||
		row.CapBefore != 0.80 || row.CapAfter != 1.00 || row.Written {
		t.Fatalf("报告前后值不符或错标已写盘，got %+v", row)
	}
	if plan.AuthKey != v4SignoffToken {
		t.Fatalf("报告应告知 authorize 值，got %q", plan.AuthKey)
	}
	if got := s.cfg.GetSizingConfig(); got.ModeOf() != "product" || got.MaxTotalPositionPct != 0.80 {
		t.Fatalf("GET 之后读侧被改动：mode=%q cap=%v", got.ModeOf(), got.MaxTotalPositionPct)
	}

	// ② POST 未签字：400 且什么都没写。
	for _, body := range []string{`{}`, `{"authorize":""}`, `{"authorize":"我看过了"}`} {
		if code, _ = post(body); code != http.StatusBadRequest {
			t.Fatalf("%s 应 400，got %d", body, code)
		}
		if got := s.cfg.GetSizingConfig(); got.ModeOf() != "product" {
			t.Fatalf("未签字的 POST 竟改写了配置（body=%s）", body)
		}
	}

	// ③ 签字落盘：真写、recheck 无遗留、读侧进新口径。
	code, out := post(`{"authorize":"` + v4SignoffToken + `"}`)
	if code != http.StatusOK {
		t.Fatalf("签字后应 200，got %d", code)
	}
	if out.Applied.Written != 1 || out.Applied.Failed != 0 {
		t.Fatalf("应写 1 处且无失败，got %+v", out.Applied)
	}
	if out.Residual != 0 {
		t.Fatalf("签字后不得有遗留可迁项，got residual=%d", out.Residual)
	}
	if got := s.cfg.GetSizingConfig(); got.ModeOf() != "power_mean" || got.MaxTotalPositionPct != 1.00 {
		t.Fatalf("签字后读侧未进新口径：mode=%q cap=%v", got.ModeOf(), got.MaxTotalPositionPct)
	}

	// ④ 幂等：再点一次签字，写入与需迁数都必须是 0（否则每次重启都在重刷资金参数）。
	_, again := post(`{"authorize":"` + v4SignoffToken + `"}`)
	if again.Applied.Changed != 0 || again.Applied.Written != 0 {
		t.Fatalf("第二次签字应判无待迁，got %+v", again.Applied)
	}

	// ⑤ 非管理员 403（四个端点都要挡）。
	member, err := s.auth.CreateUser("member_signoff", "memberpw", "user", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []struct{ method, path string }{
		{http.MethodGet, "/api/config/sizing-v4-plan"},
		{http.MethodPost, "/api/config/sizing-v4-apply"},
		{http.MethodGet, "/api/paper/anchor-backfill-plan"},
		{http.MethodPost, "/api/paper/anchor-backfill"},
	} {
		rr := adminDo(s, adminReq(s, member, p.method, p.path, `{}`))
		if rr.Code != http.StatusForbidden {
			t.Fatalf("非管理员 %s %s 应 403，got %d", p.method, p.path, rr.Code)
		}
	}
}

// legacyPaperBook 造一本"只有摊薄成本、没有层账首买价"的存量纸面账本（回填的靶子）。
// 走落盘再加载这条路而不是内部字段：加载期回填（只认 first_lot）也必须被测到——
// 若加载时就把成本写成锚点，签字面就形同虚设，而这件事只有从文件读回来才看得见。
func legacyPaperBook(t *testing.T) (*paper.Engine, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "paper.json")
	state := map[string]any{
		"cash": 50000.0,
		"positions": map[string]any{
			"600000": map[string]any{"code": "600000", "name": "浦发", "qty": 1000,
				"cost_price": 12.5, "cost": 12500.0, "mark": 13.0},
		},
	}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	pe := paper.New(paper.Config{Enabled: true, AutoSell: true, FixedAmount: 10000, InitialCapital: 100000}, path)
	return pe, path
}

func TestAnchorBackfillSignoffAPI(t *testing.T) {
	s, admin, _ := newSignoffTestServer(t)
	pe, path := legacyPaperBook(t)
	s.SetPaper(pe)

	// 加载期回填不许把成本写成锚点（只有签字后的显式回填才允许 cost_fallback）。
	if snap := pe.Positions(); len(snap) != 1 || snap[0].AnchorPrice != 0 {
		t.Fatalf("存量账本加载后锚点必须仍为空（成本档须签字），got %+v", snap)
	}

	rr := adminDo(s, adminReq(s, admin, http.MethodGet, "/api/paper/anchor-backfill-plan", ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("报告应 200，got %d: %s", rr.Code, rr.Body.String())
	}
	var plan map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	rows, _ := plan["plan"].([]interface{})
	if len(rows) != 1 {
		t.Fatalf("应列出 1 行待回填，got %v", plan["plan"])
	}
	first, _ := rows[0].(map[string]interface{})
	if src, _ := first["anchor_source"].(string); src != "cost_fallback" {
		t.Fatalf("无层账、无买入历史时应落到 cost_fallback，got %q", src)
	}
	// 前后触发价必须都给出来：签字单看的就是"这条线会移动多少"。
	if stop, _ := first["stop_after"].(float64); stop <= 0 {
		t.Fatalf("报告应给出回填后的止损触发价，got %+v", first)
	}

	// 未签字：400 且锚点仍为空。
	if rr := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/paper/anchor-backfill", `{"authorize":"x"}`)); rr.Code != http.StatusBadRequest {
		t.Fatalf("错误 authorize 应 400，got %d", rr.Code)
	}
	if snap := pe.Positions(); len(snap) == 1 && snap[0].AnchorPrice != 0 {
		t.Fatalf("未签字不得回填锚点，got %v", snap[0].AnchorPrice)
	}

	// 签字：回填 1 条，但 t_eligible 必须是 0（成本档没有开 T 资格）。
	rr = adminDo(s, adminReq(s, admin, http.MethodPost, "/api/paper/anchor-backfill",
		`{"authorize":"`+v4SignoffToken+`"}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("签字后应 200，got %d: %s", rr.Code, rr.Body.String())
	}
	var out map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if filled, _ := out["filled"].(float64); filled != 1 {
		t.Fatalf("应回填 1 条，got %v", out["filled"])
	}
	if elig, _ := out["t_eligible"].(float64); elig != 0 {
		t.Fatalf("cost_fallback 档不得有开 T 资格，got t_eligible=%v", out["t_eligible"])
	}
	if snap := pe.Positions(); len(snap) != 1 || snap[0].AnchorPrice != 12.5 {
		t.Fatalf("回填后锚点应为成本 12.5，got %+v", snap)
	}
	// 落盘自证：账本文件里真带着锚点与来源（内存对了盘没对＝重启回退）。
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "cost_fallback") {
		t.Fatalf("回填后账本应写明 anchor_source=cost_fallback，got %s", string(raw))
	}

	// 幂等 + 报告转空：第二次签字回填 0 条，plan 里不再有待回填行。
	rr = adminDo(s, adminReq(s, admin, http.MethodPost, "/api/paper/anchor-backfill",
		`{"authorize":"`+v4SignoffToken+`"}`))
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if filled, _ := out["filled"].(float64); filled != 0 {
		t.Fatalf("第二次回填应为 0（幂等），got %v", out["filled"])
	}
	rr = adminDo(s, adminReq(s, admin, http.MethodGet, "/api/paper/anchor-backfill-plan", ""))
	if err := json.Unmarshal(rr.Body.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if rows, _ = plan["plan"].([]interface{}); len(rows) != 0 {
		t.Fatalf("回填后计划应转空，got %v", plan["plan"])
	}
}
