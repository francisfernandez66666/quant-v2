// 文件：registry_library_gate_test.go
// 包名：engine
// 所属模块：「多账号引擎注册表与装配」
// 模块职责：§0925EVE-C1（2026-09-25）实盘腿战法库三态闸的行为锁——
//   - not_loaded（读库失败，applied_*.json 损坏）：newAccountRunners 返回 nil（当轮不出单），
//     量规 live_strategy_library_load_errors>0，且只有"读库失败"这条 p1 告警命中；
//   - no_enabled（读取成功但零条启用规则）：同样返回 nil，量规 live_strategy_no_enabled==1，
//     命中的是"库里真没启用规则"那条告警——两条文案不许混成一条（各自规则各自触发）；
//   - ok（正常态，反证不误报警）：runner 照常返回并注入规则，三条量规都落在"不触发"侧，
//     两条新规则评估零事件。
//
// 构造方式沿用 registry 现有测试的假依赖注入（config.NewManager("") + t.TempDir() 造库文件，
// 见 registry_exit_overrides_test.go / registry_exit_retention_test.go）。
package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/metrics"
)

// writeBrokenAppliedFactors 造一份**存在但不可解析**的 applied_factors.json：
// research 侧把"文件缺失"判为读取成功（零条目），所以读库失败态必须用坏文件构造。
func writeBrokenAppliedFactors(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "applied_factors.json"), []byte("{这不是合法 JSON"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeEnabledAppliedFactor 造一份含一条**启用**因子规则的战法库（与 §P2-d 现有测试同构）。
func writeEnabledAppliedFactor(t *testing.T, dir string) {
	t.Helper()
	entry := map[string]any{
		"id": "fac_7", "name": "因子战法#7", "enabled": true,
		"candidate_id": 7, "applied_at": "2026-09-25 00:00:00",
		"factors":       []string{"mom_5"},
		"weights":       map[string]float64{"mom_5": 1},
		"directions":    map[string]int{"mom_5": 1},
		"buy_threshold": 60.0, "horizon": 5,
	}
	b, _ := json.Marshal([]map[string]any{entry})
	if err := os.WriteFile(filepath.Join(dir, "applied_factors.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// evalLiveLibraryAlerts 用当前量规快照对两条 §0925EVE-C1 新规则跑一轮评估，返回触发事件名。
// 刻意只喂本闸的三条量规（其余规则的 gauge 由别的链路喂值，不属本测试命题）。
func evalLiveLibraryAlerts(t *testing.T) []string {
	t.Helper()
	vals := map[string]int64{}
	for _, g := range []string{"live_strategy_library_load_errors", "live_strategy_enabled_rules", "live_strategy_no_enabled"} {
		v, _ := metrics.GetGauge(g)
		vals[g] = v
	}
	a := metrics.NewAlerter()
	var rules []metrics.AlertRule
	for _, r := range metrics.DefaultAlertRules() {
		if r.Name == "live_strategy_library_not_loaded" || r.Name == "live_strategy_no_enabled_rules" {
			rules = append(rules, r)
		}
	}
	var fired []string
	for _, e := range a.Evaluate(rules, vals) {
		if e.Kind == "fire" {
			fired = append(fired, e.Name)
		}
	}
	return fired
}

// mustGauge（读量规值）复用本包既有测试助手 scoring_loop_staleness_test.go:102（§CAL-GATE 批落地），
// 本文件不重复声明。

// TestLiveLibraryGateNotLoaded 态 a：读库失败＝fail-close——当轮不出单 + 只报"读库失败"文案。
func TestLiveLibraryGateNotLoaded(t *testing.T) {
	dir := t.TempDir()
	writeBrokenAppliedFactors(t, dir)

	runners := newAccountRunners(config.NewManager(""), nil, "tester", dir, nil)
	if len(runners) != 0 {
		t.Fatalf("读库失败应 fail-close 返回零 runner，got %d", len(runners))
	}
	if n := mustGauge(t, "live_strategy_library_load_errors"); n != 1 {
		t.Fatalf("load_errors 量规应为 1（因子侧读失败），got %d", n)
	}
	if n := mustGauge(t, "live_strategy_no_enabled"); n != 0 {
		t.Fatalf("读库失败不得把 no_enabled 写成 1（两条告警不许混），got %d", n)
	}
	if n := mustGauge(t, "live_strategy_enabled_rules"); n != 0 {
		t.Fatalf("读失败时规则数读数如实写 0, got %d", n)
	}
	fired := evalLiveLibraryAlerts(t)
	if len(fired) != 1 || fired[0] != "live_strategy_library_not_loaded" {
		t.Fatalf("应只触发 live_strategy_library_not_loaded，got %v", fired)
	}
}

// TestLiveLibraryGateNoEnabled 态 b：读取成功但零条启用＝当轮不出单 + 只报"库里真没规则"文案。
// 构造：目录存在但没有任何 applied_*.json（research 判为"读取成功、零条目"）。
func TestLiveLibraryGateNoEnabled(t *testing.T) {
	dir := t.TempDir()

	runners := newAccountRunners(config.NewManager(""), nil, "tester", dir, nil)
	if len(runners) != 0 {
		t.Fatalf("零条启用应 fail-close 返回零 runner，got %d", len(runners))
	}
	if n := mustGauge(t, "live_strategy_no_enabled"); n != 1 {
		t.Fatalf("no_enabled 量规应为 1，got %d", n)
	}
	if n := mustGauge(t, "live_strategy_library_load_errors"); n != 0 {
		t.Fatalf("读库没失败，load_errors 必须为 0（与态 a 文案分家），got %d", n)
	}
	fired := evalLiveLibraryAlerts(t)
	if len(fired) != 1 || fired[0] != "live_strategy_no_enabled_rules" {
		t.Fatalf("应只触发 live_strategy_no_enabled_rules，got %v", fired)
	}
}

// TestLiveLibraryGateOkNoAlerts 态 c + 反证：正常态照旧注入规则、runner 非空，
// 三条量规全部落在"不触发"侧，两条新告警零事件——不许把健康装配报成出事。
func TestLiveLibraryGateOkNoAlerts(t *testing.T) {
	dir := t.TempDir()
	writeEnabledAppliedFactor(t, dir)

	runners := newAccountRunners(config.NewManager(""), nil, "tester", dir, nil)
	if len(runners) == 0 {
		t.Fatal("正常态 runner 不应被闸拦掉")
	}
	// 规则注入细节由 §P2-d 既有测试（registry_exit_overrides_test）锁；本用例命题是"闸不误伤 + 不误报"。
	if n := mustGauge(t, "live_strategy_enabled_rules"); n != 1 {
		t.Fatalf("启用规则数读数应为 1，got %d", n)
	}
	if n := mustGauge(t, "live_strategy_no_enabled"); n != 0 {
		t.Fatalf("正常态 no_enabled 必须归 0，got %d", n)
	}
	if n := mustGauge(t, "live_strategy_library_load_errors"); n != 0 {
		t.Fatalf("正常态 load_errors 必须归 0，got %d", n)
	}
	if fired := evalLiveLibraryAlerts(t); len(fired) != 0 {
		t.Fatalf("正常态不得触发任何战法库告警，got %v", fired)
	}
}

// TestLiveLibraryGateSkippedWithoutDataDir 边界：dataDir==""（未配置持久化目录）时闸不表态——
// 不把"还没装配过读库路径"误报成"库挂了"（§CB 防误熔同向）。
func TestLiveLibraryGateSkippedWithoutDataDir(t *testing.T) {
	g := gateLiveStrategyLibrary("", nil, nil, nil, nil)
	if g != liveLibraryGateSkipped {
		t.Fatalf("dataDir 为空应为 skipped，got %v", g)
	}
}
