// 流动性类因子：换手率、成交额、Amihud 非流动性。
package factor

import (
	"math"

	"quant-trading-v2/internal/indicator"
)

// sto20 20 日平均换手率（短期换手率）。
// （sto20 is the 20-day mean turnover rate.）
func sto20(s *StockSeries) []float64 {
	return indicator.SMA(s.Turnover, 20)
}

// stoa 对数 20 日平均成交额。
// （stoa is the log of the 20-day mean amount.）
func stoa(s *StockSeries) []float64 {
	amount := s.Amount
	ma := indicator.SMA(amount, 20)
	out := make([]float64, s.Len())
	for i := range out {
		out[i] = math.NaN()
		if ma[i] > 0 && !math.IsNaN(ma[i]) {
			out[i] = math.Log(ma[i])
		}
	}
	return out
}

// amihud20 20 日平均非流动性 = |当日收益| / 成交额（越高越不流动）。
// （amihud20 is the 20-day mean of |return|/amount; higher means less liquid.）
func amihud20(s *StockSeries) []float64 {
	out := make([]float64, s.Len())
	for i := range out {
		out[i] = math.NaN()
		if i >= 1 && s.Amount[i] > 0 && s.CloseHfq[i-1] > 0 {
			r := math.Abs(s.CloseHfq[i]/s.CloseHfq[i-1] - 1)
			out[i] = r / s.Amount[i]
		}
	}
	return indicator.SMA(out, 20)
}

func init() {
	Register(Def{ID: "STO20", Name: "20日平均换手率", Cat: CatLiquidity, Desc: "换手率（%）20日均值", Compute: sto20})
	Register(Def{ID: "STOA", Name: "对数20日均成交额", Cat: CatLiquidity, Desc: "ln(20日均成交额)", Compute: stoa})
	Register(Def{ID: "Amihud20", Name: "20日Amihud非流动性", Cat: CatLiquidity, Desc: "20日均 |r|/成交额，越高越不流动", Compute: amihud20})
}