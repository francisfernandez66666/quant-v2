// paper_r12.go R1/R2 模拟盘仿真级修复第二期（§PAPER_REALISM_FIX_PLAN）。
//
// 本文件实现：
//   - T+1 约束：当日买入的股票当日禁止卖出（A 股交易规则）
//   - 再入场冷却：清仓后同票冷却期内禁止再买入
//   - 绩效指标：Sharpe / 最大回撤 / Calmar 入 statsFor
//
// English: R1+R2 paper realism improvements — T+1 constraint, re-entry cooldown after close,
// and performance metrics (Sharpe ratio, max drawdown, Calmar) added to statsFor.
package paper

import (
	"math"
	"time"
)

// ── R1.5 T+1 约束 ──

// canSellToday T+1 检查：当日买入的股票当日禁止卖出（A 股规则）。
// FilledAt 为成交时间戳，与 now 同日则不可卖。
// English: T+1 rule — shares bought today cannot be sold until the next trading day.
func canSellToday(filledAt time.Time, now time.Time) bool {
	return filledAt.Format("2006-01-02") != now.Format("2006-01-02")
}

// ── R1.4 清仓后再入场冷却 ──

// reEntryCooldown 同票清仓后冷却期内的再入场拒绝。
// 冷却时间可配（config.json rules.paper.reentry_cooldown_min，默认 0=不限制）。
type reEntryTracker struct {
	lastClose map[string]time.Time // code → 最近清仓时间
}

func newReEntryTracker() *reEntryTracker {
	return &reEntryTracker{lastClose: make(map[string]time.Time)}
}

// recordClose 记录清仓事件。
func (t *reEntryTracker) recordClose(code string, now time.Time) {
	t.lastClose[code] = now
}

// canReEnter 检查是否可以再入场；cooldownMin<=0 时恒允许。
func (t *reEntryTracker) canReEnter(code string, cooldownMin int, now time.Time) bool {
	if cooldownMin <= 0 {
		return true
	}
	if last, ok := t.lastClose[code]; ok {
		return now.Sub(last).Minutes() >= float64(cooldownMin)
	}
	return true
}

// ── R2.2 绩效指标 ──

// PerfMetrics 风险调整后绩效指标。
type PerfMetrics struct {
	Sharpe      float64 `json:"sharpe"`       // 年化夏普比率（无风险利率=0）
	MaxDrawdown float64 `json:"max_drawdown"` // 最大回撤%（正数，如 15.3 表示最大回撤 15.3%）
	Calmar      float64 `json:"calmar"`       // Calmar 比率 = 年化收益 ÷ 最大回撤
	TotalReturn float64 `json:"total_return"` // 总收益率%
}

// computePerfMetrics 从净值序列计算绩效指标。
// equity 为按时间升序的净值序列（每项=账户总值）；daysPerYear 用于年化，默认 244（A 股交易日）。
// 样本 <2 返回零值——数据不足时不算指标。
func computePerfMetrics(equity []float64, daysPerYear int) PerfMetrics {
	var m PerfMetrics
	n := len(equity)
	if n < 2 {
		return m
	}
	if daysPerYear <= 0 {
		daysPerYear = 244
	}

	// 日收益率序列
	returns := make([]float64, n-1)
	for i := 1; i < n; i++ {
		if equity[i-1] > 0 {
			returns[i-1] = (equity[i] - equity[i-1]) / equity[i-1]
		}
	}

	// 总收益率
	m.TotalReturn = (equity[n-1] - equity[0]) / equity[0] * 100

	// 年化夏普比率（日收益率均值/标准差 × √年化因子）
	mean := 0.0
	for _, r := range returns {
		mean += r
	}
	mean /= float64(len(returns))
	std := 0.0
	for _, r := range returns {
		std += (r - mean) * (r - mean)
	}
	std /= float64(len(returns))
	if std > 0 {
		std = math.Sqrt(std)
		m.Sharpe = mean / std * math.Sqrt(float64(daysPerYear))
	}

	// 最大回撤：遍历历史最高点到后续最低点的最大跌幅
	peak := equity[0]
	maxDD := 0.0
	for _, v := range equity {
		if v > peak {
			peak = v
		}
		if peak > 0 {
			dd := (peak - v) / peak * 100
			if dd > maxDD {
				maxDD = dd
			}
		}
	}
	m.MaxDrawdown = maxDD

	// Calmar = 年化收益 ÷ 最大回撤
	tradingDays := float64(n) * float64(daysPerYear) / float64(daysPerYear) // n 个交易日
	if tradingDays > 0 && equity[0] > 0 && maxDD > 0 {
		totalRet := (equity[n-1] - equity[0]) / equity[0]
		annualized := totalRet * float64(daysPerYear) / tradingDays
		m.Calmar = annualized / (maxDD / 100)
	}

	return m
}
