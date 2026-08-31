// 合成事件：板块涨停潮检测。
// English: synthetic events: sector limit-up surge detection.
package backtest

import (
	"sort"

	"quant-trading-v2/internal/store"
)

// SynthesizeEvents 合成"板块涨停潮"事件：逐交易日统计各行业涨停家数，
// 超过 minLimitUps 即触发；每日最多保留 maxPerDay 个（按涨停家数降序）。
// English: SynthesizeEvents builds sector limit-up surge events: per trade day it counts limit-ups in
// each industry, firing when they exceed minLimitUps; at most maxPerDay are kept per day, sorted by count desc.
// （SynthesizeEvents detects sector limit-up surge events day by day.）
func SynthesizeEvents(db *store.DB, start, end string, minLimitUps, maxPerDay int) ([]SectorEvent, error) {
	if minLimitUps <= 0 {
		minLimitUps = 3
	}
	// 区间一次扫描（性能关键，§8.6）：旧实现逐日查询，742 个交易日 × ~8.8s 随机 I/O
	// ≈ 合成阶段近 2 小时且零输出；区间版单条 SQL 顺序扫 + GROUP BY，秒级。
	// English: one range query replaces the per-day loop (742 × 8.8s random I/O ≈ 2h of silent
	// synthesis); semantics preserved — per day, filter by minLimitUps, sort desc, cap maxPerDay.
	rows, err := db.LimitUpCountsRange(start, end)
	if err != nil {
		return nil, err
	}
	var out []SectorEvent
	i := 0
	for i < len(rows) {
		d := rows[i].Date
		// pair 单日涨停行业命中：行业名 + 涨停家数。
		type pair struct {
			industry string
			count    int
		}
		var hits []pair
		for i < len(rows) && rows[i].Date == d {
			if rows[i].Count >= minLimitUps {
				hits = append(hits, pair{rows[i].Industry, rows[i].Count})
			}
			i++
		}
		sort.Slice(hits, func(j, k int) bool { return hits[j].count > hits[k].count })
		if maxPerDay > 0 && len(hits) > maxPerDay {
			hits = hits[:maxPerDay]
		}
		for _, h := range hits {
			cons, err := db.IndustryConstituents(h.industry, d)
			if err != nil {
				return nil, err
			}
			out = append(out, SectorEvent{
				Date: d, Industry: h.industry, LimitUpCount: h.count,
				Constituents: len(cons),
			})
		}
	}
	return out, nil
}
