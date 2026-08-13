package engine

import (
	"path/filepath"
	"testing"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/strategy_engine"
)

// mkBuySig 构造一个做多交易信号（含生成时间）。
func mkBuySig(code, strategy string, at time.Time) combat_agent.Signal {
	return combat_agent.Signal{
		ID:          code + "@" + strategy,
		Code:        code,
		Name:        "测试",
		Strategy:    strategy,
		Direction:   "做多",
		Action:      "buy",
		Price:       10.0,
		Confidence:  0.7,
		Reason:      "full_chain",
		GeneratedAt: at,
	}
}

// TestSignalStoreInvalidateTombstone 验证失效墓碑核心行为：
// 打墓碑后信号移出固化列表，同日同一 code@strategy 再次 Upsert 也不会复活。
func TestSignalStoreInvalidateTombstone(t *testing.T) {
	s := newSignalStore("")
	at := time.Now()

	s.Upsert([]combat_agent.Signal{mkBuySig("600001", "n_shape", at)})
	if len(s.List()) != 1 {
		t.Fatalf("固化后应有 1 条信号, got %d", len(s.List()))
	}

	s.Invalidate("600001", "n_shape")
	if len(s.List()) != 0 {
		t.Fatalf("打墓碑后固化列表应清空, got %+v", s.List())
	}
	if !s.IsInvalidated("600001", "n_shape") {
		t.Fatal("打墓碑后应记录失效标记")
	}

	// 同日再次出现同一 key 的 Pass 信号 → 被墓碑拦截，不复活
	s.Upsert([]combat_agent.Signal{mkBuySig("600001", "n_shape", at.Add(10*time.Second))})
	if len(s.List()) != 0 {
		t.Fatalf("墓碑后同 key 不应复活, got %+v", s.List())
	}
}

// TestSignalStoreInvalidateOtherKeysUnaffected 验证墓碑只作用单只股票单战法，其他信号不受影响。
func TestSignalStoreInvalidateOtherKeysUnaffected(t *testing.T) {
	s := newSignalStore("")
	at := time.Now()
	s.Upsert([]combat_agent.Signal{
		mkBuySig("600001", "n_shape", at),
		mkBuySig("600002", "n_shape", at),
	})

	s.Invalidate("600001", "n_shape")

	list := s.List()
	if len(list) != 1 || list[0].Code != "600002" {
		t.Fatalf("墓碑应只移除 600001@n_shape, got %+v", list)
	}
}

// TestSignalStoreInvalidatePersistsAcrossReload 验证墓碑跨重启持久化：重载后同日仍不复活。
func TestSignalStoreInvalidatePersistsAcrossReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "signals.json")
	at := time.Now()

	s1 := newSignalStore(path)
	s1.Upsert([]combat_agent.Signal{mkBuySig("600001", "n_shape", at)})
	s1.Invalidate("600001", "n_shape")

	s2 := newSignalStore(path)
	if !s2.IsInvalidated("600001", "n_shape") {
		t.Fatal("重载后应保留失效墓碑标记")
	}
	s2.Upsert([]combat_agent.Signal{mkBuySig("600001", "n_shape", at.Add(10*time.Second))})
	if len(s2.List()) != 0 {
		t.Fatalf("重载后同日同 key 仍不应复活, got %+v", s2.List())
	}
}

// invalidateTestEngine 组装 invalidateBrokenSignals 所需的最小 Engine（固化存储 + 消息中心）。
func invalidateTestEngine() *Engine {
	return &Engine{
		signalStore: newSignalStore(""),
		msgStore:    data.NewMessageStore(""),
	}
}

// TestInvalidateBrokenSignalsRemovesBelowTrigger 验证失效墓碑：现价跌破触发价 → 移出固化、
// 删除消息中心条目并记录墓碑（同日不复活）。
func TestInvalidateBrokenSignalsRemovesBelowTrigger(t *testing.T) {
	e := invalidateTestEngine()
	at := time.Now()
	e.signalStore.Upsert([]combat_agent.Signal{mkBuySig("600001", "n_shape", at)})
	e.msgStore.Sync([]data.MessageItem{{ID: "600001@交易信号@n_shape", Code: "600001"}})

	md := map[string]*strategy_engine.StockMarketData{
		"600001": {Code: "600001", Price: 9.5, Quote: &data.StockInfo{Price: 9.5}},
	}
	e.invalidateBrokenSignals(md)

	if len(e.signalStore.List()) != 0 {
		t.Fatalf("跌破触发价后固化信号应移除, got %+v", e.signalStore.List())
	}
	if !e.signalStore.IsInvalidated("600001", "n_shape") {
		t.Fatal("跌破触发价后应打失效墓碑")
	}
	for _, m := range e.msgStore.List() {
		if m.ID == "600001@交易信号@n_shape" {
			t.Fatalf("消息中心对应条目应被删除, got %+v", m)
		}
	}
}

// TestInvalidateBrokenSignalsKeepsAboveTrigger 验证现价未跌破触发价 → 信号保持有效。
func TestInvalidateBrokenSignalsKeepsAboveTrigger(t *testing.T) {
	e := invalidateTestEngine()
	at := time.Now()
	e.signalStore.Upsert([]combat_agent.Signal{mkBuySig("600001", "n_shape", at)})

	md := map[string]*strategy_engine.StockMarketData{
		"600001": {Code: "600001", Price: 10.5, Quote: &data.StockInfo{Price: 10.5}},
	}
	e.invalidateBrokenSignals(md)

	if len(e.signalStore.List()) != 1 {
		t.Fatalf("现价未跌破触发价不应移除, got %+v", e.signalStore.List())
	}
	if e.signalStore.IsInvalidated("600001", "n_shape") {
		t.Fatal("现价未跌破触发价不应打墓碑")
	}
}

// TestInvalidateBrokenSignalsSkipsShortAndMissing 验证：做空信号不受墓碑影响；
// 行情缺失/价格无效时跳过（不误删），留待下一轮有数据再判。
func TestInvalidateBrokenSignalsSkipsShortAndMissing(t *testing.T) {
	e := invalidateTestEngine()
	at := time.Now()
	long := mkBuySig("600001", "n_shape", at)
	short := mkBuySig("600002", "n_shape", at)
	short.Direction = "做空"
	e.signalStore.Upsert([]combat_agent.Signal{long, short})

	// 600002 无行情（不在 md 中）；600001 有行情但价格无效
	md := map[string]*strategy_engine.StockMarketData{
		"600001": {Code: "600001", Quote: &data.StockInfo{Price: 0}},
	}
	e.invalidateBrokenSignals(md)

	if len(e.signalStore.List()) != 2 {
		t.Fatalf("做空/无有效行情信号不应被移除, got %+v", e.signalStore.List())
	}
}
