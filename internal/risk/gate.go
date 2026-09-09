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
type LiveOrder struct {
	SignalID   string
	Code       string
	Name       string
	Strategy   string
	StrategyID string
	Side       string
	Price      float64
	Qty        int
	Amount     float64

	// StalenessMs 行情快照陈旧度（毫秒；-1=未提供，StaleQuoteGuard 跳过）。
	// CurrentPrice 现价（集中度闸用；0=未提供）。PrevClose 昨收（涨跌停闸用；0=未提供）。
	StalenessMs  int64
	CurrentPrice float64
	PrevClose    float64
}

// Verdict 单笔下单的风控裁定。
// English: Verdict is the risk ruling for one live order.
type Verdict struct {
	Pass    bool   `json:"pass"`
	Action  string `json:"action"` // pass/block/wait
	Gate    string `json:"gate"`   // 命中的闸口标识
	Reason  string `json:"reason"`
	Alert   string `json:"alert,omitempty"` // 非空=触发上层告警的标题
	Blocked bool   `json:"blocked"`
}

// Gate 下单风控闸：依赖本地账本（store）与归属账号，产出阻断裁定并记录命中。
// English: Gate is the live-order risk gate backed by the local ledger and the owning account.
type Gate struct {
	st     *store.DB
	userID string
	now    func() time.Time
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
	checks := []struct {
		gate  string
		alert bool
		run   func() string
	}{
		{"st", false, func() string { return g.checkST(o) }},
		{"blacklist", false, func() string { return g.checkBlacklist(cfg, o) }},
		{"t1_sellable", false, func() string { return g.checkT1Sellable(cfg, o) }},
		{"limit_up_down", true, func() string { return g.checkLimitPrice(cfg, o) }},
		{"stale_quote", true, func() string { return g.checkStaleQuote(cfg, o) }},
		{"day_loss", true, func() string { return g.checkDayLoss(cfg, o) }},
		{"concentration", true, func() string { return g.checkConcentration(cfg, o) }},
		{"buy_discipline", false, func() string { return g.checkBuyDiscipline(cfg, o) }},
		{"whitelist", false, func() string { return g.checkWhitelist(cfg, o) }},
		{"max_positions", false, func() string { return g.checkMaxPositions(cfg, o) }},
	}
	for _, c := range checks {
		if reason := c.run(); reason != "" {
			return g.verdict(c.gate, reason, c.alert)
		}
	}
	return &Verdict{Pass: true, Action: "pass", Reason: "风控通过"}
}

// CheckPortfolio §WS-C 组合级兜底（M8）：把原 Engine.M8Check 逻辑收敛进 Gate 统一入口
// （原逻辑保持不变，仅由 Engine.M8Check 委托共享实现）。
// English: CheckPortfolio is the portfolio-level fallback (M8), unified into the gate entry; the
// original Engine.M8Check logic is unchanged and delegated to the shared implementation.
func (g *Gate) CheckPortfolio(cfg *config.Rules, currentTotal, peakTotal float64) *CheckResult {
	return M8CheckWith(cfg, currentTotal, peakTotal)
}

// verdict 记录命中（risk_gates 表）并按需触发高优告警。
// English: verdict records the hit (risk_gates table) and optionally fires a high-priority alert.
func (g *Gate) verdict(gate, reason string, alert bool) *Verdict {
	if g.st != nil {
		_ = g.st.RecordRiskGate(g.userID, g.today(), gate, reason)
	}
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
	if o.Side != SideBuy {
		return ""
	}
	if combat_agent.IsSTStock(o.Name) {
		return fmt.Sprintf("ST/退市风险股禁止买入: %s", o.Name)
	}
	return ""
}

// checkBlacklist §GAP1.7 黑名单接线（仅买方向）：命中 qmt.blacklist 即拒。
// English: §GAP1.7 blacklist wiring (buy-only) via the canonical normalized matcher.
func (g *Gate) checkBlacklist(cfg config.QMTConfig, o LiveOrder) string {
	if o.Side != SideBuy {
		return ""
	}
	if config.CodeInBlacklist(cfg.Blacklist, o.Code) {
		return fmt.Sprintf("黑名单股票禁止买入: %s", o.Code)
	}
	return ""
}

// checkT1Sellable §WS-A T+1 可卖量前置守卫（仅卖出）：当日买入份额当日不可卖。
// **未知仓位 fail-open**：本地无该持仓行时跳过，交由券商柜台终裁——宁可多问柜台也不拦合法退出。
// English: §WS-A T+1 sell-guard (sell-only): shares bought today can't be sold today. Fail-open when
// no local position row exists — the broker has the final word so legitimate exits are never blocked.
func (g *Gate) checkT1Sellable(cfg config.QMTConfig, o LiveOrder) string {
	if o.Side != SideSell || !cfg.EnforceT1Enabled() || g.st == nil {
		return ""
	}
	if p, perr := g.st.RealPositionByCodeForUser(g.userID, o.Code); perr == nil && p.Qty > 0 {
		bought := g.st.TodayBoughtQty(g.userID, o.Code, g.today())
		sellable := p.Qty - bought
		if sellable < 0 {
			sellable = 0
		}
		if o.Qty > sellable {
			return fmt.Sprintf("T+1 不可卖: 当日买入锁定, 可卖 %d < 请求 %d（持仓 %d, 当日买入 %d）",
				sellable, o.Qty, p.Qty, bought)
		}
	}
	return ""
}

// checkLimitPrice 涨停不可追买 / 跌停不可追卖（默认关）。板感知阈值取 data.LimitUpPct 唯一权威实现；
// 无昨收（PrevClose<=0）时 fail-open 跳过（数据缺口不误拦）。
// English: block chasing a limit-up buy / limit-down sell (off by default). Board-aware threshold from
// the canonical data.LimitUpPct; fails open when PrevClose is unknown.
func (g *Gate) checkLimitPrice(cfg config.QMTConfig, o LiveOrder) string {
	if o.PrevClose <= 0 || o.Price <= 0 {
		return ""
	}
	pct := data.LimitUpPct(o.Code, o.Name)
	if o.Side == SideBuy && cfg.RiskGate.LimitUpBlockBuy {
		limitUp := o.PrevClose * (1 + pct/100)
		if o.Price >= limitUp {
			return fmt.Sprintf("涨停不可追买: 参考价 %.2f ≥ 涨停价 %.2f（昨收 %.2f +%.1f%%）", o.Price, limitUp, o.PrevClose, pct)
		}
	}
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
	if cfg.RiskGate.StaleQuoteMs <= 0 || o.StalenessMs < 0 {
		return ""
	}
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
	if cfg.RiskGate.DayLossLimitPct <= 0 || g.st == nil || o.Side != SideBuy {
		return ""
	}
	pnl, err := g.st.TodayRealizedPnl(g.userID, g.today())
	if err != nil || pnl >= 0 {
		return ""
	}
	base := 0.0
	if total, terr := g.st.TotalAssets(g.userID); terr == nil && total > 0 {
		base = total
	} else if cfg.InitialCapital > 0 {
		base = cfg.InitialCapital
	}
	if base <= 0 {
		return ""
	}
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
	if cfg.RiskGate.SingleStockValuePct <= 0 || g.st == nil || o.Side != SideBuy {
		return ""
	}
	total, err := g.st.TotalAssets(g.userID)
	if err != nil || total <= 0 {
		return ""
	}
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
	proj := posVal + o.Amount
	if proj/total*100 > cfg.RiskGate.SingleStockValuePct {
		return fmt.Sprintf("单票集中度超限: 预计市值 %.0f / 总资产 %.0f = %.1f%% > 上限 %.1f%%",
			proj, total, proj/total*100, cfg.RiskGate.SingleStockValuePct)
	}
	return ""
}

// checkWhitelist 策略白名单过滤（仅买方向；卖出不受限——退出的持仓其战法可能不在白名单，
// 拦截卖出会强迫扛单）。English: strategy whitelist filter (buy-only; sells exempt so exits aren't blocked).
func (g *Gate) checkWhitelist(cfg config.QMTConfig, o LiveOrder) string {
	if o.Side != SideBuy || len(cfg.Strategies) == 0 || o.Strategy == "" {
		return ""
	}
	for _, s := range cfg.Strategies {
		if s == o.Strategy || s == o.StrategyID {
			return ""
		}
	}
	return fmt.Sprintf("strategy %q not in qmt whitelist", o.Strategy)
}

// checkMaxPositions 仓位上限校验（仅买方向，按账号过滤）：max_positions>0 且当前持仓数已达上限。
// English: position-count cap (buy-only, per-account) — reject new buys when max_positions is reached.
func (g *Gate) checkMaxPositions(cfg config.QMTConfig, o LiveOrder) string {
	if cfg.MaxPositions <= 0 || o.Side != SideBuy || g.st == nil {
		return ""
	}
	poses, err := g.st.RealPositionsForUser(g.userID)
	if err != nil {
		return fmt.Sprintf("read real positions: %v", err)
	}
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
	amount := o.Amount
	if amount <= 0 {
		amount = o.Price * float64(o.Qty)
	}
	orders, err := g.st.RealOrdersForUser(g.userID)
	if err != nil {
		return fmt.Sprintf("read real orders: %v", err)
	}
	today := g.today()
	buys := 0
	spent := 0.0
	for _, ord := range orders {
		if ord.Side != SideBuy || ord.SignalID == o.SignalID {
			continue
		}
		switch ord.Status {
		case "已报", "部成", "已成":
		default:
			continue
		}
		at, perr := time.Parse(time.RFC3339, ord.CreatedAt)
		if perr != nil || cntime.In(at).Format("2006-01-02") != today {
			continue
		}
		buys++
		spent += ord.Price * float64(ord.Qty)
	}
	if cfg.DailyMaxBuys > 0 && buys >= cfg.DailyMaxBuys {
		return fmt.Sprintf("单日买入笔数达上限 %d（今日已报 %d 笔）", cfg.DailyMaxBuys, buys)
	}
	if cfg.DailyBudgetAmount > 0 && spent+amount > cfg.DailyBudgetAmount {
		return fmt.Sprintf("单日买入预算不足: 已报 %.0f + 本次 %.0f > 预算 %.0f", spent, amount, cfg.DailyBudgetAmount)
	}
	if cfg.InitialCapital > 0 {
		brokerFresh := false
		if acc, aerr := g.st.GetRealAccount(g.userID); aerr == nil && acc.AvailableCash > 0 {
			if at, perr := time.ParseInLocation("2006-01-02 15:04:05", acc.UpdatedAt, cntime.Loc); perr == nil &&
				g.now().Sub(at) <= 10*time.Minute {
				brokerFresh = true
			}
		}
		if !brokerFresh {
			pos, perr := g.st.RealPositionsForUser(g.userID)
			if perr != nil {
				return fmt.Sprintf("read real positions: %v", perr)
			}
			held := 0.0
			for _, p := range pos {
				held += p.CostPrice * float64(p.Qty)
			}
			avail := cfg.InitialCapital - held - spent
			if !cfg.Money.TrustBrokerFreezeEnabled() {
				avail -= g.st.LocalBuyFrozen(g.userID, today)
			}
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
	if acc, err := g.st.GetRealAccount(g.userID); err == nil && acc.AvailableCash > 0 {
		at, perr := time.ParseInLocation("2006-01-02 15:04:05", acc.UpdatedAt, cntime.Loc)
		if perr == nil {
			cap := acc.AvailableCash
			fresh := g.now().Sub(at) <= 10*time.Minute
			if !fresh {
				cap = acc.AvailableCash * 0.5
			}
			label := "实时"
			if !fresh {
				label = "陈旧保守折算50%"
			}
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
			if amount > acc.AvailableCash*0.5 {
				return fmt.Sprintf("可用资金不足(券商口径, 时间戳异常保守折算50%%): 上限 %.2f < 本次 %.2f",
					acc.AvailableCash*0.5, amount)
			}
		}
	}
	return ""
}
