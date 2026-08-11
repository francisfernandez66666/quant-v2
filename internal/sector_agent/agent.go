// Package sector_agent 板块代理：验证新闻归因板块，结合 RPS 排名和成分股评分输出可操作的已验证板块。
// （Package sector_agent validates news-attributed sectors, combining RPS ranking and constituent-stock
// scoring to output actionable, verified sectors.）
package sector_agent

import (
	"log"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/strategy_engine"
)

// VerifiedSector 已验证板块，包含方向、评分、RPS 排名、成分股与板块状态（加强/持续/退潮/反弹）。
// （VerifiedSector is a verified sector: direction, score, RPS rank, constituent stocks and sector
// phase (strengthening/sustaining/retreating/bouncing).）
type VerifiedSector struct {
	Name       string   `json:"name"`                  // 板块名称
	Direction  string   `json:"direction"`             // 板块方向（利好/利空）
	Score      float64  `json:"score"`                 // 板块评分
	RPSRank    int      `json:"rps_rank,omitempty"`    // 板块 RPS 排名位次
	RPS20      float64  `json:"rps20,omitempty"`       // 板块20日RPS（用于龙回头龙性判定）
	Phase      string   `json:"phase,omitempty"`       // 板块状态：加强/持续/退潮/反弹
	Flow       float64  `json:"flow,omitempty"`        // 主力净流入(元)
	ChangePct  float64  `json:"change_pct,omitempty"`  // 板块当日涨跌幅(%)
	LimitupCnt int      `json:"limitup_cnt,omitempty"` // 板块内涨停家数
	Stocks     []string `json:"stocks,omitempty"`      // 评分靠前的可操作成分股代码
	Reason     string   `json:"reason,omitempty"`      // 验证结论/理由
}

// classifyPhase 板块状态机（抄自开源 sector_rotation 规则）：
//   - changePct>0 且 资金净流入 → 加强
//   - changePct>0 且 资金净流出 → 持续
//   - changePct<0 且 资金净流出 → 退潮
//   - changePct<0 且 资金净流入 → 反弹
//
// （classifyPhase is the sector phase state machine (ported from the open-source sector_rotation rules):
// changePct>0 & net inflow → strengthening; changePct>0 & net outflow → sustaining;
// changePct<0 & net outflow → retreating; changePct<0 & net inflow → bouncing.）
func classifyPhase(changePct, flow float64) string {
	switch {
	case changePct > 0 && flow > 0:
		return "加强"
	case changePct > 0 && flow <= 0:
		return "持续"
	case changePct < 0 && flow < 0:
		return "退潮"
	default:
		return "反弹"
	}
}

// Agent 板块验证代理，依赖板块扫描器和 RPS 排名系统。
// （Agent is the sector verification agent, depending on a sector scanner and the RPS ranking system.）
type Agent struct {
	scanner *data.SectorScanner // 板块扫描器
	rps     *data.RPSManager    // RPS 强弱排名管理器
}

// New 创建板块验证代理实例。
// （New creates a sector verification agent.）
func New(scanner *data.SectorScanner, rps *data.RPSManager) *Agent {
	return &Agent{scanner: scanner, rps: rps}
}

// FeedRPS 将板块 RPS 数据喂给内部 RPSManager（engine 每轮刷新板块名单时调用）。
// （FeedRPS feeds sector RPS data into the internal RPSManager; the engine calls it each round it
// refreshes the sector list.）
func (a *Agent) FeedRPS(sectors []data.SectorRPS) {
	if a.rps != nil && len(sectors) > 0 {
		a.rps.Update(sectors)
	}
}

// Verify 验证事件归因板块：补充 RPS 排名、板块状态与成分股评分，返回已验证板块列表。
// （Verify validates news-attributed sectors: it enriches RPS rank, sector phase and constituent-stock
// scores, returning the verified sector list.）
func (a *Agent) Verify(sectors []strategy_engine.SectorHot) []VerifiedSector {
	if len(sectors) == 0 {
		return nil
	}

	var result []VerifiedSector
	for _, s := range sectors {
		// 组装基础信息：方向/分数/涨跌幅/资金流/涨停数，并按状态机推断板块阶段
		vs := VerifiedSector{
			Name:       s.Name,
			Direction:  s.Direction,
			Score:      s.Score,
			Reason:     s.Reason,
			ChangePct:  s.ChangePct,
			Flow:       s.NetInflow,
			LimitupCnt: s.LimitupCnt,
			Phase:      classifyPhase(s.ChangePct, s.NetInflow),
		}

		// RPS 验证：在 RPS 排名榜中定位该板块，补充排名与 20 日 RPS 强度
		if a.rps != nil {
			top := a.rps.GetTopSectors()
			for i, ts := range top {
				if ts.Name == s.Name {
					vs.RPSRank = i + 1
					vs.RPS20 = ts.RPS20
					break
				}
			}
		}

		// 成分股验证：按板块代码评分前 10 只成分股，取其代码作为可操作标的
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
