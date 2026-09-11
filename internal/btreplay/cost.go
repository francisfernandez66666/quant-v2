// cost.go — 回测交易成本模型（§GAP4.1/4.2 回测真实性 + 回测自动增强模块 A/B）：
// 费率与模拟盘 §R11 默认值同源（paper.DefaultConfig）：佣金万2.5 双边、印花税 0.05%
// 卖出单边。增强关闭时滑点固定 5bp 单边（旧行为，逐字节一致）；增强启用时基准值
// 由模拟盘实测成交滑点自动校准（paper_trades 中位数扣纸面模型滑点，A.3 回退链），
// 并按个股流动性（信号日滑窗日均成交额）与单笔名义额动态罚档、区分买卖方向。
// 另含板级判定：开盘一字板不可成交、涨停盘中打开可成交加滑点、跌停封死不可卖。
// 按名义额比例计费，忽略最低佣金 5 元——回测为逐笔收益率口径、无固定仓位规模，
// 万 2.5 费率下 5 元下限仅对不足 2 万元的小额单有意义。
package btreplay

import (
	"math"
	"sort"

	"quant-trading-v2/internal/config"
	data "quant-trading-v2/internal/data"
	"quant-trading-v2/internal/paper"
	"quant-trading-v2/internal/store"
)

// 回测交易成本费率常量（与模拟盘同源）。
const (
	costSlippageBps    = 5.0     // 单边滑点（bp）——增强关闭时的旧口径
	costCommissionRate = 0.00025 // 佣金万2.5（双边）
	costStampTaxRate   = 0.0005  // 印花税（卖出单边）

	limitEps = 0.001 // 板价判定容差（元，吸收复权价缩放舍入误差）
)

// costRoundTripPnl 净额口径收益率(%)：买价上浮滑点、卖价下浮滑点，
// 扣双边佣金 + 卖出印花税。raw 入参为未加滑点的原始价格序列取值。
// Deprecated: 增强关闭路径保留为兼容包装（委托 Ex(5,5)），新调用一律走 Ex 版。
// English: legacy fixed-5bp wrapper — delegates to the Ex variant for byte-compat.
func costRoundTripPnl(rawEntry, rawExit float64) float64 {
	return costRoundTripPnlEx(rawEntry, rawExit, costSlippageBps, costSlippageBps)
}

// costRoundTripPnlEx 动态滑点净额口径：买卖滑点可不同（非对称增强）。
// buy=sell=5bp 时与旧 costRoundTripPnl 逐字节一致（Ex 是旧函数的代数恒等变形）。
// English: net round-trip return (%) with per-side slippage; identical to the legacy
// fixed-5bp formula when both sides are 5.
func costRoundTripPnlEx(rawEntry, rawExit, buySlip, sellSlip float64) float64 {
	if rawEntry <= 0 || rawExit <= 0 {
		return 0
	}
	buy := rawEntry * (1 + buySlip/10000)
	sell := rawExit * (1 - sellSlip/10000)
	feeFrac := 2*costCommissionRate + costStampTaxRate
	return (sell-buy)/buy*100 - feeFrac*100
}

// costOpenAtLimitUp 开盘即封板不可成交判定（§GAP4.2）：开盘价 ≥ 前收×(1+板块涨停幅) − 容差。
// 一字板/秒板买单现实中几乎排队无望；容差 0.001 吸收复权价缩放后的舍入误差。
// 局限：bars 不带名称，ST 档（5%）不在此判定，按代码板块幅度近似。
// English: unfillable-at-open check — an open already at the board limit cannot realistically be filled.
func costOpenAtLimitUp(code string, prevClose, open float64) bool {
	if prevClose <= 0 || open <= 0 {
		return false
	}
	limit := prevClose * (1 + paper.LimitUpPct(code, "")/100)
	return open >= limit-limitEps
}

// costLimitUpOpenable 涨停开盘但盘中打开过（可成交、追入成本高）：开盘 ≥ 涨停价 且 最低 < 涨停价。
// 比例型判定在后复权价下复权因子抵消（B.5-2），容差同封板判定。
// English: opened at the limit but broke open intraday — fillable with extra slippage.
func costLimitUpOpenable(code string, prevClose, open, low float64) bool {
	if prevClose <= 0 || open <= 0 {
		return false
	}
	limit := prevClose * (1 + paper.LimitUpPct(code, "")/100)
	return open >= limit-limitEps && low < limit-limitEps
}

// costLimitDownSealedDay 单日跌停封死判定（不可卖）：最低 ≤ 跌停价+ε 且 收盘 ≤ 跌停价+ε。
// 日K无逐笔数据，不区分盘中瞬时开板（B.5-3 口径）；跌停与涨停同幅对称（B.5-1）。
// English: day-level limit-down sealed check — cannot sell while the close sits on the limit.
func costLimitDownSealedDay(code string, prevClose, low, close float64) bool {
	if prevClose <= 0 || low <= 0 || close <= 0 {
		return false
	}
	limit := prevClose * (1 - paper.LimitUpPct(code, "")/100)
	return low <= limit+limitEps && close <= limit+limitEps
}

// fillRate 部分成交比例：订单占日均成交额比例越高、流动性越差，成交比例越低
// （元/元 无量纲，复权因子不涉及，Risk-2 不影响）。返回不低于 floor（FillRateMin）。
// 口径注意：pnl × fillRate 是"未成交部分留现金属零收益"的近似（B.5-5）。
// English: partial-fill ratio by order/turnover footprint; floored at FillRateMin.
func fillRate(orderValueYuan, avgAmtWan, floor float64) float64 {
	if avgAmtWan <= 0 {
		return floor // 无成交额样本（停牌缺行）按最差档处理
	}
	ratio := orderValueYuan / (avgAmtWan * 10000)
	r := 1.0
	switch {
	case ratio < 0.001:
		r = 1.0 // 小单全成
	case ratio < 0.005:
		r = 0.95
	case ratio < 0.01:
		r = 0.80
	case ratio < 0.02:
		r = 0.50
	default:
		r = 0.30 // 超大单
	}
	return math.Max(r, floor)
}

// volumePenalty 低流动性罚档：avgAmtWan（万元）≤ 档位上限时取该档 ExtraBps；
// ≤0（样本不足/零成交额）按最差档 +15bp（Risk-3 保守口径）。
// English: liquidity penalty tier by trailing-average turnover (10k CNY); unknown = worst tier.
func volumePenalty(avgAmtWan float64, tiers []config.VolumeTier) float64 {
	if avgAmtWan <= 0 {
		return 15
	}
	// tiers 按 max_volume_wan 升序（保存端已校验单调性）
	for _, t := range tiers {
		if avgAmtWan <= t.MaxVolumeWan {
			return t.ExtraBps
		}
	}
	return 0
}

// sizePenalty 大单冲击罚档：名义额/日均成交额 占比越高分档罚点；
// avgAmtWan≤0 时返回 0——volumePenalty 已按最差档处理，避免双重罚。
// English: market-impact penalty by order-value/turnover ratio (dimensionless).
func sizePenalty(orderValueYuan, avgAmtWan float64, tiers []config.SizeTier) float64 {
	if avgAmtWan <= 0 || orderValueYuan <= 0 {
		return 0
	}
	ratio := orderValueYuan / (avgAmtWan * 10000)
	// tiers 按 min_ratio 降序（占比越大越先命中重罚档）
	for _, t := range tiers {
		if ratio >= t.MinRatio {
			return t.ExtraBps
		}
	}
	return 0
}

// slippageBps 动态滑点核心：基准 + 流动性罚档 + 大单冲击罚档（+ 买入非对称加项）。
// baseBps 传入"校准合并后的最终基准"，由调用方在每轮扫参开始时算一次、逐笔复用。
// English: dynamic slippage = calibrated base + volume tier + size tier (+ buy-side extra).
func slippageBps(avgAmtWan, orderValueYuan, baseBps float64, cfg config.SlippageConfig) (buySlip, sellSlip float64) {
	vol := volumePenalty(avgAmtWan, cfg.VolumeTiers)
	sz := sizePenalty(orderValueYuan, avgAmtWan, cfg.SizeTiers)
	buySlip = baseBps + vol + sz
	sellSlip = baseBps + vol + sz
	if cfg.Asymmetric {
		buySlip += cfg.BuyExtraBps
	}
	return
}

// slipCtx 动态滑点上下文：每战法每轮构建一次（校准合并后的基准已折入 baseBps），
// 逐笔触发只查表算罚档、零额外扫参开销；nil = 增强关闭 = 旧固定 5bp 行为。
// English: per-strategy slippage context built once per sweep round; nil = legacy.
type slipCtx struct {
	baseBps    float64 // 校准合并后的最终基准（bp）
	slip       config.SlippageConfig
	orderValue float64 // 单笔名义额（元）
	liq        config.LiquidityConfig
	liqOn      bool // 流动性约束总开关（涨停打开/跌停封死/部分成交）
}

// entrySlip 入场日定档（信号日 i、入场日 i+1）：返回该笔的 买/卖滑点(bp)、成交比例与
// 可成交性。ok=false = 一字板封死不可成交——增强开启即顺带修复网格模式缺失的
// 一字板过滤（与回放 backtestStock 同口径，原有系统性乐观偏差不再进网格）；
// 涨停开盘但盘中打开（Liquidity 开启时）= 可成交，买滑点加"追入"罚分。
// English: resolve per-trade buy/sell slippage and fill ratio at entry; false = unfillable
// one-word limit-up board; an opened limit-up board pays the configured extra slippage.
func (sc *slipCtx) entrySlip(code string, kls []data.KLine, i int) (buy, sell, fill float64, ok bool) {
	avg := avgAmountWan(kls, i)
	prevClose := kls[i].Close
	bar := kls[i+1]
	openableExtra := 0.0
	if costOpenAtLimitUp(code, prevClose, bar.Open) {
		if sc.liqOn && costLimitUpOpenable(code, prevClose, bar.Open, bar.Low) {
			openableExtra = sc.liq.LimitUpOpenableExtraBps
		} else {
			return 0, 0, 0, false // 一字封死（或 Liquidity 关闭=旧严格口径）不可成交
		}
	}
	// 动态滑点定档：基准（校准合并后）+ 流动性罚档 + 大单冲击罚档（+非对称买差）
	buy, sell = slippageBps(avg, sc.orderValue, sc.baseBps, sc.slip)
	buy += openableExtra
	fill = 1.0
	if sc.liqOn && sc.liq.PartialFillEnabled {
		fill = fillRate(sc.orderValue, avg, sc.liq.FillRateMin)
	}
	return buy, sell, fill, true
}

// sellSealedExtra 跌停打开日的额外卖出滑点（bp）；0 = 跌停封死门控未启用。
// English: extra sell slippage on the day a sealed limit-down opens; 0 = gating off.
func (sc *slipCtx) sellSealedExtra() float64 {
	if sc != nil && sc.liqOn && sc.liq.LimitDownSealedEnabled {
		return sc.liq.LimitDownSealedExtraBps
	}
	return 0
}

// avgAmountWan 信号日（含）往前 20 根 K 线 Amount 滑窗均值（万元）。
// 用"滑窗根数"而非固定日期区间，规避停牌缺行导致的均值失真（Risk-3）；
// 有效样本（Amount>0）<5 根返回 0 → 调用方按最差档处理。
// English: trailing 20-bar average turnover (10k CNY) up to the signal day; <5 valid bars → 0.
func avgAmountWan(kls []data.KLine, i int) float64 {
	lo := i - 19
	if lo < 0 {
		lo = 0
	}
	var sum float64
	n := 0
	for j := lo; j <= i; j++ {
		if kls[j].Amount > 0 {
			sum += kls[j].Amount
			n++
		}
	}
	if n < 5 {
		return 0
	}
	return sum / float64(n) / 1e4
}

// clampF 数值钳位到 [lo, hi]。
func clampF(v, lo, hi float64) float64 {
	return math.Min(math.Max(v, lo), hi)
}

// calibAudit 校准合并：AutoCalibrate 且样本达标时用实测中位数推导基准与非对称买差
// （扣 paper 撮合自身模型滑点防双重计费，A.3 定稿公式），否则回退配置值；
// 返回最终 (baseBps, buyExtraBps) 与审计简报（source/buy_bps/sell_bps/n_buy/n_sell）。
// 回退链：实测校准 → 配置表值（FillDefaults 后已是新默认 3.0/1.0，即内置默认）。
// English: merge paper-median calibration into base slippage with clamped formulas and
// return an audit brief; fallback chain = measured → configured → built-in default.
func calibAudit(bt *config.BacktestConfig, calib *store.SlippageCalib) (float64, float64, map[string]any) {
	s := &bt.Slippage
	base, extra := s.BaseBps, s.BuyExtraBps
	source := "config"
	if s.AutoCalibrate && calib == nil {
		source = "config(no_sample)"
	}
	audit := map[string]any{"source": source}
	// 生效条件：开关开 + 买卖双向样本均 ≥ CalibMinSample（战法级不足回退全局在调用端做）
	if s.AutoCalibrate && calib != nil && calib.BuyN >= s.CalibMinSample && calib.SellN >= s.CalibMinSample {
		// 关键：扣掉 paper 撮合自身的模型滑点（成交价已被上浮 SlippageBps），防双重计费假校准
		base = clampF(math.Min(calib.BuyMedBps, calib.SellMedBps)-bt.PaperModelSlippageBps, 3, 15)
		if s.Asymmetric {
			extra = clampF(calib.BuyMedBps-calib.SellMedBps, 0, 5)
		}
		audit["source"] = "paper_median"
		audit["buy_bps"] = round2(calib.BuyMedBps)
		audit["sell_bps"] = round2(calib.SellMedBps)
		audit["n_buy"] = calib.BuyN
		audit["n_sell"] = calib.SellN
	}
	audit["base_bps"] = round2(base)
	audit["buy_extra_bps"] = round2(extra)
	return base, extra, audit
}

// fixAmountScale Risk-1 单位自校：tushare 装载的 daily 表 amount=千元（与契约"元"差 1000 倍）。
// 逐票用"当日均价 = Amount/(Vol×100)"（Vol=手，天然未复权量纲）判定：A 股均价合理带为
// [1,500] 元，中位数 <1 元即视为千元口径，Amount 整体 ×1000 归一。仅增强启用时调用
// （避免扰动旧路径数据），归一返回 true 并由调用端日志留痕。
// English: per-stock amount-unit self-check — median daily price below 1 CNY means the
// thousand-yuan caliber (tushare loader); scale Amount ×1000 in place and report.
func fixAmountScale(kls []data.KLine) bool {
	var ratios []float64
	for i := range kls {
		// 注意：toDataKLine 直传 store.Bar.Vol（手）到 KLine.Volume，回测链路里该字段实际是手
		if kls[i].Volume > 0 && kls[i].Amount > 0 {
			ratios = append(ratios, kls[i].Amount/(kls[i].Volume*100))
		}
	}
	if len(ratios) < 5 {
		return false
	}
	sort.Float64s(ratios)
	med := ratios[len(ratios)/2]
	// 均价 <1 元（仙股都不至于）→ 千元口径，归一到元
	if med > 0 && med < 1 {
		for i := range kls {
			kls[i].Amount *= 1000
		}
		return true
	}
	return false
}

// round2 两位小数（审计简报展示用，避免 JSON 里出现 4.09999999）。
func round2(v float64) float64 { return math.Round(v*100) / 100 }
