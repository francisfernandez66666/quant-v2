// Package engine 核心引擎：信号生产、打分池、板块传播、持仓退出、通知推送与 QMT 自动交易的 orchestration。
package engine

import (
	"path/filepath"
	"testing"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/report"
	"quant-trading-v2/internal/strategy_engine"
)

// mkBuySig 构造一个做多交易信号（含生成时间）。
// English: mkBuySig constructs a long trade signal (including the generation time).
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
// English: TestSignalStoreInvalidateTombstone verifies the core tombstone behavior:
// after tombstoning, the signal leaves the persisted list, and a same-day Upsert of the same code@strategy does not revive it.
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
	// English: A Pass signal for the same key reappearing the same day → blocked by the tombstone, not revived.
	s.Upsert([]combat_agent.Signal{mkBuySig("600001", "n_shape", at.Add(10*time.Second))})
	if len(s.List()) != 0 {
		t.Fatalf("墓碑后同 key 不应复活, got %+v", s.List())
	}
}

// TestSignalStoreInvalidateOtherKeysUnaffected 验证墓碑只作用单只股票单战法，其他信号不受影响。
// English: TestSignalStoreInvalidateOtherKeysUnaffected verifies the tombstone only affects a single stock and single strategy; other signals are unaffected.
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
// English: TestSignalStoreInvalidatePersistsAcrossReload verifies the tombstone persists across restarts: same day still not revived after reload.
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
// English: invalidateTestEngine builds the minimal Engine required by invalidateBrokenSignals (persisted store + message center).
func invalidateTestEngine() *Engine {
	return &Engine{
		signalStore: newSignalStore(""),
		msgStore:    data.NewMessageStore(""),
	}
}

// TestInvalidateBrokenSignalsRemovesBelowTrigger 验证失效墓碑：现价跌破触发价 → 移出固化、
// 删除消息中心条目并记录墓碑（同日不复活）。
// English: TestInvalidateBrokenSignalsRemovesBelowTrigger verifies the invalidation tombstone: current price below trigger → removed from persisted,
// message-center entry deleted and tombstone recorded (not revived same day).
func TestInvalidateBrokenSignalsRemovesBelowTrigger(t *testing.T) {
	e := invalidateTestEngine()
	at := time.Now()
	e.signalStore.Upsert([]combat_agent.Signal{mkBuySig("600001", "n_shape", at)})
	e.msgStore.Sync([]data.MessageItem{{ID: "600001@交易信号@n_shape", Code: "600001"}})

	md := map[string]*strategy_engine.StockMarketData{
		"600001": {Code: "600001", Price: 9.5, Quote: &data.StockInfo{Price: 9.5}},
	}
	e.invalidateBrokenSignals(md, nil)

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
// English: TestInvalidateBrokenSignalsKeepsAboveTrigger verifies that when the current price has not fallen below the trigger → the signal stays valid.
func TestInvalidateBrokenSignalsKeepsAboveTrigger(t *testing.T) {
	e := invalidateTestEngine()
	at := time.Now()
	e.signalStore.Upsert([]combat_agent.Signal{mkBuySig("600001", "n_shape", at)})

	md := map[string]*strategy_engine.StockMarketData{
		"600001": {Code: "600001", Price: 10.5, Quote: &data.StockInfo{Price: 10.5}},
	}
	e.invalidateBrokenSignals(md, nil)

	if len(e.signalStore.List()) != 1 {
		t.Fatalf("现价未跌破触发价不应移除, got %+v", e.signalStore.List())
	}
	if e.signalStore.IsInvalidated("600001", "n_shape") {
		t.Fatal("现价未跌破触发价不应打墓碑")
	}
}

// TestInvalidateBrokenSignalsSkipsHeld P2#24 回归：已持有持仓的信号不打失效墓碑——
// 买入成交后现价跌破触发价只是浮亏，不是"买入依据破坏"，固化信号/消息中心条目必须保留。
// English: P2#24 regression — signals of currently-held codes are exempt from invalidation tombstones:
// a filled position breaking below its trigger is a floating loss, not a "broken buy premise"; the pinned
// signal and its message-center entry must survive.
func TestInvalidateBrokenSignalsSkipsHeld(t *testing.T) {
	e := invalidateTestEngine()
	at := time.Now()
	e.signalStore.Upsert([]combat_agent.Signal{mkBuySig("600001", "n_shape", at)})
	e.msgStore.Sync([]data.MessageItem{{ID: "600001@交易信号@n_shape", Code: "600001"}})

	// 装配 report 账本：600001 处于持仓中（已买入成交）——userID 需与 HeldPositionCodesFor 过滤口径一致
	rpt := report.New(filepath.Join(t.TempDir(), "rpt.json"))
	rpt.LogSignalWithMetaQty("S1", "600001", "测试", "做多", "n_shape", 10, 0, 0, 100, nil)
	e.rpt = rpt
	e.userID = ""

	md := map[string]*strategy_engine.StockMarketData{
		"600001": {Code: "600001", Price: 9.5, Quote: &data.StockInfo{Price: 9.5}},
	}
	e.invalidateBrokenSignals(md, nil)

	if len(e.signalStore.List()) != 1 {
		t.Fatalf("已持有持仓的信号不应被墓碑移除, got %+v", e.signalStore.List())
	}
	if e.signalStore.IsInvalidated("600001", "n_shape") {
		t.Fatal("已持有持仓的信号不应打失效墓碑")
	}
	for _, m := range e.msgStore.List() {
		if m.ID == "600001@交易信号@n_shape" {
			return // 消息中心条目保留 ✓
		}
	}
	t.Fatal("已持有持仓的信号消息中心条目应保留")
}

// TestSignalStoreClearDay 验证交易日滚动清空：ClearDay 移除全部固化信号与墓碑，
// 并落盘为当前交易日空桶（重载后不再带旧信号）。
// English: TestSignalStoreClearDay verifies the trading-day rollover: ClearDay removes every pinned
// signal and tombstone and persists an empty current-day bucket (a reload no longer sees stale signals).
func TestSignalStoreClearDay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "signals.json")
	s := newSignalStore(path)
	at := time.Now()
	s.Upsert([]combat_agent.Signal{
		mkBuySig("600001", "n_shape", at),
		mkBuySig("600002", "dragon", at),
	})
	s.Invalidate("600002", "dragon")
	if len(s.List()) != 1 {
		t.Fatalf("清空前应有 1 条信号, got %d", len(s.List()))
	}

	s.ClearDay()
	if len(s.List()) != 0 {
		t.Fatalf("ClearDay 后固化列表应为空, got %+v", s.List())
	}
	if s.IsInvalidated("600002", "dragon") {
		t.Fatal("ClearDay 后墓碑应一并清空")
	}

	// 重载验证：文件已带当前交易日空桶，旧信号不再复活
	s2 := newSignalStore(path)
	if len(s2.List()) != 0 {
		t.Fatalf("重载后不应有旧信号, got %+v", s2.List())
	}
}

// TestEngineRolloverDayStores 验证跨日清理：昨日固化信号与信号批次记录在新交易日首轮即被清空，
// 同日重复巡检是幂等空操作。
// English: TestEngineRolloverDayStores verifies the cross-day cleanup: yesterday's pinned signals and
// the batch log are cleared on the first cycle of the new trading day, and a same-day re-check is a no-op.
func TestEngineRolloverDayStores(t *testing.T) {
	e := invalidateTestEngine()
	e.storeDay = "20260907" // 模拟上一个交易日（与真实当前日不同 → 应触发清空）
	at := time.Now()
	e.signalStore.Upsert([]combat_agent.Signal{mkBuySig("600001", "n_shape", at)})
	e.signalRecords = append(e.signalRecords, combat_agent.SignalLog{ProcessTime: at})

	e.RolloverDayStores()
	if len(e.signalStore.List()) != 0 {
		t.Fatalf("跨日首轮应清空昨日固化信号, got %+v", e.signalStore.List())
	}
	if len(e.signalRecords) != 0 {
		t.Fatalf("跨日首轮应清空信号批次记录, got %d", len(e.signalRecords))
	}
	if e.storeDay == "20260907" {
		t.Fatal("storeDay 应已推进到当前交易日")
	}

	// 同日再次调用 → 幂等，不影响已固化内容
	day := e.storeDay
	e.signalStore.Upsert([]combat_agent.Signal{mkBuySig("600002", "dragon", time.Now())})
	e.RolloverDayStores()
	if e.storeDay != day {
		t.Fatalf("同日巡检不应改 storeDay: got %s want %s", e.storeDay, day)
	}
	if len(e.signalStore.List()) != 1 {
		t.Fatalf("同日巡检不应清空今日信号, got %+v", e.signalStore.List())
	}
}

// TestCountUniqueBuyCodes 验证 toast 计数口径：同票多战法去重只算一只，watch/brief 观察信号不计。
// English: TestCountUniqueBuyCodes verifies the toast-count semantics: a stock caught by several
// strategies counts once, and watch/brief observation signals are excluded.
func TestCountUniqueBuyCodes(t *testing.T) {
	at := time.Now()
	sigs := []combat_agent.Signal{
		mkBuySig("600001", "n_shape", at),
		mkBuySig("600001", "dragon", at), // 同一只票第二个战法 → 去重
		mkBuySig("600002", "dragon", at),
		{Code: "600003", Strategy: "momentum", Action: "watch", GeneratedAt: at},             // 观察信号不计
		{Code: "600004", Strategy: "n_shape", Action: "buy", Direction: "", GeneratedAt: at}, // 无方向但 buy → 计
	}
	if n := countUniqueBuyCodes(sigs, "buy"); n != 3 {
		t.Fatalf("去重后应按 code 计 3 只, got %d", n)
	}
	if n := countAction(sigs, "buy"); n != 4 {
		t.Fatalf("原始计数应 4 条（含重复票）, got %d", n)
	}
}

// TestInvalidateBrokenSignalsSkipsShortAndMissing 验证：做空信号不受墓碑影响；
// 行情缺失/价格无效时跳过（不误删），留待下一轮有数据再判。
// English: TestInvalidateBrokenSignalsSkipsShortAndMissing verifies: short signals are not affected by the tombstone;
// missing quotes / invalid prices are skipped (no wrongful deletion), left to be judged next round when data is available.
func TestInvalidateBrokenSignalsSkipsShortAndMissing(t *testing.T) {
	e := invalidateTestEngine()
	at := time.Now()
	long := mkBuySig("600001", "n_shape", at)
	short := mkBuySig("600002", "n_shape", at)
	short.Direction = "做空"
	e.signalStore.Upsert([]combat_agent.Signal{long, short})

	// 600002 无行情（不在 md 中）；600001 有行情但价格无效
	// English: 600002 has no quotes (not in md); 600001 has quotes but an invalid price.
	md := map[string]*strategy_engine.StockMarketData{
		"600001": {Code: "600001", Quote: &data.StockInfo{Price: 0}},
	}
	e.invalidateBrokenSignals(md, nil)

	if len(e.signalStore.List()) != 2 {
		t.Fatalf("做空/无有效行情信号不应被移除, got %+v", e.signalStore.List())
	}
}
