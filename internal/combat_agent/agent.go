// Package combat_agent 战法引擎：多策略信号执行与持仓监控。
// 支持多方向（做多/做空）扫描、Laodeng 评分修正、止盈止损提醒。
// 核心入口包括 ScanLong/ScanShort/Scan 三大扫描路径、ScorePool 持续打分、
// CheckPositionAlerts 持仓止盈止损提醒，以及 HotReload 配置热更新。
// 配套文件：types.go(数据结构)、adapter.go(数据适配)、momentum.go/nshape_input.go(打分输入)、
// d1_scorer.go(D1 事件评分)、expectation_gap.go(预期差)、limit_up.go(涨停龙头) 、loader.go(配置热加载)。
package combat_agent

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/report"
	"quant-trading-v2/internal/sector_agent"
	"quant-trading-v2/internal/strategy"
	"quant-trading-v2/internal/strategy_engine"
)

// StrategyRunner 策略运行器，封装策略类型与策略实例。
// Type 标识该运行器对应的战法（如 SignalDragon 龙头战法），
// Strategy 是具体的策略实现，Scan 阶段按 Type 分发到真实评分逻辑。
type StrategyRunner struct {
	Type     strategy.SignalType // 策略信号类型（龙/双响炮/N形/龙回头）
	Strategy strategy.Strategy   // 策略接口实现
}

// orDefault 返回 a 非空时的值，否则回退到 b。
func orDefault(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// strategyLabel 战法类型 → 日志用简称。
func strategyLabel(t strategy.SignalType) string {
	switch t {
	case strategy.SignalDragon:
		return "龙"
	case strategy.SignalDoubleBump:
		return "双"
	case strategy.SignalNShape:
		return "N"
	case strategy.SignalDragonReturn:
		return "回"
	}
	return string(t)
}

// nShapeReason 为 N 形信号附加 D1 评分理由（LLM 分析的故事），使信号可读性更强。
// base 为战法自身原因（如 left_signal/full_chain），d1 非空时把其 Reason 拼在其后。
func nShapeReason(base string, d1 *D1Score) string {
	if d1 == nil || d1.Reason == "" {
		return base
	}
	if base == "" {
		return "D1: " + d1.Reason
	}
	return base + " | D1: " + d1.Reason
}

// nShapeTag 映射 N 形评分级别到信号标记（一突/二突），其余级别返回 ""。
func nShapeTag(eval *strategy.Evaluation) string {
	if eval == nil {
		return ""
	}
	switch eval.Level {
	case "left_signal":
		return "一突"
	case "right_signal":
		return "二突"
	}
	return ""
}

// Agent 战法引擎核心，管理多策略运行器与配置热更新。
// 所有字段通过 mu 读写锁保护，保证并发扫描/热更新安全。
type Agent struct {
	mu           sync.RWMutex           // 读写锁，保护并发访问
	strategyCfg  *config.StrategyConfig // 策略参数配置（含动量分权重等，可热更新）
	laodengCfg   *config.LaodengConfig  // Laodeng 评分配置（nil 表示未启用）
	runners      []StrategyRunner       // 多策略运行器列表（做多/通用扫描共用）
	shortRunner  StrategyRunner         // 做空策略运行器（预留）
	shortEnabled bool                   // 做空功能开关（关闭时 ScanShort 直接返回 nil）

	waves  *WaveTracker // N 形一突/二突日内状态机（跨 5s 周期）
	diagMu sync.Mutex   // 保护 nDiag 的并发读写
	nDiag  []NDiag      // 本轮 N 形候选诊断条目（engine 每轮 DrainNDiag 收口）
}

// New 创建战法引擎实例。
func New(cfg *config.StrategyConfig) *Agent {
	return &Agent{
		strategyCfg: cfg,
		runners:     make([]StrategyRunner, 0),
		waves:       NewWaveTracker(),
	}
}

// DrainNDiag 收口并清空本轮 N 形诊断条目（engine 每轮打分后调用并打印）。
func (a *Agent) DrainNDiag() []NDiag {
	a.diagMu.Lock()
	defer a.diagMu.Unlock()
	out := a.nDiag
	a.nDiag = nil
	return out
}

// recordNDiag 追加一条 N 形候选诊断（仅 N 形战法路径调用）。
func (a *Agent) recordNDiag(d NDiag) {
	a.diagMu.Lock()
	defer a.diagMu.Unlock()
	a.nDiag = append(a.nDiag, d)
}

// SetLaodengConfig 设置 Laodeng 评分配置（线程安全）。
func (a *Agent) SetLaodengConfig(cfg *config.LaodengConfig) {
	a.mu.Lock()
	a.laodengCfg = cfg
	a.mu.Unlock()
}

// SetRunners 设置策略运行器列表（线程安全）。
func (a *Agent) SetRunners(runners []StrategyRunner) {
	a.mu.Lock()
	a.runners = runners
	a.mu.Unlock()
}

// SetShortEnabled 设置做空开关（线程安全）。
func (a *Agent) SetShortEnabled(enabled bool) {
	a.mu.Lock()
	a.shortEnabled = enabled
	a.mu.Unlock()
}

// ShortEnabled 返回当前做空是否启用（线程安全）。
func (a *Agent) ShortEnabled() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.shortEnabled
}

// HotReload 热更新策略参数（线程安全）。
func (a *Agent) HotReload(newCfg *config.StrategyConfig) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.strategyCfg = newCfg
	log.Printf("[combat_agent] 策略参数已热更新")
}

// seqID 生成全局唯一信号 ID，格式：SIG + 纳秒时间戳。
func seqID() string {
	return fmt.Sprintf("SIG%d", time.Now().UnixNano())
}

// applyLaodeng 对信号应用 Laodeng 评分修正，按评分系数提高置信度（上限 1.0）。
// 逐信号乘以 (1+Laodeng 分)，使高分股置信度放大、低分股基本不变。
// 入参 signals 为待修正的原始信号列表，返回修正后的新列表（不可用时原样返回）。
func (a *Agent) applyLaodeng(signals []Signal) []Signal {
	a.mu.RLock()
	cfg := a.laodengCfg
	a.mu.RUnlock()

	// 未配置/未启用/无信号时直接透传，不做修正
	if cfg == nil || !cfg.Enabled || len(signals) == 0 {
		return signals
	}

	out := make([]Signal, len(signals))
	for i, s := range signals {
		// 模拟 laodeng 评分所需数据（实际可从 marketAPI 实时查）
		// 简化：使用默认值，后续可增强
		laodengScore := strategy.ScoreLaodeng(cfg, 600, 12, 2.5, "白酒")
		s.Confidence *= (1 + laodengScore)
		// 置信度封顶 1.0，避免溢出
		if s.Confidence > 1 {
			s.Confidence = 1
		}
		out[i] = s
	}
	return out
}

// ScanLong 执行做多扫描：7a 板块利好→验证后个股→8a；8a 个股利好→直入战法。
// 返回做多信号列表，若输入的板块/个股为空或无可运行策略则返回 nil。
// 入参 input 含已验证板块（Sectors）、个股直入列表（IndividualStocks）、行情与 D1/L1 过滤结果。
func (a *Agent) ScanLong(input ScanInput) []Signal {
	a.mu.RLock()
	runners := a.runners
	a.mu.RUnlock()

	// 无可运行策略，或板块/个股输入均为空 → 无扫描对象，直接返回
	if len(runners) == 0 || (len(input.Sectors) == 0 && len(input.IndividualStocks) == 0) {
		return nil
	}

	var raw []Signal
	now := time.Now()

	// 7a 板块利好 → 验证后的个股走 8a（同时记录持续打分）
	for _, sector := range input.Sectors {
		// 仅处理方向为"利好"的板块（利空走做空路径 ScanShort）
		if sector.Direction != "利好" {
			continue
		}
		for _, code := range sector.Stocks {
			// L1 过滤阻塞的个股跳过，不再进入战法评分
			if input.L1Blocked[code] {
				continue
			}
			raw = append(raw, a.evalAll(&input, runners, code, input.MarketData[code], &sector, "做多", sector.Name, now)...)
		}
	}

	// 8a 个股利好 → 直入战法（同时记录自选/持仓持续打分）
	// 个股直入场景无板块上下文，sector 传 nil 交给战法自行降级处理
	for _, code := range input.IndividualStocks {
		if input.L1Blocked[code] {
			continue
		}
		raw = append(raw, a.evalAll(&input, runners, code, input.MarketData[code], nil, "做多", "个股", now)...)
	}

	// 最后统一套 Laodeng 评分修正置信度
	signals := a.applyLaodeng(raw)
	log.Printf("[combat_agent] ScanLong: %d 板块 %d 个股 → %d 做多信号", len(input.Sectors), len(input.IndividualStocks), len(signals))
	return signals
}

// evalAll 对单只股票跑全部战法评分：无论 Pass 与否都记录原始分到 input.Scores
// （8a/8b 持续打分），只对通过的战法生成信号并返回。这是 8a/8b 打分与信号输出的统一入口。
// 入参 sector 为板块上下文（nil 表示个股直入），direction 为做多/做空，
// now 用于统一信号的生成时间。返回本次评分为该股生成的信号列表。
func (a *Agent) evalAll(input *ScanInput, runners []StrategyRunner, code string, md *strategy_engine.StockMarketData, sector *sector_agent.VerifiedSector, direction, sectorName string, now time.Time) []Signal {
	// 无可运行策略或行情数据缺失 → 无法评分
	if len(runners) == 0 || md == nil {
		return nil
	}
	// 惰性初始化打分输出表，避免 nil map 写入 panic
	if input.Scores == nil {
		input.Scores = make(map[string]StockScores)
	}
	sc := StockScores{Code: code, DataGaps: make(map[string]bool)}
	// 提取 N 形战法消费的 D1 评分 / 事件描述 / PE（仅一次，供各战法共享）
	var d1 *D1Score
	if ds, ok := input.D1Scores[code]; ok {
		d1 = &ds
	}
	eventDesc := strings.Join(newsTitlesOf(input.News, code), "；")
	// PE 由上层 Engine 预取填充（input.PE 为空表示该股无 PE，N 形 D3 走斐波那契兜底）
	var pe float64
	if input.PE != nil {
		pe = input.PE[code]
	}
	var sigs []Signal
	var unSig []string // 未出信号战法的原因（诊断：为何非龙头战法不出信号）

	// 战法评分并发化：同一只股票的各战法评分彼此独立（无共享可变状态），
	// 并发调用 evalFor 后按原 runner 顺序合并处理，保证信号顺序与波状态机确定性。
	type evalResult struct {
		eval *strategy.Evaluation
		err  error
	}
	n := len(runners)
	res := make([]evalResult, n)
	var wg sync.WaitGroup
	for i, runner := range runners {
		if runner.Strategy == nil {
			continue
		}
		wg.Add(1)
		go func(i int, r StrategyRunner) {
			defer wg.Done()
			ev, err := evalFor(r, code, md, sector, input.EmotionPhase, d1, eventDesc, pe)
			res[i] = evalResult{eval: ev, err: err}
		}(i, runner)
	}
	wg.Wait()

	for i, runner := range runners {
		// 策略实例为空则跳过该运行器
		if runner.Strategy == nil {
			continue
		}
		// 按战法类型分发到真实评分逻辑（adapter.go evalFor，并发结果按序取用）
		eval, err := res[i].eval, res[i].err
		// 评分失败或返回空结果 → 该战法视为 0 分，不产出信号；同时标记数据缺口
		if err != nil || eval == nil {
			markDataGap(&sc, runner.Type, md)
			if err != nil {
				unSig = append(unSig, strategyLabel(runner.Type)+":错误("+err.Error()+")")
			} else {
				unSig = append(unSig, strategyLabel(runner.Type)+":无结果")
			}
			continue
		}
		// 战法各自数据不满足硬性门槛时（如 K 线不足被降级为 0）标记缺口
		if eval.TotalScore == 0 && strategyDataInsufficient(runner.Type, md) {
			markDataGap(&sc, runner.Type, md)
		}
		// 按战法类型归档原始总分到 StockScores（前端展示用，即使未通过也记录）
		switch runner.Type {
		case strategy.SignalNShape:
			sc.NScore = eval.TotalScore
		case strategy.SignalDragon:
			sc.DragonScore = eval.TotalScore
		case strategy.SignalDoubleBump:
			sc.DoubleBumpScore = eval.TotalScore
		case strategy.SignalDragonReturn:
			sc.DragonReturnScore = eval.TotalScore
		}
		// N 形候选：推进一突/二突日内状态机，并尊重 D 硬闸（硬闸在于 noscore 被拦、d1=0 不提级）。
		// 一突打标需 d1>0；二突为最强确认同样要求 d1>0。未满足硬闸保持原级别不发信号。
		if runner.Type == strategy.SignalNShape && eval != nil {
			left, right := a.waves.Eval(code, md)
			d1 := 0.0
			if v, ok := eval.Details["d1"]; ok {
				d1 = v
			}
			tag := ""
			switch {
			case right && d1 > 0:
				eval.Level = "right_signal"
				eval.Pass = true
				eval.Details["right_signal"] = 1
				tag = "二突"
			case left && d1 > 0:
				eval.Level = "left_signal"
				eval.Pass = true
				eval.Details["left_signal"] = 1
				tag = "一突"
			}
			reason := eval.Level
			if d1 <= 0 {
				reason = "d1=0"
			} else if !eval.Pass {
				reason = "total_below"
			}
			a.recordNDiag(NDiag{Code: code, Name: md.Name, D1: d1, Total: eval.TotalScore,
				Level: eval.Level, Tag: tag, Pass: eval.Pass, Reason: reason})
		}
		// 未通过战法硬性/评分门槛；二突/一突已在上面被提为 Pass → 只记分不出信号
		if !eval.Pass {
			unSig = append(unSig, fmt.Sprintf("%s:%s(%.0f)", strategyLabel(runner.Type), eval.Level, eval.TotalScore))
			continue
		}
		// 通过的战法生成交易信号，失败或为空则跳过
		sig, err := runner.Strategy.GenerateSignal(code, eval)
		if err != nil || sig == nil {
			unSig = append(unSig, strategyLabel(runner.Type)+":信号生成失败")
			continue
		}
		// 操作类型缺省为 watch（仅观察），避免空 action
		action := string(sig.Action)
		if action == "" {
			action = "watch"
		}
		sigReason := sig.Reason
		if runner.Type == strategy.SignalNShape {
			sigReason = nShapeReason(sigReason, d1)
		}
		sigs = append(sigs, Signal{
			ID:          seqID(),
			Code:        code,
			Name:        orDefault(sig.Name, md.Name),
			Strategy:    string(runner.Type),
			Direction:   direction,
			Action:      action,
			Tag:         nShapeTag(eval),
			Price:       sig.Price,
			Confidence:  sig.Confidence,
			Reason:      sigReason,
			Sector:      sectorName,
			GeneratedAt: now,
		})
	}
	// 动量分单独计算（量价+MACD+走势），作为 8a/8b 打分量的一部分
	sc.MomentumScore = MomentumScore(md, a.momentumWeights())
	sc.MomentumValid = momentumDataValid(md)
	sc.SignalActive = len(sigs) > 0

	// Q2: 动量分达到阈值且四战法均未出信号时，补一条 watch 观察信号
	// （量价齐升/资金流入但战法形态未确认，仅观察不自动交易）
	// 门控 sc.MomentumValid：竞价/盘前今日成交量=0 时动量数据不完整（无真实成交），
	// 不发存量历史数据凑出来的动量 watch，等 9:30 实盘有成交量后再出。
	if len(sigs) == 0 && sc.MomentumValid && sc.MomentumScore >= a.momentumSignalThreshold() {
		sigs = append(sigs, Signal{
			ID:          seqID(),
			Code:        code,
			Name:        md.Name,
			Strategy:    "动量",
			Direction:   direction,
			Action:      "watch",
			Price:       md.Price,
			Confidence:  sc.MomentumScore / 100.0,
			Reason:      fmt.Sprintf("动量%.0f 量价齐升(MA+MACD+走势)", sc.MomentumScore),
			Sector:      sectorName,
			GeneratedAt: now,
		})
		sc.SignalActive = true
	} else if len(sigs) == 0 && sc.MomentumScore >= a.momentumSignalThreshold() && !sc.MomentumValid {
		// 竞价/盘前数据不完整时不发 watch，但保留可排查日志
		log.Printf("[combat_agent] 动量%.0f达阈值但成交前数据不完整(Volume<=0/MACD缺), 暂不发动量watch: %s",
			sc.MomentumScore, code)
	}
	sc.UpdatedAt = now
	input.Scores[code] = sc
	// 战法评分日志：code + 各维度分 + 是否命中（FLOW 全流程日志要求）
	// 附加"未出"原因（diagnostic）：各战法未出信号的具体原因，便于排查非龙头为何不出分/不发声。
	unSigNote := ""
	if len(unSig) > 0 {
		unSigNote = " | 未出: " + strings.Join(unSig, " ")
	}
	log.Printf("[combat_agent] 评分 %s(%s) 龙=%.0f 双=%.0f N=%.0f 回=%.0f 动量=%.0f 命中=%v%s",
		code, md.Name, sc.DragonScore, sc.DoubleBumpScore, sc.NScore, sc.DragonReturnScore, sc.MomentumScore, sc.SignalActive, unSigNote)
	return sigs
}

// ScorePool 近实时 8a/8b 持续打分入口：对打分池（持仓+自选）逐只执行四战法评分 + 动量分，
// 无论是否通过都记录原始分；Pass 的战法生成信号返回（由调用方决定是否广播）。
// 与 ScanLong/ScanShort 共用 evalAll，保证打分口径一致。
// 入参 codes 为打分池代码列表，md 为行情映射，d1Scores 为最近一轮 D1 评分缓存
// （主循环产出，近实时循环复用，不每 5s 调 LLM），emotionPhase 供 N 形情绪硬闸使用。
// 返回 scores（code → 各战法原始分）与 sigs（本轮通过战法产生的信号）。
func (a *Agent) ScorePool(codes []string, md map[string]*strategy_engine.StockMarketData, d1Scores map[string]D1Score, emotionPhase string) (map[string]StockScores, []Signal) {
	a.mu.RLock()
	runners := a.runners
	a.mu.RUnlock()
	if len(runners) == 0 {
		return nil, nil
	}
	scores := make(map[string]StockScores, len(codes))
	// 组装最小化 ScanInput：个股直入（无板块上下文）、打分池行情、D1 评分缓存、情绪阶段
	input := &ScanInput{MarketData: md, Scores: scores, D1Scores: d1Scores, EmotionPhase: emotionPhase}
	now := time.Now()
	var sigs []Signal
	for _, code := range codes {
		// L1 阻塞的股票跳过打分
		if input.L1Blocked[code] {
			continue
		}
		// 逐只走 evalAll，方向统一为做多、板块记为"个股"
		sigs = append(sigs, a.evalAll(input, runners, code, md[code], nil, "做多", "个股", now)...)
	}
	return scores, sigs
}

// momentumWeights 读取动量分权重配置（nil 防护，缺省回退 40/30/30）。
// 返回量价/ MACD /走势三者的权重配置，供 MomentumScore 使用。
func (a *Agent) momentumWeights() config.MomentumConfig {
	a.mu.RLock()
	cfg := a.strategyCfg
	a.mu.RUnlock()
	// 配置缺失时回退默认权重：量价40 + MACD30 + 走势30
	if cfg == nil {
		return config.MomentumConfig{VolumePriceWeight: 40, MACDWeight: 30, TrendWeight: 30, SignalThreshold: 60}
	}
	return cfg.Momentum
}

// momentumSignalThreshold 读取动量分触发信号的阈值（默认 60）。
func (a *Agent) momentumSignalThreshold() float64 {
	w := a.momentumWeights()
	if w.SignalThreshold <= 0 {
		return 60
	}
	return w.SignalThreshold
}

// strategyDataInsufficient 判断某战法类型的输入数据是否不足（不足时得分 0 不代表真实 0 分）。
func strategyDataInsufficient(t strategy.SignalType, md *strategy_engine.StockMarketData) bool {
	if md == nil {
		return true
	}
	switch t {
	case strategy.SignalDragon:
		// 龙头需要实时价 + 至少 5 根日K（RS 趋势）
		return md.Price <= 0 || len(md.KLines) < 5
	case strategy.SignalDoubleBump:
		// 双响炮需要至少 20 根日K（均线与量能）
		return len(md.KLines) < 20
	case strategy.SignalDragonReturn:
		// 龙回头需要至少 30 根日K（主升段/回调）
		return len(md.KLines) < 30
	case strategy.SignalNShape:
		// N 形需要实时价 + 日K + 分钟K(MACD)
		return md.Price <= 0 || len(md.KLines) < 2 || len(md.MinuteKLine) < 2
	default:
		return false
	}
}

// momentumDataValid 判断动量分所需数据是否完整（量价 + 走势 + MACD 任一缺失即视为不完整）。
func momentumDataValid(md *strategy_engine.StockMarketData) bool {
	if md == nil || md.Quote == nil {
		return false
	}
	q := md.Quote
	if q.Price <= 0 || q.Volume <= 0 || len(md.KLines) < 5 {
		return false
	}
	// 分钟 MACD 需有有效数据
	m := md.MinuteMACD
	return !(m.DIF == 0 && m.DEA == 0 && m.Bar == 0)
}

// markDataGap 记录某战法因数据不足而降级的数据缺口，供前端区分真实 0 分与无数据。
func markDataGap(sc *StockScores, t strategy.SignalType, md *strategy_engine.StockMarketData) {
	if sc.DataGaps == nil {
		sc.DataGaps = make(map[string]bool)
	}
	sc.DataGaps[string(t)] = true
}

// ScanShort 执行做空扫描：7b 板块利空→验证后个股→8b；8b 个股利空→直入战法（反向信号）。
// 仅当做空开关启用时执行，否则返回 nil。
// 流程与 ScanLong 对称：方向字段标记为"做空"，供上层做反向处理。
func (a *Agent) ScanShort(input ScanInput) []Signal {
	// 做空开关关闭 → 直接返回，不做任何做空扫描
	if !a.ShortEnabled() {
		return nil
	}
	if len(input.Sectors) == 0 && len(input.IndividualStocks) == 0 {
		return nil
	}

	a.mu.RLock()
	runners := a.runners
	a.mu.RUnlock()

	if len(runners) == 0 {
		log.Printf("[combat_agent] ScanShort: 无策略策略")
		return nil
	}

	var raw []Signal
	now := time.Now()

	// 7b 板块利空 → 验证后的个股走 8b
	for _, sector := range input.Sectors {
		// 仅处理方向为"利空"的板块（利好走做多路径 ScanLong）
		if sector.Direction != "利空" {
			continue
		}
		for _, code := range sector.Stocks {
			// L1 阻塞的个股跳过
			if input.L1Blocked[code] {
				continue
			}
			raw = append(raw, a.evalAll(&input, runners, code, input.MarketData[code], &sector, "做空", sector.Name, now)...)
		}
	}

	// 8b 个股利空 → 直入战法（反向信号）
	for _, code := range input.IndividualStocks {
		if input.L1Blocked[code] {
			continue
		}
		raw = append(raw, a.evalAll(&input, runners, code, input.MarketData[code], nil, "做空", "个股", now)...)
	}

	// 同样套 Laodeng 评分修正
	signals := a.applyLaodeng(raw)
	log.Printf("[combat_agent] ScanShort: %d 板块 %d 个股 → %d 做空信号", len(input.Sectors), len(input.IndividualStocks), len(signals))
	return signals
}

// CheckPositionAlerts 检查所有持仓的止盈止损条件，返回需要提醒的信号列表。
// 根据实时行情价格计算盈亏比例，触发止盈/止损阈值时生成提醒信号。
// 入参 rpt 提供持仓明细，marketAPI 提供实时报价，scores 为当轮做多打分表（SignalActive=该股做多信号），
// bearHit 为客置参数：该股本轮命中利空板块/利空个股的 code→true 映射（即"做空/利空信号"）。
//
// 止盈/止损按持仓方向看对应信号（跟 N 形/动量一致，做空则镜像反）：
//   - 做多止盈：仍有做多信号 → 继续持有(降级提示)；无 → 硬止盈。
//   - 做多止损：出现做空/利空信号 → 硬止损；未出现 → 可能洗盘，降级提示观察。
//   - 做空止盈：仍有做空/利空信号 → 继续持有(降级提示)；无 → 硬止盈。
//   - 做空止损：出现做多/利好信号 → 硬止损；未出现 → 降级提示观察。
func (a *Agent) CheckPositionAlerts(rpt *report.Report, marketAPI *data.MarketAPI, scores map[string]StockScores, bearHit ...map[string]bool) []Signal {
	positions := rpt.HeldPositions()
	if len(positions) == 0 {
		return nil
	}

	// 可选：本轮利空(做空)信号命中集合（命中利空板块/利空个股）
	var bears map[string]bool
	if len(bearHit) > 0 {
		bears = bearHit[0]
	}

	var alerts []Signal
	now := time.Now()

	for _, pos := range positions {
		// 未设置止盈/止损阈值的持仓不检查
		if pos.TakeProfitPct <= 0 && pos.StopLossPct <= 0 {
			continue
		}

		// 拉取实时报价，失败/为空/现价为 0（停牌未成交）则跳过该持仓，
		// 避免以无效价格误判止盈/止损（如 0 价会算成 -100%）。
		quote, err := marketAPI.GetRealtimeQuote(pos.Code)
		if err != nil || quote == nil || quote.Price <= 0 {
			continue
		}

		// 按现价计算持仓盈亏比例（%）
		pnl := (quote.Price - pos.EntryPrice) / pos.EntryPrice * 100

		// 该股当前信号：做多信号=打分表 SignalActive(做多池)；做空/利空信号=命中利空板块映射
		hasBull := false
		if sc, ok := scores[pos.Code]; ok {
			hasBull = sc.SignalActive
		}
		hasBear := false
		if bears != nil {
			hasBear = bears[pos.Code]
		}
		isShort := pos.Direction == "做空"

		// 触及止盈线 → 生成止盈提醒。
		// 做多：仍有做多信号→持有(降级提示)；无→硬止盈。做空：仍有做空信号→持有；无→硬止盈。
		if pos.TakeProfitPct > 0 && pnl >= pos.TakeProfitPct {
			alertType, action := "止盈", "止盈"
			reason := fmt.Sprintf("盈亏%.2f%% 触及止盈%.0f%%", pnl, pos.TakeProfitPct)
			keepSig := hasBear // 做空：有做空信号继续持有
			if !isShort {
				keepSig = hasBull // 做多：有做多信号继续持有
			}
			if keepSig {
				alertType, action = "提示", "关注"
				if isShort {
					reason = fmt.Sprintf("盈亏%.2f%% 已触止盈%.0f%% 但仍有做空信号,建议持有观察", pnl, pos.TakeProfitPct)
				} else {
					reason = fmt.Sprintf("盈亏%.2f%% 已触止盈%.0f%% 但仍有做多信号,建议持有观察", pnl, pos.TakeProfitPct)
				}
			}
			alerts = append(alerts, Signal{
				ID:          seqID(),
				Code:        pos.Code,
				Name:        pos.Name,
				Strategy:    pos.Strategy,
				Direction:   "提醒",
				Action:      action,
				AlertType:   alertType,
				Price:       quote.Price,
				Confidence:  1.0,
				Reason:      reason,
				GeneratedAt: now,
			})
		}

		// 触及止损线 → 生成止损提醒。
		// 做多：出现做空/利空信号→硬止损；未出现→可能是洗盘，降级提示观察。做空：出现做多信号→硬止损；否则提示。
		if pos.StopLossPct > 0 && pnl <= -pos.StopLossPct {
			alertType, action := "止损", "止损"
			reason := fmt.Sprintf("盈亏%.2f%% 触及止损%.0f%%", pnl, pos.StopLossPct)
			// 是否出现对已方向不利的信号决定是否硬止损
			hard := false
			if isShort {
				hard = hasBull // 做空止损：出现做多(利好)信号→硬止损
			} else {
				hard = hasBear // 做多止损：出现做空(利空)信号→硬止损
			}
			if !hard {
				alertType, action = "提示", "关注"
				if isShort {
					reason = fmt.Sprintf("盈亏%.2f%% 已触止损%.0f%% 未出现利好确认,关注是否回稳", pnl, pos.StopLossPct)
				} else {
					reason = fmt.Sprintf("盈亏%.2f%% 已触止损%.0f%% 未出现利空/做空信号,关注是否洗盘", pnl, pos.StopLossPct)
				}
			}
			alerts = append(alerts, Signal{
				ID:          seqID(),
				Code:        pos.Code,
				Name:        pos.Name,
				Strategy:    pos.Strategy,
				Direction:   "提醒",
				Action:      action,
				AlertType:   alertType,
				Price:       quote.Price,
				Confidence:  1.0,
				Reason:      reason,
				GeneratedAt: now,
			})
		}
	}

	log.Printf("[combat_agent] CheckPositionAlerts: %d 持仓 → %d 提醒", len(positions), len(alerts))
	return alerts
}

// Scan 通用扫描入口（不区分方向），对输入板块的每只股票逐策略评估打分。
// 方向直接沿用板块本身的 Direction（利好/利空），不做做多/做空分流。
// 返回所有通过策略评估的信号。
func (a *Agent) Scan(input ScanInput) []Signal {
	a.mu.RLock()
	runners := a.runners
	a.mu.RUnlock()

	// 无可运行策略或没有输入板块 → 直接返回
	if len(runners) == 0 || len(input.Sectors) == 0 {
		return nil
	}

	var allSignals []Signal
	now := time.Now()

	for _, sector := range input.Sectors {
		for _, code := range sector.Stocks {
			if input.L1Blocked[code] {
				continue
			}
			// 板块方向即信号方向，板块名作为信号所属板块
			allSignals = append(allSignals, a.evalAll(&input, runners, code, input.MarketData[code], &sector, sector.Direction, sector.Name, now)...)
		}
	}

	log.Printf("[combat_agent] Scan: %d 板块 → %d 信号 (%v)",
		len(input.Sectors), len(allSignals), time.Since(now))
	return allSignals
}
