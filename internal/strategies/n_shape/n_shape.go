// Package n_shape 实现 N 形超短策略。
//
// 本包实现了N形超短策略，这是一种日内动量突破策略，核心思想是：
// 识别股价在日内走出"第一波拉升→小幅回调(旗形整理)→第二波拉升"的 N 形结构。
//
// 市场模式：
//   - 第一波拉升：快速上涨，伴随放量
//   - 旗形整理：小幅回调，成交量萎缩
//   - 第二波拉升：再次放量突破前高
//
// 四维评分体系（D1~D4）：
//   - D1 事件硬闸（满分 40）：事件驱动匹配，事件正向且强度达标才可入场
//   - D2 相对强度 RS（满分 30）：集合竞价涨幅 + 量比 + 个股相对大盘超额收益
//   - D3 超跌确认（满分 20）：PE 估值超跌或斐波那契回调深度在 0.382~0.618 之间
//   - D4 资金确认（满分 10）：MACD 水上金叉 + 当日量能放大至 20 日均量 1.5 倍以上
//
// 信号生成阈值：
//   - full_chain（完整链）：D1>0 且总分≥60 → 买入
//   - 置信度 ≥0.8 → P1；≥0.6 → P2；其余 P3
//   - 左侧一突信号（价格突破前高×1.005 且量比≥1.8）提升优先级至至少 P2
//
// 前置依赖：需提供 WaveA（昨日波形）、IntradayB（日内快照）、Ctx（板块&情绪上下文）。
//
// （English: Implements the intraday "N-shape" ultra-short-term momentum breakout strategy. Scores four dimensions
// D1 event-gate (40) / D2 relative strength (30) / D3 oversold-pullback (20) / D4 fund confirmation (10); a signal is
// valid when D1>0 and total ≥60, with priority boosted by time and by a left-side breakout.）
package n_shape

import (
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/strategy"
)

// NShapeStrategy N 形超短策略主结构。
// 持有配置管理器和左侧评分器，通过 EvaluateWave 执行实时评分。
// 这是一个日内超短线策略，专注于捕捉动量突破机会。
//
// （NShapeStrategy is the N-shape strategy main struct.）
type NShapeStrategy struct {
	cfg    *config.Manager // 策略配置热加载（Strategy config hot reload）
	scorer *LeftSideScorer // D1~D4 评分器（D1~D4 scorer）
}

// New 创建 N 形策略实例。
// 初始化策略配置和事件匹配器，返回可直接使用的策略实例。
// matcher 用于事件匹配（D1 评分依赖）。
//
// （New creates an N-shape instance; matcher feeds D1 event matching.）
func New(cfg *config.Manager, matcher *data.EventMatcher) *NShapeStrategy {
	return &NShapeStrategy{
		cfg:    cfg,
		scorer: NewLeftSideScorer(matcher),
	}
}

// Name 返回策略中文名称"N形"（§名称规整：与配置白名单/模拟盘池名统一，旧名"N形超短"作别名兼容）。
// 用于日志输出和前端展示。
//
// （Name returns the strategy display name "N形"; the legacy "N形超短" is accepted as an alias.）
func (s *NShapeStrategy) Name() string {
	return "N形"
}

// Type 返回信号类型标识 SignalNShape。
// 用于信号分类和去重。
//
// （Type returns the signal type SignalNShape.）
func (n *NShapeStrategy) Type() strategy.SignalType {
	return strategy.SignalNShape
}

// Evaluate 标准接口（占位）。
// 实际使用 EvaluateWave 传入结构化数据，这个方法仅作为 Strategy 接口的占位实现。
// 返回空结果，表示无数据可评分。
//
// （Standard interface stub; real scoring uses EvaluateWave.）
func (n *NShapeStrategy) Evaluate(code string, data interface{}) (*strategy.Evaluation, error) {
	return &strategy.Evaluation{Pass: false, Level: "nodata", Confidence: 0}, nil
}

// EvaluateWave 执行 N 形策略核心评分。
// 这是N形策略的核心评分函数，调用左侧评分器执行D1~D4四维评分。
//
// 输入参数：
//   - wa: 昨日波形（A波）数据
//   - ib: 日内快照（B段）数据，含竞价/量能/MACD
//   - ctx: 板块情绪&事件上下文
//
// 返回值：
//   - *Evaluation: 评分结果，包含 D1~D4 各维度分数、总分、是否 full_chain
//   - error: 错误信息（当前实现不会返回错误）
//
// 评分链路: scorer.Evaluate → D1/D2/D3/D4 → Total → Valid 判断。
//
// （Inputs: wa yesterday's wave, ib intraday snapshot, ctx sector/emotion/event context. Pipeline: scorer.Evaluate→D1..D4→Total→Valid.）
func (n *NShapeStrategy) EvaluateWave(wa *WaveA, ib *IntradayB, ctx *Ctx) (*strategy.Evaluation, error) {
	sr := n.scorer.Evaluate(wa, ib, ctx)
	// 评分器返回 nil 表示数据不足或情绪硬闸（"衰退"）拦截（Nil from scorer → insufficient data or emotion hard-block "衰退"）
	if sr == nil {
		return &strategy.Evaluation{Pass: false, Level: "noscore"}, nil
	}

	// 仅当 Valid（D1>0 且 总分≥60）时才标记为 full_chain（Mark full_chain only when Valid (D1>0 and total ≥60)）
	level := "fail"
	if sr.Valid {
		level = "full_chain"
	}

	// 组装 D1~D4 分值与信号标志（供前端各维度展示）（Assemble D1~D4 scores and signal flags for the frontend）
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
// 根据评估结果的Level字段决定交易动作和优先级：
//   - full_chain: 买入信号，按置信度分P1/P2/P3
//   - left_signal: 左侧一突信号，至少P2
//   - right_signal: 右侧二突信号，P1
//
// 若左侧一突信号触发则最低 P2。
//
// （GenerateSignal converts an evaluation into a trade signal.）
func (n *NShapeStrategy) GenerateSignal(code string, eval *strategy.Evaluation) (*strategy.Signal, error) {
	// 默认动作：watch / P3；仅 full_chain 才升级为买入（Default: watch / P3; only full_chain escalates to buy）
	action := strategy.ActionWatch
	prio := strategy.P3

	switch eval.Level {
	case "full_chain":
		// 完整链确认：按置信度分档 高→P1(≥0.8)、中→P2(≥0.6)、低→P3（Full-chain: confidence tiers P1(≥0.8)/P2(≥0.6)/P3）
		action = strategy.ActionBuy
		if eval.Confidence >= 0.8 {
			prio = strategy.P1
		} else if eval.Confidence >= 0.6 {
			prio = strategy.P2
		} else {
			prio = strategy.P3
		}
	case "left_signal":
		// 左侧一突（价格破前高+量比≥1.8，且 D1>0 非情绪硬闸）→ 立即打标买入，至少 P2（Left breakout (price>prev-high, vol ratio≥1.8) → immediate buy, at least P2）
		action = strategy.ActionBuy
		prio = strategy.P2
	case "right_signal":
		// 右侧二突（一突破位→回调→二次放量重破前高）→ 最强确认，P1（Right second breakout → strongest confirm, P1）
		action = strategy.ActionBuy
		prio = strategy.P1
	}

	// 一突信号提高优先级: 价格突破前高且量比≥1.8 且 D1>0 时，最低提升至 P2
	// （说明主力已开始攻击，即使置信度不高也应提高关注级别；无 D1 事件分不提升，
	// 避免"无特定事件"占位低分仍被当一突拔高优先级）
	// English: left signal boosts priority to at least P2 only when a valid D1 event score exists.
	if d1v, d1ok := eval.Details["d1"]; d1ok && d1v > 0 {
		if d, ok := eval.Details["left_signal"]; ok && d > 0 {
			if prio > strategy.P2 {
				prio = strategy.P2
			}
		}
	}

	// 将评分明细复制进 Meta（前端展示各维度分数）（Copy score details into Meta for the frontend）
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
// 用于识别个股当前所处的N形形态阶段。
//
// （NPhase is the intraday N-shape state machine.）
type NPhase int

const (
	NPhaseIdle           NPhase = 0 // 空闲，未检测到首次突破（Idle: no first breakout yet）
	NPhaseFirstBreakout  NPhase = 1 // 第一波突破：价格快速拉高，伴随放量（First breakout: fast rally on volume）
	NPhaseFlag           NPhase = 2 // 旗形整理：小幅回调，成交量萎缩（Flag: shallow pullback, shrinking volume）
	NPhaseSecondBreakout NPhase = 3 // 第二波突破：再次放量拉升突破前高（Second breakout: scale up past the prior high）
	NPhaseCompleted      NPhase = 4 // N 形完成：二突破确认后完成形态（Completed: second breakout confirms the N-shape）
	NPhaseFailed         NPhase = 5 // 形态失败：回调过深或二突破未出现（Failed: pullback too deep or no second breakout）
)

// remindToInt 将提醒级别文本转为数值（用于前端展示）。
// strong=3, observe=2, mute=1, 其他=0。
// 用于将文本标签转换为前端可展示的数值。
//
// （remindToInt maps a remind label to a numeric for frontend display.）
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
// 便于在评分明细中记录布尔类型的状态标志。
//
// （boolToFloat converts a bool to 1/0 for Details maps.）
func boolToFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// NPhaseString 将 NPhase 枚举转为可读字符串。
// 用于日志输出和前端展示，将数字枚举转换为可读的阶段名称。
//
// （NPhaseString converts an NPhase to a human-readable string.）
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
