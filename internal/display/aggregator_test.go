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

// TestUpdateFastPreservesDashboard 验证近实时更新（UpdateFast）不会破坏主循环写下的看板数据：
// 主循环信号保留、分数刷新生效、近实时信号并入 BullSignals 与 FinalSignals。
func TestUpdateFastPreservesDashboard(t *testing.T) {
	a := New()
	// 先由主循环写入完整看板
	a.Update(&emptyResult{}, nil, nil, []combat_agent.Signal{mkSig("600000", "dragon")}, nil, nil,
		map[string]combat_agent.StockScores{"600000": {Code: "600000", DragonScore: 80}},
		nil)

	// 近实时循环 UpdateFast：刷新分数 + 合并近实时信号
	fast := []combat_agent.Signal{mkSig("000001", "n_shape")}
	scores := map[string]combat_agent.StockScores{
		"600000": {Code: "600000", DragonScore: 85, MomentumScore: 70, UpdatedAt: time.Now()},
		"000001": {Code: "000001", NScore: 66, MomentumScore: 60, UpdatedAt: time.Now()},
	}
	a.UpdateFast(scores, fast, nil)

	cur := a.Current()
	if cur == nil {
		t.Fatal("current 应为非 nil")
	}
	// 主循环信号保留 + 近实时信号并入 BullSignals
	if len(cur.BullSignals) != 2 {
		t.Fatalf("BullSignals 应含主循环1+近实时1=2, got %d", len(cur.BullSignals))
	}
	// 近实时信号并入
	if cur.Scores["600000"].DragonScore != 85 {
		t.Fatalf("分数应更新为85, got %.0f", cur.Scores["600000"].DragonScore)
	}
	foundFast := false
	for _, s := range cur.FinalSignals {
		if s.Code == "000001" {
			foundFast = true
		}
	}
	if !foundFast {
		t.Fatal("近实时信号应进入 FinalSignals")
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
