// 自动研究闭环（B5）：optimize 优化权重产出候选 → 审批 → 应用。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
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
	action := fs.String("action", "approve", "approve|reject|grayscale")
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
	case "grayscale":
		// §Phase2 自动灰度分级：factor/pattern 候选写入灰度库（paper 观察），不上实盘。
		// English: Phase-2 grayscale — factor/pattern candidates enter the grayscale library for paper observation.
		if c.Kind != "factor" && c.Kind != "pattern" {
			log.Fatalf("灰度仅支持 factor/pattern 候选（kind=%s）", c.Kind)
		}
		if err := research.ApplyGrayscale(dataDir, c); err != nil {
			log.Fatalf("写入灰度库失败: %v", err)
		}
		if err := db.UpdateCandidateStatus(id, research.StatusGrayscale); err != nil {
			log.Fatalf("更新状态失败: %v", err)
		}
		log.Printf("候选 #%d 已进入灰度观察（paper 盘）", id)
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
	// 逐票拉取盘口并识别大单：托单/压单分别计数，记录买1卖1快照。
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

// cmdLifecycleEval §WS-H 维7：灰度晋升评估 CLI——读灰度库 + 分池 paper 逐笔净收益
// （--pools 指定 poolKey→收益数组 JSON，缺省自动从 paper.json 聚合）→ 打印 verdicts →
// promote 判定生成晋升候选（复用现有审批流）。衰退降级由调度器按日指标调用 EvaluateDemote。
// English: WS-H 维7 grayscale promotion eval CLI — reads the grayscale library plus per-pool paper
// net returns (--pools JSON map; auto-aggregated from paper.json when omitted), prints verdicts and
// writes promote decisions as promotion candidates via the existing approval flow.
func cmdLifecycleEval(db *store.DB, dataDir string, args []string) {
	fs := flag.NewFlagSet("lifecycle-eval", flag.ExitOnError)
	pools := fs.String("pools", "", "分池收益 JSON：{\"fac_<id>\": [逐笔净收益%...], ...}（缺省读 paper.json 聚合）")
	paperPath := fs.String("paper", "", "paper.json 路径（缺省 dataDir/paper.json）")
	fs.Parse(args)

	gs, err := research.LoadGrayscaleRules(dataDir)
	if err != nil {
		log.Fatalf("读取灰度库失败（无灰度规则则无需评估）: %v", err)
	}
	poolTrades := map[string][]float64{}
	if *pools != "" {
		b, err := os.ReadFile(*pools)
		if err != nil {
			log.Fatalf("读取 pools JSON 失败: %v", err)
		}
		if err := json.Unmarshal(b, &poolTrades); err != nil {
			log.Fatalf("pools JSON 解析失败: %v", err)
		}
	} else {
		poolTrades = paperPoolReturns(*paperPath, dataDir)
	}
	verds := research.EvaluateGrayscale(&gs, poolTrades, research.PromotionOpts{})
	if len(verds) == 0 {
		log.Printf("灰度库为空，无需评估")
		return
	}
	for _, v := range verds {
		log.Printf("[lifecycle] %s cand=%d verdict=%s trades=%d IR=%.3f 胜率=%.1f%% 盈亏比=%.2f 回撤=%.1f%% 理由=%s",
			v.RuleID, v.CandID, v.Verdict, v.Trades, v.IR, v.WinRate, v.ProfitFactor, v.MaxDrawdownPct, v.Reason)
	}
	ids, err := research.PromotionCandidates(db, &gs, verds)
	if err != nil {
		log.Fatalf("生成晋升候选失败: %v", err)
	}
	for _, id := range ids {
		log.Printf("✅ 晋升候选 #%d 已入库（待人工确认上实盘）", id)
	}
}

// paperPoolReturns 从 paper.json 聚合各策略池的逐笔净收益：
// 卖出成交价/买入成本 → 单笔盈亏%，按池归类（fac_<id>/pat_<id>）。
// 简化实现：卖出记录按 (code) 匹配该池最近一笔买入价计算收益率。
// English: aggregates per-pool per-trade net returns from paper.json. Simplified: for each sell,
// match the pool's most recent buy of the same code and compute the return.
func paperPoolReturns(paperPath, dataDir string) map[string][]float64 {
	path := paperPath
	if path == "" {
		path = filepath.Join(dataDir, "paper.json")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return map[string][]float64{}
	}
	var state struct {
		Trades []struct {
			Code     string  `json:"code"`
			PoolKey  string  `json:"pool_key,omitempty"`
			Strategy string  `json:"strategy"`
			Side     string  `json:"side"`
			Price    float64 `json:"price"`
			Signal   float64 `json:"signal_price,omitempty"`
			Qty      int     `json:"qty"`
			Time     string  `json:"time"`
		} `json:"trades"`
	}
	if err := json.Unmarshal(b, &state); err != nil {
		return map[string][]float64{}
	}
	// poolKey → code → (qty cost basis via most recent buy price)
	lastBuy := map[string]map[string]float64{}
	out := map[string][]float64{}
	for _, t := range state.Trades {
		pool := t.PoolKey
		if pool == "" {
			pool = "other"
		}
		if t.Side == "buy" && t.Qty > 0 {
			if lastBuy[pool] == nil {
				lastBuy[pool] = map[string]float64{}
			}
			lastBuy[pool][t.Code] = t.Price
			continue
		}
		if t.Side == "sell" {
			cost := lastBuy[pool][t.Code]
			if cost > 0 && t.Price > 0 {
				pnl := (t.Price - cost) / cost * 100
				out[pool] = append(out[pool], pnl)
			}
		}
	}
	return out
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
	pool := fs.String("pool", "", "风格子池（''=全池 all|value|growth|quality|size|volatility|momentum|liquidity|mom_liq|value_quality|vol_size 等预设）")
	codesFile := fs.String("codes", "", "研究池文件（每行一个 ts_code）")
	// §S1/C2/C3/C4 多轮发现护栏与去重参数（调度器从 rules.nightly.discover 注入，缺省即旧行为）
	topN := fs.Int("top-n", 1, "S1 排他重跑最优组合数（每变体连续产出前 N 个互异组合）")
	dedupJaccard := fs.Float64("dedup-jaccard", 0.8, "S2 近似去重 Jaccard 阈值（0~1，0 关闭）")
	guardStrong := fs.Float64("guard-strong", 0.45, "C2 强护栏：样本外 IR ≥ 此值 标记 strong")
	guardWeak := fs.Float64("guard-weak", 0.2, "C2 弱护栏：样本外 IR ≥ 此值 标记 weak（否则 reject 不落库）")
	minYrSign := fs.Int("min-yr-sign", 0, "C3a 样本外分年度 IR 符号一致最少年数（0=关闭）")
	changeGate := fs.Bool("change-gate", false, "S3 变化门：与最新 proposed 组合相同且在冷却期内则跳过")
	stalenessDays := fs.Int("staleness-days", 30, "S3 冷却天数")
	hysteresis := fs.Float64("hysteresis", 0.05, "C4 滞回：与最新 applied 组合相同且 |ΔIR|<此值 则跳过")
	// §P1.3 因子相关度去重开关（0=关闭，0~1 阈值）。调度器经 rules.enhance.factor_dedup 注入。
	dedupCorr := fs.Float64("dedup-corr", 0, "P1.3 因子相关度去重阈值（0=关闭，建议 0.7）")
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
	poolFactors := resolveFactorPool(*factors, *pool)
	opts := research.DiscoverOpts{
		Factors: poolFactors, Horizon: *h, MinStocks: *minStocks, Metric: *metric,
		MaxFactors: *maxFactors, SplitPct: *split, MinIR: *minIR, MinDays: *minDays, MinGenT: *minGenT,
		MinYrSign: *minYrSign,
	}
	// §F4 已驳回去重：取历史全部 rejected 的 factor 候选，解析其因子组合注入 ExcludeCombos，
	// 贪心选择时命中即跳过，避免每晚重复生成同一已驳回战法（如 #108~#111 的 Brk60）。
	// English: §F4 — load all rejected factor candidates, parse their factor sets into ExcludeCombos so
	// greedy selection skips already-rejected combos instead of regenerating them every night.
	if rej, err := db.RejectedFactorCombos(); err != nil {
		log.Printf("读取已驳回候选失败（忽略）: %v", err)
	} else if len(rej) > 0 {
		for _, raw := range rej {
			var combo []string
			if json.Unmarshal([]byte(raw), &combo) == nil && len(combo) > 0 {
				opts.ExcludeCombos = append(opts.ExcludeCombos, combo)
			}
		}
		log.Printf("§F4 已驳回组合 %d 组将跳过", len(opts.ExcludeCombos))
	}
	// 内存可控的窗口分块发现：不再一次性 BuildPanels 全量加载（全市场近3年约 2.8GB），
	// 而是按交易日窗口逐窗装配、算完即释放，峰值内存压到单窗口（900M 内），代价是更慢。
	// English: memory-bounded windowed discovery — no longer loads the full panel set at once
	// (~2.8GB for the whole universe × 3y), but assembles per trading-day window and releases it,
	// keeping peak memory within a single window (under 900M) at the cost of speed.
	log.Printf("因子发现（窗口分块）：%d 只股票 目标=%s 组合上限=%d 样本内=%.0f%% 排他topN=%d 子池=%q…",
		len(codes), *metric, *maxFactors, *split*100, *topN, *pool)
	results := research.DiscoverFactorsWindowedN(db, codes, *start, *end, opts, *topN)
	if len(results) == 0 {
		log.Printf("未发现任何候选")
		return
	}
	// §P1.3 因子相关度去重（结果集层面：结果内 + 结果间）。
	// 结果内：逐因子 IC 序列相关聚类，簇内仅保留均值 |IC| 最高者，权重按去相关 IC 重归一
	//（避免一次结果里塞进一堆高度相关的重复 alpha）；结果间：去重后因子组合（排序 key）相同的
	// 结果只保留最早出现的。阈值 0 关闭 → 零行为变化。全部走 WindowFactorIC 有界二分装配，不驻留全量面板。
	// English: §P1.3 factor-correlation dedup at the results level (within + across). Within: cluster
	// each result's factors by IC-series correlation, keep the highest mean |IC| per cluster and
	// re-normalize weights on the de-correlated set (avoid packing one result full of highly related
	// duplicate alphas). Across: keep only the first result whose post-dedup sorted factor set matches.
	// Threshold 0 disables; all IC series computed via the bounded windowed helper.
	if *dedupCorr > 0 && len(results) > 1 {
		union := make(map[string]bool)
		for _, r := range results {
			for _, f := range r.Factors {
				union[f] = true
			}
		}
		fids := make([]string, 0, len(union))
		for f := range union {
			fids = append(fids, f)
		}
		icByFactor := research.WindowFactorIC(db, codes, *start, *end, fids, *h, *minStocks)
		seen := make(map[string]bool, len(results))
		deduped := make([]research.DiscoverResult, 0, len(results))
		for _, r := range results {
			kept, ndirs, nw := research.ApplyDedup(r.Factors, r.Directions, icByFactor, *dedupCorr)
			if nw != nil && len(kept) != len(r.Factors) {
				log.Printf("§P1.3 结果内去重：%v→%v（相关阈值 %.2f），权重重归一", r.Factors, kept, *dedupCorr)
				r.Factors, r.Directions, r.Weights = kept, ndirs, nw
			}
			key := comboKey(r.Factors)
			if seen[key] {
				log.Printf("§P1.3 结果间去重：组合重复跳过 %v", r.Factors)
				continue
			}
			seen[key] = true
			deduped = append(deduped, r)
		}
		if len(deduped) != len(results) {
			log.Printf("§P1.3 结果集去重：%d→%d 条候选", len(results), len(deduped))
			results = deduped
		}
	}
	// S2 去重的状态集合：已提出/已审批/已应用/灰度中 都算占用，杜绝重复副本堆进审批面。
	live := []string{store.CandProposed, store.CandApproved, store.CandApplied, store.CandGrayscale}
	for i := range results {
		res := &results[i]
		if len(res.Factors) == 0 {
			continue
		}
		// S2 落库前全状态去重：精确重复或 Jaccard 近似重复 → 跳过（任务仍 Done，不落 error）。
		if fd, _ := db.ComboExistsLike(res.Factors, live...); fd {
			log.Printf("§S2 组合已存在（proposed/approved/applied/grayscale），跳过：因子=%v", res.Factors)
			continue
		}
		if *dedupJaccard > 0 {
			if nd, _ := db.ComboNearDup(res.Factors, *dedupJaccard, live...); nd {
				log.Printf("§S2 组合近似重复（Jaccard≥%.2f），跳过：因子=%v", *dedupJaccard, res.Factors)
				continue
			}
		}
		// S3 变化门（默认关）：与最新 proposed 组合精确相同且在冷却期内 → 当晚跳过。
		if *changeGate {
			if last, err := db.LatestCandidate("factor", store.CandProposed); err == nil && last != nil && comboEqual(res.Factors, last.Factors) {
				ageDays := int(candidateAgeDays(last.CreatedAt))
				if ageDays < *stalenessDays {
					log.Printf("§S3 变化门：组合与最新 proposed 相同且仍在冷却期(%d天<%d天)，跳过", ageDays, *stalenessDays)
					continue
				}
			}
		}
		// C4 滞回：与最新 applied 组合相同且 |ΔIR|<hysteresis → 跳过（防边际改进噪音顶掉实盘战法）。
		if *hysteresis > 0 {
			if app, err := db.LatestCandidate("factor", store.CandApplied); err == nil && app != nil && comboEqual(res.Factors, app.Factors) {
				if math.Abs(res.IR-app.IR) < *hysteresis {
					log.Printf("§C4 滞回：组合与最新 applied 相同且 |ΔIR|=%.3f<%.3f，跳过", math.Abs(res.IR-app.IR), *hysteresis)
					continue
				}
			}
		}
		// C2 护栏分级（基于样本外 IR）：strong/standard/weak → 落库；reject → 不落库。
		tier := factorGuardTier(res.OutsampleIR, *guardStrong, *minIR, *guardWeak)
		if tier == "reject" {
			log.Printf("§C2 护栏不够（样本外IR=%.3f<%.3f），不落库：因子=%v", res.OutsampleIR, *guardWeak, res.Factors)
			continue
		}
		// C4 参数快照：精确复现审批战法的全量参数 JSON。
		params, _ := json.Marshal(map[string]any{
			"start": *start, "end": *end, "h": *h, "variant": i + 1, "top_n": *topN,
			"pool": *pool, "min_stocks": *minStocks, "max_factors": *maxFactors,
			"split": *split, "min_ir": *minIR, "min_days": *minDays, "min_gen_t": *minGenT,
			"metric": *metric, "guard_strong": *guardStrong, "guard_weak": *guardWeak,
			"min_yr_sign": *minYrSign,
		})
		fj, _ := json.Marshal(res.Factors)
		// E6：方向与权重一并存盘，供实盘因子 runner 恢复完整规则。
		// English: store directions alongside weights so the live factor runner can rebuild the full rule.
		ruleJSON, _ := json.Marshal(map[string]any{
			"weights":       res.Weights,
			"directions":    res.Directions,
			"buy_threshold": 70,
		})
		rank := "冠军"
		if i > 0 {
			rank = fmt.Sprintf("亚军+%d", i)
		}
		reason := fmt.Sprintf("%s | 样本内IR=%.3f 样本外IR=%.3f 反推超额=%.4f 反推t=%.2f 排他第%d",
			res.Reason, res.InsampleIR, res.OutsampleIR, res.GenExcess, res.GenT, i+1)
		if res.YearlyTotalYears > 0 {
			reason += fmt.Sprintf(" 年度符号一致(%d/%d)", res.YearlyConsistentYears, res.YearlyTotalYears)
		}
		if tier == "weak" {
			reason = "[弱护栏-观察] " + reason
		}
		id, err := db.SaveCandidate(&store.Candidate{
			Kind: "factor", Status: store.CandProposed, Guard: tier, Params: string(params),
			Factors: string(fj), Weights: string(ruleJSON),
			Metric: res.IR, ICMean: res.ICMean, IR: res.IR,
			Horizon: *h, Reason: reason,
		})
		if err != nil {
			log.Fatalf("保存候选失败: %v", err)
		}
		log.Printf("因子候选[%s] #%d（%s）：因子=%v IR=%.3f 样本内=%.3f 样本外=%.3f 反推=%.4f 反推t=%.2f",
			rank, id, tier, res.Factors, res.IR, res.InsampleIR, res.OutsampleIR, res.GenExcess, res.GenT)
		_ = rank
		for _, f := range sortedIDs(res.Weights) {
			dir := "+"
			if res.Directions[f] < 0 {
				dir = "-"
			}
			log.Printf("  %s%s %.3f", dir, f, res.Weights[f])
		}
	}
}

// factorGuardTier §C2 护栏分级：基于样本外 IR 判定 strong/standard/weak/reject。
// strong ≥ guardStrong；standard ≥ minIR；weak ≥ guardWeak；其余 reject（不落库）。
// English: C2 guard tiering from out-of-sample IR.
func factorGuardTier(outIR, guardStrong, minIR, guardWeak float64) string {
	switch {
	case outIR >= guardStrong:
		return "strong"
	case outIR >= minIR:
		return "standard"
	case outIR >= guardWeak:
		return "weak"
	}
	return "reject"
}

// resolveFactorPool 结算 --factors/--pool 指定的候选因子池。
// --factors 显式逗号列表优先；否则按 --pool 风格子池（"" 或 "all" 即全池）解析为
// 对应大类因子 ID 列表（支持 "mom_liq" 等多类组合名）。
// English: resolves the candidate factor pool from --factors/--pool — explicit list wins; style pools
// map to category factor IDs (supports multi-category combos like "mom_liq").
func resolveFactorPool(explicit, pool string) []string {
	if explicit != "" {
		var out []string
		for _, f := range strings.Split(explicit, ",") {
			f = strings.TrimSpace(f)
			if f != "" {
				out = append(out, f)
			}
		}
		return out
	}
	if pool == "" || pool == "all" {
		return nil // 全池：由 DiscoverFactorsWindowedN 缺省兜底
	}
	catByName := map[string]factor.Category{
		"value":         factor.CatValue,
		"growth":        factor.CatGrowth,
		"quality":       factor.CatQuality,
		"size":          factor.CatSize,
		"volatility":    factor.CatVolatility,
		"momentum":      factor.CatMomentum,
		"liquidity":     factor.CatLiquidity,
		"mom_liq":       factor.CatMomentum,
		"value_quality": factor.CatValue,
		"vol_size":      factor.CatVolatility,
	}
	// 多类组合名依次展开后按大类过滤去重
	type pair struct {
		name string
		cat  factor.Category
	}
	groups := []pair{
		{"mom_liq", factor.CatMomentum}, {"mom_liq", factor.CatLiquidity},
		{"value_quality", factor.CatValue}, {"value_quality", factor.CatQuality},
		{"vol_size", factor.CatVolatility}, {"vol_size", factor.CatSize},
	}
	var cats []factor.Category
	if c, ok := catByName[pool]; ok && isSingle(pool) {
		cats = append(cats, c)
	} else {
		for _, g := range groups {
			if g.name == pool {
				cats = append(cats, g.cat)
			}
		}
	}
	var out []string
	seen := map[string]bool{}
	for _, c := range cats {
		for _, d := range factor.ByCategory(c) {
			if !seen[d.ID] {
				seen[d.ID] = true
				out = append(out, d.ID)
			}
		}
	}
	return out
}

// isSingle 池名是否为单一大类（非组合名）。English: whether pool is a single category name.
func isSingle(pool string) bool {
	switch pool {
	case "mom_liq", "value_quality", "vol_size":
		return false
	}
	return true
}

// comboEqual 规范化排序后比较两个因子组合（JSON 字符串 vs []string）。
// English: normalized (sorted) equality of a factor set against its persisted JSON form.
func comboEqual(a []string, bJSON string) bool {
	var b []string
	if json.Unmarshal([]byte(bJSON), &b) != nil {
		return false
	}
	return comboKey(a) == comboKey(b)
}

// comboKey 排序拼接为判等/去重 key。English: sorted-join key for equality/dedup.
func comboKey(a []string) string {
	s := append([]string{}, a...)
	sort.Strings(s)
	return strings.Join(s, "\x00")
}

// candidateAgeDays 解析候选创建时间（"2006-01-02 15:04:05"，解析失败按 0 天）。
// 注意 created_at 由 time.Now().Format 写入（本地时区），解析必须用 time.Local 对齐，
// 否则 UTC 解析会把 +8 时区的当日候选错算成负龄期。English: age in days of a
// candidate's created_at string (local-time aware; 0 on parse failure).
func candidateAgeDays(createdAt string) float64 {
	now := time.Now()
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, createdAt, time.Local); err == nil {
			return now.Sub(t).Hours() / 24
		}
	}
	if len(createdAt) == 8 {
		if t, err := time.ParseInLocation("20060102", createdAt, time.Local); err == nil {
			return now.Sub(t).Hours() / 24
		}
	}
	return 0
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
	live := []string{store.CandProposed, store.CandApproved, store.CandApplied, store.CandGrayscale}
	for i := range results {
		p := &results[i]
		condsJSON, _ := json.Marshal(p.Conds)
		// §S2 形态候选去重：以「模板名 + 条件签名」为组合，全状态占用即跳过。
		sig := make([]string, 0, len(p.Conds)+1)
		sig = append(sig, p.Name)
		for _, c := range p.Conds {
			sig = append(sig, fmt.Sprintf("%s[%.3f,%.3f)", c.Factor, c.Min, c.Max))
		}
		if fd, _ := db.ComboExistsLike(sig, live...); fd {
			log.Printf("§S2 形态候选已存在，跳过：[%s]", p.Name)
			continue
		}
		reason := fmt.Sprintf("触发=%d 超额=%.4f 命中率=%.2f 样本外超额=%.4f",
			p.Triggers, p.Excess, p.HitRate, p.SampleOut)
		params, _ := json.Marshal(map[string]any{
			"start": *start, "end": *end, "h": *h,
			"min_trigger": *minTrigger, "min_excess": *minExcess, "split": *split,
		})
		// 存候选：Factors=模板名+条件JSON，Weights=空，Reason=证据；guard 统一 standard
		// （形态搜索已过 MinTrigger/MinExcess 护栏），params 落参数快照（C4）。
		id, err := db.SaveCandidate(&store.Candidate{
			Kind: "pattern", Status: "proposed", Guard: "standard", Params: string(params),
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
	id := fs.Int64("id", 0, "候选 ID（0=按 since/最新一条 proposed factor 候选）")
	since := fs.String("since", "", "只回填此日(YYYYMMDD)以来创建的 proposed factor 候选（A2 配对回测）")
	minStocks := fs.Int("min-stocks", 10, "B4 回测每日最小样本")
	minLimitUps := fs.Int("min-limit-ups", 3, "B4 回测事件触发涨停下限")
	topK := fs.Int("top-k", 5, "B4 回测每事件选股数")
	maxPerDay := fs.Int("max-per-day", 1, "B4 回测每日最多事件数")
	minBtEvents := fs.Int("min-bt-events", 0, "C3b 事件数护栏：回测事件 < 此值时 Reason 追加统计意义弱标注")
	fs.Parse(args)

	// A2 夜间配对回测：--since 指定后回填该日以来全部 proposed factor 候选（逐一回测），
	// 不再只认最近一条——多轮 top-N 产出的冠军/亚军都会被回填 avg_excess。
	cands, err := factorCandidatesForBackfill(db, *id, *since)
	if err != nil {
		log.Fatalf("读取候选失败: %v", err)
	}
	if len(cands) == 0 {
		log.Printf("无可回测的因子候选")
		return
	}
	for _, c := range cands {
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
		// 命中即复用（同一候选重跑/中断后续跑只重算未缓存事件）；多候选逐一回测也受益。
		// English: checkpoint-resume — the candidate ID is passed to backtest.Run so each event first
		// reads the backtest_event_results cache and reuses hits (reruns / resumes after interruption only
		// recompute uncached events).
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
		reason := fmt.Sprintf(" B4事件=%d 入选=%d 平均超额=%.4f（已回填）", rep.TotalEvents, rep.TotalPicks, avgExcess)
		// C3b 事件数护栏：事件不足 → Reason 标注统计意义弱，前端提示。
		if *minBtEvents > 0 && rep.TotalEvents < *minBtEvents {
			reason += fmt.Sprintf(" 事件不足(%d<%d) 统计意义弱", rep.TotalEvents, *minBtEvents)
		}
		if err := db.AppendCandidateReason(c.ID, reason); err != nil {
			log.Fatalf("追加候选 Reason 失败: %v", err)
		}
		log.Printf("B4 回测完成: 候选 #%d 事件=%d 入选=%d 平均超额=%.4f（已回填）",
			c.ID, rep.TotalEvents, rep.TotalPicks, avgExcess)
	}
}

// factorCandidatesForBackfill 选择本次回测要回填的 proposed factor 候选：
// --id 指定 → 单条；--since YYYYMMDD → 该日以来全部 created（A2 配对）；缺省 → 最近一条。
// English: picks proposed factor candidates for excess backfill — fixed id, all created since a date
// (nightly paired mode), or newest.
func factorCandidatesForBackfill(db *store.DB, id int64, since string) ([]store.Candidate, error) {
	if id > 0 {
		c, err := db.CandidateByID(id)
		if err != nil {
			return nil, err
		}
		if c != nil {
			return []store.Candidate{*c}, nil
		}
		return nil, nil
	}
	if since != "" {
		preds, err := db.ProposedFactorCandidatesSince(since)
		if err != nil {
			return nil, err
		}
		return preds, nil
	}
	c, err := latestFactorCandidate(db)
	if err != nil {
		return nil, err
	}
	if c == nil {
		return nil, nil
	}
	return []store.Candidate{*c}, nil
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
