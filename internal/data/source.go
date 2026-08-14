// Package data — 多数据源调度与熔断协调层。
// 统一封装东方财富(MarketAPI)与同花顺(THSClient)的调度策略：
//   - 行情：新浪 → 同花顺 → 东财 三级降级链，同花顺失败自动熔断 60s；
//   - 板块/IPO：同花顺 → 东财，带 30s/60s TTL 缓存；
//   - 新闻：同花顺(主) → 新浪(兜底) 去重合并。
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
	"time"
)

// DataCoordinator 多数据源调度与熔断。
// 行情数据源链：新浪 → 同花顺 → 东财
// 板块/IPO 数据源链：同花顺 → 东财
// DataCoordinator coordinates multiple sources with circuit breaking.
// Quote chain: Sina → THS → EastMoney; Sector/IPO chain: THS → EastMoney.
type DataCoordinator struct {
	eastMoney *MarketAPI
	ths       *THSClient

	mu sync.RWMutex

	thsDeadline time.Time // 同花顺熔断截止时间（失败后 60s 内不再尝试）

	sectorCache      []SectorInfo                  // 板块列表缓存
	sectorCacheAt    time.Time                     // 板块缓存写入时间（30s TTL）
	sectorStockCache map[string]cachedSectorStocks // 板块成分股缓存（60s TTL）

	ipoCache   []IPOEvent // 新股日历缓存
	ipoCacheAt time.Time  // 新股日历缓存写入时间（5min TTL）
}

// cachedSectorStocks 板块成分股缓存条目。
// cachedSectorStocks is a sector-constituent cache entry.
type cachedSectorStocks struct {
	stocks []StockInfo // 缓存的成分股列表
	at     time.Time   // 缓存写入时间（用于判断是否过期）
}

// NewDataCoordinator 创建数据协调器实例。
// NewDataCoordinator creates a DataCoordinator instance.
func NewDataCoordinator(api *MarketAPI, ths *THSClient) *DataCoordinator {
	return &DataCoordinator{
		eastMoney:        api,
		ths:              ths,
		sectorStockCache: make(map[string]cachedSectorStocks),
	}
}

// GetQuote 获取个股实时行情。新浪 → 同花顺 → 东财
// GetQuote fetches a per-stock realtime quote down the chain Sina → THS → EastMoney,
// tripping THS for 60s after each THS failure.
// HealthCheck 探测所有行情源的可用性，委托给东财 MarketAPI 的健康检查。
// （HealthCheck probes the availability of all market data sources, delegating to the EastMoney MarketAPI health check.）
func (dc *DataCoordinator) HealthCheck() map[string]bool {
	if dc.eastMoney == nil {
		return map[string]bool{"eastmoney": false, "sina": false, "tencent": false, "ths": false}
	}
	base := dc.eastMoney.HealthCheck()
	// 同花顺由 THSClient 探测（由 DataCoordinator 持有）
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
// （NewsSourceHealth probes the availability of news information sources.）
// 探测三大主流资讯源：财联社、同花顺快讯、新浪
// Probe the three major news sources: CLS, THS flash news, Sina
func (dc *DataCoordinator) NewsSourceHealth() map[string]bool {
	// 探测财联社：检查 eastMoney client 是否就绪（CLS 为主要新闻源）
	clsOk := dc.eastMoney != nil && dc.eastMoney.client != nil
	// 探测同花顺快讯：检查 THSClient 是否就绪
	thsOk := dc.ths != nil
	// 探测新浪：简化判断，检查 eastMoney client 是否就绪
	// (新浪新闻通过 GetSinaNews 接口获取，同东财 client 就绪视为可用)
	sinaOk := dc.eastMoney != nil && dc.eastMoney.client != nil
	return map[string]bool{
		"cainanshe": clsOk,
		"kuaixun":   thsOk,
		"sina":      sinaOk,
	}
}
func (dc *DataCoordinator) GetQuote(code string) (*StockInfo, error) {
	si, err := dc.eastMoney.GetSinaQuote(code)
	if err == nil && si != nil && si.Price > 0 {
		return si, nil
	}
	if err != nil {
		log.Printf("新浪行情失败 (%s): %v, 降级同花顺", code, err)
	}

	if dc.ths != nil && time.Now().After(dc.thsDeadline) {
		thsSi, thsErr := dc.ths.GetQuote(code)
		if thsErr == nil && thsSi != nil && thsSi.Price > 0 {
			log.Printf("同花顺返回 %s 最新价 %.2f", code, thsSi.Price)
			return thsSi, nil
		} else if thsErr != nil {
			dc.thsDeadline = time.Now().Add(60 * time.Second)
			log.Printf("同花顺失败 (%s): %v, 熔断60s", code, thsErr)
		}
	}

	emSI, emErr := dc.eastMoney.GetRealtimeQuote(code)
	if emErr == nil && emSI != nil && emSI.Price > 0 {
		return emSI, nil
	}
	if emErr != nil {
		log.Printf("东财行情失败 (%s): %v", code, emErr)
	}

	// 三级源全部失败，兜底返回新浪的错误信息（优先级最高的源）
	return si, err
}

// GetKLine 获取 K 线数据。新浪日线 → 东财
// GetKLine fetches K-lines: Sina daily first, then EastMoney.
func (dc *DataCoordinator) GetKLine(code, period string, count int) ([]KLine, error) {
	if period == "101" {
		if klines, err := dc.eastMoney.GetSinaKLine(code, count); err == nil && len(klines) > 0 {
			return klines, nil
		}
	}

	if klines, err := dc.eastMoney.GetKLine(code, period, count); err == nil && len(klines) > 0 {
		return klines, nil
	}
	return nil, fmt.Errorf("所有K线源均失败 for %s", code)
}

// GetMinuteKLine 获取分钟级 K 线（分时）。新浪分钟 → 同花顺分钟 → 腾讯分钟 → 东财分钟。
// scale 为分钟数（1/5/15/30/60），返回按时间升序排列的 KLine。
// GetMinuteKLine fetches minute K-lines (分时). Chain: Sina → THS → Tencent → EastMoney.
func (dc *DataCoordinator) GetMinuteKLine(code string, scale, count int) ([]KLine, error) {
	if klines, err := dc.eastMoney.GetSinaMinuteKLine(code, scale, count); err == nil && len(klines) > 0 {
		return klines, nil
	}

	if dc.ths != nil && time.Now().After(dc.thsDeadline) {
		thsKL, thsErr := dc.ths.GetTHSMinuteKLine(code)
		if thsErr == nil && len(thsKL) > 0 {
			return thsKL, nil
		} else if thsErr != nil {
			dc.thsDeadline = time.Now().Add(60 * time.Second)
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

// GetSectors 获取板块列表。同花顺 → 东财
// GetSectors returns the board list from THS → EastMoney with a 30s cache,
// merging THS board inventory with EastMoney realtime data when both succeed.
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
	if dc.ths != nil && time.Now().After(dc.thsDeadline) {
		var thsErr error
		thsSectors, thsErr = dc.ths.GetBoardList()
		if thsErr == nil && len(thsSectors) > 0 {
			log.Printf("GetSectors: 同花顺 (%d个板块)", len(thsSectors))
		} else if thsErr != nil {
			dc.thsDeadline = time.Now().Add(60 * time.Second)
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

// GetSectorStocks 获取板块成分股。东财 → 同花顺
// GetSectorStocks returns a sector's constituent stocks (EastMoney first, THS
// fallback) with a 60s per-sector cache.
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
	// THS-first: when EastMoney is rate-limited, sector constituents come from THS.
	if dc.ths != nil && time.Now().After(dc.thsDeadline) {
		thsCode, thsName := dc.matchTHSBoardCode(sectorCode)
		if thsCode == "" {
			thsCode = sectorCode
		}
		stockList, thsErr := dc.ths.GetBoardStocks(thsCode, topN)
		if thsErr == nil && len(stockList) > 0 {
			// 只取代码/名称，实时行情后续由 BuildScoringData/快照兜底补全
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
// matchTHSBoardCode maps an incoming sector code to a THS board code. The input may
// already be a THS code (308xxx/881xxx, from sector_scanner) or an EastMoney BK code.
func (dc *DataCoordinator) matchTHSBoardCode(sectorCode string) (string, string) {
	dc.mu.RLock()
	defer dc.mu.RUnlock()
	// 1) 直接精确匹配（入参已是同花顺代码）
	for _, sec := range dc.sectorCache {
		if sec.Code == sectorCode {
			return sec.Code, sec.Name
		}
	}
	// 2) 东财 BK 代码 → 剥离前缀尝试数字段（BK0477 ↔ 同花顺 885477 偶有对应）
	if strings.HasPrefix(sectorCode, "BK") {
		try := strings.TrimPrefix(sectorCode, "BK")
		for _, sec := range dc.sectorCache {
			if sec.Code == try {
				return sec.Code, sec.Name
			}
		}
	}
	// 3) 名称匹配：东财板块代码在 sectorCache 中对应的名称去匹配同花顺板块
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
// GetStockMoneyFlow fetches capital flow (EastMoney only).
func (dc *DataCoordinator) GetStockMoneyFlow(code string) (*CapitalFlow, error) {
	return dc.eastMoney.GetStockMoneyFlow(code)
}

// GetIndexData 获取指数行情。
// GetIndexData fetches the index data via EastMoney.
func (dc *DataCoordinator) GetIndexData() (indexPrice float64, ma20 float64, upCount, downCount int, err error) {
	return dc.eastMoney.GetIndexData()
}

// CrossCheckPrice 用东财 push2 获取个股价格，用于信号复核。
// CrossCheckPrice returns a price via EastMoney push2 for signal cross-checking.
func (dc *DataCoordinator) CrossCheckPrice(code string) (price float64, err error) {
	si, err := dc.eastMoney.GetRealtimeQuote(code)
	if err != nil || si == nil {
		return 0, err
	}
	return si.Price, nil
}

// SourceName 返回当前首选数据源的名称。
// SourceName returns the name of the current primary data source.
func (dc *DataCoordinator) SourceName() string {
	return "Sina"
}

// GetAuctionData 获取集合竞价数据。
// GetAuctionData fetches pre-open auction data via EastMoney.
func (dc *DataCoordinator) GetAuctionData(code string) (*StockInfo, error) {
	return dc.eastMoney.GetAuctionData(code)
}

// GetHotNews 多源合并获取热门新闻，按 pageSize 截顶返回。
// 同花顺快讯(主源) → 新浪财经(兜底)
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
// RefreshIPOCalendar refreshes the IPO-calendar cache (5min TTL),
// populating from EastMoney then enriching each record's sector.
func (dc *DataCoordinator) RefreshIPOCalendar() {
	dc.mu.Lock()
	defer dc.mu.Unlock()

	if !dc.ipoCacheAt.IsZero() && time.Since(dc.ipoCacheAt) < 5*time.Minute {
		return
	}

	if dc.eastMoney != nil {
		list, err := dc.eastMoney.GetEastMoneyIPOCalendar()
		if err == nil && len(list) > 0 {
			dc.enrichIPOSector(list)
			dc.ipoCache = list
			dc.ipoCacheAt = time.Now()
			log.Printf("IPO日历: 东财加载 %d 条", len(list))
			return
		}
		log.Printf("IPO日历 东财 失败: %v", err)
	}
}

// enrichIPOSector 为新股日历事件补充所属行业板块。
// 逐条调用东财行业查询接口（GetStockIndustry），缺失行业的事件保留空值。
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
// GetStockSector returns the sector name of a stock via EastMoney.
func (dc *DataCoordinator) GetStockSector(code string) string {
	if dc.eastMoney != nil {
		return dc.eastMoney.GetStockIndustry(code)
	}
	return ""
}

// TushareToken 保留以供前端初始化配置页面展示（已不再实际使用）。
// TushareToken is kept only for display on the frontend config page (no longer used).
func TushareToken() string {
	return os.Getenv("TUSHARE_TOKEN")
}
