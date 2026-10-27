// Package tcalib 是 §T-COST-CALIB **B2** 的离线阈值标定内核（2026-10-27）。
//
// 它只用研究库里的**分钟线**（只读、不接交易进程）回答四个问题：
//
//  1. min_spread_pct    —— 价差门槛：由成本**反推**（佣金+滑点+印花税）× 安全边际；
//  2. vwap_dev_pct      —— 回踩门槛：分钟收盘价对当日累计 VWAP 的偏离分位（P70）；
//  3. min_amplitude_pct —— 振幅门槛：能覆盖成本的交易日振幅 P60；
//  4. max_rounds        —— 同日第 2 次回转还有没有净价差（≤0 就不放开）。
//
// 三条不可动摇的口径（改任何一条，读数就变成另一种东西）：
//
//   - **确定性**：同输入两次跑必须逐字节一致。因此全程不读时钟、不遍历 map、
//     所有输出浮点统一 round6（末位抖动会让"两次一致"这条锁形同虚设）。
//   - **窗口不重叠**：标定窗 [T−N, T−1] 与验证窗 [T, T+M] 的交集必须为空。
//     splitWindowsAt 是唯一落点，重叠直接报错，不许"调用方自己注意"。
//   - **样本不足不替换**：任一分档样本数 < MinSample ⇒ 该档 Replaced=false 并写明原因。
//     宁可保持现值，也不许拿二十个样本去改一个资金参数。
//
// 与实盘的关系：本包只产出**建议值**，不写任何配置。owner 看报告 → 设置页签字 →
// 才落 sizing.t.calib（§4.5「不自动写」）。
//
// English: package tcalib is the offline threshold calibrator for §T-COST-CALIB B2.
// It is read-only, deterministic (no clock, no map iteration, round6 everywhere) and
// refuses to replace any tier whose sample count is below the floor.
package tcalib

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
)

// 出厂参数（对应 §T-COST-CALIB §4.1 / §4.2 / §4.4 的拍板值）。
const (
	DefaultMinDays   = 60  // 步骤 0：每票至少 60 个交易日的 5 分钟线（§1 拍板项 7）
	DefaultTiers     = 4   // 分档数（与回测 SlippageConfig.volume_tiers 同源口径）
	DefaultMinSample = 30  // 单档最小样本数（票×日）；不足即"证据不足"
	DefaultCalibDays = 60  // 标定窗长度（交易日）
	DefaultOOSDays   = 11  // 验证窗长度（T … T+10，闭区间 ⇒ 11 个交易日）
	DefaultAmountWin = 20  // 分档用的滑窗长度（交易日）
	DefaultSafetyMul = 1.3 // min_spread_pct 的安全边际
	DefaultCommBps   = 2.5 // 佣金万 2.5（与 btreplay costCommissionRate 同源，双边各一次）
	DefaultStampBps  = 5.0 // 印花税万 5（与 btreplay costStampTaxRate 同源，仅卖出）
	DefaultSlipBps   = 0.0 // 滑点：由 B1 实盘账本的中位数喂进来；0 = 未计入（报告会标注偏乐观）
)

// 分钟窗口边界（北京墙钟 "HH:MM"）。T 腿窗口刻意收在 14:30——尾盘配平与
// 收盘集合竞价不属于 T 的战场，把它们算进来会虚增"可捕获价差"。
const (
	vwapWindowFrom = "09:35" // VWAP 偏离统计起点（开盘 5 分钟价格未成形）
	vwapWindowTo   = "14:00" // VWAP 偏离统计终点（§4.3 明写 09:35–14:00）
	tWindowFrom    = "09:35" // T 腿窗口起点（振幅/两轮价差用）
	tWindowTo      = "14:30" // T 腿窗口终点
)

// Bar 一根分钟 K（与 store.MinuteBar 同形的纯数据视图）。
// 刻意不直接依赖 store：本包要保持"喂什么数据都算得出来"的可测性，
// 也让确定性测试不必拖一个 SQLite 进来。
// English: one minute bar as a plain data view of store.MinuteBar.
type Bar struct {
	Ts     string  // 北京墙钟 "2026-09-24 14:35:00"
	Open   float64 // 开盘
	High   float64 // 最高
	Low    float64 // 最低
	Close  float64 // 收盘
	Vol    float64 // 成交量
	Amount float64 // 成交额（元）
}

// DayBars 一个交易日的分钟根（Bars 必须已按 Ts 升序）。
// English: one trading day's minute bars (already ascending by Ts).
type DayBars struct {
	Day  string // "2026-09-24"
	Bars []Bar
}

// CodeDays 一只票的全部样本日（Days 必须已按 Day 升序）。
// English: all sample days of one code (ascending by Day).
type CodeDays struct {
	Code string
	Days []DayBars
}

// Params 标定参数。零值字段一律由 norm 回填出厂值——
// 这样调用方可以只传关心的那几个，不必抄一遍常量。
// English: calibration parameters; zero fields are backfilled by norm().
type Params struct {
	MinDays   int     `json:"min_days"`   // 步骤 0 的天数门槛
	Tiers     int     `json:"tiers"`      // 分档数
	MinSample int     `json:"min_sample"` // 单档最小样本数
	CalibDays int     `json:"calib_days"` // 标定窗长度
	OOSDays   int     `json:"oos_days"`   // 验证窗长度
	AmountWin int     `json:"amount_win"` // 分档滑窗长度
	CommBps   float64 `json:"comm_bps"`   // 单边佣金（bp）
	StampBps  float64 `json:"stamp_bps"`  // 卖出印花税（bp）
	SlipBps   float64 `json:"slip_bps"`   // 单边滑点（bp，B1 实测中位数）
	SafetyMul float64 `json:"safety_mul"` // 安全边际倍数
}

// norm 回填出厂值（≤0 视为未填）。负数参数没有合法语义，
// 所以"填了负数"与"没填"走同一条回填——避免负数把读数算成负号乱飞。
// English: norm backfills factory defaults for non-positive fields.
func (p Params) norm() Params {
	if p.MinDays <= 0 {
		p.MinDays = DefaultMinDays
	}
	if p.Tiers <= 0 {
		p.Tiers = DefaultTiers
	}
	if p.Tiers > 10 {
		p.Tiers = 10 // 上界保护：超过 10 档每档样本必然不足，没有信息量
	}
	// 样本数与窗口长度：三者共同决定"能不能签字"，任何一个是 0 都必须回填出厂值，
	// 否则窗口放不下会退化成"永远数据不足"。
	if p.MinSample <= 0 {
		p.MinSample = DefaultMinSample
	}
	if p.CalibDays <= 0 {
		p.CalibDays = DefaultCalibDays
	}
	if p.OOSDays <= 0 {
		p.OOSDays = DefaultOOSDays
	}
	if p.AmountWin <= 0 {
		p.AmountWin = DefaultAmountWin
	}
	// 费率三件套 + 安全边际：费率是"反推价差门槛"的唯一输入，填 0 会让门槛变成 0，
	// 所以只允许 SlipBps 显式为 0（"滑点未计入"），其余一律回填出厂值。
	if p.CommBps <= 0 {
		p.CommBps = DefaultCommBps
	}
	if p.StampBps <= 0 {
		p.StampBps = DefaultStampBps
	}
	if p.SlipBps < 0 {
		p.SlipBps = DefaultSlipBps
	}
	if p.SafetyMul <= 0 {
		p.SafetyMul = DefaultSafetyMul
	}
	return p
}

// costPct 一次完整 T 回转的成本（%），不含安全边际：
// 买入佣金 + 卖出佣金 + 买入滑点 + 卖出滑点 + 卖出印花税。
// 注意滑点是**单边各一次**（买腿吃亏一次、卖腿吃亏一次），所以是 2×SlipBps。
// English: round-trip cost in percent (no safety multiplier).
func (p Params) costPct() float64 {
	return (2*p.CommBps + 2*p.SlipBps + p.StampBps) / 1e4 * 100
}

// minSpreadPct 价差门槛：成本 × 安全边际。这是四个量里唯一"算出来"的数，
// 其余三个都来自分钟线分位。
// English: min_spread_pct = round-trip cost × safety multiplier.
func (p Params) minSpreadPct() float64 {
	return p.costPct() * p.SafetyMul
}

// CoverageRow 一只票的分钟线覆盖情况（步骤 0 的逐票结论）。
// English: per-code minute coverage row (step 0).
type CoverageRow struct {
	Code     string `json:"code"`
	Days     int    `json:"days"`     // 有 5 分钟线的交易日数
	FirstTs  string `json:"first_ts"` // 最早一根的时间戳
	LastTs   string `json:"last_ts"`  // 最晚一根的时间戳
	Eligible bool   `json:"eligible"` // 是否进标定样本
	Reason   string `json:"reason,omitempty"`
}

// CoverageSummary 步骤 0 的汇总。不合格的票**逐条列出**（§4.1「不静默丢」）——
// 静默丢样本会让"合格票数"看起来比实际好，是这类工具最经典的假绿。
// English: step-0 coverage summary; excluded codes are listed, never silently dropped.
type CoverageSummary struct {
	MinDays       int           `json:"min_days"`
	CodesTotal    int           `json:"codes_total"`
	CodesEligible int           `json:"codes_eligible"`
	DaysP10       int           `json:"days_p10"`
	DaysP50       int           `json:"days_p50"`
	DaysMax       int           `json:"days_max"`
	Eligible      []CoverageRow `json:"eligible"`
	Excluded      []CoverageRow `json:"excluded"`
}

// Coverage 按 minDays 判定每只票能否进标定样本，并给出天数分布分位。
// 入参 codes 的顺序不影响结果（内部按代码排序），这是确定性锁的一部分。
// English: Coverage decides which codes are eligible (>= minDays) and reports the day distribution.
func Coverage(codes []CodeDays, minDays int) CoverageSummary {
	if minDays <= 0 {
		minDays = DefaultMinDays
	}
	sum := CoverageSummary{MinDays: minDays, Eligible: []CoverageRow{}, Excluded: []CoverageRow{}}
	sorted := append([]CodeDays(nil), codes...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Code < sorted[j].Code })
	dayCounts := make([]float64, 0, len(sorted))

	// 逐票统计交易日数与时间戳跨度；不合格的写进 Excluded 并带上"差多少天"的原因。
	for _, cd := range sorted {
		row := CoverageRow{Code: cd.Code, Days: len(cd.Days)}
		// 首末时间戳取该票最早/最晚一根分钟根（空日不参与，避免空串污染跨度）。
		for _, d := range cd.Days {
			if len(d.Bars) == 0 {
				continue
			}
			if row.FirstTs == "" {
				row.FirstTs = d.Bars[0].Ts
			}
			row.LastTs = d.Bars[len(d.Bars)-1].Ts
		}
		sum.CodesTotal++
		dayCounts = append(dayCounts, float64(row.Days))
		if row.Days >= minDays {
			row.Eligible = true
			sum.CodesEligible++
			sum.Eligible = append(sum.Eligible, row)
			continue
		}
		row.Reason = fmt.Sprintf("分钟线仅 %d 个交易日，差 %d 天（需先回填）", row.Days, minDays-row.Days)
		sum.Excluded = append(sum.Excluded, row)
	}

	// 天数分布（P10/P50/max）：判断"整体缺数据"还是"个别票缺"，两者的处置完全不同。
	if len(dayCounts) > 0 {
		sum.DaysP10 = int(percentile(dayCounts, 10))
		sum.DaysP50 = int(percentile(dayCounts, 50))
		sum.DaysMax = int(percentile(dayCounts, 100))
	}
	return sum
}

// sample 一只票一个交易日的观测（内部结构，不落盘）。
// English: one (code, day) observation — internal only.
type sample struct {
	Code          string
	Day           string
	Tier          int
	DayAmountWan  float64   // 当日成交额（万元）
	SlidingAmtWan float64   // 滑窗日均成交额（万元）——分档依据
	VwapDevs      []float64 // 当日每个统计分钟点的 VWAP 偏离（%）
	AmpPct        float64   // 当日振幅（%），NaN = 无前收
	CapPct        float64   // 可捕获价差（%），NaN = 无前收
	Round1Pct     float64   // 第 1 轮毛价差（%）
	Round2Pct     float64   // 第 2 轮毛价差（%），0 = 不足两段
	Hit30         bool      // 开盘 30 分钟内是否已能覆盖成本
	Hit60         bool      // 开盘 60 分钟内是否已能覆盖成本
}

// tsHHMM 从 "2026-09-24 14:35:00" 取出 "14:35"（形态不符返回空串，
// 空串在字典序上小于任何真实时刻，会被窗口过滤掉而不是被当成 00:00）。
// English: tsHHMM extracts "HH:MM" from a wall-clock timestamp.
func tsHHMM(ts string) string {
	if len(ts) < 16 || ts[10] != ' ' || ts[13] != ':' {
		return ""
	}
	return ts[11:16]
}

// inWindow 判断 hhmm 是否落在 [from, to] 闭区间内（字符串比较对 "HH:MM" 成立）。
// English: inWindow reports whether hhmm is within [from, to].
func inWindow(hhmm, from, to string) bool {
	if hhmm == "" {
		return false
	}
	return hhmm >= from && hhmm <= to
}

// windowCloses 取某日落在 [from, to] 内的分钟收盘价序列（升序）。
// English: windowCloses returns the minute closes inside [from, to].
func windowCloses(bars []Bar, from, to string) []float64 {
	out := make([]float64, 0, len(bars))
	// 只保留有正收盘价且落在窗口内的根：0 价（停牌/脏行）会让百分比失真。
	for _, b := range bars {
		if b.Close <= 0 || !inWindow(tsHHMM(b.Ts), from, to) {
			continue
		}
		out = append(out, b.Close)
	}
	return out
}

// barAmount 取一根分钟根的成交额，并在上游缺 amount 时用 close×vol 近似。
//
// 为什么必须有这条兜底：本仓 minute_klines 的 amount 列在实跑库里大量为 0
// （上游只给了 vol），若不兜底，VWAP 分母恒为 0 ⇒ 回踩门槛恒为 0、
// 分档成交额全为 0 ⇒ 四档塌成一档，而报告看起来"正常"。这是最坏的一类静默失效。
// 返回值第二项标记"本次用了近似"，供报告统计近似根数。
// English: barAmount falls back to close×vol when upstream amount is missing
// (the real minute table often stores 0) — otherwise VWAP and tiering silently collapse.
func barAmount(b Bar) (float64, bool) {
	if b.Amount > 0 {
		return b.Amount, false
	}
	if b.Close > 0 && b.Vol > 0 {
		return b.Close * b.Vol, true
	}
	return 0, false
}

// vwapDevsOf 算当日 [09:35, 14:00] 每分钟收盘对**累计** VWAP 的偏离（%）。
//
// 分母口径写死：VWAP 用**当日已累计**的成交额/成交量（分钟近似），不是全天量——
// 盘中的"全天 VWAP"是未来数据，用它算偏离会系统性低估（§2.1 同一条纪律）。
// 越早的时刻累计样本越少，读数越毛躁，报告里必须标注这一点。
// 第二个返回值是"用 close×vol 近似成交额"的根数（上游缺 amount 时的兜底计数）。
// English: per-minute deviation from the *running* VWAP (never the full-day VWAP).
func vwapDevsOf(bars []Bar) ([]float64, int) {
	out := make([]float64, 0, 32)
	proxy := 0
	var cumAmt, cumVol float64
	// 累计额/量从当日第一根开始累加（09:30 起），统计点才从 09:35 起——
	// 这样 09:35 的 VWAP 已经有 5 分钟的底量，不会一上来就被单根噪声主导。
	for _, b := range bars {
		amt, usedProxy := barAmount(b)
		if amt > 0 {
			cumAmt += amt
			if usedProxy {
				proxy++
			}
		}
		if b.Vol > 0 {
			cumVol += b.Vol
		}
		if !inWindow(tsHHMM(b.Ts), vwapWindowFrom, vwapWindowTo) || cumVol <= 0 || b.Close <= 0 {
			continue
		}
		v := cumAmt / cumVol
		if v <= 0 {
			continue
		}
		out = append(out, (b.Close-v)/v*100)
	}
	return out, proxy
}

// bestRound 在一段分钟收盘价里取"先买后卖"的最大单波段涨幅（%）。
// 返回 (spread, 买点下标, 卖点下标)；不足两根或全段下跌时 spread=0、下标 -1。
//
// 这是**上界**：它假设买在最低那根的收盘、卖在之后最高的那根收盘，
// 实盘成交不到极值。所以下游只拿它的**中位数**做判据，绝不拿均值当预期收益。
// English: bestRound returns the largest buy-then-sell swing (upper bound, used via median only).
func bestRound(closes []float64) (float64, int, int) {
	if len(closes) < 2 {
		return 0, -1, -1
	}
	minIdx, best, buyIdx, sellIdx := 0, 0.0, -1, -1
	// 单遍扫描：维护"到目前为止的最低点"，每根都试一次"从最低点卖到这里"。
	for j := 1; j < len(closes); j++ {
		if closes[j] < closes[minIdx] {
			minIdx = j
		}
		base := closes[minIdx]
		if base <= 0 {
			continue
		}
		if sp := (closes[j] - base) / base * 100; sp > best {
			best, buyIdx, sellIdx = sp, minIdx, j
		}
	}
	return best, buyIdx, sellIdx
}

// twoRounds 贪心切两段（**不重叠**）：第 1 轮取全窗口最大单波段，
// 第 2 轮在剩下的两段（买点之前、卖点之后）里再取一次，取较大者。
//
// 为什么必须不重叠：允许重叠等于把同一段行情卖两次，第 2 轮读数会恒为正，
// 那样"第 2 轮该不该放开"这个问题就永远答不了。
// English: twoRounds greedily splits two non-overlapping swings; overlap would fake round 2.
func twoRounds(closes []float64) (float64, float64) {
	r1, buyIdx, sellIdx := bestRound(closes)
	if buyIdx < 0 || sellIdx < 0 {
		return r1, 0
	}
	// 前段 [0, buyIdx]、后段 (sellIdx, end]：两段各自再取一次最大波段。
	left, _, _ := bestRound(closes[:buyIdx])
	right, _, _ := bestRound(closes[sellIdx+1:])
	r2 := math.Max(left, right)
	return r1, r2
}

// buildSamples 把每票的分钟样本日折成"票×日"观测，并算出分档用的滑窗日均成交额。
// 滑窗 = 该票**最近 AmountWin 个样本日**的日均成交额（含当日），不足则用已有天数平均。
// 第二个返回值是"用 close×vol 近似成交额"的根数（0 表示上游 amount 完整）。
// English: buildSamples folds each (code, day) into an observation with its trailing-window turnover.
func buildSamples(codes []CodeDays, p Params) ([]sample, int) {
	out := make([]sample, 0, 1024)
	proxyBars := 0
	sorted := append([]CodeDays(nil), codes...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Code < sorted[j].Code })

	for _, cd := range sorted {
		days := append([]DayBars(nil), cd.Days...)
		sort.Slice(days, func(i, j int) bool { return days[i].Day < days[j].Day })
		amtWin := make([]float64, 0, p.AmountWin) // 环形窗口（用切片滚动，长度即样本数）
		prevClose := 0.0
		for _, d := range days {
			if len(d.Bars) == 0 {
				continue
			}
			// 当日成交额（万元）：分钟额求和（缺 amount 时按 close×vol 近似）。
			// 上游若给"千元"，档位绝对值会变但**排序不变**，而四分位切档只依赖排序
			// ⇒ 单位口径差异不影响分档结果。
			var dayAmt float64
			for _, b := range d.Bars {
				amt, usedProxy := barAmount(b)
				dayAmt += amt
				if usedProxy {
					proxyBars++
				}
			}
			dayAmtWan := dayAmt / 1e4
			amtWin = append(amtWin, dayAmtWan)
			if len(amtWin) > p.AmountWin {
				amtWin = amtWin[len(amtWin)-p.AmountWin:]
			}
			var sum float64
			for _, v := range amtWin {
				sum += v
			}
			devs, devProxy := vwapDevsOf(d.Bars)
			proxyBars += devProxy
			s := sample{
				Code:          cd.Code,
				Day:           d.Day,
				DayAmountWan:  dayAmtWan,
				SlidingAmtWan: sum / float64(len(amtWin)),
				VwapDevs:      devs,
			}
			closes := windowCloses(d.Bars, tWindowFrom, tWindowTo)
			s.Round1Pct, s.Round2Pct = twoRounds(closes)
			// 振幅与可捕获价差都以**前收**为基准；没有前收（首日）就留 NaN，
			// 让聚合阶段把它剔除，而不是拿当日开盘价顶替（那会系统性低估振幅）。
			if prevClose > 0 && len(closes) > 0 {
				hi, lo := closes[0], closes[0]
				for _, c := range closes {
					if c > hi {
						hi = c
					}
					if c < lo {
						lo = c
					}
				}
				s.AmpPct = (hi - lo) / prevClose * 100
				if lo > 0 {
					s.CapPct = (hi - lo) / lo * 100
				}
			} else {
				s.AmpPct, s.CapPct = math.NaN(), math.NaN()
			}
			// 命中率：开盘 30 / 60 分钟内"可捕获价差"是否已经覆盖成本门槛。
			// 这是"当天值不值得盯"的前置读数，与全天可捕获价差是两件事。
			s.Hit30 = windowCap(closes, 30) >= p.costPct()
			s.Hit60 = windowCap(closes, 60) >= p.costPct()
			out = append(out, s)
			// 前收取当日最后一根收盘（分钟表的"当日收盘"就是收盘前最后一根）。
			prevClose = d.Bars[len(d.Bars)-1].Close
		}
	}
	return out, proxyBars
}

// windowCap 取一段分钟收盘价里"前 minutes 根"的最大单波段涨幅（%）。
// 分钟根按 5 分钟一根计，30 分钟 ≈ 6 根、60 分钟 ≈ 12 根。
// English: windowCap is the best single swing within the first `minutes` of the session.
func windowCap(closes []float64, minutes int) float64 {
	n := minutes / 5
	if n < 2 || len(closes) < 2 {
		return 0
	}
	if n > len(closes) {
		n = len(closes)
	}
	sp, _, _ := bestRound(closes[:n])
	return sp
}

// assignTiers 按滑窗日均成交额的四分位把样本切档（P25/P50/P75 为界）。
// 分位用最近秩法，样本数不足时相邻档自然重合，交给 mergeSmallTiers 处理。
// English: assignTiers buckets samples by trailing-turnover quartiles.
func assignTiers(samples []sample, tiers int) {
	if tiers <= 0 || len(samples) == 0 {
		return
	}
	amts := make([]float64, 0, len(samples))
	for _, s := range samples {
		amts = append(amts, s.SlidingAmtWan)
	}
	cuts := make([]float64, 0, tiers-1)
	// 切点取 i/tiers 分位（i=1..tiers-1）：4 档 ⇒ P25/P50/P75。
	for i := 1; i < tiers; i++ {
		cuts = append(cuts, percentile(amts, float64(i)*100/float64(tiers)))
	}
	for i := range samples {
		t := 0
		for _, c := range cuts {
			if samples[i].SlidingAmtWan > c {
				t++
			}
		}
		if t >= tiers {
			t = tiers - 1
		}
		samples[i].Tier = t
	}
}

// mergeSmallTiers 把小样本档并入相邻档，返回 old→new 的档号映射与合并说明。
//
// 算法：先找出"样本达标"的**锚点档**，其余档各自归到**最近的锚点**（等距取更低档）。
// 为什么不用"依次并入前一档"那种写法：它会在两个都不达标的相邻档之间来回乒乓
// （A 并进 B 后 B 达标了、但下一轮 B 又被判为不达标而并回 A），永远收敛不了。
//
// 等距取更低档是有意的：低流动性档滑点更高、门槛更严，把稀疏样本并进去是**保守**方向。
// 全部档都不达标时合并成单档——那种情况下"分四档"本身就是伪精度。
// English: mergeSmallTiers assigns every under-sampled tier to its nearest anchor tier
// (ties go to the lower tier, the conservative side); a naive cascade would never converge.
func mergeSmallTiers(samples []sample, tiers, minSample int) (map[int]int, []string) {
	counts := make([]int, tiers)
	for _, s := range samples {
		if s.Tier >= 0 && s.Tier < tiers {
			counts[s.Tier]++
		}
	}
	mapping := make(map[int]int, tiers)
	notes := make([]string, 0, tiers)

	// 锚点＝样本达标的档；一个都没有时退化为单档，并写明原因（不假装还能分档）。
	anchors := make([]int, 0, tiers)
	for i, c := range counts {
		if c >= minSample {
			anchors = append(anchors, i)
		}
	}
	if len(anchors) == 0 {
		for i := 0; i < tiers; i++ {
			mapping[i] = 0
		}
		notes = append(notes, fmt.Sprintf("全部 %d 档样本均 < %d，已合并为单档（分档无意义）", tiers, minSample))
		return mapping, notes
	}

	// 每档归到最近锚点；说明里保留**原档号**（T3 并入 T2 比"新档 1 并入新档 0"好核对）。
	for i := 0; i < tiers; i++ {
		best, bestD := anchors[0], absInt(anchors[0]-i)
		for _, a := range anchors[1:] {
			if d := absInt(a - i); d < bestD {
				best, bestD = a, d
			}
		}
		mapping[i] = best
		if i != best && counts[i] > 0 {
			notes = append(notes, fmt.Sprintf("T%d 样本 %d < %d，已并入 T%d", i+1, counts[i], minSample, best+1))
		}
	}
	// 锚点按升序重新编号为 0..m-1（空档不占号，报告里不会出现"跳号"）。
	renum := make(map[int]int, len(anchors))
	for k, a := range anchors {
		renum[a] = k
	}
	for i := range mapping {
		mapping[i] = renum[mapping[i]]
	}
	return mapping, notes
}

// absInt 整数绝对值（只为窗口/档距计算，不引 math 的浮点版本）。
// English: absInt is integer abs for tier distance.
func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// TierStat 一档的标定结果（报告与 JSON 的主体）。
// English: TierStat is one tier's calibration outcome.
type TierStat struct {
	Tier            int     `json:"tier"`              // 档号（1 起，便于人读）
	N               int     `json:"n"`                 // 标定窗样本数（票×日）
	Codes           int     `json:"codes"`             // 该档涉及票数
	AmountLoWan     float64 `json:"amount_lo_wan"`     // 该档滑窗日均成交额下沿（万元）
	AmountHiWan     float64 `json:"amount_hi_wan"`     // 上沿（万元）
	MinSpreadPct    float64 `json:"min_spread_pct"`    // 价差门槛（反推 × 安全边际）
	VwapDevPct      float64 `json:"vwap_dev_pct"`      // 回踩门槛（P70）
	MinAmplitudePct float64 `json:"min_amplitude_pct"` // 振幅门槛（P60，不低于价差门槛）
	MaxRounds       int     `json:"max_rounds"`        // 1 或 2
	Round1MedianPct float64 `json:"round1_median_pct"` // 第 1 轮毛价差中位数
	Round2MedianPct float64 `json:"round2_median_pct"` // 第 2 轮毛价差中位数
	CostPct         float64 `json:"cost_pct"`          // 该档回转成本（%）
	IsNet           bool    `json:"is_net"`            // 标定窗内 第 1 轮 − 成本 > 0
	HitRate30m      float64 `json:"hit_rate_30m"`      // 开盘 30 分钟内覆盖成本的比例
	HitRate60m      float64 `json:"hit_rate_60m"`      // 开盘 60 分钟内覆盖成本的比例
	OOSN            int     `json:"oos_n"`             // 样本外样本数
	OOSNetPct       float64 `json:"oos_net_pct"`       // 样本外 第 1 轮中位数 − 成本
	Replaced        bool    `json:"replaced"`          // 是否建议替换现值
	Note            string  `json:"note,omitempty"`    // 不替换/合并的原因
}

// WindowInfo 窗口切分的机读形态（含边界日，便于人工核对"有没有重叠"）。
// English: WindowInfo records the calibration/OOS split for audit.
type WindowInfo struct {
	CalibFrom string `json:"calib_from"`
	CalibTo   string `json:"calib_to"`
	OOSFrom   string `json:"oos_from"`
	OOSTo     string `json:"oos_to"`
	CalibN    int    `json:"calib_n"`
	OOSN      int    `json:"oos_n"`
	TotalN    int    `json:"total_n"`
}

// Report 标定报告（机读）。字段顺序固定、全部 round6，保证两次跑逐字节一致。
// English: Report is the machine-readable calibration artifact (deterministic by construction).
type Report struct {
	SampleEnd  string          `json:"sample_end"` // 样本最后一日（YYYYMMDD），不是运行时刻
	Params     Params          `json:"params"`
	Coverage   CoverageSummary `json:"coverage"`
	Window     WindowInfo      `json:"window"`
	Tiers      []TierStat      `json:"tiers"`
	MergeNotes []string        `json:"merge_notes,omitempty"` // 档位合并说明（原档号，全局一份）
	// AmountProxyBars 上游缺 amount、用 close×vol 近似的分钟根数。
	// 0 = 上游成交额完整；非 0 时报告会提示"VWAP 与分档基于近似额"。
	AmountProxyBars int    `json:"amount_proxy_bars"`
	Verdict         string `json:"verdict"` // 一句话结论（人读摘要，也是自动化的判据入口）
}

// ErrShortHistory 样本日不足（标定窗+验证窗放不下）。调用方据此输出"数据不足，先回填"。
// English: ErrShortHistory means there are not enough sample days for both windows.
type ErrShortHistory struct {
	Need int
	Got  int
}

func (e ErrShortHistory) Error() string {
	return fmt.Sprintf("样本日不足：需要 %d 个交易日（标定+验证），实得 %d 个", e.Need, e.Got)
}

// splitWindowsAt 按**显式边界**切分标定窗与验证窗，并硬校验两者不重叠。
// 这是窗口锁的唯一落点：任何"偷进标定窗"的改动（把 oosFrom 改成 calibTo）
// 都会在这里被判红，而不是靠调用方自觉。
// English: splitWindowsAt is the single enforcement point of the non-overlap lock.
func splitWindowsAt(days []string, calibTo, oosFrom int) (WindowInfo, error) {
	var w WindowInfo
	if calibTo < 0 || oosFrom < 0 || calibTo >= len(days) || oosFrom >= len(days) {
		return w, fmt.Errorf("窗口边界越界：calibTo=%d oosFrom=%d 样本日=%d", calibTo, oosFrom, len(days))
	}
	// 重叠锁：验证窗起点必须严格晚于标定窗终点（差 1 天都不行，闭区间）。
	if oosFrom <= calibTo {
		return w, fmt.Errorf("窗口重叠：标定窗止于第 %d 日，验证窗起于第 %d 日（必须严格不重叠）", calibTo+1, oosFrom+1)
	}
	w.CalibFrom, w.CalibTo = days[0], days[calibTo]
	w.OOSFrom, w.OOSTo = days[oosFrom], days[len(days)-1]
	w.CalibN = calibTo + 1
	w.OOSN = len(days) - oosFrom
	w.TotalN = len(days)
	return w, nil
}

// SplitWindows 把样本日切成"标定窗在前、验证窗在后"两段（不重叠）。
// 样本日多于所需时**只用最后 need 天**（最近的一段）：越早的分钟样本越可能来自
// 不同的流动性环境，把它们并进标定窗只会让阈值更钝。
// 不足 CalibDays+OOSDays 时返回 ErrShortHistory——此时工具仍要交付，
// 但只产出覆盖率报告，不产出阈值（§4.1）。
// English: SplitWindows uses only the most recent need days and splits them into two disjoint windows.
func SplitWindows(days []string, calibDays, oosDays int) (WindowInfo, error) {
	sorted := append([]string(nil), days...)
	sort.Strings(sorted)
	need := calibDays + oosDays
	if len(sorted) < need {
		return WindowInfo{}, ErrShortHistory{Need: need, Got: len(sorted)}
	}
	// 只保留最近 need 天：标定窗恰好 calibDays 天、验证窗恰好 oosDays 天，两者首尾相接不重叠。
	recent := sorted[len(sorted)-need:]
	oosFrom := len(recent) - oosDays
	return splitWindowsAt(recent, oosFrom-1, oosFrom)
}

// Calibrate 是唯一对外入口：喂覆盖率合格的分钟样本，产出完整报告。
// 返回 error 只在"窗口放不下"时出现；那种情况下 report 仍带覆盖率（可交付）。
// English: Calibrate is the single entry point; a short history still yields a coverage-only report.
func Calibrate(codes []CodeDays, p Params) (Report, error) {
	p = p.norm()
	cov := Coverage(codes, p.MinDays)
	rep := Report{Params: p, Coverage: cov, Tiers: []TierStat{}}

	// 只把合格票喂进样本构建：天数不够的票进样本会污染分位（它们的分钟覆盖本身就缺）。
	eligible := make([]CodeDays, 0, len(cov.Eligible))
	byCode := make(map[string]CodeDays, len(codes))
	for _, cd := range codes {
		byCode[cd.Code] = cd
	}
	for _, row := range cov.Eligible {
		if cd, ok := byCode[row.Code]; ok {
			eligible = append(eligible, cd)
		}
	}
	samples, proxyBars := buildSamples(eligible, p)
	rep.AmountProxyBars = proxyBars
	if len(samples) == 0 {
		rep.Verdict = "无可用分钟样本（先跑分钟线回填，再重跑 tcalib）"
		return rep, nil
	}

	// 样本日去重升序：窗口按"交易日"切，不是按"样本条数"切。
	daySet := make(map[string]bool, len(samples))
	days := make([]string, 0, len(samples))
	for _, s := range samples {
		if !daySet[s.Day] {
			daySet[s.Day] = true
			days = append(days, s.Day)
		}
	}
	sort.Strings(days)
	rep.SampleEnd = strings.ReplaceAll(days[len(days)-1], "-", "")

	win, err := SplitWindows(days, p.CalibDays, p.OOSDays)
	if err != nil {
		rep.Verdict = fmt.Sprintf("数据不足，先回填：%v", err)
		return rep, err
	}
	rep.Window = win

	// 分档在**全部样本**上做（档位边界必须来自完整分布），
	// 统计量再按窗口分别聚合——反过来做会让标定窗与验证窗的档位定义不一致。
	assignTiers(samples, p.Tiers)
	mapping, notes := mergeSmallTiers(samples, p.Tiers, p.MinSample)
	for i := range samples {
		if n, ok := mapping[samples[i].Tier]; ok {
			samples[i].Tier = n
		} else {
			samples[i].Tier = -1 // 被合并掉且无幸存目标（极端情况：全档都空）
		}
	}
	tierCount := 0
	for _, n := range mapping {
		if n+1 > tierCount {
			tierCount = n + 1
		}
	}
	rep.Tiers = aggregate(samples, tierCount, win, p)
	rep.MergeNotes = notes
	rep.Verdict = verdictOf(rep)
	return rep, nil
}

// aggregate 按档聚合标定窗与验证窗的读数。
// English: aggregate computes per-tier stats for both windows.
func aggregate(samples []sample, tierCount int, win WindowInfo, p Params) []TierStat {
	cost := p.costPct()
	minSpread := p.minSpreadPct()
	stats := make([]TierStat, 0, tierCount)
	for t := 0; t < tierCount; t++ {
		// 先按窗口把样本分成两桶：calib 用于定阈值，oos 用于验证。
		var calib, oos []sample
		for _, s := range samples {
			if s.Tier != t {
				continue
			}
			if s.Day >= win.CalibFrom && s.Day <= win.CalibTo {
				calib = append(calib, s)
			} else if s.Day >= win.OOSFrom && s.Day <= win.OOSTo {
				oos = append(oos, s)
			}
		}
		st := TierStat{
			Tier:         t + 1,
			N:            len(calib),
			CostPct:      round6(cost),
			MinSpreadPct: round6(minSpread),
			MaxRounds:    1,
			OOSN:         len(oos),
		}
		if len(calib) == 0 {
			st.Note = appendNote(st.Note, "标定窗内无样本")
			stats = append(stats, st)
			continue
		}
		st.Codes = distinctCodes(calib)
		st.AmountLoWan, st.AmountHiWan = amountRange(calib)

		// 四个量：VWAP 偏离 P70、振幅 P60、两轮价差中位数、30/60 分钟命中率。
		devs := make([]float64, 0, len(calib)*16)
		for _, s := range calib {
			devs = append(devs, s.VwapDevs...)
		}
		st.VwapDevPct = round6(percentile(devs, 70))

		amps := make([]float64, 0, len(calib))
		for _, s := range calib {
			if isFinite(s.AmpPct) && isFinite(s.CapPct) && s.CapPct >= minSpread {
				amps = append(amps, s.AmpPct)
			}
		}
		if len(amps) == 0 {
			// 没有任何一天的"可捕获价差"能覆盖成本 ⇒ 振幅门槛退回价差门槛，
			// 并留痕。这不是"算出来 0"，而是"样本里根本没有能做的日子"。
			st.MinAmplitudePct = round6(minSpread)
			st.Note = appendNote(st.Note, "无可捕获价差覆盖成本的样本，振幅门槛回退为价差门槛")
		} else {
			st.MinAmplitudePct = round6(math.Max(percentile(amps, 60), minSpread))
		}

		r1 := make([]float64, 0, len(calib))
		r2 := make([]float64, 0, len(calib))
		hit30, hit60 := 0, 0
		for _, s := range calib {
			r1 = append(r1, s.Round1Pct)
			if s.Round2Pct > 0 {
				r2 = append(r2, s.Round2Pct)
			}
			if s.Hit30 {
				hit30++
			}
			if s.Hit60 {
				hit60++
			}
		}
		st.Round1MedianPct = round6(median(r1))
		st.Round2MedianPct = round6(median(r2))
		st.HitRate30m = round6(float64(hit30) / float64(len(calib)) * 100)
		st.HitRate60m = round6(float64(hit60) / float64(len(calib)) * 100)
		st.IsNet = st.Round1MedianPct > st.CostPct
		// 第 2 轮放开条件：扣成本后的中位数必须为正。中位数而非均值——
		// 单日极值会把均值拉起来，而 T 的日常体验由中位那天决定。
		if st.Round2MedianPct > st.CostPct {
			st.MaxRounds = 2
		}

		// 样本外：同一套口径在验证窗上重算一次，只回答"这个档能不能签字"。
		if len(oos) > 0 {
			or1 := make([]float64, 0, len(oos))
			for _, s := range oos {
				or1 = append(or1, s.Round1Pct)
			}
			st.OOSNetPct = round6(median(or1) - cost)
		}
		st.Replaced = st.OOSN >= p.MinSample && st.OOSNetPct > 0
		if !st.Replaced {
			st.Note = appendNote(st.Note, replaceBlockReason(st, p.MinSample))
		}
		stats = append(stats, st)
	}
	return stats
}

// replaceBlockReason 给出"不替换"的具体原因（样本数不足 / 样本外不为正）。
// 缺这条原因就等于让读者自己猜，是这类报告最常见的失效方式。
// English: replaceBlockReason names why a tier must not be replaced.
func replaceBlockReason(st TierStat, minSample int) string {
	if st.OOSN < minSample {
		return fmt.Sprintf("样本外仅 %d < %d，证据不足，保持现值", st.OOSN, minSample)
	}
	if st.OOSNetPct <= 0 {
		return fmt.Sprintf("样本外净价差 %.3f%% ≤ 0，保持现值", st.OOSNetPct)
	}
	return ""
}

// verdictOf 一句话结论：决定"能不能签字"，也方便运维脚本 grep。
// English: verdictOf summarises whether the report is actionable.
func verdictOf(rep Report) string {
	if rep.Coverage.CodesEligible == 0 {
		return "数据不足，先回填：无票达到分钟线天数门槛"
	}
	replaced := 0
	for _, st := range rep.Tiers {
		if st.Replaced {
			replaced++
		}
	}
	if replaced == 0 {
		return fmt.Sprintf("无档可替换（%d 档全部证据不足），阈值保持现值", len(rep.Tiers))
	}
	return fmt.Sprintf("%d/%d 档可签字（样本外为正且样本数达标）", replaced, len(rep.Tiers))
}

// JSON 序列化为机读产物（缩进固定两空格，末尾无换行差异）。
// English: JSON renders the machine-readable artifact.
func (r Report) JSON() ([]byte, error) {
	return json.MarshalIndent(r, "", "  ")
}

// Markdown 渲染人读报告。刻意不复用 JSON 的结构化字段顺序——
// 人读的是"这张表能不能签字"，所以把判据列放在阈值列前面。
// English: Markdown renders the human report (verdict columns before threshold columns).
func (r Report) Markdown() string {
	var b strings.Builder
	b.WriteString("# §T-COST 阈值标定报告（tcalib）\n\n")
	fmt.Fprintf(&b, "- 样本最后交易日：**%s**（报告名与内容都由它决定，与运行时刻无关）\n", r.SampleEnd)
	fmt.Fprintf(&b, "- 结论：**%s**\n", r.Verdict)
	fmt.Fprintf(&b, "- 窗口：标定窗 %s ~ %s（%d 日）；验证窗 %s ~ %s（%d 日）\n",
		r.Window.CalibFrom, r.Window.CalibTo, r.Window.CalibN, r.Window.OOSFrom, r.Window.OOSTo, r.Window.OOSN)
	fmt.Fprintf(&b, "- 成本口径：佣金 %.2fbp×2 + 滑点 %.2fbp×2 + 印花税 %.2fbp = %.4f%%（安全边际 ×%.2f ⇒ 门槛 %.4f%%）\n",
		r.Params.CommBps, r.Params.SlipBps, r.Params.StampBps, r.Params.costPct(), r.Params.SafetyMul, r.Params.minSpreadPct())
	if r.Params.SlipBps == 0 {
		b.WriteString("- ⚠️ 滑点未计入（`--slip-bps` 未给）：价差门槛偏乐观，签字前请用 B1 账本的中位数重跑\n")
	}
	if r.AmountProxyBars > 0 {
		fmt.Fprintf(&b, "- ⚠️ 上游分钟成交额缺失：%d 根用 `close×vol` 近似（VWAP 与分档基于近似额，口径可接受但需周知）\n",
			r.AmountProxyBars)
	}

	b.WriteString("\n## 步骤 0：分钟线覆盖率\n\n")
	fmt.Fprintf(&b, "- 票数 %d，合格（≥%d 交易日）%d；天数 P10/P50/max = %d/%d/%d\n",
		r.Coverage.CodesTotal, r.Coverage.MinDays, r.Coverage.CodesEligible,
		r.Coverage.DaysP10, r.Coverage.DaysP50, r.Coverage.DaysMax)
	if len(r.Coverage.Excluded) > 0 {
		b.WriteString("\n不合格票（**不静默丢**，逐条列出）：\n\n| 代码 | 交易日 | 原因 |\n|---|---|---|\n")
		for _, row := range r.Coverage.Excluded {
			fmt.Fprintf(&b, "| %s | %d | %s |\n", row.Code, row.Days, row.Reason)
		}
	}

	b.WriteString("\n## 分档阈值\n\n")
	b.WriteString("| 档 | 样本 | 票数 | 滑窗日均额(万) | 价差门槛% | 回踩门槛% | 振幅门槛% | 最大轮次 | 第1轮中位% | 第2轮中位% | 30m命中% | 60m命中% | 样本外n | 样本外净% | 可签字 |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	// 逐档一行；"可签字"列直接写结论，避免读者在两个表之间来回对照。
	for _, st := range r.Tiers {
		fmt.Fprintf(&b, "| T%d | %d | %d | %.0f~%.0f | %.4f | %.4f | %.4f | %d | %.4f | %.4f | %.1f | %.1f | %d | %.4f | %s |\n",
			st.Tier, st.N, st.Codes, st.AmountLoWan, st.AmountHiWan, st.MinSpreadPct, st.VwapDevPct,
			st.MinAmplitudePct, st.MaxRounds, st.Round1MedianPct, st.Round2MedianPct,
			st.HitRate30m, st.HitRate60m, st.OOSN, st.OOSNetPct, boolMark(st.Replaced))
	}

	// 逐档原因：合并说明与不替换原因都落在这里，报告才自解释。
	var notes []string
	for _, n := range r.MergeNotes {
		notes = append(notes, "- "+n)
	}
	for _, st := range r.Tiers {
		if st.Note == "" {
			continue
		}
		notes = append(notes, fmt.Sprintf("- T%d：%s", st.Tier, st.Note))
	}
	if len(notes) > 0 {
		b.WriteString("\n## 说明（合并 / 不替换的原因）\n\n")
		b.WriteString(strings.Join(notes, "\n"))
		b.WriteString("\n")
	}

	b.WriteString("\n## 口径提醒（读数必须带着这几句读）\n\n")
	b.WriteString("1. `可捕获价差` = (日内高−日内低)/日内低，是**上界**：它假设买在最低那根的收盘、卖在之后最高的那根收盘，实盘成交不到极值。所以判据只用**中位数**，不用均值。\n")
	b.WriteString("2. `回踩门槛` 的 VWAP 用**当日累计**成交额/成交量（分钟近似），不是全天 VWAP——盘中拿全天量算等于用未来数据，会系统性低估。越早的时刻累计样本越少，读数越毛躁。\n")
	b.WriteString("3. `最大轮次` 用贪心切**不重叠**两段：允许重叠等于把同一段行情卖两次，第 2 轮读数会恒为正，问题就永远答不了。\n")
	b.WriteString("4. 本工具**只产出建议值**，不写任何配置。签字后才落 `sizing.t.calib`（4 档结构，沿用 `tClamp` 的区间校验）。\n")
	return b.String()
}

// boolMark 把布尔判据渲染成中文标记（报告里不出现 true/false）。
// English: boolMark renders a boolean as a CJK mark.
func boolMark(v bool) string {
	if v {
		return "✅"
	}
	return "—"
}

// appendNote 追加一条说明（分号分隔，空串时直接替换）。
// English: appendNote appends a semicolon-separated note.
func appendNote(existing, add string) string {
	if add == "" {
		return existing
	}
	if existing == "" {
		return add
	}
	return existing + "；" + add
}

// distinctCodes 统计样本涉及的票数。
// English: distinctCodes counts the codes in the sample set.
func distinctCodes(ss []sample) int {
	seen := make(map[string]bool, len(ss))
	for _, s := range ss {
		seen[s.Code] = true
	}
	return len(seen)
}

// amountRange 取该档滑窗日均成交额的上下沿（用于报告标注档位边界）。
// English: amountRange returns the trailing-turnover band of a tier.
func amountRange(ss []sample) (float64, float64) {
	if len(ss) == 0 {
		return 0, 0
	}
	lo, hi := ss[0].SlidingAmtWan, ss[0].SlidingAmtWan
	for _, s := range ss[1:] {
		if s.SlidingAmtWan < lo {
			lo = s.SlidingAmtWan
		}
		if s.SlidingAmtWan > hi {
			hi = s.SlidingAmtWan
		}
	}
	return round6(lo), round6(hi)
}

// percentile 最近秩分位（0~100，空样本返回 0）。升序副本排序后取
// int((n−1)×p/100) 位——确定性、无插值，两次跑必然同值。
// English: nearest-rank percentile (no interpolation) — deterministic by construction.
func percentile(vals []float64, p float64) float64 {
	clean := make([]float64, 0, len(vals))
	for _, v := range vals {
		if isFinite(v) {
			clean = append(clean, v)
		}
	}
	if len(clean) == 0 {
		return 0
	}
	sort.Float64s(clean)
	idx := int(float64(len(clean)-1) * p / 100)
	if idx < 0 {
		idx = 0
	}
	if idx >= len(clean) {
		idx = len(clean) - 1
	}
	return clean[idx]
}

// median 中位数（等价于 P50）。
// English: median is P50.
func median(vals []float64) float64 {
	return percentile(vals, 50)
}

// isFinite 判断有限数（NaN/Inf 一律剔除，防止它们把分位算成 NaN 再静默传下去）。
// English: isFinite rejects NaN/Inf so they never poison a percentile.
func isFinite(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}

// round6 保留 6 位小数。输出前统一走它，是"两次跑逐字节一致"的最后一道保险：
// 浮点累加顺序不同会在末位抖动，而 JSON 会把抖动原样写出来。
// English: round6 is the last line of defence for byte-identical reruns.
func round6(v float64) float64 {
	if !isFinite(v) {
		return 0
	}
	return math.Round(v*1e6) / 1e6
}
