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
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"sort"
	"sync"
	"sync/atomic"
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
	// AutoSell 卖出信号自动成交开关（阶段1.1 全自动执行）：开启时 清仓/减仓/硬止盈/硬止损
	// 告警直接在模拟盘自动平仓。由 config.PaperConfig.AutoSell 缺省填充（未配置=true）。
	// English: auto-sell switch (full-auto execution) — when on, 清仓/减仓/hard-TP/hard-SL alerts close
	// paper positions automatically. Defaulted from config.PaperConfig.AutoSell (true when unset).
	AutoSell bool `json:"auto_sell"`
}

// DefaultConfig 返回模拟盘出厂默认配置。
// English: returns the paper engine's factory-default config.
func DefaultConfig() Config {
	return Config{
		Enabled:        false,
		FixedAmount:    10000,
		MaxPositions:   0,
		InitialCapital: 100000,
		AutoSell:       true,
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
	// ATR 该股 ATR14（开仓信号携带，镜像写 report 账时供 C4/ATR 动态止损距离计算；
	// 手动买入为 0 → 镜像回退固定百分比止损）。
	// English: the stock's ATR14 carried by the opening signal — used by the report-book mirror for
	// C4 ATR dynamic stop distance; 0 for manual buys (mirror falls back to fixed-percentage stops).
	ATR float64 `json:"atr,omitempty"`
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

// Order 订单生命周期记录（阶段1.3）：一次"信号→订单→结果"的完整审计条目，
// 与 Trade（仅成交）互补——被拒绝/部分成交的尝试也留痕，便于复盘为何没买进/没卖出。
// English: an order-lifecycle record (one full signal→order→outcome audit entry), complementing
// Trade (fills only) — rejected/partial attempts are kept too, so missed fills are reviewable.
type Order struct {
	ID           string    `json:"id"`                      // 订单号（ord_<seq>）
	Code         string    `json:"code"`                    // 股票代码
	Name         string    `json:"name"`                    // 股票名称
	Strategy     string    `json:"strategy"`                // 战法名
	StrategyType string    `json:"strategy_type,omitempty"` // 战法池类型
	Side         string    `json:"side"`                    // buy / sell
	Kind         string    `json:"kind"`                    // 来源：自动撮合/手动买入/自动清仓/自动减仓/手动卖出/手动减仓
	SignalPrice  float64   `json:"signal_price,omitempty"`  // 信号价（参照）
	Price        float64   `json:"price"`                   // 成交价（rejected 时为 0）
	Qty          int       `json:"qty"`                     // 成交数量（rejected 0；partial 为部分量）
	Status       string    `json:"status"`                  // filled=全部成交 / partial=部分成交 / rejected=已拒绝
	Reason       string    `json:"reason,omitempty"`        // 拒绝原因或触发理由（告警文案）
	CreatedAt    time.Time `json:"created_at"`              // 下单时间
}

// orderSeq 订单号自增（进程内唯一即可，重启从时间戳续）。
// English: in-process order sequence; restarts reseed from the timestamp.
var orderSeq atomic.Int64

func newOrderID() string {
	return fmt.Sprintf("ord_%d_%d", time.Now().Unix(), orderSeq.Add(1))
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
// Cost/Realized/Floating/ReturnPct 为该池持久化的累计表现：买入按成交成本累计（卖出不减，
// 收益仍记该池），卖出按实现盈亏累计，浮动盈亏按当前持仓现价估算；
// 总涨跌幅 = (已实现 + 浮动) / 累计买入成本。Stats 为该池独立的绩效/信号质量汇总（前端
// 切换到该池 tab 时，页顶统计卡/信号质量卡据此展示，与全局统计并列）。
// English: one strategy cash pool's display snapshot (frontend allocation strip).
// Cost/Realized/Floating/ReturnPct are the pool's persisted cumulative performance: buys accumulate
// cost (never reduced on sells — P&L stays attributed to the pool), sells accumulate realized P&L,
// floating P&L is marked from the open positions; total return = (realized + floating) / cumulative cost.
// Stats is the pool's own performance/signal-quality summary (the top stat cards & quality cards render
// from it when this pool tab is selected, alongside the global stats).
type StrategyPoolState struct {
	Key       string  `json:"key"`       // 策略类型（""=其他/手动池）
	Label     string  `json:"label"`     // 展示名
	Cash      float64 `json:"cash"`      // 池内可用现金
	RatioPct  float64 `json:"ratio_pct"` // 占总现金比例（%）
	Positions int     `json:"positions"` // 池内持仓数
	// MaxPos 该池持仓上限（0=不单独设限，仅受全局上限约束；与全局解耦可自定义）。
	// English: this pool's position cap (0 = no per-pool limit, only the global cap applies; decoupled
	// from the global cap and customizable).
	MaxPos    int     `json:"max_pos"`
	Cost      float64 `json:"cost"`       // 累计买入成本（按买入后计数，卖出不减）
	Realized  float64 `json:"realized"`   // 已实现盈亏（卖出结算，仍记本池）
	Floating  float64 `json:"floating"`   // 浮动盈亏（当前持仓市值 - 成本）
	ReturnPct float64 `json:"return_pct"` // 总涨跌幅（%）= (已实现+浮动)/累计成本
	Stats     Stats   `json:"stats"`      // 该池独立绩效/信号质量汇总（前端分仓 tab 统计卡用）
}

// PoolPerf 一个战法资金池的持久化表现（成本基准 + 已实现盈亏）。
// English: a strategy pool's persisted performance (cost basis + realized P&L).
type PoolPerf struct {
	Cost     float64 `json:"cost"`     // 累计买入成本（按买入后计数，卖出不减）
	Realized float64 `json:"realized"` // 已实现盈亏（卖出结算，仍记本池）
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

	mu             sync.Mutex
	cash           float64
	pools          map[string]float64        // 战法资金池：key=策略类型（""=其他/手动池），Σpools == cash
	poolTypes      []string                  // 启用的战法类型（不含 ""），保持有序；空=未分仓（单池）
	poolPerf       map[string]*PoolPerf      // 战法资金池持久化表现：key=策略类型（买入累计成本/已实现盈亏）
	poolMaxPos     map[string]int            // 每池持仓上限：key=策略类型（0=该池不单独设限，仅受全局上限约束；可自定义）
	poolBuyRules   map[string]*PoolBuyRule   // 每池买入纪律规则（冷却/日限/门槛/预算配比）
	poolDiscipline map[string]poolDiscipline // 运行时状态：每池今日买入计数/花费/最近时间
	positions      map[string]*Position
	trades         []Trade
	orders         []Order // 订单生命周期（阶段1.3）：信号→订单→成交/拒绝 全留痕
	equity         []EquityPoint
	hasFilled      bool    // 是否发生过任何成交：false 期间 Snapshot 不记净值点（无买入不应有净值曲线）
	realized       float64 // 已实现盈亏累计
	path           string
	// 账本镜像回调（阶段1.2 两本账合一）：paper 为唯一真实账本，开仓/清仓经回调同步写
	// report 持仓账（由 engine/registry 注入），退出引擎/持仓页/打分池消费的 rpt 与模拟盘一致。
	// onOpen 在新开仓后触发（含手动买入；加仓不触发）；onClose 仅在整笔清仓时触发（部分减仓不触发）。
	// English: book-mirror callbacks (unified books) — paper is the single source of truth; opens/closes
	// are mirrored into the report holding book via callbacks injected by engine/registry, so the rpt
	// consumed by exit engines / positions page / scoring pool stays consistent with the paper book.
	// onOpen fires after a NEW position (manual buys included; add-ons excluded); onClose fires only on
	// full closes (partial trims excluded).
	onOpen  func(p Position)
	onClose func(code string, price, qty float64, reason string)
	// trimDone 当日减仓去重：code → "YYYYMMDD"（同一交易日同一持仓只响应一次减仓类信号，
	// 防止多轮告警把仓位反复减半）。清仓类无需去重（平仓后自然 no-op）。
	// English: same-day trim dedup — code → "YYYYMMDD"; trim-type signals act at most once per code per
	// trading day so repeated alerts can't keep halving the position. Full closes need no dedup (natural
	// no-op once flat).
	trimDone map[string]string
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
		cfg:            cfg,
		cash:           cfg.InitialCapital,
		pools:          map[string]float64{"": cfg.InitialCapital}, // 默认单池（未分仓）兼容
		poolPerf:       make(map[string]*PoolPerf),
		poolMaxPos:     make(map[string]int),
		poolBuyRules:   make(map[string]*PoolBuyRule),
		poolDiscipline: make(map[string]poolDiscipline),
		positions:      make(map[string]*Position),
		trimDone:       make(map[string]string),
		path:           path,
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

// SetMirror 注入账本镜像回调（阶段1.2 两本账合一，由 engine/registry 在创建账号模拟盘时调用）。
// open 在新开仓后触发（手动买入含、加仓不含）；close 仅整笔清仓触发（部分减仓不含）。
// 回调内部不得再调用本引擎方法（避免锁递归）；nil 安全。
// English: injects the book-mirror callbacks (unified books; called by engine/registry when creating an
// account's paper engine). open fires after a NEW open (manual buys included, add-ons excluded); close
// fires only on full closes. Callbacks must not call back into this engine (no recursive locking); nil-safe.
func (e *Engine) SetMirror(open func(p Position), close func(code string, price, qty float64, reason string)) {
	e.mu.Lock()
	e.onOpen, e.onClose = open, close
	e.mu.Unlock()
}

// mirrorOpenLocked 触发开仓镜像（须持锁调用；副本传值防回调侧读到后续变更）。
// English: fires the open mirror (caller must hold the lock; passes a copy so the callback never sees later mutations).
func (e *Engine) mirrorOpenLocked(p *Position) {
	if e.onOpen == nil || p == nil {
		return
	}
	cp := *p
	e.onOpen(cp)
}

// mirrorCloseLocked 触发清仓镜像（须持锁调用）。
// English: fires the close mirror (caller must hold the lock).
func (e *Engine) mirrorCloseLocked(code string, price, qty float64, reason string) {
	if e.onClose == nil {
		return
	}
	e.onClose(code, price, qty, reason)
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
	Orders       []Order              `json:"orders,omitempty"` // 订单生命周期（旧数据无此字段，load 兼容为空）
	Equity       []EquityPoint        `json:"equity"`
	Realized     float64              `json:"realized"`
	// Pools 战法资金池（key=策略类型，""=其他池；Pools 与 PoolTypes 同时落盘，跨重启保留各池现金）。
	// 旧数据（无 Pools）兼容：load 时按现金建单池 {"": cash}，行为与分仓前完全一致。
	// English: strategy cash pools (key = strategy type, "" = the other pool; both Pools and PoolTypes are
	// persisted so per-pool cash survives restarts). Legacy data without Pools is compatible: load falls
	// back to the single pool {"": cash}, identical to pre-allocation behavior.
	Pools     map[string]float64 `json:"pools,omitempty"`
	PoolTypes []string           `json:"pool_types,omitempty"`
	// PoolMaxPos 每池持仓上限（key=策略类型，0=该池不单独设限；跨重启保留）。
	// English: per-pool position caps (key = strategy type, 0 = no per-pool limit; survives restarts).
	PoolMaxPos map[string]int `json:"pool_max_pos,omitempty"`
	// PoolPerf 战法资金池持久化表现（累计买入成本/已实现盈亏；按买入后计数，卖出仍记本池）。
	// 旧数据（无 PoolPerf）兼容：load 时按池类型初始化空记录。
	// English: persisted per-pool performance (cumulative buy cost / realized P&L; counted after buy,
	// sells still attribute to the pool). Legacy data without PoolPerf initializes empty records on load.
	PoolPerf map[string]*PoolPerf `json:"pool_perf,omitempty"`
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
		Orders:         e.orders,
		Equity:         e.equity,
		Realized:       e.realized,
		Pools:          e.pools,
		PoolTypes:      e.poolTypes,
		PoolMaxPos:     e.poolMaxPos,
		PoolPerf:       e.poolPerf,
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
	// 恢复战法资金池持久化表现：旧数据无 PoolPerf 时按池类型初始化空记录。
	// English: restore persisted pool performance; legacy data without PoolPerf gets empty records.
	if st.PoolPerf != nil {
		e.poolPerf = st.PoolPerf
	}
	// 恢复每池持仓上限（旧数据无则留空，各池不单独设限）。
	// English: restore per-pool position caps (legacy data leaves them empty — no per-pool limits).
	if st.PoolMaxPos != nil {
		e.poolMaxPos = st.PoolMaxPos
	}
	// 恢复订单生命周期（旧数据无此字段 → 空列表，兼容）。
	// English: restore order lifecycle (legacy data without the field → empty list).
	if st.Orders != nil {
		e.orders = st.Orders
	}
	e.backfillPoolPerfLocked()
	e.trimTradesLocked()
}

// backfillPoolPerfLocked 兼容迁移：保证每个池的累计成本基准 ≥ 当前持仓成本合计。
// 旧数据（分仓性能统计上线前建的池）没有完整 PoolPerf.Cost：即使有少量新买入累计了成本，
// 历史持仓的成本从未记入池，导致分母偏小、浮盈/浮亏被异常放大（如 -150%）。
// 回填 = max(池已记成本, 当前持仓成本合计)，只补足缺失部分：
//   - 正常按新代码累计的池（Cost 已含全部买入，≥ 持仓成本）不受影响；
//   - 只有持仓成本未记入的旧池会补到持仓成本合计（作为部署资本的下限基准）。
//
// 调用方须持锁；有变更则落盘。
// English: compatibility migration — guarantees every pool's cumulative-cost basis ≥ the sum of its
// open positions' cost. Legacy pools (created before the pool-performance feature) lack a complete
// PoolPerf.Cost: even if a few post-upgrade buys accrued cost, the historical positions' cost was never
// recorded, shrinking the denominator and blowing up floating P&L into absurd percentages (e.g. -150%).
// Backfill = max(recorded cost, open-position cost sum), only topping up the shortfall:
//   - pools already accruing full cost under the new code (Cost ≥ position cost) are untouched;
//   - only legacy pools whose position cost was never recorded get topped up to their position-cost sum
//     (a floor basis for the actually deployed capital).
//
// Caller must hold the lock; persists on any change.
func (e *Engine) backfillPoolPerfLocked() {
	if len(e.positions) == 0 {
		return
	}
	if e.poolPerf == nil {
		e.poolPerf = make(map[string]*PoolPerf)
	}
	changed := false
	// 按池汇总当前持仓成本
	posCost := map[string]float64{}
	for _, p := range e.positions {
		posCost[p.StrategyType] += p.Cost
	}
	for k, cost := range posCost {
		perf := e.poolPerf[k]
		if perf == nil {
			e.poolPerf[k] = &PoolPerf{Cost: cost}
			changed = true
			continue
		}
		if perf.Cost < cost {
			perf.Cost = cost
			changed = true
		}
	}
	if changed {
		e.persist()
	}
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
	// 订单生命周期同窗口清理 + 绝对上限（防拒绝风暴撑爆文件）。
	// English: order lifecycle trimmed on the same window plus an absolute cap (reject storms can't bloat the file).
	if len(e.orders) > 0 {
		oout := e.orders[:0]
		for _, o := range e.orders {
			if o.CreatedAt.After(cutoff) {
				oout = append(oout, o)
			}
		}
		e.orders = oout
	}
	if capOrderLimit > 0 && len(e.orders) > capOrderLimit {
		e.orders = e.orders[len(e.orders)-capOrderLimit:]
	}
}

// capOrderLimit 订单记录绝对上限（超出丢最旧）。
// English: absolute order-record cap (oldest dropped beyond it).
const capOrderLimit = 2000

// recordOrderLocked 追加一条订单生命周期记录（须持锁调用；不主动 persist，由成交路径统一落盘）。
// English: appends one order-lifecycle record (caller holds the lock; no persist here — fill paths persist).
func (e *Engine) recordOrderLocked(o Order) {
	if o.ID == "" {
		o.ID = newOrderID()
	}
	if o.CreatedAt.IsZero() {
		o.CreatedAt = time.Now()
	}
	e.orders = append(e.orders, o)
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
		// 卖出信号自动成交（阶段1.1 全自动执行）：清仓/硬止盈/硬止损 → 全平；减仓类 → 半仓
		// （每码每日一次）。非本账持仓（如手动记录账）自然跳过。
		// English: auto-execute sell signals (full-auto) — 清仓/hard-TP/hard-SL close fully; trim-type
		// alerts halve the position (once per code per day). Codes not in this book are skipped naturally.
		if act := combat_agent.SellAction(s); act != "" {
			e.autoSellLocked(&s, act, quotes)
			continue
		}
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
		var price float64
		if q, ok := quotes[s.Code]; ok && q != nil && q.Price > 0 {
			price = q.Price
		} else if s.Price > 0 {
			// 行情缺失回退信号价（尽量避免该票长期滞留在参考价上；成交仍以实时优先）
			price = s.Price
		} else {
			continue
		}
		// 每池持仓上限（与全局上限解耦，可自定义）：该池已持仓数 ≥ 池上限时跳过该信号。
		// 池上限 0 = 该池不单独设限（仅受全局上限约束）；Σ池上限 ≤ 全局上限（前端配置时守恒校验）。
		// English: per-pool position cap (decoupled from the global cap, customizable) — skip the signal
		// when the pool already holds >= its cap. 0 = no per-pool limit (global cap only); Σpool caps ≤
		// the global cap (conserved by the frontend config).
		if poolMax := e.poolMaxPos[poolKey]; poolMax > 0 && e.poolPositionCountLocked(poolKey) >= poolMax {
			continue
		}
		if err := e.fillLocked(poolKey, s.Code, s.Name, s.Strategy, s.Price, s.GeneratedAt, now, price, 0, s.Reason); err != nil {
			log.Printf("[paper] 撮合失败 %s(%s): %v", s.Code, s.Name, err)
			// 订单留痕：买入被拒（现金不足/超上限/池上限/买不起一手）。
			// English: order audit — the buy was rejected (cash short / cap / pool cap / can't afford a lot).
			e.recordOrderLocked(Order{Code: s.Code, Name: s.Name, Strategy: s.Strategy, StrategyType: poolKey,
				Side: "buy", Kind: "自动撮合", SignalPrice: s.Price, Status: "rejected",
				Reason: fmt.Sprintf("%v", err), CreatedAt: now})
			continue
		}
		// 订单留痕：自动撮合全部成交。
		// English: order audit — the auto fill completed.
		e.recordOrderLocked(Order{Code: s.Code, Name: s.Name, Strategy: s.Strategy, StrategyType: poolKey,
			Side: "buy", Kind: "自动撮合", SignalPrice: s.Price, Price: price,
			Qty: e.positions[s.Code].Qty, Status: "filled", Reason: s.Reason, CreatedAt: now})
		// 记录信号携带的 ATR14（镜像写 report 账时供 ATR 动态止损距离计算），随后触发开仓镜像。
		// English: record the signal's ATR14 (used by the report-book mirror for the ATR dynamic stop
		// distance), then fire the open mirror.
		if p, ok := e.positions[s.Code]; ok {
			p.ATR = s.ATR
			e.mirrorOpenLocked(p)
		}
	}
	e.persist()
}

// autoSellLocked 卖出信号自动成交（阶段1.1，须持锁调用）：
//   - act=="close"：全仓卖出（清仓/硬止盈/硬止损），收益回池并镜像关闭 report 账记录；
//   - act=="trim" ：半仓减仓（减仓/情绪退潮/利空归因），每码每交易日至多一次（trimDone 去重）。
//
// 行情缺失时跳过（避免以信号价误成交）；非本账持仓自然 no-op；AutoSell=false 时整体停用。
// English: auto-executes a sell signal (caller must hold the lock): "close" sells the whole position
// (proceeds back to pool + report-book mirror close); "trim" halves it at most once per code per trading
// day (trimDone dedup). Skips on missing quotes; no-op for codes not in this book; disabled entirely
// when AutoSell is off.
func (e *Engine) autoSellLocked(s *combat_agent.Signal, act string, quotes map[string]*data.StockInfo) {
	if !e.cfg.AutoSell {
		return
	}
	p, held := e.positions[s.Code]
	if !held || p == nil {
		return
	}
	var price float64
	if q, ok := quotes[s.Code]; ok && q != nil && q.Price > 0 {
		price = q.Price
	} else {
		return // 无实时价不自动成交（宁可不卖也不以错误价格记账）
	}
	reason := "自动" + s.AlertType
	if reason == "自动" {
		reason = "自动卖出"
	}
	mkOrder := func(status string, qty int) Order {
		return Order{Code: s.Code, Name: s.Name, Strategy: p.Strategy, StrategyType: p.StrategyType,
			Side: "sell", Kind: reason, SignalPrice: s.Price, Qty: qty, Status: status,
			Reason: s.Reason, CreatedAt: time.Now()}
	}
	switch act {
	case "close":
		qty := p.Qty
		e.sellAllLocked(p, price, reason)
		e.recordOrderLocked(func() Order { o := mkOrder("filled", qty); o.Price = price; return o }())
	case "trim":
		today := time.Now().Format("20060102")
		if e.trimDone[s.Code] == today {
			return
		}
		half := p.Qty / 2 / 100 * 100 // 半仓取整手（A 股一手 100 股）
		if half <= 0 {
			return // 持仓不足两手无法半仓减仓，等清仓类信号处理
		}
		e.sellQtyLocked(p, price, half, reason)
		e.recordOrderLocked(func() Order { o := mkOrder("partial", half); o.Price = price; return o }())
		e.trimDone[s.Code] = today
	}
}

// poolPositionCountLocked 返回某池当前持仓数（须持锁调用）。
// English: returns the current position count of a pool (caller must hold the lock).
func (e *Engine) poolPositionCountLocked(poolKey string) int {
	n := 0
	for _, p := range e.positions {
		if p.StrategyType == poolKey {
			n++
		}
	}
	return n
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
	// 每池持仓上限（与全局上限解耦，可自定义）：该池新开仓达到池上限即拒绝。
	// 池上限 0 = 该池不单独设限；加仓（已持仓）不在此路径（走 addToPositionLocked）。
	// English: per-pool position cap (decoupled from the global cap, customizable) — a new open in this
	// pool is rejected once the pool reaches its cap. 0 = no per-pool limit; add-ons (already held) don't
	// pass through here (they use addToPositionLocked).
	if poolMax := e.poolMaxPos[poolKey]; poolMax > 0 && e.poolPositionCountLocked(poolKey) >= poolMax {
		return errMaxPos
	}
	// §分仓纪律引擎·预检查（不需要cost）：日限次数+冷却+评分门槛。
	if reason := e.checkPoolDisciplinePre(poolKey); reason != "" {
		log.Printf("[paper] 分仓纪律拒绝 %s→%s: %s", strategy, code, reason)
		return errPoolDiscipline
	}
	explicitQty := qty > 0
	if !explicitQty {
		qty = int(e.cfg.FixedAmount/price/100) * 100
	}
	if qty <= 0 {
		return errLotTooSmall // 一手都买不起（不足 A 股一手）
	}
	cost := float64(qty) * price
	// §分仓纪律引擎·预算检查（需要cost）：日内花费不超池资金配比。
	if reason := e.checkPoolBudget(poolKey, cost); reason != "" {
		log.Printf("[paper] 分仓预算拒绝 %s→%s: %s", strategy, code, reason)
		return errPoolDiscipline
	}
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
	e.hasFilled = true             // §反馈修复：首笔成交起才开始记净值曲线
	e.recordPoolBuy(poolKey, cost) // §分仓纪律：记录买入事件（冷却/日限/预算计数）
	// 买入后计数：累计买入成本计入本池（卖出不减，收益仍记该池）。
	// English: counted after buy — the buy cost accumulates to the pool (never reduced on sells, so
	// P&L stays attributed to the pool).
	if e.poolPerf[poolKey] == nil {
		e.poolPerf[poolKey] = &PoolPerf{}
	}
	e.poolPerf[poolKey].Cost += cost
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
		e.recordOrderLocked(Order{Code: code, Name: name, Strategy: strategy, Side: "buy",
			Kind: "手动买入", SignalPrice: signalPrice, Status: "rejected", Reason: fmt.Sprintf("%v", err)})
		return err
	}
	e.recordOrderLocked(Order{Code: code, Name: name, Strategy: strategy, Side: "buy",
		Kind: "手动买入", SignalPrice: signalPrice, Price: price, Qty: e.positions[code].Qty,
		Status: "filled", Reason: "手动模拟买入"})
	e.mirrorOpenLocked(e.positions[code]) // 手动买入同样镜像开仓（两本账合一）
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
		e.recordOrderLocked(Order{Code: code, Name: name, Strategy: strategy, Side: "buy",
			Kind: "手动买入", SignalPrice: signalPrice, Status: "rejected", Reason: fmt.Sprintf("%v", err)})
		return err
	}
	e.recordOrderLocked(Order{Code: code, Name: name, Strategy: strategy, Side: "buy",
		Kind: "手动买入", SignalPrice: signalPrice, Price: price, Qty: e.positions[code].Qty,
		Status: "filled", Reason: "手动模拟买入"})
	e.mirrorOpenLocked(e.positions[code]) // 手动买入同样镜像开仓（两本账合一）
	e.persist()
	return nil
}

// addToPositionLocked 已持仓加仓：加权平均成本、追加买入记录、从该持仓所属战法池扣款（须持锁调用）。
// 手动加仓归原持仓池（按持仓记录的 StrategyType；旧数据/空 = 其他池），与买入回池语义一致：
// 加仓成本记入该池累计成本，卖出时收益仍记该池。
// English: adds to an existing position — quantity up, cost averaged, an extra buy fill appended, cash
// debited from the position's own strategy pool (caller must hold the lock). A manual add-on goes to the
// pool the position belongs to (per its recorded StrategyType; empty/legacy = the other pool), matching
// the buy-into-pool semantics: the add-on cost accrues to that pool's cumulative cost, and sale proceeds
// still attribute to it.
func (e *Engine) addToPositionLocked(p *Position, code, name, strategy string, signalPrice, price float64, qty int, reason string) error {
	if qty <= 0 {
		return errLotTooSmall
	}
	cost := float64(qty) * price
	poolKey := p.StrategyType // 加仓归原持仓池
	pool := e.pools[poolKey]
	if cost > pool {
		return errCash
	}
	e.pools[poolKey] = pool - cost
	e.cash -= cost
	e.hasFilled = true             // §反馈修复：首笔成交起才开始记净值曲线
	e.recordPoolBuy(poolKey, cost) // §分仓纪律：记录买入事件（冷却/日限/预算计数）
	// 加仓成本照记入该池，保证池累计表现不遗漏。
	// English: the add-on cost accrues to the pool so its cumulative performance stays complete.
	if e.poolPerf[poolKey] == nil {
		e.poolPerf[poolKey] = &PoolPerf{}
	}
	e.poolPerf[poolKey].Cost += cost
	p.Cost += cost
	p.Qty += qty
	p.CostPrice = p.Cost / float64(p.Qty)
	p.Mark = price
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
		Time:         time.Now(),
		Reason:       reason,
	})
	log.Printf("[paper] 模拟加仓 %s(%s) +%d股 @%.2f 现持%d股 均价%.3f 池=%s", code, name, qty, price, p.Qty, p.CostPrice, strategyPoolLabel(poolKey))
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
	// §反馈修复：从未成交过 → 不记录净值点（否则每天一个平线点，"没有买入也有净值曲线"）。
	// 首笔成交后自动开始正常记录；Reset 清盘后回到不记录状态直到再成交。
	if !e.hasFilled {
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
	qty := p.Qty
	err := e.sellAllLocked(p, price, "手动模拟卖出")
	if err == nil {
		e.recordOrderLocked(Order{Code: code, Name: p.Name, Strategy: p.Strategy, StrategyType: p.StrategyType,
			Side: "sell", Kind: "手动卖出", Price: price, Qty: qty, Status: "filled", Reason: "手动模拟卖出"})
		e.persist()
	}
	return err
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
		full := p.Qty
		err := e.sellAllLocked(p, price, "手动模拟卖出")
		if err == nil {
			e.recordOrderLocked(Order{Code: code, Name: p.Name, Strategy: p.Strategy, StrategyType: p.StrategyType,
				Side: "sell", Kind: "手动卖出", Price: price, Qty: full, Status: "filled", Reason: "手动模拟卖出"})
			e.persist()
		}
		return err
	}
	err := e.sellQtyLocked(p, price, qty, "手动模拟减仓")
	if err == nil {
		e.recordOrderLocked(Order{Code: code, Name: p.Name, Strategy: p.Strategy, StrategyType: p.StrategyType,
			Side: "sell", Kind: "手动减仓", Price: price, Qty: qty, Status: "partial", Reason: "手动模拟减仓"})
		e.persist()
	}
	return err
}

// sellQtyLocked 按股数部分减仓：回池、结算已实现盈亏、追加卖出记录（须持锁调用）。
// 手动减仓与自动减仓（阶段1.1 卖出信号半仓）共用；不触发清仓镜像（report 账记录保留至整笔平仓）。
// English: trims by share count — pool return, realized P&L, extra sell fill (caller must hold the lock).
// Shared by manual trims and auto trims (sell-signal half exits); does NOT fire the close mirror (the
// report-book record stays until the full close).
func (e *Engine) sellQtyLocked(p *Position, price float64, qty int, reason string) error {
	proceeds := price * float64(qty)
	// 减仓收益回原战法池（按持仓记录的类型；空=其他池）。
	// English: trim proceeds return to the position's own strategy pool (per its recorded type; empty =
	// the other pool).
	e.pools[p.StrategyType] += proceeds
	e.cash += proceeds
	e.realized += (price - p.CostPrice) * float64(qty)
	// 卖出结算：已实现盈亏计入本池（卖出仍记分仓资金池，跨重启保留）。
	// English: sale settlement — realized P&L credits the position's own pool (sells still attribute
	// to the pool, persisted across restarts).
	if e.poolPerf[p.StrategyType] == nil {
		e.poolPerf[p.StrategyType] = &PoolPerf{}
	}
	e.poolPerf[p.StrategyType].Realized += (price - p.CostPrice) * float64(qty)
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
		Reason:       reason,
	})
	log.Printf("[paper] 模拟减仓 %s(%s) -%d股 @%.2f 剩余%d股 原因=%s", p.Code, p.Name, qty, price, p.Qty, reason)
	e.persist()
	return nil
}

// sellAllLocked 清仓单一持仓：回池、结算已实现盈亏、追加卖出记录，并触发清仓镜像
// （阶段1.2 两本账合一：report 账对应记录同步平仓；须持锁调用）。
// English: closes a single position — pool return, realized P&L, extra sell fill, plus the close mirror
// (unified books: the report-book record is closed in sync; caller must hold the lock).
func (e *Engine) sellAllLocked(p *Position, price float64, reason string) error {
	proceeds := price * float64(p.Qty)
	// 卖出收益回原战法池（按持仓记录的类型；空=其他池）。
	// English: sale proceeds return to the position's own strategy pool (per its recorded type; empty =
	// the other pool).
	e.pools[p.StrategyType] += proceeds
	e.cash += proceeds
	e.realized += proceeds - p.Cost
	// 清仓结算：已实现盈亏计入本池（卖出仍记分仓资金池，跨重启保留）。
	// English: close settlement — realized P&L credits the position's own pool (sells still attribute
	// to the pool, persisted across restarts).
	if e.poolPerf[p.StrategyType] == nil {
		e.poolPerf[p.StrategyType] = &PoolPerf{}
	}
	e.poolPerf[p.StrategyType].Realized += proceeds - p.Cost
	qty := p.Qty
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
		Reason:       reason,
	})
	log.Printf("[paper] 模拟卖出 %s(%s) %d股 @%.2f 盈亏%.2f 原因=%s", p.Code, p.Name, p.Qty, price, proceeds-p.Cost, reason)
	delete(e.positions, p.Code)
	// 清仓镜像（阶段1.2）：report 账对应记录同步平仓（pap_<code> 稳定键）。
	// English: close mirror (unified books) — the report-book record closes in sync (stable key pap_<code>).
	e.mirrorCloseLocked(p.Code, price, float64(qty), reason)
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
	} else {
		keys := append([]string(nil), e.poolTypes...)
		keys = append(keys, "") // 其他/手动池
		share := e.cash / float64(len(keys))
		e.pools = make(map[string]float64, len(keys))
		for _, k := range keys {
			e.pools[k] = share
		}
	}
	// 池集合变化：为每个池确保持久化表现记录存在（保留已有 Cost/Realized）。
	// English: on a pool-set change, ensure each pool has a persisted-performance record (keeps any
	// existing Cost/Realized).
	if e.poolPerf == nil {
		e.poolPerf = make(map[string]*PoolPerf)
	}
	for k := range e.pools {
		if e.poolPerf[k] == nil {
			e.poolPerf[k] = &PoolPerf{}
		}
	}
	// 池集合变化：为每个池初始化持仓上限记录（0=该池不单独设限）。
	// English: ensure each pool has a position-cap entry (0 = no per-pool limit).
	if e.poolMaxPos == nil {
		e.poolMaxPos = make(map[string]int)
	}
	for k := range e.pools {
		if _, ok := e.poolMaxPos[k]; !ok {
			e.poolMaxPos[k] = 0
		}
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

// StrategyPools 返回各战法资金池的展示快照（前端分仓条：池余量/占比/持仓数/累计表现）。
// English: returns each strategy pool's display snapshot (frontend allocation strip:
// balance/ratio/positions/cumulative performance).
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
		floating := 0.0
		for _, p := range e.positions {
			if p.StrategyType == k {
				cnt++
				floating += p.PnL()
			}
		}
		perf := e.poolPerf[k]
		var cost, realized float64
		if perf != nil {
			cost = perf.Cost
			realized = perf.Realized
		}
		retPct := 0.0
		if cost > 0 {
			retPct = (realized + floating) / cost * 100
		}
		out = append(out, StrategyPoolState{
			Key:       k,
			Label:     strategyPoolLabel(k),
			Cash:      cash,
			RatioPct:  ratio,
			Positions: cnt,
			MaxPos:    e.poolMaxPos[k],
			Cost:      round2(cost),
			Realized:  round2(realized),
			Floating:  round2(floating),
			ReturnPct: round2(retPct),
			Stats:     e.statsFor(&k),
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
	e.poolPerf = make(map[string]*PoolPerf)
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
	// 全局重置镜像（阶段1.2）：report 账中 pap_ 开头的模拟盘记录一并平仓（按估值价），
	// 手动录入的非镜像记录不动。
	// English: global-reset mirror (unified books) — pap_-prefixed report records close at mark price;
	// manually entered non-mirrored records are untouched.
	if e.onClose != nil {
		for _, p := range e.positions {
			e.onClose(p.Code, p.Mark, float64(p.Qty), "模拟盘重置")
		}
	}
	e.cash = e.cfg.InitialCapital
	e.positions = make(map[string]*Position)
	e.trades = nil
	e.equity = nil
	e.hasFilled = false // 清盘后回到"未成交不记净值"状态，直到再产生成交
	e.realized = 0
	e.poolPerf = make(map[string]*PoolPerf)
	e.trimDone = make(map[string]string)
	e.rebuildPoolsLocked() // 现金恢复后按当前池集合重新均分
	e.persist()
}

// ResetPool 单池清盘：只清该战法资金池的持仓与持久化表现（按最后估值价平仓回补池现金，
// 累计成本/已实现盈亏归零），其余池与全局净值/成交日志不受影响。
// 对应前端"清盘本池"按钮（选中某分仓 tab 时出现）。
// English: ResetPool liquidates a single strategy pool only — closes its positions at the last mark
// price (proceeds return to the pool), zeroing that pool's cumulative cost/realized, while other pools
// and the global equity/fill log are untouched. Backs the frontend "清盘本池" button (shown when a
// pool tab is selected).
func (e *Engine) ResetPool(key string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	proceeds := 0.0
	closed := 0
	for code, p := range e.positions {
		if p.StrategyType == key {
			proceeds += p.MarketValue()
			delete(e.positions, code)
			closed++
			// 清盘镜像（阶段1.2）：report 账对应记录同步平仓（按估值价结算）。
			// English: liquidation mirror (unified books) — the report-book record closes in sync
			// (settled at the mark price).
			e.mirrorCloseLocked(code, p.Mark, float64(p.Qty), "分仓清盘")
		}
	}
	if proceeds > 0 {
		e.pools[key] += proceeds
		e.cash += proceeds
	}
	if e.poolPerf[key] != nil {
		e.poolPerf[key].Cost = 0
		e.poolPerf[key].Realized = 0
	}
	// 清掉该池成交记录（该池已清盘，避免统计仍从全局日志累出信号数）
	// English: drop the pool's fills (the pool is liquidated, so stats must not count its fills again).
	if len(e.trades) > 0 {
		out := e.trades[:0]
		for _, t := range e.trades {
			if t.StrategyType != key {
				out = append(out, t)
			}
		}
		e.trades = out
	}
	log.Printf("[paper] 单池清盘 %s：平仓 %d 笔回池 %.2f", strategyPoolLabel(key), closed, proceeds)
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

// SetPoolCaps 设置每池持仓上限（key=策略类型；n<=0 = 该池不单独设限）。
// 与全局持仓上限解耦：池上限是全局上限之内的子约束，Σ池上限 ≤ 全局上限由调用方（前端）守恒校验。
// 池集合变化的池自动初始化；仅覆盖传入的池，其余保留。持久化。
// English: sets per-pool position caps (key = strategy type; n<=0 = no per-pool limit). Decoupled from
// the global cap — pool caps are sub-constraints within it; Σpool caps ≤ the global cap is conserved by
// the caller (frontend). Only the given pools are set; others keep their values. Persisted.
func (e *Engine) SetPoolCaps(caps map[string]int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.poolMaxPos == nil {
		e.poolMaxPos = make(map[string]int)
	}
	for k, n := range caps {
		if n < 0 {
			n = 0
		}
		e.poolMaxPos[k] = n
	}
	e.persist()
}

// SetPoolAllocs 设置每池资金分配（key=策略类型 → 目标现金额），并按总和守恒重排：Σ池现金=总现金，
// 各池目标额缩放为与总现金一致（总现金含持仓占用，故按当前现金占比分配可用现金）。
// 仅覆盖传入的池；未传的池按剩余现金均分。持久化。
// English: sets per-pool cash allocations (key → target cash) with conservation: Σpool cash = total
// cash, targets scaled to the total. Only the given pools are set; unmentioned pools split the rest
// evenly. Persisted.
func (e *Engine) SetPoolAllocs(allocs map[string]float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(allocs) == 0 || len(e.pools) == 0 {
		return
	}
	total := 0.0
	for _, v := range e.pools {
		total += v
	}
	// 先按传入目标分配，剩余现金再均分给未指定的池
	assigned := 0.0
	rest := make([]string, 0, len(e.pools))
	for k := range e.pools {
		if v, ok := allocs[k]; ok && v > 0 {
			e.pools[k] = v
			assigned += v
		} else {
			rest = append(rest, k)
		}
	}
	if assigned >= total {
		// 目标合计超总现金：等比压缩（守恒）
		for k := range e.pools {
			e.pools[k] = e.pools[k] / assigned * total
		}
	} else if len(rest) > 0 {
		remain := total - assigned
		share := remain / float64(len(rest))
		for _, k := range rest {
			e.pools[k] = share
		}
	}
	e.cash = total
	log.Printf("[paper] 资金分配已自定义：%v 总现金 %.2f", e.pools, total)
	e.persist()
}

// ResetPoolAllocs 恢复资金分配为均分（清空自定义分配）：按当前池集合把总现金均分到各池。
// 保留每池持仓上限（poolMaxPos 不动）。持久化。
// English: restores the cash allocation to an even split (clears custom allocations): the total cash is
// split evenly across the current pool set. Per-pool position caps (poolMaxPos) are kept. Persisted.
func (e *Engine) ResetPoolAllocs() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.pools) == 0 {
		return
	}
	total := 0.0
	for _, v := range e.pools {
		total += v
	}
	share := total / float64(len(e.pools))
	for k := range e.pools {
		e.pools[k] = share
	}
	e.cash = total
	log.Printf("[paper] 资金分配已恢复均分：每池 %.2f 总现金 %.2f", share, total)
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

// Orders 返回订单生命周期记录（最新在前；阶段1.3 信号→订单→成交/拒绝 全留痕）。
// English: returns order-lifecycle records (newest first; full signal→order→outcome audit).
func (e *Engine) Orders() []Order {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]Order, len(e.orders))
	copy(out, e.orders)
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
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

// Stats 汇总绩效与信号质量指标（全账号）。
// English: aggregates performance and signal-quality metrics (whole account).
func (e *Engine) Stats() Stats {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.statsFor(nil)
}

// PoolStats 返回某战法资金池的独立绩效/信号质量汇总（前端分仓 tab 统计卡用）。
// 收益基准 = 该池累计买入成本（按买入后计数），已实现盈亏 = 该池持久化 realized。
// English: returns a strategy pool's own performance/signal-quality summary (used by the frontend
// stat cards when that pool tab is selected). The return basis is the pool's cumulative buy cost
// (counted after buy); realized P&L is the pool's persisted realized value.
func (e *Engine) PoolStats(key string) Stats {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.statsFor(&key)
}

// statsFor 汇总绩效与信号质量。poolKey 为 nil = 全账号；否则仅统计该战法池
// （现金=池余量、持仓=池内持仓、成交=池内成交、收益基准=池累计买入成本）。
// 调用方须持锁。English: aggregates performance/signal quality. nil poolKey = whole account;
// otherwise scoped to that strategy pool (cash=pool balance, positions=fills scoped to the pool,
// return basis=pool cumulative buy cost). Caller must hold the lock.
func (e *Engine) statsFor(poolKey *string) Stats {
	global := poolKey == nil
	matches := func(t string) bool { return global || t == *poolKey }
	cash := e.cash
	if !global {
		cash = e.pools[*poolKey]
	}
	mv := 0.0
	openPos := 0
	for _, p := range e.positions {
		if matches(p.StrategyType) {
			mv += p.MarketValue()
			openPos++
		}
	}
	total := cash + mv
	st := Stats{
		Cash:          cash,
		MarketValue:   mv,
		TotalValue:    total,
		OpenPositions: openPos,
	}
	if global {
		st.InitialCapital = e.cfg.InitialCapital
		st.RealizedPnl = e.realized
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
	} else {
		perf := e.poolPerf[*poolKey]
		if perf != nil {
			st.InitialCapital = perf.Cost
			st.RealizedPnl = perf.Realized
		}
		if st.InitialCapital > 0 {
			floating := 0.0
			for _, p := range e.positions {
				if p.StrategyType == *poolKey {
					floating += p.PnL()
				}
			}
			st.TotalReturnPct = (st.RealizedPnl + floating) / st.InitialCapital * 100
		}
	}

	// 胜率：按卖出记录相对对应持仓成本估算（简化：盈利卖出笔数 / 卖出笔数）
	wins, sells := 0, 0
	// 滑点统计：遍历买入成交，取每个买入的滑点与延迟
	var sumSlip, sumLat float64
	var maxLat int64
	for _, t := range e.trades {
		if !matches(t.StrategyType) {
			continue
		}
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
				if bt.Side == "buy" && matches(bt.StrategyType) && bt.Code == t.Code && bt.Time.Before(t.Time) {
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

// SetInitialCapital 显式设定初始资金额（§反馈修复：配合 Reset 使用，
// 解决多次 Deposit 累加导致 cfg.InitialCapital 被抬高后清盘基数不对的问题）。
func (e *Engine) SetInitialCapital(v float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if v > 0 {
		e.cfg.InitialCapital = v
	}
}

// ── 分仓纪律引擎（§用户反馈：因子战法触发太快打满预算；分仓买入没有严格规则）──

// PoolBuyRule 单个资金池的买入纪律规则。
// 所有字段为零值时该池不做额外限制（仅受全局持仓上限和池资金约束）。
type PoolBuyRule struct {
	MaxDailyBuys    int     `json:"max_daily_buys"`     // 每日最大买入次数（0=不限）
	CooldownMinutes int     `json:"cooldown_minutes"`   // 两次买入最小间隔分钟（0=不限）
	MinScore        float64 `json:"min_score"`          // 入场最低评分（0=不过滤；信号 Confidence≥此值才买）
	BudgetPctPerDay float64 `json:"budget_pct_per_day"` // 每日最多动用池资金的%（0=不限，如30=一天最多花池的30%）
}

// poolDiscipline 每池买入纪律运行时状态（不持久化，重启重置——保守安全）。
type poolDiscipline struct {
	buysToday  int       // 今日已买次数
	lastBuyAt  time.Time // 最近一次买入时间
	spentToday float64   // 今日已花费金额（元）
	day        string    // 当前日期（跨日自动重置）
}

// checkPoolDisciplinePre 分仓纪律预检查（不需要 cost）：日限次数+冷却+评分门槛。
func (e *Engine) checkPoolDisciplinePre(poolKey string) string {
	now := time.Now()
	today := now.Format("2006-01-02")

	d, ok := e.poolDiscipline[poolKey]
	if !ok || d.day != today {
		e.poolDiscipline[poolKey] = poolDiscipline{day: today} // 跨日重置计数
		d = e.poolDiscipline[poolKey]
	}

	rule := e.poolBuyRules[poolKey]
	if rule == nil {
		return ""
	}

	if rule.MaxDailyBuys > 0 && d.buysToday >= rule.MaxDailyBuys {
		return fmt.Sprintf("日买次数达上限(%d/%d)", d.buysToday, rule.MaxDailyBuys)
	}
	if rule.CooldownMinutes > 0 && !d.lastBuyAt.IsZero() {
		elapsed := now.Sub(d.lastBuyAt).Minutes()
		if elapsed < float64(float64(rule.CooldownMinutes)) {
			return fmt.Sprintf("冷却中(%.0f/%.0f分钟)", elapsed, float64(rule.CooldownMinutes))
		}
	}
	return ""
}

// checkPoolBudget 预算占比检查（需要 cost）。
func (e *Engine) checkPoolBudget(poolKey string, cost float64) string {
	rule := e.poolBuyRules[poolKey]
	if rule == nil || rule.BudgetPctPerDay <= 0 || rule.BudgetPctPerDay >= 100 {
		return ""
	}
	d := e.poolDiscipline[poolKey]
	poolCash := e.pools[poolKey]
	budget := poolCash * rule.BudgetPctPerDay / 100
	if d.spentToday+cost > budget {
		return fmt.Sprintf("日内预算超限(%.0f+%.0f>%.0f)", d.spentToday, cost, budget)
	}
	return ""
}

// recordPoolBuy 纪律检查通过后记录买入事件（更新计数和时间戳）。
func (e *Engine) recordPoolBuy(poolKey string, cost float64) {
	now := time.Now()
	today := now.Format("2006-01-02")
	d, ok := e.poolDiscipline[poolKey]
	if !ok || d.day != today {
		e.poolDiscipline[poolKey] = poolDiscipline{day: today}
		d = e.poolDiscipline[poolKey]
	}
	d.buysToday++
	d.spentToday += cost
	d.lastBuyAt = now
	e.poolDiscipline[poolKey] = d
}

// SetPoolBuyRule 设置单池买入纪律规则（nil=清除该池规则）。
func (e *Engine) SetPoolBuyRule(poolKey string, rule *PoolBuyRule) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if rule == nil {
		delete(e.poolBuyRules, poolKey)
	} else {
		e.poolBuyRules[poolKey] = rule
	}
}

// errPoolDiscipline 分仓纪律拒绝（冷却/日限/门槛/预算）。
var errPoolDiscipline = errors.New("分仓纪律拒绝")
