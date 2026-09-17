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
//   - Side         买卖方向（SideBuy/SideSell）；
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
}

// NewGate 创建风控闸。onGate 可空（命中时告警回调；新闸默认高优告警，存量守卫不告警）。
// English: NewGate builds a risk gate; onGate may be nil (new gates alert high, legacy guards don't).
func NewGate(st *store.DB, userID string, onGate func(level, title, content string)) *Gate {
	return &Gate{st: st, userID: userID, now: time.Now, onGate: onGate}
}

// CheckLiveOrder 下单前置守卫统一入口：按序执行全部闸口，首个命中即返回阻断裁定
// （后续闸不再评估，行为与既有 controller 短路语义一致）。全部放行返回 Pass。
// English: single entry for all live-order pre-checks — runs every gate in order and returns the first
// blocking verdict (later gates are not evaluated, matching the existing short-circuit semantics).
func (g *Gate) CheckLiveOrder(cfg config.QMTConfig, o LiveOrder) *Verdict {
	// 闸口清单：按序评估，gate=留痕标识，alert=命中是否触发高优告警（新机构级闸为 true），
	// run 返回非空字符串即视为命中并携带原因。前四项为存量守卫（不告警），后七项含新增机构闸。
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
		{"day_loss", true, func() string { return g.checkDayLoss(cfg, o) }},                // 日内已实现亏损熔断
		{"concentration", true, func() string { return g.checkConcentration(cfg, o) }},     // 单票市值集中度
		{"buy_discipline", false, func() string { return g.checkBuyDiscipline(cfg, o) }},   // 买入纪律（笔数/预算/可用资金）
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
// English: §UAT-D4 — sellable now also deducts today's still-open (non-terminal) sell tickets, so
// concurrent same-second sells can no longer both pass on identical settled-only inputs.
func (g *Gate) checkT1Sellable(cfg config.QMTConfig, o LiveOrder) string {
	// 仅卖出方向且开关启用且账本可用时才检查；其余情况跳过。
	if o.Side != SideSell || !cfg.EnforceT1Enabled() || g.st == nil {
		return ""
	}
	// 未知仓位 fail-open：本地查不到该持仓行时跳过校验，交由券商柜台终裁（不拦合法退出）。
	if p, perr := g.st.RealPositionByCodeForUser(g.userID, o.Code); perr == nil && p.Qty > 0 {
		// 可卖量 = 持仓 − 当日已买入（未结算不可卖） − 当日在途卖单（已报/部成等非终态，占用额度）。
		bought := g.st.TodayBoughtQty(g.userID, o.Code, g.today())
		openSell := g.st.SumOpenSellQty(g.userID, o.Code, g.today())
		sellable := p.Qty - bought - openSell
		// 三项扣减后可能为负（数据时序误差），下限取 0 仅用于比较与提示。
		if sellable < 0 {
			sellable = 0
		}
		// 请求量超过可卖量 → 拒单（先到者占额度，后到者在网关侧被拦）。
		if o.Qty > sellable {
			return fmt.Sprintf("T+1 不可卖: 当日买入/在途卖单锁定, 可卖 %d < 请求 %d（持仓 %d, 当日买入 %d, 在途卖 %d）",
				sellable, o.Qty, p.Qty, bought, openSell)
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

// checkLimitPrice 涨停不可追买 / 跌停不可追卖（默认关）。板感知阈值取 data.LimitUpPct 唯一权威实现；
// 无昨收（PrevClose<=0）时 fail-open 跳过（数据缺口不误拦）。
// English: block chasing a limit-up buy / limit-down sell (off by default). Board-aware threshold from
// the canonical data.LimitUpPct; fails open when PrevClose is unknown.
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
	if o.Side == SideSell && cfg.RiskGate.LimitDownBlockSell {
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
// English: §GAP1.3/1.4 buy-discipline precheck (migrated verbatim from the controller): daily buy-count
// cap, daily budget and estimated/broker available-cash gates, run before the pending ticket persists.
func (g *Gate) checkBuyDiscipline(cfg config.QMTConfig, o LiveOrder) string {
	if o.Side != SideBuy || g.st == nil {
		return ""
	}
	// 本单金额缺失时按 qty×参考价回退估算。
	amount := o.Amount
	if amount <= 0 {
		amount = o.Price * float64(o.Qty)
	}
	// 读取全部委托，统计今日已报买入的笔数与金额。
	orders, err := g.st.RealOrdersForUser(g.userID)
	if err != nil {
		return fmt.Sprintf("read real orders: %v", err)
	}
	today := g.today()
	buys := 0
	spent := 0.0
	// 只统计今日买入委托：排除卖出方向与本单自身（避免自计数）；排除非终态前占位/已撤等状态。
	for _, ord := range orders {
		if ord.Side != SideBuy || ord.SignalID == o.SignalID {
			continue
		}
		// 仅"已报/部成/已成"三种有效状态计入今日预算与笔数。
		switch ord.Status {
		case "已报", "部成", "已成":
		default:
			continue
		}
		// 时间戳解析失败或非今日（北京时）的不计入。
		at, perr := time.Parse(time.RFC3339, ord.CreatedAt)
		if perr != nil || cntime.In(at).Format("2006-01-02") != today {
			continue
		}
		buys++
		spent += ord.Price * float64(ord.Qty)
	}
	// 闸1：单日买入笔数上限（0=不限）。
	if cfg.DailyMaxBuys > 0 && buys >= cfg.DailyMaxBuys {
		return fmt.Sprintf("单日买入笔数达上限 %d（今日已报 %d 笔）", cfg.DailyMaxBuys, buys)
	}
	// 闸2：单日买入预算（0=不限）：今日已报金额 + 本次金额超预算即拒。
	if cfg.DailyBudgetAmount > 0 && spent+amount > cfg.DailyBudgetAmount {
		return fmt.Sprintf("单日买入预算不足: 已报 %.0f + 本次 %.0f > 预算 %.0f", spent, amount, cfg.DailyBudgetAmount)
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
			// 近似口径：预估可用 = 本金 − 持仓成本市值 − 今日已报买入。
			pos, perr := g.st.RealPositionsForUser(g.userID)
			if perr != nil {
				return fmt.Sprintf("read real positions: %v", perr)
			}
			held := 0.0
			for _, p := range pos {
				held += p.CostPrice * float64(p.Qty)
			}
			avail := cfg.InitialCapital - held - spent
			// 不信任券商冻结口径时，额外扣减本地在途买入冻结量。
			if !cfg.Money.TrustBrokerFreezeEnabled() {
				avail -= g.st.LocalBuyFrozen(g.userID, today)
			}
			// 再扣固定/比例保留现金后与本次金额比较。
			avail -= cfg.Money.EffectiveReserve(avail)
			if amount > avail {
				return fmt.Sprintf("可用资金不足: 预估可用 %.0f（本金%.0f−持仓成本%.0f−今日已报%.0f%v%v）< 本次 %.0f",
					avail, cfg.InitialCapital, held, spent,
					func() string {
						if !cfg.Money.TrustBrokerFreezeEnabled() {
							return fmt.Sprintf("−在途冻结%.0f", g.st.LocalBuyFrozen(g.userID, today))
						}
						return ""
					}(),
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
			// 不信任券商冻结时扣本地在途冻结；再扣保留现金，得到可下单上限。
			var frozenDed, reserveDed float64
			if !cfg.Money.TrustBrokerFreezeEnabled() {
				frozenDed = g.st.LocalBuyFrozen(g.userID, today)
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
