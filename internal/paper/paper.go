// Package paper 实现独立模拟盘（纸面交易）：把策略 buy 信号按实时快照价自动撮合成虚拟持仓，
// 用实时价每日估值产出净值曲线，并记录「信号价 vs 成交价」的滑点与「信号发出→成交」延迟，
// 用于印证信号质量与时效性对收益的影响。
// 与真实持仓体系完全隔离：独立存储（paper.json），清盘/重置不影响任何实盘数据。
// English: independent paper-trading engine. It auto-fills strategy buy signals at the live snapshot
// price into virtual positions, marks the book to market daily for an equity curve, and records the
// signal-vs-fill price slippage plus the signal-to-fill latency — evidence of signal quality and
// timeliness impact on returns. Fully isolated from the real book: its own storage (paper.json), and
// a reset never touches live data.
package paper

import (
	"encoding/json"
	"log"
	"math"
	"os"
	"sort"
	"sync"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/data"
)

// Config 模拟盘配置（rules.paper）。
// English: paper-trading config (rules.paper).
type Config struct {
	Enabled        bool    `json:"enabled"`         // 总开关（默认 false）
	FixedAmount    float64 `json:"fixed_amount"`    // 每票固定买入资金（元，默认 10000；现金不足时按剩余现金整手买入）
	MaxPositions   int     `json:"max_positions"`   // 自定义持仓上限（0=不设限，持仓数由本金/现金余额自然决定；默认 0）
	InitialCapital float64 `json:"initial_capital"` // 初始资金（元，默认 100000）
}

// DefaultConfig 返回模拟盘出厂默认配置。
// English: returns the paper engine's factory-default config.
func DefaultConfig() Config {
	return Config{
		Enabled:        false,
		FixedAmount:    10000,
		MaxPositions:   0,
		InitialCapital: 100000,
	}
}

// Position 模拟持仓。
// SignalPrice 为信号发出时的信号价（辅助参照），CostPrice 为实际撮合价（实时价）。
// English: a paper position. SignalPrice is the reference signal price; CostPrice is the actual
// fill price (live price).
type Position struct {
	Code     string `json:"code"`
	Name     string `json:"name"`
	Strategy string `json:"strategy"`
	// StrategyType 该持仓所属战法池类型（fillLocked 时由信号 StrategyType 记录；
	// 卖出时收益按此回池。旧数据/手动买入为空 → 归"其他池"）。
	// English: the strategy-pool type this position belongs to (recorded at fill from the signal's
	// StrategyType; sale proceeds return to that pool on exit. Empty for legacy data / manual buys,
	// which fall into the "other" pool).
	StrategyType string    `json:"strategy_type,omitempty"`
	Qty          int       `json:"qty"`
	CostPrice    float64   `json:"cost_price"`
	Cost         float64   `json:"cost"`
	SignalPrice  float64   `json:"signal_price"`
	SignalAt     time.Time `json:"signal_at"`
	FilledAt     time.Time `json:"filled_at"`
	// Mark 最近一次估值价（实时快照，前端展示现价/浮盈用）。
	// English: last mark price from the live snapshot, for live P/L display.
	Mark float64 `json:"mark"`
}

// MarketValue 返回该持仓按当前估值价的市值。
// English: returns the position's market value at the current mark price.
func (p *Position) MarketValue() float64 { return p.Mark * float64(p.Qty) }

// MarshalJSON 输出持仓字段外加浮动盈亏/滑点/延迟等计算值（前端直接展示）。
// English: marshals the position with computed floating P/L, slippage and latency for the frontend.
func (p Position) MarshalJSON() ([]byte, error) {
	type alias Position
	return json.Marshal(&struct {
		alias
		Pnl         float64 `json:"pnl"`
		PnlPct      float64 `json:"pnl_pct"`
		SlippagePct float64 `json:"slippage_pct"`
		LatencySec  int64   `json:"latency_sec"`
	}{
		alias:       alias(p),
		Pnl:         p.PnL(),
		PnlPct:      round2(p.PnLPct()),
		SlippagePct: round2(p.SlippagePct()),
		LatencySec:  p.LatencySec(),
	})
}

// PnL 返回浮动盈亏（正=盈利）。
// English: returns the floating P/L (positive = profit).
func (p *Position) PnL() float64 { return (p.Mark - p.CostPrice) * float64(p.Qty) }

// PnLPct 返回浮动盈亏百分比（相对成本）。
// English: returns the floating P/L percent relative to cost.
func (p *Position) PnLPct() float64 {
	if p.Cost <= 0 {
		return 0
	}
	return (p.MarketValue() - p.Cost) / p.Cost * 100
}

// SlippagePct 返回「成交价 vs 信号价」滑点百分比（正=买贵了）。
// English: the fill-vs-signal price slippage percent (positive = filled higher than signal).
func (p *Position) SlippagePct() float64 {
	if p.SignalPrice <= 0 {
		return 0
	}
	return (p.CostPrice - p.SignalPrice) / p.SignalPrice * 100
}

// LatencySec 返回信号发出→成交的延迟秒数。
// English: returns the seconds from signal generation to fill.
func (p *Position) LatencySec() int64 {
	if p.SignalAt.IsZero() {
		return 0
	}
	d := int64(p.FilledAt.Sub(p.SignalAt).Seconds())
	if d < 0 {
		return 0
	}
	return d
}

// Trade 一笔模拟成交记录（买入含信号价参照与延迟）。
// English: one paper fill. Buys carry the reference signal price and latency.
type Trade struct {
	Code     string `json:"code"`
	Name     string `json:"name"`
	Strategy string `json:"strategy"`
	// StrategyType 该成交所属战法池类型（分池/盘后研究落库归类用；空=其他池/手动）。
	// English: the strategy-pool type of this fill (for pooling and post-close research export; empty =
	// the other pool / manual).
	StrategyType string    `json:"strategy_type,omitempty"`
	Side         string    `json:"side"` // buy / sell
	Price        float64   `json:"price"`
	SignalPrice  float64   `json:"signal_price,omitempty"`
	Qty          int       `json:"qty"`
	Amount       float64   `json:"amount"`
	Time         time.Time `json:"time"`
	// LatencySec 信号发出→成交 的秒数（买入时记录；量化信号时效性）。
	// English: seconds from signal generation to fill (recorded on buys; quantifies signal timeliness).
	LatencySec int64  `json:"latency_sec,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

// EquityPoint 净值序列（按交易日一个点）。
// English: an equity-curve point, one per trading day.
type EquityPoint struct {
	Date  string  `json:"date"`  // YYYY-MM-DD
	Value float64 `json:"value"` // 总资产 = 现金 + 持仓市值
	Cash  float64 `json:"cash"`
}

// Stats 绩效与信号质量汇总。
// English: performance and signal-quality summary.
type Stats struct {
	InitialCapital float64 `json:"initial_capital"`
	Cash           float64 `json:"cash"`
	MarketValue    float64 `json:"market_value"`
	TotalValue     float64 `json:"total_value"`
	TotalReturnPct float64 `json:"total_return_pct"`
	RealizedPnl    float64 `json:"realized_pnl"`
	OpenPositions  int     `json:"open_positions"`
	WinRatePct     float64 `json:"win_rate_pct"` // 已平仓胜率
	// 信号质量统计：买入信号→成交 的滑点与延迟
	FilledBuys        int     `json:"filled_buys"`         // 已撮合的买入信号数
	AvgSlippagePct    float64 `json:"avg_slippage_pct"`    // 平均滑点%（成交价相对信号价）
	AvgLatencySec     float64 `json:"avg_latency_sec"`     // 平均信号→成交延迟（秒）
	MaxLatencySec     int64   `json:"max_latency_sec"`     // 最大延迟（秒）
	SlippageCost      float64 `json:"slippage_cost"`       // 滑点累计成本（元，相对信号价多花的钱）
	SignalAmountPct   float64 `json:"signal_amount_pct"`   // 滑点成本占初始资金比（%）
	TodayReturnPct    float64 `json:"today_return_pct"`    // 当日收益%
	EquityCurvePoints int     `json:"equity_curve_points"` // 净值点数量
}

// StrategyPoolState 一个战法资金池的展示快照（前端分仓条）。
// English: one strategy cash pool's display snapshot (frontend allocation strip).
type StrategyPoolState struct {
	Key       string  `json:"key"`       // 策略类型（""=其他/手动池）
	Label     string  `json:"label"`     // 展示名
	Cash      float64 `json:"cash"`      // 池内可用现金
	RatioPct  float64 `json:"ratio_pct"` // 占总现金比例（%）
	Positions int     `json:"positions"` // 池内持仓数
}

// strategyPoolLabel 战法池类型 → 展示名。
// English: strategy-pool type → display label.
func strategyPoolLabel(t string) string {
	switch t {
	case "dragon":
		return "龙回头"
	case "double_bump":
		return "双响炮"
	case "n_shape":
		return "N形超短"
	case "dragon_return":
		return "龙回头中线"
	case "factor":
		return "因子战法"
	case "pattern":
		return "形态战法"
	}
	return "其他"
}

// Engine 模拟盘引擎：独立于真实持仓的虚拟撮合/估值/统计。
// path 为 JSON 持久化路径（空则不落盘，纯内存）。
// English: the paper engine — virtual fill/mark/statistics, isolated from the real book.
// path is the JSON persistence file (empty = in-memory only).
type Engine struct {
	cfg Config

	mu        sync.Mutex
	cash      float64
	pools     map[string]float64 // 战法资金池：key=策略类型（""=其他/手动池），Σpools == cash
	poolTypes []string           // 启用的战法类型（不含 ""），保持有序；空=未分仓（单池）
	positions map[string]*Position
	trades    []Trade
	equity    []EquityPoint
	realized  float64 // 已实现盈亏累计
	path      string
}

// New 创建模拟盘引擎并加载历史持久化数据。
// English: creates the paper engine and loads persisted state when present.
func New(cfg Config, path string) *Engine {
	if cfg.FixedAmount <= 0 {
		cfg.FixedAmount = 10000
	}
	if cfg.InitialCapital <= 0 {
		cfg.InitialCapital = 100000
	}
	e := &Engine{
		cfg:       cfg,
		cash:      cfg.InitialCapital,
		pools:     map[string]float64{"": cfg.InitialCapital}, // 默认单池（未分仓）兼容
		positions: make(map[string]*Position),
		path:      path,
	}
	if path != "" {
		e.load()
	}
	return e
}

// Enabled 返回模拟盘开关。
// English: reports whether paper trading is enabled.
func (e *Engine) Enabled() bool { return e.cfg.Enabled }

// Cfg 返回当前生效的配置副本。
// English: returns a copy of the effective config.
func (e *Engine) Cfg() Config {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cfg
}

// persistedState 持久化状态快照（persist/load 共享）。
// 直接序列化 Engine 会因 cash/positions 等私有字段被 json 忽略而写入空对象，
// 导致 load 读到 cash=0、模拟盘永久无法买入（errCash）。故显式落盘状态字段。
// English: the persisted-state snapshot shared by persist/load. Marshaling the Engine directly would
// drop the private fields (cash/positions/…), writing an empty object and loading cash=0 so the paper
// book could never fill. State fields are therefore persisted explicitly.
type persistedState struct {
	Cash           float64 `json:"cash"`
	InitialCapital float64 `json:"initial_capital,omitempty"` // 自定义初始资金（reset 设置；空历史时保留，重启后恢复）
	// 自定义持仓上限：>0 生效，0=不设限（由资金自然决定）。不用 omitempty，
	// 保证 0（不设限）也明确落盘可见，避免"上限设置没固化"的排查困惑。
	// English: custom position cap — applies when > 0, 0 = unlimited (driven by the balance).
	// No omitempty so that 0 (unlimited) is explicitly written to disk, avoiding "cap not persisted" confusion.
	MaxPositions int                  `json:"max_positions"`
	Positions    map[string]*Position `json:"positions"`
	Trades       []Trade              `json:"trades"`
	Equity       []EquityPoint        `json:"equity"`
	Realized     float64              `json:"realized"`
	// Pools 战法资金池（key=策略类型，""=其他池；Pools 与 PoolTypes 同时落盘，跨重启保留各池现金）。
	// 旧数据（无 Pools）兼容：load 时按现金建单池 {"": cash}，行为与分仓前完全一致。
	// English: strategy cash pools (key = strategy type, "" = the other pool; both Pools and PoolTypes are
	// persisted so per-pool cash survives restarts). Legacy data without Pools is compatible: load falls
	// back to the single pool {"": cash}, identical to pre-allocation behavior.
	Pools     map[string]float64 `json:"pools,omitempty"`
	PoolTypes []string           `json:"pool_types,omitempty"`
}

// tradeRetention 成交日志保留时长：3 个月，供战法效果/滑点/延迟分析。
// English: how long fill records are kept — 3 months, for strategy-effect / slippage / latency analysis.
const tradeRetention = 90 * 24 * time.Hour

// persist 将当前状态写入 JSON（幂等，失败仅记录日志）。
// 写入前先清理超过保留期（3 个月）的成交日志，避免无限膨胀。
// English: writes current state to the JSON file (best-effort; failures only log), trimming fills
// older than the retention window first so the log can't grow without bound.
func (e *Engine) persist() {
	if e.path == "" {
		return
	}
	e.trimTradesLocked()
	st := persistedState{
		Cash:           e.cash,
		InitialCapital: e.cfg.InitialCapital,
		MaxPositions:   e.cfg.MaxPositions,
		Positions:      e.positions,
		Trades:         e.trades,
		Equity:         e.equity,
		Realized:       e.realized,
		Pools:          e.pools,
		PoolTypes:      e.poolTypes,
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		log.Printf("[paper] 序列化失败: %v", err)
		return
	}
	if err := os.WriteFile(e.path, data, 0644); err != nil {
		log.Printf("[paper] 写入 %s 失败: %v", e.path, err)
	}
}

// load 从 JSON 恢复状态。
// English: restores state from the JSON file.
func (e *Engine) load() {
	raw, err := os.ReadFile(e.path)
	if err != nil {
		return
	}
	var st persistedState
	if err := json.Unmarshal(raw, &st); err != nil {
		log.Printf("[paper] 解析 %s 失败: %v", e.path, err)
		return
	}
	// 空/旧格式历史（无有效状态）：保留初始资金，避免 cash=0 导致永久无法买入。
	// English: empty/legacy history (no valid state) keeps the initial capital, avoiding a permanent
	// cash=0 that makes the paper book unable to fill.
	if st.Cash <= 0 && len(st.Positions) == 0 && len(st.Trades) == 0 {
		return
	}
	if st.InitialCapital > 0 {
		e.cfg.InitialCapital = st.InitialCapital
	}
	if st.MaxPositions > 0 {
		e.cfg.MaxPositions = st.MaxPositions
	}
	e.cash = st.Cash
	e.realized = st.Realized
	if st.Positions != nil {
		e.positions = st.Positions
	}
	e.trades = st.Trades
	e.equity = st.Equity
	// 恢复战法资金池：旧数据（无 Pools）按现金建单池（分仓前行为一致）；有 Pools 时还原各池现金，
	// 并按其求和兜底现金（历史现金字段与池和一致性修正）。
	// English: restore strategy pools — legacy data without Pools becomes the single pool {"": cash} (same as
	// before allocation); with Pools, per-pool cash is restored and the aggregate cash is reconciled to it.
	if st.Pools != nil && len(st.Pools) > 0 {
		e.pools = st.Pools
		e.poolTypes = st.PoolTypes
		sum := 0.0
		for _, v := range e.pools {
			sum += v
		}
		if sum > 0 {
			e.cash = sum
		}
	} else {
		e.pools = map[string]float64{"": e.cash}
	}
	e.trimTradesLocked()
}

// trimTradesLocked 清理超过保留期（3 个月）的成交记录（调用方须持锁）。
// 按成交时间过滤；trades 数组长度变化时原地压缩。供成交日志长期留档、分析用。
// English: drops fills older than the retention window (3 months); caller must hold the lock.
// Filters by fill time and compacts in place when the length changes. Keeps the trade log for analysis.
func (e *Engine) trimTradesLocked() {
	if len(e.trades) == 0 {
		return
	}
	cutoff := time.Now().Add(-tradeRetention)
	out := e.trades[:0]
	for _, t := range e.trades {
		if t.Time.After(cutoff) {
			out = append(out, t)
		}
	}
	if len(out) != len(e.trades) {
		e.trades = out
	}
}

// OnSignals 消费一轮策略信号做自动撮合：仅做多 buy 信号，用实时快照价成交固定资金。
// 同一股票已持仓则跳过；达持仓上限跳过。行情缺失时跳过该信号（不伪造成交）。
// 记录信号价作辅助参照 + 信号→成交延迟。
// English: auto-fills a round of strategy signals: long buy signals only, filled at the live
// snapshot price with a fixed capital per stock. Skip when already held or at max positions;
// skip when no live quote (no fake fills). Records the signal price as a reference and the
// signal-to-fill latency.
func (e *Engine) OnSignals(sigs []combat_agent.Signal, quotes map[string]*data.StockInfo) {
	if !e.cfg.Enabled {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	now := time.Now()
	for i := range sigs {
		s := sigs[i]
		if s.Direction == "做空" || s.Action != "buy" {
			continue
		}
		if _, held := e.positions[s.Code]; held {
			continue
		}
		// 自定义持仓上限：>0 时封顶；0（默认）不设限，持仓数由本金/现金余额自然决定。
		// English: custom position cap — enforced only when > 0; 0 (default) means unlimited, with the
		// position count naturally bounded by the capital / cash balance.
		if e.cfg.MaxPositions > 0 && len(e.positions) >= e.cfg.MaxPositions {
			return
		}
		var price float64
		if q, ok := quotes[s.Code]; ok && q != nil && q.Price > 0 {
			price = q.Price
		} else if s.Price > 0 {
			// 行情缺失回退信号价（尽量避免该票长期滞留在参考价上；成交仍以实时优先）
			price = s.Price
		} else {
			continue
		}
		// 战法分池：按信号 StrategyType 归池；类型未启用（无对应池）时跳过该信号，
		// 空类型（watch/手动）走"其他池"。池扣款由 fillLocked 完成。
		// English: strategy pooling — the signal debits its own pool by StrategyType; a type without a
		// pool (disabled) is skipped, and empty types (watch/manual) fall into the "other" pool.
		poolKey := s.StrategyType
		if poolKey != "" {
			if _, ok := e.pools[poolKey]; !ok {
				continue
			}
		}
		if err := e.fillLocked(poolKey, s.Code, s.Name, s.Strategy, s.Price, s.GeneratedAt, now, price, 0, s.Reason); err != nil {
			log.Printf("[paper] 撮合失败 %s(%s): %v", s.Code, s.Name, err)
		}
	}
	e.persist()
}

// fillLocked 按给定价格撮合一笔买入（调用方须持锁）。返回错误信息。
// poolKey 为该笔买入所属的战法资金池（""=其他/手动池）；扣款只扣对应池预算，池与池互不侵占。
// qty > 0 为显式手数（手动买入/加仓，超出池内资金直接失败）；
// qty <= 0 时按 FixedAmount 自动算整手，现金不足按池内剩余现金缩减到整手（不足一手跳过）。
// English: fills a buy at the given price (caller must hold the lock); returns an error on failure.
// poolKey is the strategy cash pool this buy debits ("" = the other/manual pool); pools never invade
// each other. qty > 0 is an explicit lot count (manual buy/add, fails when exceeding the pool balance);
// qty <= 0 auto-sizes to FixedAmount in whole lots, shrinking to the largest affordable lot on a
// shortfall (skipping below one lot).
func (e *Engine) fillLocked(poolKey, code, name, strategy string, signalPrice float64, signalAt, now time.Time, price float64, qty int, reason string) error {
	explicitQty := qty > 0
	if !explicitQty {
		qty = int(e.cfg.FixedAmount/price/100) * 100
	}
	if qty <= 0 {
		return errLotTooSmall // 一手都买不起（不足 A 股一手）
	}
	cost := float64(qty) * price
	pool := e.pools[poolKey]
	if cost > pool {
		if explicitQty {
			return errCash // 手动指定手数超出池内资金，不静默缩减
		}
		// 自动金额现金不足：按池内剩余现金缩减到整手；缩减后仍买不起一手则跳过
		// English: auto-amount cash short — shrink to the largest whole lot within the pool balance.
		qty = int(pool/price/100) * 100
		if qty <= 0 {
			return errCash
		}
		cost = float64(qty) * price
	}
	e.pools[poolKey] = pool - cost
	e.cash -= cost
	e.positions[code] = &Position{
		Code:         code,
		Name:         name,
		Strategy:     strategy,
		StrategyType: poolKey,
		Qty:          qty,
		CostPrice:    price,
		Cost:         cost,
		SignalPrice:  signalPrice,
		SignalAt:     signalAt,
		FilledAt:     now,
		Mark:         price,
	}
	latency := int64(0)
	if !signalAt.IsZero() {
		latency = int64(now.Sub(signalAt).Seconds())
		if latency < 0 {
			latency = 0
		}
	}
	e.trades = append(e.trades, Trade{
		Code:         code,
		Name:         name,
		Strategy:     strategy,
		StrategyType: poolKey,
		Side:         "buy",
		Price:        price,
		SignalPrice:  signalPrice,
		Qty:          qty,
		Amount:       cost,
		Time:         now,
		LatencySec:   latency,
		Reason:       reason,
	})
	log.Printf("[paper] 模拟买入 %s(%s) %d股 @%.2f 信号价%.2f 延迟%ds",
		code, name, qty, price, signalPrice, latency)
	return nil
}

// Buy 手动按实时价买入一只股票（前端信号页/持仓页"模拟买入"按钮触发，固定金额整手）。
// 与自动撮合共用 fillLocked：同一持仓去重/仓位上限/现金约束；手动买入归"其他池"。
// English: manually buys one stock at the live price (frontend/APK signal-page or positions-page "paper
// buy" button; fixed-amount whole lots). Shares fillLocked with auto-fill: dedupe / position cap / cash
// checks; manual buys debit the "other" pool.
func (e *Engine) Buy(code, name, strategy string, signalPrice float64, quotes map[string]*data.StockInfo) error {
	if !e.cfg.Enabled {
		return errDisabled
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, held := e.positions[code]; held {
		return errHeld
	}
	if e.cfg.MaxPositions > 0 && len(e.positions) >= e.cfg.MaxPositions {
		return errMaxPos
	}
	var price float64
	if q, ok := quotes[code]; ok && q != nil && q.Price > 0 {
		price = q.Price
	} else {
		return errNoQuote
	}
	if err := e.fillLocked("", code, name, strategy, signalPrice, time.Now(), time.Now(), price, 0, "手动模拟买入"); err != nil {
		return err
	}
	e.persist()
	return nil
}

// BuyEx 手动按指定价格与手数买入一只股票（普通用户模拟盘：输入买入价+买入手数，静态记账）。
// price > 0 时按用户输入价成交（不依赖行情）；price = 0 时回退实时价。
// qty 为手数（A 股一手 100 股，调用方已换算；<=0 拒绝）。手动买入归"其他池"，不挤占战法池。
// 已持仓时自动合并为加仓（加权平均成本，追加买入记录）。
// English: manually buys a stock at an explicit price and lot count (normal users' paper book: the buyer
// types the price and lots; static bookkeeping). A price > 0 fills at the typed price (no quote needed);
// price = 0 falls back to the live quote. qty is in board lots (1 lot = 100 shares; <=0 rejected).
// Manual buys debit the "other" pool and never crowd a strategy pool. An already-held code merges as an
// add-on (quantity added, cost averaged, extra fill appended).
func (e *Engine) BuyEx(code, name, strategy string, signalPrice, price float64, qty int, quotes map[string]*data.StockInfo) error {
	if !e.cfg.Enabled {
		return errDisabled
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if p, held := e.positions[code]; held {
		return e.addToPositionLocked(p, code, name, strategy, signalPrice, price, qty, "手动模拟加仓")
	}
	if e.cfg.MaxPositions > 0 && len(e.positions) >= e.cfg.MaxPositions {
		return errMaxPos
	}
	if qty <= 0 {
		return errLotTooSmall
	}
	if price <= 0 {
		if q, ok := quotes[code]; ok && q != nil && q.Price > 0 {
			price = q.Price
		} else {
			return errNoQuote
		}
	}
	if err := e.fillLocked("", code, name, strategy, signalPrice, time.Now(), time.Now(), price, qty, "手动模拟买入"); err != nil {
		return err
	}
	e.persist()
	return nil
}

// addToPositionLocked 已持仓加仓：加权平均成本、追加买入记录、从"其他池"扣款（须持锁调用）。
// English: adds to an existing position — quantity up, cost averaged, an extra buy fill appended, cash
// debited from the "other" pool (caller must hold the lock).
func (e *Engine) addToPositionLocked(p *Position, code, name, strategy string, signalPrice, price float64, qty int, reason string) error {
	if qty <= 0 {
		return errLotTooSmall
	}
	cost := float64(qty) * price
	pool := e.pools[""]
	if cost > pool {
		return errCash
	}
	e.pools[""] = pool - cost
	e.cash -= cost
	p.Cost += cost
	p.Qty += qty
	p.CostPrice = p.Cost / float64(p.Qty)
	p.Mark = price
	e.trades = append(e.trades, Trade{
		Code:         code,
		Name:         name,
		Strategy:     strategy,
		StrategyType: "",
		Side:         "buy",
		Price:        price,
		SignalPrice:  signalPrice,
		Qty:          qty,
		Amount:       cost,
		Time:         time.Now(),
		Reason:       reason,
	})
	log.Printf("[paper] 模拟加仓 %s(%s) +%d股 @%.2f 现持%d股 均价%.3f", code, name, qty, price, p.Qty, p.CostPrice)
	e.persist()
	return nil
}

// MarkToMarket 用实时快照价更新持仓估值价。
// English: updates position mark prices from the live snapshot.
func (e *Engine) MarkToMarket(quotes map[string]*data.StockInfo) {
	if !e.cfg.Enabled {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	changed := false
	for code, p := range e.positions {
		if q, ok := quotes[code]; ok && q != nil && q.Price > 0 {
			if p.Mark != q.Price {
				p.Mark = q.Price
				changed = true
			}
		}
	}
	if changed {
		e.persist()
	}
}

// Snapshot 记录当日净值（同一交易日只保留最新一个点）。
// 值变化才落盘：同日净值不变（价格未波动）不再重复写盘，
// 避免近实时循环每 5s 一次的无意义 IO/日志压力（低配服务器磁盘与 CPU 友好）。
// English: records the day's equity (one point per trading day, overwritten when the day repeats).
// Persists only when the value actually changes — a same-day identical value is not rewritten,
// cutting the pointless every-5s write (friendly to the small server's disk and CPU).
func (e *Engine) Snapshot(now time.Time) {
	if !e.cfg.Enabled {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	date := now.Format("2006-01-02")
	mv := e.marketValueLocked()
	val := e.cash + mv
	if len(e.equity) > 0 && e.equity[len(e.equity)-1].Date == date {
		last := &e.equity[len(e.equity)-1]
		if last.Value == val && last.Cash == e.cash {
			return
		}
		*last = EquityPoint{Date: date, Value: val, Cash: e.cash}
	} else {
		e.equity = append(e.equity, EquityPoint{Date: date, Value: val, Cash: e.cash})
	}
	e.persist()
}

// marketValueLocked 返回持仓市值（须持锁调用）。
// English: total market value of open positions (caller must hold the lock).
func (e *Engine) marketValueLocked() float64 {
	mv := 0.0
	for _, p := range e.positions {
		mv += p.MarketValue()
	}
	return mv
}

// Sell 手动按实时价卖出持仓（清仓）。返回错误信息（无持仓/行情缺失时 err != nil）。
// English: manually sells a position at the live price. Returns an error when not held / no quote.
func (e *Engine) Sell(code string, quotes map[string]*data.StockInfo) error {
	if !e.cfg.Enabled {
		return errDisabled
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	p, held := e.positions[code]
	if !held {
		return errNotHeld
	}
	var price float64
	if q, ok := quotes[code]; ok && q != nil && q.Price > 0 {
		price = q.Price
	} else {
		return errNoQuote
	}
	return e.sellAllLocked(p, price)
}

// SellEx 手动按指定价格与数量减仓（部分卖出）。qty 手数；price > 0 用输入价，price = 0 回退实时价。
// 数量 >= 当前持仓时退化为清仓（复用 sellAllLocked）。
// English: manually trims a position at an explicit price and count. qty is in board lots; price > 0 uses
// the typed price, price = 0 falls back to the live quote. A qty >= the position degrades to a full close
// (reuses sellAllLocked).
func (e *Engine) SellEx(code string, price float64, qty int, quotes map[string]*data.StockInfo) error {
	if !e.cfg.Enabled {
		return errDisabled
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	p, held := e.positions[code]
	if !held {
		return errNotHeld
	}
	if qty <= 0 {
		return errLotTooSmall
	}
	if price <= 0 {
		if q, ok := quotes[code]; ok && q != nil && q.Price > 0 {
			price = q.Price
		} else {
			return errNoQuote
		}
	}
	if qty >= p.Qty {
		return e.sellAllLocked(p, price)
	}
	proceeds := price * float64(qty)
	// 减仓收益回原战法池（按持仓记录的类型；空=其他池）。
	// English: trim proceeds return to the position's own strategy pool (per its recorded type; empty =
	// the other pool).
	e.pools[p.StrategyType] += proceeds
	e.cash += proceeds
	e.realized += (price - p.CostPrice) * float64(qty)
	p.Qty -= qty
	e.trades = append(e.trades, Trade{
		Code:         p.Code,
		Name:         p.Name,
		Strategy:     p.Strategy,
		StrategyType: p.StrategyType,
		Side:         "sell",
		Price:        price,
		Qty:          qty,
		Amount:       proceeds,
		Time:         time.Now(),
		Reason:       "手动模拟减仓",
	})
	log.Printf("[paper] 模拟减仓 %s(%s) -%d股 @%.2f 剩余%d股", p.Code, p.Name, qty, price, p.Qty)
	e.persist()
	return nil
}

// sellAllLocked 清仓单一持仓：回池、结算已实现盈亏、追加卖出记录（须持锁调用）。
// English: closes a single position — pool return, realized P&L, extra sell fill (caller must hold the lock).
func (e *Engine) sellAllLocked(p *Position, price float64) error {
	proceeds := price * float64(p.Qty)
	// 卖出收益回原战法池（按持仓记录的类型；空=其他池）。
	// English: sale proceeds return to the position's own strategy pool (per its recorded type; empty =
	// the other pool).
	e.pools[p.StrategyType] += proceeds
	e.cash += proceeds
	e.realized += proceeds - p.Cost
	e.trades = append(e.trades, Trade{
		Code:         p.Code,
		Name:         p.Name,
		Strategy:     p.Strategy,
		StrategyType: p.StrategyType,
		Side:         "sell",
		Price:        price,
		Qty:          p.Qty,
		Amount:       proceeds,
		Time:         time.Now(),
	})
	log.Printf("[paper] 模拟卖出 %s(%s) %d股 @%.2f 盈亏%.2f", p.Code, p.Name, p.Qty, price, proceeds-p.Cost)
	delete(e.positions, p.Code)
	e.persist()
	return nil
}

// SetStrategyPools 设置启用的战法资金池（按当前现金均分，key 集合 = types + ""）。
// 幂等：types 集合未变化时保留各池现金（热加载/重复注入安全）；
// 集合变化时按当前总现金重新均分并持久化。
// 注入路径：engine.ActivePoolTypes → registry.SetPaperPools → 各账号 pe.SetStrategyPools。
// English: configures the enabled strategy cash pools (split the current cash evenly, key set = types +
// ""). Idempotent: an unchanged type set keeps per-pool cash intact (safe for hot reload / repeated
// injection); a changed set re-splits the current total cash evenly and persists. Injected via
// engine.ActivePoolTypes → registry.SetPaperPools → each account's pe.SetStrategyPools.
func (e *Engine) SetStrategyPools(types []string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	// 规范化排序比较，集合比较与输入顺序无关
	// English: normalized sorted comparison makes the set comparison order-insensitive.
	sorted := append([]string(nil), types...)
	sort.Strings(sorted)
	cur := append([]string(nil), e.poolTypes...)
	sort.Strings(cur)
	if equalStrings(sorted, cur) {
		return // 集合未变：保留各池现金
	}
	e.poolTypes = append([]string(nil), types...)
	e.rebuildPoolsLocked()
	log.Printf("[paper] 战法资金池已按 %v 均分：每池 %.2f", e.poolTypes, e.pools[""])
}

// rebuildPoolsLocked 按当前 poolTypes 重建资金池：总现金均分到 types + "" 每池（须持锁调用）。
// 未分仓（poolTypes 为空）时退化为单池 {"": cash}，与分仓前行为一致。
// English: rebuilds the pools from poolTypes: the total cash is split evenly across types + "" (caller
// must hold the lock). With no allocation (empty poolTypes) it degrades to the single pool {"": cash},
// identical to pre-allocation behavior.
func (e *Engine) rebuildPoolsLocked() {
	if len(e.poolTypes) == 0 {
		e.pools = map[string]float64{"": e.cash}
		return
	}
	keys := append([]string(nil), e.poolTypes...)
	keys = append(keys, "") // 其他/手动池
	share := e.cash / float64(len(keys))
	e.pools = make(map[string]float64, len(keys))
	for _, k := range keys {
		e.pools[k] = share
	}
}

// equalStrings 顺序无关的字符串切片相等比较（SetStrategyPools 幂等判断用）。
// English: order-insensitive string-slice equality (used for SetStrategyPools idempotency).
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// StrategyPools 返回各战法资金池的展示快照（前端分仓条：池余量/占比/持仓数）。
// English: returns each strategy pool's display snapshot (frontend allocation strip: balance/ratio/positions).
func (e *Engine) StrategyPools() []StrategyPoolState {
	e.mu.Lock()
	defer e.mu.Unlock()
	total := 0.0
	for _, v := range e.pools {
		total += v
	}
	keys := append([]string(nil), e.poolTypes...)
	keys = append(keys, "")
	sort.Strings(keys)
	out := make([]StrategyPoolState, 0, len(keys))
	for _, k := range keys {
		cash := e.pools[k]
		ratio := 0.0
		if total > 0 {
			ratio = cash / total * 100
		}
		cnt := 0
		for _, p := range e.positions {
			if p.StrategyType == k {
				cnt++
			}
		}
		out = append(out, StrategyPoolState{
			Key: k, Label: strategyPoolLabel(k), Cash: cash, RatioPct: ratio, Positions: cnt,
		})
	}
	return out
}

// Reconfigure 确认资金：按新初始资金/持仓上限重开模拟盘，并落盘固化。
// 语义（对应前端"确认资金"按钮）：
//   - initialCapital > 0 时设置新初始资金（否则沿用当前初始资金）
//   - maxPositions >= 0 时设置新持仓上限（0 = 不设限，由资金自然决定）
//   - 现金重置为新初始资金，清空当前持仓与已实现盈亏
//   - 净值曲线从新资金重新记录（equity 清空，重新按日 Snapshot）
//   - 成交日志保留（trades 不清，3 个月自动清理继续生效）——历史交易固化，改资金不丢
//
// English: Reconfigure reopens the paper book with a new starting capital / position cap and persists.
// Semantics (the frontend "confirm capital" action):
//   - a positive initialCapital sets the new starting capital (otherwise the current one is kept)
//   - a non-negative maxPositions sets the new position cap (0 = unlimited, driven by the balance)
//   - cash resets to the new starting capital; current positions and realized P/L are cleared
//   - the equity curve restarts from the new capital (equity cleared, re-snapshotted daily)
//   - the fill log is preserved (trades kept; the 3-month retention still applies) — history survives a
//     capital change instead of being wiped.
func (e *Engine) Reconfigure(initialCapital float64, maxPositions int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if initialCapital > 0 {
		e.cfg.InitialCapital = initialCapital
	}
	if maxPositions >= 0 {
		e.cfg.MaxPositions = maxPositions
	}
	e.cash = e.cfg.InitialCapital
	e.positions = make(map[string]*Position)
	e.equity = nil
	e.realized = 0
	e.rebuildPoolsLocked() // 新资金按当前池集合重新均分
	e.persist()
}

// Reset 清盘重置：不改资金/持仓上限，按当前初始资金恢复现金，清空持仓/成交/净值/已实现盈亏。
// 对应前端"清盘重置"按钮——只清空重开，不修改用户已自定义的资金与上限设置。
// English: Reset liquidates the whole book without touching capital/cap: cash restores to the current
// initial capital, while positions/trades/equity/realized are all cleared. Backs the frontend
// "清盘重置" button — it only clears, never changes the user's customized capital and cap.
func (e *Engine) Reset() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.cash = e.cfg.InitialCapital
	e.positions = make(map[string]*Position)
	e.trades = nil
	e.equity = nil
	e.realized = 0
	e.rebuildPoolsLocked() // 现金恢复后按当前池集合重新均分
	e.persist()
}

// Deposit 注入资金（增量）：现金 += amount，并按当前各池占比分配新增资金，保留全部持仓/净值/成交日志。
// 收益基准 initial_capital 同步累计（+amount），使总收益基于真实累计投入计算，而非被注入稀释。
// 对应前端"注入资金"按钮（区别于清盘重置：只加钱、不清仓）。
// English: Deposit adds capital incrementally — cash += amount, distributed to the pools by their current
// share; positions / equity / fill log are all kept. The return basis (initial_capital) accumulates so
// total_return is computed against the true cumulative investment instead of being diluted by deposits.
// Backs the frontend "注入资金" action (unlike a liquidation reset: it only adds money, never clears).
func (e *Engine) Deposit(amount float64) {
	if amount <= 0 {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	total := 0.0
	for _, v := range e.pools {
		total += v
	}
	e.cash += amount
	e.cfg.InitialCapital += amount
	if total > 0 {
		for k, v := range e.pools {
			e.pools[k] = v + amount*(v/total)
		}
	} else if len(e.pools) > 0 {
		share := amount / float64(len(e.pools))
		for k := range e.pools {
			e.pools[k] = share
		}
	}
	log.Printf("[paper] 注入资金 +%.2f → 现金=%.2f 累计投入=%.2f 持仓保留=%d",
		amount, e.cash, e.cfg.InitialCapital, len(e.positions))
	e.persist()
}

// SetMaxPositions 设置自定义持仓上限（>0 生效；0/负数=不设限，由资金自然决定），并持久化。
// English: sets the custom position cap (>0 applies; <=0 means unlimited, driven by the balance) and persists it.
func (e *Engine) SetMaxPositions(n int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if n < 0 {
		n = 0
	}
	e.cfg.MaxPositions = n
	e.persist()
}

// Positions 返回持仓快照（按代码排序）。
// English: returns a snapshot of open positions (sorted by code).
func (e *Engine) Positions() []Position {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]Position, 0, len(e.positions))
	for _, p := range e.positions {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out
}

// Trades 返回成交记录（最新在前）。
// English: returns the fill records (newest first).
func (e *Engine) Trades() []Trade {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]Trade, len(e.trades))
	copy(out, e.trades)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time.After(out[j].Time) })
	return out
}

// Equity 返回净值序列。
// English: returns the equity curve.
func (e *Engine) Equity() []EquityPoint {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]EquityPoint, len(e.equity))
	copy(out, e.equity)
	return out
}

// Stats 汇总绩效与信号质量指标。
// English: aggregates performance and signal-quality metrics.
func (e *Engine) Stats() Stats {
	e.mu.Lock()
	defer e.mu.Unlock()
	mv := e.marketValueLocked()
	total := e.cash + mv
	st := Stats{
		InitialCapital: e.cfg.InitialCapital,
		Cash:           e.cash,
		MarketValue:    mv,
		TotalValue:     total,
		RealizedPnl:    e.realized,
		OpenPositions:  len(e.positions),
	}
	if e.cfg.InitialCapital > 0 {
		st.TotalReturnPct = (total - e.cfg.InitialCapital) / e.cfg.InitialCapital * 100
	}
	// 当日收益：最新净值 vs 前一交易日净值
	if len(e.equity) >= 2 {
		prev := e.equity[len(e.equity)-2].Value
		cur := e.equity[len(e.equity)-1].Value
		if prev > 0 {
			st.TodayReturnPct = (cur - prev) / prev * 100
		}
	}
	st.EquityCurvePoints = len(e.equity)

	// 胜率：按卖出记录相对对应持仓成本估算（简化：盈利卖出笔数 / 卖出笔数）
	wins, sells := 0, 0
	// 滑点统计：遍历买入成交，取每个买入的滑点与延迟
	var sumSlip, sumLat float64
	var maxLat int64
	for _, t := range e.trades {
		if t.Side == "buy" {
			if t.SignalPrice > 0 {
				sumSlip += (t.Price - t.SignalPrice) / t.SignalPrice * 100
			}
			sumLat += float64(t.LatencySec)
			if t.LatencySec > maxLat {
				maxLat = t.LatencySec
			}
			st.FilledBuys++
			if t.SignalPrice > 0 {
				st.SlippageCost += (t.Price - t.SignalPrice) * float64(t.Qty)
			}
		} else {
			sells++
			// 查找对应买入价：按代码向前匹配最近一笔 buy
			for i := len(e.trades) - 1; i >= 0; i-- {
				bt := e.trades[i]
				if bt.Side == "buy" && bt.Code == t.Code && bt.Time.Before(t.Time) {
					if (t.Price-bt.Price)*float64(t.Qty) > 0 {
						wins++
					}
					break
				}
			}
		}
	}
	if st.FilledBuys > 0 {
		st.AvgSlippagePct = sumSlip / float64(st.FilledBuys)
		st.AvgLatencySec = sumLat / float64(st.FilledBuys)
	}
	st.MaxLatencySec = maxLat
	if st.InitialCapital > 0 {
		st.SignalAmountPct = st.SlippageCost / st.InitialCapital * 100
	}
	if sells > 0 {
		st.WinRatePct = float64(wins) / float64(sells) * 100
	}
	st.TotalReturnPct = round2(st.TotalReturnPct)
	st.TodayReturnPct = round2(st.TodayReturnPct)
	st.AvgSlippagePct = round2(st.AvgSlippagePct)
	st.AvgLatencySec = math.Round(st.AvgLatencySec)
	st.SignalAmountPct = round2(st.SignalAmountPct)
	return st
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// 撮合错误定义：全部为中文可读消息，直接透传给前端弹窗/接口返回。
// （Fill errors: human-readable Chinese messages surfaced to the frontend directly.）
var (
	errDisabled    = errMsg("模拟盘未启用")
	errNotHeld     = errMsg("未持有该股票")
	errNoQuote     = errMsg("无实时行情，无法成交")
	errHeld        = errMsg("已持有该股票")
	errMaxPos      = errMsg("已达持仓数量上限")
	errLotTooSmall = errMsg("资金不足以买入一手")
	errCash        = errMsg("可用资金不足")
)

// errMsg 让普通字符串可充当 error，避免为每个错误单独建类型。
// （errMsg lets a plain string act as an error without a dedicated type per case.）
type errMsg string

func (e errMsg) Error() string { return string(e) }
