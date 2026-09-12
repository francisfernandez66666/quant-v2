// Package e2e §SHORT-5 做空全链路 e2e：真实四战法 runner（NewShortRunners）→ ScanShort
// 信号产出（持仓 sell / 非持仓 watch）→ SellAction 归一（决策②口径）→ 模拟盘融券账本
// 建仓/平多路由与做空池隔离（决策④）。开关关闭全链路静默。
// English: §SHORT-5 end-to-end short pipeline — real bear tactics through ScanShort, the
// SellAction normalization, and the paper margin-short book routing; fully silent when gated off.
package e2e

import (
	"testing"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/paper"
	"quant-trading-v2/internal/strategy_engine"
)

// brokenKLines 40 根日K：前段平台 20 元，末根高开低走放量收 13.6（双破位+放量+大面）。
// English: 40 daily bars ending in a high-open/low-close volume breakdown bar.
func brokenKLines() []data.KLine {
	kl := make([]data.KLine, 40)
	for i := range kl {
		c := 20.0
		kl[i] = data.KLine{Date: time.Now().AddDate(0, 0, i-40), Open: c, High: c, Low: c * 0.99, Close: c, Volume: 1e6}
	}
	last := &kl[39]
	last.Open, last.High, last.Low, last.Close, last.Volume = 14.5, 15.2, 13.2, 13.6, 2.6e6
	return kl
}

func TestShortPipelineTactics(t *testing.T) {
	agent := combat_agent.New(&config.StrategyConfig{})
	agent.SetShortRunners(combat_agent.NewShortRunners(nil))
	agent.SetShortEnabled(true)

	md := func(code, name string) *strategy_engine.StockMarketData {
		m := &strategy_engine.StockMarketData{Code: code, Name: name, Price: 13.6, ChangePct: -6.2, KLines: brokenKLines()}
		m.MinuteKLine = nil
		return m
	}
	in := combat_agent.ScanInput{
		IndividualStocks: []string{"600001.SH", "600002.SH"},
		MarketData: map[string]*strategy_engine.StockMarketData{
			"600001.SH": md("600001.SH", "持仓股"),
			"600002.SH": md("600002.SH", "非持仓股"),
		},
		HeldCodes: map[string]bool{"600001.SH": true},
		Scores:    map[string]combat_agent.StockScores{},
	}

	// 1) 开关开：两票都应出放量破位做空信号；持仓=sell，非持仓=watch。
	sigs := agent.ScanShort(in)
	byCode := map[string]combat_agent.Signal{}
	for _, s := range sigs {
		byCode[s.Code] = s
	}
	held, okHeld := byCode["600001.SH"]
	watch, okWatch := byCode["600002.SH"]
	if !okHeld || held.Direction != "做空" || held.Action != "sell" {
		t.Fatalf("持仓股应产出做空 sell 信号: %+v ok=%v", held, okHeld)
	}
	if held.StrategyType != "break_down" {
		t.Fatalf("信号战法类型应为 break_down: %+v", held)
	}
	if !okWatch || watch.Action != "watch" {
		t.Fatalf("非持仓股应降级 watch: %+v ok=%v", watch, okWatch)
	}
	// 2) SellAction 归一（决策②）：sell=close 平多、watch 不触发动作。
	if combat_agent.SellAction(held) != "close" {
		t.Fatal("做空战法 sell 信号应归一 close")
	}
	if combat_agent.SellAction(watch) != "" {
		t.Fatal("watch 信号不应触发动作")
	}

	// 3) 模拟盘融券账本：两票在本纸面账户均无多仓 → 各开一笔空头（平多只发生在持该多仓的
	// 账户/账簿内，账本间互不挪用；跨账簿隔离由 registry 逐仓路由保证，见 paper 侧测试）。
	// English: this paper account holds neither long, so both codes open shorts — long closes
	// happen only inside the account/book that holds them (per-account routing, paper-side tests).
	pe := paper.New(paper.Config{
		Enabled: true, FixedAmount: 10000, InitialCapital: 100000, AutoSell: true,
		ShortEnabled: true, ShortCapital: 100000, ShortMarginRate: 0.5,
	}, "")
	cashBefore := pe.Stats().Cash
	pe.OnSignals([]combat_agent.Signal{watch, held}, map[string]*data.StockInfo{
		"600002.SH": {Price: 13.6}, "600001.SH": {Price: 13.6},
	})
	sb := pe.ShortBook()
	codes := map[string]bool{}
	for _, p := range sb.Positions {
		codes[p.Code] = true
	}
	if !sb.Enabled || !codes["600002.SH"] || !codes["600001.SH"] {
		t.Fatalf("做空战法信号应融券开仓: %+v", sb)
	}
	if pe.Stats().Cash != cashBefore {
		t.Fatalf("融券开仓不得动用做多侧资金: %v → %v", cashBefore, pe.Stats().Cash)
	}
	if sb.MarginUsed <= 0 || sb.Equity <= 0 {
		t.Fatalf("担保/权益字段缺失: %+v", sb)
	}

	// 4) 开关关（全局门）：整条管道零信号、账本不动。
	agent.SetShortEnabled(false)
	if got := agent.ScanShort(in); got != nil {
		t.Fatalf("开关关闭应零信号, got %d", len(got))
	}
}
