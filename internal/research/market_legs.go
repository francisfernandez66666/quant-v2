// market_legs.go — §REPLAY-INPUTS 2026-11-28：逐股装配的市场级八列填装（档 2 / 档 3）。
//
// 本文件是"因子输入面"的落点，纪律全部来自方案 §3.2 与仓库既有教训，逐条写在函数里：
//
//  1. **只认精确日期**。资金流/股本/分位都必须"这一天的这一条读数"，缺日就是 NaN。
//     拿上一天的值顶班，等于给一个日频序列偷偷换了判定日（§Freshness 那条
//     "MAX(date) 不等于补齐"的同型：末行在，不代表每天有）。
//  2. **5 日窗口要求连续 5 个交易日**，交易日序取 `s.Dates`（该股自己的日线序列），
//     不是日历日也不是"库里最近的 5 行"。中间断一天 ⇒ 整窗 NaN。
//     断日还硬算，表现是一条 6 天窗口的比率被当成 5 日窗口参与打分——数值看着正常、
//     语义已经变了，而这正是 `fac_175` 要用的东西。
//  3. **双分母各写各的列**，绝不在同一列里换分母（现网载体就是因为一个键两种量纲
//     导致 band 标在错尺度上，见 §0.5 探针 3/8）。
//  4. **前视防线**：只允许用 ≤ 判定日的行；窗口右端点就是判定日本身。
//
// English: fills the eight market-level columns per stock — exact-day match only,
// five-session continuity required, two denominators kept in two columns, no look-ahead.
package research

import (
	"math"

	"quant-trading-v2/internal/factor"
	"quant-trading-v2/internal/store"
)

// flowWindowSessions Flow5d 的窗口长度（交易日数，含当日）。
// 与载体的 5 日档同义；不写成字面量散落在两处——这里是唯一一处。
const flowWindowSessions = 5

// fillMarketLegs 填 StockSeries 的八个市场级列。
//
// 全程"能读到就写、读不到留 NaN"，任何一步取数失败都不中断装配：
// 这是只读观测腿，不许有停机权限（11-19 那次只读探针 NameError 带走整轮的教训）。
func fillMarketLegs(db *store.DB, s *factor.StockSeries, code, start, end string, p *Panels) {
	if db == nil || s == nil || len(s.Dates) == 0 {
		return
	}
	n := len(s.Dates)

	// ① D1 与指数涨跌幅来自全池共用面板（逐票查会退化成 4309 次查询）。
	for i, d := range s.Dates {
		if p != nil {
			if v, ok := p.D1On(d, code); ok {
				s.D1[i] = v
			}
			if v, ok := p.IndexChangeOn(d); ok {
				s.IndexChange[i] = v
			}
		}
	}

	// ② 个股资金流：一次取该股区间内的全部行，按日期建索引。
	//    limit 给足（区间长度 + 窗口余量），StockFlowSeries 只取 ≤end 的最近 limit 行，
	//    给小了会让区间前段整片没有读数（不是 NaN 弃权，是"根本没查"）。
	flowRows, err := db.StockFlowSeries(code, end, n+flowWindowSessions+5)
	if err == nil && len(flowRows) > 0 {
		type flowPair struct {
			net    float64
			pct    float64
			amt    float64
			hasNet bool
			hasPct bool
			hasAmt bool
		}
		byDay := make(map[string]flowPair, len(flowRows))
		for _, r := range flowRows {
			// 三个 Has* 各自独立带下来：上游可能给净额但不给净占比，
			// 折叠成一个"整行有效"会把可用读数丢掉，反之会把缺数读成 0。
			byDay[r.TradeDate] = flowPair{net: r.MainNet, pct: r.MainPct, amt: r.Amount,
				hasNet: r.HasMainNet, hasPct: r.HasMainPct, hasAmt: r.HasAmount}
		}
		// 占成交额口径：当日直接取上游 f57（净额/成交额×100），不做二次除法——
		// 多一次除法就多一个舍入与分母为 0 的分支，而上游已经把这一列算好了。
		for i, d := range s.Dates {
			row, ok := byDay[d]
			if !ok {
				continue
			}
			if row.hasPct {
				s.FlowToday[i] = row.pct
			}
			// 5 日窗口：必须凑满连续 5 个交易日，且每天的 amount 都在（分母残缺即整窗弃权）。
			if i >= flowWindowSessions-1 {
				var sumNet, sumAmt float64
				complete := true
				for k := i - flowWindowSessions + 1; k <= i; k++ {
					r, has := byDay[s.Dates[k]]
					if !has || !r.hasNet || !r.hasAmt || !(r.amt > 0) {
						complete = false
						break
					}
					sumNet += r.net
					sumAmt += r.amt
				}
				if complete && sumAmt > 0 {
					s.Flow5d[i] = sumNet / sumAmt * 100.0
				}
			}
		}

		// ③ 占流通市值口径（原公式，另开两列）：分母＝当日流通股本 × 当日原始收盘。
		shares, shErr := db.FloatSharesForCodeRange(code, start, end)
		if shErr == nil && len(shares) > 0 {
			for i, d := range s.Dates {
				share, ok := shares[d]
				if !ok || !(share > 0) {
					continue
				}
				cap := share * s.CloseRaw[i]
				if !(cap > 0) || math.IsNaN(cap) {
					continue
				}
				row, has := byDay[d]
				if has && row.hasNet {
					s.FlowCapToday[i] = row.net / cap * 100.0
				}
				if i >= flowWindowSessions-1 {
					var sumNet float64
					complete := true
					for k := i - flowWindowSessions + 1; k <= i; k++ {
						r, has2 := byDay[s.Dates[k]]
						if !has2 || !r.hasNet {
							complete = false
							break
						}
						sumNet += r.net
					}
					if complete {
						s.FlowCap5d[i] = sumNet / cap * 100.0
					}
				}
			}
		}
	}

	// ④ 板块净流入横截面分位（代理）：先解析该股逐日的板块归属，再查面板算好的当日分位。
	//    归属没命中 ⇒ 整格 NaN。绝不许把"查不到板块"折算成分位 0——0 是"全市场最弱"，
	//    是一条方向相反的真读数（载体那条腿当年就是因为 dict 当成标量，整条腿静默弃权）。
	//
	//    ★ as-of 解析（owner 2026-11-28 裁决 2）：板块归属是**缓慢变化的维度**，
	//    现网只有单日快照时，早于首份快照的日期回填首份、晚于末份的日期沿用末份。
	//    不做这一步的现场形态：sector_flow_daily 有 126 天，而连接键只有 1 天能命中
	//    ⇒ 分位腿覆盖率 0.8%，读数上完全看不出是"接上了还是没接"。
	//    回填格数记进 Panels（报告头 `sector_map=`），方向是"看得见"，不是"偷偷放宽"。
	if p != nil {
		snaps, err := db.SectorSnapshotsForCode(code, end)
		if err == nil && len(snaps) > 0 {
			cur := 0
			for i, d := range s.Dates {
				// 推进到"最后一个 ≤ 当日"的快照；当日早于首份时 cur 停在 0（= 回填首份）。
				for cur+1 < len(snaps) && snaps[cur+1].Day <= d {
					cur++
				}
				if snaps[cur].Day > d {
					p.noteSectorBackfill() // 用了"未来"的快照 ⇒ 前视回填，计数留痕
				}
				if v, ok := p.SectorQuantileOn(d, snaps[cur].Sector); ok {
					s.SectorFlowQuantile[i] = v
				}
			}
		}
	}

	// ⑤ 龙头代理分位：由预计算表落库（横截面不能在逐股循环里算），按日精确命中。
	ranks, err := db.LeaderRanksForCode(code, start, end)
	if err == nil {
		for i, d := range s.Dates {
			if v, ok := ranks[d]; ok {
				s.SectorGainRank[i] = v
			}
		}
	}
}
