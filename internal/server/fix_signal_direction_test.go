// fix_signal_direction_test.go — 2026-11-10 owner 需求：信号方向归类（做空战法正式归入做空）。
//
// 锁两件事：
//  1. 四个做空战法（高位滞涨/放量破位/龙头断板/利好兑现砸盘，含英文名 StrategyType）的信号
//     在 /api/signals 下发层（toFixSignals）Direction 一律归一为 "做空"——即使引擎侧 Direction
//     缺失/为空（这正是此前前端 `(s.direction || '做多')` 兜底把做空战法漏进做多筛选的根因）；
//  2. 「卖点评估」保持现有 direction 语义不动（做多模式下"提醒"、做空模式下"做空"），
//     做多战法信号 Direction 原样透传（空=历史兼容，omitempty）。
//
// English: locks bear-tactic direction normalization at the /api/signals assembly boundary;
// 卖点评估 keeps its existing direction semantics.
package server

import (
	"testing"

	"quant-trading-v2/internal/combat_agent"
)

func TestToFixSignalsShortTacticDirectionNormalized(t *testing.T) {
	in := []combat_agent.Signal{
		// 做空战法：Direction 缺失（引擎历史路径可能不带）也必须归一为做空
		{Code: "600001", Strategy: "高位滞涨", StrategyType: "high_churn", Action: "sell"},
		{Code: "600002", Strategy: "放量破位", StrategyType: "break_down", Action: "sell", Direction: "做多"},
		{Code: "600003", Strategy: "龙头断板", StrategyType: "leader_decay", Action: "sell"},
		{Code: "600004", Strategy: "利好兑现砸盘", StrategyType: "good_news_fade", Action: "sell", Direction: ""},
		// 做多战法：原样透传
		{Code: "600005", Strategy: "龙头", StrategyType: "dragon", Action: "buy", Direction: "做多"},
		// 卖点评估：保持现有 direction 语义（做多模式下"提醒"）
		{Code: "600006", Strategy: "卖点评估", StrategyType: "high_churn", Action: "sell", Direction: "提醒"},
		// Direction 为空的做多信号：历史兼容（omitempty，前端按缺省做多处理）
		{Code: "600007", Strategy: "N形", StrategyType: "n_shape", Action: "buy"},
	}
	out := toFixSignals(in)
	if len(out) != len(in) {
		t.Fatalf("条数应一致，got %d want %d", len(out), len(in))
	}
	want := map[string]string{
		"600001": "做空", // 高位滞涨：Direction 缺失 → 归一做空
		"600002": "做空", // 引擎误标做多也强制纠正
		"600003": "做空",
		"600004": "做空",
		"600005": "做多",
		"600006": "提醒", // 卖点评估语义不动
		"600007": "",   // 历史兼容：空=前端按做多处理
	}
	for _, fs := range out {
		if fs.Direction != want[fs.Code] {
			t.Errorf("%s (%s) Direction=%q, want %q", fs.Code, fs.Strategy, fs.Direction, want[fs.Code])
		}
	}
}
