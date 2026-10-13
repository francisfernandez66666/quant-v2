// ths_backfill.go §ENH-A：同花顺盘口三池历史回填子命令。
//
// 用法:
//
//	dataload ths-backfill --start 20230801 --end 20260919 [--sleep-ms 300] [--force]
//
// 逐交易日拉取 涨停/跌停/炸板 三池入库（ths_limit_up_daily 等），已收录日默认跳过
// （涨停池当日行数>0 视为已回填；--force 重拉＝整日快照替换，见 §POOL-SNAPSHOT）。
// 单日失败记日志续跑，重跑自然补齐；全程节流防打爆 hithink 配额。
// 目的：涨停微结构因子（CatLimit）面板装配需要 ≥1 年历史事件；现库内三池仅 9 月起
// 的盘后增量（§P1 建表后从未回填历史）。
package main

import (
	"flag"
	"log"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/store"
)

// cmdThsBackfill 执行区间内逐交易日的三池回填。
func cmdThsBackfill(db *store.DB, args []string) {
	fs := flag.NewFlagSet("ths-backfill", flag.ExitOnError)
	start := fs.String("start", "", "起始交易日 yyyyMMdd（必填）")
	end := fs.String("end", cntime.DayCompactOf(time.Now()), "结束交易日 yyyyMMdd（缺省=今日）")
	sleepMs := fs.Int("sleep-ms", 300, "逐日节流（毫秒），防打爆上游配额")
	force := fs.Bool("force", false, "已收录日也重拉（默认跳过）")
	// §POOL-STAMP：与 pools 子命令同两道守卫（默认拦停影子日、时间戳只记读数），
	// 回填历史日同样会撞上"上游把缺数日兜成最近有数据日"，那一包写成历史日＝把不存在的交易日钉进因子面板。
	allowShadow := fs.Bool("allow-shadow", false, "影子日（与上一有数据日逐行签名相同）允许落库")
	enforceStamp := fs.Bool("enforce-stamp", false, "回包时间戳与请求日不符即判该日失败")
	if err := fs.Parse(args); err != nil {
		log.Fatalf("参数解析失败: %v", err)
	}
	opts := defaultPoolSyncOpts()
	opts.allowShadow, opts.stampEnforce = *allowShadow, *enforceStamp
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
		if _, serr := syncPoolsForDate(client, db, d, opts); serr != nil {
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
		// 失败原因里最常见的一条是"影子日拦停"：上游对没有数据的旧日不会报缺，而是把最近一个
		// 有数据日原样兜回来。这一型重跑不会自己变绿，必须由人先看那行 shadow_of= 再决定。
		log.Printf("[ths-backfill] 提示：重跑本命令只补失败日；影子日拦停的日重跑仍会拦（见 shadow_of= 读数）")
	}
}
