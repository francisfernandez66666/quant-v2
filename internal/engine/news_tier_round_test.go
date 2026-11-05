// news_tier_round_test.go — §NEWS-TIER 生产接线的"真流水线"腿（区别于 news_tier_wiring_test.go）。
//
// wiring 文件证的是"分级腿本身接得上、算得对"；本文件证的是**真生产调用点**：
// produceNews 的每一条 return 前都必须把定稿事件交给出站腿——
//   - 进料非空但 LLM 缺席（Stage0 整批失败留重试队列）时，固化回填出的 out.valid
//     仍走到末尾的 dispatchNewsTier（命中持仓的利空从真实推送器出站）；
//   - 进料为空（三源全坏）的早退分支同样要过出站腿——"今天没读到任何新闻"那条通道
//     告警恰恰只有这里发得出去（§5A.3 第 2 条），跳过早退分支等于把通道故障憋成静默；
//   - 命中集回调没装配（生产上＝注册表注入缺失）时，流水线照跑完，且跳过被记成
//     显式读数（constructed=false + 原因 + 本轮事件数）——"没分级"不许被读成"没新闻"。
//
// 网络与 LLM 全部用假传输挡掉：同花顺返回一条真形状的快讯 JSON（其余源一律解析失败），
// LLM 客户端为 nil（Stage0 按既有语义整批留未归因队列，不产事件）——事件由固化层回填供给。
// English: proves the real produceNews call sites reach the dispatcher (finalized-event path
// via frozen backfill, zero-intake early-return path, and the skip-observable path when the
// hit provider was never wired), with all network/LLM legs stubbed.
package engine

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/newsagent"
	"quant-trading-v2/internal/notify"
)

// tierRoundTransport 假网络：only 同花顺快讯按真形状回一条新闻（thsOK=false 时连它也坏，
// 用来驱动"三源全坏"的早退分支）；其余源回一个必然解析失败的载荷并记 200，
// 让 newsagent 把这一源记成"本轮取数失败"而不是"从没取过"。
// English: stub network — THS returns one well-formed flash item (or fails everything),
// every other source returns an unparseable 200 so the source ledger records a real failure.
type tierRoundTransport struct{ thsOK bool }

func (m *tierRoundTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if m.thsOK && strings.Contains(req.URL.Host, "10jqka") {
		body := `{"code":"200","data":{"list":[{"title":"测试股发布业绩提示",` +
			`"digest":"测试股三季度业绩大幅下滑","ctime":"1762329600","url":"https://example.invalid/x"}]}}`
		return &http.Response{StatusCode: 200, Status: "200 OK",
			Header: http.Header{"Content-Type": {"application/json"}},
			Body:   io.NopCloser(strings.NewReader(body))}, nil
	}
	return &http.Response{StatusCode: 200, Status: "200 OK",
		Header: http.Header{"Content-Type": {"application/json"}},
		// 刻意用"连 JSON 都不是"的载荷：`{"code":"500"}` 这类合法空壳会被财联社/新浪的
		// 解析器当成"本轮零条"正常返回（源记 OK），allSourcesDown 判不成——通道告警那条腿
		// 就永远点不着。坏载荷才复刻真实的"取数失败"。
		Body: io.NopCloser(strings.NewReader(`!!!not-json!!!`))}, nil
}

// newTierRoundEngine 造一台"过真流水线"的引擎：MarketAPI 挂假传输、LLM 缺席、
// 固化层预置一条命中持仓的利空（Stage0 失败时由 6a 回填供给 out.valid）。
// 时钟由用例持有（与冷却/日切同一时刻源），推送走真实 notify.Notifier 的 WS 捕获。
// English: builds an engine that runs the real pipeline: stubbed transport, no LLM, and one
// pre-frozen bearish event on a held code so the frozen-backfill branch finalizes out.valid.
func newTierRoundEngine(t *testing.T, uids ...string) (*Engine, chan notify.Message, *newsagent.Agent) {
	t.Helper()
	dir := t.TempDir()
	api := data.NewMarketAPI()
	api.SetTransport(&tierRoundTransport{thsOK: true})
	na := newsagent.New(api, nil, nil, dir)
	// 固化层种子：Level=个股（避开板块衰减腿）、-0.75（过 0.5 阈值与固化线）、命中 600519。
	na.SaveFrozen([]newsagent.NewsEvent{tierWireEvent("测试股业绩爆雷", "利空", -0.75, "600519")})
	now := time.Date(2026, 11, 5, 10, 20, 0, 0, time.Local)
	e := &Engine{
		newsAgent: na,
		msgStore:  data.NewMessageStore(filepath.Join(dir, "messages.json")),
		notifier:  notify.New(),
		members:   append([]string(nil), uids...),
		clockFn:   func() time.Time { return now },
	}
	ch := e.notifier.RegisterWS("tier-round")
	t.Cleanup(func() { e.notifier.UnregisterWS("tier-round") })
	return e, ch, na
}

// TestNewsTierProduceNewsRoundReachesDispatcher 一轮真 produceNews（进料非空、LLM 缺席、
// 事件由固化回填定稿）必须走到末尾的 dispatchNewsTier：命中持仓的利空出 1 条 LevelHigh，
// 别名归在册账号，读数记 constructed=true。
// 这条就是"机制有主"的证明——之前分级器只有自己的单测调用，生产每轮新闻一条都不出站。
// English: a real produceNews round (intake via stubbed fetch, events finalized through the
// frozen-backfill branch) reaches the dispatcher: one LevelHigh push for the held code,
// attributed to the booked account, with a constructed readout.
func TestNewsTierProduceNewsRoundReachesDispatcher(t *testing.T) {
	e, ch, _ := newTierRoundEngine(t, "u_a")
	e.SetNewsHitProvider(func(uid string) ([]string, []string) {
		if uid == "u_a" {
			return []string{"600519"}, nil
		}
		return nil, nil
	})

	out := e.produceNews(context.Background(), time.Now().Add(-time.Hour))
	if len(out.valid) != 1 {
		t.Fatalf("固化回填应把本轮定稿事件补到 1 条（用例前提），实得 %d", len(out.valid))
	}

	msgs := drainPushes(ch, 300*time.Millisecond)
	high := 0
	for _, m := range msgs {
		if m.Level == notify.LevelHigh {
			high++
			if m.Alias != "u_a" {
				t.Errorf("必推别名应归 u_a（身份只认在册），实得 %q", m.Alias)
			}
			if !strings.Contains(m.Title+m.Content, "600519") {
				t.Errorf("必推文案要带命中代码，实得 %s / %s", m.Title, m.Content)
			}
		}
	}
	if high != 1 {
		t.Fatalf("一轮真流水线后命中持仓的利空应恰好 1 条必推，实得 %d（全部推送 %+v）", high, msgs)
	}
	ro := tierReadoutFor(t, e, "u_a")
	if !ro.Constructed || ro.Events != 1 || ro.HighPushed != 1 {
		t.Errorf("读数应为 constructed=true events=1 high=1，实得 %+v", ro)
	}
}

// TestNewsTierZeroIntakeRoundStillDispatches 三源全坏的早退分支（rawNews=0）同样必须过出站腿：
// 那条"今天没读到任何新闻"的通道告警只有这里发得出去，跳过它＝把通道故障憋成静默。
// English: the zero-intake early return still dispatches — the all-sources-down channel
// announcement can only fire there; skipping it swallows an outage as "no bad news today".
func TestNewsTierZeroIntakeRoundStillDispatches(t *testing.T) {
	dir := t.TempDir()
	api := data.NewMarketAPI()
	api.SetTransport(&tierRoundTransport{thsOK: false}) // 全部源解析失败
	na := newsagent.New(api, nil, nil, dir)
	now := time.Date(2026, 11, 5, 10, 20, 0, 0, time.Local)
	e := &Engine{
		newsAgent: na,
		msgStore:  data.NewMessageStore(filepath.Join(dir, "messages.json")),
		notifier:  notify.New(),
		members:   []string{"u_a"},
		clockFn:   func() time.Time { return now },
	}
	e.SetNewsHitProvider(func(string) ([]string, []string) { return []string{"600519"}, nil })
	ch := e.notifier.RegisterWS("tier-round-zero")
	defer e.notifier.UnregisterWS("tier-round-zero")

	out := e.produceNews(context.Background(), time.Now().Add(-time.Hour))
	if len(out.rawNews) != 0 {
		t.Fatalf("用例前提：三源全坏应零进料，实得 %d", len(out.rawNews))
	}
	ro := tierReadoutFor(t, e, "u_a")
	if !ro.Constructed || ro.Events != 0 {
		t.Fatalf("零进料轮也要留下分级读数（constructed=true, events=0），实得 %+v", ro)
	}
	if !ro.ChannelAnnounced {
		t.Errorf("三源全坏却没发通道告警——故障被折叠成静默，正是 §5A.3 第 2 条要根除的形状： %+v", ro)
	}
	// 告警本身确实从真实推送器出了站（读数说发了不算，要看腿）。
	msgs := drainPushes(ch, 300*time.Millisecond)
	announced := 0
	for _, m := range msgs {
		if strings.Contains(m.Title+m.Content, "没读到任何新闻") {
			announced++
		}
	}
	if announced != 1 {
		t.Fatalf("通道告警应恰好出站 1 条，实得 %d（全部推送 %+v）", announced, msgs)
	}
}

// TestNewsTierSkipRecordedThroughProduceNews 命中集回调没装配（生产上＝注册表注入缺失）时：
// produceNews 必须**跑完**（事件照常定稿、不 panic），且跳过被记成显式读数
// ——constructed=false + 非空原因 + 本轮真实事件数。"没分级"永远不许读成"没新闻"。
// English: with the hit provider never wired, produceNews still completes and the skip lands in
// the readout (constructed=false, non-empty reason, the round's true event count).
func TestNewsTierSkipRecordedThroughProduceNews(t *testing.T) {
	e, ch, _ := newTierRoundEngine(t, "u_a")
	// 刻意不调 SetNewsHitProvider——装配缺口的现场复刻。

	out := e.produceNews(context.Background(), time.Now().Add(-time.Hour))
	if len(out.valid) != 1 {
		t.Fatalf("跳过分级不该影响事件定稿（本轮应仍回填出 1 条），实得 %d", len(out.valid))
	}
	ro := tierReadoutFor(t, e, "u_a")
	if ro.Constructed {
		t.Fatal("没接命中集回调却报 constructed=true")
	}
	if !strings.Contains(ro.SkipReason, "newsHitProvider") {
		t.Errorf("跳过原因要点名缺的是哪条腿，实得 %q", ro.SkipReason)
	}
	if ro.Events != 1 {
		t.Errorf("跳过读数要带本轮事件数 1（否则与「今天没有新闻」无法区分），实得 %+v", ro)
	}
	// 分级被跳过 ⇒ 一条推送都不许出站（事件表/流水线不受影响，出站是分级腿自己的事）。
	if msgs := drainPushes(ch, 200*time.Millisecond); len(msgs) != 0 {
		t.Errorf("跳过态却出了站：%+v", msgs)
	}
}
