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

// cmdList 列出候选。
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

func supportCount(orders []data.BigOrder) int {
	n := 0
	for _, o := range orders {
		if o.Kind == data.BigOrderSupport {
			n++
		}
	}
	return n
}

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

func sortedIDs(m map[string]float64) []string {
	ids := make([]string, 0, len(m))
	for k := range m {
		ids = append(ids, k)
	}
	sort.Strings(ids)
	return ids
}

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
	defs := factor.All()
	log.Printf("装配 %d 只股票（%s ~ %s）…", len(codes), *start, *end)
	panels, err := research.BuildPanels(db, codes, *start, *end, defs)
	if err != nil {
		log.Fatalf("装配面板失败: %v", err)
	}
	if len(panels) == 0 {
		log.Fatalf("无有效面板")
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
		MaxFactors: *maxFactors, SplitPct: *split, MinIR: *minIR, MinDays: *minDays,
	}
	log.Printf("因子发现：池=%d 只 目标=%s 组合上限=%d 样本内=%.0f%%…",
		len(opts.Factors), *metric, *maxFactors, *split*100)
	res := research.DiscoverFactors(panels, opts)

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
	reason := fmt.Sprintf("%s | 样本内IR=%.3f 样本外IR=%.3f 反推超额=%.4f",
		res.Reason, res.InsampleIR, res.OutsampleIR, res.GenExcess)
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
	log.Printf("因子候选 #%d：因子=%v IR=%.3f 样本内=%.3f 样本外=%.3f 反推=%.4f 护栏=%v",
		id, res.Factors, res.IR, res.InsampleIR, res.OutsampleIR, res.GenExcess, res.PassGuard)
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
	defs := factor.All()
	log.Printf("装配 %d 只股票（%s ~ %s）…", len(codes), *start, *end)
	panels, err := research.BuildPanels(db, codes, *start, *end, defs)
	if err != nil {
		log.Fatalf("装配面板失败: %v", err)
	}
	if len(panels) == 0 {
		log.Fatalf("无有效面板")
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
	log.Printf("形态搜索：模板=%d 个 目标超额>%.3f 最小触发=%d 样本外=%.0f%%…",
		len(templates), *minExcess, *minTrigger, *split*100)
	results := research.DiscoverPatterns(panels, templates, opts)
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
