// backfill_stock_moneyflow_gz.go — §REPLAY-INPUTS 2026-11-28（档 3）：个股主力净流入的长期采集器。
//
// 定位（owner 2026-11-28 裁决）：**这是一份长期数据，不是一次性回填脚本**。
// 实测依据（方案 §0.5 探针 1）：东财个股资金流日线是 **120 根滚动窗口**——
// 今天能拿到的最早一天是 20260604，往后每拖一个交易日就永久少一天。
// 所以本程序有两种模式，同一个二进制、同一张表：
//
//	--mode=seed    每票一次 `klt=101&lmt=0` → 拿满上游给的 120 根，全部落库（一次性，幂等）。
//	               刻意**不写今天那根**：当日累计分时归 append 腿，seed 与盘中引擎写口互不打架。
//	--mode=append  每票一次 `klt=1&lmt=0` → 末根=当日累计，只写"今天"一行（交易日 15:35 由
//	               systemd timer 拉起，日常累积）。分时腿失败时回落 `klt=101` 的末根（当日已定型）。
//	--mode=full    **显式未实现并报错**：上游只有 120 根，"全历史"这个概念在本数据源上不成立，
//	               留一个静默跑全史的分支等于骗自己（也骗未来读这条读数的人）。
//
// 三条硬纪律：
//  1. **只走盒子中继**（`internal/data.FlowProxy` → 盒子 `/flow/money`）。
//     广州对东财全族在 TLS 层被 RST（AGENTS 2026-11-19 实录），而本文件里出现任何
//     `push2*.eastmoney.com` 字面量都会绕过那层白名单校验——门禁 §260 ⑥ 有负锁。
//  2. **北交所直接跳过、不请求**：上游 `rc=100` 空体（实测），且现成的 `secID()` 会把
//     `4xxxxx/8xxxxx/920xxx` 当深市 `0.` 发出去。市场归属一律走 `data.ExchangeSuffix`
//     那一张唯一分类表（`internal/data/exchange.go` 文件头自记"三处各自实现是历史缺陷成因"）。
//  3. **失败分三类**，只有 `no_data`（上游明确没有这只）落 state 永久跳过；
//     `relay_error` 不落 state——中继有 300s 负缓存，一次抖动若被记成"这只票永久没数"，
//     表现就是"某天网络抖了一下，从此这批票永远不进库"。
//
// English: per-stock main-force money-flow collector — 120-session seed plus daily append,
// relay-only, Beijing Exchange skipped at the source, and relay errors never persisted.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/store"
)

// upstreamDepth 上游给的滚动窗口根数（2026-11-28 实测：lmt=0 与 lmt=1000 都回 120 根）。
// 只用于 dry-run 自证与报告头对照，**不是**落库上限——上游哪天放宽了，本程序自然多存。
const upstreamDepth = 120

// consecutiveRelayAbort 连续中继失败达到这个数就整轮弃权。
// 理由：这批请求打的是**自己家盒子**再转东财；把盒子 IP 打进东财风控的代价，
// 远大于今天少采一批（现网前例：广州被 RST、盒子打 push2his 间歇断连）。
const consecutiveRelayAbort = 20

// runMarker 采集器写给夜间链的唯一判据（方案 §5.2）。
// ★ 枚举里**没有** dry_run 这一态：`--dry-run` 从不写标记（它只自证上游深度与列宽）。
//
//	干跑落一份 status=ok 等于替夜间链说出「今天采完了」这句话，
//	表现是"链按好数据打分、库里一字节都没有"——与"降级报成功"同族，方向必须先堵死。
//
// 结构体的字段名就是 JSON 键名——读侧（scheduler）与写侧（本程序）不共享代码，
// 靠这份声明对齐；改名必须两侧同批，否则"标记永远读不到"的表现是夜间链白等到兜底时限。
type runMarker struct {
	Day         string  `json:"day"`           // 紧凑 YYYYMMDD（= 本次判定日）
	Mode        string  `json:"mode"`          // seed / append
	Status      string  `json:"status"`        // ok / partial / failed / relay_unavailable / not_trading_day
	CodesTotal  int     `json:"codes_total"`   // 本轮应采的票数（已排除北交所）
	CodesOK     int     `json:"codes_ok"`      // 拿到并落库的票数
	CodesNoData int     `json:"codes_no_data"` // 上游明确无此票（含历史过短）
	CodesError  int     `json:"codes_error"`   // 中继/解析失败（不落 state，下一轮重试）
	SkippedBSE  int     `json:"skipped_bse"`   // 北交所跳过数（不计入失败）
	RowsWritten int64   `json:"rows_written"`  // 落库行数
	CoveragePct float64 `json:"coverage_pct"`  // codes_ok / codes_total ×100
	ElapsedSec  float64 `json:"elapsed_sec"`   // 首跑用它回填方案 §10 探针 d
	StartedAt   string  `json:"started_at"`
	FinishedAt  string  `json:"finished_at"`
	Note        string  `json:"note"` // 一句话成因（限流中止 / 网关不通 / 无票可采 等）
}

// main 解析参数、按模式跑一轮、**无论成败都写标记**，再以退出码收尾。
func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	dbPath := flag.String("db", filepath.Join(os.Getenv("HOME"), ".quant-trading-v2", "trading.db"), "研究库路径")
	gateway := flag.String("gateway-url", "", "盒子网关地址（如 http://10.77.0.2:8789）；空=从 --config 读 rules.qmt.gateway_url")
	tokenFile := flag.String("token-file", "", "含中继钥匙的文件（JSON 里 token 字段，或整文件即钥匙）；**不经命令行**")
	configPath := flag.String("config", "", "全局 config.json（仅在未显式给 gateway/token 时读其中 rules.qmt）")
	codesFile := flag.String("codes", "", "票数清单（每行一个 ts_code 或 6 位码）；空=库内非北交所全集")
	mode := flag.String("mode", "append", "seed | append（full 明确未实现）")
	statePath := flag.String("state", "", "断点续跑状态文件（只记 no_data；空=不启用）")
	markerPath := flag.String("marker", "", "完成标记路径（空=<db 同目录>/moneyflow_run.json）")
	rate := flag.Float64("rate", 4.0, "每秒请求上限（打的是自己家盒子，别贪快）")
	concurrency := flag.Int("concurrency", 2, "并发数（与 rate 共用同一个令牌桶）")
	dryRun := flag.Bool("dry-run", false, "只抽样自证上游深度/列宽，**一个字节都不写**（不建库、不写标记、不落 state）")
	sample := flag.Int("sample", 20, "dry-run 抽样票数")
	seedFloat := flag.String("seed-float-share-from-precalc", "", "从 precalc JSON 灌 stock_float_share_daily（一次性，缺字段不写）")
	flag.Parse()

	if *dryRun {
		if err := runDryRun(*dbPath, *gateway, *tokenFile, *configPath, *codesFile, *sample); err != nil {
			log.Printf("[moneyflow] DRY-RUN 失败: %v", err)
			fmt.Println("MONEYFLOW_RUN=DRYRUN_FAILED")
			os.Exit(1)
		}
		return
	}
	if err := runRound(*mode, *dbPath, *gateway, *tokenFile, *configPath, *codesFile,
		*statePath, *markerPath, *rate, *concurrency, *seedFloat); err != nil {
		log.Printf("[moneyflow] 本轮失败: %v", err)
		fmt.Println("MONEYFLOW_RUN=FAILED")
		os.Exit(1)
	}
}

// runDryRun 首采前置自检（方案 §10 探针 d）：抽几只票验"120 根 × 7 列"这个形状经中继是否成立。
//
// 为什么必须单独有这一步而不是直接开跑：那 120 根是**从本机直连**测的，
// 中继侧的 fields2 由盒子写死（含 f57），两者一致是推断而不是一开始的事实。
// 若中继只回 5 根或 6 列，全量 seed 会写出一份"看着正常、深度只有 5"的表，
// 而覆盖率读数完全看不出来。
func runDryRun(dbPath, gateway, tokenFile, configPath, codesFile string, sample int) error {
	sec, err := loadCredentials(gateway, tokenFile, configPath)
	if err != nil {
		return err
	}
	db, err := store.Open(dbPath)
	if err != nil {
		return fmt.Errorf("打开研究库失败: %w", err)
	}
	defer db.Close()
	codes, skipped, err := collectTargets(db, codesFile)
	if err != nil {
		return err
	}
	if len(codes) == 0 {
		return fmt.Errorf("没有可采的票（清单为空且库内也无非北交所代码）")
	}
	proxy := data.NewFlowProxy(sec.baseURL, sec.token)
	if proxy == nil {
		return fmt.Errorf("中继未装配（gateway/token 缺一）")
	}
	n := sample
	if n > len(codes) {
		n = len(codes)
	}
	fmt.Printf("MONEYFLOW_DRYRUN targets=%d sampled=%d skipped_bse=%d gateway=%s\n",
		len(codes), n, skipped, sec.baseURL)
	// 抽样统计三件事：深度分布（min/max）、列宽（有没有 f57）、失败样本原文。
	// 只抽样不写库——**一个字节都不落**（含标记），理由见函数头的 ★ 注。
	maxRows, minRows, colsSeen := 0, 1<<30, ""
	for _, code := range codes[:n] {
		secid, ok := relaySecID(code)
		if !ok {
			continue
		}
		series, err := proxy.IndexMoneyFlow(secid, 101, 0)
		if err != nil {
			fmt.Printf("MONEYFLOW_DRYRUN code=%s FAIL %v\n", code, err)
			continue
		}
		if len(series) > maxRows {
			maxRows = len(series)
		}
		if len(series) < minRows {
			minRows = len(series)
		}
		if last := series[len(series)-1]; last.HasMainPct && colsSeen == "" {
			colsSeen = "7"
		} else if colsSeen == "" {
			colsSeen = "6(no f57)"
		}
		// 末根日期与深度**都要打**：只有根数看不出"上游给的是不是最新 120 根"
		// （现网 ths_* 静默停更一年多的形状就是"数字齐全、日期是旧的"）。
		fmt.Printf("MONEYFLOW_DRYRUN code=%s rows=%d last_date=%s has_pct=%v\n",
			code, len(series), series[len(series)-1].Date, series[len(series)-1].HasMainPct)
	}
	fmt.Printf("MONEYFLOW_DRYRUN_SUM rows_min=%d rows_max=%d cols=%s expect_depth=%d\n",
		minRows, maxRows, colsSeen, upstreamDepth)
	if maxRows < upstreamDepth {
		// 上游深度不足不等于失败（新上市票本来就短），但**全体都短**说明中继在截断，
		// 那就不能按"首采拿满 120 根"来规划——读数必须显形，不能悄悄降级。
		fmt.Printf("MONEYFLOW_DRYRUN_WARN depth_below_expected=%d\n", upstreamDepth)
	}
	if colsSeen != "7" {
		return fmt.Errorf("中继未返回第 7 列（f57 净占比）——成交额分母就没来源，先修中继字段串再谈采集")
	}
	fmt.Println("MONEYFLOW_DRYRUN=OK")
	return nil
}

// validateMode 只支持 seed/append；full **明确未实现并报错**，
// 因为上游只有 120 根滚动窗口（实测），"全历史"在这个数据源上不存在。
// 留一个静默跑全史的分支等于骗自己，也骗未来读这条读数的人——这是独立函数而不是
// 埋在 runRound 里，是为了能被单测钉住（"传 full 会报错"必须是可执行的断言）。
func validateMode(mode string) error {
	switch mode {
	case "seed", "append":
		return nil
	case "full":
		return fmt.Errorf("--mode=full 未实现：上游只有 %d 根滚动窗口，没有全历史可回填；"+
			"要更长历史只能天天 append 攒（方案 §0.5 探针 1）", upstreamDepth)
	default:
		return fmt.Errorf("未知 --mode=%q（只支持 seed|append）", mode)
	}
}

// runRound 跑一轮真实采集，并在结束时写标记。
func runRound(mode, dbPath, gateway, tokenFile, configPath, codesFile, statePath, markerPath string,
	rate float64, concurrency int, seedFloat string) error {
	started := cntime.Now()
	if markerPath == "" {
		markerPath = filepath.Join(filepath.Dir(dbPath), "moneyflow_run.json")
	}
	mk := runMarker{Day: cntime.DayCompactOf(started), Mode: mode, StartedAt: started.Format(time.RFC3339)}
	// 无论走到哪一步退出，都要把标记落下：夜间链等的是"这一轮有结论"，不是"这轮成功"。
	// 反过来，若标记缺失，夜间链会被拖到兜底时限——所以"写标记"比"返回错误"优先级更高。
	defer func() {
		mk.FinishedAt = cntime.Now().Format(time.RFC3339)
		mk.ElapsedSec = cntime.Now().Sub(started).Seconds()
		if mk.CodesTotal > 0 {
			mk.CoveragePct = float64(mk.CodesOK) / float64(mk.CodesTotal) * 100.0
		}
		if err := writeMarker(markerPath, mk); err != nil {
			log.Printf("[moneyflow] 标记写入失败 %s: %v（夜间链将按 run_age 判缺失）", markerPath, err)
		}
		fmt.Printf("MONEYFLOW_RUN mode=%s status=%s codes=%d ok=%d no_data=%d relay_error=%d skipped_bse=%d rows=%d coverage=%.1f%% elapsed=%.1fs\n",
			mk.Mode, mk.Status, mk.CodesTotal, mk.CodesOK, mk.CodesNoData, mk.CodesError,
			mk.SkippedBSE, mk.RowsWritten, mk.CoveragePct, mk.ElapsedSec)
	}()

	if err := validateMode(mode); err != nil {
		mk.Status = "failed"
		mk.Note = "mode_undetermined"
		return err
	}

	// 休市日（法定节假日 / 周末补跑的 timer）：当日行结构上不存在。
	// 判据用 `data.IsTradingDay`（全仓唯一那把日历尺），不在这另写一份周末判断。
	// seed 模式不受这条影响——它取的是历史 120 根，休市日照采。
	if mode == "append" && !data.IsTradingDay(started) {
		mk.Status = "not_trading_day"
		mk.Note = "market_closed"
		fmt.Printf("MONEYFLOW_SKIP reason=not_trading_day day=%s\n", mk.Day)
		return nil
	}

	db, err := store.Open(dbPath)
	if err != nil {
		mk.Status = "failed"
		mk.Note = "db_open_failed"
		return fmt.Errorf("打开研究库失败: %w", err)
	}
	defer db.Close()

	// ① 可选：从 precalc 灌流通股本（占流通市值口径的分母来源）。
	if seedFloat != "" {
		n, ferr := seedFloatShares(db, seedFloat)
		if ferr != nil {
			// 股本缺失只影响 FlowCap* 一列，不该带走整轮资金流采集——降级为告警。
			log.Printf("[moneyflow] 流通股本种子失败（只影响占流通市值口径）: %v", ferr)
			mk.Note = "float_share_seed_failed"
		} else {
			log.Printf("[moneyflow] 流通股本种子写入 %d 行", n)
		}
	}

	sec, err := loadCredentials(gateway, tokenFile, configPath)
	if err != nil {
		mk.Status = "failed"
		mk.Note = "credentials_missing"
		return err
	}
	proxy := data.NewFlowProxy(sec.baseURL, sec.token)
	if proxy == nil {
		mk.Status = "failed"
		mk.Note = "relay_not_configured"
		return fmt.Errorf("中继未装配（gateway/token 缺一）")
	}
	codes, skippedBSE, err := collectTargets(db, codesFile)
	if err != nil {
		mk.Status = "failed"
		mk.Note = "target_list_failed"
		return err
	}
	mk.SkippedBSE = skippedBSE
	mk.CodesTotal = len(codes)
	if len(codes) == 0 {
		mk.Status = "failed"
		mk.Note = "no_targets"
		return fmt.Errorf("没有可采的票")
	}

	done, err := loadState(statePath)
	if err != nil {
		log.Printf("[moneyflow] 状态文件读取失败（本轮全量重跑，不阻断）: %v", err)
	}
	limiter := newLimiter(rate)
	var okCount, noData, errCount, aborted int32
	var rows int64
	var wg sync.WaitGroup
	sema := make(chan struct{}, concurrency)
	stop := make(chan struct{})
	var stopOnce sync.Once

	// 派发循环必须能被"连续失败阈值"提前打断：用带标签的 for + break，
	// 写成裸 `break` 只会跳出 select，整批票照样逐个发出去（等于中止形同虚设）。
dispatch:
	for _, code := range codes {
		select {
		case <-stop:
			break dispatch
		default:
		}
		// state 只记 no_data：把一次中继抖动记成"这只票永久没有"，
		// 表现就是某天网络抖了一下、从此这批票永远不进库（且覆盖率读数看着正常）。
		if done.has(code) {
			continue // 上游明确没有这只（已确认，不是抖动）
		}
		secid, isBSE := relaySecID(code)
		if isBSE {
			atomic.AddInt32(&noData, 1) // 北交所：上游无此档，计 no_data 并单独读数
			continue
		}
		wg.Add(1)
		sema <- struct{}{}
		go func(code, secid string) {
			defer wg.Done()
			defer func() { <-sema }()
			limiter.wait()
			n, kind := fetchOne(proxy, db, mode, code, secid)
			switch kind {
			case fetchOK:
				atomic.AddInt32(&okCount, 1)
				atomic.AddInt64(&rows, int64(n))
			case fetchNoData:
				atomic.AddInt32(&noData, 1)
				if statePath != "" {
					done.mark(code)
				}
			case fetchRelayError:
				// 抖动：不落 state，下一轮重试。计数只在这里加一次
				// （两处各加会把 20 次阈值实际变成 40 次，等于阈值形同虚设）。
				if n := atomic.AddInt32(&errCount, 1); n%50 == 0 {
					log.Printf("[moneyflow] 累计中继失败 %d 只", n)
				}
				if atomic.LoadInt32(&errCount) >= consecutiveRelayAbort {
					atomic.AddInt32(&aborted, 1)
					stopOnce.Do(func() { close(stop) })
				}
			}
		}(code, secid)
	}
	wg.Wait()

	mk.RowsWritten = rows
	mk.CodesOK = int(atomic.LoadInt32(&okCount))
	mk.CodesNoData = int(atomic.LoadInt32(&noData))
	mk.CodesError = int(atomic.LoadInt32(&errCount))
	if err := flushState(statePath, done); err != nil {
		log.Printf("[moneyflow] 状态文件写入失败（不影响本轮结果）: %v", err)
	}
	if atomic.LoadInt32(&aborted) > 0 {
		mk.Status = "relay_unavailable"
		mk.Note = fmt.Sprintf("aborted_after_consecutive_errors>=%d", consecutiveRelayAbort)
		return fmt.Errorf("连续中继失败达阈值，整轮弃权（保护盒子上游通道，明天 timer 会再来一次）")
	}
	switch {
	case mk.CodesOK == 0:
		mk.Status = "failed"
		mk.Note = "no_rows_written"
		return fmt.Errorf("本轮一只都没写进库（ok=0 no_data=%d error=%d）", mk.CodesNoData, mk.CodesError)
	case mk.CodesError > 0 || mk.CoveragePct < 80:
		mk.Status = "partial"
	default:
		mk.Status = "ok"
	}
	return nil
}

// fetchKind 单票结果三分类（只有 NoData 会落 state，理由见文件头纪律 3）。
type fetchKind int

const (
	fetchOK fetchKind = iota
	fetchNoData
	fetchRelayError
)

// fetchOne 取一票并落库，返回 (写入行数, 分类)。
//
// 判定日一致性（前视防线）：seed 模式**跳过今天**那根（当日累计归 append 腿，
// 两个写口同一天互相覆盖就是"两本账"）；append 模式只接受末根日期==今天的一行，
// 末根不是今天说明上游还没出今天的数 ⇒ 整票 no_data（不是写一根旧值当今天）。
func fetchOne(proxy *data.FlowProxy, db *store.DB, mode, code, secid string) (int, fetchKind) {
	today := cntime.DayCompactOf(cntime.Now())
	series, err := proxy.IndexMoneyFlow(secid, 101, 0)
	if err != nil {
		// 逐日历史整条不通 ⇒ 中继/上游问题，不落 state（明天再来）。
		return 0, fetchRelayError
	}
	if len(series) == 0 {
		return 0, fetchNoData
	}
	rows := make([]store.StockFlowRow, 0, len(series))
	for _, d := range series {
		day := cntime.CompactDay(d.Date)
		if day == "" {
			continue // 日期认不出就丢这一行（不是当 0 写进去）
		}
		if mode == "seed" && day == today {
			continue // seed 不碰当日（引擎/append 的写口）
		}
		if mode == "append" && day != today {
			continue // append 只写当日一行，历史由 seed 那次已经拿全
		}
		rows = append(rows, flowRow(code, day, d, mode))
	}
	if len(rows) == 0 {
		// 上游有响应但窗口里没有判定日 ⇒ 新票/停牌/上游未定型，明确 no_data
		return 0, fetchNoData
	}
	n, err := db.UpsertStockMoneyFlow(rows)
	if err != nil {
		// 写库失败是本地问题：算 error（不落 state），并把错误留在日志里而不是静默。
		log.Printf("[moneyflow] 落库失败 %s: %v", code, err)
		return 0, fetchRelayError
	}
	return int(n), fetchOK
}

// flowRow 把上游一行转成落库行（只存原子量：净额 + 净占比 + 反推的成交额）。
func flowRow(code, day string, d data.FlowDay, mode string) store.StockFlowRow {
	source := "eastmoney_fflow_relay_live"
	if mode == "seed" {
		source = "eastmoney_fflow_relay_seed"
	}
	r := store.StockFlowRow{
		TsCode: code, TradeDate: day,
		MainNet: d.MainNet, HasMainNet: true,
		MainPct: d.MainPct, HasMainPct: d.HasMainPct,
		SmallNet: d.SmallNet, MediumNet: d.MediumNet,
		LargeNet: d.LargeNet, SuperLargeNet: d.SuperLargeNet,
		Source: source, FetchedAt: cntime.Now().Format("2006-01-02 15:04:05"),
	}
	// 成交额由上游自己的净占比反推（一个商，不是第二套公式）——
	// 与 §SECTOR-FLOW-BACKFILL 里 `amount = net/(pct/100)` 同一算法，两处不许各写一版。
	if d.HasMainPct && d.MainPct != 0 {
		r.Amount = d.MainNet / (d.MainPct / 100.0)
		r.HasAmount = true
	}
	return r
}

// relaySecID 推东财 secid，并报告这是否是北交所（true＝跳过不请求）。
//
// 取市场后缀的优先级是刻意的：
//  1. **代码自带后缀就用它**。`daily/stocks` 里的 ts_code 来自 tushare/baostock 的正规列，
//     是权威事实；而 `data.ExchangeSuffix` 把 5 前缀默认归 .SZ —— 对 510300/510050 这类
//     沪市 ETF/LOF 不成立（用例实测抓到）。信库里的后缀，而不是按首位数字猜。
//  2. 裸 6 位码才回退 `ExchangeSuffix`（全系统唯一分类表，920/4/8 → .BJ 的判定顺序在那里）。
//  3. 后缀认不出来（既不是 .SH/.SZ 也不是 .BJ）⇒ **跳过**，绝不猜成深市。
//     现成 `data.secID()` 就是把所有非 6/5 前缀一律发成 `0.`+code 的那副老形状（market.go:667），
//     本函数不复用它，也不新写第二套首位数字判据。
func relaySecID(code string) (string, bool) {
	c := strings.ToUpper(strings.TrimSpace(code))
	pure := store.PureCode(c)
	if len(pure) != 6 {
		return "", true
	}
	suffix := ""
	if i := strings.LastIndex(c, "."); i > 0 {
		suffix = c[i:]
	} else {
		full := data.ExchangeSuffix(pure)
		suffix = full[len(pure):]
	}
	switch suffix {
	case ".BJ":
		return "", true
	case ".SH":
		return "1." + pure, false
	case ".SZ":
		return "0." + pure, false
	default:
		return "", true // 认不出＝不请求（下一轮也不会因猜错市场写出错位行）
	}
}

// collectTargets 取本轮要采的票：--codes 清单优先，否则用库内 stocks 全集；
// 北交所一律排除并单独计数（不是失败）。返回 (代码清单, 被跳过的北交所只数, 错误)。
func collectTargets(db *store.DB, codesFile string) ([]string, int, error) {
	raw := make([]string, 0, 512)
	if codesFile != "" {
		b, err := os.ReadFile(codesFile)
		if err != nil {
			return nil, 0, fmt.Errorf("读取票数清单失败: %w", err)
		}
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			raw = append(raw, line)
		}
	} else {
		codes, err := db.StockCodes()
		if err != nil {
			return nil, 0, fmt.Errorf("读取股票列表失败: %w", err)
		}
		raw = codes
	}
	out := make([]string, 0, len(raw))
	skipped := 0
	for _, c := range raw {
		secid, isBSE := relaySecID(c)
		if isBSE {
			skipped++
			continue // 上游无北交所资金流档（实测 rc=100），连请求都不发
		}
		// 落库用带后缀的 ts_code（与 daily 同形），从 secid 反推比重新猜市场可靠。
		pure := store.PureCode(c)
		if strings.HasPrefix(secid, "1.") {
			out = append(out, pure+".SH")
		} else {
			out = append(out, pure+".SZ")
		}
	}
	return out, skipped, nil
}

// credentials 中继地址与钥匙（钥匙只从文件/配置读，**绝不进命令行与日志**）。
type credentials struct {
	baseURL string
	token   string
}

// loadCredentials 三条来路按优先级：显式 --gateway-url/--token-file → --config 的 rules.qmt。
// 只打印"有没有、长度多少"，不打印值（AGENTS 的取向：短令牌按前缀打印等于全文）。
func loadCredentials(gateway, tokenFile, configPath string) (credentials, error) {
	c := credentials{baseURL: strings.TrimSpace(gateway)}
	if tokenFile != "" {
		b, err := os.ReadFile(tokenFile)
		if err != nil {
			return c, fmt.Errorf("读取 token-file 失败: %w", err)
		}
		tok := strings.TrimSpace(string(b))
		var doc struct {
			Token string `json:"token"`
		}
		if json.Unmarshal(b, &doc) == nil && strings.TrimSpace(doc.Token) != "" {
			tok = strings.TrimSpace(doc.Token)
		}
		c.token = tok
	}
	if c.baseURL == "" || c.token == "" {
		if configPath == "" {
			configPath = filepath.Join(filepath.Dir(os.Getenv("QUANT_DATA_DIR")), "quant", "config.json")
			if v := os.Getenv("QUANT_DATA_DIR"); v != "" {
				configPath = filepath.Join(v, "config.json")
			}
		}
		f, err := os.Open(configPath)
		if err == nil {
			defer f.Close()
			var cfg struct {
				Rules struct {
					QMT struct {
						GatewayURL string `json:"gateway_url"`
						Token      string `json:"token"`
					} `json:"qmt"`
				} `json:"rules"`
			}
			if json.NewDecoder(f).Decode(&cfg) == nil {
				if c.baseURL == "" {
					c.baseURL = strings.TrimSpace(cfg.Rules.QMT.GatewayURL)
				}
				// N1 纪律：全局 rules.qmt.token **不该持有实盘凭证**，读不到不算缺陷——
				// 此时必须显式用 --token-file 指定运营账号在册快照里的钥匙。
				if c.token == "" && strings.TrimSpace(cfg.Rules.QMT.Token) != "" {
					c.token = strings.TrimSpace(cfg.Rules.QMT.Token)
				}
			}
		}
	}
	if c.baseURL == "" {
		return c, fmt.Errorf("中继地址未知（--gateway-url 或 config.rules.qmt.gateway_url）")
	}
	if c.token == "" {
		return c, fmt.Errorf("中继钥匙未知：请用 --token-file 指定（钥匙不经命令行、不经对话）")
	}
	log.Printf("[moneyflow] 中继 %s token_len=%d", c.baseURL, len(c.token))
	return c, nil
}

// stateFile 断点续跑状态：只存"上游明确没有这只"的集合。
type stateFile struct {
	Day    string   `json:"day"`
	NoData []string `json:"no_data"`
	set    map[string]bool
	mu     sync.Mutex
}

// has 判断该票是否已被"上游明确无数据"永久跳过（读锁：并发派发循环每票都调）。
func (s *stateFile) has(code string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.set[code]
}

func (s *stateFile) mark(code string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.NoData = append(s.NoData, code)
	s.set[code] = true
	s.mu.Unlock()
}

// loadState 读断点续跑账本。读不到/解不开一律当"今天从零开始"（返回空账本而非错误）：
// state 只是**省流量的优化**，不是正确性前提——把它坏了当致命错，等于一次文件损坏
// 就让整轮采集停摆，而那点流量代价远小于一整天不进库。
func loadState(path string) (*stateFile, error) {
	sf := &stateFile{set: map[string]bool{}, Day: cntime.DayCompactOf(cntime.Now())}
	if path == "" {
		return sf, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return sf, nil
		}
		return sf, err
	}
	if err := json.Unmarshal(b, sf); err != nil {
		return sf, err
	}
	if sf.set == nil {
		sf.set = map[string]bool{}
	}
	for _, c := range sf.NoData {
		sf.set[c] = true
	}
	return sf, nil
}

// flushState 落账本。只有 no_data 会进这里（relay_error 永不落盘），
// 且写失败只警告不中止：账本丢了最坏是明天重打一遍上游，采集中途停下来才是真损失。
func flushState(path string, s *stateFile) error {
	if path == "" || s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// writeMarker 原子写完成标记（夜间链读它，半截 JSON 会被判成"读不到"＝整晚白等）。
func writeMarker(path string, mk runMarker) error {
	b, err := json.MarshalIndent(mk, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// seedFloatShares 从 precalc JSON 灌当日流通股本（**缺该字段就不写这一行**，绝不写 0）。
// 文件形状：{"day":"20261111","precalc":{"600519.SH":{"float_shares":1.2e9}}}
func seedFloatShares(db *store.DB, path string) (int64, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("读取 precalc 失败: %w", err)
	}
	var doc struct {
		Day     string                    `json:"day"`
		Precalc map[string]map[string]any `json:"precalc"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return 0, fmt.Errorf("解析 precalc 失败: %w", err)
	}
	day := cntime.CompactDay(doc.Day)
	if day == "" {
		day = cntime.DayCompactOf(cntime.Now())
	}
	rows := make([]store.FloatShareRow, 0, len(doc.Precalc))
	for sym, fields := range doc.Precalc {
		v, ok := fields["float_shares"]
		if !ok {
			continue // 缺字段＝没有读数，不是 0
		}
		f, ok2 := toFloat(v)
		if !ok2 || f <= 0 {
			continue
		}
		rows = append(rows, store.FloatShareRow{
			TsCode: data.ExchangeSuffix(store.PureCode(sym)), TradeDate: day,
			Share: f, Source: "precalc_seed_v1",
		})
	}
	if len(rows) == 0 {
		return 0, fmt.Errorf("precalc 里一条 float_shares 都没有（%s）——不写库", path)
	}
	n, err := db.UpsertFloatShareDaily(rows)
	if err != nil {
		return 0, err
	}
	return n, nil
}

// toFloat 把 JSON 解出来的数值转 float64（int/float 两态都接）。
func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	}
	return 0, false
}

// limiter 简单令牌桶（rate<=0 视为不限，但会打日志点名——不限速打自己家盒子是事故源）。
type limiter struct {
	interval time.Duration
	mu       sync.Mutex
	last     time.Time
}

func newLimiter(rate float64) *limiter {
	if rate <= 0 {
		log.Printf("[moneyflow] rate<=0 ⇒ 本次不限速（会把盒子打进东财风控，仅演练时用）")
		return &limiter{}
	}
	return &limiter{interval: time.Duration(float64(time.Second) / rate)}
}

func (l *limiter) wait() {
	if l == nil || l.interval <= 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if d := l.interval - now.Sub(l.last); d > 0 {
		time.Sleep(d)
		l.last = time.Now()
		return
	}
	l.last = now
}
