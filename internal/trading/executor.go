// Package trading — 实盘交易执行层（AUTO_TRADING_PLAN M1 首尔侧）。
// 负责把首尔侧决策（信号/持仓建议）转发给国内 Windows 网关（东莞证券 MiniQMT）执行真实下单，
// 并管理网关连接状态、熔断、幂等与本地订单状态。与纸面账本（paper/report.Report）完全独立。
// English: live-trading execution layer (AUTO_TRADING_PLAN M1, Seoul side). Forwards Seoul-side decisions
// (signals / position advice) to the domestic Windows gateway (Guoxin MiniQMT) for real orders, and
// manages gateway connectivity, circuit breaking, idempotency and local order state. Fully independent
// of the paper book (paper/report.Report).
package trading

import (
	"quant-trading-v2/internal/config"
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
	SignalID   string  `json:"signal_id"`             // 信号唯一标识（幂等键，网关去重）
	Code       string  `json:"code"`                  // 股票代码（带后缀，如 600000.SH）
	Name       string  `json:"name"`                  // 股票名称
	Strategy   string  `json:"strategy"`              // 触发战法名称（显示名）
	StrategyID string  `json:"strategy_id,omitempty"` // 战法库规则 ID（fac_1/pat_2/n_shape…，白名单同键）
	Side       string  `json:"side"`                  // 买入/卖出
	PriceType  string  `json:"price_type"`            // market=对手价 / limit=限价
	Price      float64 `json:"price"`                 // 参考价（limit 时按此限价）
	Qty        int     `json:"qty"`                   // 股数（整手）
	Amount     float64 `json:"amount"`                // 金额（元）
	CreatedAt  string  `json:"created_at"`            // 创建时间（RFC3339）
}

// OrderResult 下单返回（网关 → 首尔）。
// English: order result (gateway → Seoul).
type OrderResult struct {
	OK      bool   `json:"ok"`            // 是否受理成功
	OrderID string `json:"order_id"`      // 网关委托单号
	Err     string `json:"err,omitempty"` // 错误信息
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

// ConfigReader 配置读取接口：控制器从配置管理器热读 QMT 段（避免引入 config.Manager 依赖）。
// English: ConfigReader abstracts hot-reading the QMT config section (avoids a hard dependency on the
// config.Manager).
type ConfigReader interface {
	QMTConfigFor(userID string) config.QMTConfig
}

// QuoteProvider 实时行情提供接口：控制器计算建议/下单参考价时读取现价。
// English: QuoteProvider abstracts realtime quote lookups for advice/order reference prices.
type QuoteProvider interface {
	Quote(code string) *data.StockInfo
}
