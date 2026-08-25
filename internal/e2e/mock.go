// Package e2e 全流程端到端测试基础设施：把 fixture 实盘快照重放为 mock HTTP 网络，
// 并提供按 system prompt 区分的 mock LLM，让 engine.Run 全链路离线可复现。
package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// fixtureTransport 实现 http.RoundTripper，按 host+path 路由到 fixture 数据，
// 响应格式与 internal/data 各解析器严格一致。
type fixtureTransport struct {
	fix *Fixture // 实盘数据快照，所有请求都从这里读取响应
}

// RoundTrip 路由请求到对应 fixture 数据。
func (t *fixtureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	host := req.URL.Hostname()
	path := req.URL.Path

	switch {
	case host == "hq.sinajs.cn":
		return t.sinaQuotes(req)
	case host == "money.finance.sina.com.cn" && strings.Contains(path, "getKLineData"):
		return t.sinaKLine(req)
	case host == "push2.eastmoney.com" && path == "/api/qt/clist/get":
		return t.emClist(req)
	case host == "push2.eastmoney.com" && path == "/api/qt/stock/get":
		return t.emStockGet(req)
	case host == "push2.eastmoney.com" && path == "/api/qt/stock/fflow/kline/get":
		return t.emMoneyFlow(req)
	case host == "push2.eastmoney.com" && path == "/api/qt/stock/kline/get":
		return t.emKLine(req)
	case host == "push2ex.eastmoney.com" && path == "/getTopicZTPool":
		return t.emLimitUpPool(req)
	case host == "datacenter-web.eastmoney.com" && strings.Contains(req.URL.Query().Get("reportName"), "IPOAPPLY"):
		return t.emIPO(req)
	case host == "datacenter-web.eastmoney.com" && strings.Contains(req.URL.Query().Get("reportName"), "BILLBOARD"):
		return t.emLHB(req)
	case host == "news.10jqka.com.cn" && path == "/tapp/news/push/stock":
		return t.thsNews(req)
	case host == "finance.sina.com.cn":
		// 新浪财经文章页正文（GetArticle 用 id="artibody" 提取）
		return t.text(`<html><body><div id="artibody"><p>新浪正文：沪指半日涨0.62%，AI算力概念持续走强，光模块方向领涨。</p><p>（mock 文章正文，供正文回填覆盖测试。）</p></div></body></html>`)
	case host == "www.cls.cn" && path == "/v1/roll/get_roll_list":
		return t.clsNews(req)
	case host == "feed.mix.sina.com.cn":
		return t.sinaNews(req)
	case host == "vip.stock.finance.sina.com.cn" && strings.Contains(path, "getHQNodeData"):
		return t.sinaStockList(req)
	case host == "q.10jqka.com.cn" && path == "/gn/":
		return t.text(t.fix.THSConcepts)
	case host == "q.10jqka.com.cn" && path == "/thshy/":
		return t.text(t.fix.THSIndustries)
	case host == "np-anotice-stock.eastmoney.com":
		return t.json(map[string]interface{}{"data": map[string]interface{}{"list": []interface{}{}}})
	case host == "q.10jqka.com.cn" && strings.Contains(path, "realhead"):
		return t.json(map[string]interface{}{})
	case host == "d.10jqka.com.cn" && strings.Contains(path, "realhead"):
		return t.thsQuote(req)
	default:
		return t.json(map[string]interface{}{})
	}
}

// json 构造一个 200 状态、application/json 类型的 mock 响应。
func (t *fixtureTransport) json(v interface{}) (*http.Response, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return response(200, "application/json", body), nil
}

// text 构造一个 200 状态、text/html 编码的 mock 响应（用于同花顺页面 HTML 重放）。
func (t *fixtureTransport) text(s string) (*http.Response, error) {
	return response(200, "text/html; charset=utf-8", []byte(s)), nil
}

// sinaQuotes 新浪批量实时行情：var hq_str_sh600519="CSV,...";
// 真实 URL 形态为 https://hq.sinajs.cn/list=sz300750,sh600519（代码位于 path 而非 query）。
func (t *fixtureTransport) sinaQuotes(req *http.Request) (*http.Response, error) {
	raw := req.URL.Path
	if !strings.Contains(raw, "=") {
		raw = req.URL.Query().Get("list")
	}
	if idx := strings.Index(raw, "="); idx >= 0 {
		raw = raw[idx+1:]
	}
	symbols := strings.Split(raw, ",")
	var buf strings.Builder
	for _, sym := range symbols {
		sym = strings.TrimSpace(sym)
		if len(sym) < 8 {
			continue
		}
		code := sym[2:]
		if csv, ok := t.fix.Quotes[code]; ok {
			buf.WriteString(fmt.Sprintf("var hq_str_%s=%q;\n", sym, csv))
		}
	}
	utf := []byte(buf.String())
	gbk, err := simplifiedchinese.GBK.NewEncoder().Bytes(utf)
	if err != nil {
		gbk = utf
	}
	return response(200, "text/plain; charset=GBK", gbk), nil
}

// sinaKLine 新浪 K 线 JSON 数组（数值为字符串，最新在前，解析器会反转）。
// scale=5 时重放 MinuteKlines（5分钟线），其余 scale 重放日K Klines。
func (t *fixtureTransport) sinaKLine(req *http.Request) (*http.Response, error) {
	symbol := req.URL.Query().Get("symbol")
	code := symbol
	if len(code) > 2 {
		code = code[2:]
	}
	kls := t.fix.Klines[code]
	isMinute := req.URL.Query().Get("scale") == "5" && len(t.fix.MinuteKlines[code]) > 0
	if isMinute {
		kls = t.fix.MinuteKlines[code]
	}
	rows := make([]map[string]string, 0, len(kls))
	// 日K：parseSinaKLine 假定输入为最新在前（倒序）并做反转，fixture 为升序 → 反序输出。
	// 分钟K：GetSinaMinuteKLine 自行按时间升序排序，fixture 升序直出。
	for i := 0; i < len(kls); i++ {
		k := kls[i]
		if !isMinute {
			k = kls[len(kls)-1-i]
		}
		dayFmt := "2006-01-02"
		if isMinute {
			dayFmt = "2006-01-02 15:04:05"
		}
		rows = append(rows, map[string]string{
			"day":    k.Date.Format(dayFmt),
			"open":   strconv.FormatFloat(k.Open, 'f', 2, 64),
			"high":   strconv.FormatFloat(k.High, 'f', 2, 64),
			"low":    strconv.FormatFloat(k.Low, 'f', 2, 64),
			"close":  strconv.FormatFloat(k.Close, 'f', 2, 64),
			"volume": strconv.FormatFloat(k.Volume, 'f', 0, 64),
			"amount": strconv.FormatFloat(k.Amount, 'f', 0, 64),
		})
	}
	return t.json(rows)
}

// emClist 东财 clist：板块成分股(b:代码) / 股票列表(m:*) / 板块列表(m:90)。
func (t *fixtureTransport) emClist(req *http.Request) (*http.Response, error) {
	fs := req.URL.Query().Get("fs")

	if strings.HasPrefix(fs, "b:") {
		code := strings.TrimPrefix(fs, "b:")
		stocks := t.fix.SectorStocks[code]
		items := make([]map[string]interface{}, 0, len(stocks))
		for _, s := range stocks {
			items = append(items, map[string]interface{}{
				"f12": s.Code, "f14": s.Name,
				"f2": s.Price * 100, "f3": s.ChangePct * 100,
				"f15": s.High * 100, "f16": s.Low * 100,
				"f17": s.Open * 100, "f18": s.Close * 100,
				"f5": s.Volume, "f6": s.Amount, "f7": s.Turnover,
			})
		}
		return t.json(map[string]interface{}{
			"data": map[string]interface{}{"total": len(items), "items": items},
		})
	}

	if strings.Contains(fs, "m:90") {
		// 东财行业板块行情列表：diff 为 map[string]sectorRawItem（f3 千分位基点）。
		items := make(map[string]interface{})
		for i, s := range t.fix.EMBoardList {
			items[strconv.Itoa(i)] = map[string]interface{}{
				"f12": s.Code, "f14": s.Name,
				"f3":  s.ChangePct * 100,
				"f20": s.Amount, "f62": s.NetInflow,
				"f104": s.VolumeRank, "f105": s.LimitupCnt,
			}
		}
		return t.json(map[string]interface{}{
			"data": map[string]interface{}{"total": len(items), "diff": items},
		})
	}

	// 全量股票列表 name -> code
	diff := make(map[string]interface{})
	for name, code := range t.fix.StockList {
		diff[code] = map[string]interface{}{"f12": code, "f14": name, "f9": 0.0, "f20": 0.0}
	}
	return t.json(map[string]interface{}{
		"data": map[string]interface{}{"total": len(diff), "diff": diff},
	})
}

// emStockGet 东财个股：industry(f128) / 实时行情(f43...) / 指数(000001)。
func (t *fixtureTransport) emStockGet(req *http.Request) (*http.Response, error) {
	secid := req.URL.Query().Get("secid")
	fields := req.URL.Query().Get("fields")
	code := secid
	if idx := strings.Index(code, "."); idx >= 0 {
		code = code[idx+1:]
	}

	if strings.Contains(fields, "f128") {
		return t.json(map[string]interface{}{
			"data": map[string]interface{}{"f128": t.fix.Industries[code]},
		})
	}

	if code == "000001" {
		return t.json(map[string]interface{}{
			"data": map[string]interface{}{"f43": t.fix.IndexPrice * 100, "f58": "上证指数"},
		})
	}

	csv, ok := t.fix.Quotes[code]
	if !ok {
		return t.json(map[string]interface{}{"data": map[string]interface{}{"f43": 0}})
	}
	p := strings.Split(csv, ",")
	parse := func(i int) float64 {
		f, _ := strconv.ParseFloat(p[i], 64)
		return f
	}
	name, open, prev, price, high, low := p[0], parse(1), parse(2), parse(3), parse(4), parse(5)
	changePct := 0.0
	if prev > 0 {
		changePct = (price - prev) / prev * 100
	}
	netInflow := 0.0
	if t.fix.NetInflows != nil {
		netInflow = t.fix.NetInflows[code]
	}
	return t.json(map[string]interface{}{
		"data": map[string]interface{}{
			"f43": price * 100, "f44": high * 100, "f45": low * 100, "f46": open * 100,
			"f60": prev * 100, "f48": parse(8), "f49": parse(9), "f50": changePct,
			"f57": code, "f58": name, "f170": changePct * 100, "f62": netInflow,
		},
	})
}

// emMoneyFlow 东财个股资金流 fflow klines。
func (t *fixtureTransport) emMoneyFlow(req *http.Request) (*http.Response, error) {
	secid := req.URL.Query().Get("secid")
	code := secid
	if idx := strings.Index(code, "."); idx >= 0 {
		code = code[idx+1:]
	}
	return t.json(map[string]interface{}{
		"data": map[string]interface{}{"klines": t.fix.MoneyFlow[code]},
	})
}

// emKLine 东财 K 线：klines 行 "date,open,high,low,close,volume,amount,..."。
func (t *fixtureTransport) emKLine(req *http.Request) (*http.Response, error) {
	secid := req.URL.Query().Get("secid")
	code := secid
	if idx := strings.Index(code, "."); idx >= 0 {
		code = code[idx+1:]
	}
	var lines []string
	for _, k := range t.fix.Klines[code] {
		lines = append(lines, fmt.Sprintf("%s,%.2f,%.2f,%.2f,%.2f,%.0f,%.0f",
			k.Date.Format("2006-01-02"), k.Open, k.High, k.Low, k.Close, k.Volume, k.Amount))
	}
	return t.json(map[string]interface{}{
		"data": map[string]interface{}{"klines": lines, "code": code},
	})
}

// emLimitUpPool 东财涨停池。
func (t *fixtureTransport) emLimitUpPool(req *http.Request) (*http.Response, error) {
	pool := make([]map[string]interface{}, 0, len(t.fix.LimitUpPool))
	for _, s := range t.fix.LimitUpPool {
		pool = append(pool, map[string]interface{}{
			"c": s.Code, "n": s.Name, "p": s.Price * 1000, "zdp": s.ChangePct,
			"amount": s.Amount, "ltsz": s.FlowMCap, "hs": s.Turnover, "lbc": s.LianBan,
			"fbt": sealToInt(s.FirstSeal), "fund": s.SealAmt, "zbc": s.BreakCount,
			"hybk": s.Industry, "zttj": map[string]interface{}{"days": s.UpDays},
		})
	}
	return t.json(map[string]interface{}{"data": map[string]interface{}{"pool": pool}})
}

// emIPO 东财新股日历。
// §日期防腐：parseEastMoneyIPO 只保留今日及以后上市的新股——fixture 快照日期会腐烂
// （20260819 实录：8/4 摄取的快照 8/19 后全被过滤，IPO 日历事件归零）。这里把每条
// 记录的日期改写为 申购=今天 / 上市=明天，保持"当日真实条数"的数据驱动语义不变。
func (t *fixtureTransport) emIPO(req *http.Request) (*http.Response, error) {
	today := time.Now().Format("20060102")
	tomorrow := time.Now().AddDate(0, 0, 1).Format("20060102")
	data := make([]map[string]interface{}, 0, len(t.fix.IPO))
	for _, s := range t.fix.IPO {
		listing := tomorrow
		if s.ListingDate >= today { // 未来日期保留原值（测试显式控制过期场景）
			listing = s.ListingDate
		}
		data = append(data, map[string]interface{}{
			"SECURITY_CODE": s.Code, "SECURITY_NAME": s.Name,
			"APPLY_DATE": today, "ISSUE_PRICE": s.IssuePrice, "LISTING_DATE": listing,
		})
	}
	return t.json(map[string]interface{}{
		"success": true,
		"result":  map[string]interface{}{"data": data},
	})
}

// emLHB 东财龙虎榜：按 fixture.LHB 数据返回（真实记录重放）。
func (t *fixtureTransport) emLHB(req *http.Request) (*http.Response, error) {
	data := make([]map[string]interface{}, 0, len(t.fix.LHB))
	for _, s := range t.fix.LHB {
		data = append(data, map[string]interface{}{
			"SECURITY_CODE": s.Code, "SECURITY_NAME_ABBR": s.Name,
			"CLOSE_PRICE": s.Price, "CHANGE_RATE": s.ChangePct,
			"EXPLANATION": s.Reason, "EXPLAIN": s.SeatInfo,
			"BILLBOARD_NET_AMT": s.NetAmt, "BILLBOARD_BUY_AMT": s.BuyAmt,
			"BILLBOARD_SELL_AMT": s.SellAmt,
			"BUY_SEAT":           s.BuySeat, "SELL_SEAT": s.SellSeat,
			"TURNOVERRATE": s.Turnover,
		})
	}
	return t.json(map[string]interface{}{
		"success": true,
		"result":  map[string]interface{}{"data": data},
	})
}

// thsQuote 同花顺实时行情 JSONP：与 parseTHSQuote 预期一致
// （items 内每只股票为数组，索引约定 [.., code, name, open, high, low, price, volume, amount, ..]，长度须≥10）。
// 真实 URL 形态为 /v2/realhead/hs_{secid}/last.js，secid 位于 path 倒数第二段。
func (t *fixtureTransport) thsQuote(req *http.Request) (*http.Response, error) {
	segs := strings.Split(strings.Trim(req.URL.Path, "/"), "/")
	secID := ""
	for i := len(segs) - 1; i >= 0; i-- {
		if strings.HasPrefix(segs[i], "hs_") {
			secID = strings.TrimPrefix(segs[i], "hs_")
			break
		}
	}
	if secID == "" {
		return t.json(map[string]interface{}{"data": map[string]interface{}{"items": map[string]interface{}{}}})
	}
	code := secID
	code = strings.ReplaceAll(code, "1.", "")
	code = strings.ReplaceAll(code, "0.", "")

	items := make(map[string]interface{})
	if csv, ok := t.fix.Quotes[code]; ok {
		p := strings.Split(csv, ",")
		parse := func(i int) float64 {
			f, _ := strconv.ParseFloat(p[i], 64)
			return f
		}
		arr := make([]interface{}, 12)
		arr[1] = "hs_" + secID // 代码（含前缀，parseTHSQuote 内部剥离）
		arr[2] = p[0]          // 名称
		arr[3] = parse(1)      // 今开
		arr[4] = parse(4)      // 最高
		arr[5] = parse(5)      // 最低
		arr[6] = parse(3)      // 现价
		arr[7] = parse(8)      // 成交量
		arr[8] = parse(9)      // 成交额
		items[secID] = arr
	}
	return t.json(map[string]interface{}{
		"data": map[string]interface{}{"items": items},
	})
}

// thsNews 同花顺快讯：page 1 返回全部 ths 场景新闻，后续页为空。
func (t *fixtureTransport) thsNews(req *http.Request) (*http.Response, error) {
	page, _ := strconv.Atoi(req.URL.Query().Get("page"))
	list := make([]map[string]interface{}, 0)
	if page <= 1 {
		for _, n := range t.fix.News["ths"] {
			list = append(list, map[string]interface{}{
				"title": n.Title, "digest": n.Content, "ctime": n.Datetime, "url": n.URL,
			})
		}
	}
	return t.json(map[string]interface{}{"code": "200", "data": map[string]interface{}{"list": list}})
}

// clsNews 财联社电报。
func (t *fixtureTransport) clsNews(req *http.Request) (*http.Response, error) {
	roll := make([]map[string]interface{}, 0, len(t.fix.News["cls"]))
	for _, n := range t.fix.News["cls"] {
		ct := time.Date(2026, 7, 31, 13, 10, 0, 0, time.Local).Unix()
		if ts, err := time.ParseInLocation("2006-01-02 15:04:05", n.Datetime, time.Local); err == nil {
			ct = ts.Unix()
		}
		roll = append(roll, map[string]interface{}{
			"id": ct, "title": n.Title, "content": n.Content, "ctime": ct, "stock_list": []interface{}{},
		})
	}
	return t.json(map[string]interface{}{
		"errno": 0, "msg": "",
		"data": map[string]interface{}{"roll_data": roll},
	})
}

// sinaNews 新浪滚动新闻：真实格式重放（兜底源解析验证；主场景由 THS+CLS 提供）。
func (t *fixtureTransport) sinaNews(req *http.Request) (*http.Response, error) {
	data := make([]map[string]interface{}, 0, len(t.fix.News["sina"]))
	for _, n := range t.fix.News["sina"] {
		data = append(data, map[string]interface{}{
			"title": n.Title, "content": n.Content,
			"show_time": n.Datetime, "url": n.URL, "ctime": "",
		})
	}
	return t.json(map[string]interface{}{
		"result": map[string]interface{}{"data": data},
	})
}

// sinaMockPageSize 新浪全市场列表 mock 每页条数（与 data 包 sinaMockPageSize 一致）。
const sinaMockPageSize = 100

// sinaStockList 新浪全市场 A 股列表（getHQNodeData 分页，100/页）。
// 将 fixture.StockList（name→code）折叠为新浪格式并按下标分组，page 参数驱动翻页。
func (t *fixtureTransport) sinaStockList(req *http.Request) (*http.Response, error) {
	page, _ := strconv.Atoi(req.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	all := make([]map[string]string, 0, len(t.fix.StockList))
	for name, code := range t.fix.StockList {
		sec := "sh"
		if strings.HasPrefix(code, "0") || strings.HasPrefix(code, "3") {
			sec = "sz"
		}
		all = append(all, map[string]string{"symbol": sec + code, "code": code, "name": name})
	}
	start := (page - 1) * sinaMockPageSize
	if start >= len(all) {
		return t.json([]map[string]string{})
	}
	end := start + sinaMockPageSize
	if end > len(all) {
		end = len(all)
	}
	return t.json(all[start:end])
}

// sealToInt 将 "HH:MM" 转为东财 HHMMSS 整数（090000 → 90000）。
func sealToInt(hhmm string) int {
	parts := strings.Split(hhmm, ":")
	if len(parts) != 2 {
		return 0
	}
	h, _ := strconv.Atoi(parts[0])
	m, _ := strconv.Atoi(parts[1])
	return h*10000 + m*100
}

// response 构造一个最小可用的 *http.Response（含状态码、Content-Type 与请求体）。
func response(code int, contentType string, body []byte) *http.Response {
	return &http.Response{
		StatusCode: code,
		Header: http.Header{
			"Content-Type": []string{contentType},
		},
		Body:          io.NopCloser(strings.NewReader(string(body))),
		ContentLength: int64(len(body)),
	}
}

// ── mock LLM ──

// llmCalls 记录 mock LLM 的调用类型与请求文本。
type llmCalls struct {
	mu sync.Mutex // §-race：handler goroutine 写、测试 goroutine 读，必须互斥

	stage0 []string
	stage2 []string
	d1     []string

	// consult 记录股票咨询请求的 system prompt（含注入的专业模式上下文）。
	consult []string

	// consultMsgs 记录每次咨询请求的完整消息序列（role 顺序），用于断言单条 system 置于最前。
	consultMsgs [][]roleMsg

	// failD1 置为 true 后，mock 对 D1 评分请求返回 500，用于验证 D1 失败标记待重试并入重试队列。
	// 经 SetFailD1/读取器访问（加锁）；测试直接赋值已改为 SetFailD1。
	failD1 bool
}

// record 追加一条记录（加锁）。
func (c *llmCalls) record(kind, v string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch kind {
	case "stage0":
		c.stage0 = append(c.stage0, v)
	case "stage2":
		c.stage2 = append(c.stage2, v)
	case "d1":
		c.d1 = append(c.d1, v)
	case "consult":
		c.consult = append(c.consult, v)
	}
}

// recordMsgs 追加一组消息记录（加锁）。
func (c *llmCalls) recordMsgs(msgs []roleMsg) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.consultMsgs = append(c.consultMsgs, msgs)
}

// lenOf 某类记录的当前长度（加锁快照）。
func (c *llmCalls) lenOf(kind string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch kind {
	case "stage0":
		return len(c.stage0)
	case "stage2":
		return len(c.stage2)
	case "d1":
		return len(c.d1)
	case "consult":
		return len(c.consult)
	}
	return 0
}

// SetFailD1 设置 D1 失败开关（加锁；测试与 handler 异步可见）。
func (c *llmCalls) SetFailD1(v bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.failD1 = v
}

// snapshot 返回某类记录的副本（加锁；测试遍历用）。
func (c *llmCalls) snapshot(kind string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var src []string
	switch kind {
	case "stage0":
		src = c.stage0
	case "stage2":
		src = c.stage2
	case "d1":
		src = c.d1
	case "consult":
		src = c.consult
	}
	out := make([]string, len(src))
	copy(out, src)
	return out
}

// IsFailD1 读取 D1 失败开关（加锁）。
func (c *llmCalls) IsFailD1() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.failD1
}

// roleContent 咨询请求中的一条消息（role + content）。
type roleMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// newMockLLMServer 启动按 system prompt 区分的 mock LLM 服务。
// 返回 (server, 调用记录)。
func newMockLLMServer() (*httptest.Server, *llmCalls) {
	calls := &llmCalls{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		var system, user string
		for _, m := range req.Messages {
			if m.Role == "system" {
				system += m.Content + "\n"
			}
			if m.Role == "user" && user == "" {
				user = m.Content
			}
		}

		var content string
		switch {
		case strings.Contains(system, "股票投资顾问"):
			// 股票咨询：记录注入的 system prompt（专业模式含实时行情上下文）与完整消息序列，返回确定性回复。
			calls.record("consult", system)
			msgs := make([]roleMsg, 0, len(req.Messages))
			for _, m := range req.Messages {
				msgs = append(msgs, roleMsg{Role: m.Role, Content: m.Content})
			}
			calls.recordMsgs(msgs)
			content = "已收到您的咨询。根据实测数据：卧龙电驱今日主力净流入-22200万元，现价36.86元，涨跌幅3.34%。请注意当前行情仅供分析参考，不构成投资建议。"
		case strings.Contains(system, "质检与价值判断"):
			calls.record("stage0", user)
			content = mockStage0JSON(user)
		case strings.Contains(system, "D1事件评分"):
			calls.record("d1", user)
			if calls.IsFailD1() {
				// 模拟 D1 LLM 整批失败（500）：BatchScore 轮询重试失败后标记 RetryPending，
				// 失败股并入重试队列，下轮重新调 LLM（不回退上一轮评分、不归0兜底）
				http.Error(w, "mock D1 LLM failure", 500)
				return
			}
			content = mockD1JSON(user)
		case strings.Contains(system, "热点分析专家"):
			calls.record("stage2", user)
			content = mockStage2JSON(user)
		default:
			content = "[]"
		}

		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{
				{"message": map[string]interface{}{"role": "assistant", "content": content}},
			},
		})
	}))
	return srv, calls
}

// combinedJudge 场景新闻的 Stage0 判定。
type combinedJudge struct {
	official bool
	material bool
}

// materialTitles 有投资价值的场景新闻（其余 official 新闻 material=false）。
var materialTitles = map[string]bool{
	"工信部等七部门印发《人工智能算力基础设施高质量发展行动计划》": true,
	"宁德时代中标沙特10GWh储能系统大单":            true,
	"贵州茅台三季度营收净利双降 单季净利同比下滑15%":      true,
	"国家医保局拟开展第七轮药品集采 创新药面临利空":        true,
	"恒瑞医药创新药获批上市":                    true,
	"突发！大消息！AI算力再迎超级风口":              true,
}

// judgeCombined 按标题关键词判定 Stage0 质检结果：
// 含"研报/中金"的归为机构噪音（official=false），其余看是否在 materialTitles 中判定投资价值。
func judgeCombined(title string) combinedJudge {
	if strings.Contains(title, "研报") || strings.Contains(title, "中金") {
		return combinedJudge{official: false}
	}
	return combinedJudge{official: true, material: materialTitles[title]}
}

// mockStage0JSON 解析 "N. 标题\n正文: ..." 格式，返回每条的 category/material。
func mockStage0JSON(user string) string {
	type item struct {
		Index          int    `json:"index"`
		Category       string `json:"category"`
		Material       bool   `json:"material"`
		CorrectedTitle string `json:"corrected_title"`
	}
	var out []item
	for _, line := range strings.Split(user, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		sep := strings.Index(line, ".")
		if sep <= 0 {
			continue
		}
		idx, err := strconv.Atoi(strings.TrimSpace(line[:sep]))
		if err != nil {
			continue
		}
		title := strings.TrimSpace(line[sep+1:])
		if i := strings.Index(title, "\n正文"); i >= 0 {
			title = title[:i]
		}
		j := judgeCombined(title)
		cat := "official"
		if !j.official {
			cat = "institution"
		}
		out = append(out, item{Index: idx, Category: cat, Material: j.material, CorrectedTitle: ""})
	}
	raw, _ := json.Marshal(out)
	return string(raw)
}

// htResult 与 llm.HotTopic 同构的 JSON 输出。
type htResult struct {
	Index             int      `json:"index"`
	Level             string   `json:"level"`
	Sentiment         string   `json:"sentiment"`
	Score             float64  `json:"score"`
	ImpactLevel       string   `json:"impact_level"`
	EventType         string   `json:"event_type"`
	Urgency           string   `json:"urgency"`
	Direction         string   `json:"direction"`
	Sectors           []string `json:"sectors"`
	UpstreamSectors   []string `json:"upstream_sectors"`
	DownstreamSectors []string `json:"downstream_sectors"`
	RelatedStocks     []string `json:"related_stocks"`
	Strategy          string   `json:"strategy"`
	Reason            string   `json:"reason"`
}

// scenarioAnalyses 场景标题 → Stage2 深度分析（确定性返回）。
var scenarioAnalyses = map[string]htResult{
	"工信部等七部门印发《人工智能算力基础设施高质量发展行动计划》": {
		Level: "板块", Sentiment: "正面", Score: 0.75, ImpactLevel: "高", EventType: "政策",
		Urgency: "立即", Direction: "利好", Sectors: []string{"人工智能"},
		RelatedStocks: []string{"300308", "000938"}, Strategy: "无", Reason: "算力基础设施政策利好AI产业链",
	},
	"宁德时代中标沙特10GWh储能系统大单": {
		Level: "个股", Sentiment: "正面", Score: 0.75, ImpactLevel: "中", EventType: "公司",
		Urgency: "关注", Direction: "利好", RelatedStocks: []string{"300750"},
		Strategy: "无", Reason: "中标海外储能大单，业绩确定性增强",
	},
	"贵州茅台三季度营收净利双降 单季净利同比下滑15%": {
		Level: "个股", Sentiment: "负面", Score: -0.75, ImpactLevel: "中", EventType: "财报",
		Urgency: "关注", Direction: "利空", RelatedStocks: []string{"600519"},
		Strategy: "无", Reason: "三季报净利大幅下滑",
	},
	"国家医保局拟开展第七轮药品集采 创新药面临利空": {
		Level: "板块", Sentiment: "负面", Score: -0.75, ImpactLevel: "高", EventType: "政策",
		Urgency: "关注", Direction: "利空", Sectors: []string{"创新药"},
		RelatedStocks: []string{"600276"}, Strategy: "无", Reason: "药品集采扩围压价利空创新药",
	},
	"恒瑞医药创新药获批上市": {
		Level: "个股", Sentiment: "正面", Score: 0.75, ImpactLevel: "中", EventType: "公司",
		Urgency: "关注", Direction: "利好", RelatedStocks: []string{"600276"},
		Strategy: "无", Reason: "重磅创新药获批上市",
	},
	"突发！大消息！AI算力再迎超级风口": {
		Level: "板块", Sentiment: "正面", Score: 0.75, ImpactLevel: "中", EventType: "行业",
		Urgency: "关注", Direction: "利好", Sectors: []string{"人工智能"},
		RelatedStocks: []string{"300308", "000938"},
		Strategy:      "无", Reason: "AI算力板块午后集体走强",
	},
}

// mockStage2JSON 解析 "N. 标题" 列表，按标题返回深度分析 JSON 数组。
func mockStage2JSON(user string) string {
	var out []htResult
	titles := parseNumberedTitles(user)
	for i, title := range titles {
		res := htResult{Index: i + 1}
		if a, ok := scenarioAnalyses[title]; ok {
			res = a
			res.Index = i + 1
		} else {
			res = htResult{Index: i + 1, Level: "板块", Sentiment: "中性", Score: 0,
				ImpactLevel: "低", EventType: "行业", Urgency: "观察", Direction: "中性",
				Sectors: []string{}, Strategy: "无", Reason: "无实质影响"}
		}
		out = append(out, res)
	}
	raw, _ := json.Marshal(out)
	return string(raw)
}

// mockD1JSON 解析 "N. 代码: XXX" 列表，返回确定性 D1 评分。
func mockD1JSON(user string) string {
	var codes []string
	for _, line := range strings.Split(user, "\n") {
		line = strings.TrimSpace(line)
		if i := strings.Index(line, "代码:"); i >= 0 {
			codes = append(codes, strings.TrimSpace(line[i+len("代码:"):]))
		}
	}
	type d1Res struct {
		Code    string  `json:"code"`
		Score   float64 `json:"score"`
		Blocked bool    `json:"blocked"`
		Reason  string  `json:"reason"`
	}
	var out []d1Res
	for _, c := range codes {
		r := d1Res{Code: c, Score: 0.3, Reason: "常规关注"}
		switch c {
		case "300750":
			r = d1Res{Code: c, Score: 0.7, Reason: "中标海外储能大单，利好兑现"}
		case "600276":
			r = d1Res{Code: c, Score: 0.5, Reason: "新药获批利好，但股价反应不足"}
		case "600519":
			r = d1Res{Code: c, Score: 0.0, Blocked: true, Reason: "三季报净利下滑，负面过滤"}
		}
		out = append(out, r)
	}
	raw, _ := json.Marshal(out)
	return string(raw)
}

// parseNumberedTitles 解析 "N. 标题" 列表为标题切片（保持顺序）。
func parseNumberedTitles(user string) []string {
	var titles []string
	for _, line := range strings.Split(user, "\n") {
		line = strings.TrimSpace(line)
		sep := strings.Index(line, ".")
		if sep <= 0 {
			continue
		}
		if _, err := strconv.Atoi(strings.TrimSpace(line[:sep])); err != nil {
			continue
		}
		title := strings.TrimSpace(line[sep+1:])
		if title == "" {
			continue
		}
		titles = append(titles, title)
	}
	return titles
}
