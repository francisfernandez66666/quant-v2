// gate.go — §WS-C 风控闸口统一入口（B2/B3 收口）。
//
// 背景：审计发现 `internal/risk` 只有 M8Check 被接线，CheckSignal/PositionLimitCheck/CheckDrawdown
// 是零生产调用死代码；实盘守卫却散落在 controller.placeOrder（ST/黑名单/纪律/白名单/仓位上限）、
// combat_agent 卖出侧与 discipline 探针——同一只股的安全判定无单一权威入口，新增闸口易漏装；
// 且缺机构级闸：日内已实现亏损熔断、单票市值集中度、涨跌停不可追单、行情新鲜度硬闸。
//
// Gate 把 controller 的下单前置守卫收敛到 CheckLiveOrder 单一入口（controller 只保留 orderMu
// 串行、幂等与 executor 分发），并新增上述四类闸（全部默认关，配置启用，命中即拒单+记录+告警）。
//
// English: §WS-C unifies every live-order pre-check behind a single authoritative RiskGate entry point
// (the controller keeps only serialization, idempotency and executor dispatch), and adds four
// institutional gates — intraday realized-loss circuit breaker, single-stock value concentration,
// limit-up/down chase blocking, and quote-staleness hard gate — all off by default, each recording a
// risk_gates hit and alerting when it trips.
package risk

import (
	"fmt"
	"log"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/signalctl"
	"quant-trading-v2/internal/store"
)

// SideBuy/SideSell 下单方向（与 trading 包同语义，避免包循环依赖而镜像常量）。
// English: order sides mirroring the trading package constants (avoids an import cycle).
const (
	SideBuy  = "买入"
	SideSell = "卖出"
)

// LiveOrder 下单风控视图：Gate 需要的全部判定字段（由 controller 从 OrderRequest 装配，
// 行情相关字段缺失时相应闸 fail-open 跳过——宁可放行也不因数据缺口误拦）。
// English: the risk view of an order — all fields the gate needs, assembled by the controller from the
// order request; quote fields that are absent make their gates fail open.
//   - SignalID     信号唯一标识（幂等键；买入纪律统计时排除本单自身）；
//   - Code         证券代码（带后缀，黑名单/涨跌停/集中度/T+1 判定主键）；
//   - Name         证券名称（ST/退市风险判定依据）；
//   - Strategy     战法显示名；StrategyID 战法库规则 ID；StrategyType 规范战法 ID
//     （白名单判定三键，任一命中即视为在白名单内）；
//   - Side         买卖方向（SideBuy/SideSell）；§N-4（2026-09-22 修复批）：**方向不做 fail-open**——
//     非这两值的订单在 CheckLiveOrder 入口即拒（fail-close），理由见该函数注释；
//   - Price        参考委托价；Qty 股数；Amount 委托金额（元；缺失时按 Qty×Price 回退估算）。
type LiveOrder struct {
	SignalID     string
	Code         string
	Name         string
	Strategy     string
	StrategyID   string
	StrategyType string
	Side         string
	Price        float64
	Qty          int
	Amount       float64

	// StalenessMs 行情快照陈旧度（毫秒；-1=未提供，StaleQuoteGuard 跳过）。
	// CurrentPrice 现价（集中度闸用；0=未提供）。PrevClose 昨收（涨跌停闸用；0=未提供）。
	StalenessMs  int64
	CurrentPrice float64
	PrevClose    float64
}

// Verdict 单笔下单的风控裁定。
// English: Verdict is the risk ruling for one live order.
type Verdict struct {
	// Pass 是否通过风控（true=全部闸口放行）。
	Pass bool `json:"pass"`
	// Action 建议动作：pass=放行 / block=阻断 / wait=等待（预留）。
	Action string `json:"action"` // pass/block/wait
	// Gate 命中的闸口标识（与 checks 表中 gate 字段一致；放行时为空）。
	Gate string `json:"gate"` // 命中的闸口标识
	// Reason 阻断原因（人可读，写入 risk_gates 留痕与告警内容）。
	Reason string `json:"reason"`
	// Alert 非空=触发上层告警的标题（仅新机构级闸会携带）。
	Alert string `json:"alert,omitempty"` // 非空=触发上层告警的标题
	// Blocked 是否被彻底阻断（不进入后续下单流程；与 Action=block 同义）。
	Blocked bool `json:"blocked"`
}

// Gate 下单风控闸：依赖本地账本（store）与归属账号，产出阻断裁定并记录命中。
// English: Gate is the live-order risk gate backed by the local ledger and the owning account.
type Gate struct {
	// st 本地账本（持久化持仓/委托/账户快照；买入纪律与 M8 判定的数据源），可空（为空时相关闸跳过）。
	st *store.DB
	// userID 归属资金账号（账本查询与 risk_gates 留痕均按账号过滤）。
	userID string
	// now 时间源（可注入便于测试；交易日期与券商快照新鲜度判定均基于此）。
	now func() time.Time
	// onGate 告警回调（可空；仅 alert=true 的新闸命中时以 high 级别触发）。
	onGate func(level, title, content string)
	// crossPrice §XCHECK 2026-09-22 C批：独立复核价格源（code→价格，可空=闸跳过）。
	// 由装配层注入 DataCoordinator.CrossCheckPrice（新浪→腾讯→东财多源链），与本单行情
	// 快照来源解耦——复核的是「两源同刻价差」，快照自身脏了也能被这层抓出来。
	// English: §XCHECK independent cross-check price source (nil = gate skipped), wired from the
	// data coordinator so the reference price is verified against a different multi-source chain.
	crossPrice func(code string) (float64, error)
}

// NewGate 创建风控闸。onGate 可空（命中时告警回调；新闸默认高优告警，存量守卫不告警）。
// English: NewGate builds a risk gate; onGate may be nil (new gates alert high, legacy guards don't).
func NewGate(st *store.DB, userID string, onGate func(level, title, content string)) *Gate {
	return &Gate{st: st, userID: userID, now: time.Now, onGate: onGate}
}

// SetCrossPriceSource §XCHECK 2026-09-22 C批：注入价格复核闸的独立复核源（可空=闸保持跳过，
// 零行为变化）。setter 而非构造参数：复核源在引擎装配尾段（registry）才就绪，且旧调用方
// 无需感知本闸存在。
// English: §XCHECK injects the cross-check price source (nil keeps the gate inert); a setter
// because the coordinator is only available late in engine assembly.
func (g *Gate) SetCrossPriceSource(fn func(code string) (float64, error)) {
	g.crossPrice = fn
}

// CheckLiveOrder 下单前置守卫统一入口：按序执行全部闸口，首个命中即返回阻断裁定
// （后续闸不再评估，行为与既有 controller 短路语义一致）。全部放行返回 Pass。
// English: single entry for all live-order pre-checks — runs every gate in order and returns the first
// blocking verdict (later gates are not evaluated, matching the existing short-circuit semantics).
func (g *Gate) CheckLiveOrder(cfg config.QMTConfig, o LiveOrder) *Verdict {
	// §N-4（2026-09-22 修复批，M-1 升级项之一）方向白名单前置 fail-close：未知方向直接拒单。
	// 缺陷原文：本文件多数闸按 `o.Side == SideBuy` / `o.Side != SideSell` 精确匹配来区分方向
	// （checkST:184、checkBlacklist:198、checkT1Sellable:218、checkLimitPrice:277/284、
	// checkDayLoss:365、checkConcentration:399、checkWhitelist:436、checkMaxPositions:452、
	// checkBuyDiscipline:493）。传入 "buy"/"SELL"/" 买入"（带空格）等非法串时，这些闸的
	// 「非买」与「非卖」两个分支同时不成立 → **T+1 卖出限制闸、涨停拒买闸、跌停拒卖闸三道方向性
	// 闸一起静默跳过**（还捎带 ST/黑名单/买入纪律），而下游 executor 是 `Side == SideSell ? 卖 : 买`
	// 的二分，非法值最终会被默认成某个真实方向落到柜台。
	// 为何这么改：未知方向不得享受任何方向性闸的"跳过红利"——方向是全部方向性守卫的判定前提，
	// 前提本身不可信时唯一安全的姿势是拒单（fail-close），而不是让每道闸各自弃权。
	// 放在 CheckLiveOrder 开头而不是各闸内部：这里是唯一权威入口（生产仅 controller.placeOrder
	// 一处调用），一次性前置即可覆盖全部现有与后续新增闸，杜绝"新闸忘了装"这一族失效。
	// English: §N-4 fail-close side whitelist at the single authoritative entry — an unknown side
	// would silently skip every directional gate (T+1 sellable / limit-up buy / limit-down sell, plus
	// ST/blacklist/buy-discipline) while the executor still folds it into one real side. An unknown
	// direction must never collect the skip dividend of directional gates, so we reject once here
	// instead of letting each gate abstain on its own.
	if o.Side != SideBuy && o.Side != SideSell {
		return g.verdict("side_unknown", fmt.Sprintf(
			"非法下单方向(side=%q)：只接受 %s/%s（不做任何归一/缺省），未知方向一律拒单（fail-close，防止方向性风控闸被静默跳过）",
			o.Side, SideBuy, SideSell), true)
	}
	// 闸口清单：按序评估，gate=留痕标识，alert=命中是否触发高优告警（新机构级闸为 true），
	// run 返回非空字符串即视为命中并携带原因。共 12 道闸（§XCHECK 2026-09-22 C批 新增第 12 道
	// price_cross_check）：其中 st/blacklist/t1_sellable/buy_discipline/whitelist/max_positions
	// 为存量守卫（不告警），其余为新增机构闸（命中高优告警）。
	checks := []struct {
		gate  string
		alert bool
		run   func() string
	}{
		{"st", false, func() string { return g.checkST(o) }},                               // ST/退市股禁买
		{"blacklist", false, func() string { return g.checkBlacklist(cfg, o) }},            // 个股黑名单禁买
		{"max_order_amount", true, func() string { return g.checkMaxOrderAmount(cfg, o) }}, // 单笔金额绝对帽
		{"t1_sellable", false, func() string { return g.checkT1Sellable(cfg, o) }},         // T+1 可卖量（含在途卖单）
		{"limit_up_down", true, func() string { return g.checkLimitPrice(cfg, o) }},        // 涨停不可追买/跌停不可追卖
		{"stale_quote", true, func() string { return g.checkStaleQuote(cfg, o) }},          // 行情新鲜度硬闸
		{"price_cross_check", true, func() string { return g.checkPriceCross(cfg, o) }},    // §XCHECK 价格复核闸（数据类闸与上闸聚拢）
		{"day_loss", true, func() string { return g.checkDayLoss(cfg, o) }},                // 日内已实现亏损熔断
		{"concentration", true, func() string { return g.checkConcentration(cfg, o) }},     // 单票市值集中度
		{"buy_discipline", false, func() string { return g.checkBuyDiscipline(cfg, o) }},   // 买入纪律（已成交笔数/预算/可用资金）
		{"whitelist", false, func() string { return g.checkWhitelist(cfg, o) }},            // 战法白名单
		{"max_positions", false, func() string { return g.checkMaxPositions(cfg, o) }},     // 持仓数上限
	}
	// 短路语义：首个命中即返回阻断裁定，后续闸不再评估（与原 controller 行为一致）。
	for _, c := range checks {
		if reason := c.run(); reason != "" {
			return g.verdict(c.gate, reason, c.alert)
		}
	}
	// 全部闸口通过 → 放行。
	return &Verdict{Pass: true, Action: "pass", Reason: "风控通过"}
}

// CheckPortfolio §WS-C 组合级兜底（M8）：Gate 统一入口对 M8 的只读封装（委托 M8CheckWith）。
// §F-7（20260917）：实盘执行链路与本方法共用 risk.M8CheckWith 唯一判定，旧 Engine 副本已删除。
func (g *Gate) CheckPortfolio(cfg *config.Rules, currentTotal, peakTotal float64) *CheckResult {
	return M8CheckWith(cfg, currentTotal, peakTotal)
}

// verdict 记录命中（risk_gates 表）并按需触发高优告警。
// English: verdict records the hit (risk_gates table) and optionally fires a high-priority alert.
func (g *Gate) verdict(gate, reason string, alert bool) *Verdict {
	// 留痕：命中写入 risk_gates 表（账号+交易日+闸口+原因）；账本缺失时静默跳过。
	if g.st != nil {
		_ = g.st.RecordRiskGate(g.userID, g.today(), gate, reason)
	}
	// 高优告警：仅 alert=true 的新机构级闸命中时触发（存量守卫保持静默，不打扰）。
	if alert && g.onGate != nil {
		g.onGate("high", "风控闸口拦截", fmt.Sprintf("[%s] %s", gate, reason))
	}
	return &Verdict{Pass: false, Action: "block", Gate: gate, Reason: reason, Blocked: true}
}

// today 交易日的本地日期（北京时）。English: today's date in Beijing time.
func (g *Gate) today() string {
	return cntime.In(g.now()).Format("2006-01-02")
}

// checkST §GAP1.6 ST/退市风险股拒绝买入（仅买方向；卖出放行——已存在的风险敞口必须可退出）。
// English: ST/delisting-risk stocks rejected on BUY only; sells stay open so existing exposure can exit.
func (g *Gate) checkST(o LiveOrder) string {
	// 仅买方向生效；卖出放行（已持有敞口必须保留退出通道）。
	if o.Side != SideBuy {
		return ""
	}
	// 名称含 ST/退市特征即命中，拒绝买入。
	if combat_agent.IsSTStock(o.Name) {
		return fmt.Sprintf("ST/退市风险股禁止买入: %s", o.Name)
	}
	return ""
}

// checkBlacklist §GAP1.7 黑名单接线（仅买方向）：命中 qmt.blacklist 即拒。
// English: §GAP1.7 blacklist wiring (buy-only) via the canonical normalized matcher.
func (g *Gate) checkBlacklist(cfg config.QMTConfig, o LiveOrder) string {
	// 仅买方向生效：黑名单拦截的是"新增敞口"，卖出放行避免强迫扛单。
	if o.Side != SideBuy {
		return ""
	}
	// 命中 qmt.blacklist（规范化比对）即拒；卖出方向已在上方放行。
	if config.CodeInBlacklist(cfg.Blacklist, o.Code) {
		return fmt.Sprintf("黑名单股票禁止买入: %s", o.Code)
	}
	return ""
}

// checkT1Sellable §WS-A T+1 可卖量前置守卫（仅卖出）：当日买入份额当日不可卖。
// **未知仓位 fail-open**：本地无该持仓行时跳过，交由券商柜台终裁——宁可多问柜台也不拦合法退出。
// §UAT-D4（2026-09-16）：可卖量同时扣减「当日在途卖单」（SumOpenSellQty：已报/部成等非终态行）——
// 旧口径只数 持仓−当日买入（均已结算成交），并发双卖（手动双入口或 M8+止损同轮）两笔校验输入相同、
// 双双放行，合计超过 T+1 可卖量（2026-09-16 实跑 01:16:36 两笔同秒卖单全成交复现）。柜台终裁仍在，
// 但废单发生在真实交易所、留废单记录；并入在途量后先到者占额度，后到者在网关侧就被拦下。
// §N-3（2026-09-22 傍晚批）联动：在途项口径由「整笔委托量」改为「未成交余量」，三项扣减算式见下。
//
// §0925EVE-W2-A1（2026-09-26 批）判定改口径——「柜台 can_use_qty 优先，本地推算作交叉告警」：
//   - 改前行为：可卖量**只有**本地推算一条腿（p.Qty − 当日买入 − 在途卖未成余量）。柜台每轮
//     对账快照都携带真实 T+1 可卖量 can_use_qty，但旧 store.RealPosition 无该字段、被
//     encoding/json 静默丢弃（§0925EVE 全量审计锤实"全仓非测试代码零命中"）——本地看不到
//     的柜台侧事实（人工在券商端的账外成交、柜台冻结口径、当日买入结算细节）全部缺失，
//     可卖量偶发虚高，卖单到交易所吃废单。
//   - 改后行为：持仓行 CanUseQty 可读且 >=0（柜台真值）时，可卖量 = min(柜台值, 本地推算)。
//     为什么是 min 而不是无条件取柜台值（裁决的资损约束决定）：柜台值**更小**（可卖更少）时
//     绝不能放行更多卖出——min 在收紧侧完整兑现"柜台优先"；柜台值**更大**时本地仍封顶，
//     因为本地三项扣减里的在途卖单（§UAT-D4）在柜台快照窗口内不可信地缺席（快照先于本单
//     受理，并发第二笔若只读柜台值会再次双双放行——正是 09-16 实录形态），且"柜台比本地
//     乐观"本身意味着两本账有一本错了，未见证据前不放大卖出权限。
//   - 交叉告警（不静默）：柜台值与本地推算偏差绝对值超过阈值 max(100 股, 持仓×2%) 时以
//     warn 级走既有 onGate 告警通道（接线方 registry.go 将非 high/low 级别映射为中档推送，
//     本批不改接线文件，已核该级别可达）。阈值为什么不能是 0：成交回报先于/晚于快照到达的
//     时序 skew 天然产生整手（100 股=A 股最小交易单位）级瞬时差，0 阈值天天误报；
//     为什么不能是大绝对值：千手仓位下绝对阈值会吞掉同比例的记账漂移——2% 相对灵敏度
//     兜住大仓，100 股地板兜住"整手噪声"。告警内容带齐 qty/bought/openSell/counter 四项：
//     若偏差恰可被在途卖量解释（柜台冻结口径），人看一眼即可排除，宁多报不静默——
//     本批主题就是"静默失效"。
//   - 柜台值读不到（CanUseQty=nil：桥通道旧 7 键行、成交回报建的行、存量未对账行）退回
//     本地推算，现行为原样保留；柜台值为 0（当日新仓全锁）是真值、照常收紧。
//
// English: §A1 — the broker-reported T+1 sellable qty (can_use_qty, now persisted) takes
// precedence in the tightening direction: sellable = min(counter, local estimate), with a
// warn-level cross-check alert when they diverge beyond max(100 shares, 2% of the position).
// The local estimate keeps the §UAT-D4 open-sell deduction because a broker snapshot cannot
// see orders accepted after it was taken; when the counter value is absent, behavior is
// exactly as before (local only).
func (g *Gate) checkT1Sellable(cfg config.QMTConfig, o LiveOrder) string {
	// 仅卖出方向且开关启用且账本可用时才检查；其余情况跳过。
	if o.Side != SideSell || !cfg.EnforceT1Enabled() || g.st == nil {
		return ""
	}
	// 未知仓位 fail-open：本地查不到该持仓行时跳过校验，交由券商柜台终裁（不拦合法退出）。
	if p, perr := g.st.RealPositionByCodeForUser(g.userID, o.Code); perr == nil && p.Qty > 0 {
		// 可卖量 = 持仓 − 当日已买入（未结算不可卖） − 当日在途卖单的未成交余量（§N-3 净额口径：
		// 已报/部成/待撤等非终态行按 qty−已成交 占额度，成交腿已由 p.Qty 扣过，不在此重复）。
		bought := g.st.TodayBoughtQty(g.userID, o.Code, g.today())
		openSell := g.st.SumOpenSellQty(g.userID, o.Code, g.today())
		sellable := p.Qty - bought - openSell
		// 三项扣减后可能为负（数据时序误差），下限取 0 仅用于比较与提示。
		if sellable < 0 {
			sellable = 0
		}
		basis := "本地推算"
		// §A1 柜台值优先（收紧方向）+ 交叉告警：nil / 负值都按"读不到"退回本地推算。
		if p.CanUseQty != nil && *p.CanUseQty >= 0 {
			counter := *p.CanUseQty
			localEst := sellable
			if counter < localEst {
				sellable = counter // 柜台更小：一律以柜台为准（资损方向，裁决钉死）
			}
			basis = fmt.Sprintf("柜台 can_use_qty=%d", counter)
			// 交叉告警：偏差阈值 max(100 股, 2%×持仓量)，理由见函数头（改前此对比根本不存在，
			// 因为柜台值从未入账——这条告警就是断腿修复后的"两本账互相守望"腿）。
			dev := counter - localEst
			if dev < 0 {
				dev = -dev
			}
			thr := p.Qty / 50
			if thr < 100 {
				thr = 100
			}
			if dev > thr {
				content := fmt.Sprintf("%s %s：柜台可卖口径 %d 与本地推算 %d 偏差 %d 股(阈值 %d)，持仓 %d 当日买入 %d 在途卖 %d —— 可卖量已按 min 收紧，偏差请核对本成交链与柜台账外操作",
					g.userID, o.Code, counter, localEst, dev, thr, p.Qty, bought, openSell)
				log.Printf("[risk] §A1 T+1 可卖量交叉偏差告警: %s", content)
				if g.onGate != nil {
					g.onGate("warn", "T+1 可卖量交叉偏差", content)
				}
			}
		}
		// 请求量超过可卖量 → 拒单（先到者占额度，后到者在网关侧被拦）。
		if o.Qty > sellable {
			return fmt.Sprintf("T+1 不可卖: 当日买入/在途卖单锁定, 可卖 %d < 请求 %d（%s；持仓 %d, 当日买入 %d, 在途卖 %d）",
				sellable, o.Qty, basis, p.Qty, bought, openSell)
		}
	}
	return ""
}

// checkMaxOrderAmount 单笔委托金额绝对帽（默认关，阈值 0=放行一切）：本单金额（装配缺失时
// 回退 qty×参考价）超过 cfg.RiskGate.MaxOrderAmount → 买卖双向拒单+告警。
// 定位（§AUDIT-PM 2026-09-15）：手动下单入口不经 sizing 通道，胖手误（多打一个 0）此前只有
// 价格守卫与日预算兜底，缺一道与信号策略无关的绝对上限；自动单同享此帽做双保险。
// English: absolute per-order amount cap (0 = off). Rejects buys and sells whose amount
// (or qty×ref price when Amount is unset) exceeds the threshold — the hard ceiling for the
// manual entry point, independent of strategy sizing.
func (g *Gate) checkMaxOrderAmount(cfg config.QMTConfig, o LiveOrder) string {
	// 绝对帽开关：阈值 ≤0 视为未启用，放行一切。
	if cfg.RiskGate.MaxOrderAmount <= 0 {
		return ""
	}
	// 优先用装配好的委托金额；缺失（≤0）时回退 qty×参考价估算。
	amt := o.Amount
	if amt <= 0 {
		amt = o.Price * float64(o.Qty)
	}
	// 买卖双向校验：超帽即拒（手动入口胖手误的最后防线）。
	if amt > cfg.RiskGate.MaxOrderAmount {
		return fmt.Sprintf("单笔金额超限: %.0f > 绝对帽 %.0f（%s %s %d股，请核对数量与价格）",
			amt, cfg.RiskGate.MaxOrderAmount, o.Side, o.Code, o.Qty)
	}
	return ""
}

// checkLimitPrice 涨停不可追买（默认关）/ 跌停不可追卖（§A5-常开：默认开，显式 false 才关）。
// 板感知阈值取 data.LimitUpPct 唯一权威实现；无昨收（PrevClose<=0）时 fail-open 跳过（数据缺口不误拦）。
// English: block chasing a limit-up buy (off by default) / limit-down sell (on by default since
// §A5-always-on). Board-aware threshold from the canonical data.LimitUpPct; fails open when
// PrevClose is unknown.
func (g *Gate) checkLimitPrice(cfg config.QMTConfig, o LiveOrder) string {
	// 无昨收或参考价（≤0）时 fail-open：数据缺口不误拦，交由柜台判定。
	if o.PrevClose <= 0 || o.Price <= 0 {
		return ""
	}
	// 板感知涨停幅度（主板/创业板/科创板差异）取 data.LimitUpPct 唯一权威实现。
	pct := data.LimitUpPct(o.Code, o.Name)
	// 买入方向：参考价达到昨收×(1+涨幅) 即视为涨停，拒追买。
	if o.Side == SideBuy && cfg.RiskGate.LimitUpBlockBuy {
		limitUp := o.PrevClose * (1 + pct/100)
		if o.Price >= limitUp {
			return fmt.Sprintf("涨停不可追买: 参考价 %.2f ≥ 涨停价 %.2f（昨收 %.2f +%.1f%%）", o.Price, limitUp, o.PrevClose, pct)
		}
	}
	// 卖出方向：参考价跌至昨收×(1−跌幅) 即视为跌停，拒追卖（防止底部割在跌停板上）。
	// §A5-常开（owner 裁决 2026-09-26）：本闸未配置即生效，仅显式 limit_down_block_sell=false 关闭。
	if o.Side == SideSell && cfg.RiskGate.LimitDownBlockSellEnabled() {
		limitDown := o.PrevClose * (1 - pct/100)
		if o.Price <= limitDown {
			return fmt.Sprintf("跌停不可追卖: 参考价 %.2f ≤ 跌停价 %.2f（昨收 %.2f −%.1f%%）", o.Price, limitDown, o.PrevClose, pct)
		}
	}
	return ""
}

// checkStaleQuote 行情新鲜度硬闸（默认关）：下单时快照陈旧度超阈值 → 拒单+告警
// （原 fetcher 只打日志不拦单）。无快照（StalenessMs<0）时跳过。
// English: quote-staleness hard gate (off by default) — orders are rejected + alerted when the snapshot
// is staler than the threshold; never-fetched (StalenessMs<0) is skipped.
func (g *Gate) checkStaleQuote(cfg config.QMTConfig, o LiveOrder) string {
	// 开关（阈值 ≤0）或未提供快照（-1=从未拉取）时不检查。
	if cfg.RiskGate.StaleQuoteMs <= 0 || o.StalenessMs < 0 {
		return ""
	}
	// 快照陈旧度超阈值 → 拒单+告警（防基于过期价格误判下单）。
	if o.StalenessMs > cfg.RiskGate.StaleQuoteMs {
		return fmt.Sprintf("行情快照陈旧: %dms > 阈值 %dms", o.StalenessMs, cfg.RiskGate.StaleQuoteMs)
	}
	return ""
}

// checkPriceCross §XCHECK 2026-09-22 C批 价格复核闸（默认关，买卖双向都查）：
// 委托参考价与独立复核源价的偏离 |o.Price−cross|/cross×100 超过 cfg.RiskGate.CrossCheckPct 即命中。
// 与 stale_quote 的分工：新鲜度闸只看「快照多旧」，看不到「快照虽然新但价格本身脏」
// （单源数据错、除权口径错位等）——本闸用另一条取价链同刻对价，专防这类单源污染价。
// 复核的是「两源同刻价差」而非趋势判断，故买卖双向同视。
// 命中姿势（§WS-C 影子惯例，同 signal_ctl.shadow_blacklist）：
//   - 影子（CrossCheckShadow 非显式 false，默认）→ 仅写 risk_gates 留痕（[shadow] 前缀）+ 放行；
//   - 正式（shadow=false）→ 返回拒单原因，由 verdict 统一落库并触发高优告警。
//
// 跳过/放行（fail-open）分支：闸关闭（pct≤0）/ 未注入复核源 / 参考价缺失（o.Price≤0）/
// 复核源取价出错或返回 ≤0 ——数据缺口不误拦，与同族数据类闸（stale_quote/limit 无昨收）一致姿势。
// English: §XCHECK price cross-check (off by default, both sides). Compares the order reference
// price against an independent quote chain; fails open on any data gap; shadow hits record-and-pass
// ([shadow] prefix in risk_gates), enforced hits (cross_check_shadow=false) reject + alert.
func (g *Gate) checkPriceCross(cfg config.QMTConfig, o LiveOrder) string {
	// 关闭 / 未注入复核源 / 无参考价 → 跳过（零配置零行为变化）。
	pct := cfg.RiskGate.CrossCheckPct
	if pct <= 0 || g.crossPrice == nil || o.Price <= 0 {
		return ""
	}
	// 独立源取价：失败或价格非法一律 fail-open——复核的意义是拦「两源同刻真实价差」，
	// 拿不到复核价时没有判定依据，宁可放行也不因数据缺口误拦（与同族数据类闸一致）。
	cross, err := g.crossPrice(o.Code)
	if err != nil || cross <= 0 {
		return ""
	}
	// 偏离度 = |参考价 − 复核价| / 复核价 × 100（分母取复核侧，与"以独立源为准"语义一致）。
	dev := (o.Price - cross) / cross * 100
	if dev < 0 {
		dev = -dev
	}
	if dev <= pct {
		return ""
	}
	// 中文格式化拒单文案：含代码、两价、实测偏差与阈值，供 risk_gates 留痕与告警直读。
	reason := fmt.Sprintf("价格复核偏差超限: %s 参考价 %.3f 与独立复核价 %.3f 偏离 %.2f%% > 阈值 %.2f%%（两源同刻价差，疑似单源脏价）",
		o.Code, o.Price, cross, dev, pct)
	// 影子期（默认）：只留痕放行——留痕走 RecordRiskGate 直写（不经 verdict，verdict 语义是拒单），
	// 原因带 [shadow] 前缀供前端闸口卡片/审计区分；正式化（shadow=false）才返回原因拒单。
	if !cfg.RiskGate.CrossCheckEnforce() {
		if g.st != nil {
			_ = g.st.RecordRiskGate(g.userID, g.today(), "price_cross_check", "[shadow] "+reason)
		}
		return ""
	}
	return reason
}

// checkDayLoss 日内已实现亏损熔断（默认关）：今日卖出已实现亏损占总资产比例达阈值 → 熔断当日新买入
// （卖出/清仓放行）。已实现盈亏取本地账本口径（TodayRealizedPnl）；总资产口径不可得时
// 回落 InitialCapital；两者皆无则跳过（fail-open）。
// English: intraday realized-loss circuit breaker (off by default) — when today's realized sell losses
// reach the threshold as a share of total assets, new buys are broken (sells stay open). P&L from the
// local ledger; falls back to InitialCapital when total assets are unavailable; both unknown → skip.
func (g *Gate) checkDayLoss(cfg config.QMTConfig, o LiveOrder) string {
	// 未启用/无账本/非买方向（卖出与清仓必须始终放行，熔断只断新买入）→ 不检查。
	if cfg.RiskGate.DayLossLimitPct <= 0 || g.st == nil || o.Side != SideBuy {
		return ""
	}
	// 已实现盈亏取本地账本口径；查询失败或今日为盈利（≥0）时直接放行。
	pnl, err := g.st.TodayRealizedPnl(g.userID, g.today())
	if err != nil || pnl >= 0 {
		return ""
	}
	// 分母（总资产）优先取账本实时口径，不可得时回落初始本金；两者皆无则跳过（fail-open）。
	base := 0.0
	if total, terr := g.st.TotalAssets(g.userID); terr == nil && total > 0 {
		base = total
	} else if cfg.InitialCapital > 0 {
		base = cfg.InitialCapital
	}
	if base <= 0 {
		return ""
	}
	// 亏损占比 = |今日已实现亏损| / 总资产 × 100，达阈值即熔断当日一切新买入。
	lossPct := -pnl / base * 100
	if lossPct >= cfg.RiskGate.DayLossLimitPct {
		return fmt.Sprintf("日内已实现亏损熔断: 今日亏损 %.0f = 总资产 %.1f%% ≥ 阈值 %.1f%%（熔断当日新买入）",
			-pnl, lossPct, cfg.RiskGate.DayLossLimitPct)
	}
	return ""
}

// checkConcentration 单票市值集中度（默认关）：买入后该票预计市值（现有持仓+本单金额）/ 总资产
// 超阈值 → 拒新买。总资产口径不可得时跳过（fail-open）。
// English: single-stock value concentration (off by default) — the stock's projected market value after
// this buy (held + order amount) over total assets above the cap rejects the buy; total-assets unknown
// fails open.
func (g *Gate) checkConcentration(cfg config.QMTConfig, o LiveOrder) string {
	// 未启用/无账本/非买方向（卖出减少集中度，无需检查）→ 不检查。
	if cfg.RiskGate.SingleStockValuePct <= 0 || g.st == nil || o.Side != SideBuy {
		return ""
	}
	// 总资产不可得（账本缺失或 ≤0）时失败跳过 —— 以草率分母算出的比例不可信。
	total, err := g.st.TotalAssets(g.userID)
	if err != nil || total <= 0 {
		return ""
	}
	// 现有持仓市值：优先现价，现价缺失回落成本价；两者皆无按 0 估算（只计本单金额）。
	posVal := 0.0
	if p, perr := g.st.RealPositionByCodeForUser(g.userID, o.Code); perr == nil && p.Qty > 0 {
		price := p.CurPrice
		if price <= 0 {
			price = p.CostPrice
		}
		if price > 0 {
			posVal = price * float64(p.Qty)
		}
	}
	// 买入后该票预计市值 = 现有持仓市值 + 本单金额，占总资产比例超上限即拒新买。
	proj := posVal + o.Amount
	if proj/total*100 > cfg.RiskGate.SingleStockValuePct {
		return fmt.Sprintf("单票集中度超限: 预计市值 %.0f / 总资产 %.0f = %.1f%% > 上限 %.1f%%",
			proj, total, proj/total*100, cfg.RiskGate.SingleStockValuePct)
	}
	return ""
}

// checkWhitelist 策略白名单校验（仅买方向；卖出不受限——退出的持仓其战法可能不在白名单，
// 拦截卖出会强迫扛单）。§SIGNAL_CONTROLLER 20260917：判定逻辑不再本地实现，统一调用
// signalctl.AdmitStrategy——与信号控制器 live 通道同一成员资格规则（规范键 StrategyType 优先、
// 空白名单=内置四形态+库规则默认全集、动量/未知来源必须显式列名），作为下单前最后防线保留
// （正常路径信号已在控制器裁定时被拦，走到这里说明调用方绕过了编排——仍需兜底）。
// English: delegates the strategy-membership check to signalctl.AdmitStrategy — single shared
// rule, kept here as a last-line guard for paths that bypass the orchestrator's admission.
func (g *Gate) checkWhitelist(cfg config.QMTConfig, o LiveOrder) string {
	// 仅买方向且带战法标识时检查；卖出不受限（退出通道必须保留）。
	if o.Side != SideBuy || o.Strategy == "" {
		return ""
	}
	// 装配信号视图，委托 signalctl.AdmitStrategy 做"唯一成员资格规则"判定
	// （正常路径在控制器裁定时已被拦，走到这里说明调用方绕过了编排 —— 兜底防线）。
	sig := combat_agent.Signal{Code: o.Code, Strategy: o.Strategy, StrategyID: o.StrategyID, StrategyType: o.StrategyType}
	if ok, _ := signalctl.AdmitStrategy(signalctl.Policy{Strategies: cfg.Strategies}, sig); !ok {
		return fmt.Sprintf("strategy %q not in qmt whitelist", o.Strategy)
	}
	return ""
}

// checkMaxPositions 仓位上限校验（仅买方向，按账号过滤）：max_positions>0 且当前持仓数已达上限。
// English: position-count cap (buy-only, per-account) — reject new buys when max_positions is reached.
func (g *Gate) checkMaxPositions(cfg config.QMTConfig, o LiveOrder) string {
	// 未启用（≤0）/非买方向（卖出减仓不受持仓数限制）/无账本 → 不检查。
	if cfg.MaxPositions <= 0 || o.Side != SideBuy || g.st == nil {
		return ""
	}
	// 按账号读取本地持仓行；读取失败视为命中（错误原因即进入判定留痕）。
	poses, err := g.st.RealPositionsForUser(g.userID)
	if err != nil {
		return fmt.Sprintf("read real positions: %v", err)
	}
	// 持仓数已达上限 → 拒绝新的买入。
	if len(poses) >= cfg.MaxPositions {
		return fmt.Sprintf("real positions %d >= max_positions %d", len(poses), cfg.MaxPositions)
	}
	return ""
}

// checkBuyDiscipline §GAP1.3/1.4 买入纪律预检（从 controller 原样迁入）：单日买入笔数上限、
// 单日买入预算、近似可用资金 + §R4-3 券商口径可用资金双闸。守卫先于占位落库执行。
//
// 闸口各自的口径（三闸互补，别把它们混成同一个口径）——**动态冻结账模型（2026-09-18）**：
// 占用 = 已成交（fills 客观成交事实）+ 在途冻结（orders 状态派生：已报=全额、部成=未成交余量、
// 已撤/部撤/废单/已成=自动解冻）− 卖出回款（fills 卖出金额，实时回血）。
// 这个口径下「撤单解冻」不再吃预算、「在途占着钱」仍然成立、**卖出即回血**：
//   - 闸1 笔数：数**今日已成交**买入笔数（fills 表，按委托去重）——「今天最多买成几笔」；
//     这是买入唯一的硬终点：今日已成交笔数达上限，当日量化买入才结束；
//   - 闸2 预算：占用（已成交+冻结−卖出回款，钳 0）+ 本次 > 预算即拒——撤单释放、
//     成交多少算多少、卖出回款实时释放额度，预算闸是活的；
//   - 闸3 近似资金：本金 − 持仓成本 − 在途冻结 + 今日已实现盈亏（仅在券商快照过期/缺失时兜底，
//     本就发生在券商冻结不可信的场景，故恒扣本地冻结，与 TrustBrokerFreezeEnabled 无关；
//     今日已成交买入的成本已随 ApplyRealFill 落进持仓，**不另扣成交额**——双扣是旧公式缺陷；
//     卖出经「持仓成本回落 + 已实现盈亏」两条路让该闸同步回血）；
//   - 闸4 券商口径：券商 AvailableCash 本身就是权威冻结账（报单冻结/成交扣除/撤单解冻/
//     卖出回增都在柜台侧发生），本地只做 TrustBrokerFreezeEnabled=false 时的显式扣减防双算。
//
// 卖出方向永不设量闸（用户裁决 2026-09-18：卖出没有终点，倒完货为止）——ST/黑名单放行卖出、
// 笔数/预算/资金闸只看买入；卖出仅受 T+1 可卖量（市场规则）与跌停拒追卖（价格保护）约束。
//
// English: §GAP1.3/1.4 buy-discipline precheck (migrated from the controller): daily buy-count cap,
// daily budget and estimated/broker available-cash gates, run before the pending ticket persists.
// Unified freeze-ledger basis since 2026-09-18: occupied = filled (fills) + frozen-in-transit
// (derived from order status — submitted freezes, fills deduct, cancels release).
func (g *Gate) checkBuyDiscipline(cfg config.QMTConfig, o LiveOrder) string {
	if o.Side != SideBuy || g.st == nil {
		return ""
	}
	// 本单金额缺失时按 qty×参考价回退估算。
	amount := o.Amount
	if amount <= 0 {
		amount = o.Price * float64(o.Qty)
	}
	today := g.today()
	// 冻结账三本账各取一次：已成交买入金额（fills）、在途冻结（orders 状态派生）、卖出回款（fills）。
	// filledAmt 是「真花掉的钱」，frozen 是「报出去还没成交、仍占着的钱」——撤单即消失；
	// sellProceeds 是「卖出去收回的钱」——实时对冲占用，让两道金额闸都随卖出动态回血。
	filledAmt, ferr := g.st.SumBuyFilledAmountByDay(g.userID, today)
	if ferr != nil {
		return fmt.Sprintf("read buy fills: %v", ferr)
	}
	// §C1（2026-09-22 修复批）：冻结查询错误不再吞成 0——读取失败按拒绝放行处理（fail-closed），
	// 与相邻两本账（ferr/serr）同一姿势：宁可少买，DB 故障期间绝不放水超买。
	frozen, frerr := g.st.LocalBuyFrozen(g.userID, today)
	if frerr != nil {
		return fmt.Sprintf("read frozen: %v", frerr)
	}
	sellProceeds, serr := g.st.SumSellFilledAmountByDay(g.userID, today)
	if serr != nil {
		return fmt.Sprintf("read sell fills: %v", serr)
	}
	// 闸1：单日买入笔数上限（0=不限）——**已成交**口径（2026-09-18 修正，原为已报口径）。
	//
	// 旧口径数 orders 里今日「已报/部成/已成」的委托数，等于报单即占额度：一笔报出去当场被券商
	// 废掉、或一直挂在委托簿上没成交，同样吃掉一天的买入额度，用户会因为一堆根本没成交的报单被
	// 锁死买入权（事故形态：daily_max_buys=5，当日 5 笔报单实际 0 成交，闸口仍报「今日已报 5 笔」）。
	// 这条纪律的语义是「今天最多买成几笔」，故以 fills 表为准——柜台回报落下的客观成交事实。
	// 附带收益：交割单 sync_fills 补记的手工成交（没有本地 orders 行）也能正确计入。
	// 防信号风暴的能力不因此丢失：闸2 预算按「已成交 + 在途冻结」计——无节制报单会先在金额闸上
	// 撞墙（见 checkBuyDiscipline 头注的闸口分工）。
	if cfg.DailyMaxBuys > 0 {
		filled, ferr := g.st.CountBuyFilledOrdersByDay(g.userID, today)
		if ferr != nil {
			return fmt.Sprintf("read buy fills: %v", ferr)
		}
		if filled >= cfg.DailyMaxBuys {
			return fmt.Sprintf("单日买入笔数达上限 %d（今日已成交 %d 笔）", cfg.DailyMaxBuys, filled)
		}
	}
	// 闸2：单日买入预算（0=不限）——**动态冻结账口径（2026-09-18，原为已报口径再改静态冻结账）**。
	//
	// 占用 = 已成交金额（fills，真花掉的钱）+ 在途冻结（LocalBuyFrozen：已报=全额、
	// 部成=未成交余量、已撤/部撤/废单/已成=自动解冻）− 卖出回款（fills 卖出金额）。
	// 卖出即回血：回款实时对冲当日占用，预算闸是活的——预算不够时卖一笔，额度立刻回来。
	// 占用钳到 0：清旧仓的回款可以吃满当日预算，但不会把预算放大到超出配置值
	// （预算语义仍是「单日净投入上限」，不是「可无限循环放大」）。
	// 买入的终点只有一个：闸1 今日已成交笔数达上限。预算/资金闸都随成交与回款动态伸缩。
	if cfg.DailyBudgetAmount > 0 {
		occupied := filledAmt + frozen - sellProceeds
		if occupied < 0 {
			occupied = 0
		}
		if occupied+amount > cfg.DailyBudgetAmount {
			return fmt.Sprintf("单日买入预算不足: 已成交 %.0f + 在途冻结 %.0f − 卖出回款 %.0f = 占用 %.0f，+ 本次 %.0f > 预算 %.0f（卖出回款可实时释放额度）",
				filledAmt, frozen, sellProceeds, occupied, amount, cfg.DailyBudgetAmount)
		}
	}
	// 闸3：近似可用资金闸（依赖 InitialCapital 配置；无本金口径时跳过）。
	if cfg.InitialCapital > 0 {
		// 券商账户快照 10 分钟内视为新鲜；新鲜时直接用券商口径，跳过本地近似估算。
		brokerFresh := false
		if acc, aerr := g.st.GetRealAccount(g.userID); aerr == nil && acc.AvailableCash > 0 {
			if at, perr := time.ParseInLocation("2006-01-02 15:04:05", acc.UpdatedAt, cntime.Loc); perr == nil &&
				g.now().Sub(at) <= 10*time.Minute {
				brokerFresh = true
			}
		}
		if !brokerFresh {
			// 近似口径（动态冻结账）：预估可用 = 本金 − 持仓成本 − 在途冻结 + 今日已实现盈亏。
			// 持仓由 ApplyRealFill 同事务即时更新：今日已成交买入的成本**已在持仓里**，
			// 故不再另扣成交额——旧公式 held+filledAmt 对当日已成交单双扣（成交后额度凭空少一半），
			// 这是比「已报口径」更隐蔽的同族缺陷，2026-09-18 一并修掉。
			// 卖出回款走两条路进来：持仓成本回落（held 下降）+ 已实现盈亏（pnl 上升），
			// 所以近似闸与预算闸一样是活的——卖出即回血，清仓后资金立刻可再投入。
			// 走到这里的前提就是券商快照过期/缺失——券商冻结不可用，故**恒扣**本地冻结，
			// 与 TrustBrokerFreezeEnabled 无关（该开关只影响闸4 对券商余额的扣减策略）。
			pos, perr := g.st.RealPositionsForUser(g.userID)
			if perr != nil {
				return fmt.Sprintf("read real positions: %v", perr)
			}
			held := 0.0
			for _, p := range pos {
				held += p.CostPrice * float64(p.Qty)
			}
			pnl, _ := g.st.TodayRealizedPnl(g.userID, today) // fail-open：数据缺口不放大额度，取 0 保守
			avail := cfg.InitialCapital - held - frozen + pnl
			// 再扣固定/比例保留现金后与本次金额比较。
			avail -= cfg.Money.EffectiveReserve(avail)
			if amount > avail {
				return fmt.Sprintf("可用资金不足: 预估可用 %.0f（本金%.0f−持仓成本%.0f−在途冻结%.0f+已实现盈亏%.0f%v）< 本次 %.0f",
					avail, cfg.InitialCapital, held, frozen, pnl,
					func() string {
						if cfg.Money.EffectiveReserve(avail) > 0 {
							return fmt.Sprintf("−保留现金%.0f", cfg.Money.EffectiveReserve(avail))
						}
						return ""
					}(),
					amount)
			}
		}
	}
	// 闸4：券商口径可用资金双闸（§R4-3）——在近似口径之外，用券商上报的可用现金再校验一次。
	if acc, err := g.st.GetRealAccount(g.userID); err == nil && acc.AvailableCash > 0 {
		at, perr := time.ParseInLocation("2006-01-02 15:04:05", acc.UpdatedAt, cntime.Loc)
		if perr == nil {
			// 快照新鲜用全额；陈旧（>10 分钟）保守折算 50%，防止过期余额放行超额单。
			cap := acc.AvailableCash
			fresh := g.now().Sub(at) <= 10*time.Minute
			if !fresh {
				cap = acc.AvailableCash * 0.5
			}
			label := "实时"
			if !fresh {
				label = "陈旧保守折算50%"
			}
			// 不信任券商冻结时扣本地在途冻结（复用闸3 取好的同一份冻结值）；再扣保留现金，得到可下单上限。
			var frozenDed, reserveDed float64
			if !cfg.Money.TrustBrokerFreezeEnabled() {
				frozenDed = frozen
				cap -= frozenDed
			}
			reserveDed = cfg.Money.EffectiveReserve(cap)
			cap -= reserveDed
			if amount > cap {
				return fmt.Sprintf("可用资金不足(券商口径%s): 上限 %.2f < 本次 %.2f（上报于 %s%s%s）",
					label, cap, amount, acc.UpdatedAt,
					func() string {
						if frozenDed > 0 {
							return fmt.Sprintf(", 在途冻结%.0f", frozenDed)
						}
						return ""
					}(),
					func() string {
						if reserveDed > 0 {
							return fmt.Sprintf(", 保留现金%.0f", reserveDed)
						}
						return ""
					}())
			}
		} else if acc.AvailableCash > 0 {
			// 时间戳解析异常：无法判断新鲜度，按最保守的 50% 折算兜底。
			if amount > acc.AvailableCash*0.5 {
				return fmt.Sprintf("可用资金不足(券商口径, 时间戳异常保守折算50%%): 上限 %.2f < 本次 %.2f",
					acc.AvailableCash*0.5, amount)
			}
		}
	}
	return ""
}
