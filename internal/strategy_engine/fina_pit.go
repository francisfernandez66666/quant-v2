// 本文件把财务数据的"可见性裁决"收成一处函数（owner 裁决 2026-09-26 两连）：
//   - 「实盘财务按公告日对齐：是」（§B7-PIT）→ LatestVisibleFina 只认 ann_date≤判定日的最新期；
//   - 因子回放要不要喂财务数据：要（§B2）→ 回放侧与实盘侧必须吃**同一份**裁决逻辑。
//
// 为什么放在 strategy_engine：它的输入是 store.FinaRow、输出是 FinancialData——两端本来就都
// 经过这个类型；此前 §B7 的循环写在 cmd/quant 的 finaCache.Lookup 体内、§M-7 的新鲜度谓词写死
// "今天"，回放若要同样语义只能抄一份——而"抄一份再各改各的"正是本仓第三种口径事故（§B7 本身）
// 的成因，这里从源头上不给第二次机会。
// English: single home for financial point-in-time visibility (LatestVisibleFina: newest report with
// ann_date <= the deciding day) and §M-7 freshness (ReportStale), shared verbatim by the live scorer
// and the §B2 replay financial input so the two sides can never drift apart.
package strategy_engine

import (
	"strings"
	"time"

	"quant-trading-v2/internal/store"
)

// FinaStaleMaxDays §M-7：财务报告期最大可容忍滞后（日历天）。
// A 股披露规则下最长寿的合法空窗 ≈140 天（1231 年报 → 次年 430 披露截止），240 天留足保护带：
// 只有数据源真断更才会触发，不误伤合法迟披露的个股。
// English: max tolerated report lag in calendar days; 240 sits well above the ~140-day legitimate
// disclosure-gap ceiling, so only a stalled feed trips it.
const FinaStaleMaxDays = 240

// LatestVisibleFina 从按 end_date 升序的财务快照里取"判定日已可见"（ann_date≤asofDay）的最新一期。
// 返回 (选中值, 因披露日晚于判定日而被跳过的期数)：
//   - 披露日缺失/长度非法（≠8 位）按「不可知」照常采用该期——§N-5 姿势：不把数据没支撑的判定做过头；
//   - skipped>0 且 fina==nil 意味着整只票的财报都还没到可见时点（研究库存在提前入库/披露日错写），
//     调用方据此留痕，本函数不自发日志（实盘走 opslog，回放只出门禁读数，两侧口径各自表达）。
//
// YYYYMMDD 等宽日期串字典序即时间序，无需解析。
// English: picks the newest report already visible on the deciding day (ann_date <= asofDay) from
// end_date-ascending rows; missing/invalid ann_date passes as "unknown" (§N-5). Returns the picked
// row plus the number of future-dated rows skipped (caller decides how to alert).
func LatestVisibleFina(rows []store.FinaRow, asofDay string) (*FinancialData, int) {
	skipped := 0
	for i := len(rows) - 1; i >= 0; i-- {
		last := rows[i]
		if d := strings.TrimSpace(last.AnnDate); len(d) == 8 && d > asofDay {
			skipped++
			continue
		}
		return &FinancialData{
			Roe:          last.ROE,
			YoyNetProfit: last.YoyNetProfit,
			NetMargin:    last.NetMargin,
			GrossMargin:  last.GrossMargin,
			DebtToAssets: last.DebtToAssets,
			Eps:          last.EPS,
			YoyOR:        last.YoyOR,
			// §N-5：报告期/披露日一并带出，下游新鲜度判定据此判断这行财务有多旧。
			EndDate: last.EndDate,
			AnnDate: last.AnnDate,
		}, skipped
	}
	return nil, skipped
}

// ReportStale 判定一条财务数据的报告期是否过旧（§M-7 停用闸，true=停用）。
// asof 是**判定时刻**而非恒等于"今天"：实盘传 time.Now()、§B2 回放传判定日的收盘时刻——
// 闸的语义是"在这个时点上还能不能看到够新的财报"，两侧共用一个谓词才不会造出两套停用口径。
// ann_date（披露日，PIT 可见边界）优先，缺失退回 end_date；两者皆缺/日期非法按「不可知」放行。
// English: §M-7 staleness gate evaluated at the deciding moment (live=now, replay=the judgment day);
// ann_date preferred with end_date fallback; absent/undecodable dates pass as "unknown".
func ReportStale(f *FinancialData, asof time.Time) (bool, string) {
	if f == nil {
		return false, ""
	}
	d := f.AnnDate
	if len(d) != 8 {
		d = f.EndDate
	}
	if len(d) != 8 {
		return false, ""
	}
	t, err := time.Parse("20060102", d)
	if err != nil {
		return false, ""
	}
	if asof.Sub(t) > FinaStaleMaxDays*24*time.Hour {
		return true, d
	}
	return false, d
}
