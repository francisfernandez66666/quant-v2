// market_index_test.go — §INDEX-BLIND（2026-11-02）指数多源腿的行为用例。
//
// 这组用例盯的是五件事，全部来自 11-02 现网那次"整天封顶半仓"的形状：
//  1. 段序真的按 东财 → 新浪 → 腾讯 走，且**开关关掉时一条后备腿都不拨**（缺省=今天的字节行为）；
//  2. 指数绝不按个股解析：`000001` 在新浪/腾讯的行里与平安银行同码，取错就是"看起来有数、数字全错"；
//  3. 指数的量/额一律不带（实测同日新浪腾讯对指数返回同一个 565372836，个股侧单位却一边股一边手）；
//  4. 熔断域独立：个股的连败不许把指数这条腿一起 fast-fail 掉（现网 14:33:11 的错误原文）；
//  5. 三条腿全断且 LKG 过窗 ⇒ 必须如实返回 error（上层据此判盲区）——fail-closed 没被放宽。
//
// 网络用 SetTransport 注入假 RoundTripper 替掉（限流与熔断逻辑留在被测代码里，桩只替掉"发包"这一步），
// 因此这里既不打真上游，也不会因为上游抖动在 CI 里假红。
package data

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"quant-trading-v2/internal/config"
)

// fakeRT 按主机返回预制响应或错误，并记录每个主机的请求次数与最后一次请求行。
type fakeRT struct {
	mu     sync.Mutex
	byHost map[string][]byte // 主机 → 响应体（nil 值表示该主机连接失败）
	hits   map[string]int
	paths  map[string]string
}

// RoundTrip 按 URL 主机命中预制体；未列出的主机视为连接失败（=EOF 形态，与现网一致）。
// 响应体必须真的带内容：doWithHeaders 会读体并判"200+空体＝失败"（§N-12b），
// 桩如果给 NoBody，测的就不是"上游可达"而是"上游回空体"，形状完全不同。
func (f *fakeRT) RoundTrip(req *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	host := req.URL.Host
	f.hits[host]++
	f.paths[host] = req.URL.Path + "?" + req.URL.RawQuery
	body, ok := f.byHost[host]
	if !ok || body == nil {
		return nil, errors.New("remote end closed connection without response")
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(body)),
		// ContentLength 必须给真实长度：为 0 会被空体判据拦下（同上）。
		ContentLength: int64(len(body)),
		Header:        http.Header{"Content-Type": []string{"application/json"}},
		Request:       req,
	}, nil
}

// newFakeMarket 造一台带假网络的 MarketAPI 并按开关装配（默认两条腿都开）。
func newFakeMarket(t *testing.T, byHost map[string][]byte, fallbackOn, scopeOn bool) (*MarketAPI, *fakeRT) {
	t.Helper()
	rt := &fakeRT{byHost: byHost, hits: map[string]int{}, paths: map[string]string{}}
	m := NewMarketAPI()
	m.SetTransport(rt)
	m.SetIndexSourcePolicy(fallbackOn, scopeOn)
	return m, rt
}

// hostHits 取某主机的请求次数（加锁读，桩里唯一的共享状态）。
func (f *fakeRT) hostHits(host string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits[host]
}

// lastPath 取某主机最后一次请求的 path+query（URL 形状的现场证据，不看代码猜）。
func (f *fakeRT) lastPath(host string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.paths[host]
}

// eastMoneyBody 东财指数量预制响应（f43 现价 ×100、f60 昨收 ×100）。
const eastMoneyBody = `{"rc":0,"data":{"f43":363457,"f60":359902,"f58":"SSE"}}`

// sinaIndexBody 新浪指数行（字段位次与个股同构：1 今开 2 昨收 3 现价；量/额在 8/9，故意给非零值，
// 用来断言指数腿**不把量/额带出去**）。
const sinaIndexBody = `var hq_str_sh000001="SSE INDEX,3614.3110,3599.0166,3638.0310,3648.1327,3594.7682,0,0,565372836,847459066881,2026-11-02,15:09:54,00,";` + "\n"

// sinaMixedBody 同一份响应里既有上证指数又有同码个股（平安银行 11.54）——按市场前缀选行才不张冠李戴。
const sinaMixedBody = `var hq_str_sz000001="PING AN,11.600,11.640,11.540,11.740,11.470,11.53,11.54,159436145,1850311247.960,";` + "\n" +
	`var hq_str_sh000001="SSE INDEX,3614.3110,3599.0166,3638.0310,3648.1327,3594.7682,0,0,565372836,847459066881,";` + "\n"

// tencentIndexBody 腾讯指数行（3 现价、4 昨收、5 今开，与个股同位；涨跌幅字段 32 一律不取）。
const tencentIndexBody = `v_sh000001="1~SSE INDEX~000001~3638.03~3599.02~3614.31~565372836~0~0~0.00~0~0.00~0~0.00~0~0.00~0~0.00~0~0.00~0~0.00~0~0.00~0~0.00~0~~20261102150900~39.01~1.08~3648.13~3594.77~3638.03~";` + "\n"

// TestIndexMarketPrefixWhitelist 指数前缀白名单逐段判定；个股必须拒收。
// 这条是"宁可拿不到，不可拿错"的闸：白名单一旦放进个股，指数解析器会按点位口径去解一只银行股。
func TestIndexMarketPrefixWhitelist(t *testing.T) {
	cases := []struct {
		code string
		want string // "" = 不认
	}{
		{"000001", "sh"}, {"000300", "sh"}, {"000905", "sh"}, {"999999", "sh"}, {"000001.SH", "sh"},
		{"399001", "sz"}, {"399006", "sz"},
		{"600519", ""}, {"000001.SZ", ""}, {"300750", ""}, {"", ""},
	}
	for _, c := range cases {
		got, ok := indexMarketPrefix(c.code)
		if c.want == "" {
			if ok {
				t.Fatalf("个股/非法码 %q 不该被指数白名单收下（得 %q）", c.code, got)
			}
			continue
		}
		if !ok || got != c.want {
			t.Fatalf("指数 %q 应解析为 %q，得 %q ok=%v", c.code, c.want, got, ok)
		}
	}
}

// TestIndexFallbackChainOrder 东财失败时按段序退到新浪，并且**不越过去拨腾讯**（第一条能上就不浪费请求）。
func TestIndexFallbackChainOrder(t *testing.T) {
	m, rt := newFakeMarket(t, map[string][]byte{
		"push2.eastmoney.com":      nil, // EOF 形态（现网 14:17 起就是这个）
		"push2delay.eastmoney.com": nil,
		"hq.sinajs.cn":             []byte(sinaIndexBody),
		"qt.gtimg.cn":              []byte(tencentIndexBody),
	}, true, true)
	q, err := m.GetIndexLiveQuote("000001")
	if err != nil {
		t.Fatalf("新浪腿应能给出点位：%v", err)
	}
	if q.Source != IndexSourceSina {
		t.Fatalf("Source 应如实记 sina，得 %q", q.Source)
	}
	if q.Price != 3638.0310 || q.PrevClose != 3599.0166 {
		t.Fatalf("点位/昨收取错：%.4f/%.4f", q.Price, q.PrevClose)
	}
	// 涨跌幅按 (现价−昨收)/昨收 自算：3638.0310/3599.0166 → +1.084%，与东财同口径
	if diff := q.ChangePct - 1.084; diff > 0.01 || diff < -0.01 {
		t.Fatalf("涨跌幅口径漂了：%.4f（期望≈1.084）", q.ChangePct)
	}
	if q.Stale {
		t.Fatalf("真实读数不许标 Stale")
	}
	if n := rt.hostHits("qt.gtimg.cn"); n != 0 {
		t.Fatalf("新浪已命中，仍去拨腾讯 %d 次（段序应短路）", n)
	}
}

// TestIndexFallbackOffIsEastMoneyOnly 开关关掉＝今天行为：一条后备腿都不发起。
// 这是每枚新键都该有的 equals-legacy 腿，"默认关"必须是**真不发包**，不是发了再丢结果。
func TestIndexFallbackOffIsEastMoneyOnly(t *testing.T) {
	m, rt := newFakeMarket(t, map[string][]byte{
		"push2.eastmoney.com":      nil,
		"push2delay.eastmoney.com": nil,
		"hq.sinajs.cn":             []byte(sinaIndexBody),
		"qt.gtimg.cn":              []byte(tencentIndexBody),
	}, false, false)
	if _, err := m.GetIndexLiveQuote("000001"); err == nil {
		t.Fatalf("全段失败应返回 error（上层据此判盲区）")
	}
	if n := rt.hostHits("hq.sinajs.cn"); n != 0 {
		t.Fatalf("开关关掉却仍拨新浪 %d 次", n)
	}
	if n := rt.hostHits("qt.gtimg.cn"); n != 0 {
		t.Fatalf("开关关掉却仍拨腾讯 %d 次", n)
	}
	// 出厂态也必须与关掉时不同（证明"出厂开"真的是开），并顺手锁住出厂段序
	if got := config.IndexFallbackOrder(); strings.Join(got, ",") != "eastmoney,sina,tencent" {
		t.Fatalf("段序真源漂了：%v", got)
	}
	m2, _ := newFakeMarket(t, map[string][]byte{"hq.sinajs.cn": []byte(sinaIndexBody)}, true, true)
	if q, err := m2.GetIndexLiveQuote("000001"); err != nil || q.Source != IndexSourceSina {
		t.Fatalf("出厂档（键缺省=开）应能用新浪兜住，得 %+v err=%v", q, err)
	}
}

// TestIndexZeroValueMarketAPIStaysFactoryOn 零值 MarketAPI（测试与老装配点的 `&MarketAPI{}`）
// 必须走出厂开——开关按"是否被显式关掉"存储，就是为了这条。
func TestIndexZeroValueMarketAPIStaysFactoryOn(t *testing.T) {
	rt := &fakeRT{byHost: map[string][]byte{"hq.sinajs.cn": []byte(sinaIndexBody)},
		hits: map[string]int{}, paths: map[string]string{}}
	m := &MarketAPI{}
	m.client = &http.Client{Timeout: time.Second}
	m.SetTransport(rt)
	q, err := m.GetIndexLiveQuote("000001")
	if err != nil || q == nil || q.Source != IndexSourceSina {
		t.Fatalf("零值 MarketAPI 应走出厂开，得 %+v err=%v", q, err)
	}
}

// TestIndexNotParsedAsStock 响应里同时有上证指数与同码个股时，必须按"市场前缀+代码"选行。
// 直接钉住 §BUG-INDEX 那笔账：000001 拿到 11.54（平安银行）而日志全绿，是最难发现的一类错。
func TestIndexNotParsedAsStock(t *testing.T) {
	m, _ := newFakeMarket(t, map[string][]byte{
		"push2.eastmoney.com":      nil,
		"push2delay.eastmoney.com": nil,
		"hq.sinajs.cn":             []byte(sinaMixedBody),
	}, true, true)
	q, err := m.GetIndexLiveQuote("000001")
	if err != nil {
		t.Fatalf("应能从混合响应里取到指数行：%v", err)
	}
	if q.Price == 11.54 || q.Price < 1000 {
		t.Fatalf("拿成了同码个股：%.4f（平安银行 11.54）", q.Price)
	}
	// 腾讯侧同理：v_sz000001 与 v_sh000001 混在一份响应里
	mt, _ := newFakeMarket(t, map[string][]byte{
		"push2.eastmoney.com":      nil,
		"push2delay.eastmoney.com": nil,
		"hq.sinajs.cn":             nil,
		"qt.gtimg.cn": []byte(`v_sz000001="51~PING AN~000001~11.54~11.64~11.60~1594361` +
			strings.Repeat("~0", 40) + `~";` + "\n" + tencentIndexBody),
	}, true, true)
	q2, err := mt.GetIndexLiveQuote("000001")
	if err != nil {
		t.Fatalf("腾讯混合响应应能取到指数行：%v", err)
	}
	if q2.Price != 3638.03 {
		t.Fatalf("腾讯侧拿错行：%.4f", q2.Price)
	}
}

// TestIndexCarriesNoVolumeOrAmount 指数腿不带量/额：上游给什么都不取。
// 实测同日新浪与腾讯对指数返回**同一个** 565372836，而个股侧一边是股、一边是手（腾讯 ×100）
// ⇒ 指数的量单位不可信；VWAP/换手/流动性一旦吃了它就整体错一档。
func TestIndexCarriesNoVolumeOrAmount(t *testing.T) {
	m, _ := newFakeMarket(t, map[string][]byte{
		"push2.eastmoney.com":      nil,
		"push2delay.eastmoney.com": nil,
		"hq.sinajs.cn":             []byte(sinaIndexBody), // 第 8/9 字段刻意给了量/额
	}, true, true)
	si, err := m.GetIndexQuote("000001")
	if err != nil {
		t.Fatalf("GetIndexQuote 应成功：%v", err)
	}
	if si.Volume != 0 || si.Amount != 0 {
		t.Fatalf("指数读数把量/额带出来了：Volume=%.0f Amount=%.0f", si.Volume, si.Amount)
	}
	if si.Name != "上证指数" {
		t.Fatalf("指数载体名称不该变：%q", si.Name)
	}
	if si.Price <= 0 || si.PrevClose <= 0 {
		t.Fatalf("价类字段必须齐（点位 %.2f 昨收 %.2f）", si.Price, si.PrevClose)
	}
}

// TestIndexBreakerScopeIsolatedFromStocks 指数独立熔断域：个股那两格（主站+镜像）熔断了，
// 指数这条腿仍应真的发请求；开关关掉时则必须像今天一样被 fast-fail 挡掉。
func TestIndexBreakerScopeIsolatedFromStocks(t *testing.T) {
	stockScopes := []string{"push2.eastmoney.com/api/qt/stock/get", "push2delay.eastmoney.com/api/qt/stock/get"}
	// 先证明"熔断开着"这件事本身可复现：逐格连败到阈值
	blow := func(m *MarketAPI) {
		for _, s := range stockScopes {
			for i := 0; i < 8; i++ {
				m.emRecord(s, errors.New("EOF"))
			}
			if m.emAllow(s) {
				t.Fatalf("scope %s 连败 8 次后仍放行（阈值/冷却形态与预期不符）", s)
			}
		}
	}
	hosts := map[string][]byte{"hq.sinajs.cn": []byte(sinaIndexBody)}
	mOn, rtOn := newFakeMarket(t, hosts, false, true) // 只开独立熔断域，后备腿关着，隔离变量
	blow(mOn)
	if _, err := mOn.GetIndexLiveQuote("000001"); err == nil {
		t.Fatalf("东财被桩成 EOF 却返回成功，说明请求没走失败路径")
	}
	// 开独立域：东财两格熔断不影响指数 ⇒ 指数请求真的发出去了（下面那行 hits>0 是唯一证据）
	if n := rtOn.hostHits("push2.eastmoney.com"); n == 0 {
		t.Fatalf("独立熔断域开着，指数请求却被个股的熔断挡掉了（一次都没发）")
	}
	// 关独立域＝今天：个股两格熔断 ⇒ 指数被 fast-fail，一个包都不发
	mOff, rtOff := newFakeMarket(t, hosts, false, false)
	blow(mOff)
	if _, err := mOff.GetIndexLiveQuote("000001"); err == nil {
		t.Fatalf("共享名额下应失败")
	}
	if n := rtOff.hostHits("push2.eastmoney.com"); n != 0 {
		t.Fatalf("开关关掉后仍发出 %d 次请求（共享熔断名额的形态没锁住）", n)
	}
}

// TestIndexLKGKeepsOriginalSource 全段失败时回退 LKG，且 Source 保留"这份陈旧值当初是谁给的"，
// 不折成 lkg——处置方向取决于哪条腿断了。
func TestIndexLKGKeepsOriginalSource(t *testing.T) {
	m, _ := newFakeMarket(t, map[string][]byte{"hq.sinajs.cn": []byte(sinaIndexBody)}, true, true)
	first, err := m.GetIndexLiveQuote("000001")
	if err != nil || first.Source != IndexSourceSina {
		t.Fatalf("预置 LKG 失败：%+v err=%v", first, err)
	}
	// 把上游全部改成失败：下一次读到的应是同一份真实值，只是标了 Stale
	m.SetTransport(&fakeRT{byHost: map[string][]byte{}, hits: map[string]int{}, paths: map[string]string{}})
	second, err := m.GetIndexLiveQuote("000001")
	if err != nil {
		t.Fatalf("10 分钟窗口内应回退 last-known-good：%v", err)
	}
	if !second.Stale || second.Source != IndexSourceSina {
		t.Fatalf("陈旧回退必须既标 Stale 又保留来源：stale=%v source=%q", second.Stale, second.Source)
	}
	if second.Price != first.Price {
		t.Fatalf("回退值与上一份真实值不一致：%.4f vs %.4f", second.Price, first.Price)
	}
}

// TestIndexAllLegsFailPastLKGWindow 连 LKG 都没有（或已过窗）才返回 error；
// 这条是"封顶半仓 + 今日不做 T"的根判据，不许被"多一条腿"顺手放宽。
func TestIndexAllLegsFailPastLKGWindow(t *testing.T) {
	m, _ := newFakeMarket(t, map[string][]byte{}, true, true)
	if _, err := m.GetIndexLiveQuote("000001"); err == nil {
		t.Fatalf("从未成功过时必须返回 error（NaN 只代表连 LKG 都没有）")
	}
	// 预置一份**已经过窗**的 LKG：同样必须失败，而不是无限期吃旧价
	m.idxLiveMu.Lock()
	m.idxLiveLKG = &idxLiveLKGEntry{
		q:  IndexLiveQuote{Price: 3638.03, PrevClose: 3599.02, ChangePct: 1.08, Source: IndexSourceSina},
		at: time.Now().Add(-indexLKGMaxAge - time.Minute),
	}
	m.idxLiveMu.Unlock()
	if _, err := m.GetIndexLiveQuote("000001"); err == nil {
		t.Fatalf("过窗的 LKG 不该再当读数用（超窗仍判盲区）")
	}
}

// TestIndexSourceURLs 各段真正请求的 URL 形状。
// 为什么不能只拼字符串自证：前缀/secid 的错误只在"发出去的地址"上才成立，
// 所以这里从桩里回看实际请求行（东财 secid=1.000001、新浪 list=sh000001、腾讯 q=sh000001）。
func TestIndexSourceURLs(t *testing.T) {
	if got := indexEastMoneyURL(indexSecID("399001")); !strings.Contains(got, "secid=0.399001") {
		t.Fatalf("深市指数 secid 前缀应取 0.：%s", got)
	}
	m, rt := newFakeMarket(t, map[string][]byte{
		"hq.sinajs.cn": []byte(sinaIndexBody),
		"qt.gtimg.cn":  []byte(tencentIndexBody),
	}, true, true)
	if _, err := m.GetIndexLiveQuote("000001"); err != nil {
		t.Fatalf("应退到新浪：%v", err)
	}
	// 东财第一段失败的请求形状（secid 与字段都必须是指数量那套）
	emURL := rt.lastPath("push2.eastmoney.com")
	if !strings.Contains(emURL, "secid=1.000001") || !strings.Contains(emURL, "fields=f43,f60,f58") {
		t.Fatalf("东财指数 URL 形状变了（会拿成深市同码个股）：%s", emURL)
	}
	if got := rt.lastPath("hq.sinajs.cn"); !strings.Contains(got, "list=sh000001") {
		t.Fatalf("新浪腿必须显式带 sh 前缀（否则 sz000001＝平安银行）：%s", got)
	}
	// 腾讯腿同样按前缀请求（用备用实例直接命中它）
	m2, rt2 := newFakeMarket(t, map[string][]byte{
		"hq.sinajs.cn": nil,
		"qt.gtimg.cn":  []byte(tencentIndexBody),
	}, true, true)
	if _, err := m2.GetIndexLiveQuote("000001"); err != nil {
		t.Fatalf("应退到腾讯：%v", err)
	}
	if got := rt2.lastPath("qt.gtimg.cn"); !strings.Contains(got, "q=sh000001") {
		t.Fatalf("腾讯腿必须显式带 sh 前缀：%s", got)
	}
}

// ── §A7-3 券商侧指数：独立后缀白名单 + 独立分账 ─────────────────────────────────

// TestIndexSuffixNotPolluted 个股后缀函数的逐段行为必须与 golden 完全不变（它被下单/风控/涨跌停共用），
// 而指数后缀只能由 IndexSuffix 给出——两者共用一个函数就等于把交易链拖进指数改造。
func TestIndexSuffixNotPolluted(t *testing.T) {
	// 个股侧：ExchangeSuffix 的行为一个字都不许动（这里按现网实测钉住关键几档）
	stockCases := map[string]string{
		"600519": "600519.SH", "000001": "000001.SZ", "300750": "300750.SZ",
		"002631": "002631.SZ", "830799": "830799.BJ", "600000.SH": "600000.SH",
	}
	for in, want := range stockCases {
		if got := ExchangeSuffix(in); got != want {
			t.Fatalf("个股后缀函数被改动：%s → %s（应为 %s）", in, got, want)
		}
	}
	// 指数侧：同一个裸码 000001 必须给出 .SH，且个股函数保持 .SZ（两本账分家的现场证明）
	if got, ok := IndexSuffix("000001"); !ok || got != "000001.SH" {
		t.Fatalf("指数后缀应为 000001.SH，得 %q ok=%v", got, ok)
	}
	if _, ok := IndexSuffix("600519"); ok {
		t.Fatalf("个股代码不该被指数白名单收下")
	}
	if got, ok := IndexSuffix("399001"); !ok || got != "399001.SZ" {
		t.Fatalf("深市指数后缀应为 399001.SZ，得 %q ok=%v", got, ok)
	}
}

// TestBrokerIndexCodes 券商侧请求的固定集合：全部带后缀、全在白名单里。
func TestBrokerIndexCodes(t *testing.T) {
	codes := BrokerIndexCodes()
	if len(codes) == 0 {
		t.Fatalf("指数请求集合为空（§A7-3 的腿没接上）")
	}
	for _, c := range codes {
		if _, ok := IndexSuffix(c); !ok {
			t.Fatalf("请求集合里出现非指数代码 %q", c)
		}
	}
}

// TestBrokerIndexLedgerIsolated 券商指数 tick 记在**独立分账**里：
// 不进快照 Stocks、不进覆盖率分母、关掉开关即清空（回滚必须在读数上看得见）。
func TestBrokerIndexLedgerIsolated(t *testing.T) {
	f := NewFetcher([]string{"600519"}, NewMarketAPI(), nil)
	f.SetFeedPolicy(FeedPolicy{Enabled: true, Primary: true, MaxAge: 30 * time.Second, IndexEnabled: true})
	tk := map[string]QMTTick{
		"000001.SH": {LastPrice: 3638.03, PrevClose: 3599.02, TickTime: time.Now().UnixMilli()},
		"000001.SZ": {LastPrice: 11.54, PrevClose: 11.64, TickTime: time.Now().UnixMilli()}, // 不在请求集合：不该入账
		"600519":    {LastPrice: 1500, PrevClose: 1490, TickTime: time.Now().UnixMilli()},   // 个股：不该进指数账
	}
	f.NoteFeedIndexTicks(tk)
	q, ok := f.HubIndexQuote("000001")
	if !ok || q.Price != 3638.03 || q.Source != IndexSourceBroker {
		t.Fatalf("应取到上证点位，得 %+v ok=%v", q, ok)
	}
	if diff := q.ChangePct - (3638.03-3599.02)/3599.02*100; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("涨跌幅口径应与外部腿一致，得 %.6f", q.ChangePct)
	}
	// 独立分账：不进快照，也不因指数而多出票
	if snap := f.Snapshot(); snap != nil {
		if si, has := snap.Stocks["000001"]; has && si != nil && si.Price > 1000 {
			t.Fatalf("指数点位漏进了个股快照（会把 3638.03 当成一只票的价）：%+v", si)
		}
	}
	// 关掉开关必须立刻不再给点位，且残留被清掉（回滚要在读数上看得见）
	f.SetFeedPolicy(FeedPolicy{Enabled: true, Primary: true, MaxAge: 30 * time.Second, IndexEnabled: false})
	if _, ok := f.HubIndexQuote("000001"); ok {
		t.Fatalf("IndexEnabled=false 却仍给出券商点位")
	}
	if n := len(f.hub().idxEnts); n != 0 {
		t.Fatalf("开关关掉后指数分账没清（残留 %d 条），下次打开会先吐一份旧点位", n)
	}
}

// TestBrokerIndexStaleIsAbsent 超龄的券商指数 tick 当"没有"处理：
// 拿不到会走外部腿或判盲区，而挂着一小时前的旧点位会算出一个看起来正常的 MS。
func TestBrokerIndexStaleIsAbsent(t *testing.T) {
	f := NewFetcher([]string{"600519"}, NewMarketAPI(), nil)
	f.SetFeedPolicy(FeedPolicy{Enabled: true, MaxAge: 2 * time.Second, IndexEnabled: true})
	f.NoteFeedIndexTicks(map[string]QMTTick{"000001.SH": {LastPrice: 3638.03, PrevClose: 3599.02,
		TickTime: time.Now().Add(-time.Minute).UnixMilli()}})
	if _, ok := f.HubIndexQuote("000001"); ok {
		t.Fatalf("超龄 tick 不该当有效点位用")
	}
	// 没有时刻的 tick 同样不算（0＝网关没给，绝不折成"现在"）
	f.NoteFeedIndexTicks(map[string]QMTTick{"000001.SH": {LastPrice: 3638.03, PrevClose: 3599.02}})
	if _, ok := f.HubIndexQuote("000001"); ok {
		t.Fatalf("tickTime=0 不该被当成新鲜读数")
	}
}

// TestIndexBrokerLegFirstAndNotStock 注入券商来源后它排在最前；而"平安银行"那种
// 同码不同所的路径永远进不了指数账（TestIndexSuffixNotPolluted 管键，这条管取数顺序）。
func TestIndexBrokerLegFirstAndNotStock(t *testing.T) {
	m := NewMarketAPI()
	m.SetTransport(&fakeRT{byHost: map[string][]byte{"push2.eastmoney.com": []byte(eastMoneyBody)},
		hits: map[string]int{}, paths: map[string]string{}})
	seen := 0
	m.SetIndexBrokerSource(func(code string) (*IndexLiveQuote, bool) {
		seen++
		return &IndexLiveQuote{Price: 3640.10, PrevClose: 3599.02, Source: IndexSourceBroker}, true
	})
	q, err := m.GetIndexLiveQuote("000001")
	if err != nil {
		t.Fatalf("券商段应命中：%v", err)
	}
	if q.Source != IndexSourceBroker || q.Price != 3640.10 {
		t.Fatalf("段序没把券商排最前：%+v", q)
	}
	if seen != 1 {
		t.Fatalf("券商段应只被调一次，得 %d", seen)
	}
	// 来源没注入（nil）时完全退出：外部三段照常，且不因为"开着开关却没通道"而报错
	m2 := NewMarketAPI()
	m2.SetTransport(&fakeRT{byHost: map[string][]byte{"push2.eastmoney.com": []byte(eastMoneyBody)},
		hits: map[string]int{}, paths: map[string]string{}})
	m2.SetIndexSourcePolicy(true, true)
	if q2, err := m2.GetIndexLiveQuote("000001"); err != nil || q2.Source != IndexSourceEastMoney {
		t.Fatalf("未注入券商来源时应照走东财：%+v err=%v", q2, err)
	}
}
