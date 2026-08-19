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
	dates, err := db.TradeDates(start, end)
	if err != nil {
		return nil, err
	}
	var out []SectorEvent
	for _, d := range dates {
		counts, err := db.LimitUpCountsByIndustry(d)
		if err != nil {
			return nil, err
		}
		type pair struct {
			industry string
			count    int
		}
		var hits []pair
		for ind, n := range counts {
			if n >= minLimitUps {
				hits = append(hits, pair{ind, n})
			}
		}
		sort.Slice(hits, func(i, j int) bool { return hits[i].count > hits[j].count })
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
