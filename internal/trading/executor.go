// Package trading — 实盘交易执行层（AUTO_TRADING_PLAN M1 首尔侧）。
// 负责把首尔侧决策（信号/持仓建议）转发给国内 Windows 网关（东莞证券 MiniQMT）执行真实下单，
// 并管理网关连接状态、熔断、幂等与本地订单状态。与纸面账本（paper/report.Report）完全独立。
// English: live-trading execution layer (AUTO_TRADING_PLAN M1, Seoul side). Forwards Seoul-side decisions
// (signals / position advice) to the domestic Windows gateway (Guoxin MiniQMT) for real orders, and
// manages gateway connectivity, circuit breaking, idempotency and local order state. Fully independent
// of the paper book (paper/report.Report).
package trading

import (
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/store"
)

// OrderSide 下单方向。
// （OrderSide is the order direction.）
const (
	SideBuy  = "买入"
	SideSell = "卖出"
)

// OrderRequest 下单请求（首尔 → 网关 /order）。
// English: order request (Seoul → gateway /order).
type OrderRequest struct {
	SignalID   string `json:"signal_id"`             // 信号唯一标识（幂等键，网关去重）
	Code       string `json:"code"`                  // 股票代码（带后缀，如 600000.SH）
	Name       string `json:"name"`                  // 股票名称
	Strategy   string `json:"strategy"`              // 触发战法名称（显示名）
	StrategyID string `json:"strategy_id,omitempty"` // 战法库规则 ID（fac_1/pat_2/n_shape…，白名单同键）
	// StrategyType 规范战法 ID（dragon/double_bump/n_shape/dragon_return/momentum/fac_*/pat_*），
	// 与实盘白名单/前端战法开关同键空间（§20260917 严格开关：白名单校验权威键）。
	// English: canonical strategy id, the same key space as the live whitelist / UI toggles
	// (§20260917 strict gating — the authoritative whitelist match key).
	StrategyType string  `json:"strategy_type,omitempty"`
	Side         string  `json:"side"`       // 买入/卖出
	PriceType    string  `json:"price_type"` // market=对手价 / limit=限价
	Price        float64 `json:"price"`      // 参考价（limit 时按此限价）
	Qty          int     `json:"qty"`        // 股数（整手）
	Amount       float64 `json:"amount"`     // 金额（元）
	CreatedAt    string  `json:"created_at"` // 创建时间（RFC3339）
	// StalenessMs §WS-C 行情快照陈旧度（毫秒；-1=未提供，StaleQuoteGuard 跳过）。由调用方（引擎）
	// 从 fetcher.StalenessMs 装配；手动下单/未提供时闸 fail-open。
	// English: §WS-C quote-staleness (ms; -1 = unavailable → StaleQuoteGuard skipped). Filled by the
	// engine from fetcher.StalenessMs; manual orders leave it unset and the gate fails open.
	StalenessMs int64 `json:"staleness_ms,omitempty"`
	// PrevClose 昨收（§WS-C 涨跌停不可追单闸用；0=未提供 → fail-open）。CurrentPrice 现价（集中度闸用）。
	// English: §WS-C prev close (limit up/down gate; 0 = unavailable → fail open) and live price
	// (concentration gate).
	PrevClose    float64 `json:"prev_close,omitempty"`
	CurrentPrice float64 `json:"current_price,omitempty"`
	// §C2 下单时点的市场 VWAP 与来源/时间；缺失时 0/空（不伪造）。
	// SignalVWAPPremiumPct = (参考价 − VWAP)/VWAP×100。
	// English: §C2 market VWAP at order time plus source/as-of; missing means zero/empty, never faked.
	MarketVWAP           float64 `json:"market_vwap,omitempty"`
	VWAPSource           string  `json:"vwap_source,omitempty"`
	VWAPAsOf             string  `json:"vwap_as_of,omitempty"`
	SignalVWAPPremiumPct float64 `json:"signal_vwap_premium_pct,omitempty"`
	// §C2 软回踩单的按单超时秒数（0=沿用全局 cancel_stale_sec）。
	// English: §C2 per-order timeout for pullback entries (0 = global cancel_stale_sec).
	TimeoutSec int `json:"timeout_sec,omitempty"`
	// DynamicBudget §DYN-SIZE-v3：本单由动态预算生成。风控闸据此让位旧的持仓数上限/每日次数/
	// 每日预算/战法分配；行业/单票检查仍作为第二道保险执行（不因本标记放行）。
	// 网关侧不消费（ignored_by_gateway），只随单留痕便于审计。
	// English: v3 dynamic-budget marker — legacy position-count/daily/strategy-alloc gates yield;
	// sector/single-stock checks still run as a second safety net. The gateway ignores this field.
	DynamicBudget bool `json:"dynamic_budget,omitempty"`
}

// OrderResult 下单返回（网关 → 首尔）。
// English: order result (gateway → Seoul).
type OrderResult struct {
	OK      bool   `json:"ok"`            // 是否受理成功
	OrderID string `json:"order_id"`      // 网关委托单号
	Err     string `json:"err,omitempty"` // 错误信息
	// PlacedQty §T1-PARTIAL-W5 实际报出的股数（风控减量放行后由 controller 回填）。
	// 为什么要单独一个字段：请求量与报出量在减量档下会不同，调用方（自动卖出腿）必须能分辨
	// "我要求卖 1000、实际只报了 600"，否则会把残量当成交、把减仓槽烧掉（Q7 的残量槽问题）。
	// 0 表示未经过 controller 的钳位回填（老调用方/直连 executor 的路径）。
	// English: §T1-PARTIAL-W5 — the qty actually sent to the broker after risk clamping (0 = not filled in).
	PlacedQty int `json:"placed_qty,omitempty"`
}

// GatewayState 网关状态快照（/state）。
// English: gateway state snapshot (/state).
type GatewayState struct {
	Connected bool                 `json:"connected"` // QMT 是否在线
	Account   string               `json:"account"`   // 资金账号
	Positions []store.RealPosition `json:"positions"` // 网关侧全部持仓（对账源）
	Orders    []store.RealOrder    `json:"orders"`    // 网关侧委托
}

// Executor 下单执行器接口：屏蔽真实网关与 mock/降级实现差异。
// English: Executor abstracts order placement, hiding real-gateway vs mock/degraded implementations.
type Executor interface {
	PlaceBuy(req OrderRequest) (*OrderResult, error)  // 买入下单
	PlaceSell(req OrderRequest) (*OrderResult, error) // 卖出下单
	Cancel(orderID string) error                      // 撤单
	State() (*GatewayState, error)                    // 网关状态/持仓对账源
	Health() (ok bool, err error)                     // 网关健康探测
}

// NoopExecutor 空实现：无网关或 qmt.enabled=false 时使用，所有调用均记录并返回"未执行"。
// English: NoopExecutor is the null implementation used when no gateway is configured or qmt.enabled is
// false — every call is logged and returns "not executed".
type NoopExecutor struct{}

// PlaceBuy 空实现：返回未执行。
func (NoopExecutor) PlaceBuy(req OrderRequest) (*OrderResult, error) { // func
	return &OrderResult{OK: false, OrderID: "", Err: "qmt disabled (noop executor)"}, nil // return
}

// PlaceSell 空实现：返回未执行。
func (NoopExecutor) PlaceSell(req OrderRequest) (*OrderResult, error) {
	return &OrderResult{OK: false, OrderID: "", Err: "qmt disabled (noop executor)"}, nil
}

// Cancel 空实现：返回 nil（无操作）。
func (NoopExecutor) Cancel(orderID string) error { return nil }

// State 空实现：返回未连接。
func (NoopExecutor) State() (*GatewayState, error) {
	return &GatewayState{Connected: false}, nil
}

// Health 空实现：返回未连接。
func (NoopExecutor) Health() (bool, error) { return false, nil }

// QuoteProvider 实时行情提供接口：控制器计算建议/下单参考价时读取现价。
// English: QuoteProvider abstracts realtime quote lookups for advice/order reference prices.
type QuoteProvider interface {
	// Quote 返回指定代码的实时行情快照（StockInfo）；无数据时返回 nil。
	Quote(code string) *data.StockInfo
}
