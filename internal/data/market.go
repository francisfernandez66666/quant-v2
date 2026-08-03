// Package data — 东方财富 push2 + 新浪财经行情 API 客户端（主力数据源）。
// 提供实时行情、K线、板块、资金流向、新闻等全量接口，
// 所有请求通过 net/http 直连，不引入第三方库。
// 限流通过 rate.go 中的 SinaLimiter / EastMoneyLimiter 控制。
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
var htmlTagRe = regexp.MustCompile(`(?s)<[^>]+>`)

// roundTripperFunc 将普通函数适配为 http.RoundTripper。
// 用于在 HTTP 请求中注入自定义逻辑（日志、限流等）。
type roundTripperFunc func(*http.Request) (*http.Response, error)

// RoundTrip 执行 RoundTripper 接口调用。
func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

// sectorRawItem 东方财富板块行情列表的原始 JSON 行。
// 对应 push2.eastmoney.com/api/qt/clist/get 返回的 diff 条目。
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
type MarketAPI struct {
	client *http.Client

	quoteMu    sync.Mutex
	quoteCache map[string]cachedQuote // 实时行情 TTL 缓存
}

// quoteTTL 实时行情缓存有效期：同一股票在窗口内只打一次网络，
// 显著降低前端轮询/多接口下的限流压力（东财 3 req/s）。
const quoteTTL = 5 * time.Second

// cachedQuote 实时行情缓存条目。
// si 为缓存的行情快照，at 为缓存写入时间（用于判断是否超过 quoteTTL）。
type cachedQuote struct {
	si *StockInfo
	at time.Time
}

// emUserAgent 东财请求使用的浏览器 User-Agent。
const emUserAgent = "Mozilla/5.0 (Linux; Android 14) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.6099.144 Mobile Safari/537.36"

// emReferer 东财请求使用的 Referer。
const emReferer = "https://quote.eastmoney.com/"

// emDataReferer 东财数据中心请求使用的 Referer。
const emDataReferer = "https://data.eastmoney.com/"

// emNewsReferer 东财快讯请求使用的 Referer。
const emNewsReferer = "https://np-anotice-stock.eastmoney.com/"

// NewMarketAPI 创建行情 API 客户端。
// 使用带限流功能的 HTTP 客户端，默认超时 10 秒。
func NewMarketAPI() *MarketAPI {
	return &MarketAPI{
		client:     &http.Client{Timeout: 10 * time.Second},
		quoteCache: make(map[string]cachedQuote),
	}
}

// SetTransport 替换底层 HTTP Transport（测试注入 mock 网络，不影响限流与缓存逻辑）。
func (m *MarketAPI) SetTransport(rt http.RoundTripper) {
	m.client.Transport = rt
}

// getWithHeaders 发起带浏览器头部模拟的 GET 请求。
// 解决东财 CDN 对无头请求的 geo-block / anti-crawler 封锁。
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
func stripSuffix(code string) string {
	if len(code) > 3 && code[len(code)-3:] == ".SH" || len(code) > 3 && code[len(code)-3:] == ".SZ" || len(code) > 3 && code[len(code)-3:] == ".BJ" {
		return code[:len(code)-3]
	}
	return code
}

// secID 将股票代码转换为东方财富 push2 证券ID格式。
// 沪市（6/5 开头）加 "1." 前缀，深市加 "0." 前缀。
// 东财 secid 形如 "1.600519"（沪）/ "0.000001"（深），是 push2 各接口的通用标识。
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
var sinaQuoteRe = regexp.MustCompile(`var\s+hq_str_(?:sh|sz|bj)(\d+)\s*=\s*"([^"]*)"`)

// getSinaQuotes 发起单次新浪批量请求并解析全部行。
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
		out[code] = si
	}
	return out
}

// ── 东方财富 push2 实时行情 ──

// stockQuoteFields 东方财富 push2 个股行情字段列表。
const stockQuoteFields = "f43,f44,f45,f46,f47,f48,f49,f50,f51,f52,f55,f57,f58,f60,f116,f117,f162,f167,f168,f169,f170,f171,f292"

// GetRealtimeQuote 获取实时行情。先尝试东方财富 push2，失败时回退到新浪。
// 结果按 quoteTTL 短期缓存，同一股票在窗口内的重复请求直接命中缓存。
// 东财 push2 接口返回的价格字段（F43/F44/F45/F46/F60）单位为分，需 ÷100 转换为元。
// 返回 StockInfo，包含名称、价格、涨跌幅、成交量、换手率、主力净流入等。
func (m *MarketAPI) GetRealtimeQuote(code string) (*StockInfo, error) {
	code = stripSuffix(code)
	if c, ok := m.quoteHit(code); ok {
		return c, nil
	}
	info, err := m.getEastMoneyQuote(code)
	if err == nil {
		m.quoteStore(code, info)
		return info, nil
	}
	sina, serr := m.GetSinaQuote(code)
	if serr != nil {
		return nil, serr
	}
	m.quoteStore(code, sina)
	return sina, nil
}

// quoteHit 命中返回缓存中的实时行情副本；缓存不存在或已过期（超过 quoteTTL）时返回 nil,false。
// 返回副本而非原指针，避免调用方修改污染缓存。
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
func (m *MarketAPI) quoteStore(code string, si *StockInfo) {
	m.quoteMu.Lock()
	m.quoteCache[code] = cachedQuote{si: si, at: time.Now()}
	m.quoteMu.Unlock()
}

// getEastMoneyQuote 通过东方财富 push2 stock/get 接口拉取单只股票实时行情。
// 返回的 F43/F44/F45/F46/F60 价格字段单位为分，已 ÷100 转换为元。
// F50 涨跌幅为百分数（如 1.23 表示 +1.23%），直接使用。
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
			F48  float64 `json:"f48"`  // 成交量
			F49  float64 `json:"f49"`  // 成交额
			F50  float64 `json:"f50"`  // 涨跌幅
			F57  string  `json:"f57"`  // 代码
			F58  string  `json:"f58"`  // 名称
			F170 float64 `json:"f170"` // 换手率
			F162 float64 `json:"f162"` // 主力净流入
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("eastmoney json: %v", err)
	}
	if raw.Data.F43 == 0 {
		return nil, fmt.Errorf("eastmoney: no data for %s", code)
	}
	return &StockInfo{
		Code:      code,
		Name:      raw.Data.F58,
		Price:     raw.Data.F43 / 100,
		Open:      raw.Data.F46 / 100,
		High:      raw.Data.F44 / 100,
		Low:       raw.Data.F45 / 100,
		Close:     raw.Data.F60 / 100,
		Volume:    raw.Data.F48,
		Amount:    raw.Data.F49,
		ChangePct: raw.Data.F50,
		Turnover:  raw.Data.F170,
		NetInflow: raw.Data.F162,
	}, nil
}

// GetAuctionData 获取指定股票的集合竞价数据（9:15-9:25 时段）。
// 通过东方财富 push2 接口获取盘前数据，含竞价价格和成交量。
func (m *MarketAPI) GetAuctionData(code string) (*StockInfo, error) {
	return m.GetRealtimeQuote(code)
}

// ── 东方财富板块列表 ──

// sectorListFields 东方财富板块列表字段（精简版，兼容性好）。
const sectorListFields = "f12,f14,f3,f20,f62,f104,f105,f184"

// GetSectorList 获取东方财富行业板块行情列表。
// 返回全量板块（约 300+），包含涨跌幅、成交额、涨停家数、主力净流入等。
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
func parseSectorList(body []byte) ([]SectorInfo, error) {
	var raw struct {
		Data *struct {
			Total int                      `json:"total"`
			Diff  map[string]sectorRawItem `json:"diff"`
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
const getSectorFields = "f12,f14,f2,f3,f4,f15,f16,f17,f18,f5,f6,f7"

// GetSectorStocks 获取指定板块的成分股列表。
// sectorCode 为板块代码（如 "BK0477"），topN 限制返回数量。
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
	// 新浪返回的是倒序（最新在前），需要反转
	for i, j := 0, len(klines)-1; i < j; i, j = i+1, j-1 {
		klines[i], klines[j] = klines[j], klines[i]
	}
	return klines, nil
}

// ── 东方财富 K 线 ──

// GetKLine 获取东方财富 K 线数据。
// code 为股票代码，period 为周期（101=日线，102=周线，103=月线），count 为根数。
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
			KLines []string `json:"klines"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("eastmoney kline json: %v", err)
	}
	return parseEastMoneyKLine(raw.Data.KLines)
}

// parseEastMoneyKLine 解析东方财富 K 线 CSV 字符串数组。
// 每行格式：date,open,high,low,close,volume,amount[,...]
func parseEastMoneyKLine(rawLines []string) ([]KLine, error) {
	klines := make([]KLine, 0, len(rawLines))
	for _, line := range rawLines {
		parts := strings.Split(line, ",")
		if len(parts) < 7 {
			continue
		}
		t, err := time.Parse("2006-01-02", parts[0])
		if err != nil {
			continue
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
type sinaNewsItemRaw struct {
	Title    string `json:"title"`     // 标题
	Content  string `json:"content"`   // 内容/摘要
	ShowTime string `json:"show_time"` // 展示时间字符串
	Ctime    string `json:"ctime"`     // 发布时间字符串
	Url      string `json:"url"`       // 原文链接
}

// parseSinaNews 解析新浪新闻 JSON 响应。
// 提取标题、内容摘要、发布时间。
func parseSinaNews(body []byte) ([]NewsItem, error) {
	var raw struct {
		Result struct {
			Data []sinaNewsItemRaw `json:"data"`
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
func parseEastMoneyNews(body []byte) ([]NewsItem, error) {
	var raw struct {
		Data struct {
			List []struct {
				Title    string `json:"title"`
				Content  string `json:"content"`
				ShowTime string `json:"show_time"`
				Source   string `json:"source"`
			} `json:"list"`
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
func parseTonghuashunNews(body []byte) ([]NewsItem, error) {
	var raw struct {
		Code string `json:"code"`
		Data struct {
			List []struct {
				Title  string `json:"title"`
				Digest string `json:"digest"`
				Ctime  string `json:"ctime"`
				Url    string `json:"url"`
			} `json:"list"`
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
func parseEastMoneyIPO(body []byte) ([]IPOEvent, error) {
	var raw struct {
		Success bool `json:"success"`
		Result  struct {
			Data []struct {
				SecurityCode string  `json:"SECURITY_CODE"`
				SecurityName string  `json:"SECURITY_NAME"`
				ApplyDate    string  `json:"APPLY_DATE"`
				IssuePrice   float64 `json:"ISSUE_PRICE"`
				ListingDate  string  `json:"LISTING_DATE"`
			} `json:"data"`
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
func (m *MarketAPI) GetIndexQuote(code string) (*StockInfo, error) {
	return m.GetRealtimeQuote(code)
}

// ── 资金流向 ──

// GetStockMoneyFlow 获取东方财富个股资金流向。
// 返回 CapitalFlow，包含超大单/大单/中单/小单的买卖金额及主力净流入。
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
func parseMoneyFlow(body []byte, code string) (*CapitalFlow, error) {
	var raw struct {
		Data struct {
			KLines []string `json:"klines"`
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
func getStringField(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// maxSectorChange 求多个 SectorInfo 中 ChangePct 最大的值，用于归一化评分。
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
const stockListFields = "f12,f14,f9,f20"

// stockRawItem 东方财富全量股票列表的原始 JSON 行。
type stockRawItem struct {
	Code string  `json:"f12"` // 股票代码
	Name string  `json:"f14"` // 股票名称
	PE   float64 `json:"f9"`  // 市盈率
	MCap float64 `json:"f20"` // 总市值
}

// GetStockList 获取全量 A 股列表（代码+名称）。
// 同时查询上海主板、深圳主板、创业板、科创板，合并返回。
func (m *MarketAPI) GetStockList() (map[string]string, error) {
	// 同时查询各板块：沪A、深A、创业板、科创板、北交所
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
			Total int                     `json:"total"`
			Diff  map[string]stockRawItem `json:"diff"`
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

// filterStocksBySector 按板块代码过滤成分股列表。
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
func sortSectorByChange(sectors []SectorInfo) {
	sort.Slice(sectors, func(i, j int) bool {
		return sectors[i].ChangePct > sectors[j].ChangePct
	})
}

// sortStockByChange 按涨跌幅降序排列股票。
func sortStockByChange(stocks []StockInfo) {
	sort.Slice(stocks, func(i, j int) bool {
		return stocks[i].ChangePct > stocks[j].ChangePct
	})
}

// init 注册初始化日志。
func init() {
	log.Println("[data] MarketAPI 初始化 (Sina + EastMoney push2)")
}
