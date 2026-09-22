// order_rate_test.go — §DEADGAUGE（2026-09-23 傍晚批收尾）窗口失败率换算的行为锁。
// 三条口径（文件头写明）逐条钉住：空窗写 0、窗内增量按千分比算、计数器回退重新起窗。
// 时钟走 orderRateNow 接缝拨时间，不用 sleep。
//
// English: behavior locks for the §DEADGAUGE order-failure-rate window: empty window reads 0,
// in-window counter deltas yield the per-mille rate, and a counter reset re-baselines the window.
// Time is driven through the orderRateNow seam (no sleeps).
package metrics

import (
	"testing"
	"time"
)

// withFakeClock 把 orderRateNow 换成可控时钟，用例结束自动复原（连同两个累计计数器与样本环，
// 避免用例间通过包级原子量互相污染）。
func withFakeClock(t *testing.T, start time.Time) func(time.Duration) {
	t.Helper()
	now := start
	prevClock := orderRateNow
	prevPlaced, prevRejected := ordersPlaced.Load(), ordersRejected.Load()
	orderRateNow = func() time.Time { return now }
	resetOrderRateWindow()
	t.Cleanup(func() {
		orderRateNow = prevClock
		ordersPlaced.Store(prevPlaced)
		ordersRejected.Store(prevRejected)
		resetOrderRateWindow()
	})
	return func(d time.Duration) { now = now.Add(d) }
}

// TestOrderFailRateEmptyWindowIsZero 无任何下单尝试（含"只有基端一个样本"）→ 0，gt 规则恒不触发。
func TestOrderFailRateEmptyWindowIsZero(t *testing.T) {
	advance := withFakeClock(t, time.Unix(1760000000, 0))
	if got := refreshOrderFailRateGauge(); got != 0 {
		t.Fatalf("单样本不构成窗口，应 0, got %d", got)
	}
	advance(30 * time.Second)
	if got := refreshOrderFailRateGauge(); got != 0 {
		t.Fatalf("窗内无尝试应 0, got %d", got)
	}
	if v, ok := GetGauge("order_fail_rate_milli"); !ok || v != 0 {
		t.Fatalf("量规必须被写入且为 0（赋值点存在性），got v=%d ok=%v", v, ok)
	}
}

// TestOrderFailRateWindowedDelta 失败率只按窗内增量算：历史累计里的旧失败不得污染当前窗口。
func TestOrderFailRateWindowedDelta(t *testing.T) {
	advance := withFakeClock(t, time.Unix(1760000000, 0))
	// t0：先埋一段"历史上的"高失败率（3 拒 1 成），随后推进超过 5 分钟窗
	ordersPlaced.Add(1)
	ordersRejected.Add(3)
	refreshOrderFailRateGauge()
	advance(orderFailRateWindow + time.Minute)
	refreshOrderFailRateGauge() // 基端样本（旧失败已滑出窗口）

	// 窗口内：10 成 1 拒 → 1/11 ≈ 90‰（规则阈值 50‰ ⇒ 触发）
	ordersPlaced.Add(10)
	ordersRejected.Add(1)
	advance(30 * time.Second)
	got := refreshOrderFailRateGauge()
	if got != 90 {
		t.Fatalf("窗内 10 成 1 拒应为 90‰, got %d", got)
	}
	if got <= 50 {
		t.Fatalf("用例本身失效：90‰ 必须高于规则阈值 50")
	}
}

// TestOrderFailRateBelowThreshold 全成功窗口 → 0‰（不伪造失败率）。
func TestOrderFailRateBelowThreshold(t *testing.T) {
	advance := withFakeClock(t, time.Unix(1760000000, 0))
	ordersPlaced.Add(4)
	refreshOrderFailRateGauge()
	advance(30 * time.Second)
	ordersPlaced.Add(6)
	if got := refreshOrderFailRateGauge(); got != 0 {
		t.Fatalf("窗内零失败应为 0‰, got %d", got)
	}
}

// TestOrderFailRateCounterReset 进程重启后累计原子量归零：必须丢弃历史端点重新起窗，
// 绝不能用「负增量」算出一个假的 100% 失败率（那会把一次重启变成 p1 告警）。
func TestOrderFailRateCounterReset(t *testing.T) {
	advance := withFakeClock(t, time.Unix(1760000000, 0))
	ordersPlaced.Add(20)
	ordersRejected.Add(2)
	refreshOrderFailRateGauge()
	advance(30 * time.Second)
	// 模拟重启：计数器回到小值
	ordersPlaced.Store(1)
	ordersRejected.Store(0)
	if got := refreshOrderFailRateGauge(); got != 0 {
		t.Fatalf("计数器回退应重新起窗并写 0, got %d", got)
	}
	advance(30 * time.Second)
	ordersRejected.Add(1) // 新进程内 1 拒 1 成 → 500‰
	ordersPlaced.Add(1)
	if got := refreshOrderFailRateGauge(); got != 500 {
		t.Fatalf("重新起窗后应按新累计算, got %d", got)
	}
}

// TestRunAlertEvaluationRefreshesDerivedGauge 评估入口必须先刷派生量规再取快照（否则永远读上一轮值）。
// 取值刻意留在规则阈值（50‰）之下：本用例只验"刷新发生了"，不往全局路由表里塞一条 p1 事件，
// 免得污染同包其它出站用例。
func TestRunAlertEvaluationRefreshesDerivedGauge(t *testing.T) {
	advance := withFakeClock(t, time.Unix(1760000000, 0))
	refreshOrderFailRateGauge()
	ordersPlaced.Add(29)
	ordersRejected.Add(1) // 1/30 ≈ 33‰
	advance(30 * time.Second)
	RunAlertEvaluation()
	if v, ok := GetGauge("order_fail_rate_milli"); !ok || v != 33 {
		t.Fatalf("RunAlertEvaluation 未刷新派生量规, got v=%d ok=%v", v, ok)
	}
}
