package trading

import (
	"testing"
	"time"

	"quant-trading-v2/internal/config"
)

func testCfg() config.DisciplineConfig {
	c := config.DefaultDisciplineConfig()
	return c
}

func at(day, hh, mm int) time.Time {
	return time.Date(2026, 9, 7, hh, mm, 0, 0, time.Local)
}

// TestProbeStopLossWindow 跌穿止损线 → 进入观察窗；窗结算仍无信号 → 止损离场（不滚动）。
func TestProbeStopLossWindow(t *testing.T) {
	cfg := testCfg()
	entry := 100.0
	// 首次探针：价格 94（-6%），触及止损线 → 锁定观察窗。
	st, d := probeDiscipline(nil, "600000", entry, 100, 94, false, false, at(2026, 10, 30), cfg)
	if st.Line != LineStopLoss || d == nil || d.Action != ActionConfirm {
		t.Fatalf("首触止损线应进入观察窗, got line=%v action=%v", st.Line, d)
	}
	// 窗口期内探针：仍在窗口，无信号 → 持续等待（未到结算点）。
	_, d = probeDiscipline(&st, "600000", entry, 94, 94.5, false, false, at(2026, 10, 35), cfg)
	if d != nil && d.Action == ActionClose {
		t.Fatalf("窗口期未到结算点不应离场")
	}
	// 到结算点仍无信号 → 止损离场（未深破 → 减半仓 trim）。
	_, d = probeDiscipline(&st, "600000", entry, 94.5, 93, false, false, at(2026, 10, 45), cfg)
	if d == nil || d.Action != ActionTrim {
		t.Fatalf("窗结算无信号应减半仓止损离场, got %+v", d)
	}
}

// TestProbeStopLossBearHard 触及止损线且有利空/做空信号 → 硬清（不等待窗）。
func TestProbeStopLossBearHard(t *testing.T) {
	cfg := testCfg()
	_, d := probeDiscipline(nil, "600000", 100, 100, 94, false, true, at(2026, 10, 30), cfg)
	if d == nil || d.Action != ActionClose || d.Line != LineStopLoss {
		t.Fatalf("触止损线+利空信号应硬清, got %+v", d)
	}
}

// TestProbeTakeProfitWindow 触及止盈线 → 无信号进入观察窗；窗结算无信号 → 止盈离场。
func TestProbeTakeProfitWindow(t *testing.T) {
	cfg := testCfg()
	st, d := probeDiscipline(nil, "600000", 100, 100, 116, false, false, at(2026, 10, 30), cfg)
	if st.Line != LineTakeProfit || d == nil || d.Action != ActionConfirm {
		t.Fatalf("触止盈线应进入观察窗, got line=%v action=%v", st.Line, d)
	}
	// 窗结算无信号 → 止盈离场。
	_, d = probeDiscipline(&st, "600000", 100, 116, 116, false, false, at(2026, 10, 46), cfg)
	if d == nil || d.Action != ActionClose {
		t.Fatalf("止盈窗结算无信号应止盈离场, got %+v", d)
	}
}

// TestProbeTakeProfitHasBull 触及止盈线但仍有做多信号 → 延持（无离场）。
func TestProbeTakeProfitHasBull(t *testing.T) {
	cfg := testCfg()
	_, d := probeDiscipline(nil, "600000", 100, 100, 116, true, false, at(2026, 10, 30), cfg)
	if d != nil {
		t.Fatalf("止盈线有做多信号应延持, got %+v", d)
	}
}

// TestProbeTrailingStop 突破止盈线后从最高价回撤 ≥6% → 移动止盈，更长观察窗。
func TestProbeTrailingStop(t *testing.T) {
	cfg := testCfg()
	// 已涨到 130（+30%），最高价 130，现价 121（(130-121)/130≈6.9% 回撤）→ 移动止盈。
	st, d := probeDiscipline(nil, "600000", 100, 130, 121, false, false, at(2026, 10, 30), cfg)
	if st.Line != LineTrail || d == nil || d.Action != ActionConfirm {
		t.Fatalf("最高价回撤≥6%%应触发移动止盈, got line=%v action=%v", st.Line, d)
	}
	if st.WindowMin != cfg.TrailConfirmMin {
		t.Fatalf("移动止盈窗应为 %d 分钟, got %d", cfg.TrailConfirmMin, st.WindowMin)
	}
}

// TestProbeDeepBreach 深破（-2×止损线）→ 深破判定，同样走观察窗。
func TestProbeDeepBreach(t *testing.T) {
	cfg := testCfg()
	// 价格 88（-12% = 2×止损6）→ 深破。
	st, d := probeDiscipline(nil, "600000", 100, 100, 88, false, false, at(2026, 10, 30), cfg)
	if st.Line != LineDeepBreach || d == nil || d.Action != ActionConfirm {
		t.Fatalf("深破应触发深破判定, got line=%v action=%v", st.Line, d)
	}
	// 深破窗结算无信号 → 硬清。
	_, d = probeDiscipline(&st, "600000", 100, 88, 87, false, false, at(2026, 10, 45), cfg)
	if d == nil || d.Action != ActionClose {
		t.Fatalf("深破窗结算无信号应硬清, got %+v", d)
	}
}

// TestProbeFirstTouchLocked 首触锁定：后续价格反弹回到窗口内不重置结算时间（不滚动）。
func TestProbeFirstTouchLocked(t *testing.T) {
	cfg := testCfg()
	st, _ := probeDiscipline(nil, "600000", 100, 100, 94, false, false, at(2026, 10, 30), cfg)
	first := st.SettleStart
	// 窗口内价格反弹回 96（-4%，回到止损线上方），但判定线已锁定、结算点不变。
	_, d := probeDiscipline(&st, "600000", 100, 94, 96, false, false, at(2026, 10, 40), cfg)
	if st.SettleStart != first {
		t.Fatalf("首触后不应滚动重置结算点, %v != %v", st.SettleStart, first)
	}
	// 结算点仍按首触 10:30+15min=10:45 对齐：10:40 未到，不结算。
	if st.Settled {
		t.Fatalf("10:40 未到 10:45 结算点不应结算")
	}
	_ = d
}
