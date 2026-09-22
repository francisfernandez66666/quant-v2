// K 线降级链单测：§H3（2026-09-22 PM 批）后日K链为 **复权优先**——
// 东财(fqt=qfq) → 腾讯(仅 qfqday；不复权回退已拒收) → 不复权兜底（新浪→同花顺，unadj=true 标记），
// 且每源过 data.ValidateKLine。分钟K链维持 新浪→同花顺→腾讯→东财 不变。
// English: §H3 daily chain is qfq-first (EastMoney → Tencent qfq-only), unadjusted sources are
// marked last resorts gated by ValidateKLine; the minute chain keeps its legacy order.
package strategy_engine

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"

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
		return testRespEngine(200, `{"code":0,"msg":"","data":{"sh600206":{"qfqday":[["2026-08-04","33.67","36.19","36.36","32.93","967576.000"],["2026-08-06","40.80","43.79","43.79","40.50","1030100.000"]]}}}`), nil
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

// qfqVsUnadjTransport 腾讯 qfq 与新浪不复权**同时可用**：链必须先取腾讯（复权优先），
// 新浪一根都不许碰（§H3 旧链新浪第一就是把因子喂给了不复权数据）。
type qfqVsUnadjTransport struct{ tencentBody string }

func (tr qfqVsUnadjTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	host := req.URL.Hostname()
	switch {
	case strings.Contains(host, "eastmoney.com"):
		return testRespEngine(500, ""), nil // 东财全挂，逼出第二复权源
	case strings.Contains(host, "gtimg.cn"):
		return testRespEngine(200, tr.tencentBody), nil
	case strings.Contains(host, "sina.com.cn"):
		return testRespEngine(200, `[{"day":"2026-08-05","open":"10","high":"11","low":"9.9","close":"10.8","volume":"123","amount":"1300"},{"day":"2026-08-06","open":"10.8","high":"12","low":"10.7","close":"11.9","volume":"200","amount":"2300"}]`), nil
	case strings.Contains(host, "10jqka.com.cn"):
		return testRespEngine(200, "empty"), nil
	}
	return testRespEngine(404, ""), nil
}

const tencentQfqBody = `{"code":0,"msg":"","data":{"sh600206":{"qfqday":[["2026-08-04","33.67","36.19","36.36","32.93","967576.000"],["2026-08-06","40.80","43.79","43.79","40.50","1030100.000"]]}}}`

// tencentDayOnlyBody：只有不复权 day 数组（无 qfqday）——§H3 起腾讯必须拒收而非静默冒充复权数据。
const tencentDayOnlyBody = `{"code":0,"msg":"","data":{"sh600206":{"day":[["2026-08-04","38.67","41.19","41.36","37.93","967576.000"],["2026-08-06","45.80","48.79","48.79","45.50","1030100.000"]]}}}`

// TestFetchDayKLineTencentFallback 东财失败时落到腾讯 qfq（第二复权源），且不碰不复权源。
func TestFetchDayKLineTencentFallback(t *testing.T) {
	m := data.NewMarketAPI()
	m.SetTransport(klineChainTransport{})
	e := New(m)

	klines, unadj := e.fetchDayKLine("600206")
	if len(klines) != 2 {
		t.Fatalf("应落到腾讯返回2根, got %d", len(klines))
	}
	if unadj {
		t.Error("腾讯 qfqday 属复权源，unadj 标记必须为 false")
	}
	if klines[1].Close != 43.79 {
		t.Errorf("末根 close 应43.79, got %.2f", klines[1].Close)
	}
	src := e.takeKLineSrc()
	if src["腾讯"] != 1 {
		t.Errorf("K线源统计应含 腾讯=1, got %v", src)
	}
}

// TestFetchDayKLinePrefersQfqOverLiveSina 新浪不复权数据完好时也必须被跳过（§H3 复权优先）。
func TestFetchDayKLinePrefersQfqOverLiveSina(t *testing.T) {
	m := data.NewMarketAPI()
	m.SetTransport(qfqVsUnadjTransport{tencentBody: tencentQfqBody})
	e := New(m)

	klines, unadj := e.fetchDayKLine("600206")
	if len(klines) != 2 || unadj {
		t.Fatalf("应取腾讯 qfq 2 根且 unadj=false, got n=%d unadj=%v", len(klines), unadj)
	}
	if klines[1].Close != 43.79 { // 腾讯值，非新浪的 11.9
		t.Errorf("取到的不是腾讯复权序列, close=%.2f", klines[1].Close)
	}
	src := e.takeKLineSrc()
	if src["新浪"] != 0 {
		t.Errorf("复权源可用时不得消费不复权新浪, src=%v", src)
	}
}

// TestFetchDayKLineUnadjustedMarked 两复权源全挂（东财 500、腾讯只有不复权 day→拒收）时
// 落到新浪兜底，且必须带 unadj=true（调用方据此拒绝喂入因子计算）。
func TestFetchDayKLineUnadjustedMarked(t *testing.T) {
	m := data.NewMarketAPI()
	m.SetTransport(qfqVsUnadjTransport{tencentBody: tencentDayOnlyBody})
	e := New(m)

	klines, unadj := e.fetchDayKLine("600206")
	if !unadj || len(klines) != 2 {
		t.Fatalf("应落到新浪并标记 unadj=true, got n=%d unadj=%v", len(klines), unadj)
	}
	if klines[1].Close != 11.9 {
		t.Errorf("兜底应来自新浪, close=%.2f", klines[1].Close)
	}
	src := e.takeKLineSrc()
	if src["腾讯"] != 0 {
		t.Errorf("腾讯无 qfqday 必须整体拒收，不得计入命中, src=%v", src)
	}
}

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

// TestFetchMinuteKLineTencentFallback 分钟K新浪失败时落到腾讯分钟源（分钟链口径不变）。
func TestFetchMinuteKLineTencentFallback(t *testing.T) {
	m := data.NewMarketAPI()
	m.SetTransport(klineChainTransport{})
	e := New(m)

	minKL := e.fetchMinuteKLine("600206")
	if len(minKL) < 2 {
		t.Fatalf("应落到腾讯分钟返回≥2根, got %d", len(minKL))
	}
	if !minKL[0].Date.Before(minKL[1].Date) {
		t.Errorf("分钟K应按时间升序")
	}
	src := e.takeKLineSrc()
	if src["腾讯分钟"] != 1 {
		t.Errorf("K线源统计应含 腾讯分钟=1, got %v", src)
	}
}

// TestFetchDayKLineAllFail 全部源失败时返回 nil 且统计"失败"、不带降级标记。
func TestFetchDayKLineAllFail(t *testing.T) {
	m := data.NewMarketAPI()
	m.SetTransport(allFailTransport{})
	e := New(m)

	if klines, unadj := e.fetchDayKLine("600206"); len(klines) != 0 || unadj {
		t.Fatalf("全失败应返回空且 unadj=false, got %d/%v", len(klines), unadj)
	}
	if src := e.takeKLineSrc(); src["失败"] != 1 {
		t.Errorf("应统计 失败=1, got %v", src)
	}
}

// allFailTransport 所有 host 均返回空响应。
type allFailTransport struct{}

// RoundTrip 全失败传输桩：总是返回网络错误。
func (allFailTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return testRespEngine(200, ""), nil
}
