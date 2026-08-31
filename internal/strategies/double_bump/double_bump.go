// Package double_bump 实现双凸战法（Double Bump Strategy）。
//
// 本包实现了双凸战法，这是一种中线趋势延续策略，核心思想是：
// 识别股价在日线级别走出"首次放量突破 → 缩量调整 → 二次放量突破"的双凸形态。
//
// 市场模式：
//   - 首次放量突破：主力资金第一阶段建仓
//   - 缩量调整：洗盘和筹码沉淀
//   - 二次放量突破：主力资金第二阶段建仓后的加速拉升
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
//   - 第二波方向闸门（当日实时涨跌幅）：
//     第二波必须是向上结构 —— 当日 ChangePct > MinChangePct(默认0) 才计"第二波放量/调整"分；
//     水下/平盘（跌水日）被降为仅均线分，最多 watch，避免全天绿票误报双凸买入。
//
// 信号阈值：
//   - total ≥ 70 → full_chain（完整链，买入），置信度>0.8 → P1，其余 P2
//   - total ≥ 50 → brief（半确认），P3_5 观察
//   - total < 50 → watch，不操作
//
// 不适用 30%/80% 仓位限制（按 N 形仓位特殊规则，仅 90% 截断）。
//
// （English: implements the Double Bump continuation strategy scoring volume / pullback depth / MA, gated by intraday up-session direction, with 70/50 thresholds→
// full_chain/brief/watch, and only a 90% position ceiling instead of 30%/80% caps.）
package double_bump

import (
	"log"
	"math"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/strategy"
)

// DoubleBumpStrategy 双凸战法策略结构。
// 通过量能/调整深度/均线三维度评分识别双凸突破机会。
// 这是一个中线趋势延续策略，专注于捕捉主力资金两阶段建仓后的加速拉升。
//
// （Double Bump strategy struct.）
type DoubleBumpStrategy struct {
	cfg    *config.Manager // 配置管理器（热加载 DoubleBumpConfig）（Config manager, hot-reloads DoubleBumpConfig）
	userID string          // 账号 ID：非空时按该账号的策略配置读取，否则回退全局（Account ID: per-account strategy config when set, else global）
}

// New 创建双凸战法策略实例。
// 初始化策略配置，返回可直接使用的策略实例。
//
// （New creates a Double Bump strategy instance.）
func New(cfg *config.Manager) *DoubleBumpStrategy {
	return &DoubleBumpStrategy{cfg: cfg}
}

// SetUserID 设置账号 ID。
// 在多账号独立引擎场景下，各账号 runner 按本账号配置读取策略参数。
// 这允许不同账号使用不同的策略配置。
//
// English: sets the account ID so a per-account engine's runner reads that account's config.
func (d *DoubleBumpStrategy) SetUserID(userID string) {
	d.userID = userID
}

// strategyCfg 返回当前账号的双凸配置。
// 优先使用账号级配置，若未设置则回退到全局配置。
// 这种设计支持多账号独立配置策略参数。
//
// English: returns the Double Bump config for the current account (account override wins, else global).
func (d *DoubleBumpStrategy) strategyCfg() config.DoubleBumpConfig {
	if d.userID != "" && d.cfg != nil {
		return d.cfg.GetStrategyConfigFor(d.userID).DoubleBump
	}
	if d.cfg != nil {
		return d.cfg.Get().Strategy.DoubleBump
	}
	return config.DoubleBumpConfig{}
}

// Name 返回策略中文名称"双凸战法"。
// 用于日志输出和前端展示。
//
// （Name returns the strategy display name "双凸战法".）
func (d *DoubleBumpStrategy) Name() string {
	return "双凸战法"
}

// Type 返回信号类型标识 SignalDoubleBump。
// 用于信号分类和去重。
//
// （Type returns the signal type SignalDoubleBump.）
func (d *DoubleBumpStrategy) Type() strategy.SignalType {
	return strategy.SignalDoubleBump
}

// Evaluate 标准接口（占位）。
// 实际使用 EvaluateReal 传入结构化数据，这个方法仅作为 Strategy 接口的占位实现。
// 返回空结果，表示无数据可评分。
//
// （Standard interface stub; real scoring uses EvaluateReal.）
func (d *DoubleBumpStrategy) Evaluate(code string, data interface{}) (*strategy.Evaluation, error) {
	return &strategy.Evaluation{Pass: false, Level: "nodata", Confidence: 0}, nil
}

// EvaluateReal 执行双凸战法核心评分。
// 这是双凸战法的核心评分函数，实现三维评分体系。
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
// 输入参数：
//   - code: 股票代码
//   - si: 个股信息（含价格/涨跌幅）
//   - kLines: 日K线列表（需 ≥10 根）
//
// 返回值：
//   - nil: 不构成双凸形态
//   - *Evaluation: 评分结果
//
// （EvaluateReal runs the core Double Bump scoring.）
func (d *DoubleBumpStrategy) EvaluateReal(code string, si *data.StockInfo, kLines []data.KLine) *strategy.Evaluation {
	// 基础校验：无实时价或 K 线不足 10 根无法评分（Guard: require a live price and ≥10 bars to score）
	if si == nil || si.Price <= 0 || len(kLines) < 10 {
		return nil
	}
	// 今日实时走弱（低开低走）时，双凸第二波确认被打破，不构成做多信号：
	// 日K最后一根可能是昨日收盘，需用实时涨跌幅抑制当日下跌时的误报。（If today is already weak (≤-1.5%),
	// the second wave confirm is broken — return watch to suppress false signals on falling days.）
	if si.ChangePct <= -1.5 {
		return &strategy.Evaluation{
			TotalScore: 0,
			Pass:       false,
			Level:      "watch",
			Confidence: 0,
		}
	}
	// 读取热加载的双凸配置（放量倍数、权重、调整阈值等）（Load the hot-reloaded Double Bump config）
	dbc := d.strategyCfg()
	// 第二波当日方向闸门：双凸的"第二波"必须是向上结构，
	// 水下/平盘（ChangePct<=MinChangePct，默认0）不能充当"放量上攻波"。
	// 此时量能分/调整分强制为 0，只剩均线分（≤MAWeight×100，远低于 full_chain 阈值）。（Direction gate: the second
	// wave must be an up session; otherwise volume/adjust scores are forced to 0 leaving only the MA score.）
	upSession := si.ChangePct > dbc.MinChangePct

	// 计算回看期内均量和均价（去掉最后一根，作为"第一波"的对比基准）（Compute average volume/close in the lookback window, excluding the last bar）
	avgVol := 0.0
	avgClose := 0.0
	n := len(kLines)
	// 回看窗口最多 20 根且剔除最后一根（避免用当日数据污染基准）（Cap lookback at 20 and drop the last bar to avoid polluting the baseline）
	lookback := int(math.Min(float64(n-1), 20))
	for i := n - lookback - 1; i < n-1; i++ {
		avgVol += kLines[i].Volume
		avgClose += kLines[i].Close
	}
	avgVol /= float64(lookback)
	avgClose /= float64(lookback)

	// 检测第一波放量突破（近5日内）：放量上破 5% 即视为第一波启动（Detect the first breakout within the last 5 bars: close +5% on above-average volume）
	firstBreak := false
	firstBreakIdx := -1
	firstBreakVol := 0.0
	for i := n - 5; i < n; i++ {
		if i < 0 || i >= n {
			continue
		}
		if kLines[i].Close > avgClose*1.05 && kLines[i].Volume > avgVol*dbc.FirstBreakVolumeMultiple {
			firstBreak = true
			firstBreakIdx = i
			firstBreakVol = kLines[i].Volume
			break
		}
	}

	// 无第一波突破 → 不构成双凸（No first wave → not a Double Bump pattern）
	if !firstBreak {
		return nil
	}

	// 第二波确认（5日窗口）：在第一波之后的近5日内，任一再放量上行（收盘>开盘 且 量>均量×SecondBreakVolumeMultiple）
	// 即视为第二波确认——不苛求"最后一根"正好放量，兼容盘中未放量但近5日已完成两波放量的形态。
	// 但仅当日上行时才计量能分（水下放量=出货/放量下跌）（Second-wave confirm within the 5-day window: any up
	// bar AFTER the first breakout with volume above avg×SecondBreakVolumeMultiple counts as the second wave.
	// The last bar needn't be the spike itself, so patterns with two completed volume waves in the window still qualify;
	// volume score still requires today's session to be up (underwater volume = distribution).）
	volScore := 0.0
	secondBreak := false
	if upSession && firstBreakVol > 0 {
		for i := firstBreakIdx + 1; i < n; i++ {
			if i >= n {
				break
			}
			if kLines[i].Volume > avgVol*dbc.SecondBreakVolumeMultiple {
				volScore = dbc.VolumeWeight * 100
				secondBreak = true
				break
			}
		}
	}

	// 双凸形态硬闸：必须有第二波放量确认（volScore>0）才构成双凸。
	// 若最后一根未放量（第二波缺失），即使均线多头+窄幅也仅是第一波后的普通回调，
	// 不构成双凸信号——否则会像"卧龙电驱(vol=0,total=54)"那样把非双凸当双凸误报。
	// English: hard gate — a Double Bump REQUIRES the second-wave volume spike (volScore>0).
	// Without it, MA-alignment + narrow amplitude is just a routine pullback after the first wave,
	// not a Double Bump. Without this gate non-pattern stocks (e.g. vol=0,total=54) get mislabeled.
	if !secondBreak {
		return nil
	}

	// 调整深度评分：振幅 / 均价 < AdjustVolRatioMax×2 说明调整温和
	// （调整幅度小意味着抛压可控、筹码锁定良好）；仅当日上行时才计调整分（水下窄幅不算确认）（Adjust-depth score: a narrow
	// amplitude (< AdjustVolRatioMax×2) means gentle consolidation, counted only on up sessions.）
	high := kLines[n-1].High
	low := kLines[n-1].Low
	adjustDepth := (high - low) / avgClose * 100
	adjustScore := 0.0
	if upSession && adjustDepth < dbc.AdjustVolRatioMax*2 {
		adjustScore = dbc.PositionWeight * 100
	}

	// 均线趋势：MA5 > MA10 为多头排列
	// 多头排列给 80%，收盘再站稳 MA5 才给满 100%（MA trend: bullish alignment MA5>MA10 gives 80%, +100% when close holds above MA5）
	maScore := 0.0
	ma5 := movingAvg(kLines, n, 5)
	ma10 := movingAvg(kLines, n, 10)
	if ma5 > ma10 {
		maScore = dbc.MAWeight * 100 * 0.8
		if kLines[n-1].Close > ma5 {
			maScore = dbc.MAWeight * 100
		}
	}

	log.Printf("[double_bump] %s 今日价=%.2f chg=%.2f%% K线n=%d 最后一根日期=%v 量=%.0f 20日均量=%.0f 二突?volScore=%.0f adjustScore=%.0f maScore=%.0f total=%.0f upSession=%v",
		code, si.Price, si.ChangePct, n, kLines[n-1].Date, kLines[n-1].Volume, avgVol, volScore, adjustScore, maScore, math.Min(volScore+adjustScore+maScore, 100), upSession)

	// 总分封顶 100；≥60 → full_chain（买入，放宽层级统一到60），≥50 → brief（观察）
	// （Cap total at 100; ≥60 full_chain (buy, relaxed to 60), ≥50 brief (watch)）
	total := math.Min(volScore+adjustScore+maScore, 100)
	pass := total >= 50
	level := "watch"
	if total >= 60 {
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
// 用于计算MA5、MA10等均线指标。
//
// （movingAvg computes a moving average of closes.）
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
// 辅助函数，用于计算回看窗口大小。
//
// （min returns the smaller of two ints.）
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// GenerateSignal 将评分结果转化为交易信号。
// 根据评估结果的Level字段决定交易动作和优先级：
//   - full_chain: 买入信号，按置信度分P1/P2
//   - brief: 观察信号，P3_5优先级
//   - 其他: 默认观察
//
// 信号生成后，会将评分明细复制到Meta字段，供前端展示各维度分数。
//
// （GenerateSignal converts an evaluation into a trade signal.）
func (d *DoubleBumpStrategy) GenerateSignal(code string, eval *strategy.Evaluation) (*strategy.Signal, error) {
	// 默认：仅观察（watch / P3）（Default: watch only with P3）
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

	// 复制评分明细到 Meta（供前端展示各维度分数）（Copy score details into Meta for the frontend）
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
// 用于识别个股当前所处的双凸形态阶段。
//
// （BumpPhase is the Double Bump formation state machine.）
type BumpPhase int

const (
	PhaseFirst  BumpPhase = 1  // 第一波突破（首次放量拉升）（First breakout: first scaling rally）
	PhaseAdjust BumpPhase = 2  // 调整阶段（缩量回调，等待第二波）（Adjust: volume-shrunk pullback awaiting second wave）
	PhaseSecond BumpPhase = 3  // 第二波突破（再次放量拉升，确认双凸）（Second breakout: scaling rally confirming the double bump）
	PhaseThird  BumpPhase = 4  // 第三波延伸（强势延续，可能出现第三波）（Third wave extension: strong continuation）
	PhaseIDF    BumpPhase = -1 // 形态失效（放量滞涨或破位）（Formation invalid: high volume stalling or breakdown）
)

// DetectPhase 检测个股当前所处的双凸形态阶段。
// 使用最新K线判断当前处于哪个阶段：
//   - PhaseFirst: 第一波突破（刚启动）
//   - PhaseAdjust: 缩量调整（蓄势待发）
//   - PhaseSecond: 第二波突破（确认双凸）
//   - PhaseIDF: 形态失效（放量滞涨或破位）
//
// 需要至少 20 根 K 线来计算均线和量能。
//
// （DetectPhase identifies the current Double Bump phase.）
func (d *DoubleBumpStrategy) DetectPhase(code string, kLines []data.KLine) BumpPhase {
	// K 线不足 20 根时无法计算均量/均线，保守判定为第一波阶段（With <20 bars, conservatively assume first wave）
	if len(kLines) < 20 {
		return PhaseFirst
	}
	dbc := d.strategyCfg()
	n := len(kLines)

	// 计算 20 日均量和均价（不含当日）（Compute 20-day average volume/close, excluding today）
	avgVol := 0.0
	avgClose := 0.0
	for i := n - 20; i < n-1; i++ {
		avgVol += kLines[i].Volume
		avgClose += kLines[i].Close
	}
	avgVol /= 20
	avgClose /= 20

	// 当日量价特征（用于阶段判定）（Today's volume/price features for phase detection）
	lastVol := kLines[n-1].Volume
	lastClose := kLines[n-1].Close
	// 放量突破：收盘上破 5% 且成交量超第一波阈值（Breakout: close +5% on volume above the first-break multiple）
	isBreakout := lastClose > avgClose*1.05 && lastVol > avgVol*dbc.FirstBreakVolumeMultiple

	// 缩量调整：成交量低于均量 70%（第二波前的蓄势）（Shrink: volume below 70% of average, gearing up for the second wave）
	isShrink := lastVol < avgVol*0.7

	// 形态失效：放量滞涨（量超 1.5 倍但收跌）（Invalidation: volume >1.5× but closing down）
	isIDF := lastVol > avgVol*1.5 && lastClose < kLines[n-2].Close

	// 优先判定失效（破坏性事件优先于形态推进）（Check invalidation first — destruction beats progression）
	if isIDF {
		return PhaseIDF
	}
	if isBreakout {
		// 近 10 日内出现过放量突破 → 本次是第二波；否则是刚启动的第一波（A breakout within the last 10 bars → second wave, else first wave）
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
// 在双凸形态失效后，捕捉可能的修复反弹买点。
//
// 判断逻辑：
//  1. 最近两根K线收涨（连续两日阳线确认反转力度）
//  2. 最近两根K线成交量均放大（大于20日均量×1.2，资金回流）
//  3. 近期（近5日）曾出现单日跌幅>3%的深跌（存在超跌修复空间）
//
// 三个条件同时满足时返回true，表示可能出现反转。
//
// （CheckIDFReturn detects a reversal signal after invalidation.）
func (d *DoubleBumpStrategy) CheckIDFReturn(code string, kLines []data.KLine) bool {
	if len(kLines) < 5 {
		return false
	}
	n := len(kLines)

	// 条件1：最近两根 K 线均收涨（连续两日阳线确认反转力度）（Condition 1: both last bars close up, confirming reversal force）
	if kLines[n-1].Close <= kLines[n-2].Close || kLines[n-2].Close <= kLines[n-3].Close {
		return false
	}

	// 条件2：最近两根 K 线成交量均放大（大于 20 日均量×1.2，资金回流）（Condition 2: both bars expand volume (>1.2× the 20-day avg), capital returning）
	avgVol := 0.0
	for i := n - 20; i < n-2; i++ {
		if i < 0 {
			continue
		}
		avgVol += kLines[i].Volume
	}
	// 回看均值分母：最多 18 根（去掉最近两根被比较的K线），不足则按实际可用数（Denominator caps at 18 bars excluding the two compared bars）
	avgVol /= float64(min(18, n-2))

	if kLines[n-1].Volume < avgVol*1.2 || kLines[n-2].Volume < avgVol*1.2 {
		return false
	}

	// 条件3：近期（近 5 日）曾出现单日跌幅 >3% 的深跌（存在超跌修复空间）（Condition 3: a >3% single-day drop in the last 5 days left oversold room）
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
