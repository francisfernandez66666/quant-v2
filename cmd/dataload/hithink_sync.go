// hithink_sync.go 同花顺（新）主源同步子命令（§HITHINK_DATA_SOURCE_PLAN Phase B0）。
//
// 用法:
//
//	dataload hithink-sync --kind daily-k-10d              # 夜间增量（最近 10 交易日）
//	dataload hithink-sync --kind daily-k --since 20230801 # 存量窗口导入（过滤 10 年全量 dump）
//
// 流程：取 S3 预签名链接（5 分钟有效）→ 落盘 parquet → 流式解析逐行回调 → 批量幂等 upsert ths_daily。
package main

import (
	"flag"
	"fmt"
	"log"
	"sort"
	"time"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/store"
)

// cmdHithinkSync 执行一次同花顺（新）日K同步。
func cmdHithinkSync(db *store.DB, args []string) {
	fs := flag.NewFlagSet("hithink-sync", flag.ExitOnError)
	kind := fs.String("kind", string(data.HithinkDumpDailyK10d), "dump 种类: daily-k-10d(增量) | daily-k(10年全量)")
	since := fs.String("since", "20230801", "只导入 trade_date >= since 的行（yyyyMMdd）")
	tmpPath := fs.String("tmp", "/tmp/hithink_dump.parquet", "parquet 落盘临时路径")
	batchSize := fs.Int("batch", 5000, "批量 upsert 行数")
	if err := fs.Parse(args); err != nil {
		log.Fatalf("参数解析失败: %v", err)
	}

	client, err := data.NewHithinkClient()
	if err != nil {
		log.Fatalf("同花顺（新）客户端初始化失败: %v（检查 /etc/quant.env 的 HITHINK_FINANCE_API_KEY）", err)
	}
	if *kind == string(data.HithinkDumpAdjFactors) {
		cmdHithinkSyncAdjFactors(client, db, *tmpPath, *since, *batchSize)
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
			Vol: row.Volume, Amount: row.Turnover,
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
}

// cmdHithinkSyncAdjFactors 复权因子同步：全量事件 dump → 窗口内事件换算累计 hfq 因子
// → 逐交易日物化入 ths_adj_factor（§HITHINK_DATA_SOURCE_PLAN §6.3）。
//
// 基线衔接：每标的窗口内首个事件之前的累计因子取旧 baostock 表在该日前的最近值
// （无缝续接历史）；窗口内乘数用 THS 事件 + ths_daily 前收盘精确计算。
// 锚定语义与 baostock 一致：因子随时间递增，历史小、当前大。
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

	for code, evs := range events {
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
