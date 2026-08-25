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

// 策略自有寻优池：每个战法独立设定止盈线/止损线/兜底天数的搜索范围（§用户反馈）。
// 不同战法出发点不同，参数范围理应不同——波动突破需要宽止损，N形需要紧止损。
// 未在表中的战法使用 defaultPool。
// strategyPool 单个战法的独立寻优池：止盈线/止损线候选值 + 固定兜底天数。
type strategyPool struct {
	tpRange []float64 // 止盈线候选值%
	slRange []float64 // 止损线候选值%
	maxHold int       // 兜底天数（固定值，不搜索）
}

// strategyPools 各战法独立寻优池（§用户反馈：分战法回测）。
// 不同战法出发点不同，参数范围理应不同——波动突破需要宽止盈宽止损，
// N形需要紧止损。键=战法显示名（与 adapter.Name() 一致），未命中走 defaultPool。
var strategyPools = map[string]strategyPool{
	"波动突破战法": {tpRange: stepRange(10, 30, 5), slRange: stepRange(5, 15, 2.5), maxHold: 30},
	"双响炮":    {tpRange: stepRange(5, 15, 2.5), slRange: stepRange(3, 12, 3), maxHold: 25},
	"龙头":     {tpRange: stepRange(3, 12, 3), slRange: stepRange(3, 10, 2), maxHold: 20},
	"龙回头":    {tpRange: stepRange(5, 15, 2.5), slRange: stepRange(3, 10, 2), maxHold: 25},
	"N形":     {tpRange: stepRange(3, 10, 2), slRange: stepRange(2, 6, 1), maxHold: 15},
}

// defaultPool 库规则（因子/形态战法）等未在 strategyPools 中登记的战法的默认寻优池。
var defaultPool = strategyPool{
	tpRange: stepRange(5, 25, 5),
	slRange: stepRange(3, 12, 3),
	maxHold: 30,
}

// stepRange 按 (起点, 终点, 步长) 生成连续候选值序列（步进形式搜索空间），
// 保留两位小数规避浮点累加误差；终点含入（+0.001 容差）。
func stepRange(from, to, step float64) []float64 {
	var out []float64
	for v := from; v <= to+0.001; v += step {
		out = append(out, math.Round(v*100)/100)
	}
	return out
}

// poolFor 返回战法对应的寻优池，未知战法返回 defaultPool。
func poolFor(name string) strategyPool {
	if p, ok := strategyPools[name]; ok {
		return p
	}
	return defaultPool
}

// sweepMinTrades 进入排名的最低触发数——几笔交易 100% 胜率的组合没有统计意义。
const sweepMinTrades = 20

// scoreQuantiles 从触发分数里取自适应阈值：p40/p60/p80/p95 分位数（去重升序）。
// 样本不足或分布无区分度（全同分）返回 nil——调用方跳过门槛维。
// 保证每档阈值都真实切分该战法的分数分布，而不是落在分布外产生完全相同的重复组合。
// English: adaptive thresholds from trigger-score quantiles (p40/p60/p80/p95, deduped);
// nil when there aren't enough samples or the distribution has no spread.
func scoreQuantiles(scores []float64) []float64 {
	if len(scores) < sweepMinTrades {
		return nil
	}
	sorted := append([]float64(nil), scores...)
	sort.Float64s(sorted)
	pick := func(p float64) float64 {
		idx := int(p * float64(len(sorted)-1))
		v := sorted[idx]
		return math.Round(v)
	}
	out := make([]float64, 0, 4)
	for _, p := range []float64{0.40, 0.60, 0.80, 0.95} {
		v := pick(p)
		if v > 0 && (len(out) == 0 || out[len(out)-1] != v) {
			out = append(out, v)
		}
	}
	if len(out) < 2 {
		return nil // 分布挤在一起（如全部顶格）——门槛维无意义，跳过
	}
	return out
}

// sweepMaxCacheStocks 扫参 K 线缓存的全局护栏：防止 CLI 直跑（MaxStocks=0=全市场）
// 把研究侧 cgroup 挤爆。English: hard cap on the sweep kline cache.
const sweepMaxCacheStocks = 500

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
	Kind           string  // 规则 ID（fac_N/pat_N）；内置战法为空
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
	Expectancy     float64 `json:"expectancy"`
	StopLossPct    float64 `json:"stop_loss_pct"`
	AvgHold        float64 `json:"avg_hold_days"`
	ObjectiveScore float64 `json:"-"`

	// §GAP4.5 风险调整指标（随 SWEEP_JSON 落库展示）
	Sharpe          float64 `json:"sharpe"`
	MaxDrawdownPct  float64 `json:"max_drawdown_pct"`
	AnnualReturnPct float64 `json:"annual_return_pct"`
	Calmar          float64 `json:"calmar"`
}

// runSweep 扫参主流程。codes 为裸码列表；industryChg 与普通回放同构。
func (o *Options) runSweep(db *store.DB, codes []string, ads []adapter,
	industryChg map[string]map[string]float64) error {

	obj := strings.ToLower(strings.TrimSpace(o.Sweep.Objective))
	if obj == "" {
		obj = "profitfactor"
	}
	objName := map[string]string{"profitfactor": "盈亏比", "winrate": "胜率", "avgwin": "平均盈利", "expectancy": "期望收益"}[obj]
	if objName == "" {
		return fmt.Errorf("未知优化目标: %s（可选 profitFactor/winRate/avgWin/expectancy）", o.Sweep.Objective)
	}

	// ── 1) K 线一次性载入内存 ──
	if len(codes) > sweepMaxStocksLimit(o.MaxStocks) {
		codes = codes[:sweepMaxStocksLimit(o.MaxStocks)]
	}
	klines := make(map[string][]data.KLine, len(codes))
	for _, tsCode := range codes {
		// §GAP4 复权价：扫参与回放同用 HfqBars（除权缺口不再污染形态/止损判定）
		bars, err := db.HfqBars(tsCode, o.Start, o.End)
		if err != nil || len(bars) < 15 {
			continue
		}
		code := strings.Split(tsCode, ".")[0]
		klines[code] = toDataKLine(bars)
	}

	// ── 2) 逐战法独立优化：四维步进网格 → 分批护栏 → 批冠军淘汰赛 → 冠军实盘口径复核 ──
	// §OPTIMIZE_POOL_INTEGRATION_PLAN D2：
	//   搜索空间 = 止盈线% × 止损线% × 持仓天数 × 门槛分数（sweep_pool_configs 可自定义，
	//   未配置走内置默认池）；网格扫参统一用 uniformExitV2 轻量出场（触发预计算一次，
	//   分数过滤在触发上做，不重跑 Trigger）；全组合按批(≤5000)切分，批内取最优为批冠军，
	//   批冠军间按目标函数 PK 出全局冠军；冠军再经真实 adapter.Exit 回放一遍复核并标注。
	for ai, ad := range ads {
		fmt.Printf("\n══════════════════════════════════════════\n")
		fmt.Printf("【%s】独立寻优\n", ad.Name())
		fmt.Printf("══════════════════════════════════════════\n")

		kind := ""
		if kp, ok := ad.(kindProvider); ok {
			kind = kp.Kind()
		}

		// 2a) 触发预计算（入场与出场参数无关，一次算完全程复用）
		var trigs []sweepTrigger
		for code, kls := range klines {
			trigs = append(trigs, o.sweepTriggersOf(ad, ai, code, kls, industryChg[code])...)
		}
		sort.Slice(trigs, func(i, j int) bool { return trigs[i].sigIdx < trigs[j].sigIdx })
		log.Printf("触发预算 %s：%d 个入场事件", ad.Name(), len(trigs))

		// 2b) 搜索空间解析：DB 配置优先，回退内置默认池
		poolCfg := defaultPoolConfig(ad.Name())
		if c, err := db.GetSweepPoolConfig(ad.Name()); err == nil && c != nil {
			poolCfg = c // 用户自定义覆盖（PUT /api/research/sweep-pools）
		}
		tps := stepRangeF(poolCfg.TpFrom, poolCfg.TpTo, poolCfg.TpStep)
		sls := stepRangeF(poolCfg.SlFrom, poolCfg.SlTo, poolCfg.SlStep)
		holds := stepRangeI(poolCfg.HoldFrom, poolCfg.HoldTo, poolCfg.HoldStep)

		// 门槛维：有连续入场分的战法用用户配置区间；无分战法只跑 0 档（配置无效自动降级）
		hasScore := false
		for _, t := range trigs {
			if t.score >= 0 {
				hasScore = true
				break
			}
		}
		scores := []float64{0}
		if hasScore {
			scores = stepRangeF(poolCfg.ScoreFrom, poolCfg.ScoreTo, poolCfg.ScoreStep)
		}

		// 2c) 全组合枚举（指数级：|tp|×|sl|×|hold|×|score|，护栏由保存端校验 ≤10万）
		type combo4 struct {
			tp, sl, score float64
			hold          int
		}
		var combos []combo4
		for _, tp := range tps {
			for _, sl := range sls {
				for _, h := range holds {
					for _, s := range scores {
						combos = append(combos, combo4{tp, sl, s, h})
					}
				}
			}
		}
		fmt.Printf("搜索空间：%d 组合（止盈%d档×止损%d档×持仓%d档×门槛%d档）\n",
			len(combos), len(tps), len(sls), len(holds), len(scores))

		// 2d) 分批锦标赛：每批 ≤5000 全量模拟出批冠军，批冠军间 PK 出全局冠军
		const batchSize = 5000                     // §护栏：单批组合上限（分批全量模拟，非抽样）
		var champions []sweepResult                // champions[bi] = 第 bi 批冠军（值语义，避免切片扩容指针失效）
		all := make([]sweepResult, 0, len(combos)) // 全量留存供热力网格聚合（10万条 ≈ 12MB）
		done := 0
		lastPct := -10
		for bi := 0; bi*batchSize < len(combos); bi++ {
			lo, hi := bi*batchSize, min((bi+1)*batchSize, len(combos))
			bestInBatch := sweepResult{}
			hasChamp := false
			for ci := lo; ci < hi; ci++ {
				cb := combos[ci]
				r := simulateUniform(ad.Name(), kind, trigs, klines, cb.tp, cb.sl, cb.hold, cb.score)
				r.ObjectiveScore = objectiveValue(obj, &r)
				all = append(all, r)
				cur := &all[len(all)-1]
				if !hasChamp || betterOf(obj, cur, &bestInBatch) == cur {
					bestInBatch = *cur
					hasChamp = true
				}
				done++
				if pct := done * 100 / len(combos); pct >= lastPct+10 {
					lastPct = pct
					fmt.Printf("参数优化进度 %d%%（第%d/%d批）\n", pct, bi+1, (len(combos)+batchSize-1)/batchSize)
				}
			}
			if hasChamp {
				champions = append(champions, bestInBatch)
			}
		}
		if len(all) == 0 {
			fmt.Println("无有效组合。")
			continue
		}
		// 批冠军终选（平局以触发数多者胜——与 betterOf 同口径）
		global := champions[0]
		for i := 1; i < len(champions); i++ {
			if betterOf(obj, &champions[i], &global) == &champions[i] {
				global = champions[i]
			}
		}
		best := &global

		// 2e) 冠军实盘口径复核：真实 adapter.Exit 注入冠军参数后整库回放一遍。
		// 网格用统一出场引擎保证四维梯度有效；复核让落地数字与实盘同口径、两种口径都留档。
		verify := verifyChampion(ad, kind, o, klines, industryChg,
			best.Trail, best.StopLossPct, best.Hold)
		fmt.Printf("★ 冠军：止盈%.0f%% 止损%.0f%% 持仓%d天 门槛%.0f → 胜率%.2f%% 盈亏比%.2f 期望%+.2f%% 触发%d\n",
			best.Trail, best.StopLossPct, best.Hold, best.MinScore, best.WinRate, best.ProfitFactor, best.Expectancy, best.Count)
		fmt.Printf("✓ 实盘口径复核：胜率%.2f%% 盈亏比%.2f 期望%+.2f%% 触发%d\n",
			verify.WinRate, verify.ProfitFactor, verify.Expectancy, verify.Count)

		// 2f) 止盈×止损热力网格（格值=该格跨持仓/门槛的最优期望）+ 批次冠军明细
		type gridCell struct {
			// §GAP 修复：Sl 原误标 json:"tp" 与 Tp 冲突，序列化后 sl 恒缺失——
			// 前端热力网格（Research.vue optCurHeat 按 tp|sl 复合键查表）sl 维度全为 0。
			Tp         float64 `json:"tp"`
			Sl         float64 `json:"sl"`
			Expectancy float64 `json:"expectancy"`
			Triggers   int     `json:"triggers"`
		}
		gridMap := map[[2]float64]*gridCell{}
		for i := range all {
			r := &all[i]
			key := [2]float64{r.Trail, r.StopLossPct}
			g := gridMap[key]
			if g == nil {
				g = &gridCell{Tp: key[0], Sl: key[1], Expectancy: math.Inf(-1)}
				gridMap[key] = g
			}
			if r.Expectancy > g.Expectancy {
				g.Expectancy = r.Expectancy
				g.Triggers = r.Count
			}
		}
		grid := make([]gridCell, 0, len(gridMap))
		for _, g := range gridMap {
			grid = append(grid, *g)
		}
		sort.Slice(grid, func(a, b int) bool {
			if grid[a].Tp != grid[b].Tp {
				return grid[a].Tp < grid[b].Tp
			}
			return grid[a].Sl < grid[b].Sl
		})

		// 控制台热力图（人读）
		fmt.Println("\n止盈×止损网格（跨持仓/门槛最优期望%）:")
		fmt.Printf("止盈线→")
		for _, tp := range tps {
			fmt.Printf("  %5.0f%%", tp)
		}
		fmt.Println()
		for _, sl := range sls {
			fmt.Printf("止损%4.0f%%  ", sl)
			for _, tp := range tps {
				if g, ok := gridMap[[2]float64{tp, sl}]; ok && !math.IsInf(g.Expectancy, -1) {
					fmt.Printf("%+5.2f%%", g.Expectancy)
				} else {
					fmt.Printf("   —  ")
				}
			}
			fmt.Println()
		}

		// 2g) 输出该战法 SWEEP_JSON（worker 解析落库：冠军行 + grid/batches 附带信息）
		jsonResult := map[string]any{
			"rank": 1, "strategy": ad.Name(), "strategy_kind": kind,
			"params":        map[string]any{"take_profit_pct": best.Trail, "stop_loss_pct": best.StopLossPct, "hold_days": best.Hold, "min_score": best.MinScore},
			"win_rate":      best.WinRate,
			"profit_factor": best.ProfitFactor,
			"expectancy":    best.Expectancy,
			"win":           best.Win, "loss": best.Loss,
			"avg_win_pct":   best.AvgWinPct,
			"avg_loss_pct":  best.AvgLossPct,
			"trigger_count": best.Count,
			"avg_hold_days": best.AvgHold,
			// 实盘口径复核数字（前端冠军卡展示，与网格口径并列）
			"verify_win_rate":      verify.WinRate,
			"verify_profit_factor": verify.ProfitFactor,
			"verify_expectancy":    verify.Expectancy,
			"verify_trigger_count": verify.Count,
		}
		batchList := make([]map[string]any, 0, len(champions))
		for i, ch := range champions {
			batchList = append(batchList, map[string]any{
				"batch": i + 1,
				"tp":    ch.Trail, "sl": ch.StopLossPct,
				"hold_days": ch.Hold, "min_score": ch.MinScore,
				"objective": ch.ObjectiveScore,
			})
		}
		payload := struct {
			Strategy  string           `json:"strategy"`
			Objective string           `json:"objective"`
			Batches   []map[string]any `json:"batches,omitempty"`
			Grid      []gridCell       `json:"grid,omitempty"`
			Results   []any            `json:"results"`
		}{ad.Name(), obj, batchList, grid, []any{jsonResult}}
		if bj, jerr := json.Marshal(payload); jerr == nil {
			fmt.Printf("SWEEP_JSON:%s\n", bj)
		}
	}

	fmt.Printf("==============================================\n")
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

// simulateCombo 单组合模拟：用战法自身真实出场逻辑回测，不依赖统一出场公式。
// 对每个组合，把参数应用到战法适配器，然后跑全量 backtestStock（真实的入场+出场），
// 聚合胜率/盈亏比/期望收益等指标。
// §用户反馈：分战法回测，每个战法用自己的出场逻辑，不搞统一公式。
func simulateCombo(ad adapter, kind string, o *Options, klines map[string][]data.KLine,
	industryChg map[string]map[string]float64, takeProfitPct float64, stopLossPct float64, maxHold int, minScore float64) sweepResult {
	// 记录原始参数，组合完成后恢复
	restore := applyComboParams(ad, takeProfitPct, stopLossPct, maxHold, minScore)
	res := sweepResult{Name: ad.Name(), Kind: kind, Trail: takeProfitPct, StopLossPct: stopLossPct, Hold: maxHold, MinScore: minScore}
	var winSum, lossSum float64
	var pnls []float64
	var dates []string
	for code, kls := range klines {
		indByDate := industryChg[code]
		trades := o.backtestStock(code, kls, ad, indByDate)
		for _, t := range trades {
			res.Count++
			res.AvgHold += float64(t.HoldDays)
			pnls = append(pnls, t.PnlPct)
			dates = append(dates, t.Date)
			if t.PnlPct > 0 {
				res.Win++
				winSum += t.PnlPct
			} else {
				res.Loss++
				lossSum += t.PnlPct
			}
		}
	}
	restore() // 恢复原始参数
	finalizeResult(&res, winSum, lossSum, pnls, dates)
	return res
}

// applyComboParams 把组合参数应用到战法适配器，返回恢复函数。
// 对于内置战法，修改 config 的出场旋钮；对于因子/形态规则，修改 ruleEvalAdapter 的字段。
func applyComboParams(ad adapter, takeProfitPct, stopLossPct float64, maxHold int, minScore float64) func() {
	switch a := ad.(type) {
	case *doubleBumpAdapter:
		old1, old2, old3 := a.cfg.TrailingDrawbackPct, a.cfg.DoubleBumpTakeProfitPct, a.cfg.MaxHoldDays
		if takeProfitPct > 0 {
			a.cfg.TrailingDrawbackPct = takeProfitPct
		}
		if stopLossPct > 0 {
			a.cfg.DoubleBumpTakeProfitPct = stopLossPct
		}
		if maxHold > 0 {
			a.cfg.MaxHoldDays = maxHold
		}
		return func() { a.cfg.TrailingDrawbackPct, a.cfg.DoubleBumpTakeProfitPct, a.cfg.MaxHoldDays = old1, old2, old3 }
	case *dragonAdapter:
		old1, old2 := a.cfg.TrailingDrawbackPct, a.cfg.MaxHoldDays
		if takeProfitPct > 0 {
			a.cfg.TrailingDrawbackPct = takeProfitPct
		}
		if maxHold > 0 {
			a.cfg.MaxHoldDays = maxHold
		}
		return func() { a.cfg.TrailingDrawbackPct, a.cfg.MaxHoldDays = old1, old2 }
	case *dragonReturnAdapter:
		old1, old2, old3, old4 := a.cfg.TakeProfitPct, a.cfg.StopLossPct, a.cfg.MaxHoldDays, a.cfg.TrailingDrawback
		if takeProfitPct > 0 {
			a.cfg.TakeProfitPct = takeProfitPct / 100
			a.cfg.TrailingDrawback = takeProfitPct
		}
		if stopLossPct > 0 {
			a.cfg.StopLossPct = stopLossPct / 100
		}
		if maxHold > 0 {
			a.cfg.MaxHoldDays = maxHold
		}
		return func() {
			a.cfg.TakeProfitPct, a.cfg.StopLossPct, a.cfg.MaxHoldDays, a.cfg.TrailingDrawback = old1, old2, old3, old4
		}
	case *nShapeAdapter:
		old1, old2, old3 := a.cfg.TrailingDrawbackPct, a.cfg.HardStopLoss, a.cfg.MaxHoldDays
		if takeProfitPct > 0 {
			a.cfg.TrailingDrawbackPct = takeProfitPct
		}
		if stopLossPct > 0 {
			a.cfg.HardStopLoss = stopLossPct / 100
		}
		if maxHold > 0 {
			a.cfg.MaxHoldDays = maxHold
		}
		return func() { a.cfg.TrailingDrawbackPct, a.cfg.HardStopLoss, a.cfg.MaxHoldDays = old1, old2, old3 }
	case *ruleEvalAdapter:
		old1, old2 := a.trailOverride, a.holdOverride
		if takeProfitPct > 0 {
			v := takeProfitPct
			a.trailOverride = &v
		}
		if maxHold > 0 {
			v := maxHold
			a.holdOverride = &v
		}
		return func() { a.trailOverride, a.holdOverride = old1, old2 }
	}
	return func() {}
}

// uniformExit 统一出场引擎 v2（§用户反馈：到线就卖，不等天数）。
//
// 三个条件按优先级逐日检查：
//  1. 止损线：亏损达 stopLossPct%（相对入场价）→ 立即卖出控制损失
//  2. 止盈线：盈利达 takeProfitPct%（相对入场价）→ 锁定利润
//  3. 移动止盈：从阶段高点回撤 trailPct%（且曾盈利）→ 保护已有利润
//  4. 兜底：超过 maxHoldDays 天 → 强制离场
//
// 与旧版的区别：新增了独立的止损线和止盈线，不再只依赖移动止盈+超期。
// maxHoldDays 是安全兜底而非主要出场方式。
func uniformExitV2(kls []data.KLine, sigIdx int, entry, sigHigh float64,
	takeProfitPct, stopLossPct, trailPct float64, maxHoldDays int) (int, float64) {
	entryDay := sigIdx + 1
	stageHigh := math.Max(entry, sigHigh)
	lastJ := len(kls) - 1

	pnlAt := func(j int) float64 {
		if entry <= 0 {
			return 0
		}
		// §GAP4.1 净额口径（滑点+佣金+印花税），与 replay 回放同模型
		return costRoundTripPnl(entry, kls[j].Close)
	}

	for j := entryDay + 1; j <= lastJ; j++ {
		cur := kls[j].Close
		if cur <= 0 {
			continue
		}
		if cur > stageHigh {
			stageHigh = cur
		}
		pnlPct := costRoundTripPnl(entry, cur)
		days := j - entryDay

		if stopLossPct > 0 && pnlPct <= -stopLossPct {
			return j, pnlPct // 止损线：立即卖出控制损失
		}
		if takeProfitPct > 0 && pnlPct >= takeProfitPct {
			return j, pnlPct // 止盈线：锁定利润
		}
		if trailPct > 0 && stageHigh > entry {
			dd := (cur - stageHigh) / stageHigh * 100
			if dd <= -trailPct {
				return j, pnlAt(j)
			}
		}
		if days >= maxHoldDays {
			return j, pnlPct
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
	case "expectancy":
		return r.Expectancy
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
			"rank": pos + 1, "strategy": r.Name, "strategy_kind": r.Kind,
			"params":   map[string]any{"trail_pct": r.Trail, "hold_days": r.Hold, "min_score": r.MinScore},
			"win_rate": r.WinRate, "profit_factor": r.ProfitFactor,
			"win": r.Win, "loss": r.Loss,
			"avg_win_pct": r.AvgWinPct, "avg_loss_pct": r.AvgLossPct,
			"trigger_count": r.Count, "avg_hold_days": r.AvgHold,
		})
	}
	return out
}

// sweepMaxStocksLimit 生效股票池上限：显式 MaxStocks 与全局护栏取小（至少 50 保底可用）。
func sweepMaxStocksLimit(maxStocks int) int {
	limit := maxStocks
	if limit <= 0 || limit > sweepMaxCacheStocks {
		limit = sweepMaxCacheStocks
	}
	if limit < 50 {
		limit = 50
	}
	return limit
}

// ── §D2 四维锦标赛寻优的辅助件 ──

// betterOf 两结果按目标函数 PK（淘汰赛比较子）；平局以触发数多者胜（小样本让位）。
func betterOf(obj string, a, b *sweepResult) *sweepResult {
	va, vb := objectiveValue(obj, a), objectiveValue(obj, b)
	if va != vb {
		if va > vb {
			return a
		}
		return b
	}
	if a.Count >= b.Count {
		return a
	}
	return b
}

// simulateUniform 网格扫参专用轻量模拟：预计算触发 + uniformExitV2 统一出场。
// 触发与出场参数无关 → 只在此处按门槛过滤/贪心不重叠/逐日出场扫描，
// 单组合成本 O(触发数×平均持仓天数)，万级组合秒~分钟级完成。
// English: grid-mode lightweight simulation — precomputed triggers filtered by threshold,
// greedy non-overlapping entries, daily uniform exit walk; thousands of combos in seconds.
func simulateUniform(name, kind string, trigs []sweepTrigger, klines map[string][]data.KLine,
	takeProfitPct, stopLossPct float64, maxHold int, minScore float64) sweepResult {
	res := sweepResult{Name: name, Kind: kind, Trail: takeProfitPct, StopLossPct: stopLossPct,
		Hold: maxHold, MinScore: minScore}
	nextFree := map[string]int{} // code -> 可再入场最早下标（同股持仓期内不重复入场）
	var winSum, lossSum float64
	var pnls []float64
	var dates []string
	for _, t := range trigs {
		if minScore > 0 && t.score >= 0 && t.score < minScore {
			continue // 门槛过滤（score=-1 标记=无分维度，不过滤）
		}
		if t.sigIdx < nextFree[t.code] {
			continue
		}
		exitJ, pnl := uniformExitV2(klines[t.code], t.sigIdx, t.entry, t.highest,
			takeProfitPct, stopLossPct, 0, maxHold)
		nextFree[t.code] = exitJ + 1
		res.Count++
		res.AvgHold += float64(exitJ - (t.sigIdx + 1))
		pnls = append(pnls, pnl)
		dates = append(dates, klines[t.code][t.sigIdx+1].Date.Format("20060102"))
		if pnl > 0 {
			res.Win++
			winSum += pnl
		} else {
			res.Loss++
			lossSum += pnl
		}
	}
	finalizeResult(&res, winSum, lossSum, pnls, dates)
	return res
}

// finalizeResult 由胜负计数与盈亏和聚合出 胜率/均盈/均亏/盈亏比/期望/风险调整指标
// （两种模拟共用口径；pnls/dates 为按发生序的逐笔净额收益与入场日）。
func finalizeResult(res *sweepResult, winSum, lossSum float64, pnls []float64, dates []string) {
	if res.Count == 0 {
		return
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
	wr := res.WinRate / 100
	res.Expectancy = wr*res.AvgWinPct + (1-wr)*res.AvgLossPct
	res.Sharpe, res.MaxDrawdownPct, res.AnnualReturnPct, res.Calmar = perfMetrics(pnls, dates)
}

// stepRangeF 浮点步进序列（止盈/止损/门槛维），含起终点；非法输入回退单档。
func stepRangeF(from, to, step float64) []float64 {
	if step <= 0 || to < from {
		return []float64{from}
	}
	out := make([]float64, 0, int((to-from)/step)+1)
	for v := from; v <= to+0.001; v += step {
		out = append(out, math.Round(v*100)/100)
	}
	return out
}

// verifyChampion 冠军实盘口径复核：把冠军参数注入该战法真实 adapter.Exit 后整库回放一遍。
// 直接复用 simulateCombo（applyComboParams 注入+恢复、backtestStock 走战法原生出场逻辑）。
func verifyChampion(ad adapter, kind string, o *Options, klines map[string][]data.KLine,
	industryChg map[string]map[string]float64, tp, sl float64, holdDays int) sweepResult {
	return simulateCombo(ad, kind, o, klines, industryChg, tp, sl, holdDays, 0)
}

// stepRangeI 整数步进序列（持仓天数维），含起终点；非法输入回退单档。
func stepRangeI(from, to, step int) []int {
	if step <= 0 || to < from {
		return []int{from}
	}
	out := make([]int, 0, (to-from)/step+1)
	for v := from; v <= to; v += step {
		out = append(out, v)
	}
	return out
}

// defaultPoolConfig 内置默认池 → DB 配置结构（引擎侧统一消费 SweepPoolConfig；
// 步长由相邻两档差值推导，单档维度按 1 计）。门槛默认 40~95 步进 5。
func defaultPoolConfig(name string) *store.SweepPoolConfig {
	p := poolFor(name)
	derive := func(r []float64) (from, to, step float64) {
		if len(r) == 0 {
			return 0, 0, 1
		}
		if len(r) == 1 {
			return r[0], r[0], 1
		}
		return r[0], r[len(r)-1], r[1] - r[0]
	}
	tpFrom, tpTo, tpStep := derive(p.tpRange)
	slFrom, slTo, slStep := derive(p.slRange)
	return &store.SweepPoolConfig{
		Strategy: name,
		TpFrom:   tpFrom, TpTo: tpTo, TpStep: tpStep,
		SlFrom: slFrom, SlTo: slTo, SlStep: slStep,
		HoldFrom: 2, HoldTo: p.maxHold, HoldStep: 2,
		ScoreFrom: 40, ScoreTo: 95, ScoreStep: 5,
	}
}
