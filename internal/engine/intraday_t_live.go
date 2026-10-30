// intraday_t_live.go — 批 8 / W8-B：实盘（真柜台）日内回转「做 T」的编排与状态机。
//
// 与纸面侧的分工（docs/FIX_PLAN_20261025_W8_LIVE_T.md §2.1）：**可复用的只有判定语义**。
// 日内口径、门槛链、配对先于新开、尾盘两态、留一手、不烧槽、不改锚点——这些作为不变量逐条搬；
// 状态、锁、对账、幂等全部独立，因为实盘腿要经"委托→回报→orders/fills 两表落库"，
// 中间任何一环掉线都会出现"回转状态说成了、委托说没成"的半态，只能靠 orders 表对账收敛。
// 钱的动作一律落在**实盘账本**（real_positions / orders / fills / 柜台可用资金）上，绝不碰纸面账本。
//
// 每轮次序（顺序即安全，§8.4.6）：
//
//	0   时段/开关/账本前置（runIntradayTLive）
//	0a  跨日收敛：昨日 open 先与 orders 对账再显式收摊（幽灵 open 必收敛，§2.3）
//	0b  当日在途腿对账：成交真相只取 fills，状态账本只是"意图"
//	1   配对/收摊（**配对先于开新腿**，同一时刻同码只允许一条腿）
//	2   开新腿判定链 step0→step9（任一步不过就留痕并结束本票本轮）
//
// 三条硬约束来自 Q13 实读而不是偏好：两腿串行、不做两腿事务（配对失败一律收摊不追买，Q8）、
// 不烧退出槽/不关生命周期/不改锚点。幂等键 `t0:<code>:<dir>:<YYYYMMDD>:<seq>`，配对腿在同键后
// 加 `:p`（撤单重报再加序号），与退出/减仓键 `sell:<码>:<类>:<日>`、战略买入键 `buy:<码>:<战法>:<日>`
// 正交——一次 T 回转不得吞掉当日真正的退出/减仓裁决。
//
// 读不到就说读不到：本文件的每个读数缺口（无价 / 分时均线不可信 / 分钟链校验不一致 / 流动性缺项 /
// 可卖量不足 / 资金口径不可得 / 跨账号在途查不到）一律按"不开腿"处置，并计入 step 统计（§8.9 的
// 可观测形状），**绝不**用 0 或默认值把"看不清水位"伪装成"水位安全"。
//
// English: the live (real-broker) intraday round-trip orchestrator. Only the decision semantics are
// shared with the paper book; state, locking, reconciliation and idempotency are independent because a
// live leg lives across orders/fills tables. Legs are strictly serial, pairing always runs before any
// new leg, half states are sealed with an explicit trail (never chased, never offset by another
// account), and every missing reading blocks the round instead of degrading to the old cost basis.
package engine

import (
	"fmt"
	"log"
	"math"
	"sort"
	"strings"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/paper"
	"quant-trading-v2/internal/store"
	"quant-trading-v2/internal/trading"
)

// 回转状态与方向的字面量集中在此：它们同时是状态文件契约、orders 归因前缀与日志口径，
// 散着写字面量等于把契约拆成若干次手滑的机会。
const (
	// tLKeyPrefix 所有 T 腿幂等键的命名空间前缀（复盘时按前缀即可从 orders/fills 捞出全部腿，§2.6）。
	tLKeyPrefix = "t0:"
	// tLPairSuffix 配对腿后缀：第一腿用回转 ID 本身，第二腿用 ID+":p"（撤单重报为 :p2、:p3…）。
	// 两腿键必须不同（W8 §3.1 锁 5），否则第二腿会被自家幂等键拦成"duplicate"、当日永远配不上。
	tLPairSuffix = ":p"

	tLDirBuy  = "buy_first"  // 正 T：先买机动仓，再卖等量底仓
	tLDirSell = "sell_first" // 反 T：先卖底仓，再买回等量（或按新口径不回补）

	tLStatusOpen     = "open"
	tLStatusDone     = "done"      // 唯一有"做 T 收益"的终态（§8.9a）
	tLStatusUnwound  = "unwound"   // 半态收摊：仓位被动变更
	tLStatusConvert  = "converted" // 尾盘不回补/闸门不放行：转战略减仓，不算 T 收益
	tLStatusAccepted = "accepted"  // 预留：本文件只写 open/done/unwound/converted

	tLMinBaseKeep = 100 // 反 T 卖腿必须留下的底仓（1 手）：卖光会走清仓/删行分支，买回腿会被自家闸门挡死

	// tLGhostGrace 幽灵 open 的判定宽限：本轮"先落账后下单"的腿在下一轮才有 orders 行，
	// 队列积压或分发器未启动时更晚；没有宽限期会把刚开出的腿误判成幽灵并当场作废。
	tLGhostGrace = 90 * time.Second

	// 费用估算口径与纸面账本同源（§8.9a：不新造费率）：佣金 max(额×0.025%, 5 元)、卖出印花税 0.05%。
	tLCommissionRate = 0.00025
	tLMinCommission  = 5.0
	tLStampTaxRate   = 0.0005

	tLTick = 0.01 // A 股个股最小变动价位（与 §C2 方案 C 同源，只买个股，不做分品种 tick 表）

	// tLPremiseGrace 腿龄门（§W-06-R4）：开腿后这么久之内不许判"本笔止损"。
	// 2 分钟的依据：覆盖一笔委托从报出到首笔回报的正常延迟，并大于幽灵宽限 tLGhostGrace(90s)。
	// 没有这道门，开腿瞬间的报价抖动 + 成交回填延迟会让止损用错价格基准（买价还是 0、或现价是上一笔）。
	tLPremiseGrace = 2 * time.Minute
	// tRegimeStale 行情读数的**新鲜度门**（§W-09）：超过这么久没更新 ⇒ 弃权，不许拿旧结论做动作。
	// 15 分钟的依据：主循环是分钟级，15 分钟等于"连续 15 轮没更新"，那已经不是抖动而是停了。
	// 出口（accelReason）比入口（regimeBlock）更依赖它——入口过期只是"照常做 T"，
	// 出口误触发是**真金白银的成交**。
	tRegimeStale = 15 * time.Minute
	// tRegimeMinVotes 研判质量的"够了"线，必须与 research 状态机的 MinVotes 一致。
	// **只用于日志分档，不做门**：票数不足时状态机已把结论降级成 range（放行档），
	// 再拦一次等于把放行又收回去（owner 2026-10-29 第 3 条裁决）。
	tRegimeMinVotes = 4

	tLLogTag = "[t-live]"
)

// tLiveReaders 一轮判定所需的外部读数注入面。
//
// 生产由 newTLiveRound 接到引擎的真实数据源上；测试替换同一批函数，
// 于是"读数不可得按不过处理"这条纪律可以被逐条证伪（缺哪个读数就让它返回 !ok）。
// 任何一个字段为 nil 的处置都是"不开腿"（fail-closed），不是"当作通过"。
type tLiveReaders struct {
	// minuteBasis 分钟链复算的分时均线（校验腿）；ok=false 只记降级、不拦（与纸面同口径）。
	minuteBasis func(code string) (vmin float64, ok bool)
	// stockMeta 上市天数与是否 ST；ok=false ⇒ 流动性前置按不过。
	stockMeta func(code string) (listedDays int, isST bool, ok bool)
	// newBuySignal 候选 B：晚于 since 且未被消费的活跃买入信号（正 T 开腿侧）。
	newBuySignal func(code string, since time.Time) (string, bool)
	// activeBuySignal 尾盘回补侧：晚于**开腿时刻**的活跃买入信号（§8.5.4 两个语境不可混用）。
	activeBuySignal func(code string, since time.Time) (string, bool)
	// activeSellVerdict 该码有活跃卖出/退出裁决 ⇒ 反 T 否决（§8.7 信号执行优先）。
	activeSellVerdict func(code string) bool
	// stopLineDistance 现价距止盈/止损线的最小距离%；ok=false 按否决处理。
	stopLineDistance func(code string, price float64) (float64, bool)
	// strategicBusy 该票有未执行的战略裁定或战略在途单 ⇒ T 让路（§8.7）。
	strategicBusy func(code string) bool
	// oppositeLeg 跨账号同码反向在途查询（§8.4.5 对手方不变量）：读不到一律按互斥命中。
	oppositeLeg func(selfUID, code, dir, day string) (blocked bool, why string)
}

// tLiveLegFact 一条腿（一个幂等键）在 fills 表里的客观成交事实。
// 成交真相只来自这里：状态账本说的是"意图"，回报才是"事实"，两者不一致时以事实为准（§8.6）。
type tLiveLegFact struct {
	qty    int
	amount float64 // Σ成交额（用于加权均价）
	fee    float64 // Σ真实回报费用（佣金+印花税，兼容旧口径）
	// comm/stamp 分项：B1 要把佣金与印花税分开记（fee_buy/fee_sell/stamp_tax）。
	// 没有分项就只能在净额里看到一个总数，无法回答"成本里最大的一项是哪个"（一个月后复盘四问之一）。
	comm    float64
	stamp   float64
	hasFee  bool // 是否拿到过**券商口径**的费用（决定 FeeEst）
	orders  []store.RealOrder
	anyFill bool
}

func (f *tLiveLegFact) avgPrice() float64 {
	if f == nil || f.qty <= 0 {
		return 0
	}
	return f.amount / float64(f.qty)
}

// qtyOf  nil 安全地取成交量：账本里"这条腿没有记录"与"记录了但 0 成交"在对账里同义（都是没成）。
func (f *tLiveLegFact) qtyOf() int {
	if f == nil {
		return 0
	}
	return f.qty
}

// exists 该键是否留下过任何痕迹（成交回报或委托行）。两者全无＝这条腿从未存在过。
func (f *tLiveLegFact) exists() bool {
	return f != nil && (f.anyFill || len(f.orders) > 0)
}

// add 聚合另一条键的同腿事实（配对腿的 :p/:p2… 多次重报要合并成"这一腿总共成了多少"）。
func (f *tLiveLegFact) add(o *tLiveLegFact) {
	if f == nil || o == nil {
		return
	}
	f.qty += o.qty
	f.amount += o.amount
	f.fee += o.fee
	f.comm += o.comm
	f.stamp += o.stamp
	f.hasFee = f.hasFee || o.hasFee
	f.anyFill = f.anyFill || o.anyFill
	f.orders = append(f.orders, o.orders...)
}

// tLiveRound 一轮实盘 T 的全部输入与中间读数。
//
// 刻意把"一轮"做成一个值对象而不是散在 Engine 上的字段：同一轮的持仓/行情/资金/账本必须是
// **同源的一次快照**（方案 §8.4.5 对总量帽分母的要求），跨函数各读各的会让判定随读取时点漂移。
type tLiveRound struct {
	e       *Engine
	uid     string
	ctrl    *trading.Controller
	db      *store.DB
	st      *tLiveStore
	p       paper.TParams
	quotes  map[string]*data.StockInfo
	now     time.Time
	day     string // YYYYMMDD（回转归属日）
	dash    string // YYYY-MM-DD（orders/fills 的当日过滤口径）
	force   bool   // 是否已到 force_unwind_at（尾盘只收摊）
	canOpen bool   // 本轮是否允许开新腿（止血/盲区/熔断/资金不可得 任一为假）
	openWhy string // 不许开新腿的全局原因（进 step0 统计）

	positions map[string]store.RealPosition // 纯代码 → 实盘持仓行
	prices    map[string]float64            // 纯代码 → 本轮现价（缺失不入表）
	book      tLiveBook
	total     float64
	cash      float64
	cashOK    bool
	capBase   float64 // 反 T 总量帽分母 = min(总资产, 持仓总市值)

	legFactsCache map[string]map[string]*tLiveLegFact // 交易日 → 键 → 成交事实
	ordersCache   map[string]map[string][]store.RealOrder
	dayBlocked    bool

	readers tLiveReaders
}

// runIntradayTLive 实盘做 T 的每轮驱动（与纸面钩子同频率；调用点见 scoring_loop.go 的战略分发之后）。
//
// 前置只拨**最外层**的三道门，且顺序是刻意的：
//  1. 时段门（`IsActiveSession`）：集合竞价没有可靠连续价、盘后没有对手盘，一律不驱动；
//  2. 执行链门：控制器没启用/不在 auto 模式，动了也只会是"标记了状态但下不出单"的假回转；
//  3. 账本门：T 状态账本取不到就不动钱（无账本 = 无幂等 = 无净额口径 = 无收摊依据）。
//
// `t.enabled=false` 的一键止血**不在这里 return**（§8.11.5）：止血要求"当轮停开新腿"，
// 不是在途腿自生自灭——在途腿照常对账、配平、收摊，且不撤在途单。
func (e *Engine) runIntradayTLive(uid string, ctrl *trading.Controller, quotes map[string]*data.StockInfo) {
	now := e.nowTime()
	if ctrl == nil {
		return
	}
	if uid == "" {
		uid = e.primaryMember()
	}
	if !data.IsActiveSession(now) {
		return
	}
	if !ctrl.Enabled() || ctrl.Mode() != "auto" {
		return
	}
	r, why := e.newTLiveRound(uid, ctrl, quotes, now)
	if r == nil {
		if why != "" {
			log.Printf("%s 本轮不驱动：%s", tLLogTag, why)
		}
		return
	}
	r.execute()
}

// newTLiveRound 装配一轮：把账号级配置解析成执行参数，再交给 newTLiveRoundWith。
// 拆成两层是为了让测试能用**手构的 TParams** 驱动同一条编排链：定权读数（MS/盲区）在离线用例里
// 不可得，若把阈值解析写死在装配里，用例就只能靠"盲区把一切拦掉"这条副作用通过——那是假绿。
func (e *Engine) newTLiveRound(uid string, ctrl *trading.Controller, quotes map[string]*data.StockInfo,
	now time.Time) (*tLiveRound, string) {
	sc := e.sizingConfigFor(uid)
	p, _, missing := e.tParamsFor(uid, sc.T, sc.ShadowOn())
	p.MissingKeyInput = missing
	return e.newTLiveRoundWith(uid, ctrl, quotes, now, p)
}

// newTLiveRoundWith 装配一轮（参数已解析）：读账本/持仓/资金，并接好真实读数源。
// 返回 (nil, 原因) 表示本轮整体不动钱（缺的是"动钱的前提"，不是某一条门槛）。
func (e *Engine) newTLiveRoundWith(uid string, ctrl *trading.Controller, quotes map[string]*data.StockInfo,
	now time.Time, p paper.TParams) (*tLiveRound, string) {
	st := e.tLiveFor(uid)
	if st == nil {
		return nil, "T 状态账本不可得（accountsRoot 未注入）——无账本即无幂等，不动钱"
	}
	e.mu.RLock()
	db := e.realStore
	e.mu.RUnlock()
	if db == nil {
		return nil, "实盘账本不可得"
	}
	missing := p.MissingKeyInput
	poses, err := db.RealPositionsForUser(uid)
	if err != nil {
		// 持仓读不到 ≠ 空仓：把"读不到"当"没有仓位"会让下一步在无行情语境里动钱（§1.5 同源纪律）。
		return nil, fmt.Sprintf("实盘持仓读取失败（fail-closed）：%v", err)
	}
	r := &tLiveRound{
		e: e, uid: uid, ctrl: ctrl, db: db, st: st, p: p, quotes: quotes, now: now,
		day:           cntime.DayCompactOf(now),
		dash:          cntime.DayOf(now),
		force:         tLHM(now) >= p.ForceUnwindHM,
		positions:     map[string]store.RealPosition{},
		prices:        map[string]float64{},
		legFactsCache: map[string]map[string]*tLiveLegFact{},
		ordersCache:   map[string]map[string][]store.RealOrder{},
		openWhy:       "",
	}
	var holdValue float64
	for _, pos := range poses {
		if pos.Qty <= 0 {
			continue
		}
		code := pureTsCode(pos.TsCode)
		r.positions[code] = pos
		if q := quotes[code]; q != nil && q.Price > 0 {
			r.prices[code] = q.Price
			holdValue += q.Price * float64(pos.Qty)
		} else if px := pos.CurPrice; px > 0 {
			r.prices[code] = px
			holdValue += px * float64(pos.Qty)
		}
		// 缺价持仓**不进 prices**：判定链据此对该票 fail-closed（step0 无实时价），
		// 但市值仍不能按 0 计进总量帽分母——帽的分母必须与 CurStrat 同一批读数（§8.4.5）。
	}
	r.capBase = math.Min(r.totalAssetsOf(db), holdValue)
	r.book = st.bookValue(r.prices)
	r.cash, r.cashOK = ctrl.AvailableCash()
	// §CANCEL-STATE（2026-10-28）禁挂单窗口闸：T 的两腿是**串行**的（先买后卖或先卖后买），
	// 第二腿若撞上窗口被拒，第一腿已经成交 → 裸腿（只买没卖/只卖没买），这是最坏的形态。
	// 所以窗口闸必须落在"开第一腿之前"，而不是指望 PlaceOrder 逐腿兜住。
	windowBlocked, windowLabel := ctrl.NewOrderBlockedByWindow(now)
	blocked, blockWhy := r.dayLossBlocked()
	r.dayBlocked = blocked
	// 开新腿的许可：止血开关 / 方向都没配 / 影子期 / 关键输入缺失 / 熔断日 / 权益或资金口径不可得。
	// 全部是"这一轮不许开新腿"，没有一条影响已开腿的配对与收摊。
	//
	// §W-09：行情门**先算再 switch，且无条件算一次**。写进 case 条件里的话，
	// 某个前置 case（如 !p.Enabled）命中时它根本不会被调用 ⇒ 弃权日志永远不打，
	// 于是"牛熊关闭 T 到底生效了没有"在日志里查不到证据。
	regimeWhy := r.regimeBlock()
	switch {
	case !p.Enabled:
		r.canOpen, r.openWhy = false, "t.enabled=false 一键止血：只推进在途腿，不开新腿"
	case windowBlocked:
		r.canOpen, r.openWhy = false, "禁挂时段（"+windowLabel+"）：交易所不接新委托，本轮不开新腿（避免两腿只成其一）"
	case !p.BuyFirst && !p.SellFirst:
		r.canOpen, r.openWhy = false, "t.mode 未放行任何方向"
	case p.Shadow:
		r.canOpen, r.openWhy = false, "影子期：目标不可信，不做日内双向操作"
	case missing:
		r.canOpen, r.openWhy = false, "关键市场输入缺失（数据盲区）：目标不可信，不做日内双向操作"
	case regimeWhy != "":
		// §W-09 形态门：位置在 missing 之后、dayBlocked 之前——它和 missing/Shadow 是同一类
		// （"环境不可信"），而 dayBlocked/cash/total 是"今天的账户状态"。归因顺序上先报环境、
		// 后报账户，排障时 openWhy 才指向真正的原因。
		r.canOpen, r.openWhy = false, regimeWhy
	case r.dayBlocked:
		r.canOpen, r.openWhy = false, "熔断日两向都不开新腿（在途腿按收摊处理）："+blockWhy
	case !r.cashOK:
		r.canOpen, r.openWhy = false, "可用资金口径不可得（§M12-A 同源 fail-close）"
	case r.total <= 0 || r.capBase <= 0:
		r.canOpen, r.openWhy = false, "总资产/持仓市值读数不可得：额度分母缺失不开腿"
	default:
		r.canOpen = true
	}
	// 真实读数源接到引擎侧（与纸面 wireTCallbacks 同一批实现，保证两态判定同源）。
	r.readers = tLiveReaders{
		minuteBasis:       e.tMinuteVWAP,
		stockMeta:         e.tStockMetaOf,
		newBuySignal:      func(code string, since time.Time) (string, bool) { return e.tFreshBuySignal(code, since) },
		activeBuySignal:   func(code string, since time.Time) (string, bool) { return e.tActiveBuySignal(code, since) },
		activeSellVerdict: func(code string) bool { return e.tSellVerdictActive(code) },
		stopLineDistance: func(code string, price float64) (float64, bool) {
			return e.tLiveStopLineDistance(uid, code, price)
		},
		strategicBusy: r.strategicBusy,
		oppositeLeg:   e.tLiveOppositeLeg,
	}
	// §FUNDS-TODAY：每轮把「T 侧」资金事实写成一帧落盘（接口只搬不算）。
	// 放在读数源接好之后：这一帧描述的是"本轮真正要用来判定的那组数"，不是另取一套。
	r.publishTFunds()
	return r, ""
}

// publishTFunds 组装并落盘当日资金帧的**T 侧**（信号仓侧由打分循环写，见 funds_today.go）。
//
// 分工（§FUNDS-TODAY 方案 §7.1）：
//   - 信号仓侧（账户资金 + MS/目标/当前/剩余）：打分循环每轮发布（去重 10s）——
//     它与做 T 开不开无关，挂在 T 循环上会让 sizing.t.enabled=false 的日子永远"尚未定权"；
//   - T 侧（本函数）：额度/可用 T 现金/反 T 冻结/在途腿/当日 T 收益——只有 T 循环算得出来。
//
// 口径纪律：
//   - 全部复用本轮已取好的读数（cash/total/book/targetCurRoom/cashForT/dayLedger），不在这里另算公式；
//   - T 没跑时**不动** T 侧字段（保持上一帧并由 t_at 标出取数时刻），不伪造 0；
//   - 每日清零：帧带 Day，跨日 load 不复活；新交易日首帧之前接口返回 null。
func (r *tLiveRound) publishTFunds() {
	if r == nil || r.st == nil {
		return
	}
	tQuota := 0.0
	if ms := math.Min(r.p.MarketScale, 1.0); ms < 1 {
		tQuota = r.total * (1 - ms)
	}
	cashT, _ := r.cashForT() // 口径不可得时回 0（cash_ok 字段已经说明"不可得"，不拿 0 冒充）
	_, done, _, _, openNow, pnlNet, pnlGross := r.st.dayLedger()
	at := r.now.Format(time.RFC3339)
	r.st.updateFunds(func(f *tLiveFunds) {
		f.TAt = at
		f.TQuota = tQuota
		f.CashT = cashT
		f.TFrozenCash = r.book.FrozenCash
		f.OpenBuyLegs = r.book.BuyLegValue
		f.OpenSellLegs = r.book.SellLegValue
		f.OpenLegs = openNow
		f.DoneRounds = done
		f.PnlNet = pnlNet
		f.PnlGross = pnlGross
	})
}

// totalAssetsOf 总资产：优先券商快照口径（与 dynamicBuyQty 同优先级），回落账本合计。
func (r *tLiveRound) totalAssetsOf(db *store.DB) float64 {
	if acc, err := db.GetRealAccount(r.uid); err == nil && acc.TotalAsset > 0 {
		r.total = acc.TotalAsset
		return r.total
	}
	if v, err := db.TotalAssets(r.uid); err == nil && v > 0 {
		r.total = v
		return r.total
	}
	return 0
}

// dayLossBlocked 熔断日判定（W8 §2.4）：risk.checkDayLoss 只拦买入，反 T 卖腿始终放行，
// 所以"熔断日两向都不开新腿"只能由编排层自己判。权益分母取不到时**按熔断日处理**——
// 少做一轮 T 是机会成本，在盲区里做日内双向操作是资损方向。
func (r *tLiveRound) dayLossBlocked() (bool, string) {
	cfg := r.ctrl.Config()
	if cfg.RiskGate.DayLossLimitPct <= 0 {
		return false, ""
	}
	pnl, err := r.db.TodayRealizedPnl(r.uid, r.dash)
	if err != nil {
		return true, fmt.Sprintf("今日已实现盈亏读数失败（按熔断日处理）：%v", err)
	}
	if pnl >= 0 {
		return false, ""
	}
	base := r.total
	if base <= 0 {
		if v, verr := r.db.TotalAssets(r.uid); verr == nil && v > 0 {
			base = v
		}
	}
	if base <= 0 {
		base = cfg.InitialCapital
	}
	if base <= 0 {
		return true, "已实现亏损达阈与否无法判定（权益分母不可得，按熔断日处理）"
	}
	if lossPct := -pnl / base * 100; lossPct >= cfg.RiskGate.DayLossLimitPct {
		return true, fmt.Sprintf("日内已实现亏损 %.0f = 总资产 %.2f%% ≥ 阈值 %.2f%%", -pnl, lossPct, cfg.RiskGate.DayLossLimitPct)
	}
	return false, ""
}

// ── 每轮主流程 ────────────────────────────────────────────────────────────────────────

func (r *tLiveRound) execute() {
	// 0a 跨日收敛先于一切：昨日 open 必须先与 orders/fills 对账再消费（§2.3「绝不重启后当没发生过」）。
	r.rollStaleOpen()

	// 0b 当日对账：成交真相只取 fills。对账读数取不到时本轮整体不动钱——
	// 在不知道"昨天那两条腿到底成没成"的情况下继续配对或新开，是把不确定性放大成两笔。
	if _, err := r.facts(r.day); err != nil {
		log.Printf("%s 对账读数不可得（本轮不动钱）：%v", tLLogTag, err)
		return
	}

	// 1 在途腿：先对账、再配对/收摊。**配对先于开新腿**——顺序一旦颠倒，
	// 同一轮里可能先开一条新腿又去配旧腿，两条腿时间上重叠，正是 Q13 要避开的形态。
	for _, rnd := range r.st.openRounds() {
		r.reconcile(rnd)
		if rnd.Status != tLStatusOpen {
			continue
		}
		// P3：熔断日封买回通道 ⇒ 反 T 只能转战略减仓（唯一还会改终态的规则）。
		// 撤在途委托必须**先于**转终态：否则终态写下去、委托还挂着，晚到的成交就是无主成交
		// （仓位变了，但没有一条腿认领它）。
		if state, why := r.premiseGone(rnd); state != "" {
			r.cancelOpenKeys(rnd)
			r.finish(rnd, state, why)
			continue
		}
		// 其余三个触发源（stop / verdict / regime）不在这里——它们是"提前配平"，
		// 由 pair() 内部的 accelReason 处理，不改终态语义。
		r.pair(rnd)
	}
	if !r.canOpen {
		if r.openWhy != "" {
			r.st.noteBlock("step0", r.openWhy, "")
		}
		return
	}
	// 2 开新腿判定链。
	r.openPass()

	// 3 每日总账：一天一行，落在尾盘判定时刻之后（该时刻后不再开新腿，计数已经稳定）。
	// 为什么这条不是可选项（§6.3 B 组第 19 条）：**当日 T 腿委托笔数与报撤比**是
	// "日内频繁反向会不会被券商异常交易观察"的唯一量化证据。没有这一行，灰度期只能靠肉数日志，
	// 而肉数出来的比例永远是不可复核的——本项目对"只能靠人记着的合规证据"一律不收。
	r.logDailyLedger()
}

// logDailyLedger 打一行当日 T 腿总账（每账号每日一行；跨日由账本 load 自然重置）。
// 委托笔数取 orders 表的真值（含撤后重报的每一笔），不是回转条数——两者差多少就是重报了多少次。
// English: the once-a-day ledger line; placement counts come from the orders table (re-submissions
// included), because submission volume is the compliance evidence this line exists to record.
func (r *tLiveRound) logDailyLedger() {
	// 闩在账本上而不是在轮对象上：每轮都会 new 一个 tLiveRound，挂在轮上等于每天打几十行。
	if r.st == nil || !r.force {
		return
	}
	r.st.mu.Lock()
	if r.st.ledgerLogged {
		r.st.mu.Unlock()
		return
	}
	r.st.ledgerLogged = true
	r.st.mu.Unlock()
	opened, done, unwound, converted, openNow, pnlNet, pnlGross := r.st.dayLedger()
	cost := r.st.dayCost()
	placed, canceled, filled, err := r.db.TLegOrderLedger(r.uid, r.dash)
	ratio := 0.0
	if placed > 0 {
		ratio = float64(canceled) / float64(placed) * 100
	}
	if err != nil {
		// 台账读不到不等于 0 笔：这条线本身就是证据，证据缺了必须说出来而不是填个好看的数。
		log.Printf("%s 每日总账 开腿%d 配对%d 收摊%d 转战略%d 在途%d 净%.2f 毛%.2f｜委托台账读取失败（报撤比不可得）：%v",
			tLLogTag, opened, done, unwound, converted, openNow, pnlNet, pnlGross, err)
		return
	}
	log.Printf("%s 每日总账 账号=%s 开腿%d 配对%d 收摊%d 转战略%d 在途%d 做T净=%.2f 做T毛=%.2f｜委托%d笔 撤%d笔 成交%d笔 报撤比%.1f%%｜成本 佣金买%.2f 佣金卖%.2f 印花%.2f 滑点%.2f 估算占比%.0f%% 参与率中位%.4f%% 最大%.4f%%",
		tLLogTag, r.uid, opened, done, unwound, converted, openNow, pnlNet, pnlGross,
		placed, canceled, filled, ratio,
		cost.FeeBuy, cost.FeeSell, cost.StampTax, cost.SlipCost, cost.FeeEstimateShare*100,
		cost.ParticipationPctP50, cost.ParticipationPctMax)
}

// facts 某交易日的 T 腿成交/委托索引（按前缀过滤，只捞 t0: 键）。
// 缓存到轮次：尾盘收摊要同时看今天与昨日的腿，逐票重复查会把这些查询放大成持仓数倍。
func (r *tLiveRound) facts(dayCompact string) (map[string]*tLiveLegFact, error) {
	if f, ok := r.legFactsCache[dayCompact]; ok {
		return f, nil
	}
	if len(dayCompact) != 8 {
		return nil, fmt.Errorf("回转记录的日子字段非法：%s（对账无法按日过滤）", dayCompact)
	}
	dash := dayCompact[:4] + "-" + dayCompact[4:6] + "-" + dayCompact[6:8]
	fills, err := r.db.ListFillsByDay(r.uid, dash)
	if err != nil {
		return nil, err
	}
	byKey := map[string]*tLiveLegFact{}
	for _, f := range fills {
		key := strings.TrimSpace(f.SignalID)
		if !strings.HasPrefix(key, tLKeyPrefix) || f.Qty <= 0 {
			continue // 非 T 腿（战略/退出键）不参与回转对账；空键更不许当成"匹配一切"
		}
		acc := byKey[key]
		if acc == nil {
			acc = &tLiveLegFact{}
			byKey[key] = acc
		}
		amt := f.Amount
		if amt <= 0 {
			amt = f.Price * float64(f.Qty)
		}
		acc.qty += f.Qty
		acc.amount += amt
		acc.anyFill = true
		// 分项聚合（B1）：fills 表的 Fee 是佣金、StampTax 是印花税；任一 >0 就说明这一腿有券商口径。
		if f.Fee > 0 || f.StampTax > 0 {
			acc.comm += f.Fee
			acc.stamp += f.StampTax
			acc.fee += f.Fee + f.StampTax
			acc.hasFee = true
		}
	}
	orders, oerr := r.db.RealOrdersForUser(r.uid)
	if oerr != nil {
		return nil, oerr
	}
	bySignal := map[string][]store.RealOrder{}
	for _, o := range orders {
		key := strings.TrimSpace(o.SignalID)
		if strings.HasPrefix(key, tLKeyPrefix) {
			bySignal[key] = append(bySignal[key], o)
		}
	}
	r.ordersCache[dayCompact] = bySignal
	r.legFactsCache[dayCompact] = byKey
	// 委托行按键挂到成交事实上（同一天的键同名；跨日轮次只用自己的那天）。
	for key, rows := range bySignal {
		acc := byKey[key]
		if acc == nil {
			acc = &tLiveLegFact{}
			byKey[key] = acc
		}
		acc.orders = append(acc.orders, rows...)
	}
	return byKey, nil
}

// fact 当日某键的成交事实（无记录返回零值指针，调用方按 qty==0 判"没成交"）。
func (r *tLiveRound) fact(key string) *tLiveLegFact {
	m, err := r.facts(r.day)
	if err != nil {
		return &tLiveLegFact{}
	}
	if f, ok := m[key]; ok {
		return f
	}
	return &tLiveLegFact{}
}

// pairFact 配对腿的聚合事实：首腿用回转 ID，第二腿用 ID+":p"，撤单重报为 ":p2"…
// 全部前缀聚合，否则重报的那一截成交会凭空消失在账本里（价差算少、配对判不成）。
func (r *tLiveRound) pairFact(id string) *tLiveLegFact {
	m, err := r.facts(r.day)
	out := &tLiveLegFact{}
	if err != nil {
		return out
	}
	for k, f := range m {
		if k == id+tLPairSuffix || strings.HasPrefix(k, id+tLPairSuffix) {
			out.qty += f.qty
			out.amount += f.amount
			out.fee += f.fee
			out.comm += f.comm
			out.stamp += f.stamp
			out.hasFee = out.hasFee || f.hasFee
			out.anyFill = out.anyFill || f.anyFill
			out.orders = append(out.orders, f.orders...)
		}
	}
	return out
}

// nextPairKey 配对腿的下一个可用键：首腿成交后先试 ID+":p"，该键已有委托但被撤/废
// （零成交终态）时按 :p2、:p3 递增。刻意不复用同一键重放——orders 唯一键的放行条件
// （ResetFailedRealOrder）只覆盖"发送失败/已撤零成交"，把重报键递增资损面最小。
func (r *tLiveRound) nextPairKey(id string) string {
	base := id + tLPairSuffix
	if len(r.fact(base).orders) == 0 {
		return base
	}
	for n := 2; n < 20; n++ {
		k := fmt.Sprintf("%s%d", base, n)
		if len(r.fact(k).orders) == 0 {
			return k
		}
	}
	return base
}

// ── 0a 跨日收敛 ───────────────────────────────────────────────────────────────────────

// rollStaleOpen 昨日（及更早）仍为 open 的记录：先对账、再显式收摊。
// 处置口径与纸面侧 rollTRoundsLocked 同构，但**买回侧同样不许跨日追买**（Q8）：
//   - 正 T 买腿有成交而配对不足 ⇒ unwound（机动仓被迫成隔夜底仓，次日 CurStrat 含它）；
//   - 反 T 卖腿有成交而未买回 ⇒ converted（那次高抛转战略减仓，不是 T 收益）；
//   - 两腿都齐 ⇒ 补记 done（回报比状态落盘更早的场景，崩溃恢复后不欠一笔收益统计）；
//   - 完全查无委托（幽灵）⇒ unwound 留痕，绝不"当它成交了"继续配对。
func (r *tLiveRound) rollStaleOpen() {
	stale := r.st.staleOpenRounds()
	if len(stale) == 0 {
		return
	}
	for _, rnd := range stale {
		fills, err := r.facts(rnd.Day)
		if err != nil {
			// 对账源不可得时**保持 open 并下一轮再试**：这里的"显式留痕"必须建立在
			// 知道腿成没成的前提上，凭猜测写终态会把一条真成交的回转折进噪声里。
			log.Printf("%s 跨日对账失败，%s 暂不收摊（下一轮重试）：%v", tLLogTag, rnd.ID, err)
			continue
		}
		first := fills[rnd.ID]
		pair := &tLiveLegFact{}
		for k, f := range fills {
			if k == rnd.ID+tLPairSuffix || strings.HasPrefix(k, rnd.ID+tLPairSuffix) {
				pair.add(f)
			}
		}
		if rnd.Dir == tLDirBuy {
			rnd.BuyFilled = first.qtyOf()
			rnd.BuyPrice = tLRound3(first.avgPrice())
			rnd.SellFilled = pair.qty
			rnd.SellPrice = tLRound3(pair.avgPrice())
		} else {
			rnd.SellFilled = first.qtyOf()
			rnd.SellPrice = tLRound3(first.avgPrice())
			rnd.BuyFilled = pair.qty
			rnd.BuyPrice = tLRound3(pair.avgPrice())
		}
		switch {
		case !first.exists() && !pair.exists():
			r.seal(rnd, tLStatusUnwound, "跨日对账：orders/fills 查无该回转的委托（幽灵 open 收敛）")
		case r.roundMatched(rnd):
			// 两腿齐但从未收尾（回报先于落盘的崩溃窗口）：按 done 补记，收益属于昨天那一天。
			r.seal(rnd, tLStatusDone, "跨日对账：两腿均已成交，补记完成")
		case rnd.Dir == tLDirBuy:
			r.seal(rnd, tLStatusUnwound, fmt.Sprintf(
				"跨日未配平：买腿 %d 股、配对卖腿 %d 股，机动仓已成隔夜底仓（次日战略仓位含它并自动压缩剩余空间）",
				rnd.BuyFilled, rnd.SellFilled))
		default:
			r.seal(rnd, tLStatusConvert, fmt.Sprintf(
				"跨日未回补：卖腿 %d 股、买回 %d 股 ⇒ 反 T 转战略减仓（§8.5.2 只有信号才回补，且绝不隔夜追买）",
				rnd.SellFilled, rnd.BuyFilled))
		}
	}
}

// sealStale 与 finish 的差别只在写回哪里：跨日记录留在留档集合（legacy）里，不进当日账本，
// 否则一条昨日 open 会被今天"在途查询"看见并参与配对。
func (r *tLiveRound) seal(rnd *TRoundLive, state, reason string) {
	rnd.Status = state
	rnd.ClosedAt = r.now.Format(time.RFC3339)
	if state != tLStatusDone {
		rnd.UnwindReason = reason
	}
	rnd.FrozenCash = 0 // 终态一律释放冻结：否则那条反 T 腿会永远占着回购款、账户系统性欠用现金
	if r.roundMatched(rnd) {
		r.fillPnl(rnd)
	}
	// 跨日收敛同样要补 B1 读数（否则昨日腿的滑点/未成交原因永远缺一截）。
	r.refreshSlip(rnd)
	r.markUnfilled(rnd)
	if err := r.st.sealStale(rnd); err != nil {
		log.Printf("%s 跨日收摊落盘失败 %s：%v", tLLogTag, rnd.ID, err)
	}
	log.Printf("%s 回转跨日收摊 %s %s(%s) → %s 原因=%s", tLLogTag, rnd.ID, rnd.Dir, rnd.Name, state, reason)
}

// ── 0b 当日对账 ───────────────────────────────────────────────────────────────────────

// reconcile 用 fills 的真值刷新一轮的成交量与均价，并收敛幽灵 open / 已配平的轮。
// 状态账本只是意图；意图与事实不一致时，一律以事实为准（§8.6）。
func (r *tLiveRound) reconcile(rnd *TRoundLive) {
	first := r.fact(rnd.ID)
	pair := r.pairFact(rnd.ID)
	changed := false
	apply := func(filled *int, px *float64, f *tLiveLegFact) {
		if f.qty != *filled || tLRound3(f.avgPrice()) != *px {
			*filled = f.qty
			*px = tLRound3(f.avgPrice())
			changed = true
		}
	}
	if rnd.Dir == tLDirBuy {
		apply(&rnd.BuyFilled, &rnd.BuyPrice, first)
		apply(&rnd.SellFilled, &rnd.SellPrice, pair)
	} else {
		apply(&rnd.SellFilled, &rnd.SellPrice, first)
		apply(&rnd.BuyFilled, &rnd.BuyPrice, pair)
	}
	// 幽灵 open（W8 §3.1 锁 13）：状态说 open，orders 里两腿都查无委托、fills 里也无成交。
	// 判据必须同时看"没有任何委托行"和"过了宽限期"：前者是事实缺失，后者排除刚排队还没落单的正常态。
	if !first.anyFill && !pair.anyFill && len(first.orders) == 0 && len(pair.orders) == 0 &&
		r.olderThanGrace(rnd) {
		r.finish(rnd, tLStatusUnwound, "对账：状态账本说 open，orders/fills 查无对应委托（幽灵 open 必收敛，不按已成交继续配对）")
		return
	}
	if changed {
		r.refreshSlip(rnd)
		if err := r.st.put(rnd); err != nil {
			log.Printf("%s 对账落盘失败 %s：%v", tLLogTag, rnd.ID, err)
		}
	}
	if r.roundMatched(rnd) {
		r.finish(rnd, tLStatusDone, r.matchedReason(rnd))
	}
}

// olderThanGrace 开腿是否已过幽灵判定宽限期（OpenedAt 解析失败按"已过"处理：
// 一条时间戳都读不出来的记录没有资格被当成新腿继续等）。
func (r *tLiveRound) olderThanGrace(rnd *TRoundLive) bool {
	return r.olderThan(rnd, tLGhostGrace)
}

// olderThan 开腿是否已超过 d（§W-06-R4 抽取：腿龄门有两个用途——幽灵宽限与本笔止损的腿龄门，
// 第二份时间戳解析逻辑一旦出现，两处的"解析失败算什么"就会各自漂移）。
// 解析失败按"已过"处理，与 olderThanGrace 同口径。
func (r *tLiveRound) olderThan(rnd *TRoundLive, d time.Duration) bool {
	at, err := time.Parse(time.RFC3339, rnd.OpenedAt)
	if err != nil {
		return true
	}
	return r.now.Sub(at) > d
}

// roundMatched 两腿是否已按等量配平（只有配平才谈"做 T 收益"）。
func (r *tLiveRound) roundMatched(rnd *TRoundLive) bool {
	if rnd.Dir == tLDirBuy {
		return rnd.BuyFilled > 0 && rnd.SellFilled >= rnd.BuyFilled
	}
	return rnd.SellFilled > 0 && rnd.BuyFilled >= rnd.SellFilled
}

func (r *tLiveRound) matchedReason(rnd *TRoundLive) string {
	q := rnd.BuyFilled
	if rnd.SellFilled < q {
		q = rnd.SellFilled
	}
	if rnd.Dir == tLDirBuy {
		return fmt.Sprintf("正T配对完成：买%.3f→卖%.3f %d股", rnd.BuyPrice, rnd.SellPrice, q)
	}
	return fmt.Sprintf("反T配对完成：卖%.3f→买%.3f %d股", rnd.SellPrice, rnd.BuyPrice, q)
}

// ── 1 配对与收摊 ──────────────────────────────────────────────────────────────────────

// pair 处理一条已开腿的第二腿。
//
// 正 T（第一腿=买）：现价相对买价走出 min_spread_pct 有利价差 ⇒ 卖**等量底仓**；
// 到尾盘判定时刻**无条件**卖出等量（亏损也卖：这是把敞口减回原状，不需要许可）。
// 反 T（第一腿=卖）：
//   - 盘中回落达价差 ⇒ 自动买回（那是当日卖掉的那部分底仓，敞口回到开腿前，不新增敞口）；
//   - 尾盘 ⇒ §8.5.2 (b) **只有活跃买入信号才回补**，且要重走战略闸门；无信号 ⇒ converted；
//     亏损超过 loss_breaker_pct ⇒ 一律封买回通道；
//   - 买回被拦（现金/纪律/涨停/无价/闸门不放行）⇒ 显式 unwound 留痕，**绝不重试、绝不隔夜追买、
//     绝不用另一个账号反向对补**（§8.6 + 对手方不变量）。
func (r *tLiveRound) pair(rnd *TRoundLive) {
	force := r.force
	price := r.prices[rnd.Code]
	if price <= 0 {
		// 无有效价：不下结论、不动状态——除了尾盘那条"买回侧"的腿：它本来就不该在没有价格时
		// 悬着过夜，按 §8.6「买回被拦（无价）」显式收摊。
		if force && rnd.Dir == tLDirSell && rnd.SellFilled > rnd.BuyFilled {
			r.finish(rnd, tLStatusUnwound, "尾盘买回被拦：该票本轮无实时价（不追买、不留过夜）")
		}
		return
	}
	if rnd.Dir == tLDirBuy {
		r.pairBuyFirst(rnd, price, force)
		return
	}
	r.pairSellFirst(rnd, price, force)
}

// pairBuyFirst 正 T 的第二腿：卖出等量底仓。
func (r *tLiveRound) pairBuyFirst(rnd *TRoundLive, price float64, force bool) {
	if rnd.BuyFilled <= 0 {
		if !force {
			return // 买腿还没成交：继续等，不动资金
		}
		// 尾盘零成交：先撤在途委托再收摊。带着一条"可能晚些成交"的委托进收盘竞价，
		// 等于让账本说"没有了"而柜台还挂着——那是幽灵敞口，不是半态留痕。
		r.cancelOpenKeys(rnd)
		r.finish(rnd, tLStatusUnwound, "尾盘买腿零成交：撤残量后收摊（机动仓未建立，不追买）")
		return
	}
	need := rnd.BuyFilled - rnd.SellFilled
	if need <= 0 {
		return // 对账环节会收尾
	}
	// §W-06-R4 加速配平：三个触发源共用一个出口（跳过价差门，立即发第二腿）。
	// accelTag 同时用于日志归因与失败分支的处置（加速失败即收摊，不许回去继续扛）。
	accelTag := ""
	if !force {
		if accel := r.accelReason(rnd, price); accel == "" {
			fav := (price - rnd.BuyPrice) / rnd.BuyPrice * 100
			if fav < r.p.MinSpreadPct {
				return // 价差没到：继续等（不追高卖）
			}
		} else {
			accelTag = accel
		}
	}
	if r.hasPendingPair(rnd) {
		if !force {
			return // 配对腿已在柜台挂着：不叠单（同一腿也不允许两条同时在途）
		}
		r.cancelOpenKeys(rnd) // 尾盘：撤掉未成交的残量，再按真实差额补一腿
	}
	pos, ok := r.positions[rnd.Code]
	if !ok || pos.Qty <= 0 {
		r.finish(rnd, tLStatusUnwound, "持仓行已不在账（被其他裁决平掉），回转作废")
		return
	}
	sellable := r.db.BuyableQtyForUserSell(r.uid, pos.TsCode, r.dash)
	if sellable < need {
		// T+1：当日买入的机动仓不可卖，第二腿只能卖原有底仓；底仓可卖量不足即半态（§8.2）。
		if force {
			r.finish(rnd, tLStatusUnwound, fmt.Sprintf(
				"尾盘强制配平失败：底仓可卖 %d < 需卖 %d ⇒ 机动仓被迫隔夜（次日战略仓位含它）", sellable, need))
			return
		}
		// 裁决 A：加速的语义是"不能再等"，失败就收摊，不许回去继续扛——
		// "stop" 的含义是"价差已亏到线，不许再扛"，失败后保持 open 会让它扛到 −8%。
		if accelTag != "" {
			r.finish(rnd, tLStatusUnwound, fmt.Sprintf(
				"加速配平失败（accel=%s）：底仓可卖 %d < 需卖 %d ⇒ 收摊不追（机动仓隔夜，次日战略仓位含它）",
				accelTag, sellable, need))
			return
		}
		r.markBlocked(rnd, "step7", fmt.Sprintf("配对可卖量不足 %d<%d", sellable, need))
		return
	}
	placed, err := r.sellLeg(pos, need, r.nextPairKey(rnd.ID), price, "正T卖出腿（配对/尾盘强制配平）")
	if err != nil {
		if force {
			r.finish(rnd, tLStatusUnwound, "配对卖腿被拒："+err.Error())
			return
		}
		if accelTag != "" {
			r.finish(rnd, tLStatusUnwound, "加速配平失败（accel="+accelTag+"）：配对卖腿被拒："+err.Error())
			return
		}
		log.Printf("%s 正T配对卖腿被拒 %s: %v（回转保持 open，下一轮再试）", tLLogTag, rnd.Code, err)
		r.markBlocked(rnd, "step8", "配对卖腿被拒: "+err.Error())
		return
	}
	// accelTag ∈ {"", "stop", "verdict", "regime"}：首周复盘必须按这三个标签**分组**算平均价差，
	// 成因完全不同（止损 / 让位信号 / 行情翻转），混在一起会得出"这套规则到底亏了多少"的错误结论。
	log.Printf("%s 正T配对卖腿已报 %s %d股@%.2f（回转 %s，等待回报，accel=%s）", tLLogTag, rnd.Code, placed, price, rnd.ID, accelTag)
}

// pairSellFirst 反 T 的第二腿：买回等量底仓。
func (r *tLiveRound) pairSellFirst(rnd *TRoundLive, price float64, force bool) {
	if rnd.SellFilled <= 0 {
		if !force {
			return
		}
		r.cancelOpenKeys(rnd)
		r.finish(rnd, tLStatusUnwound, "反T卖腿零成交：撤残量后收摊（未形成高抛，不动买回）")
		return
	}
	need := rnd.SellFilled - rnd.BuyFilled
	if need <= 0 {
		return
	}
	// §W-06-R4：与 pairBuyFirst 同一出口（三个触发源共用），accelTag 落日志与失败文案。
	accelTag := ""
	if force {
		// 尾盘两态判定：买回需要许可，减敞口不需要（正 T 那一半的时钟义务保留）。
		why, allowed := r.allowBuyBack(rnd, price, need)
		if !allowed {
			state := tLStatusConvert
			if r.dayBlocked {
				// 熔断日不是"这条减仓由 T 决定"，而是被动收摊：仓位变更由熔断触发，标 unwound。
				state = tLStatusUnwound
			}
			r.finish(rnd, state, why)
			return
		}
	} else {
		if accel := r.accelReason(rnd, price); accel == "" {
			fav := (rnd.SellPrice - price) / rnd.SellPrice * 100
			if fav < r.p.MinSpreadPct {
				return // 盘中配对只看价差
			}
		} else {
			accelTag = accel
		}
	}
	if r.hasPendingPair(rnd) {
		if !force {
			return
		}
		r.cancelOpenKeys(rnd)
	}
	// 买回的钱是开腿时就冻好的专款（§8.3 纪律 3）；专款不够 ⇒ 按 §8.6 收摊，不去动战略现金。
	if r.cashOK && r.cash < float64(need)*price*(1+r.p.FeeBufferPct/100) {
		r.finish(rnd, tLStatusUnwound, fmt.Sprintf(
			"买回被拦：可用现金 %.0f 不足 %d股@%.2f（专款之外不与战略买入抢钱，不追买）%s", r.cash, need, price, tLAccelSuffix(accelTag)))
		return
	}
	if err := r.buyLeg(rnd, r.nextPairKey(rnd.ID), need, price, "反T买回腿"); err != nil {
		// 不变量 6：买回被拦一律显式留痕收摊，**绝不重试**——重试就是"隔夜追买"的前一天形态。
		r.finish(rnd, tLStatusUnwound, "买回被拦，收摊不追买："+err.Error()+tLAccelSuffix(accelTag))
		return
	}
	log.Printf("%s 反T买回腿已报 %s %d股@%.2f（回转 %s）", tLLogTag, rnd.Code, need, price, rnd.ID)
}

// allowBuyBack 尾盘买回许可（§8.5.2 (b) + §8.5.3）。返回 (原因, 是否放行)。
// 拒绝时的状态区分由调用方按 reason 决定：熔断日＝被动收摊（unwound），其余＝主动转战略减仓（converted）。
func (r *tLiveRound) allowBuyBack(rnd *TRoundLive, price float64, need int) (string, bool) {
	if r.dayBlocked {
		return "熔断日：买回通道关闭 ⇒ 在途反 T 卖腿收摊（两向都不开新动作，不追买）", false
	}
	// §W-06-R4：尾盘买回熔断统一改用 round_stop_pct（**原为 loss_breaker_pct=3.0，现 2.0**），
	// 且算式换成 roundSpreadPctNow（整笔价差率）而不是单腿价差率——
	// 全系统只剩**一个**止损算式：accelReason 的 "stop" 与本处共用它（门禁 #26 钉住）。
	// 口径仍是纯价差不含费；差异只出现在"第二腿已部成"的情形（旧口径忽略已配平部分）。
	pct, _, okSpread := r.roundSpreadPctNow(rnd, price)
	if !okSpread {
		return "买回熔断判定不可得：本笔价差率算不出（首腿未成交/本金为 0）⇒ 封买回通道，转战略减仓", false
	}
	if r.p.RoundStopPct > 0 && pct <= -r.p.RoundStopPct {
		// 亏损一律封买回通道：这条腿不再是"T 买回"，只剩转战略这一条路，而那条路要另走闸门。
		log.Printf("%s t-buyback-breaker 熔断 %s(%s) 价差率%.2f%% 阈值-%.2f%%",
			tLLogTag, rnd.Code, rnd.Name, pct, r.p.RoundStopPct)
		return fmt.Sprintf("买回熔断：价差率 %.2f%% ≤ −%.2f%% ⇒ 封买回通道，转战略减仓", pct, r.p.RoundStopPct), false
	}
	openedAt, err := time.Parse(time.RFC3339, rnd.OpenedAt)
	if err != nil {
		return "开腿时刻不可解析 ⇒ 无法判定信号是否晚于开腿，不回补（fail-closed）", false
	}
	sigID, has := "", false
	if r.readers.activeBuySignal != nil {
		sigID, has = r.readers.activeBuySignal(rnd.Code, openedAt)
	}
	if !has {
		return "尾盘无活跃买入信号 ⇒ 不回补，转战略减仓（§8.5.2 作废买回侧的时钟义务）", false
	}
	if r.st.signalConsumed(sigID) || (r.e != nil && r.e.tStrategicConsumed(sigID)) {
		return "尾盘回补要用的信号已被消费 ⇒ 不再买回（§8.4.3 一次性消费），转战略减仓", false
	}
	// §8.5.3 转战略＝重走闸门：可转量 = min(T 腿数量, 单笔基准, Room, 行业/单票额度, 可用现金)，
	// 实盘的这套额度全在 dynamicBuyQty 一条路径里（与战略买入同一个函数，不是"改个状态"）。
	allowed, why := r.strategicAllowedQty(rnd, price, need)
	if allowed <= 0 {
		return "尾盘回补被战略闸门拦下 ⇒ 转战略减仓：" + why, false
	}
	if allowed < need {
		// 只批了部分 ⇒ 不部分买回：一部分成交的回转既不是 done 也不是干净的 converted，
		// 差额那截会挂着"等下一轮"的幻觉过夜。整轮转战略减仓，明早由卖出裁决接手。
		return fmt.Sprintf("尾盘回补仅部分过闸门 %d/%d ⇒ 整轮转战略减仓（§8.5.3）", allowed, need), false
	}
	rnd.UsedSignalID = sigID
	return "", true
}

// strategicAllowedQty 把回转的买回量交给战略预算通道裁一次（不新开旁路）。
func (r *tLiveRound) strategicAllowedQty(rnd *TRoundLive, price float64, qty int) (int, string) {
	if r.e == nil {
		return 0, "引擎不可用（无法走战略闸门）"
	}
	sc := r.e.sizingConfigFor(r.uid)
	sig := combat_agent.Signal{Code: rnd.Code, Name: rnd.Name, Direction: "做多", Action: "buy", Price: price}
	allowed, _ := r.e.dynamicBuyQty(sig, price, 0, sc, r.ctrl,
		dynBudgetCtx{MarketScale: r.p.MarketScale, StockScale: 1.0, MissingKeyInput: r.p.MissingKeyInput})
	if allowed > qty {
		allowed = qty
	}
	if allowed <= 0 {
		return 0, "战略预算给出 0 股（目标剩余空间/行业与单票额度/现金任一为限）"
	}
	return allowed, "战略闸门放行"
}

// cancelOpenKeys 撤掉该回转仍未终态的委托（买卖两侧都撤）。
// 只在收摊路径调用：`t.enabled=false` 的止血轮**不撤在途单**（§8.11.5），因为止血停的是"新动作"。
func (r *tLiveRound) cancelOpenKeys(rnd *TRoundLive) {
	keys := []string{rnd.ID}
	m, err := r.facts(r.day)
	if err == nil {
		for k := range m {
			if strings.HasPrefix(k, rnd.ID+tLPairSuffix) {
				keys = append(keys, k)
			}
		}
	}
	for _, k := range keys {
		f := r.fact(k)
		for _, o := range f.orders {
			if !tLOpenOrderStatus(o.Status) {
				continue
			}
			// 网关单号没回填（占位行 pend:）时撤不了：这条只能留在柜台侧由 SweepOrders 收尾，
			// 编排层不去猜单号——猜错的后果是撤掉别人的单。
			if o.OrderID == "" || strings.HasPrefix(o.OrderID, "pend:") {
				log.Printf("%s 撤单跳过（委托号未回填）%s %s", tLLogTag, k, o.Code)
				continue
			}
			if err := r.ctrl.CancelOrder(o.OrderID); err != nil {
				log.Printf("%s 撤单失败 %s %s：%v", tLLogTag, k, o.OrderID, err)
				continue
			}
			log.Printf("%s 收摊撤单 %s %s（状态=%s）", tLLogTag, k, o.OrderID, o.Status)
		}
	}
}

// hasPendingPair 该回转的第二腿是否**已经报出去但还没成交**（在途）。
//
// 为什么必须先问这一句再去对账：配对腿刚报出时，orders 表里还查不到成交、状态仍是"已报/部成"，
// 若直接按"腿没成交"处理就会重报第二条同键委托（柜台按唯一键拒了也是白报一次，还可能踩"频繁撤单"）。
// 在途即本轮什么都不做，下一轮再看——两腿串行的真正含义是"任一时刻至多一条开腿委托活着"。
// English: a pairing leg already in flight means do nothing this round; serialized legs means at
// most one live order per round at any instant.
func (r *tLiveRound) hasPendingPair(rnd *TRoundLive) bool {
	m, err := r.facts(r.day)
	if err != nil {
		return false
	}
	for k, f := range m {
		if k == rnd.ID || !strings.HasPrefix(k, rnd.ID+tLPairSuffix) {
			continue
		}
		for _, o := range f.orders {
			if tLOpenOrderStatus(o.Status) {
				return true
			}
		}
	}
	return false
}

// markBlocked 给一条在途腿盖"最近被哪一步拦"的戳。
//
// 为什么在回转记录上再留一份（除了全局 step 计数之外）：灰度期排障问的是"这条腿为什么挂了
// 40 分钟还没配平"，全局计数只能回答"今天有多少次拦在 step7"，两者合起来才解释得清单笔。
func (r *tLiveRound) markBlocked(rnd *TRoundLive, step, why string) {
	rnd.LastBlockStep = step + " " + why
	r.st.noteBlock(step, why, rnd.Code)
	if err := r.st.put(rnd); err != nil {
		log.Printf("%s 被拦留痕落盘失败 %s：%v", tLLogTag, rnd.ID, err)
	}
}

// orDashSource 空来源在拦截理由里显影成 "<空>"，让"这列上线前的存量行"与"回填判成 none"
// 在日志里可分辨（两者都不开腿，但排障时要知道是哪一种）。
func orDashSource(s string) string {
	if strings.TrimSpace(s) == "" {
		return "<空>"
	}
	return s
}

// ── 2 开新腿判定链（step0 → step9）──────────────────────────────────────────────────

// openPass 逐只实盘持仓走一遍判定链；任一步不过就留痕并结束本票本轮（§8.4.6）。
func (r *tLiveRound) openPass() {
	codes := make([]string, 0, len(r.positions))
	for code := range r.positions {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	for _, code := range codes {
		dir, qty, block := r.openPlan(code)
		if block != "" {
			step, rest := splitTLStep(block)
			r.st.noteBlock(step, rest, code)
			continue
		}
		if qty < tLMinBaseKeep {
			r.st.noteBlock("step7", "额度截断后不足 1 手", code)
			continue
		}
		r.openLeg(code, dir, qty)
	}
}

// openPlan 返回 (方向, 计划股数, "stepN 原因")；原因为空串表示允许开腿。
func (r *tLiveRound) openPlan(code string) (string, int, string) {
	pos, ok := r.positions[code]
	if !ok || pos.Qty <= 0 {
		return "", 0, "step0 无持仓"
	}
	if r.st.openRoundsForCode(code) != nil {
		return "", 0, "step1 该票已有在途腿（两腿串行：配对先于新开，绝不叠腿）"
	}
	// 锚点前置（§8.4.5）：没有锚点时止盈/止损基准会回落到摊薄成本，T 腿一成交就拖走风控参数线。
	// 实盘侧只有 anchor_price 一列、没有 anchor_source 列，故成本回落档无法在此辨识（已在交付说明里上报）。
	// 锚点前置（§8.4.5）：光有 anchor_price>0 不够，来源必须是**首买价可证**那一档。
	// cost_fallback/none/空值一律不开腿——否则"用摊薄成本冒充锚点"就从实盘侧绕回来了
	// （纸面侧同判据是 paper.Position.AnchorTicket，两边各一份、门禁 §127 锁字面量一致）。
	if pos.AnchorPrice <= 0 || !pos.AnchorTicket() {
		return "", 0, fmt.Sprintf("step0 锚点不可证(anchor_price=%.4f source=%q)：非首买价来源不得开 T（T 腿不许改动卖出裁决基准）",
			pos.AnchorPrice, orDashSource(pos.AnchorSource))
	}
	q := r.quotes[code]
	if q == nil || q.Price <= 0 {
		return "", 0, "step0 无实时价（fail-closed）"
	}
	v, okV := data.VWAPOf(q)
	if !okV {
		return "", 0, fmt.Sprintf("step0 分时均线不可信(Volume=%.0f Amount=%.0f)，不做 T 也不回落成本口径", q.Volume, q.Amount)
	}
	price := q.Price
	dev := (price - v) / v * 100
	buyDir := r.p.BuyFirst && dev <= -r.p.VWAPDevPct
	sellDir := r.p.SellFirst && dev >= r.p.VWAPDevPct
	if !buyDir && !sellDir {
		return "", 0, fmt.Sprintf("step4 现价相对分时均线只偏 %.2f%%，未达偏离门 ±%.2f%%", dev, r.p.VWAPDevPct)
	}
	// 方向门（step0）：正 T 新增敞口 ⇒ 环境门 + 大盘急跌门；反 T 减敞口 ⇒ 弱环境门 + 当日总量帽。
	if buyDir {
		if r.p.MinMarketScale > 0 && r.p.MarketScale < r.p.MinMarketScale {
			return "", 0, fmt.Sprintf("step0 正T环境门 MS=%.3f<%.2f", r.p.MarketScale, r.p.MinMarketScale)
		}
		if r.p.MarketPlungePct < 0 && r.p.IndexChangePct <= r.p.MarketPlungePct {
			return "", 0, fmt.Sprintf("step0 大盘急跌 %.2f%% ≤ %.2f%%，停正T", r.p.IndexChangePct, r.p.MarketPlungePct)
		}
	}
	if sellDir {
		if r.p.MarketScale < r.p.SellEnvMinMarketScale {
			return "", 0, fmt.Sprintf("step0 反T环境门 MS=%.3f<%.2f（极端弱势不做T）", r.p.MarketScale, r.p.SellEnvMinMarketScale)
		}
		if why := r.sellCapWhy(); why != "" {
			return "", 0, "step0 " + why
		}
	}
	if why := r.liquidityBlock(code, q); why != "" {
		return "", 0, "step0.5 " + why
	}
	// §W-09 量能门：位置在流动性前置之后、跨账号互斥之前——它与流动性前置是同一类
	// （"这只票今天适不适合做 T"），而互斥是合规红线，归因顺序上先报适配性、再报红线。
	// 形态门在 canOpen 那条链上（全市场一票否决），不在这里。
	if why := r.volumeBlock(code); why != "" {
		return "", 0, "step0.6 " + why
	}
	// 跨账号同码反向互斥（§8.4.5 对手方不变量，合规红线：对敲不申请、不做）。
	if r.readers.oppositeLeg != nil {
		if blocked, why := r.readers.oppositeLeg(r.uid, code, dirOf(buyDir, sellDir), r.day); blocked {
			return "", 0, "step1.5 " + why
		}
	}
	if r.p.MaxRounds > 0 {
		if used := r.st.tRoundCountFor(code); used >= r.p.MaxRounds {
			return "", 0, fmt.Sprintf("step2 单码当日回转次数已满(%d/%d)", used, r.p.MaxRounds)
		}
	}
	if tLHM(r.now) >= r.p.LastOpenHM {
		return "", 0, fmt.Sprintf("step3 已过最后开腿时刻 %s（只配对/收摊，不开新的半态腿）", r.p.LastOpenHM)
	}
	if tLInCooling(r.now, r.p.OpenCoolingMin) {
		return "", 0, "step3 开盘冷静期内（分时均线样本不足，V 与现价几乎重合）"
	}
	if why := r.intradayBlock(code, q, v); why != "" {
		return "", 0, "step4 " + why
	}
	if why := r.vminBlock(code, v); why != "" {
		return "", 0, "step4 " + why
	}
	if why := r.signalBlock(code, price, v); why != "" {
		return "", 0, "step5 " + why
	}
	if r.readers.strategicBusy != nil && r.readers.strategicBusy(code) {
		return "", 0, "step6 该股有未执行战略裁定/战略在途单，T 让路（§8.7）"
	}
	dir, qty, why := r.quota(code, pos, q, v, buyDir, sellDir)
	if why != "" {
		return "", 0, "step7 " + why
	}
	return dir, qty, ""
}

func dirOf(buy, sell bool) string {
	if buy {
		return tLDirBuy
	}
	if sell {
		return tLDirSell
	}
	return ""
}

// sellCapWhy 当日反 T 卖腿市值总量帽（分母 min(总资产, 持仓市值)，六轮 T6-2）：
// 与 CurStrat 同一批读数，不另取快照——两个来源不同会让帽的松紧随读取时点漂。
func (r *tLiveRound) sellCapWhy() string {
	if r.p.IntradaySellCapTotalPct <= 0 {
		return ""
	}
	if r.capBase <= 0 {
		return "反T总量帽分母不可得，按不过处理"
	}
	limit := r.capBase * r.p.IntradaySellCapTotalPct / 100
	if remain := limit - r.st.sellFirstNotional(r.day); remain <= 0 {
		return fmt.Sprintf("当日反T卖腿市值已达总量帽 %.0f（分母 min(总资产,持仓市值)=%.0f），不再开卖腿", limit, r.capBase)
	}
	return ""
}

// liquidityBlock 流动性前置（§8.4.4b）：任一读数不可得**按不过处理**。
func (r *tLiveRound) liquidityBlock(code string, q *data.StockInfo) string {
	p := r.p
	if p.MinAmountWan > 0 {
		if q.Amount <= 0 {
			return "当日成交额读数不可得，按不过处理"
		}
		if w := q.Amount / 10000.0; w < p.MinAmountWan {
			return fmt.Sprintf("当日成交额 %.0f 万 < %.0f 万", w, p.MinAmountWan)
		}
	}
	if p.MinTurnoverPct > 0 {
		if q.Turnover <= 0 {
			return "换手率读数不可得，按不过处理"
		}
		if q.Turnover < p.MinTurnoverPct {
			return fmt.Sprintf("换手率 %.2f%% < %.2f%%", q.Turnover, p.MinTurnoverPct)
		}
	}
	if strings.Contains(strings.ToUpper(q.Name), "ST") {
		return "ST/退市整理股不做日内回转（±5% 限制 + 停牌风险，止损通道随时关门）"
	}
	if p.MinListedDays > 0 {
		if r.readers.stockMeta == nil {
			return "上市天数无源（未接 stockMeta），按不过处理"
		}
		days, isST, ok := r.readers.stockMeta(code)
		if isST {
			return "ST 股不做日内回转"
		}
		if !ok || days <= 0 {
			return "上市天数读数不可得，按不过处理"
		}
		if days < int(p.MinListedDays) {
			return fmt.Sprintf("上市 %d 天 < %.0f 天（次新股日内振幅常超 10%%，振幅门形同不设）", days, p.MinListedDays)
		}
	}
	return ""
}

// intradayBlock 日内位置：振幅门 → 近极值带 → 涨/跌停距离（偏离门已在方向判定里过）。
func (r *tLiveRound) intradayBlock(code string, q *data.StockInfo, v float64) string {
	p := r.p
	base := q.PrevClose
	if base <= 0 {
		base = q.Close
	}
	if base <= 0 || q.High <= 0 || q.Low <= 0 || q.High < q.Low {
		return "日内 High/Low/PrevClose 读数不完整"
	}
	if amp := (q.High - q.Low) / base * 100; amp < p.MinAmplitudePct {
		return fmt.Sprintf("当日振幅 %.2f%% < 门槛 %.2f%%（振幅不够就走不完价差，开了也配不上对）", amp, p.MinAmplitudePct)
	}
	dev := (q.Price - v) / v * 100
	if dev < 0 && q.Price < q.Low*(1+p.NearExtremePct/100) {
		return "正T位置门：现价已砸到日内低点附近，不接正在砸向低点的刀"
	}
	if dev > 0 && q.Price < q.High*(1-p.NearExtremePct/100) {
		return "反T位置门：涨过又回落过半，不在近高点区，不做半山腰卖"
	}
	if p.LimitDistancePct > 0 {
		up := tLRound2(base * (1 + paper.LimitUpPct(code, q.Name)/100))
		down := tLRound2(base * (1 - paper.LimitUpPct(code, q.Name)/100))
		if up > 0 && q.Price >= up*(1-p.LimitDistancePct/100) {
			return "逼近涨停：反T卖腿可能封板、正T也买不进，本票本轮不做"
		}
		if down > 0 && q.Price <= down*(1+p.LimitDistancePct/100) {
			return "逼近跌停：正T买腿买得到也卖不掉，本票本轮不做"
		}
	}
	return ""
}

// vminBlock 分钟链一致性校验（§8.4.1 校验腿）：两个读数不一致时至少有一个不可信，不动资金。
// 分钟链自己取不到只降级、不拦——它不该成为日内操作的新单点依赖。
func (r *tLiveRound) vminBlock(code string, v float64) string {
	if r.readers.minuteBasis == nil || v <= 0 {
		return ""
	}
	vm, ok := r.readers.minuteBasis(code)
	if !ok || vm <= 0 {
		return ""
	}
	if dev := math.Abs(vm-v) / v * 100; dev > r.p.VWAPCheckTolPct {
		return fmt.Sprintf("分时均线校验不一致：主源%.3f vs 分钟链%.3f 偏差%.2f%%>%.2f%%", v, vm, dev, r.p.VWAPCheckTolPct)
	}
	return ""
}

// signalBlock 信号侧（§8.4.2）：正 T 要新且活跃的买入信号；反 T 是否决门（有卖出裁决或贴着止盈止损线就不做）。
// 两侧语义刻意相反：正 T 缺信号＝不作为（安全），反 T 缺信号＝照做（减敞口）。
func (r *tLiveRound) signalBlock(code string, price, v float64) string {
	dev := (price - v) / v * 100
	if dev < 0 {
		if !r.p.BuyFirst {
			return "正T方向未开（t.mode）"
		}
		if r.readers.newBuySignal == nil {
			return "正T需要活跃买入信号，但未接 newBuySignal 回调（不作为）"
		}
		id, ok := r.readers.newBuySignal(code, r.st.lastRoundEndedAtOf(code, tLDayStart(r.now)))
		if !ok {
			return "无候选 B 口径的新活跃买入信号（老信号不许反复低吸）"
		}
		if r.st.signalConsumed(id) {
			return "该信号已被本账 T 腿消费（§8.4.3 一次性消费）"
		}
		if r.e != nil && r.e.tStrategicConsumed(id) {
			return "该信号已被战略买单消费，同一判断不买第二次"
		}
		return ""
	}
	if !r.p.SellFirst {
		return "反T方向未开（t.mode）"
	}
	if r.readers.activeSellVerdict != nil && r.readers.activeSellVerdict(code) {
		return "反T否决：已有活跃卖出裁决，交给信号执行（§8.7）"
	}
	if r.readers.stopLineDistance == nil {
		return "反T否决门未接线（无 stopLineDistance 回调），宁可不 T"
	}
	d, ok := r.readers.stopLineDistance(code, price)
	if !ok {
		return "反T否决：止盈/止损线距离读数不可得，按否决处理（fail-closed）"
	}
	if d < r.p.StopGuardPct {
		return fmt.Sprintf("反T否决：距止盈/止损线 %.2f%% < %.2f%%（止盈止损优先于做T）", d, r.p.StopGuardPct)
	}
	return ""
}

// quota 额度门（§8.3 的 T 侧公式）：单票 50% 帽 / T 仓额度 / T 可用现金 / 底仓可卖量 / 留一手。
func (r *tLiveRound) quota(code string, pos store.RealPosition, q *data.StockInfo, v float64, buyDir, sellDir bool) (string, int, string) {
	price := q.Price
	if price <= 0 {
		return "", 0, "无有效现价"
	}
	capQty := tLLotFloor(paper.T0Cap(pos.Qty, r.p.CapPct/100.0))
	if capQty < tLMinBaseKeep {
		return "", 0, fmt.Sprintf("单票 T 帽（%.0f%%）不足 1 手", r.p.CapPct)
	}
	// T 仓额度 TQ = 总资产 × max(0, 1 − min(MS,1))：额度是"最多能开多少"，与门槛相互独立。
	tqQty := 0
	if ms := math.Min(r.p.MarketScale, 1.0); ms < 1 {
		tqQty = tLLotFloor(int((r.total * (1 - ms)) / price))
	}
	if buyDir {
		// T 可用现金 = 可用现金 − 战略剩余空间 − 反 T 冻结专款：先给战略留足，剩下的才是 T 的。
		cashT, why := r.cashForT()
		if why != "" {
			return "", 0, why
		}
		qty := tLMinLot(capQty, tqQty, tLLotFloor(int(cashT/(price*(1+r.p.FeeBufferPct/100)))))
		if qty < tLMinBaseKeep {
			return "", 0, fmt.Sprintf("正T额度截到 %d 股（单票帽 %d / TQ %d / 现金可负担额度后不足 1 手）",
				qty, capQty, tqQty)
		}
		// 配对前提：当日买入的机动仓 T+1 不可卖，第二腿只能卖原有底仓 ⇒ 底仓可卖量必须覆盖本腿，
		// 否则开出来就是一条注定配不上的半态腿（§8.2「底仓可卖量不足即半态」）。
		sellable := r.db.BuyableQtyForUserSell(r.uid, pos.TsCode, r.dash)
		if sellable < qty {
			return "", 0, fmt.Sprintf("底仓可卖量 %d < 计划买腿 %d（T+1 下配不上对，不开半态腿）", sellable, qty)
		}
		return tLDirBuy, qty, ""
	}
	// 反 T 卖腿：留一手（≥1 手）+ 卖后 ≥50% 原持仓 + 单票 50% 帽 + T 仓额度 TQ + 当日总量帽余量。
	// TQ 同样约束卖腿（§8.1 表：MS=1.0 时"反 T 也无额度"）——回转的两条腿是同一笔机动额度，
	// 只给买腿上帽会让"满仓市"里还能高抛底仓、把 T 变成第二条减仓通道。
	maxSell := capQty
	if tqQty <= 0 {
		return "", 0, fmt.Sprintf("T 仓额度为 0（MS=%.2f 已达目标仓位上限），两向都不开回转", r.p.MarketScale)
	}
	if tqQty < maxSell {
		maxSell = tqQty
	}
	if keep := tLLotFloor(pos.Qty / 2); keep < maxSell {
		maxSell = keep
	}
	if halfKeep := pos.Qty - tLMinBaseKeep; halfKeep < maxSell {
		maxSell = halfKeep
	}
	if r.p.IntradaySellCapTotalPct > 0 && r.capBase > 0 {
		remain := r.capBase*r.p.IntradaySellCapTotalPct/100 - r.st.sellFirstNotional(r.day)
		if byCap := tLLotFloor(int(remain / price)); byCap < maxSell {
			maxSell = byCap
		}
	}
	sellable := r.db.BuyableQtyForUserSell(r.uid, pos.TsCode, r.dash)
	if sellable < maxSell {
		maxSell = sellable
	}
	qty := tLLotFloor(maxSell)
	if qty < tLMinBaseKeep {
		return "", 0, fmt.Sprintf("反T额度截到 %d 股（留一手+卖后≥50%%原持仓+单票帽+总量帽余量）", qty)
	}
	return tLDirSell, qty, ""
}

// cashForT T 可用现金（§8.3）：可用现金 − 战略剩余空间 − 反 T 冻结专款。
func (r *tLiveRound) cashForT() (float64, string) {
	if !r.cashOK {
		return 0, "可用资金口径不可得（fail-close）"
	}
	room := r.strategicRoom()
	cashT := r.cash - room - r.book.FrozenCash
	if cashT < 0 {
		cashT = 0
	}
	if cashT < r.p.FeeBufferPct {
		return 0, fmt.Sprintf("T 可用现金 %.0f 不足 1 手（战略剩余空间 %.0f 与反 T 冻结专款 %.0f 优先）",
			cashT, room, r.book.FrozenCash)
	}
	return cashT, ""
}

// strategicRoom 目标剩余空间（Room）：total×min(MS,天花板) − 战略当前仓位。
// 与 dynamicBuyQty 同一套口径（正 T 买腿剔除、反 T 卖腿加回），但这里只要 Room 这个数——
// 不调 dynamicBuyQty 是因为它含"单笔基准 10%×SS"与行业/单票额度，那是战略建仓的帽，
// T 腿刻意不占它（§8.3 隔离纪律 1），拿来缩 T 腿会把日内手法当成第二套战略预算。
func (r *tLiveRound) strategicRoom() float64 {
	_, _, room, _ := r.targetCurRoom()
	return room
}

// targetCurRoom 目标 / 战略当前 / 剩余（一次算出，供 T 可用现金与「今日资金」读数共用）。
//
// 为什么抽出来：同一个式子若在 strategicRoom 和资金帧里各写一遍，某天调了 T 腿净额口径
// 就会出现"资金卡显示剩余 3 万、T 腿按剩余 2 万算"的双账；抽成一个出口后，
// 面板看到的和开腿用的是同一组数。
// 返回 ok=false 表示在途委托读数不可得（此时 room 仍按"只算持仓"给保守值，调用方自行决定是否动钱）。
func (r *tLiveRound) targetCurRoom() (target, cur, room float64, ok bool) {
	sc := r.e.sizingConfigFor(r.uid)
	ceiling := sc.MaxTotalPositionPct
	pend, err := r.db.PendingBuys(r.uid, data.TradingDayDate(r.now))
	if err != nil {
		// 在途读不到时不放大 T 额度，也不把战略空间当已花（与旧 strategicRoom 同口径）：
		// 这一分支**不套 T 腿净额**（旧实现是提前 return，腿净额在其后），保持口径逐字不变。
		target, cur, room, _ = fundsTargetCurRoom(r.total, r.p.MarketScale, ceiling, r.positions, r.prices, nil, tLiveBook{})
		return target, cur, room, false
	}
	return fundsTargetCurRoom(r.total, r.p.MarketScale, ceiling, r.positions, r.prices, pend, r.book)
}

// ── 开腿与落账 ────────────────────────────────────────────────────────────────────────

// openLeg 登记回转并下第一腿。
//
// **先落账后下单**：状态账本是"意图"，orders/fills 是"事实"；反过来会在"下单已受理、落账前崩溃"
// 的窗口里留下一条没人认领的委托。先落账的代价是可能留下幽灵 open，而那条路径有对账收敛（锁 13）。
// 同步下单被明确拒绝时立刻标终态（不留半态）；异步排队时保持 open，由下一轮对账裁定。
func (r *tLiveRound) openLeg(code, dir string, qty int) {
	// B1 时延读数：判定时刻＝进入开腿落账；"委托落库"在本层只能观察到受理（异步队列入队或网关返回），
	// 柜台真正入库的时刻在网关侧不可观测——这里记的是**我们能诚实拿到的那一段**，注释同源。
	decidedAt := time.Now()
	pos := r.positions[code]
	q := r.quotes[code]
	price := q.Price
	v, _ := data.VWAPOf(q)
	vmin := 0.0
	if r.readers.minuteBasis != nil {
		vmin, _ = r.readers.minuteBasis(code) // 取不到留 0：记录里 0 就是"没有校验读数"，不伪造
	}
	if r.st.hasOpenRound(code) {
		// 双保险：判定链已经查过一次，这里再查一次是因为开腿与配对之间可能已被别的轮次插入。
		r.st.noteBlock("step1", "并发下该票已有在途腿", code)
		return
	}
	seq := r.st.tRoundCountFor(code) + 1
	id := fmt.Sprintf("%s%s:%s:%s:%d", tLKeyPrefix, code, dir, r.day, seq)
	rnd := &TRoundLive{
		ID: id, Day: r.day, Code: code, Name: q.Name, Dir: dir, Status: tLStatusOpen, Qty: qty,
		AnchorPrice:   pos.AnchorPrice,
		BaseQty:       pos.Qty,
		UsedSignalID:  "",
		VWAP:          tLRound3(v),
		Vmin:          tLRound3(vmin),
		DevPct:        tLRound2((price - v) / v * 100),
		High:          q.High,
		Low:           q.Low,
		AmplitudePct:  tLRound2(r.amplitudePct(q)),
		AmountWan:     tLRound2(q.Amount / 10000),
		TurnoverPct:   tLRound2(q.Turnover),
		MarketScaleAt: r.p.MarketScale,
		OpenedAt:      r.now.Format(time.RFC3339),
		// B1：决策价与参与率在开腿瞬间落库。参与率分母＝**当时已累计**的当日成交额（q.Amount），
		// 不是全天量——盘中的"全天量"是未来数据，用它当分母会系统性低估参与率。
		DecisionPrice: tLRound4(price),
	}
	if q.Amount > 0 {
		rnd.ParticipationPct = tLRound4(price * float64(qty) / q.Amount * 100)
	}
	if dir == tLDirSell {
		// 冻结回购专款 = 现价×量 + 费用缓冲：买回价高于卖价时也要有钱等着，
		// 否则"尾盘有信号却不回补"会变成现金不足造成的半态，而不是一次决策（评审 P1-1）。
		buf := r.p.FeeBufferPct
		if buf <= 0 {
			buf = tLFeeBufferPctDefault
		}
		rnd.FrozenCash = tLRound2(float64(qty) * price * (1 + buf/100))
	} else if r.readers.newBuySignal != nil {
		// 正 T 开腿即消费信号（一次性消费，§8.4.3 ①）；反 T 由规则发起，不占信号。
		if id2, ok := r.readers.newBuySignal(code, r.st.lastRoundEndedAtOf(code, tLDayStart(r.now))); ok {
			rnd.UsedSignalID = id2
		}
	}
	if err := r.st.put(rnd); err != nil {
		log.Printf("%s 回转落账失败 %s：%v（不落账不下单）", tLLogTag, id, err)
		return
	}
	log.Printf("%s 开腿 %s %s(%s) %d股@%.2f 底仓%d股 V=%.3f Vmin=%.3f 偏差%.2f%% 振幅%.2f%% 成交额%.0f万 换手%.2f%% MS=%.3f 信号=%s 冻结=%.0f",
		tLLogTag, id, dir, q.Name, qty, price, pos.Qty, v, vmin, rnd.DevPct, rnd.AmplitudePct,
		rnd.AmountWan, rnd.TurnoverPct, r.p.MarketScale, orDashTL(rnd.UsedSignalID), rnd.FrozenCash)

	var err error
	if dir == tLDirBuy {
		err = r.buyLeg(rnd, id, qty, price, "正T买入腿")
	} else {
		_, err = r.sellLeg(pos, qty, id, price, "反T卖出腿（高抛）")
	}
	// 判定 → 委托受理的毫秒（无论成否都记：被拒的时延同样是执行链路的读数）。
	rnd.LatencyMs = int(time.Since(decidedAt).Milliseconds())
	if err != nil {
		// 开腿被拦 ⇒ 不留半态：立即标终态并释放冻结款（§8.4.6 step8「被拦 → 不开，不留半态」）。
		// 不重试、不换键重放：重试就是同一判断买/卖两次，换键重放则绕过了 orders 唯一键这层去重。
		r.st.noteBlock("step8", "开腿被拒: "+err.Error(), code)
		r.finish(rnd, tLStatusUnwound, "开腿被拦（不留半态）："+err.Error())
		return
	}
	// 时延是下单之后才有的读数：补一次落盘（覆盖率上 fail-open——时延丢了不影响判定，
	// 但丢了"这一腿从判定到受理用了多久"这个证据，滑点归因就少一半）。
	if perr := r.st.put(rnd); perr != nil {
		log.Printf("%s 时延读数落盘失败 %s：%v", tLLogTag, id, perr)
	}
	if dir == tLDirBuy && rnd.UsedSignalID != "" && r.e != nil {
		r.e.tMarkTConsumed(rnd.UsedSignalID)
	}
}

func (r *tLiveRound) amplitudePct(q *data.StockInfo) float64 {
	base := q.PrevClose
	if base <= 0 {
		base = q.Close
	}
	if base <= 0 || q.High < q.Low {
		return 0
	}
	return (q.High - q.Low) / base * 100
}

// buyLeg 买腿下单：与战略买入同一条队列、同一套幂等键（orders.signal_id 唯一键）。
// IntradayT=true 让集中度按净额计（§8.11 / W8-A：复核在 risk 层，本层不重复实现）；
// Strategy 留空＝T 是执行手法不是战法（Q12），因此不进白名单判定、不进战法统计。
func (r *tLiveRound) buyLeg(rnd *TRoundLive, key string, qty int, price float64, why string) error {
	orderPrice := tLRound2(price + tLTick) // 现价 + 1 个最小变动价位（§C2 方案 C：保成交优先）
	cfg := r.ctrl.Config()
	// VWAP 四列按"有就记、没有留空"的姿势装配（与战略买入同口径）：缺失时保持 0/空，
	// 绝不为了"看着有数"把现价写进 market_vwap——那会把灰度期的 VWAP 归因变成假证据。
	vwap, vwapOK := data.VWAPOf(r.quotes[rnd.Code])
	vwapSource, vwapAsOf := "", ""
	if vwapOK {
		vwapSource = "quote"
		vwapAsOf = r.now.Format(time.RFC3339)
	}
	req := trading.OrderRequest{
		SignalID:  key,
		Code:      withSuffix(rnd.Code),
		Name:      rnd.Name,
		Side:      trading.SideBuy,
		PriceType: "limit",
		Price:     orderPrice,
		Qty:       qty,
		Amount:    float64(qty) * orderPrice,
		CreatedAt: r.now.Format(time.RFC3339),
		// 资金口径不可得时上游已 fail-closed（canOpen=false / 尾盘买回走许可判定），这里只补新鲜度留痕。
		StalenessMs:          r.e.quoteStalenessMs(rnd.Code),
		CurrentPrice:         price,
		PrevClose:            tLPrevCloseOf(r.quotes[rnd.Code]),
		MarketVWAP:           vwap,
		VWAPSource:           vwapSource,
		VWAPAsOf:             vwapAsOf,
		SignalVWAPPremiumPct: tLPremiumPct(orderPrice, vwap, vwapOK),
		// §DYN-SIZE-v3 同姿势：动态预算已把额度算好，旧的持仓数/每日次数/战法分配闸让位。
		DynamicBudget: true,
		// §DYN-T 批 8（不变量 12）：这一腿是日内回转，不是战略建仓——集中度按净额计（净额化的
		// 复核在 risk 层已完成，本层不重复实现也不许改它）。
		IntradayT: true,
	}
	req.PriceType = tLFirstNonEmpty(cfg.PriceType, "limit")
	sig := combat_agent.Signal{ID: key, Code: rnd.Code, Name: rnd.Name, Direction: "做多", Action: "buy", Price: price}
	r.e.mu.RLock()
	ch := r.e.buyCh
	r.e.mu.RUnlock()
	if ch != nil {
		select {
		case ch <- buyTask{req: req, sig: sig}:
			log.Printf("%s %s 已入异步买单队列 %s %d股@%.2f", tLLogTag, why, key, qty, orderPrice)
			return nil
		default:
			// 队列满：与 autoPlaceScaled 的 FIX#8 同语义，宁阻塞当前轮也不丢这条腿——
			// 丢弃会让账本留下一条永远等不到回报的 open。
			log.Printf("%s 买单队列满 ⇒ 同步兜底 %s %s", tLLogTag, why, key)
		}
	}
	res, err := r.ctrl.PlaceOrder(req)
	if err != nil {
		return err
	}
	if res != nil && !res.OK {
		if strings.HasPrefix(res.Err, "duplicate") {
			// 同键当日已有委托：幂等命中＝这条腿已经报过了，按"已下"处理（不重复动钱）。
			return nil
		}
		reject := res.Err
		if reject == "" {
			reject = "网关未受理"
		}
		return fmt.Errorf("%s", reject)
	}
	log.Printf("%s %s 已报 %s %d股@%.2f order=%s", tLLogTag, why, key, qty, orderPrice, tLOrderID(res))
	return nil
}

// sellLeg 卖腿下单：复用实盘部分卖原语 sellRealPosition（不走全平 delete+冷却分支）。
// class 参数刻意不带 止损/止盈/m8 任何一个全平类名——T 腿不得进入全平类的聚合口径
// （realSoldOrOpenQtyToday 按类累加，混进去等于让一次回转吞掉当日真正的退出裁决）。
func (r *tLiveRound) sellLeg(pos store.RealPosition, qty int, key string, price float64, why string) (int, error) {
	if qty <= 0 {
		return 0, nil
	}
	placed, err := r.e.sellRealPosition(r.ctrl, pos, qty, key, price, "做T", why, true)
	if err != nil {
		return placed, err
	}
	if placed <= 0 {
		return placed, fmt.Errorf("卖出未受理（qty/price 守卫）")
	}
	return placed, nil
}

// ── 终态与「做 T 收益」 ───────────────────────────────────────────────────────────────

// finish 收尾一条当日回转：置终态、算收益、**一律释放冻结款**、落盘、打日志。
// FrozenCash 的释放是资金面的硬要求：目标仓位模型按 bookValue 扣它，
// 一条忘了释放的腿会让这笔钱在整个账户里永久"被借走"，账户系统性欠用现金。
func (r *tLiveRound) finish(rnd *TRoundLive, state, reason string) {
	if rnd.Status == tLStatusDone || rnd.Status == tLStatusUnwound || rnd.Status == tLStatusConvert {
		return // 已终态：不重复收尾（重复落盘会把 ClosedAt 与收益口径搅浑）
	}
	rnd.Status = state
	rnd.ClosedAt = r.now.Format(time.RFC3339)
	if state != tLStatusDone {
		rnd.UnwindReason = reason
	}
	if state == tLStatusDone && r.roundMatched(rnd) {
		r.fillPnl(rnd)
	}
	// B1：首腿滑点在终态补一次（成交价来自对账，开腿那一刻还没有），未成交腿留原因码。
	r.refreshSlip(rnd)
	r.markUnfilled(rnd)
	rnd.FrozenCash = 0
	if err := r.st.put(rnd); err != nil {
		log.Printf("%s 终态落盘失败 %s：%v", tLLogTag, rnd.ID, err)
	}
	net, gross := rnd.TPnlNet, rnd.TPnlGross
	log.Printf("%s 回转 %s %s(%s) 终态=%s 买%d@%.3f 卖%d@%.3f 毛%.2f 净%.2f 费用估算=%v 原因=%s",
		tLLogTag, rnd.ID, rnd.Dir, rnd.Name, state, rnd.BuyFilled, rnd.BuyPrice, rnd.SellFilled, rnd.SellPrice,
		gross, net, rnd.FeeEst, reason)
}

// fillPnl 「做 T 收益」独立口径（§8.9a / 六轮 T6-5）：
//   - 只有配平的轮算收益，converted/unwound 的仓位变更属战略侧，计进去就是把日内手法虚增成收益；
//   - q = min(买腿成交量, 卖腿成交量)，未成交部分不参与盈亏；
//   - 费用优先取券商成交回报，取不到才按纸面同公式估算并标 FeeEst=true；
//   - **不回写任何基准**：这两个数不进 EntryPrice/持仓成本，也不进卖出裁决的分母。
//
// B1（§2.2）：未成交的机会成本**绝不**进 t_pnl_net。净额公式固定为
// gross − fee_buy − fee_sell − stamp_tax；反事实只在 B2 的离线标定里估。
func (r *tLiveRound) fillPnl(rnd *TRoundLive) {
	q := rnd.BuyFilled
	if rnd.SellFilled < q {
		q = rnd.SellFilled
	}
	if q <= 0 || rnd.BuyPrice <= 0 || rnd.SellPrice <= 0 {
		return
	}
	gross := (rnd.SellPrice - rnd.BuyPrice) * float64(q)
	buyAmt := rnd.BuyPrice * float64(q)
	sellAmt := rnd.SellPrice * float64(q)
	feeBuy, feeSell, stamp, source := r.roundFee(rnd, buyAmt, sellAmt)
	rnd.QMatched = q
	rnd.TPnlGross = tLRound2(gross)
	// 先落分项、再按**分项之和**算净额：这样 |gross − 三项和 − net| < 0.01 由构造保证，
	// 判据 1 验的是"分项确实被算进去了"，而不是一个恒等式（反证：滑点置 0 时它会红）。
	rnd.FeeBuy, rnd.FeeSell, rnd.StampTax = tLRound2(feeBuy), tLRound2(feeSell), tLRound2(stamp)
	rnd.TPnlNet = tLRound2(rnd.TPnlGross - (rnd.FeeBuy + rnd.FeeSell + rnd.StampTax))
	rnd.FeeSource = source
	rnd.FeeEst = source != tLFeeSourceBroker
}

// roundFee 该轮两腿费用分项：**逐腿**取券商回报，哪条腿没有就用估算补哪条腿，
// 并在来源不一致时标 mixed——把"一条实测+一条估算"混成一个 broker 数是本批明令禁止的混算。
// 返回 (买腿佣金, 卖腿佣金, 卖腿印花税, 来源 broker/estimate/mixed)。
func (r *tLiveRound) roundFee(rnd *TRoundLive, buyAmt, sellAmt float64) (feeBuy, feeSell, stamp float64, source string) {
	first := r.fact(rnd.ID)
	pair := r.pairFact(rnd.ID)
	// 首腿／配对腿各自对应买还是卖，由方向决定（正 T 首腿买、反 T 首腿卖）。
	buyFact, sellFact := first, pair
	if rnd.Dir == tLDirSell {
		buyFact, sellFact = pair, first
	}
	// 估算腿（与 paper.buyFeeLocked/sellFeeLocked 同式）：佣金 max(额×0.025%, 5)，印花税 0.05%。
	estBuy := math.Max(buyAmt*tLCommissionRate, tLMinCommission)
	estSell := math.Max(sellAmt*tLCommissionRate, tLMinCommission)
	estStamp := sellAmt * tLStampTaxRate
	buyBroker := buyFact != nil && buyFact.hasFee
	sellBroker := sellFact != nil && sellFact.hasFee
	if buyBroker {
		feeBuy = buyFact.comm
	} else {
		feeBuy = estBuy
	}
	if sellBroker {
		feeSell, stamp = sellFact.comm, sellFact.stamp
	} else {
		feeSell, stamp = estSell, estStamp
	}
	switch {
	case buyBroker && sellBroker:
		source = tLFeeSourceBroker
	case !buyBroker && !sellBroker:
		source = tLFeeSourceEstimate
	default:
		source = tLFeeSourceMixed
	}
	if math.IsNaN(feeBuy+feeSell+stamp) || feeBuy < 0 || feeSell < 0 || stamp < 0 {
		return 0, 0, 0, tLFeeSourceEstimate
	}
	return feeBuy, feeSell, stamp, source
}

// 费用来源三态（与纸面同名，前端/日汇总按字面量读）。
const (
	tLFeeSourceBroker   = "broker"
	tLFeeSourceEstimate = "estimate"
	tLFeeSourceMixed    = "mixed"
)

// refreshSlip 用**首腿成交均价**对决策价算滑点（B1 判据 2 的数据来源）。
// 只锚首腿：配对腿由价差阈值触发，没有"判定报价"；给它编一个决策价等于把两件事焊在一起。
func (r *tLiveRound) refreshSlip(rnd *TRoundLive) {
	if rnd == nil || rnd.DecisionPrice <= 0 {
		return
	}
	px, qty := rnd.BuyPrice, rnd.BuyFilled
	if rnd.Dir == tLDirSell {
		px, qty = rnd.SellPrice, rnd.SellFilled
	}
	if qty <= 0 || px <= 0 {
		return
	}
	v := (px - rnd.DecisionPrice) / rnd.DecisionPrice * 1e4
	if rnd.Dir == tLDirSell {
		v = -v // 卖腿：成交低于决策价才是对我不利（正数）
	}
	rnd.SlipBps = tLRound2(v)
}

// markUnfilled 终态时归纳"下了单但没成交/被撤"的腿及原因码。
// **只记事实，不算机会成本**：反事实（若成交能赚多少）不可观测，只在 B2 离线标定里估（§2.2）。
func (r *tLiveRound) markUnfilled(rnd *TRoundLive) {
	if rnd == nil {
		return
	}
	first := r.fact(rnd.ID)
	pair := r.pairFact(rnd.ID)
	legs := []*tLiveLegFact{first, pair}
	rnd.Unfilled, rnd.UnfilledReason = false, ""
	for _, f := range legs {
		if f == nil || f.qty > 0 || len(f.orders) == 0 {
			continue // 没下过单不算"未成交"；成交了当然也不算
		}
		rnd.Unfilled = true
		rnd.UnfilledReason = tLUnfilledReasonOf(f)
		return
	}
}

// tLUnfilledReasonOf 从委托终态归纳原因码：canceled（撤单）/ rejected（废单）/ timeout（超时未成）/ no_fill。
// 顺序上"撤/废"优先于"超时"：一条被撤的单也是"等了很久"，但它有更具体的原因。
func tLUnfilledReasonOf(f *tLiveLegFact) string {
	for _, o := range f.orders {
		switch {
		case strings.Contains(o.Status, "撤"):
			return "canceled"
		case strings.Contains(o.Status, "废") || strings.Contains(o.Status, "拒"):
			return "rejected"
		}
	}
	return "timeout"
}

// ── 供判定链使用的引擎侧读数 ──────────────────────────────────────────────────────────

// strategicBusy 该票有未执行的战略裁定或在途战略买单 ⇒ T 让路（§8.7「信号执行 > 做 T」）。
// 裁定表 liveDecisions 只存非 pass 的留痕项，pass 的已经在本轮 dispatch 里被执行；
// 两种形态都要让路：前者说明"这件事还轮到执行层判"，后者说明"本轮已经在动这笔钱了"。
func (r *tLiveRound) strategicBusy(code string) bool {
	pure := pureTsCode(code)
	r.e.mu.RLock()
	for k := range r.e.liveDecisions {
		if strings.HasPrefix(k, pure+"@") {
			r.e.mu.RUnlock()
			return true
		}
	}
	r.e.mu.RUnlock()
	for key, rows := range r.ordersCache[r.day] {
		if !strings.HasPrefix(key, "buy:"+pure+":") {
			continue
		}
		for _, o := range rows {
			if tLOpenOrderStatus(o.Status) {
				return true
			}
		}
	}
	return false
}

// tLiveStopLineDistance 现价距止盈/止损线的最小距离%（反 T 否决门，实盘版）。
//
// 与纸面 tStopLineDistanceOf 同判据但取数不同：那条读 paper.Position.AnchorPrice，这条读
// real_positions.anchor_price——实盘侧的锚点与纸面账本没有任何关系，串库比串数更糟。
// 基准刻意用锚点而非成本：拿摊薄成本凑数会正中本批要修的那个洞（T 腿摊薄成本 ⇒ 判线漂移）。
func (e *Engine) tLiveStopLineDistance(uid, code string, price float64) (float64, bool) {
	if price <= 0 {
		return 0, false
	}
	e.mu.RLock()
	db := e.realStore
	e.mu.RUnlock()
	if db == nil || uid == "" {
		return 0, false
	}
	pos, err := db.RealPositionByCodeForUser(uid, withSuffix(code))
	if err != nil || pos.AnchorPrice <= 0 {
		return 0, false
	}
	slPct := e.disciplineStopLossPct(uid)
	tpPct := e.disciplineTakeProfitPct(uid)
	if slPct <= 0 && tpPct <= 0 {
		return 0, false
	}
	best := -1.0
	if slPct > 0 {
		d := (price - pos.AnchorPrice*(1-slPct/100)) / price * 100
		if d < 0 {
			d = 0 // 已破线：距离 0 ⇒ 否决门必然命中（该由裁决处理，不是由 T 处理）
		}
		best = dMin(best, d)
	}
	if tpPct > 0 {
		d := (pos.AnchorPrice*(1+tpPct/100) - price) / price * 100
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

// tLiveOppositeLeg 跨账号同码反向在途查询（§8.4.5 对手方不变量，合规红线：对敲不申请、不做）。
// 读不到状态时按互斥命中（fail-closed）：当前生产只有一个账号，这条落码是为了
// "以后加账号时不留未验证路径"，而不是等第二个账号出现时才现补。
func (e *Engine) tLiveOppositeLeg(selfUID, code, dir, day string) (bool, string) {
	e.mu.RLock()
	rg := e.tLiveReg
	e.mu.RUnlock()
	if rg == nil {
		return true, "跨账号回转状态不可得（无账本根目录），按互斥命中"
	}
	return rg.oppositeOpenLeg(selfUID, code, dir, day)
}

// ── 小工具 ────────────────────────────────────────────────────────────────────────────

// tLHM 北京时 HHMM（字符串比较即时间比较，与纸面同口径）。
func tLHM(t time.Time) string { return cntime.In(t).Format("1504") }

// tLDayStart 当日 09:30（候选 B "今天新出的信号才算数"的基准）。
func tLDayStart(t time.Time) time.Time {
	cst := cntime.In(t)
	return time.Date(cst.Year(), cst.Month(), cst.Day(), 9, 30, 0, 0, cst.Location())
}

// tLInCooling 开盘冷静期内（默认 9:30 起 15 分钟）：此刻 V 与现价几乎重合，偏离门失效。
func tLInCooling(t time.Time, minutes float64) bool {
	if minutes <= 0 {
		return false
	}
	return cntime.In(t).Before(tLDayStart(t).Add(time.Duration(minutes) * time.Minute))
}

// tLLotFloor 向下取整到整手；不足 1 手返回 0（两腿都必须整手，零股只能清仓卖出）。
func tLLotFloor(qty int) int {
	if qty < tLMinBaseKeep {
		return 0
	}
	return qty / tLMinBaseKeep * tLMinBaseKeep
}

// tLMinLot 取"非负候选"里的最小值；全是负数（＝该维度不可得）返回 0，调用方按 0 处理成不放行。
// 为什么不用现成的 sort/math.Min：调用点传的是三个不同来源的量（可卖量/单票帽/总量帽余量），
// 任一读不到时是 -1 而不是 0——0 在这里是合法值（"就是不能再卖"），必须与"没读数"区分开。
// English: min over non-negative candidates; -1 means "reading unavailable" and is skipped, so an
// all-(-1) input returns 0 — the caller treats "no reading" as "no room", never as a free pass.
func tLMinLot(vals ...int) int {
	best := -1
	for _, v := range vals {
		if v < 0 {
			continue
		}
		if best < 0 || v < best {
			best = v
		}
	}
	if best < 0 {
		return 0
	}
	return best
}

// tLOpenOrderStatus 委托是否仍可能继续成交（与 store.SumOpenSellQty 的同名口径同源）。
func tLOpenOrderStatus(status string) bool {
	switch status {
	case "已报", "部成", "已报待撤", "部成待撤", "未报", "待报":
		return true
	default:
		return false
	}
}

func tLRound2(v float64) float64 { return math.Round(v*100) / 100 }

// tLRound3 三位小数：分时均线类读数要能看出分厘差，两位会把 V 抹成现价。
func tLRound3(v float64) float64 { return math.Round(v*1000) / 1000 }

// tLRound4 四位小数：参与率常在 0.01% 量级（B1/B4 的观察量），两位会把整列抹成 0。
func tLRound4(v float64) float64 { return math.Round(v*10000) / 10000 }

func tLPrevCloseOf(q *data.StockInfo) float64 {
	if q == nil {
		return 0
	}
	if q.PrevClose > 0 {
		return q.PrevClose
	}
	return q.Close
}

func tLFirstNonEmpty(vals ...string) string {
	for _, s := range vals {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// tLPremiumPct 委托价相对分时均线的溢价%（orders 归因列，与战略买入同口径）；V 不可得时留 0。
func tLPremiumPct(orderPrice, v float64, ok bool) float64 {
	if !ok || v <= 0 {
		return 0
	}
	return tLRound2((orderPrice - v) / v * 100)
}

func tLOrderID(res *trading.OrderResult) string {
	if res == nil {
		return ""
	}
	return res.OrderID
}

func orDashTL(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

// splitTLStep 把 "step4 xxx" 拆成 (step4, xxx)，供留痕按步骤归一（§8.9）。
func splitTLStep(s string) (string, string) {
	if i := strings.Index(s, " "); i > 0 {
		return s[:i], s[i+1:]
	}
	return "step?", s
}

// tLFeeBufferPctDefault 冻结款费用缓冲的兜底值（与 paper.TParams.FeeBufferPct 的出厂值同数）：
// 配置层给不出该值时用它，而不是给 0——0 缓冲会让"差几毛钱买不回"变成一次半态。
const tLFeeBufferPctDefault = 0.6

// ── §W-09 行情门：形态门（入口）+ 量能门（逐票）────────────────────────────────
//
// 两道门回答的是同一个问题的两面："现在这种行情该不该做 T"。
// 现有的 MS 数值门（MinMarketScale / SellEnvMinMarketScale）与大盘急跌门**都留着**，
// 它们管的是"该买多少"与"今天是不是在砸"，表达不了"盘整还是趋势"——
// 牛市里 MS 可以很高（该重仓但不该做 T），震荡市里 MS 也可以中等（该做 T）。

// regimeBlock 行情门·形态（§W-09）：只有盘整行情才做 T；牛/熊一律关闭。
//
// **弃权而不是 fail-closed**：读数为空串（T 行情门关 / 状态机从未产生读数）时放行并出声。
// 若空串按"不通过"处理，默认配置下所有账号的 T 会立刻全停，而现象是"T 不做了"
// 这种最难归因的形态。owner 关掉状态机是显式选择，不能反过来把 T 永久关死。
// 反过来 `default` 分支（未知状态字符串）必须 fail-closed——那不是"没配"，是"配了但值不认识"。
//
// 读数来源是 **T 专属字段** MarketState/MarketStateAt（来自 tRegimeNow，不是定权的 lastMarketState）。
// 读后者等于把 enhance.market_state_enabled 这个全局开关重新绑回 T 门上。
//
// 本函数只管**入口**（不开新腿）。出口由 accelReason 的 "regime" 分支负责，动作是**配平**
// 不是"结掉"——熊市里把正 T 腿结掉＝少卖一笔等量底仓＝敞口反而变大（§W-09「两个出口」）。
// 两个函数共用同一个读数与新鲜度门，但**不许合并**：合并就会有人把"结掉"当成"配平"。
func (r *tLiveRound) regimeBlock() string {
	st, at := r.p.MarketState, r.p.MarketStateAt
	if st != "" && (at.IsZero() || r.now.Sub(at) > tRegimeStale) {
		r.st.noteBlock("step0", fmt.Sprintf(
			"行情门弃权：市场状态读数过期（%q，%.0f 分钟前，票 %d/%d）——主循环可能已停",
			st, r.now.Sub(at).Minutes(), r.p.MarketStateVotes, r.p.MarketStateVotesTotal), "")
		return ""
	}
	switch st {
	case "range":
		// 票数不足时状态机已把结论降级成 range（research/market_state.go 的安全阀）——放行结论不变，
		// 但要留一行，否则复盘时分不清"真震荡"和"票太少"。这就是 owner 第 3 条裁决的全部落地。
		// **零票单独一条**：votes==0 是兜底 range（状态机无任何有效输入），比 2/6 更不可信、
		// 也更安静。写成 `votes > 0 && ...` 就会把它静默放行，于是"数据源全挂"看起来像"真震荡"。
		switch {
		case r.p.MarketStateVotes == 0:
			r.st.noteBlock("step0", "行情门放行（**零票**：状态机无任何有效输入 ⇒ 这是兜底 range，不是真震荡）——先查炸板池/指数斜率/广度数据源", "")
		case r.p.MarketStateVotes < tRegimeMinVotes:
			r.st.noteBlock("step0", fmt.Sprintf(
				"行情门放行（研判质量不足：票 %d/%d < %d ⇒ 状态机已降级为 range，非真震荡）",
				r.p.MarketStateVotes, r.p.MarketStateVotesTotal, tRegimeMinVotes), "")
		}
		return ""
	case "bull", "bear":
		return fmt.Sprintf("行情门：市场状态=%s，非盘整行情不做 T", st)
	case "":
		r.st.noteBlock("step0", "行情门弃权：T 行情门已关（t.regime_gate_enabled=false）或读数从未产生，牛熊关闭 T 未生效", "")
		return ""
	default:
		return fmt.Sprintf("行情门：未知市场状态 %q，按不做 T 处理（fail-closed）", st)
	}
}

// volumeBlock 行情门·量能（§W-09）：放量的票不做 T。
//
// 读数不可得 ⇒ 不做该票的 T——与流动性前置同一纪律（"读不到"不按"没放量"放行）。
// 与 minuteBasis 的降级**刻意不同**（分钟链自己取不到只降级、不拦）：那条是**校验腿**，
// 这条是**准入判据**。判据缺了就不能判"通过"，校验腿缺了只是少一道复核。
//
// 只收 code、不收 q：量比是"当日累计量 ÷ 20 日均量"，累计量从读数源里取（与快照同源），
// 这里再传一个 q 会让后人以为用的是 q.Volume 直算（那样就不折算时间窗了，早盘的放量永远测不出来）。
func (r *tLiveRound) volumeBlock(code string) string {
	if r.p.VolRatioMax <= 0 {
		return "" // 0 合法＝不设该条（整条门的开关）
	}
	if r.p.VolRatio == nil {
		return "量比读数无源（未接 VolRatio），按不过处理"
	}
	ratio, ok := r.p.VolRatio(code)
	if !ok || ratio <= 0 {
		return "量比读数不可得，按不过处理"
	}
	if ratio >= r.p.VolRatioMax {
		return fmt.Sprintf("放量：折算量比 %.2f ≥ %.2f（放量行情不做 T）", ratio, r.p.VolRatioMax)
	}
	return ""
}

// ── §W-06-R4 盘中加速配平（三个触发源、一个出口）────────────────────────────────
//
// 三个触发源共用一个出口，因为**动作完全相同**：发第二腿、回到开腿前的敞口、不等价差。
// 分开写三份判断只会让"到底谁触发的"在日志里分不清，而复盘时这三个的成本完全不同
// （止损是成本、让位信号是成本、行情翻转是成本，但成因不同，混在一起会得出错误结论）。

// roundSpreadPctNow 假设此刻按 price 平掉本笔 T，这笔 T 的价差率（%）与本金。
// ok=false：首腿未成交 / 本金 ≤ 0（没有"这笔 T"可算）。
//
// 口径（§W-06-R4 v3，owner 2026-10-29 22:40 定调「做 T 的唯一口径是价差」）：
//
//	价差   = 卖额 − 买额（已配平部分按成交价、未配平部分按 price 折算）
//	本金   = 首腿成交额（正 T 用买额、反 T 用卖额）
//	价差率 = 价差 / 本金 × 100
//
// **不含费用**。费用模型是 fillPnl 的事，混进来就变成第二份口径（v2 版就是这么被推翻的）：
// 门禁 #21 与 ACC-16 一起钉住"本函数体不得出现 roundFee / 费率常量"。
// 正反 T 同一算式：卖额−买额 对两侧都成立，不需要按方向分分支（v1 版两条分支、符号相反，
// 抄反一个就静默失效，那是它被废掉的原因）。
func (r *tLiveRound) roundSpreadPctNow(rnd *TRoundLive, price float64) (pct, notional float64, ok bool) {
	if rnd == nil || price <= 0 {
		return 0, 0, false
	}
	matched := rnd.BuyFilled
	if rnd.SellFilled < matched {
		matched = rnd.SellFilled
	}
	var buyAmt, sellAmt float64
	if rnd.Dir == tLDirBuy {
		if rnd.BuyFilled <= 0 || rnd.BuyPrice <= 0 {
			return 0, 0, false
		}
		buyAmt = rnd.BuyPrice * float64(rnd.BuyFilled)
		sellAmt = rnd.SellPrice*float64(matched) + price*float64(rnd.BuyFilled-matched)
		notional = buyAmt
	} else {
		if rnd.SellFilled <= 0 || rnd.SellPrice <= 0 {
			return 0, 0, false
		}
		sellAmt = rnd.SellPrice * float64(rnd.SellFilled)
		buyAmt = rnd.BuyPrice*float64(matched) + price*float64(rnd.SellFilled-matched)
		notional = sellAmt
	}
	if notional <= 0 {
		return 0, 0, false
	}
	return (sellAmt - buyAmt) / notional * 100, notional, true
}

// accelReason 该腿此刻是否值得"跳过价差门、立即配平"（§W-06-R4 v3）。
// 返回 ""（照常等价差）/ "stop"（本笔价差率跌破止损线）/ "verdict"（有卖出裁决）/
//
//	"regime"（行情已离开 range）。
//
// 这里**只读**，不判"该不该卖"：裁决的生成是 combat_agent 的事，行情判定是状态机的事。
// T 层再判一次"该不该卖"就是第二份卖出逻辑（门禁 #25 钉住本函数不得引 combat_agent/Disposal）。
//
// 腿龄门只加在 "stop" 上（防开腿瞬间的报价抖动被当成止损），不加在 verdict/regime 上——
// 刚开腿就出现裁决或行情翻转，正是最该立刻配平的情形。
func (r *tLiveRound) accelReason(rnd *TRoundLive, price float64) string {
	if rnd == nil {
		return ""
	}
	// S1：本笔止损。与入口的"止损线"同口径（纯价差率），且必须先过**止血门 + 腿龄门**。
	// 止血（!p.Enabled）期间不许走这条：§8.11.5 的止血是"停开新腿、在途腿照常收摊"，
	// 而"因为亏到线主动卖底仓"是一个**新动作**。漏了这个判断 ⇒ ACC-4 红。
	// 注意 verdict/regime 在止血期间**仍然生效**——它们不是止损，是"回到开腿前"。
	if r.p.Enabled && r.p.RoundStopPct > 0 && r.olderThan(rnd, tLPremiseGrace) {
		if pct, _, ok := r.roundSpreadPctNow(rnd, price); ok && pct <= -r.p.RoundStopPct {
			return paper.TAccelStop
		}
	}
	// S2：卖出裁决（TTL 5min，tSellVerdictActive 内部判）。
	if r.e != nil && r.e.tSellVerdictActive(rnd.Code) {
		return paper.TAccelVerdict
	}
	// S3：行情翻转。与 regimeBlock 同一读数、同一新鲜度门（过期的 bear 不许拿来配平）。
	st, at := r.p.MarketState, r.p.MarketStateAt
	if st != "" && !at.IsZero() && r.now.Sub(at) <= tRegimeStale {
		if st == "bull" || st == "bear" {
			return paper.TAccelRegime
		}
	}
	return ""
}

// premiseGone 盘中"必须改终态"的判定（§W-06-R4 v3）。返回 ("", "") 表示"继续按 T 处理"。
//
// **只处理 P3（熔断日反 T）**：那是"买回通道被关、配平做不了"的情形，只能转战略减仓。
// 其余三个触发源（stop/verdict/regime）都是"提前配平"，在 accelReason 里，不走这里。
// **不许把它们留一份在这里**——那就有两个入口，同一 tick 里会抢同一笔腿；
// 更致命的是写在这里的动作是"结掉"（不卖底仓），熊市里等于**敞口变大**（§W-09「两个出口」）。
//
// 守卫（每一条都是资损方向）：
//  1. 尾盘（force=true）⇒ 不判：尾盘那条路是为"不隔夜"设计的，动作与文案都不同；
//  2. 第二腿在途 ⇒ 不判（撤单与成交有竞态，早转会造无主成交）；
//  3. 腿龄 < tLPremiseGrace ⇒ 不判（报价抖动 + 成交回填延迟会拿错价格基准）；
//  4. t.enabled=false 的止血轮 ⇒ 不判（止血停新动作，不是提前结掉在途腿）。
func (r *tLiveRound) premiseGone(rnd *TRoundLive) (state, why string) {
	if r.force || !r.p.Enabled || r.hasPendingPair(rnd) || !r.olderThan(rnd, tLPremiseGrace) {
		return "", ""
	}
	// P3：熔断日。尾盘的 allowBuyBack 对 dayBlocked 一律封买回通道，盘中不提前封会自相矛盾
	// （同一笔腿"盘中买回、尾盘不许买回"）。只对反 T：正 T 在熔断日提前结掉＝不卖底仓＝敞口更大。
	if rnd.Dir == tLDirSell && r.dayBlocked && rnd.SellFilled > 0 {
		return tLStatusUnwound, "盘中收摊：熔断日买回通道关闭 ⇒ 反T卖腿转战略减仓（与尾盘 allowBuyBack 的 dayBlocked 分支同口径）"
	}
	return "", ""
}

// tLAccelSuffix 失败文案的加速标签后缀（§W-06-R4）。空标签返回空串，
// 于是非加速路径的既有文案**逐字不变**（ACC-12 的反向断言依赖这一点）。
func tLAccelSuffix(accelTag string) string {
	if accelTag == "" {
		return ""
	}
	return "（accel=" + accelTag + "）"
}
