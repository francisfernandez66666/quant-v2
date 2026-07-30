// Package data — DataCoordinator 多数据源调度与熔断。
// 按优先级 Tushare > 新浪 > 东财 > 同花顺 依次尝试获取行情，
// 任一源连续失败时触发 60 秒熔断，避免无效重试。
package data

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// DataCoordinator 数据协调器。
// 封装三个数据源（东财、Tushare、同花顺），
// 按优先级自动降级，带熔断机制（Tushare/同花顺 失败后熔断 60 秒）。
type DataCoordinator struct {
	eastMoney *MarketAPI     // 东方财富 + 新浪（主力）
	tushare   *TushareClient // Tushare Pro（首选，有 token 时启用）
	ths       *THSClient     // 同花顺（第三备用）
	jq        *JQClient      // 聚宽（第四备用，板块降级）

	mu sync.RWMutex

	tushareDeadline time.Time // Tushare 熔断截止时间
	thsDeadline     time.Time // 同花顺熔断截止时间

	// 板块数据缓存（60秒）
	sectorCache      []SectorInfo
	sectorCacheAt    time.Time
	sectorStockCache map[string]cachedSectorStocks

	// Tushare THS 板块缓存（用于东财降级）
	thsIndexList []ThsSectorInfo
	thsIndexAt   time.Time

	// 新股日历缓存（1小时TTL）
	ipoCache   []IPOEvent
	ipoCacheAt time.Time
}

type cachedSectorStocks struct {
	stocks []StockInfo
	at     time.Time
}

// NewDataCoordinator 创建数据协调器。
// 所有参数可 nil，内部会做 nil 判断。
func NewDataCoordinator(api *MarketAPI, tushare *TushareClient, ths *THSClient, jq *JQClient) *DataCoordinator {
	return &DataCoordinator{
		eastMoney:        api,
		tushare:          tushare,
		ths:              ths,
		jq:               jq,
		sectorStockCache: make(map[string]cachedSectorStocks),
	}
}

// GetQuote 获取个股实时行情（多源降级）。
// 新浪有数据就直接返回，不请求后面的源。只有新浪失败才逐级降级。
func (dc *DataCoordinator) GetQuote(code string) (*StockInfo, error) {
	// P1: 新浪行情（主力高频）
	si, err := dc.eastMoney.GetSinaQuote(code)
	if err == nil && si != nil {
		if si.Price > 0 {
			return si, nil
		}
		// 价格=0（盘前/盘后），保存结果备降级
	}
	if err != nil {
		log.Printf("新浪行情失败 (%s): %v, 降级东财", code, err)
	}

	// P2: 东财 push2
	emSI, emErr := dc.eastMoney.GetRealtimeQuote(code)
	if emErr == nil && emSI != nil && emSI.Price > 0 {
		return emSI, nil
	}
	if emErr != nil {
		log.Printf("东方财富行情失败 (%s): %v", code, emErr)
	}

	// P3: Tushare（仅在非熔断期）
	if dc.tushare != nil && dc.tushare.token != "" && time.Now().After(dc.tushareDeadline) {
		today := time.Now().Format("20060102")
		tc := dc.tushare
		req := tushareReq{
			APIName: "daily",
			Token:   tc.token,
			Params: map[string]interface{}{
				"ts_code":    code,
				"start_date": today,
				"end_date":   today,
			},
			Fields: "trade_date,open,high,low,close,vol,amount",
		}
		resp, e := tc.call(req)
		if e == nil && resp != nil && resp.Data != nil && len(resp.Data.Items) > 0 {
			idx := make(map[string]int)
			for i, f := range resp.Data.Fields {
				idx[f] = i
			}
			var row []interface{}
			if err := json.Unmarshal(resp.Data.Items[0], &row); err != nil {
				return nil, fmt.Errorf("unmarshal tushare row: %v", err)
			}
			price := toFloatFromRow(row, idx, "close")
			if price > 0 {
				si := &StockInfo{
					Code:   code,
					Price:  price,
					Open:   toFloatFromRow(row, idx, "open"),
					High:   toFloatFromRow(row, idx, "high"),
					Low:    toFloatFromRow(row, idx, "low"),
					Close:  price,
					Volume: toFloatFromRow(row, idx, "vol"),
					Amount: toFloatFromRow(row, idx, "amount"),
				}
				log.Printf("Tushare 返回 %s 最新价 %.2f", code, price)
				return si, nil
			}
		} else if e != nil {
			dc.tushareDeadline = time.Now().Add(60 * time.Second)
			log.Printf("Tushare 失败 (%s): %v, 熔断60s", code, e)
		}
	}

	// P4: 同花顺（末位备用，有熔断）
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

	return si, err
}

// GetKLine 获取 K 线数据（多源降级）。
// 优先级：Tushare（非熔断期）→ 新浪日线（仅 period="101"）→ 东财。
func (dc *DataCoordinator) GetKLine(code, period string, count int) ([]KLine, error) {
	// P1: 新浪日线（主力）
	if period == "101" {
		if klines, err := dc.eastMoney.GetSinaKLine(code, count); err == nil && len(klines) > 0 {
			return klines, nil
		}
	}

	// P2: 东财日线备用
	if klines, err := dc.eastMoney.GetKLine(code, period, count); err == nil && len(klines) > 0 {
		return klines, nil
	}

	return nil, fmt.Errorf("all kline sources failed for %s", code)
}

// GetSectors 获取板块列表。
// 优先级：东财(实时) → Tushare ths_index → 聚宽 get_industries → 缓存
func (dc *DataCoordinator) GetSectors() ([]SectorInfo, error) {
	dc.mu.RLock()
	if len(dc.sectorCache) > 0 && time.Since(dc.sectorCacheAt) < 30*time.Second {
		c := make([]SectorInfo, len(dc.sectorCache))
		copy(c, dc.sectorCache)
		dc.mu.RUnlock()
		return c, nil
	}
	dc.mu.RUnlock()

	// P1: 同花顺（稳定，451+板块，无实时行情）
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

	// P2: 东财 push2（实时行情，可能EOF/限流）
	s, emErr := dc.eastMoney.GetSectorList()
	if emErr != nil {
		log.Printf("东财板块不可用: %v", emErr)
	}

	// 合并：同花顺为基础，东财附加实时行情
	// 注意：同花顺代码体系(881xxx)与东财(BK0xxx)不同，需按名称匹配
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
			// 东财独有的实时板块（未匹配到同花顺名称）附加到末尾
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

	// P3: 只有东财数据（同花顺失败时）
	if len(s) > 0 {
		dc.mu.Lock()
		dc.sectorCache = s
		dc.sectorCacheAt = time.Now()
		dc.mu.Unlock()
		log.Printf("GetSectors: 东财 (%d个板块)", len(s))
		return s, nil
	}

	// P4: Tushare ths_index（降级）
	if dc.tushare != nil && dc.tushare.token != "" {
		tsSectors, tsErr := dc.tushare.ThsIndex()
		if tsErr == nil && len(tsSectors) > 0 {
			result := make([]SectorInfo, len(tsSectors))
			for i, ts := range tsSectors {
				result[i] = SectorInfo{Code: ts.Code, Name: ts.Name}
			}
			dc.mu.Lock()
			dc.sectorCache = result
			dc.sectorCacheAt = time.Now()
			dc.thsIndexList = tsSectors
			dc.thsIndexAt = time.Now()
			dc.mu.Unlock()
			log.Printf("GetSectors: Tushare ths_index (%d个板块)", len(result))
			return result, nil
		}
		if tsErr != nil {
			log.Printf("GetSectors: Tushare ths_index 失败: %v", tsErr)
		}
	}

	// P5: 聚宽 get_industries（末位降级）
	if dc.jq != nil {
		jqSectors, jqErr := dc.jq.GetIndustries()
		if jqErr == nil && len(jqSectors) > 0 {
			result := make([]SectorInfo, len(jqSectors))
			for i, js := range jqSectors {
				result[i] = SectorInfo{Code: js.Code, Name: js.Name}
			}
			dc.mu.Lock()
			dc.sectorCache = result
			dc.sectorCacheAt = time.Now()
			dc.mu.Unlock()
			log.Printf("GetSectors: 聚宽 (%d个板块)", len(result))
			return result, nil
		}
		if jqErr != nil {
			log.Printf("GetSectors: 聚宽 失败: %v", jqErr)
		}
	}

	return nil, fmt.Errorf("all sector sources failed")
}

// GetSectorStocks 获取板块成分股。
// 优先级：东财 → Tushare ths_member → 聚宽 get_industry_stocks → 缓存
func (dc *DataCoordinator) GetSectorStocks(sectorCode string, topN int) ([]StockInfo, error) {
	dc.mu.RLock()
	if c, ok := dc.sectorStockCache[sectorCode]; ok && time.Since(c.at) < 60*time.Second {
		out := make([]StockInfo, len(c.stocks))
		copy(out, c.stocks)
		dc.mu.RUnlock()
		return out, nil
	}
	dc.mu.RUnlock()

	// P1: 东财
	s, err := dc.eastMoney.GetSectorStocks(sectorCode, topN)
	if err == nil && len(s) > 0 {
		dc.mu.Lock()
		dc.sectorStockCache[sectorCode] = cachedSectorStocks{stocks: s, at: time.Now()}
		dc.mu.Unlock()
		return s, nil
	}

	// P2: Tushare ths_member（按板块名称匹配）
	if dc.tushare != nil && dc.tushare.token != "" {
		dc.mu.RLock()
		// 通过 sectorCode 查找板块名称
		sectorName := ""
		for _, sec := range dc.sectorCache {
			if sec.Code == sectorCode {
				sectorName = sec.Name
				break
			}
		}
		if sectorName == "" {
			for _, ts := range dc.thsIndexList {
				if ts.Code == sectorCode {
					sectorName = ts.Name
					break
				}
			}
		}
		// 在 thsIndexList 中查找匹配的 THS 代码
		thsCode := ""
		for _, ts := range dc.thsIndexList {
			if sectorName != "" && (strings.Contains(ts.Name, sectorName) || strings.Contains(sectorName, ts.Name)) {
				thsCode = ts.Code
				break
			}
		}
		dc.mu.RUnlock()

		if thsCode != "" {
			members, memberErr := dc.tushare.ThsMember(thsCode)
			if memberErr == nil && len(members) > 0 {
				result := make([]StockInfo, 0, len(members))
				for _, m := range members {
					code := strings.TrimSuffix(strings.TrimSuffix(m.ConCode, ".SZ"), ".SH")
					result = append(result, StockInfo{Code: code, Name: m.Name})
				}
				log.Printf("GetSectorStocks: 东财失败，降级到 Tushare ths_member for %s (%d只)", sectorCode, len(result))
				return result, nil
			}
		}
	}

	// P3: 聚宽 get_industry_stocks（末位降级）
	if dc.jq != nil {
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
			// 聚宽使用申万行业分类，代码不同。尝试在缓存中查找匹配的申万行业代码
			jqCode := sectorCode
			stocks, jqErr := dc.jq.GetIndustryStocks(jqCode, "")
			if jqErr == nil && len(stocks) > 0 {
				result := make([]StockInfo, len(stocks))
				for i, st := range stocks {
					result[i] = StockInfo{Code: st.Code, Name: st.Name}
				}
				log.Printf("GetSectorStocks: 东财+Tushare均失败，降级到聚宽 for %s (%d只)", sectorCode, len(result))
				if len(result) > topN {
					result = result[:topN]
				}
				return result, nil
			}
		}
	}

	return s, err
}

// GetFinancial 获取个股基本面信息（仅 Tushare）。
func (dc *DataCoordinator) GetFinancial(code string) (*FinancialInfo, error) {
	if dc.tushare != nil && dc.tushare.token != "" {
		return dc.tushare.Financial(code)
	}
	return nil, fmt.Errorf("tushare not available")
}

// GetDailyTS 通过 Tushare 获取日 K 线。
func (dc *DataCoordinator) GetDailyTS(code string, start, end string) ([]KLine, error) {
	if dc.tushare != nil && dc.tushare.token != "" {
		return dc.tushare.Daily(code, start, end)
	}
	return nil, fmt.Errorf("tushare not set")
}

// GetHotNews 多源合并获取热门新闻，按 pageSize 截顶返回。
//
// 合并策略：Tushare(可选) → 同花顺快讯(主源) → 新浪财经(兜底)。
// 同花顺快讯推送及时、覆盖面广且无调用次数限制，取代 Tushare 成为主力新闻源；
// Tushare 仅在 token 有效时作为第一优先级补充（受限频且不稳定）；
// 新浪财经作为末位兜底，响应慢但数据稳定。
// 标题截取前 60 字作为 key 去重，避免三个来源推送同一条新闻。
func (dc *DataCoordinator) GetHotNews(pageSize int) []NewsItem {
	seen := make(map[string]bool)
	var all []NewsItem

	// 1. Tushare（不限权时可用）
	if dc.tushare != nil && dc.tushare.token != "" {
		if items, err := dc.tushare.GetNews("eastmoney", pageSize); err == nil && len(items) > 0 {
			for _, n := range items {
				key := truncateStr(n.Title, 60)
				if !seen[key] {
					seen[key] = true
					all = append(all, n)
				}
			}
		}
	}

	// 2. 同花顺快讯（主源）
	if items, err := dc.eastMoney.GetTonghuashunNews(pageSize); err == nil && len(items) > 0 {
		for _, n := range items {
			key := truncateStr(n.Title, 60)
			if !seen[key] {
				seen[key] = true
				all = append(all, n)
			}
		}
	}

	// 3. 新浪财经（兜底）
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
		log.Printf("GetHotNews: all sources empty")
	}
	return all
}

// truncateStr 截断字符串到 maxLen 个字符，用于标题去重 key。
func truncateStr(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) > maxLen {
		return string(runes[:maxLen])
	}
	return s
}

// GetAuctionData 获取集合竞价数据。
func (dc *DataCoordinator) GetAuctionData(code string) (*StockInfo, error) {
	return dc.eastMoney.GetAuctionData(code)
}

// GetStockMoneyFlow 获取资金流向。优先 Tushare，失败降级东方财富。
func (dc *DataCoordinator) GetStockMoneyFlow(code string) (*CapitalFlow, error) {
	if dc.tushare != nil && dc.tushare.token != "" {
		today := time.Now().Format("20060102")
		if cf, err := dc.tushare.Moneyflow(code, today, today); err == nil && cf != nil {
			return cf, nil
		}
	}
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
	_ = dc.eastMoney
	return "Sina"
}

// HasTushare 判断是否配置了 Tushare 客户端。
func (dc *DataCoordinator) HasTushare() bool {
	return dc.tushare != nil && dc.tushare.token != ""
}

// RefreshIPOCalendar 刷新新股日历缓存。
// 优先 Tushare new_share API，失败降级东方财富数据中心。
func (dc *DataCoordinator) RefreshIPOCalendar() {
	dc.mu.Lock()
	defer dc.mu.Unlock()

	// 5min TTL（东财实时更新）
	if !dc.ipoCacheAt.IsZero() && time.Since(dc.ipoCacheAt) < 5*time.Minute {
		return
	}

	// P1: Tushare
	if dc.tushare != nil && dc.tushare.token != "" {
		list, err := dc.tushare.GetNewShareList()
		if err == nil && len(list) > 0 {
			dc.enrichIPOSector(list)
			dc.ipoCache = list
			dc.ipoCacheAt = time.Now()
			log.Printf("IPO日历: Tushare 加载 %d 条", len(list))
			return
		}
		log.Printf("IPO日历 Tushare 失败: %v, 降级东财", err)
	}

	// P2: 东财
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

// enrichIPOSector 查询并填充每只IPO股票的所属板块。
// 调用方必须持有 dc.mu 写锁。
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
// 优先用东财实时查询，失败返回空串。
func (dc *DataCoordinator) GetStockSector(code string) string {
	if dc.eastMoney != nil {
		return dc.eastMoney.GetStockIndustry(code)
	}
	return ""
}

// tushareReq Tushare API 请求体。
// 与 Tushare Pro JSON-RPC 协议对齐。
type tushareReq struct {
	APIName string                 `json:"api_name"`
	Token   string                 `json:"token"`
	Params  map[string]interface{} `json:"params,omitempty"`
	Fields  string                 `json:"fields,omitempty"`
}

// tushareResp Tushare API 响应体。
type tushareResp struct {
	Code int          `json:"code"`
	Msg  string       `json:"msg"`
	Data *tushareData `json:"data,omitempty"`
}

// tushareData Tushare 数据部分——字段定义 + 行数据。
type tushareData struct {
	Fields []string          `json:"fields"`
	Items  []json.RawMessage `json:"items"`
}

// call 执行 Tushare API 调用。序列化请求 → POST → 反序列化响应。
func (tc *TushareClient) call(req tushareReq) (*tushareResp, error) {
	body, _ := json.Marshal(req)
	resp, err := tc.client.Post(tc.apiURL, "application/json", strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("tushare http: %v", err)
	}
	defer resp.Body.Close()
	rb, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("tushare read: %v", err)
	}
	var result tushareResp
	if err := json.Unmarshal(rb, &result); err != nil {
		return nil, fmt.Errorf("tushare json: %v", err)
	}
	if result.Code != 0 {
		return nil, fmt.Errorf("tushare err %d: %s", result.Code, result.Msg)
	}
	return &result, nil
}

// TushareClient Tushare Pro HTTP 客户端。
// token 为空时所有方法返回错误，不做 API 调用。
type TushareClient struct {
	client *http.Client
	token  string
	apiURL string
}

// NewTushareClient 创建 Tushare 客户端。
// token 从环境变量 TUSHARE_TOKEN 读取，可留空（此时所有方法不可用）。
func NewTushareClient(token string) *TushareClient {
	return &TushareClient{
		client: &http.Client{Timeout: 10 * time.Second},
		token:  token,
		apiURL: "https://api.tushare.pro",
	}
}

// Daily 获取日 K 线数据。
// 返回按日期升序排列的 K 线切片。
func (tc *TushareClient) Daily(code string, start, end string) ([]KLine, error) {
	if tc.token == "" {
		return nil, fmt.Errorf("tushare token not set")
	}
	req := tushareReq{
		APIName: "daily",
		Token:   tc.token,
		Params: map[string]interface{}{
			"ts_code":    code,
			"start_date": start,
			"end_date":   end,
		},
		Fields: "trade_date,open,high,low,close,vol,amount",
	}
	resp, err := tc.call(req)
	if err != nil {
		return nil, err
	}
	if resp.Data == nil || len(resp.Data.Items) == 0 {
		return nil, nil
	}
	idx := make(map[string]int)
	for i, f := range resp.Data.Fields {
		idx[f] = i
	}
	var items []json.RawMessage
	items = resp.Data.Items
	klines := make([]KLine, 0, len(items))
	for i := len(items) - 1; i >= 0; i-- {
		var row []interface{}
		if err := json.Unmarshal(items[i], &row); err != nil {
			continue
		}
		ts, _ := row[idx["trade_date"]].(string)
		t, err := time.Parse("20060102", ts)
		if err != nil {
			continue
		}
		klines = append(klines, KLine{
			Date:   t,
			Open:   toFloatFromRow(row, idx, "open"),
			High:   toFloatFromRow(row, idx, "high"),
			Low:    toFloatFromRow(row, idx, "low"),
			Close:  toFloatFromRow(row, idx, "close"),
			Volume: toFloatFromRow(row, idx, "vol"),
			Amount: toFloatFromRow(row, idx, "amount"),
		})
	}
	return klines, nil
}

// Moneyflow 获取资金流向（Tushare moneyflow API）。
// 返回当日资金流向（含超大单/大单/中单/小单的买卖金额）。
// 注意：Tushare 返回金额单位是万元，转换为元（×10000）。
func (tc *TushareClient) Moneyflow(code string, start, end string) (*CapitalFlow, error) {
	if tc.token == "" {
		return nil, fmt.Errorf("tushare token not set")
	}
	req := tushareReq{
		APIName: "moneyflow",
		Token:   tc.token,
		Params: map[string]interface{}{
			"ts_code":    code,
			"start_date": start,
			"end_date":   end,
		},
		Fields: "ts_code,trade_date,buy_sm_amount,sell_sm_amount,buy_md_amount,sell_md_amount,buy_lg_amount,sell_lg_amount,buy_elg_amount,sell_elg_amount,net_mf_amount",
	}
	resp, err := tc.call(req)
	if err != nil {
		return nil, err
	}
	if resp.Data == nil || len(resp.Data.Items) == 0 {
		return nil, nil
	}
	idx := make(map[string]int)
	for i, f := range resp.Data.Fields {
		idx[f] = i
	}
	var row []interface{}
	if err := json.Unmarshal(resp.Data.Items[0], &row); err != nil {
		return nil, err
	}
	return &CapitalFlow{
		Code:          code,
		NetInflow:     toFloatFromRow(row, idx, "net_mf_amount") * 10000,
		SuperLargeIn:  toFloatFromRow(row, idx, "buy_elg_amount") * 10000,
		SuperLargeOut: toFloatFromRow(row, idx, "sell_elg_amount") * 10000,
		LargeIn:       toFloatFromRow(row, idx, "buy_lg_amount") * 10000,
		LargeOut:      toFloatFromRow(row, idx, "sell_lg_amount") * 10000,
		MediumIn:      toFloatFromRow(row, idx, "buy_md_amount") * 10000,
		MediumOut:     toFloatFromRow(row, idx, "sell_md_amount") * 10000,
		SmallIn:       toFloatFromRow(row, idx, "buy_sm_amount") * 10000,
		SmallOut:      toFloatFromRow(row, idx, "sell_sm_amount") * 10000,
		Time:          time.Now(),
	}, nil
}

// Financial 获取个股基本面指标（PE/PB/ROE/总市值）。
func (tc *TushareClient) Financial(code string) (*FinancialInfo, error) {
	if tc.token == "" {
		return nil, fmt.Errorf("tushare token not set")
	}
	req := tushareReq{
		APIName: "fin_indicator",
		Token:   tc.token,
		Params: map[string]interface{}{
			"ts_code": code,
		},
		Fields: "ts_code,pe,pb,roe,total_mv",
	}
	resp, err := tc.call(req)
	if err != nil {
		return nil, err
	}
	if resp.Data == nil || len(resp.Data.Items) == 0 {
		return nil, nil
	}
	idx := make(map[string]int)
	for i, f := range resp.Data.Fields {
		idx[f] = i
	}
	var row []interface{}
	if err := json.Unmarshal(resp.Data.Items[0], &row); err != nil {
		return nil, err
	}
	fi := &FinancialInfo{Code: code}
	fi.PE = toFloatFromRow(row, idx, "pe")
	fi.PB = toFloatFromRow(row, idx, "pb")
	fi.ROE = toFloatFromRow(row, idx, "roe")
	fi.MarketCap = toFloatFromRow(row, idx, "total_mv")
	return fi, nil
}

// ThsIndex 获取同花顺板块指数列表（Tushare ths_index）。
// 返回所有板块代码、名称、成分股数量。无实时行情数据。
func (tc *TushareClient) ThsIndex() ([]ThsSectorInfo, error) {
	if tc.token == "" {
		return nil, fmt.Errorf("tushare token not set")
	}
	req := tushareReq{
		APIName: "ths_index",
		Token:   tc.token,
		Fields:  "ts_code,name,count",
	}
	resp, err := tc.call(req)
	if err != nil {
		return nil, err
	}
	if resp.Data == nil || len(resp.Data.Items) == 0 {
		return nil, nil
	}
	idx := make(map[string]int)
	for i, f := range resp.Data.Fields {
		idx[f] = i
	}
	var result []ThsSectorInfo
	for _, rawItem := range resp.Data.Items {
		var row []interface{}
		if err := json.Unmarshal(rawItem, &row); err != nil {
			continue
		}
		var info ThsSectorInfo
		if i, ok := idx["ts_code"]; ok && i < len(row) {
			info.Code, _ = row[i].(string)
		}
		if i, ok := idx["name"]; ok && i < len(row) {
			info.Name, _ = row[i].(string)
		}
		if i, ok := idx["count"]; ok && i < len(row) {
			if v, ok := row[i].(float64); ok {
				info.Count = int(v)
			}
		}
		if info.Code != "" {
			result = append(result, info)
		}
	}
	return result, nil
}

// ThsMember 获取同花顺板块成分股（Tushare ths_member）。
// tsCode 为 THS 板块代码（如 "TS886001"），返回成分股代码和名称。
func (tc *TushareClient) ThsMember(tsCode string) ([]ThsMemberInfo, error) {
	if tc.token == "" {
		return nil, fmt.Errorf("tushare token not set")
	}
	req := tushareReq{
		APIName: "ths_member",
		Token:   tc.token,
		Params:  map[string]interface{}{"ts_code": tsCode},
		Fields:  "ts_code,con_code,name",
	}
	resp, err := tc.call(req)
	if err != nil {
		return nil, err
	}
	if resp.Data == nil || len(resp.Data.Items) == 0 {
		return nil, nil
	}
	idx := make(map[string]int)
	for i, f := range resp.Data.Fields {
		idx[f] = i
	}
	var result []ThsMemberInfo
	for _, rawItem := range resp.Data.Items {
		var row []interface{}
		if err := json.Unmarshal(rawItem, &row); err != nil {
			continue
		}
		var info ThsMemberInfo
		if i, ok := idx["con_code"]; ok && i < len(row) {
			info.ConCode, _ = row[i].(string)
		}
		if i, ok := idx["name"]; ok && i < len(row) {
			info.Name, _ = row[i].(string)
		}
		if info.ConCode != "" {
			result = append(result, info)
		}
	}
	return result, nil
}

// GetNews 获取 Tushare 新闻数据。
func (tc *TushareClient) GetNews(src string, pageSize int) ([]NewsItem, error) {
	if tc.token == "" {
		return nil, fmt.Errorf("tushare token not set")
	}
	today := time.Now().Format("20060102")
	req := tushareReq{
		APIName: "news",
		Token:   tc.token,
		Params: map[string]interface{}{
			"start_date": today,
			"end_date":   today,
			"src":        src,
		},
		Fields: "title,content,datetime",
	}
	resp, err := tc.call(req)
	if err != nil {
		return nil, err
	}
	if resp.Data == nil || len(resp.Data.Items) == 0 {
		return nil, nil
	}
	idx := make(map[string]int)
	for i, f := range resp.Data.Fields {
		idx[f] = i
	}
	var items []NewsItem
	for _, rawItem := range resp.Data.Items {
		var row []interface{}
		if err := json.Unmarshal(rawItem, &row); err != nil {
			continue
		}
		var ni NewsItem
		if i, ok := idx["title"]; ok && i < len(row) {
			ni.Title, _ = row[i].(string)
		}
		if i, ok := idx["content"]; ok && i < len(row) {
			ni.Content, _ = row[i].(string)
		}
		if i, ok := idx["datetime"]; ok && i < len(row) {
			ni.Datetime, _ = row[i].(string)
		}
		ni.Source = "tushare"
		items = append(items, ni)
	}
	if len(items) > pageSize {
		items = items[:pageSize]
	}
	return items, nil
}

// GetNewShareList 获取新股日历。
// 调用 Tushare new_share API，返回近期（近3月）新股申购/上市数据。
// 返回按上市日期降序排列。
func (tc *TushareClient) GetNewShareList() ([]IPOEvent, error) {
	if tc.token == "" {
		return nil, fmt.Errorf("tushare token not set")
	}
	start := time.Now().AddDate(0, -3, 0).Format("20060102")
	end := time.Now().AddDate(0, 0, 30).Format("20060102")
	req := tushareReq{
		APIName: "new_share",
		Token:   tc.token,
		Params: map[string]interface{}{
			"start_date": start,
			"end_date":   end,
		},
		Fields: "ts_code,symbol,name,ipo_date,issue_price,curr_iss_amount,listing_date,list_status",
	}
	resp, err := tc.call(req)
	if err != nil {
		return nil, err
	}
	if resp.Data == nil || len(resp.Data.Items) == 0 {
		return nil, nil
	}
	idx := make(map[string]int)
	for i, f := range resp.Data.Fields {
		idx[f] = i
	}
	var items []json.RawMessage
	items = resp.Data.Items
	list := make([]IPOEvent, 0, len(items))
	for _, rawItem := range items {
		var row []interface{}
		if err := json.Unmarshal(rawItem, &row); err != nil {
			continue
		}
		ev := IPOEvent{
			Code:        toStringFromRow(row, idx, "symbol"),
			Name:        toStringFromRow(row, idx, "name"),
			IPODate:     toStringFromRow(row, idx, "ipo_date"),
			ListingDate: toStringFromRow(row, idx, "listing_date"),
			IssuePrice:  toFloatFromRow(row, idx, "issue_price"),
			ListStatus:  toStringFromRow(row, idx, "list_status"),
		}
		if ev.Code == "" {
			ev.Code = toStringFromRow(row, idx, "ts_code")
			if idx2 := strings.Index(ev.Code, "."); idx2 > 0 {
				ev.Code = ev.Code[:idx2]
			}
		}
		list = append(list, ev)
	}
	// 按上市日期降序（有值的优先，无值按申购日期）
	sort.Slice(list, func(i, j int) bool {
		return list[i].ListingDate > list[j].ListingDate
	})
	return list, nil
}

// toStringFromRow 从 Tushare 行数据中按字段名取 string。
func toStringFromRow(row []interface{}, idx map[string]int, key string) string {
	i, ok := idx[key]
	if !ok || i >= len(row) {
		return ""
	}
	s, _ := row[i].(string)
	return s
}

// toFloat 从 []interface{} 中按索引取 float64 值。
// 支持 float64、int、string 三种类型。
func toFloat(item []interface{}, idx map[string]int, key string) float64 {
	i, ok := idx[key]
	if !ok || i >= len(item) {
		return 0
	}
	switch v := item[i].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case string:
		var f float64
		fmt.Sscanf(v, "%f", &f)
		return f
	}
	return 0
}

// toFloatFromRow 从 Tushare 行数据中按字段名取 float64。
// 与 toFloat 逻辑相同，为 Tushare 行解析的专用封装。
func toFloatFromRow(row []interface{}, idx map[string]int, key string) float64 {
	i, ok := idx[key]
	if !ok || i >= len(row) {
		return 0
	}
	switch v := row[i].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case string:
		var f float64
		fmt.Sscanf(v, "%f", &f)
		return f
	}
	return 0
}

// TushareToken 从环境变量读取 Tushare token。
func TushareToken() string {
	return os.Getenv("TUSHARE_TOKEN")
}
