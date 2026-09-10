// bear_hits_test.go — 利空命中情报构造器（§NEWS_BEAR）单元测试。
// 验证 bearHitsFromResult 从「当日利空事件 + 利空板块上榜 + 利空个股」组装每只持仓的
// BearHitInfo（命中级别/新闻强度/影响/归因）的匹配口径：
//   - 个股直命中（事件 CleanedStocks "名称|代码"）→ stock 级；
//   - 事件板块命中（事件 Sectors == 持仓所属板块）→ sector 级；
//   - 利空板块上榜 LeadStocks 兜底 → sector 级；利空个股列表兜底 → stock 级；
//   - 合并优先级：stock > sector，同级取更大新闻强度。
//
// English: unit tests for the §NEWS_BEAR hit-intelligence builder — verifies the matching rules of
// bearHitsFromResult (event direct/sector hits, bear-sector/bear-stock fallbacks, and the upsert
// priority stock > sector, bigger score within a level).
package engine

import (
	"math"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/newsagent"
	"quant-trading-v2/internal/report"
	"quant-trading-v2/internal/strategy_engine"
)

// bearHitsPositions 构造测试持仓集：600276 医药 / 600519 白酒 / 000001 银行 / 600000 钢铁。
func bearHitsPositions() []report.ExecLog {
	return []report.ExecLog{
		{Code: "600276", Name: "恒瑞", Direction: "做多", Strategy: "龙头"},
		{Code: "600519", Name: "茅台", Direction: "做多", Strategy: "手动"},
		{Code: "000001", Name: "平安", Direction: "做多", Strategy: "手动"},
		{Code: "600000", Name: "浦发", Direction: "做多", Strategy: "手动"},
		{Code: "000002", Name: "万科", Direction: "做空", Strategy: "手动"}, // 做空忽略
	}
}

// bearHitsResult 构造带行情（板块归属）的 8b 结果：600276/600519/000001/600000 各有所属板块。
func bearHitsResult(events []newsagent.NewsEvent) *strategy_engine.StrategyResult {
	sr := &strategy_engine.StrategyResult{
		Events: events,
		MarketData: map[string]*strategy_engine.StockMarketData{
			"600276": {Code: "600276", Quote: &data.StockInfo{Code: "600276", Sector: "医药"}},
			"600519": {Code: "600519", Quote: &data.StockInfo{Code: "600519", Sector: "白酒"}},
			"000001": {Code: "000001", Quote: &data.StockInfo{Code: "000001", Sector: "银行"}},
			"600000": {Code: "600000", Quote: &data.StockInfo{Code: "600000", Sector: "钢铁"}},
		},
	}
	return sr
}

// TestBearHitsFromResultMatching 覆盖事件直命/板块命/上榜兜底/做空忽略四类匹配。
func TestBearHitsFromResultMatching(t *testing.T) {
	sr := bearHitsResult([]newsagent.NewsEvent{
		{
			Title: "医药集采落地", Direction: "利空", Score: -0.9, ImpactLevel: "高",
			CleanedStocks: []string{"恒瑞医药|600276"}, // 直命中 600276
		},
		{
			Title: "白酒渠道利空", Direction: "利空", Score: -0.6,
			Sectors: []string{"白酒"}, // 板块命中 600519
		},
	})
	sr.BearSectors = []strategy_engine.SectorHot{
		{Name: "银行", Direction: "利空", Score: -0.5, LeadStocks: []string{"000001"}},
	}
	sr.BearStocks = []string{"600000"}

	hits := (&Engine{}).bearHitsFromResult(sr, bearHitsPositions())
	if len(hits) != 4 {
		t.Fatalf("应命中 4 只持仓, got %d: %+v", len(hits), hits)
	}
	// 600276：事件个股直命中 → stock 级、新闻强度 0.9、影响 高、归因含事件标题
	h := hits["600276"]
	if h.HitLevel != combat_agent.BearHitStock || h.Impact != "高" || math.Abs(h.NewsScore-0.9) > 1e-9 {
		t.Fatalf("600276 应 stock/0.9/高, got %+v", h)
	}
	if !strings.Contains(h.Reason, "医药集采落地") {
		t.Errorf("600276 归因应含事件标题, got %s", h.Reason)
	}
	// 600519：事件板块命中 → sector 级、新闻强度 0.6
	h = hits["600519"]
	if h.HitLevel != combat_agent.BearHitSector || math.Abs(h.NewsScore-0.6) > 1e-9 {
		t.Fatalf("600519 应 sector/0.6, got %+v", h)
	}
	// 000001：利空板块上榜 LeadStocks 兜底 → sector 级
	h = hits["000001"]
	if h.HitLevel != combat_agent.BearHitSector || math.Abs(h.NewsScore-0.5) > 1e-9 {
		t.Fatalf("000001 应 sector/0.5（上榜兜底）, got %+v", h)
	}
	// 600000：利空个股列表兜底 → stock 级
	h = hits["600000"]
	if h.HitLevel != combat_agent.BearHitStock || math.Abs(h.NewsScore-0.5) > 1e-9 {
		t.Fatalf("600000 应 stock/0.5（利空个股兜底）, got %+v", h)
	}
	// 做空持仓（000002）不产生命中
	if _, ok := hits["000002"]; ok {
		t.Fatal("做空持仓不应命中利空归因")
	}
}

// TestBearHitsFromResultUpsertPriority 合并优先级：个股直命中覆盖板块命中（即使新闻强度更低）；
// 同级命中保留更大新闻强度。
func TestBearHitsFromResultUpsertPriority(t *testing.T) {
	sr := bearHitsResult([]newsagent.NewsEvent{
		{Title: "板块医药利空", Direction: "利空", Score: -0.95, Sectors: []string{"医药"}},             // 先 sector/0.95
		{Title: "个股集采利空", Direction: "利空", Score: -0.5, CleanedStocks: []string{"恒瑞|600276"}}, // 后 stock/0.5
	})
	hits := (&Engine{}).bearHitsFromResult(sr, []report.ExecLog{{Code: "600276", Name: "恒瑞", Direction: "做多"}})
	h := hits["600276"]
	if h.HitLevel != combat_agent.BearHitStock {
		t.Fatalf("个股直命中的级别应覆盖板块命中, got %s", h.HitLevel)
	}
	if math.Abs(h.NewsScore-0.5) > 1e-9 {
		t.Fatalf("直命中的新闻强度应按本次事件 0.5 记录（不取旧板块 0.95）, got %.2f", h.NewsScore)
	}
	if !strings.Contains(h.Reason, "个股集采利空") {
		t.Errorf("直命中的归因应覆盖为最新事件, got %s", h.Reason)
	}
}

// TestBearHitsFromResultEmpty 空结果/空持仓 → nil（不产出任何信号）。
func TestBearHitsFromResultEmpty(t *testing.T) {
	if hits := (&Engine{}).bearHitsFromResult(nil, bearHitsPositions()); hits != nil {
		t.Fatalf("nil 结果不应有命中, got %+v", hits)
	}
	if hits := (&Engine{}).bearHitsFromResult(&strategy_engine.StrategyResult{}, nil); hits != nil {
		t.Fatalf("空持仓不应有命中, got %+v", hits)
	}
}

// TestBearHitsFromResultNeutralEventIgnored 中性/利好事件不进入利空归因。
func TestBearHitsFromResultNeutralEventIgnored(t *testing.T) {
	sr := bearHitsResult([]newsagent.NewsEvent{
		{Title: "医药利好", Direction: "利好", Score: 0.9, CleanedStocks: []string{"恒瑞医药|600276"}},
		{Title: "宏观中性", Direction: "中性", Score: 0.1, CleanedStocks: []string{"恒瑞医药|600276"}},
	})
	hits := (&Engine{}).bearHitsFromResult(sr, []report.ExecLog{{Code: "600276", Name: "恒瑞", Direction: "做多"}})
	if len(hits) != 0 {
		t.Fatalf("利好/中性事件不应命中利空归因, got %+v", hits)
	}
}

// TestBearHitsFromResultTimeSensitive 时间字段编译期存在性兜底（事件沿用 8b 时间戳字段）。
func TestBearHitsFromResultTimeSensitive(t *testing.T) {
	sr := bearHitsResult([]newsagent.NewsEvent{
		{Title: "个股利空", Direction: "利空", Score: -0.7, Datetime: time.Now().Format("2006-01-02 15:04:05"), CleanedStocks: []string{"恒瑞|600276"}},
	})
	hits := (&Engine{}).bearHitsFromResult(sr, []report.ExecLog{{Code: "600276", Name: "恒瑞", Direction: "做多"}})
	if h := hits["600276"]; h.HitLevel != combat_agent.BearHitStock {
		t.Fatalf("带时间戳的利空事件应命中 stock 级, got %+v", h)
	}
}
