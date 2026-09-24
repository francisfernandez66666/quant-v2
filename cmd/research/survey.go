// survey.go — research strategy-survey 子命令：复权口径修复后的**全战法基线排摸**。
//
// 背景（为什么需要这条命令）：store.HfqBars 的后复权因子从"因子日==行情日等值 JOIN"改为
// 前向填充（§ADJ P0-A）后，CloseHfq 口径整体漂移——此前 ~99.2% 的 股票-交易日 实际按
// 不复权价出数。于是所有以 CloseHfq 派生的量（因子 IC/IR/分层、动量/RSI/MACD、以及据此
// 拟合的战法权重与 buy_threshold、历史期望收益）都建立在旧口径结论上。A/B 实测：fac_1 的
// 四个成分因子三个从 3~4pp 的 5 日分层差塌到 ≈0 或翻号，内置龙头战法期望 +1.13% → -0.29%。
// 本命令把"哪些在实盘的交易战法在新口径下仍然成立"收敛成一条可反复执行的排摸：
//
//	调研对象（枚举全部取自代码，不在命令里手写清单）：
//	  1. 内置形态战法：btreplay.BuiltinStrategies()（与生产 backtest-strategy all 模式同源）；
//	  2. 战法库 applied_factors.json / applied_patterns.json 的**全部**条目（含停用——
//	     库即全貌，运维需要看到整库状态）。
//
// 每条产出：回放触发数/胜率/平均盈亏/盈亏比/期望/持仓（复用 btreplay.RunCollect，与生产
// 回测同一执行路径，不另起口径；刻意不采 夏普/年化/卡玛——逐笔采样口径下无意义）
// + 成分因子当前口径健康度（research.Summarize 的 IC/IR/有效日/分层首末差，**按条目自身 horizon 度量**）
// + stale_basis（条目载入侧派生标记，与战法库红标/失效闸同一判定）+ verdict 结论行。
//
// 输出：stdout ASCII 表（本仓库已知坑：PowerShell→SSH→bash 回传 GBK 字节，中文输出在
// SSH 链路下的 grep 判据全是假绿——表格文本一律 ASCII，中文显示名只进 JSON）
//   - <out>/strategy_survey.json 机读产物
//   - 两条 grep 锚点行：survey_unhealthy=<非 ok 条数>、survey_unsurveyable=<白名单在跑但默认回放
//     集合量不到的形态战法数>（当前 0：momentum 的判据已于 2026-09-24 按实盘语义重写、真进回放；
//     此锚点行必须每轮可见——缺适配器与默认停用两种状态由 notes 逐个 ID 标出，新战法进白名单
//     却没有适配器时它就非零）。
//
// 只读性：排摸对研究库只读。经核，btreplay 回放路径不写任何表
// （backtest_event_results 断点缓存只属于 internal/backtest 候选事件链路，
// survey 不经该路径），store.Open 仅做幂等建表/迁移，与其余 research 子命令一致。
//
// English: one-shot survey of every live-trading strategy under the corrected hfq basis —
// built-in form strategies (enumerated from btreplay, identical to the production replay path)
// plus every entry (enabled AND disabled) of the applied factor/pattern libraries; emits replay
// health, per-component IC/layer health under the current basis, stale-basis flags and a
// verdict, as an ASCII stdout table plus a machine-readable strategy_survey.json.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"quant-trading-v2/internal/btreplay"
	"quant-trading-v2/internal/factor"
	"quant-trading-v2/internal/research"
	"quant-trading-v2/internal/store"
)

// ── JSON 产物结构（verdict/flag 取值为稳定英文枚举，探针/脚本按字面量匹配）──

// surveyComponent 成分因子在当前口径下的健康度（research.Summarize 同源产出）。
// Horizon = 本次度量真正使用的前瞻天数。**必须落进产物**：战法条目各有自己的 horizon（fac_1 是 5、
// fac_117/118 是 10），若一律按全局 --h 量，就会出现「拿 5 日尺子判 10 日战法成分已死」的误判。
type surveyComponent struct {
	Factor        string  `json:"factor"`
	Horizon       int     `json:"horizon"`    // 该成分度量所用前瞻天数（= 所属战法条目自身 horizon）
	Registered    bool    `json:"registered"` // false=因子注册表中不存在（复权修复不影响，但规则已不可复算）
	ICMean        float64 `json:"ic_mean"`
	IR            float64 `json:"ir"`
	ICDays        int     `json:"ic_days"` // 有效日（参与 IC 计算的重平衡日数）
	SpreadPP      float64 `json:"spread_pp"`
	DeadComponent bool    `json:"dead_component"`
}

// surveyReplay 回放统计（字段语义 = btreplay.ReplayStat = 生产回测汇总）。
type surveyReplay struct {
	Signals       int     `json:"signals"`
	Win           int     `json:"win"`
	Loss          int     `json:"loss"`
	WinRatePct    float64 `json:"win_rate_pct"`
	AvgWinPct     float64 `json:"avg_win_pct"`
	AvgLossPct    float64 `json:"avg_loss_pct"`
	ProfitFactor  float64 `json:"profit_factor"`
	ExpectancyPct float64 `json:"expectancy_pct"`
	AvgHoldDays   float64 `json:"avg_hold_days"`
	// PriorExpectancyPct 条目落库时记录的历史期望（fac_* 的候选回测超额 avg_excess，
	// 原始为小数口径，统一 ×100 折算成 %）；形态/内置战法无历史记录 = 0。flipped_sign 判据用。
	PriorExpectancyPct float64 `json:"prior_expectancy_pct"`
}

// surveyRecord 一条被排摸战法的完整结论。
type surveyRecord struct {
	ID       string `json:"id"`   // 稳定 ASCII ID：内置名 / fac_* / pat_*
	Kind     string `json:"kind"` // builtin | factor | pattern
	Name     string `json:"name"` // 显示名（中文仅入 JSON，不上 ASCII 表）
	Enabled  bool   `json:"enabled"`
	AdjBasis string `json:"adj_basis_stored"` // 空串=条目无该字段（落库早于口径打标）

	StaleBasis bool              `json:"stale_basis"` // 存储 adj_basis ≠ 当前 AdjBaselineVersion
	Replay     surveyReplay      `json:"replay"`
	Components []surveyComponent `json:"components,omitempty"`

	Verdict string `json:"verdict"` // ok | no_edge | flipped_sign | no_trigger | stale_params
	Notes   string `json:"notes,omitempty"`
}

// surveyArtifact strategy_survey.json 顶层结构。
type surveyArtifact struct {
	GeneratedAt string `json:"generated_at"`
	// AdjBasisCurrent 当前复权取数口径（research.AdjBaselineVersion），stale 判定基准。
	AdjBasisCurrent string `json:"adj_basis_current"`
	Window          struct {
		Start     string `json:"start"`
		End       string `json:"end"`
		Horizon   int    `json:"horizon"`
		Quantiles int    `json:"quantiles"`
		MinStocks int    `json:"min_stocks"`
		PoolSize  int    `json:"pool_size"`
	} `json:"window"`
	Thresholds struct {
		MinSpreadPP float64 `json:"min_spread_pp"`
	} `json:"thresholds"`
	Records []surveyRecord `json:"records"`
	// Unhealthy = verdict 非 ok 的条数；同步输出为 stdout 的 survey_unhealthy=<count> 锚点行。
	Unhealthy int      `json:"unhealthy"`
	Notes     []string `json:"notes"`
	// Unsurveyable = 实盘白名单在跑、但**默认回放集合量不到它**的形态战法数：要么根本没写适配器
	// （没适配器就没数据，进不了表），要么适配器已实现但默认停用（表里有行，可那一行恒 0 笔，
	// 读成"0 触发=战法没问题"就是盲区）。两种状态由 UnsurveyedLiveFormStatus 分开报出。
	// 必须单独计数并打锚点行：排摸表里"没有这一行"和"这一行没问题"在只看表的运维眼里长得一样。
	// 2026-09-24 §MOMENTUM-LIVE-REPLAY 起当前值为 0（动量判据已按实盘语义重写、真进回放）；
	// 计数归零 ≠ 机制退役——它是下一个"能下单却量不到"的战法唯一的显形通道。
	// UnsurveyableIDs 落进产物便于核对。
	Unsurveyable    int      `json:"unsurveyable"`
	UnsurveyableIDs []string `json:"unsurveyable_ids,omitempty"`
}

// verdict 结论判定规则（优先级自上而下，先命中先取）：
//  1. no_trigger     —— 区间内零触发：门槛/条件在新口径下已打不到任何标的（或战法本身失血），
//     排摸数值无从谈起，最优先暴露；
//  2. flipped_sign   —— 回放期望 < 0 且条目落库时记录的历史期望 > 0：新旧口径下收益符号
//     对调（A/B 实测的"龙头 +1.13% → -0.29%"即此类，最危险）；
//  3. no_edge        —— 回放期望 ≤ 0（无正向历史记录可比对时同样直接判无边际）；
//  4. stale_params   —— 回放期望仍 > 0，但条目 adj_basis 缺失或 ≠ 当前口径：权重/阈值是
//     在别的复权基座上拟合的，数值只能算"幸存"，须经重训复核后才可升为 ok；
//  5. ok             —— 期望为正且口径一致：新基线下仍然成立。
//
// 内置战法无落库 adj_basis（参数=出厂配置即当前口径），不适用规则 4。
func verdict(signals int, expectancyPct, priorPct float64, staleBasis, builtinNoStoredBasis bool) string {
	switch {
	case signals == 0:
		return "no_trigger"
	case expectancyPct < 0 && priorPct > 0:
		return "flipped_sign"
	case expectancyPct <= 0:
		return "no_edge"
	case staleBasis && !builtinNoStoredBasis:
		return "stale_params"
	default:
		return "ok"
	}
}

// cmdStrategySurvey 排摸入口（main.go 的 case "strategy-survey" 分发至此）。
// 研究库连接复用 main 已打开的句柄；回放经 Options.DB 走同一连接，避免同进程双开抢锁。
func cmdStrategySurvey(db *store.DB, dbPath string, args []string) {
	fs := flag.NewFlagSet("strategy-survey", flag.ExitOnError)
	start := fs.String("start", "20230101", "排摸起始日 YYYYMMDD（与 backtest-strategy 缺省一致）")
	end := fs.String("end", "", "排摸结束日 YYYYMMDD（空=今天）")
	horizon := fs.Int("h", 5, "前瞻天数（分层首末差按此前瞻期计算）")
	quantiles := fs.Int("quantiles", 5, "分层数")
	minStocks := fs.Int("min-stocks", 10, "每日最小样本数（IC/分层统计口径）")
	codesFile := fs.String("codes", "", "研究池文件（每行一个 ts_code；空=StockCodes()）")
	outDir := fs.String("out", defaultSurveyOutDir(), "输出目录（写 strategy_survey.json；缺省=系统临时目录，**不允许落在代码仓库工作树内**）")
	applied := fs.String("applied", "", "战法库 JSON 文件或所在目录（空=数据目录，同 LoadEnabledFactorRules 约定）")
	minSpread := fs.Float64("min-spread", 0.5, "死成分阈值：|分层首末差|（百分点）低于此值判 dead_component")
	maxStocks := fs.Int("maxstocks", 500, "回放股票池上限（0=全部；--codes 显式池不受此限）")
	d1 := fs.Float64("d1", 20, "n_shape 的规则 D1 分（与 backtest-strategy 缺省一致，0=不触发）")
	fs.Parse(args)

	if *end == "" {
		*end = today()
	}
	dataDir := resolveAppliedDir(*applied)

	// ── 战法库条目（含停用）──
	// 口径戳直接取条目自身的 AdjBasis（§ADJ-BASIS-2/-2P 后因子侧与形态侧都有该字段）；
	// 旧库文件没有这个键，反序列化后为空串 = "打标机制存在之前拟合" ⇒ 同样判 stale。
	facEntries, err := research.ListAppliedFactorRules(dataDir)
	if err != nil {
		log.Fatalf("读取因子战法库失败（%s）: %v", dataDir, err)
	}
	patEntries, err := research.ListAppliedPatternRules(dataDir)
	if err != nil {
		log.Fatalf("读取形态战法库失败（%s）: %v", dataDir, err)
	}

	// ── 研究池 + 成分因子面板（当前口径健康度）──
	codes, err := db.StockCodes()
	if *codesFile != "" {
		codes, err = readCodesFile(*codesFile)
	}
	if err != nil {
		log.Fatalf("读取研究池失败: %v", err)
	}
	if len(codes) == 0 {
		log.Fatalf("研究池为空（库中无股票且未给 --codes）")
	}

	// 成分因子全集：fac 条目的 Factors + pat 条目条件引用的因子，去重后一次装配面板。
	compIDs := make([]string, 0, 16)
	seen := map[string]bool{}
	addComp := func(id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			compIDs = append(compIDs, id)
		}
	}
	for _, e := range facEntries {
		for _, f := range e.Factors {
			addComp(f)
		}
	}
	for _, e := range patEntries {
		for _, c := range e.Conds {
			addComp(c.Factor)
		}
	}
	defs := make([]factor.Def, 0, len(compIDs))
	for _, id := range compIDs {
		if d, ok := factor.Get(id); ok {
			defs = append(defs, d)
		}
	}
	report := map[string]*research.FactorReport{}
	if len(defs) > 0 {
		// 度量尺子按**条目自身 horizon** 逐档准备：面板只装配一次，Summarize 每档各算一次
		// （前瞻收益在 Summarize 内部按 horizon 现算，多一档只是重复一次分组统计，代价可接受）。
		horizons := []int{*horizon}
		for _, e := range facEntries {
			h := entryHorizon(e.Horizon, *horizon)
			if !containsInt(horizons, h) {
				horizons = append(horizons, h)
			}
		}
		sortInts(horizons)
		log.Printf("装配成分因子面板：%d 因子 × %d 股票（%s ~ %s），度量前瞻档=%v",
			len(defs), len(codes), *start, *end, horizons)
		panels, perr := research.BuildPanels(db, codes, *start, *end, defs)
		if perr != nil {
			log.Fatalf("装配面板失败: %v", perr)
		}
		if len(panels) == 0 {
			log.Printf("[survey] 警告：无有效面板，成分因子健康度将全部记为缺数据")
		}
		for _, h := range horizons {
			for _, d := range defs {
				report[reportKey(d.ID, h)] = research.Summarize(panels, d, *start, *end, h, *quantiles, *minStocks)
			}
		}
	}

	// ── 全战法回放（与生产 backtest-strategy 同一执行路径）──
	ro := &btreplay.Options{
		DBPath:          dbPath,
		DB:              db,
		Start:           *start,
		End:             *end,
		Strategy:        "all", // 内置 + 因子库 + 形态库
		IncludeDisabled: true,  // 排摸看全库（生产回放默认只跑启用条目）
		MaxStocks:       *maxStocks,
		D1Score:         *d1,
		DataDir:         dataDir,
	}
	if *codesFile != "" {
		ro.Codes = codes // 显式池时回放与面板装配钉在同一份清单
		ro.MaxStocks = 0
	}
	stats, rerr := ro.RunCollect()
	if rerr != nil {
		log.Fatalf("全战法回放失败: %v", rerr)
	}
	statByID := map[string]btreplay.ReplayStat{}
	for _, s := range stats {
		statByID[s.ID] = s
	}

	// ── 逐条建记录：内置 → 因子库 → 形态库 ──
	records := make([]surveyRecord, 0, len(btreplay.BuiltinStrategies())+len(facEntries)+len(patEntries))
	for _, id := range btreplay.BuiltinStrategies() {
		st := statByID[id]
		// 名称从**战法名来源**取（适配器自己的 Name()），不依赖回放量出了什么：区间内一笔都没有的
		// 战法名字若从交易行反查就会是空串，那行会被读成坏数据而不是"这轮没机会"。
		name := btreplay.BuiltinDisplayName(id)
		if name == "" {
			name = st.Name // 兜底：适配器构造失败时仍用回放带回的名字，不交空串
		}
		// 内置战法参数=出厂配置（回放适配器 NewManager("") 取默认），无落库口径可言：
		// stale 判恒 false；历史期望无记录（prior=0），flipped_sign 不适用。
		rec := surveyRecord{
			ID: id, Kind: "builtin", Name: name, Enabled: !isDisabledBuiltin(id),
			StaleBasis: false,
			Replay:     replayFromStat(st, 0),
			Verdict:    verdict(st.Signals, st.ExpectancyPct, 0, false, true),
			Notes:      "builtin replay uses factory-default config (config.NewManager(\"\")), not config.json overrides",
		}
		// 近似/停用口径必须跟着这一行进产物：排摸表把"纯日K完整回放"、"靠日内/分钟数据近似"
		// 和"适配器在位但默认不放行（恒 0 笔）"三类混在同一列里，不加标注则同名数字根本不是一个
		// 东西。说明文本由 btreplay.ReplayApproxNote 单点维护，这里只做搬运，不在命令侧重写口径。
		if st.Approx != "" {
			rec.Notes = joinNote(rec.Notes, st.Approx)
		}
		records = append(records, rec)
	}
	for _, e := range facEntries {
		st := statByID[e.ID]
		// stale 直接用载入侧算出的派生标记（与失效闸/战法库红标同一判定，不在此重算一遍口径）。
		stored := e.AdjBasis
		stale := e.StaleAdjBasis
		notes := ""
		if stored == "" {
			notes = "entry has no adj_basis tag — fitted before basis tagging existed"
		}
		comps, deadN := componentHealth(e.Factors, entryHorizon(e.Horizon, *horizon), report, *minSpread)
		r := surveyRecord{
			ID: e.ID, Kind: "factor", Name: e.Name, Enabled: e.Enabled,
			AdjBasis: stored, StaleBasis: stale,
			Replay:     replayFromStat(st, e.Excess*100), // avg_excess 为小数口径，折成 %
			Components: comps,
			Verdict:    verdict(st.Signals, st.ExpectancyPct, e.Excess*100, stale, false),
			Notes:      notes,
		}
		if deadN > 0 {
			r.Notes = joinNote(r.Notes, fmt.Sprintf("%d dead component(s) under current basis", deadN))
		}
		records = append(records, r)
	}
	for _, e := range patEntries {
		st := statByID[e.ID]
		stored := e.AdjBasis
		stale := e.StaleAdjBasis
		condFactors := make([]string, 0, len(e.Conds))
		for _, c := range e.Conds {
			condFactors = append(condFactors, c.Factor)
		}
		// 形态条目无自身 horizon（回放按形态自身规则出场）⇒ 条件因子按全局 --h 度量。
		comps, deadN := componentHealth(condFactors, *horizon, report, *minSpread)
		notes := ""
		if stored == "" {
			notes = "entry has no adj_basis tag — fitted before basis tagging existed"
		}
		r := surveyRecord{
			ID: e.ID, Kind: "pattern", Name: e.Name, Enabled: e.Enabled,
			AdjBasis: stored, StaleBasis: stale,
			Replay:     replayFromStat(st, 0), // 形态条目无落库历史期望，flipped_sign 不适用
			Components: comps,
			Verdict:    verdict(st.Signals, st.ExpectancyPct, 0, stale, false),
			Notes:      notes,
		}
		if deadN > 0 {
			r.Notes = joinNote(r.Notes, fmt.Sprintf("%d dead component(s) under current basis", deadN))
		}
		records = append(records, r)
	}

	art := &surveyArtifact{GeneratedAt: time.Now().Format(time.RFC3339), AdjBasisCurrent: research.AdjBaselineVersion}
	art.Window.Start, art.Window.End = *start, *end
	art.Window.Horizon, art.Window.Quantiles, art.Window.MinStocks = *horizon, *quantiles, *minStocks
	art.Window.PoolSize = len(codes)
	art.Thresholds.MinSpreadPP = *minSpread
	art.Records = records
	sanitizeArtifact(art)
	// 盲区显式化：白名单在跑但默认回放集合量不到的形态战法（无适配器的连 records 行都没有；
	// 有适配器但默认停用的有行、恒 0 笔）。只写成一句 note 的话锚点行 grep 不到它——"没量到"必须
	// 是一个非零计数才能被巡检脚本接住。当前差集为空（动量判据已于 2026-09-24 按实盘语义重写，
	// 见 btreplay.momentumAdapter 注释），但空值的来源必须是"没有盲区"而不是"这条链没接上"：
	// 此段与锚点行**不能删**，它是下一个"能下单却量不到"的战法唯一的显形通道。
	art.UnsurveyableIDs = btreplay.UnsurveyedLiveForms()
	art.Unsurveyable = len(art.UnsurveyableIDs)
	art.Notes = []string{
		unsurveyedNote(art.Unsurveyable, art.UnsurveyableIDs),
		"sharpe/annual/calmar intentionally omitted: meaningless under sampled per-trade replay basis",
		"replay path verified read-only: btreplay writes no tables (backtest_event_results belongs to the internal/backtest candidate chain, unused here)",
	}
	for _, r := range records {
		if r.Verdict != "ok" {
			art.Unhealthy++
		}
	}

	printSurveyTable(art)

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		log.Fatalf("创建输出目录失败: %v", err)
	}
	// 产物绝不落仓库工作树：本轮实测就发生过一次——`research --db … --out …` 的全局 --out 会被
	// 子命令自己的 --out 缺省盖回 ./research_out，于是含战法权重/阈值的 strategy_survey.json
	// 掉进可被 `git add` 的目录里。缺省已改到系统临时目录，这里再加硬闸：显式指进仓库也拒。
	if root, ok := goModuleRoot(*outDir); ok {
		log.Fatalf("拒绝把排摸产物写进代码仓库工作树（输出目录解析到 %s，位于模块根 %s 之内）："+
			"战法库参数属策略资产，掉进工作树就有被 commit 的口子。请用 --out 指定仓库外目录（如 %s）。",
			mustAbs(*outDir), root, defaultSurveyOutDir())
	}
	b, err := json.MarshalIndent(art, "", "  ")
	if err != nil {
		log.Fatalf("序列化排摸产物失败: %v", err)
	}
	out := filepath.Join(*outDir, "strategy_survey.json")
	if err := os.WriteFile(out, b, 0o644); err != nil {
		log.Fatalf("写排摸产物失败: %v", err)
	}
	fmt.Printf("survey_artifact=%s\n", out)
	// 运维锚点行：探针/巡检脚本 grep 这两行即可判断本轮排摸是否需要人工介入。
	// unsurveyable>0 不是失败（那是"量不到"），但必须让人每轮都看见它；2026-09-24 起当前值为 0
	// （动量判据按实盘语义重写后真进了回放集合，历史值 1 = momentum 默认停用）。
	// **差集真为空时 0 也照样打这一行**——脚本按 survey_unsurveyable=
	// 前缀取值，行消失与值为 0 是两回事：前者会被读成"这条链没接"，后者才是"确实没有盲区"。
	fmt.Printf("survey_unhealthy=%d\n", art.Unhealthy)
	fmt.Printf("survey_unsurveyable=%d\n", art.Unsurveyable)
}

// unsurveyedNote 盲区锚点的产物说明文本（两种取值都必须自解释，见调用点注释）。
// 差集非空：逐个列 ID **并带状态**——"没写适配器"要人补代码，"适配器写好但默认停用"要人裁决
// 判据怎么按实盘语义重写，两种处置完全不同，压成一个数字就会派错工。
// 差集为空：明确写出"覆盖面已与白名单对齐、锚点保留"，避免读成"这行统计的是个没接上的空字段"。
// English: the survey note for the live-whitelist-minus-replayed set; each id carries its status
// (missing adapter vs. implemented-but-disabled), and the empty case must still read unambiguously.
func unsurveyedNote(count int, ids []string) string {
	if count == 0 {
		return "0 live-whitelist form strategy ids are outside the default replay set: survey coverage == live form whitelist " +
			"(anchor kept on purpose — a non-zero value here means a new whitelist entry that btreplay does not measure)"
	}
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		status := btreplay.UnsurveyedLiveFormStatus(id)
		if status == "" {
			status = "unknown_status" // 差集与状态表脱钩：如实报 unknown，不猜一个原因糊过去
		}
		parts = append(parts, id+"="+status)
	}
	return fmt.Sprintf("%d live-whitelist form strategy id(s) are NOT measured by the default replay set: %s",
		count, strings.Join(parts, ","))
}

// isDisabledBuiltin 该内置战法是否属"适配器已实现但默认停用"名单（btreplay 单点维护，这里只查表）。
// 用途只有一个：记录行的 enabled 字段如实反映"这轮有没有真跑"，不让运维把"写好但停用"读成"已启用"。
func isDisabledBuiltin(id string) bool {
	for _, d := range btreplay.DefaultDisabledBuiltins() {
		if d == id {
			return true
		}
	}
	return false
}

// replayFromStat 把回放统计装进 JSON 记录；priorPct=条目落库时的历史期望（%），无记录传 0。
func replayFromStat(st btreplay.ReplayStat, priorPct float64) surveyReplay {
	return surveyReplay{
		Signals: st.Signals, Win: st.Win, Loss: st.Loss,
		WinRatePct: st.WinRate, AvgWinPct: st.AvgWinPct, AvgLossPct: st.AvgLossPct,
		ProfitFactor: st.ProfitFactor, ExpectancyPct: st.ExpectancyPct, AvgHoldDays: st.AvgHoldDays,
		PriorExpectancyPct: priorPct,
	}
}

// entryHorizon 条目度量档：落库 horizon 有效就用它（各战法有自己的前瞻期），否则退回全局 --h。
// 零/负值一律视为"未记录"——不能让一个坏值把尺子悄悄换成 0 日前瞻。
func entryHorizon(entryH, globalH int) int {
	if entryH > 0 {
		return entryH
	}
	if globalH > 0 {
		return globalH
	}
	return 5 // 与命令缺省一致的全局兜底
}

// reportKey 成分健康度查表键：因子 ID + 度量前瞻天数（同一因子在 5 日与 10 日是两份结论）。
func reportKey(factorID string, horizon int) string { return factorID + "|h" + strconv.Itoa(horizon) }

// containsInt 线性去重（档位数量级为个位数，不必建集合）。
func containsInt(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

// sortInts 升序原地排序（同上，规模极小，避免为一行日志引入 sort 依赖）。
func sortInts(xs []int) {
	for i := 1; i < len(xs); i++ {
		for j := i; j > 0 && xs[j] < xs[j-1]; j-- {
			xs[j], xs[j-1] = xs[j-1], xs[j]
		}
	}
}

// componentHealth 逐成分因子出当前口径健康度，返回（明细, 死成分计数）。
// horizon = **本条目自己的**前瞻天数（不是全局 --h）：见 surveyComponent.Horizon 注释，
// 用别的尺子量出来的 spread 会把正常战法误判成死成分。
// 分层首末差 = 最高层均值 − 最低层均值（前瞻收益率差，折算成百分点）；
// 层数据缺失/全 NaN（如财务因子在纯价量库区间无样本）同样记 spread=0 判死——
// "没有证据"与"证据为零"在排摸语境下同义：该成分当前不承载任何边际。
func componentHealth(ids []string, horizon int, report map[string]*research.FactorReport, minSpreadPP float64) ([]surveyComponent, int) {
	out := make([]surveyComponent, 0, len(ids))
	dead := 0
	for _, id := range ids {
		c := surveyComponent{Factor: id, Horizon: horizon}
		if r, ok := report[reportKey(id, horizon)]; ok && r != nil {
			c.Registered = true
			c.ICMean, c.IR, c.ICDays = r.ICMean, r.IR, len(r.IC)
			if n := len(r.Layers); n >= 2 {
				lo, hi := r.Layers[0].MeanReturn, r.Layers[n-1].MeanReturn
				if !math.IsNaN(lo) && !math.IsNaN(hi) {
					c.SpreadPP = (hi - lo) * 100 // 前瞻收益为小数口径 → pp
				}
			}
		}
		c.DeadComponent = !c.Registered || math.Abs(c.SpreadPP) < minSpreadPP
		if c.DeadComponent {
			dead++
		}
		out = append(out, c)
	}
	return out, dead
}

// safeNaN NaN/Inf → 0：research 层的缺失语义是 NaN（无有效 IC 日等），
// 但 encoding/json 无法序列化 NaN——缺数据统一落 0，死成分判定在序列化前已完成，不受影响。
func safeNaN(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return v
}

// sanitizeArtifact 落盘前清洗全部浮点字段的 NaN（见 safeNaN 注释）。
func sanitizeArtifact(art *surveyArtifact) {
	for i := range art.Records {
		r := &art.Records[i]
		r.Replay.WinRatePct = safeNaN(r.Replay.WinRatePct)
		r.Replay.AvgWinPct = safeNaN(r.Replay.AvgWinPct)
		r.Replay.AvgLossPct = safeNaN(r.Replay.AvgLossPct)
		r.Replay.ProfitFactor = safeNaN(r.Replay.ProfitFactor)
		r.Replay.ExpectancyPct = safeNaN(r.Replay.ExpectancyPct)
		r.Replay.AvgHoldDays = safeNaN(r.Replay.AvgHoldDays)
		for j := range r.Components {
			c := &r.Components[j]
			c.ICMean = safeNaN(c.ICMean)
			c.IR = safeNaN(c.IR)
			c.SpreadPP = safeNaN(c.SpreadPP)
		}
	}
}

// printSurveyTable 输出 ASCII 汇总表（表头/枚举全 ASCII，见文件头 SSH-GBK 陷阱说明）。
func printSurveyTable(art *surveyArtifact) {
	fmt.Println("=== STRATEGY SURVEY (hfq basis re-check) ===")
	fmt.Printf("basis=%s window=%s..%s h=%d quantiles=%d min_stocks=%d pool=%d min_spread_pp=%.2f\n",
		art.AdjBasisCurrent, art.Window.Start, art.Window.End, art.Window.Horizon,
		art.Window.Quantiles, art.Window.MinStocks, art.Window.PoolSize, art.Thresholds.MinSpreadPP)
	fmt.Printf("%-14s %-8s %-6s %-6s %6s %7s %7s %7s %6s %8s %5s %-6s %s\n",
		"ID", "KIND", "ON", "STALE", "SIGS", "WIN%", "AVG_W%", "AVG_L%", "PF", "EXPR%", "HOLD", "DEAD", "VERDICT")
	for _, r := range art.Records {
		dead := "-"
		if len(r.Components) > 0 {
			d := 0
			for _, c := range r.Components {
				if c.DeadComponent {
					d++
				}
			}
			dead = fmt.Sprintf("%d/%d", d, len(r.Components))
		}
		fmt.Printf("%-14s %-8s %-6v %-6v %6d %7.2f %7.2f %7.2f %6.2f %+8.2f %5.1f %-6s %s\n",
			r.ID, r.Kind, r.Enabled, r.StaleBasis, r.Replay.Signals, r.Replay.WinRatePct,
			r.Replay.AvgWinPct, r.Replay.AvgLossPct, r.Replay.ProfitFactor,
			r.Replay.ExpectancyPct, r.Replay.AvgHoldDays, dead, r.Verdict)
	}
	for _, n := range art.Notes {
		fmt.Printf("note: %s\n", n)
	}
}

// resolveAppliedDir --applied 接受 JSON 文件或其所在目录；空=数据目录
// （与 LoadEnabledFactorRules 的 dataDir 约定同源：btreplay.DefaultDataDir()）。
func resolveAppliedDir(p string) string {
	if p == "" {
		return btreplay.DefaultDataDir()
	}
	if fi, err := os.Stat(p); err == nil && fi.IsDir() {
		return p
	}
	return filepath.Dir(p)
}

// defaultSurveyOutDir 排摸产物缺省目录：系统临时目录下的独立子目录。
// 刻意不再是缺省工作树里的 ./research_out——见写侧硬闸注释。
func defaultSurveyOutDir() string { return filepath.Join(os.TempDir(), "quant-research-survey") }

// goModuleRoot 从给定路径向上找 go.mod，命中即返回模块根。
// 判据用 go.mod 而不是 .git：worktree/子目录/CI 里 .git 可能是文件甚至是符号链接，
// 而能编译出这个二进制的地方一定在 Go 模块内——"落在仓库里"就是要拦的那件事。
func goModuleRoot(p string) (string, bool) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", false
	}
	for i := 0; i < 12; i++ { // 上限防环：只在真实深度内向上走
		if fi, err := os.Stat(filepath.Join(abs, "go.mod")); err == nil && !fi.IsDir() {
			return abs, true
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return "", false
		}
		abs = parent
	}
	return "", false
}

// mustAbs 仅用于错误文案：取绝对路径，失败退回原值。
func mustAbs(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

// joinNote 追加备注（避免空串开头）。
func joinNote(existing, add string) string {
	if existing == "" {
		return add
	}
	return existing + "; " + add
}
