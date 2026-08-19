// baostock 数据源装载实现（B0 主数据源）。
// 装载策略与 Tushare 不同：Tushare 行情按交易日拉全市场，baostock 按单票拉全历史
// （每票一次 kline 即含原始日线+估值+换手+ST+停牌；复权因子另一次调用）。财务按
// (票, 年, 季) 逐季拉取（baostock 接口粒度），故单独 `finance` 子命令按研究池后台执行。
package main

import (
	"fmt"
	"log"
	"math"
	"os"
	"strings"
	"time"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/store"
)

// bsIndexCodes 基准/风格指数（baostock 代码，与 Tushare 格式不同：sh.000300 等）。
var bsIndexCodes = []struct{ code, name string }{
	{"sh.000300", "000300.SH"}, // 沪深300
	{"sh.000905", "000905.SH"}, // 中证500
	{"sh.000852", "000852.SH"}, // 中证1000
}

// bsFull 全量装载：元数据 → 指数 → 行情类（逐票）→ 财务（可选，见 finance 子命令）。
// （bsFull loads metadata, index bars, then per-stock bar-like tables. Financials are
// loaded separately via the `finance` subcommand.）
func bsFull(db *store.DB, c *data.BaostockClient, start, end string) error {
	if err := bsLoadMeta(db, c); err != nil {
		return fmt.Errorf("元数据: %v", err)
	}
	if err := bsLoadIndex(db, c, start, end); err != nil {
		return fmt.Errorf("指数: %v", err)
	}
	return bsRunDaily(db, c, start, end)
}

// bsLoadMeta 装载股票列表 + 交易日历。
// （bsLoadMeta loads the stock universe and trading calendar.）
func bsLoadMeta(db *store.DB, c *data.BaostockClient) error {
	rows, err := c.AllStock()
	if err != nil {
		return err
	}
	stocks := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		ts := data.BsCodeToTS(r.S("code"))
		if ts == "" || ts == r.S("code") {
			continue
		}
		stocks = append(stocks, map[string]any{
			"ts_code": ts, "name": r.S("code_name"),
			"area": "", "industry": "", "market": "",
			"list_date": "", "delist_date": "",
		})
	}
	if n, err := db.InsertRows("stocks", store.TableColumns("stocks"), stocks); err != nil {
		return err
	} else {
		log.Printf("[dataload] stocks 写入 %d 行", n)
	}

	cal, err := c.TradeDays("2015-01-01", time.Now().Format("2006-01-02"))
	if err != nil {
		return err
	}
	calRows := make([]map[string]any, 0, len(cal))
	for _, r := range cal {
		calRows = append(calRows, map[string]any{
			"cal_date": normDate(r.S("calendar_date")),
			"is_open":  int(r.F("is_open")),
		})
	}
	if n, err := db.InsertRows("trade_cal", store.TableColumns("trade_cal"), calRows); err != nil {
		return err
	} else {
		log.Printf("[dataload] trade_cal 写入 %d 行", n)
	}
	return nil
}

// bsLoadIndex 装载指数日线。
// （bsLoadIndex loads index daily bars.）
func bsLoadIndex(db *store.DB, c *data.BaostockClient, start, end string) error {
	start = toISO(start)
	end = toISO(end)
	for _, idx := range bsIndexCodes {
		rows, err := c.IndexKline(idx.code, start, end)
		if err != nil {
			log.Printf("[dataload] index %s 失败（跳过）: %v", idx.name, err)
			continue
		}
		recs := make([]map[string]any, 0, len(rows))
		for _, r := range rows {
			recs = append(recs, map[string]any{
				"ts_code": idx.name, "trade_date": normDate(r.S("date")),
				"open": r.F("open"), "high": r.F("high"), "low": r.F("low"), "close": r.F("close"),
				"pre_close": r.F("preclose"), "change": r.F("pctchg"),
				"pct_chg": r.F("pctchg"), "vol": r.F("volume") / 100, "amount": r.F("amount"),
			})
		}
		if n, err := db.InsertRows("index_daily", store.TableColumns("index_daily"), recs); err != nil {
			return err
		} else {
			log.Printf("[dataload] index_daily %s 写入 %d 行", idx.name, n)
		}
	}
	return nil
}

// bsRunDaily 逐票增量装载 daily/adj_factor/daily_basic/stk_limit。
// 断点续传：按 daily 表单票最近交易日续拉（四表同票同批写入，幂等）。
// 健壮性：单只股票失败重试 3 次（baostock 单连接长时间跑易断），重试仍失败则
// 记录日志并跳过该股票继续（不中断整批），保证全市场能跑完；后续可再次断点续传补齐。
// （bsRunDaily incrementally loads bar-like tables per stock, resuming after each stock's
// latest daily trade date; the four tables are upserted together per stock. Robustness: retries a
// failing stock up to 3 times (baostock's single connection tends to drop on long runs); if it still
// fails, logs and skips that stock to keep the full universe moving, and a later run can top it up.）
func bsRunDaily(db *store.DB, c *data.BaostockClient, start, end string) error {
	codes, err := db.StockCodes()
	if err != nil {
		return err
	}
	log.Printf("[dataload] 行情类开始：%d 只股票（baostock 逐票）", len(codes))
	tStart := time.Now()
	done, inserted, skipped := 0, 0, 0
	const maxRetry = 3
	for _, code := range codes {
		n, err := bsLoadStockTables(db, c, code, start, end)
		// 失败重试（网络错误 10002007 等；每次重试前短暂等待，让 baostock 连接恢复）
		for attempt := 0; err != nil && attempt < maxRetry; attempt++ {
			log.Printf("[dataload] %s 重试 %d/%d: %v", code, attempt+1, maxRetry, err)
			time.Sleep(time.Duration(attempt+1) * 2 * time.Second)
			n, err = bsLoadStockTables(db, c, code, start, end)
		}
		if err != nil {
			// 重试耗尽仍失败 → 跳过该股票（记录，不中断整批）
			log.Printf("[dataload] %s 重试 %d 次仍失败，跳过: %v", code, maxRetry, err)
			skipped++
			continue
		}
		inserted += n
		done++
		if done%100 == 0 || done == len(codes) {
			log.Printf("[dataload] 行情类进度 %d/%d，累计 %d 行，跳过 %d，耗时 %v",
				done, len(codes), inserted, skipped, time.Since(tStart).Round(time.Second))
		}
	}
	log.Printf("[dataload] 行情类完成：%d 只，累计 %d 行，跳过 %d，耗时 %v",
		len(codes), inserted, skipped, time.Since(tStart).Round(time.Second))
	return nil
}

// bsLoadStockTables 装载单只股票的行情类四表，返回插入行数。
// （bsLoadStockTables loads one stock's four bar-like tables, returning inserted rows.）
func bsLoadStockTables(db *store.DB, c *data.BaostockClient, code, start, end string) (int, error) {
	max, err := db.MaxTradeDate("daily", code)
	if err != nil {
		return 0, err
	}
	from := start
	if max != "" {
		from = nextDay(max)
		if from > end {
			return 0, nil // 已最新，跳过
		}
	}
	bsCode := data.TsCodeToBS(code)
	isoFrom, isoEnd := toISO(from), toISO(end)

	kl, err := c.StockKline(bsCode, isoFrom, isoEnd)
	if err != nil {
		return 0, err
	}
	if len(kl) == 0 {
		return 0, nil
	}

	daily, basis, limits := make([]map[string]any, 0, len(kl)), make([]map[string]any, 0, len(kl)), make([]map[string]any, 0, len(kl))
	for _, r := range kl {
		if int(r.F("tradestatus")) == 0 {
			continue // 停牌日跳过（等价 Tushare 缺行语义）
		}
		date := normDate(r.S("date"))
		closeV, preClose := r.F("close"), r.F("preclose")
		daily = append(daily, map[string]any{
			"ts_code": code, "trade_date": date,
			"open": r.F("open"), "high": r.F("high"), "low": r.F("low"), "close": closeV,
			"pre_close": preClose, "change": closeV - preClose, "pct_chg": r.F("pctchg"),
			"vol":    r.F("volume") / 100, // 股 → 手
			"amount": r.F("amount"),
		})
		basis = append(basis, map[string]any{
			"ts_code": code, "trade_date": date,
			"turnover_rate": r.F("turn"), "pe_ttm": r.F("pettm"), "pb": r.F("pbmrq"),
			"ps_ttm": r.F("psttm"), "pcf_ttm": r.F("pcfncfttm"), "is_st": int(r.F("isst")),
		})
		up, down := limitUpDown(preClose, int(r.F("isst")), code)
		if up > 0 {
			limits = append(limits, map[string]any{"ts_code": code, "trade_date": date, "up_limit": up, "down_limit": down})
		}
	}

	adj, err := c.AdjFactor(bsCode, isoFrom, isoEnd)
	if err != nil {
		return 0, err
	}
	adjRows := make([]map[string]any, 0, len(adj))
	for _, r := range adj {
		adjRows = append(adjRows, map[string]any{
			"ts_code": code, "trade_date": normDate(r.S("dividoperatedate")),
			"adj_factor": r.F("backadjustfactor"),
		})
	}

	total := 0
	for _, t := range []struct {
		table string
		rows  []map[string]any
	}{{"daily", daily}, {"daily_basic", basis}, {"stk_limit", limits}, {"adj_factor", adjRows}} {
		if len(t.rows) == 0 {
			continue
		}
		n, err := db.InsertRows(t.table, store.TableColumns(t.table), t.rows)
		if err != nil {
			return 0, err
		}
		total += int(n)
	}
	return total, nil
}

// limitUpDown 按板块/ST 规则计算当日涨跌停价（回测护栏用）。
// 主板 ±10%（ST ±5%）；创业板(300/301)/科创板(688/689) ±20%；北交所(4/8) ±30%。
// 精确到分（四舍五入）；上市初期无涨跌停的近似由 pre_close=0 时返回 0 兜底。
// （limitUpDown computes limit-up/down prices from board/ST rules as a backtest guard rail.）
func limitUpDown(preClose float64, isST int, code string) (up, down float64) {
	if preClose <= 0 {
		return 0, 0
	}
	ratio := 0.10
	switch {
	case strings.HasPrefix(code, "300"), strings.HasPrefix(code, "301"),
		strings.HasPrefix(code, "688"), strings.HasPrefix(code, "689"):
		ratio = 0.20
	case strings.HasPrefix(code, "4"), strings.HasPrefix(code, "8"):
		ratio = 0.30
	case isST == 1:
		ratio = 0.05
	}
	round := func(v float64) float64 { return math.Round(v*100) / 100 }
	return round(preClose * (1 + ratio)), round(preClose * (1 - ratio))
}

// bsLoadFinancial 按(票,年,季)装载财务类（fina_indicator + income）。
// 断点续传：按 fina_indicator 单票最近报告期跳过。全市场逐季约需数十万次调用，故按研究池
// （--codes 文件）执行；cashflow 暂不装载（CFP 因子已由 daily_basic.pcf_ttm 提供）。
// （bsLoadFinancial loads fina_indicator+income per (stock, year, quarter), resuming per stock
// by its latest report end_date. Run with a --codes pool file since full-market is slow.
// cashflow is skipped for now — the CFP factor already uses daily_basic.pcf_ttm.）
func bsLoadFinancial(db *store.DB, c *data.BaostockClient, startYear, endYear int, codes []string) error {
	log.Printf("[dataload] 财务类开始：%d 只股票 × %d-%d 年逐季", len(codes), startYear, endYear)
	tStart := time.Now()
	pulled := 0
	for i, code := range codes {
		maxE, err := db.MaxEndDate("fina_indicator", code)
		if err != nil {
			return err
		}
		for year := startYear; year <= endYear; year++ {
			for q := 1; q <= 4; q++ {
				stat := statDateOf(year, q)
				if maxE != "" && stat <= maxE {
					continue // 该报告期已装载
				}
				bsCode := data.TsCodeToBS(code)
				profit, err := c.FinaProfit(bsCode, year, q)
				if err != nil {
					return fmt.Errorf("%s profit %dq%d: %v", code, year, q, err)
				}
				if len(profit) == 0 {
					continue // 无该期数据
				}
				growth, err := c.FinaGrowth(bsCode, year, q)
				if err != nil {
					return fmt.Errorf("%s growth %dq%d: %v", code, year, q, err)
				}
				balance, err := c.FinaBalance(bsCode, year, q)
				if err != nil {
					return fmt.Errorf("%s balance %dq%d: %v", code, year, q, err)
				}
				if err := bsInsertFinancial(db, code, profit, growth, balance); err != nil {
					return fmt.Errorf("%s insert %dq%d: %v", code, year, q, err)
				}
				pulled++
			}
		}
		if (i+1)%100 == 0 || i == len(codes)-1 {
			log.Printf("[dataload] 财务类进度 %d/%d，已拉 %d 期，耗时 %v", i+1, len(codes), pulled, time.Since(tStart).Round(time.Second))
		}
	}
	log.Printf("[dataload] 财务类完成：%d 只，%d 期，耗时 %v", len(codes), pulled, time.Since(tStart).Round(time.Second))
	return nil
}

// bsInsertFinancial 把某期 profit/growth/balance 写入 fina_indicator + income 两表。
// 注意：baostock 的 MBRevenue/npMargin/gpMargin 只有年报（Q4）有值，中间报告期为空 → 存 NULL。
// （bsInsertFinancial writes one period's profit/growth/balance into fina_indicator+income.
// baostock only fills MBRevenue/npMargin/gpMargin for annual reports; interim periods are NULL.）
func bsInsertFinancial(db *store.DB, code string, profit, growth, balance []data.TushareRow) error {
	p := profit[0]
	ann, stat := normDate(p.S("pubdate")), normDate(p.S("statdate"))

	fina := map[string]any{
		"ts_code": code, "end_date": stat, "ann_date": ann,
		"eps":                optF(p, "epsttm"),
		"roe":                optF(p, "roeavg"),
		"grossprofit_margin": optF(p, "gpmargin"),
		"netprofit_margin":   optF(p, "npmargin"),
		"debt_to_assets":     optF(firstRow(balance), "liabilitytoasset"),
		"yoy_or":             nil,
		"yoy_net_profit":     optF(firstRow(growth), "yoyni"),
	}
	income := map[string]any{
		"ts_code": code, "end_date": stat,
		"n_income_attr_p": optF(p, "netprofit"),
		"revenue":         optF(p, "mbrevenue"),
		"total_revenue":   optF(p, "mbrevenue"),
	}
	if _, err := db.InsertRows("fina_indicator", store.TableColumns("fina_indicator"), []map[string]any{fina}); err != nil {
		return err
	}
	_, err := db.InsertRows("income", store.TableColumns("income"), []map[string]any{income})
	return err
}

// bsLoadAdjFactor 专项补齐 adj_factor 表（复权因子）。
// 背景：daily 表已最新但 adj_factor 可能单独缺失（如 baostock 对 adjust_factor 接口失败时）。
// bsLoadStockTables 的断点只看 daily 表，daily 满了会跳过整只股票，补不了缺失的因子。
// 本函数按 adj_factor 单票断点续拉：一次调用覆盖整段区间，空则全量拉，非空则从最近之后补。
// （bsLoadAdjFactor backfills the adj_factor table on its own. bsLoadStockTables resumes by the
// daily table only, so a full daily table skips the stock and leaves missing factors. This walks
// the adj_factor table's own per-stock resume point; one call fetches the whole span.）
func bsLoadAdjFactor(db *store.DB, c *data.BaostockClient, codes []string, start, end string) error {
	log.Printf("[dataload] adj_factor 补齐开始：%d 只股票（baostock 逐票单次全区间）", len(codes))
	tStart := time.Now()
	done, inserted, skipped := 0, 0, 0
	const maxRetry = 3
	for _, code := range codes {
		maxD, err := db.MaxTradeDate("adj_factor", code)
		if err != nil {
			return err
		}
		from := start
		if maxD != "" {
			from = nextDay(maxD)
			if from > end {
				skipped++
				continue // 该票因子已是最新
			}
		}
		bsCode := data.TsCodeToBS(code)
		adj, err := c.AdjFactor(bsCode, toISO(from), toISO(end))
		for attempt := 0; err != nil && attempt < maxRetry; attempt++ {
			log.Printf("[dataload] %s adj_factor 重试 %d/%d: %v", code, attempt+1, maxRetry, err)
			time.Sleep(time.Duration(attempt+1) * 2 * time.Second)
			adj, err = c.AdjFactor(bsCode, toISO(from), toISO(end))
		}
		if err != nil {
			log.Printf("[dataload] %s adj_factor 重试 %d 次仍失败，跳过: %v", code, maxRetry, err)
			skipped++
			continue
		}
		rows := make([]map[string]any, 0, len(adj))
		for _, r := range adj {
			rows = append(rows, map[string]any{
				"ts_code": code, "trade_date": normDate(r.S("dividoperatedate")),
				"adj_factor": r.F("backadjustfactor"),
			})
		}
		if len(rows) > 0 {
			n, err := db.InsertRows("adj_factor", store.TableColumns("adj_factor"), rows)
			if err != nil {
				return fmt.Errorf("%s adj_factor insert: %v", code, err)
			}
			inserted += int(n)
		}
		done++
		if done%500 == 0 || done == len(codes) {
			log.Printf("[dataload] adj_factor 进度 %d/%d，累计 %d 行，跳过 %d，耗时 %v",
				done, len(codes), inserted, skipped, time.Since(tStart).Round(time.Second))
		}
	}
	log.Printf("[dataload] adj_factor 完成：%d 只，累计 %d 行，跳过 %d，耗时 %v",
		len(codes), inserted, skipped, time.Since(tStart).Round(time.Second))
	return nil
}

// optF 取行字段；空值/缺失返回 nil（写库为 NULL），有值返回 float64。
// （optF returns the float value of a cell, or nil when empty/missing so it is stored as NULL.）
func optF(r data.TushareRow, key string) any {
	if len(r.S(key)) == 0 {
		return nil
	}
	return r.F(key)
}

// firstRow 取 rows[0]（无行返回空行）。
func firstRow(rows []data.TushareRow) data.TushareRow {
	if len(rows) == 0 {
		return nil
	}
	return rows[0]
}

// statDateOf 返回某年某季的报告期末日 YYYYMMDD。
func statDateOf(year, quarter int) string {
	m := []int{3, 6, 9, 12}[quarter-1]
	return fmt.Sprintf("%04d%02d31", year, m)
}

// toISO 把 YYYYMMDD 转 YYYY-MM-DD（baostock 日期格式）。
func toISO(d string) string {
	d = strings.TrimSpace(d)
	if len(d) == 8 && !strings.Contains(d, "-") {
		return d[:4] + "-" + d[4:6] + "-" + d[6:]
	}
	return d
}

// normDate 把 YYYY-MM-DD 或 YYYYMMDD 统一为 YYYYMMDD。
func normDate(d string) string {
	d = strings.TrimSpace(d)
	if len(d) == 10 && d[4] == '-' {
		return d[:4] + d[5:7] + d[8:]
	}
	return d
}

// readCodesFile 读取 --codes 指定的研究池文件（每行一个 ts_code，支持 # 注释）。
func readCodesFile(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, strings.SplitN(line, " ", 2)[0])
	}
	return out, nil
}
