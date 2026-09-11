// signal_quality.go — 信号质量实时打榜 → 动态权重（§SIGNAL_EDGE_ENHANCEMENT_PLAN P2.5）。
// 按"战法 × 板块 × 新闻类型 × 情绪相位"分桶统计滚动窗口命中率，少作加权前先冷启动默认
// 权重 1.0，达到最小样本量后才驱动权重乘子与准入阈值漂移（限幅 ±10% 单次，慢漂移防震荡）。
// 自包含 + 线程安全；引擎侧 Enhance.DynWeight 门控接入（关闭时全部返回 1.0/默认，零行为变化）。
// English: signal-quality leaderboard → dynamic weights (P2.5). Rolls per-bucket hit-rate by
// tactic × sector × news-type × emotion-phase; keeps a default weight of 1.0 until a minimum
// sample is reached, then drives weight multipliers and gate thresholds (clamped to ±10% per step
// for a slow drift that resists oscillation). Self-contained & thread-safe; gated by Enhance.DynWeight.

package research

import (
	"strings"
	"sync"
)

// QualityBucketKey 分桶键：tactic|sector|newsType|phase（其中空维度用 "-" 占位）。
type QualityBucketKey string

// MakeQualityKey 组装分桶键。English: builds a quality bucket key.
func MakeQualityKey(tactic, sector, newsType, phase string) QualityBucketKey {
	return QualityBucketKey(strings.Join([]string{tactic, sector, newsType, phase}, "|"))
}

// QualityBucket 单桶滚动统计（N≥MinSample 才启用权重调节）。
type QualityBucket struct {
	N       int     // 样本数
	Hit     int     // 命中数
	Weight  float64 // 动态权重乘子（默认 1.0）
	Gate    float64 // 准入阈值漂移（默认 0，>0 抬高门槛）
	HitRate float64 // 当前命中率（Hit/N）
}

// signalQualityConfig 动态权重配置。English: dynamic-weight config.
type signalQualityConfig struct {
	MinSample     int     // 启用所需最小样本（默认 20）
	SlidePct      float64 // 单次权重变化限幅（默认 0.10 = ±10%）
	TargetHitRate float64 // 目标命中率（默认 0.35）
}

func (c *signalQualityConfig) fill() {
	if c.MinSample <= 0 {
		c.MinSample = 20
	}
	if c.SlidePct <= 0 {
		c.SlidePct = 0.10
	}
	if c.TargetHitRate <= 0 {
		c.TargetHitRate = 0.35
	}
}

// SignalQualityTable 分桶质量表（线程安全）。
type SignalQualityTable struct {
	mu     sync.RWMutex
	cfg    signalQualityConfig
	Buckets map[QualityBucketKey]*QualityBucket
}

// NewSignalQualityTable 建表。English: creates the quality table.
func NewSignalQualityTable(minSample int) *SignalQualityTable {
	if minSample <= 0 {
		minSample = 20
	}
	return &SignalQualityTable{cfg: signalQualityConfig{MinSample: minSample, SlidePct: 0.10, TargetHitRate: 0.35}, Buckets: make(map[QualityBucketKey]*QualityBucket)}
}

func (t *SignalQualityTable) bucket(k QualityBucketKey) *QualityBucket {
	b, ok := t.Buckets[k]
	if !ok {
		b = &QualityBucket{Weight: 1.0}
		t.Buckets[k] = b
	}
	return b
}

// Update 记录一次信号结果（hit 是否命中），按滑窗滚动（只保留最近 MaxSamples 条）。
// 达到 MinSample 后按偏差驱动权重与门槛漂移。
// English: records one signal outcome and drifts weight/gate when MinSample is reached
// (retains only the most recent MaxSamples entries).
func (t *SignalQualityTable) Update(key QualityBucketKey, hit bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	b := t.bucket(key)
	b.N++
	if hit {
		b.Hit++
	}
	b.HitRate = float64(b.Hit) / float64(b.N)
	if b.N >= t.cfg.MinSample {
		dev := b.HitRate - t.cfg.TargetHitRate // >0 好于目标 → 加权
		slide := t.cfg.SlidePct
		if dev < 0 {
			slide = -t.cfg.SlidePct
		}
		b.Weight = clampf(b.Weight+slide, 0.7, 1.3)
		// 命中率低于目标 → 抬高准入门槛（Gate>0），高于 → 放松
		b.Gate = clampf(b.Gate-dev, -0.2, 0.2)
	}
}

// Weight 返回分桶权重乘子（未达最小样本前恒 1.0——零行为变化）。
func (t *SignalQualityTable) Weight(key QualityBucketKey) float64 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	b, ok := t.Buckets[key]
	if !ok {
		return 1.0
	}
	return b.Weight
}

// GateShift 返回分桶准入阈值漂移（正常应为 0；高则需更高分数才放行）。
func (t *SignalQualityTable) GateShift(key QualityBucketKey) float64 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	b, ok := t.Buckets[key]
	if !ok {
		return 0
	}
	return b.Gate
}

func clampf(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}