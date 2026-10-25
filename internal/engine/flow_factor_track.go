// flow_factor_track.go — §FLOW-FACTOR 净流入 dynamic 因子的引擎侧解析/挂载层
// （2026-10-25，docs/MODPLAN_UAT_FLOWFACTOR_20261025.md §2.3~§2.5 + R3/R8/R9）。
//
// 职责（与 signal_quality_track.go 同构的"读侧接线"）：
//
//	① annotateFlowFactors：买入信号准入前，把两个净流入率挂到 Signal.FlowRatioToday /
//	   FlowRatio5D（signalctl 的 flow5DFactor/flowTodayFactor 消费；0=弃权）；
//	② judgeSellPositions：卖出裁决前，把同样两个比值挂到 SellInput（flowSellWeight 消费，
//	   净流出 → 确认窗时长×权重）。
//
// 取数纪律：
//   - 当日比值只用**本轮已有的行情快照**（StockInfo.NetInflow + FloatMarketCapOf），零新增上游；
//   - 5 日历史只有东财日线一个源（GetStockMoneyFlowHistory），按 (code, 交易日) 缓存
//     （成功 TTL 10min / 失败 TTL 60s 负缓存）——绝不逐信号每轮硬打（§SIGBATCH 教训）；
//   - R3 日期断言：历史序列**末行必须等于本交易日**，陈旧序列整体弃权（不拿昨天的累计
//     冒充今天的读数）；
//   - R9 弃权率 opslog：每轮打印命中/弃权计数与原因分布，数据源退化时值班可一眼看见。
//
// 缺数纪律（§FIX-9e 同款）：任何一环不可得 → 该维比值保持 0（弃权），绝不编造。
// English: resolves the two net-inflow ratios onto buy signals and sell inputs. Same-day uses
// only the round's existing quotes; the 5-day history is cached per (code, trading day); stale
// series abstain; abstain rates are logged every round (R9).
package engine

import (
	"log"
	"math"
	"strings"
	"sync"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/data"
)

// flowHistTTL 5 日历史取数缓存 TTL（成功）。盘中序列只会追加当日行，10 分钟新鲜度对
// "近 5 日累计"这个慢变量足够；跨交易日自动失效（缓存键带交易日）。
// English: success-cache TTL for the 5-day history (10 min, keyed by trading day).
const flowHistTTL = 10 * time.Minute

// flowHistFailTTL 失败负缓存 TTL：上游抖动/限速时防每轮硬打（负缓存同样按日失效）。
// English: negative-cache TTL so a flaky upstream is not hammered every round.
const flowHistFailTTL = 60 * time.Second

// flowHistDays 5 日窗口行数（含当日）。当日被"5 日主因子"与"当日辅因子"两个维度共享，
// 辅因子档位幅度减半正是对这份重叠的定价（§2.4），故这里含当日取 5 行。
// English: 5 rows including today — the same-day factor's halved bands price in this overlap.
const flowHistDays = 5

// flowHistMinRows 5 日维度的最少行数：至少要有"当日 + 前 1 日"两行。只有 1 行时累计值
// 恒等于当日值，两个维度会变成同一份数据的双倍权重——新股票/断档按弃权处理。
// English: require today + at least one prior day; a single row would double-count today.
const flowHistMinRows = 2

// flowHistMaxEntries 历史缓存条数上限（超限不缓存、直接取数——防长跑进程无界增长）。
// English: cap on cached codes; beyond it entries are simply not cached.
const flowHistMaxEntries = 4000

// flowHistEntry 单码 5 日历史缓存（day=交易日 YYYYMMDD，跨日必重取）。
// English: per-code history cache entry (trading-day keyed).
type flowHistEntry struct {
	day string
	sum float64
	ok  bool
	why string // 失败原因（ok=false 时非空；R9 弃权率归因用）
	at  time.Time
}

// flowFactorTracker 净流入率解析器（线程安全；每引擎一份，懒建）。
// English: the net-inflow ratio resolver (thread-safe, one per engine).
type flowFactorTracker struct {
	mu   sync.Mutex
	hist map[string]*flowHistEntry
}

// newFlowFactorTracker 建解析器。
func newFlowFactorTracker() *flowFactorTracker {
	return &flowFactorTracker{hist: make(map[string]*flowHistEntry)}
}

// fiveDaySum 带缓存的近 5 日主力净流入累计（元）。why 非空 = 弃权原因。
//
// 缓存命中条件：同交易日 + TTL 内（成功 10min / 失败 60s）。未命中时调 fetch 取数，
// 依次过三道闸：取数失败 → 末行日期≠本交易日（R3 陈旧断言）→ 行数不足。
// English: cached 5-day main-force net sum; abstains (with a reason) on fetch failure,
// a stale last row (R3), or too few rows.
func (t *flowFactorTracker) fiveDaySum(code, day string, fetch func() ([]data.FlowDay, error), now time.Time) (float64, bool, string) {
	if t == nil {
		return 0, false, "no_tracker"
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if e := t.hist[code]; e != nil && e.day == day && now.Sub(e.at) < flowCacheTTL(e.ok) {
		return e.sum, e.ok, e.why
	}
	sum, ok, why := 0.0, false, ""
	if fetch == nil {
		why = "no_source" // 无协调器（未注入行情源）→ 明确弃权原因，不是静默空串
	} else {
		days, err := fetch()
		switch {
		case err != nil:
			why = "hist_fail"
		case !flowHistFresh(days, day):
			why = "hist_stale" // R3：末行不是本交易日 → 整体弃权
		case len(days) < flowHistMinRows:
			why = "hist_short"
		default:
			sum, ok, why = data.SumMainNet(days), true, ""
		}
	}
	if t.hist == nil {
		t.hist = make(map[string]*flowHistEntry)
	}
	if len(t.hist) < flowHistMaxEntries {
		t.hist[code] = &flowHistEntry{day: day, sum: sum, ok: ok, why: why, at: now}
	}
	return sum, ok, why
}

// flowCacheTTL 成功/失败两种 TTL。
func flowCacheTTL(ok bool) time.Duration {
	if ok {
		return flowHistTTL
	}
	return flowHistFailTTL
}

// flowHistFresh R3 日期断言：序列非空且**末行日期**（最新一行）等于本交易日。
// 末行可能带 "-"（上游 "2026-10-16" 形态）——先归一化再比。
// English: the newest row must carry today's trading date (normalized).
func flowHistFresh(days []data.FlowDay, today string) bool {
	if len(days) == 0 || today == "" {
		return false
	}
	return normFlowDate(days[len(days)-1].Date) == today
}

// normFlowDate "2026-10-16" → "20261016"（其余原样 trim）。
func normFlowDate(s string) string {
	return strings.ReplaceAll(strings.TrimSpace(s), "-", "")
}

// flowTodayRatio 当日主力净流入率（%）= 实时主力净流入 / 流通市值 × 100。
//
// 只用本轮已有快照，零新增上游调用。弃权情形：无报价 / 行情无价格 / 源未返回资金流
// （HasFlow=false，§FIX-9e：判"有无"绝不靠零值）/ 流通市值两路都不可得。
// 注意 HasFlow=true 且净流入恰为 0 是合法读数 → 比值 0 → 调用方按"压线中性"处理
// （flowBandOf(0) 归弃权，等效中性，无偏）。
// English: same-day ratio from the round's quote only; abstains on any missing input.
func flowTodayRatio(q *data.StockInfo) (float64, string) {
	if q == nil || q.Price <= 0 {
		return 0, "no_quote"
	}
	if !q.HasFlow {
		return 0, "no_flow_today"
	}
	mcap, ok := data.FloatMarketCapOf(q)
	if !ok {
		return 0, "no_floatcap"
	}
	r := q.NetInflow / mcap * 100
	if math.IsNaN(r) || math.IsInf(r, 0) {
		return 0, "bad_ratio"
	}
	return r, ""
}

// flowRatiosResolve 单码两个比值 + 两个弃权原因（买入/卖出两条挂载路径共用，口径逐字一致）。
// whyT/whyF 空串 = 该维命中；fiveD 仅在 5 日累计成功**且**分母（流通市值）可得时非零。
// English: shared resolver for both ratios plus abstain reasons.
func (e *Engine) flowRatiosResolve(code string, q *data.StockInfo, tr *flowFactorTracker, day string, now time.Time) (today, fiveD float64, whyT, whyF string) {
	today, whyT = flowTodayRatio(q)
	sum, ok, whyF := tr.fiveDaySum(pureTsCode(code), day, e.flowHistFetch(code), now)
	if !ok {
		return today, 0, whyT, whyF
	}
	mcap, mok := data.FloatMarketCapOf(q)
	if !mok || mcap <= 0 {
		return today, 0, whyT, "no_floatcap"
	}
	r := sum / mcap * 100
	if math.IsNaN(r) || math.IsInf(r, 0) {
		return today, 0, whyT, "bad_ratio"
	}
	return today, r, whyT, ""
}

// flowRatiosFor 卖出裁决路径的单码比值解析（薄封装，供 judgeSellPositions 调用）。
// English: thin wrapper for the sell-judge path.
func (e *Engine) flowRatiosFor(code string, q *data.StockInfo, now time.Time) (today, fiveD float64) {
	today, fiveD, _, _ = e.flowRatiosResolve(code, q, e.flowTrackerRef(), data.TradingDayDate(now), now)
	return today, fiveD
}

// flowHistFetch 5 日历史取数闭包（nil 协调器 → nil，调用方按弃权处理）。
// English: builds the history fetch closure; a nil coordinator yields nil (abstain).
func (e *Engine) flowHistFetch(code string) func() ([]data.FlowDay, error) {
	e.mu.RLock()
	dc := e.coordinator
	e.mu.RUnlock()
	if dc == nil {
		return nil
	}
	pure := pureTsCode(code)
	return func() ([]data.FlowDay, error) {
		return dc.GetStockMoneyFlowHistory(pure, flowHistDays)
	}
}

// flowTrackerRef 惰性建解析器（每引擎一份）。
// English: lazily creates the per-engine ratio resolver.
func (e *Engine) flowTrackerRef() *flowFactorTracker {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.flowTrack == nil {
		e.flowTrack = newFlowFactorTracker()
	}
	return e.flowTrack
}

// flowFactorGateOn 任一侧开着就要挂载（买入侧关+卖出侧关 → 零操作零取数）。
// English: annotate when either side is on; both off is a complete no-op.
func (e *Engine) flowFactorGateOn() bool {
	sc := e.sizingConfigFor(e.primaryMember())
	return sc.FlowFactorOn() || sc.FlowSellOn()
}

// annotateFlowFactors §FLOW-FACTOR 挂载：把两个净流入率（%）解析到每条**买入**信号。
//
// 放在准入之前（与 annotateQualityWeights 同位置）——signalctl 的 flow5DFactor/
// flowTodayFactor 读的就是这两个字段。卖出侧信号不在此挂载（卖出裁决路径单独挂，
// 见 judgeSellPositions），避免为不消费的取数付上游成本。
//
// R9 弃权率 opslog：有信号且有弃权时打印本轮命中/弃权计数与原因分布。
// English: resolves both ratios onto each buy signal before admission; logs abstain rates (R9).
func (e *Engine) annotateFlowFactors(sigs []combat_agent.Signal, quotes map[string]*data.StockInfo) {
	if len(sigs) == 0 || !e.flowFactorGateOn() {
		return
	}
	tr := e.flowTrackerRef()
	now := time.Now()
	day := data.TradingDayDate(now)
	var nBuy, nToday, nFive int
	whyToday := map[string]int{}
	whyFive := map[string]int{}
	for i := range sigs {
		if !isQualityBuy(sigs[i]) {
			continue // 只挂买入（卖出路径另挂）；非买入不付取数成本
		}
		nBuy++
		code := pureTsCode(sigs[i].Code)
		today, fiveD, whyT, whyF := e.flowRatiosResolve(code, quoteFor(quotes, code), tr, day, now)
		sigs[i].FlowRatioToday, sigs[i].FlowRatio5D = today, fiveD
		if whyT == "" {
			nToday++
		} else {
			whyToday[whyT]++
		}
		if whyF == "" {
			nFive++
		} else {
			whyFive[whyF]++
		}
	}
	// R9：有弃权才打点（全命中时静默，防刷屏）；原因分布让"数据源退化"与"个股断档"可区分。
	if nBuy > 0 && (len(whyToday) > 0 || len(whyFive) > 0) {
		log.Printf("[flowfactor] 本轮 %d 条买入：当日命中 %d 弃权 %v；5日命中 %d 弃权 %v",
			nBuy, nToday, whyToday, nFive, whyFive)
	}
}
