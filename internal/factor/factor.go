// Package factor 因子库（B2：7 大类因子）。
//
// 7 大类（以 A 股通用分类为骨干，用户可随回测迭代调整）：
//
//	估值   Valuation     EP/BP/SP/CFP/DP（市盈率/市净率/市销率/市现率倒数 + 股息率）
//	成长   Growth        净利同比（yoy_net_profit）与 SUE 降级版（单季净利同比）
//	质量   Quality       ROE/毛利率/净利率/资产负债率
//	规模   Size          LnMktCap（原始价 × 股本，季频近似）
//	波动率 Volatility    20 日收益波动率、20 日平均振幅
//	动量   Momentum      5/10/20/60 日收益（反转由 B3 层按 IC 符号决定方向）
//	流动性 Liquidity     20 日平均换手率、对数成交额、Amihud 非流动性
//
// 约定：
//   - 输入 StockSeries 为单只股票按日期对齐的序列；价格类因子用 hfq（复权）价，
//     规模因子用原始价（复权因子对单股单日为常数，会扭曲市值横截面排序）。
//   - 财务字段由调用方预先点对时对齐（取 ann_date ≤ 当日 的最新报告值），无则 NaN。
//   - 因子值按日对齐，预热期/缺失为 NaN；因子值含当日收盘，B3 用其预测未来 h 日收益。
//   - 每因子公式单测；B3（cmd/research factor）做横截面 IC/IR/分层。
//
// （English: Package factor implements the 7-category factor library. Inputs are per-stock
// date-aligned series; price-ratio factors use hfq prices, size uses raw prices; financial
// fields are expected point-in-time aligned by the caller; NaN for warm-up/missing.）
package factor

import (
	"math"
	"sort"
)

// Category 因子大类。
type Category int

// 7 大类因子类别。
const (
	CatValue      Category = iota // 估值
	CatGrowth                     // 成长
	CatQuality                    // 质量
	CatSize                       // 规模
	CatVolatility                 // 波动率
	CatMomentum                   // 动量/反转
	CatLiquidity                  // 流动性
)

// CategoryName 返回类别中文名。
func (c Category) CategoryName() string {
	switch c {
	case CatValue:
		return "估值"
	case CatGrowth:
		return "成长"
	case CatQuality:
		return "质量"
	case CatSize:
		return "规模"
	case CatVolatility:
		return "波动率"
	case CatMomentum:
		return "动量"
	case CatLiquidity:
		return "流动性"
	}
	return "未知"
}

// StockSeries 单只股票按日期对齐的研究输入（hfq 主序列 + 估值/换手 + 点对时财务）。
// （StockSeries is one stock's date-aligned research input.）
type StockSeries struct {
	Dates      []string
	CloseHfq   []float64 // hfq 收盘（价格比率因子）
	CloseRaw   []float64 // 原始收盘（规模等需真实价）
	Open       []float64 // hfq 开盘
	High       []float64 // hfq 最高
	Low        []float64 // hfq 最低
	Vol        []float64 // 成交量（手）
	Amount     []float64 // 成交额（元）
	Turnover   []float64 // 换手率（%）
	PeTTM      []float64 // 市盈率 TTM
	Pb         []float64 // 市净率
	PsTTM      []float64 // 市销率 TTM
	PcfTTM     []float64 // 市现率 TTM
	DvTTM      []float64 // 股息率 TTM（%）
	TotalShare []float64 // 股本（股，季频近似）
	IsST       []float64 // 是否 ST（1=是；回测需剔除）
	// 财务（点对时：调用方取 ann_date ≤ 当日 的最新报告值，无则 NaN）
	Roe                []float64 // 净资产收益率
	GrossMargin        []float64 // 毛利率
	NetMargin          []float64 // 净利率
	DebtToAssets       []float64 // 资产负债率
	YoyNetProfit       []float64 // 净利同比
	SingleQuarterNIYoy []float64 // 单季净利同比（SUE 降级版）
	Eps                []float64 // 每股收益
}

// Len 返回序列长度。
func (s *StockSeries) Len() int {
	if s == nil {
		return 0
	}
	return len(s.Dates)
}

// Def 单个因子的定义与计算函数。
// （Def is one factor's metadata and compute function.）
type Def struct {
	ID      string                       // 英文标识（registry 唯一）
	Name    string                       // 中文名
	Cat     Category                     // 因子所属大类（见 Category 常量）
	Desc    string                       // 因子中文描述
	Compute func(*StockSeries) []float64 // 因子计算函数（输出与序列等长，缺失/预热期为 NaN）
}

// 因子注册表（ID → Def），按 ID 排序提供全量列表。
var registry = make(map[string]Def)

// Register 注册一个因子（重复 ID 以最后一次为准）。
func Register(d Def) {
	if d.ID == "" || d.Compute == nil {
		panic("factor: Register 需要非空 ID 与 Compute")
	}
	registry[d.ID] = d
}

// Get 按 ID 取因子定义；不存在返回 (false)。
func Get(id string) (Def, bool) {
	d, ok := registry[id]
	return d, ok
}

// All 返回全部因子定义（按 ID 排序）。
func All() []Def {
	ids := make([]string, 0, len(registry))
	for id := range registry {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]Def, 0, len(ids))
	for _, id := range ids {
		out = append(out, registry[id])
	}
	return out
}

// ByCategory 返回某大类的全部因子（按 ID 排序）。
func ByCategory(c Category) []Def {
	all := All()
	out := make([]Def, 0, len(all))
	for _, d := range all {
		if d.Cat == c {
			out = append(out, d)
		}
	}
	return out
}

// N 返回注册因子总数。
func N() int { return len(registry) }

// fieldOrNaN 把点对时财务字段复制为与序列等长的输出；字段缺失/长度不足时置 NaN。
// （fieldOrNaN copies a point-in-time field into an equal-length output, NaN when missing.）
func fieldOrNaN(v []float64, n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = math.NaN()
	}
	if len(v) >= n {
		copy(out, v)
	}
	return out
}
