// decision_record_test.go — §W-02（2026-10-29 夜批）纸面决策快照的落点锁。
//
// 为什么单独一组：纸面的动态预算跑在 internal/paper 内部，和实盘那条 persistDynBuyDecision
// 完全是两套代码。没有这组锁，「今日动态仓位」在纸面通道恒空这件事只会在页面上表现为
// "今天没有纸面交易"，而不会在测试里红。
package paper

import (
	"math"
	"sync"
	"testing"
)

// B1：池现金不足 ⇒ 回调必须收到 skipped（带原因、Qty=0），不是静默丢弃。
func TestDecisionRecordOnSkip(t *testing.T) {
	pe := New(dynPaperConfig(100000), "")
	pe.SetSizingContext(1, 0.1, 0.3, 1, false)
	var got []DecisionRecord
	pe.SetDecisionRecorder(func(r DecisionRecord) { got = append(got, r) })

	quotes := dynQuotes("600000.SH", "600001.SH")
	if err := pe.BuyInPool("600000.SH", "浦发", "龙头", "", 10, quotes); err != nil {
		t.Fatalf("首笔买入: %v", err)
	}
	// 池权益仍 > 0（持仓还在），但池现金清零 ⇒ 第二笔必须走 skipped 分支。
	pe.mu.Lock()
	pe.pools[""] = 0
	pe.mu.Unlock()
	if err := pe.BuyInPool("600001.SH", "华夏", "龙头", "", 10, quotes); err == nil {
		t.Fatal("池现金为 0 时应拒绝买入")
	}
	if len(got) < 2 {
		t.Fatalf("两笔预算都应播快照，实得 %d 条", len(got))
	}
	rec := got[len(got)-1]
	if rec.Stage != "skipped" || rec.Reason != "池现金不足" || rec.Qty != 0 {
		t.Fatalf("跳过快照应为 skipped/池现金不足/Qty=0，实得 %+v", rec)
	}
}

// B2：正常买入 ⇒ placed，且 Notional 恰等于 Qty×成交价。
func TestDecisionRecordOnPlaced(t *testing.T) {
	pe := New(dynPaperConfig(100000), "")
	pe.SetSizingContext(1, 0.1, 0.3, 1, false)
	var rec DecisionRecord
	got := false
	pe.SetDecisionRecorder(func(r DecisionRecord) { rec = r; got = true })

	if err := pe.BuyInPool("600000.SH", "浦发", "龙头", "", 10, dynQuotes("600000.SH")); err != nil {
		t.Fatalf("买入: %v", err)
	}
	if !got {
		t.Fatal("正常买入没有播决策快照（页面会恒空）")
	}
	if rec.Stage != "placed" || rec.Qty <= 0 {
		t.Fatalf("正常买入应为 placed 且 Qty>0，实得 %+v", rec)
	}
	if math.Abs(rec.Notional-float64(rec.Qty)*10) > 1e-6 {
		t.Fatalf("Notional 必须等于 Qty×成交价，实得 %.6f（Qty=%d）", rec.Notional, rec.Qty)
	}
	if rec.Day == "" || rec.At.IsZero() {
		t.Fatalf("日键/时刻必须落值（否则前端按时间排序会挤到最前），实得 %+v", rec)
	}
}

// B3：回调在 e.mu 内执行，回调里不许再取锁——这里用 -race 跑 1000 次证明不会自锁。
func TestDecisionRecordNoDeadlock(t *testing.T) {
	pe := New(dynPaperConfig(100000), "")
	pe.SetSizingContext(1, 0.1, 0.3, 1, false)
	var mu sync.Mutex
	n := 0
	pe.SetDecisionRecorder(func(DecisionRecord) {
		mu.Lock()
		n++
		mu.Unlock()
	})
	for i := 0; i < 1000; i++ {
		pe.mu.Lock()
		_, _ = pe.dynamicPaperQtyLocked("", "600000.SH", 10, 1.0)
		pe.mu.Unlock()
	}
	mu.Lock()
	defer mu.Unlock()
	if n != 1000 {
		t.Fatalf("1000 次预算应播 1000 条快照，实得 %d（回调被跳过＝录制点没接在出口上）", n)
	}
}
