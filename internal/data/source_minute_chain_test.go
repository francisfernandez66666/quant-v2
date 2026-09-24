// source_minute_chain_test.go — §MINUTE-K 分钟链两条口径（2026-09-24 回填实跑锤出的两个坑）。
//
//  1. **代码形态**：三条分钟腿（新浪/腾讯的 symbol、同花顺 URL）只认裸 6 位代码。09-24 装载实跑
//     传的是 ts_code（"600000.SH"），新浪把 "sh600000.SH" 当代码直接回 null（**0 根、无错误**），
//     腾讯回数组壳（解到 map 上报 unmarshal 错）——日志长得像"源失效所以降级"，其实一条都没
//     真取到数。归一后 ts_code 形态的入参也必须出数，且出站 URL 里是裸代码。
//  2. **复权口径**：链尾的东财腿固定 fqt=1（前复权），落库表承诺不复权，所以装载器走的
//     GetUnadjustedMinuteKLine 必须**根本不请求**这条腿（不是"请求了再丢"）；实盘看当日分时的
//     GetMinuteKLine 保留该末腿（当日 bars 前复权＝不复权）。
//
// English: the minute chain must (1) normalize ts_code to the bare 6-digit code the upstreams
// actually accept, and (2) never touch the qfq EastMoney leg in the strict unadjusted variant.
package data

import (
	"net/http"
	"strings"
	"sync"
	"testing"
)

// minuteURLRecorder 按 host 回放固定报文并登记出站 URL（分钟链断言用）。
// minuteURLRecorder replays canned bodies per host and records outbound URLs.
type minuteURLRecorder struct {
	mu     sync.Mutex
	urls   []string
	bodies map[string]string // host → 响应体（缺省 404）
}

func (r *minuteURLRecorder) count(sub string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, u := range r.urls {
		if strings.Contains(u, sub) {
			n++
		}
	}
	return n
}

// RoundTrip 假传输：登记 URL，命中 bodies 则 200 回放，否则 404（该腿按失败记账）。
func (r *minuteURLRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	r.urls = append(r.urls, req.URL.String())
	r.mu.Unlock()
	if body, ok := r.bodies[req.URL.Host]; ok {
		return testResp(http.StatusOK, body), nil
	}
	return testResp(http.StatusNotFound, ""), nil
}

// sinaMinuteBody 新浪 5 分钟线可用响应（两只连续根，字段全为字符串）。
const sinaMinuteBody = `[{"day":"2026-09-24 14:50:00","open":"10.0","high":"10.1","low":"9.9","close":"10.05","volume":"1000","amount":"10050"},` +
	`{"day":"2026-09-24 14:55:00","open":"10.05","high":"10.2","low":"10.0","close":"10.15","volume":"1200","amount":"12180"}]`

// TestMinuteChainNormalizesTsCodeToBareCode 入参写 ts_code 时，出站请求必须是裸 6 位代码。
// 这条锁的正是要害：**旧实现不报错、只是空返回**，所以断言不能只看 err==nil，必须看 URL 形态
// 并且真的拿到根数（拿不到根数就是 09-24 实跑那个假降级现场）。
func TestMinuteChainNormalizesTsCodeToBareCode(t *testing.T) {
	rec := &minuteURLRecorder{bodies: map[string]string{"money.finance.sina.com.cn": sinaMinuteBody}}
	m := NewMarketAPI()
	m.SetTransport(rec)
	dc := NewDataCoordinator(m, NewTHSClient())

	for _, in := range []string{"600000.SH", "sh600000", "600000"} {
		rec.mu.Lock()
		rec.urls = nil
		rec.mu.Unlock()
		kls, err := dc.GetMinuteKLine(in, 5, 48)
		if err != nil {
			t.Fatalf("%s 应命中新浪腿: %v", in, err)
		}
		if len(kls) != 2 {
			t.Fatalf("%s 应返回 2 根，实际 %d", in, len(kls))
		}
		if !strings.Contains(rec.urls[0], "symbol=sh600000&") {
			t.Fatalf("%s 出站 URL 未归一为裸代码：%s", in, rec.urls[0])
		}
		if strings.Contains(rec.urls[0], ".SH") || strings.Contains(rec.urls[0], ".SZ") {
			t.Fatalf("%s 出站 URL 仍带交易所后缀：%s", in, rec.urls[0])
		}
	}
}

// TestUnadjustedMinuteChainNeverRequestsQFQLeg 严格不复权链**一次都不请求**东财（末腿是前复权）；
// 通用链（实盘看当日分时）保留该腿。用调用次数做判据而不是返回值：两条链到末腿都会因为没有其它
// 可用源而返回错误，只有"有没有发出那个请求"能区分"拒用"与"取完丢掉"。
func TestUnadjustedMinuteChainNeverRequestsQFQLeg(t *testing.T) {
	newD := func() (*DataCoordinator, *minuteURLRecorder) {
		rec := &minuteURLRecorder{bodies: map[string]string{}} // 所有 host 404：没有主源可用
		m := NewMarketAPI()
		m.SetTransport(rec)
		tc := NewTHSClient()
		tc.SetTransport(rec)
		return NewDataCoordinator(m, tc), rec
	}

	dc, rec := newD()
	if _, err := dc.GetUnadjustedMinuteKLine("600000.SH", 5, 48); err == nil {
		t.Fatal("三条不复权腿全失效时必须报错，不许静默降级到前复权腿")
	}
	if n := rec.count("push2.eastmoney.com"); n != 0 {
		t.Fatalf("严格不复权链请求了东财前复权腿 %d 次（该腿会污染 minute_klines 口径）", n)
	}

	dc2, rec2 := newD()
	if _, err := dc2.GetMinuteKLine("600000.SH", 5, 48); err == nil {
		t.Fatal("夹具下所有腿都应失败")
	}
	if n := rec2.count("push2.eastmoney.com"); n == 0 {
		t.Fatal("通用链的东财末腿被摘掉了：实盘分时兜底不该受本批改动影响")
	}
}

// TestMinuteChainFailureNamesEveryLeg 全链失败时的错误必须带逐腿原因（09-24 实跑只能靠
// 猜腿定位问题；"所有分钟K线源均失败"这一行不带形态信息，等于没带）。
func TestMinuteChainFailureNamesEveryLeg(t *testing.T) {
	rec := &minuteURLRecorder{bodies: map[string]string{}}
	m := NewMarketAPI()
	m.SetTransport(rec)
	tc := NewTHSClient()
	tc.SetTransport(rec)
	dc := NewDataCoordinator(m, tc)

	_, err := dc.GetUnadjustedMinuteKLine("000001.SZ", 5, 48)
	if err == nil {
		t.Fatal("应失败")
	}
	msg := err.Error()
	if !strings.Contains(msg, "000001.SZ") {
		t.Fatalf("错误里要能认出调用方给的形态：%s", msg)
	}
	for _, leg := range []string{"新浪", "同花顺"} {
		if !strings.Contains(msg, leg+"=") {
			t.Fatalf("缺少 %s 腿的失败记账：%s", leg, msg)
		}
	}
}
