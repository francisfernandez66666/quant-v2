// heartbeat_test.go — §0929HB-1（2026-09-29 全量审计批 ⑪-4）零固化信号心跳用例。
//
// 职责三条（逐条对应本批锤实的失效形态）：
//  1. 判据纯函数：计时/不计时场景的边界（含时钟回拨、锚未建立、已有信号）；
//  2. 喂数走真实 refreshStalenessGauges 通路：锁死"规则读的键＝写端落的键"（§DEADGAUGE 死规则
//     老坑：09-23 一次锤出三条有规则无数据源的告警），并锁死"非实盘引擎不写 0"这条
//     反掩蔽纪律；
//  3. 规则登记面：名称/等级/阈值/键名等值断言（不是"至少有一条"这种单向锁）。
//
// 反证设计（不靠回滚代码验证）：同一时刻、同一锚，成对给"零信号 31 分钟"与"已有 2 条固化信号"，
// 前者必须 1860、后者必须恰好 0；摘掉 `pinnedSignals > 0` 这一支判据，用例当场变红。
// English: §0929HB-1 heartbeat tests — pure-predicate boundaries, the real feed path through
// refreshStalenessGauges (key-name equality with the alert rule, plus the no-write-for-non-live
// anti-masking discipline), and equality locks on the registered rule.
package engine

import (
	"testing"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/metrics"
	"quant-trading-v2/internal/trading"
)

// hbAnchor/hbLater 两个稳定时刻：2026-09-29（周二，交易日）盘中 10:00 建锚、10:31 再走一轮。
// 判据一律吃注入的 now，不依赖用例运行的墙上时钟。
var (
	hbAnchor = time.Date(2026, 9, 29, 10, 0, 0, 0, cntime.Loc)
	hbLater  = hbAnchor.Add(31 * time.Minute)
	hbClosed = time.Date(2026, 9, 29, 20, 0, 0, 0, cntime.Loc) // 盘后：必须收案写 0
	// hbSaturday＝2026-09-26（周六）盘中 10:31：**钟点在盘中、日历上不是交易日**。
	// 这一支是 §0929HB-1 自己埋的雷（IsActiveSession 只看钟点），补成用例钉住：
	// 摘掉 signalHeartbeatAge 的 tradingDay 判据，TestSignalHeartbeatSaturdayIsNotAFault 当场红。
	hbSaturday = time.Date(2026, 9, 26, 10, 31, 0, 0, cntime.Loc)
	// hbFriday＝同一天的"昨天"，用来造"休市日看到的是上一交易日回填的老信号"这种合法态。
	hbFriday = hbSaturday.AddDate(0, 0, -1)
)

// TestSignalHeartbeatAgePredicate 纯函数判据表：九支各自独立，覆盖计时/不计时全部入口条件。
func TestSignalHeartbeatAgePredicate(t *testing.T) {
	cases := []struct {
		name       string
		now        time.Time
		anchor     time.Time
		session    bool
		tradingDay bool
		live       bool
		pinned     int
		want       int64
	}{
		{"盘中零信号 31 分钟", hbLater, hbAnchor, true, true, true, 0, 1860},
		{"盘中刚建锚（时长 0）", hbAnchor, hbAnchor, true, true, true, 0, 0},
		{"已有固化信号必须归零", hbLater, hbAnchor, true, true, true, 3, 0},
		{"非实盘不计时", hbLater, hbAnchor, true, true, false, 0, 0},
		{"非盘中不计时", hbLater, hbAnchor, false, true, true, 0, 0},
		{"休市日不计时", hbSaturday, hbSaturday.Add(-31 * time.Minute), true, false, true, 0, 0},
		{"锚未建立（零值时间）", hbLater, time.Time{}, true, true, true, 0, 0},
		{"时钟回拨不倒计", hbAnchor, hbLater, true, true, true, 0, 0},
	}
	for _, c := range cases {
		if got := signalHeartbeatAge(c.now, c.anchor, c.session, c.tradingDay, c.live, c.pinned); got != c.want {
			t.Errorf("%s: signalHeartbeatAge=%d 期望 %d", c.name, got, c.want)
		}
	}
}

// TestClosedDayPinnedCountPair 反证对：同一批信号，**休市日**只数"今天生成的"，
// 交易日恒 0；跨日回填（GeneratedAt 是上一个交易日）与零值时刻都不计。
// 四支各自摘掉一条判据都会红：tradingDay 短路、同日判定、IsZero 跳过。
func TestClosedDayPinnedCountPair(t *testing.T) {
	sigs := []combat_agent.Signal{
		{Code: "600001.SH", Strategy: "dragon", GeneratedAt: hbSaturday},                      // 休市日当天新增 ⇒ 计
		{Code: "600002.SH", Strategy: "dragon", GeneratedAt: hbSaturday.Add(2 * time.Minute)}, // 同上 ⇒ 计
		{Code: "600003.SH", Strategy: "dragon", GeneratedAt: hbFriday.Add(10 * time.Hour)},    // 上一交易日老信号 ⇒ 不计
		{Code: "600004.SH", Strategy: "dragon"},                                               // 无生成时刻 ⇒ 不计（不伪造红）
	}
	if got := closedDayPinnedCount(hbSaturday, false, sigs); got != 2 {
		t.Fatalf("休市日应只数当天新增的 2 条，got %d", got)
	}
	// 反证 A：同一天换成交易日口径（tradingDay=true）⇒ 必须恰好 0（交易日出信号是本分）。
	if got := closedDayPinnedCount(hbSaturday, true, sigs); got != 0 {
		t.Fatalf("交易日恒 0，got %d", got)
	}
	// 反证 B：摘掉"同一天"判据就会把 3 条都算进来 ⇒ 这里单独钉跨日那一支。
	if got := closedDayPinnedCount(hbSaturday, false, sigs[:1]); got != 1 {
		t.Fatalf("单条当天新增应为 1，got %d", got)
	}
	if got := closedDayPinnedCount(hbSaturday, false, sigs[2:]); got != 0 {
		t.Fatalf("老信号+无时刻都必须不计，got %d", got)
	}
}

// hbEngineWithLive 造一台引擎：控制器实盘开关按参数，固化信号库按参数条数（键互不相同，
// signalStore 以 code@strategy 去重，同键只会留 1 条 ⇒ 条数必须真不相同）。
func hbEngineWithLive(t *testing.T, enabled bool, pinned int) *Engine {
	t.Helper()
	e := &Engine{}
	e.mu.Lock()
	e.qmtCtrl = trading.NewController(nil, nil, "u_hb", config.QMTConfig{Enabled: enabled}, nil)
	if pinned > 0 {
		byKey := map[string]combat_agent.Signal{}
		for i := 0; i < pinned; i++ {
			code := "60000" + string(rune('0'+i)) + ".SH"
			byKey[code+"@dragon"] = combat_agent.Signal{Code: code, Strategy: "dragon"}
		}
		e.signalStore = &signalStore{byKey: byKey, invalidated: map[string]bool{}}
	}
	e.mu.Unlock()
	return e
}

// TestFeedSignalHeartbeatZeroAndPinnedPair 反证对：同一时刻、同一锚，
// 零信号必须报出时长、已有固化信号必须恰好归零。
func TestFeedSignalHeartbeatZeroAndPinnedPair(t *testing.T) {
	e := hbEngineWithLive(t, true, 0)
	e.feedSignalHeartbeatGauge(hbAnchor) // 首轮建锚 ⇒ 0（防"刚重启就报"）
	if v := mustGauge(t, "signal_zero_session_sec"); v != 0 {
		t.Fatalf("首轮建锚应为 0，got %d", v)
	}
	e.feedSignalHeartbeatGauge(hbLater)
	if v := mustGauge(t, "signal_zero_session_sec"); v != 1860 {
		t.Fatalf("盘中零信号 31 分钟应报 1860 秒，got %d", v)
	}
	// 同一锚、同一时刻，改为今日已固化 2 条 ⇒ 读数必须等值 0（不是"变小"）。
	e2 := hbEngineWithLive(t, true, 2)
	e2.hbSignalDay, e2.hbSignalAt = e.hbSignalDay, e.hbSignalAt
	e2.feedSignalHeartbeatGauge(hbLater)
	if v := mustGauge(t, "signal_zero_session_sec"); v != 0 {
		t.Fatalf("已有固化信号时必须归零（反证 pinned 判据），got %d", v)
	}
}

// TestFeedSignalHeartbeatNonLiveDoesNotWrite 反掩蔽纪律：非实盘引擎不得写 0 覆盖实盘读数。
// 先把量规置成哨兵值 1860（模拟实盘引擎刚报出的数），非实盘引擎走一轮后哨兵必须原样保留。
func TestFeedSignalHeartbeatNonLiveDoesNotWrite(t *testing.T) {
	metrics.SetGauge("signal_zero_session_sec", 1860)
	e := hbEngineWithLive(t, false, 0)
	e.feedSignalHeartbeatGauge(hbLater)
	if v := mustGauge(t, "signal_zero_session_sec"); v != 1860 {
		t.Fatalf("非实盘引擎不得改写量规（影子账号会掩掉实盘读数），got %d", v)
	}
	// 同一时刻换成盘后：实盘引擎必须写 0 收案，否则上午的读数会挂一整天冒充故障。
	metrics.SetGauge("signal_zero_session_sec", 1860)
	eLive := hbEngineWithLive(t, true, 0)
	eLive.feedSignalHeartbeatGauge(hbClosed)
	if v := mustGauge(t, "signal_zero_session_sec"); v != 0 {
		t.Fatalf("盘后必须收案写 0，got %d", v)
	}
}

// TestSignalHeartbeatRuleRegisteredAndKeyAligned 规则登记面等值锁：
// 规则名/量规键/等级/阈值/For 五项都必须等于交付真值（单向"至少存在"锁会放过去阈值漂移）。
func TestSignalHeartbeatRuleRegisteredAndKeyAligned(t *testing.T) {
	var found metrics.AlertRule
	n := 0
	for _, r := range metrics.DefaultAlertRules() {
		if r.Name == "signal_zero_in_session" {
			found = r
			n++
		}
	}
	if n != 1 {
		t.Fatalf("规则 signal_zero_in_session 命中 %d 条（应为 1，重复登记会双推）", n)
	}
	if found.Metric != "signal_zero_session_sec" {
		t.Fatalf("规则读的键 %q ≠ 写端落的键 %q（§DEADGAUGE：键名漂移＝规则恒不触发）", found.Metric, "signal_zero_session_sec")
	}
	if found.Level != "p1" || found.Op != "gt" || found.Threshold != 1800 {
		t.Fatalf("规则口径漂移：level=%s op=%s threshold=%.0f（期望 p1/gt/1800）", found.Level, found.Op, found.Threshold)
	}
	if found.For != "60s" {
		t.Fatalf("For=%q：时长已在写侧算好，这里只防单轮毛刺，不得再叠加计时义务", found.For)
	}
}

// TestRefreshStalenessFeedsSignalHeartbeat 走真实刷新通路（refreshStalenessGauges 里那次调用），
// 锁死"喂数点确实挂在每轮量规刷新上"——只直调 feed 会漏掉接线本身被删掉的形态。
// 墙上时钟在盘中/盘外两条分支都必须让这个键被写过（ok=true）才算接线在位。
func TestRefreshStalenessFeedsSignalHeartbeat(t *testing.T) {
	e := hbEngineWithLive(t, true, 0)
	e.refreshStalenessGauges()
	if _, ok := metrics.GetGauge("signal_zero_session_sec"); !ok {
		t.Fatalf("refreshStalenessGauges 未喂 %s（接线缺失）", "signal_zero_session_sec")
	}
	if data.IsActiveSession(time.Now()) && e.hbSignalDay == "" {
		t.Fatalf("盘中跑过一轮却没建心跳锚（day=%q）", e.hbSignalDay)
	}
	if !data.IsActiveSession(time.Now()) {
		if v, _ := metrics.GetGauge("signal_zero_session_sec"); v != 0 {
			t.Fatalf("盘外跑一轮必须收案为 0，got %d", v)
		}
	}
	// §0929HB-4 接线腿：同一次刷新必须也把休市日增量键喂过（缺这条＝新规则出生即死规则）。
	if _, ok := metrics.GetGauge("signal_closed_day_pinned"); !ok {
		t.Fatalf("refreshStalenessGauges 未喂 %s（§DEADGAUGE：有规则无赋值点）", "signal_closed_day_pinned")
	}
}

// hbEnginePinnedAt 造一台实盘引擎，固化信号全部带同一个 GeneratedAt（跨日回填/当天新增两种态都靠它造）。
func hbEnginePinnedAt(t *testing.T, pinned int, at time.Time) *Engine {
	t.Helper()
	e := hbEngineWithLive(t, true, pinned)
	if pinned > 0 {
		e.mu.Lock()
		for k, s := range e.signalStore.byKey {
			s.GeneratedAt = at
			e.signalStore.byKey[k] = s
		}
		e.mu.Unlock()
	}
	return e
}

// TestSignalHeartbeatSaturdayIsNotAFault 钉死 §0929HB-1 自己埋的那颗雷：
// 周六 10:31 钟点在盘中、实盘开关开着、当日零信号——旧实现会报 1860 秒并推 p1，
// 于是**每个周末都有一条假故障**（造假告警的代价是两周后真告警一起被关掉）。
// 同一时刻的休市日增量键还必须被写 0（不是"没写"）：交易日/休市日两种态都得留下读数。
func TestSignalHeartbeatSaturdayIsNotAFault(t *testing.T) {
	if data.IsTradingDay(hbSaturday) {
		t.Skipf("运行环境的日历把 %s 判成交易日（节假日日历把周六补班日认成交易日？）——本用例前提不成立", hbSaturday.Format("2006-01-02"))
	}
	metrics.SetGauge("signal_zero_session_sec", 1860) // 哨兵：模拟"上午真报过一次"
	e := hbEnginePinnedAt(t, 0, time.Time{})
	e.feedSignalHeartbeatGauge(hbSaturday.Add(-31 * time.Minute)) // 先建锚（同日首轮）
	e.feedSignalHeartbeatGauge(hbSaturday)                        // 31 分钟后第二轮
	if v, _ := metrics.GetGauge("signal_zero_session_sec"); v != 0 {
		t.Fatalf("休市日必须收案写 0（got %d）：只看钟点计时会在每个周末造一条假 p1", v)
	}
	if v, _ := metrics.GetGauge("signal_closed_day_pinned"); v != 0 {
		t.Fatalf("休市日零新增时增量键应为 0，got %d", v)
	}
	// 反证对：同一台引擎、同一休市日，只是把 2 条信号的生成时刻挪到"今天"⇒ 增量键必须变成 2。
	e2 := hbEnginePinnedAt(t, 2, hbSaturday)
	e2.feedSignalHeartbeatGauge(hbSaturday)
	if v, _ := metrics.GetGauge("signal_closed_day_pinned"); v != 2 {
		t.Fatalf("休市日当天新增 2 条必须报 2（got %d）：这条不报数＝§CAL-GATE 残留面又隐身了", v)
	}
	// 同日但落在**上一交易日**的回填（重启后从磁盘读回来的老批次）：不得算成今天新增。
	e3 := hbEnginePinnedAt(t, 3, hbFriday)
	e3.feedSignalHeartbeatGauge(hbSaturday)
	if v, _ := metrics.GetGauge("signal_closed_day_pinned"); v != 0 {
		t.Fatalf("跨日回填（GeneratedAt=上一交易日）不得冒充休市日新增，got %d", v)
	}
	// 交易日反向腿：同一批"今天生成"的信号在交易日必须让增量键写 0（交易日出信号是本分）。
	metrics.SetGauge("signal_closed_day_pinned", 9)
	e4 := hbEnginePinnedAt(t, 2, hbLater)
	e4.feedSignalHeartbeatGauge(hbLater)
	if v, _ := metrics.GetGauge("signal_closed_day_pinned"); v != 0 {
		t.Fatalf("交易日必须写 0（got %d），否则哨兵值会冒充成休市日异常", v)
	}
}

// TestSignalClosedDayRuleRegisteredAndKeyAligned 新规则登记面等值锁：名称/键名/等级/阈值/For
// 五项都等于交付真值（"至少有一条"式单向锁放得过阈值漂移）。
func TestSignalClosedDayRuleRegisteredAndKeyAligned(t *testing.T) {
	var found metrics.AlertRule
	n := 0
	for _, r := range metrics.DefaultAlertRules() {
		if r.Name == "signal_pinned_on_closed_day" {
			found = r
			n++
		}
	}
	if n != 1 {
		t.Fatalf("规则 signal_pinned_on_closed_day 命中 %d 条（应为 1，重复登记会双推）", n)
	}
	if found.Metric != "signal_closed_day_pinned" {
		t.Fatalf("规则读的键 %q ≠ 写端落的键 %q（§DEADGAUGE：键名漂移＝规则恒不触发）", found.Metric, "signal_closed_day_pinned")
	}
	if found.Level != "p2" || found.Op != "gt" || found.Threshold != 0 {
		t.Fatalf("规则口径漂移：level=%s op=%s threshold=%.0f（期望 p2/gt/0——休市日的正确读数只有一个值）", found.Level, found.Op, found.Threshold)
	}
	if found.For != "600s" {
		t.Fatalf("For=%q（期望 600s：只防一轮毛刺/重启回填竞态，不再叠加计时义务）", found.For)
	}
	routes := metrics.DefaultAlertRouting()
	if got := routes.Routes["signal_pinned_on_closed_day"]; got != metrics.RouteDaily {
		t.Fatalf("signal_pinned_on_closed_day 必须 RouteDaily（got %v）：休市日全程破线，走必推会刷满长假", got)
	}
}
