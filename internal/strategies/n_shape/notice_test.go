// notice_test.go — §B6（2026-11-05 通知增强）n形买入通知的档位判定与文案要素。
//
// 钉两件事：① "买入档"只认 priorityOf 的 strong + CanOpen（观察档不许占必推通道）；
// ② 文案要素缺一不可（票 + 形态阶段 + D1/情绪/是否龙头 + 有效期）——缺一项就退化成"刷一条标题"。
package n_shape

import (
	"strings"
	"testing"
)

// TestBuyTierFollowsPriority 买入档判定：强分且可开仓才算，observe/mute 都不算。
// English: buy tier requires the strong label AND can-open; observe/mute never qualify.
func TestBuyTierFollowsPriority(t *testing.T) {
	strong := priorityOf(t935, 30, true, "启动")
	if !strong.IsBuyTier() {
		t.Fatalf("黄金窗口 + 高 D1 应判买入档，实得 %+v", strong)
	}
	observe := priorityOf(t1430, 30, false, "退潮")
	if observe.IsBuyTier() {
		t.Errorf("退潮 14:30 的低优先级不该算买入档，实得 %+v", observe)
	}
	mute := priorityOf(t1430, 30, false, "衰退")
	if mute.IsBuyTier() {
		t.Errorf("情绪硬闸（衰退）否决后仍判买入档")
	}
}

// TestBuyNoticeTextCarriesEveryElement 文案要素逐条核对：少一项值班就没法决策。
// English: every notice element is asserted — a missing one leaves the operator unable to act.
func TestBuyNoticeTextCarriesEveryElement(t *testing.T) {
	n := BuyNotice{
		Code: "600519", Name: "测试股", Stage: "右侧二突", D1: 32, Emotion: "启动",
		IsLeader: true, Priority: 100, Remind: "strong", CanOpen: true,
	}
	if !n.BuyTier() {
		t.Fatal("strong+CanOpen 应判买入档")
	}
	text := n.BuyText()
	for _, want := range []string{
		"600519",  // 票（代码）
		"测试股",     // 票（名称）
		"右侧二突",    // 形态阶段
		"D1=32.0", // D1 输入值
		"情绪=启动",   // 情绪输入值
		"板块龙头",    // 是否龙头
		"有效期",     // 有效期
		"15:00",   // 有效期落点（当日时间优先级窗口）
	} {
		if !strings.Contains(text, want) {
			t.Errorf("买入通知文案缺要素 %q，全文=%s", want, text)
		}
	}
	// 非买入档（observe）仍要能出文案，但分级器按 BuyTier 决定去向——这里只证判定不判文案
	weak := BuyNotice{Code: "600519", Name: "测试股", Remind: "observe", CanOpen: false}
	if weak.BuyTier() {
		t.Error("observe 档不该判买入（它会去抢必推通道）")
	}
}

// TestBuyNoticeFromScoreResult 装配点：Valid+CanOpen 才产通知，阶段串按右突>一突>完整链取。
// English: the assembly point only yields a notice for Valid+CanOpen, naming the newest confirmed stage.
func TestBuyNoticeFromScoreResult(t *testing.T) {
	res := &ScoreResult{Valid: true, CanOpen: true, RemindLevel: "strong", Priority: 100, D1Event: 28, RightSignal: true}
	n, ok := BuyNoticeFrom("600519", "测试股", res, "启动", true)
	if !ok {
		t.Fatal("Valid+CanOpen 应装配出买入通知")
	}
	if n.Stage != "右侧二突" || !n.BuyTier() {
		t.Errorf("装配结果不对：%+v", n)
	}
	// Valid=false（分数没过 60 硬闸）与 CanOpen=false（10 点后未达强分）都不许产通知
	if _, ok := BuyNoticeFrom("600519", "测试股", &ScoreResult{Valid: false, CanOpen: true}, "启动", true); ok {
		t.Error("未过 Valid 硬闸仍产出了买入通知")
	}
	if _, ok := BuyNoticeFrom("600519", "测试股", &ScoreResult{Valid: true, CanOpen: false}, "启动", true); ok {
		t.Error("未达强分（CanOpen=false）仍产出了买入通知")
	}
	if _, ok := BuyNoticeFrom("600519", "测试股", nil, "启动", true); ok {
		t.Error("res 为 nil 却产出了通知（nil 解引用前的假成功）")
	}
	// 一突优先于完整链：LeftSignal 命中就要点名"左侧一突"
	left := &ScoreResult{Valid: true, CanOpen: true, RemindLevel: "strong", LeftSignal: true, D1Event: 20}
	if n2, _ := BuyNoticeFrom("600588", "另一股", left, "高潮", false); n2.Stage != "左侧一突" {
		t.Errorf("一突阶段串错：%s", n2.Stage)
	}
}
