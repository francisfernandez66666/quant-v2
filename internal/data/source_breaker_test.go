// source_breaker_test.go — §修复 THS-BREAKER(20260920) 集成级回归：
// 熔断必须按**操作域**隔离，且"客户端能力缺失"（同花顺不支持的分钟周期）不得被
// 当成"供应商故障"熔断——否则一次 15 分钟请求就会把已正常工作的同花顺
// 报价/板块链路一起挡掉 60s（旧实现的真实自伤形态）。
// English: integration-level regression for per-operation THS circuit breaking; a
// capability miss (unsupported intraday period) must not trip the provider breaker.
package data

import (
	"net/http"
	"sync/atomic"
	"testing"
)

// countingTHSTransport 统计同花顺被实际请求的次数并按需返回报文。
// English: counts actual THS requests and replies with a fixed payload.
type countingTHSTransport struct {
	calls int32
	body  string
	code  int
}

func (t *countingTHSTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	atomic.AddInt32(&t.calls, 1)
	return testResp(t.code, t.body), nil
}

// allDownTransport 让除同花顺外的所有上游（新浪/腾讯/东财）全失败，
// 把调用路径逼到同花顺这一环，从而单独观察熔断行为。
// English: fails every non-THS upstream so the call path reaches the THS leg.
type allDownTransport struct{}

func (allDownTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return testResp(404, ""), nil
}

// TestMinuteKLineUnsupportedPeriodDoesNotTripTHSBreaker 核心不变量：
// 15 分钟周期同花顺不提供（返回 ErrTHSUnsupportedPeriod）→ 必须**不熔断**、
// **不发请求**，且同花顺其余操作域全部保持可用。
// 旧实现：唯一的 thsDeadline 被置位 → 报价/板块/日线链路连带被挡 60s。
func TestMinuteKLineUnsupportedPeriodDoesNotTripTHSBreaker(t *testing.T) {
	tc := NewTHSClient()
	thsT := &countingTHSTransport{code: 200, body: `{"data":""}`}
	tc.SetTransport(thsT)

	m := NewMarketAPI()
	m.SetTransport(allDownTransport{})
	dc := NewDataCoordinator(m, tc)

	if _, err := dc.GetMinuteKLine("600519", 15, 100); err == nil {
		t.Fatal("全部源均无 15 分钟数据时应返回错误")
	}
	if n := atomic.LoadInt32(&thsT.calls); n != 0 {
		t.Fatalf("不支持的周期不应发出同花顺请求，实际 %d 次", n)
	}
	for _, op := range []string{thsOpMinute, thsOpQuote, thsOpKLine, thsOpBoards, thsOpBoardStock} {
		if !dc.thsAvailable(op) {
			t.Fatalf("不支持的周期不得熔断同花顺：%s 域被误关", op)
		}
	}
}

// TestMinuteKLineProviderFailureTripsOnlyMinuteDomain：同花顺**真的**取不到数据
// （上游 500 / 报文不可解析）时，应熔断——但只熔断分钟域，报价/板块/日线域不受影响。
func TestMinuteKLineProviderFailureTripsOnlyMinuteDomain(t *testing.T) {
	tc := NewTHSClient()
	thsT := &countingTHSTransport{code: 500, body: ""}
	tc.SetTransport(thsT)

	m := NewMarketAPI()
	m.SetTransport(allDownTransport{})
	dc := NewDataCoordinator(m, tc)

	if _, err := dc.GetMinuteKLine("600519", 5, 100); err == nil {
		t.Fatal("全部源均失败时应返回错误")
	}
	if n := atomic.LoadInt32(&thsT.calls); n == 0 {
		t.Fatal("支持的周期应真实请求同花顺（用于暴露供应商故障）")
	}
	if dc.thsAvailable(thsOpMinute) {
		t.Fatal("同花顺分钟线真实失败后应熔断该域")
	}
	for _, op := range []string{thsOpQuote, thsOpKLine, thsOpBoards, thsOpBoardStock} {
		if !dc.thsAvailable(op) {
			t.Fatalf("分钟域熔断不得连带关闭 %s 域", op)
		}
	}
}

// TestQuoteChainUnaffectedByMinuteBreaker 用户可见面：分钟线熔断后，
// 同花顺报价链路仍可正常出数（夹具为线上真实 realhead 报文）。
// 这条覆盖"熔断语义=该能力不可用，而非该供应商挂了"这一用户可感知后果。
func TestQuoteChainUnaffectedByMinuteBreaker(t *testing.T) {
	tc := NewTHSClient()
	tc.SetTransport(&countingTHSTransport{code: 200, body: thsReal600580})

	m := NewMarketAPI()
	m.SetTransport(allDownTransport{})
	dc := NewDataCoordinator(m, tc)

	// 先制造"分钟线供应商故障"熔断
	dc.tripThs(thsOpMinute)

	si, err := dc.GetQuote("600580")
	if err != nil {
		t.Fatalf("分钟域熔断不应影响报价链路: %v", err)
	}
	if si.Price != 27.27 {
		t.Fatalf("报价应取同花顺真实字段 10=27.27, got %v", si.Price)
	}
}
