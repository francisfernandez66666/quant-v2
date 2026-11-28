// series_input.go — §REPLAY-INPUTS 2026-11-28（档 1）：回放侧"整段序列一次装配"的接入口。
//
// 解决的问题（方案 §1.1/§1.2，现场成因）：
//
//	回放原来的形状是"逐日 × 逐规则"调 `FactorStrategy.Evaluate(md{KLines,Fina,D1})`，
//	每次调用内部跑一遍 `seriesFromKLines`。这带来两个后果：
//	  ① 同一只票的序列被重算几百次（纯浪费，900 天 × 37 条规则）；
//	  ② 更要命的是 **StockMarketData 里根本没有情绪/涨停微结构/市场级标量这些列**，
//	     所以 fac_172/173/174/176/177/178 这批规则在回放里只会静默 decline——
//	     读数上没有任何异常，看起来就像"这策略今天没信号"。
//
// 本文件把输入换成 `research.AssembleWithPanels`（整段一次装配，含市场级八列），
// 逐日只做一个 as-of 索引查找，打分仍走**与实盘同一个** `scoreRule`。
//
// ★ 内存取向（这一条决定本方案在 2 核 4G 上跑得起来）：
//   v2 方案写的是"缓存 map[string]*StockSeries、key 仅 ts_code"，全回放共用一份。
//   实测算账：单票 900 交易日 × 38 列 × 8B ≈ 267 KB，加日期串约 330 KB；
//   时点池 4309 只 ⇒ **约 1.4 GB**，而 researchd 的配额是 CPUQuota=60% / MemoryHigh=500M。
//   按票累积缓存等于把回放进程自己 OOM 掉。
//   正确形状是**只持有当前这一只票**：换票时丢弃上一份。所以本文件是
//   "一票一份 + 换票重建"，而不是"全池一张大表"。装配次数仍是池大小（4309 次），
//   不是 390 万次——v2 review #1 想省的那个数量级保住了，内存没爆。
//
// English: per-stock (not per-universe) series assembly — a full-pool cache would need ~1.4 GB
// against a 500 MB quota; holding only the current stock keeps the assembly count at pool size
// while staying inside memory.

package btreplay

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"quant-trading-v2/internal/cntime"
	factorlib "quant-trading-v2/internal/factor"
	"quant-trading-v2/internal/research"
	"quant-trading-v2/internal/scheduler"
	"quant-trading-v2/internal/store"
)

// seriesSource 当前股票的研究序列（一次装配、全池适配器共用一份）。
//
// 生命周期：`useStock(code)` 换票 ⇒ 丢弃上一份并重新装配；
// 37 条因子规则适配器在同一天里读的是**同一个对象**，所以一只票只装配一次。
type seriesSource struct {
	db     *store.DB
	panels *research.Panels
	start  string // 紧凑 YYYYMMDD（Options.Start 是横线形态，库里是紧凑，这里归一一次）
	end    string
	dbPath string // 库路径（只用于反推数据目录，读采集完成标记）

	// 个股资金流"这份长期数据现在到哪了"的四枚读数（§5.4 报告头一行）。
	// 在 newSeriesSource 里读一次：全池共用一份，逐票重查等于 4309 次相同的全表 MAX/COUNT。
	//   flowDay      全表最新数据日（紧凑 YYYYMMDD；空表＝""）
	//   flowCodes    该日有行的票数（覆盖率分子）
	//   flowCoverage 分母用 CountCollectableCodes（非北交所全集），与 /api/status.flow 同一把尺
	//   flowGate     夜间链这道采集闸此刻的结论（pass/wait/timeout，公式只在 scheduler 一处）
	flowDay      string
	flowCodes    int
	flowCoverage float64
	flowGate     string

	code   string
	series *factorlib.StockSeries

	// 读数（全部进报告头，不静默）：
	//   assemblies 真正装配过几次（应≈池大小）；
	//   assembleFallback 装配失败次数 ⇒ 这些票回落到旧 K 线路径（行为与升级前一致，但输入面少了情绪腿）
	//   hits        走整段路径的判定次数（与 Trigger 调用次数同量级）
	assemblies       int
	assembleFallback int
	hits             int
	skips            int // 序列里没有该判定日（停牌/区间外）⇒ 回落旧路径
}

// newSeriesSource 装配回放侧的序列来源。db 为 nil 直接返回 nil——
// 所有使用点都判空并回落旧路径，"没装配"与"装配了但读不到"在读数上分开。
func newSeriesSource(db *store.DB, start, end, dbPath string) *seriesSource {
	if db == nil {
		return nil
	}
	s := &seriesSource{
		db:     db,
		start:  cntime.CompactDay(start),
		end:    cntime.CompactDay(end),
		dbPath: dbPath,
	}
	// 市场级面板一次读（D1 整表 / 板块净流入分位 / 基准指数涨跌幅）。
	// 面板读不到不是错误：对应列整列 NaN，相关因子腿按弃权计入，Missing 进报告头点名。
	s.panels = research.LoadPanels(db, s.start, s.end)
	s.loadStockFlowReadout()
	return s
}

// loadStockFlowReadout 读一次个股资金流的"到哪了 + 闸是什么结论"（§5.4）。
//
// 三条边界：
//  1. **读不到就留空**，不写 0——0 只票会被读成"采集到了但一只都没有"，与"没采过"是两件事；
//  2. 覆盖率分母与 `/api/status.flow` 同源（`CountCollectableCodes`），
//     展示层不许自己再数一遍全集，否则两处读数会漂；
//  3. 闸结论转调 `scheduler.MoneyflowGateReport`，**不在这里写第二份**"几点算超时"。
func (s *seriesSource) loadStockFlowReadout() {
	day, err := s.db.MaxStockFlowDay()
	if err != nil || day == "" {
		return
	}
	s.flowDay = day
	if n, cerr := s.db.CountStockFlowCodes(day); cerr == nil {
		s.flowCodes = n
	}
	if total, terr := s.db.CountCollectableCodes(); terr == nil && total > 0 {
		s.flowCoverage = float64(s.flowCodes) / float64(total) * 100.0
	}
	s.flowGate = scheduler.MoneyflowGateReport(filepath.Dir(s.dbPath), cntime.Now())
}

// useStock 换到某只票：与当前不同才重新装配（同一只票跨 900 天只装一次）。
// 返回该票的序列；装配失败返回 nil，调用方回落旧路径并计入 assembleFallback。
//
// "同一票不重复装配"的判据只有 `s.code == code` 一条：失败时也已经把 code 记下了，
// 所以下一次进来直接返回 nil——**不能**拿"全局失败计数 > 0"当缓存命中条件，
// 那是把上一只票的失败算到当前票头上（表现：装配过一只坏票之后，好票也不再尝试装配）。
func (s *seriesSource) useStock(code string) *factorlib.StockSeries {
	if s == nil || code == "" {
		return nil
	}
	if s.code == code {
		return s.series
	}
	s.code = code
	s.series = nil
	ser, err := research.AssembleWithPanels(s.db, code, s.start, s.end, s.panels)
	s.assemblies++
	if err != nil || ser == nil || ser.Len() == 0 {
		s.assembleFallback++
		return nil
	}
	s.series = ser
	return ser
}

// asIndexOf 把判定日（K 线那天）映射成序列里的索引；找不到返回 -1。
//
// 入参是 time.Time：回放侧的 data.KLine.Date 是时间类型，而库里 trade_date 是紧凑串——
// 两者必须经 cntime（北京时区）换算成同一把尺再比，直接 fmt.Sprint 会在跨零点上错一天。
//
// 用二分而不是线性扫：单日一次调用 × 37 规则 × 4309 票，线性扫会白白多烧几千万次比较。
// 找不到是合法形态（该股当日停牌 / 序列区间比 K 线区间窄），调用方按"回落旧路径"处理并计入 skips，
// **绝不许退化成"用最后一根"**——那是拿区间末尾的读数给中间某一天打分（前视）。
func (s *seriesSource) asIndexOf(day time.Time) int {
	if s == nil || s.series == nil {
		return -1
	}
	if day.IsZero() {
		return -1
	}
	target := cntime.DayCompactOf(day)
	dates := s.series.Dates
	i := sort.Search(len(dates), func(k int) bool {
		return cntime.CompactDay(dates[k]) >= target
	})
	if i >= len(dates) || cntime.CompactDay(dates[i]) != target {
		return -1
	}
	return i
}

// String 报告头读数行（与 finaSrc.String()/poolNote 同一姿势：数字与前提同屏出门）。
func (s *seriesSource) String() string {
	if s == nil {
		return "序列装配=未启用（回落下单前逐日重算，情绪/市场级腿不参与）"
	}
	misses := "<无面板>"
	if s.panels != nil {
		misses = fmt.Sprint(s.panels.Missing())
	}
	return "序列装配=整段一次 票数=" + itoa(s.assemblies) +
		" assemble_fallback=" + itoa(s.assembleFallback) +
		" 判定命中=" + itoa(s.hits) +
		" 无该日=" + itoa(s.skips) +
		" 面板缺=" + misses
}

// itoa 小整数转字符串（避免为了一个日志行把 strconv 引进来再引出去）。
func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// marketNote 报告头一行：把"本轮市场级输入的前提"随数字一起出门。
//
// 每一项都对应一个"只有读源码才知道"的事实，而三个月后看审批表的人不会去读源码：
//
//	flow_quantile_src=sector_flow_proxy → fac_176/177 吃的"热度"其实是板块净流入分位，
//	  不是盒子上那个利好池 LLM 热度；
//	leader_src=sector_gain_rank → 同理，是连续分位而不是盒上的布尔龙头；
//	band_calibrated=provisional(...) → 这两档的尺度是从"载体一个键混两种量纲"的时代
//	  照抄来的，等首采实测分布重标并经 owner 签字（§10 裁决 1）；
//	flow_mode=seed120+daily → 资金流历史起点就是首采日，不外推到 2023。
func (s *seriesSource) marketNote() string {
	if s == nil {
		return "市场输入=未启用（情绪/资金流/指数/龙头腿一律弃权，复合分只由价量与财务构成）"
	}
	d1Days, idxSrc := 0, "-"
	// 未标定名单从因子层取，不在这里再抄一遍 ID 清单（两份名单必然漂）。
	sectorDays, miss := 0, "[]"
	backfill := int64(0)
	if s.panels != nil {
		d1Days = s.panels.D1DayCount()
		if v := s.panels.IndexSource(); v != "" {
			idxSrc = v
		}
		sectorDays = s.panels.SectorQuantileDayCount()
		backfill = s.panels.SectorBackfillCells()
		miss = fmt.Sprint(s.panels.Missing())
	}
	return fmt.Sprintf(
		"市场输入=整段装配 d1_days=%d index_src=%s flow_quantile_src=sector_flow_proxy(days=%d) "+
			"leader_src=sector_gain_rank flow_mode=seed120+daily(缺则弃权) "+
			"band_calibrated=provisional(%s) "+
			"assemble_fallback=%d panel_missing=%s "+
			"sector_map_backfill=%d(归属按 as-of 固化快照；>0 表示借用了未来快照的格数) "+
			"stock_flow day=%s codes=%d coverage=%.1f%% gate=%s",
		d1Days, idxSrc, sectorDays,
		strings.Join(factorlib.ProvisionalBandKeys(), ","), s.assembleFallback, miss,
		backfill,
		s.flowDayText(), s.flowCodes, s.flowCoverage, s.flowGateText())
}

// flowDayText 最新资金流数据日；空表如实写 none（不写 0，也不写"今天"）。
func (s *seriesSource) flowDayText() string {
	if s == nil || s.flowDay == "" {
		return "none"
	}
	return s.flowDay
}

// flowGateText 采集闸结论；未启用序列装配（s==nil）时同样如实写 none，
// 不拿 pass 顶班——"没这道闸"与"这道闸放行了"在审批表上不是一件事。
func (s *seriesSource) flowGateText() string {
	if s == nil || s.flowGate == "" {
		return "none"
	}
	return s.flowGate
}

// seriesScoped 因子规则适配器的逐股注入点（与 finaScoped / minuteMACDScoped 同一手法）。
type seriesScoped interface {
	setSeriesScope(src *seriesSource, tsCode string)
}

// applySeriesScope 注入序列来源并立刻换到当前票。
//
// 与 applyFinaScope 同一个调用点集合（backtestStock / sweepTriggersOf / 复核链三处）——
// 装配点分开写迟早漏一处，漏的那处表现是"这条腿今天没参与而读数看着正常"
// （§0925EVE-W3-J(B6) 的跨股串台、§MINUTE-K 的串台都是同一个形状）。
func (o *Options) applySeriesScope(ad adapter, tsCode string) {
	inject := func(a adapter) {
		if ss, ok := a.(seriesScoped); ok {
			ss.setSeriesScope(o.seriesSrc, tsCode)
		}
	}
	inject(ad)
	if isFallbackTier(ad) {
		for _, p := range o.fallbackPeers {
			inject(p)
		}
	}
}
