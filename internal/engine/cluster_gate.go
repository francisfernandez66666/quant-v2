// cluster_gate.go — §T-COST B3（2026-10-27）相关性簇帽的引擎侧装配。
//
// 分工：`internal/cluster` 只做纯计算（Pearson / 并查集 / 簇值聚合），本文件负责三件"带 IO 的事"：
//  1. 取数：从 `daily`（后复权）读近 60 个交易日的日收益，按代码缓存**到交易日粒度**
//     （日线盘中不变，逐信号重查等于在下单路径上打数据库）；
//  2. 装配：把"持仓 ∪ 在途买单 ∪ 这一只"作为候选集，交给 cluster.Build；
//  3. 读数：算出目标代码所在簇的已用市值，连同"并簇依据"一起返回给 dynamicBuyQty。
//
// 口径（与施工图 §5.1 一致）：
//   - 收益用**后复权**收盘算（hfq）；复权基座在收益率里自然抵消，与 qfq 结果同值——
//     所以这里不必为"方案写的是 qfq"另建一条取数路径（那会变成两套复权体系并存）；
//   - 停牌日**缺席**不补 0（补 0 会把停牌算成不涨不跌，稀释相关性 ⇒ 方向偏向"更敢买"）；
//   - 重叠样本 < 40 ⇒ 相关系数不可信 ⇒ 按同簇处理（fail-closed）。
//
// 一条刻意的例外（必须在报告里点名）：**查询本身失败**（DB 错误）时整条帽**跳过**并告警，
// 不按同簇处理。理由是"读数根本没产生"与"读数说它们同簇"是两件事——前者是数据层故障，
// 把它变成"全面停止开仓"会让一次 DB 抖动伪装成一个风险决策。样本不足（读到了但太短）
// 仍然 fail-closed，那条是方案明文。
//
// English: the IO half of the correlation-cluster gate — loads 60-day hfq returns (cached per
// trading day), builds the cluster index over holdings ∪ pending ∪ the target, and returns the
// cluster's used market value. Short samples fail closed as specified; a *query failure* skips the
// cap with an alarm instead, because "no reading" is not "the reading says same cluster".
package engine

import (
	"log"
	"strings"
	"sync"
	"time"

	"quant-trading-v2/internal/cluster"
	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/opslog"
)

// clusterWindowDays 标定窗口的交易日数（方案 §5.1：近 60 个交易日）。
const clusterWindowDays = 60

// clusterCalendarSpan 取数时向前回溯的自然日数：60 个交易日 + 长假冗余 ≈ 90 天，
// 给 120 天是为了覆盖春节/国庆连续休市，窗口末尾再按 clusterWindowDays 截断。
const clusterCalendarSpan = 120

// clusterReturnCache 日收益序列的**日级**缓存（日线盘中不变，跨日整体失效）。
// 用独立的锁而不是 e.mu：这里要发 SQL，不能把引擎主锁按在磁盘 IO 上（§SIGBATCH 的教训）。
type clusterReturnCache struct {
	mu   sync.Mutex
	day  string
	ser  map[string]cluster.Series
	bad  map[string]bool // 该代码当日"查询失败"（与"没有行"区分，见文件头例外条款）
	load func(code string) (cluster.Series, bool, bool)
}

// clusterSeriesFor 取某代码当日的收益序列，返回 (序列, 是否有行, 是否查询失败)。
// 命中缓存直接返回；缓存跨日整体清空（不用逐条 TTL：日线的"新"是日级的）。
func (e *Engine) clusterSeriesFor(day, code string) (cluster.Series, bool, bool) {
	e.clusterCache.mu.Lock()
	defer e.clusterCache.mu.Unlock()
	c := &e.clusterCache
	if c.day != day {
		c.day = day
		c.ser = map[string]cluster.Series{}
		c.bad = map[string]bool{}
	}
	if s, ok := c.ser[code]; ok {
		return s, len(s) > 0, c.bad[code]
	}
	if c.bad[code] {
		return nil, false, true
	}
	load := e.clusterLoad
	if load == nil {
		load = e.loadClusterSeries
	}
	s, has, failed := load(code)
	if failed {
		c.bad[code] = true
		return nil, false, true
	}
	c.ser[code] = s
	return s, has, false
}

// loadClusterSeries 读某代码近 clusterWindowDays 个交易日的后复权日收益。
// 返回 (序列, 是否有行, 是否查询失败)；"有行但不足窗口"由调用方按样本不足处理。
func (e *Engine) loadClusterSeries(code string) (cluster.Series, bool, bool) {
	e.mu.RLock()
	st := e.realStore
	e.mu.RUnlock()
	if st == nil {
		return nil, false, true // 账本未就绪：按查询失败处理（跳过帽并告警，不冻结交易）
	}
	code = pureTsCode(code)
	end := time.Now()
	start := end.AddDate(0, 0, -clusterCalendarSpan)
	bars, err := st.HfqBars(code, cntime.DayCompactOf(start), cntime.DayCompactOf(end))
	if err != nil {
		log.Printf("[qmt] §T-CLUSTER 日线读取失败 %s（本条帽本轮跳过，不按同簇处理）：%v", code, err)
		return nil, false, true
	}
	if len(bars) < 2 {
		return nil, false, false // 没有行/只有一行：样本不足 ⇒ 交给 fail-closed
	}
	// 只保留最近 clusterWindowDays 个收益率（窗口右端＝最新交易日）。
	first := len(bars) - 1 - clusterWindowDays
	if first < 1 {
		first = 1
	}
	ser := make(cluster.Series, clusterWindowDays)
	for i := first; i < len(bars); i++ {
		prev, cur := bars[i-1].Close, bars[i].Close
		if prev <= 0 || cur <= 0 {
			continue // 脏行跳过（缺席不补 0）
		}
		ser[bars[i].Date] = cur/prev - 1
	}
	return ser, true, false
}

// clusterGateRead 一次簇帽装配的读数（供 dynamicBuyQty 落日志与门禁用例断言）。
type clusterGateRead struct {
	// Applied 是否真正参与了预算（false＝关闭/读数不可用/没有候选集）。
	Applied bool
	// Value 目标代码所在簇的已用市值（**不含**目标自身）。
	Value float64
	// ClusterID 目标代码的簇 id（簇内最小代码）。
	ClusterID string
	// Members 同簇代码（含目标，便于日志一眼看出"这簇是谁"）。
	Members []string
	// Unknown 因读数不可得被强制并簇的对数（>0 时必须点名）。
	Unknown int
	// Reason Applied=false 的原因（关闭/读数不可用/DB 故障）。
	Reason string
}

// clusterGateFor 装配目标代码的相关性簇读数（§T-COST B3）。
//
// values 传**集中度口径**的当前市值（持仓 + 在途，含 T 腿——与行业/单票帽同源：
// 簇帽防的是"敞口集中在同一注"，T 腿买来的敞口同样算数）。
// industryOf 与 dynamicBuyQty 里那个同源（测试缝优先）。
// English: assembles the cluster reading for the target code; `values` is the concentration-basis
// market value (positions + pending, T legs included) exactly like the industry/stock caps.
func (e *Engine) clusterGateFor(target string, values map[string]float64,
	industryOf func(string) string, capPct, rho float64) clusterGateRead {
	target = pureTsCode(target)
	if capPct <= 0 {
		return clusterGateRead{Reason: "cluster_cap_pct=0（关闭）"}
	}
	// 候选集：持仓/在途里出现过的代码 ∪ 目标自己。排序是为了让日志与用例可复现
	// （map 遍历顺序随机，否则同一个场景两次跑出两行不同顺序的"成员"）。
	codes := make([]string, 0, len(values)+1)
	seen := map[string]bool{}
	for c := range values {
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		codes = append(codes, c)
	}
	if !seen[target] {
		codes = append(codes, target)
	}
	if len(codes) < 2 {
		// 只有目标自己：簇里没有别的仓位，帽天然不参与（不必取数）。
		return clusterGateRead{Reason: "该簇暂无其它持仓"}
	}
	sortStrings(codes)

	day := tradingDayCompact(time.Now())
	rets := make(map[string]cluster.Series, len(codes))
	inds := make(map[string]string, len(codes))
	for _, c := range codes {
		if ind := strings.TrimSpace(industryOf(c)); ind != "" {
			inds[c] = ind
		}
		ser, has, failed := e.clusterSeriesFor(day, c)
		// 查询失败与"没有行"在这里同归"这条序列不可用"：两者都进不了 rets，
		// 差别只在"整批都不可用"那条例外条款上（见下），单只票的差别不影响判定。
		if failed || !has {
			continue
		}
		rets[c] = ser
	}
	// 例外条款：**整批代码一条收益都取不到** ⇒ 整条帽本轮跳过并告警，不按同簇处理。
	// 为什么必须留这个口子：一条序列都没有时，"按同簇处理"会把簇帽退化成"全账户总敞口帽"
	// （40%），而目标仓位模型要的是 86.5%——两者直接打架，结果是**全面停止开仓**。
	// 那不是保守，那是把数据层缺失伪装成一个风险决策（本仓反复吃亏的同一形状）。
	// 只要有一条序列可用，就回到方案的 fail-closed：缺读数的那几对按同簇处理。
	// English: if *no* series is available at all, skip the cap with an alarm. Treating every pair
	// as same-cluster would degenerate the 40% cap into a total-exposure cap and freeze all buying —
	// that is a data outage disguised as a risk decision, not conservatism.
	if len(rets) == 0 {
		opslog.DayOnce("cluster-unavailable", func() {
			opslog.Logf("quant", "§T-CLUSTER 日线读数整体不可用（%d 只全部缺序列），本条帽本轮跳过：%s",
				len(codes), strings.Join(codes, ","))
		})
		log.Printf("[qmt] §T-CLUSTER 日线读数整体不可用（%d 只全部缺序列），簇帽本轮跳过：%s",
			len(codes), strings.Join(codes, ","))
		return clusterGateRead{Reason: "日线读数整体不可用（本轮跳过，不按同簇处理）"}
	}
	idx := cluster.Build(codes, rets, inds, rho, 0)
	out := clusterGateRead{
		Applied:   true,
		Value:     cluster.ValueOf(idx, target, values),
		ClusterID: idx.ClusterOf(target),
		Unknown:   idx.UnknownPairs(),
	}
	for _, c := range codes {
		if idx.ClusterOf(c) == out.ClusterID {
			out.Members = append(out.Members, c)
		}
	}
	// 读数不可得被强制并簇时必须出声：那会把"这只票没有历史"变成"它和所有票同簇"，
	// 从而把簇帽变成"全账户总敞口帽"——方向正确但力度远超设计意图，owner 必须看得见。
	if out.Unknown > 0 {
		opslog.DayOnce("cluster-unknown", func() {
			opslog.Logf("quant", "§T-CLUSTER 有 %d 对代码因收益样本不足(<40)按同簇处理 候选集=%s",
				out.Unknown, strings.Join(codes, ","))
		})
		log.Printf("[qmt] §T-CLUSTER %d 对代码因样本不足按同簇处理（fail-closed）：候选集=%s 簇=%s 成员=%s",
			out.Unknown, strings.Join(codes, ","), out.ClusterID, strings.Join(out.Members, ","))
	}
	return out
}

// sortStrings 插入排序：候选集只有个位数元素，避免为一行排序引入 sort 依赖的额外分支。
// English: insertion sort over a handful of codes.
func sortStrings(v []string) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && v[j] < v[j-1]; j-- {
			v[j], v[j-1] = v[j-1], v[j]
		}
	}
}

// tradingDayCompact 当前交易日的紧凑日期键（簇缓存的失效粒度）。
// data.TradingDayDate 直接返回 YYYYMMDD 字符串，与 cntime.DayCompactOf 同形——
// 用它而不是自然日，避免"缓存按自然日失效、交易日没变"的重复取数。
// English: the trading-day key that drives cache invalidation (already YYYYMMDD).
func tradingDayCompact(now time.Time) string { return data.TradingDayDate(now) }
