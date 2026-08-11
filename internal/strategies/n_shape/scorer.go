// Package n_shape scorer — N 形超短策略 D1~D4 评分核心。
//
// 评分体系总分 100，由四个维度加权组成：
//
//   - D1 事件驱动（40分）：事件驱动匹配。
//     采用三段式计算：YAML负面阻断（硬闸）→ LLM评分（优先）→ YAML正面兜底。
//     LLM 评分从 HotTopic.Direction 和 Score 推导：利空→blocked，中性→减半，利好→正常。
//     LLMD1Score 以 0.0~1.0 传入，calcD1 映射到 0~40 分。
//     信号硬闸门：D1>0（放宽，原为 D1=40），配合 LLM 评分体系。
//
//   - D2 相对强度 RS（30分）：三层proxy衡量资金攻击意愿。
//     ① 集合竞价涨幅（15分）：+1.5%~+5% 最佳（15分），>5% 次之（10分），低开微涨（2分）
//     ② 量比（8分）：1.2~1.8 得4分，1.8~3.0 得8分，>3.0 得3分（过高说明一致性太强）
//     ③ 超额收益（7分）：（个股涨幅-基准涨幅）/ 3%，上限7分
//     软门槛：D2 仅贡献总分，不单独拦截信号。
//
//   - D3 超跌确认（20分）：判断回调深度是否到位。
//     有PE数据时：PE<15 满20分，<30 得10分，<50 得5分，>50 不计分。
//     无PE数据时：用斐波那契回调深度替代。
//     深度0.382~0.618 得12分（最理想），0.2~0.382 得16分，0.618~1.0 得8分。
//
//   - D4 资金确认（10分）：MACD+量能双确认。
//     MACD水上（DIF>DEA且DIF>0）：5分
//     当日累计量 > 20日均量×1.5（按时间进度折算）：5分
//
// 评分始终计算（D1不足时也出总分），信号硬闸门：D1>0 且 总分≥60 → Valid=true → 才生成信号
// 时间衰减：10:00后优先级降低，14:30后再降低。
// 情绪否决："衰退"阶段禁止入场；"退潮"减30基础分；"高潮"减20基础分。
//
// （English: Core D1~D4 scoring for the N-shape strategy, total 100. D1 is a three-stage event gate (YAML negative block →
// LLM score → YAML backstop) and the signal gate is D1>0 with total ≥60; D2/D3/D4 are soft contributors. Scores decay with
// time after 10:00, and emotion phases veto ("衰退"), slash ("退潮" −30) or reduce ("高潮" −20) the base priority.）
package n_shape

import (
	"fmt"
	"math"
	"strings"

	"quant-trading-v2/internal/data"
)

const (
	MaxD1      = 40.0 // D1 事件维度满分（D1 event dimension max）
	MaxD2      = 30.0 // D2 相对强度维度满分（D2 relative-strength dimension max）
	MaxD3      = 20.0 // D3 超跌维度满分（D3 oversold dimension max）
	MaxD4      = 10.0 // D4 资金维度满分（D4 fund dimension max）
	Threshold  = 60.0 // 总分通过阈值（需≥60 才视为有效信号）（Total pass threshold, ≥60 for a valid signal）
	StrongMin  = 80   // 高优先级基础分阈值（≥80 可开仓）（High-priority base threshold, ≥80 allows opening）
	ObserveMin = 40   // 可观察最低基础分（<40 直接 mute）（Minimum observable base, <40 mutes）
)

// 时间窗口定义（HHMM 整数格式，用于优先级计算）。（Time-window constants in HHMM integer form for priority math.）
const (
	t915  = 915  // 9:15 集合竞价开始（9:15 auction opens）
	t920  = 920  // 9:20 不可撤单（9:20 orders become irreversible）
	t925  = 925  // 9:25 集合竞价结束（9:25 auction closes）
	t935  = 935  // 9:35 开盘后5分钟（9:35 five minutes after open）
	t1000 = 1000 // 10:00（10:00）
	t1030 = 1030 // 10:30（10:30）
	t1130 = 1130 // 11:30 午休（11:30 lunch break）
	t1300 = 1300 // 13:00 下午开盘（13:00 afternoon open）
	t1400 = 1400 // 14:00（14:00）
	t1430 = 1430 // 14:30（14:30）
	t1457 = 1457 // 14:57 尾盘集合竞价开始（14:57 closing auction starts）
	t1500 = 1500 // 15:00 收盘（15:00 market close）
)

// emotionHardBlock 情绪硬闸：标记不可操作的周期阶段。
// "衰退"阶段禁止任何开仓操作。（Emotion hard-block: "衰退" (recession) forbids any position opening.）
var emotionHardBlock = map[string]bool{"衰退": true}

// PriorityResult 时间优先级计算结果。（PriorityResult is the time-priority computation result.）
// Level 为基础分（0~100），Label 为等级（strong/observe/mute），CanOpen 表示是否允许开仓。
// （Level is the base score 0~100, Label is strong/observe/mute, CanOpen flags whether to allow opening.）
type PriorityResult struct {
	Level   int    `json:"level"`    // 优先级基础分（Priority base score）
	Label   string `json:"label"`    // 提醒级别（strong/observe/mute）（Remind level: strong/observe/mute）
	CanOpen bool   `json:"can_open"` // 是否允许开仓（Whether opening is allowed）
}

// priorityOf 根据时间、D1分数、是否龙头、情绪周期计算当前时刻的优先级。（priorityOf computes the time priority from clock time, D1, leader flag and emotion phase.）
//
// 时间窗口权重：
//
//	9:15~9:20   70分（集合竞价，不可开仓）
//	9:20~10:00 100分（黄金窗口）
//	10:00~11:30 初始90，每半小时扣5分：90→85→80→75
//	13:00~15:00 初始90，每半小时扣7.5：90→82.5→75→67.5→60
//
// 情绪因子修正：退潮-30（最低10）、高潮-20（最低20）。
// "衰退"直接否决（返回 -1/mute/false）。
//
// （English: auction 9:15–9:20=70 (no open), golden window 9:20–10:00=100, then decays −5 per 30min to 11:30 and −7.5 per
// 30min in the afternoon. Emotion: 退潮 −30 (floor 10), 高潮 −20 (floor 20); 衰退 hard-vetoes with −1/mute/false.）
func priorityOf(t int, d1 float64, isLeader bool, emotion string) PriorityResult {
	if t < t915 || t > t1500 {
		return PriorityResult{-1, "mute", false}
	}
	if t >= t915 && t < t920 {
		return PriorityResult{70, "observe", false}
	}

	var base int
	switch {
	case t >= t920 && t < t1000:
		base = 100
	case t >= t1000 && t <= t1130:
		mins := float64(t/100*60 + t%100 - 600) // 10:00起分钟数（Minutes since 10:00）
		blocks := math.Floor(mins / 30.0)
		base = int(90.0 - 5.0*blocks)
	case t >= t1300 && t <= t1500:
		mins := float64(t/100*60 + t%100 - 780) // 13:00起分钟数（Minutes since 13:00）
		blocks := math.Floor(mins / 30.0)
		base = int(90.0 - 7.5*blocks)
	default:
		base = 0
	}
	// 基础分下限保护：时间衰减后不出现负分（Floor the base so time decay never goes negative）
	if base < 0 {
		base = 0
	}
	if base < 0 {
		base = 0
	}

	switch emotion {
	case "退潮":
		base = maxInt(10, base-30)
	case "高潮":
		base = maxInt(20, base-20)
	}
	if emotionHardBlock[emotion] {
		return PriorityResult{-1, "mute", false}
	}

	label := "observe"
	canOpen := false
	if base >= StrongMin {
		label = "strong"
		canOpen = true
	} else if base < ObserveMin {
		label = "mute"
	}
	return PriorityResult{base, label, canOpen}
}

// WaveA 昨日波形（A波）数据。（WaveA is yesterday's A-wave data.）
// 用于 N 形形态识别：A波是 N 形的"第一竖"，昨日已完成的拉升波形。（Used for N-shape recognition: the A wave is the first vertical of the N.）
type WaveA struct {
	ADate                      string  // 日期（Date）
	AOpen, AHigh, ALow, AClose float64 // 昨开/昨高/昨低/昨收（Prev open/high/low/close）
	AVol                       float64 // 昨日成交量（Yesterday's volume）
	AChgPct                    float64 // 昨日涨幅百分比（Yesterday's gain %）
	AAboveMA60                 bool    // 昨日收盘是否站上60日均线（中期趋势多头）（Close above the 60-day MA, mid-term bullish）
	IsSectorLeader             bool    // 是否为板块内龙头股（Whether it is the sector leader）
	PrevSessionWeak            bool    // 前日是否弱势（用于排除弱转强假突破）（Whether the prior day was weak, filters fake weak-to-strong breaks）
}

// IntradayB 日内快照（B段）数据。（IntradayB is the intraday B-segment snapshot.）
// 包含竞价数据、当前价格/量、MACD 指标等，是评分的主要输入。（Contains auction data, live price/volume and MACD — the main scoring input.）
type IntradayB struct {
	TTime         int     // 当前时间（HHMM 整数格式，如 935=9:35）（Current time as HHMM, e.g. 935 = 9:35）
	CurPrice      float64 // 当前最新价格（Current latest price）
	CumVol        float64 // 当日累计成交量（手）（Cumulative volume of the day）
	AuctionVol    float64 // 集合竞价成交量（Auction volume）
	AuctionHigh   float64 // 集合竞价最高价（Auction high）
	AuctionLow    float64 // 集合竞价最低价（Auction low）
	AuctionChgPct float64 // 集合竞价涨跌幅（%）（Auction change %）
	AuctionTrend  string  // 集合竞价趋势描述（如 "上行"/"下行"/"平开"）（Auction trend, e.g. 上行/下行/平开）
	BenchCurChg   float64 // 基准指数（大盘/创业板）当前涨跌幅（Benchmark index current change %）
	EventType     string  // 当日事件类型（Today's event type）
	PrevClose     float64 // 前日收盘价（Previous close）
	PrevHigh      float64 // 前日最高价（Previous high）
	PrevLow       float64 // 前日最低价（Previous low）
	AvgDailyVol   float64 // 20日均量（用于量比计算）（20-day average volume, for volume-ratio math）

	// 分钟级 MACD 指标，用于 D4 资金确认。（Minute-level MACD for D4 fund confirmation.）
	MinuteMACDDIF float64 // MACD DIF 快线（MACD DIF fast line）
	MinuteMACDDEA float64 // MACD DEA 慢线（MACD DEA slow line）
	MinuteMACDBar float64 // MACD 柱状图值（MACD histogram value）
}

// Ctx 策略运行上下文，包含板块、情绪、LLM 等多种数据。（Ctx carries sector, emotion and LLM context for scoring.）
type Ctx struct {
	// LLM D1 评分字段
	// LLMD1Score > 0 时优先使用 LLM 评分（替换 YAML EventMatcher 的得分）。
	// LLMBlocked 为 true 时直接否决（LLM 判定为利空）。
	// （LLM D1 fields: LLMD1Score>0 takes precedence over the YAML matcher; LLMBlocked vetoes outright.）
	LLMD1Score float64 // LLM 给出的个股 D1 评分（0.0~1.0），0 表示无 LLM 结果（LLM D1 score 0.0~1.0; 0 means no LLM result）
	LLMBlocked bool    // LLM 判定利空时阻塞（calcD1 直接返回 blocked）（LLM bearish → calcD1 returns blocked）

	EmotionPhase       string  // 情绪周期阶段（"冰点"/"启动"/"高潮"/"退潮"/"衰退"）（Emotion phase: 冰点/启动/高潮/退潮/衰退）
	EventDesc          string  // 事件描述文本（用于 D1 YAML 兜底匹配）（Event description for D1 YAML fallback matching）
	SectorTurnover     float64 // 板块当日成交额（Sector daily turnover）
	SectorTurnoverMA20 float64 // 板块20日平均成交额（用于判断板块冷热）（Sector 20-day avg turnover, gauges sector activity）
	PreEventReturn5d   float64 // 事件预支回报率5日（大于40%可能已被提前透支）（5-day pre-event return; >40% may be pre-priced）
	StockPE            float64 // 个股PE（用于D3超跌评分）（Stock PE for D3 oversold scoring）
	AvgDailyVol        float64 // 20日均量（用于D4量能放大对比）（20-day avg volume for D4 volume expansion）
}

// ScoreResult N 形策略完整评分结果。（ScoreResult is the complete N-shape scoring result.）
// 包含 D1~D4 各维分数、总分、有效性标志和信号标志。（Holds D1~D4 scores, total, validity and signal flags.）
type ScoreResult struct {
	D1Event     float64  `json:"d1"`               // D1 事件硬闸得分（0~40）（D1 event-gate score 0~40）
	D2RS        float64  `json:"d2"`               // D2 相对强度得分（0~30）（D2 relative-strength score 0~30）
	D3Pullback  float64  `json:"d3"`               // D3 超跌确认得分（0~20）（D3 oversold-pullback score 0~20）
	D4Accept    float64  `json:"d4"`               // D4 资金确认得分（0~10）（D4 fund-acceptance score 0~10）
	Total       float64  `json:"total"`            // 总分（D1+D2+D3+D4，0~100）（Total, D1+D2+D3+D4, 0~100）
	Valid       bool     `json:"valid"`            // 是否有效信号（D1>0 且 总分≥60）（Valid signal: D1>0 and total ≥60）
	Priority    int      `json:"priority"`         // 时间优先级分数（0~100）（Time priority score 0~100）
	RemindLevel string   `json:"remind"`           // 提醒级别（strong/observe/mute）（Remind level: strong/observe/mute）
	CanOpen     bool     `json:"can_open"`         // 是否允许开仓（Whether opening is allowed）
	LeftSignal  bool     `json:"left_signal"`      // 左侧一突信号（价格突破前高且量比≥1.8）（Left breakout: price above prev-high with volume ratio ≥1.8）
	RightSignal bool     `json:"right_signal"`     // 右侧信号（二突破确认，由外部状态机设置）（Right signal: second-breakout confirmed, set by external state machine）
	Matched     []string `json:"matched"`          // 匹配到的 D1 事件规则列表（Matched D1 event rules）
	Reason      string   `json:"reason,omitempty"` // 失败原因或备注（Failure reason or remarks）
	D2Desc      string   `json:"d2_desc"`          // D2 相对强度描述（D2 relative-strength description）
	D3Desc      string   `json:"d3_desc"`          // D3 超跌确认描述（D3 oversold description）
	D4Desc      string   `json:"d4_desc"`          // D4 资金确认描述（D4 fund description）
}

// LeftSideScorer N 形策略左侧评分器。（LeftSideScorer is the N-shape left-side scorer.）
// 负责 D1~D4 四维评分计算和有效性判断。（Computes the D1~D4 scores and validity.）
type LeftSideScorer struct {
	matcher *data.EventMatcher // 事件匹配器，用于 D1 评分（Event matcher for D1 scoring）
}

// NewLeftSideScorer 创建左侧评分器实例。（NewLeftSideScorer creates a left-side scorer.）
func NewLeftSideScorer(matcher *data.EventMatcher) *LeftSideScorer {
	return &LeftSideScorer{matcher: matcher}
}

// Evaluate 完整评分入口。按顺序执行 D1→D2→D3→D4 评分。（Evaluate is the full scoring entry, running D1→D2→D3→D4 in order.）
//
// 评分流程：
//  1. 情绪硬闸检查（"衰退"阶段直接返回）
//  2. D1 事件评分 → 三段式：YAML 负面阻断 → LLM 评分 → YAML 正面兜底
//     若 LLM 判定利空（LLMBlocked=true）直接 blocked；
//     若 LLM 有正向评分（LLMD1Score>0）则以其为准（×MaxD1）；
//     若 LLM 无结果则走 YAML EventMatcher 兜底。
//     信号硬闸从 D1=40 放宽到 D1>0。
//  3. 板块冷热检查：板块成交 < 20日均量×2 → sector_cold
//  4. 事件预支检查：5日涨幅 > 40% → pre_overdrawn
//  5. D2 相对强度评分（集合竞价+量比+超额收益）
//  6. D3 超跌评分（PE 或斐波那契深度）
//  7. D4 资金评分（MACD+量能）
//  8. 计算总分，判断 full_chain 有效性（D1>0 且 总分≥60）
//  9. 10:00 后降级处理
//  10. 左侧一突信号检测
//
// （English: 1 emotion hard-block → 2 three-stage D1 → 3 sector-cold check → 4 event pre-priced check → 5 D2 → 6 D3 →
// 7 D4 → 8 total + validity (D1>0 and ≥60) → 9 post-10:00 downgrade → 10 left-signal detection.）
func (s *LeftSideScorer) Evaluate(wa *WaveA, ib *IntradayB, ctx *Ctx) *ScoreResult {
	res := &ScoreResult{}

	// 情绪硬闸：衰退期一律拒绝入场（可能伴随流动性枯竭/恐慌）（Emotion hard-block: veto recessions — likely liquidity drought/panic）
	if emotionHardBlock[ctx.EmotionPhase] {
		res.Reason = "emotion_recession_block"
		return res
	}

	// D1: 事件硬闸 — 三段式计算（YAML 负面阻断 → LLM 评分 → YAML 正面兜底）（D1 event gate: YAML negative block → LLM → YAML backstop）
	d1, tags, blocked := s.calcD1(ctx)
	res.Matched = tags
	if blocked {
		// 负面阻断：记录命中的第一条负面规则作为失败原因（Record the first negative rule hit as the failure reason）
		res.Reason = "d1_neg:" + (func() string {
			if len(tags) > 0 {
				return tags[0]
			}
			return ""
		})()
		return res
	}
	res.D1Event = d1

	// 板块冷清检查：板块当日成交 < 20日均量×2 → 流动性不足，否决（Sector-cold check: turnover < 20-day avg ×2 → illiquid, veto）
	if ctx.SectorTurnoverMA20 > 0 && ctx.SectorTurnover < ctx.SectorTurnoverMA20*2.0 {
		res.Reason = "sector_cold"
	}
	// 事件预支检查：事件发生后 5 日内涨幅已超 40%，预期已被透支（Pre-priced check: >40% gain within 5 days → expectations exhausted）
	if ctx.PreEventReturn5d > 0.40 {
		res.Reason = "pre_overdrawn"
	}

	// D2: 三层受益 proxy（集合竞价强度+量比+超额收益）
	// 同时按时间窗口与情绪周期计算优先级基础分（D2 three-layer proxies; also compute priority base from time/emotion）
	prio := priorityOf(ib.TTime, d1, wa.IsSectorLeader, ctx.EmotionPhase)
	res.Priority = prio.Level
	res.RemindLevel = prio.Label
	res.CanOpen = prio.CanOpen

	d2 := s.calcD2(wa, ib)
	res.D2RS = d2
	res.D2Desc = d2desc(wa, ib, d2)

	// D3: 超跌确认 — PE或斐波那契回调深度（D3 oversold — PE or Fibonacci pullback depth）
	d3 := s.calcD3(wa, ib, ctx)
	res.D3Pullback = d3
	res.D3Desc = d3desc(wa, ib, ctx, d3)

	// D4: 资金确认 — MACD水上 + 量能放大（D4 fund — MACD above zero-line + volume expansion）
	d4 := s.calcD4(ib, ctx.AvgDailyVol)
	res.D4Accept = d4
	res.D4Desc = d4desc(ib, ctx.AvgDailyVol, d4)

	res.Total = res.D1Event + res.D2RS + res.D3Pullback + res.D4Accept

	// 评分始终计算，但信号硬闸门：D1>0 且 总分≥60 才 Valid
	// D1 标准从 ==40 放宽到 >0，使 LLM 打分的个股也能通过闸门；
	// D2/D3/D4 仅贡献总分（软门槛），不单独拦截信号。
	// （Scores always computed, but only D1>0 with total ≥60 is valid — D1 relaxed from ==40 to >0 for LLM-scored stocks;
	// D2/D3/D4 are soft contributors only.）
	if d1 > 0 && res.Total >= Threshold {
		res.Valid = true
	} else if d1 > 0 {
		// D1 有分但总分不足（D2/D3/D4 凑分不够）（D1 scored but total insufficient after D2/D3/D4）
		if res.Reason == "" {
			res.Reason = "total_below_threshold"
		} else {
			res.Reason += ";total_below_threshold"
		}
	} else {
		// D1 未满分（事件强度不足），无 D1 分无信号（D1 not scored — insufficient event strength, no signal）
		if res.Reason == "" {
			res.Reason = "d1_not_full"
		} else {
			res.Reason += ";d1_not_full"
		}
	}

	// 10:00 后降级：即使 Valid，但时间优先级不足 strong 则降级为 observe 不可开仓
	// （黄金窗口过后追高风险上升，需要更严格的入场条件）（After 10:00, downgrade to observe/no-open unless priority is strong —
	// chasing risk rises past the golden window.）
	if res.Valid && res.Priority < StrongMin {
		res.RemindLevel = "observe"
		res.CanOpen = false
		if res.Reason == "" {
			res.Reason = "post_10am_downgraded"
		} else {
			res.Reason = "post_10am_downgraded:" + res.Reason
		}
	}

	// 一突信号检测: 当前价 > 前日最高价×1.005 且量比 ≥ 1.8
	// 说明已有主力资金开始攻击，是左侧抢先入场信号（Left-breakout detection: price > prev-high×1.005 with volume ratio ≥1.8 —
	// main capital is attacking, a left-side early-entry signal）
	if ib.CurPrice > ib.PrevHigh*1.005 && ib.CumVol > 0 && ib.PrevClose > 0 {
		// 量比用当日累计量 / 前日最低价量级近似（Volume ratio approximated as cumulative volume scaled by prior low）
		volRatio := ib.CumVol / math.Max(ib.PrevLow, 1)
		if volRatio >= 1.8 {
			res.LeftSignal = true
		}
	}

	// 右侧信号: 二突确认后设置（Right signal: set once the second breakout is confirmed）
	// RightSignal 由外部状态机设置（RightSignal is set by the external state machine）

	return res
}

// calcD1 计算 D1 事件评分。两段式计算：（calcD1 computes the D1 event score in two stages.）
//
//	Stage 1: YAML 负面阻断（始终执行，硬闸）
//	  EventMatcher 只做 negative_filter，命中即 blocked。
//
//	Stage 2: LLM 评分（优先于 YAML）
//	  若 LLM 判定利空（LLMBlocked=true），直接返回 blocked。
//	  否则以 LLMD1Score × MaxD1 作为 D1 得分。
//
// D1 评分收拢到 combat_agent/d1_scorer，此方法仅在 LLM 结果已传入时调用。
// （Stage 1 YAML negative block (hard gate); Stage 2 LLM score takes priority — blocked on LLMBlocked, else LLMD1Score×MaxD1.）
func (s *LeftSideScorer) calcD1(ctx *Ctx) (float64, []string, bool) {
	// Stage 1: YAML 负面阻断（Stage 1: YAML negative block）
	if ctx.EventDesc != "" && ctx.EventDesc != "null" && s.matcher != nil {
		mr := s.matcher.MatchD1(ctx.EventDesc)
		if mr.Blocked {
			return 0, []string{mr.BlockReason}, true
		}
	}

	// Stage 2: LLM 评分（Stage 2: LLM scoring）
	if ctx.LLMBlocked {
		return 0, []string{"llm_blocked"}, true
	}
	if ctx.LLMD1Score > 0 {
		return ctx.LLMD1Score * MaxD1, []string{"llm_d1"}, false
	}

	return 0, nil, false
}

// calcD2 计算 D2 相对强度评分（满分 30）。（calcD2 computes D2 relative strength, max 30.）
//
// PRD 公式:
//
//	D2 = D2a(竞价强度) + D2b(量比) + D2c(超额收益)
//
// D2a 竞价强度（15分上限）:
//   - 竞价涨幅 1.5%~5%: 15分（最理想，温和高开有空间）
//   - 竞价涨幅 > 5%: 10分（过高的开盘可能透支涨幅）
//   - 竞价涨幅 < 0%: 2分（低开但可能弱转强）
//
// D2b 量比（8分上限）:
//   - 量比 = 当前累计量 / (日均量 × 时间进度比例)
//   - 1.2~1.8: 4分（温和放量）
//   - 1.8~3.0: 8分（理想放量，资金有序介入）
//   - > 3.0: 3分（放量过度，一致性过强易反转）
//
// D2c 超额收益（7分上限）:
//   - 超额 = 个股涨幅 - 基准涨幅
//   - 每 3% 超额得 7 分，线性插值，上限 7 分
//
// （English: auction strength 15 (1.5~5% best, >5% = 10, <0% = 2) + volume ratio 8 (1.2~1.8 = 4, 1.8~3.0 = 8, >3.0 = 3) +
// excess return 7 (linear, +7 per 3% above the benchmark, capped).）
func (s *LeftSideScorer) calcD2(wa *WaveA, ib *IntradayB) float64 {
	score := 0.0
	switch {
	case ib.AuctionChgPct >= 1.5 && ib.AuctionChgPct <= 5.0:
		score += 15.0
	case ib.AuctionChgPct > 5.0:
		score += 10.0
	case ib.AuctionChgPct < 0:
		score += 2.0
	}
	// D2b: 量比 = CumVol / (AvgDailyVol × 时间进度)（Volume ratio = CumVol / (AvgDailyVol × time progress)）
	if ib.AvgDailyVol > 0 && ib.CumVol > 0 {
		mins := float64(ib.TTime/100*60 + ib.TTime%100 - 570)
		if mins < 0 {
			mins = 0
		}
		timeRatio := mins / 330.0
		if timeRatio < 0.1 {
			timeRatio = 0.1
		}
		ratio := safeDiv(ib.CumVol, ib.AvgDailyVol*timeRatio)
		switch {
		case ratio >= 1.8 && ratio <= 3.0:
			score += 8.0
		case ratio > 3.0:
			score += 3.0
		case ratio >= 1.2 && ratio < 1.8:
			score += 4.0
		}
	}
	stockChg := safeDiv(ib.CurPrice, ib.PrevClose) - 1
	excess := stockChg - ib.BenchCurChg
	score += math.Min(7.0, math.Max(0.0, excess/0.03*7.0))
	return math.Min(score, MaxD2)
}

// calcD3 计算 D3 超跌确认评分（满分 20）。（calcD3 computes D3 oversold-pullback, max 20.）
//
// PRD v1.0 公式：
//
//	有PE数据时，按估值分档：
//	  PE < 15:  20分（明显低估）
//	  PE < 30:  10分（合理偏低）
//	  PE < 50:  5分（中性）
//	  PE ≥ 50:  0分（高估，不超跌）
//
//	无PE数据时（fallback），用斐波那契回调深度：
//	  深度 = (A波高点 - 当前价) / (A波高点 - A波低点)
//	  0.382~0.618: 12分（黄金回调区，最理想）
//	  0.200~0.382: 16分（浅回调，强度高）
//	  0.618~1.000: 8分（深回调，偏弱）
//	  其他:        0分
//
// （English: with PE — <15→20, <30→10, <50→5, ≥50→0. Without PE (fallback) — Fibonacci depth 0.382~0.618→12, 0.2~0.382→16,
// 0.618~1.0→8, else 0.）
func (s *LeftSideScorer) calcD3(wa *WaveA, ib *IntradayB, ctx *Ctx) float64 {
	if ctx.StockPE > 0 {
		if ctx.StockPE < 15 {
			return MaxD3
		}
		if ctx.StockPE < 30 {
			return MaxD3 * 0.5
		}
		if ctx.StockPE < 50 {
			return MaxD3 * 0.25
		}
		return 0
	}
	span := wa.AHigh - wa.ALow
	if span <= 0 {
		return 0
	}
	depth := safeDiv(wa.AHigh-ib.CurPrice, span)
	switch {
	case depth >= 0.382 && depth <= 0.618:
		return MaxD3 * 0.6
	case depth >= 0.2 && depth < 0.382:
		return MaxD3 * 0.8
	case depth > 0.618 && depth <= 1.0:
		return MaxD3 * 0.4
	default:
		return 0
	}
}

// calcD4 计算 D4 资金确认评分（满分 10）。（calcD4 computes D4 fund confirmation, max 10.）
//
// PRD 公式:
//
//	D4 = D4a(MACD水上) + D4b(量能放大)
//
// D4a MACD水上（5分）:
//
//	DIF > DEA 且 DIF > 0 → 5分（趋势多头，资金持续流入）
//
// D4b 量能放大（5分）:
//
//	当日累计量 > 20日均量 × 1.5（按时间进度折算）
//	→ 说明当日有增量资金入场
//
// （English: 5 pts for MACD above zero (DIF>DEA and DIF>0) + 5 pts when cumulative volume > 20-day avg ×1.5 (time-scaled).）
func (s *LeftSideScorer) calcD4(ib *IntradayB, avgVol float64) float64 {
	score := 0.0
	// MACD水上: DIF > DEA AND DIF > 0（MACD above zero line: DIF > DEA and DIF > 0）
	if ib.MinuteMACDDIF > ib.MinuteMACDDEA && ib.MinuteMACDDIF > 0 {
		score += 5.0
	}
	// 量能放大: 当日累计量 > 20日均量 × 1.5 (按时间比例折算)（Volume expansion: cumulative volume > 20-day avg ×1.5, scaled by time progress）
	if ib.CumVol > 0 && avgVol > 0 {
		// 当天时间进度 (从9:30开始到15:00 = 330分钟)（Time progress from 9:30 to 15:00 = 330 minutes）
		mins := float64(ib.TTime/100*60 + ib.TTime%100 - 570) // 570=9:30（570 = 9:30）
		if mins < 0 {
			mins = 0
		}
		if mins > 330 {
			mins = 330
		}
		timeRatio := mins / 330.0
		if timeRatio < 0.1 {
			timeRatio = 0.1
		}
		expectedVol := avgVol * timeRatio
		if ib.CumVol > expectedVol*1.5 {
			score += 5.0
		}
	}
	return math.Min(score, MaxD4)
}

// morphologyGate 形态学前置过滤。在评分前检查 K 线形态是否满足 N 形基本要求。（morphologyGate is a pre-scoring morphological gate.）
// 返回空字符串表示通过检查；非空字符串为失败原因（如 broke_a_low / weak_wave_a 等）。（Empty string = pass; otherwise the failure reason e.g. broke_a_low / weak_wave_a.）
func morphologyGate(wa *WaveA, ib *IntradayB) string {
	if ib.CurPrice < wa.ALow {
		return "broke_a_low" // 当前价跌破 A 波低点，形态破坏（Price broke below the A-wave low, formation broken）
	}
	if wa.AChgPct < 0.05 {
		return "weak_wave_a" // A 波涨幅不足 5%，不够强势（A wave gained <5%, not strong enough）
	}
	if !wa.AAboveMA60 {
		return "a_not_above_ma60" // A 波未站上 60 日线，中期趋势未确认（A wave below the 60-day MA, mid-term trend unconfirmed）
	}
	if wa.IsSectorLeader && !wa.PrevSessionWeak && ib.AuctionChgPct > 5 {
		return "not_weak_to_strong" // 龙头股竞价过高（>5%）但前日非弱，不属弱转强模式（Leader auction >5% with a strong prior day — not a weak-to-strong setup）
	}
	return ""
}

// --- helpers ---
// safeDiv 安全除法，分母为 0 时返回 0（避免评分出现 NaN/Inf）。（safeDiv divides safely, returning 0 on a zero denominator to avoid NaN/Inf.）
func safeDiv(a, b float64) float64 {
	if b == 0 {
		return 0
	}
	return a / b
}

// maxInt 返回两个整数中的较大值。（maxInt returns the larger of two ints.）
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// minInt 返回两个整数中的较小值。（minInt returns the smaller of two ints.）
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// itoa 将整数转为十进制字符串（Go 内置 strconv.Itoa 的简化等价）。（itoa formats an int to a decimal string.）
func itoa(i int) string {
	return fmt.Sprintf("%d", i)
}

// d1desc 生成 D1 事件维度的描述文本：无匹配返回"无事件"，否则拼接事件标签。（d1desc builds the D1 description: "无事件" when empty, else joined tags.）
func d1desc(tags []string) string {
	if len(tags) == 0 {
		return "无事件"
	}
	return "事件:" + strings.Join(tags, ",")
}

// d2desc 生成 D2 相对强度维度的描述：按 竞价强弱/放量程度/超额收益 组合中文短语。（d2desc builds the D2 description from auction strength, volume and excess return.）
func d2desc(wa *WaveA, ib *IntradayB, score float64) string {
	var parts []string
	// 竞价强度档位（Auction strength tier）
	if ib.AuctionChgPct >= 1.5 && ib.AuctionChgPct <= 5 {
		parts = append(parts, "竞价强")
	} else if ib.AuctionChgPct > 5 {
		parts = append(parts, "竞价过强")
	} else if ib.AuctionChgPct < 0 {
		parts = append(parts, "竞价弱")
	}
	// 量比档位（与 calcD2 同一时间进度口径）（Volume-ratio tier, same time-progress basis as calcD2）
	if ib.AvgDailyVol > 0 && ib.CumVol > 0 {
		mins := float64(ib.TTime/100*60 + ib.TTime%100 - 570)
		if mins < 0 {
			mins = 0
		}
		timeRatio := mins / 330.0
		if timeRatio < 0.1 {
			timeRatio = 0.1
		}
		ratio := safeDiv(ib.CumVol, ib.AvgDailyVol*timeRatio)
		if ratio >= 1.8 {
			parts = append(parts, "放量")
		} else if ratio >= 1.2 {
			parts = append(parts, "量平")
		} else {
			parts = append(parts, "缩量")
		}
	}
	// 相对大盘超额收益（Excess return vs. benchmark）
	stockChg := safeDiv(ib.CurPrice, ib.PrevClose) - 1
	excess := stockChg - ib.BenchCurChg
	if excess > 0.02 {
		parts = append(parts, "超额收益")
	} else if excess < -0.02 {
		parts = append(parts, "落后大盘")
	}
	if len(parts) == 0 {
		return fmt.Sprintf("强度%.0f分", score)
	}
	return strings.Join(parts, ",")
}

// d3desc 生成 D3 超跌确认维度的描述：有 PE 时按估值档位，否则按回撤深度区间。（d3desc builds the D3 description by PE band or pullback-depth range.）
func d3desc(wa *WaveA, ib *IntradayB, ctx *Ctx, score float64) string {
	// PE 估值档位（PE valuation band）
	if ctx.StockPE > 0 {
		if ctx.StockPE < 15 {
			return "PE低估"
		} else if ctx.StockPE < 30 {
			return "PE合理偏低"
		} else if ctx.StockPE < 50 {
			return "PE中性"
		}
		return "PE高估"
	}
	span := wa.AHigh - wa.ALow
	if span <= 0 {
		return "无回撤"
	}
	depth := safeDiv(wa.AHigh-ib.CurPrice, span)
	switch {
	case depth >= 0.382 && depth <= 0.618:
		return "黄金回撤区"
	case depth >= 0.2 && depth < 0.382:
		return "浅回调"
	case depth > 0.618 && depth <= 1.0:
		return "深回调"
	}
	return fmt.Sprintf("回撤%.0f分", score)
}

// d4desc 生成 D4 资金确认维度的描述：MACD 水上/水下 + 增量资金/量能平平。（d4desc builds the D4 description: MACD above/below zero + inflow/flat volume.）
func d4desc(ib *IntradayB, avgVol float64, score float64) string {
	var parts []string
	if ib.MinuteMACDDIF > ib.MinuteMACDDEA && ib.MinuteMACDDIF > 0 {
		parts = append(parts, "MACD水上")
	} else {
		parts = append(parts, "MACD水下")
	}
	// 量能对比（与 calcD4 同一时间进度折算口径）（Volume comparison, same time-progress basis as calcD4）
	if ib.CumVol > 0 && avgVol > 0 {
		mins := float64(ib.TTime/100*60 + ib.TTime%100 - 570)
		if mins < 0 {
			mins = 0
		}
		if mins > 330 {
			mins = 330
		}
		timeRatio := mins / 330.0
		if timeRatio < 0.1 {
			timeRatio = 0.1
		}
		expectedVol := avgVol * timeRatio
		if ib.CumVol > expectedVol*1.5 {
			parts = append(parts, "增量资金")
		} else {
			parts = append(parts, "量能平平")
		}
	}
	if len(parts) == 0 {
		return fmt.Sprintf("资金%.0f分", score)
	}
	return strings.Join(parts, ",")
}
