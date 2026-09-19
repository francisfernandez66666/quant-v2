// lifecycle_run.go §GAP-P1 20260915：EvaluateDemote 夜间链落地——把「已应用战法的逐日滚动
// 归因指标」从 paper.json 规则池成交聚合出来喂给 EvaluateDemote，连续衰退 → 自动禁用该战法
// （applied_factors/applied_patterns 的 Enabled=false，注入器即时生效），留审计日志。
// English: nightly wiring of EvaluateDemote — aggregates per-rule daily rolling stats from the
// paper pools, runs the decline evaluator, and auto-disables demoted strategies in the library.
package research

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"
)

// paperTradeLite 解析 paper.json 所需的最小成交结构（买卖配对算单笔收益）。
// 池归属优先 strategy_type（撮合落库字段，规则池 key 即 fac_<id>/pat_<id>），回落旧 pool_key。
type paperTradeLite struct {
	Code         string  `json:"code"`
	StrategyType string  `json:"strategy_type,omitempty"`
	PoolKey      string  `json:"pool_key,omitempty"`
	Side         string  `json:"side"`
	Price        float64 `json:"price"`
	Qty          int     `json:"qty"`
	Time         string  `json:"time"`
}

// poolKey 返回该成交的规则池 key（优先 strategy_type），无池归属返回空。
func (t paperTradeLite) poolKey() string {
	if t.StrategyType != "" {
		return t.StrategyType
	}
	return t.PoolKey
}

// paperDayKey 成交时间 → 交易日 key（YYYY-MM-DD）：RFC3339（time.Time 序列化）取前 10 位，
// 兼容 "20060102" 紧凑形态。
func paperDayKey(t string) string {
	if len(t) >= 10 && t[4] == '-' {
		return t[:10]
	}
	if len(t) >= 8 {
		if _, err := time.Parse("20060102", t[:8]); err == nil {
			return t[:4] + "-" + t[4:6] + "-" + t[6:8]
		}
	}
	return t
}

// PoolDailyStats 从 paper.json 聚合各规则池的逐日滚动指标（日期升序）：
// 卖出成交按 (pool, code) 匹配该池最近一笔买入价 → 单笔净收益%；当日 Trades=卖出笔数、
// WinRate=当日胜率、IR=截至当日的日频收益序列年化 IR（滚动口径，样本 <2 为 0）。
// 池无成交则该 key 缺席（调用方按「无观测」处理）。
// English: aggregates per-pool daily rolling stats from paper.json (ascending dates); IR is the
// rolling annualized IR of the pool's daily mean-return series up to each day.
func PoolDailyStats(paperPath string) map[string][]DailyStat {
	b, err := os.ReadFile(paperPath)
	if err != nil {
		return map[string][]DailyStat{}
	}
	var state struct {
		Trades []paperTradeLite `json:"trades"`
	}
	if err := json.Unmarshal(b, &state); err != nil {
		return map[string][]DailyStat{}
	}
	type dayAgg struct {
		rets []float64
	}
	lastBuy := map[string]map[string]float64{}
	perPool := map[string]map[string]*dayAgg{}
	dayOrder := map[string][]string{}
	for _, t := range state.Trades {
		pool := t.poolKey()
		if pool == "" {
			continue // 无池归属（手动/旧记录）不参与规则级评估
		}
		if t.Side == "buy" && t.Qty > 0 {
			if lastBuy[pool] == nil {
				lastBuy[pool] = map[string]float64{}
			}
			lastBuy[pool][t.Code] = t.Price
			continue
		}
		if t.Side != "sell" {
			continue
		}
		cost := lastBuy[pool][t.Code]
		if cost <= 0 || t.Price <= 0 {
			continue
		}
		day := paperDayKey(t.Time)
		if day == "" {
			continue
		}
		if perPool[pool] == nil {
			perPool[pool] = map[string]*dayAgg{}
		}
		d := perPool[pool][day]
		if d == nil {
			d = &dayAgg{}
			perPool[pool][day] = d
			dayOrder[pool] = append(dayOrder[pool], day)
		}
		pnl := (t.Price - cost) / cost * 100
		d.rets = append(d.rets, pnl)
	}
	out := map[string][]DailyStat{}
	for pool, days := range perPool {
		order := append([]string(nil), dayOrder[pool]...)
		sort.Strings(order)
		series := make([]float64, 0, len(order))
		stats := make([]DailyStat, 0, len(order))
		for _, day := range order {
			d := days[day]
			mean := 0.0
			wins := 0
			for _, r := range d.rets {
				mean += r
				if r > 0 {
					wins++
				}
			}
			mean /= float64(len(d.rets))
			series = append(series, mean)
			stats = append(stats, DailyStat{
				IR:      irAnnualized(series),
				WinRate: float64(wins) / float64(len(d.rets)) * 100,
				Trades:  len(d.rets),
			})
		}
		out[pool] = stats
	}
	return out
}

// DemoteAction 一条降级执行记录（verdict + 是否已实际禁用）。
type DemoteAction struct {
	Verdict  DemoteVerdict `json:"verdict"`
	Kind     string        `json:"kind"` // factor | pattern
	Disabled bool          `json:"disabled"`
	// §RFIX-3 反静默：已启用但零成交观测的战法也记入 actions（verdict=keep），
	// DaysSinceApply=自上线天数；ZeroObs=零观测已超过告警阈值天数（调用方据此推告警）。
	DaysSinceApply int  `json:"days_since_apply,omitempty"`
	ZeroObs        bool `json:"zero_obs,omitempty"`
}

// DemoteAppliedRules 衰退降级主流程：遍历战法库**启用中**的因子/形态规则，按规则池
// （ID 即 pool_key）逐日滚动指标跑 EvaluateDemote；判 disable 时置 Enabled=false
// （dryRun 仅报告不落库）。无观测数据的规则保持原状（keep，不误杀）。
// paperPath 为空时回落 dataDir/paper.json。
// English: evaluates every enabled applied rule against its pool's daily stats and disables
// declining ones (dryRun = report only); rules without observations stay untouched.
func DemoteAppliedRules(dataDir, paperPath string, opts DemoteOpts, dryRun bool) ([]DemoteAction, error) {
	// §RFIX-3：先补默认（EvaluateDemote 的 fill 作用在它自己的值副本上，
	// 零观测分支要在本函数内读到 ZeroObsDays 默认值）。
	opts.fill()
	if paperPath == "" {
		paperPath = filepath.Join(dataDir, "paper.json")
	}
	stats := PoolDailyStats(paperPath)
	factors, err := ListAppliedFactorRules(dataDir)
	if err != nil {
		return nil, err
	}
	patterns, err := ListAppliedPatternRules(dataDir)
	if err != nil {
		return nil, err
	}
	var actions []DemoteAction
	evaluate := func(id, kind, appliedAt string) {
		if _, ok := stats[id]; !ok {
			// §RFIX-3 反静默：无观测 = 不判定（保守，新上/无成交战法不能被静默禁用），
			// 但必须**留痕**——旧实现直接 return，lifecycle 输出"无已启用战法需要衰退评估"
			// 的误导空话，fac_1 阈值失校准导致数月零信号无人察觉（生产 #265 实录）。
			// 现记入 actions（keep + 上线天数），连续零观测 ≥ZeroObsDays 置 ZeroObs 供告警。
			days := daysSince(appliedAt)
			a := DemoteAction{
				Verdict:        DemoteVerdict{RuleID: id, Verdict: "keep", Reason: fmt.Sprintf("上线%d日零成交观测，不判定衰退（请核查阈值/池注入是否失校准）", days)},
				Kind:           kind,
				DaysSinceApply: days,
			}
			if opts.ZeroObsDays > 0 && days >= opts.ZeroObsDays {
				a.ZeroObs = true
			}
			actions = append(actions, a)
			return
		}
		v := EvaluateDemote(stats[id], opts)
		v.RuleID = id
		a := DemoteAction{Verdict: v, Kind: kind}
		if v.Verdict == "disable" && !dryRun {
			var err error
			if kind == "factor" {
				err = SetAppliedFactorEnabled(dataDir, id, false)
			} else {
				err = SetAppliedPatternEnabled(dataDir, id, false)
			}
			if err == nil {
				a.Disabled = true
			} else {
				a.Verdict = DemoteVerdict{RuleID: id, Verdict: "keep", Reason: "降级落库失败: " + err.Error()}
			}
		}
		actions = append(actions, a)
	}
	for _, e := range factors {
		if e.Enabled {
			evaluate(e.ID, "factor", e.AppliedAt)
		}
	}
	for _, e := range patterns {
		if e.Enabled {
			evaluate(e.ID, "pattern", e.AppliedAt)
		}
	}
	return actions, nil
}

// daysSince 解析 "2006-01-02 15:04:05" 应用时间，返回距今天数（解析失败/未来时间=0）。
// English: whole days elapsed since an "applied_at" timestamp; unparsable or future = 0.
func daysSince(appliedAt string) int {
	t, err := time.ParseInLocation("2006-01-02 15:04:05", appliedAt, time.Local)
	if err != nil {
		// 兼容仅日期形态（旧数据/手工编辑）
		t, err = time.ParseInLocation("2006-01-02", appliedAt, time.Local)
		if err != nil {
			return 0
		}
	}
	d := int(time.Since(t).Hours() / 24)
	if d < 0 {
		d = 0
	}
	return d
}

// LifecycleDemoteSummary 供审计日志/任务输出的一行式降级摘要。
// English: one-line demotion summary for audit logs.
func LifecycleDemoteSummary(actions []DemoteAction) string {
	if len(actions) == 0 {
		return "无已启用战法需要衰退评估（战法库为空或全部无观测）"
	}
	disabled := 0
	zeroObs := 0
	for _, a := range actions {
		if a.Verdict.Verdict == "disable" {
			disabled++
		}
		if a.ZeroObs {
			zeroObs++
		}
	}
	s := "衰退评估 " + strconv.Itoa(len(actions)) + " 条规则，判降级 " + strconv.Itoa(disabled) + " 条"
	// §RFIX-3 反静默：零观测超阈值的战法在摘要里点名（旧输出对 fac_1 式失校准完全无声）。
	if zeroObs > 0 {
		s += "，零观测告警 " + strconv.Itoa(zeroObs) + " 条（上线超阈值天数无成交，核查阈值/池注入）"
	}
	return s
}
