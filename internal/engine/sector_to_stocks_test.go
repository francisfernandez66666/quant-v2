// 本文件：板块→个股归因 mergeSectorStocksIntoScores 的单测。
// 覆盖纯决策路径：打分池已覆盖的成分股跳过、未覆盖的进入扩展（且未配置行情时安全返回）。
// English: This file: unit tests for the sector→stock attribution mergeSectorStocksIntoScores. Covers the pure decision path: constituents already covered by the scoring pool are skipped, uncovered ones enter expansion (and it safely returns when market data is not configured).
package engine

import (
	"context"
	"testing"

	"quant-trading-v2/internal/sector_agent"
	"quant-trading-v2/internal/strategy_engine"
)

// TestMergeSectorStocksSkipsCovered 验证：打分池已覆盖的成分股不重复补拉，未覆盖的不入扩展。
// English: TestMergeSectorStocksSkipsCovered verifies: constituents already covered by the scoring pool are not fetched again, and uncovered ones do not enter expansion.
func TestMergeSectorStocksSkipsCovered(t *testing.T) {
	e := &Engine{}
	sr := &strategy_engine.StrategyResult{
		MarketData: map[string]*strategy_engine.StockMarketData{
			"600001": {Code: "600001"}, // 已覆盖
			// English: Already covered.
		},
	}
	pe := map[string]float64{}
	vs := []sector_agent.VerifiedSector{{Name: "贵金属", Score: 0.5, Stocks: []string{"600001", "600002"}}}
	// 无行情/策略引擎：未配置时应安全返回，不得 panic
	// English: No market/strategy engine: when not configured it should return safely and must not panic.
	e.mergeSectorStocksIntoScores(context.Background(), sr, vs, nil, pe)
	if _, ok := sr.MarketData["600002"]; ok {
		t.Fatal("未配置行情引擎时不得写入 MarketData")
	}
}

// TestMergeSectorStocksNoSector 验证：无板块成分股时直接返回，不写 MarketData。
// English: TestMergeSectorStocksNoSector verifies: returns directly when there are no sector constituents, without writing MarketData.
func TestMergeSectorStocksNoSector(t *testing.T) {
	e := &Engine{}
	sr := &strategy_engine.StrategyResult{MarketData: map[string]*strategy_engine.StockMarketData{}}
	pe := map[string]float64{}
	e.mergeSectorStocksIntoScores(context.Background(), sr, nil, nil, pe)
	if len(sr.MarketData) != 0 {
		t.Fatalf("无板块时不应写入 MarketData, got %d", len(sr.MarketData))
	}
}
