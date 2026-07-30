package strategy_engine

import "quant-trading-v2/internal/newsagent"

type SectorHot struct {
	Name      string   `json:"name"`
	Direction string   `json:"direction"` // 利好/利空/中性
	Score     float64  `json:"score"`
	Reason    string   `json:"reason"`
	LeadStocks []string `json:"lead_stocks,omitempty"`
}

type StrategyResult struct {
	HotSectors  []SectorHot           `json:"hot_sectors"`
	BearSectors []SectorHot           `json:"bear_sectors,omitempty"`
	BearStocks  []string              `json:"bear_stocks,omitempty"`
	L1Score     map[string]float64    `json:"l1_score,omitempty"`   // code → D1评分
	L1Blocked   map[string]bool       `json:"l1_blocked,omitempty"` // code → 利空阻塞
	Events      []newsagent.NewsEvent `json:"events,omitempty"`
}
