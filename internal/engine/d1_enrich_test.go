package engine

import (
	"testing"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/newsagent"
)

// TestEnrichSignalsWithD1 验证 enrichSignalsWithD1：把真实 D1 事件信息（评分/负面拦截/LLM理由/事件标题）
// 回填到信号上，方向匹配选择对应利好/利空事件标题；无 D1 评分的信号保持原样。
// English: TestEnrichSignalsWithD1 verifies enrichSignalsWithD1: backfills real D1 event info (score/negative block/LLM reason/event title)
// onto signals, direction matching picks the corresponding bullish/bearish event title; signals without a D1 score stay unchanged.
func TestEnrichSignalsWithD1(t *testing.T) {
	d1s := map[string]combat_agent.D1Score{
		"000001": {Code: "000001", Score: 0.8, Blocked: false, Reason: "中标海外储能大单"},
		"000002": {Code: "000002", Score: 0.0, Blocked: true, Reason: "股东减持"},
	}
	briefs := map[string][]combat_agent.NewsBrief{
		"000001": {{Title: "中标海外储能大单", Positive: true}},
		"000002": {{Title: "股东减持风险", Positive: false}},
		"000003": {{Title: "个股利好公告", Positive: true}},
	}
	sigs := []combat_agent.Signal{
		{Code: "000001", Direction: "做多"},
		{Code: "000002", Direction: "做空"},
		{Code: "000003", Direction: "做多"}, // 无 D1 评分，应保持原样
		// English: No D1 score, should stay unchanged.
	}

	enrichSignalsWithD1(sigs, d1s, briefs)

	if sigs[0].D1Score != 0.8 || sigs[0].D1Blocked || sigs[0].D1Reason != "中标海外储能大单" {
		t.Fatalf("000001 D1 未正确回填: %+v", sigs[0])
	}
	if sigs[0].D1Event != "中标海外储能大单" {
		t.Fatalf("000001 应匹配利好事件标题, got %q", sigs[0].D1Event)
	}
	if sigs[1].D1Score != 0 || !sigs[1].D1Blocked || sigs[1].D1Reason != "股东减持" {
		t.Fatalf("000002 D1 负面拦截未正确回填: %+v", sigs[1])
	}
	if sigs[1].D1Event != "股东减持风险" {
		t.Fatalf("000002 应匹配利空事件标题, got %q", sigs[1].D1Event)
	}
	// 000003 无 D1 评分缓存但有新闻事件：事件标题仍应回填（LLM D1 降级/缺失时事件独立展示）
	// English: 000003 has no D1 score cache but has a news event: the event title is still backfilled (event shown independently when LLM D1 is degraded/missing).
	if sigs[2].D1Score != 0 || sigs[2].D1Reason != "" {
		t.Fatalf("000003 无 D1 评分应保持零值: %+v", sigs[2])
	}
	if sigs[2].D1Event != "个股利好公告" {
		t.Fatalf("000003 应回填匹配方向的事件标题, got %q", sigs[2].D1Event)
	}
}

// TestEnrichSignalsWithD1NilD1 无 D1 评分缓存时（nil）不 panic 且不改动信号。
// English: TestEnrichSignalsWithD1NilD1 with a nil D1 score cache does not panic and does not change signals.
func TestEnrichSignalsWithD1NilD1(t *testing.T) {
	sigs := []combat_agent.Signal{{Code: "000001", Direction: "做多", Reason: "动量80"}}
	enrichSignalsWithD1(sigs, nil, nil)
	if sigs[0].D1Score != 0 || sigs[0].Reason != "动量80" {
		t.Fatalf("nil D1 不应改动信号: %+v", sigs[0])
	}
}

// TestNewsBriefsByCodeCleanedStocks 事件个股关联可能主要落在 CleanedStocks（"名称|代码"）而非
// RelatedStocks，newsBriefsByCode 应优先/一并消费 CleanedStocks，并兼容 "名称(代码)" 与裸代码。
// English: TestNewsBriefsByCodeCleanedStocks event-stock associations may mainly land in CleanedStocks ("name|code") rather than
// RelatedStocks; newsBriefsByCode should prefer/also consume CleanedStocks and be compatible with "name(code)" and bare codes.
func TestNewsBriefsByCodeCleanedStocks(t *testing.T) {
	events := []newsagent.NewsEvent{
		{Title: "迈为股份全自动D2W混合键合设备获客户追加订单", Score: 0.5,
			CleanedStocks: []string{"迈为股份|300751"}, RelatedStocks: nil},
		{Title: "中兴商业涨停", Score: 0.7,
			CleanedStocks: []string{"中兴商业|000715"}, RelatedStocks: []string{"中兴商业"}},
		{Title: "合力泰(002217)中标", Score: -0.3,
			CleanedStocks: nil, RelatedStocks: []string{"合力泰(002217)"}},
		{Title: "无关联事件", Score: 0.4},
	}
	m := newsBriefsByCode(events)
	if len(m["300751"]) != 1 || m["300751"][0].Title != "迈为股份全自动D2W混合键合设备获客户追加订单" || !m["300751"][0].Positive {
		t.Fatalf("CleanedStocks 未消费: %+v", m["300751"])
	}
	if len(m["000715"]) != 1 || m["000715"][0].Title != "中兴商业涨停" || m["000715"][0].Positive == false {
		t.Fatalf("RelatedStocks 补充未并入: %+v", m["000715"])
	}
	if len(m["002217"]) != 1 || m["002217"][0].Title != "合力泰(002217)中标" || m["002217"][0].Positive {
		t.Fatalf("name(code) 形式未解析出代码: %+v", m["002217"])
	}
	if len(m) != 3 {
		t.Fatalf("无关联事件不应产生简报, map=%v", m)
	}
}
