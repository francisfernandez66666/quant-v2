// 聚合器单元测试：覆盖 UpdateFast 对主循环看板数据的保留/合并行为，以及并发读写安全性。
package display

import (
	"sync"
	"testing"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/strategy_engine"
)

// emptyResult 空策略结果别名，用于构造不含任何事件/板块的默认结果。
type emptyResult = strategy_engine.StrategyResult

// mkSig 构造一个仅含代码与策略名的测试信号（生成时间取当前时刻）。
func mkSig(code, strategy string) combat_agent.Signal {
	return combat_agent.Signal{Code: code, Strategy: strategy, GeneratedAt: time.Now()}
}

// TestUpdateFastPreservesDashboard 验证近实时更新（UpdateFast）的【替换】契约：
// 生产调用方（scoring_loop）每次传 mergeSignals(emit, signalStore.List()) 全量集合——
// 固化库已含主循环信号，因此替换不会丢主循环数据；并锁定 §R3-8 P1-B 防膨胀回归：
// 连续多轮 UpdateFast 后 BullSignals 长度不得增长（旧 append 实现每轮翻倍堆积）。
func TestUpdateFastPreservesDashboard(t *testing.T) {
	a := New()
	// 先由主循环写入完整看板
	main := mkSig("600000", "dragon")
	a.Update(&emptyResult{}, nil, nil, []combat_agent.Signal{main}, nil, nil,
		map[string]combat_agent.StockScores{"600000": {Code: "600000", DragonScore: 80}},
		nil)

	// 近实时循环 UpdateFast：传"固化(含主循环) + 本轮新翻转"的全量集合（生产口径）
	fast := []combat_agent.Signal{mkSig("000001", "n_shape")}
	merged := append([]combat_agent.Signal{main}, fast...)
	scores := map[string]combat_agent.StockScores{
		"600000": {Code: "600000", DragonScore: 85, MomentumScore: 70, UpdatedAt: time.Now()},
		"000001": {Code: "000001", NScore: 66, MomentumScore: 60, UpdatedAt: time.Now()},
	}
	a.UpdateFast(scores, merged, nil)

	cur := a.Current()
	if cur == nil {
		t.Fatal("current 应为非 nil")
	}
	if len(cur.BullSignals) != 2 {
		t.Fatalf("BullSignals 应为主循环1+近实时1=2, got %d", len(cur.BullSignals))
	}
	// 分数刷新生效
	if cur.Scores["600000"].DragonScore != 85 {
		t.Fatalf("分数应更新为85, got %.0f", cur.Scores["600000"].DragonScore)
	}
	// 近实时信号并入 FinalSignals
	foundFast := false
	for _, s := range cur.FinalSignals {
		if s.Code == "000001" {
			foundFast = true
		}
	}
	if !foundFast {
		t.Fatal("近实时信号应进入 FinalSignals")
	}

	// §R3-8 P1-B 防膨胀回归：连续 100 轮传入同一全量集合，BullSignals 恒为 2 条。
	// （旧实现：每轮 append 整份列表 → 102 条且持续翻倍。）
	for i := 0; i < 100; i++ {
		a.UpdateFast(scores, merged, nil)
	}
	if got := len(a.Current().BullSignals); got != 2 {
		t.Fatalf("重复全量更新不得累积: 第100轮后 BullSignals=%d, 期望 2", got)
	}
}

// TestAggregatorConcurrentAccess 并发安全测试：多个 goroutine 交替调用 Update/UpdateFast
// 并并发读取 Current，运行一段时间后应无数据竞争或 panic。
func TestAggregatorConcurrentAccess(t *testing.T) {
	a := New()
	var wg sync.WaitGroup
	stop := make(chan struct{})
	// 并发写读：主循环 Update 与近实时 UpdateFast 交替，Current 并发读
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					if n%2 == 0 {
						a.Update(&emptyResult{}, nil, nil, nil, nil, nil, map[string]combat_agent.StockScores{}, nil)
					} else {
						a.UpdateFast(map[string]combat_agent.StockScores{}, nil, nil)
					}
					_ = a.Current()
				}
			}
		}(i)
	}
	time.Sleep(200 * time.Millisecond)
	close(stop)
	wg.Wait()
}

// TestResolveConflictBranches 信号冲突裁决全分支：
// 提醒信号不并入最终信号、blocked 剔除、同代码取最新、按置信度排序。
func TestResolveConflictBranches(t *testing.T) {
	t0 := time.Date(2026, 8, 5, 10, 0, 0, 0, time.Local)
	old := time.Date(2026, 8, 5, 9, 0, 0, 0, time.Local)
	bull := []combat_agent.Signal{
		{Code: "600580", Direction: "做多", Confidence: 0.5, GeneratedAt: old},
		{Code: "002460", Direction: "做多", Confidence: 0.9, GeneratedAt: t0},
	}
	bear := []combat_agent.Signal{
		{Code: "600580", Direction: "做空", Confidence: 0.8, GeneratedAt: t0}, // 600580 冲突，应取最新(做空)
	}
	alerts := []combat_agent.Signal{
		{Code: "999999", Direction: "提醒", AlertType: "止盈", Confidence: 0.7, GeneratedAt: t0},
	}
	blocked := map[string]bool{"002460": true}

	got := resolveConflict(bull, bear, alerts, blocked)

	// 提醒信号 999999 不并入最终信号
	for _, s := range got {
		if s.Code == "999999" {
			t.Error("提醒信号不应并入最终信号")
		}
	}
	// blocked 的 002460 应剔除
	for _, s := range got {
		if s.Code == "002460" {
			t.Error("blocked 股票应剔除")
		}
	}
	// 600580 冲突应取最新（做空, conf 0.8）
	var fss *combat_agent.Signal
	for i := range got {
		if got[i].Code == "600580" {
			fss = &got[i]
		}
	}
	if fss == nil {
		t.Fatal("应保留 600580 信号")
	}
	if fss.Direction != "做空" || fss.Confidence != 0.8 {
		t.Errorf("600580 冲突应取最新做空信号, got %s conf %.1f", fss.Direction, fss.Confidence)
	}
	// 只剩 1 条最终信号
	if len(got) != 1 {
		t.Fatalf("最终信号应仅剩 600580, got %d", len(got))
	}
	// 置信度降序排序
	if len(got) > 1 {
		for i := 1; i < len(got); i++ {
			if got[i].Confidence > got[i-1].Confidence {
				t.Error("最终信号应置信度降序")
			}
		}
	}
}
