package combat_agent

import (
	"log"
	"sync"
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/strategy"
)

// StrategyRunner 战法运行时接口
type StrategyRunner struct {
	Type     strategy.SignalType
	Strategy strategy.Strategy
}

type Agent struct {
	mu          sync.RWMutex
	strategyCfg *config.StrategyConfig
	runners     []StrategyRunner
	shortRunner StrategyRunner // 做空骨架(留空)
}

func New(cfg *config.StrategyConfig) *Agent {
	return &Agent{
		strategyCfg: cfg,
	}
}

func (a *Agent) SetRunners(runners []StrategyRunner) {
	a.mu.Lock()
	a.runners = runners
	a.mu.Unlock()
}

// HotReload 热加载策略参数（被 fsnotify 回调调用）
func (a *Agent) HotReload(newCfg *config.StrategyConfig) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.strategyCfg = newCfg
	log.Printf("[combat_agent] 策略参数已热更新")
}

// Scan 扫描验证板块的成分股，执行所有战法
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

			for _, runner := range runners {
				if runner.Strategy == nil {
					continue
				}

				eval, err := runner.Strategy.Evaluate(code, nil)
				if err != nil || eval == nil || !eval.Pass {
					continue
				}

				sig, err := runner.Strategy.GenerateSignal(code, eval)
				if err != nil || sig == nil {
					continue
				}

				allSignals = append(allSignals, Signal{
					Code:       code,
					Name:       sig.Name,
					Strategy:   string(runner.Type),
					Direction:  "做多",
					Price:      sig.Price,
					Confidence: sig.Confidence,
					Reason:     sig.Reason,
					Sector:     sector.Name,
				})
			}
		}
	}

	log.Printf("[combat_agent] 扫描 %d 板块/%d 信号 (耗时 %v)",
		len(input.Sectors), len(allSignals), time.Since(now))
	return allSignals
}

// ScanShort 做空骨架（方法体空，等你填充）
func (a *Agent) ScanShort(input ScanInput) []Signal {
	return nil
}
