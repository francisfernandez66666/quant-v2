// Package data — 东方财富 push2 + 新浪财经行情 API 客户端（主力数据源）。
// 提供实时行情、K线、板块、资金流向、新闻等全量接口，
// 所有请求通过 net/http 直连，不引入第三方库。
// 限流通过 rate.go 中的 SinaLimiter / EastMoneyLimiter 控制。
// Package data is the primary market-data client (EastMoney push2 + Sina Finance).
// It exposes realtime quotes, K-lines, sectors, money flow and news over direct
// net/http calls without third-party libraries, rate-limited by rate.go.
package data

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	urlpkg "net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
	"time"
)

// htmlTagRe 匹配 HTML 标签（含属性），用于正文清洗。
// htmlTagRe matches HTML tags (with attributes) for article body cleanup.
var htmlTagRe = regexp.MustCompile(`(?s)<[^>]+>`)

// roundTripperFunc 将普通函数适配为 http.RoundTripper。
// 用于在 HTTP 请求中注入自定义逻辑（日志、限流等）。
// roundTripperFunc adapts a plain function into an http.RoundTripper,
// used to inject custom logic (logging, rate limiting) into requests.
type roundTripperFunc func(*http.Request) (*http.Response, error)

// RoundTrip 执行 RoundTripper 接口调用。
// RoundTrip executes the RoundTripper interface call.
func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

// sectorRawItem 东方财富板块行情列表的原始 JSON 行。
// 对应 push2.eastmoney.com/api/qt/clist/get 返回的 diff 条目。
// sectorRawItem is a raw JSON row of the EastMoney sector quote list,
// corresponding to a diff entry of clist/get.
type sectorRawItem struct {
	Code       string  `json:"f12"`  // 板块代码（BKXXXX）
	Name       string  `json:"f14"`  // 板块名称
	ChangePct  float64 `json:"f3"`   // 涨跌幅（千分位，需归一化）
	Amount     float64 `json:"f20"`  // 总市值（元）
	NetInflow  float64 `json:"f62"`  // 主力净流入（元）
	VolumeRank int     `json:"f104"` // 成交量排名
	LimitupCnt int     `json:"f105"` // 涨停家数
}

// MarketAPI 东方财富 + 新浪财经行情 API 客户端。
// 同时封装东方财富 push2 和新浪财经两个数据源的直连调用，
// 包含实时行情、K线、板块、资金流向、新闻等所有接口。
// 内部使用 http.Client 直连 API，不依赖任何第三方库。
// MarketAPI is a client wrapping both EastMoney push2 and Sina Finance.
// It covers realtime quotes, K-lines, sectors, money flow and news, with an
// internal quote cache and PE cache used to reduce request throttling.
type MarketAPI struct {
	client *http.Client

	quoteMu    sync.Mutex
	quoteCache map[string]cachedQuote // 实时行情 TTL 缓存

	peMu  sync.Mutex              // 保护 PE 缓存的读写
	peTTL time.Duration           // PE 缓存有效期（PE 变动低频）
	peMap map[string]peCacheEntry // code → PE 缓存条目
}

// peCacheEntry PE 缓存条目。
// peCacheEntry is a PE (price-to-earnings) cache entry.
type peCacheEntry struct {
	pe float64   // 动态市盈率
	at time.Time // 缓存写入时间
}

// getPECache 读取 PE 缓存，命中且在有效期内返回 true。
// getPECache reads the PE cache; returns the value if it is fresh within the TTL.
func (m *MarketAPI) getPECache(code string) (float64, bool) {
	m.peMu.Lock()
	defer m.peMu.Unlock()
	e, ok := m.peMap[code]
	if !ok || time.Since(e.at) > m.peTTL {
		return 0, false
	}
	return e.pe, true
}

// setPECache 写入 PE 缓存。
// setPECache writes a PE value into the cache with the current timestamp.
func (m *MarketAPI) setPECache(code string, pe float64) {
	m.peMu.Lock()
	defer m.peMu.Unlock()
	if m.peMap == nil {
		m.peMap = make(map[string]peCacheEntry)
	}
	m.peMap[code] = peCacheEntry{pe: pe, at: time.Now()}
}

// quoteTTL 实时行情缓存有效期：同一股票在窗口内只打一次网络，
// 显著降低前端轮询/多接口下的限流压力（东财 3 req/s）。
// quoteTTL is the cache TTL for realtime quotes: each stock is fetched
// at most once per window to ease rate limits under polling.
const quoteTTL = 5 * time.Second

// cachedQuote 实时行情缓存条目。
// si 为缓存的行情快照，at 为缓存写入时间（用于判断是否超过 quoteTTL）。
// cachedQuote holds a realtime quote cache entry with the stored snapshot
// and the write time used to detect expiry.
type cachedQuote struct {
	si *StockInfo
	at time.Time
}

// emUserAgent 东财请求使用的浏览器 User-Agent。
// emUserAgent is the browser User-Agent used for EastMoney requests.
const emUserAgent = "Mozilla/5.0 (Linux; Android 14) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.6099.144 Mobile Safari/537.36"

// emReferer 东财请求使用的 Referer。
// emReferer is the Referer header used for EastMoney quote requests.
const emReferer = "https://quote.eastmoney.com/"

// emDataReferer 东财数据中心请求使用的 Referer。
// emDataReferer is the Referer for EastMoney data-center requests.
const emDataReferer = "https://data.eastmoney.com/"

// emNewsReferer 东财快讯请求使用的 Referer。
// emNewsReferer is the Referer for EastMoney flash-news requests.
const emNewsReferer = "https://np-anotice-stock.eastmoney.com/"

// NewMarketAPI 创建行情 API 客户端。
// 使用带限流功能的 HTTP 客户端，默认超时 10 秒。
// NewMarketAPI creates a market API client with 10s HTTP timeout
// and pre-initialized quote/PE caches.
func NewMarketAPI() *MarketAPI {
	return &MarketAPI{
		client:     &http.Client{Timeout: 10 * time.Second},
		quoteCache: make(map[string]cachedQuote),
		peMap:      make(map[string]peCacheEntry),
		peTTL:      10 * time.Minute,
	}
}

// SetTransport 替换底层 HTTP Transport（测试注入 mock 网络，不影响限流与缓存逻辑）。
// SetTransport swaps the underlying HTTP Transport, mainly for injecting a
// mock network in tests without affecting rate-limit and cache logic.
func (m *MarketAPI) SetTransport(rt http.RoundTripper) {
	m.client.Transport = rt
}

// getWithHeaders 发起带浏览器头部模拟的 GET 请求。
// 解决东财 CDN 对无头请求的 geo-block / anti-crawler 封锁。
// getWithHeaders issues a GET with simulated browser headers to bypass
// EastMoney CDN geo-blocking / anti-crawler filters.
func (m *MarketAPI) getWithHeaders(url, referer string) (*http.Response, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", emUserAgent)
	req.Header.Set("Referer", referer)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	return m.client.Do(req)
}

// stripSuffix 剥离股票代码的交易所后缀（.SH / .SZ / .BJ）。
// 如 "600519.SH" → "600519"，用于统一内部代码格式。
// stripSuffix removes the exchange suffix (.SH / .SZ / .BJ) from a code,
// e.g. "600519.SH" → "600519", to normalize internal code format.
func stripSuffix(code string) string {
	if len(code) > 3 && code[len(code)-3:] == ".SH" || len(code) > 3 && code[len(code)-3:] == ".SZ" || len(code) > 3 && code[len(code)-3:] == ".BJ" {
		return code[:len(code)-3]
	}
	return code
}

// secID 将股票代码转换为东方财富 push2 证券ID格式。
// 沪市（6/5 开头）加 "1." 前缀，深市加 "0." 前缀。
// 东财 secid 形如 "1.600519"（沪）/ "0.000001"（深），是 push2 各接口的通用标识。
// secID converts a stock code into the EastMoney push2 secid format:
// Shanghai (6/5 prefix) gets "1.", Shenzhen gets "0."; the universal id used by push2 APIs.
func secID(code string) string {
	code = stripSuffix(code)
	if strings.HasPrefix(code, "6") || strings.HasPrefix(code, "5") {
		return "1." + code
	}
	return "0." + code
}

// ── Sina 实时行情（CSV 格式） ──

// sinaQuoteURL 返回新浪财经实时行情 URL。
// 支持批量查询，多个代码用逗号分隔。
// sinaQuoteURL builds the Sina realtime quote URL; supports batch codes joined by commas.
func sinaQuoteURL(codes ...string) string {
	prefix := ""
	for _, c := range codes {
		c = stripSuffix(c)
		if strings.HasPrefix(c, "6") || strings.HasPrefix(c, "5") {
			prefix += "sh" + c + ","
		} else {
			prefix += "sz" + c + ","
		}
	}
	prefix = strings.TrimSuffix(prefix, ",")
	return "https://hq.sinajs.cn/list=" + prefix
}

// GetSinaQuote 获取新浪财经实时行情。
// 返回 StockInfo，包含名称、价格、涨跌幅、成交量等。
// 当 Price <= 0 时表示数据无效。
// GetSinaQuote fetches a Sina realtime quote for one symbol; Price<=0 means invalid data.
func (m *MarketAPI) GetSinaQuote(code string) (*StockInfo, error) {
	quotes := m.getSinaQuotes([]string{code})
	if si, ok := quotes[code]; ok {
		return si, nil
	}
	return nil, fmt.Errorf("sina: no quote data for %s", code)
}

// GetSinaQuotes 批量获取新浪财经实时行情（单次请求批量解析）。
// 返回 code → StockInfo 映射，仅含解析成功的股票。
// 内部按 80 只一批分段请求，规避新浪 URL 长度限制。
// GetSinaQuotes fetches Sina quotes for many codes, chunked by batches of 80
// to stay under Sina's URL length limit; returns only successfully parsed stocks.
func (m *MarketAPI) GetSinaQuotes(codes []string) map[string]*StockInfo {
	out := make(map[string]*StockInfo, len(codes))
	if len(codes) == 0 {
		return out
	}
	const batch = 80
	for i := 0; i < len(codes); i += batch {
		end := i + batch
		if end > len(codes) {
			end = len(codes)
		}
		for code, si := range m.getSinaQuotes(codes[i:end]) {
			out[code] = si
		}
	}
	return out
}

// sinaQuoteRe 批量解析新浪行情行：var hq_str_sh600519="字段,...";
// sinaQuoteRe matches each Sina quote line like var hq_str_sh600519="fields,...".
var sinaQuoteRe = regexp.MustCompile(`var\s+hq_str_(?:sh|sz|bj)(\d+)\s*=\s*"([^"]*)"`)

// getSinaQuotes 发起单次新浪批量请求并解析全部行。
// getSinaQuotes issues one Sina batch request and parses every quote line.
func (m *MarketAPI) getSinaQuotes(codes []string) map[string]*StockInfo {
	out := make(map[string]*StockInfo, len(codes))
	if len(codes) == 0 {
		return out
	}
	url := sinaQuoteURL(codes...)
	SinaLimiter.Wait()
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return out
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Linux; Android 14) AppleWebKit/537.36")
	req.Header.Set("Referer", "https://finance.sina.com.cn")
	resp, err := m.client.Do(req)
	if err != nil {
		return out
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return out
	}
	utfBody, _, _ := transform.String(simplifiedchinese.GBK.NewDecoder(), string(body))
	for _, mch := range sinaQuoteRe.FindAllStringSubmatch(utfBody, -1) {
		if len(mch) < 3 {
			continue
		}
		code := mch[1]
		fields := strings.Split(mch[2], ",")
		// 新浪行情 CSV 字段索引：0名称 1今开 2昨收 3现价 4最高 5最低 6买价 7卖价 8成交量 9成交额
		if len(fields) < 10 {
			continue
		}
		si := &StockInfo{Code: code}
		si.Name = fields[0]
		si.Open, _ = strconv.ParseFloat(fields[1], 64)
		prevClose, _ := strconv.ParseFloat(fields[2], 64)
		si.Price, _ = strconv.ParseFloat(fields[3], 64)
		si.High, _ = strconv.ParseFloat(fields[4], 64)
		si.Low, _ = strconv.ParseFloat(fields[5], 64)
		si.Volume, _ = strconv.ParseFloat(fields[8], 64)
		si.Amount, _ = strconv.ParseFloat(fields[9], 64)
		si.Close = prevClose
		if prevClose > 0 && si.Price > 0 {
			si.ChangePct = (si.Price - prevClose) / prevClose * 100
		}
		// 现价为 0（停牌/未成交）视为无效行情，不入缓存，避免下游按 0 价误算盈亏。
		if si.Price <= 0 {
			continue
		}
		out[code] = si
	}
	return out
}

// ── 东方财富 push2 实时行情 ──

// stockQuoteFields 东方财富 push2 个股行情字段列表。
// 注意：主力净流入是 f62，不是 f162（f162 在 stock/get 单股接口中为动态市盈率）。
// stockQuoteFields lists the EastMoney push2 per-stock fields. Note main-capital
// inflow is f62 (f162 is the dynamic PE on the stock/get endpoint).
const stockQuoteFields = "f43,f44,f45,f46,f47,f48,f49,f50,f51,f52,f55,f57,f58,f60,f62,f116,f117,f167,f168,f169,f170,f171,f292"

// GetRealtimeQuote 获取实时行情。先尝试东方财富 push2，失败时回退到新浪。
// 结果按 quoteTTL 短期缓存，同一股票在窗口内的重复请求直接命中缓存。
// 东财 push2 接口返回的价格字段（F43/F44/F45/F46/F60）单位为分，需 ÷100 转换为元。
// 返回 StockInfo，包含名称、价格、涨跌幅、成交量、换手率、主力净流入等。
// GetRealtimeQuote returns a realtime quote: EastMoney push2 first, falling back to
// Sina on failure. Results are cached for quoteTTL; prices come back in cents (/100).
func (m *MarketAPI) GetRealtimeQuote(code string) (*StockInfo, error) {
	code = stripSuffix(code)
	if c, ok := m.quoteHit(code); ok {
		return c, nil
	}
	info, emErr := m.getEastMoneyQuote(code)
	if emErr == nil {
		m.quoteStore(code, info)
		return info, nil
	}
	// 东财失败 → 回落到新浪。注意把两个来源的错误都透传，避免上层把新浪错误误标成东财失败。
	sina, serr := m.GetSinaQuote(code)
	if serr != nil {
		return nil, fmt.Errorf("eastmoney: %v; sina: %v", emErr, serr)
	}
	m.quoteStore(code, sina)
	return sina, nil
}

// quoteHit 命中返回缓存中的实时行情副本；缓存不存在或已过期（超过 quoteTTL）时返回 nil,false。
// 返回副本而非原指针，避免调用方修改污染缓存。
// quoteHit returns a copy of a cached quote (nil,false if missing/expired);
// a copy is returned to avoid callers mutating the shared cache.
func (m *MarketAPI) quoteHit(code string) (*StockInfo, bool) {
	m.quoteMu.Lock()
	defer m.quoteMu.Unlock()
	c, ok := m.quoteCache[code]
	if !ok || time.Since(c.at) > quoteTTL {
		return nil, false
	}
	cp := *c.si
	return &cp, true
}

// quoteStore 将行情快照写入缓存，并记录当前时间。
// quoteStore caches a quote snapshot with the current write time.
func (m *MarketAPI) quoteStore(code string, si *StockInfo) {
	m.quoteMu.Lock()
	m.quoteCache[code] = cachedQuote{si: si, at: time.Now()}
	m.quoteMu.Unlock()
}

// getEastMoneyQuote 通过东方财富 push2 stock/get 接口拉取单只股票实时行情。
// 返回的 F43/F44/F45/F46/F60 价格字段单位为分，已 ÷100 转换为元。
// F170 涨跌幅 / F168 换手率 / F169 涨跌额 均为基准值 ×100，已 ÷100 转换。
// F50 为量比（非涨跌幅），注意勿混淆。
// getEastMoneyQuote pulls one stock's realtime quote via push2 stock/get.
// Price fields (F43/F44/F45/F46/F60) are in cent units and divided by 100 to Yuan;
// F170/F168/F169 are scaled by 100; F50 is the volume ratio, not change pct.
func (m *MarketAPI) getEastMoneyQuote(code string) (*StockInfo, error) {
	sid := secID(code)
	url := fmt.Sprintf("https://push2.eastmoney.com/api/qt/stock/get?secid=%s&fields=%s", sid, stockQuoteFields)
	EastMoneyLimiter.Wait()
	resp, err := m.getWithHeaders(url, emReferer)
	if err != nil {
		return nil, fmt.Errorf("eastmoney http: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("eastmoney read: %v", err)
	}
	var raw struct {
		Data struct {
			F43  float64 `json:"f43"`  // 当前价（分）
			F44  float64 `json:"f44"`  // 最高（分）
			F45  float64 `json:"f45"`  // 最低（分）
			F46  float64 `json:"f46"`  // 开盘（分）
			F60  float64 `json:"f60"`  // 昨收（分）
			F47  float64 `json:"f47"`  // 成交量（手，1手=100股）
			F48  float64 `json:"f48"`  // 成交额（元）
			F49  float64 `json:"f49"`  // 成交额辅助字段（与 F48 同口径，冗余）
			F50  float64 `json:"f50"`  // 量比（非涨跌幅）
			F57  string  `json:"f57"`  // 代码
			F58  string  `json:"f58"`  // 名称
			F168 float64 `json:"f168"` // 换手率 ×100
			F169 float64 `json:"f169"` // 涨跌额 ×100
			F170 float64 `json:"f170"` // 涨跌幅 ×100
			F62  float64 `json:"f62"`  // 主力净流入（元）
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("eastmoney json: %v", err)
	}
	if raw.Data.F43 == 0 {
		return nil, fmt.Errorf("eastmoney: no data for %s", code)
	}
	volHands := raw.Data.F47
	amount := raw.Data.F48
	if amount == 0 {
		amount = raw.Data.F49
	}
	return &StockInfo{
		Code:      code,
		Name:      raw.Data.F58,
		Price:     raw.Data.F43 / 100,
		Open:      raw.Data.F46 / 100,
		High:      raw.Data.F44 / 100,
		Low:       raw.Data.F45 / 100,
		Close:     raw.Data.F60 / 100,
		Volume:    volHands * 100, // 手→股，与新浪日K/余量表单位对齐
		Amount:    amount,
		ChangePct: raw.Data.F170 / 100,
		Turnover:  raw.Data.F168 / 100,
		NetInflow: raw.Data.F62,
	}, nil
}

// GetAuctionData 获取指定股票的集合竞价数据（9:15-9:25 时段）。
// 通过东方财富 push2 接口获取盘前数据，含竞价价格和成交量。
// GetAuctionData returns pre-open auction data (9:15-9:25) via EastMoney push2,
// currently delegating to the realtime quote endpoint.
func (m *MarketAPI) GetAuctionData(code string) (*StockInfo, error) {
	return m.GetRealtimeQuote(code)
}

// ── 东方财富板块列表 ──

// sectorListFields 东方财富板块列表字段（精简版，兼容性好）。
// sectorListFields is the compact EastMoney sector list field set.
const sectorListFields = "f12,f14,f3,f20,f62,f104,f105,f184"

// GetSectorList 获取东方财富行业板块行情列表。
// 返回全量板块（约 300+），包含涨跌幅、成交额、涨停家数、主力净流入等。
// GetSectorList fetches the EastMoney industry sector quote list (300+ sectors)
// including change pct, turnover, limit-up count and main-capital inflow.
func (m *MarketAPI) GetSectorList() ([]SectorInfo, error) {
	url := fmt.Sprintf("https://push2.eastmoney.com/api/qt/clist/get?pn=1&pz=50&fs=m:90+t:2&fields=%s", sectorListFields)
	EastMoneyLimiter.Wait()
	resp, err := m.getWithHeaders(url, emReferer)
	if err != nil {
		return nil, fmt.Errorf("eastmoney sector list http: %v", err)
	}
	log.Printf("eastmoney sector list status: %d", resp.StatusCode)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("eastmoney sector list read: %v", err)
	}
	return parseSectorList(body)
}

// parseSectorList 解析东方财富板块列表 JSON。
// 东财返回 diff 为 map[string]sectorRawItem，key 为板块索引。
// parseSectorList parses the EastMoney sector-list JSON; diff arrives as a
// map whose keys are board indexes.
func parseSectorList(body []byte) ([]SectorInfo, error) {
	var raw struct {
		Data *struct {
			Total int                      `json:"total"` // 板块总数
			Diff  map[string]sectorRawItem `json:"diff"`  // 板块明细（key 为索引）
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("eastmoney sector json: %v", err)
	}
	if raw.Data == nil || len(raw.Data.Diff) == 0 {
		return nil, nil
	}
	sectors := make([]SectorInfo, 0, len(raw.Data.Diff))
	for _, item := range raw.Data.Diff {
		if item.Code == "" {
			continue
		}
		// 东财 f3 为基点（1=0.01%），统一 ÷100 转百分位
		cp := item.ChangePct / 100
		sectors = append(sectors, SectorInfo{
			Code:       item.Code,
			Name:       item.Name,
			ChangePct:  cp,
			Amount:     item.Amount,
			NetInflow:  item.NetInflow,
			LimitupCnt: item.LimitupCnt,
			VolumeRank: item.VolumeRank,
		})
	}
	return sectors, nil
}

// ── 东方财富板块成分股 ──

// getSectorFields 东方财富板块成分股查询字段。
// 与 parseSectorStocks 使用的解析标签一致：f12/f14/f2/f3/f4/f15/f16/f17/f18/f5/f6/f7。
// getSectorFields lists the EastMoney sector-constituent fields,
// matching the tags parsed in parseSectorStocks.
const getSectorFields = "f12,f14,f2,f3,f4,f15,f16,f17,f18,f5,f6,f7"

// GetSectorStocks 获取指定板块的成分股列表。
// sectorCode 为板块代码（如 "BK0477"），topN 限制返回数量。
// GetSectorStocks returns constituents of a sector; sectorCode like "BK0477",
// topN caps the number of returned stocks.
func (m *MarketAPI) GetSectorStocks(sectorCode string, topN int) ([]StockInfo, error) {
	url := fmt.Sprintf("https://push2.eastmoney.com/api/qt/clist/get?pn=1&pz=%d&po=1&np=1&fs=b:%s&fields=%s", topN, sectorCode, getSectorFields)
	EastMoneyLimiter.Wait()
	resp, err := m.getWithHeaders(url, emReferer)
	if err != nil {
		return nil, fmt.Errorf("eastmoney sector stocks http: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("eastmoney sector stocks read: %v", err)
	}
	return parseSectorStocks(body)
}

// parseSectorStocks 解析东方财富板块成分股 JSON。
// 从 data.items 中提取每只股票的代码、名称、价格、涨跌幅、成交量等。
// 价格字段 F2/F15/F16/F17/F18 单位为分，需 ÷100 转换为元。
// 涨跌幅 F3 单位为基点（1 基点 = 0.01%），需 ÷100 转换为百分数。
// parseSectorStocks parses the EastMoney sector-constituent JSON. Price fields
// (F2/F15/F16/F17/F18) are in cents (/100 to Yuan); F3 is a basis-point value.
func parseSectorStocks(body []byte) ([]StockInfo, error) {
	var raw struct {
		Data struct {
			Items []struct {
				F12 string  `json:"f12"` // 代码
				F14 string  `json:"f14"` // 名称
				F2  float64 `json:"f2"`  // 最新价
				F3  float64 `json:"f3"`  // 涨跌幅
				F4  float64 `json:"f4"`  // 涨跌额
				F15 float64 `json:"f15"` // 最高
				F16 float64 `json:"f16"` // 最低
				F17 float64 `json:"f17"` // 开盘
				F18 float64 `json:"f18"` // 昨收
				F5  float64 `json:"f5"`  // 成交量
				F6  float64 `json:"f6"`  // 成交额
				F7  float64 `json:"f7"`  // 换手率
			} `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("eastmoney sector stocks json: %v", err)
	}
	stocks := make([]StockInfo, 0, len(raw.Data.Items))
	for _, item := range raw.Data.Items {
		if item.F12 == "" {
			continue
		}
		stocks = append(stocks, StockInfo{
			Code:      item.F12,
			Name:      item.F14,
			Price:     item.F2 / 100,
			ChangePct: item.F3 / 100,
			High:      item.F15 / 100,
			Low:       item.F16 / 100,
			Open:      item.F17 / 100,
			Close:     item.F18 / 100,
			Volume:    item.F5,
			Amount:    item.F6,
			Turnover:  item.F7,
		})
	}
	return stocks, nil
}

// ── 新浪 K 线 ──

// GetSinaKLine 获取新浪财经日 K 线数据。
// code 为股票代码，count 为请求的 K 线根数（通常 30-120）。
// 仅支持日线（period="101"）。
// GetSinaKLine fetches Sina daily K-line data (daily only, period "101").
func (m *MarketAPI) GetSinaKLine(code string, count int) ([]KLine, error) {
	// Sina KLine 需要 sh/sz 前缀（如 sh600519），不能用 EastMoney 的 1.600519 格式
	prefix := "sh"
	if !strings.HasPrefix(code, "6") && !strings.HasPrefix(code, "5") {
		prefix = "sz"
	}
	url := fmt.Sprintf("https://money.finance.sina.com.cn/quotes_service/api/json_v2.php/CN_MarketData.getKLineData?symbol=%s%s&scale=240&datalen=%d", prefix, code, count)
	SinaLimiter.Wait()
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("sina kline request: %v", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Linux; Android 14) AppleWebKit/537.36")
	req.Header.Set("Referer", "https://finance.sina.com.cn")
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sina kline http: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("sina kline read: %v", err)
	}
	return parseSinaKLine(body)
}

// sinaKLineRaw 新浪 K 线数据原始行。
// sinaKLineRaw is one raw Sina K-line row.
type sinaKLineRaw struct {
	Day    string `json:"day"`    // 日期（yyyy-mm-dd）
	Open   string `json:"open"`   // 开盘价
	High   string `json:"high"`   // 最高价
	Low    string `json:"low"`    // 最低价
	Close  string `json:"close"`  // 收盘价
	Volume string `json:"volume"` // 成交量
	Amount string `json:"amount"` // 成交额
}

// GetSinaMinuteKLine 获取新浪财经分钟级 K 线。
// scale 为分钟数（1/5/15/30/60），count 为返回的K线根数。
// 分钟K线的 day 字段为 "YYYY-MM-DD HH:MM:SS" 格式，解析逻辑与日线不同。
// GetSinaMinuteKLine fetches Sina minute-level K-lines; scale is 1/5/15/30/60,
// and minute rows use "YYYY-MM-DD HH:MM:SS" timestamps (parsed differently).
func (m *MarketAPI) GetSinaMinuteKLine(code string, scale, count int) ([]KLine, error) {
	prefix := "sh"
	if !strings.HasPrefix(code, "6") && !strings.HasPrefix(code, "5") {
		prefix = "sz"
	}
	url := fmt.Sprintf("https://money.finance.sina.com.cn/quotes_service/api/json_v2.php/CN_MarketData.getKLineData?symbol=%s%s&scale=%d&ma=no&datalen=%d", prefix, code, scale, count)
	SinaLimiter.Wait()
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("sina minute kline request: %v", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Linux; Android 14) AppleWebKit/537.36")
	req.Header.Set("Referer", "https://finance.sina.com.cn")
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sina minute kline http: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("sina minute kline read: %v", err)
	}
	var raw []sinaKLineRaw
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("sina minute kline json: %v", err)
	}
	klines := make([]KLine, 0, len(raw))
	for _, r := range raw {
		if r.Day == "" {
			continue
		}
		t, err := time.ParseInLocation("2006-01-02 15:04:05", r.Day, time.Local)
		if err != nil {
			continue
		}
		open, _ := strconv.ParseFloat(r.Open, 64)
		high, _ := strconv.ParseFloat(r.High, 64)
		low, _ := strconv.ParseFloat(r.Low, 64)
		close, _ := strconv.ParseFloat(r.Close, 64)
		volume, _ := strconv.ParseFloat(r.Volume, 64)
		amount, _ := strconv.ParseFloat(r.Amount, 64)
		klines = append(klines, KLine{
			Date:   t,
			Open:   open,
			High:   high,
			Low:    low,
			Close:  close,
			Volume: volume,
			Amount: amount,
		})
	}
	// 分钟数据源时间序不固定，统一按时间升序排序
	sort.Slice(klines, func(i, j int) bool {
		return klines[i].Date.Before(klines[j].Date)
	})
	return klines, nil
}

// parseSinaKLine 解析新浪 K 线 JSON 数组。
// 每行包含 day/open/high/low/close/volume/amount 字段。
// 返回按日期升序排列的 KLine 切片。
// parseSinaKLine parses a Sina K-line JSON array into time-ascending KLine rows.
func parseSinaKLine(body []byte) ([]KLine, error) {
	var raw []sinaKLineRaw
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("sina kline json: %v", err)
	}
	klines := make([]KLine, 0, len(raw))
	for _, r := range raw {
		if r.Day == "" {
			continue
		}
		t, err := time.Parse("2006-01-02", r.Day)
		if err != nil {
			continue
		}
		open, _ := strconv.ParseFloat(r.Open, 64)
		high, _ := strconv.ParseFloat(r.High, 64)
		low, _ := strconv.ParseFloat(r.Low, 64)
		close, _ := strconv.ParseFloat(r.Close, 64)
		volume, _ := strconv.ParseFloat(r.Volume, 64)
		amount, _ := strconv.ParseFloat(r.Amount, 64)
		klines = append(klines, KLine{
			Date:   t,
			Open:   open,
			High:   high,
			Low:    low,
			Close:  close,
			Volume: volume,
			Amount: amount,
		})
	}
	// 新浪返回的是升序（旧->新），但历史上曾返回倒序，统一按时间升序排序保证稳健
	sort.SliceStable(klines, func(i, j int) bool {
		return klines[i].Date.Before(klines[j].Date)
	})
	return klines, nil
}

// ── 东方财富 K 线 ──

// GetKLine 获取东方财富 K 线数据。
// code 为股票代码，period 为周期（101=日线，102=周线，103=月线），count 为根数。
// GetKLine fetches EastMoney K-lines; period 101=day,102=week,103=month.
func (m *MarketAPI) GetKLine(code, period string, count int) ([]KLine, error) {
	sid := secID(code)
	url := fmt.Sprintf("https://push2.eastmoney.com/api/qt/stock/kline/get?secid=%s&klt=%s&lmt=%d&fqt=1", sid, period, count)
	EastMoneyLimiter.Wait()
	resp, err := m.getWithHeaders(url, emReferer)
	if err != nil {
		return nil, fmt.Errorf("eastmoney kline http: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("eastmoney kline read: %v", err)
	}
	var raw struct {
		Data struct {
			KLines []string `json:"klines"` // K 线 CSV 行数组
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("eastmoney kline json: %v", err)
	}
	return parseEastMoneyKLine(raw.Data.KLines)
}

// parseEastMoneyKLine 解析东方财富 K 线 CSV 字符串数组。
// 每行格式：date,open,high,low,close,volume,amount[,...]
// parseEastMoneyKLine parses EastMoney K-line CSV rows:
// each line is date,open,high,low,close,volume,amount[, ...].
func parseEastMoneyKLine(rawLines []string) ([]KLine, error) {
	klines := make([]KLine, 0, len(rawLines))
	for _, line := range rawLines {
		parts := strings.Split(line, ",")
		if len(parts) < 7 {
			continue
		}
		t, err := time.Parse("2006-01-02", parts[0])
		if err != nil {
			// 分钟级别行形如 "2006-01-02 15:04"，尝试日期时间格式
			// minute-level rows look like "2006-01-02 15:04"; try the datetime format
			t, err = time.Parse("2006-01-02 15:04", parts[0])
			if err != nil {
				continue
			}
		}
		klines = append(klines, KLine{
			Date:   t,
			Open:   toFloat64(parts[1]),
			High:   toFloat64(parts[2]),
			Low:    toFloat64(parts[3]),
			Close:  toFloat64(parts[4]),
			Volume: toFloat64(parts[5]),
			Amount: toFloat64(parts[6]),
		})
	}
	return klines, nil
}

// ── 新浪财经新闻 ──

// GetSinaNews 获取新浪财经新闻（快讯/滚动）。
// pageSize 限制返回条数。
// GetSinaNews fetches Sina Finance flash/rolling news capped by pageSize.
func (m *MarketAPI) GetSinaNews(pageSize int) ([]NewsItem, error) {
	url := fmt.Sprintf("https://feed.mix.sina.com.cn/api/roll/get?pageid=153&lid=2516&knum=%d", pageSize)
	SinaLimiter.Wait()
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("sina news request: %v", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Linux; Android 14) AppleWebKit/537.36")
	req.Header.Set("Referer", "https://finance.sina.com.cn")
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sina news http: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("sina news read: %v", err)
	}
	log.Printf("sina news: got %d bytes", len(body))
	return parseSinaNews(body)
}

// sinaNewsItemRaw 新浪新闻原始响应条目。
// sinaNewsItemRaw is one raw Sina news response item.
type sinaNewsItemRaw struct {
	Title    string `json:"title"`     // 标题
	Content  string `json:"content"`   // 内容/摘要
	ShowTime string `json:"show_time"` // 展示时间字符串
	Ctime    string `json:"ctime"`     // 发布时间字符串
	Url      string `json:"url"`       // 原文链接
}

// parseSinaNews 解析新浪新闻 JSON 响应。
// 提取标题、内容摘要、发布时间。
// parseSinaNews parses a Sina news JSON response, extracting title, summary and time.
func parseSinaNews(body []byte) ([]NewsItem, error) {
	var raw struct {
		Result struct {
			Data []sinaNewsItemRaw `json:"data"` // 新闻条目数组
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("sina news json: %v", err)
	}
	items := make([]NewsItem, 0, len(raw.Result.Data))
	for _, r := range raw.Result.Data {
		if r.Title == "" {
			continue
		}
		items = append(items, NewsItem{
			Title:    r.Title,
			Content:  r.Content,
			URL:      r.Url,
			Datetime: r.ShowTime,
			Source:   "新浪财经",
		})
	}
	return items, nil
}

// ── 东方财富新闻 ──

// GetEastMoneyNews 获取东方财富快讯。
// pageSize 限制返回条数。
// GetEastMoneyNews fetches EastMoney flash news capped by pageSize.
func (m *MarketAPI) GetEastMoneyNews(pageSize int) ([]NewsItem, error) {
	if pageSize <= 0 {
		pageSize = 20
	}
	apiURL := fmt.Sprintf("https://np-anotice-stock.eastmoney.com/api/security/announcement/query?page_size=%d&page_index=1&ann_type=fast", pageSize)
	EastMoneyLimiter.Wait()
	resp, err := m.getWithHeaders(apiURL, emNewsReferer)
	if err != nil {
		return nil, fmt.Errorf("eastmoney news http: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("eastmoney news read: %v", err)
	}
	return parseEastMoneyNews(body)
}

// parseEastMoneyNews 解析东方财富快讯 JSON。
// 提取标题、内容、发布时间。
// parseEastMoneyNews parses the EastMoney flash-news JSON.
func parseEastMoneyNews(body []byte) ([]NewsItem, error) {
	var raw struct {
		Data struct {
			List []struct {
				Title    string `json:"title"`     // 标题
				Content  string `json:"content"`   // 正文摘要
				ShowTime string `json:"show_time"` // 发布时间
				Source   string `json:"source"`    // 来源
			} `json:"list"` // 快讯列表
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("eastmoney news json: %v", err)
	}
	items := make([]NewsItem, 0, len(raw.Data.List))
	for _, n := range raw.Data.List {
		if n.Title == "" {
			continue
		}
		items = append(items, NewsItem{
			Title:    n.Title,
			Content:  n.Content,
			Datetime: n.ShowTime,
			Source:   "东方财富",
		})
	}
	return items, nil
}

// ── 同花顺快讯 ──

// GetTonghuashunNews 获取同花顺快讯（主力新闻源）。
// 同花顺快讯推送速度最快，作为新闻获取的首选数据源。
// API 端点：https://news.10jqka.com.cn/tapp/news/push/stock
// 当同花顺不可用时，降级到新浪或东方财富新闻。
// pageSize 限制返回条数。
// GetTonghuashunNews fetches Tonghuashun flash news (the preferred news source,
// fastest pump); falls back to Sina/EastMoney when unavailable.
func (m *MarketAPI) GetTonghuashunNews(pageSize int) ([]NewsItem, error) {
	if pageSize <= 0 {
		pageSize = 20
	}
	url := fmt.Sprintf("https://news.10jqka.com.cn/tapp/news/push/stock?page=1&pagesize=%d", pageSize)
	THSLimiter.Wait()
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("ths news request: %v", err)
	}
	req.Header.Set("User-Agent", thsUserAgent)
	req.Header.Set("Referer", "https://www.10jqka.com.cn/")
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ths news http: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("ths news read: %v", err)
	}
	return parseTonghuashunNews(body)
}

// GetTonghuashunNewsPage 获取指定页的同花顺快讯（用于分页历史追回）。
// GetTonghuashunNewsPage fetches a specific page of THS news for historical backfill.
func (m *MarketAPI) GetTonghuashunNewsPage(page, pageSize int) ([]NewsItem, error) {
	if pageSize <= 0 {
		pageSize = 20
	}
	url := fmt.Sprintf("https://news.10jqka.com.cn/tapp/news/push/stock?page=%d&pagesize=%d", page, pageSize)
	THSLimiter.Wait()
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("ths news request: %v", err)
	}
	req.Header.Set("User-Agent", thsUserAgent)
	req.Header.Set("Referer", "https://www.10jqka.com.cn/")
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ths news http: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("ths news read: %v", err)
	}
	return parseTonghuashunNews(body)
}

// parseTonghuashunNews 解析同花顺快讯 JSON。
// 同花顺 push API 返回结构：
//
//	{"code":"200","data":{"list":[
//	  {"title":"标题","digest":"摘要","time":"2026-07-29 10:30:00"}
//	]}}
//
// code 非 "200" 表示接口异常，直接返回错误。
// parseTonghuashunNews parses the THS flash-news JSON; a code other than
// "200" is treated as an API error.
func parseTonghuashunNews(body []byte) ([]NewsItem, error) {
	var raw struct {
		Code string `json:"code"` // 接口状态码（"200" 表示成功）
		Data struct {
			List []struct {
				Title  string `json:"title"`  // 标题
				Digest string `json:"digest"` // 摘要
				Ctime  string `json:"ctime"`  // 发布时间
				Url    string `json:"url"`    // 原文链接
			} `json:"list"` // 快讯列表
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("ths news json: %v", err)
	}
	if raw.Code != "200" {
		return nil, fmt.Errorf("ths news code=%s", raw.Code)
	}
	items := make([]NewsItem, 0, len(raw.Data.List))
	for _, r := range raw.Data.List {
		if r.Title == "" {
			continue
		}
		items = append(items, NewsItem{
			Title:    r.Title,
			Content:  r.Digest,
			URL:      r.Url,
			Datetime: r.Ctime,
			Source:   "同花顺",
		})
	}
	return items, nil
}

// ── 新闻原文抓取 ──

// GetArticle 抓取新闻原文正文。
// 支持同花顺（news.10jqka.com.cn）与新浪财经（finance.sina.com.cn）两类文章页：
//   - 同花顺：正文位于 <div class="news-content article-content">，移动 UA + Referer 直接可取。
//   - 新浪：正文位于 <div id="artibody">，需桌面 UA 且跟随 302 跳转（移动 UA 会被重定向到网关页）。
//
// 失败返回空串，调用方应保留原始摘要继续流水线。
// GetArticle scrapes the full article body from THS or Sina article pages;
// returns "" on failure so callers keep the original summary in the pipeline.
func (m *MarketAPI) GetArticle(url string) (string, error) {
	u, err := urlpkg.Parse(url)
	if err != nil {
		return "", fmt.Errorf("article url parse: %v", err)
	}
	host := u.Hostname()
	if host == "" {
		return "", fmt.Errorf("article url host empty")
	}

	var req *http.Request
	if req, err = http.NewRequest("GET", url, nil); err != nil {
		return "", fmt.Errorf("article request: %v", err)
	}

	switch {
	case strings.Contains(host, "10jqka.com.cn"):
		THSLimiter.Wait()
		req.Header.Set("User-Agent", thsUserAgent)
		req.Header.Set("Referer", "https://www.10jqka.com.cn/")
	case strings.Contains(host, "sina.com.cn"):
		SinaLimiter.Wait()
		req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
		req.Header.Set("Referer", "https://finance.sina.com.cn")
	default:
		return "", fmt.Errorf("unsupported article host: %s", host)
	}

	resp, err := m.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("article http: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("article status=%d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("article read: %v", err)
	}

	var content string
	if strings.Contains(host, "10jqka.com.cn") {
		content = extractArticleDiv(string(body), `class="news-content article-content"`)
	} else {
		content = extractArticleDiv(string(body), `id="artibody"`)
	}
	return content, nil
}

// extractArticleDiv 从文章 HTML 中提取指定起始标记所在的 <div> 块文本。
// 从标记后的下一个 '>' 开始，按 <div/</div> 深度匹配找到闭合，再去除标签与空白。
// extractArticleDiv extracts text of the <div> block containing a marker,
// matching div depth from the first '>' then stripping tags and whitespace.
func extractArticleDiv(html, marker string) string {
	i := strings.Index(html, marker)
	if i < 0 {
		return ""
	}
	gt := strings.Index(html[i:], ">")
	if gt < 0 {
		return ""
	}
	start := i + gt + 1

	depth := 1
	pos := start
	for depth > 0 && pos < len(html) {
		o, c := -1, -1
		if idx := strings.Index(html[pos:], "<div"); idx >= 0 {
			o = idx
		}
		if idx := strings.Index(html[pos:], "</div>"); idx >= 0 {
			c = idx
		}
		if o < 0 && c < 0 {
			break
		}
		if c < 0 || (o >= 0 && o < c) {
			depth++
			pos += o + 4
		} else {
			depth--
			pos += c + 6
		}
	}
	if depth > 0 {
		pos = len(html)
	}
	raw := html[start:pos]

	raw = htmlTagRe.ReplaceAllString(raw, " ")
	raw = strings.ReplaceAll(raw, "&nbsp;", " ")
	raw = strings.ReplaceAll(raw, "&quot;", "\"")
	raw = strings.ReplaceAll(raw, "&amp;", "&")
	raw = strings.ReplaceAll(raw, "&lt;", "<")
	raw = strings.ReplaceAll(raw, "&gt;", ">")
	return strings.TrimSpace(strings.Join(strings.Fields(raw), " "))
}

// ── 新股日历 ──

// GetEastMoneyIPOCalendar 获取东方财富新股日历。
// 调用 datacenter-web 数据中心接口，返回近期新股申购/上市数据。
// GetEastMoneyIPOCalendar fetches the EastMoney IPO calendar via the
// data-center API, returning recent subscription/listing events.
func (m *MarketAPI) GetEastMoneyIPOCalendar() ([]IPOEvent, error) {
	apiURL := "https://datacenter-web.eastmoney.com/api/data/v1/get?reportName=RPTA_APP_IPOAPPLY&columns=ALL&pageNumber=1&pageSize=50&sortTypes=-1&sortColumns=LISTING_DATE"
	EastMoneyLimiter.Wait()
	resp, err := m.getWithHeaders(apiURL, emDataReferer)
	if err != nil {
		return nil, fmt.Errorf("eastmoney ipo http: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("eastmoney ipo read: %v", err)
	}
	return parseEastMoneyIPO(body)
}

// parseEastMoneyIPO 解析东方财富新股日历 JSON。
// parseEastMoneyIPO parses the EastMoney IPO-calendar JSON into IPOEvent records,
// keeping only listings on or after today and sorting by listing date.
func parseEastMoneyIPO(body []byte) ([]IPOEvent, error) {
	var raw struct {
		Success bool `json:"success"` // 接口调用是否成功
		Result  struct {
			Data []struct {
				SecurityCode string  `json:"SECURITY_CODE"` // 股票代码
				SecurityName string  `json:"SECURITY_NAME"` // 股票名称
				ApplyDate    string  `json:"APPLY_DATE"`    // 申购日期
				IssuePrice   float64 `json:"ISSUE_PRICE"`   // 发行价（元）
				ListingDate  string  `json:"LISTING_DATE"`  // 上市日期
			} `json:"data"` // IPO 记录数组
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("eastmoney ipo json: %v", err)
	}
	if !raw.Success {
		return nil, fmt.Errorf("eastmoney ipo: API returned success=false")
	}
	today := time.Now().Format("20060102")
	items := make([]IPOEvent, 0, len(raw.Result.Data))
	for _, r := range raw.Result.Data {
		code := r.SecurityCode
		if idx := strings.Index(code, "."); idx > 0 {
			code = code[:idx]
		}
		listingDate := shortDate(r.ListingDate)
		// 只保留 T日及之后的上市
		if listingDate < today {
			continue
		}
		items = append(items, IPOEvent{
			Code:        code,
			Name:        r.SecurityName,
			IPODate:     shortDate(r.ApplyDate),
			ListingDate: listingDate,
			IssuePrice:  r.IssuePrice,
			ListStatus:  listingStatus(listingDate),
		})
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].ListingDate > items[j].ListingDate
	})
	return items, nil
}

// listingStatus 根据上市日期返回 list_status。
// listingStatus returns the listing status ("U" unlisted/"L" listed) from a date.
func listingStatus(listingDate string) string {
	if listingDate == "" {
		return "U"
	}
	t, err := time.Parse("20060102", listingDate)
	if err != nil {
		return "U"
	}
	if t.After(time.Now()) {
		return "U"
	}
	return "L"
}

// shortDate 将 "2026-07-29 00:00:00" 转为 "20260729"。
// shortDate normalizes "2026-07-29 00:00:00" to "20260729".
func shortDate(s string) string {
	if len(s) < 10 {
		return s
	}
	return strings.ReplaceAll(s[:10], "-", "")
}

// ── 指数行情 ──

// GetIndexData 获取上证指数行情和全市场涨跌家数。
// 指数 F43 字段单位为分，需 ÷100 还原为实际指数点位。
// 返回：
//   - indexPrice: 上证指数当前价
//   - ma20: 上证指数 20 日均线
//   - upCount: 上涨家数
//   - downCount: 下跌家数
//
// GetIndexData returns the SH index price, its MA20, and the market-wide
// up/down counts for sentiment gauging.
func (m *MarketAPI) GetIndexData() (indexPrice float64, ma20 float64, upCount, downCount int, err error) {
	// 获取上证指数实时行情
	sid := secID("000001")
	url := fmt.Sprintf("https://push2.eastmoney.com/api/qt/stock/get?secid=%s&fields=f43,f44,f45,f46,f47,f48,f49,f50,f51,f52,f58,f169,f170,f171", sid)
	EastMoneyLimiter.Wait()
	resp, err := m.getWithHeaders(url, emReferer)
	if err != nil {
		return 0, 0, 0, 0, fmt.Errorf("eastmoney index http: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, 0, 0, 0, fmt.Errorf("eastmoney index read: %v", err)
	}
	var raw struct {
		Data struct {
			F43 float64 `json:"f43"` // 当前价
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return 0, 0, 0, 0, fmt.Errorf("eastmoney index json: %v", err)
	}
	if raw.Data.F43 == 0 {
		return 0, 0, 0, 0, fmt.Errorf("eastmoney: no index data")
	}
	indexPrice = raw.Data.F43 / 100

	// 获取上证指数日 K 线计算 MA20
	klines, err := m.GetKLine("000001", "101", 30)
	if err == nil && len(klines) >= 20 {
		sum := 0.0
		start := len(klines) - 20
		if start < 0 {
			start = 0
		}
		for i := start; i < len(klines); i++ {
			sum += klines[i].Close
		}
		ma20 = sum / 20.0
	}

	// 获取涨跌家数（使用东方财富市场概况接口）
	upCount = 1500
	downCount = 1500
	marketURL := "https://push2.eastmoney.com/api/qt/stock/fflow/kline/get?secid=1.000001&fields1=f1,f2,f3,f4,f5,f6&fields2=f51,f52,f53,f54,f55,f56,f57,f58,f59,f60,f61,f62,f63"
	EastMoneyLimiter.Wait()
	mResp, mErr := m.getWithHeaders(marketURL, emReferer)
	if mErr == nil {
		defer mResp.Body.Close()
		if mBody, mReadErr := io.ReadAll(mResp.Body); mReadErr == nil {
			var marketRaw struct {
				Data struct {
					F62 float64 `json:"f62"` // 上涨家数
					F63 float64 `json:"f63"` // 下跌家数
				} `json:"data"`
			}
			if json.Unmarshal(mBody, &marketRaw) == nil {
				if marketRaw.Data.F62 > 0 {
					upCount = int(marketRaw.Data.F62)
				}
				if marketRaw.Data.F63 > 0 {
					downCount = int(marketRaw.Data.F63)
				}
			}
		}
	}

	return indexPrice, ma20, upCount, downCount, nil
}

// GetIndexQuote 获取指数实时报价（含涨跌幅）。
// 用于策略评估中的基准对比。
// GetIndexQuote returns a live index quote for baseline comparison in strategies.
func (m *MarketAPI) GetIndexQuote(code string) (*StockInfo, error) {
	return m.GetRealtimeQuote(code)
}

// ── 资金流向 ──

// GetStockMoneyFlow 获取东方财富个股资金流向。
// 返回 CapitalFlow，包含超大单/大单/中单/小单的买卖金额及主力净流入。
// GetStockMoneyFlow returns an individual stock's capital-flow breakdown
// (super-large/large/medium/small orders and main-capital net inflow).
func (m *MarketAPI) GetStockMoneyFlow(code string) (*CapitalFlow, error) {
	sid := secID(code)
	url := fmt.Sprintf("https://push2.eastmoney.com/api/qt/stock/fflow/kline/get?secid=%s&fields1=f1,f2,f3,f4,f5,f6&fields2=f51,f52,f53,f54,f55,f56,f57,f58,f59,f60,f61,f62,f63", sid)
	EastMoneyLimiter.Wait()
	resp, err := m.getWithHeaders(url, emReferer)
	if err != nil {
		return nil, fmt.Errorf("eastmoney moneyflow http: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("eastmoney moneyflow read: %v", err)
	}
	return parseMoneyFlow(body, code)
}

// parseMoneyFlow 解析东方财富资金流向 JSON。
// klines 中最近的 N 行分别对应超大单/大单/中单/小单的各方向金额。
// 字段索引：buy_elg, sell_elg, buy_lg, sell_lg, buy_md, sell_md, buy_sm, sell_sm, net
// parseMoneyFlow parses the EastMoney capital-flow JSON; the last K-line row is
// the latest cumulative day, with fields in Yunt order by order size; amounts are in 万元.
func parseMoneyFlow(body []byte, code string) (*CapitalFlow, error) {
	var raw struct {
		Data struct {
			KLines []string `json:"klines"` // 资金流 CSV 行数组
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("eastmoney moneyflow json: %v", err)
	}
	if len(raw.Data.KLines) == 0 {
		return nil, fmt.Errorf("eastmoney: no moneyflow data for %s", code)
	}

	// 取最新一行（当日累计）
	lastLine := raw.Data.KLines[len(raw.Data.KLines)-1]
	parts := strings.Split(lastLine, ",")
	if len(parts) < 13 {
		return nil, fmt.Errorf("eastmoney: moneyflow fields too short (%d)", len(parts))
	}

	cf := &CapitalFlow{
		Code:          code,
		SuperLargeIn:  toFloat64(parts[1]) * 10000, // 超大单流入
		SuperLargeOut: toFloat64(parts[2]) * 10000, // 超大单流出
		LargeIn:       toFloat64(parts[3]) * 10000, // 大单流入
		LargeOut:      toFloat64(parts[4]) * 10000, // 大单流出
		MediumIn:      toFloat64(parts[5]) * 10000, // 中单流入
		MediumOut:     toFloat64(parts[6]) * 10000, // 中单流出
		SmallIn:       toFloat64(parts[7]) * 10000, // 小单流入
		SmallOut:      toFloat64(parts[8]) * 10000, // 小单流出
		NetInflow:     toFloat64(parts[9]) * 10000, // 主力净流入
		Time:          time.Now(),
	}
	return cf, nil
}

// ── 通用辅助函数 ──

// toFloat64 将 interface{} 安全转为 float64。
// 支持 float64、float32、int、int64、int32、string 类型。
// toFloat64 safely converts interface{} (numeric or numeric-string) to float64.
func toFloat64(v interface{}) float64 {
	switch val := v.(type) {
	case float64:
		return val
	case float32:
		return float64(val)
	case int:
		return float64(val)
	case int64:
		return float64(val)
	case int32:
		return float64(val)
	case string:
		f, _ := strconv.ParseFloat(val, 64)
		return f
	default:
		return 0
	}
}

// getStringField 从 map 中安全读取字符串字段。
// getStringField safely reads a string field from a map ("" when missing/mistyped).
func getStringField(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// maxSectorChange 求多个 SectorInfo 中 ChangePct 最大的值，用于归一化评分。
// maxSectorChange returns the max ChangePct across sectors for score normalization.
func maxSectorChange(sectors []SectorInfo) float64 {
	m := 0.0
	for _, s := range sectors {
		if s.ChangePct > m {
			m = s.ChangePct
		}
	}
	return m
}

// GetStockIndustry 获取个股所属行业（东财 push2）。
// 返回行业名称（如"白酒""半导体"），查询失败返回空串。
// GetStockIndustry returns a stock's industry name via EastMoney push2
// (e.g. "白酒"/"半导体"), or "" on failure.
func (m *MarketAPI) GetStockIndustry(code string) string {
	sid := secID(code)
	url := fmt.Sprintf("https://push2.eastmoney.com/api/qt/stock/get?secid=%s&fields=f57,f58,f127,f128", sid)
	EastMoneyLimiter.Wait()
	resp, err := m.getWithHeaders(url, emReferer)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return ""
	}
	var raw struct {
		Data struct {
			F128 string `json:"f128"` // 行业名称
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return ""
	}
	return raw.Data.F128
}

// stockListFields 全量股票列表查询字段（代码+名称+PE+市值）。
// stockListFields lists the fields for the full-stock-list query (code/name/PE/market cap).
const stockListFields = "f12,f14,f9,f20"

// stockRawItem 东方财富全量股票列表的原始 JSON 行。
// stockRawItem is a raw row of the EastMoney full-stock-list JSON.
type stockRawItem struct {
	Code string  `json:"f12"` // 股票代码
	Name string  `json:"f14"` // 股票名称
	PE   float64 `json:"f9"`  // 市盈率
	MCap float64 `json:"f20"` // 总市值
}

// GetStockPE 获取单只个股的动态市盈率（PE-TTM）。
// 通过东财 clist 接口按证券ID单查（fields=f9 市盈率），失败返回 0（调用方按无PE处理）。
// 带独立 TTL 缓存（PE 变动低频），避免高频调用撞东财限流。
// GetStockPE returns a stock's PE-TTM via the EastMoney clist endpoint,
// with an independent TTL cache since PE changes infrequently. Returns 0 on failure.
func (m *MarketAPI) GetStockPE(code string) float64 {
	code = stripSuffix(code)
	if code == "" {
		return 0
	}
	if v, ok := m.getPECache(code); ok {
		return v
	}

	url := fmt.Sprintf("https://push2.eastmoney.com/api/qt/clist/get?pn=1&pz=1&po=1&np=1&fltt=2&invt=2&fid=f3&fs=m:0+t:6,m:0+t:80,m:1+t:2,m:1+t:23,m:0+t:81+s:%s&fields=f12,f14,f9", secID(code))
	EastMoneyLimiter.Wait()
	resp, err := m.getWithHeaders(url, emReferer)
	if err != nil {
		log.Printf("[stockpe] 获取 %s 失败: %v", code, err)
		return 0
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0
	}
	var raw struct {
		Data *struct {
			Diff []stockRawItem `json:"diff"` // 查询命中的股票行
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &raw); err != nil || raw.Data == nil || len(raw.Data.Diff) == 0 {
		return 0
	}
	item := raw.Data.Diff[0]
	if item.PE <= 0 {
		return 0
	}
	m.setPECache(code, item.PE)
	return item.PE
}

// GetStockList 获取全量 A 股列表（代码+名称）。
// 兜底链：新浪财经全市场列表（主源，分页抓取） → 东方财富（按板块合并）。
// 注意：同花顺全市场列表页为 JS 渲染（chameleon 反爬），无服务端可解析端点，
// 故兜底链为 新浪 → 东财 两级（同花顺仅提供个股行情/板块名单）。
// GetStockList returns the full A-share list (code+name) with a fallback chain:
// Sina full-market list (primary) then EastMoney by merged boards.
func (m *MarketAPI) GetStockList() (map[string]string, error) {
	// 主源：新浪财经全市场列表（hs_a 节点，分页抓取，约 5500+ 只）
	if list, err := m.GetSinaStockList(); err == nil && len(list) > 0 {
		log.Printf("[stocklist] 新浪全市场 %d 只", len(list))
		return list, nil
	} else if err != nil {
		log.Printf("[stocklist] 新浪列表失败: %v, 降级东财", err)
	}

	// 兜底：东方财富各板块合并
	fs := "m:0+t:6,m:0+t:80,m:1+t:2,m:1+t:23,m:0+t:81"
	url := fmt.Sprintf("https://push2.eastmoney.com/api/qt/clist/get?pn=1&pz=10000&fs=%s&fields=%s", fs, stockListFields)
	EastMoneyLimiter.Wait()
	resp, err := m.getWithHeaders(url, emReferer)
	if err != nil {
		return nil, fmt.Errorf("eastmoney stock list http: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("eastmoney stock list read: %v", err)
	}
	var raw struct {
		Data *struct {
			Total int                     `json:"total"` // 股票总数
			Diff  map[string]stockRawItem `json:"diff"`  // 股票明细（key 为索引）
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("eastmoney stock list json: %v", err)
	}
	if raw.Data == nil || len(raw.Data.Diff) == 0 {
		return nil, nil
	}
	result := make(map[string]string, len(raw.Data.Diff))
	for _, item := range raw.Data.Diff {
		if item.Code == "" || item.Name == "" {
			continue
		}
		result[item.Name] = item.Code
	}
	return result, nil
}

// sinaStockPageSize 新浪全市场列表每页条数（接口固定上限 100）。
// sinaStockPageSize is the Sina full-market list page size (API caps at 100).
const sinaStockPageSize = 100

// sinaStockListMaxPages 新浪全市场列表最大翻页数（约 5500 只 / 100 = 56 页）。
// sinaStockListMaxPages caps the Sina full-market pagination (~55 pages needed).
const sinaStockListMaxPages = 60

// GetSinaStockList 从新浪财经获取全市场 A 股列表（代码+名称），分页抓取合并。
// 数据来源：Market_Center.getHQNodeData，node=hs_a 覆盖沪深北全部 A 股。
// 返回 map[股票名称]代码，供 StockCleaner 建立名称↔代码映射。
// GetSinaStockList fetches the full A-share list from Sina (paginated merge),
// returning map[name]code for StockCleaner's name↔code mapping.
func (m *MarketAPI) GetSinaStockList() (map[string]string, error) {
	result := make(map[string]string)
	for page := 1; page <= sinaStockListMaxPages; page++ {
		items, err := m.fetchSinaStockPage(page)
		if err != nil {
			if len(result) > 0 {
				return result, nil // 已有部分数据，部分失败也返回已抓取的
			}
			return nil, err
		}
		if len(items) == 0 {
			break
		}
		for _, it := range items {
			if it.Code == "" || it.Name == "" {
				continue
			}
			result[it.Name] = it.Code
		}
		if len(items) < sinaStockPageSize {
			break // 已到最后一页
		}
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("sina stock list empty")
	}
	return result, nil
}

// sinaStockItemRaw 新浪全市场列表 JSON 条目（getHQNodeData 返回数组）。
// sinaStockItemRaw is one Sina full-market list item (an array element of getHQNodeData).
type sinaStockItemRaw struct {
	Symbol string `json:"symbol"` // 带交易所前缀代码，如 "sh600519"
	Code   string `json:"code"`   // 纯 6 位代码
	Name   string `json:"name"`   // 股票名称
}

// fetchSinaStockPage 拉取新浪全市场列表第 page 页（每页 100 条）。
// fetchSinaStockPage pulls one page (100 items) of the Sina full-market list.
func (m *MarketAPI) fetchSinaStockPage(page int) ([]sinaStockItemRaw, error) {
	url := fmt.Sprintf("https://vip.stock.finance.sina.com.cn/quotes_service/api/json_v2.php/Market_Center.getHQNodeData?page=%d&num=%d&sort=symbol&asc=1&node=hs_a", page, sinaStockPageSize)
	SinaLimiter.Wait()
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("sina stock list request: %v", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Referer", "https://finance.sina.com.cn")
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sina stock list http: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("sina stock list read: %v", err)
	}
	var items []sinaStockItemRaw
	if err := json.Unmarshal(body, &items); err != nil {
		return nil, fmt.Errorf("sina stock list json: %v", err)
	}
	return items, nil
}

// filterStocksBySector 按板块代码过滤成分股列表。
// filterStocksBySector filters a constituent list by sector code.
func filterStocksBySector(stocks []StockInfo, sectorCode string) []StockInfo {
	var out []StockInfo
	for _, s := range stocks {
		if s.Code == sectorCode {
			out = append(out, s)
		}
	}
	return out
}

// sortSectorByChange 按涨跌幅降序排列板块。
// sortSectorByChange sorts sectors by ChangePct descending.
func sortSectorByChange(sectors []SectorInfo) {
	sort.Slice(sectors, func(i, j int) bool {
		return sectors[i].ChangePct > sectors[j].ChangePct
	})
}

// sortStockByChange 按涨跌幅降序排列股票。
// sortStockByChange sorts stocks by ChangePct descending.
func sortStockByChange(stocks []StockInfo) {
	sort.Slice(stocks, func(i, j int) bool {
		return stocks[i].ChangePct > stocks[j].ChangePct
	})
}

// init 注册初始化日志。
// init logs the MarketAPI startup message.
func init() {
	log.Println("[data] MarketAPI 初始化 (Sina + EastMoney push2)")
}
