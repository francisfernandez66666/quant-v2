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
	"errors"
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
// §2026-09-14 WAF 实录：原 Android 移动 UA 被 q.10jqka 的 Nginx 防火墙整段拉黑
// （返回 "Nginx forbidden."，板块名单/行情接口全挂），桌面 Chrome UA 正常放行——
// 固定用桌面主流浏览器指纹，勿改回移动端。
// thsUserAgent is the browser User-Agent used for THS requests; the THS WAF blocks
// mobile fingerprints (2026-09-14 incident), keep a desktop Chrome UA.
const thsUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

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

	// 同花顺老接口多按 GBK 回包：先校验是否已是合法 UTF-8，非法才走 GBK 解码；
	// 解码失败时保留原始文本，宁可少解析几列也不要整页丢弃。
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

	// 去重收集板块链接：空代码/空名称/重复代码一律跳过。
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
		// 去重收集成分股代码，到达 topN 上限即停止分页。
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

	// 解析榜单页：逐 tbody/tr/td 提取单元格，少于 6 列或取不到代码则跳过该行。
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
			// 单元格约定：0序号 1名称 2涨跌幅 ... 5主力净流入。
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

// 同花顺 v6 K 线 scale 码（全表实测钉死 2026-09-20，样本 300489，穷举 00..99）。
// 有效（每档另有 +1/+2 别名，含义相同）：
//
//	0x=日线  1x=周线  2x=月线  3x=5分钟  4x=30分钟  5x=60分钟  6x=1分钟
//	7x=日线快照(带时间戳)  8x=年线  9x=季线
//
// 无效（上游 404）：x3..x9 全部。**无 15 分钟档**。
// THS v6 K-line scale codes, exhaustively measured 2026-09-20 over 00..99 (each has +1/+2 aliases).
// There is no 15-minute scale.
const (
	thsScaleDaily  = "01" // 日线
	thsScaleOneMin = "60" // 1 分钟
	thsScale5Min   = "30" // 5 分钟
	thsScale30Min  = "40" // 30 分钟
	thsScale60Min  = "50" // 60 分钟
)

// ErrTHSUnsupportedPeriod 表示"该周期同花顺不提供"——**客户端能力缺失**，不是供应商故障。
// §修复 THS-BREAKER(20260920)：调用方必须用 errors.Is 区分出来并**跳过熔断**，
// 否则一个 15 分钟周期请求就会把同花顺整源按"故障"关掉 60s（连带报价/板块一起失效）。
// English: unsupported period is a client-side capability miss, NOT a provider outage;
// callers must detect it via errors.Is and must NOT trip the breaker.
var ErrTHSUnsupportedPeriod = errors.New("ths: unsupported intraday period")

// thsIntradayScale 把"分钟周期"映射为同花顺 scale 码；不支持的周期返回 false。
// §2026-09-20：不支持的周期必须让调用方降级，**绝不拿别的周期顶替**——
// 5 分钟 MACD 与 1 分钟 MACD 数值完全不同，静默替换等于给出错误指标。
// thsIntradayScale maps a period in minutes to the THS scale code; unsupported periods
// return false so the caller degrades instead of silently substituting another period.
func thsIntradayScale(minutes int) (string, bool) {
	switch minutes {
	case 1:
		return thsScaleOneMin, true
	case 5:
		return thsScale5Min, true
	case 30:
		return thsScale30Min, true
	case 60:
		return thsScale60Min, true
	}
	return "", false
}

// thsRealheadURL 同花顺 realhead 实时行情 URL。
// §修复 THS-URL(20260920)：真实路径为 hs_{6 位代码}，**不带市场前缀**。
// 旧实现经 thsSecID 生成 hs_0.300489 / hs_1.600519 → 上游一律 404（实测 2026-09-20），
// 这个"同花顺兜底"因此从未真正生效（且失败后还会触发 60s 熔断，把后续尝试一并挡掉）。
// thsRealheadURL builds the THS realhead quote URL: hs_{code} with no market prefix.
// The old build produced hs_{marketid}.{code}, which upstream answers with 404, so this
// fallback source never actually worked.
func thsRealheadURL(code string) string {
	return fmt.Sprintf("https://d.10jqka.com.cn/v2/realhead/hs_%s/last.js", stripSuffix(strings.TrimSpace(code)))
}

// thsLineURL 同花顺 v6 K 线 URL（scale 见下表）。
// §修复 THS-URL(20260920)：与 realhead 同病——路径用 hs_{6 位代码}，带市场前缀会 404/504。
// 实测 scale 语义（2026-09-20，300489）：01/00=日线 10=周线 20=月线 40=30分钟 50=60分钟
// 60=1分钟；03/04/06/11/12/13/15 上游返回 502（无效值）。
// thsLineURL builds the THS v6 K-line URL. Measured scale codes (2026-09-20): 01/00 daily,
// 10 weekly, 20 monthly, 40 30-min, 50 60-min, 60 1-min; 03/04/06/11/12/13/15 return 502.
func thsLineURL(code, scale string) string {
	return fmt.Sprintf("https://d.10jqka.com.cn/v6/line/hs_%s/%s/last.js", stripSuffix(strings.TrimSpace(code)), scale)
}

// GetQuote 获取同花顺实时行情。
// code 为股票代码（如 "600519"，可带 .SH/.SZ 后缀，内部剥离）。
// GetQuote fetches a THS realtime quote for a code like "600519"
// (an exchange suffix is stripped internally).
func (tc *THSClient) GetQuote(code string) (*StockInfo, error) {
	code = stripSuffix(strings.TrimSpace(code))
	url := thsRealheadURL(code)

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

// thsLineRowFields 同花顺 K 线单行的最小字段数。
// 实测行形如：20260302,43.45,43.72,42.55,42.68,2660990,114529247.00,1.940,,0.00,0
// 即 [日期,开,高,低,收,成交量(股),成交额(元),换手率(%),…] 共 11 列；取前 7 列即足够。
// thsLineRowFields is the minimum column count of one THS K-line row.
const thsLineRowFields = 7

// thsSplitLineRows 从 K 线响应的 data 字段取出逐行字符串。
// §实测（2026-09-20）：data 为单条 JSON **字符串**，行间以 ";" 分隔、行内以 "," 分隔。
// 为兼容上游改型，同时接受 JSON 数组形态；两种都拿不到行时返回错误（调用方降级）。
// thsSplitLineRows extracts the per-row strings from the K-line `data` field: in practice a
// single JSON string with ";" between rows (a JSON array is also tolerated).
func thsSplitLineRows(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("ths kline: data missing")
	}
	// 主形态：JSON 字符串 → 按 ";" 切行。
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		rows := make([]string, 0, 256)
		for _, line := range strings.Split(s, ";") {
			if line = strings.TrimSpace(line); line != "" {
				rows = append(rows, line)
			}
		}
		if len(rows) == 0 {
			return nil, fmt.Errorf("ths kline: data empty")
		}
		return rows, nil
	}
	// 兼容形态：JSON 数组。
	var arr []string
	if err := json.Unmarshal(raw, &arr); err == nil && len(arr) > 0 {
		return arr, nil
	}
	return nil, fmt.Errorf("ths kline: data not parseable")
}

// parseTHSLineTime 解析同花顺 K 线的时间列。
// 实测格式：日/周/月线为 `20060102`；分钟线（scale 40/50/60）为 `200601021504`（12 位）。
// 两种都按中国时区 cst 解析，与东财/新浪口径一致，消灭 8 小时错位。
// parseTHSLineTime parses the THS K-line time column (daily: yyyyMMdd; intraday:
// yyyyMMddHHmm), always in China Standard Time to match the EastMoney/Sina convention.
func parseTHSLineTime(col string, isMinute bool) (time.Time, bool) {
	col = strings.TrimSpace(col)
	// 先按含连字符的宽松格式试一次，再看是否分钟级长格式。
	for _, layout := range []string{"2006-01-02 15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, col, cst); err == nil {
			return t, true
		}
	}
	if isMinute {
		for _, layout := range []string{"200601021504", "20060102150405"} {
			if t, err := time.ParseInLocation(layout, col, cst); err == nil {
				return t, true
			}
		}
	}
	if t, err := time.ParseInLocation("20060102", col, cst); err == nil {
		return t, true
	}
	return time.Time{}, false
}

// parseTHSLine 解析同花顺 K 线 JSONP 响应。
// 响应为 `quotebridge_v6_line_hs_{code}_{scale}_last({...})` 联牌格式。
// §修复 THS-KLINE(20260920)：真实 `data` 是**以 ";" 分隔的单条字符串**（不是 JSON 数组），
// 旧实现按 `[]string` 反序列化 → 恒定报 "no data"，同花顺 K 线降级源因此从未生效。
// 逐行严格校验数值，脏行直接跳过，无有效行时返回错误（调用方据此降级到下一源）。
// 时间格式：日/周/月线为 `yyyyMMdd`；分钟线为 `yyyyMMddHHmm`（实测 scale 60 即此形）。
// parseTHSLine parses a THS JSONP K-line response. The real payload's `data` is a single
// ";"-separated string, not a JSON array — the old `[]string` unmarshal therefore always
// failed, so this THS K-line fallback never actually worked.
func parseTHSLine(body []byte, isMinute bool) ([]KLine, error) {
	text := strings.TrimSpace(string(body))
	// 剥 JSONP 包裹：e.g. quotebridge_v6_line_hs_300489_01_last({...})
	if i := strings.Index(text, "("); i >= 0 {
		text = text[i+1:]
	}
	if i := strings.LastIndex(text, ")"); i >= 0 {
		text = text[:i]
	}
	// data 兼容两种形态：主形态为字符串（真实现状），另容忍数组（上游若改型不至于整源失能）。
	var wrapper struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal([]byte(text), &wrapper); err != nil {
		return nil, fmt.Errorf("ths kline json: %v", err)
	}
	rows, err := thsSplitLineRows(wrapper.Data)
	if err != nil {
		return nil, err
	}

	// 逐行解析 CSV → K线结构：字段数不足/数值解析失败的脏行直接跳过（不因一行坏数据废整段历史）。
	klines := make([]KLine, 0, len(rows))
	for _, line := range rows {
		parts := strings.Split(line, ",")
		if len(parts) < thsLineRowFields {
			continue
		}
		t, ok := parseTHSLineTime(parts[0], isMinute)
		if !ok {
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
// 走 d.10jqka.com.cn/v6/line/hs_{code}/01/last.js（scale 01=日线）。
// 解析失败/空返回错误，由上层降级。
// GetTHSKLine fetches THS daily K-lines (best-effort, second source in the chain);
// parse failures/empty results return errors so the caller can fall back.
func (tc *THSClient) GetTHSKLine(code string) ([]KLine, error) {
	url := thsLineURL(code, thsScaleDaily)
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

// GetTHSMinuteKLine 获取同花顺分钟 K 线（best-effort，作为降级链中段源）。
// scale 为**分钟周期**（1/5/30/60），内部映射到同花顺 scale 码；不支持的周期（如 15）
// 直接返回错误由调用方降级——宁可降级到别的源，也不拿不匹配的周期冒充。
// §修复 THS-KLINE(20260920)：旧实现写死 scale 06（上游 404/502 的无效值）且忽略调用方周期，
// 该源从未生效；同时它若"修好"成固定取 1 分钟，会被误当 5 分钟喂给 MACD —— 故一并接入周期映射。
// GetTHSMinuteKLine fetches THS minute K-lines; scale is a period in MINUTES (1/5/30/60) mapped
// to THS scale codes internally. Unsupported periods error out so the caller degrades rather
// than substituting a mismatched period.
func (tc *THSClient) GetTHSMinuteKLine(code string, scale int) ([]KLine, error) {
	sc, ok := thsIntradayScale(scale)
	if !ok {
		// 包一层 sentinel：调用方据此判定"客户端能力缺失"，不得按供应商故障熔断。
		return nil, fmt.Errorf("%w: %d 分钟（支持 1/5/30/60）", ErrTHSUnsupportedPeriod, scale)
	}
	url := thsLineURL(code, sc)
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

// 同花顺 realhead 字段 id。
// §实测钉死（2026-09-20）：300489 与 600519 两只样本的 14 个字段与腾讯行情逐项交叉验证一致
// （现价/昨收/今开/最高/最低/成交量/成交额/涨跌幅/换手率/涨跌额/振幅/流通市值/总市值/量比）。
// 注意：这些是**同花顺自有的无规律数字 id**，不是位序数组下标——旧解析器按位序读，因而恒失败。
// THS realhead field ids, verified 2026-09-20 against Tencent quotes on two independent samples.
// These are THS-specific opaque numeric ids, NOT positional array indexes.
const (
	thsFCode      = "5"       // 6 位证券代码
	thsFPrevClose = "6"       // 昨收（元）
	thsFOpen      = "7"       // 今开（元）
	thsFHigh      = "8"       // 最高（元）
	thsFLow       = "9"       // 最低（元）
	thsFPrice     = "10"      // 现价（元）
	thsFVolume    = "13"      // 成交量（股）
	thsFAmount    = "19"      // 成交额（元）
	thsFChangePct = "199112"  // 涨跌幅（%）
	thsFTurnover  = "1968584" // 换手率（%）
	thsFName      = "name"    // 证券名称
)

// parseTHSQuote 解析同花顺 realhead 实时行情响应体。
// 响应为 JSONP（quotebridge_v2_realhead_hs_{code}_last({...})），需先剥壳。
// §修复 THS-QUOTE(20260920)：真实结构是顶层 {"items":{"<字段id>": <值>, ...}}——
// 即"单个证券的字段 id → 值"扁平字典（值为字符串），**不是** data.items 下按证券 id 索引的
// 位置数组。旧解析器按后者写（夹具也按后者造），故线上恒定报 "no data"，
// 同花顺报价源其实一直没工作过；而换手率正是靠这个源才能在东财以外拿到。
// parseTHSQuote parses the THS realhead JSONP payload. The real shape is a top-level
// {"items":{"<field id>":<value>}} flat dict for the single requested security — not a
// data.items map of positional arrays (what the old parser and the fixture both assumed).
func parseTHSQuote(body []byte, code string) (*StockInfo, error) {
	// 剥 JSONP 包裹：quotebridge_v2_realhead_hs_300489_last({...})
	text := string(body)
	if i := strings.Index(text, "{"); i >= 0 {
		text = text[i:]
	} else {
		return nil, fmt.Errorf("ths: no json in response")
	}
	if j := strings.LastIndex(text, "}"); j >= 0 {
		text = text[:j+1]
	} else {
		return nil, fmt.Errorf("ths: no closing brace")
	}

	var raw struct {
		Items map[string]interface{} `json:"items"`
	}
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		return nil, fmt.Errorf("ths json: %v", err)
	}
	if len(raw.Items) == 0 {
		return nil, fmt.Errorf("ths: no items for %s", code)
	}

	// 取值辅助：字段值实际为字符串，但容忍上游改回数值型。
	num := func(id string) float64 {
		switch v := raw.Items[id].(type) {
		case string:
			return toFloat64(v)
		case float64:
			return v
		}
		return 0
	}
	str := func(id string) string {
		s, _ := raw.Items[id].(string)
		return s
	}

	// 代码交叉校验：代码由 URL 决定，响应自称不一致（含**缺失**）说明上游串号或改了形状 ——
	// 一律报错交给上层降级。绝不把别人的价格当成这只票的（错而不报比取不到危险得多）；
	// 字段 5 在实测样本中恒定存在，缺失即视为上游改形，宁可失能也不要静默错值。
	// 用 HasSuffix 而非等值比较：兼容传入 "600519.SH" 这类带后缀形式，且不会按长度切片。
	respCode := str(thsFCode)
	if respCode == "" || !strings.HasSuffix(code, respCode) {
		return nil, fmt.Errorf("ths: code mismatch (want %s, got %q)", code, respCode)
	}
	price := num(thsFPrice)
	if price <= 0 {
		return nil, fmt.Errorf("ths: no data for %s (price missing)", code)
	}

	si := &StockInfo{
		Code:      code,
		Name:      str(thsFName),
		Price:     price,
		Open:      num(thsFOpen),
		High:      num(thsFHigh),
		Low:       num(thsFLow),
		Close:     num(thsFPrevClose),
		PrevClose: num(thsFPrevClose), // 显式昨收（字段 6）
		Volume:    num(thsFVolume),    // 已为股，与新浪/东财口径一致
		Amount:    num(thsFAmount),    // 已为元
		Turnover:  num(thsFTurnover),  // 换手率（%）——新浪无此字段，此源可补
		ChangePct: num(thsFChangePct),
	}
	// 涨跌幅字段缺失时才用昨收推算；字段存在则以字段为准，避免口径分歧。
	if _, ok := raw.Items[thsFChangePct]; !ok && si.PrevClose > 0 {
		si.ChangePct = (si.Price - si.PrevClose) / si.PrevClose * 100
	}
	return si, nil
}
