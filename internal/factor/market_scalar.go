// market_scalar.go — §REPLAY-INPUTS 2026-11-28（档 2）：市场级标量因子的带值打分。
//
// 为什么需要"标量带值"这第三条归一路径（前两条是 D1Event 绝对带、财务 finaScore，
// 其余价量走时序分位 percentile）：
//
//	这些量的语义是**绝对尺度**，不是"和自己历史比的位置"。
//	   指数涨 1.2% 就是 1.2%，把它折成"该指数过去 900 天里的分位"，
//	   在趋势市里会把每天都读成高分；板块净流入同理。
//	载体侧早就是这个形状（`rules_factor._scalar_score`：band + linear/门槛两态 + NaN 弃权），
//	Go 侧此前**一条都没有**（grep 五个 ID 零命中），所以引用它们的规则在回放里只会 decline。
//
// ★ band 数值不是这里发明的，是**逐字抄载体 params.scalar_bands**（方案 §0.5 探针 2）。
//   两侧是否相等由门禁 §259 ① 的 Python 腿逐键逐值比，不靠肉眼。
//   ★ 其中 FlowToday/Flow5d 两条在册值是在"载体一个键混两种量纲"的状态下标的
//   （探针 3），必须等首采实测分布重标并经 owner 签字（§10 裁决 1）——
//   所以这里带 `provisional` 标记导出，回放报告头会打 `band_calibrated=provisional`，
//   不假装它已经标定好了。
//
// ★ 与载体的两处刻意差异（都写在注释里，别当成漂移）：
//   ① Go 侧**不注册** ConceptHeat / IsLeader 两个 ID：载体那两个分别是"利好池 LLM 热度"
//      与"是否板块前二（布尔）"，Go 只有代理，同名不同义会让回测结论被当成实盘行为。
//      代理另起名字：SectorFlowQuantile、SectorGainRank，且**不进载体**（A2-4/A2-5 负锁）。
//   ② 方向一律由规则 Directions 决定，band 本身绝不翻转（与载体一致）。
//
// English: absolute-band scoring for market-level scalars, mirroring the carrier's
// _scalar_score; band values are copied verbatim from params.scalar_bands and two of them
// are provisional until the owner signs the recalibration.

package factor

import "math"

// ScalarBand 一档标量的绝对带。
type ScalarBand struct {
	Min    float64 // 带下界（含）
	Max    float64 // 带上界（不含，与载体 `low <= n < high` 同形状）
	Linear bool    // true＝按尺度线性归一并保留强度；false＝二值门槛（只问"在不在带内"）
}

// scalarBands 市场级标量的带值表。
//
// 每一项都注明**抄自载体哪一条**，因为这条表一旦与载体分家，
// 同一条规则在实盘与回放会得到不同的复合分（§跨侧契约：两侧各写一份迟早漂）。
var scalarBands = map[string]ScalarBand{
	// 载体 scalar_bands.IndexChange = {min:-2.0, max:1.5, mode:linear}
	"IndexChange": {Min: -2.0, Max: 1.5, Linear: true},
	// 载体 scalar_bands.FlowToday = {min:-0.5, max:1.2, mode:linear}
	// ★ provisional：载体这条是在"个股腿占流通市值 / 板块腿占成交额"两种量纲混用一个键
	//   的状态下标的（探针 3）。本批 Go 侧口径定为**占成交额**，尺度已变，
	//   必须等首采 p5/p95 重标并经 owner 签字（§10 裁决 1），两侧同批改。
	"FlowToday": {Min: -0.5, Max: 1.2, Linear: true},
	// 载体 scalar_bands.Flow5d = {min:-1.0, max:2.0, mode:linear}，同上 provisional
	"Flow5d": {Min: -1.0, Max: 2.0, Linear: true},
	// 载体 scalar_bands.ConceptHeat = {min:0, max:100, mode:linear}——
	// 这里是**板块净流入横截面分位**，尺度同为 0..100 但含义是代理，故另起 ID 并沿用同一尺度。
	"SectorFlowQuantile": {Min: 0, Max: 100, Linear: true},
	// 连续分位 0..1。载体的 IsLeader 是 `{0,1}` 且走 bool 特例（门槛态），
	// 这里**必须 linear**：否则 0<=1<1 为假，分位 1.0 会被判成 0 分且不报错。
	"SectorGainRank": {Min: 0, Max: 1, Linear: true},
	// FlowCapToday / FlowCap5d（占流通市值口径，原公式）刻意**不给带**：
	// 没有实测分布就没有尺度，无带 ⇒ ScalarScore 返回 NaN ⇒ 该因子腿弃权、引用它的规则 decline。
	// 这是 fail-closed，不是漏配（§10 裁决 1 签字后两侧同批补）。
}

// ProvisionalBandKeys 返回尚未标定（数值可疑但已照抄在册）的带键，供报告头如实标注。
// 读的人看到 `band_calibrated=provisional` 才知道这几档的分数尺度还没经实测分布校准。
func ProvisionalBandKeys() []string {
	return []string{"FlowToday", "Flow5d"}
}

// ScalarBandOf 取某 ID 的带值；不存在返回 (零值, false)。
// 供门禁/parity 腿与读面用——带值表必须是**可导出的**，否则两侧对齐只能靠肉眼。
func ScalarBandOf(id string) (ScalarBand, bool) {
	b, ok := scalarBands[id]
	return b, ok
}

// AllScalarBands 返回全部标量带（副本，调用方改不动内部表）。
func AllScalarBands() map[string]ScalarBand {
	out := make(map[string]ScalarBand, len(scalarBands))
	for k, v := range scalarBands {
		out[k] = v
	}
	return out
}

// IsScalarFactor 判定某因子 ID 是否走"绝对带打分"这条路。
//
// ★ 判据必须是**大类**，不能只看带值在不在。
//
//	实测踩到的形态：带值缺席时若判 false，该因子会掉进 `scoreRule` 最后的 percentile 分支，
//	于是"还没标定"静默变成"按时序分位打分"——`FlowCapToday` 无带仍得 0 分并通过门槛，
//	读数上一切正常。这恰好是本批方案反复点名的那类缺陷（机制在、方向是错的）。
//	所以路由按大类走：**CatMarket 一律进标量分支**，有带按带归一、无带返回 NaN 弃权。
//	"将来某个市场级因子想走时序分位"的正确做法是把它归到别的大类，而不是靠漏配带来实现。
func IsScalarFactor(id string) bool {
	if _, ok := scalarBands[id]; ok {
		return true
	}
	d, ok := Get(id)
	return ok && d.Cat == CatMarket
}

// ScalarScore 把一个绝对读数归一到 [0,1]。
//
// 返回 NaN 的三种情况（都代表"弃权"，不是"0 分"）：
//  1. 读数本身 NaN/Inf；
//  2. 该 ID 没有带（没标定就不许打分，与载体"无 band ⇒ 规则 decline"同取向）；
//  3. 带的上下界倒置（Min>=Max，配错了要立刻显形，而不是把整条腿压成常数）。
//
// 线性态保留**强度**：v=带中点得 0.5，不是"在带内=1"。二值态与载体的
// `low <= number < high` 逐字同形（右开），两边差一个端点就能让同一批票差一档分。
func ScalarScore(id string, v float64) float64 {
	b, ok := scalarBands[id]
	if !ok {
		return math.NaN()
	}
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return math.NaN()
	}
	if !(b.Max > b.Min) {
		return math.NaN()
	}
	if !b.Linear {
		if v >= b.Min && v < b.Max {
			return 1
		}
		return 0
	}
	return clamp01((v - b.Min) / (b.Max - b.Min))
}

// clamp01 把值夹到 [0,1]（NaN 原样透传，由调用方按弃权处理）。
func clamp01(v float64) float64 {
	if math.IsNaN(v) {
		return v
	}
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// columnOf 安全取一列：空序列返回 nil（因子层按"无输入"处理，而不是打分 0）。
func columnOf(s *StockSeries, sel func(*StockSeries) []float64) []float64 {
	if s == nil || len(s.Dates) == 0 {
		return nil
	}
	return sel(s)
}

// init 注册市场级标量因子（§REPLAY-INPUTS 档 2/档 3）。
//
// 这些 Def 只做"把列交出去"，归一在 scoreRule 的标量分支里做——
// 因为 Compute 的输出要与其他因子同形（等长序列），而"怎么变成 0..1"是打分口径的事。
func init() {
	Register(Def{
		ID:   "IndexChange",
		Name: "基准指数涨跌幅",
		Cat:  CatMarket,
		Desc: "当日基准指数涨跌幅 %（上证主、沪深300 次，与载体 index_change_pct 同口径同序）",
		Compute: func(s *StockSeries) []float64 {
			return columnOf(s, func(x *StockSeries) []float64 { return x.IndexChange })
		},
	})
	Register(Def{
		ID:   "FlowToday",
		Name: "主力净流入占成交额(当日)",
		Cat:  CatMarket,
		Desc: "当日主力净额/成交额×100（与大盘腿 market_flow_pct、板块腿同一把尺）；北交所无此档",
		Compute: func(s *StockSeries) []float64 {
			return columnOf(s, func(x *StockSeries) []float64 { return x.FlowToday })
		},
	})
	Register(Def{
		ID:   "Flow5d",
		Name: "主力净流入占成交额(5日)",
		Cat:  CatMarket,
		Desc: "近 5 个连续交易日累计净额/累计成交额×100；不足或断日 NaN（不许用较少行冒充 5 日档）",
		Compute: func(s *StockSeries) []float64 {
			return columnOf(s, func(x *StockSeries) []float64 { return x.Flow5d })
		},
	})
	Register(Def{
		ID:   "FlowCapToday",
		Name: "主力净流入占流通市值(当日)",
		Cat:  CatMarket,
		Desc: "原公式留存：净额/(流通股本×收盘)×100；带值待标定，未标定前该腿一律弃权",
		Compute: func(s *StockSeries) []float64 {
			return columnOf(s, func(x *StockSeries) []float64 { return x.FlowCapToday })
		},
	})
	Register(Def{
		ID:   "FlowCap5d",
		Name: "主力净流入占流通市值(5日)",
		Cat:  CatMarket,
		Desc: "近 5 日累计净额/(流通股本×收盘)×100；带值待标定，未标定前该腿一律弃权",
		Compute: func(s *StockSeries) []float64 {
			return columnOf(s, func(x *StockSeries) []float64 { return x.FlowCap5d })
		},
	})
	Register(Def{
		ID:   "SectorFlowQuantile",
		Name: "板块净流入横截面分位(代理)",
		Cat:  CatMarket,
		Desc: "所属板块当日净流入率在全市场板块中的分位 0..100；是 ConceptHeat 的**代理**，非同一读数",
		Compute: func(s *StockSeries) []float64 {
			return columnOf(s, func(x *StockSeries) []float64 { return x.SectorFlowQuantile })
		},
	})
	Register(Def{
		ID:   "SectorGainRank",
		Name: "同板块涨幅分位(龙头代理)",
		Cat:  CatMarket,
		Desc: "同板块内当日涨幅分位 0..1；是 is_leader(布尔)的**代理**，非同一读数",
		Compute: func(s *StockSeries) []float64 {
			return columnOf(s, func(x *StockSeries) []float64 { return x.SectorGainRank })
		},
	})
}
