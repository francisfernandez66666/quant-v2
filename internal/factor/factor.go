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

// 8 大类因子类别（§Phase2 新增情绪类）。
const (
	CatValue      Category = iota // 估值
	CatGrowth                     // 成长
	CatQuality                    // 质量
	CatSize                       // 规模
	CatVolatility                 // 波动率
	CatMomentum                   // 动量/反转
	CatLiquidity                  // 流动性
	CatSentiment                  // 情绪（涨停/炸板/连板高度市场状态）
	// CatLimit §ENH-A 涨停微结构（单票连板/封单/炸板/首封时间——同花顺三池按日对齐）。
	CatLimit
	// CatMarket §REPLAY-INPUTS 2026-11-28 市场级标量（基准指数涨跌幅/个股资金流双分位/
	// 板块净流入横截面分位/龙头代理分位）。与 CatSentiment 分开是因为语义不同：
	// 情绪列是"市场温度"，本类是"资金与相对强弱"，且它们走**绝对带打分**而不是时序分位
	// （见 market_scalar.go 的 ScalarScore）。
	CatMarket
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
	case CatSentiment:
		return "情绪"
	case CatLimit:
		return "涨停微结构"
	case CatMarket:
		return "市场级标量"
	}
	return "未知"
}

// StockSeries 单只股票按日期对齐的研究输入（hfq 主序列 + 估值/换手 + 点对时财务）。
// （StockSeries is one stock's date-aligned research input.）
type StockSeries struct {
	Dates      []string  // 交易日序列
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

	// §NEWS-GATE 2026-11-12（R7）：D1 事件带符号分（−40~+40），单日常量列——实盘由
	// StockMarketData.D1 经 seriesFromKLines 装载；研究侧缺数据整列 NaN（因子层过滤）。
	// 负分=明确利空：因子层弃权（不参与复合分），拦截职责在信号层 news gate。
	D1 []float64 // 当日 D1 事件分（带符号；缺失 NaN）

	// 市场情绪（单日全体一致，由 Store.EmotionStatsRange 预装载；缺失为 NaN）。
	// §Phase2 情绪因子入池：同截面所有股票当日情绪值相同，属于"市场状态因子"，
	// 用于 B3 分层/IC 时须与其他风格因子配合，或作情绪相位分组回测输入。
	EmoLimitUp   []float64 // 当日涨停家数
	EmoBreakCnt  []float64 // 当日炸板家数
	EmoMaxBoard  []float64 // 当日最高连板
	EmoBlastRate []float64 // 当日炸板率（%）

	// §ENH-A 单票涨停微结构（ths_limit_up_daily / ths_break_pool_daily 按日对齐；
	// 由 research.Assemble 逐股索引 seek 装载。非事件日：连板数/炸板次数/封单比=0，
	// 首封分钟=NaN；数据收录范围外整列 NaN 由因子层过滤）。
	LimitBoard   []float64 // 当日连板数（非涨停日 0）
	SealAmtRatio []float64 // 峰值封单额/流通市值（非涨停日 0；流通市值缺失 NaN）
	BreakCnt     []float64 // 当日开板（炸板）次数（炸板池 open_times；无记录 0）
	FirstSealMin []float64 // 首封时间（当日 0 点起分钟数，如 09:35=575；非涨停日 NaN）

	// §REPLAY-INPUTS 2026-11-28（档 2/档 3）：市场级标量列。**缺失整列 NaN，绝不写 0**——
	// 0 在这些量纲里都是"一条方向可能相反的真读数"（分位 0＝最弱、涨跌幅 0＝平盘、净流入 0＝中性），
	// 而因子层的弃权语义依赖 NaN：scoreRule 读到 NaN 就 continue，读到 0 会参与打分。
	// 命名与载体 params.factor_defs 的 input 逐字对齐（方案 §2.2/§4）：
	// 同名列必须同义，不同义的代理一律另起名字（见 SectorFlowQuantile / SectorGainRank 注释）。
	IndexChange        []float64 // 基准指数当日涨跌幅（%），与载体 index_change_pct 同口径
	FlowToday          []float64 // 主力净流入 **占成交额**（%）＝上游 f57，与大盘腿/板块腿同一把尺
	Flow5d             []float64 // 近 5 个**连续**交易日累计净流入 / 累计成交额 ×100（%）；不足或断日 NaN
	FlowCapToday       []float64 // 主力净流入 **占流通市值**（%）＝原公式，另开一列避免与上面混量纲
	FlowCap5d          []float64 // 近 5 日累计净流入 / 流通市值 ×100（%）
	SectorFlowQuantile []float64 // 所属板块当日净流入率的**全市场板块横截面分位**（0..100）
	// ↑ 这是**代理**，与载体的 ConceptHeat（利好池 LLM 热度分）**不是同一个量**，
	//   所以刻意不叫 ConceptHeat：同名不同义会让回测结论被当成实盘行为（方案 §2.2 第 1 条）。
	SectorGainRank []float64 // 同板块内当日涨幅分位（0..1），龙头**代理**
	// ↑ 载体的 is_leader 是布尔（是不是板块前二），这里是连续分位——同样刻意不同名。
	//   布尔走的是"门槛 band + bool 特例"，分位走 linear band，两者共用一个 ID 必然算错。
}

// Len 返回序列长度。
func (s *StockSeries) Len() int {
	if s == nil {
		return 0
	}
	return len(s.Dates)
}

// floatColumns 列出全部数值列的可寻址指针。
//
// 为什么要有这张清单而不是在每个使用点手写：StockSeries 有 30+ 列，任何"逐列处理"的
// 逻辑（截断视图、对齐校验、覆盖率统计）只要有一处漏列，表现就是那一列**静默不参与**——
// 本仓的 `_series_on_bars` 事故就是这个形状（六条估值腿永远 NaN，读数上一切正常）。
// 有了单点清单，新增一列只需要在这里加一行，所有逐列逻辑自动覆盖。
func (s *StockSeries) floatColumns() []*[]float64 {
	return []*[]float64{
		&s.CloseHfq, &s.CloseRaw, &s.Open, &s.High, &s.Low, &s.Vol, &s.Amount,
		&s.Turnover, &s.PeTTM, &s.Pb, &s.PsTTM, &s.PcfTTM, &s.DvTTM,
		&s.TotalShare, &s.IsST, &s.Eps, &s.D1,
		&s.Roe, &s.GrossMargin, &s.NetMargin, &s.DebtToAssets,
		&s.YoyNetProfit, &s.SingleQuarterNIYoy,
		&s.EmoLimitUp, &s.EmoBreakCnt, &s.EmoMaxBoard, &s.EmoBlastRate,
		&s.LimitBoard, &s.SealAmtRatio, &s.BreakCnt, &s.FirstSealMin,
		&s.IndexChange, &s.FlowToday, &s.Flow5d, &s.FlowCapToday, &s.FlowCap5d,
		&s.SectorFlowQuantile, &s.SectorGainRank,
	}
}

// UpTo 返回只看前 i+1 个交易日（[0, i]）的**视图**：结构体值拷贝 + 各列切片头截断，
// 不复制任何底层数组，所以一次调用的代价是 O(列数) 而不是 O(列数×长度)。
//
// 为什么是视图而不是"把 asofIdx 一路传进 scoreRule/percentile"（方案 §1.2 第 1 条）：
// scoreRule 同时服务实盘 Evaluate()，改它的签名要么动钱路径、要么在每个下游 helper 再传一层。
// 视图让"只看过去"这件事在**入口**完成一次，下游一行不用改。
//
// i 越界按边界钳制（不 panic）；列长度不足 n 时保持原样——那说明该列本来没装，
// 让因子层按 NaN 弃权，绝不在视图里凭空补出一段零（补零＝"有读数"，是假读数）。
//
// ★ 视图与本体**共享底层数组**：调用方写视图的任何一格等于写缓存本体。
// 纪律是"视图只读"，任何需要改值的场景必须显式克隆（见 StockSeries.CloneColumn）。
//
// English: a zero-copy as-of view over the first i+1 sessions. The view shares the backing
// arrays with the cache, so it must be treated as read-only.
func (s *StockSeries) UpTo(i int) *StockSeries {
	if s == nil {
		return nil
	}
	n := i + 1
	if n < 0 {
		n = 0
	}
	if n > len(s.Dates) {
		n = len(s.Dates)
	}
	out := *s
	out.Dates = s.Dates[:n]
	for _, col := range out.floatColumns() {
		if col == nil || *col == nil {
			continue
		}
		if len(*col) >= n {
			*col = (*col)[:n]
		}
	}
	return &out
}

// CloneColumn 把某一列**深拷贝**出来，供确实需要改写单日读数的调用方使用
// （例：只有当日 D1 分而没有整列时）。返回的切片与本体不共享内存，改它不影响缓存。
// 列不存在（未装配）返回 nil，调用方按"该因子弃权"处理，不许自己造长度。
func (s *StockSeries) CloneColumn(name string) []float64 {
	if s == nil {
		return nil
	}
	var src []float64
	switch name {
	case "D1":
		src = s.D1
	case "CloseHfq":
		src = s.CloseHfq
	case "CloseRaw":
		src = s.CloseRaw
	case "Amount":
		src = s.Amount
	case "Turnover":
		src = s.Turnover
	default:
		return nil
	}
	if len(src) == 0 {
		return nil
	}
	out := make([]float64, len(src))
	copy(out, src)
	return out
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
