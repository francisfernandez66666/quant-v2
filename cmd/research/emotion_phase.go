// 情绪相位分参回测子命令（§Phase3）：
// 读取历史情绪统计（涨停家数/最高连板），按 DetectEmotionPhaseV2 同阈值标定每日情绪阶段，
// 输出区间相位分布（分参时按阶段查看战法表现差异的依据）。
// 用法：research [--db ...] [--start ...] [--end ...] emotion-phases
// English: sentiment-phase histogram subcommand (Phase 3) — labels each day's historical sentiment
// phase and prints the phase distribution over the range, which is the basis for phase-grouped
// backtests and parameterization.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"sort"

	"quant-trading-v2/internal/research"
	"quant-trading-v2/internal/store"
)

// cmdEmotionPhases 情绪相位分布入口。
// English: entry point for the sentiment-phase distribution report.
func cmdEmotionPhases(db *store.DB, start, end string, args []string) {
	fs := flag.NewFlagSet("emotion-phases", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "输出 JSON")
	fs.Parse(args)

	stats, err := db.EmotionStatsRange(start, end)
	if err != nil {
		log.Fatalf("读取情绪统计失败: %v", err)
	}
	if len(stats) == 0 {
		log.Fatalf("区间内无情绪数据（先运行 dataload 拉取 ths_limit_up_daily/ths_break_pool_daily）")
	}
	h := research.EmotionPhaseHist(stats, nil)
	if *asJSON {
		b, err := json.MarshalIndent(h, "", "  ")
		if err != nil {
			log.Fatalf("序列化失败: %v", err)
		}
		fmt.Println(string(b))
		return
	}
	log.Printf("情绪阶段分布 %s ~ %s（%d 个交易日，末尾阶段=%s）", h.From, h.To, h.Days, h.Last)
	phases := make([]string, 0, len(h.PhaseDays))
	for p := range h.PhaseDays {
		phases = append(phases, p)
	}
	sort.Strings(phases)
	for _, p := range phases {
		fmt.Printf("  %-6s %d 天（%4.1f%%）\n", p, h.PhaseDays[p], float64(h.PhaseDays[p])/float64(h.Days)*100)
	}
	// 分段明细：每日相位 + 涨停家数与连板高度
	fmt.Println("末尾交易日明细（近 10 日）：")
	for i := len(stats) - 1; i >= 0 && i >= len(stats)-10; i-- {
		p := research.PhaseFromEmotionStat(stats[i], nil)
		fmt.Printf("  %s [%s] 涨停=%d 连板=%d 炸板率=%.1f%%\n",
			stats[i].Date, p, stats[i].LimitUp, stats[i].MaxBoard, stats[i].BlastRate)
	}
}
