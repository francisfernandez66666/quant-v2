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

// Agent 战法引擎核心，管理多策略运行器与配置热更新。
// 所有字段通过 mu 读写锁保护，保证并发扫描/热更新安全。
type Agent struct {
	mu           sync.RWMutex           // 读写锁，保护并发访问
	strategyCfg  *config.StrategyConfig // 策略参数配置（含动量分权重等，可热更新）
	laodengCfg   *config.LaodengConfig  // Laodeng 评分配置（nil 表示未启用）
	runners      []StrategyRunner       // 多策略运行器列表（做多/通用扫描共用）
	shortRunner  StrategyRunner         // 做空策略运行器（预留）
	shortEnabled bool                   // 做空功能开关（关闭时 ScanShort 直接返回 nil）
}

// New 创建战法引擎实例。
func New(cfg *config.StrategyConfig) *Agent {
	return &Agent{
		strategyCfg: cfg,
		runners:     make([]StrategyRunner, 0),
	}
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
	sc := StockScores{Code: code}
	var sigs []Signal
	for _, runner := range runners {
		// 策略实例为空则跳过该运行器
		if runner.Strategy == nil {
			continue
		}
		// 按战法类型分发到真实评分逻辑（adapter.go evalFor）
		eval, err := evalFor(runner, code, md, sector, input.EmotionPhase)
		// 评分失败或返回空结果 → 该战法视为 0 分，不产出信号
		if err != nil || eval == nil {
			continue
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
		// 未通过战法硬性/评分门槛 → 只记分不出信号
		if !eval.Pass {
			continue
		}
		// 通过的战法生成交易信号，失败或为空则跳过
		sig, err := runner.Strategy.GenerateSignal(code, eval)
		if err != nil || sig == nil {
			continue
		}
		// 操作类型缺省为 watch（仅观察），避免空 action
		action := string(sig.Action)
		if action == "" {
			action = "watch"
		}
		sigs = append(sigs, Signal{
			ID:          seqID(),
			Code:        code,
			Name:        sig.Name,
			Strategy:    string(runner.Type),
			Direction:   direction,
			Action:      action,
			Price:       sig.Price,
			Confidence:  sig.Confidence,
			Reason:      sig.Reason,
			Sector:      sectorName,
			GeneratedAt: now,
		})
	}
	// 动量分单独计算（量价+MACD+走势），作为 8a/8b 打分量的一部分
	sc.MomentumScore = MomentumScore(md, a.momentumWeights())
	sc.SignalActive = len(sigs) > 0
	sc.UpdatedAt = now
	input.Scores[code] = sc
	// 战法评分日志：code + 各维度分 + 是否命中（FLOW 全流程日志要求）
	log.Printf("[combat_agent] 评分 %s(%s) 龙=%.0f 双=%.0f N=%.0f 回=%.0f 动量=%.0f 命中=%v",
		code, md.Name, sc.DragonScore, sc.DoubleBumpScore, sc.NScore, sc.DragonReturnScore, sc.MomentumScore, sc.SignalActive)
	return sigs
}

// ScorePool 近实时 8a/8b 持续打分入口：对打分池（持仓+自选）逐只执行四战法评分 + 动量分，
// 无论是否通过都记录原始分；Pass 的战法生成信号返回（由调用方决定是否广播）。
// 与 ScanLong/ScanShort 共用 evalAll，保证打分口径一致。
// 入参 codes 为打分池代码列表，md 为行情映射，emotionPhase 供 N 形情绪硬闸使用。
// 返回 scores（code → 各战法原始分）与 sigs（本轮通过战法产生的信号）。
func (a *Agent) ScorePool(codes []string, md map[string]*strategy_engine.StockMarketData, emotionPhase string) (map[string]StockScores, []Signal) {
	a.mu.RLock()
	runners := a.runners
	a.mu.RUnlock()
	if len(runners) == 0 {
		return nil, nil
	}
	scores := make(map[string]StockScores, len(codes))
	// 组装最小化 ScanInput：个股直入（无板块上下文）、打分池行情、情绪阶段
	input := &ScanInput{MarketData: md, Scores: scores, EmotionPhase: emotionPhase}
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
		return config.MomentumConfig{VolumePriceWeight: 40, MACDWeight: 30, TrendWeight: 30}
	}
	return cfg.Momentum
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
// 入参 rpt 提供持仓明细，marketAPI 提供实时报价；返回提醒信号（AlertType=止盈/止损）。
func (a *Agent) CheckPositionAlerts(rpt *report.Report, marketAPI *data.MarketAPI) []Signal {
	positions := rpt.HeldPositions()
	if len(positions) == 0 {
		return nil
	}

	var alerts []Signal
	now := time.Now()

	for _, pos := range positions {
		// 未设置止盈/止损阈值的持仓不检查
		if pos.TakeProfitPct <= 0 && pos.StopLossPct <= 0 {
			continue
		}

		// 拉取实时报价，失败/为空则跳过该持仓
		quote, err := marketAPI.GetRealtimeQuote(pos.Code)
		if err != nil || quote == nil {
			continue
		}

		// 按现价计算持仓盈亏比例（%）
		pnl := (quote.Price - pos.EntryPrice) / pos.EntryPrice * 100

		// 触及止盈线 → 生成止盈提醒（置信度固定 1.0）
		if pos.TakeProfitPct > 0 && pnl >= pos.TakeProfitPct {
			alerts = append(alerts, Signal{
				ID:          seqID(),
				Code:        pos.Code,
				Name:        pos.Name,
				Strategy:    pos.Strategy,
				Direction:   "提醒",
				Action:      "止盈",
				AlertType:   "止盈",
				Price:       quote.Price,
				Confidence:  1.0,
				Reason:      fmt.Sprintf("盈亏%.2f%% 触及止盈%.0f%%", pnl, pos.TakeProfitPct),
				GeneratedAt: now,
			})
		}

		// 触及止损线 → 生成止损提醒（置信度固定 1.0）
		if pos.StopLossPct > 0 && pnl <= -pos.StopLossPct {
			alerts = append(alerts, Signal{
				ID:          seqID(),
				Code:        pos.Code,
				Name:        pos.Name,
				Strategy:    pos.Strategy,
				Direction:   "提醒",
				Action:      "止损",
				AlertType:   "止损",
				Price:       quote.Price,
				Confidence:  1.0,
				Reason:      fmt.Sprintf("盈亏%.2f%% 触及止损%.0f%%", pnl, pos.StopLossPct),
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
