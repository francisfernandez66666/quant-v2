// backtest-strategy 子命令（二期）：四大手写战法与战法库规则的历史回放回测。
// 逻辑本体在 internal/btreplay（自独立二进制 bt_strategy 并入），此处仅做 flag 装配，
// 消除研究子系统内的第二套回测进程代码。
// English: the backtest-strategy subcommand — thin flag wiring over internal/btreplay
// (merged from the standalone bt_strategy binary in phase 2).
package main

import (
	"flag"
	"log"

	"quant-trading-v2/internal/btreplay"
	"quant-trading-v2/internal/store"
)

// cmdBacktestStrategy 战法/规则回放入口。
func cmdBacktestStrategy(db *store.DB, dbPath string, args []string) {
	fs := flag.NewFlagSet("backtest-strategy", flag.ExitOnError)
	start := fs.String("start", "20230101", "回放起始日 YYYYMMDD")
	end := fs.String("end", "", "回放结束日 YYYYMMDD（空=今天）")
	strategy := fs.String("strategy", "double_bump",
		"战法: double_bump|dragon|dragon_return|n_shape|factor(库全部启用因子规则)|pattern(库全部启用形态规则)|all(因子+形态一起)")
	maxStocks := fs.Int("maxstocks", 500, "最多回测股票数（0=全部）")
	d1 := fs.Float64("d1", 20, "n_shape 的规则 D1 分（0=不触发 n_shape）")
	industry := fs.Bool("industry", false, "dragon 是否用行业板块涨幅近似板块共振")
	dataDir := fs.String("datadir", "", "战法库目录 applied_*.json（空=默认数据目录）")
	quality := fs.Bool("quality", false, "全量回放用质控池替代 StockCodes()（剔 ST/退市/多年亏损/地量股）")
	throttleMs := fs.Int("throttle-ms", 0, "逐股节流毫秒（>0 时每处理一只股票 sleep，摊平全量回放瞬时负载）")
	fs.Parse(args)

	if *dataDir == "" {
		*dataDir = btreplay.DefaultDataDir()
	}
	if *end == "" {
		*end = today()
	}
	// 组装回放选项：DB 路径/区间/战法/标的数/D1/行业/节流等，可选叠加质量筛查。
	o := &btreplay.Options{
		DBPath:     dbPath,
		Start:      *start,
		End:        *end,
		Strategy:   *strategy,
		MaxStocks:  *maxStocks,
		D1Score:    *d1,
		Industry:   *industry,
		DataDir:    *dataDir,
		ThrottleMs: *throttleMs,
	}
	if *quality {
		sc := store.DefaultQualityScreen()
		sc.End = *end
		o.Screen = &sc
	}
	if err := o.Run(); err != nil {
		log.Fatalf("战法回放失败: %v", err)
	}
}
