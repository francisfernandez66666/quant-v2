// dynglobal_warn_test.go — §DYN-GLOBAL 半开态每日报警锁（模拟盘侧）。
//
// 裁决一（dynamic 全局、legacy 账号级退路）留下的唯一危险组合：budget_mode=dynamic 已经把
// 旧的数量/预算闸全部挡在门外，而缩放本身没交付（账号总开关 off 或仍影子）——此刻撮合按中性
// scale=1.0 吃满 35% 额度，是所有口径里最激进的一档，且不会抛任何错误。这一条把它变成每日
// 至少可见一次的日志，并且钉死三件事：
//  1. 判据真值表（四种组合只有该半开态为真）；
//  2. 同一自然日只报一次（探针每 5s 进 OnSignals，不去重会刷爆日志）；
//  3. 跨日必须再报一次（配置改坏了要能再次看见；只在启动报一次的"提醒"等于没有提醒）。
//
// English: locks for the paper-side daily warning on the half-open state — truth table, once-per-day
// dedup, and re-arms on the next calendar day.
package paper

import (
	"bytes"
	"log"
	"strings"
	"testing"
	"time"
)

// dynGlobalHalfOpenCfg 造一份指定三键状态的模拟盘配置（只关心预算口径这三键）。
func dynGlobalHalfOpenCfg(mode string, enabled, shadow bool) Config {
	return Config{Enabled: true, InitialCapital: 100000, FixedAmount: 10000,
		BudgetMode: mode, SizingEnabled: enabled, SizingShadow: shadow}
}

// TestDynGlobalBudgetWithoutScaleTruthTable 半开态判据真值表。
func TestDynGlobalBudgetWithoutScaleTruthTable(t *testing.T) {
	cases := []struct {
		name            string
		mode            string
		enabled, shadow bool
		halfOpen        bool
	}{
		{"dynamic + 下发到位", "dynamic", true, false, false},
		{"dynamic + 账号总开关关＝半开", "dynamic", false, false, true},
		{"dynamic + 仍影子＝半开", "dynamic", true, true, true},
		{"legacy + 关（旧口径整套还在，不算半开）", "legacy", false, true, false},
		{"legacy + 下发（正常灰度推进中）", "legacy", true, false, false},
		{"口径大小写/空格脏写也按 dynamic 认", " Dynamic ", true, true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := dynGlobalHalfOpenCfg(c.mode, c.enabled, c.shadow)
			if got := cfg.DynamicBudgetWithoutScale(); got != c.halfOpen {
				t.Fatalf("半开态判据不符: got=%v want=%v (mode=%q enabled=%v shadow=%v)",
					got, c.halfOpen, c.mode, c.enabled, c.shadow)
			}
		})
	}
}

// capturePaperLog 把标准 log 导到缓冲区，返回缓冲区与恢复函数。
func capturePaperLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	return &buf
}

// TestDynGlobalBudgetWarnOncePerDay 报警按自然日去重：同日一次、次日再来一次。
func TestDynGlobalBudgetWarnOncePerDay(t *testing.T) {
	buf := capturePaperLog(t)
	e := New(dynGlobalHalfOpenCfg("dynamic", false, true), "")
	day1 := time.Now()
	warnHalfOpen := func(at time.Time) int {
		e.warnBudgetWithoutScaleLocked(at)
		return strings.Count(buf.String(), "§DYN-GLOBAL 半开态")
	}
	if n := warnHalfOpen(day1); n != 1 {
		t.Fatalf("首拨应报一次, got %d", n)
	}
	// 同日反复进 OnSignals（真实节律是每 5s 一轮）不得再加行。
	for i := 0; i < 5; i++ {
		warnHalfOpen(day1.Add(time.Duration(i) * time.Minute))
	}
	if n := strings.Count(buf.String(), "§DYN-GLOBAL 半开态"); n != 1 {
		t.Fatalf("同日应只报一次, got %d", n)
	}
	// 次日必须再报：报警的目的是"还在坏着就每天看得见"，不是"启动时打一行"。
	if n := warnHalfOpen(day1.AddDate(0, 0, 1)); n != 2 {
		t.Fatalf("次日应再报一次, got %d", n)
	}
	// 报警正文必须带上归因所需的三键读数（只有"半开态"三个字等于没法查）。
	text := buf.String()
	for _, want := range []string{"enabled=false", "shadow=true", "scale=1.0"} {
		if !strings.Contains(text, want) {
			t.Fatalf("报警文案缺读数 %q: %s", want, text)
		}
	}
}

// TestDynGlobalBudgetWarnWiredIntoOnSignals 接线反证：报警必须从**生产入口** OnSignals 走出来。
// 上面两条直接调私有方法的锁，抓不到"方法在、但没人调"这一形态（本项目 §有代码无调度 的老坑）。
// 传空信号集：报警应当在启用闸之后、逐信号撮合之前出声。
func TestDynGlobalBudgetWarnWiredIntoOnSignals(t *testing.T) {
	buf := capturePaperLog(t)
	e := New(dynGlobalHalfOpenCfg("dynamic", true, true), "")
	e.OnSignals(nil, nil)
	if !strings.Contains(buf.String(), "§DYN-GLOBAL 半开态") {
		t.Fatalf("OnSignals 未接上半开态报警: %q", buf.String())
	}
	// 引擎关闭时不得报警：模拟盘没在跑，就没有"被静默放大的仓位"这回事。
	buf.Reset()
	off := dynGlobalHalfOpenCfg("dynamic", true, true)
	off.Enabled = false
	New(off, "").OnSignals(nil, nil)
	if strings.Contains(buf.String(), "§DYN-GLOBAL 半开态") {
		t.Fatalf("停用引擎不应报警: %q", buf.String())
	}
}

// TestDynGlobalBudgetWarnSilentWhenDelivered 交付态与纯 legacy 都不许出声（报警一旦常态化就没人看）。
func TestDynGlobalBudgetWarnSilentWhenDelivered(t *testing.T) {
	buf := capturePaperLog(t)
	for _, cfg := range []Config{
		dynGlobalHalfOpenCfg("dynamic", true, false), // 下发到位
		dynGlobalHalfOpenCfg("legacy", false, true),  // 完整旧口径
	} {
		e := New(cfg, "")
		e.warnBudgetWithoutScaleLocked(time.Now())
	}
	if strings.Contains(buf.String(), "§DYN-GLOBAL 半开态") {
		t.Fatalf("正常口径不应报警: %s", buf.String())
	}
}
