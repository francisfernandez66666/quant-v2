package paper

// §SHORT-3 模拟盘融券做空侧（docs/SHORT_STRATEGIES_PLAN_20260912.md 决策④）。
//
// 独立于做多账本的「融券」模拟：做空战法通过信号触发「融券卖出开仓」形成负持仓，
// 价格下跌时按现价「买回平仓」获利、上涨超过止损阈值强制买回止损。做空池 shortCash
// 与做多 cash/pools 完全隔离（独立预算 ShortCapital，默认 0=不开设即整侧关闭）；
// 开仓冻结保证金 = 开仓名义额 × 保证金率（默认 50%），融券利息按自然日对开仓名义额
// 计提（年化费率默认 8.3%）；T+1 同规（当日开仓次日方可买回）。净值口径：
//
//	总权益 = 做多(cash + 持仓市值) + 做空(shortCash + Σ冻结保证金 + Σ浮动盈亏)
//
// 卖空开仓复用「不伪造成交」契约（行情缺失一律不撮合）与涨停封板对称守卫（跌停封板
// 无法融券卖出开仓）。所有方法以 *Locked 变体供持锁调用，公开方法负责加锁。
//
// English: §SHORT-3 the paper short-selling (margin-lending simulation) book. Bear-tactic pass
// signals open short positions; price drops realize profit on buy-back, rallies beyond the stop
// threshold force-cover. shortCash is fully isolated from the long cash/pools (dedicated
// ShortCapital budget, 0 = the whole side is off). Opening freezes margin = notional ×
// margin-rate (50%), lending interest accrues daily on the opening notional (8.3% annual), and
// T+1 applies (open today, cover next day). It reuses the "never fabricate a fill" contract (no
// quote → no fill) and a limit-down-open guard symmetric to the buy-side limit-up guard.

import (
	"errors"
	"fmt"
	"log"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/data"
)

// 融券做空侧错误集合。
// English: sentinel errors for the short book.
var (
	errShortDisabled   = errors.New("融券做空未启用")            // ShortEnabled=false or ShortCapital<=0
	errShortHeldDup    = errors.New("该票已持有融券空头")          // already short
	errShortCash       = errors.New("做空池可用资金不足")          // short pool cash short
	errShortTPlusOne   = errors.New("T+1限制：当日融券开仓次日方可买回") // same-day open can't cover
	errShortNoPosition = errors.New("无该票融券空头")            // not short
	errShortLimitDown  = errors.New("跌停封板无法融券卖出")         // sealed limit-down → can't sell-to-open
)

// ShortPosition 一笔融券空头持仓（负持仓语义，Qty 记为正数=借出卖出股数）。
// MarginUsed 为开仓冻结的保证金，FeeAccrued 为累计融券利息，Mark 为最近估值价（现价）。
// 浮动盈亏 = (开仓价 − 现价)×数量 − 已计提利息（做空：价格跌才赚）。
// English: one short position (Qty positive = shares borrowed and sold). MarginUsed is the frozen
// opening margin, FeeAccrued the accrued lending interest, Mark the last live price. Floating P&L
// = (open − mark) × qty − accrued fees (a short profits when price falls).
type ShortPosition struct {
	Code     string `json:"code"`     // 代码
	Name     string `json:"name"`     // 名称
	Strategy string `json:"strategy"` // 触发战法中文名
	// StrategyType 做空战法类型（high_churn/break_down/leader_decay/good_news_fade）。
	// English: the bear-tactic type that opened this short.
	StrategyType string    `json:"strategy_type"`
	Qty          int       `json:"qty"`          // 融券卖出的股数（正数，代表欠券数量）
	OpenPrice    float64   `json:"open_price"`   // 融券开仓价（卖出成交价）
	MarginUsed   float64   `json:"margin_used"`  // 冻结保证金 = OpenPrice×Qty×保证金率
	SignalPrice  float64   `json:"signal_price"` // 信号价（参照）
	FeeAccrued   float64   `json:"fee_accrued"`  // 累计融券利息（已计提）
	Mark         float64   `json:"mark"`         // 最近现价（买回估值价）
	SignalAt     time.Time `json:"signal_at"`    // 信号时间
	FilledAt     time.Time `json:"filled_at"`    // 开仓成交时间
}

// MarketValue 融券空头当前买回成本（现价 × 欠券数）。
// English: current cost to cover the short (mark × qty).
func (s *ShortPosition) MarketValue() float64 { return s.Mark * float64(s.Qty) }

// FloatPnl 空头浮动盈亏 = (开仓价 − 现价)×数量 − 已计提利息。
// English: floating P&L of the short = (open − mark) × qty − accrued interest.
func (s *ShortPosition) FloatPnl() float64 {
	return (s.OpenPrice-s.Mark)*float64(s.Qty) - s.FeeAccrued
}

// FloatPnlPct 空头浮动收益率（相对冻结保证金，保证金为 0 时按开仓名义额兜底）。
// English: floating return percent, relative to frozen margin (notional fallback if margin 0).
func (s *ShortPosition) FloatPnlPct() float64 {
	base := s.MarginUsed
	if base <= 0 {
		base = s.OpenPrice * float64(s.Qty)
	}
	if base <= 0 {
		return 0
	}
	return s.FloatPnl() / base * 100
}

// ShortBookEnabled 融券做空侧是否启用（ShortEnabled 且做空池预算 >0）。
// English: whether the short side is on (flag set and a funded short pool).
func (e *Engine) ShortBookEnabled() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.shortBookEnabledLocked()
}

func (e *Engine) shortBookEnabledLocked() bool {
	return e.cfg.Enabled && e.cfg.ShortEnabled && e.cfg.ShortCapital > 0
}

// ShortHolds 该票是否已有融券空头（供 engine 侧决策：不做多/不重复开）。
// English: whether the code already carries an open short.
func (e *Engine) ShortHolds(code string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, ok := e.shorts[code]
	return ok
}

// ShortPositions 返回当前融券空头持仓副本列表（registry 钉行情监控 / 前端展示用）。
// English: a copy of all open short positions (for quote-pinning and the frontend short card).
func (e *Engine) ShortPositions() []ShortPosition {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]ShortPosition, 0, len(e.shorts))
	for _, s := range e.shorts {
		out = append(out, *s)
	}
	return out
}

// ShortOpenManual 手动融券开仓（供 API/测试；须持实时价）。返回开仓股数与错误。
// English: manual short-open (API/tests); requires a live quote; returns filled qty and error.
func (e *Engine) ShortOpenManual(code, name, strategy, strategyType string, signalPrice float64, quotes map[string]*data.StockInfo) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.shortOpenLocked(code, name, strategy, strategyType, signalPrice, quotes, "手动融券")
}

// shortOpenLocked 融券卖出开仓（须持锁调用）。做空战法信号/手动共用主路：
//   - 前置：做空侧启用、无同票空头、当日未重复开、行情有效、未跌停封板（对称于涨停拒买）；
//   - 名义额 = min(单笔预算, shortCash/保证金率)（受做空池可用约束），整手向下取整，不足一手拒；
//   - 冻结保证金占用 shortCash（开仓价×数量×保证金率），利息从当日开始计提；
//   - 留痕融券订单 + 成交记录（side=short_open）并落盘。
//     English: short-sell-to-open (caller holds the lock), shared by tactic and manual paths.
//     Guards: side on, no existing short, not already opened today, valid quote, not limit-down-sealed.
//     Notional = min(per-trade budget, shortCash/marginRate) floored to a 100-share lot; frozen margin
//     occupies shortCash; interest accrues from today; an order + a short_open fill are audited.
func (e *Engine) shortOpenLocked(code, name, strategy, strategyType string, signalPrice float64, quotes map[string]*data.StockInfo, kind string) (int, error) {
	now := time.Now()
	if !e.shortBookEnabledLocked() {
		return 0, errShortDisabled
	}
	if code == "" {
		return 0, errors.New("融券开仓缺少代码")
	}
	if _, held := e.shorts[code]; held {
		return 0, errShortHeldDup
	}
	// 同票当日只开一次融券（5s 探针反复喂同一战法信号不重复开）。
	if e.shortOpenSeen == nil {
		e.shortOpenSeen = map[string]string{}
	}
	if e.shortOpenSeen[code] == cntime.DayOf(now) {
		return 0, errShortHeldDup
	}
	q := quotes[code]
	if q == nil || q.Price <= 0 {
		return 0, errNoQuote // 复用「不伪造成交」：无实时价不开仓
	}
	// 跌停封板无法融券卖出（对称于买入侧涨停封板守卫）。
	if q.ChangePct <= -LimitUpPct(code, name) { // 分板块跌停幅（与买入侧涨停守卫对称）
		e.recordBuyRejectLocked(Order{Code: code, Name: name, Strategy: strategy, StrategyType: strategyType,
			Side: "short", Kind: kind, SignalPrice: signalPrice, Status: "rejected", CreatedAt: now},
			fmt.Sprintf("跌停封板无法融券卖出(%.1f%%)", q.ChangePct))
		return 0, errShortLimitDown
	}
	rate := e.shortMarginRateLocked()
	// 名义额上限：单笔预算 与 做空池可支撑(=shortCash/保证金率) 取小，保证保证金占用不超池现金。
	notional := e.cfg.ShortFixedAmount
	if notional <= 0 {
		notional = e.cfg.FixedAmount
	}
	if capByMargin := e.shortCash / rate; capByMargin < notional {
		notional = capByMargin
	}
	qty := int(notional/q.Price) / 100 * 100
	if qty < 100 {
		return 0, errShortCash
	}
	price := e.applyOpenShortPriceLocked(q.Price) // 开仓卖价下浮滑点（保守）
	margin := price * float64(qty) * rate
	if margin > e.shortCash+1e-6 {
		// 缩到做空池可承受的整手（防御浮点/滑点导致的微超）。
		qty = int((e.shortCash/rate)/price) / 100 * 100
		if qty < 100 {
			return 0, errShortCash
		}
		margin = price * float64(qty) * rate
	}
	sp := &ShortPosition{
		Code: code, Name: name, Strategy: strategy, StrategyType: strategyType,
		Qty: qty, OpenPrice: price, MarginUsed: margin, SignalPrice: signalPrice,
		Mark: price, SignalAt: now, FilledAt: now,
	}
	// 融券卖出开仓按成交计费（佣金+印花税，与普通卖出同口径）：费用记入 FeeAccrued（应计负债，
	// 随浮动盈亏反映），现金在买回平仓时随结算一次性扣付——开仓只冻结保证金，权益不被重复扣减。
	// English: opening trade fees (commission + stamp) accrue into FeeAccrued (visible via floating
	// P&L) and are settled in cash at cover; the open itself freezes margin only, so equity is
	// never double-charged.
	sp.FeeAccrued = e.sellFeeLocked(price * float64(qty))
	e.shortCash -= margin // 保证金冻结（离开可用现金，仍在做空权益内）
	e.shorts[code] = sp
	e.shortOpenSeen[code] = cntime.DayOf(now)
	e.hasFilled = true // 融券开仓也算成交，使净值曲线开始记录
	e.trades = append(e.trades, Trade{
		Code: code, Name: name, Strategy: strategy, StrategyType: strategyType,
		Side: "short_open", Price: price, SignalPrice: signalPrice, Qty: qty,
		Amount: price * float64(qty), Time: now, Reason: kind,
	})
	e.recordOrderLocked(Order{Code: code, Name: name, Strategy: strategy, StrategyType: strategyType,
		Side: "short", Kind: kind, SignalPrice: signalPrice, Price: price, Qty: qty,
		Status: "filled", Reason: kind, CreatedAt: now})
	e.persist()
	log.Printf("[paper][short] 融券开仓 %s(%s) %d股 @%.2f 占用保证金%.0f 费率%.1f%% 原因=%s",
		code, name, qty, price, margin, rate*100, kind)
	return qty, nil
}

// shortOpenFromSignalLocked 从做空战法信号触发融券开仓（OnSignals 持锁路径调用）；
// 仅在信号为做空战法且（watch 或 sell）时尝试；已持多头的票不开空（决策②先走平多，避免同码多空双开）。
// English: open a short from a bear-tactic signal (called under OnSignals' lock). Skips codes the
// long book already holds (decision ② closes those longs instead — never long+short the same code).
func (e *Engine) shortOpenFromSignalLocked(s *combat_agent.Signal, quotes map[string]*data.StockInfo) {
	if _, heldLong := e.positions[s.Code]; heldLong {
		return
	}
	qty, err := e.shortOpenLocked(s.Code, s.Name, s.Strategy, s.StrategyType, s.Price, quotes, "做空战法自动")
	if err != nil && !errors.Is(err, errShortDisabled) && !errors.Is(err, errShortHeldDup) {
		log.Printf("[paper][short] 战法开仓跳过 %s(%s): %v (qty=%d)", s.Code, s.Name, err, qty)
	}
}

// ShortCoverManual 手动买回平仓（现价成交，含滑点上浮+佣金）。返回结算的已实现盈亏。
// English: manual buy-to-cover at the live price (slippage + commission). Returns realized P&L.
func (e *Engine) ShortCoverManual(code string, quotes map[string]*data.StockInfo) (float64, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.shortCoverLocked(code, 0, "手动买回", quotes, time.Now())
}

// ShortCover 手动买回平仓（API 入口）：price>0 用指定价（可无实时价），0=实时价。
// English: manual cover for the API — a positive price is used as-is, zero falls back to the quote.
func (e *Engine) ShortCover(code string, price float64, quotes map[string]*data.StockInfo) (float64, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.shortCoverLocked(code, price, "手动买回", quotes, time.Now())
}

// shortCoverLocked 融券买回平仓（须持锁）：price>0 用给定价（止损/信号），否则取实时现价；
// 行情缺失拒平（不伪造）；T+1 拦截当日开仓；结算盈亏=(开仓价−买回价)×数量−累计利息−买回佣金，
// 解冻保证金回流做空池。返回已实现盈亏。
// English: buy-to-cover (caller holds the lock). Uses the given price when >0 else the live quote;
// refuses on missing quote (no fabricated fill) and on same-day open (T+1). Realized =
// (open−cover)×qty − accrued interest − cover commission; frozen margin returns to the pool.
func (e *Engine) shortCoverLocked(code string, price float64, reason string, quotes map[string]*data.StockInfo, now time.Time) (float64, error) {
	sp, ok := e.shorts[code]
	if !ok {
		return 0, errShortNoPosition
	}
	if price <= 0 {
		q := quotes[code]
		if q == nil || q.Price <= 0 {
			return 0, errNoQuote
		}
		price = q.Price
	}
	if !canSellToday(sp.FilledAt, now) {
		log.Printf("[paper][short] T+1拦截 %s(%s) 当日融券开仓不可买回", sp.Code, sp.Name)
		return 0, errShortTPlusOne
	}
	coverPrice := e.applyCoverPriceLocked(price) // 买回价上浮滑点（保守）
	gross := coverPrice * float64(sp.Qty)
	commission := e.buyFeeLocked(gross)
	realized := (sp.OpenPrice-coverPrice)*float64(sp.Qty) - sp.FeeAccrued - commission
	// 解冻保证金回流 + 结算盈亏入池（权益连续：开仓冻结的 MarginUsed 此刻归还）。
	e.shortCash += sp.MarginUsed + realized
	e.shortRealized += realized
	e.trades = append(e.trades, Trade{
		Code: sp.Code, Name: sp.Name, Strategy: sp.Strategy, StrategyType: sp.StrategyType,
		Side: "short_cover", Price: coverPrice, Qty: sp.Qty, Amount: gross,
		Time: now, Fee: commission, Reason: reason,
	})
	e.recordOrderLocked(Order{Code: sp.Code, Name: sp.Name, Strategy: sp.Strategy, StrategyType: sp.StrategyType,
		Side: "short", Kind: "融券买回", Price: coverPrice, Qty: sp.Qty, Status: "filled",
		Reason: reason, CreatedAt: now})
	log.Printf("[paper][short] 融券买回 %s(%s) %d股 @%.2f 开仓价%.2f 已计息%.2f 实现盈亏%.2f 原因=%s",
		sp.Code, sp.Name, sp.Qty, coverPrice, sp.OpenPrice, sp.FeeAccrued, realized, reason)
	delete(e.shorts, code)
	// 注意：不删 shortOpenSeen 日标记——同日同码只允许一笔融券开仓（防「止损买回→信号重放
	// 再开→T+1 又被锁」的日内抖动；隔日自然可再开）。
	e.persist()
	return realized, nil
}

// shortMarginRateLocked 生效保证金率（≤0 归一为默认 0.5）。
// English: effective margin rate (non-positive normalized to the 0.5 default).
func (e *Engine) shortMarginRateLocked() float64 {
	if e.cfg.ShortMarginRate > 0 {
		return e.cfg.ShortMarginRate
	}
	return 0.5
}

// applyOpenShortPriceLocked 融券开仓价 = 现价下浮滑点（卖出拿更差价，保守）；0=不启用。
// English: short-open price marks the live price DOWN by slippage (a worse sell price — conservative).
func (e *Engine) applyOpenShortPriceLocked(price float64) float64 {
	if e.cfg.SlippageBps > 0 {
		return price * (1 - e.cfg.SlippageBps/10000.0)
	}
	return price
}

// applyCoverPriceLocked 融券买回价 = 现价上浮滑点（回补花更多钱，保守）；0=不启用。
// English: cover price marks the price UP by slippage (paying more to buy back — conservative).
func (e *Engine) applyCoverPriceLocked(price float64) float64 {
	if e.cfg.SlippageBps > 0 {
		return price * (1 + e.cfg.SlippageBps/10000.0)
	}
	return price
}

// shortMarkLocked 用实时快照刷新所有空头估值价（MarkToMarket 持锁路径调用）；有变动返回 true。
// English: refresh every short's mark from the live snapshot (from MarkToMarket, lock held); true if
// any mark changed.
func (e *Engine) shortMarkLocked(quotes map[string]*data.StockInfo) bool {
	changed := false
	for code, s := range e.shorts {
		if q, ok := quotes[code]; ok && q != nil && q.Price > 0 && s.Mark != q.Price {
			s.Mark = q.Price
			changed = true
		}
	}
	return changed
}

// shortAccrueFeesLocked 按自然日对每笔空头开仓名义额计提融券利息（年化费率/365），
// 每码每日只提一次（shortFeeDone 去重）；利息计入 FeeAccrued（应计负债，平仓时扣付）。
// 返回是否有计提发生（供上层落盘）。
// English: accrues daily lending interest on each short's opening notional (annual/365), once per code
// per calendar day (shortFeeDone dedup); adds to FeeAccrued and debits the short pool. Reports whether
// anything accrued (caller persists).
func (e *Engine) shortAccrueFeesLocked(now time.Time) bool {
	if len(e.shorts) == 0 {
		return false
	}
	annual := e.cfg.ShortFeeAnnual
	if annual <= 0 {
		return false // 未配费率=不收息（测试/兼容）
	}
	if e.shortFeeDone == nil {
		e.shortFeeDone = map[string]string{}
	}
	day := cntime.DayOf(now)
	daily := annual / 365.0
	accrued := false
	for code, s := range e.shorts {
		if e.shortFeeDone[code] == day {
			continue
		}
		fee := s.OpenPrice * float64(s.Qty) * daily
		s.FeeAccrued += fee // 应计负债：买回平仓时随结算扣付（权益经 FloatPnl 反映，不双扣现金）
		e.shortFeeDone[code] = day
		accrued = true
	}
	return accrued
}

// shortStopLossLocked 检查空头涨幅止损：现价 ≥ 开仓价×(1+止损%) → 强制买回（T+1 未解禁则本轮跳过，
// 下轮探针重试）。返回买回数量。
// English: force-cover shorts whose live mark rallied past open×(1+stop%) (T+1-locked covers retry
// next round). Returns the number covered.
func (e *Engine) shortStopLossLocked(now time.Time) int {
	if e.cfg.ShortStopLossPct <= 0 {
		return 0
	}
	th := 1 + e.cfg.ShortStopLossPct/100.0
	covered := 0
	for code, s := range e.shorts {
		if s.Mark > 0 && s.Mark >= s.OpenPrice*th {
			if _, err := e.shortCoverLocked(code, s.Mark, fmt.Sprintf("做空止损(涨≥%.0f%%)", e.cfg.ShortStopLossPct), nil, now); err == nil {
				covered++
			}
		}
	}
	return covered
}

// shortMarkToMarketLocked 做空侧估值总入口（MarkToMarket 尾部持锁调用）：刷新现价 →
// 按日计提融券利息 → 涨幅止损强制买回；任一发生变化即落盘。
// English: the short-side marking entry (called from MarkToMarket under the lock): refresh marks,
// accrue daily lending interest, force-cover rallies; persists when anything changed.
func (e *Engine) shortMarkToMarketLocked(quotes map[string]*data.StockInfo) {
	if len(e.shorts) == 0 {
		return
	}
	now := time.Now()
	changed := e.shortMarkLocked(quotes)
	if e.shortAccrueFeesLocked(now) {
		changed = true
	}
	if e.shortStopLossLocked(now) > 0 {
		changed = true
	}
	if changed {
		e.persist()
	}
}

// shortEquityLocked 做空侧当前权益 = shortCash + Σ冻结保证金 + Σ浮动盈亏（净值曲线并入）。
// English: short-side equity = pool cash + Σ frozen margin + Σ floating P&L (merged into the curve).
func (e *Engine) shortEquityLocked() float64 {
	eq := e.shortCash
	for _, s := range e.shorts {
		eq += s.MarginUsed + s.FloatPnl()
	}
	return eq
}

// ShortSnapshot 做空侧展示快照（前端融券卡）。
// English: a display snapshot of the short book for the frontend margin-short card.
type ShortSnapshot struct {
	Enabled     bool            `json:"enabled"`
	Cash        float64         `json:"cash"`         // 做空池可用现金
	Equity      float64         `json:"equity"`       // 做空侧权益（现金+冻结保证金+浮动）
	Realized    float64         `json:"realized"`     // 已实现盈亏累计
	MarginUsed  float64         `json:"margin_used"`  // 当前冻结保证金合计
	FloatingPnl float64         `json:"floating_pnl"` // 当前浮动盈亏合计
	FeeAccrued  float64         `json:"fee_accrued"`  // 当前持仓累计已计利息
	Positions   []ShortPosition `json:"positions"`    // 空头持仓
}

// ShortBook 返回做空侧快照（公开只读；前端显隐由 Enabled 决定，决策⑤）。
// English: returns the short-book snapshot (read-only; the frontend shows it only when Enabled).
func (e *Engine) ShortBook() ShortSnapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	sb := ShortSnapshot{Enabled: e.shortBookEnabledLocked(), Cash: round2(e.shortCash),
		Equity: round2(e.shortEquityLocked()), Realized: round2(e.shortRealized), MarginUsed: 0, FloatingPnl: 0, FeeAccrued: 0}
	for _, s := range e.shorts {
		sb.Positions = append(sb.Positions, *s)
		sb.MarginUsed += s.MarginUsed
		sb.FloatingPnl += s.FloatPnl()
		sb.FeeAccrued += s.FeeAccrued
	}
	sb.MarginUsed = round2(sb.MarginUsed)
	sb.FloatingPnl = round2(sb.FloatingPnl)
	sb.FeeAccrued = round2(sb.FeeAccrued)
	return sb
}
