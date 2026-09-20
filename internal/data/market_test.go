// market_test.go — 行情 API 单元测试：验证 PE 预取与 TTL 缓存命中、股票列表主源（新浪）与兜底（东财）。
package data

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// testResp 构造一个 mock HTTP 响应（本地 helper，避免跨包依赖）。
// English: testResp builds a mock HTTP response (local helper to avoid cross-package dependencies).
func testResp(code int, body string) *http.Response {
	return &http.Response{
		StatusCode: code,
		Status:     http.StatusText(code),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewBufferString(body)),
	}
}

// listMockTransport 按 host 区分响应：新浪列表 host 返回可切换结果，东财返回股票列表 JSON。
// 另按 clist 的 pz 参数区分 GetStockList（pz=10000→diff map）与 GetStockPE（pz=1→diff 数组），
// 并统计 PE 请求次数以验证 TTL 缓存命中。
// English: listMockTransport routes responses by host: the Sina list host returns switchable results, Eastmoney returns stock-list JSON.
// English: It also distinguishes GetStockList (pz=10000 → diff map) from GetStockPE (pz=1 → diff array) by the clist pz parameter, and counts PE requests to verify TTL cache hits.
type listMockTransport struct {
	sinaFail bool
	peCalls  int
}

// RoundTrip 实现 http.RoundTripper：按 host 与参数路由返回 mock 响应，并统计 PE 请求次数（测试辅助）。
func (rt *listMockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	host := req.URL.Hostname()
	switch {
	case strings.Contains(host, "vip.stock.finance.sina.com.cn"):
		if rt.sinaFail {
			return testResp(500, "upstream error"), nil
		}
		return testResp(200,
			`[{"symbol":"sh600580","code":"600580","name":"卧龙电驱"},{"symbol":"sz300750","code":"300750","name":"宁德时代"}]`), nil
	case strings.Contains(host, "push2.eastmoney.com") && req.URL.Path == "/api/qt/clist/get":
		if req.URL.Query().Get("pz") == "1" {
			// GetStockPE 单查：diff 为数组（f9 市盈率）
			// English: GetStockPE single query: diff is an array (f9 = P/E ratio)
			rt.peCalls++
			price := "18.5"
			if strings.Contains(req.URL.Query().Get("fs"), "300750") {
				price = "22.1"
			}
			return testResp(200,
				`{"data":{"total":1,"diff":[{"f12":"A","f14":"X","f9":`+price+`}]}}`), nil
		}
		// GetStockList：diff 为 map
		// English: GetStockList: diff is a map
		return testResp(200,
			`{"data":{"total":2,"diff":{"0":{"f12":"600580","f14":"卧龙电驱","f9":18.5,"f20":1.2e11},"1":{"f12":"300750","f14":"宁德时代","f9":22.1,"f20":8e11}}}}`), nil
	}
	return testResp(404, ""), nil
}

// TestGetStockPEAndTTLCache PE 预取：东财 clist 单查 f9 市盈率；TTL 缓存命中时二次调用不再请求。
// English: TestGetStockPEAndTTLCache PE prefetch: Eastmoney clist single query of f9 P/E ratio; when the TTL cache hits, a second call makes no further request.
func TestGetStockPEAndTTLCache(t *testing.T) {
	rt := &listMockTransport{}
	m := NewMarketAPI()
	m.SetTransport(rt)

	pe := m.GetStockPE("600580")
	if pe <= 0 {
		t.Fatalf("GetStockPE 应>0, got %v", pe)
	}
	if diff := pe - 18.5; diff > 0.01 || diff < -0.01 {
		t.Errorf("600580 PE 应=18.5, got %.2f", pe)
	}

	// 第二次调用命中 TTL 缓存，不再发起网络请求
	// English: the second call hits the TTL cache and makes no network request
	if got := m.GetStockPE("600580"); got != pe {
		t.Errorf("二次调用应命中缓存返回 %.2f, got %.2f", pe, got)
	}
	if rt.peCalls != 1 {
		t.Errorf("PE 请求应仅1次(缓存命中), got %d", rt.peCalls)
	}
}

// TestGetStockListSinaPrimary 新浪主源成功时优先返回新浪列表。
// English: TestGetStockListSinaPrimary returns the Sina list first when the Sina primary source succeeds.
func TestGetStockListSinaPrimary(t *testing.T) {
	m := NewMarketAPI()
	m.SetTransport(&listMockTransport{sinaFail: false})
	list, err := m.GetStockList()
	if err != nil {
		t.Fatalf("GetStockList: %v", err)
	}
	if list["卧龙电驱"] != "600580" || list["宁德时代"] != "300750" {
		t.Errorf("新浪主源应返回2只, got %v", list)
	}
}

// TestGetStockListFallbackEastMoney 新浪失败时兜底东财列表（含 PE/市值字段解析）。
// English: TestGetStockListFallbackEastMoney falls back to the Eastmoney list when Sina fails (including PE/market-cap field parsing).
func TestGetStockListFallbackEastMoney(t *testing.T) {
	m := NewMarketAPI()
	m.SetTransport(&listMockTransport{sinaFail: true})
	list, err := m.GetStockList()
	if err != nil {
		t.Fatalf("兜底东财: %v", err)
	}
	if list["卧龙电驱"] != "600580" || list["宁德时代"] != "300750" {
		t.Errorf("东财兜底应返回2只, got %v", list)
	}
}

// TestMasLowSlope §MARKET_RISK_GATE P2：均线斜率口径（上行正/下行负/数据不足 NaN 弃权）。
func TestMasLowSlope(t *testing.T) {
	up := make([]float64, 120)
	for i := range up {
		up[i] = float64(i + 1)
	}
	if s := masLowSlope(up, 20, 5); !(s > 0) {
		t.Fatalf("上升序列斜率应>0, got %v", s)
	}
	down := make([]float64, 120)
	for i := range down {
		down[i] = float64(120 - i)
	}
	if s := masLowSlope(down, 60, 5); !(s < 0) {
		t.Fatalf("下降序列 MA60 斜率应<0, got %v", s)
	}
	flat := make([]float64, 120)
	for i := range flat {
		flat[i] = 100
	}
	if s := masLowSlope(flat, 20, 5); s != 0 {
		t.Fatalf("恒定序列斜率应为0, got %v", s)
	}
	if s := masLowSlope([]float64{1, 2, 3}, 60, 5); s == s {
		t.Fatalf("数据不足应返回 NaN, got %v", s)
	}
}

// ── §修复 EM-MIRROR / EM-F127 / TX-TURNOVER（20260920）单元测试 ──

// hostBodyTransport 按 host 返回固定响应体并统计各 host 调用次数。
// 未登记的 host 返回 404（在 getWithHeaders 中计为失败，从而触发镜像故障转移）。
// English: hostBodyTransport returns a fixed body per host and counts calls; unregistered hosts
// return 404, which getWithHeaders treats as a failure and thus triggers mirror failover.
type hostBodyTransport struct {
	body  map[string]string
	calls map[string]int
}

func (rt *hostBodyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	host := req.URL.Hostname()
	if rt.calls == nil {
		rt.calls = map[string]int{}
	}
	rt.calls[host]++
	if b, ok := rt.body[host]; ok {
		return testResp(200, b), nil
	}
	return testResp(404, ""), nil
}

// TestEmFailoverURLCandidates 主机故障转移只对镜像确有数据的路径生效。
// kline/fflow 在镜像上返回 rc:102（HTTP 200 + data:null），若纳入转移会顶掉同花顺/新浪第二源降级。
// English: failover candidates are produced only for paths the mirror actually serves; kline/fflow
// (rc:102 + data:null on the mirror) must stay single-candidate or the THS/Sina fallback is suppressed.
func TestEmFailoverURLCandidates(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int
	}{
		{"stock/get 可转移", "https://push2.eastmoney.com/api/qt/stock/get?secid=0.300489&fields=f43", 2},
		{"clist/get pz=1 (PE单查) 可转移", "https://push2.eastmoney.com/api/qt/clist/get?pn=1&pz=1&fields=f12,f9", 2},
		{"clist/get pz=100 (板块成分股) 可转移", "https://push2.eastmoney.com/api/qt/clist/get?pn=1&pz=100&fs=b:BK0739", 2},
		{"clist/get 缺 pz 可转移", "https://push2.eastmoney.com/api/qt/clist/get?pn=1&fs=m:90+t:2", 2},
		// 镜像对 clist 的 pz 硬封顶 100：超出即不得转移，否则静默截断（实测 pz=10000 只回 100 条）。
		{"clist/get pz=101 不可转移", "https://push2.eastmoney.com/api/qt/clist/get?pn=1&pz=101&fs=m:90+t:2", 1},
		{"clist/get pz=500 (板块列表) 不可转移", "https://push2.eastmoney.com/api/qt/clist/get?pn=1&pz=500&fs=m:90+t:2", 1},
		{"clist/get pz=10000 (全市场列表) 不可转移", "https://push2.eastmoney.com/api/qt/clist/get?pn=1&pz=10000", 1},
		{"kline 不可转移", "https://push2.eastmoney.com/api/qt/stock/kline/get?secid=0.300489", 1},
		// §修复 EM-FFLOW(20260920)：fflow 现可转移——镜像在带 klt/lmt 时返回 rc:0 且 klines 完整；
		// 上一版"不可转移"的断言源自缺参数的探测，已修正（见 emFailoverPaths 注释）。
		{"fflow 可转移", "https://push2.eastmoney.com/api/qt/stock/fflow/kline/get?secid=1.000001&klt=1&lmt=0", 2},
		{"push2ex 主机不转移", "https://push2ex.eastmoney.com/getTopicZTPool?dpt=wz.ztzt", 1},
		{"datacenter 主机不转移", "https://datacenter-web.eastmoney.com/api/data/v1/get", 1},
	}
	for _, c := range cases {
		got := emFailoverURLs(c.in)
		if len(got) != c.want {
			t.Errorf("%s: 候选数=%d want %d (%v)", c.name, len(got), c.want, got)
			continue
		}
		if got[0] != c.in {
			t.Errorf("%s: 首候选应为主域名原 URL, got %q", c.name, got[0])
		}
		if c.want == 2 {
			// 镜像候选必须只换主机：路径与 query（含 pz 等分页/字段参数）原样保留。
			wantMirror := strings.Replace(c.in, emPrimaryPush2Host, emMirrorPush2Host, 1)
			if got[1] != wantMirror {
				t.Errorf("%s: 镜像候选应为 %q, got %q", c.name, wantMirror, got[1])
			}
		}
	}
}

// TestEmMirrorFailoverKeepsServing 主域名不可达时经镜像取数；且主域名熔断打开后
// 不得连带跳过镜像（熔断 scope 必须含主机维度，否则一个死主机会让镜像也取不到数）。
// English: when the primary host is unreachable the mirror still serves data; once the primary's
// breaker trips, the mirror must NOT be skipped — the breaker scope must be host-scoped.
func TestEmMirrorFailoverKeepsServing(t *testing.T) {
	defer func() { DisableAll = false }()
	DisableAll = true

	rt := &hostBodyTransport{body: map[string]string{
		emMirrorPush2Host: `{"data":{"f57":"300489","f58":"光智科技","f127":"光学光电子","f128":"浙江板块"}}`,
	}}
	m := NewMarketAPI()
	m.SetTransport(rt)

	// 调用次数 > 阈值(5)，确保覆盖"主域名熔断打开之后"的调用。
	for i := 0; i < 8; i++ {
		if got := m.GetStockIndustry("300489"); got != "光学光电子" {
			t.Fatalf("第 %d 次取行业: got %q, want 光学光电子（主域名熔断后镜像仍应可用）", i+1, got)
		}
	}
	if rt.calls[emMirrorPush2Host] != 8 {
		t.Errorf("镜像应被调用 8 次, got %d", rt.calls[emMirrorPush2Host])
	}
	if n := rt.calls[emPrimaryPush2Host]; n != emBreakerThreshold {
		t.Errorf("主域名应在熔断阈值(%d)次失败后停止重试, got %d 次", emBreakerThreshold, n)
	}
}

// TestGetStockIndustryPrefersF127 f127=行业 优先；f127 缺失才回退 f128=地域板块。
// English: f127 (industry) wins; f128 (region board) is used only when f127 is absent.
func TestGetStockIndustryPrefersF127(t *testing.T) {
	defer func() { DisableAll = false }()
	DisableAll = true

	cases := []struct {
		name string
		body string
		want string
	}{
		{"f127 优先于 f128", `{"data":{"f127":"光学光电子","f128":"浙江板块"}}`, "光学光电子"},
		{"仅 f128 时回退", `{"data":{"f128":"浙江板块"}}`, "浙江板块"},
		{"两者皆空", `{"data":{"f127":"","f128":""}}`, ""},
	}
	for _, c := range cases {
		rt := &hostBodyTransport{body: map[string]string{emPrimaryPush2Host: c.body}}
		m := NewMarketAPI()
		m.SetTransport(rt)
		if got := m.GetStockIndustry("300489"); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

// TestTencentQuoteTurnoverAmount 腾讯字段 37=成交额(万元)、38=换手率(%)必须被解析。
// 东财不可达时咨询数据块的换手率曾恒为 0.00%，进而被反幻觉审计判为编造替换成 [数据缺失]。
// English: Tencent fields 37 (turnover value in 万元) and 38 (turnover rate %) must be parsed —
// with EastMoney down the consult block used to show 换手率 0.00% and the anti-hallucination audit
// replaced the model's real number with a missing-data marker.
func TestTencentQuoteTurnoverAmount(t *testing.T) {
	defer func() { DisableAll = false }()
	DisableAll = true

	// 88 字段与真实响应同长；仅设置解析器用到的下标（值取自 2026-09-20 光智科技实测）。
	f := make([]string, 88)
	f[1], f[2] = "TEST", "300489"
	f[3], f[4], f[5], f[6] = "224.62", "223.60", "227.00", "161198"
	f[32], f[33], f[34] = "0.46", "231.80", "215.02"
	f[37], f[38] = "359464", "11.70" // 成交额(万元) / 换手率(%)
	line := `v_sz300489="` + strings.Join(f, "~") + `";`

	rt := &hostBodyTransport{body: map[string]string{"qt.gtimg.cn": line}}
	m := NewMarketAPI()
	m.SetTransport(rt)

	si, err := m.getTencentQuote("300489")
	if err != nil {
		t.Fatalf("腾讯行情解析失败: %v", err)
	}
	if si.Turnover != 11.70 {
		t.Errorf("换手率: got %v want 11.70（腾讯字段 38）", si.Turnover)
	}
	if si.Amount != 359464e4 {
		t.Errorf("成交额: got %v want %v（腾讯字段 37，万元→元）", si.Amount, 359464e4)
	}
	if si.Volume != 16119800 {
		t.Errorf("成交量: got %v want 16119800（字段 6 手→股）", si.Volume)
	}
}

// TestParseSectorStocksDiffShapes clist 行集合位于 data.diff，数组与索引映射两种形态都必须解析。
// §修复 SECTOR-STOCKS(20260920)：旧实现读 clist 响应中不存在的 data.items，
// 导致 GetSectorStocks 恒返回空表且不报错（板块成分股/热点池静默为空）。
// English: clist rows live under data.diff; both the array and the index-keyed map form must parse.
// The old code read data.items (never present in a clist response), so GetSectorStocks always came
// back empty without an error.
func TestParseSectorStocksDiffShapes(t *testing.T) {
	cases := []struct {
		name string
		body string
		want int
	}{
		{"diff 数组形态", `{"data":{"total":2,"diff":[{"f12":"000157","f14":"中联重科","f2":620,"f3":65},{"f12":"000425","f14":"徐工机械","f2":745,"f3":-119}]}}`, 2},
		{"diff 索引映射形态", `{"data":{"total":2,"diff":{"0":{"f12":"000157"},"1":{"f12":"000425"}}}}`, 2},
		{"items 兼容别名", `{"data":{"total":1,"items":[{"f12":"000157"}]}}`, 1},
		{"两者皆空", `{"data":{"total":0}}`, 0},
		{"data 为 null", `{"rc":102,"data":null}`, 0},
	}
	for _, c := range cases {
		got, err := parseSectorStocks([]byte(c.body))
		if err != nil {
			t.Errorf("%s: 不应报错, got %v", c.name, err)
			continue
		}
		if len(got) != c.want {
			t.Errorf("%s: 行数=%d want %d", c.name, len(got), c.want)
		}
	}

	// 映射形态：按索引数值序还原顺序（保持服务端 po 排序语义），且字段换算正确。
	got, err := parseSectorStocks([]byte(`{"data":{"diff":{"1":{"f12":"B","f2":745,"f3":-119,"f7":2.39},"0":{"f12":"A","f2":620,"f3":65,"f7":1.14}}}}`))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(got) != 2 || got[0].Code != "A" || got[1].Code != "B" {
		t.Fatalf("映射形态应按索引序还原, got %+v", got)
	}
	if got[0].Price != 6.20 || got[0].ChangePct != 0.65 || got[0].Turnover != 1.14 || got[1].Turnover != 2.39 {
		t.Errorf("字段换算错误: got0=%+v got1=%+v", got[0], got[1])
	}

	// 空代码行必须跳过（避免脏行污染成分股列表）。
	if got, _ := parseSectorStocks([]byte(`{"data":{"diff":[{"f12":""},{"f12":"000157"}]}}`)); len(got) != 1 {
		t.Errorf("空代码行应被跳过, got %d 行", len(got))
	}
}

// TestParseMoneyFlowRealRowShape §修复 EM-FFLOW(20260920)：东财 fflow 实测行宽为 **6 列**
// （日期,主力净额,小单净额,中单净额,大单净额,超大单净额，单位=元），不是旧实现假设的 13 列 in/out 对。
// 旧实现按 13 列校验 → 在真实响应上恒定报 "fields too short (6)"，资金流因此长期整行缺失。
// 报文取自线上实测（300489，2026-09-18 15:00 累计值）。
// English: locks the real 6-column fflow row shape (the old 13-column assumption always errored).
func TestParseMoneyFlowRealRowShape(t *testing.T) {
	body := []byte(`{"rc":0,"data":{"code":"300489","name":"光智科技","klines":[
		"2026-09-18 14:59,202562373.0,-306067702.0,103505330.0,186881392.0,15680981.0",
		"2026-09-18 15:00,202562373.0,-306067702.0,103505330.0,186881392.0,15680981.0"]}}`)
	cf, err := parseMoneyFlow(body, "300489")
	if err != nil {
		t.Fatalf("parseMoneyFlow: %v", err)
	}
	// 取最后一根（15:00 累计）。
	if cf.NetInflow != 202562373 {
		t.Errorf("主力净流入应=202562373 元, got %.0f", cf.NetInflow)
	}
	if cf.SmallNet != -306067702 || cf.MediumNet != 103505330 {
		t.Errorf("小/中单净额错误: %.0f/%.0f", cf.SmallNet, cf.MediumNet)
	}
	if cf.LargeNet != 186881392 || cf.SuperLargeNet != 15680981 {
		t.Errorf("大/超大单净额错误: %.0f/%.0f", cf.LargeNet, cf.SuperLargeNet)
	}
	// 实测恒等式 ①：主力净 = 大净 + 超大净。
	if got := cf.LargeNet + cf.SuperLargeNet; got != cf.NetInflow {
		t.Errorf("主力净应=大净+超大净: %.0f vs %.0f", got, cf.NetInflow)
	}
	// 实测恒等式 ②：四档净额之和 ≈ 0（资金守恒，允许上游取整误差）。
	sum := cf.SmallNet + cf.MediumNet + cf.LargeNet + cf.SuperLargeNet
	if sum > 10 || sum < -10 {
		t.Errorf("四档净额之和应≈0, got %.0f", sum)
	}
	// 上游只给净额，In/Out 必须保持 0——消费方据此改用 *Net，不得用 In−Out 反推。
	if cf.SuperLargeIn != 0 || cf.SuperLargeOut != 0 || cf.SmallIn != 0 || cf.SmallOut != 0 {
		t.Errorf("fflow 不提供 in/out，应保持 0: %.0f/%.0f/%.0f/%.0f",
			cf.SuperLargeIn, cf.SuperLargeOut, cf.SmallIn, cf.SmallOut)
	}
	// 单位必须是"元"而非"万元"：旧实现乘 1e4，会把这个真实值放大 1 万倍。
	if cf.NetInflow > 1e9 {
		t.Errorf("净额被误当万元放大: %.0f", cf.NetInflow)
	}
}

// TestParseMoneyFlowShortRowRejected 行宽不足（旧 13 列表述不可能再出现）必须报错，不得静默取错列。
// English: rows shorter than the real 6-column shape must error rather than silently mis-index.
func TestParseMoneyFlowShortRowRejected(t *testing.T) {
	body := []byte(`{"data":{"klines":["2026-09-18 15:00,1,2,3"]}}`)
	if _, err := parseMoneyFlow(body, "300489"); err == nil {
		t.Fatal("行宽不足应返回错误")
	}
	if _, err := parseMoneyFlow([]byte(`{"data":{"klines":[]}}`), "300489"); err == nil {
		t.Fatal("空 klines 应返回错误")
	}
	// 行宽大于 6（如旧夹具的 13 列）时，只按**前 6 列**读净额——
	// 绝不退回"1..8 列是 in/out 对、第 10 列是主力净额"的旧解释（那套列语义上游根本不返回）。
	legacy := []byte(`{"data":{"klines":["2026-07-29,48000,8000,40000,16000,24000,24000,16000,32000,80000,0,0,0"]}}`)
	cf, err := parseMoneyFlow(legacy, "300489")
	if err != nil {
		t.Fatalf("行宽大于 6 时应按前 6 列解析: %v", err)
	}
	if cf.NetInflow != 48000 {
		t.Errorf("主力净额应取第 2 列 48000, got %.0f", cf.NetInflow)
	}
	if cf.SmallNet != 8000 || cf.MediumNet != 40000 {
		t.Errorf("小/中单净额应取第 3/4 列, got %.0f/%.0f", cf.SmallNet, cf.MediumNet)
	}
	if cf.SuperLargeIn != 0 || cf.SuperLargeOut != 0 {
		t.Errorf("不得按旧 in/out 语义解释列: in=%.0f out=%.0f", cf.SuperLargeIn, cf.SuperLargeOut)
	}
}

// TestEmFFlowURLHasRequiredParams §修复 EM-FFLOW(20260920)：fflow URL 必须带 klt/lmt，
// 缺这两个参数时镜像域名返回 rc:102 + data:null（实测 2026-09-20），资金流会整行缺失。
// English: the fflow URL must carry klt/lmt, otherwise the mirror answers rc:102 + data:null.
func TestEmFFlowURLHasRequiredParams(t *testing.T) {
	if emFFlowFields2 != "f51,f52,f53,f54,f55,f56" {
		t.Errorf("fields2 应为 6 个净额字段, got %q", emFFlowFields2)
	}
	if emFFlowKLType != 1 || emFFlowLimitAll != 0 {
		t.Errorf("klt/lmt 应为 1/0, got %d/%d", emFFlowKLType, emFFlowLimitAll)
	}
	m := NewMarketAPI()
	DisableAll = true
	defer func() { DisableAll = false }()
	rec := &urlRecorderTransport{body: `{"data":{"klines":["2026-09-18 15:00,1,2,3,4,5"]}}`}
	m.SetTransport(rec)
	if _, err := m.GetStockMoneyFlow("300489"); err != nil {
		t.Fatalf("GetStockMoneyFlow: %v", err)
	}
	gotURL := rec.lastURL()
	for _, want := range []string{"klt=1", "lmt=0", "fields2=f51,f52,f53,f54,f55,f56"} {
		if !strings.Contains(gotURL, want) {
			t.Errorf("请求 URL 缺少 %q: %s", want, gotURL)
		}
	}
}

// urlRecorderTransport 记录请求到的 URL 并回放固定响应体（用于断言出站请求形态）。
// urlRecorderTransport records requested URLs and replays a canned body.
type urlRecorderTransport struct {
	body string
	urls []string
}

func (t *urlRecorderTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.urls = append(t.urls, req.URL.String())
	return &http.Response{
		StatusCode: 200,
		Status:     "OK",
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(t.body)),
	}, nil
}

// lastURL 返回最后一次请求的 URL（无请求时为空串）。
func (t *urlRecorderTransport) lastURL() string {
	if len(t.urls) == 0 {
		return ""
	}
	return t.urls[len(t.urls)-1]
}

// ── §修复 EM-F62-MIRROR(20260920) ──

// emStockGetTransport 模拟 stock/get 请求：可按需令主站不可达（实测形态：TLS 握手被重置）
// 从而落到镜像；并在响应上回填 Request —— 生产 http.Client 会设置 Response.Request，
// f62 可信度判定正依赖它（缺省 testResp 不带 Request，故此处必须显式回填）。
type emStockGetTransport struct {
	primaryDown bool
	body        string
	hosts       []string
}

func (t *emStockGetTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.hosts = append(t.hosts, req.URL.Host)
	if t.primaryDown && req.URL.Host == emPrimaryPush2Host {
		return nil, fmt.Errorf("EOF (主站 TLS 握手被重置)")
	}
	r := testResp(200, t.body)
	r.Request = req
	return r, nil
}

// emStockGetBody 单股行情报文：f43=2727（27.27 元）、f62=2（镜像占位值）。
const emStockGetBody = `{"data":{"f43":2727,"f44":2763,"f45":2700,"f46":2725,` +
	`"f47":247123,"f48":672864810,"f58":"\u5367\u9f99\u7535\u9a71",` +
	`"f168":158,"f169":18,"f170":66,"f62":2}}`

// TestEastMoneyQuoteF62TrustedOnlyFromPrimary 主站服务时 f62 采信（HasFlow=true）。
func TestEastMoneyQuoteF62TrustedOnlyFromPrimary(t *testing.T) {
	tr := &emStockGetTransport{body: emStockGetBody}
	m := NewMarketAPI()
	m.SetTransport(tr)

	si, err := m.getEastMoneyQuote("600580")
	if err != nil {
		t.Fatalf("主站可用时应成功: %v", err)
	}
	if len(tr.hosts) != 1 || tr.hosts[0] != emPrimaryPush2Host {
		t.Fatalf("应只请求主站, got %v", tr.hosts)
	}
	if !si.HasFlow || si.NetInflow != 2 {
		t.Fatalf("主站服务的 f62 应采信: HasFlow=%v NetInflow=%v", si.HasFlow, si.NetInflow)
	}
}

// TestEastMoneyQuoteF62UntrustedFromMirror 镜像服务时 f62 必须弃用——不得把占位值 2
// 当成"主力净流入 2 元"透出（错而不报比缺数更危险）；但价/量等其余字段照常可用。
func TestEastMoneyQuoteF62UntrustedFromMirror(t *testing.T) {
	tr := &emStockGetTransport{primaryDown: true, body: emStockGetBody}
	m := NewMarketAPI()
	m.SetTransport(tr)

	si, err := m.getEastMoneyQuote("600580")
	if err != nil {
		t.Fatalf("主站不可达时应降级镜像成功: %v", err)
	}
	if len(tr.hosts) != 2 || tr.hosts[1] != emMirrorPush2Host {
		t.Fatalf("应先主站后镜像, got %v", tr.hosts)
	}
	if si.HasFlow || si.NetInflow != 0 {
		t.Fatalf("镜像服务的 f62 为已知占位值，必须弃用: HasFlow=%v NetInflow=%v", si.HasFlow, si.NetInflow)
	}
	// 弃用 f62 不得牵连其余字段
	if si.Price != 27.27 || si.High != 27.63 || si.Low != 27.00 || si.Turnover != 1.58 {
		t.Fatalf("除 f62 外字段应照常解析: %+v", si)
	}
}
