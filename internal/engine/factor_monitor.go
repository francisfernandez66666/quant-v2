// 因子战法效果监测（战法库）：跟踪已应用因子战法触发信号的 5 日前向收益，评估其实际效果。
// 触发时记录 (ruleID, code, 触发价, 触发日)；达到前瞻天数（Horizon）后用最新行情价算收益，
// 计入该规则 Win/Loss/CumReturn，并把统计回写战法库 applied_factors.json（前端"效果"栏展示）。
// English: factor-strategy effectiveness monitor (strategy library) — tracks the Horizon-day forward
// return of signals fired by applied factor strategies to assess their real effect. On trigger it
// records (ruleID, code, entry price, fire day); when the forward window matures it computes the return
// against fresh quotes, tallies Win/Loss/CumReturn for that rule, and writes the stats back to the
// library file (shown in the frontend "效果" column).
package engine

import (
	"log"
	"sync"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/research"
)

// factorMonitorEntry 一条待结算的因子信号跟踪。
// 记录信号触发时的关键信息（规则ID、股票代码、触发价、触发时间），
// 用于在前瞻窗口到期后计算前向收益，评估该因子规则的实际效果。
// English: one pending factor-signal tracking entry awaiting settlement.
type factorMonitorEntry struct {
	RuleID     string    // 战法库规则 ID（fac_<candID>）
	Code       string    // 股票代码
	EntryPrice float64   // 触发价（结算基准）
	FiredAt    time.Time // 触发时间
}

// factorMonitor 因子战法效果监测器。
// 维护一个待结算的信号跟踪队列，定期结算到期条目并更新规则统计。
// 支持因子战法（fac_ 前缀）和形态战法（pat_ 前缀）两类规则的效果监测。
// English: factor-strategy effectiveness monitor.
type factorMonitor struct {
	mu      sync.Mutex                       // 保护 entries 并发访问的互斥锁
	dataDir string                           // 战法库文件所在目录
	entries map[string]*factorMonitorEntry   // 待结算跟踪条目（key = ruleID|code，去重）
	horizon int                              // 前瞻天数（默认 5 日）
}

// newFactorMonitor 创建监测器。horizon 为前瞻天数（默认 5）。
// English: creates a monitor; horizon is the forward days (default 5).
func newFactorMonitor(dataDir string, horizon int) *factorMonitor {
	if horizon <= 0 {
		horizon = 5
	}
	return &factorMonitor{dataDir: dataDir, entries: make(map[string]*factorMonitorEntry), horizon: horizon}
}

// Observe 观察一轮信号：为战法库信号（因子 fac_ 或形态 pat_ 的 StrategyID）登记跟踪。
// English: observes a round of signals, registering tracking for library signals (factor fac_ / pattern pat_).
func (m *factorMonitor) Observe(signals []combat_agent.Signal) {
	if m == nil {
		return
	}
	m.mu.Lock()
	for _, s := range signals {
		if s.StrategyID == "" || len(s.StrategyID) < 4 {
			continue
		}
		pre := s.StrategyID[:4]
		if pre != "fac_" && pre != "pat_" {
			continue
		}
		if s.Price <= 0 || (s.Action != "买入" && s.Action != "buy") {
			continue
		}
		key := s.StrategyID + "|" + s.Code
		if _, ok := m.entries[key]; ok {
			continue // 已跟踪
		}
		m.entries[key] = &factorMonitorEntry{
			RuleID: s.StrategyID, Code: s.Code, EntryPrice: s.Price,
			FiredAt: s.GeneratedAt,
		}
	}
	m.mu.Unlock()
}

// Settle 结算已到期的跟踪：对每个达到前瞻天数的条目，用行情最新价算收益并记录到对应规则。
// 返回本批结算条数（日志用）。English: settles matured tracking entries by computing their forward
// return against fresh quotes and recording it to the owning rule; returns the count settled.
func (m *factorMonitor) Settle(market *data.MarketAPI, record func(ruleID string, ret float64)) int {
	if m == nil || market == nil {
		return 0
	}
	m.mu.Lock()
	var due []*factorMonitorEntry
	for key, e := range m.entries {
		if m.matured(e.FiredAt) {
			due = append(due, e)
			delete(m.entries, key)
		}
	}
	m.mu.Unlock()

	settled := 0
	for _, e := range due {
		ret := m.forwardReturn(market, e)
		if record != nil {
			record(e.RuleID, ret)
		}
		settled++
		log.Printf("[factor-monitor] 结算 %s %s 触发价%.2f 前向收益=%+.4f", e.RuleID, e.Code, e.EntryPrice, ret)
	}
	return settled
}

// PersistStats 把当前运行统计回写战法库文件（效果监测持久化，供前端展示）。
// English: writes current run stats back to the library file (effectiveness persistence for the UI).
func (m *factorMonitor) PersistStats(stats map[string]factorRuleStat) {
	if m == nil || m.dataDir == "" {
		return
	}
	for id, st := range stats {
		_ = research.UpdateAppliedFactorStats(m.dataDir, id, st.SignalCount, st.Win, st.Loss, st.CumReturn)
	}
}

// PersistPatternStats 把形态战法库运行统计回写 applied_patterns.json（效果监测持久化）。
// English: writes pattern-library run stats back to applied_patterns.json (effectiveness persistence).
func (m *factorMonitor) PersistPatternStats(stats map[string]factorRuleStat) {
	if m == nil || m.dataDir == "" {
		return
	}
	for id, st := range stats {
		_ = research.UpdateAppliedPatternStats(m.dataDir, id, st.SignalCount, st.Win, st.Loss, st.CumReturn)
	}
}

// matured 判断是否已超过前瞻天数（按自然日粗略近似；交易日精确追踪留待后续增强）。
// English: whether the forward window has elapsed (natural-day approximation; exact trading-day
// settlement is left for future enhancement).
func (m *factorMonitor) matured(fired time.Time) bool {
	return time.Since(fired) >= time.Duration(m.horizon*24)*time.Hour
}

// forwardReturn 用行情最新价算前向收益（相对触发价）。
// English: computes forward return against the latest quote (vs entry price).
func (m *factorMonitor) forwardReturn(market *data.MarketAPI, e *factorMonitorEntry) float64 {
	if e.EntryPrice <= 0 {
		return 0
	}
	si, err := market.GetRealtimeQuote(e.Code)
	if err != nil || si == nil || si.Price <= 0 {
		return 0
	}
	return (si.Price - e.EntryPrice) / e.EntryPrice
}

// factorRuleStat 一条规则的运行统计聚合（供 PersistStats）。
// 包含信号触发次数、胜率、累计收益等核心指标，用于前端"效果"栏展示。
// English: aggregated run stats of one rule (for PersistStats).
type factorRuleStat struct {
	SignalCount int     // 信号触发总次数
	Win         int     // 盈利次数（前向收益>0）
	Loss        int     // 亏损次数（前向收益≤0）
	CumReturn   float64 // 累计前向收益（所有已结算信号的收益之和）
}

// observeSettleFactor 引擎层：登记本轮战法库信号（因子/形态）跟踪 + 结算到期跟踪，并把统计回写战法库。
// 由 scoreCycle 调用。English: engine-level — registers this round's library signals (factor/pattern),
// settles matured trackings, then writes stats back to the library. Called from scoreCycle.
func (e *Engine) observeSettleFactor(sigs []combat_agent.Signal) {
	if e.factorMon == nil {
		return
	}
	e.factorMon.Observe(sigs)
	settled := e.factorMon.Settle(e.marketAPI, func(ruleID string, ret float64) {
		if isPatternID(ruleID) {
			e.RecordPatternForwardReturn(ruleID, ret)
		} else {
			e.RecordFactorForwardReturn(ruleID, ret)
		}
	})
	if settled > 0 {
		// 结算后把最新统计回写战法库（前端效果监测持久化）——因子与形态两库
		e.factorMon.PersistStats(e.factorStatsMap())
		e.factorMon.PersistPatternStats(e.patternStatsMap())
	}
}

// isPatternID 判断战法库规则 ID 是否为形态战法（pat_ 前缀）。
// English: reports whether a library rule ID is a pattern strategy (pat_ prefix).
func isPatternID(id string) bool {
	return len(id) >= 4 && id[:4] == "pat_"
}

// factorStatsMap 汇总因子 runner 各规则运行统计为 map（供回写战法库）。
// English: aggregates per-rule run stats into a map (for library write-back).
func (e *Engine) factorStatsMap() map[string]factorRuleStat {
	out := map[string]factorRuleStat{}
	for _, r := range e.FactorStats() {
		out[r.ID] = factorRuleStat{
			SignalCount: r.SignalCount, Win: r.Win, Loss: r.Loss, CumReturn: r.CumReturn,
		}
	}
	return out
}

// patternStatsMap 汇总形态 runner 各规则运行统计为 map（供回写战法库）。
// English: aggregates per-rule run stats of the pattern runner into a map (for library write-back).
func (e *Engine) patternStatsMap() map[string]factorRuleStat {
	out := map[string]factorRuleStat{}
	for _, r := range e.PatternStats() {
		out[r.ID] = factorRuleStat{
			SignalCount: r.SignalCount, Win: r.Win, Loss: r.Loss, CumReturn: r.CumReturn,
		}
	}
	return out
}
