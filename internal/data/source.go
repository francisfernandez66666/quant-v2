package data

import (
	"fmt"
	"log"
	"os"
	"sync"
	"time"
)

// DataCoordinator 多数据源调度与熔断。
// 行情数据源链：新浪 → 同花顺 → 东财
// 板块/IPO 数据源链：同花顺 → 东财
type DataCoordinator struct {
	eastMoney *MarketAPI
	ths       *THSClient

	mu sync.RWMutex

	thsDeadline time.Time

	sectorCache      []SectorInfo
	sectorCacheAt    time.Time
	sectorStockCache map[string]cachedSectorStocks

	ipoCache   []IPOEvent
	ipoCacheAt time.Time
}

type cachedSectorStocks struct {
	stocks []StockInfo
	at     time.Time
}

func NewDataCoordinator(api *MarketAPI, ths *THSClient) *DataCoordinator {
	return &DataCoordinator{
		eastMoney:        api,
		ths:              ths,
		sectorStockCache: make(map[string]cachedSectorStocks),
	}
}

// GetQuote 获取个股实时行情。新浪 → 同花顺 → 东财
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

	return si, err
}

// GetKLine 获取 K 线数据。新浪日线 → 东财
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

// GetSectors 获取板块列表。同花顺 → 东财
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
func (dc *DataCoordinator) GetSectorStocks(sectorCode string, topN int) ([]StockInfo, error) {
	dc.mu.RLock()
	if c, ok := dc.sectorStockCache[sectorCode]; ok && time.Since(c.at) < 60*time.Second {
		out := make([]StockInfo, len(c.stocks))
		copy(out, c.stocks)
		dc.mu.RUnlock()
		return out, nil
	}
	dc.mu.RUnlock()

	s, err := dc.eastMoney.GetSectorStocks(sectorCode, topN)
	if err == nil && len(s) > 0 {
		dc.mu.Lock()
		dc.sectorStockCache[sectorCode] = cachedSectorStocks{stocks: s, at: time.Now()}
		dc.mu.Unlock()
		return s, nil
	}

	if dc.ths != nil && time.Now().After(dc.thsDeadline) {
		dc.mu.RLock()
		sectorName := ""
		for _, sec := range dc.sectorCache {
			if sec.Code == sectorCode {
				sectorName = sec.Name
				break
			}
		}
		dc.mu.RUnlock()

		if sectorName != "" {
			stockInfo, thsErr := dc.ths.GetQuote(sectorCode)
			if thsErr == nil && stockInfo != nil {
				dc.mu.Lock()
				dc.sectorStockCache[sectorCode] = cachedSectorStocks{stocks: []StockInfo{
					{Code: sectorCode, Name: sectorName, Price: stockInfo.Price},
				}, at: time.Now()}
				dc.mu.Unlock()
				return []StockInfo{
					{Code: sectorCode, Name: sectorName, Price: stockInfo.Price},
				}, nil
			}
		}
	}

	return s, err
}

// GetStockMoneyFlow 获取资金流向。仅东财。
func (dc *DataCoordinator) GetStockMoneyFlow(code string) (*CapitalFlow, error) {
	return dc.eastMoney.GetStockMoneyFlow(code)
}

// GetIndexData 获取指数行情。
func (dc *DataCoordinator) GetIndexData() (indexPrice float64, ma20 float64, upCount, downCount int, err error) {
	return dc.eastMoney.GetIndexData()
}

// CrossCheckPrice 用东财 push2 获取个股价格，用于信号复核。
func (dc *DataCoordinator) CrossCheckPrice(code string) (price float64, err error) {
	si, err := dc.eastMoney.GetRealtimeQuote(code)
	if err != nil || si == nil {
		return 0, err
	}
	return si.Price, nil
}

// SourceName 返回当前首选数据源的名称。
func (dc *DataCoordinator) SourceName() string {
	return "Sina"
}

// GetAuctionData 获取集合竞价数据。
func (dc *DataCoordinator) GetAuctionData(code string) (*StockInfo, error) {
	return dc.eastMoney.GetAuctionData(code)
}

// GetHotNews 多源合并获取热门新闻，按 pageSize 截顶返回。
// 同花顺快讯(主源) → 新浪财经(兜底)
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

func truncateStr(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) > maxLen {
		return string(runes[:maxLen])
	}
	return s
}

// RefreshIPOCalendar 刷新新股日历缓存。同花顺(板块丰富) → 东财
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
func (dc *DataCoordinator) GetStockSector(code string) string {
	if dc.eastMoney != nil {
		return dc.eastMoney.GetStockIndustry(code)
	}
	return ""
}

// TushareToken 保留以供前端初始化配置页面展示（已不再实际使用）。
func TushareToken() string {
	return os.Getenv("TUSHARE_TOKEN")
}
