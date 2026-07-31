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
	"time"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// fixtureTransport 实现 http.RoundTripper，按 host+path 路由到 fixture 数据，
// 响应格式与 internal/data 各解析器严格一致。
type fixtureTransport struct {
	fix *Fixture
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
	case host == "news.10jqka.com.cn" && path == "/tapp/news/push/stock":
		return t.thsNews(req)
	case host == "www.cls.cn" && path == "/v1/roll/get_roll_list":
		return t.clsNews(req)
	case host == "feed.mix.sina.com.cn":
		return t.sinaNews(req)
	case host == "q.10jqka.com.cn" && path == "/gn/":
		return t.text(t.fix.THSConcepts)
	case host == "q.10jqka.com.cn" && path == "/thshy/":
		return t.text(t.fix.THSIndustries)
	case host == "np-anotice-stock.eastmoney.com":
		return t.json(map[string]interface{}{"data": map[string]interface{}{"list": []interface{}{}}})
	case host == "q.10jqka.com.cn" && strings.Contains(path, "realhead"):
		return t.json(map[string]interface{}{})
	default:
		return t.json(map[string]interface{}{})
	}
}

func (t *fixtureTransport) json(v interface{}) (*http.Response, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return response(200, "application/json", body), nil
}

func (t *fixtureTransport) text(s string) (*http.Response, error) {
	return response(200, "text/html; charset=utf-8", []byte(s)), nil
}

// sinaQuotes 新浪批量实时行情：var hq_str_sh600519="CSV,...";
func (t *fixtureTransport) sinaQuotes(req *http.Request) (*http.Response, error) {
	symbols := strings.Split(req.URL.Query().Get("list"), ",")
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
func (t *fixtureTransport) sinaKLine(req *http.Request) (*http.Response, error) {
	symbol := req.URL.Query().Get("symbol")
	code := symbol
	if len(code) > 2 {
		code = code[2:]
	}
	kls := t.fix.Klines[code]
	rows := make([]map[string]string, 0, len(kls))
	// parseSinaKLine 假定输入为最新在前（倒序）并做反转，fixture 为升序 → 反序输出
	for i := len(kls) - 1; i >= 0; i-- {
		k := kls[i]
		rows = append(rows, map[string]string{
			"day":    k.Date.Format("2006-01-02"),
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
				"f3": s.ChangePct * 100,
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
	return t.json(map[string]interface{}{
		"data": map[string]interface{}{
			"f43": price * 100, "f44": high * 100, "f45": low * 100, "f46": open * 100,
			"f60": prev * 100, "f48": parse(8), "f49": parse(9), "f50": changePct,
			"f57": code, "f58": name, "f170": 0.0, "f162": 0.0,
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
			"c": s.Code, "n": s.Name, "p": s.Price * 100, "zdp": s.ChangePct,
			"amount": s.Amount, "ltsz": s.FlowMCap, "hs": s.Turnover, "lbc": s.LianBan,
			"fbt": sealToInt(s.FirstSeal), "fund": s.SealAmt, "zbc": s.BreakCount,
			"hybk": s.Industry, "zttj": map[string]interface{}{"days": s.UpDays},
		})
	}
	return t.json(map[string]interface{}{"data": map[string]interface{}{"pool": pool}})
}

// emIPO 东财新股日历。
func (t *fixtureTransport) emIPO(req *http.Request) (*http.Response, error) {
	data := make([]map[string]interface{}, 0, len(t.fix.IPO))
	for _, s := range t.fix.IPO {
		data = append(data, map[string]interface{}{
			"SECURITY_CODE": s.Code, "SECURITY_NAME": s.Name,
			"APPLY_DATE": s.IPODate, "ISSUE_PRICE": s.IssuePrice, "LISTING_DATE": s.ListingDate,
		})
	}
	return t.json(map[string]interface{}{
		"success": true,
		"result":  map[string]interface{}{"data": data},
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
	stage0   []string
	stage2   []string
	d1       []string
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
				system = m.Content
			}
			if m.Role == "user" {
				user = m.Content
			}
		}

		var content string
		switch {
		case strings.Contains(system, "质检与价值判断"):
			calls.stage0 = append(calls.stage0, user)
			content = mockStage0JSON(user)
		case strings.Contains(system, "D1事件评分"):
			calls.d1 = append(calls.d1, user)
			content = mockD1JSON(user)
		case strings.Contains(system, "热点分析专家"):
			calls.stage2 = append(calls.stage2, user)
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
	"宁德时代中标沙特10GWh储能系统大单":                      true,
	"贵州茅台三季度营收净利双降 单季净利同比下滑15%":             true,
	"国家医保局拟开展第七轮药品集采 创新药面临利空":             true,
	"恒瑞医药创新药获批上市":                            true,
	"突发！大消息！AI算力再迎超级风口":                     true,
}

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
		Strategy: "无", Reason: "AI算力板块午后集体走强",
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
