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
	FixedAmount    float64 `json:"fixed_amount"`    // 每票固定买入资金（元，默认 10000）
	MaxPositions   int     `json:"max_positions"`   // 最大并行持仓数（默认 10）
	InitialCapital float64 `json:"initial_capital"` // 初始资金（元，默认 100000）
}

// DefaultConfig 返回模拟盘出厂默认配置。
// English: returns the paper engine's factory-default config.
func DefaultConfig() Config {
	return Config{
		Enabled:        false,
		FixedAmount:    10000,
		MaxPositions:   10,
		InitialCapital: 100000,
	}
}

// Position 模拟持仓。
// SignalPrice 为信号发出时的信号价（辅助参照），CostPrice 为实际撮合价（实时价）。
// English: a paper position. SignalPrice is the reference signal price; CostPrice is the actual
// fill price (live price).
type Position struct {
	Code        string    `json:"code"`
	Name        string    `json:"name"`
	Strategy    string    `json:"strategy"`
	Qty         int       `json:"qty"`
	CostPrice   float64   `json:"cost_price"`
	Cost        float64   `json:"cost"`
	SignalPrice float64   `json:"signal_price"`
	SignalAt    time.Time `json:"signal_at"`
	FilledAt    time.Time `json:"filled_at"`
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
	Code        string    `json:"code"`
	Name        string    `json:"name"`
	Strategy    string    `json:"strategy"`
	Side        string    `json:"side"` // buy / sell
	Price       float64   `json:"price"`
	SignalPrice float64   `json:"signal_price,omitempty"`
	Qty         int       `json:"qty"`
	Amount      float64   `json:"amount"`
	Time        time.Time `json:"time"`
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

// Engine 模拟盘引擎：独立于真实持仓的虚拟撮合/估值/统计。
// path 为 JSON 持久化路径（空则不落盘，纯内存）。
// English: the paper engine — virtual fill/mark/statistics, isolated from the real book.
// path is the JSON persistence file (empty = in-memory only).
type Engine struct {
	cfg Config

	mu        sync.Mutex
	cash      float64
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
	if cfg.MaxPositions <= 0 {
		cfg.MaxPositions = 10
	}
	if cfg.InitialCapital <= 0 {
		cfg.InitialCapital = 100000
	}
	e := &Engine{
		cfg:       cfg,
		cash:      cfg.InitialCapital,
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

// persist 将当前状态写入 JSON（幂等，失败仅记录日志）。
// English: writes current state to the JSON file (best-effort; failures only log).
func (e *Engine) persist() {
	if e.path == "" {
		return
	}
	data, err := json.MarshalIndent(e, "", "  ")
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
	var st struct {
		Cash      float64              `json:"cash"`
		Positions map[string]*Position `json:"positions"`
		Trades    []Trade              `json:"trades"`
		Equity    []EquityPoint        `json:"equity"`
		Realized  float64              `json:"realized"`
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		log.Printf("[paper] 解析 %s 失败: %v", e.path, err)
		return
	}
	e.cash = st.Cash
	e.realized = st.Realized
	if st.Positions != nil {
		e.positions = st.Positions
	}
	e.trades = st.Trades
	e.equity = st.Equity
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
		if len(e.positions) >= e.cfg.MaxPositions {
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
		e.fillLocked(s.Code, s.Name, s.Strategy, s.Price, s.GeneratedAt, now, price, s.Reason)
	}
	e.persist()
}

// fillLocked 按给定价格撮合一笔买入（调用方须持锁）。返回错误信息。
// English: fills a buy at the given price (caller must hold the lock); returns an error on failure.
func (e *Engine) fillLocked(code, name, strategy string, signalPrice float64, signalAt, now time.Time, price float64, reason string) error {
	qty := int(e.cfg.FixedAmount/price/100) * 100
	if qty <= 0 {
		return errLotTooSmall // 一手都买不起（不足 A 股一手）
	}
	cost := float64(qty) * price
	if cost > e.cash {
		return errCash
	}
	e.cash -= cost
	e.positions[code] = &Position{
		Code:        code,
		Name:        name,
		Strategy:    strategy,
		Qty:         qty,
		CostPrice:   price,
		Cost:        cost,
		SignalPrice: signalPrice,
		SignalAt:    signalAt,
		FilledAt:    now,
		Mark:        price,
	}
	latency := int64(0)
	if !signalAt.IsZero() {
		latency = int64(now.Sub(signalAt).Seconds())
		if latency < 0 {
			latency = 0
		}
	}
	e.trades = append(e.trades, Trade{
		Code:        code,
		Name:        name,
		Strategy:    strategy,
		Side:        "buy",
		Price:       price,
		SignalPrice: signalPrice,
		Qty:         qty,
		Amount:      cost,
		Time:        now,
		LatencySec:  latency,
		Reason:      reason,
	})
	log.Printf("[paper] 模拟买入 %s(%s) %d股 @%.2f 信号价%.2f 延迟%ds",
		code, name, qty, price, signalPrice, latency)
	return nil
}

// Buy 手动按实时价买入一只股票（APK/前端信号页"模拟买入"按钮触发）。
// 与自动撮合共用 fillLocked：同一持仓去重/仓位上限/现金约束。
// English: manually buys one stock at the live price (triggered by the APK/frontend signal page's
// "paper buy" button). Shares fillLocked with auto-fill: dedupe / position cap / cash checks.
func (e *Engine) Buy(code, name, strategy string, signalPrice float64, quotes map[string]*data.StockInfo) error {
	if !e.cfg.Enabled {
		return errDisabled
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, held := e.positions[code]; held {
		return errHeld
	}
	if len(e.positions) >= e.cfg.MaxPositions {
		return errMaxPos
	}
	var price float64
	if q, ok := quotes[code]; ok && q != nil && q.Price > 0 {
		price = q.Price
	} else {
		return errNoQuote
	}
	if err := e.fillLocked(code, name, strategy, signalPrice, time.Now(), time.Now(), price, "手动模拟买入"); err != nil {
		return err
	}
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
// English: records the day's equity (one point per trading day, overwritten when the day repeats).
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
		e.equity[len(e.equity)-1] = EquityPoint{Date: date, Value: val, Cash: e.cash}
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
	proceeds := price * float64(p.Qty)
	e.cash += proceeds
	e.realized += proceeds - p.Cost
	e.trades = append(e.trades, Trade{
		Code:     p.Code,
		Name:     p.Name,
		Strategy: p.Strategy,
		Side:     "sell",
		Price:    price,
		Qty:      p.Qty,
		Amount:   proceeds,
		Time:     time.Now(),
	})
	log.Printf("[paper] 模拟卖出 %s(%s) %d股 @%.2f 盈亏%.2f", p.Code, p.Name, p.Qty, price, proceeds-p.Cost)
	delete(e.positions, code)
	e.persist()
	return nil
}

// Reset 清盘模拟盘：全部持仓按最后估值价平仓，清空成交/净值并重置现金到初始资金。
// English: liquidates everything at the last mark, wipes trades/equity and resets cash to initial.
func (e *Engine) Reset() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.cash = e.cfg.InitialCapital
	e.positions = make(map[string]*Position)
	e.trades = nil
	e.equity = nil
	e.realized = 0
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

var (
	errDisabled    = errMsg("模拟盘未启用")
	errNotHeld     = errMsg("未持有该股票")
	errNoQuote     = errMsg("无实时行情，无法成交")
	errHeld        = errMsg("已持有该股票")
	errMaxPos      = errMsg("已达持仓数量上限")
	errLotTooSmall = errMsg("资金不足以买入一手")
	errCash        = errMsg("可用资金不足")
)

type errMsg string

func (e errMsg) Error() string { return string(e) }
