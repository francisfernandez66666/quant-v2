// price_source_contract_test.go — §C7 行情来源契约单测。
//
// 锁四件事：
//  1. live 才是真实价格，涨跌幅/盈亏才可用；
//  2. trigger 只是信号触发价，不能当现价、不能给涨跌幅；
//  3. cost 只是成本回退，涨跌/盈亏必须标不可用；
//  4. none 两者都没有。
//
// English: §C7 locks — only live is a real quote; trigger/cost never masquerade as live, and
// their change/PnL flags stay false.
package server

import (
	"testing"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/display"
	"quant-trading-v2/internal/report"
)

func TestQuoteSourceContract(t *testing.T) {
	cases := []struct {
		name    string
		info    *data.StockInfo
		trigger float64
		price   float64
		kind    string
		quoteOk bool
		chgOk   bool
	}{
		{"live", &data.StockInfo{Price: 9.99, ChangePct: 1.2}, 9.5, 9.99, "live", true, true},
		{"trigger", nil, 9.5, 9.5, "trigger", false, false},
		{"none", nil, 0, 0, "none", false, false},
		// 行情对象存在但价格无效时，仍只能回退触发价，不能标 live。
		{"zero_quote_falls_back", &data.StockInfo{Price: 0, ChangePct: 3}, 9.5, 9.5, "trigger", false, false},
	}
	for _, c := range cases {
		price, chg, kind, quoteOk, chgOk := quoteSource(c.info, c.trigger)
		if price != c.price || kind != c.kind || quoteOk != c.quoteOk || chgOk != c.chgOk {
			t.Fatalf("[%s] got price=%v kind=%s quote=%v chg=%v（chg=%v）", c.name, price, kind, quoteOk, chgOk, chg)
		}
		if kind != "live" && chg != 0 {
			t.Fatalf("[%s] 非 live 不应携带涨跌幅, got %v", c.name, chg)
		}
	}
}

func TestHoldingSourceContract(t *testing.T) {
	cases := []struct {
		name    string
		info    *data.StockInfo
		entry   float64
		cur     float64
		kind    string
		quoteOk bool
		chgOk   bool
		pnlOk   bool
	}{
		{"live", &data.StockInfo{Price: 11, ChangePct: 2}, 10, 11, "live", true, true, true},
		{"cost", nil, 10, 10, "cost", false, false, false},
		{"none", nil, 0, 0, "none", false, false, false},
		// 有行情但成本为 0：价格是 live，盈亏仍不可算。
		{"live_no_cost", &data.StockInfo{Price: 11, ChangePct: 2}, 0, 11, "live", true, true, false},
	}
	for _, c := range cases {
		cur, _, kind, quoteOk, chgOk, pnlOk := holdingSource(c.info, c.entry)
		if cur != c.cur || kind != c.kind || quoteOk != c.quoteOk || chgOk != c.chgOk || pnlOk != c.pnlOk {
			t.Fatalf("[%s] got cur=%v kind=%s quote=%v chg=%v pnl=%v", c.name, cur, kind, quoteOk, chgOk, pnlOk)
		}
	}
}

// TestBuildHoldingNoQuoteMarksCost 无行情时现价字段仍回填成本（兼容旧前端），但来源必须是 cost。
func TestBuildHoldingNoQuoteMarksCost(t *testing.T) {
	s := &Server{agg: display.New()}
	h := s.buildHolding(report.ExecLog{
		Code: "600000.SH", Name: "浦发银行", EntryPrice: 10, Quantity: 100, SignalID: "T-C7",
	}, "u")
	if h.PriceKind != "cost" {
		t.Fatalf("无行情应标 cost, got %q", h.PriceKind)
	}
	if h.QuoteOK || h.ChangeOK || h.PnlOK {
		t.Fatalf("cost 态的 ok 标志必须为 false: quote=%v chg=%v pnl=%v", h.QuoteOK, h.ChangeOK, h.PnlOK)
	}
	if h.CurPrice != 10 || h.ChangePct != 0 || h.PnlPct != 0 {
		t.Fatalf("兼容数值不符: cur=%v chg=%v pnl=%v", h.CurPrice, h.ChangePct, h.PnlPct)
	}
}
