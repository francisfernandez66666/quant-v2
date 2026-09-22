// strategy_merge_test.go — §N-4（2026-09-22 傍晚批 §CFGSMASH + §中-6 + 并发补强）行为锁。
//
// 锁四件事：
//  1. 稀疏 merge：MergeStrategyConfig 只更新 body 中出现的键（含嵌套逐字段），未出现的键
//     保留旧值——旧「typed 解码全量替换」把缺键折叠成 0 并落库，一次失败加载+一次保存=
//     五套战法阈值清零、重启救不回，本用例钉死该语义不再回退；
//  2. §中-6 乐观锁：baseVersion 不匹配回 ErrStrategyVersionConflict 且**不落盘**；
//     空 baseVersion=不比对（兼容脚本直 POST）；
//  3. 并发收口：所有 strategy 写路径持 m.mu、读路径走快照拷贝（GetStrategyConfig*），
//     「边保存边打分」在 `go test -race` 下必须无 race；
//  4. 全量 setter（内部程序化入口 optimizations）仍全量替换但同样盖版本戳。
//
// English: behavior locks for §N-4 — sparse merge keeps absent keys, mid-6 optimistic locking
// rejects stale writers without persisting, and the save-while-scoring -race case proves the
// setter/getter locking symmetry.
package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// seedStrategy 构造带非零基线的管理器：merge 用例必须先有"旧值"，缺键保留才有判据。
func seedStrategy(t *testing.T) (*Manager, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	m := NewManager(path)
	base := StrategyConfig{
		Dragon:       DragonConfig{F1SealWeight: 0.4, F2ResonanceWeight: 0.2, TakeProfitPct: 10, PullbackMaxPct: 0.05},
		DoubleBump:   DoubleBumpConfig{FirstBreakVolumeMultiple: 2.5, DoubleBumpTakeProfitPct: 0.08, MAWeight: 0.3},
		NShape:       NShapeConfig{NPatternScoreThreshold: 70, HardStopLoss: 0.05},
		DragonReturn: DragonReturnConfig{StopLossPct: 0.03, TakeProfitPct: 0.12, MaxHoldDays: 5},
		Momentum:     MomentumConfig{VolumePriceWeight: 40, MACDWeight: 30, TrendWeight: 30},
	}
	m.SetStrategyConfig(&base)
	return m, path
}

// TestMergeStrategyConfigSparse 稀疏 merge 核心断言：只写出现键、嵌套逐字段、缺省保留、
// 未知键丢弃、显式 0 仍可写 0（"清零"必须是明示意图而非缺失折叠）。
func TestMergeStrategyConfigSparse(t *testing.T) {
	m, path := seedStrategy(t)

	patch := map[string]json.RawMessage{
		// 嵌套对象只带一个键：同组其余字段（f1_seal_weight 等）必须保留旧值
		"dragon":   json.RawMessage(`{"take_profit_pct": 3.5}`),
		"unknown":  json.RawMessage(`{"whatever": 1}`),       // 未知顶层键：typed 反序列化自然丢弃
		"n_shape":  json.RawMessage(`{"hard_stop_loss": 0}`), // 显式 0：必须真的写 0
		"momentum": json.RawMessage(`{"volume_price_weight": 55}`),
	}
	merged, err := m.MergeStrategyConfig(patch, "")
	if err != nil {
		t.Fatalf("merge 失败: %v", err)
	}
	if merged.Dragon.TakeProfitPct != 3.5 {
		t.Fatalf("出现键应被更新, got %v", merged.Dragon.TakeProfitPct)
	}
	if merged.Dragon.F1SealWeight != 0.4 || merged.Dragon.PullbackMaxPct != 0.05 {
		t.Fatalf("§N-4：同组未出现的键必须保留旧值（旧全量替换在此会折叠成 0）, got %+v", merged.Dragon)
	}
	if merged.NShape.HardStopLoss != 0 || merged.NShape.NPatternScoreThreshold != 70 {
		t.Fatalf("显式 0 应写入、缺省键应保留, got %+v", merged.NShape)
	}
	if merged.Momentum.VolumePriceWeight != 55 || merged.Momentum.TrendWeight != 30 {
		t.Fatalf("momentum 逐字段合并失败, got %+v", merged.Momentum)
	}
	if merged.DoubleBump.FirstBreakVolumeMultiple != 2.5 || merged.DoubleBump.MAWeight != 0.3 {
		t.Fatalf("§N-4：body 整组缺失时该组必须原样保留, got %+v", merged.DoubleBump)
	}
	if merged.UpdatedAt == "" {
		t.Fatal("§中-6：merge 写必须盖 updated_at 版本戳")
	}
	// 落盘一致性：merge 后重开管理器读回的必须是合并结果（已持久化，不落内存假成功）。
	reopened := NewManager(path)
	if got := reopened.GetStrategyConfig(); got.Dragon.TakeProfitPct != 3.5 || got.Dragon.F1SealWeight != 0.4 {
		t.Fatalf("落盘后重载应与内存一致, got %+v", got.Dragon)
	}
	if strings.Contains(readFileOr(t, path), `"unknown"`) {
		t.Fatal("未知顶层键必须被丢弃，不得污染 config.json")
	}
}

// TestMergeStrategyConfigVersionConflict §中-6：后写不得静默覆盖前写。
func TestMergeStrategyConfigVersionConflict(t *testing.T) {
	m, _ := seedStrategy(t)

	v1, err := m.MergeStrategyConfig(map[string]json.RawMessage{"dragon": json.RawMessage(`{"take_profit_pct": 8}`)}, "")
	if err != nil {
		t.Fatalf("首写应成功: %v", err)
	}
	// 用旧基线（空/过期）再写：管理员 A 先落一步后，管理员 B 带旧 updated_at 必须 409 语义。
	stale := v1.UpdatedAt + "-stale"
	merged, err := m.MergeStrategyConfig(map[string]json.RawMessage{"dragon": json.RawMessage(`{"take_profit_pct": 1}`)}, stale)
	if !errors.Is(err, ErrStrategyVersionConflict) {
		t.Fatalf("版本不匹配应回 ErrStrategyVersionConflict, got %v", err)
	}
	// 冲突时返回当前快照（供 409 响应回传 current_updated_at），且**没有**覆盖成 B 的值。
	if merged.Dragon.TakeProfitPct != 8 {
		t.Fatalf("冲突写入不得改动服务端值, got %v", merged.Dragon.TakeProfitPct)
	}
	// body 里携带的 updated_at 不参与 merge（服务端所有）：即便被放进 patch 也不得回退版本戳。
	withVer := map[string]json.RawMessage{
		"dragon":     json.RawMessage(`{"take_profit_pct": 9}`),
		"updated_at": json.RawMessage(`"2020-01-01T00:00:00Z"`),
	}
	v2, err := m.MergeStrategyConfig(withVer, "")
	if err != nil {
		t.Fatalf("不比对模式应成功: %v", err)
	}
	if v2.UpdatedAt == stale || v2.UpdatedAt == "2020-01-01T00:00:00Z" {
		t.Fatalf("updated_at 必须由服务端盖戳, got %q", v2.UpdatedAt)
	}
	// 新基线一致 → 正常通过
	if _, err := m.MergeStrategyConfig(map[string]json.RawMessage{"dragon": json.RawMessage(`{"take_profit_pct": 10}`)}, v2.UpdatedAt); err != nil {
		t.Fatalf("版本一致的写应成功: %v", err)
	}
}

// TestSetStrategyConfigFullReplaceStamps 内部全量入口保留「缺键=清零」语义（该入口由程序
// 构造完整快照后落盘），但同样必须盖版本戳 + 持锁。
func TestSetStrategyConfigFullReplaceStamps(t *testing.T) {
	m, _ := seedStrategy(t)
	before := m.GetStrategyConfig().UpdatedAt
	next := StrategyConfig{Dragon: DragonConfig{TakeProfitPct: 1}}
	m.SetStrategyConfig(&next)
	got := m.GetStrategyConfig()
	if got.DoubleBump.FirstBreakVolumeMultiple != 0 {
		t.Fatal("SetStrategyConfig 应保持全量替换语义（内部程序化入口专用）")
	}
	if got.UpdatedAt == "" || got.UpdatedAt == before {
		t.Fatalf("全量写也必须推进版本戳, before=%q after=%q", before, got.UpdatedAt)
	}
}

// TestStrategyConfigSaveWhileScoringRace 「边保存边打分」-race 行为锁（§N-4 三轮补强）：
// 写侧并发 merge，读侧走打分路径同款快照 getter（GetStrategyConfig/GetStrategyConfigFor/
// StrategyConfigSnapshot，均含值拷贝）。旧实现 setter 裸写不持锁 + getter 返回活体指针，
// 本用例在 `go test -race` 下必红；修复后必须无 race 且每轮读到的都是完整一致快照。
// English: the save-while-scoring -race lock — writers merge concurrently while scorer goroutines
// read snapshots; every read must observe a fully consistent (non-torn) snapshot.
func TestStrategyConfigSaveWhileScoringRace(t *testing.T) {
	m, _ := seedStrategy(t)
	var writers, readers sync.WaitGroup
	stop := make(chan struct{})

	// 写侧：两个管理员节奏并发 merge 不同字段（模拟盘中保存）
	for i := 0; i < 4; i++ {
		writers.Add(1)
		go func(i int) {
			defer writers.Done()
			for n := 0; n < 50; n++ {
				patch := map[string]json.RawMessage{
					"dragon":   json.RawMessage(`{"take_profit_pct": 7.5}`),
					"momentum": json.RawMessage(`{"macd_weight": 33}`),
				}
				if _, err := m.MergeStrategyConfig(patch, ""); err != nil {
					t.Errorf("并发 merge 失败: %v", err)
					return
				}
			}
		}(i)
	}
	// 读侧：模拟打分循环每 tick 取快照（引擎各战法 runner 的读形）
	for i := 0; i < 4; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				sc := m.GetStrategyConfig()
				// 一致性抽查：take_profit_pct 只可能是 10（种子）或 7.5（merge 值），
				// 读到撕裂中间态/零值即说明仍共享了活体内存。
				v := sc.Dragon.TakeProfitPct
				if v != 10 && v != 7.5 {
					t.Errorf("打分读到撕裂的策略快照: take_profit_pct=%v", v)
					return
				}
				snap := m.StrategyConfigSnapshot()
				if snap.Dragon.TakeProfitPct != 10 && snap.Dragon.TakeProfitPct != 7.5 {
					t.Errorf("快照读撕裂: %+v", snap.Dragon)
					return
				}
			}
		}()
	}
	// 写侧全部落定后叫停读侧（两组 WaitGroup 分开，避免读者自锁等不到的死锁）
	writers.Wait()
	close(stop)
	readers.Wait()
	if m.GetStrategyConfig().Dragon.TakeProfitPct != 7.5 {
		t.Fatal("并发写终态应为 merge 值")
	}
}

// readFileOr 读文件内容（断言未知键未污染落盘 JSON）。
func readFileOr(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读配置文件失败: %v", err)
	}
	return string(data)
}
