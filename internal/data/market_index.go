// market_index.go — §INDEX-BLIND（2026-11-02）大盘指数的多源实时腿与指数专用代码前缀。
//
// 为什么单独成文件（而不是往 market.go 的个股函数里加两支）：
//  1. **指数在个股链里必然是错的**，而且错得好看：`000001` 经个股前缀函数拼成 `sz000001`＝平安银行
//     （market.go:619 sinaQuoteURL、market.go:1017 getTencentQuotes、tencent.go:26 tencentPrefix
//     一律"首位 6/5→sh，否则 sz"），而 2026-10-27 的 §BUG-INDEX 已经为此付过一整天的代价
//     （大盘跌幅因子/趋势破位/止损偏移一直在看一只银行股）。这三处被下单/风控/涨跌停共用，
//     **一个字节都不许改**，所以指数只能有自己的入口。
//  2. 11-02 现网读数（不是设想）：东财 push2 与镜像 push2delay 对这台机器间歇 TLS 重置（EOF），
//     而 `GetIndexLiveQuote` 只有东财一族 + 10 分钟 last-known-good ⇒ 14:36 起 IndexChangePct=NaN，
//     目标仓位封顶半仓、当天不做 T，全天 545/859 条决策快照在盲区态。同机实测新浪与腾讯的
//     指数接口**都是 200**，字段位次与个股解析器逐位对齐（新浪 34 字段：1 开 2 昨收 3 现价；
//     腾讯 88 字段：3 现价 4 昨收 32 涨跌幅）——所以这两条腿是现成的，缺的只是"敢不敢按指数解"。
//  3. 熔断域：东财熔断 scope=host+path，指数量 URL 与个股实时价同 host 同 path
//     `/api/qt/stock/get` ⇒ 个股每轮几百次的连败会把指数这条腿一起 fast-fail 掉
//     （14:33:11 现网错误原文即 `circuit-breaker open (push2delay.eastmoney.com/api/qt/stock/get)`）。
//
// 三条硬纪律（改动前先读）：
//   - 指数腿**只取价与涨跌幅**：实测同日新浪与腾讯对指数返回同一个成交量数字（565372836），
//     而个股侧一边是"股"、一边是"手"（腾讯解析 ×100 归一）⇒ 指数的量/额单位不可信，
//     绝不允许进 VWAP、换手、流动性任何一处。
//   - 指数结果**不进任何 `map[string]*StockInfo`**：两条链的解析正则捕获的是裸 6 位码，
//     指数与平安银行同为 `000001`，共用 map 会让两只互相覆盖（谁赢取决于响应顺序）。
//   - 降级链的段序只有一个真源（IndexLegOrderFor），卡面回显与实际试序不许各列一份。
//
// English: index-only live quote legs (EastMoney → Sina → Tencent) behind one orchestrator.
// Stock-side prefix helpers are deliberately untouched because 000001 must never resolve to
// Ping An Bank; index volume/amount are never propagated since the two free sources disagree on
// units for indices.
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

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

// indexMarketPrefix 指数代码 → 市场前缀白名单。
// 只认指数段：沪市 000/899/999（000001 上证、000300 沪深 300、000905 中证 500、999999 上证指数另一路写法），
// 深市 399（399001 深证成指、399006 创业板指）。个股一律 false——**宁可拿不到，不可拿错**。
func indexMarketPrefix(code string) (string, bool) {
	raw := strings.TrimSpace(code)
	c := stripSuffix(raw)
	var pfx string
	switch {
	case strings.HasPrefix(c, "000"), strings.HasPrefix(c, "899"), strings.HasPrefix(c, "999"):
		pfx = "sh"
	case strings.HasPrefix(c, "399"):
		pfx = "sz"
	default:
		return "", false
	}
	// 显式后缀必须与段判定一致：`000001.SZ` 是平安银行、不是上证综指，
	// 这种"自我矛盾"的入参一律拒收——宁可不取点位，也不许给指数挂上错的交易所。
	// §BUG-INDEX（2026-10-27）的教训就是矛盾形态看起来还是有数，其实数字全错、日志全绿。
	if strings.HasSuffix(raw, ".SH") && pfx != "sh" {
		return "", false
	}
	if strings.HasSuffix(raw, ".SZ") && pfx != "sz" {
		return "", false
	}
	return pfx, true
}

// IndexSourceBroker 券商 L1 给的指数点位（§A7-3）。值与 QuoteSourceQMTL1 同串，
// 这样卡面/日志里"谁给的价"与"谁给的点位"是同一套词，不需要第二张映射表。
const IndexSourceBroker = QuoteSourceQMTL1

// IndexSuffix 指数代码 → 带后缀的券商代码（000001→000001.SH、399001→399001.SZ）。
//
// **独立于 ExchangeSuffix**（exchange.go:24）：那个函数按首位数字判交易所，
// 裸 `000001` 得到 `000001.SZ`＝平安银行——它在下单/风控/涨跌停里都是对的，
// 恰恰在指数上是错的。A7-3 的验收用例就叫 TestIndexSuffixNotPolluted，
// 因为这两条腿一旦共用，改动面立刻扩到交易链。
func IndexSuffix(code string) (string, bool) {
	pfx, ok := indexMarketPrefix(code)
	if !ok {
		return "", false
	}
	return stripSuffix(code) + "." + strings.ToUpper(pfx), true
}

// BrokerIndexCodes 向券商侧请求的固定指数集合（带后缀）。
// 只放**定权真正要的那一只**（上证 000001.SH）＋两只常用备查指数：
// 每多一只就多一个 tick 订阅与一份"点位被当成个股价"的暴露面，而消费端目前只有上证一路。
func BrokerIndexCodes() []string {
	out := make([]string, 0, 3)
	for _, c := range []string{"000001", "000300", "399001"} {
		if s, ok := IndexSuffix(c); ok {
			out = append(out, s)
		}
	}
	return out
}

// SetIndexBrokerSource 注入"券商侧指数点位"的来源（§A7-3，装配期一次性注入）。
// 为什么用闭包而不是让 MarketAPI 直接持 Fetcher：data 里 Fetcher→MarketAPI 已经是指向关系，
// 反向再握一次就成了环，且测试里没法替换。注入 nil 即完全退出（回到东财→新浪→腾讯三段）。
func (m *MarketAPI) SetIndexBrokerSource(fn func(code string) (*IndexLiveQuote, bool)) {
	m.idxSwitchMu.Lock()
	m.idxBrokerSrc = fn
	m.idxSwitchMu.Unlock()
}

// indexBrokerLeg 把注入的来源包装成一段腿：拿不到就返回 error（**绝不**折成 0 点位）。
func (m *MarketAPI) indexBrokerLeg(code string) (*IndexLiveQuote, error) {
	m.idxSwitchMu.Lock()
	fn := m.idxBrokerSrc
	m.idxSwitchMu.Unlock()
	if fn == nil {
		return nil, fmt.Errorf("broker index: 未装配券商侧来源")
	}
	q, ok := fn(code)
	if !ok || q == nil || q.Price <= 0 || q.PrevClose <= 0 {
		return nil, fmt.Errorf("broker index: 券商侧没有这一只的可用 tick（未接通/超龄都不编造）")
	}
	if q.Source == "" {
		q.Source = IndexSourceBroker
	}
	return q, nil
}

// indexLeg 一段指数腿：名字（进 Source 与失败原因）+ 取数函数。
type indexLeg struct {
	Name  string
	Fetch func(string) (*IndexLiveQuote, error)
}

// IndexLegOrderFor 段序的**唯一真源**：给两个事实（后备腿开不开、券商段注入没注入），
// 返回"本轮会依次试谁"。东财永远在外部腿第一位（价格口径与既有账同源，动它等于换基准）。
//
// 为什么单独成函数而不是只挂在 MarketAPI 上：设置卡也要回显同一串，而"读面按开关再推一份"
// 必然漏掉券商段（注入是运行期事实，配置里查不到）——11-02 施工时就是这么漂出来的。
// 装配与读面共骑一个函数，才没有第二份可漂。
// English: single source of the leg order, shared by the orchestrator and the settings readout.
func IndexLegOrderFor(fallbackOn, brokerInjected bool) []string {
	order := []string{IndexSourceEastMoney}
	if fallbackOn {
		order = append(order, IndexSourceSina, IndexSourceTencent)
	}
	if brokerInjected {
		// 券商段排最前：owner 的既有裁决是"行情以券商为准"，且本地 L1 比三条外部腿都新。
		// 通道不通时它每次都失败并落到后面，外部腿一字不动（"先落地"不等于"依赖它"）。
		order = append([]string{IndexSourceBroker}, order...)
	}
	return order
}

// IndexLegNames 指数腿的**全部合法名**（声明集，与实际段序分两栏回显）。
// 为什么不复用快照 quote_source 的 golden：那是"个股价由谁给"的命名空间，
// 指数腿是另一套语义（腾讯只做指数、不进个股快照）——11-02 在 e2e 里混用过一次，
// 红在"tencent 不在 golden 里"，而代码是对的。
func IndexLegNames() []string { return IndexLegOrderFor(true, true) }

// indexLegs 按上面的单点段序装配本轮可用的腿（段序与段实现对齐，卡面读的就是这一份）。
func (m *MarketAPI) indexLegs() []indexLeg {
	m.idxSwitchMu.Lock()
	hasBroker := m.idxBrokerSrc != nil
	m.idxSwitchMu.Unlock()
	byName := map[string]func(string) (*IndexLiveQuote, error){
		IndexSourceBroker:    m.indexBrokerLeg,
		IndexSourceEastMoney: m.indexLiveFromEastMoney,
		IndexSourceSina:      m.indexLiveFromSina,
		IndexSourceTencent:   m.indexLiveFromTencent,
	}
	order := IndexLegOrderFor(m.indexFallbackEnabled(), hasBroker)
	legs := make([]indexLeg, 0, len(order))
	for _, name := range order {
		legs = append(legs, indexLeg{name, byName[name]})
	}
	return legs
}

// IndexLegOrder 本轮**实际**会依次试谁（直接取自编排器的段表，不另列一份）。
// 设置卡要回显这件事：按开关推算必然漏掉券商段（注入与否是运行期事实）。
func (m *MarketAPI) IndexLegOrder() []string {
	legs := m.indexLegs()
	out := make([]string, 0, len(legs))
	for _, l := range legs {
		out = append(out, l.Name)
	}
	return out
}

// indexEastMoneyURL 东财指数量 URL（现行主链，secid 由 indexSecID 给 1./0. 前缀）。
func indexEastMoneyURL(sid string) string {
	return fmt.Sprintf("https://push2.eastmoney.com/api/qt/stock/get?secid=%s&fields=f43,f60,f58", sid)
}

// indexLiveFromEastMoney 东财指数实时腿（原 GetIndexLiveQuote 内联那段整段搬出，行为不变，
// 只多了"指数可用独立熔断域"这一层可选隔离）。涨跌幅仍按 (现价−昨收)/昨收 自算，不吃上游百分比字段。
func (m *MarketAPI) indexLiveFromEastMoney(code string) (*IndexLiveQuote, error) {
	url := indexEastMoneyURL(indexSecID(code))
	EastMoneyLimiter.Wait()
	var q IndexLiveQuote
	err := func() error {
		resp, err := m.getWithHeadersScoped(url, emReferer, m.indexScopeSuffix())
		if err != nil {
			return fmt.Errorf("eastmoney index-live http: %w", err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("eastmoney index-live read: %w", err)
		}
		var raw struct {
			Data struct {
				F43 float64 `json:"f43"` // 最新点位（原始值 ×100）
				F60 float64 `json:"f60"` // 昨收（原始值 ×100）
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &raw); err != nil {
			return fmt.Errorf("eastmoney index-live json: %w", err)
		}
		if raw.Data.F43 <= 0 || raw.Data.F60 <= 0 {
			return fmt.Errorf("eastmoney index-live: 点位/昨收不可得（f43=%.0f f60=%.0f）", raw.Data.F43, raw.Data.F60)
		}
		q.Price = raw.Data.F43 / 100
		q.PrevClose = raw.Data.F60 / 100
		q.ChangePct = (q.Price - q.PrevClose) / q.PrevClose * 100
		q.At = time.Now()
		return nil
	}()
	if err != nil {
		return nil, err
	}
	q.Source = IndexSourceEastMoney
	return &q, nil
}

// indexSinaRe 新浪行情行的**指数专用**解析：键捕获"市场前缀 + 6 位码"两段，
// 因此不可能与个股的同码结果撞在同一个裸码键上（个股那条链捕获的只有数字）。
var indexSinaRe = regexp.MustCompile(`hq_str_(sh|sz)(\d+)\s*=\s*"([^"]*)"`)

// indexLiveFromSina 新浪指数实时。字段位次与个股同构：1 今开 2 昨收 3 现价 4 最高 5 最低，
// 涨跌幅自算（与东财同口径，实测昨收同为 3599.02）。成交量/成交额**一律不取**（单位对指数不可信）。
func (m *MarketAPI) indexLiveFromSina(code string) (*IndexLiveQuote, error) {
	pfx, ok := indexMarketPrefix(code)
	if !ok {
		return nil, fmt.Errorf("sina index: %s 不是指数代码（白名单只认 000/899/999/399 段）", code)
	}
	SinaLimiter.Wait()
	body, err := m.getIndexBody("https://hq.sinajs.cn/list="+pfx+stripSuffix(code),
		"https://finance.sina.com.cn", "Mozilla/5.0 (Linux; Android 14) AppleWebKit/537.36")
	if err != nil {
		return nil, fmt.Errorf("sina index http: %w", err)
	}
	utf, _, _ := transform.String(simplifiedchinese.GBK.NewDecoder(), body)
	for _, mch := range indexSinaRe.FindAllStringSubmatch(utf, -1) {
		if mch[1] != pfx || mch[2] != stripSuffix(code) {
			continue // 只收这一只指数的行：批量或异常响应里拿别人的点位＝张冠李戴
		}
		f := strings.Split(mch[3], ",")
		if len(f) < 4 {
			continue
		}
		prev, e1 := strconv.ParseFloat(strings.TrimSpace(f[2]), 64)
		last, e2 := strconv.ParseFloat(strings.TrimSpace(f[3]), 64)
		if e1 != nil || e2 != nil || prev <= 0 || last <= 0 {
			continue
		}
		return &IndexLiveQuote{
			Price: last, PrevClose: prev,
			ChangePct: (last - prev) / prev * 100,
			At:        time.Now(), Source: IndexSourceSina,
		}, nil
	}
	return nil, fmt.Errorf("sina index: %s%s 无有效点位行", pfx, stripSuffix(code))
}

// indexTencentRe 腾讯行情行的指数专用解析（同样捕获市场前缀 + 码两段）。
var indexTencentRe = regexp.MustCompile(`v_(sh|sz)(\d+)\s*=\s*"([^"]*)"`)

// indexLiveFromTencent 腾讯指数实时。字段位次与个股同构：3 现价 4 昨收 32 涨跌幅（实测 88 字段）。
// 这里仍按 (现价−昨收)/昨收 自算而不取字段 32，理由与东财段一致：口径可自证，
// 上游百分比字段换口径（如改按昨收与结算价）时不会出现"看起来还是个数"的静默漂移。
func (m *MarketAPI) indexLiveFromTencent(code string) (*IndexLiveQuote, error) {
	pfx, ok := indexMarketPrefix(code)
	if !ok {
		return nil, fmt.Errorf("tencent index: %s 不是指数代码", code)
	}
	TencentLimiter.Wait()
	body, err := m.getIndexBody("https://qt.gtimg.cn/q="+pfx+stripSuffix(code),
		"https://gu.qq.com/", "Mozilla/5.0 (Linux; Android 14) AppleWebKit/537.36")
	if err != nil {
		return nil, fmt.Errorf("tencent index http: %w", err)
	}
	utf, _, _ := transform.String(simplifiedchinese.GBK.NewDecoder(), body)
	for _, mch := range indexTencentRe.FindAllStringSubmatch(utf, -1) {
		if mch[1] != pfx || mch[2] != stripSuffix(code) {
			continue
		}
		f := strings.Split(mch[3], "~")
		if len(f) < 5 {
			continue
		}
		last, e1 := strconv.ParseFloat(strings.TrimSpace(f[3]), 64)
		prev, e2 := strconv.ParseFloat(strings.TrimSpace(f[4]), 64)
		if e1 != nil || e2 != nil || prev <= 0 || last <= 0 {
			continue
		}
		return &IndexLiveQuote{
			Price: last, PrevClose: prev,
			ChangePct: (last - prev) / prev * 100,
			At:        time.Now(), Source: IndexSourceTencent,
		}, nil
	}
	return nil, fmt.Errorf("tencent index: %s%s 无有效点位行", pfx, stripSuffix(code))
}

// getIndexBody 指数腿的单次 GET（UA/Referer 与个股链同款：上游对裸脚本请求限流或拒绝）。
// 刻意不复用 getWithHeaders——那条带着东财熔断记账，新浪/腾讯的失败不该记到东财头上，
// 反之东财熔断也不许把这两条腿挡在门外（那正是本批要拆的那道连带）。
func (m *MarketAPI) getIndexBody(url, referer, ua string) (string, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Referer", referer)
	resp, err := m.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("http status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return "", fmt.Errorf("空响应体")
	}
	return string(body), nil
}

// indexScopeSuffix 指数请求的熔断域后缀：开时 "#index"，关时空串＝与个股共享 host+path 名额。
// 出厂开（config.IndexBreakerScopeOn），关掉是回滚位。
func (m *MarketAPI) indexScopeSuffix() string {
	if m.indexSwitchOn(&m.idxScopeOff) {
		return indexBreakerScopeSuffix
	}
	return ""
}

// indexFallbackEnabled 指数降级链（新浪/腾讯两段）是否参与。
func (m *MarketAPI) indexFallbackEnabled() bool {
	return m.indexSwitchOn(&m.idxLiveOff)
}

// indexSwitchOn 读一枚"以关为语义存储"的开关（见 MarketAPI 的 idxLiveOff/idxScopeOff 注释）。
func (m *MarketAPI) indexSwitchOn(off *bool) bool {
	m.idxSwitchMu.Lock()
	defer m.idxSwitchMu.Unlock()
	return !*off
}

// SetIndexSourcePolicy 装配期与设置页热更注入两枚指数开关（出厂开，故此处写的是"是否被显式关掉"）。
// 为什么参数是"开"而内部存"关"：零值 MarketAPI（测试与老装配点 `&data.MarketAPI{}`）必须走出厂开，
// 存"开"的裸 bool 零值就是关——那会让"没装配"静默变成"功能被关掉"，正是本仓反复踩的形状。
// English: policy injection point; the fields store the negation so a zero-value MarketAPI stays
// factory-on.
func (m *MarketAPI) SetIndexSourcePolicy(fallbackOn, scopeOn bool) {
	m.idxSwitchMu.Lock()
	m.idxLiveOff, m.idxScopeOff = !fallbackOn, !scopeOn
	m.idxSwitchMu.Unlock()
}

// IndexSource* 指数量来源枚举（进 IndexLiveQuote.Source 与资金帧读数）。
const (
	IndexSourceEastMoney = "eastmoney"
	IndexSourceSina      = "sina"
	IndexSourceTencent   = "tencent"
)

// indexBreakerScopeSuffix 指数熔断域的 scope 后缀。
// 为什么是后缀而不是新域名维度：熔断表按 scope 字符串分格（emBreakers），加后缀即独立一格，
// 既不动个股那格的计数，也不需要给熔断器加类型——改动面最小，而语义正是"这一条腿自己的名额"。
const indexBreakerScopeSuffix = "#index"

// NoteFeedIndexTicks 记录本轮券商侧的指数 tick（§A7-3）。
// 策略关掉时**清空**而不是留着：留着会让"关掉开关"之后卡片仍报 Source=QMT-L1，
// 那是把用户刚做的回滚动作在读数上抹掉（本仓 §UAT 锤过的"改了没生效"家族）。
func (f *Fetcher) NoteFeedIndexTicks(ticks map[string]QMTTick) {
	if f == nil {
		return
	}
	h := f.hub()
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.policy.IndexEnabled || len(ticks) == 0 {
		if !h.policy.IndexEnabled {
			for k := range h.idxEnts {
				delete(h.idxEnts, k)
			}
		}
		return
	}
	if h.idxEnts == nil {
		h.idxEnts = map[string]QMTTick{}
	}
	for code, tk := range ticks {
		// 只收白名单里的指数代码：券商回包给什么就存什么，等于把指数账与个股账混回一张表。
		if _, ok := IndexSuffix(code); !ok {
			continue
		}
		h.idxEnts[code] = tk
	}
}

// HubIndexQuote 取券商侧某只指数的最新点位（涨跌幅按 (现价−昨收)/昨收 自算，与外部腿同口径）。
// 返回 false 的三种情形都不编造：开关关着、从没喂进过 tick、tick 超龄。
// English: broker-side index reading; absent/disabled/stale all report "no reading" rather than a
// fabricated level, so the fallback legs still get their turn.
func (f *Fetcher) HubIndexQuote(code string) (*IndexLiveQuote, bool) {
	if f == nil {
		return nil, false
	}
	suffixed, ok := IndexSuffix(code)
	if !ok {
		return nil, false
	}
	h := f.hub()
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.policy.IndexEnabled {
		return nil, false
	}
	tk, has := h.idxEnts[suffixed]
	if !has || tk.LastPrice <= 0 || tk.PrevClose <= 0 {
		return nil, false
	}
	maxAge := h.policy.MaxAge
	if maxAge <= 0 {
		maxAge = 30 * time.Second
	}
	at := time.UnixMilli(tk.TickTime)
	if tk.TickTime <= 0 || time.Since(at) > maxAge {
		// 超龄即当"没有"：指数点位挂着一小时前的旧值去定权，比拿不到更危险
		// （拿不到会判盲区，旧值会算出一个看起来正常的 MS）。
		return nil, false
	}
	return &IndexLiveQuote{
		Price:     tk.LastPrice,
		PrevClose: tk.PrevClose,
		ChangePct: (tk.LastPrice - tk.PrevClose) / tk.PrevClose * 100,
		At:        at,
		Source:    IndexSourceBroker,
	}, true
}
