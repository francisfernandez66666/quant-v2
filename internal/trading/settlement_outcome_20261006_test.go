// settlement_outcome_20261006_test.go — §P2-E（2026-10-06 修复批 波 5）对账三态的**记账行为锁**。
//
// 缺陷原文（docs/AUDIT_20261005 报告 P2-E）：MaybeSettleDay 只把 `err != nil` 当失败，
// 而 SettleDay 在"执行器不支持交割单"与"网关未连接"两条分支返回 (nil, nil)——
// 于是这两条**什么都没比对**的出口一起走了"成功"那条记账路：
//
//	c.lastSettleDay = day（当日封盘，之后一整天不再试）
//	metrics.SetGauge("settle_fail_streak", 0)（向告警面宣布"已恢复"）
//	settlement_diff_count = 0（与"对完了、确实没有差异"共用同一个读数）
//
// 日终三方对账是账本漂移的唯一全量核对网，这套语义下它在最该响的那天表现为"今天对过了"。
//
// 本文件钉的是改好后的四条语义（全部从**被消费侧**断，不重复断字符串）：
//  1. 未连接腿：lastSettleDay 不推进、失败 streak 不归零、settlement_state=2、每个 retry 窗口真试一次；
//  2. 结构性不支持腿：同样不推进、settlement_state=3，但当日只留痕一次（不刷屏）；
//  3. 短路判据是"日 + 原因"两个条件（只比日期会把后来的恢复挡在门外）；
//  4. 真验证腿：推进 lastSettleDay、streak 归零、settlement_state=1、跳过戳清空。
//
// English: §P2-E behavior locks — a skipped reconciliation must not book the day, must not reset
// the failure streak, and must publish a distinct settlement_state per skip reason; only a real
// three-way comparison may book the day.
package trading

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/metrics"
	"quant-trading-v2/internal/opslog"
)

// countingOfflineSettleExecutor §P2-E 用例桩：实现 SettlementFetcher，但每轮都回 Connected=false，
// 并自带被触达次数（断言"过窗必须再试一次"要知道的就是次数）。
// 刻意不复用包内 mockSettle：它没有计数器，而跨用例共用一个"整包累计次数"会让
// "本条用例试了几次"这种判据读到别人的账（同族教训见 §P1-A 键空间同源）。
type countingOfflineSettleExecutor struct {
	guardStub
	calls int
	date  string
}

// FetchSettlement 桩：永远"未连接"，但确实被调用到了（这正是与"根本没重试"的区别）。
func (e *countingOfflineSettleExecutor) FetchSettlement(date string) (*SettlementResponse, error) {
	e.calls++
	e.date = date
	return &SettlementResponse{Date: date, Connected: false}, nil
}

// readOpslogAll 把临时 opslog 目录里的行拼成一整串供用例检索。
// 刻意不做"最后一行"式的取巧：跨日清理、多标签交错都会让"取末行"读到别的东西。
func readOpslogAll(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读不到 opslog 临时目录 %s: %v（留痕断言必须真落到文件，不是只打了 stderr）", dir, err)
	}
	var sb strings.Builder
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, rerr := os.ReadFile(filepath.Join(dir, e.Name()))
		if rerr != nil {
			t.Fatalf("读 opslog 文件 %s: %v", e.Name(), rerr)
		}
		sb.Write(b)
	}
	return sb.String()
}

// captureOpslog 把 opslog 重定向到临时目录并返回该目录（与仓内既有取数手法同形）。
func captureOpslog(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "opslog")
	opslog.Init(dir, 0)
	return dir
}

// TestMaybeSettleDayNotConnectedDoesNotBookTheDay J1：网关未连接＝本轮未验证 ⇒ 当日**不记账**、
// 失败 streak **不归零**、量规给出专属读数，且每个 retry 窗口都真去试一次。
func TestMaybeSettleDayNotConnectedDoesNotBookTheDay(t *testing.T) {
	orig := settleRetryInterval
	settleRetryInterval = 0
	t.Cleanup(func() { settleRetryInterval = orig })

	logDir := captureOpslog(t)
	db := testDB(t)
	cfg := config.DefaultQMTConfig()
	cfg.Enabled = true
	exec := &countingOfflineSettleExecutor{}
	ctrl := NewController(exec, db, "u_st_p2e", cfg, nil)
	at := settleAtNowBeijing(t)
	day := "2026-09-08"

	// 基线：先让当日处于"上一轮真失败"的状态（streak=2）。跳过腿若把它归零，等于向告警面
	// 宣布"对账已恢复"，而这一轮什么都没比对——那正是本条缺陷的后半段。
	metrics.SetGauge("settle_fail_streak", 2)

	ctrl.MaybeSettleDay(day, SettleModeReportOnly, at, true)
	if exec.calls == 0 {
		t.Fatalf("未连接腿必须真触达交割单源（不触达就无法在恢复那轮验证）")
	}
	ctrl.mu.RLock()
	booked := ctrl.lastSettleDay
	skipDay := ctrl.lastSettleSkipDay
	skipOutcome := ctrl.lastSettleSkipOutcome
	ctrl.mu.RUnlock()
	if booked == day {
		t.Fatalf("§P2-E 反例：本轮未验证（网关未连接）却把 %s 记成已对账＝当日封盘，差异永远不会被发现", day)
	}
	if skipDay != day || skipOutcome != SettleOutcomeSkippedNotConnected {
		t.Fatalf("跳过戳应记下当日该原因，got day=%q outcome=%s", skipDay, skipOutcome)
	}
	if v, _ := metrics.GetGauge("settle_fail_streak"); v != 2 {
		t.Fatalf("跳过腿不得把 settle_fail_streak 归零（归零＝发 recover），got %d", v)
	}
	if v, ok := metrics.GetGauge("settlement_state"); !ok || v != 2 {
		t.Fatalf("网关未连接应写 settlement_state=2，got %v ok=%v", v, ok)
	}
	// 未连接腿**不短路**：过了 retry 窗口必须再试（结构性腿才短路，见下一条用例）。
	before := exec.calls
	ctrl.MaybeSettleDay(day, SettleModeReportOnly, at, true)
	if exec.calls != before+1 {
		t.Fatalf("未连接属瞬时状态，过窗必须再试一次, calls %d→%d", before, exec.calls)
	}
	// 留痕可查（写进 opslog 文件而不是只有一句 log.Printf），且文案带的是三态原文。
	journal := readOpslogAll(t, logDir)
	if !strings.Contains(journal, "skipped-not-verified:gateway_not_connected") {
		t.Fatalf("opslog 未出现跳过留痕（三态原文）：\n%s", journal)
	}
	if !strings.Contains(journal, "当日不记为已对账") {
		t.Fatalf("留痕必须把「不记账」这件事说出口（否则运维读到的仍是「今天对过了」）：\n%s", journal)
	}
}

// TestMaybeSettleDayUnsupportedShortCircuitsOncePerDay J1b：执行器结构性不支持（Noop/桩）⇒
// 不记账、专属读数 3、**同一原因当日只试一次/只留痕一次**（不刷屏），且留痕不隐身。
func TestMaybeSettleDayUnsupportedShortCircuitsOncePerDay(t *testing.T) {
	orig := settleRetryInterval
	settleRetryInterval = 0
	t.Cleanup(func() { settleRetryInterval = orig })

	logDir := captureOpslog(t)
	db := testDB(t)
	cfg := config.DefaultQMTConfig()
	cfg.Enabled = true
	// guardStub 不实现 SettlementFetcher ⇒ SettleDay 走结构性不支持腿
	ctrl := NewController(&guardStub{}, db, "u_st_p2e2", cfg, nil)
	at := settleAtNowBeijing(t)
	day := "2026-09-08"

	ctrl.MaybeSettleDay(day, SettleModeReportOnly, at, true)
	if v, ok := metrics.GetGauge("settlement_state"); !ok || v != 3 {
		t.Fatalf("执行器不支持应写 settlement_state=3，got %v ok=%v", v, ok)
	}
	ctrl.mu.RLock()
	booked := ctrl.lastSettleDay
	stamp := ctrl.lastSettleAttemptAt
	ctrl.mu.RUnlock()
	if booked == day {
		t.Fatalf("§P2-E 反例：结构性不适用被记成已对账")
	}
	if stamp.IsZero() {
		t.Fatalf("首次尝试应推进尝试戳（否则每 60s 的评分轮会把它当「从未试过」，白占一个 retry 窗口的护栏就失效了）")
	}
	// 再喊三次：当日短路，一次都不该再动（尝试戳不变 = 连 SettleDay 都没进）。
	for i := 0; i < 3; i++ {
		ctrl.MaybeSettleDay(day, SettleModeReportOnly, at, true)
	}
	ctrl.mu.RLock()
	stampAfter := ctrl.lastSettleAttemptAt
	ctrl.mu.RUnlock()
	if !stampAfter.Equal(stamp) {
		t.Fatalf("结构性不支持当日应短路（尝试戳不得再推进）：%v → %v", stamp, stampAfter)
	}
	journal := readOpslogAll(t, logDir)
	if n := strings.Count(journal, "skipped-not-verified:executor_unsupported"); n != 1 {
		t.Fatalf("同一结构性原因当日只留痕一次，got %d 次（0＝运维看不见；>1＝十分钟一条把日志刷满，§CAL-GATE 判例）\n%s", n, journal)
	}
}

// TestSettleSkipShortCircuitIsReasonAndDayScoped J1c：短路判据的两个条件各自都必须在位。
// 只比日期 ⇒ 先发生 NoFetcher、后来网关从不连变成已连（或执行器换过）时，当日真能对账的窗口
// 被旧原因永久挡死（本仓「派生状态不是来源」同族：判据不能只看一个派生戳）。
// English: the short-circuit requires BOTH the same day and the same structural reason.
func TestSettleSkipShortCircuitIsReasonAndDayScoped(t *testing.T) {
	db := testDB(t)
	cfg := configDefault()
	cfg.Enabled = true
	ctrl := NewController(&guardStub{}, db, "u_st_p2e3", cfg, nil)
	day := "2026-09-08"

	if ctrl.settleSkipShortCircuits(day) {
		t.Fatalf("空桩状态就该短路＝任何一次跳过之前就已经不试了，方向错")
	}
	ctrl.recordSettleSkip(day, SettleOutcomeSkippedNoFetcher)
	if !ctrl.settleSkipShortCircuits(day) {
		t.Fatalf("当日同结构性原因应短路（不短路会按十分钟一条刷满日志）")
	}
	if ctrl.settleSkipShortCircuits("2026-09-09") {
		t.Fatalf("换一天不得沿用短路（结构性判断必须按日重新确认一次）")
	}
	// 原因换成"未连接"：同一天的短路必须失效，让后面真能跑的窗口有机会跑。
	ctrl.recordSettleSkip(day, SettleOutcomeSkippedNotConnected)
	if ctrl.settleSkipShortCircuits(day) {
		t.Fatalf("原因已变（不再是结构性不支持）却仍短路＝把恢复挡在门外")
	}
	// 零值占位同样不得短路：新增返回位忘置值时必须显式暴露，而不是顺带落进某个既有分支。
	ctrl.recordSettleSkip(day, SettleOutcomeUnknown)
	if ctrl.settleSkipShortCircuits(day) {
		t.Fatalf("零值结论不得短路")
	}
	// 第三条判据（**此刻**执行器是否仍不支持）必须真的参与判定：现网 executor 会在交易时段被
	// ApplyPendingConfig 换装（§FIX#7），换成支持交割单的执行器之后，当日旧戳不得继续挡窗口——
	// 只读派生戳的写法在这里会给出"今天已经不试了"的假象，而事实是今天已经能对了。
	supported := NewController(&settleExecutor{src: &mockSettle{resp: &SettlementResponse{
		Date: day, Connected: true,
	}}}, db, "u_st_p2e3b", cfg, nil)
	supported.recordSettleSkip(day, SettleOutcomeSkippedNoFetcher)
	if supported.settleSkipShortCircuits(day) {
		t.Fatalf("执行器现在已支持交割单，却仍按旧戳短路＝用旧读数否决新事实（派生状态不是来源）")
	}
	if SettleOutcomeUnknown.Verified() || SettleOutcomeUnknown.String() != "unknown" {
		t.Fatalf("零值必须是可识别的 unknown 且绝不 Verified，got %q", SettleOutcomeUnknown.String())
	}
	// 三态读数互不相同且与"未跑过"不同：0 未知 / 1 已对账 / 2 未连接 / 3 不支持。
	// 断**映射本身**（而不是"两个数不相等"），因为规则用的是 `ge 2`：阈值一侧的顺序也必须是
	// 已对账(1) < 破线(2/3)，写成 5/9/1 之类同样互不相同却会把破线态放在阈值之下。
	want := map[SettleOutcome]int64{
		SettleOutcomeUnknown:             0,
		SettleOutcomeVerified:            1,
		SettleOutcomeSkippedNotConnected: 2,
		SettleOutcomeSkippedNoFetcher:    3,
	}
	seen := map[int64]string{}
	for o, exp := range want {
		v := o.gaugeValue()
		if v != exp {
			t.Fatalf("%s 的量规读数应=%d（规则 settlement_not_verified 用 ge 2 判破线，映射一改判据就落到阈值之下），got %d", o, exp, v)
		}
		if prev, dup := seen[v]; dup {
			t.Fatalf("两种态共用同一个量规读数 %d（%s 与 %s）＝运维面又看不出区别了，这正是本条缺陷的原形", v, prev, o)
		}
		seen[v] = o.String()
	}
}

// TestMaybeSettleDayVerifiedBooksDayAndClearsSkipStamps J1d：真验证那轮才记账，并清空跳过戳
// （旧原因不得把之后的窗口挡在门外）。
func TestMaybeSettleDayVerifiedBooksDayAndClearsSkipStamps(t *testing.T) {
	orig := settleRetryInterval
	settleRetryInterval = 0
	t.Cleanup(func() { settleRetryInterval = orig })

	db := testDB(t)
	cfg := config.DefaultQMTConfig()
	cfg.Enabled = true
	exec := &flakySettleExecutor{src: &mockSettle{}, failTimes: 0}
	ctrl := NewController(exec, db, "u_st_p2e4", cfg, nil)
	at := settleAtNowBeijing(t)
	day := "2026-09-08"

	// 先人为压一个结构性跳过戳（模拟"今天早些时候执行器还不支持"）。
	ctrl.recordSettleSkip(day, SettleOutcomeSkippedNoFetcher)
	ctrl.MaybeSettleDay(day, SettleModeReportOnly, at, true)
	if exec.calls == 0 {
		t.Fatalf("已有当日跳过戳时，本应仍有机会真对一次账（原因导向的短路判据失效才算修对）")
	}
	ctrl.mu.RLock()
	booked := ctrl.lastSettleDay
	skipDay := ctrl.lastSettleSkipDay
	skipOutcome := ctrl.lastSettleSkipOutcome
	ctrl.mu.RUnlock()
	if booked != day {
		t.Fatalf("真对完账应记为已对账, got %q", booked)
	}
	if skipDay != "" || skipOutcome != SettleOutcomeUnknown {
		t.Fatalf("成功后必须清空跳过戳（残留会把后续窗口挡在门外）, got day=%q outcome=%s", skipDay, skipOutcome)
	}
	if v, _ := metrics.GetGauge("settlement_state"); v != 1 {
		t.Fatalf("已对账应写 settlement_state=1，got %d", v)
	}
	// 记账之后当日封盘：再喊不动它（§D4 的成功语义不因三态改造而松动）。
	calls := exec.calls
	ctrl.MaybeSettleDay(day, SettleModeReportOnly, at, true)
	if exec.calls != calls {
		t.Fatalf("成功后同日不得再对账, calls %d→%d", calls, exec.calls)
	}
	// 跨日：换一天必须重新试（时间戳是 0 值或旧日，都不得阻止新交易日）。
	if at := time.Now(); at.IsZero() {
		t.Skip("时钟异常")
	}
	ctrl.MaybeSettleDay("2026-09-09", SettleModeReportOnly, at, true)
	if exec.calls != calls+1 {
		t.Fatalf("次日应正常对账一次, calls=%d", exec.calls)
	}
}
