// Package engine 核心引擎：信号生产、打分池、板块传播、持仓退出、通知推送与 QMT 自动交易的 orchestration。
package engine

// 本文件：信号固化存储（SignalStore）与失效墓碑链路的单测。
// 覆盖行为：①墓碑=移出固化列表且同日同 key 不复活；②墓碑只作用于单 code@单战法；
// ③墓碑跨重启持久化（重载后仍拦截）；④现价跌破触发价自动失效 + 删除消息中心条目；
// ⑤已持有持仓豁免（浮亏≠买入依据破坏）；⑥交易日滚动清空（ClearDay/跨日首轮）；
// ⑦手动忽略（IgnoreSignal）与自动墓碑同机制；⑧toast 计数口径（去重/排除观察类）。
// 构造思想：全部用内存存储/最小 Engine 装配 + 手造 Signal，不依赖真实行情与网络。

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
	// Arrange：内存存储（""=不落盘；本用例只关心进程内行为，不关心持久化）
	s := newSignalStore("")
	at := time.Now()

	// Act ①：固化一条 600001@n_shape 信号
	s.Upsert([]combat_agent.Signal{mkBuySig("600001", "n_shape", at)})
	if len(s.List()) != 1 { // Assert ①：固化后列表应有 1 条
		t.Fatalf("固化后应有 1 条信号, got %d", len(s.List()))
	}

	// Act ②：对同一 key 打失效墓碑
	s.Invalidate("600001", "n_shape")
	if len(s.List()) != 0 { // Assert ②：墓碑即移出固化列表（当日不再展示）
		t.Fatalf("打墓碑后固化列表应清空, got %+v", s.List())
	}
	if !s.IsInvalidated("600001", "n_shape") { // Assert ③：墓碑记录留底，供后续 Upsert 拦截
		t.Fatal("打墓碑后应记录失效标记")
	}

	// 同日再次出现同一 key 的 Pass 信号 → 被墓碑拦截，不复活
	// （为什么这样构造：翻转去重机制下信号会反复重评，墓碑必须压过同日重评，否则失效信号"打不掉"）
	// English: A Pass signal for the same key reappearing the same day → blocked by the tombstone, not revived.
	s.Upsert([]combat_agent.Signal{mkBuySig("600001", "n_shape", at.Add(10*time.Second))})
	if len(s.List()) != 0 {
		t.Fatalf("墓碑后同 key 不应复活, got %+v", s.List())
	}
}

// TestSignalStoreInvalidateOtherKeysUnaffected 验证墓碑只作用单只股票单战法，其他信号不受影响。
// English: TestSignalStoreInvalidateOtherKeysUnaffected verifies the tombstone only affects a single stock and single strategy; other signals are unaffected.
func TestSignalStoreInvalidateOtherKeysUnaffected(t *testing.T) {
	// Arrange：两条同战法不同 code 的信号并存——验证墓碑键的"code 隔离性"
	s := newSignalStore("")
	at := time.Now()
	s.Upsert([]combat_agent.Signal{
		mkBuySig("600001", "n_shape", at),
		mkBuySig("600002", "n_shape", at),
	})

	// Act：只对 600001 打墓碑
	s.Invalidate("600001", "n_shape")

	list := s.List() // Assert：列表只剩 600002（600002@n_shape 不受牵连）
	if len(list) != 1 || list[0].Code != "600002" {
		t.Fatalf("墓碑应只移除 600001@n_shape, got %+v", list)
	}
}

// TestSignalStoreInvalidatePersistsAcrossReload 验证墓碑跨重启持久化：重载后同日仍不复活。
// English: TestSignalStoreInvalidatePersistsAcrossReload verifies the tombstone persists across restarts: same day still not revived after reload.
func TestSignalStoreInvalidatePersistsAcrossReload(t *testing.T) {
	// Arrange：带真实文件路径的存储——本用例专门验证墓碑持久化路径
	path := filepath.Join(t.TempDir(), "signals.json")
	at := time.Now()

	// Act ①：实例 s1 固化一条信号并立即打墓碑（不显式 delete 文件，全靠存储自身落盘）
	s1 := newSignalStore(path)
	s1.Upsert([]combat_agent.Signal{mkBuySig("600001", "n_shape", at)})
	s1.Invalidate("600001", "n_shape")

	// Act ②：从同一文件重新加载出实例 s2（等价进程重启恢复）
	s2 := newSignalStore(path)
	if !s2.IsInvalidated("600001", "n_shape") { // Assert ①：墓碑标记必须随文件存活
		t.Fatal("重载后应保留失效墓碑标记")
	}
	s2.Upsert([]combat_agent.Signal{mkBuySig("600001", "n_shape", at.Add(10*time.Second))})
	if len(s2.List()) != 0 { // Assert ②：重载后同日同 key 仍被拦截（墓碑语义跨重启不变）
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
	// Arrange：固化一条触发价 10.0 的买入信号，并在消息中心造同 key 条目——
	// 证明失效要"双清理"：固化列表 + 消息中心，缺一用户仍能看到死信号。
	e := invalidateTestEngine()
	at := time.Now()
	e.signalStore.Upsert([]combat_agent.Signal{mkBuySig("600001", "n_shape", at)})
	e.msgStore.Sync([]data.MessageItem{{ID: "600001@交易信号@n_shape", Code: "600001"}})

	// Act：注入现价 9.5 的行情（< 触发价 10.0，买入依据破坏）并跑失效判定
	// 为什么构造 9.5：严格低于触发价且价差明显，避免边界浮点歧义。
	md := map[string]*strategy_engine.StockMarketData{
		"600001": {Code: "600001", Price: 9.5, Quote: &data.StockInfo{Price: 9.5}},
	}
	e.invalidateBrokenSignals(md, nil)

	// Assert：①固化列表清空 ②留墓碑标记 ③消息中心同 key 条目被删
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

	// Act：注入现价 10.5（> 触发价 10.0，信号前提依然成立）
	md := map[string]*strategy_engine.StockMarketData{
		"600001": {Code: "600001", Price: 10.5, Quote: &data.StockInfo{Price: 10.5}},
	}
	e.invalidateBrokenSignals(md, nil)

	// Assert：信号保留且不留墓碑（上一用例的镜像）
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

	// Act：注入现价 9.5 的行情——若不豁免，这将命中"跌破触发价"墓碑分支
	md := map[string]*strategy_engine.StockMarketData{
		"600001": {Code: "600001", Price: 9.5, Quote: &data.StockInfo{Price: 9.5}},
	}
	e.invalidateBrokenSignals(md, nil)

	// Assert：固化信号、墓碑标记、消息中心条目三者全都原样保留
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
	// Arrange：固化两条信号（600001@n_shape 有效性未知、600002@dragon 已打墓碑）
	path := filepath.Join(t.TempDir(), "signals.json")
	s := newSignalStore(path)
	at := time.Now()
	s.Upsert([]combat_agent.Signal{
		mkBuySig("600001", "n_shape", at),
		mkBuySig("600002", "dragon", at),
	})
	s.Invalidate("600002", "dragon")
	if len(s.List()) != 1 { // 断言基线：1 条有效 + 1 条被墓碑
		t.Fatalf("清空前应有 1 条信号, got %d", len(s.List()))
	}

	// Act：执行交易日滚动清空
	s.ClearDay()
	if len(s.List()) != 0 { // Assert ①：有效信号被清空
		t.Fatalf("ClearDay 后固化列表应为空, got %+v", s.List())
	}
	if s.IsInvalidated("600002", "dragon") { // Assert ②：墓碑标记也一并清空（次日允许同信号再次发有效版）
		t.Fatal("ClearDay 后墓碑应一并清空")
	}

	// 重载验证：文件已带当前交易日空桶，旧信号不再复活
	// （ClearDay 必须落盘而非仅清内存，否则重启后昨日信号又冒出来）
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
	// Act：把 storeDay 伪造成昨日 → 触发跨日清理路径
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
	// Arrange：同票双战法（600001 被 n_shape+dragon 同时扫中 → 必须只算 1 只）、
	// 另一只独立票、一条 watch 观察信号（不计）、一条无方向但 action=buy 的信号（仍计）。
	// 构造动机：精确锁定"去重口径 + 只数可操作买入"两个计数控件各自的边界。
	at := time.Now()
	sigs := []combat_agent.Signal{
		mkBuySig("600001", "n_shape", at),
		mkBuySig("600001", "dragon", at), // 同一只票第二个战法 → 去重
		mkBuySig("600002", "dragon", at),
		{Code: "600003", Strategy: "momentum", Action: "watch", GeneratedAt: at},             // 观察信号不计
		{Code: "600004", Strategy: "n_shape", Action: "buy", Direction: "", GeneratedAt: at}, // 无方向但 buy → 计
	}
	// Assert：去重口径 3 只 vs 原始计数 4 条（两条口径并存，防止有人把 countAction 的旧口径改回来）
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
	// Act：执行失效判定（nil d1Scores——本用例只走价格分支，不涉及 N 形 D1 逻辑）
	e.invalidateBrokenSignals(md, nil)

	// Assert：两条信号原地保留（做空方向豁免 + 行情无效跳过），不误删任何一条
	if len(e.signalStore.List()) != 2 {
		t.Fatalf("做空/无有效行情信号不应被移除, got %+v", e.signalStore.List())
	}
}

// TestIgnoreSignalTombstones §F-1（20260917 修复批）：用户手动忽略 = 与自动失效同款墓碑机制——
// 带 strategy 精确忽略一条；strategy 为空忽略该 code 当日全部；空 code 无操作。
// 断言同步覆盖消息中心条目移除（忽略后消息中心不再残留该信号）。
func TestIgnoreSignalTombstones(t *testing.T) {
	// Arrange：同 code（600002）两条不同战法的固化信号 + 两条对应消息中心条目
	e := invalidateTestEngine()
	at := time.Now()
	e.signalStore.Upsert([]combat_agent.Signal{mkBuySig("600002", "n_shape", at), mkBuySig("600002", "dragon", at)})
	e.msgStore.Sync([]data.MessageItem{
		{ID: "600002@交易信号@n_shape", Code: "600002"},
		{ID: "600002@交易信号@dragon", Code: "600002"},
	})
	// Act ①：带 strategy 精确忽略 n_shape
	if n := e.IgnoreSignal("600002", "n_shape"); n != 1 { // Assert ①：恰好墓碑 1 条
		t.Fatalf("指定战法忽略应返回 1, got %d", n)
	}
	if got := len(e.signalStore.List()); got != 1 || e.signalStore.List()[0].Strategy != "dragon" { // Assert ②：其余战法不受牵连
		t.Fatalf("n_shape 应被墓碑、dragon 保留, got %+v", e.signalStore.List())
	}
	if !e.signalStore.IsInvalidated("600002", "n_shape") {
		t.Fatal("n_shape 应打墓碑")
	}
	// Act ②：strategy 为空 → 忽略该 code 当日全部剩余信号
	if n := e.IgnoreSignal("600002", ""); n != 1 {
		t.Fatalf("空战法应忽略该 code 剩余 1 条, got %d", n)
	}
	if len(e.signalStore.List()) != 0 {
		t.Fatalf("该 code 信号应清空, got %+v", e.signalStore.List())
	}
	// Assert ③：消息中心 600002 的条目随忽略一并移除（忽略后前端不再残留卡片）
	for _, m := range e.msgStore.List() {
		if m.Code == "600002" {
			t.Fatalf("消息中心 600002 条目应随忽略移除, got %+v", m)
		}
	}
	// Act ④ + Assert ④：空 code 无操作返回 0（防御脏输入）
	if e.IgnoreSignal("", "x") != 0 {
		t.Fatal("空 code 应为无操作")
	}
}
