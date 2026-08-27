// Package data — 同花顺行情 API 客户端。
// 作为东方财富和 Tushare 之后的第三备用数据源，防止单一源失效。
// 提供：
//   - GetQuote: 个股实时行情（d.10jqka.com.cn）
//   - GetBoardList: 行业+概念板块列表（q.10jqka.com.cn HTML 解析）
//   - GetTopBoards: 板块行情表首屏 top-20（含涨跌幅/主力净流入，同花顺出口）
//
// Package data — the Tonghuashun (THS) market API client.
// It is the third backup source after EastMoney and Tushare, providing:
//   - GetQuote: per-stock realtime quote (d.10jqka.com.cn)
//   - GetBoardList: industry+concept board list (q.10jqka.com.cn HTML parsing)
//   - GetTopBoards: top-20 board quote table with change pct / main-capital inflow
package data

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

// THSClient 同花顺行情客户端。
// 提供个股实时行情和板块列表两种数据获取能力。
// 所有请求经 THSLimiter 限流（3 req/5s）。
// THSClient is the Tonghuashun quote client, fetching realtime quotes and
// board lists; all requests are rate-limited by THSLimiter (3 req/5s).
type THSClient struct {
	client *http.Client // 底层 HTTP 客户端（默认超时 10s，可经 SetTransport 替换）
}

// thsUserAgent 同花顺请求使用的浏览器 User-Agent。
// thsUserAgent is the browser User-Agent used for THS requests.
const thsUserAgent = "Mozilla/5.0 (Linux; Android 14) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.6099.144 Mobile Safari/537.36"

// thsReferer 同花顺请求 Referer。
// thsReferer is the Referer used for THS requests.
const thsReferer = "https://q.10jqka.com.cn/"

// NewTHSClient 创建同花顺客户端，超时 10 秒。
// NewTHSClient creates a THS client with a 10s HTTP timeout.
func NewTHSClient() *THSClient {
	return &THSClient{
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// SetTransport 替换底层 HTTP Transport（测试注入 mock 网络）。
// SetTransport swaps the underlying HTTP Transport for test mock injection.
func (tc *THSClient) SetTransport(rt http.RoundTripper) {
	tc.client.Transport = rt
}

// HealthCheck 探测 THS 服务是否可达。
// （HealthCheck probes whether the THS service is reachable.）
func (tc *THSClient) HealthCheck() bool {
	// 简单探测：通过 HEAD 请求检查探针 URL
	// Simple probe: check reachability via HEAD request to probe URL
	u := "https://d.10jqka.com.cn"
	req, err := http.NewRequest("HEAD", u, nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", thsUserAgent)
	req.Header.Set("Referer", thsReferer)
	resp, err := tc.client.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	// 只要服务响应（HTTP 状态码在 200-599 范围）即视为可达；
	// 同花顺根路径可能返回 404/405，但连接建立成功说明源可用。
	return resp.StatusCode > 0
}

// getWithHeaders 发起带浏览器头部模拟的 GET 请求。
// getWithHeaders issues a GET request with simulated browser headers.
func (tc *THSClient) getWithHeaders(url string) (*http.Response, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", thsUserAgent)
	req.Header.Set("Referer", thsReferer)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	return tc.client.Do(req)
}

// boardLinkRe 匹配同花顺板块列表页中的板块链接。
// 格式: <a href=".../gn/detail/code/{code}/" target="_blank">{name}</a>
// 或:   <a href=".../thshy/detail/code/{code}/" target="_blank">{name}</a>
// code 为纯数字，name 为板块中文名。
// boardLinkRe matches board links in the THS board pages, e.g.
// <a href=".../gn/detail/code/{code}/" target="_blank">{name}</a>.
var boardLinkRe = regexp.MustCompile(`/(?:gn|thshy)/detail/code/(\d+)/"\s*target="_blank">([^<]+)`)

// GetBoardListRaw 返回同花顺板块页解码后的原始 HTML（行业+概念），供测试 fixture 抓取。
// GetBoardListRaw returns the decoded raw HTML of the industry and concept board
// pages, mainly for capturing test fixtures.
func (tc *THSClient) GetBoardListRaw() (map[string]string, error) {
	ind, err := tc.fetchDecoded("https://q.10jqka.com.cn/thshy/")
	if err != nil {
		return nil, err
	}
	con, err := tc.fetchDecoded("https://q.10jqka.com.cn/gn/")
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"https://q.10jqka.com.cn/thshy/": ind,
		"https://q.10jqka.com.cn/gn/":    con,
	}, nil
}

// GetBoardList 获取同花顺行业+概念板块合并列表。
// 数据来源：
//   - 概念板块: https://q.10jqka.com.cn/gn/    (约 360 个)
//   - 行业板块: https://q.10jqka.com.cn/thshy/ (约 140 个)
//
// 两页均为 GBK 编码 HTML，通过解析 <a> 链接提取板块代码和名称。
// 同花顺板块代码格式：行业为 881xxx，概念为 308xxx。
// 本接口为公开接口，无需 API Token，适合作为板块数据的末位兜底源。
// GetBoardList returns the merged THS industry+concept board list parsed from
// two GBK HTML pages; open API without a token, ideal as a last-resort source.
func (tc *THSClient) GetBoardList() ([]SectorInfo, error) {
	// 概念板块
	concepts, err := tc.getBoardPage("https://q.10jqka.com.cn/gn/")
	if err != nil {
		return nil, fmt.Errorf("ths concept board: %v", err)
	}
	// 行业板块
	industries, err := tc.getBoardPage("https://q.10jqka.com.cn/thshy/")
	if err != nil {
		return nil, fmt.Errorf("ths industry board: %v", err)
	}
	// 合并，行业板块在前
	all := make([]SectorInfo, 0, len(industries)+len(concepts))
	all = append(all, industries...)
	all = append(all, concepts...)
	return all, nil
}

// fetchDecoded 发起请求并自动将 GBK 响应解码为 UTF-8 文本。
// fetchDecoded issues a request and decodes a GBK response into UTF-8 text.
func (tc *THSClient) fetchDecoded(url string) (string, error) {
	THSLimiter.Wait()
	resp, err := tc.getWithHeaders(url)
	if err != nil {
		return "", fmt.Errorf("http get: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read body: %v", err)
	}

	text := string(body)
	if !utf8.Valid(body) {
		decoded, _, err := transform.String(simplifiedchinese.GBK.NewDecoder(), text)
		if err == nil {
			text = decoded
		}
	}
	return text, nil
}

// getBoardPage 解析单个同花顺板块列表页，提取板块代码和名称。
// url 为页面地址（如 https://q.10jqka.com.cn/gn/）。
// 页面编码为 GBK，自动解码为 UTF-8 后解析。
// getBoardPage parses one THS board page (GBK, auto-decoded to UTF-8)
// and extracts board codes and names.
func (tc *THSClient) getBoardPage(url string) ([]SectorInfo, error) {
	text, err := tc.fetchDecoded(url)
	if err != nil {
		return nil, err
	}

	// 正则提取所有板块链接
	matches := boardLinkRe.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return nil, fmt.Errorf("no board links found in %s", url)
	}

	seen := make(map[string]bool)
	result := make([]SectorInfo, 0, len(matches))
	for _, m := range matches {
		code := m[1]
		name := strings.TrimSpace(m[2])
		if code == "" || name == "" || seen[code] {
			continue
		}
		seen[code] = true
		result = append(result, SectorInfo{
			Code: code,
			Name: name,
		})
	}
	return result, nil
}

// boardStockLinkRe 匹配同花顺板块成分股页面中的个股链接。
// 格式: <a ... href="http://stockpage.10jqka.com.cn/{code}/" ...>
// boardStockLinkRe matches per-stock links in a THS board-constituent page.
var boardStockLinkRe = regexp.MustCompile(`stockpage\.10jqka\.com\.cn/(\d{6})`)

// GetBoardStocks 获取同花顺板块成分股列表（东财接口故障时的降级源）。
// 板块代码为同花顺格式：行业板块 881xxx 走 /thshy/detail/code/，概念板块 308xxx 走 /gn/detail/code/；
// 页面为 GBK 服务端渲染，成分股以 stockpage.10jqka.com.cn/{code}/ 链接形式出现，每页 10 只，
// 按 page/{n}/ 分页拉取直至满足 topN。仅返回代码/名称（无行情字段），调用方自行补全实时行情。
// GetBoardStocks returns a THS board's constituent stock codes (fallback when EastMoney
// sector-stocks is down). THS board codes: industry 881xxx → /thshy/detail/code/, concept
// 308xxx → /gn/detail/code/. Pages are GBK server-rendered, constituents appear as
// stockpage links (10/page), paginated via page/{n}/ until topN is met. Only code/name are
// filled; live quotes are backfilled by the caller.
func (tc *THSClient) GetBoardStocks(boardCode string, topN int) ([]StockInfo, error) {
	if topN <= 0 {
		topN = 10
	}
	base := "https://q.10jqka.com.cn/gn/detail/code/" + boardCode
	if strings.HasPrefix(boardCode, "881") || strings.HasPrefix(boardCode, "885") {
		base = "https://q.10jqka.com.cn/thshy/detail/code/" + boardCode
	}
	var out []StockInfo
	seen := make(map[string]bool)
	// 最多拉 10 页（100 只），超过即止（板块成分通常远小于此）
	for page := 1; page <= 10 && len(out) < topN; page++ {
		url := base
		if page > 1 {
			url = fmt.Sprintf("%s/page/%d/", base, page)
		}
		text, err := tc.fetchDecoded(url)
		if err != nil {
			if len(out) > 0 {
				break // 后续页失败但已有数据 → 返回已取部分
			}
			return nil, err
		}
		matches := boardStockLinkRe.FindAllStringSubmatch(text, -1)
		if len(matches) == 0 {
			break // 无新链接 → 到底了
		}
		for _, m := range matches {
			code := m[1]
			if code == "" || seen[code] {
				continue
			}
			seen[code] = true
			out = append(out, StockInfo{Code: code})
			if len(out) >= topN {
				break
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("ths board %s: no constituents found", boardCode)
	}
	return out, nil
}

// topBoardTableRe 匹配同花顺板块列表页中带行情数据的表格。
// 首屏表（按涨跌幅排序的前 20 名）为服务端渲染，无分页反爬，可直接解析。
// The regexes below parse the top-board table; the first-screen table
// (top 20 by change pct) is server-rendered and directly parseable.
var (
	topBoardTbodyRe = regexp.MustCompile(`(?s)<tbody>(.*?)</tbody>`) // 匹配 <tbody> 块
	topBoardTrRe    = regexp.MustCompile(`(?s)<tr>(.*?)</tr>`)       // 匹配表格行
	topBoardTdRe    = regexp.MustCompile(`(?s)<td[^>]*>(.*?)</td>`)  // 匹配表格单元格
	topBoardCodeRe  = regexp.MustCompile(`/detail/code/(\d+)/`)      // 从行内链接提取板块代码
	topBoardStripRe = regexp.MustCompile(`<[^>]+>`)                  // 去除单元格内的残留 HTML 标签
)

// GetTopBoards 获取同花顺板块行情表（首屏 top-20 按涨跌幅排序）。
// 一级行业(https://q.10jqka.com.cn/thshy/) + 概念(https://q.10jqka.com.cn/gn/) 各一页。
// 表格列：序号/板块/涨跌幅(%)/总成交额(亿元)/主力净流入(亿元)/上涨家数/下跌家数/领涨股等。
// 主力净流入按东财口径转换为元（亿×1e8），供前端 /1e8 还原。
// GetTopBoards returns the top-20 boards (industry + concept) with change pct
// and main-capital inflow; inflow is converted to yuan (亿×1e8) like EastMoney.
func (tc *THSClient) GetTopBoards() ([]SectorInfo, error) {
	ind, indErr := tc.getTopBoardPage("https://q.10jqka.com.cn/thshy/")
	con, conErr := tc.getTopBoardPage("https://q.10jqka.com.cn/gn/")
	if indErr != nil && conErr != nil {
		return nil, fmt.Errorf("ths top boards: industry=%v concept=%v", indErr, conErr)
	}
	out := make([]SectorInfo, 0, len(ind)+len(con))
	out = append(out, ind...)
	out = append(out, con...)
	if len(out) == 0 {
		return nil, fmt.Errorf("ths top boards: 空结果 (industry=%v concept=%v)", indErr, conErr)
	}
	return out, nil
}

// getTopBoardPage 解析单个同花顺板块列表页首屏表格，提取代码/名称/涨跌幅/主力净流入。
// getTopBoardPage parses the top-board table of one THS page, extracting
// code/name/change-pct and main-capital inflow.
func (tc *THSClient) getTopBoardPage(url string) ([]SectorInfo, error) {
	text, err := tc.fetchDecoded(url)
	if err != nil {
		return nil, err
	}

	var out []SectorInfo
	for _, tbody := range topBoardTbodyRe.FindAllStringSubmatch(text, -1) {
		for _, tr := range topBoardTrRe.FindAllStringSubmatch(tbody[1], -1) {
			var cells []string
			for _, td := range topBoardTdRe.FindAllStringSubmatch(tr[1], -1) {
				c := strings.TrimSpace(topBoardStripRe.ReplaceAllString(td[1], ""))
				cells = append(cells, c)
			}
			if len(cells) < 6 {
				continue
			}
			cm := topBoardCodeRe.FindStringSubmatch(tr[1])
			if len(cm) != 2 {
				continue
			}
			chg, _ := strconv.ParseFloat(cells[2], 64)
			inflow, _ := strconv.ParseFloat(cells[5], 64)
			out = append(out, SectorInfo{
				Code:      cm[1],
				Name:      cells[1],
				ChangePct: chg,
				NetInflow: inflow * 1e8,
			})
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no board table found in %s", url)
	}
	return out, nil
}

// GetQuote 获取同花顺实时行情。
// code 为股票代码（如 "600519"），自动处理沪/深前缀。
// GetQuote fetches a THS realtime quote for a code like "600519",
// automatically handling the exchange prefix.
func (tc *THSClient) GetQuote(code string) (*StockInfo, error) {
	code = strings.TrimSpace(code)
	secID := thsSecID(code)
	url := fmt.Sprintf("https://d.10jqka.com.cn/v2/realhead/hs_%s/last.js", secID)

	THSLimiter.Wait()
	resp, err := tc.getWithHeaders(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	return parseTHSQuote(body, code)
}

// thsSecID 将股票代码转换为同花顺证券 ID 格式。
// 沪市（6/5 开头）加 "1." 前缀，深市加 "0." 前缀。
// thsSecID converts a stock code to the THS security-id format:
// "1." prefix for Shanghai (6/5), "0." for Shenzhen.
func thsSecID(code string) string {
	if strings.HasPrefix(code, "6") || strings.HasPrefix(code, "5") {
		return "1." + code
	}
	return "0." + code
}

// parseTHSLine 解析同花顺 K 线 JSONP 响应。
// 响应为 `quotebridge_v6_line_..._last({...})` 联牌格式，内部 data 为 K 线 CSV 字符串数组
// （行内字段同东财：[date,open,high,low,close,volume,amount]）。
// 采用松散提取：先剥 JSONP 括号，再取 data 数组，逐行严格校验数值，脏行直接跳过，
// 无有效行时返回错误（调用方据此降级到下一源）。isMinute 为 true 时时间含 "HH:MM"。
// parseTHSLine parses a THS JSONP K-line response, loosely unwrapping the JSONP
// wrapper and validating each row; dirty rows are skipped and an error is returned
// when no valid rows remain so callers can degrade to the next source.
func parseTHSLine(body []byte, isMinute bool) ([]KLine, error) {
	text := strings.TrimSpace(string(body))
	// 剥 JSONP 包裹：e.g. quotebridge_v6_line_hs_1.600206_01_last({...})
	if i := strings.Index(text, "("); i >= 0 {
		text = text[i+1:]
	}
	if i := strings.LastIndex(text, ")"); i >= 0 {
		text = text[:i]
	}
	var wrapper struct {
		Data []string `json:"data"`
	}
	if err := json.Unmarshal([]byte(text), &wrapper); err != nil || len(wrapper.Data) == 0 {
		return nil, fmt.Errorf("ths kline json: no data")
	}
	klines := make([]KLine, 0, len(wrapper.Data))
	for _, line := range wrapper.Data {
		parts := strings.Split(line, ",")
		if len(parts) < 7 {
			continue
		}
		var t time.Time
		var err error
		if isMinute {
			if len(parts[0]) >= 15 {
				// "yyyyMMddHHmmss" 或 "yyyyMMdd HH:MM"
				t, err = time.ParseInLocation("20060102150405", parts[0], time.Local)
				if err != nil {
					t, err = time.ParseInLocation("20060102 15:04", parts[0], time.Local)
				}
			} else {
				t, err = time.Parse("2006-01-02", parts[0])
			}
		} else {
			t, err = time.Parse("2006-01-02", parts[0])
			if err != nil {
				t, err = time.Parse("20060102", parts[0])
			}
		}
		if err != nil {
			continue
		}
		open := toFloat64(parts[1])
		high := toFloat64(parts[2])
		low := toFloat64(parts[3])
		close := toFloat64(parts[4])
		volume := toFloat64(parts[5])
		if open <= 0 || high <= 0 || low <= 0 || close <= 0 {
			continue
		}
		if high < open || high < close || high < low || low > open || low > close {
			continue
		}
		klines = append(klines, KLine{
			Date:   t,
			Open:   open,
			High:   high,
			Low:    low,
			Close:  close,
			Volume: volume,
			Amount: toFloat64(parts[6]),
		})
	}
	if len(klines) == 0 {
		return nil, fmt.Errorf("ths line: no valid rows")
	}
	return klines, nil
}

// GetTHSKLine 获取同花顺日 K 线（best-effort，作为降级链第二源）。
// 走 d.10jqka.com.cn/v6/line/hs_{secid}/01/last.js。解析失败/空返回错误，由上层降级。
// GetTHSKLine fetches THS daily K-lines (best-effort, second source in the chain);
// parse failures/empty results return errors so the caller can fall back.
func (tc *THSClient) GetTHSKLine(code string) ([]KLine, error) {
	url := fmt.Sprintf("https://d.10jqka.com.cn/v6/line/hs_%s/01/last.js", thsSecID(code))
	THSLimiter.Wait()
	resp, err := tc.getWithHeaders(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return parseTHSLine(body, false)
}

// GetTHSMinuteKLine 获取同花顺分钟 K 线（best-effort，作为降级链第二源）。
// 走 d.10jqka.com.cn/v6/line/hs_{secid}/06/last.js（06=分钟线）。
// GetTHSMinuteKLine fetches THS minute K-lines (best-effort second source)
// via the 06 (minute) endpoint.
func (tc *THSClient) GetTHSMinuteKLine(code string) ([]KLine, error) {
	url := fmt.Sprintf("https://d.10jqka.com.cn/v6/line/hs_%s/06/last.js", thsSecID(code))
	THSLimiter.Wait()
	resp, err := tc.getWithHeaders(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return parseTHSLine(body, true)
}

// thsQuoteRaw 同花顺行情 JSON 响应结构（备用，实际解析用更松散的 []interface{}）。
// thsQuoteRaw is the typed form of the THS quote response (backup; actual parsing
// uses a looser []interface{} form).
type thsQuoteRaw struct {
	Items map[string]struct {
		Code      string  `json:"code"`       // 股票代码
		Name      string  `json:"name"`       // 股票名称
		Price     float64 `json:"price"`      // 最新价
		High      float64 `json:"high"`       // 最高价
		Low       float64 `json:"low"`        // 最低价
		Open      float64 `json:"open"`       // 开盘价
		Volume    float64 `json:"volume"`     // 成交量
		Amount    float64 `json:"amount"`     // 成交额
		ChangePct float64 `json:"change_pct"` // 涨跌幅（%）
	} `json:"items"`
}

// parseTHSQuote 解析同花顺实时行情响应体。
// 响应格式为 JavaScript 填充 JSON（JSONP），需先提取 {} 部分再反序列化。
// 数据以 map[string][]interface{} 形式返回，按数组索引读取各字段。
// parseTHSQuote parses the THS realtime quote response (JSONP): it extracts the
// {} object and reads fields from each symbol's array by index.
func parseTHSQuote(body []byte, code string) (*StockInfo, error) {
	text := string(body)
	idx := strings.Index(text, "{")
	if idx < 0 {
		return nil, fmt.Errorf("ths: no json in response")
	}
	text = text[idx:]
	idx = strings.LastIndex(text, "}")
	if idx < 0 {
		return nil, fmt.Errorf("ths: no closing brace")
	}
	text = text[:idx+1]

	var raw struct {
		Data struct {
			Items map[string][]interface{} `json:"items"` // 个股数组，key 为证券ID
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		return nil, fmt.Errorf("ths json: %v", err)
	}

	for _, arr := range raw.Data.Items {
		if len(arr) < 10 {
			continue
		}
		c, _ := arr[1].(string)
		if c == "" {
			continue
		}
		// 清理代码前缀："hs_1.600519" → "600519"
		c = strings.TrimPrefix(c, "hs_")
		c = strings.ReplaceAll(c, "1.", "")
		c = strings.ReplaceAll(c, "0.", "")
		// §R3-2 P0-D4 越界防御：清理后不足 6 位（脏数据/上游格式变化）直接跳过，
		// 此前 c[len(c)-6:] 对短串会 slice bounds out of range panic 杀死整轮解析。
		if len(c) < 6 {
			continue
		}
		if !strings.HasSuffix(code, c[len(c)-6:]) {
			continue
		}
		si := &StockInfo{
			Code: code,
		}
		// 数组索引约定：[..., code, name, open, high, low, price, volume, amount, ...]
		if len(arr) > 2 {
			si.Name, _ = arr[2].(string)
		}
		if len(arr) > 3 {
			if v, ok := arr[3].(float64); ok {
				si.Open = v
			}
		}
		if len(arr) > 4 {
			if v, ok := arr[4].(float64); ok {
				si.High = v
			}
		}
		if len(arr) > 5 {
			if v, ok := arr[5].(float64); ok {
				si.Low = v
			}
		}
		if len(arr) > 6 {
			if v, ok := arr[6].(float64); ok {
				si.Price = v
			}
		}
		if len(arr) > 7 {
			if v, ok := arr[7].(float64); ok {
				si.Volume = v
			}
		}
		if len(arr) > 8 {
			if v, ok := arr[8].(float64); ok {
				si.Amount = v
			}
		}
		// 昨收（同花顺 realhead 数组中多数版本位于索引 9）：
		// 仅在确实存在且数值合理时用于推算涨跌幅，避免猜测错误索引污染现有字段。
		if len(arr) > 9 {
			if v, ok := arr[9].(float64); ok && v > 0 && si.Price > 0 {
				ratio := si.Price / v
				if ratio > 0.5 && ratio < 5 {
					si.Close = v
					si.ChangePct = (si.Price - v) / v * 100
				}
			}
		}
		if si.Price > 0 {
			return si, nil
		}
	}

	return nil, fmt.Errorf("ths: no data for %s", code)
}
