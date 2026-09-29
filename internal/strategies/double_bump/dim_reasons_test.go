// §0929DIM 双凸战法：vol/adjust/ma/振幅四维理由必须在「评分→信号」边界上活下来。
//
// 缺陷因果链完整版见 internal/server/signal_dim_desc_test.go 头注：四维**分数**一直经 Meta 过界，
// 文本被 GenerateSignal 整段丢弃，前端 d1_desc~d4_desc 的说明分支从未走到。
// 双凸此前**连 Reasons 都不产出**（EvaluateReal 只填 Details），所以本文件同时锁产地与搬运两侧。
//
//	T1 EvaluateReal 产出四个维度键的中文理由，且文本里的原值与评分输入一致
//	   （均线位置、当日量/均量倍数、当日振幅——都是评分现场已经算过的量，不是事后编的话）；
//	T2 GenerateSignal 把四维理由带到 Signal.Reasons；
//	T3 拷贝语义 + 无理由评分给 nil（与下游 omitempty 口径一致）。
//
// English: double_bump previously produced no dimension reasons at all, so this locks both ends —
// EvaluateReal must word each factor from the values it already computed, GenerateSignal must carry
// them as a clone, and a reasonless evaluation must yield nil.
package double_bump

import (
	"strings"
	"testing"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/strategy"
)

// greenBump 构造"绿日放量上攻"的双凸场景（与 TestGreenDayFullChain 同形态）。
// 手工可核的中间量：近 20 日（去末根）均量 108000、均价 10.145，
// 今日量 250000 ⇒ 2.3 倍均量；末根振幅 1.0/10.145 ⇒ 9.9%；MA5=10.92 > MA10=10.46 且收盘 11.7 站上 MA5。
func greenBump() (*data.StockInfo, []data.KLine) {
	closes := make([]float64, 30)
	vol := make([]float64, 30)
	for i := 0; i < 30; i++ {
		closes[i] = 10.0
		vol[i] = 100000
	}
	closes[27] = 11.6
	vol[27] = 300000
	closes[28] = 11.3
	vol[28] = 60000
	closes[29] = 11.7
	vol[29] = 250000
	si := &data.StockInfo{Code: "600001", Name: "双凸票", Price: closes[29], ChangePct: 1.2}
	return si, kbump(closes, vol)
}

// TestDoubleBumpReasonsProduced T1：四维理由齐备，且每句都讲得出评分现场的原始量。
func TestDoubleBumpReasonsProduced(t *testing.T) {
	si, kl := greenBump()
	ev := newTest().EvaluateReal("600001", si, kl)
	if ev == nil {
		t.Fatal("EvaluateReal 不应返回 nil")
	}
	for _, k := range []string{"vol_score", "adjust_score", "ma_score", "adjust_depth"} {
		if v, ok := ev.Reasons[k]; !ok || v == "" {
			t.Fatalf("§0929DIM：双凸评分未产出 %s 的维度理由（got %q）", k, v)
		}
	}
	// 均线维：等值锁。10.92/10.46 由测试数据手算得到，不是从实现里读出来的。
	if got := ev.Reasons["ma_score"]; got != "多头排列且站稳MA5(10.92/10.46)" {
		t.Fatalf("§0929DIM：均线理由与输入不符，期望「多头排列且站稳MA5(10.92/10.46)」got %q", got)
	}
	// 量能维：今日量 250000 / 均量 108000 = 2.3 倍
	if !strings.Contains(ev.Reasons["vol_score"], "2.3倍均量") {
		t.Fatalf("§0929DIM：量能理由未报出当日量/均量倍数，got %q", ev.Reasons["vol_score"])
	}
	// 振幅维：末根 High-Low=1.0，均价 10.145 ⇒ 9.9%
	if got := ev.Reasons["adjust_depth"]; got != "当日振幅9.9%" {
		t.Fatalf("§0929DIM：振幅理由与输入不符，期望「当日振幅9.9%%」got %q", got)
	}
	// 调整分与振幅文本必须讲同一件事：窄幅判据（<AdjustVolRatioMax×2）与振幅值同现
	if !strings.Contains(ev.Reasons["adjust_score"], "振幅9.9%") {
		t.Fatalf("§0929DIM：调整维度理由没引用当日振幅，got %q", ev.Reasons["adjust_score"])
	}
}

// TestDoubleBumpReasonsCarried T2+T3：理由随信号过界、是独立副本、无理由时为 nil。
func TestDoubleBumpReasonsCarried(t *testing.T) {
	si, kl := greenBump()
	d := newTest()
	ev := d.EvaluateReal("600001", si, kl)
	if ev == nil {
		t.Fatal("EvaluateReal 不应返回 nil")
	}
	sig, err := d.GenerateSignal("600001", ev)
	if err != nil || sig == nil {
		t.Fatalf("GenerateSignal 失败: %v", err)
	}
	for k, want := range ev.Reasons {
		if got := sig.Reasons[k]; got != want {
			t.Fatalf("§0929DIM：%s 的理由在信号侧变成 %q（评分侧 %q）——文本又被丢了", k, got, want)
		}
	}
	ev.Reasons["ma_score"] = "事后改写"
	if sig.Reasons["ma_score"] == "事后改写" {
		t.Fatalf("§0929DIM：Reasons 与评分对象共享同一张 map，历史信号的理由会被后续重评改写")
	}

	// 无理由评分（watch 档手搭）→ nil，而不是空 map
	bare, _ := d.GenerateSignal("600002", &strategy.Evaluation{Level: "watch", Confidence: 0.2})
	if bare != nil && bare.Reasons != nil {
		t.Fatalf("§0929DIM：无理由评分应给出 nil Reasons, got %#v", bare.Reasons)
	}
}
