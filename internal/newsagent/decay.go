// decay.go — 新闻时效衰减（§SIGNAL_EDGE_ENHANCEMENT_PLAN P1.1）。
// 新闻 alpha 随年龄按类型半衰期指数衰减：score *= 2^(-age/halfLife)；
// age > 3×halfLife 视为过期（EffectiveScore 返回 0）。
// 仅作用于"衰减后打分"副本（引擎在 NewsEvent 值副本上原地改写 Score），
// 原始 Score 与持久化/展示数据保持不变。
// English: news time-decay (P1.1). News alpha decays exponentially by per-type half-life:
// score *= 2^(-age/halfLife); age > 3×halfLife expires the event (EffectiveScore returns 0).
// Applied only to a scoring copy (the engine rewrites Score on its value-copy), leaving the
// original Score and persisted/display data untouched.

package newsagent

import (
	"math"
	"time"
)

// DefaultNewsHalfLife 各事件类型默认半衰期（分钟）。
// 快变量（个股/产业链扩散传导）衰减快；慢变量（政策发酵）衰减慢。
// English: default half-life (minutes) per event type. Fast-decaying for stock-level / chain
// diffusion, slow for policy that keeps fermenting.
var DefaultNewsHalfLife = map[string]time.Duration{
	"政策":    120 * time.Minute,
	"宏观":    120 * time.Minute,
	"行业":    90 * time.Minute,
	"公司":    60 * time.Minute,
	"产业链扩散": 45 * time.Minute,
	"降级兜底":  120 * time.Minute,
	"IPO":   120 * time.Minute,
	"政策反制":  120 * time.Minute,
	"对抗":    90 * time.Minute,
}

const (
	// newsExpireHalfLives 过期倍数：age > 3×halfLife 视为过期。
	newsExpireHalfLives = 3.0
)

// decayHalfLife 返回事件类型的半衰期；未配置类型回退 120 分钟默认。
// English: returns the per-type half-life, falling back to a 120min default for unknown types.
func decayHalfLife(ev *NewsEvent) time.Duration {
	if ev == nil {
		return 120 * time.Minute
	}
	key := ev.EventType
	if hl, ok := DefaultNewsHalfLife[key]; ok {
		return hl
	}
	// 降级兜底
	return DefaultNewsHalfLife["降级兜底"]
}

// EffectiveScore 返回按年龄衰减后的有效分。
// age>0 时 score *= 2^(-age/halfLife)；过期事件（age > 3×halfLife）返回 0，
// 由引擎 0.50 阈值过滤自然掉出有效事件池。
// 注意：本方法为纯数学（不做开关判断）；开关（Enhance.NewsDecay）在引擎侧 gating——
// 引擎在值副本上调用，原始 Score 与持久化/展示数据不变。
// English: EffectiveScore returns the age-decayed score. For age>0 the score is multiplied by
// 2^(-age/halfLife); expired events (age > 3×halfLife) return 0 so the engine's 0.50 threshold
// naturally drops them. NOTE: this is pure math — the Enchance.NewsDecay toggle gates at the
// engine; called on value-copies so stored/display scores stay intact.
func (ev *NewsEvent) EffectiveScore(now time.Time) float64 {
	if ev == nil {
		return 0
	}
	t, err := time.ParseInLocation("2006-01-02 15:04:05", ev.Datetime, time.Local)
	if err != nil {
		// Datetime 异常不误杀，回退原分
		return ev.Score
	}
	age := now.Sub(t)
	if age <= 0 {
		return ev.Score
	}
	hl := decayHalfLife(ev)
	if age > newsExpireHalfLives*hl {
		return 0
	}
	ratio := float64(age) / float64(hl)
	return ev.Score * pow2(-ratio)
}

// pow2 计算 2^exp（含分数指数）。事件数量级很小，直接用标准库浮点实现保证精度。
func pow2(exp float64) float64 {
	return math.Exp2(exp)
}
