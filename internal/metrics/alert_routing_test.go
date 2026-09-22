// alert_routing_test.go §高-3（2026-09-22 傍晚审计批）：指标型告警出口的出站路由回归。
// 四把锁：①破线只推一条、冷却窗内重复破线被抑制 ②恢复成对销案（不发孤立 resolved，
// 被冷却挡下的销案到期补发）③日汇总在检测到本地日历日变更时补发（次数/峰值/首末时间）
// ④未注入出口时那条 Warn 必响（主代理据此写 verify 负向锁）。时钟全部注入，测试不 sleep。
package metrics

import (
	"bytes"
	"log"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuffer 并发安全的日志捕获缓冲（log.SetOutput 重定向用）。
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *syncBuffer) reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.Reset()
}

// captureLog 把标准日志重定向到 buffer，返回 buffer 与还原函数。
func captureLog(t *testing.T) (*syncBuffer, func()) {
	t.Helper()
	b := &syncBuffer{}
	old := log.Default().Writer()
	log.SetOutput(b)
	return b, func() { log.SetOutput(old) }
}

// fakeClock 可推进的测试时钟（手动 advance，绝不 sleep）。
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

// newTestRouter 造一个用固定时钟 + 默认路由表的路由器。
func newTestRouter(start time.Time) (*alertRouter, *fakeClock) {
	clk := &fakeClock{t: start}
	return newAlertRouter(clk.now), clk
}

// fireEv / recoverEv 构造评估器会吐出的事件。
func fireEv(name, level string, v float64) AlertEvent {
	return AlertEvent{Name: name, Level: level, Kind: "fire", Value: v, Message: name + " 文案"}
}

func recoverEv(name, level string, v float64) AlertEvent {
	return AlertEvent{Name: name, Level: level, Kind: "recover", Value: v, Message: name + " 文案"}
}

// dayStart 用本地时区构造测试基准时刻（日汇总按本地日历日切分，必须与 time.Local 一致）。
func dayStart(y int, mo time.Month, d, h, mi int) time.Time {
	return time.Date(y, mo, d, h, mi, 0, 0, time.Local)
}

// TestPushRuleFiresOnceWithinCooldown ①必推类：首次破线出 1 条 alert；
// 30s 节拍下的重复破线（震荡）在 30 分钟触发冷却窗内不再出站；窗到期后才放行下一条。
func TestPushRuleFiresOnceWithinCooldown(t *testing.T) {
	r, clk := newTestRouter(dayStart(2026, 9, 22, 10, 0))

	dels := r.Route([]AlertEvent{fireEv("breaker_open", "p1", 1)})
	if len(dels) != 1 || dels[0].Kind != KindAlert || dels[0].Level != "p1" {
		t.Fatalf("首次破线应出 1 条 alert，got %+v", dels)
	}
	if !strings.Contains(dels[0].Body, "breaker_active") {
		t.Errorf("alert 正文应带 metric 名便于排障，got %q", dels[0].Body)
	}

	// 震荡：恢复后在触发冷却窗内反复破线 → 既不刷第二条 alert，
	// 也不发"没报过"的孤立 resolved（alert/resolved 严格成对）。
	clk.advance(30 * time.Second)
	if got := r.Route([]AlertEvent{recoverEv("breaker_open", "p1", 0)}); len(got) != 1 || got[0].Kind != KindResolved {
		t.Fatalf("恢复应出 1 条 resolved，got %+v", got)
	}
	for i := 0; i < 20; i++ { // 20 个来回 = 20 分钟，仍在 30 分钟窗内
		clk.advance(30 * time.Second)
		if got := r.Route([]AlertEvent{fireEv("breaker_open", "p1", 1)}); len(got) != 0 {
			t.Fatalf("触发冷却窗内第 %d 轮不应重复推 alert，got %+v", i, got)
		}
		clk.advance(30 * time.Second)
		if got := r.Route([]AlertEvent{recoverEv("breaker_open", "p1", 0)}); len(got) != 0 {
			t.Fatalf("被抑制的触发不该产生孤立 resolved（第 %d 轮），got %+v", i, got)
		}
	}
	if r.suppressed["breaker_open"] != 20 {
		t.Errorf("抑制次数应被计数（诊断用），got %d", r.suppressed["breaker_open"])
	}
	// 窗到期（首轮 alert 在 10:00:00，走到 10:21:00，再推到 10:32:00）→ 允许再报一次
	clk.advance(11 * time.Minute)
	if got := r.Route([]AlertEvent{fireEv("breaker_open", "p1", 1)}); len(got) != 1 || got[0].Kind != KindAlert {
		t.Fatalf("触发冷却窗到期后应再次放行 alert，got %+v", got)
	}
}

// TestResolvedIsPairedAndNeverOrphan ②alert/resolved 成对：没报过就不许销案；
// 被恢复冷却窗挡下的销案挂起、窗到期后补发（不出现"只报不销"）。
func TestResolvedIsPairedAndNeverOrphan(t *testing.T) {
	r, clk := newTestRouter(dayStart(2026, 9, 22, 11, 0))

	// 未触发过就直接恢复 → 孤立 resolved，必须吞掉。
	if got := r.Route([]AlertEvent{recoverEv("uplink_stale", "p1", 0)}); len(got) != 0 {
		t.Fatalf("未报过的规则不应发孤立 resolved，got %+v", got)
	}
	if got := r.Route([]AlertEvent{fireEv("uplink_stale", "p1", 400)}); len(got) != 1 || got[0].Kind != KindAlert {
		t.Fatalf("必推规则触发应出 alert，got %+v", got)
	}
	clk.advance(time.Minute)
	if got := r.Route([]AlertEvent{recoverEv("uplink_stale", "p1", 5)}); len(got) != 1 || got[0].Kind != KindResolved {
		t.Fatalf("恢复应立刻出 resolved 销案，got %+v", got)
	}
	// 冷却窗内再次破线 → 抑制；其后的恢复也就不会产生待销状态（无在途 alert）
	clk.advance(30 * time.Second)
	if got := r.Route([]AlertEvent{fireEv("uplink_stale", "p1", 500)}); len(got) != 0 {
		t.Fatalf("冷却窗内重复触发应被抑制，got %+v", got)
	}
	clk.advance(30 * time.Second)
	if got := r.Route([]AlertEvent{recoverEv("uplink_stale", "p1", 1)}); len(got) != 0 {
		t.Fatalf("被抑制的触发不该留下待销状态，got %+v", got)
	}

	// 悬置销案补发：触发窗 1 分钟 / 恢复窗 30 分钟的配置下，第二次 resolved 会撞窗被挂起。
	r2, clk2 := newTestRouter(dayStart(2026, 9, 22, 12, 0))
	r2.cfg.FireCooldown = time.Minute
	r2.cfg.ResolvedCooldown = 30 * time.Minute
	if got := r2.Route([]AlertEvent{fireEv("quote_stale", "p2", 90)}); len(got) != 1 {
		t.Fatalf("应出第 1 条 alert，got %+v", got)
	}
	clk2.advance(30 * time.Second)
	if got := r2.Route([]AlertEvent{recoverEv("quote_stale", "p2", 1)}); len(got) != 1 || got[0].Kind != KindResolved {
		t.Fatalf("应出第 1 条 resolved，got %+v", got)
	}
	clk2.advance(30 * time.Second) // 触发窗到期 → 第二次 alert 放行
	if got := r2.Route([]AlertEvent{fireEv("quote_stale", "p2", 120)}); len(got) != 1 || got[0].Kind != KindAlert {
		t.Fatalf("触发窗到期后应再出 alert，got %+v", got)
	}
	clk2.advance(30 * time.Second)
	if got := r2.Route([]AlertEvent{recoverEv("quote_stale", "p2", 2)}); len(got) != 0 {
		t.Fatalf("恢复窗内第二条 resolved 应先挂起，got %+v", got)
	}
	if len(r2.pendingR) != 1 {
		t.Fatalf("应有 1 条待补发销案，got %v", r2.pendingR)
	}
	clk2.advance(31 * time.Minute)
	if got := r2.Route(nil); len(got) != 1 || got[0].Kind != KindResolved {
		t.Fatalf("恢复冷却到期后应补发销案，got %+v", got)
	}
	if len(r2.pendingR) != 0 || r2.announced["quote_stale"] {
		t.Errorf("补发后待销队列与在途标记应清空，pending=%v announced=%v", r2.pendingR, r2.announced)
	}
}

// TestDailySummaryOnRollover ③日汇总：当天聚合（次数/峰值/首次/末次），
// 在下一个 tick 检测到本地日历日变更时补发前一日；同一天不重复发；空日不发。
func TestDailySummaryOnRollover(t *testing.T) {
	r, clk := newTestRouter(dayStart(2026, 9, 22, 9, 30))

	seq := []struct {
		ev  AlertEvent
		adv time.Duration
	}{
		{fireEv("buy_queue_high", "p2", 40), 0},
		{recoverEv("buy_queue_high", "p2", 5), 2 * time.Hour},
		{fireEv("buy_queue_high", "p2", 55), time.Hour},
		{fireEv("llm_cooldown", "p2", 3), 0},
	}
	for i, s := range seq {
		clk.advance(s.adv)
		if got := r.Route([]AlertEvent{s.ev}); len(got) != 0 {
			t.Fatalf("日汇总类规则不该即时出站（第 %d 步），got %+v", i, got)
		}
	}
	// 同一天继续跑：不发汇总
	clk.advance(4 * time.Hour)
	if got := r.Route(nil); len(got) != 0 {
		t.Fatalf("同一日历日内不应发汇总，got %+v", got)
	}
	// 跨日：次日首个 tick 补发前一日汇总
	clk.t = dayStart(2026, 9, 23, 0, 1)
	got := r.Route(nil)
	if len(got) != 1 || got[0].Kind != KindDailySummary {
		t.Fatalf("跨日首个 tick 应发 1 条日汇总，got %+v", got)
	}
	d := got[0]
	if !strings.Contains(d.Title, "2026-09-22") {
		t.Errorf("汇总标题应标注被汇总的那一日，got %q", d.Title)
	}
	if d.FireCount != 3 {
		t.Errorf("当日触发总次数应为 3（buy_queue_high 2 + llm_cooldown 1），got %d", d.FireCount)
	}
	for _, want := range []string{"buy_queue_high", "触发 2 次", "峰值 55.00", "首次 09:30:00", "末次", "llm_cooldown", "触发 1 次"} {
		if !strings.Contains(d.Body, want) {
			t.Errorf("汇总正文缺 %q:\n%s", want, d.Body)
		}
	}
	if !strings.Contains(d.Body, "llm_cooldown") || !strings.Contains(d.Body, "仍在触发") {
		t.Errorf("未恢复的规则应标注仍在触发:\n%s", d.Body)
	}
	// 新的一天没有任何触发 → 再次跨日不发噪声汇总
	clk.advance(26 * time.Hour)
	if got := r.Route(nil); len(got) != 0 {
		t.Fatalf("空日不应发噪声汇总，got %+v", got)
	}
}

// TestUnwiredSinkWarnsLoudly ④关键锁：未注入 AlertSink 时，首次出站必须打一条明确 Warn
// 说明「指标告警已评估但出口未接线」，且每条投递仍落兜底日志——绝不静默丢失。
func TestUnwiredSinkWarnsLoudly(t *testing.T) {
	buf, restore := captureLog(t)
	t.Cleanup(func() { SetAlertSink(nil); restore() })
	SetAlertSink(nil) // 复位 sink 与 Warn 标记

	alertOutput.emit(AlertDelivery{Kind: KindAlert, Rule: "breaker_open", Level: "p1",
		Title: "【指标告警/p1】实盘网关熔断中", Body: "规则 breaker_open 触发：metric=breaker_active"})
	out := buf.String()
	if !strings.Contains(out, "[WARN]") || !strings.Contains(out, "出口未接线") {
		t.Fatalf("未接线时首条出站应有明确 Warn，got:\n%s", out)
	}
	if !strings.Contains(out, "告警出站") || !strings.Contains(out, "breaker_open") {
		t.Fatalf("未接线时事件仍须落兜底日志（不能静默丢），got:\n%s", out)
	}
	// Warn 只响一次（不刷屏），后续投递照旧有兜底日志。
	buf.reset()
	alertOutput.emit(AlertDelivery{Kind: KindResolved, Rule: "breaker_open", Level: "p1",
		Title: "【指标告警恢复】实盘网关熔断中", Body: "规则 breaker_open 恢复正常"})
	out2 := buf.String()
	if strings.Contains(out2, "[WARN]") {
		t.Errorf("Warn 应只打一次，got:\n%s", out2)
	}
	if !strings.Contains(out2, "告警出站") {
		t.Errorf("第二条仍应有兜底日志，got:\n%s", out2)
	}
}

// TestSinkReceivesRoutedDeliveries 出口注入后：投递确实交给 sink，且不再打未接线 Warn。
func TestSinkReceivesRoutedDeliveries(t *testing.T) {
	buf, restore := captureLog(t)
	t.Cleanup(func() { SetAlertSink(nil); restore() })
	var got []AlertDelivery
	SetAlertSink(func(d AlertDelivery) { got = append(got, d) })

	r, _ := newTestRouter(dayStart(2026, 9, 22, 14, 0))
	for _, d := range r.Route([]AlertEvent{fireEv("order_fail_rate", "p1", 120)}) {
		alertOutput.emit(d)
	}
	if len(got) != 1 || got[0].Kind != KindAlert || got[0].Rule != "order_fail_rate" {
		t.Fatalf("sink 应收到 1 条 alert，got %+v", got)
	}
	if got[0].Level != "p1" {
		t.Errorf("必推项应带 p1（注入方据此走 notify.LevelHigh 高优通道），got %q", got[0].Level)
	}
	if strings.Contains(buf.String(), "出口未接线") {
		t.Errorf("已接线时不该再打未接线 Warn:\n%s", buf.String())
	}
	if !AlertSinkInjected() {
		t.Errorf("AlertSinkInjected 应为 true")
	}
}

// TestRoutingCoversAllDefaultRules 路由表必须覆盖 DefaultAlertRules() 每一条：
// 任何一条落到"未列出"就意味着它悄悄走了默认分支（本批要消灭的静默形态）。
func TestRoutingCoversAllDefaultRules(t *testing.T) {
	cfg := DefaultAlertRouting()
	for _, rule := range DefaultAlertRules() {
		rt, ok := cfg.Routes[rule.Name]
		if !ok {
			t.Errorf("规则 %s 未在路由表中显式列出", rule.Name)
			continue
		}
		if rt != RoutePush && rt != RouteDaily && rt != RouteLog {
			t.Errorf("规则 %s 去向非法: %s", rule.Name, rt)
		}
	}
	if len(cfg.Routes) != len(DefaultAlertRules()) {
		t.Errorf("路由表 %d 条 ≠ 规则表 %d 条", len(cfg.Routes), len(DefaultAlertRules()))
	}
}

// TestRunAlertEvaluationEndToEnd 端到端：RunAlertEvaluation 签名不变（无参可调用），
// 量规破线后经全局路由器把必推事件交给注入的 sink；持续破线不刷屏；回落成对销案。
// 全局评估器/路由器状态会被换出换新（否则 -count=2 复跑会被上一轮的冷却窗挡住）。
func TestRunAlertEvaluationEndToEnd(t *testing.T) {
	_, restore := captureLog(t)
	oldAlerter, oldRouter := globalAlerter, globalAlertRouter
	t.Cleanup(func() {
		SetAlertSink(nil)
		SetGauge("breaker_active", 0)
		globalAlerter, globalAlertRouter = oldAlerter, oldRouter
		restore()
	})
	globalAlerter, globalAlertRouter = NewAlerter(), newAlertRouter(nil)
	var got []AlertDelivery
	SetAlertSink(func(d AlertDelivery) { got = append(got, d) })
	SetGauge("breaker_active", 1) // 熔断：For=0s 立即 fire

	RunAlertEvaluation()
	if n := countKind(got, "breaker_open", KindAlert); n != 1 {
		t.Fatalf("熔断破线应经出口推 1 条，got %+v", got)
	}
	// 第二个节拍：仍持续破线 → 不重复刷（评估器去重 + 路由限频双保险）
	RunAlertEvaluation()
	if n := countKind(got, "breaker_open", KindAlert); n != 1 {
		t.Fatalf("持续破线不得每节拍刷一条，got %+v", got)
	}
	// 回落 → 销案成对
	SetGauge("breaker_active", 0)
	RunAlertEvaluation()
	if n := countKind(got, "breaker_open", KindResolved); n != 1 {
		t.Fatalf("回落应补 1 条 resolved，got %+v", got)
	}
}

// countKind 统计某规则某种类的出站条数。
func countKind(ds []AlertDelivery, rule, kind string) int {
	n := 0
	for _, d := range ds {
		if d.Rule == rule && d.Kind == kind {
			n++
		}
	}
	return n
}

// TestConfigureAlertRoutingIdempotent ConfigureAlertRouting 幂等：零值字段保留当前配置。
func TestConfigureAlertRoutingIdempotent(t *testing.T) {
	before := globalAlertRouter.cfg
	ConfigureAlertRouting(AlertRoutingConfig{}) // 全零值 → 什么都不该改
	if globalAlertRouter.cfg.FireCooldown != before.FireCooldown ||
		globalAlertRouter.cfg.ResolvedCooldown != before.ResolvedCooldown ||
		len(globalAlertRouter.cfg.Routes) != len(before.Routes) {
		t.Fatalf("零值配置不应改动路由：before=%+v after=%+v", before, globalAlertRouter.cfg)
	}
	ConfigureAlertRouting(AlertRoutingConfig{Routes: map[string]AlertRoute{"breaker_open": RouteLog}})
	if globalAlertRouter.routeOfLocked("breaker_open") != RouteLog {
		t.Errorf("显式覆盖未生效")
	}
	if rt := globalAlertRouter.routeOfLocked("brand_new_rule"); rt != RoutePush {
		t.Errorf("未列出的新规则应默认必推（宁多报不漏报），got %s", rt)
	}
	ConfigureAlertRouting(before) // 复原，避免影响同包其它测试
	if globalAlertRouter.routeOfLocked("breaker_open") != RoutePush {
		t.Errorf("复原失败")
	}
}
