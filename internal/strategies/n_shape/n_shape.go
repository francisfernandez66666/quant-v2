// Package n_shape 实现 N 形超短策略。
//
// 市场模式：识别股价在日内走出"第一波拉升→小幅回调(旗形整理)→第二波拉升"的 N 形结构。
// 这是典型的超短线动量突破形态，常见于强势股开盘后的延续行情。
//
// 四维评分体系（D1~D4）：
//   - D1 事件硬闸（满分 40）：事件驱动匹配，事件正向且强度达标才可入场
//   - D2 相对强度 RS（满分 30）：集合竞价涨幅 + 量比 + 个股相对大盘超额收益
//   - D3 超跌确认（满分 20）：PE 估值超跌或斐波那契回调深度在 0.382~0.618 之间
//   - D4 资金确认（满分 10）：MACD 水上金叉 + 当日量能放大至 20 日均量 1.5 倍以上
//
// 信号生成阈值：
//   - full_chain（完整链）：D1≥40 且总分≥60 且 D2≥15 → 买入
//   - 置信度 ≥0.8 → P1；≥0.6 → P2；其余 P3
//   - 左侧一突信号（价格突破前高×1.005 且量比≥1.8）提升优先级至至少 P2
//
// 前置依赖：需提供 WaveA（昨日波形）、IntradayB（日内快照）、Ctx（板块&情绪上下文）。
package n_shape

import (
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/strategy"
)

// NShapeStrategy N 形超短策略主结构。
// 持有配置管理器和左侧评分器，通过 EvaluateWave 执行实时评分。
type NShapeStrategy struct {
	cfg    *config.Manager // 策略配置热加载
	scorer *LeftSideScorer // D1~D4 评分器
}

// New 创建 N 形策略实例。matcher 用于事件匹配（D1 评分依赖）。
func New(cfg *config.Manager, matcher *data.EventMatcher) *NShapeStrategy {
	return &NShapeStrategy{
		cfg:    cfg,
		scorer: NewLeftSideScorer(matcher),
	}
}

// Name 返回策略中文名称"N形超短"。
func (n *NShapeStrategy) Name() string {
	return "N形超短"
}

// Type 返回信号类型标识 SignalNShape。
func (n *NShapeStrategy) Type() strategy.SignalType {
	return strategy.SignalNShape
}

// Evaluate 标准接口（占位）。实际使用 EvaluateWave 传入结构化数据。
func (n *NShapeStrategy) Evaluate(code string, data interface{}) (*strategy.Evaluation, error) {
	return &strategy.Evaluation{Pass: false, Level: "nodata", Confidence: 0}, nil
}

// EvaluateWave 执行 N 形策略核心评分。
// 输入: wa（昨日波形）、ib（日内快照，含竞价/量能/MACD）、ctx（板块情绪&事件）
// 输出: Evaluation，包含 D1~D4 各维度分数、总分、是否 full_chain。
// 评分链路: scorer.Evaluate → D1/D2/D3/D4 → Total → Valid 判断。
func (n *NShapeStrategy) EvaluateWave(wa *WaveA, ib *IntradayB, ctx *Ctx) (*strategy.Evaluation, error) {
	sr := n.scorer.Evaluate(wa, ib, ctx)
	if sr == nil {
		return &strategy.Evaluation{Pass: false, Level: "noscore"}, nil
	}

	level := "fail"
	if sr.Valid {
		level = "full_chain"
	}

	return &strategy.Evaluation{
		TotalScore: sr.Total,
		Details: map[string]float64{
			"d1":          sr.D1Event,
			"d2":          sr.D2RS,
			"d3":          sr.D3Pullback,
			"d4":          sr.D4Accept,
			"prio":        float64(sr.Priority),
			"remind":      float64(remindToInt(sr.RemindLevel)),
			"canopen":     boolToFloat(sr.CanOpen),
			"left_signal": boolToFloat(sr.LeftSignal),
		},
		Reasons: map[string]string{
			"d1": d1desc(sr.Matched),
			"d2": sr.D2Desc,
			"d3": sr.D3Desc,
			"d4": sr.D4Desc,
		},
		Pass:       sr.Valid,
		Level:      level,
		Confidence: sr.Total / 100.0,
	}, nil
}

// GenerateSignal 将评分结果转化为交易信号。
// full_chain 级别生成 buy 信号，按置信度分 P1/P2/P3；
// 若左侧一突信号触发则最低 P2。
func (n *NShapeStrategy) GenerateSignal(code string, eval *strategy.Evaluation) (*strategy.Signal, error) {
	action := strategy.ActionWatch
	prio := strategy.P3

	switch eval.Level {
	case "full_chain":
		action = strategy.ActionBuy
		if eval.Confidence >= 0.8 {
			prio = strategy.P1
		} else if eval.Confidence >= 0.6 {
			prio = strategy.P2
		} else {
			prio = strategy.P3
		}
	}

	// 一突信号提高优先级: 价格突破前高且量比≥1.8 时，最低提升至 P2
	if d, ok := eval.Details["left_signal"]; ok && d > 0 {
		if prio > strategy.P2 {
			prio = strategy.P2
		}
	}

	meta := make(map[string]float64)
	for k, v := range eval.Details {
		meta[k] = v
	}

	return &strategy.Signal{
		Action:     action,
		Priority:   prio,
		Confidence: eval.Confidence,
		Reason:     eval.Level,
		Type:       strategy.SignalNShape,
		Meta:       meta,
	}, nil
}

// NPhase N 形日内状态机阶段。
// 跟踪从 idle 到 first_breakout → flag → second_breakout → completed/failed 的完整生命周期。
type NPhase int

const (
	NPhaseIdle           NPhase = 0 // 空闲，未检测到首次突破
	NPhaseFirstBreakout  NPhase = 1 // 第一波突破：价格快速拉高，伴随放量
	NPhaseFlag           NPhase = 2 // 旗形整理：小幅回调，成交量萎缩
	NPhaseSecondBreakout NPhase = 3 // 第二波突破：再次放量拉升突破前高
	NPhaseCompleted      NPhase = 4 // N 形完成：二突破确认后完成形态
	NPhaseFailed         NPhase = 5 // 形态失败：回调过深或二突破未出现
)

// remindToInt 将提醒级别文本转为数值（用于前端展示）。
// strong=3, observe=2, mute=1, 其他=0。
func remindToInt(l string) float64 {
	switch l {
	case "strong":
		return 3
	case "observe":
		return 2
	case "mute":
		return 1
	}
	return 0
}

// boolToFloat 布尔转 float64（true→1, false→0），用于 Details map。
func boolToFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// NPhaseString 将 NPhase 枚举转为可读字符串。
func NPhaseString(p NPhase) string {
	switch p {
	case NPhaseIdle:
		return "idle"
	case NPhaseFirstBreakout:
		return "first_breakout"
	case NPhaseFlag:
		return "flag"
	case NPhaseSecondBreakout:
		return "second_breakout"
	case NPhaseCompleted:
		return "completed"
	case NPhaseFailed:
		return "failed"
	}
	return "unknown"
}
