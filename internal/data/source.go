// Package data — 多数据源调度与熔断协调层。
// 统一封装东方财富(MarketAPI)与同花顺(THSClient)的调度策略：
//   - 行情：新浪 → 同花顺 → 东财 三级降级链，同花顺失败自动熔断 60s；
//   - 板块/IPO：同花顺 → 东财，带 30s/60s TTL 缓存；
//   - 新闻：同花顺(主) → 新浪(兜底) 去重合并。
//
// English: Package data — a multi-source dispatch and circuit-breaker coordination layer.
// English: It uniformly wraps the dispatch strategy of EastMoney (MarketAPI) and Tonghuashun (THSClient):
// English:   - Quotes: Sina → THS → EastMoney 3-level fallback chain; THS auto-breaks for 60s on failure;
// English:   - Sectors/IPOs: THS → EastMoney with 30s/60s TTL caches;
// English:   - News: THS (primary) → Sina (fallback), merged with dedup.
//
// Package data — a multi-source dispatch and circuit-breaker layer.
// It wraps EastMoney (MarketAPI) and Tonghuashun (THSClient) strategy:
//   - Quotes: Sina → THS → EastMoney fallback chain, THS auto-breaks for 60s;
//   - Sectors/IPOs: THS → EastMoney with 30s/60s TTL caches;
//   - News: THS (primary) → Sina (fallback), merged with dedup.
package data

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// DataCoordinator 多数据源调度与熔断。
// 行情数据源链：新浪 → 同花顺 → 东财
// 板块/IPO 数据源链：同花顺 → 东财
// English: DataCoordinator coordinates multiple sources with circuit breaking.
// English: Quote chain: Sina → THS → EastMoney; Sector/IPO chain: THS → EastMoney.
// DataCoordinator coordinates multiple sources with circuit breaking.
// Quote chain: Sina → THS → EastMoney; Sector/IPO chain: THS → EastMoney.
type DataCoordinator struct {
	eastMoney *MarketAPI
	ths       *THSClient

	// hithink 同花顺（新）官方数据源客户端，作为行情/板块的最高优先级源（东财永远兜底）。
	// 该字段为可选：默认 nil，由 server 层在持有 hithink 客户端时通过 SetHithink 注入；
	// 为 nil 时自动降级到后续链，不影响既有逻辑（不报错、不破坏调用方）。
	// English: hithink (the new Tonghuashun official client) is the top-priority source;
	// EastMoney is always the final fallback. It is optional and injected via SetHithink.
	hithink *HithinkClient

	mu sync.RWMutex

	thsDeadline time.Time // 同花顺熔断截止时间（失败后 60s 内不再尝试）
	// English: THS circuit-break deadline (no retry within 60s after a failure).

	sectorCache      []SectorInfo                  // 板块列表缓存
	sectorCacheAt    time.Time                     // 板块缓存写入时间（30s TTL）
	sectorStockCache map[string]cachedSectorStocks // 板块成分股缓存（60s TTL）
	// English: sectorCache: sector list cache; sectorCacheAt: cache write time (30s TTL);
	// English: sectorStockCache: per-sector constituent cache (60s TTL).

	ipoCache   []IPOEvent // 新股日历缓存
	ipoCacheAt time.Time  // 新股日历缓存写入时间（5min TTL）
	// English: ipoCache: IPO-calendar cache; ipoCacheAt: cache write time (5min TTL).

	ipoRefreshing atomic.Bool // IPO 日历刷新进行中标志（防 TTL 到期瞬间多调用方并发重复刷新）
	// English: ipoRefreshing: an IPO-calendar refresh is in flight (prevents concurrent
	// English: duplicate refreshes when multiple callers hit the expired-TTL path at once).
}

// cachedSectorStocks 板块成分股缓存条目。
// English: cachedSectorStocks is a sector-constituent cache entry.
// cachedSectorStocks is a sector-constituent cache entry.
type cachedSectorStocks struct {
	stocks []StockInfo // 缓存的成分股列表
	at     time.Time   // 缓存写入时间（用于判断是否过期）
	// English: stocks: cached constituent list; at: cache write time (used to check expiry).
}

// NewDataCoordinator 创建数据协调器实例。
// English: NewDataCoordinator creates a DataCoordinator instance.
// NewDataCoordinator creates a DataCoordinator instance.
func NewDataCoordinator(api *MarketAPI, ths *THSClient) *DataCoordinator {
	return &DataCoordinator{
		eastMoney:        api,
		ths:              ths,
		sectorStockCache: make(map[string]cachedSectorStocks),
	}
}

// SetHithink 注入同花顺（新）官方数据源客户端（可选）。
// 调用方（server 层）若已构造 *HithinkClient 则调用本方法注入，使行情/板块链路
// 以 hithink 为第一顺位、东财为最末兜底。未注入（h==nil）时功能自动降级，不报错。
// 注意：本方法不改变 NewDataCoordinator(api, ths) 签名，避免牵连大量调用方。
// English: SetHithink injects the optional Hithink (new THS) client as the top-priority
// source. When not called (nil), the chain degrades gracefully; EastMoney stays last.
func (dc *DataCoordinator) SetHithink(h *HithinkClient) {
	dc.mu.Lock()
	dc.hithink = h
	dc.mu.Unlock()
}

// thsAvailable §R3-2 P0-D1 熔断状态锁内读取：thsDeadline 此前被 GetQuote/GetKLine/
// GetMinuteKLine/GetSectors/GetSectorStocks 五条并发路径（fetcher 5s 循环 × HTTP handler ×
// 打分循环）裸读写——data race 且熔断时间戳撕裂会导致熔断失效或提前熔断。统一走本封装。
// English: R3-2 P0-D1 — locked read of the THS circuit-break deadline (previously read/written
// unlocked from five concurrent paths).
func (dc *DataCoordinator) thsAvailable() bool {
	dc.mu.RLock()
	defer dc.mu.RUnlock()
	return dc.ths != nil && time.Now().After(dc.thsDeadline)
}

// tripThs §R3-2 P0-D1 熔断置位锁内写入（默认 60s，与历史口径一致）。
// English: R3-2 P0-D1 — locked write that trips the THS breaker for d.
func (dc *DataCoordinator) tripThs() {
	dc.mu.Lock()
	dc.thsDeadline = time.Now().Add(60 * time.Second)
	dc.mu.Unlock()
}

// HealthCheck 探测所有行情源的可用性，委托给东财 MarketAPI 的健康检查。
// English: HealthCheck probes the availability of all quote sources, delegating to the EastMoney MarketAPI health check.
// （HealthCheck probes the availability of all market data sources, delegating to the EastMoney MarketAPI health check.）
func (dc *DataCoordinator) HealthCheck() map[string]bool {
	if dc.eastMoney == nil {
		return map[string]bool{"eastmoney": false, "sina": false, "tencent": false, "ths": false}
	}
	base := dc.eastMoney.HealthCheck()
	// 同花顺由 THSClient 探测（由 DataCoordinator 持有）
	// English: THS is probed by the THSClient (held by DataCoordinator).
	if dc.ths != nil {
		result := make(map[string]bool, 4)
		for k, v := range base {
			result[k] = v
		}
		result["ths"] = dc.ths.HealthCheck()
		return result
	}
	return base
}

// NewsSourceHealth 探测新闻资讯源的可用性。
// English: NewsSourceHealth probes the availability of news information sources.
// （NewsSourceHealth probes the availability of news information sources.）
// 探测三大主流资讯源：财联社、同花顺快讯、新浪
// English: Probes the three major news sources: CLS (Cailianshe), THS flash news, Sina.
// Probe the three major news sources: CLS, THS flash news, Sina
func (dc *DataCoordinator) NewsSourceHealth() map[string]bool {
	// 探测财联社：检查 eastMoney client 是否就绪（CLS 为主要新闻源）
	// English: Probe CLS: check whether the eastMoney client is ready (CLS is the primary news source).
	clsOk := dc.eastMoney != nil && dc.eastMoney.client != nil
	// 探测同花顺快讯：检查 THSClient 是否就绪
	// English: Probe THS flash news: check whether THSClient is ready.
	thsOk := dc.ths != nil
	// 探测新浪：简化判断，检查 eastMoney client 是否就绪
	// (新浪新闻通过 GetSinaNews 接口获取，同东财 client 就绪视为可用)
	// English: Probe Sina: simplified check on eastMoney client readiness (Sina news comes via GetSinaNews, available when the EastMoney client is ready).
	sinaOk := dc.eastMoney != nil && dc.eastMoney.client != nil
	return map[string]bool{
		"cainanshe": clsOk,
		"kuaixun":   thsOk,
		"sina":      sinaOk,
	}
}

// GetQuote 获取个股实时行情：同花顺（新）hithink → 新浪 → 同花顺 → 东财 四级降级链，
// 同花顺每失败一次熔断 60s。东财永远处于最末兜底位（绝不作为第一/主源）。
// 同花顺（新）hithink 为第一顺位：优先用 BatchQuotes 批量快照取该 code 的实时价。
// 四级源全部失败时返回明确错误（不再返回零值脏快照，避免前端显示 0.00 元）。
// English: GetQuote fetches a per-stock realtime quote down the chain
// hithink → Sina → THS → EastMoney; EastMoney is always the final fallback.
func (dc *DataCoordinator) GetQuote(code string) (*StockInfo, error) {
	// 空代码防御（§门控配套）：上游对空代码必然失败并刷错误日志
	// （周六实录：每秒数行"新浪/东财行情失败 ()"），直接本地拒绝。
	if code == "" {
		return nil, fmt.Errorf("空股票代码")
	}

	// ① 同花顺（新）hithink 第一顺位：批量快照命中则直接返回，失败/空则继续降级链。
	// hithink 为可选源（可能 nil），nil 时跳过本步，自动降级到后续链，不报错。
	// BatchQuotes 返回的 map 以裸码（去掉 sh/sz/bj 前缀）为键；入参 code 可能带前缀，
	// 故同时尝试原始 code 与去前缀后的裸码两种键。
	if hk := dc.hithink; hk != nil {
		if hkQuotes, hkErr := hk.BatchQuotes([]string{code}); hkErr == nil {
			if si := lookupHithinkQuote(hkQuotes, code); si != nil && si.Price > 0 {
				log.Printf("hithink(新)返回 %s 最新价 %.2f", code, si.Price)
				return si, nil
			}
		} else {
			log.Printf("hithink(新)行情失败 (%s): %v, 降级新浪", code, hkErr)
		}
	}

	// ② 新浪：hithink 缺失/失败时的主用源。
	si, err := dc.eastMoney.GetSinaQuote(code)
	if err == nil && si != nil && si.Price > 0 {
		return si, nil
	}
	if err != nil {
		log.Printf("新浪行情失败 (%s): %v, 降级同花顺", code, err)
	}

	// ③ 同花顺（旧）ths：保留原链，失败按既有逻辑熔断 60s。
	if dc.thsAvailable() {
		thsSi, thsErr := dc.ths.GetQuote(code)
		if thsErr == nil && thsSi != nil && thsSi.Price > 0 {
			log.Printf("同花顺返回 %s 最新价 %.2f", code, thsSi.Price)
			return thsSi, nil
		} else if thsErr != nil {
			dc.tripThs()
			log.Printf("同花顺失败 (%s): %v, 熔断60s", code, thsErr)
		}
	}

	// ④ 东财：永远处于最末兜底位（绝不作为第一/主源）。
	emSI, emErr := dc.eastMoney.GetRealtimeQuote(code)
	if emErr == nil && emSI != nil && emSI.Price > 0 {
		return emSI, nil
	}
	if emErr != nil {
		log.Printf("东财行情失败 (%s): %v", code, emErr)
	}

	// §D3 修复：四级源全部失败时不再返回 (nil/零值, nil) 脏快照——此前新浪返回空行时
	// si=nil,err=nil 直通 fetcher 写入 5s 快照、前端显示 0.00 元。现统一返回明确错误。
	// English: D3 fix — when all four sources fail, never return a zero-value snapshot with a
	// nil error (it flowed straight into the 5s snapshot and rendered 0.00 on the frontend).
	if si != nil && si.Price > 0 {
		return si, err
	}
	if err == nil {
		err = fmt.Errorf("全部行情源失败 (%s)", code)
	}
	return nil, err
}

// lookupHithinkQuote 从 hithink 批量快照结果中取指定 code 的行情。
// BatchQuotes 以裸码（去掉 sh/sz/bj 交易所前缀）为键；入参 code 可能带前缀，
// 故依次尝试原始 code 与去前缀裸码两种键，命中且价格有效则返回。
// English: lookupHithinkQuote picks the quote for code from the hithink batch result,
// trying both the raw code and the stripped bare code (BatchQuotes keys by bare code).
func lookupHithinkQuote(quotes map[string]*StockInfo, code string) *StockInfo {
	if si, ok := quotes[code]; ok {
		return si
	}
	// 去除常见交易所前缀（sh/sz/bj），回退到裸码键。
	bare := code
	if len(code) > 2 {
		switch code[:2] {
		case "sh", "sz", "bj":
			bare = code[2:]
		}
	}
	if si, ok := quotes[bare]; ok {
		return si
	}
	return nil
}

// GetKLine 获取 K 线数据。新浪日线 → 腾讯 → 同花顺 → 东财。
// 说明：同花顺（新）hithink 当前未提供通用 K 线接口（不臆造），故本方法保持
// 新浪/腾讯/同花顺/东财顺序，东财恒为最后兜底项。
// English: GetKLine fetches K-lines: Sina → Tencent → THS → EastMoney (EastMoney always last).
func (dc *DataCoordinator) GetKLine(code, period string, count int) ([]KLine, error) {
	if period == "101" {
		// §GAP3.6 日线降级链补全（原仅 新浪→东财 两级）：新浪 → 腾讯 → 同花顺 → 东财，
		// 腾讯日K客户端此前已实现但未接入，作为新浪被 IP 封禁时的主力兜底。
		if klines, err := dc.eastMoney.GetSinaKLine(code, count); err == nil && len(klines) > 0 {
			return klines, nil
		}
		if klines, err := dc.eastMoney.GetTencentKLine(code, count); err == nil && len(klines) > 0 {
			return klines, nil
		}
		if dc.thsAvailable() {
			thsKL, thsErr := dc.ths.GetTHSKLine(code)
			if thsErr == nil && len(thsKL) > 0 {
				return thsKL, nil
			} else if thsErr != nil {
				dc.tripThs()
				log.Printf("同花顺日线失败 (%s): %v, 熔断60s", code, thsErr)
			}
		}
	}

	if klines, err := dc.eastMoney.GetKLine(code, period, count); err == nil && len(klines) > 0 {
		return klines, nil
	}
	return nil, fmt.Errorf("所有K线源均失败 for %s", code)
}

// GetMinuteKLine 获取分钟级 K 线（分时）。新浪分钟 → 同花顺分钟 → 腾讯分钟 → 东财分钟。
// scale 为分钟数（1/5/15/30/60），返回按时间升序排列的 KLine。
// 说明：同花顺（新）hithink 当前未提供通用分时接口（不臆造），保持既有顺序，
// 东财恒为最后兜底项（绝不成为第一/主源）。
// English: GetMinuteKLine fetches minute K-lines (intraday). Sina → THS → Tencent → EastMoney
// (EastMoney always last; hithink has no generic intraday method, so the chain is unchanged).
func (dc *DataCoordinator) GetMinuteKLine(code string, scale, count int) ([]KLine, error) {
	if klines, err := dc.eastMoney.GetSinaMinuteKLine(code, scale, count); err == nil && len(klines) > 0 {
		return klines, nil
	}

	if dc.thsAvailable() {
		thsKL, thsErr := dc.ths.GetTHSMinuteKLine(code)
		if thsErr == nil && len(thsKL) > 0 {
			return thsKL, nil
		} else if thsErr != nil {
			dc.tripThs()
			log.Printf("同花顺分钟线失败 (%s): %v, 熔断60s", code, thsErr)
		}
	}

	if klines, err := dc.eastMoney.GetTencentMinuteKLine(code, scale, count); err == nil && len(klines) > 0 {
		return klines, nil
	}

	if klines, err := dc.eastMoney.GetKLine(code, strconv.Itoa(scale), count); err == nil && len(klines) > 0 {
		return klines, nil
	}
	return nil, fmt.Errorf("所有分钟K线源均失败 for %s", code)
}

// GetSectors 获取板块列表。同花顺(ths) → 东财。
// 说明：同花顺（新）hithink 当前未提供板块列表接口（不臆造），故保持 ths→东财，
// 东财恒为最末兜底（绝不成为第一/主源）。
// English: GetSectors fetches the sector list. THS → EastMoney (EastMoney always last;
// hithink has no board-list method, so the chain is unchanged).
func (dc *DataCoordinator) GetSectors() ([]SectorInfo, error) {
	dc.mu.RLock()
	if len(dc.sectorCache) > 0 && time.Since(dc.sectorCacheAt) < 30*time.Second {
		c := make([]SectorInfo, len(dc.sectorCache))
		copy(c, dc.sectorCache)
		dc.mu.RUnlock()
		return c, nil
	}
	dc.mu.RUnlock()

	var thsSectors []SectorInfo
	if dc.thsAvailable() {
		var thsErr error
		thsSectors, thsErr = dc.ths.GetBoardList()
		if thsErr == nil && len(thsSectors) > 0 {
			log.Printf("GetSectors: 同花顺 (%d个板块)", len(thsSectors))
		} else if thsErr != nil {
			dc.tripThs()
			log.Printf("同花顺板块列表失败: %v, 熔断60s", thsErr)
		}
	}

	s, emErr := dc.eastMoney.GetSectorList()
	if emErr != nil {
		log.Printf("东财板块不可用: %v", emErr)
	}

	if len(thsSectors) > 0 {
		if len(s) > 0 {
			// 合并策略：同花顺提供板块清单结构，东财提供实时行情数据
			// 1) 按代码/名称匹配同花顺板块，回填东财的涨跌幅/成交额/净流入/涨停家数
			// English: Merge strategy: THS provides the sector list structure, EastMoney provides realtime quotes.
			// English: 1) Match THS sectors by code/name and backfill EastMoney's change%, amount, net inflow, limit-up count.
			emByCode := make(map[string]SectorInfo, len(s))
			emByName := make(map[string]SectorInfo, len(s))
			for _, em := range s {
				emByCode[em.Code] = em
				emByName[em.Name] = em
			}
			used := make(map[string]bool, len(s))
			for i := range thsSectors {
				em, ok := emByCode[thsSectors[i].Code]
				if !ok {
					em, ok = emByName[thsSectors[i].Name]
				}
				if ok {
					thsSectors[i].ChangePct = em.ChangePct
					thsSectors[i].Amount = em.Amount
					thsSectors[i].NetInflow = em.NetInflow
					thsSectors[i].LimitupCnt = em.LimitupCnt
					used[em.Code] = true
				}
			}
			thsNames := make(map[string]bool, len(thsSectors))
			for _, t := range thsSectors {
				thsNames[t.Name] = true
			}
			// 2) 东财独有的板块（同花顺无此代码且无此名称）追加到末尾，保证板块覆盖面
			// English: 2) EastMoney-only sectors (no matching THS code/name) are appended at the end to keep coverage.
			for _, em := range s {
				if !used[em.Code] && !thsNames[em.Name] {
					thsSectors = append(thsSectors, em)
				}
			}
			log.Printf("GetSectors: 同花顺(%d个) + 东财实时(%d个)", len(thsSectors), len(s))
		}
		dc.mu.Lock()
		dc.sectorCache = thsSectors
		dc.sectorCacheAt = time.Now()
		dc.mu.Unlock()
		return thsSectors, nil
	}

	if len(s) > 0 {
		dc.mu.Lock()
		dc.sectorCache = s
		dc.sectorCacheAt = time.Now()
		dc.mu.Unlock()
		log.Printf("GetSectors: 东财 (%d个板块)", len(s))
		return s, nil
	}

	return nil, fmt.Errorf("所有板块源均失败")
}

// GetSectorStocks 获取板块成分股。同花顺(ths) → 东财。
// 说明：同花顺（新）hithink 当前未提供板块成分股接口（不臆造），故保持 ths→东财，
// 东财恒为最末兜底（绝不成为第一/主源）。
// English: GetSectorStocks fetches sector constituents. THS → EastMoney (EastMoney always
// last; hithink has no board-constituent method, so the chain is unchanged).
func (dc *DataCoordinator) GetSectorStocks(sectorCode string, topN int) ([]StockInfo, error) {
	dc.mu.RLock()
	if c, ok := dc.sectorStockCache[sectorCode]; ok && time.Since(c.at) < 60*time.Second {
		out := make([]StockInfo, len(c.stocks))
		copy(out, c.stocks)
		dc.mu.RUnlock()
		return out, nil
	}
	dc.mu.RUnlock()

	// 同花顺优先：东财被限流时板块成分股改走同花顺。
	// English: THS-first: when EastMoney is rate-limited, sector constituents route to THS.
	// THS-first: when EastMoney is rate-limited, sector constituents come from THS.
	if dc.thsAvailable() {
		thsCode, thsName := dc.matchTHSBoardCode(sectorCode)
		if thsCode == "" {
			thsCode = sectorCode
		}
		stockList, thsErr := dc.ths.GetBoardStocks(thsCode, topN)
		if thsErr == nil && len(stockList) > 0 {
			// 只取代码/名称，实时行情后续由 BuildScoringData/快照兜底补全
			// English: Only keep code/name; realtime quotes are backfilled later by BuildScoringData/snapshot.
			codes := make([]StockInfo, 0, len(stockList))
			for _, st := range stockList {
				codes = append(codes, StockInfo{Code: st.Code, Name: st.Name})
			}
			dc.mu.Lock()
			dc.sectorStockCache[sectorCode] = cachedSectorStocks{stocks: codes, at: time.Now()}
			dc.mu.Unlock()
			log.Printf("GetSectorStocks: 同花顺取 %d 只成分股 (%s%s)", len(codes), sectorCode, func() string {
				if thsName != "" {
					return "/" + thsName
				}
				return ""
			}())
			return codes, nil
		}
	}

	// 东财兜底
	// English: EastMoney fallback.
	s, err := dc.eastMoney.GetSectorStocks(sectorCode, topN)
	if err == nil && len(s) > 0 {
		dc.mu.Lock()
		dc.sectorStockCache[sectorCode] = cachedSectorStocks{stocks: s, at: time.Now()}
		dc.mu.Unlock()
		return s, nil
	}
	if err != nil {
		log.Printf("东财板块成分股失败 (%s): %v", sectorCode, err)
	}
	return s, err
}

// matchTHSBoardCode 将入参板块代码映射到同花顺板块代码。
// 入参可能是同花顺代码（308xxx/881xxx，来自 sector_scanner）或东财 BK 代码。
// 返回同花顺板块代码与名称；映射失败时返回空串（调用方回退用原始代码尝试）。
// English: matchTHSBoardCode maps an input sector code to a THS board code.
// English: The input may be a THS code (308xxx/881xxx, from sector_scanner) or an EastMoney BK code.
// English: Returns the THS code and name; empty string on failure (caller falls back to the raw code).
// matchTHSBoardCode maps an incoming sector code to a THS board code. The input may
// already be a THS code (308xxx/881xxx, from sector_scanner) or an EastMoney BK code.
func (dc *DataCoordinator) matchTHSBoardCode(sectorCode string) (string, string) {
	dc.mu.RLock()
	defer dc.mu.RUnlock()
	// 1) 直接精确匹配（入参已是同花顺代码）
	// English: 1) Direct exact match (input is already a THS code).
	for _, sec := range dc.sectorCache {
		if sec.Code == sectorCode {
			return sec.Code, sec.Name
		}
	}
	// 2) 东财 BK 代码 → 剥离前缀尝试数字段（BK0477 ↔ 同花顺 885477 偶有对应）
	// English: 2) EastMoney BK code → strip the prefix and try the numeric part (BK0477 ↔ THS 885477 occasionally correspond).
	if strings.HasPrefix(sectorCode, "BK") {
		try := strings.TrimPrefix(sectorCode, "BK")
		for _, sec := range dc.sectorCache {
			if sec.Code == try {
				return sec.Code, sec.Name
			}
		}
	}
	// 3) 名称匹配：东财板块代码在 sectorCache 中对应的名称去匹配同花顺板块
	// English: 3) Name matching: use the name of an EastMoney code in sectorCache to match a THS sector.
	for _, sec := range dc.sectorCache {
		if sec.Code == sectorCode && sec.Name != "" {
			for _, sec2 := range dc.sectorCache {
				if sec2.Name == sec.Name && sec2.Code != sec.Code {
					return sec2.Code, sec2.Name
				}
			}
		}
	}
	return "", ""
}

// GetStockMoneyFlow 获取资金流向。仅东财。
// English: GetStockMoneyFlow fetches capital flow. EastMoney only.
// GetStockMoneyFlow fetches capital flow (EastMoney only).
func (dc *DataCoordinator) GetStockMoneyFlow(code string) (*CapitalFlow, error) {
	return dc.eastMoney.GetStockMoneyFlow(code)
}

// GetIndexData 获取指数行情。
// English: GetIndexData fetches index quotes.
// GetIndexData fetches the index data via EastMoney.
func (dc *DataCoordinator) GetIndexData() (indexPrice float64, ma20 float64, upCount, downCount int, err error) {
	return dc.eastMoney.GetIndexData()
}

// CrossCheckPrice 用东财 push2 获取个股价格，用于信号复核。
// English: CrossCheckPrice fetches a stock price via EastMoney push2 for signal cross-checking.
// CrossCheckPrice returns a price via EastMoney push2 for signal cross-checking.
func (dc *DataCoordinator) CrossCheckPrice(code string) (price float64, err error) {
	si, err := dc.eastMoney.GetRealtimeQuote(code)
	if err != nil || si == nil {
		return 0, err
	}
	return si.Price, nil
}

// SourceName 返回当前首选数据源的名称。
// English: SourceName returns the name of the current primary data source.
// SourceName returns the name of the current primary data source.
func (dc *DataCoordinator) SourceName() string {
	return "Sina"
}

// GetAuctionData 获取集合竞价数据。
// English: GetAuctionData fetches pre-open auction data.
// GetAuctionData fetches pre-open auction data via EastMoney.
func (dc *DataCoordinator) GetAuctionData(code string) (*StockInfo, error) {
	return dc.eastMoney.GetAuctionData(code)
}

// GetHotNews 多源合并获取热门新闻，按 pageSize 截顶返回。
// 同花顺快讯(主源) → 新浪财经(兜底)
// English: GetHotNews merges hot news across sources, capped at pageSize.
// English: THS flash news (primary) → Sina Finance (fallback).
// GetHotNews merges hot news across sources (THS primary → Sina fallback),
// deduplicating by truncated titles and capping at pageSize.
func (dc *DataCoordinator) GetHotNews(pageSize int) []NewsItem {
	seen := make(map[string]bool)
	var all []NewsItem

	if items, err := dc.eastMoney.GetTonghuashunNews(pageSize); err == nil && len(items) > 0 {
		for _, n := range items {
			key := truncateStr(n.Title, 60)
			if !seen[key] {
				seen[key] = true
				all = append(all, n)
			}
		}
	}

	if items, err := dc.eastMoney.GetSinaNews(pageSize); err == nil && len(items) > 0 {
		for _, n := range items {
			key := truncateStr(n.Title, 60)
			if !seen[key] {
				seen[key] = true
				all = append(all, n)
			}
		}
	}

	if len(all) > pageSize {
		all = all[:pageSize]
	}
	if len(all) == 0 {
		log.Printf("GetHotNews: 所有新闻源均无数据")
	}
	return all
}

// truncateStr 将字符串按 rune 截断到 maxLen 长度（保留中文字符完整性）。
// 用于新闻标题去重的归一化 key 生成。
// English: truncateStr truncates a string to maxLen runes (preserving multi-byte Chinese characters intact).
// English: Used to build normalized dedup keys for news titles.
// truncateStr truncates a string to maxLen runes (keeping multi-byte Chinese
// characters intact), used to build normalized dedup keys for news titles.
func truncateStr(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) > maxLen {
		return string(runes[:maxLen])
	}
	return s
}

// RefreshIPOCalendar 刷新新股日历缓存。同花顺(板块丰富) → 东财
// English: RefreshIPOCalendar refreshes the IPO-calendar cache. THS (rich sectors) → EastMoney.
// RefreshIPOCalendar refreshes the IPO-calendar cache (5min TTL),
// populating from EastMoney then enriching each record's sector.
//
// §GAP-20260826 行情卡顿根修：旧实现全程持有 dc.mu（保护行情降级链/板块缓存的同一把全局锁）
// 执行东财日历拉取 + enrichIPOSector 逐条串行 HTTP——一次慢刷新（数秒~数十秒）会阻塞所有
// goroutine 的 GetQuote/GetKLine/GetSectors，fetcher 5s 节拍被卡死 → 前端股价冻结/刷新极慢。
// 新实现三段式：①锁内仅做 TTL 快检；②网络取数与板块丰富全部在锁外；③锁内只做指针换装(O(1))。
// 另加 atomic 防并发刷新风暴 + 失败 60s 负缓存（旧实现失败不推进 TTL，上游故障时每个请求都重打全量接口）。
// English: Root fix for quote latency: the old version held dc.mu (the same global lock guarding the
// English: quote fallback chain / sector caches) while doing the EastMoney calendar fetch plus a serial
// English: per-IPO enrichment — one slow refresh stalled every GetQuote/GetKLine/GetSectors caller and
// English: froze the fetcher's 5s cadence. Now: ① TTL fast-path under lock; ② all network IO outside the
// English: lock; ③ O(1) pointer swap under lock. Plus an atomic in-flight guard against refresh storms and
// English: a 60s negative cache on failure (the old code left ipoCacheAt untouched, so an upstream outage
// English: re-hit the full API on every single request).
func (dc *DataCoordinator) RefreshIPOCalendar() {
	// ① 快路径：TTL 未到期直接返回（锁内只读两个时间字段，零网络）。
	// English: ① Fast path: return immediately when the cache is still fresh (lock held only to read two fields, zero network).
	dc.mu.Lock()
	if !dc.ipoCacheAt.IsZero() && time.Since(dc.ipoCacheAt) < 5*time.Minute {
		dc.mu.Unlock()
		return
	}
	dc.mu.Unlock()

	// 防并发：同一时刻只允许一个刷新在跑，其余调用方直接放弃（它们下轮自然拿到新缓存或再触发）。
	// English: Single-flight guard: only one refresh runs at a time; concurrent callers bail out and
	// English: will observe the refreshed cache on a subsequent call.
	if !dc.ipoRefreshing.CompareAndSwap(false, true) {
		return
	}
	defer dc.ipoRefreshing.Store(false)

	// ② 网络取数 + 板块丰富：全部在 dc.mu 之外执行，绝不阻塞行情链路。
	// English: ② Network fetch + sector enrichment run entirely outside dc.mu, never blocking the quote path.
	var list []IPOEvent
	if dc.eastMoney != nil {
		l, err := dc.eastMoney.GetEastMoneyIPOCalendar()
		if err == nil && len(l) > 0 {
			list = l
		} else if err != nil {
			log.Printf("IPO日历 东财 失败: %v", err)
		}
	}
	if len(list) == 0 {
		// 失败负缓存：推进 TTL 60s，避免上游故障期间每个请求都重打全量日历接口。
		// English: Negative cache on failure: advance the TTL by 60s so an upstream outage doesn't
		// English: turn every request into a full calendar refetch.
		dc.mu.Lock()
		dc.ipoCacheAt = time.Now().Add(-5*time.Minute + 60*time.Second)
		dc.mu.Unlock()
		return
	}
	dc.enrichIPOSector(list)

	// ③ 发布：锁内只做指针换装与 TTL 推进（O(1)，微秒级）。
	// English: ③ Publish: pointer swap + TTL bump under the lock (O(1), microseconds).
	dc.mu.Lock()
	dc.ipoCache = list
	dc.ipoCacheAt = time.Now()
	dc.mu.Unlock()
	log.Printf("IPO日历: 东财加载 %d 条", len(list))
}

// enrichIPOSector 为新股日历事件补充所属行业板块。
// 逐条调用东财行业查询接口（GetStockIndustry），缺失行业的事件保留空值。
// English: enrichIPOSector fills each IPO event's industry sector.
// English: Calls EastMoney's GetStockIndustry per event; events without a sector keep an empty value.
// enrichIPOSector fills each IPO event's sector via EastMoney's GetStockIndustry;
// events with no sector keep an empty value.
func (dc *DataCoordinator) enrichIPOSector(list []IPOEvent) {
	if dc.eastMoney == nil {
		return
	}
	for i := range list {
		sector := dc.eastMoney.GetStockIndustry(list[i].Code)
		if sector != "" {
			list[i].Sector = sector
		}
	}
	hasSector := 0
	for _, ev := range list {
		if ev.Sector != "" {
			hasSector++
		}
	}
	log.Printf("IPO日历: 板块填充 %d/%d 只", hasSector, len(list))
}

// GetIPOByCode 按股票代码查询新股日历事件。
// English: GetIPOByCode looks up an IPO-calendar event by stock code.
// GetIPOByCode looks up an IPO event by stock code, refreshing the cache if empty.
func (dc *DataCoordinator) GetIPOByCode(code string) *IPOEvent {
	dc.mu.RLock()
	cache := dc.ipoCache
	dc.mu.RUnlock()

	if len(cache) == 0 {
		dc.RefreshIPOCalendar()
		dc.mu.RLock()
		cache = dc.ipoCache
		dc.mu.RUnlock()
	}

	for i := range cache {
		if cache[i].Code == code {
			return &cache[i]
		}
	}
	return nil
}

// GetAllIPOCalendar 返回全部新股日历数据。
// English: GetAllIPOCalendar returns all IPO-calendar data.
// GetAllIPOCalendar returns all IPO-calendar data, refreshing when empty.
func (dc *DataCoordinator) GetAllIPOCalendar() []IPOEvent {
	dc.mu.RLock()
	cache := dc.ipoCache
	dc.mu.RUnlock()

	if len(cache) == 0 {
		dc.RefreshIPOCalendar()
		dc.mu.RLock()
		cache = dc.ipoCache
		dc.mu.RUnlock()
	}

	return cache
}

// GetStockSector 查询个股所属板块名称。
// English: GetStockSector returns the sector name of a stock.
// GetStockSector returns the sector name of a stock via EastMoney.
func (dc *DataCoordinator) GetStockSector(code string) string {
	if dc.eastMoney != nil {
		return dc.eastMoney.GetStockIndustry(code)
	}
	return ""
}

// TushareToken 保留以供前端初始化配置页面展示（已不再实际使用）。
// English: TushareToken is kept for display on the frontend config page (no longer used).
// TushareToken is kept only for display on the frontend config page (no longer used).
func TushareToken() string {
	return os.Getenv("TUSHARE_TOKEN")
}
