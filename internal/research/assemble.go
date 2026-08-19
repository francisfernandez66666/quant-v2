// 从研究 SQLite 库装配单只股票的 StockSeries（B3/B4 的数据入口）。
// 财务字段做点对时对齐（取 ann_date ≤ 当日 的最新报告），避免未来函数。
// English: assembles a single stock's StockSeries from the research SQLite DB (the data entry for B3/B4).
// Financial fields are point-in-time aligned (the latest report with ann_date <= today) to avoid look-ahead.
package research

import (
	"fmt"
	"math"
	"sort"

	"quant-trading-v2/internal/factor"
	"quant-trading-v2/internal/store"
)

// Assemble 从 store 装配单只股票 [start,end] 区间的研究序列。
// 区间内无行情返回错误；行情存在但某类数据缺失时对应字段为 0/NaN（由因子层过滤）。
// English: Assemble builds one stock's research series over [start,end] from the store DB.
// It returns an error when there is no market data; when bars exist but some data type is missing,
// the corresponding fields are 0/NaN (filtered by the factor layer).
// （Assemble builds one stock's research series over [start,end] from the store DB.
// Financial fields are point-in-time aligned to each bar via ann_date.）
func Assemble(db *store.DB, code, start, end string) (*factor.StockSeries, error) {
	hfq, err := db.HfqBars(code, start, end)
	if err != nil {
		return nil, err
	}
	if len(hfq) == 0 {
		return nil, fmt.Errorf("%s 区间 %s-%s 无行情", code, start, end)
	}
	raw, err := db.RawBars(code, start, end)
	if err != nil {
		return nil, err
	}
	basic, err := db.DailyBasicRange(code, start, end)
	if err != nil {
		return nil, err
	}
	fina, err := db.FinaHistory(code)
	if err != nil {
		return nil, err
	}
	income, err := db.IncomeHistory(code)
	if err != nil {
		return nil, err
	}

	s := &factor.StockSeries{Dates: make([]string, len(hfq))}
	fillSlices(s, len(hfq))
	for i, b := range hfq {
		s.Dates[i] = b.Date
		s.Open[i], s.High[i], s.Low[i], s.CloseHfq[i] = b.Open, b.High, b.Low, b.Close
		s.Vol[i], s.Amount[i] = b.Vol, b.Amount
	}

	// 原始价（规模因子用）
	// English: raw price (used by the size factor).
	rawByDate := make(map[string]float64, len(raw))
	for _, b := range raw {
		rawByDate[b.Date] = b.Close
	}
	// 每日指标
	// English: daily metrics.
	basicByDate := make(map[string]store.DailyBasic, len(basic))
	for _, b := range basic {
		basicByDate[b.Date] = b
	}
	for i, d := range s.Dates {
		if c, ok := rawByDate[d]; ok {
			s.CloseRaw[i] = c
		}
		if b, ok := basicByDate[d]; ok {
			s.Turnover[i] = b.TurnoverRate
			s.PeTTM[i] = b.PETTM
			s.Pb[i] = b.PB
			s.PsTTM[i] = b.PSTTM
			s.PcfTTM[i] = b.PcfTTM
			s.DvTTM[i] = b.DVTTM
			s.TotalShare[i] = b.TotalShare
			s.IsST[i] = float64(b.IsST)
		}
	}

	// 财务点对时：按 (end_date, ann_date) 排序后对每个交易日取 ann_date ≤ 当日 的最新报告
	// English: point-in-time financials: after sorting by (end_date, ann_date), take for each trading day the latest report with ann_date <= that day.
	finaPIT := alignFina(fina, income, s.Dates)
	for i := range s.Dates {
		if f, ok := finaPIT[i]; ok {
			s.Roe[i] = f.roe
			s.GrossMargin[i] = f.grossMargin
			s.NetMargin[i] = f.netMargin
			s.DebtToAssets[i] = f.debtToAssets
			s.YoyNetProfit[i] = f.yoyNetProfit
			s.SingleQuarterNIYoy[i] = f.sue
		}
	}
	return s, nil
}

// finaPITValue 点对时后的财务值。
// English: finaPITValue is the financial values after point-in-time alignment.
type finaPITValue struct {
	roe, grossMargin, netMargin, debtToAssets, yoyNetProfit, sue float64
}

// alignFina 把财务快照（按 ann_date 排序）对齐到每个交易日的点对时值。
// English: alignFina aligns the financial snapshots (sorted by ann_date) to each trading day's point-in-time value.
func alignFina(fina []store.FinaRow, income []store.IncomeRow, dates []string) map[int]finaPITValue {
	// SUE 序列（按 end_date 对齐）与公告日（fina 提供）
	// English: SUE series (aligned by end_date) and announcement dates (provided by fina).
	sueByEnd := make(map[string]float64)
	for i, r := range income {
		sueByEnd[r.EndDate] = SingleQuarterNetProfitYoy(income)[i]
	}
	annByEnd := make(map[string]string)
	for _, f := range fina {
		annByEnd[f.EndDate] = f.AnnDate
	}

	type rec struct {
		ann string
		f   store.FinaRow
	}
	recs := make([]rec, 0, len(fina))
	for _, f := range fina {
		ann := f.AnnDate
		if ann == "" {
			ann = f.EndDate // 公告日缺失时保守按报告期末日（实际更晚，宁可晚不可早）
			// English: when the announcement date is missing, conservatively use the reporting-period end date (actually later; better late than early).
		}
		recs = append(recs, rec{ann: ann, f: f})
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].ann < recs[j].ann })

	out := make(map[int]finaPITValue)
	var best *rec // 跨日期携带：单调推进后保持"最近已披露"
	// English: carried across dates: keeps the "most recently disclosed" one as it advances monotonically.
	j := 0
	for i, d := range dates {
		for j < len(recs) && recs[j].ann <= d {
			best = &recs[j]
			j++
		}
		if best == nil {
			continue
		}
		out[i] = finaPITValue{
			roe:          best.f.ROE,
			grossMargin:  best.f.GrossMargin,
			netMargin:    best.f.NetMargin,
			debtToAssets: best.f.DebtToAssets,
			yoyNetProfit: best.f.YoyNetProfit,
			sue:          sueByEnd[best.f.EndDate],
		}
	}
	return out
}

// fillSlices 初始化 StockSeries 数值切片为 NaN（缺失语义），并补日期切片长度。
// English: fillSlices initializes the StockSeries numeric slices to NaN (missing semantics) and fills the date slice length.
func fillSlices(s *factor.StockSeries, n int) {
	s.Open = make([]float64, n)
	s.High = make([]float64, n)
	s.Low = make([]float64, n)
	s.CloseHfq = make([]float64, n)
	s.CloseRaw = make([]float64, n)
	s.Vol = make([]float64, n)
	s.Amount = make([]float64, n)
	s.Turnover = make([]float64, n)
	s.PeTTM = make([]float64, n)
	s.Pb = make([]float64, n)
	s.PsTTM = make([]float64, n)
	s.PcfTTM = make([]float64, n)
	s.DvTTM = make([]float64, n)
	s.TotalShare = make([]float64, n)
	s.IsST = make([]float64, n)
	s.Roe = make([]float64, n)
	s.GrossMargin = make([]float64, n)
	s.NetMargin = make([]float64, n)
	s.DebtToAssets = make([]float64, n)
	s.YoyNetProfit = make([]float64, n)
	s.SingleQuarterNIYoy = make([]float64, n)
	for i := 0; i < n; i++ {
		s.Open[i], s.High[i], s.Low[i], s.CloseHfq[i] = math.NaN(), math.NaN(), math.NaN(), math.NaN()
		s.CloseRaw[i] = math.NaN()
		s.Vol[i], s.Amount[i] = math.NaN(), math.NaN()
		s.Turnover[i] = math.NaN()
		s.PeTTM[i], s.Pb[i], s.PsTTM[i], s.PcfTTM[i], s.DvTTM[i] = math.NaN(), math.NaN(), math.NaN(), math.NaN(), math.NaN()
		s.TotalShare[i] = math.NaN()
		s.IsST[i] = math.NaN()
		s.Roe[i], s.GrossMargin[i], s.NetMargin[i], s.DebtToAssets[i] = math.NaN(), math.NaN(), math.NaN(), math.NaN()
		s.YoyNetProfit[i], s.SingleQuarterNIYoy[i] = math.NaN(), math.NaN()
	}
}
