// report_stamp_20261119_test.go — §REPORT-STAMP：载体自闭环这条腿的委托/成交时间补日期。
//
// 事故（2026-11-19 实盘）：载体把 QMT 导出原样上行，`created_at` 只有 `HH:MM:SS`，
// 云端 orders/fills 落成 `10:01:51` 这种形态。所有 day-keyed 读面按 `YYYY-MM-DD` 前缀过滤
// ⇒ 当日 9 笔真实委托、7 笔成交在页面上一条都看不到（而柜台确实成交了）。
// 同批还发现柜台委托号**按会话递增、跨日复用**，占位键 `ext:<号>` 会被前一天同号行吃掉，
// 所以占位键改为 `ext:<yyyymmdd>:<号>`（消费方判的都是前缀，语义不变）。
//
// English: §REPORT-STAMP — the carrier export carries time-of-day only, so orders/fills landed
// without a date and every day-keyed read missed them; the placeholder key also collides with the
// previous day's same broker order id, so the key now carries the day.
package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// postCarrierReport 走真实 handler（不做任何字段预处理），模拟载体上行的一次性事件。
func postCarrierReport(t *testing.T, s *Server, body string) {
	t.Helper()
	rr := httptest.NewRecorder()
	s.handleQMTReport(rr, withReportUser(httptest.NewRequest(http.MethodPost, "/api/qmt/report",
		bytes.NewBufferString(body))))
	if rr.Code < 200 || rr.Code >= 300 {
		t.Fatalf("回报应 2xx, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestReportStampAddsDateToCarrierOrder 只带时分秒的委托必须落成完整北京时间戳，
// 否则 `/api/orders` 的当日前缀过滤整天读不到它（现网 11-19 的真实形态）。
func TestReportStampAddsDateToCarrierOrder(t *testing.T) {
	s, db, _ := newTestResearchServer(t)
	postCarrierReport(t, s, `{"type":"order","order_id":"1253","code":"000034.SZ","side":"买入",`+
		`"status":"已成","price":21.89,"qty":900,"created_at":"10:01:51",`+
		`"remark":"v109-20261119-FC000034","at":"2026-11-19T10:58:19+08:00"}`)

	orders, err := db.RealOrders()
	if err != nil {
		t.Fatalf("read orders: %v", err)
	}
	if len(orders) != 1 {
		t.Fatalf("应落 1 行, got %d", len(orders))
	}
	o := orders[0]
	if o.CreatedAt != "2026-11-19 10:01:51" {
		t.Fatalf("委托时间应补日期（只有时分秒 ⇒ 当日读面读不到）, got %q", o.CreatedAt)
	}
	if !strings.HasPrefix(o.CreatedAt, "2026-11-19") {
		t.Fatalf("当日前缀必须命中, got %q", o.CreatedAt)
	}
	if o.SignalID != "ext:20261119:1253" {
		t.Fatalf("占位键应带当日（跨日同号会互相吞掉）, got %q", o.SignalID)
	}
}

// TestReportStampSameOrderIDOnTwoDays 柜台委托号跨日复用：两天的同一号必须各成一行。
func TestReportStampSameOrderIDOnTwoDays(t *testing.T) {
	s, db, _ := newTestResearchServer(t)
	postCarrierReport(t, s, `{"type":"order","order_id":"1253","code":"000034.SZ","side":"买入",`+
		`"status":"已成","price":21.89,"qty":900,"created_at":"10:01:51","at":"2026-11-18T10:58:19+08:00"}`)
	postCarrierReport(t, s, `{"type":"order","order_id":"1253","code":"000034.SZ","side":"买入",`+
		`"status":"已成","price":21.89,"qty":100,"created_at":"10:46:48","at":"2026-11-19T10:58:19+08:00"}`)

	orders, err := db.RealOrders()
	if err != nil {
		t.Fatalf("read orders: %v", err)
	}
	if len(orders) != 2 {
		t.Fatalf("跨日同号应各成一行（否则今天的委托被昨天那行吞掉）, got %d 行", len(orders))
	}
	byDay := map[string]string{}
	for _, o := range orders {
		byDay[o.SignalID] = o.CreatedAt
	}
	if byDay["ext:20261118:1253"] != "2026-11-18 10:01:51" {
		t.Fatalf("18 日那行不对: %v", byDay)
	}
	if byDay["ext:20261119:1253"] != "2026-11-19 10:46:48" {
		t.Fatalf("19 日那行不对: %v", byDay)
	}
}

// TestReportStampAddsDateToCarrierFill 成交腿同一形态：traded_at 只有时分秒 ⇒ 当日成交读面、
// 日报盈亏、战法归因的日期全部落空。
func TestReportStampAddsDateToCarrierFill(t *testing.T) {
	s, db, _ := newTestResearchServer(t)
	postCarrierReport(t, s, `{"type":"trade","order_id":"1253","trade_id":"103000053404173",`+
		`"code":"000034.SZ","side":"买入","status":"已成","price":21.89,"qty":900,"amount":19701,`+
		`"traded_at":"10:01:51","remark":"v109-20261119-FC000034","at":"2026-11-19T10:58:19+08:00"}`)

	fills, err := db.RealFills()
	if err != nil {
		t.Fatalf("read fills: %v", err)
	}
	var hit *struct{ at, sid string }
	for _, f := range fills {
		if f.TradeID == "103000053404173" {
			hit = &struct{ at, sid string }{f.TradedAt, f.SignalID}
		}
	}
	if hit == nil {
		t.Fatalf("成交应落库, got %d 行", len(fills))
	}
	if hit.at != "2026-11-19 10:01:51" {
		t.Fatalf("成交时间应补日期, got %q", hit.at)
	}
	if hit.sid != "buy:000034.SZ:factor_composite:2026-11-19" {
		t.Fatalf("战法归因的日期应取自完整时间戳, got %q", hit.sid)
	}
}

// TestReportStampLeavesFullTimestampAlone 旧网关口径（带日期）必须逐字节不变。
func TestReportStampLeavesFullTimestampAlone(t *testing.T) {
	if got := reportStampFromParts("2026-11-19 09:31:00", "2026-11-19T10:00:00+08:00"); got != "2026-11-19 09:31:00" {
		t.Fatalf("带日期的原样保留, got %q", got)
	}
	if got := reportStampFromParts("1999-01-01 00:00:00", ""); got != "1999-01-01 00:00:00" {
		t.Fatalf("带日期的原样保留（不依赖 at）, got %q", got)
	}
	if got := normalizeClock("9:xx:5"); got != "" {
		t.Fatalf("非时刻形态应判空, got %q", got)
	}
	if got := normalizeClock("9:01:05"); got != "09:01:05" {
		t.Fatalf("单位数小时应补零, got %q", got)
	}
}
