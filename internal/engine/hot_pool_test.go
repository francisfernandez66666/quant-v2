package engine

import (
	"testing"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/sector_agent"
)

// newFetcher 构造一个仅带 base/hot 列表的 Fetcher（不启动轮询协程）。
func newFetcher() *data.Fetcher {
	return &data.Fetcher{}
}

// TestUpdateHotPoolMergesSectorStocks 验证通过的板块成分股并入 5s 实时监控池：
// bull/bear 板块成分股去重合并后调用 fetcher.UpdateHotStocks。
func TestUpdateHotPoolMergesSectorStocks(t *testing.T) {
	f := newFetcher()
	e := &Engine{fetcher: f}

	bull := []sector_agent.VerifiedSector{
		{Name: "稀土永磁", Direction: "利好", Stocks: []string{"600580", "002460"}},
	}
	bear := []sector_agent.VerifiedSector{
		{Name: "机器人", Direction: "利空", Stocks: []string{"600580", "300750"}},
	}
	e.updateHotPool(bull, bear)

	got := f.HotStocks()
	if len(got) != 3 {
		t.Fatalf("去重后热点池应=3只{600580,002460,300750}, got %v", got)
	}
	set := make(map[string]bool)
	for _, c := range got {
		set[c] = true
	}
	for _, want := range []string{"600580", "002460", "300750"} {
		if !set[want] {
			t.Errorf("热点池缺少 %s, got %v", want, got)
		}
	}
}

// TestUpdateHotPoolEmptyKeepsHotStocks 无验证通过的板块时热点池保持不变。
func TestUpdateHotPoolEmptyKeepsHotStocks(t *testing.T) {
	f := newFetcher()
	f.UpdateHotStocks([]string{"600580"})
	e := &Engine{fetcher: f}

	e.updateHotPool(nil, nil)

	got := f.HotStocks()
	if len(got) != 1 || got[0] != "600580" {
		t.Errorf("空板块应保持原热点池, got %v", got)
	}
}

// TestUpdateHotStocksCapsAt60 热点池超过上限 60 只时截断。
func TestUpdateHotStocksCapsAt60(t *testing.T) {
	f := newFetcher()
	stocks := make([]string, 80)
	for i := range stocks {
		stocks[i] = "600000"
	}
	f.UpdateHotStocks(stocks)
	got := f.HotStocks()
	if len(got) > 60 {
		t.Fatalf("热点池应截断到60只, got %d", len(got))
	}
}
