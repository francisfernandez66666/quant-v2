// flow_factor_live_test.go — §FLOW-FACTOR R3「休市态」现网实测（2026-10-26 补测项）
//
// 为什么需要这条：R3 的日期断言只保护 **5 日维**（末行 ≠ 本交易日即整体弃权），当日维靠
// `HasFlow` 判"有没有"、没有自己的日期戳。休市日上游仍会给上一交易日的收盘快照，所以
// "休市态下两个维度各自怎么读数"必须实测，不能靠推演。
//
// 做法：不改任何生产代码，只把**假时钟**喂给同一条解析链（`flowRatiosResolve`），历史数据仍
// 来自真实上游。两个场景一次跑完：
//
//	A 真休市（时钟钉到最近一个周六）：TradingDayDate 回退到该周周五 ⇒ 5 日维**必须命中**、
//	  当日维**必须弃权**（HasFlow=false）——这就是 R3 承诺的"当日弃权、5 日照用"。
//	B 交易日盘前（时钟=现在）：TradingDayDate 就是今天，而上游日线还没有今天那一行 ⇒ 两个维度
//	  都弃权（hist_stale + no_flow_today）。这是**设计内**形态（缺数不伪造），只记录不判红，
//	  但它是"盘前整段因子失效"的唯一证据，必须留档。
//
// 默认跳过（要走公网）：
//
//	UAT_LIVE=1 go test ./internal/engine -run TestFlowClosedMarketLive -count=1 -v
//
// English: R3 closed-market live probe. A fake clock is fed into the very same resolver while the
// history still comes from the real upstream, so scenario A (a real weekend) must show "5-day usable,
// same-day abstained" and scenario B (pre-open of a trading day) documents the by-design double abstain.
package engine

import (
	"os"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/data"
)

// liveProbeCodes 三只流动性充足、资金流必然有数的票（沪深各取）。可用 UAT_LIVE_CODES 覆盖
// （逗号分隔）——在服务器侧跑时用来把上游调用量压到最小。
// English: three liquid codes; override with UAT_LIVE_CODES to minimise upstream calls.
func liveProbeCodes() []string {
	if v := os.Getenv("UAT_LIVE_CODES"); v != "" {
		var out []string
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return []string{"600519", "000001", "601318"}
}

// lastSaturday 最近一个周六 12:00（A 场景时钟：保证 TradingDayDate 一定回退到上一交易日）。
// English: the most recent Saturday noon — a clock that always rolls the trading day back.
func lastSaturday(now time.Time) time.Time {
	d := now
	for d.Weekday() != time.Saturday {
		d = d.AddDate(0, 0, -1)
	}
	return time.Date(d.Year(), d.Month(), d.Day(), 12, 0, 0, 0, d.Location())
}

// TestFlowClosedMarketLive R3 休市态实测（A 场景判红、B 场景留档）。
// English: R3 closed-market probe — scenario A asserts, scenario B records.
func TestFlowClosedMarketLive(t *testing.T) {
	if os.Getenv("UAT_LIVE") != "1" {
		t.Skip("R3 休市态实测需真实上游：UAT_LIVE=1 go test ./internal/engine -run TestFlowClosedMarketLive -v")
	}
	realNow := time.Now()
	dc := data.NewDataCoordinator(data.NewMarketAPI(), nil)
	e := &Engine{coordinator: dc}

	// ── A 场景：真休市（时钟钉到最近周六）──
	sat := lastSaturday(realNow)
	satDay := data.TradingDayDate(sat)
	t.Logf("A 真休市：假时钟=%s（%s） TradingDayDate 回退后=%s",
		sat.Format("2006-01-02 15:04"), sat.Weekday(), satDay)
	var aHit5D, aStale5D, aAbstainToday, aHistFail, aTotal int
	for _, code := range liveProbeCodes() {
		q, err := dc.GetQuote(code)
		if err != nil {
			t.Logf("  %s 报价失败（网络/源问题，跳过该票）：%v", code, err)
			continue
		}
		logRawHistory(t, dc, code)
		today, fiveD, whyT, whyF := e.flowRatiosResolve(code, q, e.flowTrackerRef(), satDay, sat)
		aTotal++
		// 把分母的两条来源一起打出来：no_floatcap 到底是"没股本"还是"没成交量/换手率"，
		// 只看原因串分不清——而这两条成因的处置完全不同（补字段 vs 等盘中）。
		t.Logf("  %s 报价=%.2f HasFlow=%v FloatShares=%.0f Volume=%.0f Turnover=%.3f | 当日=%+.3f%%(原因=%q) | 5日=%+.3f%%(原因=%q)",
			code, q.Price, q.HasFlow, q.FloatShares, q.Volume, q.Turnover, today, whyT, fiveD, whyF)
		if whyF == "" {
			aHit5D++
		}
		switch whyF {
		case "hist_stale":
			aStale5D++
		case "hist_fail", "no_source":
			aHistFail++
		}
		if whyT != "" {
			aAbstainToday++
		}
	}
	if aTotal == 0 {
		t.Skip("三只票都没取到报价（上游不可达），本次实测无读数")
	}
	// 全部 hist_fail = 上游退化（实测实录：东财 push2his 凌晨会 EOF 并触发熔断）。
	// 这种轮次必须判 skip 而不是判绿——否则"上游全挂"会伪装成"R3 日期断言没问题"的假绿。
	if aHistFail == aTotal {
		t.Skipf("上游日线资金流全失败（%d/%d），本次无有效读数：这是 R9 告警要抓的形态，不是 R3 的结论", aHistFail, aTotal)
	}
	t.Logf("A 汇总：%d 只票里 5 日维命中 %d、因日期陈旧弃权 %d、上游取数失败 %d、当日维弃权 %d", aTotal, aHit5D, aStale5D, aHistFail, aAbstainToday)
	// R3 的休市态承诺拆成两条可判的断言——分开判才不会把"分母缺"记成"日期断言误杀"：
	//   ① 日期断言不得误杀：末行 = 回退后的交易日 ⇒ 5 日维的弃权原因里**不许出现 hist_stale**；
	//   ② 当日维必须弃权（休市没有当日净流入快照）。
	// 5 日维是否最终命中取决于流通市值能否算出来（轻量报价路径下常缺），那是分母问题、不是 R3 问题，
	// 故只记录不判红——判红会让人误以为日期断言坏了，把真正的分母缺口盖掉。
	if aStale5D > 0 {
		t.Errorf("休市态 5 日维被日期断言误杀 %d/%d（末行应等于回退后的交易日 %s）", aStale5D, aTotal, satDay)
	}
	if aAbstainToday != aTotal {
		t.Errorf("休市态当日维应全部弃权（休市无当日净流入快照），实测 %d/%d 弃权", aAbstainToday, aTotal)
	}

	// ── B 场景：交易日盘前（时钟=现在）──
	dayNow := data.TradingDayDate(realNow)
	t.Logf("B 当前时态：时钟=%s TradingDayDate=%s（日历日=%s）",
		realNow.Format("2006-01-02 15:04"), dayNow, realNow.Format("20060102"))
	for _, code := range liveProbeCodes() {
		q, err := dc.GetQuote(code)
		if err != nil {
			continue
		}
		logRawHistory(t, dc, code)
		today, fiveD, whyT, whyF := e.flowRatiosResolve(code, q, e.flowTrackerRef(), dayNow, realNow)
		t.Logf("  %s | 当日=%+.3f%%(原因=%q) | 5日=%+.3f%%(原因=%q)", code, today, whyT, fiveD, whyF)
	}
}

// logRawHistory 直接读一次原始日线资金流并打出"行数 + 末行日期"。
//
// 为什么必须单独读：`hist_stale` 在"末行不是本交易日"和"序列是空"两种情形下是同一个原因串
// （flowHistFresh 对空序列也返回 false），只看原因串会把"上游没给数"误读成"上游给了旧数"。
// 这条读数把两者分开——行数 0 = 上游空回包，行数>0 且末行≠目标日 = 真的陈旧。
// English: reads the raw history so "empty response" and "stale rows" are never conflated
// (both surface as the same hist_stale reason string).
func logRawHistory(t *testing.T, dc *data.DataCoordinator, code string) {
	days, err := dc.GetStockMoneyFlowHistory(code, flowHistDays)
	if err != nil {
		t.Logf("  %s 原始历史：取数失败 err=%v", code, err)
		return
	}
	last := "-"
	if len(days) > 0 {
		last = days[len(days)-1].Date
	}
	t.Logf("  %s 原始历史：行数=%d 末行=%s", code, len(days), last)
}
