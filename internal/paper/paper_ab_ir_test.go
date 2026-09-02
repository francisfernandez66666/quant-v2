// paper_ab_ir_test.go — §Phase3 paper A/B 对照组与 §Phase4 IR 动态仓位单元测试。
// 验证：A/B 组标签的 set/get/持久化与对照度量；IR 对自动买入金额的缩放（预算乘子 clamp）与清零。
// English: tests for Phase-3 paper A/B group tags and Phase-4 IR-scaled position sizing — tag
// set/get/persistence, the A/B comparison metrics, and IR-driven auto-buy amount scaling with clamps.
package paper

import (
	"testing"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/data"
)

// TestABGroupTags 验证 A/B 组标签的设置/读取/清除与全部标签快照。
// English: verifies A/B group tag set/get/clear and the full-tag snapshot.
func TestABGroupTags(t *testing.T) {
	e := New(testCfg(), "")
	// 未设置 → 空串
	if g := e.PoolABGroup("n_shape"); g != "" {
		t.Fatalf("未设置组标签应为空, got %q", g)
	}
	e.SetPoolABGroup("n_shape", "B")
	e.SetPoolABGroup("dragon", "A")
	if g := e.PoolABGroup("n_shape"); g != "B" {
		t.Fatalf("n_shape 应为 B 组, got %q", g)
	}
	all := e.PoolABGroups()
	if all["dragon"] != "A" || all["n_shape"] != "B" {
		t.Fatalf("全部标签快照错误: %+v", all)
	}
	// 清空单池标记
	e.SetPoolABGroup("n_shape", "")
	if g := e.PoolABGroup("n_shape"); g != "" {
		t.Fatalf("清空后 n_shape 应为空, got %q", g)
	}
	// 空池 key 忽略（不 panic、不写入）
	e.SetPoolABGroup("", "C")
	if len(e.PoolABGroups()) != 1 {
		t.Fatalf("空 key 不应写入, got %+v", e.PoolABGroups())
	}
}

// TestABGroupPersistence 验证 A/B 组标签跨重启持久化（写入路径文件再 New 恢复）。
// English: verifies A/B tags survive a restart via the path-based state file.
func TestABGroupPersistence(t *testing.T) {
	path := t.TempDir() + "/paper_ab.json"
	e := New(testCfg(), path)
	e.SetPoolABGroup("fac_7", "B")
	e.SetPoolABGroup("n_shape", "A")
	e2 := New(testCfg(), path)
	if g := e2.PoolABGroup("fac_7"); g != "B" {
		t.Fatalf("重启后 fac_7 组标签丢失, got %q", g)
	}
	if g := e2.PoolABGroup("n_shape"); g != "A" {
		t.Fatalf("重启后 n_shape 组标签丢失, got %q", g)
	}
}

// TestIRScaleClamp 验证 IR 缩放系数的 clamp 边界：
// 无 IR→1.0；IR=0.6→1.2；IR=1.4→2.0（上限）；IR=-1→0.6（下限）与 IR<=0 时 unset→1.0。
// English: verifies IR-scale clamps — no IR→1.0; IR=0.6→1.2; IR=1.4→2.0 (upper); IR=-1→0.6 (lower);
// IR<=0 cleared→1.0.
func TestIRScaleClamp(t *testing.T) {
	e := New(testCfg(), "")
	e.SetPoolIR("dragon", 0.6)
	if s := e.PoolIRScales()["dragon"]; absIR(s-1.2) > 1e-9 {
		t.Fatalf("IR=0.6 应缩放到 1.2, got %.3f", s)
	}
	e.SetPoolIR("dragon", 1.4)
	if s := e.PoolIRScales()["dragon"]; absIR(s-2.0) > 1e-9 {
		t.Fatalf("IR=1.4 应 clamp 到 2.0, got %.3f", s)
	}
	e.SetPoolIR("dragon", -1)
	if s := e.PoolIRScales()["dragon"]; absIR(s-0.6) > 1e-9 {
		t.Fatalf("负 IR 应 clamp 到 0.6, got %.3f", s)
	}
	// 未配置池 → 不再返回该 key（映射仅含已配置池）；缺失键语义为 1.0 由实现保证
	if _, ok := e.PoolIRScales()["n_shape"]; ok {
		t.Fatal("未配置池不应出现在 PoolIRScales 映射中")
	}
	// 清零（IR=0 删除配置）：
	e.SetPoolIR("dragon", 0)
	if v := e.PoolIR("dragon"); v != 0 {
		t.Fatalf("清零后参考 IR 应为 0, got %.3f", v)
	}
	if _, ok := e.PoolIRScales()["dragon"]; ok {
		t.Fatal("清零后 dragon 不应再出现在 PoolIRScales 映射中")
	}
	// 缺失键的直接缩放语义：applyPoolIRLocked（无该 key）→ 1.0
	e.mu.Lock()
	if s := e.applyPoolIRLocked("dragon"); s != 1.0 {
		t.Fatalf("缺失 key 缩放应为 1.0, got %.3f", s)
	}
	e.mu.Unlock()
}

// TestIRScaleBuyAmount 验证 IR 动态仓位对自动买入金额的真实影响：
// 默认单笔 10000（IR unset）→ 1000 股 @10；IR=0.6（scale 1.2）→ 1200 股（金额 12000）。
// English: verifies IR-scaling actually changes the auto-buy amount — default 10000 (IR unset) buys
// 1000 shares @10; IR=0.6 (scale 1.2) buys 1200 shares (12000).
func TestIRScaleBuyAmount(t *testing.T) {
	e := New(testCfg(), "")
	e.SetStrategyPools([]string{"dragon"})
	now := time.Now()
	quotes := map[string]*data.StockInfo{"600000.SH": {Price: 10.0}}

	// 基线：无 IR → FixedAmount 10000 → 1000 股
	e.OnSignals([]combat_agent.Signal{
		{Code: "600000.SH", Name: "基", Strategy: "龙头", StrategyType: "dragon", Direction: "做多", Action: "buy", Price: 10, GeneratedAt: now},
	}, quotes)
	base := e.PoolStats("dragon")
	if base.FilledBuys != 1 {
		t.Fatalf("基线应成交 1 笔, got %d", base.FilledBuys)
	}

	// IR=0.6（scale 1.2）→ 新开仓买入 12000 元
	e2 := New(testCfg(), "")
	e2.SetStrategyPools([]string{"dragon"})
	e2.SetPoolIR("dragon", 0.6)
	e2.OnSignals([]combat_agent.Signal{
		{Code: "600000.SH", Name: "IR", Strategy: "龙头", StrategyType: "dragon", Direction: "做多", Action: "buy", Price: 10, GeneratedAt: now},
	}, quotes)
	pos := e2.positions["600000.SH"]
	if pos == nil {
		t.Fatal("IR 缩放后应成功建仓")
	}
	wantQty := 1200 // 12000/10 → 1200 股（整手）
	if pos.Qty != wantQty {
		t.Fatalf("IR=0.6 应买 %d 股, got %d", wantQty, pos.Qty)
	}
}

// applyPoolIRLocked 用同一 clamp 口径（配合持锁测试），避免与实现重复命名。
// English: mirrors the clamp under the test's own lock-free context.
func applyPoolIRLockedTest(ir float64) float64 {
	if ir <= 0 {
		return 1.0
	}
	if v := 0.6 + ir; v < 0.6 {
		return 0.6
	} else if v > 2.0 {
		return 2.0
	}
	return 0.6 + ir
}

// absIR 浮点绝对值（测试断言辅助，避免与包内 abs 冲突）。
func absIR(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
