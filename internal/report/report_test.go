// report 包测试：开仓/平仓/更新/删除、持仓查询、统计汇总与 JSON 持久化往返。
package report

import (
	"path/filepath"
	"testing"
	"time"
)

// TestLogSignalAndHeld 开仓后进入持仓，代码去重。
func TestLogSignalAndHeld(t *testing.T) {
	r := New("")
	r.LogSignal("s1", "600276", "恒瑞医药", "做多", "dragon", 20.0, 50, 10)
	r.LogSignal("s2", "600276", "恒瑞医药", "做多", "dragon", 22.0, 50, 10)
	r.LogSignal("s3", "300750", "宁德时代", "做多", "n_shape", 180.0, 30, 8)

	held := r.HeldPositionCodes()
	if len(held) != 2 {
		t.Errorf("持仓代码应去重为 2 只, got %d (%v)", len(held), held)
	}
	if len(r.HeldPositions()) != 3 {
		t.Errorf("持仓记录应=3, got %d", len(r.HeldPositions()))
	}
}

// TestLogExitProfitLabel 平仓后按盈亏标记 止盈/止损。
func TestLogExitProfitLabel(t *testing.T) {
	r := New("")
	r.LogSignal("a", "600000", "浦发", "long", "x", 10.0, 50, 10)
	r.Update("a", func(l *ExecLog) { l.EntryAt = time.Now().Add(-time.Hour) })

	// 盈利 20% → 已止盈
	r.LogExit("a", 11.0)
	l := r.FindBySignalID("a")
	if l == nil || l.Status != "已止盈" {
		t.Fatalf("盈利平仓应标记 已止盈, got %+v", l)
	}
	if l.ProfitPct == nil || *l.ProfitPct != 10.0 {
		t.Errorf("ProfitPct 应=10.0, got %v", l.ProfitPct)
	}
	if l.ExitAt == nil {
		t.Error("平仓后 ExitAt 不应为空")
	}

	// 重复平仓应为无操作
	r.LogSignal("b", "600001", "B", "long", "x", 10.0, 50, 10)
	r.LogExit("b", 9.0)
	b := r.FindBySignalID("b")
	if b.Status != "已止损" {
		t.Errorf("亏损平仓应标记 已止损, got %s", b.Status)
	}
	// 再平失败不 panic
	r.LogExit("nonexistent", 1.0)
}

// TestDeleteSoftDelete 删除为软删除（状态置"已删除"，仍可查到）。
func TestDeleteSoftDelete(t *testing.T) {
	r := New("")
	r.LogSignal("d", "600002", "D1", "long", "x", 1, 10, 10)
	r.Delete("d")
	l := r.FindBySignalID("d")
	if l == nil || l.Status != "已删除" {
		t.Errorf("应保留为 已删除, got %+v", l)
	}
}

// TestStats 胜率/平均盈损统计。
func TestStats(t *testing.T) {
	r := New("")
	r.LogSignal("w1", "1", "A", "long", "x", 10, 100, 100)
	r.LogSignal("w2", "2", "B", "long", "x", 10, 100, 100)
	r.LogSignal("l1", "3", "C", "long", "x", 10, 100, 100)
	r.LogSignal("h1", "4", "D", "long", "x", 10, 100, 100)

	r.LogExit("w1", 12)  // +20%
	r.LogExit("w2", 8)   // -20%
	r.LogExit("h1", 20)  // +100%

	total, holding, win, winRate, avgWin, avgLoss := r.Stats()
	if total != 4 || holding != 1 {
		t.Errorf("total/holding 应=4/1, got %d/%d", total, holding)
	}
	if win != 2 {
		t.Errorf("win 应=2(w1:+20, h1:+100), got %d", win)
	}
	if diff := winRate - 66.6667; diff > 0.01 || diff < -0.01 {
		t.Errorf("winRate 应≈66.7%% (2胜/1负), got %.1f%%", winRate)
	}
	if diff := avgWin - 60.0; diff > 0.01 || diff < -0.01 {
		t.Errorf("avgWin 应=(20+100)/2=60, got %.1f", avgWin)
	}
	if diff := avgLoss + 20.0; diff > 0.01 || diff < -0.01 {
		t.Errorf("avgLoss 应=-20, got %.1f", avgLoss)
	}
}

// TestPersistenceRoundTrip 落盘后可重载恢复全部记录与状态。
func TestPersistenceRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	r := New(path)
	r.LogSignal("p1", "300750", "宁德时代", "long", "dragon", 20.0, 50, 10)
	r.LogExit("p1", 25.0)

	r2 := New(path)
	if len(r2.List()) != 1 {
		t.Fatalf("重载后应恢复 1 条记录, got %d", len(r2.List()))
	}
	got := r2.FindBySignalID("p1")
	if got == nil || got.Status != "已止盈" || got.Name != "宁德时代" {
		t.Errorf("重载后状态/名称异常: %+v", got)
	}
}

// TestEmptyInMemory 内存模式（path=""）不落盘不报错。
func TestEmptyInMemory(t *testing.T) {
	r := New("")
	r.LogSignal("m", "1", "A", "long", "x", 1, 10, 10)
	if got := r.FindBySignalID("m"); got == nil {
		t.Error("内存模式应可读写")
	}
	r.LogExit("m", 1.1)
}