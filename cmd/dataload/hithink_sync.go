// hithink_sync.go 同花顺（新）主源同步子命令（§HITHINK_DATA_SOURCE_PLAN Phase B0）。
//
// 用法:
//
//	dataload hithink-sync --kind daily-k-10d              # 夜间增量（最近 10 交易日）
//	dataload hithink-sync --kind daily-k --since 20230801 # 存量窗口导入（过滤 10 年全量 dump）
//
// 流程：取 S3 预签名链接（5 分钟有效）→ 落盘 parquet → 流式解析逐行回调 → 批量幂等 upsert ths_daily。
//
// §W7-D（2026-10-09）：日 K 导入收尾多一步 ths_daily 成交额量纲抽检（与 daily 同一把尺子），
// 该表的 amount 口径从此由读数担保而不是注释担保；判红不改判导入结果（详见 cmdHithinkSync 函数头）。
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/store"
)

// defaultHithinkTmpPath 返回 dump parquet 的默认落盘临时路径：必须用 os.TempDir() 跨平台解析，
// 不能写死 "/tmp/..."——那是 Linux（首尔时代）路径，Windows 上不存在，曾导致广州迁移后
// daily-k-10d dump 从未落盘成功（ths_daily 停摆至 0821 的根因，2026-09-16 修复并加回归锁）。
func defaultHithinkTmpPath() string {
	return filepath.Join(os.TempDir(), "hithink_dump.parquet")
}

// cmdHithinkSync 执行一次同花顺（新）日K同步。
//
// §W7-D（2026-10-09 波 7）ths_daily.amount 的口径声明与它的机器保证：
//   - 写侧：parquet 的 `turnover` 列**原样**写入 ths_daily.amount，一条换算都没有。
//     依据是同花顺 API 同名字段的既有口径（internal/data/hithink.go 的
//     `Turnover float64 // 成交额（元）`）；该 dump 列在本仓 schema 注释里曾同时写着
//     "换手率（%）"与"成交额/换手率"（internal/data/hithink_dump.go，本批已改），
//     两处注释互相矛盾时不能挑一个当事实，所以这条口径改由读数担保。
//   - 读数：日 K 导入收尾调 `checkThsAmountScale`（判定体与 daily 完全同一个：
//     均价 = amount/(vol×100) 落在 [1,500] 元带），现网另有一条独立腿
//     `dataload amount-check --table ths_daily`（verify 第 30 探针）。
//     读数判千元/超带/混源 ⇒ P1 日志 + 探针判红，由人决定要不要在写侧加换算；
//   - 为什么不给它配写侧归一：data.AmountScaledTables 是"上游单位已核实为千元"的换算白名单
//     （tushare 日线家族），ths_daily 没有这份核实记录；THS dump 从未在本仓留下抽样实录。
//     把一个猜测的 ×1000 写进库里，比"注释矛盾"更难回滚——历史行没有版本标记可辨。
//     两张集合为什么分开，见 internal/store/amount_scale_probe.go 的 AmountProbedTables 注释。
func cmdHithinkSync(db *store.DB, args []string) {
	fs := flag.NewFlagSet("hithink-sync", flag.ExitOnError)
	kind := fs.String("kind", string(data.HithinkDumpDailyK10d), "种类: daily-k-10d|daily-k|adjustment-factors|pools|anomaly")
	since := fs.String("since", "20230801", "只导入 trade_date >= since 的行（yyyyMMdd）")
	dateF := fs.String("date", "", "pools/anomaly 数据交易日 yyyyMMdd（缺省=今日）")
	// 2026-09-16 修复：原默认 "/tmp/..." 是 Linux（首尔时代）路径，Windows 上不存在
	// → daily-k-10d dump 自广州迁移起从未落盘成功（ths_daily 停摆至 0821 的根因）。
	tmpPath := fs.String("tmp", defaultHithinkTmpPath(), "parquet 落盘临时路径")
	batchSize := fs.Int("batch", 5000, "批量 upsert 行数")
	// §POOL-STAMP 两道守卫的提级/逃生开关（默认值与理由见 poolSyncOpts 注释）。
	allowShadow := fs.Bool("allow-shadow", false, "pools：影子日（与上一有数据日逐行签名相同）允许落库——仅在人工核实上游确有其事后使用")
	enforceStamp := fs.Bool("enforce-stamp", false, "pools：回包时间戳与请求日不符即判失败——data.timestamp 语义现网取证后才提级")
	if err := fs.Parse(args); err != nil {
		log.Fatalf("参数解析失败: %v", err)
	}

	client, err := data.NewHithinkClient()
	if err != nil {
		log.Fatalf("同花顺（新）客户端初始化失败: %v（检查 /etc/quant.env 的 HITHINK_FINANCE_API_KEY）", err)
	}
	// 按 --kind 分发到对应同步子命令（adj_factor / pools / anomaly / valuations / fin-indicators）。
	if *kind == string(data.HithinkDumpAdjFactors) {
		cmdHithinkSyncAdjFactors(client, db, *tmpPath, *since, *batchSize)
		return
	}
	if *kind == "pools" {
		cmdHithinkSyncPools(client, db, *dateF, poolSyncOpts{allowShadow: *allowShadow, stampEnforce: *enforceStamp})
		return
	}
	if *kind == "anomaly" {
		cmdHithinkSyncAnomaly(client, db, *since)
		return
	}
	if *kind == "valuations" {
		// §M-8：降级错误在此转成非零退出（旧版失败批次只打日志，exit 0 骗过调度器）
		if err := cmdHithinkSyncValuations(client, db); err != nil {
			log.Fatalf("[hithink] %v", err)
		}
		return
	}
	if *kind == "fin-indicators" {
		if err := cmdHithinkSyncFinIndicators(client, db, fs.Args()); err != nil {
			log.Fatalf("[hithink] %v", err)
		}
		return
	}

	log.Printf("[hithink] 拉取 %s dump…", *kind)
	size, err := client.DownloadDumpFile(data.HithinkDumpKind(*kind), *tmpPath)
	if err != nil {
		log.Fatalf("dump 下载失败: %v", err)
	}
	log.Printf("[hithink] dump 已落盘 %s（%.1f MB）", *tmpPath, float64(size)/1048576)

	var batch []store.ThsDailyRow
	total := 0
	imported := 0
	if err := data.StreamDailyKParquet(*tmpPath, *since, func(row data.HithinkDailyKRow) error {
		total++
		batch = append(batch, store.ThsDailyRow{
			TsCode: row.ThsCode, TradeDate: row.Date,
			Open: row.Open, High: row.High, Low: row.Low, Close: row.Close,
			// 单位归一：THS 成交量单位为股，baostock 为手（1手=100股）——
			// ÷100 对齐存量口径（2026-08-24 双源对账实录：平安银行 106,085,094 股 vs 1,060,851 手）。
			// §W7-D：amount 这一列原样落库、不做换算，且这个"不做"由收尾抽检证明
			// （口径声明与禁止 ×1000 的理由见本文件 cmdHithinkSync 函数头注释）。
			Vol: row.Volume / 100, Amount: row.Turnover,
		})
		if len(batch) >= *batchSize {
			n, uerr := db.UpsertThsDailyRows(batch)
			if uerr != nil {
				return fmt.Errorf("批量写入失败: %w", uerr)
			}
			imported += int(n)
			batch = batch[:0]
		}
		return nil
	}); err != nil {
		log.Fatalf("parquet 解析/导入失败: %v", err)
	}
	if len(batch) > 0 {
		n, uerr := db.UpsertThsDailyRows(batch)
		if uerr != nil {
			log.Fatalf("尾批写入失败: %v", uerr)
		}
		imported += int(n)
	}
	maxDate, _ := db.ThsDailyMaxDate("")
	count, _ := db.ThsDailyCount(*since)
	log.Printf("[hithink] 同步完成：解析 %d 行，入库影响 %d 行；ths_daily(since=%s) 共 %d 行，最新交易日 %s",
		total, imported, *since, count, maxDate)
	// §W7-D：导入成功后抽一次 ths_daily 的成交额量纲读数（只报不改判，理由见函数头与
	// checkThsAmountScale）。放在这里而不是每个 kind 分支：只有日 K 这条腿写 amount。
	checkThsAmountScale(db)
}

// cmdHithinkSyncAdjFactors 复权因子同步：全量事件 dump → 窗口内事件换算累计 hfq 因子
// → 逐交易日物化入 ths_adj_factor（§HITHINK_DATA_SOURCE_PLAN §6.3）。
//
// 基线衔接：每标的窗口内首个事件之前的累计因子取旧 baostock 表在该日前的最近值
// （无缝续接历史）；窗口内乘数用 THS 事件 + ths_daily 前收盘精确计算。
// 锚定语义与 baostock 一致：因子随时间递增，历史小、当前大。
//
// §ADJ(P0-A 20260922) 两源口径对照（施工核实结论，以本函数代码为准不以文档为准）：
// ths_adj_factor 是【日粒度全覆盖的累计因子】——下面的日期循环对
// db.ThsDatesSince(code, since) 返回的**每一个交易日**都 append 一行（cur 只在跨过
// 除权事件时乘一次乘数，之后原样续写到下一个事件日），与 baostock 侧
// adj_factor 的【事件稀疏点】形态（trade_date=分红实施日，一年 0~3 行）根本不同。
// ⇒ internal/store/store.go 的 thsHfqBars 用等值 JOIN 读取是**正确**的，不需要前向填充；
// 而旧表路径必须前向填充。若日后把本函数改成"只写事件日"，thsHfqBars 那条等值 JOIN
// 会静默退化成不复权——改动务必同步跟随（该约束已在 store.go 注释里留证据行号）。
// 已知覆盖缺口（门禁放行前须补，本次只登记不修）：无事件的标的在下面的
// `if len(evs) == 0 { continue }`（cmdHithinkSyncAdjFactors 内）直接跳过，与上方
// "无事件标的也要物化恒等基线行"的注释不符；且日期只覆盖 since 之后。
// 现状影响有限：HfqBars 的路由门禁按 ts_code 查 ths_adj_factor 有无行，无行即回退旧表。
func cmdHithinkSyncAdjFactors(client *data.HithinkClient, db *store.DB, tmpPath, since string, batchSize int) {
	log.Printf("[hithink] 拉取 adjustment-factors 全量 dump…")
	size, err := client.DownloadDumpFile(data.HithinkDumpAdjFactors, tmpPath)
	if err != nil {
		log.Fatalf("dump 下载失败: %v", err)
	}
	log.Printf("[hithink] dump 已落盘 %s（%.1f MB）", tmpPath, float64(size)/1048576)

	// 收集窗口内事件，按标的分组（ex_date 升序）
	events := map[string][]data.HithinkAdjEvent{}
	nEvents := 0
	if err := data.StreamAdjFactorsParquet(tmpPath, func(ev data.HithinkAdjEvent) error {
		if ev.ExDate >= since {
			events[ev.ThsCode] = append(events[ev.ThsCode], ev)
			nEvents++
		}
		return nil
	}); err != nil {
		log.Fatalf("parquet 解析失败: %v", err)
	}
	log.Printf("[hithink] 窗口(%s起)内事件 %d 个，涉及标的 %d 只", since, nEvents, len(events))

	var batch []store.ThsAdjFactorRow
	total := 0
	flush := func() error {
		n, uerr := db.UpsertThsAdjFactorRows(batch)
		batch = batch[:0]
		total += int(n)
		return uerr
	}

	// 遍历 ths_daily 全部标的：有事件的做乘数展开，无事件的物化恒等基线行——
	// 保证任一代码都有完整因子覆盖（引擎级原子切换的前提）。
	allCodes, cerr := db.ThsAllCodes()
	if cerr != nil {
		log.Fatalf("读 ths 标的清单失败: %v", cerr)
	}
	for _, code := range allCodes {
		evs := events[code]
		if len(evs) == 0 {
			continue // 2026-09-16 修复：窗口内无复权事件的标的（占绝大多数）此前直接 evs[0] panic
		}
		sort.Slice(evs, func(i, j int) bool { return evs[i].ExDate < evs[j].ExDate })
		dates, derr := db.ThsDatesSince(code, since)
		if derr != nil || len(dates) == 0 {
			continue // 该标的无本地行情日期，无法展开
		}
		firstDate := dates[0]
		// 首个事件前一日 → 旧表基线因子（无缝续接 baostock 历史）
		pre := prevDayBefore(firstDate, evs[0].ExDate)
		base, ok, berr := db.LegacyAdjFactorAt(code, pre)
		if berr != nil {
			log.Printf("[hithink] %s 基线读取失败: %v", code, berr)
			continue
		}
		if !ok {
			base = 1 // 无历史基线（如次新股），从 1 起算
		}
		evIdx := 0
		cur := base
		for _, dt := range dates {
			for evIdx < len(evs) && evs[evIdx].ExDate <= dt {
				ev := evs[evIdx]
				if pc, pok, _ := db.ThsCloseBefore(code, ev.ExDate); pok {
					cur *= data.AdjMultiplier(pc, ev.Dividend, ev.BonusRatio, ev.AllotRatio, ev.AllotPrice)
				}
				evIdx++
			}
			batch = append(batch, store.ThsAdjFactorRow{TsCode: code, TradeDate: dt, Factor: cur})
			if len(batch) >= batchSize {
				if ferr := flush(); ferr != nil {
					log.Fatalf("批量写入失败: %v", ferr)
				}
			}
		}
	}
	if err := flush(); err != nil {
		log.Fatalf("尾批写入失败: %v", err)
	}
	log.Printf("[hithink] 复权因子同步完成：物化 %d 行（ths_adj_factor）", total)
}

// prevDayBefore 返回 anchor 与 first 中较小者再往前推的参照日（取基线因子的查询上界）。
// 语义：取"窗口起点之前"的旧表最近因子；若首事件晚于窗口起点则用窗口起点前一天。
func prevDayBefore(firstDate, firstEvent string) string {
	if firstEvent < firstDate {
		return prevStr(firstEvent)
	}
	return prevStr(firstDate)
}

// prevStr yyyyMMdd 字符串减一天（用 time 标准换算，跨月安全）。
func prevStr(d string) string {
	t, err := time.ParseInLocation("20060102", d, time.Local)
	if err != nil {
		return d
	}
	return t.AddDate(0, 0, -1).Format("20060102")
}

// ── 盘口三池 + 连板天梯 + 异动（§P1 盘口升级）──

// poolSyncOpts 盘口同步的两道守卫开关（§POOL-STAMP，2026-10-12）。
//
// 两个默认值不是随手取的，各自对应一条现网取证强度：
//   - allowShadow=false：影子日（回包与上一有数据日逐行签名完全相同）**默认拦停**。
//     依据是 2026-10-12 实测的那 72 行——写进 20261012 的池子与 20261009 逐字节相同，
//     那一天的涨停池从未发生。这一型不需要新前提就能判。
//   - stampEnforce=false：回包时间戳与请求日不符**默认只记读数、不拦停**。
//     原因是池子回包里 data.timestamp 到底是"数据日"还是"请求时刻"尚未在现网取证
//     （internal/data/hithink_flow.go 把另一族端点的 Timestamp 当数据时刻用，只是旁证）。
//     把一个未取证的语义抬成拦停＝造一条永远判红的腿；先出读数，读数归因后再由 --enforce-stamp 提级。
type poolSyncOpts struct {
	allowShadow  bool // true＝影子日照常落库（人工确认为真数据后的逃生口）
	stampEnforce bool // true＝回包日期与请求日不符直接判失败（现网取证后才提级）
}

// defaultPoolSyncOpts 盘后同步链的缺省守卫组合：影子日拦停、时间戳只记读数。
func defaultPoolSyncOpts() poolSyncOpts { return poolSyncOpts{} }

// cmdHithinkSyncPools 盘后三池+连板天梯入库。
// dateFlag 为 yyyyMMdd；缺省=今日（API 缺省当日）。
func cmdHithinkSyncPools(client *data.HithinkClient, db *store.DB, dateFlag string, opts poolSyncOpts) {
	res, err := syncPoolsForDate(client, db, dateFlag, opts)
	if err != nil {
		log.Fatalf("盘口同步失败: %v", err)
	}
	tradeDate := dateFlag
	if tradeDate == "" {
		tradeDate = cntime.DayCompactOf(time.Now())
	}
	// 连板天梯为当日快照接口（无历史参数），仅在同步"今日"时追加。
	if tradeDate == cntime.DayCompactOf(time.Now()) {
		lad, lerr := client.LimitUpLadderEntries()
		if lerr != nil {
			log.Fatalf("连板天梯拉取失败: %v", lerr)
		}
		ladRows := make([]store.ThsLadderRow, 0, len(lad))
		for _, e := range lad {
			ladRows = append(ladRows, store.ThsLadderRow{TradeDate: e.TradeDate,
				BoardNum: e.BoardNum, TsCode: e.ThsCode, Name: e.Name,
				SealNextDay: e.SealNextDay, SignLevel: e.SignLevel})
		}
		n4, err := db.UpsertThsLadder(ladRows)
		if err != nil {
			log.Fatalf("天梯写入失败: %v", err)
		}
		log.Printf("[hithink] 连板天梯 %d 行", n4)
	}
	cnt, _ := db.LimitUpCountOnDate(tradeDate)
	// deleted/written 两个数都要打：只有 written 就分不清"池子缩了"与"根本没动"
	// （§POOL-SNAPSHOT：整日替换的两侧读数，同 PoolReplaceResult 的注释）。
	log.Printf("[hithink] 盘口同步完成(trade_date=%s)：涨停池 deleted=%d written=%d(当日家数 %d)",
		tradeDate, res.Deleted, res.Written, cnt)
}

// poolStampReadout 一次池子拉取的"日期同源"读数（三池各一行，字段名固定供门禁 grep 与现网归因）。
type poolStampReadout struct {
	Pool       string // limit_up|limit_down|break_pool
	Requested  string // 请求的交易日 yyyyMMdd
	PayloadMS  int64  // 上游 data.timestamp（毫秒；0＝未给出）
	PayloadDay string // 时间戳换成的北京日 yyyyMMdd（""＝无从判定）
	Match      string // yes|no|unknown（unknown＝时间戳缺失或越界，不许当成 yes）
	Rows       int    // 本次回包行数
}

// newPoolStampReadout 从池子回包构造读数。Match 的三态是判据本体：
// unknown 与 no 的含义相反——unknown 是"这条证据不存在"，no 是"这条证据说不是同一天"。
// 把 unknown 折进 no 会让上游一改响应形状就集体判红。
func newPoolStampReadout(pool, requested string, p data.HithinkPool) poolStampReadout {
	r := poolStampReadout{Pool: pool, Requested: requested, PayloadMS: p.TimestampMs,
		PayloadDay: data.PoolDayCompact(p.TimestampMs), Rows: len(p.Items)}
	switch {
	case r.PayloadDay == "":
		r.Match = "unknown"
	case r.PayloadDay == requested:
		r.Match = "yes"
	default:
		r.Match = "no"
	}
	return r
}

// String 一行的固定形状：键=值 空格分隔，现网日志与门禁都按这条抓。
func (r poolStampReadout) String() string {
	return fmt.Sprintf("pool=%s requested=%s payload_ms=%d payload_day=%s match=%s rows=%d",
		r.Pool, r.Requested, r.PayloadMS, dayOr(r.PayloadDay), r.Match, r.Rows)
}

// dayOr 空日期在读数里打成 "<none>"，不能让 `payload_day=` 后面空着——
// 空值会被 grep 成"这行没有这个键"，等于把读数藏起来。
func dayOr(s string) string {
	if s == "" {
		return "<none>"
	}
	return s
}

// limitUpRowsFromItems 把涨停池回包折成待落库行集——**构造口径的唯一定义处**：
// 同步链与只读探针 `pool-probe` 都走这里，否则探针验的行集与真要写的行集不同形，影子日判据
// 在探针里"通过"到了现网就不算数（§深验证审计：判据必须跑在交付同一条代码上）。
// English: the single item→row builder shared by the sync chain and the read-only pool probe.
func limitUpRowsFromItems(items []data.HithinkLimitUpItem, tradeDate string) []store.ThsLimitUpRow {
	rows := make([]store.ThsLimitUpRow, 0, len(items))
	for _, it := range items {
		rows = append(rows, store.ThsLimitUpRow{
			TradeDate: tradeDate, TsCode: it.ThsCode, Name: it.Name,
			IsST: it.IsST, IsNew: it.IsNew, Price: it.LastPrice,
			PctChg: it.PriceChangeRatioPct, FirstSealTime: it.LimitUpTime,
			ContinueCnt: it.ContinueDayCnt, ContinueText: it.ContinueDayText,
			LimitReason: deref(it.LimitUpReason), SealMoney: it.SealMoney,
			MaxSealMoney: it.MaxSealMoney,
		})
	}
	return rows
}

// syncPoolsForDate 三池（涨停/跌停/炸板）单日同步核心：拉取 + 日期同源校验 + 整日快照替换。
// 返回涨停池的替换读数（deleted/written），供调用方打总结。
// 错误一律上抛（供 §ENH-A ths-backfill 逐日循环容错续跑；pools 子命令外层转 Fatal）。
//
// §POOL-STAMP／§POOL-SNAPSHOT（2026-10-12）三处口径变化：
//  1. 落库从 INSERT OR REPLACE 的"只增不删"改成整日替换——旧写法让已出池的股票留在同一天，
//     涨停家数/最高连板/炸板率单向漂移，而第二次同步仍是 rc=0；
//  2. 空回包、缺 trade_date、跨多个交易日三种行集在 DELETE **之前**返回错误，库一个字节不动
//     （§降级链会兜死分支：失败不许伪装成"产物为空"）；
//  3. 每池打一行日期同源读数，涨停池另加影子日判据（与库内上一有数据日逐行签名比对）。
//
// English: per-date three-pool sync — stamp readout, phantom-day guard, whole-day snapshot replace.
func syncPoolsForDate(client *data.HithinkClient, db *store.DB, dateFlag string, opts poolSyncOpts) (store.PoolReplaceResult, error) {
	tradeDate := dateFlag
	if tradeDate == "" {
		tradeDate = cntime.DayCompactOf(time.Now())
	}
	var dateMs int64
	if t, err := time.ParseInLocation("20060102", tradeDate, time.Local); err == nil {
		dateMs = t.UnixMilli()
	}

	up, err := client.LimitUpPoolStamped(dateMs)
	if err != nil {
		return store.PoolReplaceResult{}, fmt.Errorf("涨停池拉取失败(%s): %w", tradeDate, err)
	}
	ro := newPoolStampReadout("limit_up", tradeDate, up)
	log.Printf("[hithink] %s", ro.String())
	if ro.Match == "no" && opts.stampEnforce {
		return store.PoolReplaceResult{},
			fmt.Errorf("涨停池日期同源判红(%s)：回包时间戳落在 %s 而非请求日 ⇒ 上游给的不是这一天的数据（--enforce-stamp）",
				tradeDate, ro.PayloadDay)
	}
	luRows := limitUpRowsFromItems(up.Items, tradeDate)
	// 影子日判据只在"有上一日可比"时成立；库内此前无数据时 identical 恒 false＝无从判定，
	// 这条不是通过，但也没有别的证据可用——由下面的 payload_day 读数继续追。
	shadowOf, same, serr := db.LimitUpShadowOf(tradeDate, luRows)
	if serr != nil {
		return store.PoolReplaceResult{}, fmt.Errorf("涨停池影子日判据读取失败(%s): %w", tradeDate, serr)
	}
	if same {
		log.Printf("[hithink] pool=limit_up requested=%s shadow_of=%s shadow=same rows=%d ⇒ 幻影日（这一包与上一有数据日逐行签名完全相同）",
			tradeDate, shadowOf, len(luRows))
		if !opts.allowShadow {
			return store.PoolReplaceResult{},
				fmt.Errorf("涨停池幻影日拦停(%s)：整包与 %s 的已落库行集逐行签名相同，落库等于把没发生过的交易日写成真数据；"+
					"人工核实上游确有其事可加 --allow-shadow 强落（shadow_of=%s）", tradeDate, shadowOf, shadowOf)
		}
		log.Printf("[hithink] pool=limit_up --allow-shadow 生效：影子日照常落库(shadow_of=%s)", shadowOf)
	}
	res, err := db.ReplaceThsLimitUps(luRows)
	if err != nil {
		return res, fmt.Errorf("涨停池写入失败(%s): %w", tradeDate, err)
	}
	log.Printf("[hithink] pool=limit_up replaced trade_date=%s deleted=%d written=%d", res.TradeDate, res.Deleted, res.Written)
	if res.Deleted > res.Written {
		// 收缩读数只记不拦：整日替换的语义就是"这一天的池子以本次回包为准"，
		// 而"少了一半"既可能是上游留存截断（--force 重拉历史日会撞上），也可能是当日真有人出池。
		// 判红需要的是能把这两种成因分开的那一行 deleted/written，不是第二个阈值。
		log.Printf("[hithink][WARN] pool=limit_up 当日池子收缩 trade_date=%s deleted=%d written=%d"+
			" ⇒ 情绪链的家数/最高连板会跟着变小：重拉历史日请核对上游是否仍留全该日，人工同步今日属正常",
			res.TradeDate, res.Deleted, res.Written)
	}

	dn, err := client.LimitDownPoolStamped(dateMs)
	if err != nil {
		return res, fmt.Errorf("跌停池拉取失败(%s): %w", tradeDate, err)
	}
	log.Printf("[hithink] %s", newPoolStampReadout("limit_down", tradeDate, dn).String())
	dnMap := make(map[string]store.ThsPoolSimple, len(dn.Items))
	for _, it := range dn.Items {
		dnMap[it.ThsCode] = store.ThsPoolSimple{Name: it.Name, Price: it.LastPrice,
			PctChg: it.PriceChangeRatioPct, TurnoverRatioPct: it.TurnoverRatioPct}
	}
	// 两侧读数（deleted/written）由 helper 自己打：空回包那一支没有 deleted/written 可报，
	// 把日志留在调用方会让「跳过了」与「替换了」共用一行格式（旧形态，且只能靠 _ = res 消警告）。
	if _, err := replaceOrSkipSimplePool(db, "ths_limit_down_daily", "limit_down", tradeDate, dnMap); err != nil {
		return res, fmt.Errorf("跌停池写入失败(%s): %w", tradeDate, err)
	}

	bk, err := client.LimitBreakPoolStamped(dateMs)
	if err != nil {
		return res, fmt.Errorf("炸板池拉取失败(%s): %w", tradeDate, err)
	}
	log.Printf("[hithink] %s", newPoolStampReadout("break_pool", tradeDate, bk).String())
	bkMap := make(map[string]store.ThsPoolSimple, len(bk.Items))
	for _, it := range bk.Items {
		bkMap[it.ThsCode] = store.ThsPoolSimple{Name: it.Name, Price: it.LastPrice,
			PctChg: it.PriceChangeRatioPct, OpenTimes: it.OpenTimes,
			TurnoverRatioPct: it.TurnoverRatioPct, Turnover: it.Turnover}
	}
	if _, err := replaceOrSkipSimplePool(db, "ths_break_pool_daily", "break_pool", tradeDate, bkMap); err != nil {
		return res, fmt.Errorf("炸板池写入失败(%s): %w", tradeDate, err)
	}
	return res, nil
}

// replaceOrSkipSimplePool 跌停/炸板池的整日替换入口，空回包走"跳过替换 + WARN 读数"而不是报错。
//
// 两侧理由都要写下来：
//   - 不删：空回包既可能是"今天真的一只都没有"，也可能是上游这次没给（配额/降级/半截响应）。
//     拿后者把一整天的跌停或炸板删空，比留下旧数据更坏——§降级链会兜死分支的同族。
//   - 不判失败：跌停家数为 0、炸板家数为 0 在强/弱市里都是**合法形态**（不同于涨停池：
//     A 股不存在零涨停的交易日，涨停池的空回包仍由 ReplaceThsLimitUps 硬拦停）。
//     把合法形态报成失败会让夜间腿与保活天天踩红线，红线一多就被忽略——那才是真的风险。
//
// 留下的可见性：调用前的 pool=… rows=0 读数 + 这里一行 skipped_replace WARN，
// 两者都在日志里，人能看到"这一天我没敢删"。
// English: whole-day replace for limit-down/break pools; an empty payload skips the delete with a
// WARN instead of failing the sync (a zero-count day is legitimate for these two pools).
func replaceOrSkipSimplePool(db *store.DB, table, name, tradeDate string,
	rows map[string]store.ThsPoolSimple) (store.PoolReplaceResult, error) {
	if len(rows) == 0 {
		log.Printf("[hithink][WARN] pool=%s empty_payload=skipped_replace trade_date=%s "+
			"⇒ 未落库也未删除该日旧行：分不清「今日真为零」与「上游这次没给数据」，先保数据。"+
			"连续多日出现这行才需要人工复核（单日出现属正常）", name, tradeDate)
		return store.PoolReplaceResult{TradeDate: tradeDate}, nil
	}
	res, err := db.ReplaceThsSimplePool(table, tradeDate, rows)
	// 整日替换的两条读数只在成功时打印：失败时 deleted/written 是半截事务的中间量，
	// 打上会被当成"这一天只剩这些"（§备份件要回验的同族：失败产物不当验收面）。
	if err == nil {
		log.Printf("[hithink] pool=%s replaced trade_date=%s deleted=%d written=%d",
			name, res.TradeDate, res.Deleted, res.Written)
	}
	return res, err
}

// cmdHithinkSyncAnomaly 当日全市场异动原因入库（D1 归因辅证 + 消息推送数据源）。
func cmdHithinkSyncAnomaly(client *data.HithinkClient, db *store.DB, since string) {
	items, err := client.AnomalyList(nil)
	if err != nil {
		log.Fatalf("异动列表拉取失败: %v", err)
	}
	tradeDate := cntime.DayCompactOf(time.Now())
	// row 异动公告行：代码/名称/标签/正文 + 关键词。
	type row struct {
		code, name, tag, content string
		kw                       []string
	}
	rows := make([]row, 0, len(items))
	for _, it := range items {
		rows = append(rows, row{it.ThsCode, it.StockName, it.TagName, it.AnalysisContent, it.KeywordList})
	}
	n := 0
	batchSize := 500
	for start := 0; start < len(rows); start += batchSize {
		endIdx := min(start+batchSize, len(rows))
		batch := rows[start:endIdx]
		items2 := make([]struct {
			ThsCode         string
			Name            string
			TagName         string
			AnalysisContent string
			KeywordList     []string
		}, 0, len(batch))
		for _, r := range batch {
			items2 = append(items2, struct {
				ThsCode         string
				Name            string
				TagName         string
				AnalysisContent string
				KeywordList     []string
			}{r.code, r.name, r.tag, r.content, r.kw})
		}
		c, uerr := db.UpsertThsAnomalies(tradeDate, items2)
		if uerr != nil {
			log.Fatalf("异动写入失败: %v", uerr)
		}
		n += int(c)
	}
	log.Printf("[hithink] 异动原因同步完成：%d 条（trade_date=%s）", n, tradeDate)
}

// deref 字符串指针解引用（nil → 空串）。
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// cmdHithinkSyncValuations 全市场估值快照批量入库（§E）。
// 从 ths_daily 取全部标的，100 只/批分页调 API；trade_date=今日。
// §M-8/N-6（2026-09-22 PM 批）：批次失败不再"跳过仍报完成"——计失败数，>0 时返回
// 降级错误由调用方非零退出（旧实现全批挂掉也只打"同步完成 0 只"且 exit 0）。
// English: §M-8/N-6 — batch failures are counted; any failure returns a degraded error so the
// process exits non-zero instead of the old "sync complete" + exit 0.
func cmdHithinkSyncValuations(client *data.HithinkClient, db *store.DB) error {
	codes, cerr := db.ThsAllCodes()
	if cerr != nil {
		log.Fatalf("读标的清单失败: %v", cerr)
	}
	tradeDate := cntime.DayCompactOf(time.Now())
	total := 0
	failed := 0
	batches := 0
	batchSize := 100
	for i := 0; i < len(codes); i += batchSize {
		end := i + batchSize
		if end > len(codes) {
			end = len(codes)
		}
		batches++
		items, err := client.ValuationsSnapshot(codes[i:end])
		if err != nil {
			failed++
			log.Printf("[hithink] 估值批次 %d-%d 失败(跳过): %v", i, end, err)
			continue
		}
		var rows []store.ThsValuationRow
		for _, it := range items {
			rows = append(rows, store.ThsValuationRow{
				TradeDate: tradeDate, TsCode: it.ThsCode,
				PeTtm: it.PeTtm, PeMrq: it.PeMrq, PbMrq: it.PbMrq,
				PsTtm: it.PsTtm, PcfTtm: it.PcfTtm,
			})
		}
		if _, uerr := db.UpsertThsValuations(rows); uerr != nil {
			failed++
			log.Printf("[hithink] 估值写入失败(跳过): %v", uerr)
			continue
		}
		total += len(rows)
	}
	if failed > 0 {
		return fmt.Errorf("估值快照同步降级：%d/%d 批失败，仅 %d 只入库（trade_date=%s，当日估值不完整）",
			failed, batches, total, tradeDate)
	}
	log.Printf("[hithink] 估值快照同步完成：%d 只（trade_date=%s）", total, tradeDate)
	return nil
}

// cmdHithinkSyncFinIndicators 财务指标同步：对 --codes 指定池内标的逐个拉取
// 最近年报+最新季报的指标入库。codesFile 每行一个 ts_code。
// §M-8/N-6（2026-09-22 PM 批）：旧实现拉取失败 `continue` 零留痕、写入失败
// `uerr == nil` 直接丢弃计数分支，末了照打"同步完成 N 条"且 exit 0——断供当晚
// 与满盘成功在退出码/文案上完全同形。现按拉取/写入两类分别计数并全部透出，
// 任一 >0 即降级返回错误（非零退出）。
// English: §M-8/N-6 — fetch and write failures are each counted and surfaced; any failure makes
// this return a degraded error (non-zero exit) instead of the old unconditional "complete" + exit 0.
func cmdHithinkSyncFinIndicators(client *data.HithinkClient, db *store.DB, args []string) error {
	fs := flag.NewFlagSet("fin-indicators", flag.ExitOnError)
	codesFile := fs.String("codes", "", "标的清单文件（每行一个 thscode，如 600519.SH）")
	year := fs.Int("year", time.Now().Year(), "报告期年份")
	reports := fs.String("reports", "1,4", "要拉取的报告期序号（逗号分隔：1一季报/2中报/3三季报/4年报）")
	if err := fs.Parse(args); err != nil {
		log.Fatalf("参数解析失败: %v", err)
	}
	if *codesFile == "" {
		log.Fatal("--codes 必填（每行一个 thscode）")
	}
	content, err := os.ReadFile(*codesFile)
	if err != nil {
		log.Fatalf("读取标的文件失败: %v", err)
	}
	var codes []string
	for _, ln := range strings.Split(string(content), "\n") {
		c := strings.TrimSpace(ln)
		if c != "" && !strings.HasPrefix(c, "#") {
			codes = append(codes, c)
		}
	}
	var repNums []string
	for _, r := range strings.Split(*reports, ",") {
		repNums = append(repNums, strings.TrimSpace(r))
	}
	// 逐标的逐报告期拉取财务指标并组装入库；失败不再静默——分类计数后统一透出。
	total := 0
	fetchErrs := 0
	writeErrs := 0
	var firstFetchErr string
	for _, code := range codes {
		for _, rn := range repNums {
			report := fmt.Sprintf("%d-%s", *year, rn)
			fi, err := client.FinancialIndicators(code, report)
			if err != nil {
				fetchErrs++
				if firstFetchErr == "" {
					firstFetchErr = fmt.Sprintf("%s %s: %v", code, report, err)
				}
				continue // 无数据/未披露与真实故障在此无法区分，一律计入失败数
			}
			var rows []store.ThsFinIndicatorRow
			for _, ab := range fi.Abilities {
				for _, ind := range ab.Indicators {
					rows = append(rows, store.ThsFinIndicatorRow{
						TsCode: code, Report: report,
						Ability: ab.Ability, IndexID: ind.IndexID, Value: ind.Value,
					})
				}
			}
			if _, uerr := db.UpsertThsFinIndicators(rows); uerr != nil {
				writeErrs++
				log.Printf("[hithink] 财务指标写入失败 %s %s: %v", code, report, uerr)
				continue
			}
			total += len(rows)
		}
	}
	if fetchErrs+writeErrs > 0 {
		return fmt.Errorf("财务指标同步降级：入库 %d 条（%d 只 × %d 报告期），拉取失败 %d 次（首例：%s）、写入失败 %d 次——财务数据可能不完整",
			total, len(codes), len(repNums), fetchErrs, firstFetchErr, writeErrs)
	}
	log.Printf("[hithink] 财务指标同步完成：%d 条（%d 只 × %d 报告期）", total, len(codes), len(repNums))
	return nil
}
