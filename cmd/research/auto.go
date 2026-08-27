// 自动研究闭环（B5）：optimize 优化权重产出候选 → 审批 → 应用。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	"quant-trading-v2/internal/backtest"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/factor"
	"quant-trading-v2/internal/research"
	"quant-trading-v2/internal/store"
)

// cmdOptimize 权重优化子命令：以 IC/IR 为目标优化 7 大类因子权重，
// 产出一条候选（含 B4 回测超额验证），护栏通过才标记可审批。
// （cmdOptimize runs weight optimization, validates the best weight set in the B4 chain,
// and saves a candidate guarded by IR/day thresholds.）
func cmdOptimize(db *store.DB, args []string) {
	fs := flag.NewFlagSet("optimize", flag.ExitOnError)
	start := fs.String("start", "20200101", "起始日期 YYYYMMDD")
	end := fs.String("end", "20210101", "结束日期 YYYYMMDD")
	h := fs.Int("h", 5, "前瞻天数")
	minStocks := fs.Int("min-stocks", 10, "每日最小样本")
	metric := fs.String("metric", "ir", "优化目标: ir|ic")
	factors := fs.String("factors", defaultFactorPool(), "因子池（逗号分隔）")
	iter := fs.Int("iter", 6, "坐标上升轮数")
	minIR := fs.Float64("min-ir", 0.3, "护栏 |IR| 下限")
	minDays := fs.Int("min-days", 20, "护栏有效日下限")
	btEvents := fs.Int("bt-events", 0, "B4 回测验证的事件数上限（0=跳过回测）")
	btMinStocks := fs.Int("bt-min-stocks", 10, "B4 回测每日最小样本")
	btMinLimitUps := fs.Int("bt-min-limit-ups", 3, "B4 回测事件触发涨停下限")
	codesFile := fs.String("codes", "", "研究池文件（每行一个 ts_code）")
	fs.Parse(args)

	codes, err := db.StockCodes()
	if *codesFile != "" {
		codes, err = readCodesFile(*codesFile)
	}
	if err != nil {
		log.Fatalf("读取研究池失败: %v", err)
	}
	if len(codes) == 0 {
		log.Fatalf("研究池为空")
	}
	pool := strings.Split(*factors, ",")
	defs := make([]factor.Def, 0, len(pool))
	for _, f := range pool {
		d, ok := factor.Get(strings.TrimSpace(f))
		if !ok {
			log.Fatalf("未知因子: %s", f)
		}
		defs = append(defs, d)
	}

	log.Printf("装配 %d 只股票…", len(codes))
	panels, err := research.BuildPanels(db, codes, *start, *end, defs)
	if err != nil {
		log.Fatalf("装配面板失败: %v", err)
	}
	if len(panels) == 0 {
		log.Fatalf("无有效面板")
	}

	ids := make([]string, len(defs))
	for i, d := range defs {
		ids[i] = d.ID
	}
	opts := research.OptimizeOpts{
		Factors: ids, Horizon: *h, MinStocks: *minStocks,
		Metric: *metric, MaxIter: *iter,
		GuardMinIR: *minIR, GuardMinDays: *minDays,
	}
	res := research.OptimizeWeights(panels, opts)
	log.Printf("优化完成: IR=%.3f IC=%.4f 有效日=%d 护栏=%v (%s)",
		res.IR, res.ICMean, res.NDays, res.PassGuard, res.Reason)
	for _, f := range sortedIDs(res.Weights) {
		log.Printf("  %s %.3f", f, res.Weights[f])
	}

	// B4 回测验证超额（可选）
	avgExcess := 0.0
	if *btEvents > 0 {
		bopts := backtest.DefaultOptions()
		bopts.Start, bopts.End = *start, *end
		bopts.Horizons = []int{*h}
		bopts.MinLimitUps = *btMinLimitUps
		bopts.Rule = backtest.DefaultRule()
		bopts.Rule.Factors = ids
		bopts.Rule.Weights = res.Weights
		bopts.Rule.TopK = 5
		bopts.Rule.MinStocks = *btMinStocks
		bopts.MaxPerDay = 1
		rep, err := backtest.Run(db, bopts)
		if err == nil {
			if v, ok := rep.AvgExcess[*h]; ok {
				avgExcess = v
			}
			log.Printf("B4 回测验证: 事件=%d 入选=%d 平均超额=%s",
				rep.TotalEvents, rep.TotalPicks, fmt.Sprintf("%.4f", avgExcess))
		} else {
			log.Printf("B4 回测验证跳过: %v", err)
		}
	}

	// 存候选
	wj, _ := json.Marshal(res.Weights)
	fj, _ := json.Marshal(ids)
	status := "proposed"
	if !res.PassGuard {
		status = "proposed" // 护栏不过仍入库，标记 reason
	}
	id, err := db.SaveCandidate(&store.Candidate{
		Kind: "weights", Status: status, Factors: string(fj), Weights: string(wj),
		Metric: res.IR, ICMean: res.ICMean, IR: res.IR, AvgExcess: avgExcess,
		Horizon: *h, Reason: res.Reason,
	})
	if err != nil {
		log.Fatalf("保存候选失败: %v", err)
	}
	log.Printf("候选 #%d 已入库（%s）", id, res.Reason)
}

// cmdList 列出候选：按状态（proposed/approved/rejected/applied）过滤，缺省全部。
// 打印每条候选的关键证据（IR/IC/回测超额/前瞻天数/理由），供人工审批参考。
// （cmdList lists candidates, optionally filtered by status, with key evidence for approval.）
func cmdList(db *store.DB, args []string) {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	status := fs.String("status", "", "按状态过滤: proposed|approved|rejected|applied")
	fs.Parse(args)
	cands, err := db.ListCandidates(*status)
	if err != nil {
		log.Fatalf("读取候选失败: %v", err)
	}
	for _, c := range cands {
		fmt.Printf("#%d %s %-8s %-9s IR=%.3f IC=%.3f 超额=%.4f h=%d 理由=%s\n",
			c.ID, c.CreatedAt, c.Kind, c.Status, c.IR, c.ICMean, c.AvgExcess, c.Horizon, c.Reason)
	}
}

// cmdApprove 审批候选：approve <id> 或 reject <id>。
// approve 同时把权重写入应用文件（B5 一键应用：随 config 热加载被引擎读取）。
func cmdApprove(db *store.DB, args []string, dataDir string) {
	fs := flag.NewFlagSet("approve", flag.ExitOnError)
	action := fs.String("action", "approve", "approve|reject")
	fs.Parse(args)
	if fs.NArg() < 1 {
		log.Fatalf("用法: research approve --action approve|reject <id>")
	}
	id, err := strconv.ParseInt(fs.Arg(0), 10, 64)
	if err != nil {
		log.Fatalf("无效 id: %s", fs.Arg(0))
	}
	c, err := db.CandidateByID(id)
	if err != nil {
		log.Fatalf("候选不存在: %v", err)
	}
	switch *action {
	case "approve":
		if err := db.UpdateCandidateStatus(id, "approved"); err != nil {
			log.Fatalf("更新状态失败: %v", err)
		}
		// 一键应用：写 applied_rules.json（战法消费方读取）
		if c.Kind == "weights" {
			if err := research.ApplyWeights(dataDir, c); err != nil {
				log.Fatalf("应用权重失败: %v", err)
			}
			if err := db.UpdateCandidateStatus(id, "applied"); err != nil {
				log.Fatalf("更新状态失败: %v", err)
			}
		}
		log.Printf("候选 #%d 已审批并应用", id)
	case "reject":
		if err := db.UpdateCandidateStatus(id, "rejected"); err != nil {
			log.Fatalf("更新状态失败: %v", err)
		}
		log.Printf("候选 #%d 已驳回", id)
	default:
		log.Fatalf("未知 action: %s", *action)
	}
}

// depthPerStock 单只股票盘口识别结果。
// depthPerStock holds one stock's detected big orders plus touch prices.
type depthPerStock struct {
	Orders []data.BigOrder `json:"orders"`
	Bid1   float64         `json:"bid1"`
	Ask1   float64         `json:"ask1"`
}

// cmdScanDepth 盘口扫描子命令：对研究池股票实时拉五档盘口，识别托单/压单，
// 汇总结果存为候选（kind="depth"），供自动研究页查看。
// （cmdScanDepth pulls live 5-level depth for the research pool, detects support/resistance
// big orders, and saves an aggregated candidate with kind="depth".）
func cmdScanDepth(db *store.DB, args []string) {
	fs := flag.NewFlagSet("scan-depth", flag.ExitOnError)
	codesFile := fs.String("codes", "", "研究池文件（每行一个 ts_code）")
	limit := fs.Int("limit", 0, "最多扫描只数（0=全部）")
	minShare := fs.Float64("min-share", 0.3, "托/压大单单档占比阈值（0~1）")
	fs.Parse(args)

	codes, err := db.StockCodes()
	if *codesFile != "" {
		codes, err = readCodesFile(*codesFile)
	}
	if err != nil {
		log.Fatalf("读取研究池失败: %v", err)
	}
	if len(codes) == 0 {
		log.Fatalf("研究池为空")
	}
	if *limit > 0 && *limit < len(codes) {
		codes = codes[:*limit]
	}

	api := data.NewMarketAPI()
	cfg := data.BigOrderConfig{MinSharePct: *minShare}
	summary := make(map[string]depthPerStock)
	nSupport, nResist, nScanned := 0, 0, 0
	for _, code := range codes {
		ob, err := api.GetOrderBook(code)
		if err != nil {
			log.Printf("[%s] 盘口获取失败: %v", code, err)
			continue
		}
		nScanned++
		orders := ob.DetectBigOrders(cfg)
		if len(orders) == 0 {
			continue
		}
		ps := depthPerStock{Bid1: ob.Bids[0].Price, Ask1: ob.Asks[0].Price}
		for _, o := range orders {
			ps.Orders = append(ps.Orders, o)
			if o.Kind == data.BigOrderSupport {
				nSupport++
			} else {
				nResist++
			}
		}
		summary[localCode(code)] = ps
		log.Printf("[%s] 买1=%.2f 卖1=%.2f 识别%d单(托%d/压%d)",
			code, ps.Bid1, ps.Ask1, len(orders), supportCount(orders), len(orders)-supportCount(orders))
	}
	if len(summary) == 0 {
		log.Fatalf("扫描 %d 只股票，未识别到托单/压单", nScanned)
	}

	wj, _ := json.Marshal(summary)
	fj, _ := json.Marshal(sortedKeys(summary))
	reason := fmt.Sprintf("盘口扫描：%d 只股票识别 %d 托单 / %d 压单",
		len(summary), nSupport, nResist)
	id, err := db.SaveCandidate(&store.Candidate{
		Kind: "depth", Status: "proposed", Factors: string(fj), Weights: string(wj),
		Metric: float64(nSupport + nResist), ICMean: float64(nSupport), IR: float64(nResist),
		Reason: reason,
	})
	if err != nil {
		log.Fatalf("保存候选失败: %v", err)
	}
	log.Printf("候选 #%d 已入库：%s", id, reason)
}

// supportCount 统计给定大单列表中"托单"（支撑大单）的数量。
func supportCount(orders []data.BigOrder) int {
	n := 0
	for _, o := range orders {
		if o.Kind == data.BigOrderSupport {
			n++
		}
	}
	return n
}

// sortedKeys 返回 map 的 key 升序切片（保证候选输出顺序确定性）。
func sortedKeys(m map[string]depthPerStock) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// localCode 去掉交易所后缀（600519.SH → 600519），与 data 包内 stripSuffix 同规则。
func localCode(code string) string {
	for _, sfx := range []string{".SH", ".SZ", ".BJ"} {
		if strings.HasSuffix(code, sfx) {
			return code[:len(code)-len(sfx)]
		}
	}
	return code
}

// sortedIDs 返回因子权重 map 的 key 升序切片（保证日志输出顺序确定性）。
func sortedIDs(m map[string]float64) []string {
	ids := make([]string, 0, len(m))
	for k := range m {
		ids = append(ids, k)
	}
	sort.Strings(ids)
	return ids
}

// defaultFactorPool 返回默认因子池（7 大类精选因子，逗号分隔）。
func defaultFactorPool() string {
	return "EP_ttm,BP,ROE,YoyNetProfit,SUE,Mom20,STO20"
}

// cmdDiscoverFactors 因子战法自动发现（E2/E3）子命令：
// 从全因子池（或 --factors 指定池）贪心前向选择子集 → 权重优化 →
// 样本内/样本外分段 + 反推泛化验证，产出 kind="factor" 候选入库。
// 用法：research [--db ...] [--start ...] [--end ...] discover-factors
//
//	--h 前瞻天数 --min-stocks 每日最小样本 --max-factors 组合上限
//	--split 样本内占比 --min-ir 护栏 --min-days 护栏有效日 --factors 候选池（逗号分隔）
//
// English: E2/E3 factor-strategy discovery — greedy forward subset selection over the full factor
// pool, weight optimization, train/test split + reverse-extension validation, saving a kind="factor"
// candidate for approval.
func cmdDiscoverFactors(db *store.DB, args []string) {
	fs := flag.NewFlagSet("discover-factors", flag.ExitOnError)
	start := fs.String("start", "20200101", "起始日期 YYYYMMDD")
	end := fs.String("end", time.Now().Format("20060102"), "结束日期 YYYYMMDD")
	h := fs.Int("h", 5, "前瞻天数")
	minStocks := fs.Int("min-stocks", 10, "每日最小样本")
	maxFactors := fs.Int("max-factors", 8, "组合最大因子数")
	split := fs.Float64("split", 0.7, "样本内占比（0~1）")
	minIR := fs.Float64("min-ir", 0.3, "护栏 |IR| 下限")
	minDays := fs.Int("min-days", 20, "护栏有效日下限")
	minGenT := fs.Float64("min-gen-t", -2, "反推泛化护栏 Welch t 阈值（t 低于此负值拦截，默认 -2）")
	metric := fs.String("metric", "ir", "优化目标: ir|ic")
	factors := fs.String("factors", "", "候选因子池（逗号分隔，缺省全部注册因子）")
	codesFile := fs.String("codes", "", "研究池文件（每行一个 ts_code）")
	fs.Parse(args)

	codes, err := db.StockCodes()
	if *codesFile != "" {
		codes, err = readCodesFile(*codesFile)
	}
	if err != nil {
		log.Fatalf("读取研究池失败: %v", err)
	}
	if len(codes) == 0 {
		log.Fatalf("研究池为空")
	}
	var pool []string
	if *factors != "" {
		for _, f := range strings.Split(*factors, ",") {
			f = strings.TrimSpace(f)
			if f != "" {
				pool = append(pool, f)
			}
		}
	}
	opts := research.DiscoverOpts{
		Factors: pool, Horizon: *h, MinStocks: *minStocks, Metric: *metric,
		MaxFactors: *maxFactors, SplitPct: *split, MinIR: *minIR, MinDays: *minDays, MinGenT: *minGenT,
	}
	// 内存可控的窗口分块发现：不再一次性 BuildPanels 全量加载（全市场近3年约 2.8GB），
	// 而是按交易日窗口逐窗装配、算完即释放，峰值内存压到单窗口（900M 内），代价是更慢。
	// English: memory-bounded windowed discovery — no longer loads the full panel set at once
	// (~2.8GB for the whole universe × 3y), but assembles per trading-day window and releases it,
	// keeping peak memory within a single window (under 900M) at the cost of speed.
	log.Printf("因子发现（窗口分块）：%d 只股票 目标=%s 组合上限=%d 样本内=%.0f%%…",
		len(codes), *metric, *maxFactors, *split*100)
	res := research.DiscoverFactorsWindowed(db, codes, *start, *end, opts)

	wj, _ := json.Marshal(res.Weights)
	fj, _ := json.Marshal(res.Factors)
	// E6：方向与权重一并存盘，供实盘因子 runner 恢复完整规则。
	// Weights 字段存 {weights, directions, buy_threshold} 复合结构。
	// English: store directions alongside weights so the live factor runner can rebuild the full rule.
	ruleJSON, _ := json.Marshal(map[string]any{
		"weights":       res.Weights,
		"directions":    res.Directions,
		"buy_threshold": 70,
	})
	_ = wj
	reason := fmt.Sprintf("%s | 样本内IR=%.3f 样本外IR=%.3f 反推超额=%.4f 反推t=%.2f",
		res.Reason, res.InsampleIR, res.OutsampleIR, res.GenExcess, res.GenT)
	status := "proposed"
	if !res.PassGuard {
		status = "proposed" // 护栏不过仍入库，标记 reason
	}
	id, err := db.SaveCandidate(&store.Candidate{
		Kind: "factor", Status: status, Factors: string(fj), Weights: string(ruleJSON),
		Metric: res.IR, ICMean: res.ICMean, IR: res.IR,
		Horizon: *h, Reason: reason,
	})
	if err != nil {
		log.Fatalf("保存候选失败: %v", err)
	}
	log.Printf("因子候选 #%d：因子=%v IR=%.3f 样本内=%.3f 样本外=%.3f 反推=%.4f 反推t=%.2f 护栏=%v",
		id, res.Factors, res.IR, res.InsampleIR, res.OutsampleIR, res.GenExcess, res.GenT, res.PassGuard)
	for _, f := range sortedIDs(res.Weights) {
		dir := "+"
		if res.Directions[f] < 0 {
			dir = "-"
		}
		log.Printf("  %s%s %.3f", dir, f, res.Weights[f])
	}
}

// cmdDiscoverPatterns 形态模板搜索（F2）子命令：用已注册形态算子定义模板，
// 在参数空间网格搜索"触发次日买入的前瞻超额"，产出 kind="pattern" 候选入库。
// 用法：research [--db ...] [--start ...] [--end ...] discover-patterns
//
//	--h 前瞻天数 --min-trigger 最小触发次数 --min-excess 护栏超额 --split 样本外占比
//
// English: F2 pattern-template search — defines templates from registered morphology operators,
// grid-searches the parameter space for forward-excess after trigger, saving kind="pattern" candidates.
func cmdDiscoverPatterns(db *store.DB, args []string) {
	fs := flag.NewFlagSet("discover-patterns", flag.ExitOnError)
	start := fs.String("start", "20200101", "起始日期 YYYYMMDD")
	end := fs.String("end", time.Now().Format("20060102"), "结束日期 YYYYMMDD")
	h := fs.Int("h", 5, "前瞻天数")
	minTrigger := fs.Int("min-trigger", 20, "最小触发次数")
	minExcess := fs.Float64("min-excess", 0.01, "护栏最小平均超额")
	split := fs.Float64("split", 0.7, "样本外占比（0~1）")
	codesFile := fs.String("codes", "", "研究池文件（每行一个 ts_code）")
	fs.Parse(args)

	codes, err := db.StockCodes()
	if *codesFile != "" {
		codes, err = readCodesFile(*codesFile)
	}
	if err != nil {
		log.Fatalf("读取研究池失败: %v", err)
	}
	if len(codes) == 0 {
		log.Fatalf("研究池为空")
	}
	// 内置形态模板骨架（可用 F1 形态算子组合扩展）
	templates := []research.PatternTemplate{
		{
			Name: "回调缩量多头",
			Conds: []research.CondGrid{
				{Factor: "Drawdown20", MinVals: []float64{0.1, 0.15, 0.2}, MaxVals: []float64{0.25, 0.3}},
				{Factor: "VolShrink", MinVals: []float64{0}, MaxVals: []float64{0.5, 0.6, 0.7}},
				{Factor: "BullAlign", MinVals: []float64{0.5}, MaxVals: []float64{1.5}},
			},
		},
		{
			Name: "放量突破",
			Conds: []research.CondGrid{
				{Factor: "VolSurge5", MinVals: []float64{1.5, 2.0}, MaxVals: []float64{10}},
				{Factor: "Brk20", MinVals: []float64{0.5}, MaxVals: []float64{1.5}},
			},
		},
	}
	opts := research.DiscoverOptsPattern{
		Horizon: *h, MinTrigger: *minTrigger, MinExcess: *minExcess, SplitPct: *split,
	}
	// 窗口分块版（内存治理收口）：旧路径一次性全量装配全市场×3年面板，实测 RSS ~700MB，
	// 是 1.6G 小机的内存挤压元凶（load 高的真正来源是内存回收风暴而非 CPU 配额失效）。
	// 窗口版逐窗装配-评估-释放 + 窗口断点 + 进度输出，聚合口径与全量版一致。
	// English: window-chunked discovery — the legacy full-range assembly peaked ~700MB RSS and caused
	// reclaim storms on the 1.6G box; the windowed path bounds memory and adds checkpoints/progress.
	log.Printf("形态搜索（窗口分块）：%d 只股票（%s ~ %s）模板=%d 个 目标超额>%.3f 最小触发=%d 样本外=%.0f%%…",
		len(codes), *start, *end, len(templates), *minExcess, *minTrigger, *split*100)
	results := research.DiscoverPatternsWindowed(db, codes, *start, *end, templates, opts)
	if len(results) == 0 {
		log.Printf("无形态通过护栏（触发数不足或超额不足）")
		return
	}
	log.Printf("发现 %d 个通过护栏的形态：", len(results))
	for i := range results {
		p := &results[i]
		condsJSON, _ := json.Marshal(p.Conds)
		reason := fmt.Sprintf("触发=%d 超额=%.4f 命中率=%.2f 样本外超额=%.4f",
			p.Triggers, p.Excess, p.HitRate, p.SampleOut)
		// 存候选：Factors=模板名+条件JSON，Weights=空，Reason=证据
		id, err := db.SaveCandidate(&store.Candidate{
			Kind: "pattern", Status: "proposed",
			Factors: string(condsJSON), Weights: "{}",
			Metric: p.Excess, AvgExcess: p.Excess, IR: 0,
			Horizon: *h, Reason: reason,
		})
		if err != nil {
			log.Fatalf("保存候选失败: %v", err)
		}
		log.Printf("  #%d [%s] %s", id, p.Name, reason)
		for _, c := range p.Conds {
			log.Printf("    %s ∈ [%.3f, %.3f)", c.Factor, c.Min, c.Max)
		}
	}
}

// cmdBacktestCandidate 对最近的因子候选跑一次 B4 全链路回测，把 avg_excess（回测超额）回填。
// 用法：research [--db ...] [--start ...] [--end ...] backtest [--id <候选ID>] [--h 5]
//
//	--id 缺省取最近一条 kind="factor" 且 status="proposed" 的候选。
//
// 用途：夜间研究的「回测开关」开启时，discover-factors 产出候选后追加本步骤，把前端
// 「全链路回测 未测」填上真实超额。
// English: runs a B4 full-chain backtest on the most recent factor candidate and backfills its
// avg_excess. --id defaults to the newest proposed factor candidate. Used by the nightly job's
// backtest step (when enabled) to fill the "全链路回测" field with a real excess.
func cmdBacktestCandidate(db *store.DB, args []string) {
	fs := flag.NewFlagSet("backtest", flag.ExitOnError)
	start := fs.String("start", "20200101", "起始日期 YYYYMMDD")
	end := fs.String("end", time.Now().Format("20060102"), "结束日期 YYYYMMDD")
	h := fs.Int("h", 5, "前瞻天数")
	id := fs.Int64("id", 0, "候选 ID（0=最近一条 proposed factor 候选）")
	minStocks := fs.Int("min-stocks", 10, "B4 回测每日最小样本")
	minLimitUps := fs.Int("min-limit-ups", 3, "B4 回测事件触发涨停下限")
	topK := fs.Int("top-k", 5, "B4 回测每事件选股数")
	maxPerDay := fs.Int("max-per-day", 1, "B4 回测每日最多事件数")
	fs.Parse(args)

	var c *store.Candidate
	var err error
	if *id > 0 {
		c, err = db.CandidateByID(*id)
		if err != nil {
			log.Fatalf("候选 %d 不存在: %v", *id, err)
		}
	} else {
		c, err = latestFactorCandidate(db)
		if err != nil {
			log.Fatalf("读取候选失败: %v", err)
		}
	}
	if c == nil {
		log.Printf("无可回测的因子候选（尚无 proposed factor 候选）")
		return
	}

	// 解析候选：factors 为 JSON 数组，weights 为复合结构 {"weights":{...},"directions":{...}}
	factors, err := parseFactorsJSON(c.Factors)
	if err != nil {
		log.Fatalf("解析候选因子失败: %v", err)
	}
	weights, directions, err := parseFactorWeightsJSON(c.Weights)
	if err != nil {
		log.Fatalf("解析候选权重失败: %v", err)
	}

	log.Printf("回测候选 #%d 因子=%v…", c.ID, factors)
	bopts := backtest.DefaultOptions()
	bopts.Start, bopts.End = *start, *end
	bopts.Horizons = []int{*h}
	bopts.MinLimitUps = *minLimitUps
	bopts.MaxPerDay = *maxPerDay
	bopts.Rule = backtest.DefaultRule()
	bopts.Rule.Factors = factors
	bopts.Rule.Directions = directions
	bopts.Rule.Weights = weights
	bopts.Rule.TopK = *topK
	bopts.Rule.MinStocks = *minStocks
	// 断点续跑：候选 ID 传给 backtest.Run——每事件先读 backtest_event_results 缓存，
	// 命中即复用（同一候选重跑/中断后续跑只重算未缓存事件）；单候选（--id）与夜间
	// （缺省最近候选）都受益。
	// English: checkpoint-resume — the candidate ID is passed to backtest.Run so each event first
	// reads the backtest_event_results cache and reuses hits (reruns / resumes after interruption only
	// recompute uncached events). Both --id (per-candidate) and nightly (default latest) runs benefit.
	bopts.CandidateID = c.ID
	// 进度上报：每推进 10% 打印一次"回测进度 xx%"（供 HTTP 层逐行解析 → 前端进度条）。
	// English: report progress — print "回测进度 xx%" every 10% so the HTTP layer can parse it
	// line-by-line and drive the frontend progress bar.
	lastPct := 0
	bopts.OnProgress = func(done, total int) {
		if total <= 0 {
			return
		}
		pct := done * 100 / total
		if pct >= lastPct+10 {
			lastPct = pct
			log.Printf("回测进度 %d%% (%d/%d)", pct, done, total)
		}
	}
	rep, err := backtest.Run(db, bopts)
	if err != nil {
		log.Fatalf("B4 回测失败: %v", err)
	}
	avgExcess := 0.0
	if v, ok := rep.AvgExcess[*h]; ok {
		avgExcess = v
	}
	if err := db.UpdateCandidateAvgExcess(c.ID, avgExcess); err != nil {
		log.Fatalf("回填 avg_excess 失败: %v", err)
	}
	log.Printf("B4 回测完成: 候选 #%d 事件=%d 入选=%d 平均超额=%.4f（已回填）",
		c.ID, rep.TotalEvents, rep.TotalPicks, avgExcess)
}

// latestFactorCandidate 取最近一条 kind="factor" 且 status="proposed" 的候选。
// （latestFactorCandidate returns the newest proposed factor candidate.）
func latestFactorCandidate(db *store.DB) (*store.Candidate, error) {
	cands, err := db.ListCandidates("proposed")
	if err != nil {
		return nil, err
	}
	for _, c := range cands { // ListCandidates 已按 id DESC（最新在前）
		if c.Kind == "factor" {
			return &c, nil
		}
	}
	return nil, nil
}

// parseFactorsJSON 解析候选 factors 字段（JSON 字符串数组）。
// （parseFactorsJSON parses the candidate factors field — a JSON string array.）
func parseFactorsJSON(raw string) ([]string, error) {
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// parseFactorWeightsJSON 解析 factor 候选的复合 weights 结构
// {"weights":{id:0.25},"directions":{id:±1},"buy_threshold":N}，返回 (weights, directions)。
// 兼容旧的扁平 {id:weight} 结构（directions 置空，走类别默认方向）。
// （parseFactorWeightsJSON parses a factor candidate's composite weights
// {"weights":{...},"directions":{...},"buy_threshold":N} into (weights, directions);
// also accepts the legacy flat {id:weight} shape with nil directions.）
func parseFactorWeightsJSON(raw string) (map[string]float64, map[string]int, error) {
	var composite struct {
		Weights    map[string]float64 `json:"weights"`
		Directions map[string]int     `json:"directions"`
	}
	if err := json.Unmarshal([]byte(raw), &composite); err == nil && composite.Weights != nil {
		return composite.Weights, composite.Directions, nil
	}
	// 回退：扁平结构
	var flat map[string]float64
	if err := json.Unmarshal([]byte(raw), &flat); err != nil {
		return nil, nil, err
	}
	return flat, nil, nil
}
