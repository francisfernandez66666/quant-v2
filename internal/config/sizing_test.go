package config

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

// TestSizingConfigDefaultsSafe §DYN-SIZE + §DYN-GLOBAL（2026-10-24 owner 裁决一「dynamic 全局，
// legacy 开关账户级别」）：出厂默认必须是**下发三键齐开**——Enabled=true、BudgetMode=dynamic、
// Shadow=false。"安全"的定义变了：旧口径下"关着最安全"，新口径下"预算决策者只有一位"才是安全
// ——dynamic 生效而 legacy 每日闸同步让位（gate.go 的 `if !o.DynamicBudget` 分支），两者必须同时
// 成立；反之若 BudgetMode=dynamic 而 Enabled=false/Shadow=true，旧闸已退役、新缩放又没下发，
// 等于**没有任何预算决策者**，那才是本案真正的不安全态（W1 因此同时加了模拟盘侧的日报警）。
// 其余因子参数不在本裁决射程内，仍逐字锁住原值。
func TestSizingConfigDefaultsSafe(t *testing.T) {
	d := DefaultSizingConfig()
	if !d.EnabledOn() {
		t.Fatalf("§DYN-GLOBAL 后出厂默认必须 Enabled=true（动态仓位全局生效）")
	}
	if d.ShadowOn() {
		t.Fatalf("§DYN-GLOBAL 后出厂默认必须 Shadow=false（真下发，不是只留痕）")
	}
	if d.BudgetModeOf() != "dynamic" || !d.DynamicBudgetOn() {
		t.Fatalf("§DYN-GLOBAL 后出厂默认必须 budget_mode=dynamic，got %q", d.BudgetMode)
	}
	// §DYN-SIZE v4（2026-10-26 owner 裁决）：**推翻旧 Q1**「本批只切是否生效、不切怎么算」，
	// 出厂默认直接切 power_mean（同组因子惩罚不叠加；product≈0.378 vs power_mean≈0.79 的差别
	// 在新模型里就是目标仓位 38% vs 79%）。这条断言因此改口为"必须是 power_mean"。
	if d.ModeOf() != "power_mean" {
		t.Fatalf("v4 裁决：出厂默认 Mode 必须是 power_mean，got %q", d.ModeOf())
	}
	if !d.RiskBudgetKeepOn() {
		t.Fatalf("放宽止损默认必须强制同比例缩仓（owner 裁决）")
	}
	if d.HolidayBlockNewOn() {
		t.Fatalf("长假禁开仓默认必须关（owner 风险偏好项）")
	}
	if d.Floor != 0.15 || d.Cap != 1.15 {
		t.Fatalf("地板/上限默认应为 0.15/1.15，got %v/%v", d.Floor, d.Cap)
	}
	if d.SellBoostLevel != "L2" {
		t.Fatalf("卖出加权默认档应为 L2，got %q", d.SellBoostLevel)
	}
	// 反证：三键里任何一把单独关掉都必须让用例红（不是只测"全开"这一支）。
	if (SizingConfig{Enabled: boolPtr(false), Shadow: boolPtr(false), BudgetMode: "dynamic"}).EnabledOn() {
		t.Fatalf("显式 Enabled=false 必须被读成关")
	}
	if (SizingConfig{Enabled: boolPtr(true), Shadow: boolPtr(true)}).ShadowOn() == false {
		t.Fatalf("显式 Shadow=true 必须被读成影子开")
	}
}

// TestSizingConfigZeroValueWithDefaults 零值（存量配置缺键）经 WithDefaults 后等价于出厂默认。
func TestSizingConfigZeroValueWithDefaults(t *testing.T) {
	var zero SizingConfig
	got := zero.WithDefaults()
	want := DefaultSizingConfig()
	if got.Floor != want.Floor || got.Cap != want.Cap || got.KellyLambda != want.KellyLambda {
		t.Fatalf("零值补齐后应与出厂默认一致，got %+v", got)
	}
	if got.StoplossGuardCount != want.StoplossGuardCount {
		t.Fatalf("连续止损阈值应补齐为 %d，got %d", want.StoplossGuardCount, got.StoplossGuardCount)
	}
	if len(got.HolidayScales) != len(want.HolidayScales) {
		t.Fatalf("节前分级表应补齐，got %v", got.HolidayScales)
	}
	if !got.RiskBudgetKeepOn() {
		t.Fatalf("零值 SLOffsetRiskBudgetKeep 应视为 true")
	}
	// §DYN-GLOBAL 分层锁（两条都要，缺一就看不出走的是哪一层）：
	//   - 裸零值：ShadowOn()==true、EnabledOn()==true —— 未归一结构体的兜底仍是"宁可只留痕"；
	//   - 经 WithDefaults：两把下发键补成出厂值（Shadow=false、Enabled=true），
	//     这是"存量配置缺键 = 跟随新全局口径"的实现点，出厂默认若不经过这里就永远走不到。
	var raw SizingConfig
	if !raw.ShadowOn() {
		t.Fatalf("未归一的裸零值 Shadow 必须仍读成影子开（宁可只留痕）")
	}
	if !raw.EnabledOn() || !got.EnabledOn() {
		t.Fatalf("Enabled 缺键必须读成开（nil=出厂默认），got raw=%v defaulted=%v", raw.EnabledOn(), got.EnabledOn())
	}
	if got.ShadowOn() {
		t.Fatalf("WithDefaults 必须把缺失的 Shadow 补成出厂值 false，got true")
	}
	if got.BudgetModeOf() != "dynamic" {
		t.Fatalf("WithDefaults 必须把缺失的 BudgetMode 补成出厂值 dynamic，got %q", got.BudgetMode)
	}
	// 显式写过的值不许被出厂默认覆盖——那是账号自己的退路。
	explicit := SizingConfig{Shadow: boolPtr(true), Enabled: boolPtr(false), BudgetMode: "legacy"}.WithDefaults()
	if !explicit.ShadowOn() || explicit.EnabledOn() || explicit.BudgetModeOf() != "legacy" {
		t.Fatalf("显式 shadow=true/enabled=false/budget_mode=legacy 必须原样保留，got %+v", explicit)
	}
	if drift := (SizingConfig{BudgetMode: "dymanic"}).BudgetModeDrift(); drift != "dymanic" {
		t.Fatalf("脏值必须被 BudgetModeDrift 报出来，got %q", drift)
	}
	if (SizingConfig{BudgetMode: "dymanic"}).DynamicBudgetOn() {
		t.Fatalf("脏 budget_mode 必须保守落到 legacy，不得当成 dynamic")
	}
}

// TestSizingConfigPartialOverride 部分覆盖：用户只改关心的键，其余走默认；默认表不被污染。
func TestSizingConfigPartialOverride(t *testing.T) {
	base := DefaultSizingConfig()
	baseLen := len(base.HolidayScales)

	in := SizingConfig{
		Enabled:       boolPtr(true),
		Floor:         0.25,
		HolidayScales: map[string]float64{"weekend": 0.50},
	}
	got := in.WithDefaults()
	if got.Floor != 0.25 {
		t.Fatalf("显式 Floor 应保留，got %v", got.Floor)
	}
	if got.HolidayScales["weekend"] != 0.50 {
		t.Fatalf("显式覆盖应生效，got %v", got.HolidayScales["weekend"])
	}
	if got.HolidayScales["long1"] != 0.40 {
		t.Fatalf("未覆盖的键应保留默认 0.40，got %v", got.HolidayScales["long1"])
	}
	// 不得污染出厂默认表（WithDefaults 必须返回新映射）。
	if len(DefaultSizingConfig().HolidayScales) != baseLen {
		t.Fatalf("WithDefaults 污染了出厂默认映射")
	}
	if DefaultSizingConfig().HolidayScales["weekend"] != 0.85 {
		t.Fatalf("出厂默认 weekend 应恒为 0.85，got %v", DefaultSizingConfig().HolidayScales["weekend"])
	}
}

// TestSizingConfigJSONRoundTrip JSON 键语义：旧配置无 sizing 段 → 零值 → **跟随 §DYN-GLOBAL 新默认**
// （EnabledOn=true、BudgetModeOf=dynamic、ShadowOn=false），不再是"安全侧关着"。
// 这条用例就是裁决一"dynamic 全局"的落点证明：不写键即跟随全局。
func TestSizingConfigJSONRoundTrip(t *testing.T) {
	// 旧配置文件（无 sizing 段）反序列化后补齐出厂默认，必须落到下发态。
	var rules Rules
	if err := json.Unmarshal([]byte(`{"position":{"max_total_position_pct":0}}`), &rules); err != nil {
		t.Fatalf("反序列化旧配置失败: %v", err)
	}
	if !rules.Sizing.EnabledOn() {
		t.Fatalf("§DYN-GLOBAL：无 sizing 段应跟随新默认 Enabled=true，got %+v", rules.Sizing)
	}
	got := rules.Sizing.WithDefaults()
	if got.Floor != 0.15 || got.Cap != 1.15 {
		t.Fatalf("旧配置补齐后应为出厂默认，got floor=%v cap=%v", got.Floor, got.Cap)
	}
	if !got.DynamicBudgetOn() || got.ShadowOn() {
		t.Fatalf("§DYN-GLOBAL：无 sizing 段补齐后应为 dynamic+非影子，got %+v", got)
	}
	// Enabled 带 omitempty：未设置的键**不能**被序列化出来，否则"缺键跟随默认"与"显式关闭"
	// 两态会在往返后混成一态（本批最怕的正是这个）。
	rawZero, err := json.Marshal(SizingConfig{})
	if err != nil {
		t.Fatalf("序列化零值失败: %v", err)
	}
	if strings.Contains(string(rawZero), `"enabled"`) || strings.Contains(string(rawZero), `"shadow"`) {
		t.Fatalf("零值不得写出 enabled/shadow 键，got %s", rawZero)
	}

	// 显式配置可往返。
	in := SizingConfig{Enabled: boolPtr(true), Shadow: boolPtr(false), Floor: 0.2, Cap: 1.3, SellBoostLevel: "L3"}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	var out SizingConfig
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("反序列化失败: %v", err)
	}
	if !out.EnabledOn() || out.ShadowOn() || out.Floor != 0.2 || out.Cap != 1.3 || out.SellBoostLevel != "L3" {
		t.Fatalf("往返不一致: %+v (raw=%s)", out, raw)
	}
	// 显式关闭也必须原样往返（账号级 legacy 退路的持久化前提）。
	off := SizingConfig{Enabled: boolPtr(false), BudgetMode: "legacy"}
	rawOff, err := json.Marshal(off.WithDefaults())
	if err != nil {
		t.Fatalf("序列化关闭态失败: %v", err)
	}
	var backOff SizingConfig
	if err := json.Unmarshal(rawOff, &backOff); err != nil {
		t.Fatalf("反序列化关闭态失败: %v", err)
	}
	if backOff.EnabledOn() || backOff.DynamicBudgetOn() {
		t.Fatalf("显式 enabled=false/budget_mode=legacy 往返后被抬成下发了: %+v (raw=%s)", backOff, rawOff)
	}
}

// TestNormalizeLegacyTotalPosPct §DYN-SIZE P6：既有死字段 PositionConfig.MaxTotalPositionPct
// 归一化为比例。两种历史写法（0.8 与 80）都必须落到同一个 0.8——否则 80 会让
// `totalPosCap` 的 limit/max(limit,risk) 恒等于 1.0（限额大到永不约束，接了个寂寞）。
func TestNormalizeLegacyTotalPosPct(t *testing.T) {
	cases := []struct {
		in   float64
		want float64
	}{
		{0, 0},     // 未设置 → 0（调用方回落 SizingConfig 自己的默认 0.80）
		{-1, 0},    // 非法 → 0
		{0.5, 0.5}, // 比例写法原样
		{0.8, 0.8},
		{1, 1},     // 边界：恰好 1（满仓上限）仍按比例
		{80, 0.8},  // 百分数写法 → ÷100
		{100, 1.0}, // 100% → 1.0
		{120, 1.2}, // 超出区间仍按百分数归一（>1 即百分数约定），由调用方钳制
	}
	for _, c := range cases {
		if got := NormalizeLegacyTotalPosPct(c.in); got != c.want {
			t.Errorf("NormalizeLegacyTotalPosPct(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func boolPtr(b bool) *bool { return &b }

// TestSizingPowerPSemantics §DYN-SIZE-v3 §6/§7：power_p 必须区分“未设置”（0.5）与显式 0（几何平均）。
// 旧 float 实现把 0 当缺省，导致文档承诺的 p=0 永远无法生效。
func TestSizingPowerPSemantics(t *testing.T) {
	if got := DefaultSizingConfig().PowerPOf(); got != 0.5 {
		t.Fatalf("出厂默认 p 应为 0.5，got %v", got)
	}
	var zero SizingConfig
	if got := zero.WithDefaults().PowerPOf(); got != 0.5 {
		t.Fatalf("零值补齐后 p 应为 0.5，got %v", got)
	}
	var explicit struct {
		PowerP *float64 `json:"power_p"`
	}
	if err := json.Unmarshal([]byte(`{"power_p":0}`), &explicit); err != nil {
		t.Fatalf("解析 power_p=0 失败: %v", err)
	}
	sc := SizingConfig{PowerP: explicit.PowerP}
	if got := sc.WithDefaults().PowerPOf(); got != 0 {
		t.Fatalf("显式 p=0 必须保留为几何平均，got %v", got)
	}
	raw, err := json.Marshal(sc.WithDefaults())
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	var back SizingConfig
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("反序列化失败: %v", err)
	}
	if got := back.PowerPOf(); got != 0 {
		t.Fatalf("显式 p=0 往返后仍应为 0，got %v (raw=%s)", got, raw)
	}
}

// TestSizingBudgetModeAndGroupFloors §DYN-SIZE-v3 §7 + §DYN-GLOBAL：新键默认值与归一化。
func TestSizingBudgetModeAndGroupFloors(t *testing.T) {
	d := DefaultSizingConfig()
	if d.BudgetModeOf() != "dynamic" || d.ModeOf() != "power_mean" {
		t.Fatalf("§DYN-GLOBAL 后默认口径应为 dynamic/product，got %s/%s", d.BudgetModeOf(), d.ModeOf())
	}
	if !d.DynamicBudgetOn() {
		t.Fatalf("默认必须开启动态预算（owner 裁决一：dynamic 全局）")
	}
	// 账号级退路只认显式写法；空串跟随全局默认（dynamic），不是 legacy。
	if (SizingConfig{BudgetMode: "legacy"}).BudgetModeOf() != "legacy" {
		t.Fatalf("显式 legacy 必须保留为 legacy（账号级退路）")
	}
	if (SizingConfig{}).BudgetModeOf() != "dynamic" {
		t.Fatalf("空 budget_mode 必须跟随新全局默认 dynamic")
	}
	if got := d.GroupFloor("market"); got != 0.45 {
		t.Fatalf("市场组 floor 默认 0.45，got %v", got)
	}
	if got := d.GroupFloor("stock"); got != 0.35 {
		t.Fatalf("个股组 floor 默认 0.35，got %v", got)
	}
	custom := SizingConfig{
		BudgetMode:  " DYNAMIC ",
		Mode:        " POWER_MEAN ",
		GroupFloors: map[string]float64{"market": 0.6},
	}
	got := custom.WithDefaults()
	if !got.DynamicBudgetOn() || got.ModeOf() != "power_mean" {
		t.Fatalf("大小写/空白应归一化，got %s/%s", got.BudgetModeOf(), got.ModeOf())
	}
	if got.GroupFloor("market") != 0.6 || got.GroupFloor("stock") != 0.35 {
		t.Fatalf("分组 floor 覆盖/合并错误: market=%v stock=%v", got.GroupFloor("market"), got.GroupFloor("stock"))
	}
	// §5.6：组 floor >1 等于凭空加杠杆，必须钳到 1。
	over := SizingConfig{GroupFloors: map[string]float64{"market": 1.5}}
	if got := over.GroupFloor("market"); got != 1 {
		t.Fatalf("分组 floor 应钳到 1，got %v", got)
	}
}

// f64Ptr 给三态数值键造指针（测试专用；生产代码由 JSON 反序列化产生）。
func f64Ptr(v float64) *float64 { return &v }

// TestTConfigRangeFallbacks §DYN-T（方案 §8.8 / §6.1 锁 13）：保护阈值改为配置后，
// 保护由"合法区间 + 非对称封顶 + 回落可观测"承担。每个键都构造越界/NaN/负值形态，
// 断言实际生效值落回默认，并且 TFallbacks 能把回落事实报出来（否则面板会以为按配入值在跑）。
func TestTConfigRangeFallbacks(t *testing.T) {
	cases := []struct {
		name string
		set  func(*TConfig)
		get  func(*TConfig) float64
		want float64
	}{
		{"偏离门越上界", func(c *TConfig) { c.VWAPDevPct = 99 }, func(c *TConfig) float64 { return c.VWAPDevPctOf() }, defaultTVWAPDevPct},
		{"偏离门NaN", func(c *TConfig) { c.VWAPDevPct = math.NaN() }, func(c *TConfig) float64 { return c.VWAPDevPctOf() }, defaultTVWAPDevPct},
		{"振幅门越下界", func(c *TConfig) { c.MinAmplitudePct = 0.5 }, func(c *TConfig) float64 { return c.TMinAmplitudePctOf() }, defaultTMinAmplitudePct},
		{"近极值带越下界", func(c *TConfig) { c.NearExtremePct = 0.01 }, func(c *TConfig) float64 { return c.TNearExtremePctOf() }, defaultTNearExtremePct},
		{"涨跌停距离越下界", func(c *TConfig) { c.LimitDistancePct = 0.05 }, func(c *TConfig) float64 { return c.TLimitDistancePctOf() }, defaultTLimitDistancePct},
		{"冷静期过小", func(c *TConfig) { c.OpenCoolingMin = 1 }, func(c *TConfig) float64 { return c.TOpenCoolingMinOf() }, defaultTOpenCoolingMin},
		{"否决门越下界", func(c *TConfig) { c.StopGuardPct = 0.05 }, func(c *TConfig) float64 { return c.TStopGuardPctOf() }, defaultTStopGuardPct},
		{"熔断配成0.2（等于永不熔断）", func(c *TConfig) { c.LossBreakerPct = 0.2 }, func(c *TConfig) float64 { return c.TLossBreakerPctOf() }, defaultTLossBreakerPct},
		{"总量帽越上界", func(c *TConfig) { c.IntradaySellCapTotalPct = 90 }, func(c *TConfig) float64 { return c.TIntradaySellCapTotalPctOf() }, defaultTIntradaySellCapTotalPct},
		{"成交额负值", func(c *TConfig) { c.MinAmountWan = f64Ptr(-1) }, func(c *TConfig) float64 { return c.TMinAmountWanOf() }, defaultTMinAmountWan},
		{"换手越上界", func(c *TConfig) { c.MinTurnoverPct = f64Ptr(90) }, func(c *TConfig) float64 { return c.TMinTurnoverPctOf() }, defaultTMinTurnoverPct},
		{"上市天数越上界", func(c *TConfig) { c.MinListedDays = f64Ptr(9999) }, func(c *TConfig) float64 { return c.TMinListedDaysOf() }, defaultTMinListedDays},
		{"校验容差越下界", func(c *TConfig) { c.VWAPCheckTolPct = 0.001 }, func(c *TConfig) float64 { return c.TVWAPCheckTolPctOf() }, defaultTVWAPCheckTolPct},
		{"急跌配成正值", func(c *TConfig) { c.MarketPlungePct = f64Ptr(2) }, func(c *TConfig) float64 { return c.TMarketPlungePctOf() }, defaultTMarketPlungePct},
		{"正T环境门越界", func(c *TConfig) { c.MinMarketScale = f64Ptr(1.5) }, func(c *TConfig) float64 { return c.TMinMarketScaleOf() }, defaultTMinMarketScale},
		{"急跌越下界", func(c *TConfig) { c.MarketPlungePct = f64Ptr(-50) }, func(c *TConfig) float64 { return c.TMarketPlungePctOf() }, defaultTMarketPlungePct},
		{"反T门越上界", func(c *TConfig) { c.SellEnvMinMarketScale = f64Ptr(2) }, func(c *TConfig) float64 { return c.TSellEnvMinMarketScaleOf() }, defaultTSellEnvMinMarketScale},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := &TConfig{}
			c.set(cfg)
			if got := c.get(cfg); got != c.want {
				t.Fatalf("%s：应回落默认 %.4f，got %.4f", c.name, c.want, got)
			}
			if fb := cfg.TFallbacks(); len(fb) == 0 {
				t.Fatalf("%s：回落必须可观测（TFallbacks 为空）", c.name)
			}
		})
	}
}

// TestTConfigLegalValuesPassThrough 收紧方向必须放行：合法区间内的值不得被"好心"改回默认。
func TestTConfigLegalValuesPassThrough(t *testing.T) {
	cfg := &TConfig{}
	cfg.MinAmplitudePct = 6         // 收紧（更不容易开腿）
	cfg.LossBreakerPct = 1.5        // 收紧（更早熔断）
	cfg.MinAmountWan = f64Ptr(8000) // 收紧（流动性更好）
	cfg.MinMarketScale = f64Ptr(0)  // 合法：0＝不设环境门
	if got := cfg.TMinAmplitudePctOf(); got != 6 {
		t.Fatalf("合法收紧值应放行，got %.2f", got)
	}
	if got := cfg.TLossBreakerPctOf(); got != 1.5 {
		t.Fatalf("合法收紧值应放行，got %.2f", got)
	}
	if got := cfg.TMinAmountWanOf(); got != 8000 {
		t.Fatalf("合法收紧值应放行，got %.2f", got)
	}
	if got := cfg.TMinMarketScaleOf(); got != 0 {
		t.Fatalf("0 是合法配置（不设门），got %.2f", got)
	}
	if fb := cfg.TFallbacks(); len(fb) != 0 {
		t.Fatalf("全部合法时不应有回落记录：%v", fb)
	}
}

// TestTLastOpenHM 最后开腿时刻：非法格式回落 1400；**晚于 1445 钳到 1445**（唯一保留的硬顶）；
// 早于 945 视为配错回落 1400。
func TestTLastOpenHM(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "1400"}, {"1400", "1400"}, {"1330", "1330"},
		{"1455", "1445"}, {"1500", "1445"}, // 放宽方向封顶
		{"0930", "1400"}, // 早于开盘冷静期下界 → 回落默认
		{"9aa5", "1400"}, {"140", "1400"}, {"14000", "1400"},
	}
	for _, c := range cases {
		cfg := &TConfig{LastOpenAt: c.in}
		if got := cfg.TLastOpenHM(); got != c.want {
			t.Fatalf("last_open_at=%q 应得 %q，got %q", c.in, c.want, got)
		}
	}
}

// TestTSellEnvNotAboveBuyEnv 反 T 环境门若被配得高于正 T，按两者较小值生效并记回落：
// 否则会出现"低吸比高抛更难"这种反直觉配置。
func TestTSellEnvNotAboveBuyEnv(t *testing.T) {
	cfg := &TConfig{MinMarketScale: f64Ptr(0.30), SellEnvMinMarketScale: f64Ptr(0.80)}
	if got := cfg.TSellEnvMinMarketScaleOf(); got != 0.30 {
		t.Fatalf("反 T 门应被夹到正 T 门 0.30，got %.2f", got)
	}
	if fb := cfg.TFallbacks(); fb["sell_env_min_market_scale"] != 0.30 {
		t.Fatalf("应记录回落事实：%v", fb)
	}
}

// TestTConfigDefaultsUnset 全部未配置（老账号）时，每个访问器都必须给出方案 §8.8 的默认值，
// 且**不产生回落记录**（没配 ≠ 配错）。
func TestTConfigDefaultsUnset(t *testing.T) {
	cfg := &TConfig{}
	wants := map[string]float64{
		"vwap_dev":    cfg.VWAPDevPctOf(),
		"amplitude":   cfg.TMinAmplitudePctOf(),
		"buy_env":     cfg.TMinMarketScaleOf(),
		"sell_env":    cfg.TSellEnvMinMarketScaleOf(),
		"sell_cap":    cfg.TIntradaySellCapTotalPctOf(),
		"near":        cfg.TNearExtremePctOf(),
		"limit":       cfg.TLimitDistancePctOf(),
		"cooling":     cfg.TOpenCoolingMinOf(),
		"guard":       cfg.TStopGuardPctOf(),
		"breaker":     cfg.TLossBreakerPctOf(),
		"amount":      cfg.TMinAmountWanOf(),
		"turnover":    cfg.TMinTurnoverPctOf(),
		"listed":      cfg.TMinListedDaysOf(),
		"check_tol":   cfg.TVWAPCheckTolPctOf(),
		"plunge":      cfg.TMarketPlungePctOf(),
		"intradaycap": cfg.TIntradayCapPct(),
		"spread":      cfg.TMinSpreadPct(),
	}
	if wants["vwap_dev"] != defaultTVWAPDevPct || wants["amplitude"] != defaultTMinAmplitudePct ||
		wants["buy_env"] != defaultTMinMarketScale || wants["sell_env"] != defaultTSellEnvMinMarketScale ||
		wants["sell_cap"] != defaultTIntradaySellCapTotalPct || wants["breaker"] != defaultTLossBreakerPct ||
		wants["intradaycap"] != defaultTIntradayCapPct || wants["spread"] != defaultTMinSpreadPct {
		t.Fatalf("未配置时的默认值与方案 §8.8 不符：%v", wants)
	}
	if wants["intradaycap"] != 50 {
		t.Fatalf("单票 T 帽默认应已提到 50（owner 裁决留 50~70%% 底仓），got %.0f", wants["intradaycap"])
	}
	if wants["spread"] != 2.0 {
		t.Fatalf("配对价差默认应已提到 2.0（日内 2%% 及以上才做 T），got %.1f", wants["spread"])
	}
	if fb := cfg.TFallbacks(); len(fb) != 0 {
		t.Fatalf("未配置（零值）不应记回落，否则每个老账号每轮都刷假回落：%v", fb)
	}
}
