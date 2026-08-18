// SUE 降级版：由利润表累计净利序列计算单季净利同比。
package research

import (
	"math"
	"strconv"

	"quant-trading-v2/internal/store"
)

// SingleQuarterNetProfitYoy 计算单季净利同比（SUE 降级版）。
//
// 输入 income 按 end_date 升序，含累计归母净利（n_income_attr_p，财年内逐季累计，
// Q1=Q1 单季、Q2=上半年累计…Q4=全年累计）。返回与 income 等长序列：
//
//	单季净利 sq = 当期累计 − 上一报告期累计（同财年；Q1 即当期累计）
//	同比 yoy = (sq − 上年同期单季) / |上年同期单季|
//
// 无法计算（无上年同期/上年同期为 0）时为 NaN。
// （SingleQuarterNetProfitYoy derives single-quarter net-profit YoY (the degraded SUE) from
// the cumulative net-profit income series; NaN when not computable.）
func SingleQuarterNetProfitYoy(income []store.IncomeRow) []float64 {
	out := make([]float64, len(income))
	for i := range out {
		out[i] = math.NaN()
	}
	if len(income) == 0 {
		return out
	}
	// 上年同期的单季净利（按 end_date 的 MMDD 索引，仅当恰好为上年时采用）
	prevY := make(map[string]prevQuarter)
	for i, r := range income {
		sq := singleQuarter(income, i)
		mmdd := r.EndDate[4:8]
		year := r.EndDate[0:4]
		if pv, ok := prevY[mmdd]; ok && pv.year == yearMinusOne(year) && pv.val != 0 {
			out[i] = (sq - pv.val) / math.Abs(pv.val)
		}
		prevY[mmdd] = prevQuarter{year: year, val: sq}
	}
	return out
}

type prevQuarter struct {
	year string
	val  float64
}

// yearMinusOne 返回上一年（YYYY）。
func yearMinusOne(year string) string {
	y, err := strconv.Atoi(year)
	if err != nil {
		return ""
	}
	return strconv.Itoa(y - 1)
}

// singleQuarter 计算第 i 个报告期的单季净利（累计差分，Q1 即累计）。
func singleQuarter(income []store.IncomeRow, i int) float64 {
	cum := income[i].NIncomeAttrP
	if i > 0 && income[i].EndDate[0:4] == income[i-1].EndDate[0:4] {
		return cum - income[i-1].NIncomeAttrP
	}
	return cum
}