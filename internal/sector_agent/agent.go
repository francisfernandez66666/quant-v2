// Package sector_agent 板块代理：验证新闻归因板块，结合 RPS 排名和成分股评分输出可操作的已验证板块。
package sector_agent

import (
	"log"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/strategy_engine"
)

// VerifiedSector 已验证板块，包含方向、评分、RPS 排名和成分股列表。
type VerifiedSector struct {
	Name      string   `json:"name"`
	Direction string   `json:"direction"`
	Score     float64  `json:"score"`
	RPSRank   int      `json:"rps_rank,omitempty"`
	Stocks    []string `json:"stocks,omitempty"`
	Reason    string   `json:"reason,omitempty"`
}

// Agent 板块验证代理，依赖板块扫描器和 RPS 排名系统。
type Agent struct {
	scanner *data.SectorScanner // 板块扫描器
	rps     *data.RPSManager    // RPS 强弱排名管理器
}

// New 创建板块验证代理实例。
func New(scanner *data.SectorScanner, rps *data.RPSManager) *Agent {
	return &Agent{scanner: scanner, rps: rps}
}

// Verify 验证事件归因板块：补充 RPS 排名、获取板块成分股评分，返回已验证板块列表。
func (a *Agent) Verify(sectors []strategy_engine.SectorHot) []VerifiedSector {
	if len(sectors) == 0 {
		return nil
	}

	var result []VerifiedSector
	for _, s := range sectors {
		vs := VerifiedSector{
			Name:      s.Name,
			Direction: s.Direction,
			Score:     s.Score,
			Reason:    s.Reason,
		}

		if a.rps != nil {
			top := a.rps.GetTopSectors()
			for i, ts := range top {
				if ts.Name == s.Name {
					vs.RPSRank = i + 1
					break
				}
			}
		}

		if a.scanner != nil {
			sectorsInfo := a.scanner.FindSectorsByNames([]string{s.Name})
			if len(sectorsInfo) > 0 {
				stocks, err := a.scanner.ScoreSectorStocks(sectorsInfo[0].Code, 10)
				if err == nil {
					for _, st := range stocks {
						vs.Stocks = append(vs.Stocks, st.Code)
					}
				}
			}
		}

		result = append(result, vs)
	}

	log.Printf("[sector_agent] 验证 %d 个板块 (%s)", len(result), sectors[0].Direction)
	return result
}
