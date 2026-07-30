package combat_agent

import "quant-trading-v2/internal/sector_agent"

type ScanInput struct {
	Sectors []sector_agent.VerifiedSector
	L1Score   map[string]float64
	L1Blocked map[string]bool
}

type Signal struct {
	Code       string  `json:"code"`
	Name       string  `json:"name"`
	Strategy   string  `json:"strategy"`
	Direction  string  `json:"direction"`
	Price      float64 `json:"price"`
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason"`
	Sector     string  `json:"sector"`
}
