package display

import (
	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/newsagent"
	"quant-trading-v2/internal/sector_agent"
	"quant-trading-v2/internal/strategy_engine"
)

type DashboardData struct {
	NewsEvents    []newsagent.NewsEvent          `json:"news_events"`
	HotSectors    []strategy_engine.SectorHot    `json:"hot_sectors"`
	BearSectors   []strategy_engine.SectorHot    `json:"bear_sectors,omitempty"`
	BearStocks    []string                        `json:"bear_stocks,omitempty"`
	VerifiedSects []sector_agent.VerifiedSector  `json:"verified_sectors,omitempty"`
	Signals       []combat_agent.Signal           `json:"signals,omitempty"`
	L1Score       map[string]float64             `json:"l1_score,omitempty"`
	L1Blocked     map[string]bool                `json:"l1_blocked,omitempty"`
}

type Aggregator struct {
	current *DashboardData
}

func New() *Aggregator {
	return &Aggregator{}
}

func (a *Aggregator) Update(result *strategy_engine.StrategyResult, verified []sector_agent.VerifiedSector, signals []combat_agent.Signal) *DashboardData {
	a.current = &DashboardData{
		NewsEvents:    result.Events,
		HotSectors:    result.HotSectors,
		BearSectors:   result.BearSectors,
		BearStocks:    result.BearStocks,
		VerifiedSects: verified,
		Signals:       signals,
		L1Score:       result.L1Score,
		L1Blocked:     result.L1Blocked,
	}
	return a.current
}

func (a *Aggregator) Current() *DashboardData {
	return a.current
}
