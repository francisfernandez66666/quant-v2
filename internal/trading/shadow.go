// shadow.go — §WS-G ShadowExecutor：staging 影子引擎的执行器。
// 接收/记录订单 → 回执"已受理"但永不真下（写 shadow_orders 表），让 staging 跑完整决策树
// （新闻/信号/建议/auto 决策/风控闸）而不碰钱。等价于"关闭实盘跑 paper"但保留 auto 决策完整。
// English: §WS-G ShadowExecutor — the staging engine's executor. Records orders and echoes "accepted"
// without ever placing real ones (persisted to shadow_orders), so staging exercises the full decision
// tree (news/signal/advice/auto/circuit breakers) without touching money — like paper mode but with the
// complete auto-decision path intact.
package trading

import (
	"fmt"

	"quant-trading-v2/internal/store"
)

// ShadowExecutor 影子执行器：实现 Executor，把下单决策落 shadow_orders 并回执受理成功。
// English: ShadowExecutor implements Executor, persisting each order decision to shadow_orders and
// echoing acceptance back.
type ShadowExecutor struct {
	st     *store.DB
	userID string
}

// NewShadowExecutor 创建影子执行器。st 可为 nil（此时仅回执，不落账）。
// English: NewShadowExecutor builds a shadow executor; st may be nil (echo-only, no persistence).
func NewShadowExecutor(st *store.DB, userID string) *ShadowExecutor {
	return &ShadowExecutor{st: st, userID: userID}
}

// PlaceBuy 记录一笔影子买单并回执成功。
// English: records a shadow buy and echoes acceptance.
func (s *ShadowExecutor) PlaceBuy(req OrderRequest) (*OrderResult, error) {
	return s.record(req, "GW-SHADOW-BUY")
}

// PlaceSell 记录一笔影子卖单并回执成功。
// English: records a shadow sell and echoes acceptance.
func (s *ShadowExecutor) PlaceSell(req OrderRequest) (*OrderResult, error) {
	return s.record(req, "GW-SHADOW-SELL")
}

// record 落账 shadow_orders（signal_id 幂等）并回执受理。
// English: persists to shadow_orders (idempotent on signal_id) and echoes acceptance.
func (s *ShadowExecutor) record(req OrderRequest, orderID string) (*OrderResult, error) {
	if s.st != nil {
		_, err := s.st.InsertShadowOrder(store.ShadowOrder{
			SignalID:   req.SignalID,
			Code:       req.Code,
			Name:       req.Name,
			Strategy:   req.Strategy,
			StrategyID: req.StrategyID,
			Side:       req.Side,
			Price:      req.Price,
			Qty:        req.Qty,
			Amount:     req.Amount,
			UserID:     s.userID,
			CreatedAt:  req.CreatedAt,
		})
		if err != nil {
			return nil, fmt.Errorf("shadow record: %w", err)
		}
	}
	return &OrderResult{OK: true, OrderID: orderID}, nil
}

// Cancel 影子环境无真实委托可撤，直接成功。
// English: no real tickets to cancel in shadow mode; always succeeds.
func (s *ShadowExecutor) Cancel(orderID string) error { return nil }

// State 影子网关恒报已连接 + 空持仓（对账源为空——真实持仓由 staging 预置或空跑）。
// English: the shadow gateway always reports connected with no positions (reconciliation source is
// empty; real positions come from staging presets or an empty run).
func (s *ShadowExecutor) State() (*GatewayState, error) {
	return &GatewayState{Connected: true}, nil
}

// Health 影子网关恒健康。
// English: the shadow gateway is always healthy.
func (s *ShadowExecutor) Health() (bool, error) { return true, nil }
