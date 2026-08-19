// n_shape N形超短：四维评分链路、信号分档与情绪硬闸。
package n_shape

import (
	"testing"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/strategy"
)

func newNS() *NShapeStrategy {
	return New(config.NewManager(""), data.NewEventMatcher(&data.EventsConfig{}))
}

// fullCtx 构造 D1=LLM满评分 + D2/D3/D4 满分 的环境，确保 full_chain。
func fullCtx() *Ctx {
	return &Ctx{
		LLMD1Score:         40, // D1 = 40（0~40 满分制，calcD1 直接采用）
		EmotionPhase:       "启动",
		SectorTurnoverMA20: 0, // 跳过板块冷清检查
		PreEventReturn5d:   0.1,
		StockPE:            12, // D3 = 20
		AvgDailyVol:        1000,
	}
}

func fullIB() *IntradayB {
	return &IntradayB{
		TTime:         935,
		CurPrice:      12.0,
		PrevClose:     11.5,
		PrevHigh:      5.0,
		PrevLow:       1.0,
		CumVol:        200,  // 量比 200/(1000*0.1)=2.0 → D2b=8
		AuctionChgPct: 2.5,  // D2a=15
		BenchCurChg:   0.01, // 超额→D2c=7
		AvgDailyVol:   1000,
		MinuteMACDDIF: 1.0, // D4a=5
		MinuteMACDDEA: 0.5,
	}
}

func fullWA() *WaveA {
	return &WaveA{AHigh: 13, ALow: 11, AChgPct: 0.06, AAboveMA60: true}
}

// TestEvaluateWaveFullChain 完整强信号 → full_chain/pass 且各维明细齐全。
func TestEvaluateWaveFullChain(t *testing.T) {
	ev, err := newNS().EvaluateWave(fullWA(), fullIB(), fullCtx())
	if err != nil {
		t.Fatalf("EvaluateWave err: %v", err)
	}
	if !ev.Pass || ev.Level != "full_chain" || ev.TotalScore < 60 {
		t.Fatalf("强信号应 full_chain, got level=%s pass=%v total=%.0f", ev.Level, ev.Pass, ev.TotalScore)
	}
	for _, k := range []string{"d1", "d2", "d3", "d4", "prio", "remind", "canopen", "left_signal"} {
		if _, ok := ev.Details[k]; !ok {
			t.Errorf("明细缺少 %s", k)
		}
	}
}

// TestEmotionGate 衰退期被硬闸拦截，不产出通过信号。
func TestEmotionGate(t *testing.T) {
	ctx := fullCtx()
	ctx.EmotionPhase = "衰退"
	ev, _ := newNS().EvaluateWave(fullWA(), fullIB(), ctx)
	if ev.Pass {
		t.Error("衰退期不应通过")
	}
}

// TestLLMBlocked LLM 利空判定直接否决。
func TestLLMBlocked(t *testing.T) {
	ctx := fullCtx()
	ctx.LLMBlocked = true
	ev, _ := newNS().EvaluateWave(fullWA(), fullIB(), ctx)
	if ev.Pass {
		t.Error("LLM 利空不应通过")
	}
}

// TestGenerateSignal 各信号级别到操作/优先级映射。
func TestGenerateSignal(t *testing.T) {
	n := newNS()

	sig, _ := n.GenerateSignal("1", &strategy.Evaluation{Level: "full_chain", Confidence: 0.85})
	if sig.Action != strategy.ActionBuy || sig.Priority != strategy.P1 {
		t.Errorf("full_chain 高置信应 buy/P1, got %s/%d", sig.Action, sig.Priority)
	}

	sig, _ = n.GenerateSignal("1", &strategy.Evaluation{Level: "full_chain", Confidence: 0.65})
	if sig.Priority != strategy.P2 {
		t.Errorf("full_chain 中置信应 P2, got %d", sig.Priority)
	}

	sig, _ = n.GenerateSignal("1", &strategy.Evaluation{Level: "left_signal"})
	if sig.Action != strategy.ActionBuy || sig.Priority != strategy.P2 {
		t.Errorf("left_signal 应 buy/P2, got %s/%d", sig.Action, sig.Priority)
	}

	sig, _ = n.GenerateSignal("1", &strategy.Evaluation{Level: "right_signal"})
	if sig.Action != strategy.ActionBuy || sig.Priority != strategy.P1 {
		t.Errorf("right_signal 应 buy/P1, got %s/%d", sig.Action, sig.Priority)
	}

	// 低置信 + 左侧一突（且 D1>0）→ 提升至 P2
	sig, _ = n.GenerateSignal("1", &strategy.Evaluation{Level: "full_chain", Confidence: 0.5, Details: map[string]float64{"left_signal": 1, "d1": 1}})
	if sig.Priority != strategy.P2 {
		t.Errorf("left_signal 提升优先级至 P2, got %d", sig.Priority)
	}

	// 无 D1（left_signal 但 d1=0）→ 不提升优先级
	sig, _ = n.GenerateSignal("1", &strategy.Evaluation{Level: "full_chain", Confidence: 0.5, Details: map[string]float64{"left_signal": 1, "d1": 0}})
	if sig.Priority == strategy.P2 {
		t.Errorf("d1=0 时不应提升优先级至 P2, got %d", sig.Priority)
	}

	// 失败级别 → 观察
	sig, _ = n.GenerateSignal("1", &strategy.Evaluation{Level: "fail", Confidence: 0.3})
	if sig.Action != strategy.ActionWatch {
		t.Errorf("fail 应 watch, got %s", sig.Action)
	}
}

// TestEvaluatePlaceholder 标准接口占位实现不 panic。
func TestEvaluatePlaceholder(t *testing.T) {
	ev, err := newNS().Evaluate("x", nil)
	if err != nil || ev.Pass {
		t.Error("占位 Evaluate 应 Pass=false")
	}
}

// TestNPhaseString 状态机枚举字符串映射。
func TestNPhaseString(t *testing.T) {
	cases := []struct {
		p    NPhase
		want string
	}{
		{NPhaseIdle, "idle"}, {NPhaseFirstBreakout, "first_breakout"},
		{NPhaseFlag, "flag"}, {NPhaseSecondBreakout, "second_breakout"},
		{NPhaseCompleted, "completed"}, {NPhaseFailed, "failed"},
	}
	for _, c := range cases {
		if got := NPhaseString(c.p); got != c.want {
			t.Errorf("NPhaseString(%d)=%s, want %s", c.p, got, c.want)
		}
	}
}

// TestRemindToInt 提醒级别数值映射。
func TestRemindToInt(t *testing.T) {
	if remindToInt("strong") != 3 || remindToInt("observe") != 2 || remindToInt("mute") != 1 || remindToInt("x") != 0 {
		t.Error("remindToInt 映射错误")
	}
}

// TestLeftSignalRequiresD1 验证一突(left_signal)必须 D1>0：
// 价格突破前高×1.005 且量比≥1.8 的形态，若无有效 D1 事件分则不标一突，
// 杜绝"无特定事件"占位低分/零分仍触发左侧买入提醒。
// English: verifies the left breakout (一突) label requires a valid D1 event score — the breakout shape
// alone (price>prev-high×1.005, volume ratio≥1.8) doesn't set LeftSignal when D1=0.
func TestLeftSignalRequiresD1(t *testing.T) {
	s := NewLeftSideScorer(nil)

	ctx := fullCtx()
	ctx.LLMD1Score = 0 // 无实质事件 → D1=0，仅形态突破
	if sr := s.Evaluate(fullWA(), fullIB(), ctx); sr == nil {
		t.Fatal("Evaluate 返回 nil")
	} else if sr.LeftSignal {
		t.Fatalf("D1=0 时不应标一突(left_signal), got LeftSignal=%v", sr.LeftSignal)
	}

	ctx.LLMD1Score = 40 // 有效 D1
	if sr := s.Evaluate(fullWA(), fullIB(), ctx); sr == nil {
		t.Fatal("Evaluate 返回 nil")
	} else if !sr.LeftSignal {
		t.Fatalf("D1>0 且突破形态应标一突(left_signal), got LeftSignal=%v", sr.LeftSignal)
	}
}
