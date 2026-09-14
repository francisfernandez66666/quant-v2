// risk_backfill.go §MARKET_RISK_GATE 停摆期历史回放：从 ths 涨停/炸板池 + daily 广度
// 重建 market_risk_daily 缺失/全零行（供情绪面板 history 恢复）。
// 与引擎实时口径的差异：无实时 breadth/指数斜率/宏观事件，emotion 用 PhaseFromEmotionStat
// 同口径相位标定，risk_tier 留空（弃权），reasons 标注 backfill。
// 默认跳过引擎已写权威行（risk_tier 非空）；--force 可覆盖。
// （Rebuild market_risk_daily rows for a data outage window from ths pools + daily breadth;
// engine-authoritative rows (non-empty risk_tier) are skipped unless --force.）
package main

import (
	"flag"
	"fmt"
	"log"

	"quant-trading-v2/internal/research"
	"quant-trading-v2/internal/store"
)

func cmdRiskDailyBackfill(db *store.DB, start, end string, args []string) {
	fs := flag.NewFlagSet("risk-daily-backfill", flag.ExitOnError)
	dry := fs.Bool("dry", false, "只打印不落库")
	force := fs.Bool("force", false, "覆盖引擎已写的权威行")
	if err := fs.Parse(args); err != nil {
		log.Fatalf("参数解析失败: %v", err)
	}
	stats, err := db.EmotionStatsRange(start, end)
	if err != nil {
		log.Fatalf("读取情绪统计失败: %v", err)
	}
	written, skipped := 0, 0
	for _, st := range stats {
		if st.LimitUp <= 0 {
			continue
		}
		if !*force {
			// 引擎权威行（risk_tier 非空）跳过，回放值不覆盖实时判定。
			rows, err := db.QueryRows(`SELECT risk_tier FROM market_risk_daily WHERE trade_date = ?`, st.Date)
			if err == nil && len(rows) > 0 {
				if tier, _ := rows[0]["risk_tier"].(string); tier != "" {
					skipped++
					continue
				}
			}
		}
		upRatio := dailyUpRatio(db, st.Date)
		brk := st.BlastRate / 100.0
		row := store.MarketRiskDailyRow{
			TradeDate:    st.Date,
			Emotion:      research.PhaseFromEmotionStat(st, nil),
			Reasons:      "backfill:历史回放(ths池+日线广度,无实时tier)",
			UpRatio:      upRatio,
			BreakRate:    &brk,
			LimitUpCount: st.LimitUp,
			LadderHeight: st.MaxBoard,
		}
		if *dry {
			line := fmt.Sprintf("%s [%s] 涨停=%d 连板=%d 炸板率=%.1f%%", st.Date, row.Emotion, st.LimitUp, st.MaxBoard, brk*100)
			if upRatio != nil {
				line += fmt.Sprintf(" 上涨占比=%.1f%%", *upRatio*100)
			}
			fmt.Println(line)
			written++
			continue
		}
		if err := db.UpsertMarketRiskDaily(row); err != nil {
			log.Printf("写入 %s 失败: %v", st.Date, err)
			continue
		}
		written++
	}
	fmt.Printf("risk-daily-backfill %s~%s: 回补 %d 行，跳过权威行 %d（dry=%v force=%v）\n", start, end, written, skipped, *dry, *force)
}

// dailyUpRatio 当日全市场上涨家数占比（daily 表口径，0..1；无行返回 nil 弃权）。
// English: up-ratio from the daily table for one trade date; nil when no rows.
func dailyUpRatio(db *store.DB, date string) *float64 {
	rows, err := db.QueryRows(`SELECT COALESCE(SUM(CASE WHEN pct_chg > 0 THEN 1 ELSE 0 END),0) AS up, COUNT(*) AS tot FROM daily WHERE trade_date = ?`, date)
	if err != nil || len(rows) == 0 {
		return nil
	}
	up, tot := toFloat(rows[0]["up"]), toFloat(rows[0]["tot"])
	if tot <= 0 {
		return nil
	}
	v := up / tot
	return &v
}

// toFloat 把 QueryRows 的驱动原生值（int64/float64）转 float64。
func toFloat(v any) float64 {
	switch x := v.(type) {
	case int64:
		return float64(x)
	case int:
		return float64(x)
	case float64:
		return x
	}
	return 0
}
