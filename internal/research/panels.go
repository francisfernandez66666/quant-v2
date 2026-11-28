// panels.go — §REPLAY-INPUTS 2026-11-28：回放/研究侧的市场级面板（一次装配、全池共用）。
//
// 为什么要有"面板"这个层次（方案 §1.2 / §2.2）：
//  1. **逐股函数拿不到全市场截面**。`Assemble` 是一只票一次，龙头分位、板块净流入横截面分位
//     这类"当日全体比出来的位次"在逐股循环里既算不出也算不起（4309 只 × 900 日）。
//  2. **小面板一次读、大表逐票查**。D1（现网实测 60681 行 / 67 个日期）与板块净流入
//     （百余日 × ~496 板块）整表进内存只有几 MB，一次读掉；流通股本、资金流、板块归属
//     按票查（主键前缀命中），否则 4309 只票的市场级数据会把一个只有 500M 配额的进程撑死。
//  3. **缺哪个面板必须点名**。本文件把每个面板的装载失败都记进 `Missing`，
//     由调用方打进回放报告头——"腿在但永远说不"和"腿没接上"在结果上同形，
//     只有点名才分得开（AGENTS 里 bars_coverage / sector_hit 两次同样的教训）。
//
// ★ 口径同义纪律（与载体逐字对齐，方案 §2.2 第 1 条）：
//
//	IndexChange  载体 `_index_change_pct` = (close[-1]/close[-2] - 1) × 100，
//	             代码优先级 `_FLOW_INDEX_CODES = ("000001.SH", "000300.SH")`
//	             ⇒ Go 侧必须同一只主指数、同一个 ×100，否则同名不同义。
//	SectorQ      板块净流入率 = net_inflow/amount×100，与 flow_factor_daily.market_flow_pct 同尺，
//	             再取当日全板块横截面分位（0..100）。这是**代理**，不是载体的 ConceptHeat。
//
// English: market-level panels for replay — batch-load the small cross-sectional inputs,
// keep per-stock inputs as per-stock queries, and never let a missing panel go unnamed.
package research

import (
	"math"
	"sort"
	"sync/atomic"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/store"
)

// panelIndexBackfillDays 算涨跌幅要多取的前置交易日（首日也要有"上一日收盘"可对）。
// 取 21 个自然日：够跨一个春节长假（最长休市约 9 天）而不让首日凭空弃权。
const panelIndexBackfillDays = 21

// indexPriority 基准指数取数顺序——**必须与载体 _FLOW_INDEX_CODES 同序**（上证主、沪深300 次）。
var indexPriority = []string{"000001.SH", "000300.SH"}

// Panels 一次装配、全池共用的市场级输入。空结构体是合法值（全部按弃权运行），
// 所以调用方拿到 nil 也能安全跑——这是 fail-closed，不是崩。
type Panels struct {
	// d1ByDay 紧凑日 → 6 位纯码 → D1 带符号分。缺日/缺票都是没有键（不是 0）。
	d1ByDay map[string]map[string]float64
	// sectorQuantile 紧凑日 → 东财板块名 → 当日净流入率在全市场板块中的分位（0..100）。
	sectorQuantile map[string]map[string]float64
	// indexPct 紧凑日 → 基准指数当日涨跌幅（%），与载体同口径。
	indexPct map[string]float64
	// indexSource 真正供数的那只指数（读数要能自证"用的是谁"）。
	indexSource string
	// missing 没装上的面板点名（如 "d1" / "sector_flow" / "index_daily"），报告头打印。
	missing []string
	// d1Days 库里 D1 覆盖的日期数（实测现网只有 67 天 ⇒ 引用 NewsEvent 的规则
	// 只在这段窗口上有完整输入，结论不得外推成全窗口）。
	d1Days int
	// sectorBackfill 板块归属"回填"格数：当日早于该票首份 sector_map 快照、
	// 只能借用未来快照的格数（owner 2026-11-28 裁决 2：归属是缓慢变化的维度）。
	// 单日快照回放历史时这个数会很大，报告头必须如实带出来——"用了前视"要能看见。
	sectorBackfill atomic.Int64
}

// noteSectorBackfill 累加回填格数（装配目前串行，但计数用原子是为了将来并行化时
// 这行读数不变成竞态——读数错比没有读数更难查）。
func (p *Panels) noteSectorBackfill() { p.sectorBackfill.Add(1) }

// SectorBackfillCells 回填格数（报告头读数用）。
func (p *Panels) SectorBackfillCells() int64 {
	if p == nil {
		return 0
	}
	return p.sectorBackfill.Load()
}

// sectorQuantileMinBoards 一天里至少要有这么多个可比板块，才认为"当日分位"成立。
// 只有一块时它既是最强也是最弱（分位 0/100 全凭它自己是唯一的样本），
// 那种"有值但方向是假的"比 NaN 弃权更坏——与 §FLOW-SECTOR-AGG 的样本闸同取向。
const sectorQuantileMinBoards = 2

// panelAssemblyTag 市场级面板装配口径位（拼进发现断点键）。
//
// 为什么必须进断点键：断点缓存键原本只含区间/参数/股票池/复权口径，**不含"面板装了什么"**。
// 2026-11-28 之前 discover 的装配走的是不带面板的 Assemble ⇒ 七枚新因子列全 NaN，
// 那些"没有 IC 行"的结论已经被写进窗口断点。若不换键，修好装配之后的第一晚仍会命中旧断点、
// 继续认为这些因子没有读数——**修了等于没修**，而且从日志上看不出任何异常。
// 改动市场级列的语义（新增/改名/换量纲/as-of 口径变化）都必须 bump 这一位。
//
// mp3（2026-11-28 收口批）：板块归属改成"整段早于首份快照时借最早一份"（as-of 向前回填）。
// mp2 的缓存里 SectorFlowQuantile 是按"没有 <=end 快照 ⇒ 整列 NaN"算出来的，
// 不换键就会继续命中那份"没有 IC 行"的旧断点——**这正是本批要修的东西**。
const panelAssemblyTag = "mp3"

// LoadPanels 一次性装配市场级面板。任何一块读不到都不返回错误——
// 面板缺失的后果是对应因子腿 NaN 弃权，而不是整轮回放跑不了。
func LoadPanels(db *store.DB, start, end string) *Panels {
	p := &Panels{
		d1ByDay:        make(map[string]map[string]float64),
		sectorQuantile: make(map[string]map[string]float64),
		indexPct:       make(map[string]float64),
	}
	if db == nil {
		p.missing = append(p.missing, "db_nil")
		return p
	}

	// ① D1 事件分：整表一次读（现网 6 万行级）。逐票查会退化成 4309 次查询。
	if d1, err := db.D1ScoresRange(start, end); err == nil {
		p.d1ByDay = d1
		p.d1Days = len(d1)
	} else {
		p.missing = append(p.missing, "d1_scores:"+err.Error())
	}

	// ② 板块净流入横截面分位：按日把当日各板块的净流入率排序取位次。
	//    分位定义写死在这里（唯一一处）：rank/(n-1)×100，n<2 时整天弃权——
	//    只有一个板块时"分位"没有意义，给 0 或 100 都是编一个方向。
	if flow, err := db.SectorFlowRange(start, end); err == nil {
		for day, bySector := range flow {
			type sp struct {
				name string
				pct  float64
			}
			vals := make([]sp, 0, len(bySector))
			for name, pair := range bySector {
				net, amount := pair[0], pair[1]
				if !(amount > 0) || math.IsNaN(net) || math.IsNaN(amount) {
					continue // 成交额缺失/为 0 ⇒ 这一板块不参与排名（不是排到最后）
				}
				vals = append(vals, sp{name: name, pct: net / amount * 100.0})
			}
			if len(vals) < sectorQuantileMinBoards {
				continue // 整天没有两个可比板块 ⇒ 该日无分位（读数上表现为 NaN 弃权）
			}
			// 分位＝"当日全板块内的位次"，定义只写在这一处（升序名次 / (n-1) ×100）。
			// 两端都占满：最弱板块得 0、最强得 100，与载体 ConceptHeat 的 0..100 尺度同形，
			// 这样同一份 band 表两边可以逐键相等（门禁 §259 ① 的 parity 判据）。
			sort.Slice(vals, func(i, j int) bool { return vals[i].pct < vals[j].pct })
			bucket := make(map[string]float64, len(vals))
			last := len(vals) - 1
			for i, v := range vals {
				bucket[v.name] = float64(i) / float64(last) * 100.0
			}
			p.sectorQuantile[day] = bucket
		}
		if len(p.sectorQuantile) == 0 {
			p.missing = append(p.missing, "sector_flow:empty")
		}
	} else {
		p.missing = append(p.missing, "sector_flow:"+err.Error())
	}

	// ③ 基准指数涨跌幅：与载体同序尝试，第一只有数的就用它，并记下是谁供的数。
	back := shiftCompactDays(start, -panelIndexBackfillDays)
	for _, code := range indexPriority {
		bars, err := db.IndexBars(code, back, end)
		if err != nil || len(bars) < 2 {
			continue
		}
		filled := 0
		for i := 1; i < len(bars); i++ {
			prev, cur := bars[i-1].Close, bars[i].Close
			if !(prev > 0) || math.IsNaN(cur) {
				continue
			}
			p.indexPct[bars[i].Date] = (cur/prev - 1.0) * 100.0
			filled++
		}
		if filled > 0 {
			p.indexSource = code
			break
		}
	}
	if p.indexSource == "" {
		p.missing = append(p.missing, "index_daily")
	}
	return p
}

// D1On 取某票某日的 D1 分。没有读数返回 (NaN, false)——绝不返回 0：
// 0 在带符号 D1 里是"无事件"，与"这票今天没被评过"是两件事，
// 而 `scoreRule` 对 D1Event 的判据是 `<=0 弃权`，两种 0 走的是同一支。
func (p *Panels) D1On(day, code string) (float64, bool) {
	if p == nil {
		return math.NaN(), false
	}
	bucket, ok := p.d1ByDay[cntime.CompactDay(day)]
	if !ok {
		return math.NaN(), false
	}
	v, ok := bucket[store.PureCode(code)]
	if !ok {
		return math.NaN(), false
	}
	return v, true
}

// IndexChangeOn 取某日基准指数涨跌幅（%）。
func (p *Panels) IndexChangeOn(day string) (float64, bool) {
	if p == nil {
		return math.NaN(), false
	}
	v, ok := p.indexPct[cntime.CompactDay(day)]
	if !ok || math.IsNaN(v) {
		return math.NaN(), false
	}
	return v, true
}

// SectorQuantileOn 取某日某板块的净流入率横截面分位（0..100）。
// 板块名空串/当日没这张表都返回 false（连接键没命中 ≠ 分位为 0）。
func (p *Panels) SectorQuantileOn(day, sector string) (float64, bool) {
	if p == nil || sector == "" {
		return math.NaN(), false
	}
	bucket, ok := p.sectorQuantile[cntime.CompactDay(day)]
	if !ok {
		return math.NaN(), false
	}
	v, ok := bucket[sector]
	if !ok {
		return math.NaN(), false
	}
	return v, true
}

// Missing 返回没装上的面板清单（副本，调用方不会改到内部状态）。
func (p *Panels) Missing() []string {
	if p == nil {
		return []string{"panels_nil"}
	}
	out := make([]string, len(p.missing))
	copy(out, p.missing)
	return out
}

// IndexSource 供数的基准指数代码（空＝没有指数腿）。
func (p *Panels) IndexSource() string {
	if p == nil {
		return ""
	}
	return p.indexSource
}

// D1DayCount 面板覆盖的 D1 日期数（报告头用它写清"D1 只有 N 天，别外推"）。
func (p *Panels) D1DayCount() int {
	if p == nil {
		return 0
	}
	return p.d1Days
}

// SectorQuantileDayCount 有板块分位的交易日数。
func (p *Panels) SectorQuantileDayCount() int {
	if p == nil {
		return 0
	}
	return len(p.sectorQuantile)
}

// shiftCompactDays 紧凑日期加减自然日（形状认不出就原样返回，交给下游的形状校验去拒）。
func shiftCompactDays(compact string, days int) string {
	t, err := time.ParseInLocation("20060102", cntime.CompactDay(compact), cntime.Loc)
	if err != nil {
		return compact
	}
	return cntime.DayCompactOf(t.AddDate(0, 0, days))
}
