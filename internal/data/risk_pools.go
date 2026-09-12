// risk_pools.go — 风险因子数据层主源切换（§MARKET_RISK_GATE P0）。
// 把「涨停池 / 跌停池 / 炸板池 / 全市场涨跌家数」四类盘口统计从「东财直连」升级为
// 「同花顺（新）hithink 主源、东财永远兜底」：涨停池东财有现成接口故保留兜底，
// 跌停/炸板池东财无接口（仅 hithink 提供），取不到即返回错误由上层弃权，绝不编造。
// 涨跌家数（breadth）按裁决⑤：东财 f62/f63 主源（走带真实弃权的 GetBreadth），
// 失败时降级用 hithink BatchQuotes 对传入全市场清单统计，再无则弃权（valid=false）。
//
// English: risk_pools.go — the §MARKET_RISK_GATE P0 data-source switch. It upgrades four board
// statistics (limit-up / limit-down / break pool / market-wide up-down counts) from "EastMoney
// direct" to "hithink (new Tonghuashun) primary, EastMoney always last-resort". EastMoney has a
// limit-up pool so it stays as the fallback; it has no limit-down/break pool (hithink-only), so a
// miss returns an error for the caller to abstain rather than fabricate. Per ruling ⑤, breadth
// keeps EastMoney f62/f63 as the primary via GetBreadth (true abstention), falling back to hithink
// BatchQuotes over a supplied universe, and abstaining (valid=false) if neither is available.
package data

import (
	"fmt"
	"log"
	"time"
)

// LimitUpPoolSource 涨停池取数结果 + 命中源名（"hithink"/"eastmoney"，供双跑一致性观测与日志）。
// English: pool fetch result plus the serving source name, for dual-run observability and logs.
type LimitUpPoolSource struct {
	Stocks []LimitUpStock
	Source string
}

// PoolLimitUp 涨停池主源切换：hithink 优先、东财兜底。
// date 为 "2006-01-02"，空串=当日（与东财 GetLimitUpPool 口径一致）。
// 两源都失败时返回最后错误，调用方按空池处理（不 panic、不编造）。
// English: limit-up pool with hithink-primary + EastMoney-fallback. Empty date = today. When both
// sources fail the last error is returned and the caller treats it as an empty pool (never panics/fabricates).
func (dc *DataCoordinator) PoolLimitUp(date string) (LimitUpPoolSource, error) {
	if hk := dc.hithinkClient(); hk != nil {
		dateMs := poolDateMillis(date)
		if items, err := hk.LimitUpPool(dateMs); err == nil && len(items) > 0 {
			return LimitUpPoolSource{Stocks: hithinkItemsToLimitUp(items), Source: "hithink"}, nil
		} else if err != nil {
			log.Printf("[risk_pools] 涨停池 hithink 取数失败，降级东财: %v", err)
		}
	}
	// 东财永远兜底（且是 hithink 缺席/无 Key 时的唯一路径）。
	if dc.eastMoney == nil {
		return LimitUpPoolSource{}, fmt.Errorf("limit-up pool: both hithink absent and eastmoney nil")
	}
	pool, err := dc.eastMoney.GetLimitUpPool(date)
	if err != nil {
		return LimitUpPoolSource{}, fmt.Errorf("limit-up pool both sources failed: %v", err)
	}
	return LimitUpPoolSource{Stocks: pool, Source: "eastmoney"}, nil
}

// PoolLimitDown 跌停池：仅 hithink 提供（东财无该接口）。取不到返回错误，上层按 0 弃权。
// English: limit-down pool — hithink-only (EastMoney has none); a miss returns an error to abstain.
func (dc *DataCoordinator) PoolLimitDown(date string) (LimitUpPoolSource, error) {
	hk := dc.hithinkClient()
	if hk == nil {
		return LimitUpPoolSource{}, fmt.Errorf("limit-down pool: hithink unavailable")
	}
	items, err := hk.LimitDownPool(poolDateMillis(date))
	if err != nil {
		return LimitUpPoolSource{}, fmt.Errorf("limit-down pool hithink: %v", err)
	}
	return LimitUpPoolSource{Stocks: hithinkItemsToLimitUp(items), Source: "hithink"}, nil
}

// PoolLimitBreak 炸板池：仅 hithink 提供（BreakCount 映射 OpenTimes，用于真实炸板率）。
// English: break pool — hithink-only; BreakCount maps to OpenTimes for the true break-rate.
func (dc *DataCoordinator) PoolLimitBreak(date string) (LimitUpPoolSource, error) {
	hk := dc.hithinkClient()
	if hk == nil {
		return LimitUpPoolSource{}, fmt.Errorf("limit-break pool: hithink unavailable")
	}
	items, err := hk.LimitBreakPool(poolDateMillis(date))
	if err != nil {
		return LimitUpPoolSource{}, fmt.Errorf("limit-break pool hithink: %v", err)
	}
	return LimitUpPoolSource{Stocks: hithinkItemsToLimitUp(items), Source: "hithink"}, nil
}

// MarketBreadth 涨跌家数（裁决⑤：东财主源 + 真实弃权 + hithink 全市场统计兜底）。
// universe 为兜底用的全市场代码清单（可空）；valid=false 表示两源都不可用，调用方必须弃权
// （不得把失败当作 50/50 中性）。EM 成功即命中 "eastmoney"，失败且有 universe 才试 THS。
// English: market-wide up/down counts (ruling ⑤: EastMoney primary with true abstention, hithink
// full-market fallback). valid=false means both sources are unavailable, so the caller must abstain
// rather than treat a failure as a neutral 50/50.
func (dc *DataCoordinator) MarketBreadth(universe []string) (up, down int, source string, valid bool) {
	if dc.eastMoney != nil {
		if u, d, err := dc.eastMoney.GetBreadth(); err == nil {
			return u, d, "eastmoney", true
		}
	}
	// THS 兜底：需调用方提供全市场代码清单（引擎监控池不完整，故由上层传全 A 清单或留空弃权）。
	if hk := dc.hithinkClient(); hk != nil && len(universe) > 2000 {
		quotes, err := hk.BatchQuotes(universe)
		if err == nil && len(quotes) > 2000 {
			var upC, downC int
			for _, q := range quotes {
				if q == nil {
					continue
				}
				if q.ChangePct > 0 {
					upC++
				} else if q.ChangePct < 0 {
					downC++
				}
			}
			if upC > 0 || downC > 0 {
				return upC, downC, "hithink", true
			}
		}
	}
	return 0, 0, "", false
}

// hithinkClient 锁内读取 hithink 客户端（防与 SetHithink 的数据竞争）。
// English: read the hithink client under lock (guards against a data race with SetHithink).
func (dc *DataCoordinator) hithinkClient() *HithinkClient {
	dc.mu.RLock()
	defer dc.mu.RUnlock()
	return dc.hithink
}

// hithinkItemsToLimitUp 把 hithink 三池条目映射为东财同构的 LimitUpStock（引擎各消费方零改动）。
// 连板数取 ContinueDayCnt、首封取 LimitUpTime(HH:MM)、封单取 SealMoney、炸板数取 OpenTimes。
// English: map hithink pool items onto the EastMoney-shaped LimitUpStock so downstream consumers stay
// unchanged. LianBan←ContinueDayCnt, FirstSeal←LimitUpTime(HH:MM), SealAmt←SealMoney, BreakCount←OpenTimes.
func hithinkItemsToLimitUp(items []HithinkLimitUpItem) []LimitUpStock {
	out := make([]LimitUpStock, 0, len(items))
	for i := range items {
		it := &items[i]
		code := it.Ticker
		if code == "" {
			code = sixFromThsCode(it.ThsCode)
		}
		out = append(out, LimitUpStock{
			Code:       code,
			Name:       it.Name,
			Price:      it.LastPrice,
			ChangePct:  it.PriceChangeRatioPct,
			Amount:     it.Turnover,
			Turnover:   it.TurnoverRatioPct,
			LianBan:    it.ContinueDayCnt,
			FirstSeal:  it.LimitUpTime,
			SealAmt:    it.SealMoney,
			BreakCount: it.OpenTimes,
		})
	}
	return out
}

// poolDateMillis 把 "2006-01-02"（或空=当日）转成 hithink 接口所需的毫秒时间戳；
// 无法解析时传 0（hithink 侧默认取当日）。
// English: convert "2006-01-02" (empty = today) to the millisecond epoch the hithink pools want;
// unparseable → 0 (hithink defaults to today).
func poolDateMillis(date string) int64 {
	if date == "" {
		return 0
	}
	t, err := time.ParseInLocation("2006-01-02", date, time.Local)
	if err != nil {
		return 0
	}
	return t.UnixMilli()
}

// sixFromThsCode 从 "600519.SH" 取裸六码 "600519"（无点号则原样返回）。
// English: strip the exchange suffix from a thscode (600519.SH → 600519); no dot → unchanged.
func sixFromThsCode(ths string) string {
	for i := 0; i < len(ths); i++ {
		if ths[i] == '.' {
			return ths[:i]
		}
	}
	return ths
}
