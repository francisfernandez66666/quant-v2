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
//     阈值：D2 ≥ 15 才构成完整链（full_chain）
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
// 评分始终计算（D1不足时也出总分），信号硬闸门：D1>0 且 总分≥60 且 D2≥15 → Valid=true → 才生成信号
// 时间衰减：10:00后优先级降低，14:30后再降低。
// 情绪否决："衰退"阶段禁止入场；"退潮"减30基础分；"高潮"减20基础分。
package n_shape

import (
	"fmt"
	"math"
	"strings"

	"quant-trading-v2/internal/data"
)

const (
	MaxD1      = 40.0 // D1 事件维度满分
	MaxD2      = 30.0 // D2 相对强度维度满分
	MaxD3      = 20.0 // D3 超跌维度满分
	MaxD4      = 10.0 // D4 资金维度满分
	Threshold  = 60.0 // 总分通过阈值（需≥60 才视为有效信号）
	StrongMin  = 80   // 高优先级基础分阈值（≥80 可开仓）
	ObserveMin = 40   // 可观察最低基础分（<40 直接 mute）

	D2FullThreshold = 15.0 // D2 完整链阈值（需要 ≥15 才算完整信号）
)

// 时间窗口定义（HHMM 整数格式，用于优先级计算）。
const (
	t915  = 915  // 9:15 集合竞价开始
	t920  = 920  // 9:20 不可撤单
	t925  = 925  // 9:25 集合竞价结束
	t935  = 935  // 9:35 开盘后5分钟
	t1000 = 1000 // 10:00
	t1030 = 1030 // 10:30
	t1130 = 1130 // 11:30 午休
	t1300 = 1300 // 13:00 下午开盘
	t1400 = 1400 // 14:00
	t1430 = 1430 // 14:30
	t1457 = 1457 // 14:57 尾盘集合竞价开始
	t1500 = 1500 // 15:00 收盘
)

// emotionHardBlock 情绪硬闸：标记不可操作的周期阶段。
// "衰退"阶段禁止任何开仓操作。
var emotionHardBlock = map[string]bool{"衰退": true}

// PriorityResult 时间优先级计算结果。
// Level 为基础分（0~100），Label 为等级（strong/observe/mute），CanOpen 表示是否允许开仓。
type PriorityResult struct {
	Level   int    `json:"level"`    // 优先级基础分
	Label   string `json:"label"`    // 提醒级别（strong/observe/mute）
	CanOpen bool   `json:"can_open"` // 是否允许开仓
}

// priorityOf 根据时间、D1分数、是否龙头、情绪周期计算当前时刻的优先级。
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
		mins := float64(t/100*60 + t%100 - 600) // 10:00起分钟数
		blocks := math.Floor(mins / 30.0)
		base = int(90.0 - 5.0*blocks)
	case t >= t1300 && t <= t1500:
		mins := float64(t/100*60 + t%100 - 780) // 13:00起分钟数
		blocks := math.Floor(mins / 30.0)
		base = int(90.0 - 7.5*blocks)
	default:
		base = 0
	}
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

// WaveA 昨日波形（A波）数据。
// 用于 N 形形态识别：A波是 N 形的"第一竖"，昨日已完成的拉升波形。
type WaveA struct {
	ADate                      string  // 日期
	AOpen, AHigh, ALow, AClose float64 // 昨开/昨高/昨低/昨收
	AVol                       float64 // 昨日成交量
	AChgPct                    float64 // 昨日涨幅百分比
	AAboveMA60                 bool    // 昨日收盘是否站上60日均线（中期趋势多头）
	IsSectorLeader             bool    // 是否为板块内龙头股
	PrevSessionWeak            bool    // 前日是否弱势（用于排除弱转强假突破）
}

// IntradayB 日内快照（B段）数据。
// 包含竞价数据、当前价格/量、MACD 指标等，是评分的主要输入。
type IntradayB struct {
	TTime         int     // 当前时间（HHMM 整数格式，如 935=9:35）
	CurPrice      float64 // 当前最新价格
	CumVol        float64 // 当日累计成交量（手）
	AuctionVol    float64 // 集合竞价成交量
	AuctionHigh   float64 // 集合竞价最高价
	AuctionLow    float64 // 集合竞价最低价
	AuctionChgPct float64 // 集合竞价涨跌幅（%）
	AuctionTrend  string  // 集合竞价趋势描述（如 "上行"/"下行"/"平开"）
	BenchCurChg   float64 // 基准指数（大盘/创业板）当前涨跌幅
	EventType     string  // 当日事件类型
	PrevClose     float64 // 前日收盘价
	PrevHigh      float64 // 前日最高价
	PrevLow       float64 // 前日最低价
	AvgDailyVol   float64 // 20日均量（用于量比计算）

	// 分钟级 MACD 指标，用于 D4 资金确认。
	MinuteMACDDIF float64 // MACD DIF 快线
	MinuteMACDDEA float64 // MACD DEA 慢线
	MinuteMACDBar float64 // MACD 柱状图值
}

// Ctx 策略运行上下文，包含板块、情绪、LLM 等多种数据。
type Ctx struct {
	// LLM D1 评分字段
	// LLMD1Score > 0 时优先使用 LLM 评分（替换 YAML EventMatcher 的得分）。
	// LLMBlocked 为 true 时直接否决（LLM 判定为利空）。
	LLMD1Score float64 // LLM 给出的个股 D1 评分（0.0~1.0），0 表示无 LLM 结果
	LLMBlocked bool    // LLM 判定利空时阻塞（calcD1 直接返回 blocked）

	EmotionPhase       string  // 情绪周期阶段（"冰点"/"启动"/"高潮"/"退潮"/"衰退"）
	EventDesc          string  // 事件描述文本（用于 D1 YAML 兜底匹配）
	SectorTurnover     float64 // 板块当日成交额
	SectorTurnoverMA20 float64 // 板块20日平均成交额（用于判断板块冷热）
	PreEventReturn5d   float64 // 事件预支回报率5日（大于40%可能已被提前透支）
	StockPE            float64 // 个股PE（用于D3超跌评分）
	AvgDailyVol        float64 // 20日均量（用于D4量能放大对比）
}

// ScoreResult N 形策略完整评分结果。
// 包含 D1~D4 各维分数、总分、有效性标志和信号标志。
type ScoreResult struct {
	D1Event     float64  `json:"d1"`               // D1 事件硬闸得分（0~40）
	D2RS        float64  `json:"d2"`               // D2 相对强度得分（0~30）
	D3Pullback  float64  `json:"d3"`               // D3 超跌确认得分（0~20）
	D4Accept    float64  `json:"d4"`               // D4 资金确认得分（0~10）
	Total       float64  `json:"total"`            // 总分（D1+D2+D3+D4，0~100）
	Valid       bool     `json:"valid"`            // 是否有效信号（D1=40 且 总分≥60 且 D2≥15）
	Priority    int      `json:"priority"`         // 时间优先级分数（0~100）
	RemindLevel string   `json:"remind"`           // 提醒级别（strong/observe/mute）
	CanOpen     bool     `json:"can_open"`         // 是否允许开仓
	LeftSignal  bool     `json:"left_signal"`      // 左侧一突信号（价格突破前高且量比≥1.8）
	RightSignal bool     `json:"right_signal"`     // 右侧信号（二突破确认，由外部状态机设置）
	Matched     []string `json:"matched"`          // 匹配到的 D1 事件规则列表
	Reason      string   `json:"reason,omitempty"` // 失败原因或备注
	D2Desc      string   `json:"d2_desc"`          // D2 相对强度描述
	D3Desc      string   `json:"d3_desc"`          // D3 超跌确认描述
	D4Desc      string   `json:"d4_desc"`          // D4 资金确认描述
}

// LeftSideScorer N 形策略左侧评分器。
// 负责 D1~D4 四维评分计算和有效性判断。
type LeftSideScorer struct {
	matcher *data.EventMatcher // 事件匹配器，用于 D1 评分
}

// NewLeftSideScorer 创建左侧评分器实例。
func NewLeftSideScorer(matcher *data.EventMatcher) *LeftSideScorer {
	return &LeftSideScorer{matcher: matcher}
}

// Evaluate 完整评分入口。按顺序执行 D1→D2→D3→D4 评分。
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
//  8. 计算总分，判断 full_chain 有效性（D1>0 且 总分≥60 且 D2≥15）
//  9. 10:00 后降级处理
//  10. 左侧一突信号检测
func (s *LeftSideScorer) Evaluate(wa *WaveA, ib *IntradayB, ctx *Ctx) *ScoreResult {
	res := &ScoreResult{}

	if emotionHardBlock[ctx.EmotionPhase] {
		res.Reason = "emotion_recession_block"
		return res
	}

	// D1: 事件硬闸 — 三段式计算（YAML 负面阻断 → LLM 评分 → YAML 正面兜底）
	d1, tags, blocked := s.calcD1(ctx)
	res.Matched = tags
	if blocked {
		res.Reason = "d1_neg:" + (func() string {
			if len(tags) > 0 {
				return tags[0]
			}
			return ""
		})()
		return res
	}
	res.D1Event = d1

	// 板块冷清检查：板块当日成交 < 20日均量×2 → 流动性不足，否决
	if ctx.SectorTurnoverMA20 > 0 && ctx.SectorTurnover < ctx.SectorTurnoverMA20*2.0 {
		res.Reason = "sector_cold"
	}
	// 事件预支检查：事件发生后 5 日内涨幅已超 40%，预期已被透支
	if ctx.PreEventReturn5d > 0.40 {
		res.Reason = "pre_overdrawn"
	}

	// D2: 三层受益 proxy（集合竞价强度+量比+超额收益）
	prio := priorityOf(ib.TTime, d1, wa.IsSectorLeader, ctx.EmotionPhase)
	res.Priority = prio.Level
	res.RemindLevel = prio.Label
	res.CanOpen = prio.CanOpen

	d2 := s.calcD2(wa, ib)
	res.D2RS = d2
	res.D2Desc = d2desc(wa, ib, d2)

	// D3: 超跌确认 — PE或斐波那契回调深度
	d3 := s.calcD3(wa, ib, ctx)
	res.D3Pullback = d3
	res.D3Desc = d3desc(wa, ib, ctx, d3)

	// D4: 资金确认 — MACD水上 + 量能放大
	d4 := s.calcD4(ib, ctx.AvgDailyVol)
	res.D4Accept = d4
	res.D4Desc = d4desc(ib, ctx.AvgDailyVol, d4)

	res.Total = res.D1Event + res.D2RS + res.D3Pullback + res.D4Accept

	// 评分始终计算，但信号硬闸门：D1>0 且 总分≥60 且 D2≥15 才 Valid
	// D1 标准从 ==40 放宽到 >0，使 LLM 打分的个股也能通过闸门。
	if d1 > 0 && res.Total >= Threshold && d2 >= D2FullThreshold {
		res.Valid = true
	} else if d1 >= MaxD1 && res.Total >= Threshold && d2 < D2FullThreshold {
		if res.Reason == "" {
			res.Reason = "d2_below_full"
		} else {
			res.Reason += ";d2_below_full"
		}
	} else if d1 < MaxD1 {
		if res.Reason == "" {
			res.Reason = "d1_not_full"
		} else {
			res.Reason += ";d1_not_full"
		}
	}

	// 10:00 后降级：即使 Valid，但时间优先级不足 strong 则降级为 observe 不可开仓
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
	// 说明已有主力资金开始攻击，是左侧抢先入场信号
	if ib.CurPrice > ib.PrevHigh*1.005 && ib.CumVol > 0 && ib.PrevClose > 0 {
		volRatio := ib.CumVol / math.Max(ib.PrevLow, 1)
		if volRatio >= 1.8 {
			res.LeftSignal = true
		}
	}

	// 右侧信号: 二突确认后设置
	// RightSignal 由外部状态机设置

	return res
}

// calcD1 计算 D1 事件评分。两段式计算：
//
//	Stage 1: YAML 负面阻断（始终执行，硬闸）
//	  EventMatcher 只做 negative_filter，命中即 blocked。
//
//	Stage 2: LLM 评分（优先于 YAML）
//	  若 LLM 判定利空（LLMBlocked=true），直接返回 blocked。
//	  否则以 LLMD1Score × MaxD1 作为 D1 得分。
//
// D1 评分收拢到 combat_agent/d1_scorer，此方法仅在 LLM 结果已传入时调用。
func (s *LeftSideScorer) calcD1(ctx *Ctx) (float64, []string, bool) {
	// Stage 1: YAML 负面阻断
	if ctx.EventDesc != "" && ctx.EventDesc != "null" && s.matcher != nil {
		mr := s.matcher.MatchD1(ctx.EventDesc)
		if mr.Blocked {
			return 0, []string{mr.BlockReason}, true
		}
	}

	// Stage 2: LLM 评分
	if ctx.LLMBlocked {
		return 0, []string{"llm_blocked"}, true
	}
	if ctx.LLMD1Score > 0 {
		return ctx.LLMD1Score * MaxD1, []string{"llm_d1"}, false
	}

	return 0, nil, false
}

// calcD2 计算 D2 相对强度评分（满分 30）。
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
	// D2b: 量比 = CumVol / (AvgDailyVol × 时间进度)
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

// calcD3 计算 D3 超跌确认评分（满分 20）。
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

// calcD4 计算 D4 资金确认评分（满分 10）。
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
func (s *LeftSideScorer) calcD4(ib *IntradayB, avgVol float64) float64 {
	score := 0.0
	// MACD水上: DIF > DEA AND DIF > 0
	if ib.MinuteMACDDIF > ib.MinuteMACDDEA && ib.MinuteMACDDIF > 0 {
		score += 5.0
	}
	// 量能放大: 当日累计量 > 20日均量 × 1.5 (按时间比例折算)
	if ib.CumVol > 0 && avgVol > 0 {
		// 当天时间进度 (从9:30开始到15:00 = 330分钟)
		mins := float64(ib.TTime/100*60 + ib.TTime%100 - 570) // 570=9:30
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

// morphologyGate 形态学前置过滤。在评分前检查 K 线形态是否满足 N 形基本要求。
// 返回空字符串表示通过检查；非空字符串为失败原因（如 broke_a_low / weak_wave_a 等）。
func morphologyGate(wa *WaveA, ib *IntradayB) string {
	if ib.CurPrice < wa.ALow {
		return "broke_a_low" // 当前价跌破 A 波低点，形态破坏
	}
	if wa.AChgPct < 0.05 {
		return "weak_wave_a" // A 波涨幅不足 5%，不够强势
	}
	if !wa.AAboveMA60 {
		return "a_not_above_ma60" // A 波未站上 60 日线，中期趋势未确认
	}
	if wa.IsSectorLeader && !wa.PrevSessionWeak && ib.AuctionChgPct > 5 {
		return "not_weak_to_strong" // 龙头股竞价过高（>5%）但前日非弱，不属弱转强模式
	}
	return ""
}

// --- helpers ---
// safeDiv 安全除法，分母为 0 时返回 0。
func safeDiv(a, b float64) float64 {
	if b == 0 {
		return 0
	}
	return a / b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func itoa(i int) string {
	return fmt.Sprintf("%d", i)
}

func d1desc(tags []string) string {
	if len(tags) == 0 {
		return "无事件"
	}
	return "事件:" + strings.Join(tags, ",")
}

func d2desc(wa *WaveA, ib *IntradayB, score float64) string {
	var parts []string
	if ib.AuctionChgPct >= 1.5 && ib.AuctionChgPct <= 5 {
		parts = append(parts, "竞价强")
	} else if ib.AuctionChgPct > 5 {
		parts = append(parts, "竞价过强")
	} else if ib.AuctionChgPct < 0 {
		parts = append(parts, "竞价弱")
	}
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

func d3desc(wa *WaveA, ib *IntradayB, ctx *Ctx, score float64) string {
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

func d4desc(ib *IntradayB, avgVol float64, score float64) string {
	var parts []string
	if ib.MinuteMACDDIF > ib.MinuteMACDDEA && ib.MinuteMACDDIF > 0 {
		parts = append(parts, "MACD水上")
	} else {
		parts = append(parts, "MACD水下")
	}
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
