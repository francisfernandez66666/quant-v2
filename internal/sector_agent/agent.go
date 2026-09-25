// Package sector_agent 板块代理：验证新闻归因板块，结合 RPS 排名和成分股评分输出可操作的已验证板块。
// （Package sector_agent validates news-attributed sectors, combining RPS ranking and constituent-stock
// scoring to output actionable, verified sectors.）
package sector_agent

import (
	"log"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/strategy_engine"
)

// VerifiedSector 已验证板块，包含方向、评分、RPS 排名、成分股与板块状态（加强/持续/退潮/反弹/未知）。
// 「未知」为 §0925EVE-W3-J（B5）新增：行情腿失败或输入全零时不编造相位，显式降级。
// （VerifiedSector is a verified sector: direction, score, RPS rank, constituent stocks and sector
// phase (strengthening/sustaining/retreating/bouncing/unknown）.）
type VerifiedSector struct {
	// 板块名称
	Name string `json:"name"`
	// 板块方向（利好/利空）
	Direction string `json:"direction"`
	// 板块评分
	Score float64 `json:"score"`
	// 板块 RPS 排名位次
	RPSRank int `json:"rps_rank,omitempty"`
	// 板块20日RPS（用于龙回头龙性判定）
	RPS20 float64 `json:"rps20,omitempty"`
	// 板块60日RPS（§P1-19 中长期相对强度，补全此前缺失字段）
	RPS60 float64 `json:"rps60,omitempty"`
	// 板块状态：加强/持续/退潮/反弹/未知（「未知」＝§0925EVE-W3-J B5：无有效输入，不编造）
	Phase string `json:"phase,omitempty"`
	// QuoteLegFailed §0925EVE-W3-J（B5）：行情腿（东财）本轮失败的透传标记——
	// 置真时 ChangePct/Flow 为未回填全零值、Phase 必为「未知」；看板与归因据此区分
	// 「相位缺失是数据降级」与「相位缺失是没算」。（做法对齐本包 Verify 的成分股失败计数。）
	QuoteLegFailed bool `json:"quote_leg_failed,omitempty"`
	// 主力净流入(元)
	Flow float64 `json:"flow,omitempty"`
	// 板块当日涨跌幅(%)
	ChangePct float64 `json:"change_pct,omitempty"`
	// 板块内涨停家数
	LimitupCnt int `json:"limitup_cnt,omitempty"`
	// 评分靠前的可操作成分股代码
	Stocks []string `json:"stocks,omitempty"`
	// 验证结论/理由
	Reason string `json:"reason,omitempty"`
}

// classifyPhase 板块状态机（抄自开源 sector_rotation 规则）：
//   - changePct>0 且 资金净流入 → 加强
//   - changePct>0 且 资金净流出 → 持续
//   - changePct<0 且 资金净流出 → 退潮
//   - changePct<0 且 资金净流入 → 反弹
//
// §0925EVE-W3-J（B5）「无有效输入」不再造假相位：
//   - quoteLegFailed 为真——行情腿（东财）本轮失败，changePct/flow 是没回填来源的全零值；
//   - changePct==0 且 flow==0——结构腿全零/东财匹配不到该板块时同样落到这组值。
//     两种情形都返回显式「未知」。旧实现靠 default 分支兜底，把 (0,0) 判成「反弹」，
//     双源半残（同花顺结构在、东财行情挂）时相位纯编造，还进 D1 归因与持仓提示的展示面。
//     default 分支现在只认真正的「跌+流入」组合（changePct<0 且 flow>=0）。
//
// （classifyPhase is the sector phase state machine (ported from the open-source sector_rotation
// rules): changePct>0 & net inflow → strengthening; changePct>0 & net outflow → sustaining;
// changePct<0 & net outflow → retreating; changePct<0 & net inflow → bouncing.
// §0925EVE-W3-J: invalid inputs (failed quote leg or the unbackfilled (0,0) zero-value pair)
// now return an explicit "unknown" instead of being fabricated into "bouncing" by the default arm.）
func classifyPhase(changePct, flow float64, quoteLegFailed bool) string {
	// §0925EVE-W3-J（B5）无有效输入 → 显式「未知」，不许编造。
	if quoteLegFailed || (changePct == 0 && flow == 0) {
		return "未知"
	}
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
	scanner         *data.SectorScanner // 板块扫描器
	rps             *data.RPSManager    // RPS 强弱排名管理器
	constituentTopN int                 // 每板块可操作成分股数量（默认 20；越大覆盖同板块强势股越广）
}

// New 创建板块验证代理实例。
// （New creates a sector verification agent.）
func New(scanner *data.SectorScanner, rps *data.RPSManager) *Agent {
	return &Agent{scanner: scanner, rps: rps, constituentTopN: 20}
}

// SetConstituentTopN 设置每板块纳入可操作成分股的数量（>0 时生效）。
// English: sets how many constituents per sector are treated as actionable (takes effect when >0).
func (a *Agent) SetConstituentTopN(n int) {
	if n > 0 {
		a.constituentTopN = n
	}
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
	verifyFailed := 0 // §M-8/N-6 成分股验证失败计数（见下方注释）
	for _, s := range sectors {
		// 组装基础信息：方向/分数/涨跌幅/资金流/涨停数，并按状态机推断板块阶段
		// §0925EVE-W3-J（B5）：classifyPhase 多带 quoteLegFailed——行情腿失败的全零输入
		// 现在得到显式「未知」，不再被 default 分支编造成「反弹」。
		vs := VerifiedSector{
			Name:       s.Name,
			Direction:  s.Direction,
			Score:      s.Score,
			Reason:     s.Reason,
			ChangePct:  s.ChangePct,
			Flow:       s.NetInflow,
			LimitupCnt: s.LimitupCnt,
			Phase:      classifyPhase(s.ChangePct, s.NetInflow, s.QuoteLegFailed),
			// §0925EVE-W3-J（B5）失败标记透传到产出结构，看板/归因侧可见「这是降级不是没算」
			QuoteLegFailed: s.QuoteLegFailed,
		}
		// §0925EVE-W3-J（B5）相位编造被闸住后留一条可观测日志（对齐本文件 §M-8/N-6 的降级留痕做法）
		if vs.Phase == "未知" {
			log.Printf("[sector_agent] 板块 %s 相位无有效输入（行情腿失败=%v，涨跌幅/资金流全零），相位报「未知」不编造反弹", s.Name, s.QuoteLegFailed)
		}

		// RPS 验证：在 RPS 排名榜中定位该板块，补充排名与 20 日 RPS 强度
		if a.rps != nil {
			top := a.rps.GetTopSectors()
			for i, ts := range top {
				if ts.Name == s.Name {
					vs.RPSRank = i + 1
					vs.RPS20 = ts.RPS20
					vs.RPS60 = ts.RPS60 // §P1-19 补全 60 日 RPS
					break
				}
			}
		}

		// 成分股验证：按板块代码评分前 N 只成分股，取其代码作为可操作标的
		// §M-8/N-6（2026-09-22 PM 批）：定位/评分失败不再零留痕——旧实现 err 分支什么都不做，
		// 末尾照打「验证 N 个板块」，全部板块成分股验证挂掉与全成功输出同形。
		// English: §M-8/N-6 — constituent verification failures are counted so the summary line
		// degrades visibly instead of the old unconditional "verified N sectors".
		if a.scanner != nil {
			sectorsInfo := a.scanner.FindSectorsByNames([]string{s.Name})
			if len(sectorsInfo) > 0 {
				stocks, err := a.scanner.ScoreSectorStocks(sectorsInfo[0].Code, a.constituentTopN)
				if err != nil {
					verifyFailed++
					log.Printf("[sector_agent] 板块 %s 成分股评分失败（本板块标的置空）: %v", s.Name, err)
				}
				for _, st := range stocks {
					vs.Stocks = append(vs.Stocks, st.Code)
				}
			} else {
				verifyFailed++
				log.Printf("[sector_agent] 板块 %s 在板块池中定位失败（本板块标的置空）", s.Name)
			}
		}

		result = append(result, vs)
	}

	if verifyFailed > 0 {
		log.Printf("[sector_agent] 验证 %d 个板块降级（%d 个成分股验证失败，%s）", len(result), verifyFailed, sectors[0].Direction)
	} else {
		log.Printf("[sector_agent] 验证 %d 个板块 (%s)", len(result), sectors[0].Direction)
	}
	return result
}
