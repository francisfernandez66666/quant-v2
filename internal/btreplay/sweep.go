// sweep.go 参数扫参优化引擎（§P2 STRATEGY_OPTIMIZE_PLAN）。
//
// 目标：回答"什么战法配什么出场参数在历史上表现最好"——跨全库战法（四大内置 +
// 库启用因子/形态规则）× 止盈回撤 × 最大持仓 × 入场门槛的网格搜索。
//
// 性能设计：触发判定与出场参数无关 → 全库 K 线一次性载入内存、逐 adapter 预计算
// 触发事件；每个参数组合只做廉价的统一出场模拟（移动止盈+超期），500 组合秒级完成。
// 统一出场引擎让跨战法排名口径一致（同入场逻辑、同出场规则，只比战法本身与参数）。
//
// English: parameter-sweep optimizer. Triggers are pre-computed once (they don't depend on
// exit params); every combo then runs a cheap uniform trailing-stop + timeout exit simulation
// over cached klines, so a few hundred combos finish in seconds with an apples-to-apples ranking.
package btreplay

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"sort"
	"strings"

	data "quant-trading-v2/internal/data"
	"quant-trading-v2/internal/store"
)

// SweepConfig 扫参模式配置；网格由系统自动推导（用户零配置，决策记录 #4）。
type SweepConfig struct {
	Objective string // "profitFactor"(默认) | "winRate" | "avgWin"；空串取默认
	TopN      int    // 输出前 N 名，默认 10
}

// 自动参数网格：止盈回撤 / 最大持仓 / 入场门槛（形态规则无连续分，跳过门槛维）。
var (
	sweepTrail = []float64{5, 8, 10, 12, 15}
	sweepHold  = []int{5, 10, 15, 20, 30}
	sweepScore = []float64{50, 60, 70, 80}
)

// sweepMinTrades 进入排名的最低触发数——几笔交易 100% 胜率的组合没有统计意义。
const sweepMinTrades = 20

// sweepTrigger 预计算的入场事件（与出场参数无关，只算一次）。
type sweepTrigger struct {
	ad      int     // adapter 序号
	code    string  // 股票代码（裸码）
	sigIdx  int     // 触发信号日下标（次日开盘入场）
	entry   float64 // 入场价 = 次日开盘
	score   float64 // 入场评分（-1=该战法无连续分，如形态区间命中）
	highest float64 // 信号日高点基准（移动止盈起点）
}

// sweepResult 单个组合的单战法汇总。
type sweepResult struct {
	Name           string
	Trail          float64 `json:"trail_pct"`
	Hold           int     `json:"hold_days"`
	MinScore       float64 `json:"min_score"`
	Count          int     `json:"trigger_count"`
	Win            int     `json:"win"`
	Loss           int     `json:"loss"`
	WinRate        float64 `json:"win_rate"`
	AvgWinPct      float64 `json:"avg_win_pct"`
	AvgLossPct     float64 `json:"avg_loss_pct"`
	ProfitFactor   float64 `json:"profit_factor"`
	AvgHold        float64 `json:"avg_hold_days"`
	ObjectiveScore float64 `json:"-"`
}

// runSweep 扫参主流程。codes 为裸码列表；industryChg 与普通回放同构。
func (o *Options) runSweep(db *store.DB, codes []string, ads []adapter,
	industryChg map[string]map[string]float64) error {

	obj := strings.ToLower(strings.TrimSpace(o.Sweep.Objective))
	if obj == "" {
		obj = "profitfactor"
	}
	objName := map[string]string{"profitfactor": "盈亏比", "winrate": "胜率", "avgwin": "平均盈利"}[obj]
	if objName == "" {
		return fmt.Errorf("未知优化目标: %s（可选 profitFactor/winRate/avgWin）", o.Sweep.Objective)
	}
	topN := o.Sweep.TopN
	if topN <= 0 {
		topN = 10
	}
	log.Printf("扫参启动：%d 战法 × 网格(止盈%d档×持仓%d档×门槛%d档) 目标=%s",
		len(ads), len(sweepTrail), len(sweepHold), len(sweepScore), objName)

	// ── 1) K 线一次性载入内存（300 只 × 数年日线 ≈ 几十 MB，cgroup 内安全）──
	klines := make(map[string][]data.KLine, len(codes))
	for _, tsCode := range codes {
		bars, err := db.RawBars(tsCode, o.Start, o.End)
		if err != nil || len(bars) < 15 {
			continue
		}
		code := strings.Split(tsCode, ".")[0]
		klines[code] = toDataKLine(bars)
	}

	// ── 2) 触发预计算：逐 adapter × 逐股 × 逐日（与 backtestStock 同口径）──
	triggers := make([][]sweepTrigger, len(ads))
	for ai, ad := range ads {
		var out []sweepTrigger
		for code, kls := range klines {
			out = append(out, o.sweepTriggersOf(ad, ai, code, kls, industryChg[code])...)
		}
		sort.Slice(out, func(i, j int) bool { return out[i].sigIdx < out[j].sigIdx })
		triggers[ai] = out
		log.Printf("触发预算 %s：%d 个入场事件", ad.Name(), len(out))
	}

	// ── 3) 组合枚举 + 廉价模拟 ──
	type combo struct {
		trail float64
		hold  int
		score float64
	}
	// 门槛维度仅对"有连续入场分"的战法展开（触发事件里出现过 score>=0）；
	// 形态区间命中类无连续分 → 只跑默认门槛 0，避免无意义重复组合。
	adScored := make([]bool, len(ads))
	for ai := range ads {
		for _, t := range triggers[ai] {
			if t.score >= 0 {
				adScored[ai] = true
				break
			}
		}
	}
	perAdCombos := make([][]combo, len(ads))
	total := 0
	for ai := range ads {
		scores := []float64{0}
		if adScored[ai] {
			scores = sweepScore
		}
		for _, t := range sweepTrail {
			for _, h := range sweepHold {
				for _, s := range scores {
					perAdCombos[ai] = append(perAdCombos[ai], combo{t, h, s})
				}
			}
		}
		total += len(perAdCombos[ai])
	}
	var all []sweepResult
	done := 0
	lastPct := -10
	for ai, ad := range ads {
		for _, cb := range perAdCombos[ai] {
			all = append(all, simulateCombo(ad.Name(), triggers[ai], klines, cb.trail, cb.hold, cb.score))
			done++
			if pct := done * 100 / total; pct >= lastPct+10 { // 每 10% 进度行（worker 尾随喂狗）
				lastPct = pct
				fmt.Printf("参数优化进度 %d%%\n", pct)
			}
		}
	}

	// ── 4) 排名输出（有效组合按目标降序；触发不足者垫底仅列出）──
	for i := range all {
		all[i].ObjectiveScore = objectiveValue(obj, &all[i])
	}
	qualifying := make([]int, 0, len(all))
	weak := make([]int, 0)
	for i := range all {
		if all[i].Count >= sweepMinTrades {
			qualifying = append(qualifying, i)
		} else {
			weak = append(weak, i)
		}
	}
	sort.Slice(qualifying, func(a, b int) bool {
		x, y := &all[qualifying[a]], &all[qualifying[b]]
		if x.ObjectiveScore != y.ObjectiveScore {
			return x.ObjectiveScore > y.ObjectiveScore
		}
		return x.Count > y.Count
	})
	sort.Slice(weak, func(a, b int) bool { return all[weak[a]].Count > all[weak[b]].Count })

	fmt.Printf("══════════════════════════════════════════════\n")
	fmt.Printf("参数优化目标: %s | 组合总数 %d | 有效排名 %d（触发≥%d）\n",
		objName, total, len(qualifying), sweepMinTrades)
	rank := qualifying
	if len(rank) > topN {
		rank = rank[:topN]
	}
	for pos, idx := range rank {
		r := &all[idx]
		fmt.Printf("#%d 【%s】止盈%.0f%% 持仓%d天 门槛%.0f → 胜率%.2f%% 盈亏比%.2f 触发%d 平均持仓%.1f天\n",
			pos+1, r.Name, r.Trail, r.Hold, r.MinScore, r.WinRate, r.ProfitFactor, r.Count, r.AvgHold)
	}
	if len(rank) == 0 {
		fmt.Println("无达到最低触发数的组合——考虑扩大日期区间或股票池。")
	}

	// 机器可读行：worker 解析后落 optimization_results（§P2-c）
	payload := map[string]any{
		"objective": obj,
		"total":     total,
		"results":   rankedJSON(&all, qualifying),
	}
	if bj, jerr := json.Marshal(payload); jerr == nil {
		fmt.Printf("SWEEP_JSON:%s\n", bj)
	}
	fmt.Printf("══════════════════════════════════════════════\n")
	return nil
}

// sweepTriggersOf 单股单 adapter 的触发扫描（backtestStock 的无出场版）。
func (o *Options) sweepTriggersOf(ad adapter, ai int, code string, kls []data.KLine,
	indByDate map[string]float64) []sweepTrigger {
	var out []sweepTrigger
	if na, ok := ad.(*nShapeAdapter); ok {
		na.macdSeries = data.CalcMACDSeries(kls)
	}
	for i := 29; i < len(kls)-1; i++ {
		if na, ok := ad.(*nShapeAdapter); ok {
			na.curIdx = i
		}
		prevClose := 0.0
		if i > 0 {
			prevClose = kls[i-1].Close
		}
		indChg := 0.0
		if indByDate != nil {
			indChg = indByDate[kls[i].Date.Format("20060102")]
		}
		meta, ok := ad.Trigger(kls[:i+1], prevClose, indChg)
		if !ok {
			continue
		}
		entry := kls[i+1].Open
		if entry <= 0 {
			continue
		}
		high := meta["highest_price"]
		if high <= 0 {
			high = entry
		}
		out = append(out, sweepTrigger{ad: ai, code: code, sigIdx: i,
			entry: entry, score: meta["score"], highest: high})
	}
	return out
}

// simulateCombo 单组合模拟：门槛过滤 + 按 (ad,code) 贪心不重叠 + 统一出场。
// 指标聚合与 summarize() 同口径（胜率/平均盈亏/盈亏比/平均持仓）。
// English: one combo simulation — score filter, greedy non-overlapping entries per stock,
// uniform trailing+timeout exit; metrics aggregated exactly like summarize().
func simulateCombo(name string, trigs []sweepTrigger, klines map[string][]data.KLine,
	trailPct float64, maxHold int, minScore float64) sweepResult {
	res := sweepResult{Name: name, Trail: trailPct, Hold: maxHold, MinScore: minScore}
	nextFree := map[string]int{} // code -> 可再入场的最早下标
	var winSum, lossSum float64
	for _, t := range trigs {
		if minScore > 0 && t.score >= 0 && t.score < minScore {
			continue // 门槛过滤（-1 标记=无分维度，不过滤）
		}
		if t.sigIdx < nextFree[t.code] {
			continue // 同股同战法持仓期内不重复入场（与单组合回放语义一致）
		}
		exitJ, pnl := uniformExit(klines[t.code], t.sigIdx, t.entry, t.highest, trailPct, maxHold)
		nextFree[t.code] = exitJ + 1
		res.Count++
		res.AvgHold += float64(exitJ - (t.sigIdx + 1))
		if pnl > 0 {
			res.Win++
			winSum += pnl
		} else {
			res.Loss++
			lossSum += pnl
		}
	}
	if res.Count == 0 {
		return res
	}
	res.AvgHold /= float64(res.Count)
	if res.Win > 0 {
		res.AvgWinPct = winSum / float64(res.Win)
	}
	if res.Loss > 0 {
		res.AvgLossPct = lossSum / float64(res.Loss)
	}
	if res.Win+res.Loss > 0 {
		res.WinRate = float64(res.Win) / float64(res.Win+res.Loss) * 100
	}
	if lossSum != 0 {
		res.ProfitFactor = winSum / -lossSum
	}
	return res
}

// uniformExit 统一出场引擎：阶段高点回撤 ≥trailPct%（且曾盈利）止盈离场；
// 持仓 ≥maxHold 天超期离场；数据末尾强制结算。返回平仓日下标与收益率(%)。
func uniformExit(kls []data.KLine, sigIdx int, entry, sigHigh, trailPct float64, maxHold int) (int, float64) {
	entryDay := sigIdx + 1
	stageHigh := math.Max(entry, sigHigh)
	lastJ := len(kls) - 1
	pnlAt := func(j int) float64 { return (kls[j].Close - entry) / entry * 100 }
	for j := entryDay + 1; j <= lastJ; j++ {
		cur := kls[j].Close
		if cur <= 0 {
			continue
		}
		if cur > stageHigh {
			stageHigh = cur
		}
		dd := (cur - stageHigh) / stageHigh * 100
		if stageHigh > entry && dd <= -trailPct {
			return j, pnlAt(j)
		}
		if j-entryDay >= maxHold {
			return j, pnlAt(j)
		}
	}
	if lastJ > entryDay {
		return lastJ, pnlAt(lastJ)
	}
	return entryDay, 0
}

// objectiveValue 按目标函数取排名值。盈亏比封顶 99 防无亏损组合发散；
// 零亏损且有盈利的组合按满分处理（否则 PF 字段保持 0 的"完美组合"会被排到末位）。
func objectiveValue(obj string, r *sweepResult) float64 {
	switch obj {
	case "winrate":
		return r.WinRate
	case "avgwin":
		return r.AvgWinPct
	default: // profitfactor
		if r.Loss == 0 && r.Win > 0 {
			return 99
		}
		if r.ProfitFactor > 99 {
			return 99
		}
		return r.ProfitFactor
	}
}

// rankedJSON 排名结果的结构化输出（worker 持仓 optimization_results 用）。
func rankedJSON(all *[]sweepResult, order []int) []map[string]any {
	out := make([]map[string]any, 0, len(order))
	for pos, idx := range order {
		r := (*all)[idx]
		out = append(out, map[string]any{
			"rank": pos + 1, "strategy": r.Name,
			"params":   map[string]any{"trail_pct": r.Trail, "hold_days": r.Hold, "min_score": r.MinScore},
			"win_rate": r.WinRate, "profit_factor": r.ProfitFactor,
			"trigger_count": r.Count, "avg_hold_days": r.AvgHold,
		})
	}
	return out
}
