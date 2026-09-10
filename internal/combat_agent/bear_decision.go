// bear_decision.go — 利空新闻持仓处理决策器（§NEWS_BEAR）。
//
// A 股只能做多，但利空新闻必须对持仓起到即时风险提示作用，并且不能"一次性无脑
// 清仓"——按「信号强度 × 真实量价趋势」输出分级动作：
//
//	清仓（BearClose）：新闻强命中（个股直命中）且量价配合破位放量 → 全部卖出；
//	减仓（BearTrim）：新闻命中且量价初步转弱 → 半仓卖出（先控制风险、留观察仓）；
//	观望（BearWatch）：新闻命中但个股仍强势/封板（量价趋势为正）→ 仅提醒，不自动卖。
//
// 分级结果经 SellAction 归一（利空清仓→close / 利空减仓→trim / 利空观望→不动作）后，
// 由引擎统一喂入模拟盘撮合（paperSignals 13e）与实盘自动卖出（trading.Advise → autoExecuteRealSells）。
//
// English: bearish-news position decision (§NEWS_BEAR). A shares are long-only, but bearish news must
// warn holdings promptly without an unconditional dump. Decision is graded by signal strength × real
// price/volume trend: close (direct news hit + breakdown on volume), trim (news hit + trend weakening),
// watch (news hit but price still strong / limit-up → remind only). SellAction normalizes the result
// (BearClose→close / BearTrim→trim / BearWatch→no action) before the engine feeds it to the paper
// fill (paperSignals step 13e) and the live auto-sell channel (trading.Advise → autoExecuteRealSells).
package combat_agent

import (
	"strings"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
)

// BearHitInfo 单只持仓的利空命中情报：由引擎层从当日利空事件/利空板块归因组装，
// 携带"命中级别 + 新闻信号强度 + 归因说明"，供决策器与前端展示使用。
// English: bearish-hit intelligence for one holding, assembled by the engine from the day's
// bearish events / bearish sectors — carries hit level + news strength + attribution reason.
type BearHitInfo struct {
	// Code 持仓纯数字代码（如 600519）。
	Code string `json:"code"`
	// HitLevel 命中级别：stock=新闻/利空板块直接命中该个股；sector=仅命中持仓所属板块（间接）。
	HitLevel string `json:"hit_level"`
	// NewsScore 新闻信号强度（0~1）：取直接命中的利空事件 |score| 归一。
	NewsScore float64 `json:"news_score"`
	// Impact 事件影响级别：高/中/低（对强度加成）。
	Impact string `json:"impact"`
	// Reason 归因说明（板块名/上榜原因/关联新闻标题），用于向用户解释为何触发。
	Reason string `json:"reason"`
}

// hit level 常量。
const (
	BearHitStock  = "stock"  // 直接命中持仓个股
	BearHitSector = "sector" // 仅命中持仓所属板块（间接）
)

// bearAction 决策输出动作。
const (
	BearActionClose = "close" // 利空清仓：全部卖出
	BearActionTrim  = "trim"  // 利空减仓：半仓卖出（先控风险留观察仓）
	BearActionWatch = "watch" // 利空观望：仅提醒，不自动卖（量价仍强势）
)

// BearPlan 决策结果：动作 + 综合分 + 面向用户的理由。
// English: BearPlan is the decision result: action + composite score + user-facing reason.
type BearPlan struct {
	Action    string  // close / trim / watch
	Score     float64 // 综合分（= wN×newsStrength + wT×trend）
	News      float64 // 新闻信号强度（0~1）
	Trend     float64 // 量价趋势因子（-1..+1，正=强势抑制卖、负=破位强化卖）
	Reason    string  // 中文理由（含强度与量价依据，便于人工复核）
	HitLevel  string  // 命中级别透传
	NewsScore float64 // 新闻事件 |score| 透传
}

// newsStrengthExact 归一新闻信号强度（0~1）：|score| 低于 0.1 视为 0，≥0.75 封顶 1，
// 中间线性映射（0.75 相当于"高"档）。影响级别加成：高 ×1.0 / 中 ×0.85 / 低 ×0.7。
// English: normalizes the news |score| into [0,1] (0 below 0.1, 1 at ≥0.75) with an impact bonus
// (高 1.0 / 中 0.85 / 低 0.7).
func newsStrengthExact(score float64, impact string) float64 {
	if score < 0 {
		score = -score
	}
	if score < 0.1 {
		return 0
	}
	s := score / 0.75
	if s > 1 {
		s = 1
	}
	switch impact {
	case "高":
		return s
	case "中":
		return s * 0.85
	case "低", "":
		return s * 0.7
	}
	return s
}

// trendFactor 由实时量价快照计算趋势因子（-1..+1）：
//   - 封涨停（价已触板）且 LimitUpHold → +1（最强，坚决不卖接刀）；
//   - 涨幅 ≥ StrongPct → +0.7（抗跌上涨）；
//   - 涨幅 ≤ -BreakPct 且（放量——换手率≥5% 或 主力净流出为负）→ -1（破位放量，强化卖出）；
//   - 涨幅 ≤ -BreakPct 但量能不足（缩量阴跌）→ -0.5（警惕但未确认）；
//   - 0 附近震荡 → 0（中性，主要看新闻强度）。
//
// English: trend factor from the live quote (−1..+1): limit-up (and LimitUpHold) → +1 (strongest hold);
// change ≥ StrongPct → +0.7; change ≤ −BreakPct with volume (turnover ≥5% or net outflow) → −1
// (breakdown confirmed on volume → forces sell); change ≤ −BreakPct without volume (shrinking) → −0.5;
// flat → 0 (news strength decides).
func trendFactor(q *data.StockInfo, code, name string, cfg *config.BearNewsConfig) float64 {
	if q == nil || q.Price <= 0 {
		return 0 // 无行情 → 中性，只按新闻强度决策（不因缺行情误判）
	}
	chg := q.ChangePct
	// 封涨停：涨跌幅达到（略低于）该板块涨停幅度即视为封板——板感知统一口径
	limit := data.LimitUpPct(code, name)
	if cfg.LimitUpHoldOn() && limit > 0 && chg >= limit-0.3 {
		return 1
	}
	if cfg.StrongPct > 0 && chg >= cfg.StrongPct {
		return 0.7
	}
	if cfg.BreakPct > 0 && chg <= -cfg.BreakPct {
		volumed := q.Turnover >= 5
		if !volumed && q.Amount > 0 {
			// 无换手率时用主力净流出近似放量恐慌：净流出额达到成交额 8% 视为放量派发
			volumed = q.NetInflow < -q.Amount*0.08
		}
		if volumed {
			return -1
		}
		return -0.5
	}
	return 0
}

// DecideBearSell 利空卖出分级决策：综合 新闻信号强度与真实量价趋势，输出 清仓/减仓/观望。
// 命中强度取命中级别权重（个股直命中 DirectHit / 板块命中 SectorHit）× 事件强度归一。
// 总分 = NewsWeight×newsStrength + TrendWeight×trend；trend 仅作抑制/加成因子（不会为负就把
// 利好当利空卖，也不会因强势利多完全屏蔽强利空——清仓仍主要看新闻强度与命中级别）。
// English: graded bearish-sell decision — combines news strength and the real price/volume trend into
// close/trim/watch. Strength = hit-level weight (direct/sector) × normalized event |score|; total =
// NewsWeight×news + TrendWeight×trend. trend only boosts/suppresses (a strong stock never turns a
// bearish hit bullish, nor does a strong move fully mask a direct hit).
func DecideBearSell(hit *BearHitInfo, q *data.StockInfo, code, name string, cfg *config.BearNewsConfig) BearPlan {
	// 防御：nil 命中/配置回退零值（决策退化为"仅按缺失信息不下单"，宁可观望不误判）。
	// English: guards nil hit/config — a missing hit degrades to watch, never a false sell.
	if hit == nil {
		return BearPlan{Action: BearActionWatch, Reason: "利空命中情报缺失，暂不自动卖出"}
	}
	if cfg == nil {
		cfg = new(config.BearNewsConfig)
	}
	p := BearPlan{HitLevel: hit.HitLevel, NewsScore: hit.NewsScore}
	// 命中级别权重：个股直命中最强，板块间接命中次之。
	hl := cfg.SectorHit
	if hit.HitLevel == BearHitStock {
		hl = cfg.DirectHit
	}
	if hl <= 0 {
		hl = 0.55
	}
	news := newsStrengthExact(hit.NewsScore, hit.Impact) * hl
	trend := trendFactor(q, code, name, cfg)
	total := cfg.NewsWeight*news + cfg.TrendWeight*trend

	p.News, p.Trend, p.Score = news, trend, total

	// 封板强势保护（LimitUpHold）：强势涨停遇利空消息不追跌（避免在涨停被砸开的
	// 低点接刀卖出），仅观望提醒——量价最强势时卖出往往卖在恐慌最低点。
	// English: limit-up strength guard (LimitUpHold): a sealed limit-up plus bearish news never sells
	// (avoid dumping into the panic low once the board cracks) — watch only; the strongest price action
	// usually means selling into the low is the worst exit.
	if cfg.LimitUpHoldOn() && trend >= 0.9 {
		p.Action = BearActionWatch
		p.Reason = "利空风险[观望]："
		if hit.Reason != "" {
			p.Reason += hit.Reason + "；"
		}
		p.Reason += "个股封板强势，暂不自动卖出（避免利空砸开涨停的低点接刀），继续跟踪量价确认"
		return p
	}
	// 分级决策：新闻强度为主、量价趋势为抑制/加成因子。
	//  清仓：新闻强命中（≥ SellScore×0.8）且量价未强势（trend≤0）；或综合分越过清仓线且个股直命
	//        （量价不得转强，trend≤0.3——强势上涨不单纯因总分高而清仓）。
	//  减仓：新闻命中减仓线（≥ TrimScore）且量价无明显转强（trend≤0.2），半平先控风险留观察仓。
	//  观望：其余——新闻过弱只提醒，或新闻命中但个股强势（强趋势是对冲利空的唯一量价理由）。
	// English: graded decision — news dominates; the price/volume trend only boosts or suppresses.
	switch {
	case news >= cfg.SellScore*0.8 && trend <= 0:
		p.Action = BearActionClose
		p.Reason = bearReason(hit, news, trend, "清仓")
	case total >= cfg.SellScore && hit.HitLevel == BearHitStock && trend <= 0.3:
		p.Action = BearActionClose
		p.Reason = bearReason(hit, news, trend, "清仓")
	case news >= cfg.TrimScore && trend <= 0.2:
		p.Action = BearActionTrim
		p.Reason = bearReason(hit, news, trend, "减仓")
	case news >= cfg.TrimScore:
		p.Action = BearActionWatch
		p.Reason = bearReason(hit, news, trend, "观望")
	default:
		p.Action = BearActionWatch
		p.Reason = bearReason(hit, news, trend, "观望")
	}
	return p
}

// bearReason 组装面向用户的中文理由（附带信号强度与量价依据，供人工复核）。
// English: assembles the user-facing Chinese reason with strength and price/volume evidence.
func bearReason(hit *BearHitInfo, news, trend float64, verb string) string {
	b := &strings.Builder{}
	b.WriteString("利空风险[" + verb + "]：")
	if hit.Reason != "" {
		b.WriteString(hit.Reason)
		b.WriteString("；")
	}
	b.WriteString("信号强度" + pct(news) + "，量价趋势" + trendTag(trend) + "。建议")
	switch verb {
	case "清仓":
		b.WriteString("全部卖出规避风险（利空强命中且量价转弱）")
	case "减仓":
		b.WriteString("先减半仓控制风险、留观察仓位")
	case "观望":
		b.WriteString("暂不自动卖出，继续跟踪量价确认")
	}
	return b.String()
}

// pct 把 0~1 因子格式化为百分比。
func pct(v float64) string {
	if v >= 1 {
		return "强"
	}
	if v >= 0.55 {
		return "中强"
	}
	if v >= 0.25 {
		return "中"
	}
	return "弱"
}

// trendTag 把趋势因子翻译为中文标签（供 Reason 展示）。
func trendTag(t float64) string {
	switch {
	case t >= 0.9:
		return "封板强势"
	case t >= 0.5:
		return "偏强"
	case t > -0.5:
		return "中性"
	case t > -0.9:
		return "偏弱(缩量)"
	default:
		return "破位放量"
	}
}
