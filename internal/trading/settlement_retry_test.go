// settlement_retry_test.go — §D4（2026-09-22 修复批）日终三方对账「失败当日可重试」单测。
//
// 缺陷原文：MaybeSettleDay 把 `c.lastSettleDay = day` 放在调用 SettleDay **之前**，
// 而顶部的「同日已对账即跳过」判定读的就是这个字段——于是一次网关超时/落库报错就把当日
// 唯一一次对账机会永久烧掉（失败分支只有 log + metrics.SettleFailed() + opslog，无补偿路径）。
// 日终三方对账是"首尔账本 ↔ 广州柜台"的唯一全量核对网，跳过一天意味着账本漂移要累积到次日。
//
// 本测试锁住修好后的四条语义：
//  1. 失败**不**置 lastSettleDay：把 retry 窗口放开（settleRetryInterval=0）后同日可再次触发；
//  2. 节流真实存在：窗口内不得立刻重投（防 scoreCycle 每 60s 打爆网关变成死循环）；
//  3. 成功后置位：同日不再重复对账；
//  4. 跨日不受影响：换一天照常对账。
//
// English: §D4 — a failed settlement must stay retryable the same day (throttled), while success
// marks the day done and stops further runs.
package trading

import (
	"errors"
	"testing"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/metrics"
)

// settleAtNowBeijing 构造"已经过对账时刻"的 settle_at 入参（北京时的 HHMM 当前分钟）。
// MaybeSettleDay 的门控是 `now < settleAt` 才跳过，取"当前分钟"即恰好放行；
// 唯一构造不出来的时刻是北京时 00:00（0 会被当成未配置并回落默认 15:30），该分钟直接跳过用例。
func settleAtNowBeijing(t *testing.T) int {
	t.Helper()
	now := cntime.In(time.Now())
	if now.Hour() == 0 && now.Minute() == 0 {
		t.Skip("北京时 00:00 无法构造「已过 settle_at」入参（0 会回落默认 15:30），跨分钟即恢复")
	}
	return now.Hour()*100 + now.Minute()
}

// flakySettleExecutor 交割单源桩：前 failTimes 次抛错，之后成功（并统计被触达次数）。
type flakySettleExecutor struct {
	guardStub
	src       *mockSettle
	failTimes int
	calls     int
}

// FetchSettlement 桩：按预置失败次数决定这次对账是失败还是成功返回空交割单。
func (f *flakySettleExecutor) FetchSettlement(date string) (*SettlementResponse, error) {
	f.calls++
	f.src.gotDate = date
	if f.calls <= f.failTimes {
		return nil, errors.New("stub: 网关超时")
	}
	return &SettlementResponse{Date: date, Connected: true}, nil
}

// TestSettleFailureRetryableSameDay §D4 主用例：失败当日不置位、节流窗口内不重投、
// 窗口放开后可再试、成功后当日封盘。
func TestSettleFailureRetryableSameDay(t *testing.T) {
	// 单测把 retry 窗口设为 0（"只要不是刚试过就能再试"），断言完成后必须恢复包级变量，
	// 否则会污染同包其它用例（这是唯一能在一秒内跑完"跨窗口重试"的做法）。
	orig := settleRetryInterval
	settleRetryInterval = 0
	t.Cleanup(func() { settleRetryInterval = orig })

	db := testDB(t)
	cfg := config.DefaultQMTConfig()
	cfg.Enabled = true
	exec := &flakySettleExecutor{src: &mockSettle{}, failTimes: 1} // 仅首次失败，其后成功
	ctrl := NewController(exec, db, "u_st", cfg, nil)
	day := "2026-09-08"
	at := settleAtNowBeijing(t)

	// ① 第一次尝试：桩抛错 → 当日**不得**被记为"已对账"
	ctrl.MaybeSettleDay(day, SettleModeReportOnly, at, true)
	if exec.calls != 1 {
		t.Fatalf("首次应触达交割单源一次, got %d", exec.calls)
	}
	ctrl.mu.RLock()
	doneAfterFail := ctrl.lastSettleDay
	ctrl.mu.RUnlock()
	if doneAfterFail == day {
		t.Fatalf("§D4 反例：对账失败却把 %s 记成已对账（旧缺陷=当日永不再试）", day)
	}
	// §N-1 配对断言：失败必须在**生产代码**里把告警量规抬起来（alerter 规则要读它）。
	// 旧教训是规则表定义了指标、生产侧零赋值 → 规则永不触发的死规则，这里从被消费侧锁死。
	if v, ok := metrics.GetGauge("settle_fail_streak"); !ok || v != 1 {
		t.Fatalf("对账失败应把 settle_fail_streak 抬到 1, got %v ok=%v", v, ok)
	}

	// ② 节流仍在：把窗口恢复成一个远超测试耗时的值，模拟"距上次尝试不足 10 分钟"
	settleRetryInterval = time.Hour
	ctrl.MaybeSettleDay(day, SettleModeReportOnly, at, true)
	settleRetryInterval = 0
	if exec.calls != 1 {
		t.Fatalf("节流窗口内不得重投（防每轮评分死循环打爆网关）, calls=%d", exec.calls)
	}

	// ③ 窗口放开（=已过 10 分钟）→ 同日第二次尝试成功 → 此后当日封盘
	ctrl.MaybeSettleDay(day, SettleModeReportOnly, at, true)
	if exec.calls != 2 {
		t.Fatalf("失败当日过窗后必须再试一次, calls=%d", exec.calls)
	}
	ctrl.mu.RLock()
	doneAfterOK := ctrl.lastSettleDay
	failCount := ctrl.settleFailCount
	failDay := ctrl.settleFailDay
	ctrl.mu.RUnlock()
	if doneAfterOK != day {
		t.Fatalf("成功当日应记为已对账, got %q", doneAfterOK)
	}
	// 成功即清零当日失败计数（留痕已进 opslog，计数只服务"当前是否仍处故障"）
	if failCount != 0 {
		t.Fatalf("成功后当日失败计数应清零, got %d（day=%s）", failCount, failDay)
	}
	// §N-1 配对断言（恢复侧）：成功后告警量规必须归零，alerter 的 settle_failed 规则才发得出 recover
	if v, _ := metrics.GetGauge("settle_fail_streak"); v != 0 {
		t.Fatalf("对账成功后 settle_fail_streak 应归零, got %d", v)
	}
	ctrl.MaybeSettleDay(day, SettleModeReportOnly, at, true)
	ctrl.MaybeSettleDay(day, SettleModeReportOnly, at, true)
	if exec.calls != 2 {
		t.Fatalf("成功后同日不得再对账, calls=%d", exec.calls)
	}

	// ④ 次日（另一交易日）不受影响
	ctrl.MaybeSettleDay("2026-09-09", SettleModeReportOnly, at, true)
	if exec.calls != 3 {
		t.Fatalf("次日应正常对账一次, calls=%d", exec.calls)
	}
}

// TestSettleFailureCountedPerDay §D4：连续失败要"当日可数"——计数字段按日聚合、跨日重置，
// 让 opslog 里的「当日第 N 次」能区分偶发抖动与系统性故障。
func TestSettleFailureCountedPerDay(t *testing.T) {
	orig := settleRetryInterval
	settleRetryInterval = 0
	t.Cleanup(func() { settleRetryInterval = orig })

	db := testDB(t)
	cfg := config.DefaultQMTConfig()
	cfg.Enabled = true
	exec := &flakySettleExecutor{src: &mockSettle{}, failTimes: 99} // 永远失败
	ctrl := NewController(exec, db, "u_st", cfg, nil)
	at := settleAtNowBeijing(t)

	for i := 1; i <= 3; i++ {
		ctrl.MaybeSettleDay("2026-09-08", SettleModeReportOnly, at, true)
		ctrl.mu.RLock()
		gotDay, gotCount := ctrl.settleFailDay, ctrl.settleFailCount
		ctrl.mu.RUnlock()
		if gotDay != "2026-09-08" || gotCount != i {
			t.Fatalf("第 %d 次失败后当日计数应为 %d, got day=%q count=%d", i, i, gotDay, gotCount)
		}
	}
	// 换一天：计数归属新交易日重新起算（旧日计数不得串账）
	ctrl.MaybeSettleDay("2026-09-09", SettleModeReportOnly, at, true)
	ctrl.mu.RLock()
	gotDay, gotCount := ctrl.settleFailDay, ctrl.settleFailCount
	ctrl.mu.RUnlock()
	if gotDay != "2026-09-09" || gotCount != 1 {
		t.Fatalf("跨日失败计数应重置, got day=%q count=%d", gotDay, gotCount)
	}
}

// TestSettleDisabledAndUntrustedInputsSkipMaybeSettle §D4 边界：enabled=false / 未到对账时刻 /
// 未启用实盘一律不动账（新增的尝试戳与计数不能被这些早退分支污染）。
func TestSettleDisabledAndUntrustedInputsSkipMaybeSettle(t *testing.T) {
	db := testDB(t)
	cfg := config.DefaultQMTConfig()
	cfg.Enabled = true
	exec := &flakySettleExecutor{src: &mockSettle{}}
	ctrl := NewController(exec, db, "u_st", cfg, nil)

	ctrl.MaybeSettleDay("2026-09-08", SettleModeReportOnly, 1530, false) // enabled=false
	ctrl.MaybeSettleDay("2026-09-08", SettleModeReportOnly, 2359, true)  // 未到对账时刻（除非北京时间 23:59）
	if at := cntime.In(time.Now()); at.Hour()*100+at.Minute() >= 2359 {
		t.Skip("北京时 23:59 无法构造「未到对账时刻」")
	}
	if exec.calls != 0 {
		t.Fatalf("早退分支不得触达交割单源, calls=%d", exec.calls)
	}
	ctrl.mu.RLock()
	stamped := !ctrl.lastSettleAttemptAt.IsZero()
	ctrl.mu.RUnlock()
	if stamped {
		t.Fatal("早退分支不得推进尝试戳（会白占一个 retry 窗口）")
	}
}
