// d1_incremental_test.go §D1-NOON（2026-10-22 遗留项②）增量 D1 空窗行为锁：
// 事件池被时效/重复衰减滤空后不得把整池评分重评归零（11:00–13:00 零窗），
// 新事件到达时仍必须按签名变化触发重评；跨日缓存必须失效。
package engine

import (
	"testing"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/newsagent"
	"quant-trading-v2/internal/strategy_engine"
)

// d1TestEvent 构造一条带时间戳、关联指定个股的新闻事件（签名依赖 Datetime+Title）。
func d1TestEvent(dt, title, code string) newsagent.NewsEvent {
	return newsagent.NewsEvent{
		Datetime:      dt,
		Title:         title,
		RelatedStocks: []string{code},
		Score:         1.0,
		Direction:     "利好",
		Level:         "个股",
	}
}

// TestPlanD1RoundNoonSeam 三段式复现中午零窗：早盘有事件评分 → 午间事件池滤空 →
// 下午新事件到达。核心断言：空窗轮复用上一轮分数且不覆写签名；新事件仍触发重评。
func TestPlanD1RoundNoonSeam(t *testing.T) {
	const code = "600000"
	pool := []string{code}
	md := map[string]*strategy_engine.StockMarketData{code: {Code: code, Name: "浦发银行"}}

	// ① 早盘：事件 E 在池，首轮无历史 → 必须进 toScore 评分。
	morning := []newsagent.NewsEvent{d1TestEvent("2026-10-22 09:35:00", "盈喜公告", code)}
	p1 := planD1Round(pool, morning, md, nil, nil, nil)
	if p1.empty || len(p1.toScore) != 1 || p1.toScore[0] != code {
		t.Fatalf("早盘首轮应重评 %s: %+v", code, p1)
	}
	if p1.sigNow[code] == "" {
		t.Fatal("早盘有事件时签名不得为空")
	}
	// 模拟 LLM 产出 15 分后引擎写回缓存。
	prevScores := map[string]combat_agent.D1Score{code: {Code: code, Score: 15, Reason: "盈喜"}}
	prevSig := p1.sigNow

	// ② 午间：最后一条有效事件被衰减滤出 0.5 线 → valid 为空。
	//    旧实现会把签名算成空串并重评 → noEvent 强制归 0（零窗本体）。
	p2 := planD1Round(pool, nil, md, nil, prevScores, prevSig)
	if !p2.empty {
		t.Fatal("空事件池应标记 empty")
	}
	if len(p2.toScore) != 0 {
		t.Fatalf("空窗轮不得重评（会整池归零），toScore=%v", p2.toScore)
	}
	if got := p2.reuse[code]; got.Score != 15 {
		t.Fatalf("空窗轮应沿用上一轮 15 分，got=%+v", got)
	}
	// sigNow 必须为 nil（调用方据此不覆写签名）：空串签名会把「同一事件集」误判成新事件。
	if p2.sigNow != nil {
		t.Fatalf("空窗轮不得生成新签名快照（会把事件集清空），got=%v", p2.sigNow)
	}

	// ③ 下午：新事件 E2 到达 → 与「最后一次真实事件集」比对不同 → 必须重评。
	afternoon := []newsagent.NewsEvent{d1TestEvent("2026-10-22 13:05:00", "人工智能订单", code)}
	p3 := planD1Round(pool, afternoon, md, nil, prevScores, prevSig)
	if p3.empty || len(p3.toScore) != 1 || p3.toScore[0] != code {
		t.Fatalf("新事件到达应触发重评: %+v", p3)
	}
	if p3.sigNow[code] == prevSig[code] || p3.sigNow[code] == "" {
		t.Fatalf("新事件签名应不同于旧签名: old=%q new=%q", prevSig[code], p3.sigNow[code])
	}
}

// TestPlanD1RoundEmptyPoolSkipsUnscored 空窗轮不得给从未评过分的个股调 LLM
// （无事件可评，只会被 noEvent 归 0）；待重试个股原样保留，继续留在重试队列。
func TestPlanD1RoundEmptyPoolSkipsUnscored(t *testing.T) {
	prevScores := map[string]combat_agent.D1Score{
		"600001": {Code: "600001", Score: 0, RetryPending: true, Reason: "LLM失败待重试"},
		"600002": {Code: "600002", Score: 9, Reason: "旧分"},
	}
	// 600002 上一轮没有事件签名（如从无事件的缺分占位），不得借用旧分；
	// 600003 完全无历史，同样不得调 LLM。
	prevSig := map[string]string{"600001": "2026-10-22 09:40:00|订单", "600002": ""}
	plan := planD1Round([]string{"600001", "600002", "600003"}, nil, nil, nil, prevScores, prevSig)
	if len(plan.toScore) != 0 {
		t.Fatalf("空窗轮不得调用 LLM，toScore=%v", plan.toScore)
	}
	if got, ok := plan.reuse["600001"]; !ok || !got.RetryPending {
		t.Fatalf("待重试个股应原样保留（继续留在重试队列），got=%+v ok=%v", got, ok)
	}
	if _, ok := plan.reuse["600002"]; ok {
		t.Fatal("上一轮无事件签名的个股不得复用旧分（跨日/无事件都不行）")
	}
	if _, ok := plan.reuse["600003"]; ok {
		t.Fatal("从未评分的个股不得凭空复用")
	}
}

// TestPlanD1RoundSectorEventChangeRescores 板块事件变化也计入签名：同一只个股的
// 新闻未变、板块事件换了 → 必须重评（与 EventSignature 口径一致）。
func TestPlanD1RoundSectorEventChangeRescores(t *testing.T) {
	const code = "600004"
	pool := []string{code}
	md := map[string]*strategy_engine.StockMarketData{code: {Code: code, Name: "白云机场"}}
	events := []newsagent.NewsEvent{d1TestEvent("2026-10-22 09:35:00", "客流恢复", code)}
	base := planD1Round(pool, events, md, map[string]string{code: "航空|利好"}, nil, nil)
	prevScores := map[string]combat_agent.D1Score{code: {Code: code, Score: 20}}
	// 板块事件不变 → 复用；板块事件变化 → 重评。
	reuse := planD1Round(pool, events, md, map[string]string{code: "航空|利好"}, prevScores, base.sigNow)
	if len(reuse.toScore) != 0 || reuse.reuse[code].Score != 20 {
		t.Fatalf("板块事件未变应复用: %+v", reuse)
	}
	changed := planD1Round(pool, events, md, map[string]string{code: "机场|利好"}, prevScores, base.sigNow)
	if len(changed.toScore) != 1 {
		t.Fatalf("板块事件变化应重评: %+v", changed)
	}
}

// TestRolloverDayStoresClearsD1Cache 跨交易日必须清空增量 D1 缓存：
// 空窗复用分支只认「上一轮有签名」，隔夜残留的签名会让昨日评分冒充今日事件分。
func TestRolloverDayStoresClearsD1Cache(t *testing.T) {
	e := &Engine{
		storeDay:     "19700101", // 非今日 → 触发滚动清空
		lastD1Scores: map[string]combat_agent.D1Score{"600000": {Code: "600000", Score: 25}},
		d1ScoredSig:  map[string]string{"600000": "2026-10-21 14:00:00|昨日事件"},
	}
	e.RolloverDayStores()
	if len(e.lastD1Scores) != 0 {
		t.Fatalf("跨日应清空 lastD1Scores，got=%v", e.lastD1Scores)
	}
	if len(e.d1ScoredSig) != 0 {
		t.Fatalf("跨日应清空 d1ScoredSig，got=%v", e.d1ScoredSig)
	}
}
