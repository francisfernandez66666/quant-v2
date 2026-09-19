// ths_backfill.go §ENH-A：同花顺盘口三池历史回填子命令。
//
// 用法:
//
//	dataload ths-backfill --start 20230801 --end 20260919 [--sleep-ms 300] [--force]
//
// 逐交易日拉取 涨停/跌停/炸板 三池入库（ths_limit_up_daily 等），已收录日默认跳过
// （涨停池当日行数>0 视为已回填；--force 重拉覆盖）。单日失败记日志续跑（幂等，
// 重跑自然补齐），全程节流防打爆 hithink 配额。
// 目的：涨停微结构因子（CatLimit）面板装配需要 ≥1 年历史事件；现库内三池仅 9 月起
// 的盘后增量（§P1 建表后从未回填历史）。
package main

import (
	"flag"
	"log"
	"time"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/store"
)

// cmdThsBackfill 执行区间内逐交易日的三池回填。
func cmdThsBackfill(db *store.DB, args []string) {
	fs := flag.NewFlagSet("ths-backfill", flag.ExitOnError)
	start := fs.String("start", "", "起始交易日 yyyyMMdd（必填）")
	end := fs.String("end", time.Now().Format("20060102"), "结束交易日 yyyyMMdd（缺省=今日）")
	sleepMs := fs.Int("sleep-ms", 300, "逐日节流（毫秒），防打爆上游配额")
	force := fs.Bool("force", false, "已收录日也重拉（默认跳过）")
	if err := fs.Parse(args); err != nil {
		log.Fatalf("参数解析失败: %v", err)
	}
	if *start == "" {
		log.Fatal("ths-backfill 需要 --start yyyyMMdd")
	}
	client, err := data.NewHithinkClient()
	if err != nil {
		log.Fatalf("同花顺（新）客户端初始化失败: %v（检查 HITHINK_FINANCE_API_KEY）", err)
	}
	dates, err := db.TradeDates(*start, *end)
	if err != nil || len(dates) == 0 {
		log.Fatalf("读取交易日历失败或区间无交易日: %v", err)
	}
	done, skipped, failed := 0, 0, 0
	for i, d := range dates {
		if !*force {
			if n, _ := db.LimitUpCountOnDate(d); n > 0 {
				skipped++
				continue
			}
		}
		if _, serr := syncPoolsForDate(client, db, d); serr != nil {
			failed++
			log.Printf("[ths-backfill] %s 回填失败(续跑): %v", d, serr)
		} else {
			done++
		}
		if i%20 == 19 {
			log.Printf("[ths-backfill] 进度 %d/%d（成功 %d 跳过 %d 失败 %d）", i+1, len(dates), done, skipped, failed)
		}
		if *sleepMs > 0 {
			time.Sleep(time.Duration(*sleepMs) * time.Millisecond)
		}
	}
	log.Printf("[ths-backfill] 完成：区间 %s-%s 共 %d 交易日，回填 %d，跳过 %d，失败 %d",
		*start, *end, len(dates), done, skipped, failed)
	if failed > 0 {
		log.Printf("[ths-backfill] 提示：重跑本命令即可续补失败日（幂等 upsert）")
	}
}
