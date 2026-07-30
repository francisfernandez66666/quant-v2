package sector_agent

import (
	"log"

	"quant-trading-v2/internal/data"
)

type VerifiedSector struct {
	Name      string   `json:"name"`
	Direction string   `json:"direction"`
	Score     float64  `json:"score"`
	RPSRank   int      `json:"rps_rank,omitempty"`
	Stocks    []string `json:"stocks,omitempty"`
	Reason    string   `json:"reason,omitempty"`
}

type Agent struct {
	scanner *data.SectorScanner
	rps     *data.RPSManager
}

func New(scanner *data.SectorScanner, rps *data.RPSManager) *Agent {
	return &Agent{scanner: scanner, rps: rps}
}

// Verify 验证热点板块：查RPS排名、查成分股
func (a *Agent) Verify(sectors []data.HotSector) []VerifiedSector {
	if len(sectors) == 0 {
		return nil
	}

	var result []VerifiedSector
	for _, s := range sectors {
		vs := VerifiedSector{
			Name:      s.Sector.Name,
			Direction: "利好",
			Score:     s.Score,
			Reason:    s.Reason,
		}

		if a.rps != nil {
			top := a.rps.GetTopSectors()
			for i, ts := range top {
				if ts.Name == s.Sector.Name {
					vs.RPSRank = i + 1
					break
				}
			}
		}

		// 取成分股前10只
		stocks, err := a.scanner.ScoreSectorStocks(s.Sector.Code, 10)
		if err == nil {
			for _, st := range stocks {
				vs.Stocks = append(vs.Stocks, st.Code)
			}
		}

		result = append(result, vs)
	}

	log.Printf("[sector_agent] 验证 %d 个板块", len(result))
	return result
}
