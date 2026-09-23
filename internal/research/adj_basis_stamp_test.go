// adj_basis_stamp_test.go §ADJ-BASIS-2（2026-09-23）：已应用因子战法的复权口径基线戳与失效判定。
// 四把锁：①ApplyFactorRule 落盘带戳（adj_basis = AdjBaselineVersion）
// ②旧格式（文件里没有 adj_basis 字段）载入判 stale，且**不被回填/改写**
// ③处置策略行为锁：shadow（缺省/未知值）下 stale 仍在 enabled 集合、disable 下被剔除
// ④读库即刷新 applied_factor_stale_basis_count 量规（含"库清空 → 归零"，不留残值）。
// 为什么必须有这组锁：§ADJ 修复改的是数值不是入口，旧权重的历史依据已经没了，而库里
// 任何一条记录都看不出这件事——只有"应用时盖戳 + 载入时判 stale"能把失效状态变成可观测事实。
package research

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"quant-trading-v2/internal/metrics"
	"quant-trading-v2/internal/store"
)

// resetStaleAdjPolicy 保存/恢复进程级处置策略（包级状态，测试之间不得互相污染）。
// 量规同为进程级共享，一并复位，避免别的用例读到本用例留下的残值。
func resetStaleAdjPolicy(t *testing.T) {
	t.Helper()
	prev := StaleAdjBasisAction()
	prevFn := takeStaleActionGetter()
	t.Cleanup(func() {
		restoreStaleActionGetter(prevFn)
		ConfigureStaleAdjBasisAction(prev)
		metrics.SetGauge("applied_factor_stale_basis_count", 0)
	})
}

// takeStaleActionGetter/restoreStaleActionGetter 仅测试用的钩子对（同包可直接读写包级变量）。
func takeStaleActionGetter() func() string {
	staleAdjMu.Lock()
	defer staleAdjMu.Unlock()
	return staleAdjActionFn
}

func restoreStaleActionGetter(fn func() string) {
	staleAdjMu.Lock()
	defer staleAdjMu.Unlock()
	staleAdjActionFn = fn
}

// writeLibraryJSON 直接落一份战法库文件（模拟"本字段上线前写入的历史文件"）。
func writeLibraryJSON(t *testing.T, dir, raw string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "applied_factors.json"), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestApplyFactorRuleStampsAdjBasis 应用一条候选必须盖上当前口径戳，载入后判为新鲜。
func TestApplyFactorRuleStampsAdjBasis(t *testing.T) {
	resetStaleAdjPolicy(t)
	dir := t.TempDir()
	c := &store.Candidate{ID: 11, Kind: "factor", Factors: `["Mom20"]`,
		Weights: `{"weights":{"Mom20":1},"directions":{"Mom20":1},"buy_threshold":70}`, Horizon: 5, IR: 0.4}
	if err := ApplyFactorRule(dir, c); err != nil {
		t.Fatalf("应用失败: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "applied_factors.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"adj_basis": "`+AdjBaselineVersion+`"`) {
		t.Fatalf("落盘条目未带口径戳: %s", raw)
	}
	entries, err := ListAppliedFactorRules(dir)
	if err != nil {
		t.Fatalf("载入失败: %v", err)
	}
	if len(entries) != 1 || entries[0].AdjBasis != AdjBaselineVersion || entries[0].StaleAdjBasis {
		t.Fatalf("当前基线条目应判新鲜, got %+v", entries)
	}
	if got, _ := metrics.GetGauge("applied_factor_stale_basis_count"); got != 0 {
		t.Fatalf("全新鲜库的失效计数应为 0, got %d", got)
	}
	// stale_adj_basis 是派生值，绝不能被写进库文件（否则口径版本 bump 后残值会自证为真）。
	if strings.Contains(string(raw), "stale_adj_basis") {
		t.Fatalf("派生标记不应落盘: %s", raw)
	}
}

// TestLegacyEntryWithoutAdjBasisIsStale 无 adj_basis 字段的旧库文件：载入判 stale，且载入不改动文件。
func TestLegacyEntryWithoutAdjBasisIsStale(t *testing.T) {
	resetStaleAdjPolicy(t)
	dir := t.TempDir()
	legacy := `[{"id":"fac_1","name":"波动突破","enabled":true,"candidate_id":1,"applied_at":"2026-09-20 02:10:00",` +
		`"factors":["AtrRatio14","Brk60","STOA","HL20"],"weights":{"AtrRatio14":0.25},"directions":{"AtrRatio14":1},` +
		`"buy_threshold":72,"horizon":5,"ir":0.5,"excess":0.02,"signal_count":9,"win":5,"loss":4,"cum_return":0.03}]`
	writeLibraryJSON(t, dir, legacy)
	before, _ := os.ReadFile(filepath.Join(dir, "applied_factors.json"))

	entries, err := ListAppliedFactorRules(dir)
	if err != nil {
		t.Fatalf("载入失败: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("应载入 1 条, got %d", len(entries))
	}
	if entries[0].AdjBasis != "" || !entries[0].StaleAdjBasis {
		t.Fatalf("旧格式条目应判 stale, 得 adj_basis=%q stale=%v", entries[0].AdjBasis, entries[0].StaleAdjBasis)
	}
	if got, _ := metrics.GetGauge("applied_factor_stale_basis_count"); got != 1 {
		t.Fatalf("失效计数应为 1, got %d", got)
	}
	// 运行统计等历史字段必须原样保留：我们只标注、不回填、不改写。
	if entries[0].SignalCount != 9 || entries[0].BuyThreshold != 72 {
		t.Fatalf("旧条目字段被改动: %+v", entries[0])
	}
	after, _ := os.ReadFile(filepath.Join(dir, "applied_factors.json"))
	if string(before) != string(after) {
		t.Fatalf("载入路径改写了历史库文件（禁止回填）")
	}
}

// TestStaleAdjBasisShadowKeepsEnabled shadow（缺省处置）下 stale 条目仍在 enabled 集合：
// 修口径这件事不得顺带改变实盘资金行为。
func TestStaleAdjBasisShadowKeepsEnabled(t *testing.T) {
	dir := withStaleLibrary(t)
	if got := ConfigureStaleAdjBasisAction("shadow"); got != StaleAdjBasisShadow {
		t.Fatalf("shadow 归一失败: %s", got)
	}
	rules, err := LoadEnabledFactorRules(dir)
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	if len(rules) != 2 {
		t.Fatalf("shadow 下 stale+fresh 都应注入, got %d: %+v", len(rules), rules)
	}
	// 未知值必须退到 shadow（拼写错误不能变成停战法）。
	if got := ConfigureStaleAdjBasisAction("ohl-no-such-value"); got != StaleAdjBasisShadow {
		t.Fatalf("未知值应归一为 shadow, got %s", got)
	}
	if rules, _ := LoadEnabledFactorRules(dir); len(rules) != 2 {
		t.Fatalf("未知值下不得改变实盘集合, got %d", len(rules))
	}
	// 兼容单规则入口同判据：shadow 下照旧取到第一条启用的（= 旧的 fac_1）。
	fr, err := LoadAppliedFactorRule(dir)
	if err != nil || fr == nil || len(fr.Factors) != 1 || fr.Factors[0] != "AtrRatio14" {
		t.Fatalf("shadow 下单规则入口应保持旧行为（fac_1）, got %+v err=%v", fr, err)
	}
}

// TestStaleAdjBasisDisableFailsClose disable（fail-close）下 stale 条目被剔出 enabled 集合：
// 只剩带当前戳的战法能产生新买入信号。这是本批的关键行为锁。
func TestStaleAdjBasisDisableFailsClose(t *testing.T) {
	dir := withStaleLibrary(t)
	if got := ConfigureStaleAdjBasisAction(StaleAdjBasisDisable); got != StaleAdjBasisDisable {
		t.Fatalf("disable 归一失败: %s", got)
	}
	rules, err := LoadEnabledFactorRules(dir)
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	if len(rules) != 1 || rules[0].ID != "fac_2" {
		t.Fatalf("disable 下应只剩当前基线条目 fac_2, got %+v", rules)
	}
	// 兼容单规则入口同判据：它必须跳过 stale 的 fac_1、取到当前基线的 fac_2，
	// 否则 fail-close 会从这条老路漏出去（旧断写成 nil 也不对：库里还有新鲜条目）。
	fr, err := LoadAppliedFactorRule(dir)
	if err != nil || fr == nil {
		t.Fatalf("disable 下单规则入口应返回新鲜条目, got %+v err=%v", fr, err)
	}
	if len(fr.Factors) != 1 || fr.Factors[0] != "Mom20" || fr.BuyThreshold != 70 {
		t.Fatalf("disable 下单规则入口取到了 stale 条目（fac_1）: %+v", fr)
	}
	// 失效计数与处置策略无关：disable 之后仍要继续报数（告警不因停投而消失）。
	if got, _ := metrics.GetGauge("applied_factor_stale_basis_count"); got != 1 {
		t.Fatalf("disable 下失效计数仍应为 1, got %d", got)
	}
}

// withStaleLibrary 造一个含 1 条旧格式（stale）+ 1 条当前戳（fresh）的战法库，并复位处置策略。
func withStaleLibrary(t *testing.T) string {
	t.Helper()
	resetStaleAdjPolicy(t)
	dir := t.TempDir()
	writeLibraryJSON(t, dir, `[`+
		`{"id":"fac_1","name":"波动突破","enabled":true,"candidate_id":1,`+
		`"factors":["AtrRatio14"],"weights":{"AtrRatio14":1},"directions":{"AtrRatio14":1},"buy_threshold":72},`+
		`{"id":"fac_2","name":"新基线战法","enabled":true,"candidate_id":2,`+
		`"factors":["Mom20"],"weights":{"Mom20":1},"directions":{"Mom20":1},"buy_threshold":70,`+
		`"adj_basis":"`+AdjBaselineVersion+`"}`+
		`]`)
	entries, err := ListAppliedFactorRules(dir)
	if err != nil || len(entries) != 2 {
		t.Fatalf("夹具库应为 2 条, got %d err=%v", len(entries), err)
	}
	if !entries[0].StaleAdjBasis || entries[1].StaleAdjBasis {
		t.Fatalf("夹具 stale 判定不符: %+v", entries)
	}
	return dir
}

// TestStaleAdjBasisGaugeClearedOnEmptyLibrary 库被清空/删除后量规必须归零：
// 不写零 = 拿上一轮残值冒充当前状态（§DEADGAUGE 负锁③同族）。
func TestStaleAdjBasisGaugeClearedOnEmptyLibrary(t *testing.T) {
	resetStaleAdjPolicy(t)
	dir := t.TempDir()
	// 旧格式先造一条 stale（手工文件，无 adj_basis）
	writeLibraryJSON(t, dir, `[{"id":"fac_3","name":"x","enabled":true,"factors":["Mom20"],`+
		`"weights":{"Mom20":1},"directions":{"Mom20":1}}]`)
	if _, err := ListAppliedFactorRules(dir); err != nil {
		t.Fatal(err)
	}
	if got, _ := metrics.GetGauge("applied_factor_stale_basis_count"); got != 1 {
		t.Fatalf("应先计 1 条失效, got %d", got)
	}
	if err := RemoveAppliedFactorRule(dir, "fac_3"); err != nil {
		t.Fatal(err)
	}
	entries, err := ListAppliedFactorRules(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("删除后库应为空, got %d err=%v", len(entries), err)
	}
	if got, _ := metrics.GetGauge("applied_factor_stale_basis_count"); got != 0 {
		t.Fatalf("空库必须把失效计数写回 0, got %d", got)
	}
}
