// §0929DIM N形战法：d1~d4 维度理由必须在「评分→信号」边界上活下来。
//
// N形是四主战法里**唯一**在评分阶段就把四维中文话术全算好的（scorer 产出 D2Desc/D3Desc/D4Desc，
// n_shape.EvaluateWave 把它们收进 Evaluation.Reasons）。此前 GenerateSignal 只搬 Details 进 Meta，
// 这套文本随评分对象一起被丢弃，前端 d3_desc/d4_desc 恒空——本文件锁住这条产地侧通道。
//
//	T1 GenerateSignal 把 eval.Reasons 的四维原文按键带到 Signal.Reasons；
//	T2 拷贝语义：评分对象事后被改写，已发出信号的理由必须保持原样；
//	T3 无理由评分（stub/noscore）给出 nil Reasons，与下游 omitempty 口径一致；
//	T4 真实评分链 EvaluateWave→GenerateSignal 端到端把四维话术带到信号上。
//
// T1~T3 用**手搭评分**锁 GenerateSignal 的搬运行为（边界本身）；
// T4 用**真实评分链**（EvaluateWave→GenerateSignal）锁端到端：scorer 算出的 d2/d3/d4 话术
// 必须一路活到信号上——这条一旦红，说明丢文本的缝又开在评分与信号之间某处。
//
// English: N-shape is the only core strategy that already words all four dimensions at scoring time;
// T1-T3 pin the transport itself with a hand-built evaluation, T4 pins the end-to-end chain
// (EvaluateWave → GenerateSignal) so the scorer's wording provably survives.
package n_shape

import (
	"strings"
	"testing"

	"quant-trading-v2/internal/strategy"
)

// reasonsEval 构造一份带四维理由的评分（full_chain 档，确保 GenerateSignal 真产出信号）。
func reasonsEval() *strategy.Evaluation {
	return &strategy.Evaluation{
		Level:      "full_chain",
		Pass:       true,
		Confidence: 0.85,
		TotalScore: 85,
		Details:    map[string]float64{"d1": 30, "d2": 20, "d3": 15, "d4": 10},
		Reasons: map[string]string{
			"d1": "事件:重组,中标",
			"d2": "竞价强/放量/超额收益",
			"d3": "缩量回调6日不破位",
			"d4": "承接放量/资金确认",
		},
	}
}

// TestNShapeGenerateSignalCarriesReasons T1：四维理由逐键原样过界，且分值映射不受影响。
func TestNShapeGenerateSignalCarriesReasons(t *testing.T) {
	n := newNS()
	ev := reasonsEval()
	sig, err := n.GenerateSignal("600000", ev)
	if err != nil || sig == nil {
		t.Fatalf("GenerateSignal 失败: %v", err)
	}
	for k, want := range ev.Reasons {
		if got := sig.Reasons[k]; got != want {
			t.Fatalf("§0929DIM：N形 %s 的理由在信号侧变成 %q（评分侧 %q）——文本又被丢了", k, got, want)
		}
	}
	if len(sig.Reasons) != 4 {
		t.Fatalf("§0929DIM：信号侧理由键数应为 4，got %d（%v）", len(sig.Reasons), sig.Reasons)
	}
	// 分值侧保持原口径（本批只补文本，不许顺手动 Meta）
	if sig.Meta["d1"] != 30 || sig.Meta["d4"] != 10 {
		t.Fatalf("§0929DIM：Meta 分值映射被改动（d1=%v d4=%v）", sig.Meta["d1"], sig.Meta["d4"])
	}
}

// TestNShapeReasonsAreCloned T2：信号持有的是副本，评分对象后续改写不得污染历史信号。
func TestNShapeReasonsAreCloned(t *testing.T) {
	n := newNS()
	ev := reasonsEval()
	sig, _ := n.GenerateSignal("600000", ev)
	ev.Reasons["d3"] = "事后改写"
	if sig.Reasons["d3"] == "事后改写" {
		t.Fatalf("§0929DIM：Reasons 与评分对象共享同一张 map，复盘看到的不再是触发那一刻的理由")
	}
	if sig.Reasons["d3"] != "缩量回调6日不破位" {
		t.Fatalf("§0929DIM：副本内容也被带偏，got %q", sig.Reasons["d3"])
	}
}

// TestNShapeReasonsNilWhenAbsent T3：无理由的评分（如 noscore/stub）应给 nil 而不是空 map。
// 空 map 会让下游 len()==0 与 ==nil 两套判据分叉，也让 combat_agent 的 omitempty 失去意义。
func TestNShapeReasonsNilWhenAbsent(t *testing.T) {
	n := newNS()
	sig, err := n.GenerateSignal("600000", &strategy.Evaluation{Level: "fail", Confidence: 0.3})
	if err != nil || sig == nil {
		t.Fatalf("GenerateSignal 失败: %v", err)
	}
	if sig.Reasons != nil {
		t.Fatalf("§0929DIM：无理由评分应给出 nil Reasons, got %#v", sig.Reasons)
	}
}

// TestNShapeReasonsSurviveRealChain T4：真实评分链（满分环境）产出的四维话术必须原样活到信号上。
// 这条是本批最贴近用户可见症状的锁：前端 d3_desc/d4_desc 恒空就是被"GenerateSignal 不搬 Reasons"
// 这一步造成的，而 T1 的手搭评分只能证明"搬的动作"存在，证不了"scorer 的话术真进了这张表"。
// English: end-to-end within the package — the scorer's four dimension wordings must survive into the
// signal, which is exactly the step whose absence left the frontend's d3_desc/d4_desc permanently empty.
func TestNShapeReasonsSurviveRealChain(t *testing.T) {
	n := newNS()
	ev, err := n.EvaluateWave(fullWA(), fullIB(), fullCtx())
	if err != nil || ev == nil {
		t.Fatalf("EvaluateWave 失败: %v", err)
	}
	if ev.Level != "full_chain" {
		t.Fatalf("满分环境应评 full_chain, got %s", ev.Level)
	}
	sig, err := n.GenerateSignal("600000", ev)
	if err != nil || sig == nil {
		t.Fatalf("GenerateSignal 失败: %v", err)
	}
	for _, k := range []string{"d1", "d2", "d3", "d4"} {
		if ev.Reasons[k] == "" {
			t.Fatalf("§0929DIM：真实评分链未产出 %s 的维度理由", k)
		}
		if sig.Reasons[k] != ev.Reasons[k] {
			t.Fatalf("§0929DIM：%s 的话术在信号侧丢失或改写（评分 %q / 信号 %q）", k, ev.Reasons[k], sig.Reasons[k])
		}
	}
	// D1 的措辞由 d1desc 定：无匹配事件时就是「无事件」，有匹配时以「事件:」开头。
	// 这两个形态之一就是产地侧的真值，前端拿到空串即为本缺陷复发。
	if d1 := sig.Reasons["d1"]; d1 != "无事件" && !strings.HasPrefix(d1, "事件:") {
		t.Fatalf("§0929DIM：d1 理由既非「无事件」也非「事件:…」开头，got %q", d1)
	}
}
