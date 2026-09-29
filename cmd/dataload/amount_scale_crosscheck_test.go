// amount_scale_crosscheck_test.go — §0929SCALE-⑩ 写侧归一与抽检读侧的**跨包等值链**验证。
//
// 为什么单独立一个 cmd/dataload 的测试文件：data（写侧系数）与 store（读侧系数与抽检）
// 互为不可 import 的两个包，它们的等值锁只能放在同时依赖两者的装载层。
// 本文件锁两件事：
//  1. 系数等值：data.TushareAmountScale == store.TushareThousandToCNY（两处各留一份是刻意的，
//     但一旦有人只改一边，写侧归一和读侧自校就会用两个不同的 1000）；
//  2. 链路贯通：tushare 形态的 daily 行经 data.NormalizeTushareAmount 后走 store.InsertRows 落库，
//     再被 ProbeDailyAmountScale 抽检——结论必须是 ok。摘掉归一这一环，抽检会判 thousand-yuan；
//     归一乘两次，抽检会判 over-scaled。三条走向都在这一个用例里对着真实表跑。
package main

import (
	"path/filepath"
	"testing"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/store"
)

// TestAmountScaleConstantsAgree 跨包系数等值锁。
func TestAmountScaleConstantsAgree(t *testing.T) {
	if data.TushareAmountScale != store.TushareThousandToCNY {
		t.Fatalf("写侧系数 %v ≠ 读侧系数 %v ⇒ 归一与自校用了两个不同的 1000",
			data.TushareAmountScale, store.TushareThousandToCNY)
	}
}

// TestNormalizeThenLoadThenProbe 端到端：归一 → 落库 → 抽检判绿（并证明两条坏路径会被判红）。
func TestNormalizeThenLoadThenProbe(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "trading.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	const date = "20260918"
	// tushare 原生形态：vol=手(2e5)、amount=千元(2e5) ⇒ 未归一时均价 0.01 元。
	tushareRows := []map[string]any{
		{"ts_code": "600001.SH", "trade_date": date, "close": 10.0, "pre_close": 10.0,
			"open": 10.0, "high": 10.5, "low": 9.8, "change": 0.0, "pct_chg": 0.0,
			"vol": 200000.0, "amount": 200000.0},
		{"ts_code": "600002.SH", "trade_date": date, "close": 12.0, "pre_close": 12.0,
			"open": 12.0, "high": 12.6, "low": 11.5, "change": 0.0, "pct_chg": 0.0,
			"vol": 100000.0, "amount": 120000.0},
	}

	// ① 未归一直落库（旧形态）：抽检必须判千元红——这条是"摘掉守卫必红"的反证支。
	if _, err := db.InsertRows("daily", store.TableColumns("daily"), copyRows(tushareRows)); err != nil {
		t.Fatalf("insert 未归一批: %v", err)
	}
	p, err := db.ProbeDailyAmountScale(date, 0)
	if err != nil {
		t.Fatalf("probe 未归一: %v", err)
	}
	if p.Verdict != store.AmountScaleThousand || !p.Red() {
		t.Fatalf("未归一形态抽检 verdict=%s red=%v，期望 thousand-yuan/红", p.Verdict, p.Red())
	}

	// ② 归一后落库（同一批数据，整批重写语义与断点续传一致）：抽检必须判绿。
	if changed := data.NormalizeTushareAmount("daily", tushareRows); changed != len(tushareRows) {
		t.Fatalf("归一行数=%d，期望 %d", changed, len(tushareRows))
	}
	if _, err := db.InsertRows("daily", store.TableColumns("daily"), tushareRows); err != nil {
		t.Fatalf("insert 归一批: %v", err)
	}
	p, err = db.ProbeDailyAmountScale(date, 0)
	if err != nil {
		t.Fatalf("probe 归一: %v", err)
	}
	if p.Verdict != store.AmountScaleOK || p.Red() {
		t.Fatalf("归一后抽检 verdict=%s red=%v reason=%s，期望 ok/绿", p.Verdict, p.Red(), p.Reason)
	}

	// ③ 重复归一（有人把换算挪进重试循环就会走到这里）：抽检必须判 over-scaled 红。
	data.NormalizeTushareAmount("daily", tushareRows)
	if _, err := db.InsertRows("daily", store.TableColumns("daily"), tushareRows); err != nil {
		t.Fatalf("insert 二次归一批: %v", err)
	}
	p, err = db.ProbeDailyAmountScale(date, 0)
	if err != nil {
		t.Fatalf("probe 二次归一: %v", err)
	}
	if p.Verdict != store.AmountScaleOver || !p.Red() {
		t.Fatalf("二次归一抽检 verdict=%s red=%v，期望 over-scaled/红（重复换算必须能被发现）", p.Verdict, p.Red())
	}
}

// copyRows 深拷贝一份 map 载荷，避免反证支的未归一数据被后续用例就地改写。
func copyRows(in []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(in))
	for _, r := range in {
		c := make(map[string]any, len(r))
		for k, v := range r {
			c[k] = v
		}
		out = append(out, c)
	}
	return out
}
