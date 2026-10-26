// regime_config_api_test.go — §N6-RISK（2026-10-21）信号生成层开关的 HTTP 面与落盘行为锁。
//
// 这一组开关（enhance / sizing / macro_gate）决定「宏观·大盘·风险情绪」怎么进入信号生成，
// owner 裁决要求「全部可配 + 前端有开关」。面板写得进、引擎吃得到，靠的就是
// GET 回显生效值 + POST 稀疏 merge + 落盘 这三件事，而它们此前**一条断言都没有**。
// 本文件钉六件事：
//  1. GET 必须回显**补齐出厂默认后的生效值**（配置文件没写 rules.sizing 时，
//     不能把零值 Red 权重/广度降权送给前端——那会让面板显示 0，而引擎实际按 0.30/0.90 跑）；
//  2. POST 稀疏 merge 只改出现的键，未出现的键与**同段兄弟键**必须保留（抹平事故的源头）；
//  3. 嵌套 map（tier_scales）按**键级**合并，只发 Red 不得清掉 Yellow；
//  4. 写侧落盘：换一个 Manager 读同一份文件必须看到新值（否则页面改完引擎重启回退）；
//  5. macro_gate 借战法配置的 merge 通道写入，red_block_new_buy 逐键往返；
//  6. 鉴权与坏输入：非 admin 403、非法 JSON / 类型不符 400（绝不半写就回 ok）。
//
// English: HTTP-surface and persistence locks for the §N6-RISK signal-layer switches — GET echoes
// defaults-filled effective values, POST sparse-merges per key (including nested maps), writes
// persist to disk, macro_gate round-trips through the strategy merge channel, and auth/bad-input
// fail closed.
package server

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"quant-trading-v2/internal/auth"
	"quant-trading-v2/internal/config"
)

// newRegimeTestServer 装配指向独立配置文件的 Server（每次独立 TempDir，避免与同包其它用例抢账本）。
// English: a Server bound to its own config file under TempDir.
func newRegimeTestServer(t *testing.T) (*Server, *auth.User, string) {
	t.Helper()
	dir := t.TempDir()
	mgr := auth.NewManager(dir)
	if err := mgr.Init(); err != nil {
		t.Fatalf("auth init: %v", err)
	}
	admin, err := mgr.CreateUser("admin", "adminpw", auth.RoleAdmin, nil, 0)
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	cfgPath := filepath.Join(dir, "config.json")
	cfgMgr := config.NewManager(cfgPath)
	cfgMgr.SetStore(mgr)
	s := &Server{auth: mgr, cfg: cfgMgr, mux: http.NewServeMux()}
	s.registerRoutes()
	return s, admin, cfgPath
}

// decodeRegime 解析 GET /api/config/regime 的三段载荷。
// English: decodes the three payload sections of GET /api/config/regime.
func decodeRegime(t *testing.T, body string) (enhance map[string]any, sizing map[string]any, macro map[string]any) {
	t.Helper()
	var got struct {
		Enhance   map[string]any `json:"enhance"`
		Sizing    map[string]any `json:"sizing"`
		MacroGate map[string]any `json:"macro_gate"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("载荷解析失败: %v body=%s", err, body)
	}
	if got.Enhance == nil || got.Sizing == nil {
		t.Fatalf("enhance/sizing 段缺失（前端会拿到 undefined 而不是生效值）: %s", body)
	}
	return got.Enhance, got.Sizing, got.MacroGate
}

// TestRegimeConfigGetEchoesDefaults 出厂空配置下 GET 必须回显补齐后的生效值。
// 反证靶心：Red 金额权重若回 0，面板显示 0 而引擎按 0.30 跑——「页面写的≠引擎吃的」正是本仓
// 反复锤实的派生状态撒谎形态。
// English: with an untouched config file, GET must report defaults-filled values, never raw zeros.
func TestRegimeConfigGetEchoesDefaults(t *testing.T) {
	s, admin, _ := newRegimeTestServer(t)
	rr := adminDo(s, adminReq(s, admin, http.MethodGet, "/api/config/regime", ""))
	if rr.Code != 200 {
		t.Fatalf("GET /api/config/regime 应 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	_, sizing, _ := decodeRegime(t, rr.Body.String())
	tiers, _ := sizing["tier_scales"].(map[string]any)
	if tiers == nil {
		t.Fatalf("tier_scales 未回显: %v", sizing)
	}
	if got, _ := tiers["Red"].(float64); got != 0.30 {
		t.Fatalf("Red 金额权重应回显默认生效值 0.30（不是零值 0）, got %v", tiers["Red"])
	}
	if got, _ := tiers["Yellow"].(float64); got != 0.60 {
		t.Fatalf("Yellow 金额权重应回显 0.60, got %v", tiers["Yellow"])
	}
	if got, _ := sizing["breadth_missing_scale"].(float64); got != 0.90 {
		t.Fatalf("广度缺失降权应回显 0.90, got %v", sizing["breadth_missing_scale"])
	}
	if lvl, _ := sizing["sell_boost_level"].(string); lvl != "L2" {
		t.Fatalf("卖出加权档位应回显 L2（owner 裁决默认档）, got %v", lvl)
	}
	// §DYN-GLOBAL（2026-10-24 裁决一）：三把交付键的出厂态全部反转，GET 必须把**新**默认回显出来
	// （面板据此显示初态；回显旧默认＝页面显示"没开"而引擎其实在按动态缩放下单）。
	if on, _ := sizing["enabled"].(bool); !on {
		t.Fatal("§DYN-GLOBAL：sizing.enabled 出厂必须回显 true（动态仓位全局默认开，legacy 只剩账号级退路）")
	}
	if mode, _ := sizing["budget_mode"].(string); mode != "dynamic" {
		t.Fatalf("§DYN-GLOBAL：sizing.budget_mode 出厂必须回显 dynamic, got %v", sizing["budget_mode"])
	}
	if sh, _ := sizing["shadow"].(bool); sh {
		t.Fatal("§DYN-GLOBAL：sizing.shadow 出厂必须回显 false（缩放真下发，不是只留痕）")
	}
}

// TestRegimeConfigSparseMergeKeepsSiblings POST 只带 enhance 时，已写入的 sizing 值不得被抹平。
// English: a PATCH carrying only enhance must leave previously stored sizing values intact.
func TestRegimeConfigSparseMergeKeepsSiblings(t *testing.T) {
	s, admin, _ := newRegimeTestServer(t)
	// 先写 sizing：把 Red 权重改成 0.5、广度降权改成 0.8。
	rr := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/config/regime",
		`{"sizing":{"tier_scales":{"Red":0.5},"breadth_missing_scale":0.8}}`))
	if rr.Code != 200 {
		t.Fatalf("写 sizing 应 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	// 再**只**写 enhance——sizing 一个键都没提，必须原样保留。
	rr2 := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/config/regime",
		`{"enhance":{"market_state_enabled":true}}`))
	if rr2.Code != 200 {
		t.Fatalf("写 enhance 应 200, got %d body=%s", rr2.Code, rr2.Body.String())
	}
	enh, sizing, _ := decodeRegime(t, rr2.Body.String())
	if on, _ := enh["market_state_enabled"].(bool); !on {
		t.Fatalf("market_state_enabled 未写入: %v", enh)
	}
	tiers, _ := sizing["tier_scales"].(map[string]any)
	if got, _ := tiers["Red"].(float64); got != 0.5 {
		t.Fatalf("只发 enhance 却把 sizing.tier_scales.Red 抹回默认（稀疏 merge 失效）, got %v", tiers["Red"])
	}
	if got, _ := sizing["breadth_missing_scale"].(float64); got != 0.8 {
		t.Fatalf("breadth_missing_scale 被抹平, got %v", sizing["breadth_missing_scale"])
	}
	// 兄弟键 Yellow 全程没发过，必须仍是默认 0.60（键级合并的旁证）。
	if got, _ := tiers["Yellow"].(float64); got != 0.60 {
		t.Fatalf("Yellow 权重应保持默认 0.60, got %v", tiers["Yellow"])
	}
}

// TestRegimeConfigNestedMapKeyMerge tier_scales 只发 Red 时 Yellow 必须存活；反向同证。
// 深合并若退化成「整map 替换」，前端提交一份只显示 Red 的载荷就会清掉运维另配的 Yellow。
// English: nested maps merge key-by-key — sending only Red must keep Yellow, and vice versa.
func TestRegimeConfigNestedMapKeyMerge(t *testing.T) {
	s, admin, _ := newRegimeTestServer(t)
	if rr := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/config/regime",
		`{"sizing":{"tier_scales":{"Yellow":0.45}}}`)); rr.Code != 200 {
		t.Fatalf("首次写应 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	rr := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/config/regime",
		`{"sizing":{"tier_scales":{"Red":0.25}}}`))
	_, sizing, _ := decodeRegime(t, rr.Body.String())
	tiers, _ := sizing["tier_scales"].(map[string]any)
	if got, _ := tiers["Red"].(float64); got != 0.25 {
		t.Fatalf("Red 未按键写入, got %v", tiers["Red"])
	}
	if got, _ := tiers["Yellow"].(float64); got != 0.45 {
		t.Fatalf("只发 Red 把上一轮 Yellow=0.45 顶掉（嵌套 map 被整体替换）, got %v", tiers["Yellow"])
	}
}

// TestRegimeConfigPersistsToDisk 写入必须落盘：换一个 Manager 读同一份文件要看到新值。
// 反证形态＝页面改成功、进程重启后引擎吃回旧值（§RestartBeforeVerify 同族）。
// English: writes must hit disk — a fresh Manager over the same file sees the new values.
func TestRegimeConfigPersistsToDisk(t *testing.T) {
	s, admin, cfgPath := newRegimeTestServer(t)
	// §DYN-GLOBAL 后 enabled=true 是默认态、测不出"落盘"，所以这里写的是**账号级显式退路**
	// （enabled=false + budget_mode=legacy）：它必须原样落盘并在重启后仍读成关——
	// 这正是把 Enabled 改成指针要守住的那一态（缺键=跟随全局开，显式 false=恒关）。
	body := `{"sizing":{"enabled":false,"budget_mode":"legacy","sell_boost_level":"L3","breadth_missing_scale":0.7},` +
		`"enhance":{"market_state_enabled":true}}`
	if rr := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/config/regime", body)); rr.Code != 200 {
		t.Fatalf("写入应 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	// §A0：sizing 已改为账号专用键，落盘验证必须挂回同一 KV 存储再读账号配置；
	// 全局 config.json 只承载 enhance/macro_gate。
	reloaded := config.NewManager(cfgPath)
	store2 := auth.NewManager(filepath.Dir(cfgPath))
	if err := store2.Init(); err != nil {
		t.Fatalf("reopen auth store: %v", err)
	}
	reloaded.SetStore(store2)
	got, _ := reloaded.GetSizingConfigFor(admin.ID)
	if got.EnabledOn() {
		t.Fatal("账号级显式 enabled=false 未落盘（重启后动态仓位会自己抬成开）")
	}
	if got.BudgetModeOf() != "legacy" {
		t.Fatalf("账号级 budget_mode=legacy 未落盘，got %q", got.BudgetModeOf())
	}
	if got.SellBoostLevelOf() != "L3" {
		t.Fatalf("sell_boost_level 未落盘, got %q", got.SellBoostLevelOf())
	}
	if got.BreadthMissingScale != 0.7 {
		t.Fatalf("breadth_missing_scale 未落盘, got %v", got.BreadthMissingScale)
	}
	if !reloaded.GetEnhanceConfig().MarketState {
		t.Fatal("enhance.market_state_enabled 未落盘")
	}
	// 落盘的是原始写入值，展示层再补默认——两侧都得对：引擎读的 sizingConfigFor 也走 WithDefaults。
	if v, ok := got.TierScales["Red"]; !ok || v != 0.30 {
		t.Fatalf("未显式写过的 Red 应经 WithDefaults 补回 0.30, got %v ok=%v", got.TierScales["Red"], ok)
	}
}

// TestRegimeConfigFineSizingRoundTrip §SIZING-FINE（2026-10-22）：面板新暴露的精细参数必须
// 走同一对通道——GET 回显补齐后的生效值、POST 稀疏 merge 后逐键落盘。这些键此前只在
// config.json 里能改，页面上一个都拨不到；若 GET 漏键，面板会把「没读到」显示成空/0，
// 用户一保存就用零值覆盖配置（§N6-RISK 同族事故）。
// English: every fine-grained sizing key surfaced by the panel round-trips through GET/POST,
// and unmentioned sibling keys survive the sparse merge.
func TestRegimeConfigFineSizingRoundTrip(t *testing.T) {
	s, admin, cfgPath := newRegimeTestServer(t)
	rr := adminDo(s, adminReq(s, admin, http.MethodGet, "/api/config/regime", ""))
	if rr.Code != 200 {
		t.Fatalf("GET 应 200, got %d", rr.Code)
	}
	_, sizing, _ := decodeRegime(t, rr.Body.String())
	num := func(key string) float64 {
		t.Helper()
		v, ok := sizing[key].(float64)
		if !ok {
			t.Fatalf("GET 未回显 %s（面板会把它当未配置）: %v", key, sizing)
		}
		return v
	}
	// 标量：出厂默认逐键核对（值取自 DefaultSizingConfig）。
	for key, want := range map[string]float64{
		"floor": 0.15, "cap": 1.15,
		"kelly_lambda": 0.5, "kelly_min_prob": 0.52,
		"vol_target_pct": 2.0, "vol_clamp_lo": 0.5, "vol_clamp_hi": 1.5,
		"macro_lead_days": 5, "macro_event_day_scale": 0.30,
		"llm_bearish_scale": 0.5,
		// §DYN-SIZE v4：max_total_position_pct 的默认从 0.80 抬到 **1.00**（语义已从"逐笔缩放乘子"
		// 改为"目标仓位天花板"，owner 允许牛市满仓）；新增 per_trade_position_pct=0.10 作为单笔基准。
		"max_total_position_pct": 1.00, "per_trade_position_pct": 0.10, "max_drawdown_pct": 0.10,
		"stoploss_guard_count": 4, "stoploss_guard_window_days": 1,
		"sl_offset_per_pct": 3, "sl_offset_cap": 4,
		"tp_offset_per_pct": 3, "tp_offset_cap": 5,
		"sl_offset_min_index_drop": 1, "bear_sl_tighten_pt": 1,
	} {
		if got := num(key); got != want {
			t.Fatalf("GET %s 默认值应 %v, got %v", key, want, got)
		}
	}
	// 映射：每张表补默认而非整表替换（缺键的面板会显示空）。
	m := func(key string) map[string]any {
		t.Helper()
		v, ok := sizing[key].(map[string]any)
		if !ok {
			t.Fatalf("GET 未回显映射 %s: %v", key, sizing)
		}
		return v
	}
	if v, _ := m("emotion_scales")["退潮"].(float64); v != 0.50 {
		t.Fatalf("emotion_scales[退潮] 应 0.50, got %v", v)
	}
	if v, _ := m("macro_window_scales")["high"].(float64); v != 0.50 {
		t.Fatalf("macro_window_scales[high] 应 0.50, got %v", v)
	}
	if v, _ := m("holiday_scales")["long1"].(float64); v != 0.40 {
		t.Fatalf("holiday_scales[long1] 应 0.40, got %v", v)
	}
	if v, _ := m("index_scale_thresholds")["deep"].(float64); v != 0.40 {
		t.Fatalf("index_scale_thresholds[deep] 应 0.40, got %v", v)
	}
	curve, _ := sizing["macro_lead_curve"].([]any)
	if len(curve) != 5 || curve[0] != 0.90 {
		t.Fatalf("macro_lead_curve 应回显默认 5 档且首档 0.90, got %v", curve)
	}

	// 写入一组非默认精细值：标量、映射单键、切片、整数、两个 *bool 开关。
	body := `{"sizing":{"floor":0.20,"kelly_lambda":0.35,"emotion_scales":{"退潮":0.40},` +
		`"macro_lead_days":3,"macro_lead_curve":[0.80,0.60,0.40],` +
		`"stoploss_guard_count":6,"max_drawdown_pct":0.15,` +
		`"holiday_block_new":true,"sl_offset_risk_budget_keep":false}}`
	rr2 := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/config/regime", body))
	if rr2.Code != 200 {
		t.Fatalf("写精细参数应 200, got %d body=%s", rr2.Code, rr2.Body.String())
	}
	_, sizing2, _ := decodeRegime(t, rr2.Body.String())
	if v, _ := sizing2["floor"].(float64); v != 0.20 {
		t.Fatalf("floor 未回显, got %v", sizing2["floor"])
	}
	if v, _ := sizing2["kelly_lambda"].(float64); v != 0.35 {
		t.Fatalf("kelly_lambda 未回显, got %v", sizing2["kelly_lambda"])
	}
	if v, _ := sizing2["stoploss_guard_count"].(float64); v != 6 {
		t.Fatalf("stoploss_guard_count 未回显, got %v", sizing2["stoploss_guard_count"])
	}
	curve2, _ := sizing2["macro_lead_curve"].([]any)
	if len(curve2) != 3 || curve2[0] != 0.80 || curve2[2] != 0.40 {
		t.Fatalf("macro_lead_curve 未按新长度回显, got %v", curve2)
	}
	// 落盘侧读回：新值在、未提过的兄弟键仍是默认（稀疏 merge 的旁证）。
	// §A0：sizing 住账号专用键，必须挂回 KV 存储再读。
	reloaded := config.NewManager(cfgPath)
	store2 := auth.NewManager(filepath.Dir(cfgPath))
	if err := store2.Init(); err != nil {
		t.Fatalf("reopen auth store: %v", err)
	}
	reloaded.SetStore(store2)
	got, _ := reloaded.GetSizingConfigFor(admin.ID)
	if got.Floor != 0.20 || got.KellyLambda != 0.35 || got.StoplossGuardCount != 6 {
		t.Fatalf("精细参数未落盘: floor=%v kelly=%v guard=%v", got.Floor, got.KellyLambda, got.StoplossGuardCount)
	}
	if got.MacroLeadDays != 3 || len(got.MacroLeadCurve) != 3 || got.MacroLeadCurve[2] != 0.40 {
		t.Fatalf("前瞻曲线未落盘: days=%v curve=%v", got.MacroLeadDays, got.MacroLeadCurve)
	}
	if got.EmotionScales["退潮"] != 0.40 || got.EmotionScales["背离"] != 0.60 {
		t.Fatalf("emotion_scales 应只改退潮且保留背离, got %v", got.EmotionScales)
	}
	if got.MaxDrawdownPct != 0.15 {
		t.Fatalf("max_drawdown_pct 未落盘, got %v", got.MaxDrawdownPct)
	}
	if got.HolidayBlockNew == nil || !*got.HolidayBlockNew {
		t.Fatal("holiday_block_new 未落盘为 true")
	}
	if got.SLOffsetRiskBudgetKeep == nil || *got.SLOffsetRiskBudgetKeep {
		t.Fatal("sl_offset_risk_budget_keep 未落盘为 false（指针被当成未设置就退回默认 true）")
	}
}

// TestRegimeConfigMacroGateRoundTrip macro_gate 走战法配置合并通道，red_block_new_buy 必须
// 逐键往返，且置信度门槛与硬拦子开关互不影响。
// English: macro_gate rides the strategy-config merge; red_block_new_buy round-trips per key.
func TestRegimeConfigMacroGateRoundTrip(t *testing.T) {
	s, admin, cfgPath := newRegimeTestServer(t)
	rr := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/config/regime",
		`{"macro_gate":{"red_block_new_buy":true,"yellow_confidence":0.88}}`))
	if rr.Code != 200 {
		t.Fatalf("写 macro_gate 应 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	_, _, macro := decodeRegime(t, rr.Body.String())
	if on, _ := macro["red_block_new_buy"].(bool); !on {
		t.Fatalf("red_block_new_buy 未回显: %v", macro)
	}
	if c, _ := macro["yellow_confidence"].(float64); c != 0.88 {
		t.Fatalf("yellow_confidence 未回显: %v", macro["yellow_confidence"])
	}
	// 落盘侧按访问器读：应急阀为 true、Yellow 门槛为 0.88，而 Red 门槛没发过应保持默认 0.92。
	mg := config.NewManager(cfgPath).GetStrategyConfig().MacroGate
	if !mg.RedBlockNewBuyOn() {
		t.Fatal("red_block_new_buy 未落盘，应急阀页面拨了引擎不吃")
	}
	if mg.YellowMinConf() != 0.88 {
		t.Fatalf("Yellow 门槛落盘值错误, got %v", mg.YellowMinConf())
	}
	if mg.RedMinConf() != 0.92 {
		t.Fatalf("未发过的 Red 门槛应保留默认 0.92, got %v", mg.RedMinConf())
	}
	// 关掉应急阀：*bool 必须能被显式写回 false（nil 与 false 语义不同，锁死这条）。
	rr2 := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/config/regime",
		`{"macro_gate":{"red_block_new_buy":false}}`))
	_, _, macro2 := decodeRegime(t, rr2.Body.String())
	if v, present := macro2["red_block_new_buy"]; present && v != false {
		t.Fatalf("应急阀显式关失败, got %v", v)
	}
	if config.NewManager(cfgPath).GetStrategyConfig().MacroGate.RedBlockNewBuyOn() {
		t.Fatal("显式写 false 后 RedBlockNewBuyOn 仍为 true（指针未落到 false）")
	}
}

// TestRegimeConfigAccountPermissions §A0 权限矩阵：admin 任意账号；本人可读写自己的 sizing；
// 他人账号 403；enhance/macro_gate 是系统级，普通账号写入 403。
// English: §A0 — admins may target any account; a user may read/write their own sizing; other
// accounts are 403; the system-level enhance/macro_gate sections stay admin-only.
func TestRegimeConfigAccountPermissions(t *testing.T) {
	s, admin, _ := newRegimeTestServer(t)
	member, err := s.auth.CreateUser("member-regime", "pw", auth.RoleUser, nil, 0)
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	other, err := s.auth.CreateUser("other-regime", "pw", auth.RoleUser, nil, 0)
	if err != nil {
		t.Fatalf("create other: %v", err)
	}
	// 本人读自己：200。
	if rr := adminDo(s, adminReq(s, member, http.MethodGet, "/api/config/regime", "")); rr.Code != 200 {
		t.Fatalf("本人 GET 自己应 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	// 本人写自己的 sizing：200，且只影响自己。
	// §DYN-GLOBAL 后 enabled 不再是可用的隔离探针（所有账号默认都读成开），
	// 改用只有"本人写过"的 floor 值做证据。
	rr := adminDo(s, adminReq(s, member, http.MethodPost, "/api/config/regime",
		`{"sizing":{"enabled":true,"shadow":false,"floor":0.42}}`))
	if rr.Code != 200 {
		t.Fatalf("本人写自己 sizing 应 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if got, _ := s.cfg.GetSizingConfigFor(member.ID); !got.EnabledOn() || got.Floor != 0.42 {
		t.Fatalf("本人写入未生效: enabled=%v floor=%v", got.EnabledOn(), got.Floor)
	}
	if got, _ := s.cfg.GetSizingConfigFor(admin.ID); got.Floor == 0.42 {
		t.Fatal("本人写入污染了 admin 的账号配置")
	}
	// 本人读/写他人：403。
	if rr := adminDo(s, adminReq(s, member, http.MethodGet, "/api/config/regime?account_id="+other.ID, "")); rr.Code != 403 {
		t.Fatalf("本人 GET 他人应 403, got %d", rr.Code)
	}
	if rr := adminDo(s, adminReq(s, member, http.MethodPost, "/api/config/regime?account_id="+other.ID,
		`{"sizing":{"enabled":true,"floor":0.42}}`)); rr.Code != 403 {
		t.Fatalf("本人写他人应 403, got %d body=%s", rr.Code, rr.Body.String())
	}
	if got, _ := s.cfg.GetSizingConfigFor(other.ID); got.Floor == 0.42 {
		t.Fatal("被 403 拒绝的写入仍生效（鉴权挡在副作用之后）")
	}
	// 普通账号写系统级段：403，且全局 enhance 不变。
	beforeEnh := s.cfg.GetEnhanceConfig()
	if rr := adminDo(s, adminReq(s, member, http.MethodPost, "/api/config/regime",
		`{"enhance":{"market_state_enabled":true}}`)); rr.Code != 403 {
		t.Fatalf("普通账号写 enhance 应 403, got %d", rr.Code)
	}
	if s.cfg.GetEnhanceConfig().MarketState != beforeEnh.MarketState {
		t.Fatal("被 403 拒绝的系统级写入仍生效")
	}
	// admin 代配他人：200。
	if rr := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/config/regime?account_id="+other.ID,
		`{"sizing":{"enabled":true,"floor":0.43}}`)); rr.Code != 200 {
		t.Fatalf("admin 代配他人应 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if got, _ := s.cfg.GetSizingConfigFor(other.ID); !got.EnabledOn() || got.Floor != 0.43 {
		t.Fatalf("admin 代配未生效: enabled=%v floor=%v", got.EnabledOn(), got.Floor)
	}
}

// TestRegimeConfigShadowEcho §A2：GET 必须显式回显 effective shadow（键不许缺失），
// 前端不再靠“字段缺失”推断默认；响应同时带回 account_id 与 sizing_scope。
// §DYN-GLOBAL 换了默认方向（影子默认**关**），所以三腿顺序也反过来拨：
// 缺省 false → 显式 true 必须看得见 → 再显式 false 必须落得回去。若沿用旧顺序（缺省 true→写 false），
// 第一腿和第三腿读数相同，这条锁就再也区分不了"有人显式关过影子"和"没人动过"。
// English: §A2 — GET always echoes an explicit effective shadow; §DYN-GLOBAL flipped the default to
// non-shadow, so the legs now run absent→true→false to keep the lock discriminating.
func TestRegimeConfigShadowEcho(t *testing.T) {
	s, admin, _ := newRegimeTestServer(t)

	shadowOf := func(t *testing.T, body string) bool {
		t.Helper()
		var got struct {
			AccountID   string         `json:"account_id"`
			SizingScope string         `json:"sizing_scope"`
			Sizing      map[string]any `json:"sizing"`
		}
		if err := json.Unmarshal([]byte(body), &got); err != nil {
			t.Fatalf("载荷解析失败: %v body=%s", err, body)
		}
		if got.AccountID != admin.ID || got.SizingScope != "account" {
			t.Fatalf("账号身份回显不对: account_id=%q scope=%q", got.AccountID, got.SizingScope)
		}
		v, ok := got.Sizing["shadow"].(bool)
		if !ok {
			t.Fatalf("shadow 未被显式回显（缺省/非布尔）: %v", got.Sizing["shadow"])
		}
		return v
	}

	rr := adminDo(s, adminReq(s, admin, http.MethodGet, "/api/config/regime", ""))
	if rr.Code != 200 {
		t.Fatalf("GET 应 200, got %d", rr.Code)
	}
	if shadowOf(t, rr.Body.String()) {
		t.Fatal("§DYN-GLOBAL：空配置的 effective shadow 应为 false（影子默认关，缩放真下发）")
	}

	// 拨到与默认相反的一侧——默认已经是 false，再写 false 就区分不出"有人显式关过"。
	rr = adminDo(s, adminReq(s, admin, http.MethodPost, "/api/config/regime", `{"sizing":{"shadow":true}}`))
	if rr.Code != 200 {
		t.Fatalf("写 shadow=true 应 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if !shadowOf(t, rr.Body.String()) {
		t.Fatal("显式 true 后回显仍为 false（写侧没吃到）")
	}
	rr = adminDo(s, adminReq(s, admin, http.MethodGet, "/api/config/regime", ""))
	if !shadowOf(t, rr.Body.String()) {
		t.Fatal("GET 未回显已保存的 shadow=true")
	}

	// 收回：显式 false 必须压过"曾经 true"的旧值并落住。
	rr = adminDo(s, adminReq(s, admin, http.MethodPost, "/api/config/regime", `{"sizing":{"shadow":false}}`))
	if rr.Code != 200 {
		t.Fatalf("写 shadow=false 应 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if shadowOf(t, rr.Body.String()) {
		t.Fatal("显式 false 后回显仍为 true")
	}
	rr = adminDo(s, adminReq(s, admin, http.MethodGet, "/api/config/regime", ""))
	if shadowOf(t, rr.Body.String()) {
		t.Fatal("GET 未回显已保存的 shadow=false")
	}
}

// TestRegimeConceptConfigRoundTrip §C1-V2：concept 段随 regime API 往返（系统级，admin 写）。
// English: §C1-V2 — the concept section round-trips through the regime API (system-level, admin-only).
func TestRegimeConceptConfigRoundTrip(t *testing.T) {
	s, admin, _ := newRegimeTestServer(t)
	rr := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/config/regime",
		`{"concept":{"enabled":true,"shadow":false,"min_coverage_pct":97,"max_staleness_hours":24}}`))
	if rr.Code != 200 {
		t.Fatalf("写 concept 应 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	var got struct {
		Concept map[string]any `json:"concept"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("解析: %v", err)
	}
	if got.Concept == nil || got.Concept["enabled"] != true || got.Concept["min_coverage_pct"] != float64(97) {
		t.Fatalf("concept 未回显: %v", got.Concept)
	}
	if c := s.cfg.GetConceptConfig(); !c.Enabled || c.MinCoverage() != 97 || c.MaxStaleness() != 24 {
		t.Fatalf("concept 未落库: %+v", c)
	}
}

// TestRegimeConfigBadInput 非法 JSON / 类型不符必须 400，且不得半写。
// 类型不符尤其关键：mergeStructPatch 靠最后一步反序列化校验，若放行会把 "0.9" 这类字符串
// 塞进 float64 字段然后静默丢掉。
// English: malformed JSON and type mismatches return 400 and never half-apply.
func TestRegimeConfigBadInput(t *testing.T) {
	s, admin, _ := newRegimeTestServer(t)
	before := s.cfg.GetSizingConfig()
	cases := map[string]string{
		"非法JSON":      `not-json`,
		"权重类型不符":      `{"sizing":{"tier_scales":{"Red":"cheap"}}}`,
		"开关类型不符":      `{"sizing":{"enabled":"yes"}}`,
		"enhance类型不符": `{"enhance":{"market_state_enabled":123}}`,
	}
	for name, body := range cases {
		rr := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/config/regime", body))
		if rr.Code != 400 {
			t.Fatalf("[%s] 非法载荷应 400, got %d body=%s", name, rr.Code, rr.Body.String())
		}
		if !strings.Contains(rr.Body.String(), "error") && rr.Body.String() == "" {
			t.Fatalf("[%s] 400 未带回原因（前端只会显示一块空白红条）", name)
		}
	}
	after := s.cfg.GetSizingConfig()
	// §DYN-GLOBAL：Enabled 改成了 *bool，裸比指针比的是地址（每次 WithDefaults 都可能是新指针），
	// 必须按语义值比，否则这条"非法请求不得改动配置"的锁会恒红。
	if after.TierScales["Red"] != before.TierScales["Red"] || after.EnabledOn() != before.EnabledOn() {
		t.Fatalf("非法请求把配置改动了: before=%v/%v after=%v/%v",
			before.TierScales["Red"], before.EnabledOn(), after.TierScales["Red"], after.EnabledOn())
	}
}

// TestRegimeConfigWriteIsRollbackable 风控开关写入必须有写前快照（可回滚）且落对账。
// 本批开工时的真实缺陷：/api/config/regime 用了未登记的表面名 "regime"，
// SnapshotBeforeWrite 直接报「未知配置面」——写照样落库、日志刷一条 [P1] 不可回滚，
// 而 handler 注释写着"写前经统一配置快照入口留痕"。留痕失败却回 ok，正是本仓反复锤实的
// 「降级报成功」形态：owner 拨了 Red 应急阀发现拨错，回滚点根本不存在。
// 另一侧基线 before 也必须取到（与快照同作用域），否则写后字段级 diff 恒空、审计退化成
// "只证明调用过"。
// English: regime writes must take a pre-write snapshot against the right ledger. The channel
// originally used an unregistered surface name, so every risk-switch write succeeded with no
// rollback point while the handler claimed it was snapshotted.
func TestRegimeConfigWriteIsRollbackable(t *testing.T) {
	s, admin, _ := newRegimeTestServer(t)
	// 先拨一次让 config.json 存在。原因如实记下：SnapshotRules 是直接读配置文件原文，
	// 文件还没落地时取不到基线（os.ReadFile 报 no such file），这条对**所有**全局配置面
	// 一视同仁，属既有边缘行为、非本批引入——现网 config.json 恒存在，故不记成缺陷，
	// 只在本用例里绕开，免得把「快照语义」这场大改混进风控批。
	// §A0 后 sizing 写账号专用键、不再落地全局 config.json；这里改用 enhance 预置，
	// 让全局配置文件先存在（本用例验证的是 regime 全局快照面）。
	if rr := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/config/regime",
		`{"enhance":{"market_state_enabled":true}}`)); rr.Code != 200 {
		t.Fatalf("预置写入应 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	tr := s.snapshotConfigWrite(admin.ID, admin.ID, "regime")
	if tr.err != nil {
		t.Fatalf("regime 配置面未登记，写入不可回滚: %v", tr.err)
	}
	if tr.name == "" {
		t.Fatal("快照名为空＝这次变更没有回滚点")
	}
	// 作用域必须是 global：三段（enhance/sizing/macro_gate）都住全局 config.json。
	// 翻错账比没账更坏——回滚会把没被改过的文档滚回旧态，真正改动的原封不动。
	if tr.ledger != "global" {
		t.Fatalf("regime 落账作用域应为 global（实际三段都写全局 config.json）, got %q", tr.ledger)
	}
	// 另一侧基线也得取到（与快照同作用域），否则写后字段级 diff 恒空、审计退化成
	// "只证明调用过"。
	if len(tr.before) == 0 {
		t.Fatal("写前原文基线为空——写后 diff 会恒为「无变更」，审计只剩自证调用")
	}
	if !strings.Contains(string(tr.before), "sizing") {
		t.Fatalf("基线文档不含 sizing 段（取错册）: %.200s", tr.before)
	}
}

// TestUnknownConfigSurfaceStillRejected 反向腿：登记表不能被"反正都登记了"撑成放行任意表面名。
// 未登记表面名必须报错——这条正是上一枚用例赖以发现缺陷的那道闸，摘掉它以后新增野路通道
// 会静默获得"看起来有留痕"的待遇。
// English: an unregistered surface must still error — that is the very gate that caught the defect.
func TestUnknownConfigSurfaceStillRejected(t *testing.T) {
	s, admin, _ := newRegimeTestServer(t)
	tr := s.snapshotConfigWrite(admin.ID, admin.ID, "regime-not-registered")
	if tr.err == nil {
		t.Fatal("未登记配置面竟拿到快照（登记表失去意义）")
	}
	if !strings.Contains(tr.err.Error(), "未知配置面") {
		t.Fatalf("报错语义应为未知配置面, got %v", tr.err)
	}
}

// TestRegimeConfigEmptyBodyIsNoOp 三段全空（`{}`）必须 200 且逐字不改——
// 面板在「没改动就点保存」时会发出这种载荷，若它把默认值折叠进载荷就等于覆盖运维配置。
// English: an empty patch is a no-op (the panel may legitimately submit nothing when unchanged).
func TestRegimeConfigEmptyBodyIsNoOp(t *testing.T) {
	s, admin, _ := newRegimeTestServer(t)
	if rr := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/config/regime",
		`{"sizing":{"tier_scales":{"Red":0.20}}}`)); rr.Code != 200 {
		t.Fatalf("预置写入应 200, got %d", rr.Code)
	}
	rr := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/config/regime", `{}`))
	if rr.Code != 200 {
		t.Fatalf("空补丁应 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	_, sizing, _ := decodeRegime(t, rr.Body.String())
	tiers, _ := sizing["tier_scales"].(map[string]any)
	if got, _ := tiers["Red"].(float64); got != 0.20 {
		t.Fatalf("空补丁动了已存值, got %v", tiers["Red"])
	}
}

// TestRegimeConfigSizingTSubKeys §DYN-T（方案 §4 同步面 / §6.1 config 组）：
// `sizing.t` 必须**按键**往返——面板只发一个键时同段兄弟键不能被抹平；GET 必须回显
// **区间校验后的生效值**（越界值落回出厂默认，绝不能把脏值原样送回去让引擎再猜一遍）。
// English: the T block merges per key and GET reports range-validated effective values.
func TestRegimeConfigSizingTSubKeys(t *testing.T) {
	s, admin, _ := newRegimeTestServer(t)
	rr := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/config/regime",
		`{"sizing":{"t":{"vwap_dev_pct":1.2,"loss_breaker_pct":0.0}}}`))
	if rr.Code != 200 {
		t.Fatalf("写 t 段应 200，got %d body=%s", rr.Code, rr.Body.String())
	}
	rr2 := adminDo(s, adminReq(s, admin, http.MethodGet, "/api/config/regime", ""))
	_, sizing, _ := decodeRegime(t, rr2.Body.String())
	tv, ok := sizing["t"].(map[string]any)
	if !ok {
		t.Fatalf("GET 未回显 sizing.t：%T", sizing["t"])
	}
	if got, _ := tv["vwap_dev_pct"].(float64); got != 1.2 {
		t.Fatalf("vwap_dev_pct 应写入 1.2，got %v", tv["vwap_dev_pct"])
	}
	// 越界值（熔断 0＝永不熔断）不得原样回显：必须是回落后的 3.0。
	if got, _ := tv["loss_breaker_pct"].(float64); got != 3.0 {
		t.Fatalf("loss_breaker_pct 越界应回显回落后的 3.0，got %v", tv["loss_breaker_pct"])
	}
	// 兄弟键必须回显出厂生效值（不是零值）：面板显示 0 而引擎按 2.5 跑就是"页面写的≠引擎吃的"。
	for key, want := range map[string]float64{
		"min_amplitude_pct":           2.5,
		"min_market_scale":            0.4,
		"sell_env_min_market_scale":   0.2,
		"intraday_sell_cap_total_pct": 20,
		"near_extreme_pct":            0.3,
		"stop_guard_pct":              1.5,
		"intraday_cap_pct":            50,
		"min_spread_pct":              2.0,
	} {
		if got, _ := tv[key].(float64); got != want {
			t.Fatalf("GET t.%s 应回显默认生效值 %v，got %v", key, want, tv[key])
		}
	}
	if on, _ := tv["enabled"].(bool); !on {
		t.Fatal("§DYN-T（六轮 T4-12）：实盘 T 生产打开，t.enabled 必须回显 true")
	}
	if mode, _ := tv["mode"].(string); mode != "both" {
		t.Fatalf("t.mode 出厂应为 both（正反都做），got %v", mode)
	}
	if rounds, _ := tv["max_rounds_per_code_per_day"].(float64); rounds != 1 {
		t.Fatalf("五轮 T5-2：灰度期发版值必须是 1，got %v", rounds)
	}

	// 第二次只发一个键：第一例写入的 vwap_dev_pct 必须保留（键级合并，不是整段替换）。
	rr3 := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/config/regime",
		`{"sizing":{"t":{"min_amplitude_pct":3.5}}}`))
	if rr3.Code != 200 {
		t.Fatalf("写第二个 t 键应 200，got %d", rr3.Code)
	}
	rr4 := adminDo(s, adminReq(s, admin, http.MethodGet, "/api/config/regime", ""))
	_, sizing2, _ := decodeRegime(t, rr4.Body.String())
	tv2, _ := sizing2["t"].(map[string]any)
	if got, _ := tv2["vwap_dev_pct"].(float64); got != 1.2 {
		t.Fatalf("第二次写入抹掉了 vwap_dev_pct，got %v", tv2["vwap_dev_pct"])
	}
	if got, _ := tv2["min_amplitude_pct"].(float64); got != 3.5 {
		t.Fatalf("min_amplitude_pct 应更新为 3.5，got %v", tv2["min_amplitude_pct"])
	}
}
