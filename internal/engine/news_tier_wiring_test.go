// news_tier_wiring_test.go — §NEWS-TIER 生产接线的行为腿（news_notify.go）。
//
// tier.go 自己的单测证的是"分级器本身是对的"；本文件证的是**它真的被接进了生产链路**：
//   - 一轮 produceNews 定稿事件后，命中持仓的利空确实从引擎的推送器出了站（走真实
//     *notify.Notifier 的 WS 通道捕获，不是假 sink 记账）；
//   - 未命中的同分事件不占必推通道（阈值下探只对持仓生效）；
//   - 同一代码当日第二条必推被预算/日汇总规则挡下且折叠计数可见；
//   - 分级器构造不了（没推送器/没身份）时流水线照跑、跳过被记成**显式读数**，
//     绝不出现"没分级"被读成"没新闻"的形态；
//   - 共享引擎的多账号：归属别名按账号独立、当日预算互不吞（一台的折叠不噤另一台）；
//   - Agent.MinScore 热更（"资讯显示全部"开关）确实传到分级器（同一把尺子）。
//
// 时钟统一走引擎的 clockFn（与 dispatchNewsTier 喂 Round 的 Now 同一时刻源），
// 冷却窗推进用挪表实现，不睡真实时间。
package engine

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/newsagent"
	"quant-trading-v2/internal/notify"
)

// tierWireEvent 造一条已归因事件（CleanedStocks 用清洗器口径的 "名称|代码"）。
// English: builds an attributed event with cleaned "name|code" stocks.
func tierWireEvent(title, direction string, score float64, code string) newsagent.NewsEvent {
	e := newsagent.NewsEvent{
		Title:     title,
		Datetime:  "2026-11-05 10:20:00",
		Source:    "财联社",
		Level:     "个股",
		Direction: direction,
		Score:     score,
		Reason:    "测试归因",
	}
	if code != "" {
		e.CleanedStocks = []string{"测试股|" + code}
		e.RelatedStocks = []string{"测试股(" + code + ")"}
	}
	return e
}

// newTierWiringEngine 造一台接线测试用引擎：真 newsAgent + 真 Notifier（WS 捕获）+ 消息中心，
// 时钟钉在 2026-11-05 10:20（交易日盘中）并由用例持有指针自行推进（避开 5 分钟冷却窗）。
// 三源先登记"取到"——否则 SourcesStatus 的全家"本轮未取数"会触发通道告警那条腿，
// 把用例想证的分级读数淹成"通道坏了"（那是 §5A.3 第 2 条自己的用例要证的，不在这里混）。
// English: builds a wired engine with the real agent/notifier/message store and a drivable clock;
// the three sources are pre-recorded healthy so the channel-outage lane stays out of these cases.
func newTierWiringEngine(t *testing.T, uids ...string) (*Engine, chan notify.Message, *time.Time, *newsagent.Agent) {
	t.Helper()
	dir := t.TempDir()
	na := newsagent.New(nil, nil, nil, dir)
	na.RecordSourceResult(newsagent.SourceTHS, 5, nil)
	na.RecordSourceResult(newsagent.SourceCLS, 5, nil)
	na.RecordSourceResult(newsagent.SourceSina, 5, nil)
	now := time.Date(2026, 11, 5, 10, 20, 0, 0, time.Local)
	e := &Engine{
		newsAgent: na,
		msgStore:  data.NewMessageStore(filepath.Join(dir, "messages.json")),
		notifier:  notify.New(),
		members:   append([]string(nil), uids...),
		clockFn:   func() time.Time { return now },
	}
	ch := e.notifier.RegisterWS("tier-wire")
	t.Cleanup(func() { e.notifier.UnregisterWS("tier-wire") })
	return e, ch, &now, na
}

// drainPushes 取空当前已入队的推送（within 之内没有更多就返回）。
// English: drains the queued pushes (returns when nothing more arrives within the window).
func drainPushes(ch chan notify.Message, within time.Duration) []notify.Message {
	var out []notify.Message
	deadline := time.After(within)
	for {
		select {
		case m := <-ch:
			out = append(out, m)
		case <-deadline:
			return out
		}
	}
}

// tierReadoutFor 从读数面取某账号最近一轮读数。
// English: pulls one account's latest readout from the status side.
func tierReadoutFor(t *testing.T, e *Engine, uid string) NewsTierReadout {
	t.Helper()
	for _, ro := range e.NewsTierStatus() {
		if ro.UID == uid {
			return ro
		}
	}
	t.Fatalf("读数面没有账号 %q 的条目——跳过态都没留下，就是本批要根除的形状", uid)
	return NewsTierReadout{}
}

// TestNewsTierHoldingHitPushesHigh 一轮定稿的利空命中持仓 ⇒ 真实推送器收到 1 条 LevelHigh，
// 别名归本账号，且「新闻」tab 落了行、读数记了数。
// English: a bearish event hitting a holding yields one LevelHigh push through the real
// notifier, scoped to the account, with a news-tab row and a recorded readout.
func TestNewsTierHoldingHitPushesHigh(t *testing.T) {
	e, ch, _, _ := newTierWiringEngine(t, "u_a")
	e.SetNewsHitProvider(func(uid string) ([]string, []string) {
		if uid == "u_a" {
			return []string{"600519"}, nil
		}
		return nil, nil
	})
	out := produceOut{valid: []newsagent.NewsEvent{tierWireEvent("测试股业绩爆雷", "利空", -0.75, "600519")}}
	e.dispatchNewsTier(out)

	msgs := drainPushes(ch, 300*time.Millisecond)
	if len(msgs) != 1 {
		t.Fatalf("命中持仓的利空应出 1 条推送，实得 %d：%+v", len(msgs), msgs)
	}
	if msgs[0].Level != notify.LevelHigh {
		t.Errorf("必推应为 LevelHigh，实得 %v", msgs[0].Level)
	}
	if msgs[0].Alias != "u_a" {
		t.Errorf("必推别名应归账号 u_a（身份只认在册），实得 %q", msgs[0].Alias)
	}
	if !strings.Contains(msgs[0].Title+msgs[0].Content, "600519") {
		t.Errorf("必推文案要带标的代码，实得 %s / %s", msgs[0].Title, msgs[0].Content)
	}
	// 新闻 tab 行落消息中心（私有 ID 前缀与 §GAP2-W2 口径一致）
	rows := e.msgStore.ListVisible("u_a")
	newsN := 0
	for _, m := range rows {
		if m.Kind == data.KindNews {
			newsN++
			if !strings.HasPrefix(m.ID, "u_u_a|") && m.Scope != "u_a" {
				t.Errorf("新闻行没按账号隔离：%+v", m)
			}
		}
	}
	if newsN == 0 {
		t.Error("RoundReport 要求落的消息中心「新闻」行一条都没写——调用方没做完它该做的事")
	}
	ro := tierReadoutFor(t, e, "u_a")
	if !ro.Constructed || ro.HighPushed != 1 || ro.Pushed != 1 {
		t.Errorf("读数应为 constructed=true pushed=1 high=1，实得 %+v", ro)
	}
}

// TestNewsTierNonHoldingKeepsMustPushLane MinScore=0.5 时同分（-0.4）两条利空：
// 命中持仓的那条靠阈值下探放行必推，未命中的那条根本不进必推通道。
// English: at MinScore=0.5 only the holding-hit event steps down the threshold and takes the
// must-push lane; the equally-scored off-book event does not.
func TestNewsTierNonHoldingKeepsMustPushLane(t *testing.T) {
	e, ch, _, na := newTierWiringEngine(t, "u_a")
	na.SetMinScore(0.5)
	e.SetNewsHitProvider(func(string) ([]string, []string) { return []string{"600519"}, nil })
	out := produceOut{valid: []newsagent.NewsEvent{
		tierWireEvent("命中持仓的中等利空", "利空", -0.4, "600519"),
		tierWireEvent("未命中的中等利空", "利空", -0.4, "600588"),
	}}
	e.dispatchNewsTier(out)

	msgs := drainPushes(ch, 300*time.Millisecond)
	if len(msgs) != 1 || msgs[0].Level != notify.LevelHigh {
		t.Fatalf("应恰好 1 条必推，实得 %+v", msgs)
	}
	if !strings.Contains(msgs[0].Content, "600519") {
		t.Errorf("放行的应是命中持仓那条，实得 %s", msgs[0].Content)
	}
	ro := tierReadoutFor(t, e, "u_a")
	if ro.HighPushed != 1 {
		t.Errorf("未持仓事件占了必推通道：high_pushed=%d", ro.HighPushed)
	}
}

// TestNewsTierSameCodeSecondOfDayFolds 同一代码当日第二条利空（未命中持仓）不再必推：
// 转日汇总且折叠计数可见（静默折叠＝通知变黑箱）。时钟推 6 分钟绕开冷却窗，
// 让这条用例证的是**日汇总规则**而不是冷却。
// English: the second bearish item for one code/day folds into the digest with a visible count;
// the clock advances past the cooldown so the fold is attributable to the rule, not the window.
func TestNewsTierSameCodeSecondOfDayFolds(t *testing.T) {
	e, ch, now, _ := newTierWiringEngine(t, "u_a")
	e.SetNewsHitProvider(func(string) ([]string, []string) { return []string{"600519"}, nil })

	e.dispatchNewsTier(produceOut{valid: []newsagent.NewsEvent{tierWireEvent("非持仓利空一", "利空", -0.75, "600588")}})
	first := drainPushes(ch, 200*time.Millisecond)
	if len(first) != 1 || first[0].Level != notify.LevelHigh {
		t.Fatalf("当日首条非持仓利空应必推 1 条，实得 %+v", first)
	}

	*now = now.Add(6 * time.Minute)
	e.dispatchNewsTier(produceOut{valid: []newsagent.NewsEvent{tierWireEvent("非持仓利空二", "利空", -0.75, "600588")}})
	second := drainPushes(ch, 200*time.Millisecond)
	if len(second) != 0 {
		t.Errorf("同代码当日第二条利空仍占了必推通道（应转日汇总）：%+v", second)
	}
	ro := tierReadoutFor(t, e, "u_a")
	if ro.Folded < 1 || ro.FoldedToday < 1 {
		t.Errorf("折叠计数没记上（读数看不出预算在走）：%+v", ro)
	}
	if ro.HighPushed != 0 {
		t.Errorf("第二轮不该有必推，实得 %+v", ro)
	}
}

// TestNewsTierSkipIsObservable 分级器构造不了（这里拿"没接推送器"驱动）时：
// 流水线照跑、不 panic，且跳过被记成**显式读数**（constructed=false + 非空原因 + 本轮事件数）。
// "没分级"与"没新闻"必须是两行不同的读数——后者是这条断言的全部内容。
// English: when the dispatcher cannot be built the round still completes AND the skip is an
// explicit readout (constructed=false, non-empty reason, this round's event count) —
// "no tiering" must never be readable as "no news".
func TestNewsTierSkipIsObservable(t *testing.T) {
	dir := t.TempDir()
	na := newsagent.New(nil, nil, nil, dir)
	now := time.Date(2026, 11, 5, 10, 20, 0, 0, time.Local)
	e := &Engine{
		newsAgent: na,
		msgStore:  data.NewMessageStore(filepath.Join(dir, "messages.json")),
		notifier:  nil, // 没接推送器 ⇒ 必推只进读数不出站，构造前提缺失
		members:   []string{"u_a"},
		clockFn:   func() time.Time { return now },
	}
	e.SetNewsHitProvider(func(string) ([]string, []string) { return []string{"600519"}, nil })
	out := produceOut{valid: []newsagent.NewsEvent{tierWireEvent("该被记下来的利空", "利空", -0.75, "600519")}}
	e.dispatchNewsTier(out) // 不许 panic，也不许静默吞掉

	ro := tierReadoutFor(t, e, "u_a")
	if ro.Constructed {
		t.Fatal("没接推送器却报 constructed=true——跳过被伪装成了在工作")
	}
	if strings.TrimSpace(ro.SkipReason) == "" {
		t.Error("跳过没有原因串：值班无从判读缺了哪条腿")
	}
	if ro.Events != 1 {
		t.Errorf("跳过读数也要带本轮事件数（1），否则与「今天没有新闻」无法区分，实得 %+v", ro)
	}
}

// TestNewsTierPerAccountAttributionAndBudget 共享引擎服务两个账号：
//   - 命中 u_a 持仓的事件只以 Alias=u_a 出站（u_b 不替它挨铃，运营/朋友同构）；
//   - 两账号同持的代码各推各的：当日账按 uid 独立，一台的预算/折叠绝不吞另一台的必推。
//
// English: per-account attribution (aliases don't cross) and independent day ledgers —
// one account's budget can never silence another account's must-push.
func TestNewsTierPerAccountAttributionAndBudget(t *testing.T) {
	e, ch, now, na := newTierWiringEngine(t, "u_a", "u_b")
	na.SetMinScore(0.5)
	e.SetNewsHitProvider(func(uid string) ([]string, []string) {
		switch uid {
		case "u_a":
			return []string{"600519", "600588"}, nil // 600588 两号同持
		case "u_b":
			return []string{"600588"}, nil
		}
		return nil, nil
	})

	// 第一轮：只命中 u_a 的持仓 ⇒ 恰好一条 Alias=u_a 的必推
	e.dispatchNewsTier(produceOut{valid: []newsagent.NewsEvent{tierWireEvent("只属于A的爆雷", "利空", -0.4, "600519")}})
	msgs := drainPushes(ch, 250*time.Millisecond)
	if len(msgs) != 1 {
		t.Fatalf("A 专属持仓事件应只出 1 条推送（B 的不许有），实得 %+v", msgs)
	}
	if msgs[0].Alias != "u_a" {
		t.Errorf("归属别名错了（B 的事件打到别人手机就是本批靶子）：%q", msgs[0].Alias)
	}
	if roB := tierReadoutFor(t, e, "u_b"); roB.HighPushed != 0 {
		t.Errorf("B 的分级器收到了 A 的持仓事件：%+v", roB)
	}

	// 第二、三轮：同持代码的事件连推两次，再连推第三次 ⇒ 两号各自被**自己的**每票预算折叠
	for i := 0; i < 3; i++ {
		*now = now.Add(6 * time.Minute)
		e.dispatchNewsTier(produceOut{valid: []newsagent.NewsEvent{
			tierWireEvent("两号同持利空第"+string(rune('一'+i))+"波", "利空", -0.75, "600588"),
		}})
	}
	// 每号在两波里各得 2 条必推、第三波各自折叠转日汇总（每票每日上限=2，账按 uid 独立）
	all := drainPushes(ch, 250*time.Millisecond)
	countA, countB := 0, 0
	for _, m := range all {
		if m.Alias == "u_a" {
			countA++
		}
		if m.Alias == "u_b" {
			countB++
		}
	}
	// 第一轮 A 已推 1 条（上面已取走），这里数三波循环里的推送：每号各 2 条（第三波被自家预算折叠）
	if countA != 2 || countB != 2 {
		t.Errorf("同持代码的必推没有按账号各算各的账：u_a=%d u_b=%d（应各 2）", countA, countB)
	}
	roA := tierReadoutFor(t, e, "u_a")
	roB := tierReadoutFor(t, e, "u_b")
	if roA.FoldedToday < 1 || roB.FoldedToday < 1 {
		t.Errorf("第三波应双双折叠留痕，读数却是 u_a.folded_today=%d u_b.folded_today=%d", roA.FoldedToday, roB.FoldedToday)
	}
}

// TestNewsTierMinScoreHotUpdateReachesDispatcher "资讯显示全部"开关热更 Agent.MinScore 后，
// 分级器必须跟着换尺子：阈值 0.5 时 -0.2 的持仓利空在最低档 0.25 之下（不再下探）被挡；
// 切到 0（全收）后同档事件要出站——读面与出站永远同一把尺子。
// English: the hot-reloaded MinScore reaches the dispatcher — a 0.2 event blocked under the
// 0.5 ruler (floor 0.25, no further step-down) goes out once "show all" (0) is toggled.
func TestNewsTierMinScoreHotUpdateReachesDispatcher(t *testing.T) {
	e, ch, now, na := newTierWiringEngine(t, "u_a")
	na.SetMinScore(0.5)
	e.SetNewsHitProvider(func(string) ([]string, []string) { return []string{"600519"}, nil })

	e.dispatchNewsTier(produceOut{valid: []newsagent.NewsEvent{tierWireEvent("低于地板档的弱利空", "利空", -0.2, "600519")}})
	if msgs := drainPushes(ch, 200*time.Millisecond); len(msgs) != 0 {
		t.Fatalf("阈值 0.5（命中下探到地板 0.25）时 -0.2 事件不该出站：%+v", msgs)
	}

	e.SetNewsShowAll(true) // 热更：Agent.MinScore→0，且必须同步到在册分级器
	*now = now.Add(6 * time.Minute)
	e.dispatchNewsTier(produceOut{valid: []newsagent.NewsEvent{tierWireEvent("全收后的弱利空", "利空", -0.2, "600519")}})
	msgs := drainPushes(ch, 250*time.Millisecond)
	if len(msgs) != 1 || msgs[0].Level != notify.LevelHigh {
		t.Fatalf("热更后分级器仍拿旧尺子把该放的挡了（或放了不止一条）：%+v", msgs)
	}
	ro := tierReadoutFor(t, e, "u_a")
	if ro.HighPushed != 1 {
		t.Errorf("读数没体现热更后的放行： %+v", ro)
	}
}
