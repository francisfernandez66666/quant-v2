// §DAILY_REVIEW 盘后持仓综合复盘测试：宇宙构建去重/优先级/码归一、量化事实渲染、
// LLM 响应解析容错、runPositionReview 全链路（注入日K与LLM桩：写消息/每日键覆盖/失败回退守卫）、
// ReviewPositionsIfDue 四道门（开关/盘中/非交易日/每日一次）。全程离线，不触网不调真实 LLM。
// English: §DAILY_REVIEW tests — universe dedup/priority/normalization, fact rendering, LLM-response
// parsing tolerance, the full runPositionReview pipeline (injected daily-K & LLM stubs: message write,
// per-day key upsert, guard reset on failure) and the four ReviewPositionsIfDue gates (switch /
// in-session / non-trading-day / once-per-day). Fully offline: no network, no real LLM.
package engine

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
)

// reviewTestLoc 与生产一致的北京时区（trade_time 系列按 cntime 判定）。
var reviewTestLoc = time.FixedZone("CST", 8*3600)

// makeBars 合成 n 根日线：base 起按 step 单调（可负=下跌），量能从 v0 起线性增长。
func makeBars(n int, base, step, v0 float64) []data.KLine {
	out := make([]data.KLine, 0, n)
	for i := 0; i < n; i++ {
		c := base + step*float64(i)
		out = append(out, data.KLine{
			Date:   time.Date(2026, 9, 1, i, 0, 0, 0, reviewTestLoc),
			Open:   c * 0.99, High: c * 1.01, Low: c * 0.98, Close: c,
			Volume: v0 * (1 + 0.02*float64(i)),
		})
	}
	return out
}

func sig(code, name string) combat_agent.Signal {
	return combat_agent.Signal{Code: code, Name: name, Strategy: "dragon", Direction: "做多", Action: "买入"}
}

// TestReviewUniverseDedupPriority 宇宙去重：同码多来源合并 Sources、后缀归一、优先级=最小来源级别。
func TestReviewUniverseDedupPriority(t *testing.T) {
	ss := newSignalStore("")
	ss.Upsert([]combat_agent.Signal{sig("600519.SH", "贵州茅台"), sig("000001", "平安银行")})
	e := &Engine{userID: "u1", signalStore: ss}
	got := e.reviewUniverse()
	if len(got) != 2 {
		t.Fatalf("期望2只（信号去重后），got %d: %+v", len(got), got)
	}
	// 代码升序 + 同为信号优先级：000001 在前
	if got[0].Code != "000001" || got[1].Code != "600519" {
		t.Fatalf("排序/归一不符: %+v", got)
	}
	if got[1].Name != "贵州茅台" {
		t.Errorf("名称应带出: %+v", got[1])
	}
	if got[0].prio != reviewPrioSignal {
		t.Errorf("信号优先级=%d，got %d", reviewPrioSignal, got[0].prio)
	}
	// signalStore 对 Strategy 为空的信号不入库 → 空策略信号不污染宇宙
	ss2 := newSignalStore("")
	ss2.Upsert([]combat_agent.Signal{{Code: "300750", Name: "宁德时代", Direction: "做多"}})
	e2 := &Engine{userID: "u1", signalStore: ss2}
	if len(e2.reviewUniverse()) != 0 {
		t.Errorf("无 Strategy 的信号不应入库进宇宙")
	}
}

// TestReviewFactForCode 事实行渲染：样本不足提示、正常含关键段落。
func TestReviewFactForCode(t *testing.T) {
	short := reviewFactForCode(reviewTarget{Code: "600519", Name: "X"}, makeBars(12, 100, 0.5, 1e6))
	if !strings.Contains(short, "样本不足") {
		t.Errorf("少于30根应标注数据不足，got %q", short)
	}
	// 上升+放量：约60根，日涨幅为正
	kl := makeBars(60, 100, 0.8, 1e6)
	f := reviewFactForCode(reviewTarget{Code: "600000", Name: "浦发银行", Cost: 140, Sources: []string{"实盘", "自选"}, prio: 0}, kl)
	for _, want := range []string{"600000", "浦发银行", "实盘·自选", "现价", "MA5/10/20", "多头排列", "MACD", "位置", "成本140.00", "浮+"} {
		if !strings.Contains(f, want) {
			t.Errorf("事实行缺少片段 %q：%s", want, f)
		}
	}
	// 下跌序列 → 空头排列
	down := makeBars(60, 200, -0.8, 1e6)
	fd := reviewFactForCode(reviewTarget{Code: "000001", Name: "A"}, down)
	if !strings.Contains(fd, "空头排列") {
		t.Errorf("单调下跌应判空头排列：%s", fd)
	}
}

// TestParseReviewResponse 解析容错：围栏/后缀/未知倾向→中性/缺正文行跳过/重复后者覆盖。
func TestParseReviewResponse(t *testing.T) {
	resp := "``` \n600519.SH|偏多|多头格局，量能配合。\n000001|看多|非法倾向按中性\n300750|偏空|破位\n600000|中性\nBAD|偏多|无正文\n偏多|错位\n```"
	m := parseReviewResponse(resp)
	if m["600519"][0] != "偏多" || !strings.Contains(m["600519"][1], "多头") {
		t.Errorf("解析首行不符: %+v", m["600519"])
	}
	if m["000001"][0] != "中性" {
		t.Errorf("未知倾向应归中性: %+v", m["000001"])
	}
	if m["600000"][0] != "" && m["600000"][1] != "" {
		t.Errorf("缺正文应跳过: %+v", m["600000"])
	}
	if _, ok := m["BAD"]; ok {
		t.Errorf("非6位代码行不应入库（parts<2 或 code 非数字视实现）")
	}
}

// TestRunPositionReviewPipeline 全链路：宇宙(2信号)→桩日K→桩LLM→私有复盘消息落库、
// 每日键覆盖更新不重复、LLM 失败守卫回退可重试。
func TestRunPositionReviewPipeline(t *testing.T) {
	store := data.NewMessageStore("")
	ss := newSignalStore("")
	ss.Upsert([]combat_agent.Signal{sig("600519", "贵州茅台"), sig("000001", "平安银行")})
	var prompts int
	e := &Engine{userID: "u1", msgStore: store, signalStore: ss}
	e.reviewDailyK = func(code string) ([]data.KLine, error) {
		if code == "000001" {
			return makeBars(60, 20, -0.2, 5e6), nil
		}
		return makeBars(60, 1500, 3, 3e4), nil
	}
	e.reviewAsk = func(system, user string) (string, error) {
		prompts++
		if !strings.Contains(user, "600519") || !strings.Contains(user, "000001") || !strings.Contains(system, "CODE|倾向|正文") {
			t.Errorf("提示词应含两只股票事实与格式约定")
		}
		return "600519|偏多|站上均线，温和放量，MACD水上扩张，回踩不破可持有。\n000001|偏空|空头排列且跌时放量，反弹缩量，注意止损位。", nil
	}
	now := time.Date(2026, 9, 14, 15, 30, 0, 0, reviewTestLoc)
	n, err := e.runPositionReview(now, true)
	if err != nil || n != 2 {
		t.Fatalf("首轮应复盘2只: n=%d err=%v", n, err)
	}
	msgs := store.ListVisible("u1")
	if len(msgs) != 2 {
		t.Fatalf("消息中心应落2条复盘: %+v", msgs)
	}
	for _, m := range msgs {
		if m.Level != "复盘" || m.Scope != "u1" || !strings.HasPrefix(m.ID, "pos-review@u1@") {
			t.Errorf("复盘消息字段不符: %+v", m)
		}
		if m.Direction != "偏多" && m.Direction != "偏空" {
			t.Errorf("Direction 应为倾向: %+v", m)
		}
		if !strings.Contains(m.Body, "量化事实") {
			t.Errorf("正文应附量化事实: %s", m.Body)
		}
	}
	// 同日重跑（手动 force）→ 按日键覆盖，不新增
	n2, err := e.runPositionReview(now.Add(time.Hour), true)
	if err != nil || n2 != 2 || len(store.ListVisible("u1")) != 2 {
		t.Fatalf("重跑应覆盖不增殖: n=%d len=%d err=%v", n2, len(store.ListVisible("u1")), err)
	}
	// LLM 失败：非 force 自动跑回退守卫、返回错误可重试
	e2 := &Engine{userID: "u2", msgStore: store, signalStore: ss}
	e2.reviewDailyK = e.reviewDailyK
	e2.reviewAsk = func(string, string) (string, error) { return "", fmt.Errorf("llm down") }
	if _, err := e2.runPositionReview(now, false); err == nil {
		t.Fatal("LLM 失败应返回错误")
	}
	if e2.reviewGuardDay != "" {
		t.Errorf("失败应清空当日守卫以便重试: %q", e2.reviewGuardDay)
	}
	// 自动跑成功后，同日第二次自动跑被守卫拦截
	e3 := &Engine{userID: "u3", msgStore: store, signalStore: ss}
	e3.reviewDailyK = e.reviewDailyK
	calls := 0
	e3.reviewAsk = func(s, u string) (string, error) { calls++; return e.reviewAsk(s, u) }
	cm := &config.Manager{Rules: &config.Rules{}}
	e3.cfgMgr = cm
	if n, err := e3.runPositionReview(now, false); err != nil || n != 2 || calls != 1 {
		t.Fatalf("自动首轮应成功: n=%d calls=%d err=%v", n, calls, err)
	}
	if n, err := e3.runPositionReview(now.Add(time.Minute), false); err != nil || n != 0 || calls != 1 {
		t.Fatalf("同日自动重跑应被守卫拦截: n=%d calls=%d", n, calls)
	}
}

// TestReviewPositionsIfDueGates 四道门：开关关闭/盘中/周末/每日一次。
func TestReviewPositionsIfDueGates(t *testing.T) {
	boom := func(system, user string) (string, error) { t.Error("门控拦截时不应调用 LLM"); return "", nil }
	newE := func(cm *config.Manager) *Engine {
		ss := newSignalStore("")
		ss.Upsert([]combat_agent.Signal{sig("600519", "贵州茅台")})
		e := &Engine{userID: "u", msgStore: data.NewMessageStore(""), signalStore: ss, cfgMgr: cm}
		e.reviewDailyK = func(string) ([]data.KLine, error) { return makeBars(60, 100, 1, 1e6), nil }
		e.reviewAsk = boom
		return e
	}
	off := false
	mOff := &config.Manager{Rules: &config.Rules{}}
	mOff.Rules.Runtime.ReviewEnabled = &off
	// ① 开关关闭
	newE(mOff).ReviewPositionsIfDue(time.Date(2026, 9, 14, 16, 0, 0, 0, reviewTestLoc))
	// ② 盘中（10:00 交易时段）
	newE(&config.Manager{Rules: &config.Rules{}}).ReviewPositionsIfDue(time.Date(2026, 9, 14, 10, 0, 0, 0, reviewTestLoc))
	// ③ 收盘后但非交易日（周六 16:00）
	newE(&config.Manager{Rules: &config.Rules{}}).ReviewPositionsIfDue(time.Date(2026, 9, 12, 16, 0, 0, 0, reviewTestLoc))
	// ④ 午间休市时刻（12:00 非活跃但早于15点）
	newE(&config.Manager{Rules: &config.Rules{}}).ReviewPositionsIfDue(time.Date(2026, 9, 14, 12, 0, 0, 0, reviewTestLoc))
}

// TestReviewPositionsIfDueRunsOnce 门全开时：交易日收盘后触发一次，reviewAsk 被调用且守卫置当日。
func TestReviewPositionsIfDueRunsOnce(t *testing.T) {
	ss := newSignalStore("")
	ss.Upsert([]combat_agent.Signal{sig("600519", "贵州茅台")})
	e := &Engine{userID: "u", msgStore: data.NewMessageStore(""), signalStore: ss, cfgMgr: &config.Manager{Rules: &config.Rules{}}}
	e.reviewDailyK = func(string) ([]data.KLine, error) { return makeBars(60, 100, 1, 1e6), nil }
	calls := 0
	e.reviewAsk = func(string, string) (string, error) { calls++; return "600519|中性|震荡整理，量能平稳。", nil }
	now := time.Date(2026, 9, 14, 15, 30, 0, 0, reviewTestLoc)
	e.ReviewPositionsIfDue(now)
	if calls != 1 {
		t.Fatalf("收盘后交易日应触发1次，calls=%d", calls)
	}
	if e.reviewGuardDay != "2026-09-14" {
		t.Fatalf("守卫应置当日: %q", e.reviewGuardDay)
	}
	if msgs := e.msgStore.ListVisible("u"); len(msgs) != 1 || msgs[0].Level != "复盘" {
		t.Fatalf("应落1条复盘消息: %+v", msgs)
	}
	e.ReviewPositionsIfDue(now.Add(15 * time.Minute)) // 盘后休眠分支 15min 后再唤醒
	if calls != 1 {
		t.Errorf("同日二次唤醒应被守卫拦截: calls=%d", calls)
	}
}
