// 本文件：近实时打分循环相关单元测试——信号状态翻转去重（filterTransitionSignals）与
// 8a/8b 打分持久化（scoreStore）的读写、容错、跨实例加载。
// English: This file: unit tests for the near-realtime scoring loop — signal state transition dedup (filterTransitionSignals) and 8a/8b score persistence (scoreStore): read/write, fault tolerance, and cross-instance loading.
package engine

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/data"
)

// mkSig 构造一个仅含 code 与 strategy 的最小信号对象（其余字段留零值即可）。
// English: mkSig builds a minimal signal object containing only code and strategy (other fields left at zero values).
func mkSig(code, strategy string) combat_agent.Signal {
	return combat_agent.Signal{Code: code, Strategy: strategy, GeneratedAt: time.Now()}
}

// TestFilterTransitionSignals 验证状态翻转去重语义：
// 首次出现即广播；持续 Pass（同 code+strategy）不重发；翻回非 Pass 后再翻上会再次广播。
// English: TestFilterTransitionSignals verifies state transition dedup semantics: the first occurrence broadcasts immediately; a sustained Pass (same code+strategy) is not re-emitted; flipping back to non-Pass and then up again broadcasts once more.
func TestFilterTransitionSignals(t *testing.T) {
	sigA := mkSig("600000", "dragon")
	sigB := mkSig("600000", "n_shape")
	sigC := mkSig("000001", "dragon")

	// 首次出现 → 全发
	// English: First occurrence → broadcast all.
	emit, next := filterTransitionSignals([]combat_agent.Signal{sigA, sigC}, nil)
	if len(emit) != 2 {
		t.Fatalf("首次应发2条, got %d", len(emit))
	}
	// 持续 Pass（同 code+strategy）→ 不重发
	// English: Sustained Pass (same code+strategy) → no re-emission.
	emit2, next2 := filterTransitionSignals([]combat_agent.Signal{sigA, sigC}, next)
	if len(emit2) != 0 {
		t.Fatalf("持续Pass不应重发, got %d", len(emit2))
	}
	_ = next2

	// 翻转回去再翻上 → 再发（strategy 从 next 里消失后）
	// 模拟某战法不再 Pass：本轮该 code 只有 sigB
	// English: Flip back then up again → re-emit (after the strategy disappears from next).
	// English: Simulate a strategy no longer passing: this round the code only has sigB.
	emit3, _ := filterTransitionSignals([]combat_agent.Signal{sigB}, next)
	if len(emit3) != 1 {
		t.Fatalf("新strategy翻上应发1条, got %d", len(emit3))
	}
	// sigB 持续后不再发
	// English: After sigB is sustained, it is not re-emitted.
	_, nextB := filterTransitionSignals([]combat_agent.Signal{sigB}, next)
	emit4, _ := filterTransitionSignals([]combat_agent.Signal{sigB}, nextB)
	if len(emit4) != 0 {
		t.Fatalf("sigB持续Pass不应重发, got %d", len(emit4))
	}
	// sigA 从状态中消失后再翻上 → 再发
	// English: After sigA disappears from the state and flips back up → re-emit.
	emit5, _ := filterTransitionSignals([]combat_agent.Signal{sigA}, nextB)
	if len(emit5) != 1 {
		t.Fatalf("sigA翻回再翻上应再发, got %d", len(emit5))
	}
}

// TestScoreStoreRoundTrip 验证打分存储的写入→新实例加载全链路：
// 写入后从磁盘新建实例应能还原全部字段；Save(nil) 不应 panic。
// English: TestScoreStoreRoundTrip verifies the full write→new-instance-load chain of score storage: a new instance created from disk after writing should restore all fields; Save(nil) should not panic.
func TestScoreStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "scores.json")

	s := newScoreStore(path)
	day := data.TradingDayDate(time.Now()) // §P2#19：仅当日的桶会被加载（跨日重置）
	in := map[string]combat_agent.StockScores{
		"600000": {Code: "600000", NScore: 66, DragonScore: 80, MomentumScore: 75, SignalActive: true, UpdatedAt: time.Now()},
		"000001": {Code: "000001", DragonScore: 0, MomentumScore: 0},
	}
	s.Save(day, in)

	// 新实例从磁盘加载
	// English: New instance loads from disk.
	s2 := newScoreStore(path)
	loaded := s2.Load()
	if len(loaded) != 2 {
		t.Fatalf("加载应2只, got %d", len(loaded))
	}
	if loaded["600000"].NScore != 66 || loaded["600000"].MomentumScore != 75 || !loaded["600000"].SignalActive {
		t.Fatalf("加载数据不一致: %+v", loaded["600000"])
	}

	// Save(nil) 不 panic
	// English: Save(nil) does not panic.
	s.Save(day, nil)
}

// TestScoreStoreNoPath 验证未配置路径（纯内存模式）时 Save/Load 均可安全调用。
// English: TestScoreStoreNoPath verifies Save/Load are both safe to call when no path is configured (pure in-memory mode).
func TestScoreStoreNoPath(t *testing.T) {
	s := newScoreStore("")
	s.Save("2026-07-31", map[string]combat_agent.StockScores{})
	if len(s.Load()) != 0 {
		t.Fatalf("无路径应空")
	}
}

// TestScoreStoreCorruptFile 验证 scores.json 损坏时加载不 panic 且返回空结果。
// English: TestScoreStoreCorruptFile verifies loading a corrupt scores.json does not panic and returns an empty result.
func TestScoreStoreCorruptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "scores.json")
	if err := os.WriteFile(path, []byte("{broken"), 0644); err != nil {
		t.Fatal(err)
	}
	s := newScoreStore(path) // 不 panic
	// English: No panic.
	if len(s.Load()) != 0 {
		t.Fatalf("损坏文件应返回空")
	}
}

// TestScoreStoreSeparation §P0-8 主循环与 5s 循环打分持久化分池：
// fastScoreStore 与主循环 scoreStore 使用不同文件，互不覆盖。
// English: P0-8 main-loop and 5s-loop score persistence must use separate pools/files.
func TestScoreStoreSeparation(t *testing.T) {
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "scores.json")
	fastPath := filepath.Join(dir, "scores_fast.json")
	mainStore := newScoreStore(mainPath)
	fastStore := newScoreStore(fastPath)

	mainScores := map[string]combat_agent.StockScores{"600000.SH": {NScore: 88}}
	fastScores := map[string]combat_agent.StockScores{"600000.SH": {NScore: 77}, "000001.SZ": {NScore: 66}}

	mainStore.Save("2026-08-28", mainScores)
	fastStore.Save("2026-08-28", fastScores)

	if got := mainStore.Load(); len(got) != 1 || got["600000.SH"].NScore != 88 {
		t.Fatalf("主循环分池被覆盖，got %+v", got)
	}
	if got := fastStore.Load(); len(got) != 2 || got["600000.SH"].NScore != 77 {
		t.Fatalf("5s 循环分池被覆盖，got %+v", got)
	}
}

// TestCountAction 验证 countAction 仅统计指定 Action 的信号（用于 SSE 通知只计 buy）。
// 做多信号里 buy / watch / brief 混合时，只统计 buy。
// English: TestCountAction verifies countAction only counts signals of the specified Action (used so SSE notifications only count buy). When buy / watch / brief are mixed among long signals, only buy is counted.
func TestCountAction(t *testing.T) {
	sigs := []combat_agent.Signal{
		{Action: "buy"}, // 全链买入 → 计入
		// English: Full-chain buy → counted.
		{Action: "watch"}, // 观察 → 不计
		// English: Watch → not counted.
		{Action: "buy"}, // 计入
		// English: Counted.
		{Action: "brief"}, // 半确认 → 不计
		// English: Half-confirmed → not counted.
		{Action: "sell"}, // 其它动作 → 不计
		// English: Other actions → not counted.
	}
	if n := countAction(sigs, "buy"); n != 2 {
		t.Fatalf("应只统计 2 条 buy, got %d", n)
	}
	if n := countAction(nil, "buy"); n != 0 {
		t.Fatalf("空列表应为 0, got %d", n)
	}
}
