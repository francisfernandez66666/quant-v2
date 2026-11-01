// quote_feed_status_test.go — §SUBSCRIPTION-A（2026-11-01 追加裁决 14/15）观测面锁。
//
// 这批要证的是"看得见"：行情通道到底是没开、开着但被鉴权拒、还是读数不可用，
// 必须在 GET /api/status 里有**一个不同的值**能读出来。旧形状是只有一条 60 秒节流日志，
// 于是"开着但每轮 401"和"根本没开"在现网无法区分——那是本仓反复出事的静默无效族。
package server

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/display"
)

// statusQuoteFeed 调一次 /api/status 并取出 quote_feed 子对象（nil＝这条通道从没跑过）。
func statusQuoteFeed(t *testing.T, f *data.Fetcher) map[string]interface{} {
	t.Helper()
	s := &Server{startTime: time.Now(), agg: &display.Aggregator{}}
	if f != nil {
		s.SetFetcher(f)
	}
	rr := httptest.NewRecorder()
	s.handleFixStatus(rr, httptest.NewRequest("GET", "/api/status", nil))
	var out map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应非合法 JSON: %v", err)
	}
	raw, ok := out["quote_feed"]
	if !ok {
		t.Fatal("响应缺 quote_feed 字段（没跑过也必须下发 null，前端才能区分「没这条通道」与「字段没实现」）")
	}
	if raw == nil {
		return nil
	}
	m, _ := raw.(map[string]interface{})
	return m
}

// newStatusFetcher 造一个有基线快照的 Fetcher（不接外源，只喂策略与读数）。
func newStatusFetcher(n int) *data.Fetcher {
	codes := make([]string, 0, n)
	snap := &data.MarketSnapshot{Stocks: map[string]*data.StockInfo{}, Source: "新浪", Time: time.Now()}
	for i := 0; i < n; i++ {
		c := "60" + string(rune('0'+i/10)) + string(rune('0'+i%10)) + "00"
		codes = append(codes, c)
		snap.Stocks[c] = &data.StockInfo{Code: c, Price: 10}
	}
	f := data.NewFetcher(codes, &data.MarketAPI{}, nil)
	f.IngestSnapshot(snap)
	return f
}

// TestStatusNoFeedNeverRanIsNull 从没轮询过 ⇒ quote_feed 必须是 null。
// 不许折成 state=ok：那会把"这条通道不存在"显示成"通道健康"，是同族假绿的另一个入口。
func TestStatusNoFeedNeverRanIsNull(t *testing.T) {
	f := newStatusFetcher(4)
	if got := statusQuoteFeed(t, f); got != nil {
		t.Fatalf("没跑过 feed 时 quote_feed 应为 null, got %+v", got)
	}
}

// TestStatusAuthFailedIsDistinguishable 撤钥匙那一路必须是 auth_failed，
// 不能与"网关挂了"（degraded:J1）混成同一个值——两者的处置动作完全不同。
func TestStatusAuthFailedIsDistinguishable(t *testing.T) {
	f := newStatusFetcher(4)
	p := f.FeedPolicyNow()
	p.Primary = true
	f.SetFeedPolicy(p)
	f.NoteFeedFailure(2, true)

	got := statusQuoteFeed(t, f)
	if got["state"] != "auth_failed" {
		t.Fatalf("撤钥匙后状态必须是 auth_failed, got %v（整包 %+v）", got["state"], got)
	}
	if cf, _ := got["consecutive_fail"].(float64); cf != 2 {
		t.Fatalf("连续失败轮次要如实可读, got %v", got["consecutive_fail"])
	}
}

// TestStatusDegradedCarriesWhichJudgement J2/J3 必须把命中的是哪一条一起带出来，
// 否则运维只知道"降级了"却不知道是陈旧还是量纲不自洽。
func TestStatusDegradedCarriesWhichJudgement(t *testing.T) {
	f := newStatusFetcher(4)
	f.NoteFeedDegrade(data.FeedStats{Hits: 0, Pool: 4, Denominator: 4, Degrade: "J2"})
	got := statusQuoteFeed(t, f)
	if got["state"] != "degraded:J2" {
		t.Fatalf("J2 应显式带在状态位里, got %v（整包 %+v）", got["state"], got)
	}
}

// TestStatusObserveModeExposesWouldOverride 观察模式（本批缺省档）必须能读出
// "本轮本可以改写价格"和"连续达标、可以开主源"——这就是开开关的现场依据。
func TestStatusObserveModeExposesWouldOverride(t *testing.T) {
	f := newStatusFetcher(4)
	p := f.FeedPolicyNow()
	p.CoverageMinPct = 80
	f.SetFeedPolicy(p) // 缺省 Primary=false ⇒ 观察档
	entries := map[string]data.QMTTick{}
	for _, c := range f.WatchCodes() {
		entries[c] = data.QMTTick{LastPrice: 11, Volume: 1000, Amount: 11000, TickTime: time.Now().UnixMilli()}
	}
	st := data.FeedStats{Hits: len(entries), Denominator: len(entries), Pool: len(entries), CoveragePct: 100, DenominatorSource: "tradable"}
	for i := 0; i < 3; i++ {
		f.IngestFeedRound(nil, entries, st)
	}
	got := statusQuoteFeed(t, f)
	if got["would_override"] != true {
		t.Fatalf("观察档覆盖率达标应报 would_override, got %v（%+v）", got["would_override"], got)
	}
	if got["ready_to_promote"] != true {
		t.Fatalf("连续 3 轮达标应报 ready_to_promote（可开 primary 的读数依据）, got %v", got["ready_to_promote"])
	}
	if got["refreshed_global"] != false {
		t.Fatalf("观察档绝不刷新全局基线, got %v", got["refreshed_global"])
	}
	if got["state"] != "ok" {
		t.Fatalf("观察档通道本身正常，状态应为 ok, got %v", got["state"])
	}
}

// statusBody 取整个 /api/status 响应（混源计数在顶层，不在 quote_feed 子对象里）。
func statusBody(t *testing.T, f *data.Fetcher) map[string]any {
	t.Helper()
	s := &Server{startTime: time.Now(), agg: &display.Aggregator{}}
	s.SetFetcher(f)
	rr := httptest.NewRecorder()
	s.handleFixStatus(rr, httptest.NewRequest("GET", "/api/status", nil))
	var out map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应非合法 JSON: %v", err)
	}
	return out
}

// TestStatusCountsMixedSources 混源必须可数：整份标称 QMT-L1、其中一只是新浪兜的 ⇒ mixed=1。
// 关着逐票标记时必须是 0（不许把"没标记"报成"全都一致"以外的任何含义，也不许编一个数）。
func TestStatusCountsMixedSources(t *testing.T) {
	f := newStatusFetcher(3)
	snap := f.Snapshot()
	codes := f.WatchCodes()
	snap.Source = data.QuoteSourceQMTL1
	snap.Stocks[codes[0]].Src = data.QuoteSourceQMTL1
	snap.Stocks[codes[1]].Src = "新浪"
	snap.Stocks[codes[2]].Src = "" // 没标记的票不参与计数（关着就是这个形状）
	f.IngestSnapshot(snap)
	if got := statusBody(t, f)["quote_source_mixed"]; got != float64(1) {
		t.Fatalf("混源计数应为 1（一只是后备源），got %v", got)
	}
}

// TestServerQuoteUsesHubWhenEnabled 裁决 13 在 server 层的落点：开关开着且这一只票新鲜时，
// 展示/自选价由 HubQuote 给出——注意这里**没接任何外部行情源**（dc/market 都是 nil），
// 能拿到价就证明它没有偷偷走降级链；新鲜度与被拒的语义在 data 层用例里断
// （lastOK 是包内字段，跨包去掰它只会把测试写成对实现的抄袭）。
func TestServerQuoteUsesHubWhenEnabled(t *testing.T) {
	f := newStatusFetcher(3)
	s := &Server{startTime: time.Now(), agg: &display.Aggregator{}}
	s.SetFetcher(f)
	codes := f.WatchCodes()

	f.SetFeedPolicy(data.FeedPolicy{Consult: true})
	si, err := s.quote(codes[0])
	if err != nil || si == nil || si.Price <= 0 {
		t.Fatalf("开关开且快照新鲜时应由 hub 命中（本用例没接任何外部源）: si=%+v err=%v", si, err)
	}
	// 交出的是拷贝：调用方就地改字段不许污染采集器自己的快照
	si.Price = 999
	if got := f.Snapshot().Stocks[codes[0]].Price; got == 999 {
		t.Fatal("quote() 交出了共享指针：读路径会写坏采集器的账")
	}
}
