// notify_tier_test.go — §B6 通知增强（施工图 §5A.4）的行为腿断言。
//
// 这里驱动的是**真实出站路径**：构造 Dispatcher、喂一轮 RoundInput、逐条核对出站的
// notify.Message（级别与文案要素）、日汇总计数、折叠行与新闻 tab 行——不是 grep 源码字符串。
// LLM 与新闻源用桩（LLMState/SourceState 是本轮输入），所以用例在没有网络、没有大模型的
// 情况下也能把"归因失败仍要出站""三源全坏仍要说出来"这两条本批真正的靶子打到实处。
package newsagent

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/metrics"
	"quant-trading-v2/internal/notify"
	"quant-trading-v2/internal/strategies/n_shape"
)

// fakeSink 记录每一次出站调用：级别、标题、正文，以及宕机播报被触发过几次。
// 它是 §5A.4 全部"通道调用 0 次 / 1 次"判定的读数来源。
// English: fakeSink records every outbound call — the readout behind every "0/1 calls" assertion.
type fakeSink struct {
	pushed      []notify.Message
	announceCnt int
	alive       bool
}

func (f *fakeSink) Push(msg notify.Message) { f.pushed = append(f.pushed, msg) }

// PushLaneAlive 默认 true；用例把它置 false 来驱动"必推通道全 trip"那条腿。
// English: defaults to alive; a test flips it to drive the all-channels-tripped rule.
func (f *fakeSink) PushLaneAlive() bool { return f.alive }

func (f *fakeSink) AnnounceChannelDown(title, reason string) {
	f.announceCnt++
	f.pushed = append(f.pushed, notify.Message{Level: notify.LevelHigh, Title: title, Content: reason})
}

// highPushed 取出走必推通道（LevelHigh）的消息。
// English: highPushed filters the recorded pushes down to the must-push lane.
func highPushed(list []notify.Message) []notify.Message {
	out := make([]notify.Message, 0, len(list))
	for _, m := range list {
		if m.Level == notify.LevelHigh {
			out = append(out, m)
		}
	}
	return out
}

// tierEvent 造一条已归因事件：CleanedStocks 用消息中心口径的 "名称|代码"。
// English: tierEvent builds an attributed event with cleaned "name|code" stocks.
func tierEvent(title, source, direction string, score float64, code string, reason string) NewsEvent {
	e := NewsEvent{
		Title:     title,
		Datetime:  "2026-11-05 10:20:00",
		Source:    source,
		Level:     "个股",
		Direction: direction,
		Score:     score,
		Reason:    reason,
	}
	if code != "" {
		e.CleanedStocks = []string{"测试股|" + code}
		e.RelatedStocks = []string{"测试股(" + code + ")"}
	}
	return e
}

// newTestDispatcher 造一台时钟可控的出站器（固定时刻＝2026-11-05 10:20，交易日盘中）。
// English: newTestDispatcher builds a dispatcher with a controllable clock pinned to a trading session.
func newTestDispatcher(hits HitSet, minScore float64) (*Dispatcher, *fakeSink, *time.Time) {
	sink := &fakeSink{alive: true}
	d := NewDispatcher(sink, hits, minScore, "u_test")
	now := time.Date(2026, 11, 5, 10, 20, 0, 0, time.Local)
	d.SetNowFunc(func() time.Time { return now })
	return d, sink, &now
}

// allDownSources 三源全失败的读数（§5A.3 第 2 条的输入形状）。
// English: allDownSources is the "every source failed" reading §5A.3 rule 2 consumes.
func allDownSources() []SourceState {
	return []SourceState{
		{Name: SourceTHS, OK: false, Err: "HTTP 567"},
		{Name: SourceCLS, OK: false, Err: "TLS EOF"},
		{Name: SourceSina, OK: false, Err: "timeout"},
	}
}

// TestNegativeNewsOnHoldingIsHighLevel §5A.1 第 1 行：利空 ∧ 命中持仓 ⇒ 必推，
// 且文案含 标的 + 归因 + 建议动作 + D1 新旧值 + 源与时刻（缺任一项就退化成"刷一条标题"）。
func TestNegativeNewsOnHoldingIsHighLevel(t *testing.T) {
	hits := NewHitSet([]string{"600519"}, nil)
	d, _, _ := newTestDispatcher(hits, 0.25)
	ev := tierEvent("测试股业绩爆雷", "财联社", "利空", -0.75, "600519", "三季度净利同比下滑 60%")

	rep := d.Round(RoundInput{
		Events: []NewsEvent{ev},
		// 命中持仓的利空要带 D1 新旧值：这里给一次 30→12 的跨档（顶级影响→中等影响）
		D1Changes: []D1Change{{Code: "600519", Name: "测试股", Old: 30, New: 12, Trigger: "测试股业绩爆雷"}},
		LLM:       LLMState{Available: true},
	})

	high := highPushed(rep.Pushed)
	if len(high) != 1 {
		t.Fatalf("利空命中持仓应出 1 条必推，实得 %d（全部出站 %+v）", len(high), rep.Pushed)
	}
	if high[0].Level != notify.LevelHigh {
		t.Errorf("必推级别应为 LevelHigh，实得 %v", high[0].Level)
	}
	text := high[0].Title + high[0].Content
	for _, want := range []string{
		"600519",              // 标的
		"三季度净利同比下滑",           // 一句归因
		"暂停买入并评估减仓",           // 建议动作
		"30.0→12.0",           // D1 新旧值
		"财联社",                 // 源
		"2026-11-05 10:20:00", // 时刻
	} {
		if !strings.Contains(text, want) {
			t.Errorf("必推文案缺要素 %q，全文=%s", want, text)
		}
	}
	// 命中持仓的新闻也要在「新闻」tab 留一行（可见不隐藏，与事件表同源）
	if len(rep.NewsRows) == 0 {
		t.Error("命中持仓的利空没在新闻 tab 留行：折叠与推送都必须可见")
	}
}

// TestPositiveNewsDoesNotTakePushLane §5A.1 第 3 行 + 反证 R8：利多**不许**走必推通道，
// 否则利空就被稀释。利多命中持仓只走普通推送（LevelMedium）。
func TestPositiveNewsDoesNotTakePushLane(t *testing.T) {
	hits := NewHitSet([]string{"600519"}, nil)
	d, sink, _ := newTestDispatcher(hits, 0.25)
	ev := tierEvent("测试股拿到大额订单", "新浪", "利好", 0.75, "600519", "订单金额占去年营收 20%")

	rep := d.Round(RoundInput{Events: []NewsEvent{ev}, LLM: LLMState{Available: true}})

	if len(rep.Pushed) != 1 {
		t.Fatalf("利多命中持仓应出 1 条普通推送，实得 %d", len(rep.Pushed))
	}
	if rep.Pushed[0].Level != notify.LevelMedium {
		t.Fatalf("利多走了必推通道（level=%v）——分级被抹平就是在稀释利空", rep.Pushed[0].Level)
	}
	if len(highPushed(rep.Pushed)) != 0 {
		t.Errorf("必推通道出现利多消息：%+v", rep.Pushed)
	}
	if !strings.Contains(rep.Pushed[0].Content, "无需动作") {
		t.Errorf("利多文案应写明\"无需动作\"，实得 %s", rep.Pushed[0].Content)
	}
	if sink.announceCnt != 0 {
		t.Errorf("通道在位时不该触发宕机播报，实得 %d 次", sink.announceCnt)
	}
	// 普通推送不吃必推额度（每票上限是按必推算的）
	if st := d.State(); st.Daily != 0 {
		t.Errorf("利多不该占必推预算，实得 daily=%d", st.Daily)
	}
}

// TestPositiveNewsOffHoldingGoesToNewsTab §5A.1 第 4 行（owner 11-03 定）：非持仓利多 ⇒
// 必推通道调用 0 次、消息中心落 1 条 kind=news、tab 未读 +1（未读由前端按行算，这里证行数与种类）。
func TestPositiveNewsOffHoldingGoesToNewsTab(t *testing.T) {
	hits := NewHitSet([]string{"600519"}, []string{"000001"})
	d, sink, _ := newTestDispatcher(hits, 0.25)
	ev := tierEvent("某无关行业利好", "财联社", "利好", 0.6, "300750", "行业景气度回升")

	rep := d.Round(RoundInput{Events: []NewsEvent{ev}, LLM: LLMState{Available: true}})

	if len(rep.Pushed) != 0 {
		t.Fatalf("非持仓利多不该有任何出站，实得 %+v", rep.Pushed)
	}
	if len(sink.pushed) != 0 {
		t.Fatalf("sink 收到 %d 条推送，判据是 0", len(sink.pushed))
	}
	if len(rep.NewsRows) != 1 {
		t.Fatalf("应落 1 条新闻行，实得 %d", len(rep.NewsRows))
	}
	row := rep.NewsRows[0]
	if row.Kind != data.KindNews {
		t.Errorf("新闻行种类应为 %q，实得 %q", data.KindNews, row.Kind)
	}
	if !strings.Contains(row.Body, "行业景气度回升") || !strings.Contains(row.Title, "某无关行业利好") {
		t.Errorf("新闻行少了标题或归因：%+v", row)
	}
	// 只命中自选的利多同样进 tab（不升级成推送）：自选 ≠ 持仓
	d2, sink2, _ := newTestDispatcher(hits, 0.25)
	rep2 := d2.Round(RoundInput{Events: []NewsEvent{tierEvent("自选股利好", "新浪", "利好", 0.7, "000001", "回购")}, LLM: LLMState{Available: true}})
	if len(sink2.pushed) != 0 || len(rep2.NewsRows) != 1 {
		t.Errorf("只命中自选的利多应进 tab 不出站：pushed=%d rows=%d", len(sink2.pushed), len(rep2.NewsRows))
	}
}

// TestUnattributedNewsStillNotifies §5A.3 第 1 条：LLM 不可用 ⇒ 仍出站，且明标"未归因（LLM 不可用）"。
// 归因失败不许伪造成"没有消息"。
func TestUnattributedNewsStillNotifies(t *testing.T) {
	hits := NewHitSet([]string{"600519"}, nil)
	d, sink, _ := newTestDispatcher(hits, 0.25)

	rep := d.Round(RoundInput{
		Unattributed: []data.NewsItem{{Title: "600519 突发停牌核查", Source: "财联社", Datetime: "2026-11-05 10:18:00"}},
		LLM:          LLMState{Available: false, Reason: "LLM 连不通"},
	})

	high := highPushed(rep.Pushed)
	if len(high) != 1 {
		t.Fatalf("LLM 不可用时命中持仓的新闻仍应出 1 条必推，实得 %d（%+v）", len(high), sink.pushed)
	}
	if !strings.Contains(high[0].Content, "未归因（LLM 不可用）") {
		t.Errorf("未归因条目必须明标降级原因，实得 %s", high[0].Content)
	}
	if !strings.Contains(high[0].Content, "600519 突发停牌核查") {
		t.Errorf("未归因条目要带标题原文，实得 %s", high[0].Content)
	}
	// 未命中持仓的未归因条目不许变成告警（否则整条新闻流都刷屏）
	d2, sink2, _ := newTestDispatcher(hits, 0.25)
	d2.Round(RoundInput{
		Unattributed: []data.NewsItem{{Title: "某无关板块发布指引", Source: "新浪"}},
		LLM:          LLMState{Available: false},
	})
	if len(sink2.pushed) != 0 {
		t.Errorf("未命中的未归因条目不该出站，实得 %+v", sink2.pushed)
	}
}

// TestAllNewsSourcesDownAlerts §5A.3 第 2 条：三源全失败 ⇒ 出一条通道告警，
// 且**不得**产"无利空"结论；当日只播一次（不逐轮刷）。
func TestAllNewsSourcesDownAlerts(t *testing.T) {
	hits := NewHitSet([]string{"600519"}, nil)
	d, sink, _ := newTestDispatcher(hits, 0.25)

	rep := d.Round(RoundInput{Sources: allDownSources(), LLM: LLMState{Available: true}})
	if len(rep.Pushed) != 1 || !rep.ChannelAnnounced {
		t.Fatalf("三源全失败应恰好出一条通道告警，实得 pushed=%d announced=%v", len(rep.Pushed), rep.ChannelAnnounced)
	}
	msg := rep.Pushed[0]
	if msg.Level != notify.LevelHigh {
		t.Errorf("通道级告警应走必推，实得 %v", msg.Level)
	}
	// 两处判据都要求落在**正文**里：只转正文的通道（手机网关折叠、日志检索）会丢掉标题，
	// 那时单看"三个来源取数失败"容易被读成"今天确实没新闻"——正是 §5A.3 第 2 条要防的形状。
	// English: both phrases must be in the body; body-only lanes drop the title, and
	// "sources failed" alone reads as "there was no news today".
	if !strings.Contains(msg.Content, "今天没读到任何新闻") || !strings.Contains(msg.Content, "不是「今天没有利空」") {
		t.Errorf("通道告警措辞必须点明\"这是取不到、不是没有利空\"，实得 %s / %s", msg.Title, msg.Content)
	}
	for _, m := range rep.Pushed {
		if strings.Contains(m.Title+m.Content, "无利空") && !strings.Contains(m.Content, "不是「今天没有利空」") {
			t.Errorf("出现了把取不到伪装成\"无利空\"的文案：%s", m.Content)
		}
	}
	// 第二轮同一天：不重复播报（当日一次）
	rep2 := d.Round(RoundInput{Sources: allDownSources(), LLM: LLMState{Available: true}})
	if len(rep2.Pushed) != 0 {
		t.Errorf("同一天第二条通道告警不该重复，实得 %+v", rep2.Pushed)
	}
	if sink.announceCnt != 0 {
		t.Errorf("通道在位时不该触发宕机播报，实得 %d", sink.announceCnt)
	}
	// 反证：只坏了两源（第三源取到）⇒ 不算"三源全失败"，不许抢报通道故障
	d3, sink3, _ := newTestDispatcher(hits, 0.25)
	rep3 := d3.Round(RoundInput{Sources: []SourceState{
		{Name: SourceTHS, OK: false, Err: "567"},
		{Name: SourceCLS, OK: false, Err: "EOF"},
		{Name: SourceSina, OK: true, Count: 12},
	}, LLM: LLMState{Available: true}})
	if len(sink3.pushed) != 0 {
		t.Errorf("还有一源取到就出通道告警（%+v）——这条会淹掉真告警", rep3.Pushed)
	}
}

// TestMustPushLaneDownUsesSurvivingChannel §5A.3 第 3 条：必推通道全 trip ⇒ 复用现成存活通道播报。
func TestMustPushLaneDownUsesSurvivingChannel(t *testing.T) {
	hits := NewHitSet([]string{"600519"}, nil)
	sink := &fakeSink{alive: false}
	d := NewDispatcher(sink, hits, 0.25, "u_test")
	d.SetNowFunc(func() time.Time { return time.Date(2026, 11, 5, 10, 20, 0, 0, time.Local) })

	d.Round(RoundInput{Events: []NewsEvent{tierEvent("利空来了", "财联社", "利空", -0.75, "600519", "立案调查")}, LLM: LLMState{Available: true}})
	if sink.announceCnt != 1 {
		t.Fatalf("必推通道全不可用时应宕机播报 1 次，实得 %d", sink.announceCnt)
	}
	// 同一日第二轮不再重复（一次宕机只播一次）
	d.Round(RoundInput{Events: []NewsEvent{tierEvent("另一条利空", "财联社", "利空", -0.75, "600519", "另一事件")}, LLM: LLMState{Available: true}})
	if sink.announceCnt != 1 {
		t.Errorf("同一天不该重复宕机播报，实得 %d", sink.announceCnt)
	}
}

// TestPerStockAndDailyBudgetFoldVisible §5A.2 第 1 条 + 反证 R9：超上限后转日汇总，
// 且"今日已折叠 N 条"必须在这一行里可见（静默折叠＝通知变黑箱）。
func TestPerStockAndDailyBudgetFoldVisible(t *testing.T) {
	hits := NewHitSet([]string{"600519"}, nil)
	d, _, now := newTestDispatcher(hits, 0.25)

	// 同一只票连推 3 轮：第 1、2 条在必推额度内，第 3 条起转日汇总。
	// 每轮把时钟推 6 分钟，避开 (类型,代码) 的 5 分钟冷却窗——让折叠由**预算**触发，
	// 而不是被冷却悄悄吃掉（那样这条锁就测不到折叠本身）。
	for i := 0; i < 3; i++ {
		*now = time.Date(2026, 11, 5, 10, 20+i*6, 0, 0, time.Local)
		rep := d.Round(RoundInput{
			Events: []NewsEvent{tierEvent("同一只票的利空第"+string(rune('1'+i))+"条", "财联社", "利空", -0.75, "600519", "原因")},
			LLM:    LLMState{Available: true},
		})
		wantPush := 1
		if i == 2 {
			wantPush = 0 // 超出每票上限 ⇒ 不再出站，转日汇总
		}
		if len(rep.Pushed) != wantPush {
			t.Fatalf("第 %d 轮应出站 %d 条，实得 %d（%+v）", i+1, wantPush, len(rep.Pushed), rep.Pushed)
		}
	}
	st := d.State()
	if st.PerStock["600519"] != NewsPushPerStockDaily {
		t.Errorf("每票必推计数应停在 %d，实得 %d", NewsPushPerStockDaily, st.PerStock["600519"])
	}
	if st.Folded != 1 {
		t.Errorf("当日折叠计数应为 1，实得 %d", st.Folded)
	}
	if !strings.Contains(d.DigestText(), "折叠 1 条") {
		t.Errorf("日汇总文本要带折叠计数，实得 %q", d.DigestText())
	}

	// 全局每日上限 12：一轮灌进 13 只不同票（各自冷却键互不影响）⇒ 12 推 + 1 折叠 + 折叠行可见
	d2, sink2, _ := newTestDispatcher(NewHitSet([]string{"600519"}, nil), 0.25)
	held := d2.hits
	var many []NewsEvent
	for i := 0; i < NewsPushDailyTotal+1; i++ {
		code := testCode(i)
		held.Holdings[code] = true
		many = append(many, tierEvent("批量利空"+code, "财联社", "利空", -0.75, code, "原因"))
	}
	d2.hits = held
	rep := d2.Round(RoundInput{Events: many, LLM: LLMState{Available: true}})
	if got := len(highPushed(sink2.pushed)); got != NewsPushDailyTotal {
		t.Fatalf("全局每日上限应放 %d 条，实得 %d", NewsPushDailyTotal, got)
	}
	if d2.State().Folded != 1 {
		t.Errorf("超出全局上限的那条应计入折叠，实得 %d", d2.State().Folded)
	}
	found := ""
	for _, row := range rep.NewsRows {
		if strings.HasPrefix(row.Title, "今日已折叠") {
			found = row.Title
		}
	}
	if found == "" {
		t.Fatalf("新闻 tab 没有\"今日已折叠 N 条\"那一行（rows=%d）——静默折叠就是把通知做成黑箱", len(rep.NewsRows))
	}
	if !strings.Contains(found, "1") {
		t.Errorf("折叠行要报出条数，实得 %q", found)
	}
}

// testCode 造 6 位测试代码（600001 起递增，避开真实代码语义）。
// English: testCode builds incrementing 6-digit test codes.
func testCode(i int) string {
	return "60" + pad4(i)
}

// pad4 把序号写成 4 位零填充串。
// English: pad4 zero-pads an index to 4 digits.
func pad4(i int) string {
	s := ""
	for d := 1000; d > 0; d /= 10 {
		s += string(rune('0' + (i/d)%10))
	}
	return s
}

// TestCooldownKeyedPerStockNotGlobal 反证 R10：冷却键按 (类型,代码) 分格。
// A 票连发两条利空只推 1 条（同格窗内），B 票仍要推 1 条——全局窗会把 B 吃掉。
func TestCooldownKeyedPerStockNotGlobal(t *testing.T) {
	hits := NewHitSet([]string{"600519", "600588"}, nil)
	d, sink, _ := newTestDispatcher(hits, 0.25)

	// 同一轮里 A 票两条利空：第二条被同键冷却吃掉（预算没到、每票上限 2 也没到）
	rep := d.Round(RoundInput{Events: []NewsEvent{
		tierEvent("A票利空一", "财联社", "利空", -0.75, "600519", "原因一"),
		tierEvent("A票利空二", "新浪", "利空", -0.75, "600519", "原因二"),
	}, LLM: LLMState{Available: true}})
	if len(highPushed(sink.pushed)) != 1 {
		t.Fatalf("同票同窗应只推 1 条，实得 %d", len(highPushed(sink.pushed)))
	}
	if rep.Suppressed != 1 {
		t.Errorf("被冷却压掉的那条应计入 Suppressed=1，实得 %d", rep.Suppressed)
	}
	// 紧接着 B 票利空：**必须**推出来（不同代码＝不同冷却格）
	rep2 := d.Round(RoundInput{Events: []NewsEvent{
		tierEvent("B票利空", "财联社", "利空", -0.75, "600588", "原因三"),
	}, LLM: LLMState{Available: true}})
	if len(rep2.Pushed) != 1 {
		t.Fatalf("第二只票的利空被第一只的冷却窗吃掉了（全局窗形态，R10 反证）：%+v", rep2.Pushed)
	}
}

// TestD1CrossTierCarriesOldNew §5A.1 第 5 行：D1 跨档 ⇒ 普通推送，且必须带新旧两个值 + 触发它的事件标题。
func TestD1CrossTierCarriesOldNew(t *testing.T) {
	d, sink, _ := newTestDispatcher(NewHitSet(nil, nil), 0.25)
	rep := d.Round(RoundInput{
		D1Changes: []D1Change{{Code: "600519", Name: "测试股", Old: 30, New: 12, Trigger: "业绩预减公告"}},
		LLM:       LLMState{Available: true},
	})
	if len(rep.Pushed) != 1 {
		t.Fatalf("D1 跨档应出 1 条普通推送，实得 %+v（sink %+v）", rep.Pushed, sink.pushed)
	}
	if rep.Pushed[0].Level != notify.LevelMedium {
		t.Errorf("D1 跨档是普通推送，不该抢必推通道：%v", rep.Pushed[0].Level)
	}
	text := rep.Pushed[0].Content
	for _, want := range []string{"30.0", "12.0", "强影响", "中等影响", "业绩预减公告"} {
		if !strings.Contains(text, want) {
			t.Errorf("D1 跨档文案缺 %q（只给\"变了\"等于没给），全文=%s", want, text)
		}
	}
	// 同档内的抖动不出站（30→25 都还是强影响）
	d2, sink2, _ := newTestDispatcher(NewHitSet(nil, nil), 0.25)
	d2.Round(RoundInput{D1Changes: []D1Change{{Code: "600519", Old: 30, New: 25}}, LLM: LLMState{Available: true}})
	if len(sink2.pushed) != 0 {
		t.Errorf("未跨档的 D1 抖动不该打扰：%+v", sink2.pushed)
	}
}

// TestNShapeBuyMultiChannel §5A.1 第 6 行：n形买入 ⇒ 必推（LevelHigh）+ 不折叠 + 同键 5 分钟内不重复。
// "多通道并发"由 notify.Push 既有的 WS/Webhook/手机网关三路扇出承担（internal/notify 的
// m8_fanout_test 已锁那条扇出本身），本用例钉的是"战法侧交来的买入一定进必推通道且不吃折叠"。
func TestNShapeBuyMultiChannel(t *testing.T) {
	d, sink, now := newTestDispatcher(NewHitSet([]string{"600519"}, nil), 0.25)
	buy := n_shape.BuyNotice{
		Code: "600519", Name: "测试股", Stage: "右侧二突", D1: 30, Emotion: "启动",
		IsLeader: true, Priority: 100, Remind: "strong", CanOpen: true,
	}

	rep := d.Round(RoundInput{Buys: []BuyNotice{buy}, LLM: LLMState{Available: true}})
	high := highPushed(sink.pushed)
	if len(high) != 1 {
		t.Fatalf("n形买入应进必推通道 1 条，实得 %d（pushed=%+v rep=%+v）", len(high), sink.pushed, rep.Pushed)
	}
	if high[0].Level != notify.LevelHigh {
		t.Errorf("n形买入级别应为 LevelHigh，实得 %v", high[0].Level)
	}
	for _, want := range []string{"右侧二突", "D1=30.0", "启动", "板块龙头", "有效期"} {
		if !strings.Contains(high[0].Content, want) {
			t.Errorf("买入文案缺要素 %q，全文=%s", want, high[0].Content)
		}
	}
	if rep.Folded != 0 || d.State().Folded != 0 {
		t.Error("n形买入不许被折叠")
	}

	// 同键 5 分钟内不重复（防它变成刷屏）
	d.Round(RoundInput{Buys: []BuyNotice{buy}, LLM: LLMState{Available: true}})
	if len(highPushed(sink.pushed)) != 1 {
		t.Errorf("同票 5 分钟内第二次不该重复出站，实得 %d", len(highPushed(sink.pushed)))
	}
	// 过窗后可以再来一条（冷却是按 (类型,代码) 分格的窗，不是永久静默）
	*now = now.Add(NewsPushCooldown + time.Minute)
	d.Round(RoundInput{Buys: []BuyNotice{buy}, LLM: LLMState{Available: true}})
	if len(highPushed(sink.pushed)) != 2 {
		t.Errorf("过冷却窗后应再次出站，实得 %d", len(highPushed(sink.pushed)))
	}

	// 不折叠：必推额度被打满之后，n形买入仍要出站（它不参与新闻预算）
	d2, sink2, _ := newTestDispatcher(NewHitSet([]string{"600519"}, nil), 0.25)
	held := d2.hits
	var many []NewsEvent
	for i := 0; i < NewsPushDailyTotal; i++ {
		code := testCode(i)
		held.Holdings[code] = true
		many = append(many, tierEvent("灌满预算的利空"+code, "财联社", "利空", -0.75, code, "原因"))
	}
	d2.hits = held
	d2.Round(RoundInput{Events: many, Buys: []BuyNotice{buy}, LLM: LLMState{Available: true}})
	// 12 条新闻 + 1 条买入 = 13 条必推；若买入也被预算挡下就只有 12
	if got := len(highPushed(sink2.pushed)); got != NewsPushDailyTotal+1 {
		t.Fatalf("n形买入在必推额度打满后仍须出站，实得 %d 条必推", got)
	}
}

// TestDedupMarksSourceTimesN §5A.2 第 2 条：多源同一事件按既有指纹合并，只发一次并标"来源 ×N"。
func TestDedupMarksSourceTimesN(t *testing.T) {
	hits := NewHitSet([]string{"600519"}, nil)
	d, sink, _ := newTestDispatcher(hits, 0.25)
	a := tierEvent("同一件事的标题", "财联社", "利空", -0.75, "600519", "原因")
	b := tierEvent("同一件事的标题", "新浪", "利空", -0.75, "600519", "原因")

	rep := d.Round(RoundInput{Events: []NewsEvent{a, b}, LLM: LLMState{Available: true}})
	if len(sink.pushed) != 1 {
		t.Fatalf("同指纹两条应只发一次，实得 %d", len(sink.pushed))
	}
	if !strings.Contains(sink.pushed[0].Content, "×2") {
		t.Errorf("文案要标\"来源 ×N\"，实得 %s", sink.pushed[0].Content)
	}
	if rep.Suppressed != 0 {
		t.Errorf("同轮合并发生在分级阶段，不该记成冷却压制，实得 %d", rep.Suppressed)
	}
	// 下一轮再来一条同标题（跨轮）：仍只发一次
	d.Round(RoundInput{Events: []NewsEvent{a}, LLM: LLMState{Available: true}})
	if len(sink.pushed) != 1 {
		t.Errorf("跨轮同指纹不该重复出站，实得 %d", len(sink.pushed))
	}
}

// TestThresholdStepsDownOnHolding §5A.2 第 4 条：阈值复用 Agent.MinScore，命中持仓时下探一档。
// MinScore=0.5 时：|0.4| 的利空**命中持仓**才放行（下探到 0.25 档），未命中的同一分数不出站。
func TestThresholdStepsDownOnHolding(t *testing.T) {
	hits := NewHitSet([]string{"600519"}, nil)
	d, sink, _ := newTestDispatcher(hits, 0.5)
	rep := d.Round(RoundInput{
		Events: []NewsEvent{
			tierEvent("命中持仓的中等利空", "财联社", "利空", -0.4, "600519", "原因"),
			tierEvent("未命中的中等利空", "财联社", "利空", -0.4, "600588", "原因"),
		},
		LLM: LLMState{Available: true},
	})
	if len(sink.pushed) != 1 {
		t.Fatalf("命中持仓应下探一档放行、未命中应挡在门外，实得 %d 条（rep %+v）", len(sink.pushed), rep.Pushed)
	}
	if !strings.Contains(sink.pushed[0].Content, "600519") {
		t.Errorf("放行的应是命中持仓的那条，实得 %s", sink.pushed[0].Content)
	}
	// 已在最低档时不再下探（低于 0.25 的事件连落盘都不落，放行只会把噪音训练成告警）
	if got := ThresholdFor(0.25, true); got != 0.25 {
		t.Errorf("最低档不该再下探，实得 %v", got)
	}
	if got := ThresholdFor(0.75, true); got != 0.5 {
		t.Errorf("0.75 档命中持仓应下探到 0.5，实得 %v", got)
	}
	if got := ThresholdFor(0.75, false); got != 0.75 {
		t.Errorf("未命中不该下探，实得 %v", got)
	}
}

// TestNewsTabResetsOnTradingDayRollover §5A.3b：切交易日 ⇒ 新闻行、折叠计数、冷却与预算账同时归零，
// 而且判据**只有消息中心那一处**（data.TradingDayDate）——不另起第二套日界线。
func TestNewsTabResetsOnTradingDayRollover(t *testing.T) {
	hits := NewHitSet([]string{"600519"}, nil)
	d, sink, now := newTestDispatcher(hits, 0.25)
	*now = time.Date(2026, 11, 5, 10, 20, 0, 0, time.Local)
	d.Round(RoundInput{Events: []NewsEvent{tierEvent("今天的利空", "财联社", "利空", -0.75, "600519", "原因")}, LLM: LLMState{Available: true}})
	if len(sink.pushed) != 1 {
		t.Fatalf("当日首条利空应出站，实得 %d", len(sink.pushed))
	}
	// 连推三条同票（每 6 分钟避开冷却）把折叠计数推起来
	for i := 1; i < 3; i++ {
		*now = time.Date(2026, 11, 5, 10, 20+i*6, 0, 0, time.Local)
		d.Round(RoundInput{Events: []NewsEvent{tierEvent("今天的利空"+string(rune('a'+i)), "财联社", "利空", -0.75, "600519", "原因")}, LLM: LLMState{Available: true}})
	}
	before := d.State()
	if before.Folded == 0 || before.Daily == 0 {
		t.Fatalf("换日前当日账不该是空的：%+v", before)
	}

	// 换到下一个交易日：预算/折叠/去重全部归零（同一条利空当天首条要能再推）
	*now = time.Date(2026, 11, 6, 9, 30, 0, 0, time.Local)
	rep := d.Round(RoundInput{Events: []NewsEvent{tierEvent("今天的利空", "财联社", "利空", -0.75, "600519", "原因")}, LLM: LLMState{Available: true}})
	after := d.State()
	if after.Day == before.Day {
		t.Fatalf("日切没生效：day 仍是 %s", after.Day)
	}
	if after.Folded != 0 || after.Daily != 1 {
		t.Errorf("换日应清折叠并把当日必推重新从首条计：folded=%d daily=%d", after.Folded, after.Daily)
	}
	if len(rep.Pushed) != 1 {
		t.Errorf("换日后同一条利空应能再次出站，实得 %d", len(rep.Pushed))
	}

	// 消息中心侧：kind=news 的行跨交易日即清，非新闻行不动
	dir := t.TempDir()
	path := filepath.Join(dir, "messages.json")
	store := data.NewMessageStore(path)
	store.Sync([]data.MessageItem{{ID: "sig-1", Level: "交易信号", Title: "信号行", GeneratedAt: *now}})
	store.PublishNews([]data.MessageItem{
		{ID: "news|1", Title: "今日已折叠 2 条", GeneratedAt: *now},
		{ID: "news|2", Title: "某条新闻", GeneratedAt: *now},
	})
	if n := countNews(store.List()); n != 2 {
		t.Fatalf("当日应有 2 条新闻行，实得 %d", n)
	}
	// 把落盘文件里的交易日改成上一个交易日，模拟"跨过一天后第一次读"。
	// day-key 一律从文件里取、不写死常量：存储用的是 data.TradingDayDate(time.Now()) 这一处判据，
	// 用例拿自己造的 11-06 时刻去猜它反而会假红（本仓两套日界线出事的同一个形状）。
	// English: the stored day-key is read back instead of hard-coded — the store rolls on
	// TradingDayDate(time.Now()), and guessing it from the test's own clock is exactly the
	// two-day-lines bug this repo keeps hitting.
	raw := readFile(t, path)
	stored := storedDay(t, raw)
	if want := data.TradingDayDate(time.Now()); stored != want {
		t.Fatalf("存储里的 day-key=%s，与消息中心判据 %s 不同源", stored, want)
	}
	prev := shiftDay(t, stored, -1)
	if prev == stored {
		t.Fatalf("回拨一天后 day-key 没变（%s）——用例拿不到可判的换日对象", stored)
	}
	writeFile(t, path, strings.Replace(raw, `"trading_day": "`+stored+`"`, `"trading_day": "`+prev+`"`, 1))
	reopened := data.NewMessageStore(path)
	if n := countNews(reopened.List()); n != 0 {
		t.Errorf("切交易日后新闻行应清零，实得 %d", n)
	}
	if !hasRow(reopened.List(), "sig-1") {
		t.Error("日切误伤了非新闻行（信号流被清了）")
	}
}

// TestNewsEventsStillPersisted §5A.3b 第 3 条：UI 不留历史 ≠ 证据不留——
// 新闻行跨日清零之后，NewsEvent 事件表照旧落库（B9 测算、D1 复核、事后归因都吃这张表）。
func TestNewsEventsStillPersisted(t *testing.T) {
	dir := t.TempDir()
	a := New(nil, nil, nil, dir)
	evs := []NewsEvent{
		tierEvent("业绩爆雷", "财联社", "利空", -0.75, "600519", "净利下滑"),
		tierEvent("行业利好", "新浪", "利好", 0.6, "300750", "景气回升"),
	}
	a.SaveEvents(evs)
	if got := a.AllEvents(); len(got) != 2 {
		t.Fatalf("事件表应落 2 条，实得 %d", len(got))
	}
	// 事件表与新闻 tab 是两本账：新闻行只是 UI 的当日视图，事件文件必须独立存在
	if _, err := os.Stat(filepath.Join(dir, "news_events.json")); err != nil {
		t.Errorf("news_events.json 不在位（证据链断了）：%v", err)
	}
	store := data.NewMessageStore(filepath.Join(dir, "messages.json"))
	store.PublishNews([]data.MessageItem{{ID: "news|x", Title: "临时行"}})
	if len(store.List()) == 0 {
		t.Fatal("新闻行没落进消息中心（那 tab 就没有数据源）")
	}
}

// TestNoNewAlertRuleAdded §5A.1 前提 + §7 组⑨：通知增强**只调分级与文案**，
// 不新增 metrics 规则——24 条（必推 15 / 日汇总 9）是四处文案等值锁盯着的数，
// 顺手加第五条规则会把那些等值锁一起推红。
func TestNoNewAlertRuleAdded(t *testing.T) {
	rules := metrics.DefaultAlertRules()
	if len(rules) != 24 {
		t.Fatalf("默认告警规则条数变了（%d ≠ 24）——通知增强不许把它变成第五条规则", len(rules))
	}
	cfg := metrics.DefaultAlertRouting()
	if len(cfg.Routes) != len(rules) {
		t.Fatalf("路由表 %d 条 ≠ 规则表 %d 条", len(cfg.Routes), len(rules))
	}
	push, daily := 0, 0
	for _, r := range rules {
		switch cfg.Routes[r.Name] {
		case metrics.RoutePush:
			push++
		case metrics.RouteDaily:
			daily++
		default:
			t.Errorf("规则 %s 的路由既不是必推也不是日汇总", r.Name)
		}
	}
	if push != 15 || daily != 9 {
		t.Fatalf("必推/日汇总条数变了：push=%d daily=%d（应为 15/9）", push, daily)
	}
}

// countNews 数出新闻行的条数。
// English: countNews counts the news-kind rows.
func countNews(list []data.MessageItem) int {
	n := 0
	for _, m := range list {
		if m.Kind == data.KindNews {
			n++
		}
	}
	return n
}

// hasRow 按 ID 找一行。
// English: hasRow looks a row up by ID.
func hasRow(list []data.MessageItem, id string) bool {
	for _, m := range list {
		if m.ID == id {
			return true
		}
	}
	return false
}

// readFile 读文件全文（不存在即判红，不留"空字符串继续跑"的假绿）。
// English: readFile reads a file or fails the test — an empty string must never keep the case running.
func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 %s 失败: %v", path, err)
	}
	return string(b)
}

// writeFile 覆盖写文件（用例拿它把 day 手工回拨到上一个交易日）。
// English: writeFile overwrites the file (the test rewinds the stored day with it).
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("写 %s 失败: %v", path, err)
	}
}

// firstLineOf 取首行（报错时只带一行，不把整个 JSON 塞进测试输出）。
// English: firstLineOf keeps failure output short by quoting only the first line.
func firstLineOf(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// dayKeyRe 匹配落盘文件里的 trading_day 字段（用例只读它，不自己算第二套日界线）。
// English: dayKeyRe matches the stored trading_day field the test reads back.
var dayKeyRe = regexp.MustCompile(`"trading_day":\s*"([0-9]{8})"`)

// storedDay 取出文件里真正写进去的 day-key；取不到即判红（不许拿"没有匹配"继续往下跑成假绿）。
// English: storedDay reads the day-key actually written to disk, failing when it is absent.
func storedDay(t *testing.T, raw string) string {
	t.Helper()
	m := dayKeyRe.FindStringSubmatch(raw)
	if m == nil {
		t.Fatalf("落盘文件里没有 trading_day 字段：%s", firstLineOf(raw))
	}
	return m[1]
}

// shiftDay 把 YYYYMMDD 前后挪 n 天（换日用例拿它造"上一个交易日"，只判不等、不判节假日）。
// English: shiftDay moves a YYYYMMDD key by n days; the rollover case only needs a different day.
func shiftDay(t *testing.T, day string, n int) string {
	t.Helper()
	d, err := time.ParseInLocation("20060102", day, time.Local)
	if err != nil {
		t.Fatalf("day-key %q 解析失败: %v", day, err)
	}
	return d.AddDate(0, 0, n).Format("20060102")
}
