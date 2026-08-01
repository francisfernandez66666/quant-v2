// Package double_bump 实现双凸战法（Double Bump Strategy）。
//
// 市场模式：识别股价在日线级别走出"首次放量突破 → 缩量调整 → 二次放量突破"的双凸形态。
// 这是典型的中线趋势延续形态，常见于主力资金两阶段建仓后的加速拉升。
//
// 三维评分体系：
//
//   - Vol 放量维度（权重来自配置）：
//     第一波突破量 > 20日均量 × FirstBreakVolumeMultiple 确认初涨；
//     第二波量 > 20日均量 × SecondBreakVolumeMultiple 确认延续。
//     双波放量说明资金持续介入，趋势健康。
//
//   - Adjust 调整维度（权重来自配置）：
//     调整深度 = (当日最高-最低) / 20日均价 × 100。
//     深度 < AdjustVolRatioMax×2 视为温和调整，反映抛压可控，筹码锁定良好。
//
//   - MA 均线维度（权重来自配置）：
//     MA5 > MA10 判定多头排列；收盘价 > MA5 确认短期趋势强势。
//     均线多头排列是趋势延续的基础条件。
//
// 信号阈值：
//   - total ≥ 70 → full_chain（完整链，买入），置信度>0.8 → P1，其余 P2
//   - total ≥ 50 → brief（半确认），P3_5 观察
//   - total < 50 → watch，不操作
//
// 不适用 30%/80% 仓位限制（按 N 形仓位特殊规则，仅 90% 截断）。
package double_bump

import (
	"math"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/strategy"
)

// DoubleBumpStrategy 双凸战法策略结构。
// 通过量能/调整深度/均线三维度评分识别双凸突破机会。
type DoubleBumpStrategy struct {
	cfg *config.Manager // 配置管理器（热加载 DoubleBumpConfig）
}

// New 创建双凸战法策略实例。
func New(cfg *config.Manager) *DoubleBumpStrategy {
	return &DoubleBumpStrategy{cfg: cfg}
}

// Name 返回策略中文名称"双凸战法"。
func (d *DoubleBumpStrategy) Name() string {
	return "双凸战法"
}

// Type 返回信号类型标识 SignalDoubleBump。
func (d *DoubleBumpStrategy) Type() strategy.SignalType {
	return strategy.SignalDoubleBump
}

// Evaluate 标准接口（占位）。实际使用 EvaluateReal 传入结构化数据。
func (d *DoubleBumpStrategy) Evaluate(code string, data interface{}) (*strategy.Evaluation, error) {
	return &strategy.Evaluation{Pass: false, Level: "nodata", Confidence: 0}, nil
}

// EvaluateReal 执行双凸战法核心评分。
//
// 评分步骤：
//  1. 计算 20 日均量和均价（lookback ≤ 可用K线数）
//  2. 检测近 5 日内是否存在放量突破（Close > avgClose×1.05 且 Vol > avgVol×FirstBreakVolumeMultiple）
//  3. 若无第一波突破则返回 nil（不构成双凸的前提缺失）
//  4. Vol 评分：最后一根K线量 > avgVol×SecondBreakVolumeMultiple 给满分
//  5. Adjust 评分：当日振幅 / 均价 < AdjustVolRatioMax×2 给满分（调整温和）
//  6. MA 评分：MA5 > MA10 多头排列 + 收盘 > MA5 确认强势
//  7. 总分 = volScore + adjustScore + maScore，上限 100
//
// 输入: si（个股信息）、kLines（日K线列表，需 ≥10 根）
func (d *DoubleBumpStrategy) EvaluateReal(code string, si *data.StockInfo, kLines []data.KLine) *strategy.Evaluation {
	// 基础校验：无实时价或 K 线不足 10 根无法评分
	if si == nil || si.Price <= 0 || len(kLines) < 10 {
		return nil
	}
	cfg := d.cfg.Get()
	dbc := cfg.Strategy.DoubleBump

	// 计算回看期内均量和均价（去掉最后一根，作为"第一波"的对比基准）
	avgVol := 0.0
	avgClose := 0.0
	n := len(kLines)
	lookback := int(math.Min(float64(n-1), 20))
	for i := n - lookback - 1; i < n-1; i++ {
		avgVol += kLines[i].Volume
		avgClose += kLines[i].Close
	}
	avgVol /= float64(lookback)
	avgClose /= float64(lookback)

	// 检测第一波放量突破（近5日内）：放量上破 5% 即视为第一波启动
	firstBreak := false
	firstBreakVol := 0.0
	for i := n - 5; i < n; i++ {
		if i < 0 || i >= n {
			continue
		}
		if kLines[i].Close > avgClose*1.05 && kLines[i].Volume > avgVol*dbc.FirstBreakVolumeMultiple {
			firstBreak = true
			firstBreakVol = kLines[i].Volume
			break
		}
	}

	// 无第一波突破 → 不构成双凸
	if !firstBreak {
		return nil
	}

	// 第二波确认：最后一根K线量能 > 均量 × SecondBreakVolumeMultiple
	// 两波放量说明资金持续介入，趋势健康
	lastVol := kLines[n-1].Volume
	volScore := 0.0
	if firstBreakVol > 0 && lastVol > avgVol*dbc.SecondBreakVolumeMultiple {
		volScore = dbc.VolumeWeight * 100
	}

	// 调整深度评分：振幅 / 均价 < AdjustVolRatioMax×2 说明调整温和
	// （调整幅度小意味着抛压可控、筹码锁定良好）
	high := kLines[n-1].High
	low := kLines[n-1].Low
	adjustDepth := (high - low) / avgClose * 100
	adjustScore := 0.0
	if adjustDepth < dbc.AdjustVolRatioMax*2 {
		adjustScore = dbc.PositionWeight * 100
	}

	// 均线趋势：MA5 > MA10 为多头排列
	// 多头排列给 80%，收盘再站稳 MA5 才给满 100%
	maScore := 0.0
	ma5 := movingAvg(kLines, n, 5)
	ma10 := movingAvg(kLines, n, 10)
	if ma5 > ma10 {
		maScore = dbc.MAWeight * 100 * 0.8
		if kLines[n-1].Close > ma5 {
			maScore = dbc.MAWeight * 100
		}
	}

	// 总分封顶 100；≥70 → full_chain（买入），≥50 → brief（观察）
	total := math.Min(volScore+adjustScore+maScore, 100)
	pass := total >= 50
	level := "watch"
	if total >= 70 {
		level = "full_chain"
		pass = true
	} else if total >= 50 {
		level = "brief"
	}

	return &strategy.Evaluation{
		TotalScore: total,
		Details: map[string]float64{
			"vol_score":    volScore,
			"adjust_score": adjustScore,
			"ma_score":     maScore,
			"adjust_depth": adjustDepth,
		},
		Pass:       pass,
		Level:      level,
		Confidence: total / 100.0,
	}
}

// movingAvg 计算移动平均线。
// 从 K 线列表末尾向前取 period 根收盘价计算均值。
func movingAvg(kLines []data.KLine, n, period int) float64 {
	if n < period {
		return kLines[n-1].Close
	}
	sum := 0.0
	for i := n - period; i < n; i++ {
		sum += kLines[i].Close
	}
	return sum / float64(period)
}

// min 返回两个整数中的较小值。
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// GenerateSignal 将评分结果转化为交易信号。
// full_chain → buy，置信度>0.8 → P1，否则 P2。
// brief → watch，P3_5。
func (d *DoubleBumpStrategy) GenerateSignal(code string, eval *strategy.Evaluation) (*strategy.Signal, error) {
	prio := strategy.P3
	action := strategy.ActionWatch

	switch eval.Level {
	case "full_chain":
		action = strategy.ActionBuy
		if eval.Confidence > 0.8 {
			prio = strategy.P1
		} else {
			prio = strategy.P2
		}
	case "brief":
		action = strategy.ActionWatch
		prio = strategy.P3_5
	}

	meta := make(map[string]float64)
	for k, v := range eval.Details {
		meta[k] = v
	}

	return &strategy.Signal{
		Type:       strategy.SignalDoubleBump,
		Action:     action,
		Priority:   prio,
		Confidence: eval.Confidence,
		Reason:     eval.Level,
		Meta:       meta,
	}, nil
}

// BumpPhase 双凸形态状态机阶段。
// 跟踪从 first → adjust → second → third 的完整周期。
// PhaseIDF 表示形态失效（形态破坏）。
type BumpPhase int

const (
	PhaseFirst  BumpPhase = 1  // 第一波突破（首次放量拉升）
	PhaseAdjust BumpPhase = 2  // 调整阶段（缩量回调，等待第二波）
	PhaseSecond BumpPhase = 3  // 第二波突破（再次放量拉升，确认双凸）
	PhaseThird  BumpPhase = 4  // 第三波延伸（强势延续，可能出现第三波）
	PhaseIDF    BumpPhase = -1 // 形态失效（放量滞涨或破位）
)

// DetectPhase 检测个股当前所处的双凸形态阶段。
// 使用最新K线判断：第一波突破 → 第二波突破 → 失效反转。
// 需要至少 20 根 K 线来计算均线和量能。
func (d *DoubleBumpStrategy) DetectPhase(code string, kLines []data.KLine) BumpPhase {
	// K 线不足 20 根时无法计算均量/均线，保守判定为第一波阶段
	if len(kLines) < 20 {
		return PhaseFirst
	}
	cfg := d.cfg.Get()
	dbc := cfg.Strategy.DoubleBump
	n := len(kLines)

	// 计算 20 日均量和均价（不含当日）
	avgVol := 0.0
	avgClose := 0.0
	for i := n - 20; i < n-1; i++ {
		avgVol += kLines[i].Volume
		avgClose += kLines[i].Close
	}
	avgVol /= 20
	avgClose /= 20

	// 当日量价特征（用于阶段判定）
	lastVol := kLines[n-1].Volume
	lastClose := kLines[n-1].Close
	// 放量突破：收盘上破 5% 且成交量超第一波阈值
	isBreakout := lastClose > avgClose*1.05 && lastVol > avgVol*dbc.FirstBreakVolumeMultiple

	// 缩量调整：成交量低于均量 70%（第二波前的蓄势）
	isShrink := lastVol < avgVol*0.7

	// 形态失效：放量滞涨（量超 1.5 倍但收跌）
	isIDF := lastVol > avgVol*1.5 && lastClose < kLines[n-2].Close

	// 优先判定失效（破坏性事件优先于形态推进）
	if isIDF {
		return PhaseIDF
	}
	if isBreakout {
		// 近 10 日内出现过放量突破 → 本次是第二波；否则是刚启动的第一波
		for i := n - 10; i < n-1; i++ {
			if i < 0 {
				continue
			}
			if kLines[i].Close > avgClose*1.05 && kLines[i].Volume > avgVol*dbc.FirstBreakVolumeMultiple {
				return PhaseSecond
			}
		}
		return PhaseFirst
	}
	if isShrink {
		return PhaseAdjust
	}
	return PhaseFirst
}

// CheckIDFReturn 检查是否出现形态失效后的反转信号。
// 判断逻辑：最近两根K线收涨且成交量放大，之前有超过 3% 的下跌。
// 用于在 PhaseIDF 失效后捕捉可能的修复反弹买点。
func (d *DoubleBumpStrategy) CheckIDFReturn(code string, kLines []data.KLine) bool {
	if len(kLines) < 5 {
		return false
	}
	n := len(kLines)

	// 条件1：最近两根 K 线均收涨（连续两日阳线确认反转力度）
	if kLines[n-1].Close <= kLines[n-2].Close || kLines[n-2].Close <= kLines[n-3].Close {
		return false
	}

	// 条件2：最近两根 K 线成交量均放大（大于 20 日均量×1.2，资金回流）
	avgVol := 0.0
	for i := n - 20; i < n-2; i++ {
		if i < 0 {
			continue
		}
		avgVol += kLines[i].Volume
	}
	avgVol /= float64(min(18, n-2))

	if kLines[n-1].Volume < avgVol*1.2 || kLines[n-2].Volume < avgVol*1.2 {
		return false
	}

	// 条件3：近期（近 5 日）曾出现单日跌幅 >3% 的深跌（存在超跌修复空间）
	for i := n - 5; i < n-2; i++ {
		if i < 1 {
			continue
		}
		dropPct := (kLines[i-1].Close - kLines[i].Close) / kLines[i-1].Close * 100
		if dropPct > 3 {
			return true
		}
	}

	return false
}
