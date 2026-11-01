// sizing_inject_wiring_test.go — §F-3（方案甲，2026-11-01）的**接线锁**。
//
// 为什么用源码锁而不是行为用例：要证的这件事本身就是"调用点在哪"——
// `SetSizingContext` 曾经挂在 runIntradayT 内部，而 runIntradayT 前面有两道早退
// （`!tc.TEnabled()`、`!data.IsActiveSession()`）。行为用例只能证明"某个配置下 ms 有值"，
// 证不了"T 关着时也会注入"——而那正是缺陷本体。
//
// 反证：把 injectPaperSizing 的调用从两处估值轮里删掉、或把 SetSizingContext 塞回
// runIntradayT，本文件立刻红。
//
// English: source-level wiring lock for §F-3 — the defect was *where* the injection lived, so the
// test asserts the call sites, not just a value.
package engine

import (
	"os"
	"strings"
	"testing"
)

// mustRead 读包内源文件（失败即红，避免"文件改名后本锁静默失效"）。
func mustRead(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", name, err)
	}
	return string(b)
}

// TestSizingInjectNotBlockedByTGate §F-3-E3：注入点必须**不在** runIntradayT 里。
//
// 判据分两半：
//  1. runIntradayT 函数体内不得再出现 SetSizingContext（否则又被两道早退遮挡）；
//  2. 两处估值轮（registry.dispatchPaperMark / engine.paperMark）必须调用 injectPaperSizing。
func TestSizingInjectNotBlockedByTGate(t *testing.T) {
	src := mustRead(t, "intraday_t.go")

	// 截出 runIntradayT 函数体：从签名到下一个顶层 func 之间。
	start := strings.Index(src, "func (e *Engine) runIntradayT(")
	if start < 0 {
		t.Fatal("找不到 runIntradayT —— 函数被改名/删除，本锁需重写")
	}
	rest := src[start:]
	end := strings.Index(rest[1:], "\nfunc ")
	if end < 0 {
		t.Fatal("runIntradayT 之后没有下一个顶层函数，截取失败")
	}
	body := rest[:end+1]
	if strings.Contains(body, "SetSizingContext") {
		t.Fatal("runIntradayT 内又出现了 SetSizingContext —— 注入点被两道早退（T 开关/交易时段）重新遮挡，" +
			"T 关着时 ms 会恒 0、目标仓位永不执法")
	}
	if !strings.Contains(src, "func (e *Engine) injectPaperSizing(") {
		t.Fatal("找不到 injectPaperSizing 定义")
	}

	// 两处估值轮都必须接线
	for _, f := range []string{"registry.go", "engine.go"} {
		s := mustRead(t, f)
		if !strings.Contains(s, "e.injectPaperSizing(") {
			t.Fatalf("%s 未调用 injectPaperSizing —— 该路径的定权上下文不会被注入", f)
		}
	}
}

// TestSizingInjectBeforeMarkToMarket §F-3：注入必须**早于** MarkToMarket。
//
// 顺序反了虽然这一轮仍能用（sizingCtx 只影响买入裁定，估值不读它），但"先估值后注入"意味着
// 本轮 Mark 用的是上一轮的上下文——两处口径会错开一轮，面板与开腿对不上。锁住顺序成本极低。
func TestSizingInjectBeforeMarkToMarket(t *testing.T) {
	for _, f := range []string{"registry.go", "engine.go"} {
		s := mustRead(t, f)
		i := strings.Index(s, "e.injectPaperSizing(")
		j := strings.Index(s, "pe.MarkToMarket(")
		if i < 0 || j < 0 {
			t.Fatalf("%s 缺少 injectPaperSizing 或 MarkToMarket", f)
		}
		if i > j {
			t.Fatalf("%s 里 injectPaperSizing 出现在 MarkToMarket 之后 —— 本轮估值会用上一轮的定权上下文", f)
		}
	}
}
