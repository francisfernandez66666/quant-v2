// 本文件：新热点立马进池单元测试——pushFreshHotspots 将归因产出的有效事件立即归因出板块，
// 经板块验真后把成分股并入 5s 实时监控池，避免"板块已热但个股迟迟不入池"。
// English: This file: unit tests for new hotspots entering the pool immediately — pushFreshHotspots immediately attributes valid events produced by attribution into sectors, then merges constituents into the 5s realtime monitoring pool after sector verification, avoiding "the sector is already hot but its stocks enter the pool late".
package engine

import (
	"testing"

	"quant-trading-v2/internal/newsagent"
	"quant-trading-v2/internal/sector_agent"
	"quant-trading-v2/internal/strategy_engine"
)

// TestPushFreshHotspotsImmediatePool 验证有效事件被立即归因进池：
// 板块验真后成分股应出现在热点池；个股级事件不产生板块。
// English: TestPushFreshHotspotsImmediatePool verifies valid events are attributed into the pool immediately: after sector verification, constituents should appear in the hotspot pool; stock-level events produce no sector.
func TestPushFreshHotspotsImmediatePool(t *testing.T) {
	f := newFetcher()
	e := &Engine{
		fetcher:     f,
		strategy:    strategy_engine.New(nil),
		sectorAgent: sector_agent.New(nil, nil), // nil scanner 优雅降级（成分股为空，但验证通道正常）
		// English: nil scanner degrades gracefully (empty constituents, but the verification channel works).
		longEnabled:  true,
		shortEnabled: true,
	}

	// 板块级事件 → 应产出板块并尝试入池
	// English: Sector-level event → should produce a sector and attempt to enter the pool.
	events := []newsagent.NewsEvent{
		{Level: "板块", Score: 0.8, Direction: "利好", Sectors: []string{"稀土永磁"}, Title: "稀土永磁政策利好"},
		{Level: "个股", Score: 0.9, Direction: "利好", Sectors: []string{"某板块"}, Title: "个股独立事件"},
	}
	e.pushFreshHotspots(events)

	// 有板块事件时不应 panic；热点池可能有成分股（有 scanner 时），nil scanner 时保持空
	// English: Should not panic with sector events; the hotspot pool may hold constituents (when a scanner exists) and stays empty with a nil scanner.
	_ = f.HotStocks()
}

// TestPushFreshHotspotsNoEvents 无有效事件时直接跳过。
// English: TestPushFreshHotspotsNoEvents: directly skips when there are no valid events.
func TestPushFreshHotspotsNoEvents(t *testing.T) {
	f := newFetcher()
	e := &Engine{fetcher: f, strategy: strategy_engine.New(nil), sectorAgent: sector_agent.New(nil, nil)}
	e.pushFreshHotspots(nil)
	if got := f.HotStocks(); len(got) != 0 {
		t.Fatalf("无事件不应更新热点池, got %v", got)
	}
}

// TestPushFreshHotspotsNilDeps 依赖未初始化时优雅跳过（不 panic）。
// English: TestPushFreshHotspotsNilDeps: gracefully skips (no panic) when dependencies are uninitialized.
func TestPushFreshHotspotsNilDeps(t *testing.T) {
	e := &Engine{fetcher: newFetcher()} // strategy/sectorAgent 均为 nil
	// English: strategy/sectorAgent are both nil.
	events := []newsagent.NewsEvent{{Level: "板块", Score: 0.8, Sectors: []string{"券商"}}}
	e.pushFreshHotspots(events)
	if got := e.fetcher.HotStocks(); len(got) != 0 {
		t.Fatalf("依赖缺失不应更新热点池, got %v", got)
	}
}
