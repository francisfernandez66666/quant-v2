// flow_freshness_test.go — §FLOW-FRESHNESS 北京时间自然日新鲜度锁。
// 三条：① 同日（含跨小时）全天可用；② 跨日丢弃；③ 空/坏日键、compact 日键正确处理。
// 反证：把判据改成固定秒窗（早盘帧午盘判过期）会让用例1红。
package signalctl

import (
	"math"
	"testing"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
)

func TestFlowFreshSameDay(t *testing.T) {
	now := time.Date(2026, 11, 19, 15, 30, 0, 0, cntime.Loc) // 北京 15:30
	if !FlowFresh("2026-11-19", now) {
		t.Fatal("同日读数（哪怕已过数小时）应全天可用")
	}
	if !FlowFresh("20261119", now) { // compact 日键
		t.Fatal("YYYYMMDD 日键应被识别为同日")
	}
	if got := FreshFlowDay("20261119", now); got != "2026-11-19" {
		t.Fatalf("新鲜应返回当日日键，实际 %q", got)
	}
}

func TestFlowFreshCrossDay(t *testing.T) {
	now := time.Date(2026, 11, 19, 9, 0, 0, 0, cntime.Loc)
	if FlowFresh("2026-11-18", now) {
		t.Fatal("昨日读数应丢弃（跨日档2）")
	}
	if FlowFresh("20261118", now) {
		t.Fatal("昨日 compact 日键应丢弃")
	}
	if got := FreshFlowDay("2026-11-18", now); got != "" {
		t.Fatalf("陈旧应返回空日键（触发弃权），实际 %q", got)
	}
}

func TestFlowFreshBadKey(t *testing.T) {
	now := time.Date(2026, 11, 19, 10, 0, 0, 0, cntime.Loc)
	for _, bad := range []string{"", "bad", "2026/11/19", "2026111", "2026-1-19"} {
		if FlowFresh(bad, now) {
			t.Fatalf("坏日键 %q 不应判新鲜", bad)
		}
	}
}

// TestSizingHardBlockQuadrant 硬闸接线：熊+放量+相对高档应拒买；量比 NaN（探针未回）应弃权不拦。
func TestSizingHardBlockQuadrant(t *testing.T) {
	cfg := config.DefaultSizingConfig().WithDefaults()
	// 熊市目标档 0.15，posRel=1.4 → TotalPosPct=0.21；放量 r_yest=1.5/r_ma5=1.4。
	r := Regime{
		MarketState: "bear",
		Account:     AccountState{TotalPosPct: 0.21},
		MaxPosPct:   0.15,
		RYest:       1.5,
		RMa5:        1.4,
		SizeCfg:     cfg,
	}
	blocked, why := sizingHardBlock(r, combat_agent.Signal{}, cfg)
	if !blocked {
		t.Fatalf("熊+放量+相对高档应拒买，实际未拦（why=%q）", why)
	}

	// 量比 NaN（探针未回）→ 平量 → Neutral，不拦（fail-open）。
	r.RYest, r.RMa5 = math.NaN(), math.NaN()
	blocked, _ = sizingHardBlock(r, combat_agent.Signal{}, cfg)
	if blocked {
		t.Fatal("量比弃权（NaN）应 fail-open 不拦")
	}
}
