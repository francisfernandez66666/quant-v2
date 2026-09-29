// §0929DIM 龙回头战法：四因子理由必须在「评分→信号」边界上活下来。
//
// 缺陷因果链完整版见 internal/server/signal_dim_desc_test.go 头注。龙回头此前只把四因子分数
// （dragon_score/pullback_score/duck_score/confirm_score）写进 Details，评分现场已经拿到的
// 首涨/回调幅度与天数/缩量比/均线全部没有留下文字，前端 D1~D4 只有数字。
//
//	T1 Evaluate 产出四个因子键的中文理由，且句中的原始量与 goodStock() 的输入逐值对得上
//	   （等值锁，期望串按测试数据手算，不从实现里读）；
//	T2 GenerateSignal 把四维理由带到 Signal.Reasons（本战法的 Meta 直接挂 eval.Details，
//	   理由必须同样过去，否则分值有、话术没有）；
//	T3 拷贝语义 + 未通过评分（返回 nil 信号）不 panic。
//
// English: locks the producer side for dragon_return — Evaluate must word each of the four factors from
// the StockData inputs it scored, GenerateSignal must carry them onto the signal, and the carry is a
// clone. The failing-score path (nil signal) must stay panic-free.
package dragon_return

import (
	"testing"

	"quant-trading-v2/internal/strategy"
)

// TestDragonReturnReasonsProduced T1：四因子理由齐备且原值可核对。
func TestDragonReturnReasonsProduced(t *testing.T) {
	d := newDR()
	ev, err := d.Evaluate("0001", goodStock())
	if err != nil || ev == nil {
		t.Fatalf("Evaluate 失败: %v", err)
	}
	// goodStock()：IsSectorTop2=true、首涨 0.5、RPS20=80、回调 0.18/6 日/缩量 0.2、
	// MA5=11.0 MA10=11.2 MA20=10.8、现价 12.0 —— 下面四个期望串全部由这组输入手算。
	want := map[string]string{
		"dragon_score":   "板块Top2=true 首涨50% RPS20=80",
		"pullback_score": "回调18%/6日/缩量20%",
		"duck_score":     "鸭头(MA5 11.00/MA10 11.20/MA20 10.80)",
		"confirm_score":  "确认(现价12.00/MA5 11.00/量比0.20)",
	}
	for k, w := range want {
		got, ok := ev.Reasons[k]
		if !ok || got == "" {
			t.Fatalf("§0929DIM：龙回头评分未产出 %s 的维度理由（got %q）", k, got)
		}
		if got != w {
			// 期望串按 goodStock() 输入手算，% 只出现在数据串里、不在格式串里
			t.Fatalf("§0929DIM：%s 的理由与评分输入不符，期望「%s」got「%s」", k, w, got)
		}
	}
}

// TestDragonReturnReasonsCarried T2+T3：理由随信号过界、是独立副本、未通过评分不 panic。
func TestDragonReturnReasonsCarried(t *testing.T) {
	d := newDR()
	ev, _ := d.Evaluate("0001", goodStock())
	sig, err := d.GenerateSignal("0001", ev)
	if err != nil || sig == nil {
		t.Fatalf("通过评分的标的应产出信号, err=%v", err)
	}
	for k, want := range ev.Reasons {
		if got := sig.Reasons[k]; got != want {
			t.Fatalf("§0929DIM：%s 的理由在信号侧变成 %q（评分侧 %q）——文本又被丢了", k, got, want)
		}
	}
	ev.Reasons["confirm_score"] = "事后改写"
	if sig.Reasons["confirm_score"] == "事后改写" {
		t.Fatalf("§0929DIM：Reasons 与评分对象共享同一张 map，历史信号的理由会被后续重评改写")
	}

	// T3：未通过评分（总分<60）产出 nil 信号，链路不得 panic 也不必带理由
	failed, err := d.GenerateSignal("0002", &strategy.Evaluation{Pass: false, Level: "none", Confidence: 0.3})
	if err != nil {
		t.Fatalf("GenerateSignal err: %v", err)
	}
	if failed != nil {
		t.Fatalf("未通过评分不应产出信号, got %+v", failed)
	}
}
