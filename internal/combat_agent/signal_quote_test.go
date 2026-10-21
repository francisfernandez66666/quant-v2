// §HISQUOTE（2026-10-22）：信号生成时刻行情冻结的判据锁。
//
// 台账 2026-10-15 第二条要求「历史信号有涨跌幅」：做法不是查询时补实时价，而是在信号生成点把
// 当时的 price/change_pct/prev_close 抄进 Signal，随批次落盘。本文件锁住四条最容易回归的语义：
//  1. price<=0 或涨跌幅不可得（且无昨收可算）→ ChangePct 必须保持 nil（前端显示「—」），
//     不能折叠成 0.00%；
//  2. 真平盘 → 必须是指向 0 的指针（与 nil 可区分）；
//  3. 有昨收时按 price/prevClose 重算涨跌幅（不同数据源混用时口径统一），并四舍五入到两位；
//  4. 打分池行情的涨跌幅基准只能是该股行情本身，**信号自己的触发价不能当现价**。
//
// English: locks the generation-time quote freeze — no quote leaves ChangePct nil, a genuine flat
// reading is a pointer to 0, prev-close wins for a consistent change-% basis, and the signal's own
// trigger price is never used as a quote basis.
package combat_agent

import (
	"encoding/json"
	"math"
	"testing"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/strategy_engine"
)

func approx(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-6 {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestFreezeQuoteNoPriceKeepsNil(t *testing.T) {
	sig := Signal{Code: "600000"}
	freezeQuote(&sig, 0, 1.23, 10)
	if sig.ChangePct != nil {
		t.Fatalf("无行情必须保持 nil，got %v", *sig.ChangePct)
	}
	if sig.PrevClose != 0 {
		t.Fatalf("无行情不得写昨收，got %v", sig.PrevClose)
	}
	// 负价与 NaN 同样视为无行情
	for _, p := range []float64{-1, math.NaN(), math.Inf(1)} {
		s := Signal{}
		freezeQuote(&s, p, 1, 0)
		if s.ChangePct != nil {
			t.Fatalf("price=%v 必须保持 nil", p)
		}
	}
}

func TestFreezeQuoteFlatIsZeroNotNil(t *testing.T) {
	sig := Signal{}
	freezeQuote(&sig, 10, 0, 0)
	if sig.ChangePct == nil {
		t.Fatal("真平盘必须是指向 0 的指针（nil 表示无行情，两者不可混）")
	}
	if *sig.ChangePct != 0 {
		t.Fatalf("got %v want 0", *sig.ChangePct)
	}
	approx(t, sig.PrevClose, 10)
}

func TestFreezeQuoteDerivesPrevClose(t *testing.T) {
	sig := Signal{}
	freezeQuote(&sig, 11, 10, 0)
	approx(t, sig.PrevClose, 10)
	approx(t, *sig.ChangePct, 10)
}

func TestFreezeQuotePrevCloseWinsAndRounds(t *testing.T) {
	sig := Signal{}
	freezeQuote(&sig, 9, 0, 10) // 涨跌幅给了 0，但昨收 10 → 真实是 -10%
	approx(t, sig.PrevClose, 10)
	approx(t, *sig.ChangePct, -10)

	// 四舍五入：涨跌幅保留 2 位，昨收保留 4 位
	s2 := Signal{}
	freezeQuote(&s2, 10, 1.2345, 0)
	if got := *s2.ChangePct; got != math.Round(got*100)/100 {
		t.Fatalf("涨跌幅必须保留两位，got %v", got)
	}
	if got := s2.PrevClose; got != math.Round(got*10000)/10000 {
		t.Fatalf("昨收必须保留四位，got %v", got)
	}
}

// TestFreezeQuoteMissingChangePctStaysNil 涨跌幅不可得且无昨收可算时，必须保持 nil（前端「—」）。
// 反证靶心：把 NaN 折叠成 0 会让历史批次报一个不存在的 0.00%——与「缺行情显示 0.00%」同族。
// 有昨收时仍按 price/prevClose 算出真值，不能因为上游缺这一个字段就丢掉整格。
func TestFreezeQuoteMissingChangePctStaysNil(t *testing.T) {
	for _, c := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		sig := Signal{}
		freezeQuote(&sig, 10, c, 0)
		if sig.ChangePct != nil {
			t.Fatalf("change=%v 且无昨收时必须保持 nil（否则会报假平盘）, got %v", c, *sig.ChangePct)
		}
		if sig.PrevClose != 0 {
			t.Fatalf("change=%v 不得反推出昨收, got %v", c, sig.PrevClose)
		}
	}
	withPC := Signal{}
	freezeQuote(&withPC, 9, math.NaN(), 10)
	if withPC.ChangePct == nil {
		t.Fatal("有昨收时 NaN 涨跌幅应按 price/prevClose 算出（-10%）")
	}
	approx(t, *withPC.ChangePct, -10)
}

func TestFreezeMarketQuote(t *testing.T) {
	// nil 行情不改写
	s := Signal{}
	freezeMarketQuote(&s, nil)
	if s.ChangePct != nil {
		t.Fatal("nil 行情必须保持 nil")
	}

	// 行情自带昨收：即使 ChangePct 与价格不自洽，也按昨收重算
	md := &strategy_engine.StockMarketData{
		Price: 10, ChangePct: 5,
		Quote: &data.StockInfo{Price: 10, ChangePct: 5, PrevClose: 9.5},
	}
	s2 := Signal{}
	freezeMarketQuote(&s2, md)
	approx(t, s2.PrevClose, 9.5)
	// 按昨收重算后再四舍五入到两位
	want := math.Round((10.0/9.5-1)*100*100) / 100
	approx(t, *s2.ChangePct, want)

	// 反证靶心：信号自己的触发价（s3.Price=20）不是行情基准。若实现拿触发价当现价，
	// 这里会冻出 (20/9.5-1)≈+110%——历史批次就会报一个当时根本没发生的涨幅。
	md2 := &strategy_engine.StockMarketData{
		Price: 10, ChangePct: 5,
		Quote: &data.StockInfo{Price: 10, ChangePct: 5, PrevClose: 9.5},
	}
	s3 := Signal{Price: 20}
	freezeMarketQuote(&s3, md2)
	if s3.ChangePct == nil {
		t.Fatal("有行情必须写涨跌幅")
	}
	approx(t, *s3.ChangePct, want)
	approx(t, s3.PrevClose, 9.5)

	// 无昨收时直接用行情自带的涨跌幅（同样不受触发价影响）
	md3 := &strategy_engine.StockMarketData{Price: 10, ChangePct: 5}
	s4 := Signal{Price: 20}
	freezeMarketQuote(&s4, md3)
	if s4.ChangePct == nil {
		t.Fatal("有行情必须写涨跌幅")
	}
	approx(t, *s4.ChangePct, 5)
}

func TestFreezeStockQuote(t *testing.T) {
	s := Signal{}
	freezeStockQuote(&s, nil)
	if s.ChangePct != nil {
		t.Fatal("nil quote 必须保持 nil")
	}
	s2 := Signal{}
	freezeStockQuote(&s2, &data.StockInfo{Price: 12, ChangePct: -2.5, PrevClose: 12.3077})
	if s2.ChangePct == nil {
		t.Fatal("有 quote 必须写涨跌幅")
	}
	approx(t, *s2.ChangePct, -2.5)
	approx(t, s2.PrevClose, 12.3077)
}

// TestSignalChangePctJSONNilVsZero 落盘快照必须能区分「无行情」与「平盘」：
// nil 走 omitempty 不出现该键；指针 0 必须序列化成 "change_pct":0。
// 反序列化侧：键缺失 = nil，键为 0 = 指向 0。
func TestSignalChangePctJSONNilVsZero(t *testing.T) {
	raw, err := json.Marshal(Signal{Code: "600000"})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["change_pct"]; ok {
		t.Fatalf("无行情不得序列化 change_pct，got %s", raw)
	}

	zero := 0.0
	raw2, err := json.Marshal(Signal{Code: "600000", ChangePct: &zero})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw2, &m); err != nil {
		t.Fatal(err)
	}
	if v, ok := m["change_pct"]; !ok || v.(float64) != 0 {
		t.Fatalf("平盘必须序列化为 change_pct:0，got %s", raw2)
	}

	var back Signal
	if err := json.Unmarshal(raw2, &back); err != nil {
		t.Fatal(err)
	}
	if back.ChangePct == nil || *back.ChangePct != 0 {
		t.Fatal("反序列化后平盘必须是指向 0 的指针")
	}
	var missing Signal
	if err := json.Unmarshal([]byte(`{"code":"600000"}`), &missing); err != nil {
		t.Fatal(err)
	}
	if missing.ChangePct != nil {
		t.Fatal("键缺失必须反序列化为 nil")
	}
}
