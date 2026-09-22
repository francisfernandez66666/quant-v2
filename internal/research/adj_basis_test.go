// 文件职责：§ADJ-BASIS 回归测试——断点（resume_key）必须携带日K复权口径位。
//
// 背景（2026-09-23 本机 A/B 锤实）：resume_key 原本只含区间/参数/股票池，不含"数值口径"。
// §ADJ(P0-A 复权因子前向填充) 改的是 SQL 语义、不改任何入参，于是夜间链当晚照旧逐窗命中
// 改前装配好的面板并跳过重算——新基线形同没上，历史研究结论也不会被覆盖。本测试把
// "口径进 key" 钉成契约：以后任何人再改复权口径而忘记 bump AdjBaselineVersion，
// 至少不会把 key 整个删掉；而 key 里少了口径位这件事会被当场判红。
package research

import (
	"path/filepath"
	"strings"
	"testing"

	"quant-trading-v2/internal/store"
)

// TestDiscoveryResumeKeyCarriesAdjBasis 因子发现断点键必须含口径位，且同参稳定。
func TestDiscoveryResumeKeyCarriesAdjBasis(t *testing.T) {
	codes := []string{"000001.SZ", "600000.SH"}
	fids := []string{"Mom20", "Brk60"}
	k := discoveryResumeKey("20230801", "20260922", 5, 10, 60, fids, codes, nil)
	if !strings.Contains(k, adjBasisTag) {
		t.Fatalf("因子发现断点键丢失复权口径位（改前/改后面板会被混用）: %s", k)
	}
	if !strings.Contains(k, AdjBaselineVersion) {
		t.Fatalf("断点键口径位与常量脱钩: %s 应含 %s", k, AdjBaselineVersion)
	}
	// 同参必须同键：口径位是常量，不得掺入时间/随机量，否则每晚都全量重算（本末倒置）。
	if k2 := discoveryResumeKey("20230801", "20260922", 5, 10, 60, fids, codes, nil); k2 != k {
		t.Fatalf("同参两次生成的断点键不相等（口径位掺入了不稳定量）:\n %s\n %s", k, k2)
	}
	// 参数变更仍须换键（口径位不得取代原有失效语义）。
	if k3 := discoveryResumeKey("20230801", "20260922", 10, 10, 60, fids, codes, nil); k3 == k {
		t.Fatal("horizon 变更未换断点键")
	}
}

// TestCkptRotationOnBasisBump 复现"改前断点不会被新口径复用"这条真实语义：
// 用**不含口径位的旧键**写入一份窗口产物，再按当前键读取必须落空（否则会静默沿用改前面板），
// 而按当前键自写自读必须命中（证明键轮换不会把有效缓存也一起打掉、导致每晚全量重算）。
func TestCkptRotationOnBasisBump(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "ckpt.db"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	defer db.Close()

	codes := []string{"000001.SZ", "600000.SH"}
	fids := []string{"Mom20"}
	rk := discoveryResumeKey("20230801", "20260922", 5, 10, 60, fids, codes, nil)
	legacy := strings.TrimSuffix(rk, adjBasisTag) // §ADJ 之前那代键
	if legacy == rk {
		t.Fatalf("当前断点键不含口径位，轮换无从谈起: %s", rk)
	}
	win := []string{"20240102", "20240331"}

	if err := db.PutWindowCkpt(legacy, "gen", win[0], win[1], `{"stale":true}`); err != nil {
		t.Fatalf("写旧键断点失败: %v", err)
	}
	if _, hit, err := db.GetWindowCkpt(rk, "gen", win[0], win[1]); err != nil || hit {
		t.Fatalf("新口径键竟复用了改前断点（hit=%v err=%v）——口径变更不会重算，历史结论无法覆盖", hit, err)
	}
	if err := db.PutWindowCkpt(rk, "gen", win[0], win[1], `{"fresh":true}`); err != nil {
		t.Fatalf("写新键断点失败: %v", err)
	}
	got, hit, err := db.GetWindowCkpt(rk, "gen", win[0], win[1])
	if err != nil || !hit || got != `{"fresh":true}` {
		t.Fatalf("新键自写自读未命中（hit=%v got=%q err=%v）——键含不稳定量，每晚都会全量重算", hit, got, err)
	}
}
