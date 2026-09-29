// §0929DIM 龙头战法：F1~F4 维度理由必须在「评分→信号」边界上活下来。
//
// 缺陷因果链（完整版本见 internal/server/signal_dim_desc_test.go 头注）：四维**分数**一直经
// Meta 过界，文本却被 GenerateSignal 整段丢弃——前端 D1~D4 单元格里"分数旁边那句依据"的
// 渲染分支从未走到。本文件锁**产地侧**：EvaluateReal 要产出文本、GenerateSignal 要把文本
// 带走、且带走的是副本而不是同一张 map。
//
//	T1 四个维度键都有话，且文本里的原值可核对（F4 的 5 日趋势、F2 的板块最强涨幅）；
//	T2 F1 的"贴板/未贴板"措辞与 F1 分值**同向**——专防"阈值改了文案没改"的假解释；
//	T3 GenerateSignal 把四维理由原样带到 Signal.Reasons；
//	T4 拷贝语义：改动 eval.Reasons 不得回灌已发出的信号（理由要定格在触发那一刻）。
//
// English: producer-side locks for the §0929DIM bridge — EvaluateReal must emit one reason string per
// factor with verifiable raw values, the F1 wording must agree with the F1 score, GenerateSignal must
// carry all four, and the carry must be a clone.
package dragon

import (
	"strings"
	"testing"

	"quant-trading-v2/internal/data"
)

// TestDragonReasonsProduced T1+T2：评分产出四维理由，且措辞与分值同向。
func TestDragonReasonsProduced(t *testing.T) {
	d := New(newCfg())
	// klines(10,12)：末根收盘 12.0，5 根前 11.1111 → 5 日趋势 +8.0%（>5% 半档）；
	// 板块最强 10%；个股涨幅 9.9%（主板阈值下贴板与否由 data.LimitUpPct 决定，见 T2 的同向断言）。
	ev := d.EvaluateReal("600000", strongSI(), klines(10, 12), []data.SectorInfo{{ChangePct: 10}})
	if ev == nil {
		t.Fatal("EvaluateReal 不应返回 nil")
	}
	for _, k := range []string{"f1_seal", "f2_resonance", "f3_premium", "f4_rs"} {
		if v, ok := ev.Reasons[k]; !ok || v == "" {
			t.Fatalf("§0929DIM：评分未产出 %s 的维度理由（got %q）", k, v)
		}
	}
	if got := ev.Reasons["f4_rs"]; got != "5日趋势+8.0%" {
		t.Fatalf("§0929DIM：F4 理由与评分输入不符，期望「5日趋势+8.0%%」got %q", got)
	}
	if got := ev.Reasons["f2_resonance"]; got != "板块共振10.0%" {
		t.Fatalf("§0929DIM：F2 理由错误，期望「板块共振10.0%%」got %q", got)
	}
	if got := ev.Reasons["f3_premium"]; got != "自身涨9.9%未超板块" {
		t.Fatalf("§0929DIM：F3 理由错误，期望「自身涨9.9%%未超板块」got %q", got)
	}

	// T2 同向锁：F1 分值>0 ⇔ 文本不写"未贴板"。
	// 不硬编码涨停阈值（主板/双创/北交所由 data.LimitUpPct 裁决），但分数与话术必须讲同一件事。
	sealed := ev.Details["f1_seal"] > 0
	textSaysSealed := !strings.Contains(ev.Reasons["f1_seal"], "未贴板")
	if sealed != textSaysSealed {
		t.Fatalf("§0929DIM：F1 分值(%.2f)与理由文本(%q)对封板的判断不一致", ev.Details["f1_seal"], ev.Reasons["f1_seal"])
	}
}

// TestDragonReasonsCarried T3+T4：四维理由随信号过界，且是独立副本。
func TestDragonReasonsCarried(t *testing.T) {
	d := New(newCfg())
	ev := d.EvaluateReal("600000", strongSI(), klines(10, 12), []data.SectorInfo{{ChangePct: 10}})
	if ev == nil {
		t.Fatal("EvaluateReal 不应返回 nil")
	}
	sig, err := d.GenerateSignal("600000", ev)
	if err != nil || sig == nil {
		t.Fatalf("GenerateSignal 失败: %v", err)
	}
	for k, want := range ev.Reasons {
		if got := sig.Reasons[k]; got != want {
			t.Fatalf("§0929DIM：%s 的理由在信号侧变成 %q（评分侧 %q）——文本又被丢了", k, got, want)
		}
	}
	if len(sig.Reasons) != len(ev.Reasons) {
		t.Fatalf("§0929DIM：信号侧理由键数 %d ≠ 评分侧 %d", len(sig.Reasons), len(ev.Reasons))
	}

	// T4：评分对象事后被改写，已发出信号的理由必须保持原样（共享 map 会顺着引用改掉历史解释）
	ev.Reasons["f4_rs"] = "事后改写"
	if sig.Reasons["f4_rs"] == "事后改写" {
		t.Fatalf("§0929DIM：Reasons 与评分对象共享同一张 map，历史信号的理由会被后续重评改写")
	}
}
