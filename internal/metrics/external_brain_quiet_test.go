// external_brain_quiet_test.go — §EXT-BRAIN-1104 外脑降噪静默域的路由锁（2026-11-04）。
//
// 判据本体：11-05 桥断之后"通道派生"的规则（uplink_stale / order_fail_rate）会整天响，
// 而默认 FireCooldown 只有 30 分钟 ⇒ 一天最多 48 推 × 两条 ⇒ 一周后没人再看，
// 真告警被训练成噪音（本仓反复登记过的同一靶子）。
//
// 这里锁的形状（四条，缺一条都可能被"顺手改成全局静默"推翻）：
//  1. 静默域内**首次仍必推**——静默不是"不报"，是"重复的转日汇总"；
//  2. 冷却窗之外（跨过一个 FireCooldown 的重复破线）不再刷第二条，而是进当日汇总；
//  3. 恢复销案一律照发——否则 announced 永远挂着，§W7-E 的成对语义就断了；
//  4. 静默按规则名逐条给，**没有全局静默**——不在静默域里的规则重复破线照常出第二条。
//
// English: routing locks for the external-brain quiet set — first push still goes out,
// repeats fold into the daily summary, resolutions always pass, and nothing outside the
// quiet set is affected (no global mute).
package metrics

import (
	"reflect"
	"testing"
	"time"
)

// quietRouterSave 保存/恢复全局路由器的相关状态（本包用例共用一台路由器，必须还原现场）。
func quietRouterSave(t *testing.T) func() {
	t.Helper()
	globalAlertRouter.mu.Lock()
	saved := struct {
		quiet      map[string]bool
		announced  map[string]bool
		lastF      map[string]time.Time
		lastR      map[string]time.Time
		pendingR   map[string]AlertEvent
		suppressed map[string]int
		daily      map[string]*dailyStat
		day        string
		rules      map[string]AlertRule
		cfg        AlertRoutingConfig
		now        func() time.Time
		statePath  string
	}{globalAlertRouter.quiet, globalAlertRouter.announced, globalAlertRouter.lastF,
		globalAlertRouter.lastR, globalAlertRouter.pendingR, globalAlertRouter.suppressed,
		globalAlertRouter.daily, globalAlertRouter.day, globalAlertRouter.rules,
		globalAlertRouter.cfg, globalAlertRouter.now, globalAlertRouter.statePath}
	// 换成干净的账本，避免上一用例的 announced/daily 串味。
	globalAlertRouter.quiet = map[string]bool{}
	globalAlertRouter.announced = map[string]bool{}
	globalAlertRouter.lastF = map[string]time.Time{}
	globalAlertRouter.lastR = map[string]time.Time{}
	globalAlertRouter.pendingR = map[string]AlertEvent{}
	globalAlertRouter.suppressed = map[string]int{}
	globalAlertRouter.daily = map[string]*dailyStat{}
	globalAlertRouter.day = ""
	globalAlertRouter.statePath = "" // 用例不落盘
	timestamp := time.Date(2026, 11, 4, 10, 0, 0, 0, time.Local)
	globalAlertRouter.now = func() time.Time { return timestamp }
	globalAlertRouter.mu.Unlock()
	_ = saved // 下面返回值里统一还原
	return func() {
		globalAlertRouter.mu.Lock()
		globalAlertRouter.quiet = saved.quiet
		globalAlertRouter.announced = saved.announced
		globalAlertRouter.lastF = saved.lastF
		globalAlertRouter.lastR = saved.lastR
		globalAlertRouter.pendingR = saved.pendingR
		globalAlertRouter.suppressed = saved.suppressed
		globalAlertRouter.daily = saved.daily
		globalAlertRouter.day = saved.day
		globalAlertRouter.rules = saved.rules
		globalAlertRouter.cfg = saved.cfg
		globalAlertRouter.now = saved.now
		globalAlertRouter.statePath = saved.statePath
		globalAlertRouter.mu.Unlock()
	}
}

// quietClockAt 把时钟钉到绝对时刻。
func quietClockAt(ts time.Time) {
	globalAlertRouter.mu.Lock()
	globalAlertRouter.now = func() time.Time { return ts }
	globalAlertRouter.mu.Unlock()
}

func quietFire(name string) AlertEvent {
	return AlertEvent{Name: name, Level: "p1", Kind: "fire", Value: 900, Message: "测试事件 " + name}
}

// TestQuietRulesFirstPushStillGoesOut 静默域内的第一条**必须照推**：
// 静默的语义是"重复的转日汇总"，不是"不报"。
func TestQuietRulesFirstPushStillGoesOut(t *testing.T) {
	restore := quietRouterSave(t)
	defer restore()
	QuietAlertRules([]string{"uplink_stale"})
	quietClockAt(time.Date(2026, 11, 4, 9, 30, 0, 0, time.Local))
	out := globalAlertRouter.Route([]AlertEvent{quietFire("uplink_stale")})
	if len(out) != 1 || out[0].Kind != "alert" {
		t.Fatalf("静默域内首推应出站 1 条，实得 %+v", out)
	}
}

// TestQuietRulesRepeatFoldsIntoDaily 跨过一个冷却窗后的重复破线：不再刷第二条，进当日汇总。
// 反证：摘掉 routeEventsLocked 里的静默分支 ⇒ 这里收到第二条 alert。
func TestQuietRulesRepeatFoldsIntoDaily(t *testing.T) {
	restore := quietRouterSave(t)
	defer restore()
	QuietAlertRules([]string{"uplink_stale"})
	base := time.Date(2026, 11, 4, 9, 30, 0, 0, time.Local)
	quietClockAt(base)
	if out := globalAlertRouter.Route([]AlertEvent{quietFire("uplink_stale")}); len(out) != 1 {
		t.Fatalf("前置条件：首推应出站，实得 %+v", out)
	}
	// 已过默认 30 分钟冷却窗（两小时）——若没有静默域，这条会出第二条 push。
	quietClockAt(base.Add(2 * time.Hour))
	out := globalAlertRouter.Route([]AlertEvent{quietFire("uplink_stale")})
	for _, d := range out {
		if d.Kind == "alert" && d.Rule == "uplink_stale" {
			t.Fatalf("静默域内跨冷却窗仍刷第二条必推：%+v", out)
		}
	}
	globalAlertRouter.mu.Lock()
	st := globalAlertRouter.daily["uplink_stale"]
	globalAlertRouter.mu.Unlock()
	// 计数＝被折叠掉的重复破线条数（首推走必推、不进汇总，所以这里是 1 而不是 2）。
	if st == nil || st.Count != 1 {
		t.Fatalf("重复破线应进当日汇总并计一条，实得 %+v", st)
	}
}

// TestQuietRulesRecoveryAlwaysPasses 恢复销案不受静默影响（成对语义，§W7-E）。
func TestQuietRulesRecoveryAlwaysPasses(t *testing.T) {
	restore := quietRouterSave(t)
	defer restore()
	QuietAlertRules([]string{"uplink_stale"})
	base := time.Date(2026, 11, 4, 9, 30, 0, 0, time.Local)
	quietClockAt(base)
	globalAlertRouter.Route([]AlertEvent{quietFire("uplink_stale")})
	quietClockAt(base.Add(40 * time.Minute))
	out := globalAlertRouter.Route([]AlertEvent{{Name: "uplink_stale", Level: "p1", Kind: "recover",
		Value: 0, Message: "测试事件 uplink_stale"}})
	var resolved int
	for _, d := range out {
		if d.Kind == "resolved" && d.Rule == "uplink_stale" {
			resolved++
		}
	}
	if resolved != 1 {
		t.Fatalf("销案必须照发（否则 announced 永远挂着），实得 %+v", out)
	}
}

// TestQuietRulesDoNotMuteOthers 对照腿：不在静默域里的规则跨冷却窗仍出第二条必推。
// 这条防的是"顺手改成全局静默"——那会把无关规则的首推也吞掉。
func TestQuietRulesDoNotMuteOthers(t *testing.T) {
	restore := quietRouterSave(t)
	defer restore()
	QuietAlertRules([]string{"uplink_stale"})
	base := time.Date(2026, 11, 4, 9, 30, 0, 0, time.Local)
	quietClockAt(base)
	globalAlertRouter.Route([]AlertEvent{quietFire("order_fail_rate")})
	quietClockAt(base.Add(2 * time.Hour))
	out := globalAlertRouter.Route([]AlertEvent{quietFire("order_fail_rate")})
	var alerts int
	for _, d := range out {
		if d.Kind == "alert" && d.Rule == "order_fail_rate" {
			alerts++
		}
	}
	if alerts != 1 {
		t.Fatalf("非静默规则的重复破线应照常出第二条，实得 %+v", out)
	}
}

// TestQuietAlertRulesRoundTrip 读写面：整份替换 + 排序回读 + nil 清空（引擎每轮按档位重算，
// 不是累加——否则通道恢复后静默域永远留着，规则又变成永不响的摆设）。
func TestQuietAlertRulesRoundTrip(t *testing.T) {
	restore := quietRouterSave(t)
	defer restore()
	QuietAlertRules([]string{"order_fail_rate", " ", "uplink_stale"})
	if got := QuietAlertRuleNames(); !reflect.DeepEqual(got, []string{"order_fail_rate", "uplink_stale"}) {
		t.Fatalf("应整份替换并丢掉空白项，实得 %v", got)
	}
	QuietAlertRules([]string{"uplink_stale"})
	if got := QuietAlertRuleNames(); !reflect.DeepEqual(got, []string{"uplink_stale"}) {
		t.Fatalf("第二次调用应覆盖而不是累加，实得 %v", got)
	}
	QuietAlertRules(nil)
	if got := QuietAlertRuleNames(); len(got) != 0 {
		t.Fatalf("nil 应清空静默域，实得 %v", got)
	}
}
