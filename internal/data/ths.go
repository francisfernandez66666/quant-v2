// Package data — 同花顺行情 API 客户端。
// 作为东方财富和 Tushare 之后的第三备用数据源，防止单一源失效。
// 提供：
//   - GetQuote: 个股实时行情（d.10jqka.com.cn）
//   - GetBoardList: 行业+概念板块列表（q.10jqka.com.cn HTML 解析）
package data

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

// THSClient 同花顺行情客户端。
// 提供个股实时行情和板块列表两种数据获取能力。
// 所有请求经 THSLimiter 限流（3 req/5s）。
type THSClient struct {
	client *http.Client
}

// thsUserAgent 同花顺请求使用的浏览器 User-Agent。
const thsUserAgent = "Mozilla/5.0 (Linux; Android 14) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.6099.144 Mobile Safari/537.36"

// thsReferer 同花顺请求 Referer。
const thsReferer = "https://q.10jqka.com.cn/"

// NewTHSClient 创建同花顺客户端，超时 10 秒。
func NewTHSClient() *THSClient {
	return &THSClient{
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// getWithHeaders 发起带浏览器头部模拟的 GET 请求。
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
var boardLinkRe = regexp.MustCompile(`/(?:gn|thshy)/detail/code/(\d+)/"\s*target="_blank">([^<]+)`)

// GetBoardList 获取同花顺行业+概念板块合并列表。
// 数据来源：
//   - 概念板块: https://q.10jqka.com.cn/gn/    (约 360 个)
//   - 行业板块: https://q.10jqka.com.cn/thshy/ (约 140 个)
//
// 两页均为 GBK 编码 HTML，通过解析 <a> 链接提取板块代码和名称。
// 同花顺板块代码格式：行业为 881xxx，概念为 308xxx。
// 本接口为公开接口，无需 API Token，适合作为板块数据的末位兜底源。
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

// getBoardPage 解析单个同花顺板块列表页，提取板块代码和名称。
// url 为页面地址（如 https://q.10jqka.com.cn/gn/）。
// 页面编码为 GBK，自动解码为 UTF-8 后解析。
func (tc *THSClient) getBoardPage(url string) ([]SectorInfo, error) {
	THSLimiter.Wait()
	resp, err := tc.getWithHeaders(url)
	if err != nil {
		return nil, fmt.Errorf("http get: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %v", err)
	}

	// GBK → UTF-8 解码
	text := string(body)
	if !utf8.Valid(body) {
		decoded, _, err := transform.String(simplifiedchinese.GBK.NewDecoder(), text)
		if err == nil {
			text = decoded
		}
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

// GetQuote 获取同花顺实时行情。
// code 为股票代码（如 "600519"），自动处理沪/深前缀。
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
func thsSecID(code string) string {
	if strings.HasPrefix(code, "6") || strings.HasPrefix(code, "5") {
		return "1." + code
	}
	return "0." + code
}

// thsQuoteRaw 同花顺行情 JSON 响应结构（备用，实际解析用更松散的 []interface{}）。
type thsQuoteRaw struct {
	Items map[string]struct {
		Code      string  `json:"code"`
		Name      string  `json:"name"`
		Price     float64 `json:"price"`
		High      float64 `json:"high"`
		Low       float64 `json:"low"`
		Open      float64 `json:"open"`
		Volume    float64 `json:"volume"`
		Amount    float64 `json:"amount"`
		ChangePct float64 `json:"change_pct"`
	} `json:"items"`
}

// parseTHSQuote 解析同花顺实时行情响应体。
// 响应格式为 JavaScript 填充 JSON（JSONP），需先提取 {} 部分再反序列化。
// 数据以 map[string][]interface{} 形式返回，按数组索引读取各字段。
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
			Items map[string][]interface{} `json:"items"`
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
		if si.Price > 0 {
			return si, nil
		}
	}

	return nil, fmt.Errorf("ths: no data for %s", code)
}
