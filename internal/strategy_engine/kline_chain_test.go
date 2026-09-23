// K 线降级链单测。
//
// §KLINE-CHAIN-3（2026-09-23 夜间批）后日K链为：**腾讯前复权 → 东财前复权 → 库内日K（按实时昨收
// 归一）→ 不复权兜底（新浪→同花顺，unadj=true 标记）**，每源过 data.ValidateKLine。分钟K链维持
// 新浪→同花顺→腾讯→东财 不变。本文件同时锁住两件事：
//   - 复权腿的**顺序**（腾讯第一、东财第二且仍在复权链内、库内第三、不复权源永远最后）；
//   - 库内那条腿的三道守卫（后复权→前复权定锚归一、量纲手→股 ×100、新鲜度），
//     以及 §H3 的老规矩不被削弱：不复权数据不得进 KLines，LastClose 无快照时不启用库内腿。
//
// English: daily-bar chain tests after §KLINE-CHAIN-3 — Tencent qfq first, EastMoney second, the local
// DB leg third (prev-close normalization, lots→shares, freshness), unadjusted sources last and never
// entering md.KLines.
package strategy_engine

import (
	"bytes"
	"io"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/data"
)

// klineChainTransport 按 host 区分响应，模拟 K 线降级链：
// 新浪 → 拒绝访问(HTML)；同花顺 → 不可用；腾讯 → 正常；东财 → 失败。
type klineChainTransport struct{}

// RoundTrip 测试传输桩：按请求返回预设 K 线响应。
func (klineChainTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	host := req.URL.Hostname()
	switch {
	case strings.Contains(host, "sina.com.cn"):
		// 新浪被封：返回 HTML 拒绝访问（parseSinaKLine 会解析失败）
		return testRespEngine(200, "<html><title>拒绝访问</title></html>"), nil
	case strings.Contains(host, "10jqka.com.cn"):
		return testRespEngine(200, "empty"), nil
	case strings.Contains(host, "gtimg.cn"):
		if strings.Contains(req.URL.Path, "mkline") {
			return testRespEngine(200, `{"code":0,"msg":"","data":{"sh600206":{"m5":[["202608071430","48.00","48.10","48.15","47.90","1500.00","{}","1.80"],["202608071435","48.17","48.17","48.17","48.17","1212.00","{}","1.43"]]}}}`), nil
		}
		return testRespEngine(200, tencentQfqBody), nil
	case strings.Contains(host, "eastmoney.com"):
		return testRespEngine(500, ""), nil
	}
	return testRespEngine(404, ""), nil
}

// testRespEngine 构造指定状态码+body 的 HTTP 测试响应。
func testRespEngine(code int, body string) *http.Response {
	return &http.Response{
		StatusCode: code,
		Status:     http.StatusText(code),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewBufferString(body)),
	}
}

// qfqVsUnadjTransport 复权腿与新浪不复权**同时可用**：链必须先取复权腿（§H3 复权优先），
// 新浪一根都不许碰。腾讯/东财谁可用由 body 决定，便于各用例复用。
type qfqVsUnadjTransport struct{ tencentBody string }

func (tr qfqVsUnadjTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	host := req.URL.Hostname()
	switch {
	case strings.Contains(host, "eastmoney.com"):
		return testRespEngine(500, ""), nil // 东财全挂，逼出后面的腿
	case strings.Contains(host, "gtimg.cn"):
		return testRespEngine(200, tr.tencentBody), nil
	case strings.Contains(host, "sina.com.cn"):
		return testRespEngine(200, `[{"day":"2026-08-05","open":"10","high":"11","low":"9.9","close":"10.8","volume":"123","amount":"1300"},{"day":"2026-08-06","open":"10.8","high":"12","low":"10.7","close":"11.9","volume":"200","amount":"2300"}]`), nil
	case strings.Contains(host, "10jqka.com.cn"):
		return testRespEngine(200, "empty"), nil
	}
	return testRespEngine(404, ""), nil
}

// bothQfqTransport 腾讯与东财**同时可用**（§KLINE-CHAIN-3 换序后必须腾讯优先的用例）。
// 两源给不同的收盘价，取到哪个一目了然。
type bothQfqTransport struct{}

func (bothQfqTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	host := req.URL.Hostname()
	switch {
	case strings.Contains(host, "gtimg.cn"):
		return testRespEngine(200, tencentQfqBody), nil // 末根 close 43.79
	case strings.Contains(host, "eastmoney.com"):
		return testRespEngine(200, eastMoneyQfqBody), nil // 末根 close 77.77
	case strings.Contains(host, "sina.com.cn"):
		return testRespEngine(200, "<html><title>拒绝访问</title></html>"), nil
	case strings.Contains(host, "10jqka.com.cn"):
		return testRespEngine(200, "empty"), nil
	}
	return testRespEngine(404, ""), nil
}

const tencentQfqBody = `{"code":0,"msg":"","data":{"sh600206":{"qfqday":[["2026-08-04","33.67","36.19","36.36","32.93","967576.000"],["2026-08-06","40.80","43.79","43.79","40.50","1030100.000"]]}}}`

// eastMoneyQfqBody 东财前复权日K（push2 klines CSV 行），供"两复权源同时可用"的用例。
const eastMoneyQfqBody = `{"data":{"klines":["2026-08-04,30.00,31.00,29.00,30.50,1000,20000","2026-08-06,70.00,78.00,69.00,77.77,2000,40000"]}}`

// tencentDayOnlyBody：只有不复权 day 数组（无 qfqday）——§H3 起腾讯必须拒收而非静默冒充复权数据。
const tencentDayOnlyBody = `{"code":0,"msg":"","data":{"sh600206":{"day":[["2026-08-04","38.67","41.19","41.36","37.93","967576.000"],["2026-08-06","45.80","48.79","48.79","45.50","1030100.000"]]}}}`

// allFailTransport 所有 host 均返回空响应（网络腿全挂，逼出库内腿/失败分支）。
type allFailTransport struct{}

// RoundTrip 全失败传输桩：总是返回空 body。
func (allFailTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return testRespEngine(200, ""), nil
}

// newChainEngine 造一个只挂 HTTP 桩的策略引擎（网络腿按 transport 表现，库内腿未注入）。
func newChainEngine(rt http.RoundTripper) *Engine {
	m := data.NewMarketAPI()
	m.SetTransport(rt)
	return New(m)
}

// ---------------- 复权腿顺序 ----------------

// TestFetchDayKLinePrefersTencent 腾讯前复权是第一腿：东财挂不挂都该由它供数。
func TestFetchDayKLinePrefersTencent(t *testing.T) {
	e := newChainEngine(klineChainTransport{}) // 该桩里东财 500

	klines, unadj, src := e.fetchDayKLine("600206", 0)
	if len(klines) != 2 {
		t.Fatalf("腾讯 qfqday 应返回2根, got %d", len(klines))
	}
	if unadj {
		t.Error("腾讯 qfqday 属复权源，unadj 标记必须为 false")
	}
	if src != "腾讯" {
		t.Errorf("供数腿应为腾讯, got %s", src)
	}
	if klines[1].Close != 43.79 {
		t.Errorf("末根 close 应43.79, got %.2f", klines[1].Close)
	}
	stat := e.takeKLineSrc()
	if stat["腾讯"] != 1 {
		t.Errorf("K线源统计应含 腾讯=1, got %v", stat)
	}
}

// TestFetchDayKLineTencentFirstEvenIfEastMoneyLive §KLINE-CHAIN-3 换序主锁：两条复权源**同时可用**时
// 必须取腾讯，东财一根都不许消费（东财已从主源位降级，但仍在链上作第二复权源）。
func TestFetchDayKLineTencentFirstEvenIfEastMoneyLive(t *testing.T) {
	e := newChainEngine(bothQfqTransport{})

	klines, unadj, src := e.fetchDayKLine("600206", 0)
	if len(klines) != 2 || unadj || src != "腾讯" {
		t.Fatalf("应取腾讯且 unadj=false, got n=%d unadj=%v src=%s", len(klines), unadj, src)
	}
	if klines[1].Close != 43.79 { // 腾讯值；77.77 是东财的，取到即说明换序没生效
		t.Errorf("取到的不是腾讯序列, close=%.2f", klines[1].Close)
	}
	stat := e.takeKLineSrc()
	if stat["东财"] != 0 {
		t.Errorf("腾讯可用时不得消费东财, src=%v", stat)
	}
}

// TestFetchDayKLineEastMoneySecondLeg 腾讯不可用（只有不复权 day 数组 → §H3 整体拒收）时，
// 东财作为第二复权源顶上，且仍标记为复权（unadj=false）。
func TestFetchDayKLineEastMoneySecondLeg(t *testing.T) {
	m := data.NewMarketAPI()
	m.SetTransport(eastMoneyOKTransport{})
	e := New(m)

	klines, unadj, src := e.fetchDayKLine("600206", 0)
	if len(klines) != 2 || unadj || src != "东财" {
		t.Fatalf("应落到东财, got n=%d unadj=%v src=%s", len(klines), unadj, src)
	}
	if klines[1].Close != 77.77 {
		t.Errorf("东财末根应 77.77, got %.2f", klines[1].Close)
	}
	if vol := klines[1].Volume; vol != 200000 {
		t.Errorf("东财成交量应由 data 层换算为股（2000 手×100）, got %v", vol)
	}
}

// eastMoneyOKTransport 腾讯无 qfqday（只有不复权 day）+ 东财正常，逼出第二复权源。
type eastMoneyOKTransport struct{}

func (eastMoneyOKTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	host := req.URL.Hostname()
	switch {
	case strings.Contains(host, "gtimg.cn"):
		return testRespEngine(200, tencentDayOnlyBody), nil
	case strings.Contains(host, "eastmoney.com"):
		return testRespEngine(200, eastMoneyQfqBody), nil
	case strings.Contains(host, "sina.com.cn"):
		return testRespEngine(200, "<html><title>拒绝访问</title></html>"), nil
	}
	return testRespEngine(404, ""), nil
}

// TestFetchDayKLinePrefersQfqOverLiveSina 新浪不复权数据完好时也必须被跳过（复权优先，含库内腿之后）。
func TestFetchDayKLinePrefersQfqOverLiveSina(t *testing.T) {
	e := newChainEngine(qfqVsUnadjTransport{tencentBody: tencentQfqBody})

	klines, unadj, src := e.fetchDayKLine("600206", 0)
	if len(klines) != 2 || unadj || src != "腾讯" {
		t.Fatalf("应取腾讯 qfq 2 根且 unadj=false, got n=%d unadj=%v src=%s", len(klines), unadj, src)
	}
	if klines[1].Close != 43.79 { // 腾讯值，非新浪的 11.9
		t.Errorf("取到的不是腾讯复权序列, close=%.2f", klines[1].Close)
	}
	stat := e.takeKLineSrc()
	if stat["新浪"] != 0 {
		t.Errorf("复权源可用时不得消费不复权新浪, src=%v", stat)
	}
}

// TestFetchDayKLineUnadjustedMarked 两条网络复权源全挂、库内腿未注入时落到新浪兜底，
// 且必须带 unadj=true（调用方据此拒绝喂入因子计算）——§H3 语义不回归。
func TestFetchDayKLineUnadjustedMarked(t *testing.T) {
	e := newChainEngine(qfqVsUnadjTransport{tencentBody: tencentDayOnlyBody})

	klines, unadj, src := e.fetchDayKLine("600206", 0)
	if !unadj || len(klines) != 2 {
		t.Fatalf("应落到新浪并标记 unadj=true, got n=%d unadj=%v", len(klines), unadj)
	}
	if src != "新浪" {
		t.Errorf("供数腿应为新浪, got %s", src)
	}
	if klines[1].Close != 11.9 {
		t.Errorf("兜底应来自新浪, close=%.2f", klines[1].Close)
	}
	stat := e.takeKLineSrc()
	if stat["腾讯"] != 0 {
		t.Errorf("腾讯无 qfqday 必须整体拒收，不得计入命中, src=%v", stat)
	}
}

// TestFetchDayKLineAllFail 全部源失败时返回 nil、统计"失败"、不带降级标记。
// 本用例同时是"未注入库内腿时行为不变"的锁。
func TestFetchDayKLineAllFail(t *testing.T) {
	e := newChainEngine(allFailTransport{})

	if klines, unadj, src := e.fetchDayKLine("600206", 11.0); len(klines) != 0 || unadj || src != "失败" {
		t.Fatalf("全失败应返回空且 unadj=false src=失败, got %d/%v/%s", len(klines), unadj, src)
	}
	if stat := e.takeKLineSrc(); stat["失败"] != 1 {
		t.Errorf("应统计 失败=1, got %v", stat)
	}
}

// ---------------- 库内兜底腿：位置 ----------------

// storeBarsFixture 造一段库内**后复权**日K：实际价 10.00/11.00，因子 2 → 后复权价 20.00/22.00，
// 量纲按库内口径为**手**（100/110 手）。返回升序序列与期望的归一 scale。
// ⚠ 末根必须落在**上一交易日**（tradingDayTime(1)）：库内是夜间同步的 T+1 数据，那才是现网健康形态，
// 也是定锚用的实时昨收所对应的那一根。拿 time.Now() 当末根会造出"当日半成品K"这种锚错位形态，
// 该形态由守卫⓪专门处理，另有 TestStoreDayKLineDropsTodayBar 锁它（见 engine.storeDayKLine）。
func storeBarsFixture(day time.Time) []data.KLine {
	prev := day.AddDate(0, 0, -7)
	return []data.KLine{
		{Date: prev, Open: 18, High: 21, Low: 17, Close: 20, Volume: 100, Amount: 4000},
		{Date: day, Open: 20, High: 23, Low: 19, Close: 22, Volume: 110, Amount: 4400},
	}
}

// newStoreLegEngine 网络腿全挂、只注入库内腿的引擎；返回引擎与调用计数指针。
func newStoreLegEngine(bars []data.KLine, calls *int) *Engine {
	e := newChainEngine(allFailTransport{})
	e.SetDayBarsLookup(func(code string, count int) ([]data.KLine, bool) {
		*calls++
		if len(bars) == 0 {
			return nil, false
		}
		return bars, true
	})
	return e
}

// TestFetchDayKLineStoreLegBeatsUnadjusted 两条网络复权腿全挂时，库内腿必须排在新浪/同花顺**之前**
// 供数，且归一后的序列按复权口径计入（unadj=false → 能进 KLines）——这正是 09-23 全灭的正解。
func TestFetchDayKLineStoreLegBeatsUnadjusted(t *testing.T) {
	calls := 0
	e := newStoreLegEngine(storeBarsFixture(tradingDayTime(1)), &calls)

	klines, unadj, src := e.fetchDayKLine("600206", 11)
	if len(klines) != 2 || unadj || src != kLineSrcStore {
		t.Fatalf("应取库内腿且 unadj=false, got n=%d unadj=%v src=%s", len(klines), unadj, src)
	}
	stat := e.takeKLineSrc()
	if stat[kLineSrcStore] != 1 {
		t.Errorf("K线源统计应含 库内=1, got %v", stat)
	}
	if stat["新浪"] != 0 {
		t.Errorf("库内复权腿可用时不得消费不复权新浪, src=%v", stat)
	}
	// 复权口径才进 KLines（§H3）：库内腿必须走这条正门
	md := &StockMarketData{Code: "600206"}
	applyDayKLine(md, klines, unadj)
	if len(md.KLines) != 2 || md.KLineUnadj {
		t.Fatalf("库内归一序列应进 KLines, got kl=%d unadjFlag=%v", len(md.KLines), md.KLineUnadj)
	}
}

// ---------------- 库内兜底腿：三道守卫 ----------------

// TestStoreDayKLineNormalizesHfqBase 归一数学主锁：末根后复权收盘 22 ÷ 实时昨收 11 ⇒ scale=0.5，
// 整条序列（Open/High/Low/Close）等比缩回实际价口径；不缩放的话下游会把 20/22 当现价，
// 止损价、价格型条件全部翻倍。
func TestStoreDayKLineNormalizesHfqBase(t *testing.T) {
	calls := 0
	e := newStoreLegEngine(storeBarsFixture(tradingDayTime(1)), &calls)

	out, ok := e.storeDayKLine("600206", 11)
	if !ok || len(out) != 2 {
		t.Fatalf("归一腿应可用, got ok=%v n=%d", ok, len(out))
	}
	want := []data.KLine{
		{Date: out[0].Date, Open: 9, High: 10.5, Low: 8.5, Close: 10, Volume: 10000, Amount: 4000},
		{Date: out[1].Date, Open: 10, High: 11.5, Low: 9.5, Close: 11, Volume: 11000, Amount: 4400},
	}
	for i := range want {
		got := out[i]
		if math.Abs(got.Open-want[i].Open) > 1e-9 || math.Abs(got.High-want[i].High) > 1e-9 ||
			math.Abs(got.Low-want[i].Low) > 1e-9 || math.Abs(got.Close-want[i].Close) > 1e-9 {
			t.Errorf("第 %d 根价格未按 scale=0.5 归一: %+v", i, got)
		}
	}
	// 末根归一到昨收本身（不是现价）：锚错成现价会把今日涨跌幅混进历史基准
	if math.Abs(out[1].Close-11) > 1e-9 {
		t.Errorf("末根收盘应锚回昨收 11, got %.6f", out[1].Close)
	}
}

// TestStoreDayKLineConvertsLotsToShares 量纲守卫：库内 Vol 是**手**，data.KLine.Volume 是**股**，
// 必须 ×100。不换算时"量比 > N 倍均量"这类放量条件当场把量看大 100 倍（双响炮首当其冲）。
func TestStoreDayKLineConvertsLotsToShares(t *testing.T) {
	calls := 0
	e := newStoreLegEngine(storeBarsFixture(tradingDayTime(1)), &calls)

	out, ok := e.storeDayKLine("600206", 11)
	if !ok {
		t.Fatalf("库内腿应可用")
	}
	if out[0].Volume != 100*lotsToShares || out[1].Volume != 110*lotsToShares {
		t.Errorf("成交量未按手→股换算: %v / %v", out[0].Volume, out[1].Volume)
	}
}

// TestStoreDayKLineRefusesWithoutPrevClose 昨收取不到（≤0）⇒ 无法定锚 ⇒ 拒用本腿，
// 而且根本不该去查库（LastClose 走的就是这条路）。
func TestStoreDayKLineRefusesWithoutPrevClose(t *testing.T) {
	for _, pc := range []float64{0, -1} {
		calls := 0
		e := newStoreLegEngine(storeBarsFixture(tradingDayTime(1)), &calls)
		if out, ok := e.storeDayKLine("600206", pc); ok || len(out) != 0 {
			t.Fatalf("prevClose=%.2f 时本腿必须拒用, got ok=%v n=%d", pc, ok, len(out))
		}
		if calls != 0 {
			t.Errorf("prevClose=%.2f 时不该查库（省一次无用的 IO）, calls=%d", pc, calls)
		}
	}
}

// TestStoreDayKLineRefusesStaleBars 新鲜度守卫：末根早于「今天往前 10 个交易日」判不可信、拒用。
func TestStoreDayKLineRefusesStaleBars(t *testing.T) {
	calls := 0
	e := newStoreLegEngine(storeBarsFixture(tradingDayTime(20)), &calls)
	if out, ok := e.storeDayKLine("600206", 11); ok || len(out) != 0 {
		t.Fatalf("超期库内日K必须拒用, got ok=%v n=%d", ok, len(out))
	}
	if stat := e.takeKLineSrc(); len(stat) != 0 {
		t.Errorf("拒用不该计入任何供数腿统计, got %v", stat)
	}
}

// TestStoreDayKLineToleratesSlightlyStaleBars 末根晚于 10 个交易日、但早于上一交易日（同步晚到一步）
// ⇒ 照用不误杀（留痕走 opslog，不在本用例断言）。
func TestStoreDayKLineToleratesSlightlyStaleBars(t *testing.T) {
	calls := 0
	e := newStoreLegEngine(storeBarsFixture(tradingDayTime(3)), &calls)
	if out, ok := e.storeDayKLine("600206", 11); !ok || len(out) != 2 {
		t.Fatalf("仅晚到 3 个交易日的库内日K应照用, got ok=%v n=%d", ok, len(out))
	}
}

// tradingDayTime 返回今天往前第 n 个交易日（与守卫同一套 weekday+日历口径，避免用例自说自话）。
func tradingDayTime(n int) time.Time {
	d := tradingDaysBefore(data.TradingDayDate(time.Now()), n)
	t, err := time.ParseInLocation("20060102", d, time.Local)
	if err != nil {
		panic(err)
	}
	return t
}

// TestStoreDayKLineDropsTodayBar 守卫⓪（锚定日的正误）：库里末根若落在**今天**（同步侧提前写入当日
// 半成品K，或库里混进未来日期的脏行），必须先丢掉再定锚。因为定锚用的是**实时昨收**，拿它去锚当日
// 那根的收盘，等于把今天的涨跌幅整体摊进历史基准——正是本腿要防的锚错位失真（同一根价格被两个日期
// 各认一次）。丢弃不损失信息：当日那一根本来由 attachLiveBar 用实时快照拼接/覆盖。
func TestStoreDayKLineDropsTodayBar(t *testing.T) {
	// 锚定日按引擎自己比较的那个"今天"取（TradingDayDate），周末/休市跑用例也不会自相矛盾。
	nowBar, perr := time.ParseInLocation("20060102", data.TradingDayDate(time.Now()), time.Local)
	if perr != nil {
		t.Fatalf("锚定日解析失败: %v", perr)
	}

	t.Run("含当日末根时只留昨天那根", func(t *testing.T) {
		calls := 0
		// storeBarsFixture(nowBar) = [今天-7天: 后复权 20, 今天: 后复权 22]
		e := newStoreLegEngine(storeBarsFixture(nowBar), &calls)
		out, ok := e.storeDayKLine("600206", 11)
		if !ok || len(out) != 1 {
			t.Fatalf("当日末根必须被丢掉且保留历史那根, got ok=%v n=%d", ok, len(out))
		}
		// 剩下的那根（今天-7）后复权收盘 20 ⇒ scale=11/20=0.55，锚回实际价
		if got := out[0].Close; got != 11 {
			t.Errorf("留存末根应锚回昨收 11, got %v", got)
		}
		if got := out[0].Open; got != 18*0.55 {
			t.Errorf("Open 应同比例缩放 18×0.55, got %v", got)
		}
		if got := out[0].Volume; got != 100*lotsToShares {
			t.Errorf("Volume 应手→股 ×100, got %v", got)
		}
	})

	t.Run("库里只有当日一根时无历史可锚即拒用", func(t *testing.T) {
		calls := 0
		only := []data.KLine{{Date: nowBar, Open: 21, High: 23, Low: 19, Close: 22, Volume: 110, Amount: 4400}}
		e := newStoreLegEngine(only, &calls)
		if out, ok := e.storeDayKLine("600206", 11); ok || len(out) != 0 {
			t.Fatalf("只剩当日一根时应拒用本腿, got ok=%v n=%d", ok, len(out))
		}
		if calls != 1 {
			t.Errorf("拒用前应只查库一次, calls=%d", calls)
		}
	})
}

// TestStoreDayKLineDirtyBarsRejectedByValidate 库内脏数据（缺档/停牌行的 0 价）必须被
// data.ValidateKLine 拦下，不许因"是本地库"就免检流入因子计算。
func TestStoreDayKLineDirtyBarsRejectedByValidate(t *testing.T) {
	bars := storeBarsFixture(tradingDayTime(1))
	bars[0].Low = 0 // 缺档行：0 价
	calls := 0
	e := newStoreLegEngine(bars, &calls)
	if out, ok := e.storeDayKLine("600206", 11); ok || len(out) != 0 {
		t.Fatalf("脏序列必须被 ValidateKLine 拦下, got ok=%v n=%d", ok, len(out))
	}

	bars2 := storeBarsFixture(tradingDayTime(1))
	bars2[1].Close = math.NaN() // 末根 NaN：连 scale 都算不出
	calls2 := 0
	e2 := newStoreLegEngine(bars2, &calls2)
	if out, ok := e2.storeDayKLine("600206", 11); ok || len(out) != 0 {
		t.Fatalf("末根 NaN 必须拒用, got ok=%v n=%d", ok, len(out))
	}
}

// TestStoreDayKLineWithoutLookupInjected 未注入库内腿（cmd/backtest 等入口）时本腿静默不可用。
func TestStoreDayKLineWithoutLookupInjected(t *testing.T) {
	e := newChainEngine(allFailTransport{})
	if out, ok := e.storeDayKLine("600206", 11); ok || len(out) != 0 {
		t.Fatalf("未注入 lookup 时应拒用, got ok=%v n=%d", ok, len(out))
	}
}

// ---------------- 缓存与 LastClose ----------------

// TestCachedKLineRefetchesWhenAnchorChanges 库内序列是按 anchor 缩放出来的：昨收变了必须重取，
// 否则会把两个交易日的基准混进同一条历史（5 分钟 TTL 内昨收不变是常态，这里锁的是"变了就换"）。
func TestCachedKLineRefetchesWhenAnchorChanges(t *testing.T) {
	calls := 0
	e := newStoreLegEngine(storeBarsFixture(tradingDayTime(1)), &calls)

	kl1, _, unadj1 := e.cachedKLine("600206", 11)
	if len(kl1) != 2 || unadj1 || math.Abs(kl1[1].Close-11) > 1e-9 {
		t.Fatalf("首次应按昨收 11 归一, got n=%d unadj=%v close=%v", len(kl1), unadj1, kl1[1].Close)
	}
	// 同一昨收再取：走缓存，不重复查库
	kl2, _, _ := e.cachedKLine("600206", 11)
	if len(kl2) != 2 || calls != 1 {
		t.Fatalf("昨收未变应命中缓存, calls=%d n=%d", calls, len(kl2))
	}
	// 昨收变了：必须重取并按新锚重算 scale
	kl3, _, _ := e.cachedKLine("600206", 12)
	if len(kl3) != 2 || calls != 2 {
		t.Fatalf("昨收变更应触发重取, calls=%d n=%d", calls, len(kl3))
	}
	if math.Abs(kl3[1].Close-12) > 1e-9 {
		t.Errorf("重取后应按新昨收 12 归一, got %.6f", kl3[1].Close)
	}
}

// TestLastCloseSkipsStoreLeg LastClose 没有实时快照 ⇒ 传 0 ⇒ 既不查库也不拿后复权价当现价：
// 只可能返回网络复权腿（或未复权兜底）的末根收盘价。行为与本批改造前一致。
func TestLastCloseSkipsStoreLeg(t *testing.T) {
	calls := 0
	e := newStoreLegEngine(storeBarsFixture(tradingDayTime(1)), &calls)
	if got := e.LastClose("600206"); got != 0 {
		t.Errorf("库内腿在无昨收时不得供数, LastClose=%.2f", got)
	}
	if calls != 0 {
		t.Errorf("LastClose 不该触发库内查询（会把未归一的后复权价带进现价用途）, calls=%d", calls)
	}

	// 腾讯可用时 LastClose 仍取实际价口径的末根收盘
	e2 := newChainEngine(klineChainTransport{})
	if got := e2.LastClose("600206"); math.Abs(got-43.79) > 1e-9 {
		t.Errorf("LastClose 应为腾讯末根 43.79, got %.2f", got)
	}
}

// ---------------- §H3 闸与分钟链 ----------------

// TestApplyDayKLineRefusesUnadjusted applyDayKLine 是因子拒参与的闸：
// 不复权序列不进 md.KLines（各策略 len 守卫自然拒算），只置 KLineUnadj 标记。
func TestApplyDayKLineRefusesUnadjusted(t *testing.T) {
	bars := []data.KLine{{Close: 1}, {Close: 2}}
	md := &StockMarketData{Code: "600206"}
	applyDayKLine(md, bars, true)
	if len(md.KLines) != 0 || !md.KLineUnadj {
		t.Fatalf("不复权兜底必须拒进 KLines 并置标记, got kl=%d unadjFlag=%v", len(md.KLines), md.KLineUnadj)
	}
	md2 := &StockMarketData{Code: "600206"}
	applyDayKLine(md2, bars, false)
	if len(md2.KLines) != 2 || md2.KLineUnadj {
		t.Fatalf("复权数据应正常进 KLines, got kl=%d unadjFlag=%v", len(md2.KLines), md2.KLineUnadj)
	}
	// 取空且未标记（全链失败）：既无序列也无降级标记
	md3 := &StockMarketData{Code: "600206"}
	applyDayKLine(md3, nil, false)
	if len(md3.KLines) != 0 || md3.KLineUnadj {
		t.Fatal("全链失败不得置标记")
	}
}

// TestLivePrevCloseIgnoresAmbiguousClose §P1-5 歧义字段守卫：昨收只认 PrevClose，
// 盘中就是现价的 Close 绝不能当归一锚（会把今日涨跌幅写进整条历史基准）。
func TestLivePrevCloseIgnoresAmbiguousClose(t *testing.T) {
	if v := livePrevClose(nil); v != 0 {
		t.Errorf("nil md 应为 0, got %v", v)
	}
	if v := livePrevClose(&StockMarketData{Code: "600206"}); v != 0 {
		t.Errorf("无快照应为 0, got %v", v)
	}
	md := &StockMarketData{Quote: &data.StockInfo{Close: 12.5, Price: 12.5}}
	if v := livePrevClose(md); v != 0 {
		t.Errorf("PrevClose 缺失时不得回退 Close（那是现价）, got %v", v)
	}
	md2 := &StockMarketData{Quote: &data.StockInfo{PrevClose: 11.2, Close: 12.5}}
	if v := livePrevClose(md2); v != 11.2 {
		t.Errorf("应取 PrevClose 11.2, got %v", v)
	}
}

// TestFetchMinuteKLineTencentFallback 分钟K新浪失败时落到腾讯分钟源（分钟链口径不变）。
func TestFetchMinuteKLineTencentFallback(t *testing.T) {
	e := newChainEngine(klineChainTransport{})

	minKL := e.fetchMinuteKLine("600206")
	if len(minKL) < 2 {
		t.Fatalf("应落到腾讯分钟返回≥2根, got %d", len(minKL))
	}
	if !minKL[0].Date.Before(minKL[1].Date) {
		t.Errorf("分钟K应按时间升序")
	}
	stat := e.takeKLineSrc()
	if stat["腾讯分钟"] != 1 {
		t.Errorf("K线源统计应含 腾讯分钟=1, got %v", stat)
	}
}
