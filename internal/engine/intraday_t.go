// intraday_t.go — §DYN-T 做 T 的 engine 侧驱动（2026-10-26 v4 重写：日内口径 + 全部读数注入）。
//
// 分层不变：本文件只做"解析账号配置 → 组装 paper.TParams（含回调）→ 在交易时段内把控制权交给纸面引擎"。
// 编排与记账全在 `internal/paper/t_rounds.go`（回转状态必须与持仓、层账、残量意图同锁同一次原子写）。
//
// v4 相对 W6/W7 版本多出来的三件事，都是因为"日内口径"这套判定链需要**实时读数**：
//  1. 分时均线的分钟链校验腿（60s TTL 缓存，绝不为全持仓每 5 秒打外网）；
//  2. 信号新鲜度与一次性消费账本（候选 B 要求"比上一次该码回转结束更新"，尾盘回补要求"开腿之后仍活跃"）；
//  3. 否决门需要的止盈/止损线距离与流动性读数（上市天数、是否 ST）。
//
// 回调取不到读数时统一返回"不可得"，由 paper 侧按 fail-closed 处理——**绝不在这层伪造默认值**，
// 那等于把"看不清水位"伪装成"水位安全"（本仓反复锤实的撒谎形态）。
//
// English: engine-side driver for §DYN-T. It resolves config, wires the read-only callbacks the
// intraday gate needs (minute-basis cross-check, signal freshness/consumption, stop-line distance,
// liquidity metadata) and hands control to the paper book. Unavailable readings are reported as
// unavailable, never fabricated.
package engine

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/paper"
	"quant-trading-v2/internal/signalctl"
)

// tMinuteTTLOf 分时均线校验腿的缓存时长。
// 为什么是 60 秒：做 T 驱动约 5 秒一轮，逐轮取分钟线会把外网打成 12 次/分钟/票；
// 分钟线本身一分钟才多一根，60 秒 TTL 与它的数据新鲜度天然对齐。
const tMinuteTTLOf = 60 * time.Second

// tSignalLedger 买入信号消费账本（§8.4.3 ①）：
// 一个 signal_id 至多产生**一条买入腿**——战略先判、T 后消费，T 消费过的战略也不得再加买。
// 刻意只记 ID 不记信号内容：它是"这轮判断有没有被花掉"的账，不是行情缓存。
type tSignalLedger struct {
	mu        sync.Mutex
	latest    map[string]tSigStamp // 每码最近一条买入信号
	byT       map[string]bool      // 已被 T 腿消费
	byStrat   map[string]bool      // 已被战略买单消费
	consumedN int
}

type tSigStamp struct {
	ID string
	At time.Time
}

func newTSigLedger() *tSignalLedger {
	return &tSignalLedger{latest: map[string]tSigStamp{}, byT: map[string]bool{}, byStrat: map[string]bool{}}
}

// tMinuteEntry 分钟链复算的分时均线缓存。
type tMinuteEntry struct {
	v    float64
	at   time.Time
	okAt time.Time
}

// tRecordBuySignals 每轮把打分/近实时通道给出的买入信号记进账本（候选 B 的新鲜度来源）。
// 为什么在这里记而不在 paper 侧：paper 包不读信号库（纯核算分工），而"哪些信号此刻活着"是引擎事实。
func (e *Engine) tRecordBuySignals(sigs []combat_agent.Signal) {
	if e.tSignals == nil {
		return
	}
	now := time.Now()
	e.tSignals.mu.Lock()
	defer e.tSignals.mu.Unlock()
	for _, s := range sigs {
		// 只记买入方向的信号：候选 B 回答的是"这只票此刻有没有新的买入理由"，
		// 卖出/做空信号进账本只会让做 T 的买腿误把"离场指令"当成入场机会。
		if s.Action != "buy" && s.Direction != "做多" {
			continue
		}
		// 无 ID 的信号不进消费账本：幂等键缺失时"这条有没有被花掉"无法回答，
		// 宁可为 T 腿判成"没有新鲜信号"（少做一轮），也不能让它凭一条追不到的信号开腿。
		id := strings.TrimSpace(s.ID)
		if id == "" {
			continue
		}
		at := s.GeneratedAt
		if at.IsZero() {
			at = now // 打分链路偶发不盖时间戳：以本轮记录时刻兜底，保证账本内可比较
		}
		// 每码只保留最新一条：账本是"此刻活着的机会"，不是历史流水（流水在 signals 存储里）。
		cur, has := e.tSignals.latest[s.Code]
		if !has || at.After(cur.At) {
			e.tSignals.latest[s.Code] = tSigStamp{ID: id, At: at}
		}
	}
}

// tMarkStrategicConsumed 战略买单实际下单后标记信号已被消费（与 T 腿共用同一本账，双向拦）。
func (e *Engine) tMarkStrategicConsumed(signalID string) {
	if e.tSignals == nil || strings.TrimSpace(signalID) == "" {
		return
	}
	e.tSignals.mu.Lock()
	e.tSignals.byStrat[signalID] = true
	e.tSignals.consumedN++
	e.tSignals.mu.Unlock()
}

// tStrategicConsumed 该信号是否已被战略侧花掉。
func (e *Engine) tStrategicConsumed(signalID string) bool {
	if e.tSignals == nil {
		return false
	}
	e.tSignals.mu.Lock()
	defer e.tSignals.mu.Unlock()
	return e.tSignals.byStrat[signalID]
}

// tMarkTConsumed / tTConsumed：T 腿消费面（paper 通过回调写、engine 持有账本）。
func (e *Engine) tMarkTConsumed(signalID string) {
	if e.tSignals == nil || strings.TrimSpace(signalID) == "" {
		return
	}
	e.tSignals.mu.Lock()
	e.tSignals.byT[signalID] = true
	e.tSignals.consumedN++
	e.tSignals.mu.Unlock()
}

func (e *Engine) tTConsumed(signalID string) bool {
	if e.tSignals == nil {
		return false
	}
	e.tSignals.mu.Lock()
	defer e.tSignals.mu.Unlock()
	return e.tSignals.byT[signalID]
}

// tFreshBuySignal 候选 B 判据：该码是否存在"晚于 since"的活跃买入信号，且尚未被任何人消费。
// since 由 paper 侧给出（＝上一次该码回转结束时刻；当日无回转＝今天 09:30），
// 这样"持仓票一直挂着的那条持有中信号"不会再被当成新机会——那正是候选 A 的漏洞。
func (e *Engine) tFreshBuySignal(code string, since time.Time) (string, bool) {
	if e.tSignals == nil {
		return "", false
	}
	e.tSignals.mu.Lock()
	st, has := e.tSignals.latest[code]
	consumedByT := has && e.tSignals.byT[st.ID]
	consumedByStrat := has && e.tSignals.byStrat[st.ID]
	e.tSignals.mu.Unlock()
	if !has || st.At.Before(since) || st.At.Equal(since) {
		return "", false
	}
	if consumedByT || consumedByStrat {
		return "", false
	}
	return st.ID, true
}

// tActiveBuySignal 尾盘回补/转换用：开腿时刻之后的活跃买入信号（同样要求未被消费）。
// 与候选 B 的区别只在 since——开腿时刻 vs 上一次回转结束时刻（§8.5.4 两个语境不可混用）。
func (e *Engine) tActiveBuySignal(code string, since time.Time) (string, bool) {
	return e.tFreshBuySignal(code, since)
}

// tMinuteVWAP 分钟链复算的分时均线（Σ成交额/Σ成交量，当日 1 分钟根）。
// 只作为主源的**校验腿**：两者不一致说明至少一个读数不可信，paper 侧据此本轮不开腿；
// 分钟链自己取不到时只降级、不拦（它不该成为日内操作的新单点依赖）。
// 60s TTL 缓存：同一票在缓存过期前只打一次外网，避免 5s 循环把行情源打爆。
func (e *Engine) tMinuteVWAP(code string) (float64, bool) {
	e.mu.RLock()
	dc, cache := e.coordinator, e.tMinuteCache
	e.mu.RUnlock()
	if dc == nil || cache == nil {
		return 0, false
	}
	return cache.fetch(dc, code, time.Now())
}

// tMinuteCache 分钟均线缓存（按代码，TTL 60s）。
type tMinuteCache struct {
	mu  sync.Mutex
	ttl time.Duration
	m   map[string]tMinuteEntry
}

func newTMinuteCache() *tMinuteCache {
	return &tMinuteCache{ttl: tMinuteTTLOf, m: map[string]tMinuteEntry{}}
}

// fetch 取值；ok=false 表示"取不到"（外网失败/根数不足/当日无成交），调用方按降级处理，不当 0 用。
func (c *tMinuteCache) fetch(dc *data.DataCoordinator, code string, now time.Time) (float64, bool) {
	if e, has := c.m[code]; has && now.Sub(e.at) < c.ttl {
		return e.v, e.v > 0
	}
	bars, err := dc.GetMinuteKLine(code, 1, 240)
	if err != nil || len(bars) < 5 {
		// 根数不足（早盘/停牌/新上市）也缓存，避免每 60 秒重试打爆上游；值 0 会被读成"不可得"。
		c.m[code] = tMinuteEntry{v: 0, at: now}
		return 0, false
	}
	today := cntime.DayOf(now)
	var amount, vol float64
	for _, b := range bars {
		if cntime.DayOf(b.Date) != today {
			continue // 只累计**当日**分钟根：分时均线的定义就是开盘以来累计
		}
		amount += b.Amount
		vol += b.Volume
	}
	v := 0.0
	if vol > 0 && amount > 0 {
		v = amount / vol
	}
	c.m[code] = tMinuteEntry{v: v, at: now}
	return v, v > 0
}

// tStockMetaOf 流动性前置的静态读数：上市天数（stocks.list_date）与是否 ST（名称判定）。
// 查不到 list_date 时 ok=false，paper 侧按"读数不可得→不开 T"处理（不是按"很老"放行）。
func (e *Engine) tStockMetaOf(code string) (days int, isST bool, ok bool) {
	e.mu.RLock()
	db := e.realStore
	e.mu.RUnlock()
	name := ""
	e.mu.RLock()
	mkt := e.marketAPI
	e.mu.RUnlock()
	if mkt != nil {
		if q, err := mkt.GetRealtimeQuote(pureTsCode(code)); err == nil && q != nil {
			name = q.Name
		}
	}
	isST = strings.Contains(strings.ToUpper(name), "ST")
	if db == nil {
		return 0, isST, false
	}
	listDate, err := db.StockListDate(pureTsCode(code))
	if err != nil || strings.TrimSpace(listDate) == "" {
		return 0, isST, false
	}
	t, perr := time.ParseInLocation("20060102", listDate, time.Local)
	if perr != nil {
		return 0, isST, false
	}
	d := int(time.Now().Sub(t).Hours() / 24)
	if d < 0 {
		d = 0
	}
	return d, isST, true
}

// tStopLineDistanceOf 现价距止盈线/止损线的最小距离%（反 T 否决门，owner：止盈止损优先于做 T）。
// 基准用**锚点价**（与切硬后的判线同一分母），锚点不可得时 ok=false 让 paper 侧按否决处理——
// 拿成本价凑数会正中本批要修的那个洞（T 腿摊薄成本 → 判线漂移）。
func (e *Engine) tStopLineDistanceOf(pe *paper.Engine, uid, code string, price float64) (float64, bool) {
	if pe == nil || price <= 0 {
		return 0, false
	}
	var anchor float64
	for _, p := range pe.Positions() {
		if p.Code == code {
			anchor = p.AnchorPrice
			break
		}
	}
	if anchor <= 0 {
		return 0, false
	}
	slPct := e.disciplineStopLossPct(uid)
	tpPct := e.disciplineTakeProfitPct(uid)
	if slPct <= 0 && tpPct <= 0 {
		return 0, false
	}
	best := -1.0
	if slPct > 0 {
		d := (price - anchor*(1-slPct/100)) / price * 100 // 现价高出止损线多少
		if d < 0 {
			d = 0 // 已经破线：距离 0，否决门必然命中（该由裁决处理，不是由 T 处理）
		}
		best = dMin(best, d)
	}
	if tpPct > 0 {
		d := (anchor*(1+tpPct/100) - price) / price * 100
		if d < 0 {
			d = 0
		}
		best = dMin(best, d)
	}
	if best < 0 {
		return 0, false
	}
	return best, true
}

// dMin 取"两个距离里更小的那个"，初值 -1 表示还没算过（0 是合法距离：已经贴着线）。
func dMin(cur, cand float64) float64 {
	if cur < 0 || cand < cur {
		return cand
	}
	return cur
}

// tRecordSellVerdicts 记录本轮"真有处置裁定"的代码（反 T 否决门的唯一数据源）。
// 只记有 Disposal 的：预警/观察卡不构成"该由信号来卖"，不该长期锁死日内高抛。
func (e *Engine) tRecordSellVerdicts(verdicts []sellRoundVerdict, now time.Time) {
	e.mu.Lock()
	if e.tSellVerdicts == nil {
		e.tSellVerdicts = map[string]time.Time{}
	}
	for _, v := range verdicts {
		if v.Verdict.Disposal == nil {
			continue
		}
		e.tSellVerdicts[pureTsCode(v.TsCode)] = now
	}
	e.mu.Unlock()
}

// tSellVerdictActive 该码本轮是否有活跃卖出/退出裁决（有 ⇒ 反 T 让位给信号执行）。
func (e *Engine) tSellVerdictActive(code string) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.tSellVerdicts == nil {
		return false
	}
	at, has := e.tSellVerdicts[pureTsCode(code)]
	if !has {
		return false
	}
	// 只认 5 分钟内的裁决：更旧的裁决要么已执行、要么已过期，不该长期锁死反 T。
	return time.Since(at) < 5*time.Minute
}

// tParamsFor 把账号级 TConfig 解析成纸面引擎的执行参数（§8.8 全部键走访问器＝带区间回落）。
// 第二个返回值是本轮定权上下文（MS/盲区），由调用方顺手喂给 paper 的目标仓位模型，
// 省掉"同一轮算两次 MarketScale"的漂移风险。
func (e *Engine) tParamsFor(uid string, tc config.TConfig, shadow bool) (paper.TParams, float64, bool) {
	r := e.regimeForAccount(uid)
	// 市场组读数与个股无关：用零值信号只求市场组（个股因子全部弃权，不污染 MS）。
	res := signalctl.ComputeSizing(r, combat_agent.Signal{})
	ms := res.MarketScale
	if ms <= 0 {
		ms = 1.0
	}
	p := paper.TParams{
		Enabled:                 tc.TEnabled(),
		BuyFirst:                tc.TBuyFirst(),
		SellFirst:               tc.TSellFirst(),
		MaxRounds:               tc.TMaxRounds(),
		CapPct:                  tc.TIntradayCapPct(),
		MinSpreadPct:            tc.TMinSpreadPct(),
		ForceUnwindHM:           tc.TForceUnwindHM(),
		MarketScale:             ms,
		MissingKeyInput:         res.MissingKeyInput,
		Shadow:                  shadow,
		IndexChangePct:          r.IndexChangePct,
		FeeBufferPct:            0.6,
		VWAPDevPct:              tc.VWAPDevPctOf(),
		MinAmplitudePct:         tc.TMinAmplitudePctOf(),
		NearExtremePct:          tc.TNearExtremePctOf(),
		LimitDistancePct:        tc.TLimitDistancePctOf(),
		OpenCoolingMin:          tc.TOpenCoolingMinOf(),
		LastOpenHM:              tc.TLastOpenHM(),
		VWAPCheckTolPct:         tc.TVWAPCheckTolPctOf(),
		MinMarketScale:          tc.TMinMarketScaleOf(),
		SellEnvMinMarketScale:   tc.TSellEnvMinMarketScaleOf(),
		MarketPlungePct:         tc.TMarketPlungePctOf(),
		IntradaySellCapTotalPct: tc.TIntradaySellCapTotalPctOf(),
		StopGuardPct:            tc.TStopGuardPctOf(),
		LossBreakerPct:          tc.TLossBreakerPctOf(),
		MinAmountWan:            tc.TMinAmountWanOf(),
		MinTurnoverPct:          tc.TMinTurnoverPctOf(),
		MinListedDays:           tc.TMinListedDaysOf(),
	}
	// §8.8 三条硬纪律里的第③条：**回落后的实际生效值必须出声**。
	// 越界值被 tClamp 挡掉是保护，但"保护悄悄生效"就是另一个缺陷——运维会一直以为自己配的那个数在跑
	// （本仓历史上"配置写错被当成网络抖动"的亏吃过多次）。这里每轮排空一次队列并逐键打出来；
	// 每轮而不是每日一次，是因为这一行的用途是**事后回答"那一刻引擎按多少在跑"**，去重反而丢证据。
	// English: drain and log every threshold that fell back to its safe default this round — silent
	// protection is the failure mode this repo has been bitten by repeatedly.
	if fb := tc.TFallbacks(); len(fb) > 0 {
		keys := make([]string, 0, len(fb))
		for k := range fb {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s→%.4g", k, fb[k]))
		}
		log.Printf("[t-config-fallback] 账号=%s 本轮越界/非法键已回落生效值：%s", uid, strings.Join(parts, " "))
	}
	return p, ms, res.MissingKeyInput
}

// TParamsForAccount 供纸面通道与只读接口复用（避免两处各写一遍阈值解析）。
func (e *Engine) TParamsForAccount(uid string) (paper.TParams, float64, bool) {
	if uid == "" {
		uid = e.primaryMember()
	}
	sc := e.sizingConfigFor(uid)
	return e.tParamsFor(uid, sc.T, sc.ShadowOn())
}

// runIntradayT 做 T 的每轮驱动（由纸面估值轮在交易时段内调用）。
//
// 前置在**拨引擎之前**判完：开关没开就一次配置都不往下传。`IsActiveSession` 是第二道——
// 集合竞价与盘后都不做 T：竞价阶段没有可靠的连续价，盘后没有对手盘。
// 第三道是 v4 新增的"读数缺失即整体不做"：影子期 / 关键市场输入缺失时，目标仓位本身不可信，
// 在盲区里做日内双向操作比不做贵得多（§8.13 C 组第 10/11 条，owner 已同意）。
func (e *Engine) runIntradayT(uid string, pe *paper.Engine, quotes map[string]*data.StockInfo) {
	if pe == nil {
		return
	}
	if uid == "" {
		uid = e.primaryMember()
	}
	sc := e.sizingConfigFor(uid)
	tc := sc.T
	if !tc.TEnabled() {
		return
	}
	if !data.IsActiveSession(time.Now()) {
		return
	}
	p, ms, missing := e.tParamsFor(uid, tc, sc.ShadowOn())
	// 顺手把定权上下文喂给纸面目标模型：一笔买入与一条 T 腿必须吃同一个 MS，
	// 分头计算会让"目标 70%、单笔却按 85% 算"这类漂移无声发生。
	perTrade := sc.PerTradePositionPct
	if perTrade <= 0 || perTrade > 1 {
		perTrade = 0.10
	}
	ceiling := sc.MaxTotalPositionPct
	if ceiling <= 0 || ceiling > 1 {
		ceiling = 1.0
	}
	pe.SetSizingContext(ms, perTrade, ceiling, missing)
	e.wireTCallbacks(uid, pe, &p)
	pe.RunIntradayT(quotes, p)
}

// wireTCallbacks 把读数回调接到引擎的真实数据源上（全部"取不到就说取不到"）。
func (e *Engine) wireTCallbacks(uid string, pe *paper.Engine, p *paper.TParams) {
	p.MinuteBasis = func(code string) (float64, bool) { return e.tMinuteVWAP(code) }
	p.NewBuySignal = func(code string, since time.Time) (string, bool) { return e.tFreshBuySignal(code, since) }
	p.ActiveBuySignal = func(code string, since time.Time) (string, bool) { return e.tActiveBuySignal(code, since) }
	p.ActiveSellVerdict = func(code string) bool { return e.tSellVerdictActive(code) }
	p.StopLineDistance = func(code string, price float64) (float64, bool) {
		return e.tStopLineDistanceOf(pe, uid, code, price)
	}
	p.StockMeta = e.tStockMetaOf
	p.StrategicBusy = func(code string) bool {
		e.mu.RLock()
		defer e.mu.RUnlock()
		for k := range e.liveDecisions {
			if strings.HasPrefix(k, pureTsCode(code)+"@") {
				return true // 该码有未执行的战略裁定 ⇒ T 让路（§8.7）
			}
		}
		return false
	}
	p.StrategicGate = func(code string, qty int) (int, string) {
		// 转战略／尾盘回补都走战略同一条预算通道（§8.5.3）：单笔基准、目标剩余空间、
		// 行业/单票 35% 一个都不能少，"改个状态"不算过闸门。
		pool, price := pe.PositionPoolAndMark(code)
		allowed, reason := pe.StrategicBuyRoom(pool, code, price)
		if allowed > qty {
			allowed = qty
		}
		if allowed <= 0 {
			return 0, "战略闸门不放行：" + reason
		}
		return allowed, reason
	}
	p.ConsumeSignal = func(id string) { e.tMarkTConsumed(id) }
	p.SignalConsumedByStrategic = func(id string) bool { return e.tStrategicConsumed(id) }
}
