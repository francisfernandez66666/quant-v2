// report 包测试：开仓/平仓/更新/删除、持仓查询、统计汇总与 JSON 持久化往返。
package report

import (
	"math"
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

	r.LogExit("w1", 12) // +20%
	r.LogExit("w2", 8)  // -20%
	r.LogExit("h1", 20) // +100%

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

// TestAddLotWeightedAverage 增量买入加权平均成本：1 元×100 + 2 元×100 = 200 股 / 成本 1.5。
func TestAddLotWeightedAverage(t *testing.T) {
	r := New("")
	r.LogSignal("lot1", "600000", "浦发", "做多", "手动", 1.0, 8, 5)
	r.AddLot("lot1", 1.0, 100) // 首笔
	l := r.FindBySignalID("lot1")
	if l.Quantity != 100 || math.Abs(l.EntryPrice-1.0) > 1e-9 {
		t.Fatalf("首笔后应 100 股@1.0, got qty=%.0f cost=%.4f", l.Quantity, l.EntryPrice)
	}
	r.AddLot("lot1", 2.0, 100) // 加仓
	l = r.FindBySignalID("lot1")
	if l.Quantity != 200 {
		t.Errorf("加仓后应 200 股, got %.0f", l.Quantity)
	}
	if math.Abs(l.EntryPrice-1.5) > 1e-9 {
		t.Errorf("加权成本应=1.5, got %.4f", l.EntryPrice)
	}
	if len(l.Lots) != 2 {
		t.Errorf("批次明细应=2 笔, got %d", len(l.Lots))
	}
	// 无效参数不加批次
	r.AddLot("lot1", 0, 100)
	if len(r.FindBySignalID("lot1").Lots) != 2 {
		t.Error("无效加仓参数不应追加批次")
	}
	// 不存在的 ID 静默
	r.AddLot("nonexistent", 3, 10)
}

// TestSetCostBasis 改成本重建为单条合成批次，数量不变。
func TestSetCostBasis(t *testing.T) {
	r := New("")
	r.LogSignal("cost1", "600001", "B", "做多", "手动", 1.0, 8, 5)
	r.AddLot("cost1", 1.0, 100)
	r.AddLot("cost1", 2.0, 100) // 200 股@1.5
	r.SetCostBasis("cost1", 1.8)
	l := r.FindBySignalID("cost1")
	if math.Abs(l.EntryPrice-1.8) > 1e-9 {
		t.Errorf("改成本后 EntryPrice 应=1.8, got %.4f", l.EntryPrice)
	}
	if l.Quantity != 200 {
		t.Errorf("改成本不应改变数量, got %.0f", l.Quantity)
	}
	if len(l.Lots) != 1 || math.Abs(l.Lots[0].Price-1.8) > 1e-9 || l.Lots[0].Quantity != 200 {
		t.Errorf("改成本后明细应重建为单条合成批次[1.8×200], got %+v", l.Lots)
	}
	// 无效价格不动
	r.SetCostBasis("cost1", 0)
	if math.Abs(r.FindBySignalID("cost1").EntryPrice-1.8) > 1e-9 {
		t.Error("无效成本价不应修改")
	}
}

// TestLotsPersistenceRoundTrip 批次明细随 JSON 持久化往返恢复。
func TestLotsPersistenceRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report_lots.json")
	r := New(path)
	r.LogSignal("p2", "600002", "C", "做多", "手动", 1.0, 8, 5)
	r.AddLot("p2", 1.0, 100)
	r.AddLot("p2", 2.0, 100)

	r2 := New(path)
	l := r2.FindBySignalID("p2")
	if l == nil {
		t.Fatal("重载后应存在记录")
	}
	if len(l.Lots) != 2 || math.Abs(l.EntryPrice-1.5) > 1e-9 || l.Quantity != 200 {
		t.Errorf("重载后明细/加权成本异常: lots=%d cost=%.4f qty=%.0f", len(l.Lots), l.EntryPrice, l.Quantity)
	}
}

// TestSellLotPartialFIFO 减仓部分卖出：FIFO 扣减批次、重算平均成本与剩余数量。
func TestSellLotPartialFIFO(t *testing.T) {
	r := New("")
	r.LogSignal("s", "000001", "平安", "做多", "x", 10.0, 8, 5)
	// 加两批：100股@10、100股@20 → 加权成本15，数量200
	r.AddLot("s", 10.0, 100)
	r.AddLot("s", 20.0, 100)
	l := r.FindBySignalID("s")
	if l.Quantity != 200 || l.EntryPrice != 15 {
		t.Fatalf("前置：应 200股/成本15, got %.0f/%.2f", l.Quantity, l.EntryPrice)
	}
	// 卖出50股 → FIFO 从第一批扣 → 剩第一批50 + 第二批100，成本 = (10*50+20*100)/150 约16.67
	r.SellLot("s", 30.0, 50)
	l = r.FindBySignalID("s")
	if l.ExitAt != nil {
		t.Fatal("部分卖出不应平仓")
	}
	if l.Quantity != 150 {
		t.Fatalf("剩余数量应=150, got %.0f", l.Quantity)
	}
	want := (10.0*50 + 20.0*100) / 150.0
	if math.Abs(l.EntryPrice-want) > 0.001 {
		t.Errorf("平均成本应≈%.4f, got %.4f", want, l.EntryPrice)
	}
	// 再卖出150股 → 全部卖出，应自动平仓
	r.SellLot("s", 30.0, 150)
	l = r.FindBySignalID("s")
	if l.ExitAt == nil {
		t.Fatal("全部卖出应平仓")
	}
	if len(r.HeldPositions()) != 0 {
		t.Fatalf("全部卖出后不应有持仓, got %d", len(r.HeldPositions()))
	}
}

// TestSellLotOverSell 减仓数量超过持仓不应产生负持有；按全部卖出处理。
func TestSellLotOverSell(t *testing.T) {
	r := New("")
	r.LogSignal("s", "000002", "万科", "做多", "x", 8.0, 8, 5)
	r.SellLot("s", 9.0, 500)
	l := r.FindBySignalID("s")
	if l.ExitAt == nil {
		t.Fatal("超过持有量减仓应视为全部卖出并平仓")
	}
}

// TestRealizedPnlIncremental 已实现盈亏随加减仓增量累计：
// 10@100 + 20@100 → 卖50@30（FIFO 从首批扣，已实现=(30-10)*50=1000）→ 再卖50@30（首批剩50全卖，已实现+=(30-10)*50=1000）
// → 全卖150@30（均价16.67，已实现+=(30-16.67)*150≈2000）→ 累计≈4000。
func TestRealizedPnlIncremental(t *testing.T) {
	r := New("")
	r.LogSignal("p", "000001", "平安", "做多", "x", 10.0, 8, 5)
	r.AddLot("p", 10.0, 100)
	r.AddLot("p", 20.0, 100) // 200 股 @ 15
	l := r.FindBySignalID("p")
	if math.Abs(l.EntryPrice-15) > 1e-9 {
		t.Fatalf("前置：加权成本应=15, got %.4f", l.EntryPrice)
	}
	// 第一次部分卖出 50 @ 30（FIFO 扣首批 100 中的 50）
	r.SellLot("p", 30.0, 50)
	l = r.FindBySignalID("p")
	if math.Abs(l.RealizedPnl-1000) > 0.01 {
		t.Errorf("卖50@30 已实现应=1000, got %.2f", l.RealizedPnl)
	}
	if l.ExitAt != nil {
		t.Fatal("部分卖出不应平仓")
	}
	// 第二次部分卖出 50 @ 30（首批 100 已剩 50，整批卖完）
	r.SellLot("p", 30.0, 50)
	l = r.FindBySignalID("p")
	if math.Abs(l.RealizedPnl-2000) > 0.01 {
		t.Errorf("累计已实现应=2000, got %.2f", l.RealizedPnl)
	}
	// 剩余 100 @ 20，全卖 @ 30
	r.SellLot("p", 30.0, 100)
	l = r.FindBySignalID("p")
	if l.ExitAt == nil {
		t.Fatal("全卖应平仓")
	}
	want := float64(2000) + (30-20)*100
	if math.Abs(l.RealizedPnl-want) > 0.01 {
		t.Errorf("全卖后累计已实现应=%.2f, got %.2f", want, l.RealizedPnl)
	}
	if got := r.TotalRealizedPnl(""); math.Abs(got-want) > 0.01 {
		t.Errorf("TotalRealizedPnl 应=%.2f, got %.2f", want, got)
	}
}

// TestRealizedPnlLogExit 直接平仓（LogExit 路径）也累计已实现。
func TestRealizedPnlLogExit(t *testing.T) {
	r := New("")
	r.LogSignal("e", "600000", "浦发", "做多", "x", 10.0, 8, 5)
	r.AddLot("e", 10.0, 100)
	r.LogExit("e", 12.0, "手动")
	if got := r.RealizedPnlFor("e"); math.Abs(got-200) > 0.01 {
		t.Errorf("LogExit 后已实现应=200, got %.2f", got)
	}
}

// TestRealizedPnlSetCostBasis 改成本不改变已实现累计。
func TestRealizedPnlSetCostBasis(t *testing.T) {
	r := New("")
	r.LogSignal("c", "600001", "B", "做多", "x", 10.0, 8, 5)
	r.AddLot("c", 10.0, 100)
	r.AddLot("c", 20.0, 100)
	r.SellLot("c", 30.0, 50) // 已实现 1000
	r.SetCostBasis("c", 18.0)
	if got := r.RealizedPnlFor("c"); math.Abs(got-1000) > 0.01 {
		t.Errorf("改成本不应改变已实现, got %.2f", got)
	}
}

// TestStatsExcludesDeletedAndDraw 覆盖胜率口径修正（A2）：
// 已删除记录不计入 total；盈亏为 0 的记录按"平手"处理，不计入赢/亏分母。
func TestStatsExcludesDeletedAndDraw(t *testing.T) {
	r := New("")
	r.LogSignal("w1", "1", "A", "long", "x", 10, 100, 100)
	r.LogSignal("w2", "2", "B", "long", "x", 10, 100, 100)
	r.LogSignal("l1", "3", "C", "long", "x", 10, 100, 100)
	r.LogSignal("d1", "4", "D", "long", "x", 10, 100, 100)
	r.LogSignal("z1", "5", "E", "long", "x", 10, 100, 100)

	r.LogExit("w1", 12) // +20%
	r.LogExit("l1", 8)  // -20%
	r.LogExit("z1", 10) // 0% 平手
	r.Delete("d1")      // 软删除

	total, holding, win, winRate, _, _ := r.Stats()
	// total 应排除已删除 d1：5 条 - 1 已删除 = 4
	if total != 4 {
		t.Errorf("total 应=4（排除已删除 d1）, got %d", total)
	}
	// 平手 z1 不算亏损，胜率分母 = 1胜+1负 = 2 → 50%
	if win != 1 {
		t.Errorf("win 应=1, got %d", win)
	}
	if diff := winRate - 50.0; diff > 0.01 || diff < -0.01 {
		t.Errorf("winRate 应=50%%（平手不进分母）, got %.1f%%", winRate)
	}
	if holding != 1 {
		t.Errorf("holding 应=1（w2 未平仓）, got %d", holding)
	}
}

// TestStatsByStrategy 覆盖按战法分组统计（A1）：各战法独立统计胜率/平均盈/平均亏/盈亏比。
func TestStatsByStrategy(t *testing.T) {
	r := New("")
	// Dragon：2 胜 1 负
	r.LogSignal("dw1", "1", "A", "做多", "Dragon", 10, 10, 8)
	r.LogSignal("dw2", "2", "B", "做多", "Dragon", 10, 10, 8)
	r.LogSignal("dl1", "3", "C", "做多", "Dragon", 10, 10, 8)
	// DoubleBump：1 胜 1 平手 1 持仓
	r.LogSignal("bw1", "4", "D", "做多", "DoubleBump", 10, 10, 8)
	r.LogSignal("bz1", "5", "E", "做多", "DoubleBump", 10, 10, 8)
	r.LogSignal("bh1", "6", "F", "做多", "DoubleBump", 10, 10, 8)

	r.LogExit("dw1", 12) // +20%
	r.LogExit("dw2", 14) // +40%
	r.LogExit("dl1", 8)  // -20%
	r.LogExit("bw1", 12) // +20%
	r.LogExit("bz1", 10) // 0% 平手

	by := r.StatsByStrategy("")
	if len(by) != 2 {
		t.Fatalf("应按 2 个战法分组, got %d", len(by))
	}

	dr := by["Dragon"]
	if dr.Total != 3 || dr.Win != 2 || dr.Loss != 1 {
		t.Errorf("Dragon 应 total=3 win=2 loss=1, got %+v", dr)
	}
	if diff := dr.WinRate - 66.6667; diff > 0.01 || diff < -0.01 {
		t.Errorf("Dragon 胜率应≈66.7%%, got %.1f%%", dr.WinRate)
	}
	if diff := dr.AvgWinPct - 30.0; diff > 0.01 || diff < -0.01 {
		t.Errorf("Dragon avgWin 应=(20+40)/2=30, got %.1f", dr.AvgWinPct)
	}
	if diff := dr.ProfitFactor - 3.0; diff > 0.01 || diff < -0.01 {
		t.Errorf("Dragon 盈亏比应=60/20=3, got %.2f", dr.ProfitFactor)
	}

	bb := by["DoubleBump"]
	if bb.Total != 3 || bb.Win != 1 || bb.Draw != 1 || bb.Holding != 1 {
		t.Errorf("DoubleBump 应 total=3 win=1 draw=1 holding=1, got %+v", bb)
	}
	if bb.Loss != 0 {
		t.Errorf("DoubleBump 平手不应算亏损, got loss=%d", bb.Loss)
	}
}
