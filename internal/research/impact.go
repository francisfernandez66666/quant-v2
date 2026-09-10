// impact.go — 新闻影响率模型（§SIGNAL_EDGE_ENHANCEMENT_PLAN P2.1）。
// 统计"新闻类型×情绪相位×方向"发布后 5/30/60 分钟的板块/个股超额收益，形成经验影响表；
// 新新闻按历史中位修正置信度（乘子 0.7~1.3），并校准 §P1.1 的半衰期。
// 自包含类型（不 import newsagent），避免研究层与新闻层循环依赖。
// 开关：引擎侧 Enhance.NewsImpact 门控；MinSample 未达标前全部回退默认（不动分数）。
// English: news impact-rate model (P2.1). Aggregates post-event 5/30/60min excess returns by
// news-type × emotion-phase × direction into an empirical impact table; new events adjust their
// confidence by the historical median (0.7~1.3 multiplier) and calibrate §P1.1 half-lives.
// Self-contained types (no newsagent import) to avoid a research↔news agent cycle. Gated by the
// engine's Enhance.NewsImpact; below MinSample everything falls back to defaults (no score change).

package research

import (
	"sort"
	"strings"
	"sync"
	"time"
)

// ImpactKey 新闻影响分桶键：type | phase | direction。
// English: impact bucket key: type | phase | direction.
type ImpactKey string

// MakeImpactKey 组装分桶键（方向归一为大写）。
func MakeImpactKey(evType, phase, direction string) ImpactKey {
	return ImpactKey(strings.ToUpper(strings.TrimSpace(evType)) + "|" +
		strings.TrimSpace(phase) + "|" + strings.ToUpper(strings.TrimSpace(direction)))
}

// ImpactBucket 单一维度桶的经验分布统计。
type ImpactBucket struct {
	// 分钟窗口
	Excess5  []float64 // 5 分钟超额收益样本
	Excess30 []float64 // 30 分钟
	Excess60 []float64 // 60 分钟
	// HalfLifeSamples 观测到的衰减半衰期样本（分钟），用于校准 §P1.1。
	HalfLifeSamples []float64
}

// medium 中位数（空返回 0）。
func medium(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := make([]float64, len(xs))
	copy(s, xs)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// medianFor 返回某窗口期的中位超额。
func (b *ImpactBucket) medianFor(win string) float64 {
	switch win {
	case "5":
		return medium(b.Excess5)
	case "30":
		return medium(b.Excess30)
	case "60":
		return medium(b.Excess60)
	}
	return 0
}

// ImpactTable 影响率查表（线程安全）。
// minSample 达标（默认 30）前，Confidence 恒 1.0、HalfLife 回退——零行为变化。
type ImpactTable struct {
	mu        sync.RWMutex
	Buckets   map[ImpactKey]*ImpactBucket
	MinSample int
}

// NewImpactTable 创建影响表。minSample<=0 时回退 30。
func NewImpactTable(minSample int) *ImpactTable {
	if minSample <= 0 {
		minSample = 30
	}
	return &ImpactTable{Buckets: make(map[ImpactKey]*ImpactBucket), MinSample: minSample}
}

func (t *ImpactTable) bucket(k ImpactKey) *ImpactBucket {
	b, ok := t.Buckets[k]
	if !ok {
		b = &ImpactBucket{}
		t.Buckets[k] = b
	}
	return b
}

// Update 记录一批样本：同桶超额（各窗口可能缺失，逐项 append 即可）。
// halfLifeObs 为观测到的衰减半衰期（分钟），0 表示本样本不参与校准。
// English: records a batch of excess samples into the bucket (each window may be missing and is
// appended independently). halfLifeObs is the observed decay half-life in minutes; 0 skips it.
func (t *ImpactTable) Update(evType, phase, direction string, excess5, excess30, excess60 []float64, halfLifeObs float64) {
	k := MakeImpactKey(evType, phase, direction)
	t.mu.Lock()
	defer t.mu.Unlock()
	b := t.bucket(k)
	b.Excess5 = append(b.Excess5, excess5...)
	b.Excess30 = append(b.Excess30, excess30...)
	b.Excess60 = append(b.Excess60, excess60...)
	if halfLifeObs > 0 {
		b.HalfLifeSamples = append(b.HalfLifeSamples, halfLifeObs)
	}
}

// sampleCount 桶内样本数（以 5 分钟窗计，保守取各窗最大值不同窗口可缺失）。
func (b *ImpactBucket) sampleCount() int {
	n := len(b.Excess30)
	if len(b.Excess60) > n {
		n = len(b.Excess60)
	}
	return n
}

// Confidence 历史中位驱动的新新闻置信度乘子 [0.7,1.3]。
// 30 分钟窗中位超额 med 映射：med>0 上修、med<0 下修；样本不足返回 1.0（不动）。
// English: confidence multiplier [0.7,1.3] driven by the historical median 30-min excess.
// Positive median boosts confidence, negative trims it; below MinSample returns 1.0 (no change).
func (t *ImpactTable) Confidence(evType, phase, direction string) float64 {
	k := MakeImpactKey(evType, phase, direction)
	t.mu.RLock()
	defer t.mu.RUnlock()
	b, ok := t.Buckets[k]
	_ = ok
	if !ok || b.sampleCount() < t.MinSample {
		return 1.0
	}
	med := b.medianFor("30")
	mul := 1.0
	if med > 0.02 {
		mul = 1.0 + (med-0.02)*4 // 0.02→1.0，0.10→1.32 封顶
	} else if med < -0.02 {
		mul = 1.0 + (med+0.02)*4 // -0.02→1.0，-0.10→0.68 下限
	}
	if mul > 1.3 {
		return 1.3
	}
	if mul < 0.7 {
		return 0.7
	}
	return mul
}

// clampHalfLet 裁剪半衰期校准值到 [15,300] 分钟避免异常。
func clampHalfLife(v float64) time.Duration {
	if v < 15 {
		return 15 * time.Minute
	}
	if v > 300 {
		return 300 * time.Minute
	}
	return time.Duration(v) * time.Minute
}

// HalfLife 返回桶校准过的半衰期；样本不足或无法得出时回退 fallback。
// English: returns the calibrated half-life for a bucket; falls back when sample is insufficient.
func (t *ImpactTable) HalfLife(evType string, fallback time.Duration) time.Duration {
	// 不依赖方向/相位：对含该类型的全部桶半衰期样本取中位（方向维度样本常不足）。
	var all []float64
	t.mu.RLock()
	for k, b := range t.Buckets {
		if strings.HasPrefix(string(k), strings.ToUpper(strings.TrimSpace(evType))+"|") {
			if b.sampleCount() >= t.MinSample && len(b.HalfLifeSamples) > 0 {
				all = append(all, b.HalfLifeSamples...)
			}
		}
	}
	t.mu.RUnlock()
	if len(all) == 0 {
		return fallback
	}
	return clampHalfLife(medium(all))
}

// Merge 合并另一张表（调度重启后从磁盘加载并合并）。English: merges another table (loaded from disk after restart).
func (t *ImpactTable) Merge(o *ImpactTable) {
	if o == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for k, ob := range o.Buckets {
		dst := t.bucket(k)
		dst.Excess5 = append(dst.Excess5, ob.Excess5...)
		dst.Excess30 = append(dst.Excess30, ob.Excess30...)
		dst.Excess60 = append(dst.Excess60, ob.Excess60...)
		dst.HalfLifeSamples = append(dst.HalfLifeSamples, ob.HalfLifeSamples...)
	}
}
