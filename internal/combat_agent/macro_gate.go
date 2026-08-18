// 宏观利空门控（E1）：股指期货交割日等高影响宏观事件作为整体利空，
// 当日处于影响期时买入信号统一降级，仅超高置信度（"特别高质量信号"）放行；
// N 形超短与动量 watch 观察信号在交割日一律拦截。
// English: E1 macro bearish gate — on high-impact macro days (e.g. index-futures delivery day,
// 交割日), buy signals are downgraded as a whole unless exceptionally high-confidence; N-shape
// ultra-short and momentum watch signals are blocked entirely on those days.
package combat_agent

import (
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/strategy"
)

// macroGateLevels 触发门控的宏观事件级别默认集合（股指期货交割日）。
// English: default macro-event levels that trigger the gate (index-futures delivery day).
var macroGateLevels = []string{"contract"}

// macroEventsAt 返回指定时刻处于影响期/临近的宏观事件（生成当年全部事件后筛选）。
// 供宏观利空门控判断使用；按天生成成本低，直接每次调用即可。
// English: returns the macro events within their impact window at the given instant (generates the
// year's full calendar then filters). Cheap enough to call per eval; used by the E1 macro gate.
func macroEventsAt(now time.Time) []data.MacroEvent {
	return data.GetActiveMacroEvents(data.GenMacroEvents(now.Year(), nil), now)
}

// macroEventsNow 返回当前时刻处于影响期/临近的宏观事件（宏门控运行入口）。
// English: macro events within their impact window at the current instant (E1 gate entry point).
func macroEventsNow() []data.MacroEvent {
	return macroEventsAt(time.Now())
}

// hasGateTriggerLevel 判断宏观事件列表中是否存在命中门控级别的事件（如 contract 交割日）。
// English: reports whether any macro event hits a gate-triggering level (e.g. contract delivery day).
func hasGateTriggerLevel(events []data.MacroEvent, levels []string) bool {
	if len(levels) == 0 {
		levels = macroGateLevels
	}
	hit := make(map[string]bool, len(levels))
	for _, l := range levels {
		hit[l] = true
	}
	for _, e := range events {
		if hit[e.Level] {
			return true
		}
	}
	return false
}

// macroGateActive 读取宏观利空门控配置并判断当前是否处于门控状态（如交割日）。
// 配置未启用（Enabled=false）或未命中门控级别时返回 false（行为不变）。
// 返回 (enabled 且命中, 门控配置)，供 evalAll 逐信号降级判断。
// English: reads the E1 macro-gate config and reports whether the gate is currently active (e.g.
// delivery day). Returns (active, config); inactive when the feature is disabled or no event level
// matches, preserving the original behavior.
func (a *Agent) macroGateActive() (bool, config.MacroGateConfig) {
	cfg := a.macroGateConfig()
	if !cfg.Enabled {
		return false, cfg
	}
	if !hasGateTriggerLevel(macroEventsNow(), cfg.Levels) {
		return false, cfg
	}
	return true, cfg
}

// macroGateConfig 读取宏观利空门控配置（nil 防护，未配置时返回零值 → 门控关闭）。
// English: reads the macro-gate config (nil-safe; zero value → gate off).
func (a *Agent) macroGateConfig() config.MacroGateConfig {
	a.mu.RLock()
	cfg := a.strategyCfg
	a.mu.RUnlock()
	if cfg == nil {
		return config.MacroGateConfig{}
	}
	return cfg.MacroGate
}

// macroGateMinConfidence 返回放行买入信号的最低置信度（默认 0.85）。
// English: returns the minimum confidence for a buy signal to pass the macro gate (default 0.85).
func macroGateMinConfidence(cfg config.MacroGateConfig) float64 {
	if cfg.MinConfidence <= 0 {
		return 0.85
	}
	return cfg.MinConfidence
}

// macroGateBlockNShape 交割日是否拦截 N 形超短（默认 true；显式 false 才取消拦截）。
// English: whether N-shape ultra-short is blocked on gate days (default true; only an explicit
// false disables the block).
func macroGateBlockNShape(cfg config.MacroGateConfig) bool {
	if cfg.BlockNShape {
		return true
	}
	// 未显式配置时按缺省 true 处理（零值 = 开启拦截），显式 false 才放行
	// English: unset (zero) defaults to true (block); only an explicit false disables it.
	return true
}

// macroGateBlockMomentum 交割日是否拦截动量 watch 观察信号（默认 true：整体利空下不新增观察）。
// English: whether the momentum watch signal is blocked on gate days (default true — no new watch
// under a whole-market bearish day).
func macroGateBlockMomentum(cfg config.MacroGateConfig) bool {
	if cfg.BlockMomentum {
		return true
	}
	// 未显式配置时按缺省 true 处理（零值 = 开启拦截），显式 false 才放行
	// English: unset (zero) defaults to true (block); only an explicit false disables it.
	return true
}

// applyMacroGate 对已生成信号应用宏观利空门控：
//   - 非 N 形战法买入信号：置信度 < 最低门槛 → 降级为 watch（理由注明宏观利空）；
//     置信度 ≥ 门槛 → 保留（"特别高质量信号"放行）。
//   - N 形超短：门控日一律降级为 watch（超短对交割日波动最敏感）。
//   - 动量 watch 观察信号：门控日一律降级拦截（整体利空下不新增观察）。
//
// 返回处理后的信号切片（原地修改 reason/action，保持顺序与稳定性）。
// English: applies the E1 macro bearish gate to generated signals — non-N buy signals below the
// minimum confidence are downgraded to watch (reason notes the macro bearish context); those at/above
// the threshold pass as "exceptionally high-quality"; N-shape ultra-short and momentum watch signals
// are always downgraded on gate days. Mutates reason/action in place, preserving order.
func applyMacroGate(sigs []Signal, gateActive bool, cfg config.MacroGateConfig) []Signal {
	if !gateActive || len(sigs) == 0 {
		return sigs
	}
	minConf := macroGateMinConfidence(cfg)
	blockN := macroGateBlockNShape(cfg)
	blockMomentum := macroGateBlockMomentum(cfg)
	for i := range sigs {
		s := &sigs[i]
		isBuy := s.Action == "buy" || s.Action == "买入"
		// 动量 watch 观察信号（Strategy="动量"，Action=watch）：门控日拦截
		// English: momentum watch signals (Strategy="动量", action watch) are blocked on gate days.
		if s.Strategy == "动量" && s.Action == "watch" {
			if blockMomentum {
				s.Reason = "宏观利空(交割日)整体降级: " + s.Reason
			}
			continue
		}
		// N 形超短：门控日一律降级 watch
		// English: N-shape ultra-short is always downgraded to watch on gate days.
		if s.Strategy == string(strategy.SignalNShape) && blockN {
			if isBuy {
				s.Action = "watch"
				s.Reason = "宏观利空(交割日)拦截N形超短: " + s.Reason
			}
			continue
		}
		// 非 N 形买入信号：置信度低于门槛 → 降级 watch；达到门槛 → 保留（特别高质量放行）
		// English: non-N buy signals — below the confidence gate are downgraded to watch; at/above the
		// threshold pass as "exceptionally high-quality".
		if isBuy && s.Confidence < minConf {
			s.Action = "watch"
			s.Reason = "宏观利空(交割日)降级(置信度不足): " + s.Reason
		}
	}
	return sigs
}
