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

func testRespEngine(code int, body string) *http.Response {
	return &http.Response{
		StatusCode: code,
		Status:     http.StatusText(code),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewBufferString(body)),
	}
}

// TestFetchDayKLineTencentFallback 新浪被封/同花顺不可用/东财失败时，日K落到腾讯。
func TestFetchDayKLineTencentFallback(t *testing.T) {
	m := data.NewMarketAPI()
	m.SetTransport(klineChainTransport{})
	e := New(m)

	klines := e.fetchDayKLine("600206")
	if len(klines) != 2 {
		t.Fatalf("应落到腾讯返回2根, got %d", len(klines))
	}
	if klines[1].Close != 43.79 {
		t.Errorf("末根 close 应43.79, got %.2f", klines[1].Close)
	}
	src := e.takeKLineSrc()
	if src["腾讯"] != 1 {
		t.Errorf("K线源统计应含 腾讯=1, got %v", src)
	}
}

// TestFetchMinuteKLineTencentFallback 分钟K新浪失败时落到腾讯分钟源。
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

// TestFetchDayKLineAllFail 全部源失败时返回 nil 且统计"失败"。
func TestFetchDayKLineAllFail(t *testing.T) {
	m := data.NewMarketAPI()
	m.SetTransport(allFailTransport{})
	e := New(m)

	if klines := e.fetchDayKLine("600206"); len(klines) != 0 {
		t.Fatalf("全失败应返回空, got %d", len(klines))
	}
	if src := e.takeKLineSrc(); src["失败"] != 1 {
		t.Errorf("应统计 失败=1, got %v", src)
	}
}

// allFailTransport 所有 host 均返回空响应。
type allFailTransport struct{}

func (allFailTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return testRespEngine(200, ""), nil
}