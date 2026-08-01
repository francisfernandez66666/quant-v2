// 本文件：近实时打分循环相关单元测试——信号状态翻转去重（filterTransitionSignals）与
// 8a/8b 打分持久化（scoreStore）的读写、容错、跨实例加载。
package engine

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"quant-trading-v2/internal/combat_agent"
)

// mkSig 构造一个仅含 code 与 strategy 的最小信号对象（其余字段留零值即可）。
func mkSig(code, strategy string) combat_agent.Signal {
	return combat_agent.Signal{Code: code, Strategy: strategy, GeneratedAt: time.Now()}
}

// TestFilterTransitionSignals 验证状态翻转去重语义：
// 首次出现即广播；持续 Pass（同 code+strategy）不重发；翻回非 Pass 后再翻上会再次广播。
func TestFilterTransitionSignals(t *testing.T) {
	sigA := mkSig("600000", "dragon")
	sigB := mkSig("600000", "n_shape")
	sigC := mkSig("000001", "dragon")

	// 首次出现 → 全发
	emit, next := filterTransitionSignals([]combat_agent.Signal{sigA, sigC}, nil)
	if len(emit) != 2 {
		t.Fatalf("首次应发2条, got %d", len(emit))
	}
	// 持续 Pass（同 code+strategy）→ 不重发
	emit2, next2 := filterTransitionSignals([]combat_agent.Signal{sigA, sigC}, next)
	if len(emit2) != 0 {
		t.Fatalf("持续Pass不应重发, got %d", len(emit2))
	}
	_ = next2

	// 翻转回去再翻上 → 再发（strategy 从 next 里消失后）
	// 模拟某战法不再 Pass：本轮该 code 只有 sigB
	emit3, _ := filterTransitionSignals([]combat_agent.Signal{sigB}, next)
	if len(emit3) != 1 {
		t.Fatalf("新strategy翻上应发1条, got %d", len(emit3))
	}
	// sigB 持续后不再发
	_, nextB := filterTransitionSignals([]combat_agent.Signal{sigB}, next)
	emit4, _ := filterTransitionSignals([]combat_agent.Signal{sigB}, nextB)
	if len(emit4) != 0 {
		t.Fatalf("sigB持续Pass不应重发, got %d", len(emit4))
	}
	// sigA 从状态中消失后再翻上 → 再发
	emit5, _ := filterTransitionSignals([]combat_agent.Signal{sigA}, nextB)
	if len(emit5) != 1 {
		t.Fatalf("sigA翻回再翻上应再发, got %d", len(emit5))
	}
}

// TestScoreStoreRoundTrip 验证打分存储的写入→新实例加载全链路：
// 写入后从磁盘新建实例应能还原全部字段；Save(nil) 不应 panic。
func TestScoreStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "scores.json")

	s := newScoreStore(path)
	day := "2026-07-31"
	in := map[string]combat_agent.StockScores{
		"600000": {Code: "600000", NScore: 66, DragonScore: 80, MomentumScore: 75, SignalActive: true, UpdatedAt: time.Now()},
		"000001": {Code: "000001", DragonScore: 0, MomentumScore: 0},
	}
	s.Save(day, in)

	// 新实例从磁盘加载
	s2 := newScoreStore(path)
	loaded := s2.Load()
	if len(loaded) != 2 {
		t.Fatalf("加载应2只, got %d", len(loaded))
	}
	if loaded["600000"].NScore != 66 || loaded["600000"].MomentumScore != 75 || !loaded["600000"].SignalActive {
		t.Fatalf("加载数据不一致: %+v", loaded["600000"])
	}

	// Save(nil) 不 panic
	s.Save(day, nil)
}

// TestScoreStoreNoPath 验证未配置路径（纯内存模式）时 Save/Load 均可安全调用。
func TestScoreStoreNoPath(t *testing.T) {
	s := newScoreStore("")
	s.Save("2026-07-31", map[string]combat_agent.StockScores{})
	if len(s.Load()) != 0 {
		t.Fatalf("无路径应空")
	}
}

// TestScoreStoreCorruptFile 验证 scores.json 损坏时加载不 panic 且返回空结果。
func TestScoreStoreCorruptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "scores.json")
	if err := os.WriteFile(path, []byte("{broken"), 0644); err != nil {
		t.Fatal(err)
	}
	s := newScoreStore(path) // 不 panic
	if len(s.Load()) != 0 {
		t.Fatalf("损坏文件应返回空")
	}
}
