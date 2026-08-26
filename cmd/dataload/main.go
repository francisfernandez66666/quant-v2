// 历史数据装载工具（B0：SQLite 数据地基）。
// 用法：dataload [flags] <子命令>
//
//	子命令：
//	  full    全量装载：基础信息+交易日历+指数+行情类（baostock 逐票 / tushare 按日）
//	  daily   仅行情类表（daily/adj_factor/daily_basic/stk_limit，增量）
//	  adjfactor  专项补齐 adj_factor（复权因子，daily 满但因子缺时用）
//	  finance 财务类表（baostock 逐(票,年,季)，默认全市场、慢，建议 --codes 研究池）
//	  bars <ts_code>  回读单只股票的 hfq 日线做校验
//	  verify 打印各表行数
//
// flags：--db（默认 ~/.quant-trading-v2/trading.db）、--provider（baostock|tushare，默认
// baostock）、--pyurl（baostock sidecar 地址，默认 http://127.0.0.1:8787）、--token
// （仅 tushare 需要）、--start（YYYYMMDD，默认 20200101）、--end（YYYYMMDD，默认今天）、
// --codes <文件>（finance 的研究池：每行一个 ts_code，# 注释）、--fin-start/--fin-end（年份）。
// 支持断点续传：行情表按最近交易日续拉，财务表按单票最近报告期续拉。
// （dataload loads historical data into the research SQLite DB. Provider baostock is the
// primary source via the Python sidecar; tushare remains as a fallback. Runs resume
// from each table's/stock's latest loaded point.）
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/store"
)

// defaultDB 默认研究库路径（与引擎配置目录一致）。
var defaultDB = filepath.Join(os.Getenv("HOME"), ".quant-trading-v2", "trading.db")

// indexCodes 基准/风格指数（超出基准收益用）。
var indexCodes = []string{"000300.SH", "000905.SH", "000852.SH"}

// main 数据装载入口：解析 flags 与子命令，按 provider 分派到 baostock/tushare 实现，
// 并完成库打开、校验与各子命令的错误处理。
func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	dbPath := flag.String("db", defaultDB, "研究 SQLite 库路径")
	provider := flag.String("provider", "baostock", "数据源: baostock|tushare")
	pyurl := flag.String("pyurl", "http://127.0.0.1:8787", "baostock sidecar 地址")
	token := flag.String("token", "", "Tushare Pro token（仅 tushare 需要）")
	start := flag.String("start", "20200101", "起始日期 YYYYMMDD")
	end := flag.String("end", time.Now().Format("20060102"), "结束日期 YYYYMMDD")
	codesFile := flag.String("codes", "", "finance 研究池文件（每行一个 ts_code）")
	finStart := flag.Int("fin-start", 2020, "财务起始年份")
	finEnd := flag.Int("fin-end", time.Now().Year(), "财务结束年份")
	flag.Parse()

	args := flag.Args()
	if len(args) < 1 {
		log.Fatalf("用法: dataload [flags] <full|daily|finance|bars ts_code|verify>")
	}
	cmd := args[0]

	if (cmd == "full" || cmd == "daily") && *provider == "tushare" && *token == "" {
		log.Fatalf("--provider tushare 需要 --token")
	}

	db, err := store.Open(*dbPath)
	if err != nil {
		log.Fatalf("打开研究库失败: %v", err)
	}
	defer db.Close()

	bsClient := data.NewBaostockClient(*pyurl)

	switch cmd {
	case "full":
		var err error
		if *provider == "tushare" {
			err = runFull(db, *token, *start, *end)
		} else {
			err = bsFull(db, bsClient, *start, *end)
		}
		if err != nil {
			log.Fatalf("full 失败: %v", err)
		}
	case "daily":
		var err error
		if *provider == "tushare" {
			err = runDaily(db, *token, *start, *end)
		} else {
			err = bsRunDaily(db, bsClient, *start, *end)
		}
		if err != nil {
			log.Fatalf("daily 失败: %v", err)
		}
	case "adjfactor":
		// 专项补齐复权因子（baostock）：daily 已满但 adj_factor 单独缺失时使用。
		// 支持 --codes 研究池文件（每行一个 ts_code），只补清单内股票。
		// （adjfactor backfills the adj_factor table when daily is current but factors are missing.
		// With --codes it processes only the listed stocks.）
		codes := make([]string, 0)
		var err error
		if *codesFile != "" {
			codes, err = readCodesFile(*codesFile)
			if err != nil {
				log.Fatalf("读取 codes 文件失败: %v", err)
			}
		} else {
			codes, err = db.StockCodes()
			if err != nil {
				log.Fatalf("读取股票列表失败: %v", err)
			}
		}
		if err := bsLoadAdjFactor(db, bsClient, codes, *start, *end); err != nil {
			log.Fatalf("adjfactor 失败: %v", err)
		}
	case "finance":
		codes, err := db.StockCodes()
		if *codesFile != "" {
			codes, err = readCodesFile(*codesFile)
		}
		if err != nil {
			log.Fatalf("读取研究池失败: %v", err)
		}
		if err := bsLoadFinancial(db, bsClient, *finStart, *finEnd, codes); err != nil {
			log.Fatalf("finance 失败: %v", err)
		}
	case "bars":
		if len(args) < 2 {
			log.Fatalf("bars 需要 ts_code 参数")
		}
		if err := runBars(db, args[1], *start, *end); err != nil {
			log.Fatalf("bars 失败: %v", err)
		}
	case "verify":
		db.DebugCount()
	case "export-delta":
		// 增量导出（阶段2.1 本地下载→云端导入）：date_col > since 的行写 gzip JSONL。
		// English: delta export — rows with date_col > since into a gzipped JSONL file.
		cmdExportDelta(db, args[1:])
	case "import-delta":
		// 增量导入：delta 文件按表 INSERT OR REPLACE 幂等合入（云端侧执行）。
		// English: delta import — upserts the delta file per table (run on the cloud side).
		cmdImportDelta(db, args[1:])
	case "hithink-sync":
		// §同花顺（新）主源同步：dump 拉取 → 流式解析 → ths_daily 幂等入库。
		cmdHithinkSync(db, args[1:])
	default:
		log.Fatalf("未知子命令: %s", cmd)
	}
}

// runFull 全量装载：元数据 → 行情类 → 财务类。
// （runFull loads metadata, then bar-like tables by date, then financials per stock.）
func runFull(db *store.DB, token, start, end string) error {
	if err := loadMeta(db, token); err != nil {
		return fmt.Errorf("元数据: %v", err)
	}
	if err := loadIndex(db, token, start, end); err != nil {
		return fmt.Errorf("指数: %v", err)
	}
	if err := runDaily(db, token, start, end); err != nil {
		return err
	}
	if err := loadFinancial(db, token, start, end); err != nil {
		return err
	}
	// §W4-c 批量装载收尾：TRUNCATE WAL，防 -wal 滞留膨胀（小盘 VPS 磁盘治理）
	if err := db.Checkpoint(); err != nil {
		log.Printf("[dataload] wal_checkpoint: %v", err)
	}
	return nil
}

// loadMeta 装载股票基础信息与交易日历（全量幂等）。
// （loadMeta loads stock basics and the trading calendar, idempotent.）
func loadMeta(db *store.DB, token string) error {
	c := data.NewTushareClient(token)

	rows, err := c.StockBasic()
	if err != nil {
		return err
	}
	if n, err := db.InsertRows("stocks", store.TableColumns("stocks"), toMaps(rows)); err != nil {
		return err
	} else {
		log.Printf("[dataload] stocks 写入 %d 行", n)
	}

	// 交易日历拉 2015-至今（含 2020 前的校准区间，供热手/校准回测使用）
	cal, err := c.TradeCal("20150101", time.Now().Format("20060102"))
	if err != nil {
		return err
	}
	if n, err := db.InsertRows("trade_cal", store.TableColumns("trade_cal"), toMaps(cal)); err != nil {
		return err
	} else {
		log.Printf("[dataload] trade_cal 写入 %d 行", n)
	}
	return nil
}

// loadIndex 装载指数日线（000300.SH 等，单次调用即可覆盖区间）。
// （loadIndex loads index daily bars for the benchmark/style indexes.）
func loadIndex(db *store.DB, token, start, end string) error {
	c := data.NewTushareClient(token)
	for _, code := range indexCodes {
		rows, err := c.IndexDaily(code, start, end)
		if err != nil {
			return fmt.Errorf("%s: %v", code, err)
		}
		n, err := db.InsertRows("index_daily", store.TableColumns("index_daily"), toMaps(rows))
		if err != nil {
			return err
		}
		log.Printf("[dataload] index_daily %s 写入 %d 行", code, n)
	}
	return nil
}

// runDaily 按交易日增量装载行情类表（daily/adj_factor/daily_basic/stk_limit）。
// 断点续传：从各表全局最近交易日之后的下一个交易日开始。
// （runDaily incrementally loads bar-like tables by trade date, resuming after the
// latest globally-loaded date per table.）
func runDaily(db *store.DB, token, start, end string) error {
	c := data.NewTushareClient(token)

	// 交易日历必须在库（loadMeta 先行，或全量之外单独先跑过）
	dates, err := db.TradeDates(start, end)
	if err != nil {
		return err
	}
	if len(dates) == 0 {
		log.Printf("[dataload] 区间 %s-%s 无交易日（需先跑 full 或确认 trade_cal）", start, end)
		return nil
	}

	// 各表从全局最近日期续拉；无数据则从 start 开始
	type tbl struct {
		table string
		fetch func(string) ([]data.TushareRow, error)
	}
	seq := []tbl{
		{"daily", c.DailyByDate},
		{"adj_factor", c.AdjFactorByDate},
		{"daily_basic", c.DailyBasicByDate},
		{"stk_limit", c.StkLimitByDate},
	}
	for _, t := range seq {
		maxD, err := db.MaxTradeDateAll(t.table)
		if err != nil {
			return err
		}
		from := start
		if maxD != "" {
			from = nextTradeDay(dates, maxD)
			if from == "" {
				log.Printf("[dataload] %s 已是最新（全局截至 %s）", t.table, maxD)
				continue
			}
			log.Printf("[dataload] %s 断点续传：%s 之后从 %s 开始", t.table, maxD, from)
		}
		if err := loadByDate(db, c, t.table, t.fetch, dates, from); err != nil {
			return fmt.Errorf("%s: %v", t.table, err)
		}
	}
	return nil
}

// loadByDate 逐交易日拉取并写入某行情表，每 50 天输出一次进度。
// （loadByDate fetches a bar-like table day by day, logging progress every 50 days.）
func loadByDate(db *store.DB, c *data.TushareClient, table string, fetch func(string) ([]data.TushareRow, error), dates []string, from string) error {
	cols := store.TableColumns(table)
	start := time.Now()
	total, done := 0, 0
	for _, d := range dates {
		if d < from {
			continue
		}
		rows, err := fetch(d)
		if err != nil {
			return fmt.Errorf("%s@%s: %v", table, d, err)
		}
		if len(rows) > 0 {
			n, err := db.InsertRows(table, cols, toMaps(rows))
			if err != nil {
				return fmt.Errorf("%s@%s: %v", table, d, err)
			}
			total += int(n)
		}
		done++
		if done%50 == 0 || done == len(dates) {
			elapsed := time.Since(start).Round(time.Second)
			log.Printf("[dataload] %s 进度 %d/%d 天，累计 %d 行，耗时 %v", table, done, len(dates), total, elapsed)
		}
	}
	log.Printf("[dataload] %s 完成：%d 行，耗时 %v", table, total, time.Since(start).Round(time.Second))
	return nil
}

// loadFinancial 逐票增量装载财务类表（fina_indicator/income/cashflow）。
// 断点续传：按单票最近报告期（end_date）之后续拉；无新数据则快速跳过。
// （loadFinancial incrementally loads financial tables per stock, resuming after each
// stock's latest report end_date, skipping stocks that are already current.）
func loadFinancial(db *store.DB, token, start, end string) error {
	c := data.NewTushareClient(token)
	codes, err := db.StockCodes()
	if err != nil {
		return err
	}
	log.Printf("[dataload] 财务类开始：%d 只股票（2次/秒限流，预计较久）", len(codes))

	type tbl struct {
		table string
		fetch func(string, string, string) ([]data.TushareRow, error)
	}
	seq := []tbl{
		{"fina_indicator", c.FinaIndicator},
		{"income", c.Income},
		{"cashflow", c.Cashflow},
	}

	tStart := time.Now()
	for i, code := range codes {
		for _, t := range seq {
			from := start
			if maxE, err := db.MaxEndDate(t.table, code); err != nil {
				return err
			} else if maxE != "" {
				// 报告期无后续则跳过（本票该表已最新）
				if maxE >= end {
					continue
				}
				from = nextDay(maxE)
			}
			rows, err := t.fetch(code, from, end)
			if err != nil {
				return fmt.Errorf("%s %s: %v", t.table, code, err)
			}
			if len(rows) > 0 {
				if _, err := db.InsertRows(t.table, store.TableColumns(t.table), toMaps(rows)); err != nil {
					return fmt.Errorf("%s %s: %v", t.table, code, err)
				}
			}
		}
		if (i+1)%100 == 0 || i == len(codes)-1 {
			elapsed := time.Since(tStart).Round(time.Second)
			log.Printf("[dataload] 财务类进度 %d/%d，耗时 %v", i+1, len(codes), elapsed)
		}
	}
	log.Printf("[dataload] 财务类完成：%d 只，耗时 %v", len(codes), time.Since(tStart).Round(time.Second))
	return nil
}

// runBars 回读单只股票 hfq 日线（校验装载正确性）。
// （runBars prints a stock's hfq bars to sanity-check loading.）
func runBars(db *store.DB, code, start, end string) error {
	bars, err := db.HfqBars(code, start, end)
	if err != nil {
		return err
	}
	log.Printf("[dataload] %s hfq 日线 %s-%s：%d 条", code, start, end, len(bars))
	for i := 0; i < len(bars); i++ {
		b := bars[i]
		if i >= 10 && i < len(bars)-3 {
			continue // 只打印前 10 条与后 3 条，避免刷屏
		}
		log.Printf("  %s O=%.2f H=%.2f L=%.2f C=%.2f V=%.0f", b.Date, b.Open, b.High, b.Low, b.Close, b.Vol)
	}
	return nil
}

// toMaps 将 data.TushareRow 切片转为通用 map 切片（给 store 层写库）。
// （toMaps converts TushareRow slices to plain maps for the store layer.）
func toMaps(rows []data.TushareRow) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		out = append(out, r)
	}
	return out
}

// nextTradeDay 返回 dates 中严格大于 maxD 的第一个日期；无则空串。
// （nextTradeDay returns the first date in dates strictly greater than maxD, or "".）
func nextTradeDay(dates []string, maxD string) string {
	for _, d := range dates {
		if d > maxD {
			return d
		}
	}
	return ""
}

// nextDay 返回 YYYYMMDD 的次日（跨月跨年处理），用于财务断点。
// （nextDay returns the day after a YYYYMMDD, for financial resume points.）
func nextDay(yyyymmdd string) string {
	t, err := time.Parse("20060102", yyyymmdd)
	if err != nil {
		return yyyymmdd
	}
	return t.AddDate(0, 0, 1).Format("20060102")
}
