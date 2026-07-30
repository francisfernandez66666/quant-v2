// Package combat_agent 战法引擎：多策略信号执行与持仓监控。
// 支持多方向（做多/做空）扫描、Laodeng 评分修正、止盈止损提醒。
package combat_agent

import (
	"fmt"
	"log"
	"sync"
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/report"
	"quant-trading-v2/internal/strategy"
)

// StrategyRunner 策略运行器，封装策略类型与策略实例。
type StrategyRunner struct {
	Type     strategy.SignalType // 策略信号类型（龙/双响炮/N形/龙回头）
	Strategy strategy.Strategy   // 策略接口实现
}

// Agent 战法引擎核心，管理多策略运行器与配置热更新。
type Agent struct {
	mu           sync.RWMutex            // 读写锁，保护并发访问
	strategyCfg  *config.StrategyConfig   // 策略参数配置
	laodengCfg   *config.LaodengConfig    // Laodeng 评分配置
	runners      []StrategyRunner         // 多策略运行器列表
	shortRunner  StrategyRunner           // 做空策略运行器（预留）
	shortEnabled bool                     // 做空功能开关
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
func (a *Agent) applyLaodeng(signals []Signal) []Signal {
	a.mu.RLock()
	cfg := a.laodengCfg
	a.mu.RUnlock()

	if cfg == nil || !cfg.Enabled || len(signals) == 0 {
		return signals
	}

	out := make([]Signal, len(signals))
	for i, s := range signals {
		// 模拟 laodeng 评分所需数据（实际可从 marketAPI 实时查）
		// 简化：使用默认值，后续可增强
		laodengScore := strategy.ScoreLaodeng(cfg, 600, 12, 2.5, "白酒")
		s.Confidence *= (1 + laodengScore)
		if s.Confidence > 1 {
			s.Confidence = 1
		}
		out[i] = s
	}
	return out
}

// ScanLong 执行做多扫描：7a 板块利好→验证后个股→8a；8a 个股利好→直入战法。
// 返回做多信号列表，若输入的板块/个股为空或无可运行策略则返回 nil。
func (a *Agent) ScanLong(input ScanInput) []Signal {
	a.mu.RLock()
	runners := a.runners
	a.mu.RUnlock()

	if len(runners) == 0 || (len(input.Sectors) == 0 && len(input.IndividualStocks) == 0) {
		return nil
	}

	var raw []Signal
	now := time.Now()

	// 7a 板块利好 → 验证后的个股走 8a
	for _, sector := range input.Sectors {
		if sector.Direction != "利好" {
			continue
		}
		for _, code := range sector.Stocks {
			if input.L1Blocked[code] {
				continue
			}
			md := input.MarketData[code]
			for _, runner := range runners {
				if runner.Strategy == nil {
					continue
				}
				eval, err := runner.Strategy.Evaluate(code, md)
				if err != nil || eval == nil || !eval.Pass {
					continue
				}
				sig, err := runner.Strategy.GenerateSignal(code, eval)
				if err != nil || sig == nil {
					continue
				}
				action := string(sig.Action)
				if action == "" {
					action = "watch"
				}
				raw = append(raw, Signal{
					ID:          seqID(),
					Code:        code,
					Name:        sig.Name,
					Strategy:    string(runner.Type),
					Direction:   "做多",
					Action:      action,
					Price:       sig.Price,
					Confidence:  sig.Confidence,
					Reason:      sig.Reason,
					Sector:      sector.Name,
					GeneratedAt: now,
				})
			}
		}
	}

	// 8a 个股利好 → 直入战法
	for _, code := range input.IndividualStocks {
		if input.L1Blocked[code] {
			continue
		}
		md := input.MarketData[code]
		for _, runner := range runners {
			if runner.Strategy == nil {
				continue
			}
			eval, err := runner.Strategy.Evaluate(code, md)
			if err != nil || eval == nil || !eval.Pass {
				continue
			}
			sig, err := runner.Strategy.GenerateSignal(code, eval)
			if err != nil || sig == nil {
				continue
			}
			action := string(sig.Action)
			if action == "" {
				action = "watch"
			}
			raw = append(raw, Signal{
				ID:          seqID(),
				Code:        code,
				Name:        sig.Name,
				Strategy:    string(runner.Type),
				Direction:   "做多",
				Action:      action,
				Price:       sig.Price,
				Confidence:  sig.Confidence,
				Reason:      sig.Reason,
				Sector:      "个股",
				GeneratedAt: now,
			})
		}
	}

	signals := a.applyLaodeng(raw)
	log.Printf("[combat_agent] ScanLong: %d 板块 %d 个股 → %d 做多信号", len(input.Sectors), len(input.IndividualStocks), len(signals))
	return signals
}

// ScanShort 执行做空扫描：7b 板块利空→验证后个股→8b；8b 个股利空→直入战法（反向信号）。
// 仅当做空开关启用时执行，否则返回 nil。
func (a *Agent) ScanShort(input ScanInput) []Signal {
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
		if sector.Direction != "利空" {
			continue
		}
		for _, code := range sector.Stocks {
			md := input.MarketData[code]
			if input.L1Blocked[code] {
				continue
			}
			for _, runner := range runners {
				if runner.Strategy == nil {
					continue
				}
				eval, err := runner.Strategy.Evaluate(code, md)
				if err != nil || eval == nil || !eval.Pass {
					continue
				}
				sig, err := runner.Strategy.GenerateSignal(code, eval)
				if err != nil || sig == nil {
					continue
				}
				action := string(sig.Action)
				if action == "" {
					action = "watch"
				}
				raw = append(raw, Signal{
					ID:          seqID(),
					Code:        code,
					Name:        sig.Name,
					Strategy:    string(runner.Type),
					Direction:   "做空",
					Action:      action,
					Price:       sig.Price,
					Confidence:  sig.Confidence,
					Reason:      sig.Reason,
					Sector:      sector.Name,
					GeneratedAt: now,
				})
			}
		}
	}

	// 8b 个股利空 → 直入战法（反向信号）
		for _, code := range input.IndividualStocks {
			if input.L1Blocked[code] {
				continue
			}
			md := input.MarketData[code]
			for _, runner := range runners {
				if runner.Strategy == nil {
					continue
				}
				eval, err := runner.Strategy.Evaluate(code, md)
				if err != nil || eval == nil || !eval.Pass {
					continue
				}
				sig, err := runner.Strategy.GenerateSignal(code, eval)
				if err != nil || sig == nil {
					continue
				}
				action := string(sig.Action)
				if action == "" {
					action = "watch"
				}
				raw = append(raw, Signal{
					ID:          seqID(),
					Code:        code,
					Name:        sig.Name,
					Strategy:    string(runner.Type),
					Direction:   "做空",
					Action:      action,
					Price:       sig.Price,
					Confidence:  sig.Confidence,
					Reason:      sig.Reason,
					Sector:      "个股",
					GeneratedAt: now,
				})
			}
		}

	signals := a.applyLaodeng(raw)
	log.Printf("[combat_agent] ScanShort: %d 板块 %d 个股 → %d 做空信号", len(input.Sectors), len(input.IndividualStocks), len(signals))
	return signals
}

// CheckPositionAlerts 检查所有持仓的止盈止损条件，返回需要提醒的信号列表。
// 根据实时行情价格计算盈亏比例，触发止盈/止损阈值时生成提醒信号。
func (a *Agent) CheckPositionAlerts(rpt *report.Report, marketAPI *data.MarketAPI) []Signal {
	positions := rpt.HeldPositions()
	if len(positions) == 0 {
		return nil
	}

	var alerts []Signal
	now := time.Now()

	for _, pos := range positions {
		if pos.TakeProfitPct <= 0 && pos.StopLossPct <= 0 {
			continue
		}

		quote, err := marketAPI.GetRealtimeQuote(pos.Code)
		if err != nil || quote == nil {
			continue
		}

		pnl := (quote.Price - pos.EntryPrice) / pos.EntryPrice * 100

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
// 返回所有通过策略评估的信号。
func (a *Agent) Scan(input ScanInput) []Signal {
	a.mu.RLock()
	runners := a.runners
	a.mu.RUnlock()

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
			md := input.MarketData[code]
			for _, runner := range runners {
				if runner.Strategy == nil {
					continue
				}
				eval, err := runner.Strategy.Evaluate(code, md)
				if err != nil || eval == nil || !eval.Pass {
					continue
				}
				sig, err := runner.Strategy.GenerateSignal(code, eval)
				if err != nil || sig == nil {
					continue
				}
				action := string(sig.Action)
				if action == "" {
					action = "watch"
				}
				allSignals = append(allSignals, Signal{
					ID:          seqID(),
					Code:        code,
					Name:        sig.Name,
					Strategy:    string(runner.Type),
					Direction:   sector.Direction,
					Action:      action,
					Price:       sig.Price,
					Confidence:  sig.Confidence,
					Reason:      sig.Reason,
					Sector:      sector.Name,
					GeneratedAt: now,
				})
			}
		}
	}

	log.Printf("[combat_agent] Scan: %d 板块 → %d 信号 (%v)",
		len(input.Sectors), len(allSignals), time.Since(now))
	return allSignals
}
