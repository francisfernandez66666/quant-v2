// controller.go — 交易执行控制器：监听 QMT 配置热加载，维护网关连接/熔断状态，执行下单并落库幂等。
// 网关失联/心跳超时 → 熔断暂停全部下单并告警；恢复自动解熔。本地订单以 signal_id 唯一键幂等。
// English: trade execution controller — hot-reads the QMT config, tracks gateway connectivity/circuit
// breaking, places orders and persists them idempotently. Gateway loss / heartbeat timeout trips a
// circuit breaker that pauses all orders and alerts; recovery auto-unbreaks. Local orders dedupe on
// the signal_id unique key.
package trading

import (
	"fmt"
	"log"
	"sync"
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/store"
)

// Controller 交易执行控制器。
// English: Controller is the trade execution controller.
type Controller struct {
	mu sync.RWMutex

	exec    Executor        // 下单执行器（真实网关 / noop）
	store   *store.DB       // 研究库（real_positions/orders/fills 落库）
	cfg     config.QMTConfig // 当前生效的 QMT 配置（热加载替换）
	userID  string          // 归属账号（多账号模式下各引擎独立控制器）

	// 熔断状态：tripped=true 表示网关失联/心跳超时，暂停一切新下单
	tripped   bool
	tripAt    time.Time
	tripReason string
	lastHealthAt time.Time // 最近一次健康探测时间（节流）
	lastHealthy  bool
	lastFailAt   time.Time // 最近一次失败探测时间（熔断判定窗口用）

	// 通知回调（告警熔断/恢复）：由上层注入（SSE/notify）。
	onAlert func(level, title, content string)
}

// NewController 创建控制器。onAlert 可空。
// English: NewController builds a controller; onAlert may be nil.
func NewController(exec Executor, db *store.DB, userID string, cfg config.QMTConfig, onAlert func(level, title, content string)) *Controller {
	if exec == nil {
		exec = NoopExecutor{}
	}
	return &Controller{
		exec:    exec,
		store:   db,
		userID:  userID,
		cfg:     cfg,
		onAlert: onAlert,
	}
}

// UpdateConfig 热更新配置（引擎每轮从 config.Manager 读取后调用）。
// English: UpdateConfig hot-updates the QMT config (called by the engine each cycle from the manager).
func (c *Controller) UpdateConfig(cfg config.QMTConfig) {
	c.mu.Lock()
	c.cfg = cfg
	c.mu.Unlock()
}

// Enabled 是否启用实盘链路。
// English: Enabled reports whether the live-trading chain is on.
func (c *Controller) Enabled() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cfg.Enabled
}

// Mode 返回执行模式（auto/manual）。
// （Mode returns the execution mode auto/manual.）
func (c *Controller) Mode() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cfg.Mode
}

// Tripped 是否处于熔断状态（网关失联/心跳超时）。
// English: Tripped reports whether the circuit breaker is open (gateway lost / heartbeat timeout).
func (c *Controller) Tripped() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.tripped
}

// TripInfo 返回熔断详情。
// （TripInfo returns circuit-breaker details.）
func (c *Controller) TripInfo() (tripped bool, reason string, at time.Time) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.tripped, c.tripReason, c.tripAt
}

// setTripped 置/解熔断并告警（仅在状态变化时触发一次）。
// English: setTripped flips the breaker and alerts once per state change.
func (c *Controller) setTripped(tripped bool, reason string) {
	c.mu.Lock()
	changed := c.tripped != tripped
	if tripped {
		c.tripped = true
		c.tripAt = time.Now()
		c.tripReason = reason
	} else {
		c.tripped = false
		c.tripReason = ""
	}
	onAlert := c.onAlert
	c.mu.Unlock()
	if !changed || onAlert == nil {
		return
	}
	if tripped {
		onAlert("high", "QMT 实盘熔断", reason)
	} else {
		onAlert("info", "QMT 实盘恢复", "网关连接恢复，自动解熔")
	}
	log.Printf("[trading] circuit breaker %v: %s", tripped, reason)
}

// SetTripped 外部置熔断（网关断线回报等确定性事件立即熔断，不等心跳超时）。
// English: SetTripped externally opens the breaker (deterministic events like a disconnect report trip
// immediately rather than waiting for the heartbeat timeout).
func (c *Controller) SetTripped(reason string) {
	c.setTripped(true, reason)
}

// HealthCheck 定期健康探测（节流 miss_heartbeat_sec 的 1/2）：失联超时 → 熔断，恢复 → 解熔。
// English: HealthCheck probes the gateway on a throttle (half of miss_heartbeat_sec); timeout trips the
// breaker, recovery unbreaks.
func (c *Controller) HealthCheck() {
	c.mu.RLock()
	cfg := c.cfg
	last := c.lastHealthAt
	c.mu.RUnlock()
	if !cfg.Enabled || cfg.GatewayURL == "" {
		return
	}
	interval := time.Duration(cfg.MissHeartbeatSec) * time.Second / 2
	if interval <= 0 {
		interval = 60 * time.Second
	}
	if time.Since(last) < interval {
		return
	}
	c.mu.Lock()
	c.lastHealthAt = time.Now()
	c.mu.Unlock()

	ok, err := c.exec.Health()
	c.mu.RLock()
	cfg2 := c.cfg
	c.mu.RUnlock()
	miss := time.Duration(cfg2.MissHeartbeatSec) * time.Second
	if miss <= 0 {
		miss = 120 * time.Second
	}
	if err == nil && ok {
		if !c.Tripped() {
			// 正常：刷新健康标记（熔断时保持 tripped 直到心跳持续恢复）
			c.mu.Lock()
			c.lastHealthy = true
			c.mu.Unlock()
		} else {
			c.setTripped(false, "")
		}
		return
	}
	// 探测失败：连续失败超过 miss 窗口才真正熔断
	c.mu.Lock()
	lastFail := c.lastFailAt
	c.lastFailAt = time.Now()
	c.lastHealthy = false
	c.mu.Unlock()
	if lastFail.IsZero() {
		return
	}
	if time.Since(lastFail) >= miss {
		c.setTripped(true, "网关心跳连续失联超过 "+miss.String())
	}
}

// PlaceOrder 下单（幂等 + 熔断前置校验）。
//  - 熔断中：拒绝新下单并返回错误；
//  - signal_id 已在 orders 表：返回已存在（幂等，不重复下单）。
// English: PlaceOrder places an order with idempotency and breaker pre-checks — rejects while tripped,
// and a signal_id already in the orders table short-circuits (idempotent, never double-sends).
func (c *Controller) PlaceOrder(req OrderRequest) (*OrderResult, error) {
	if c.Tripped() {
		return nil, fmt.Errorf("qmt circuit-breaker open: %s", c.tripReasonLocked())
	}
	c.mu.RLock()
	cfg := c.cfg
	c.mu.RUnlock()
	if !cfg.Enabled {
		return nil, fmt.Errorf("qmt disabled")
	}
	if c.store == nil {
		return nil, fmt.Errorf("qmt store not set")
	}

	// 幂等：同一 signal_id 不重复下单
	existed, err := c.store.UpsertRealOrder(store.RealOrder{
		SignalID:  req.SignalID,
		Code:      req.Code,
		Side:      req.Side,
		Status:    "已报",
		Price:     req.Price,
		Qty:       req.Qty,
		CreatedAt: req.CreatedAt,
	})
	if err != nil {
		return nil, err
	}
	if existed {
		return &OrderResult{OK: false, Err: "duplicate signal_id (already ordered)"}, nil
	}

	// 白名单过滤：strategies 非空且不含该策略时拒绝
	if len(cfg.Strategies) > 0 && req.Strategy != "" {
		allowed := false
		for _, s := range cfg.Strategies {
			if s == req.Strategy {
				allowed = true
				break
			}
		}
		if !allowed {
			return nil, fmt.Errorf("strategy %q not in qmt whitelist", req.Strategy)
		}
	}

	// 仓位上限校验：max_positions>0 且当前持仓数已达上限时，仅允许卖出
	if cfg.MaxPositions > 0 && req.Side == SideBuy {
		poses, err := c.store.RealPositions()
		if err != nil {
			return nil, err
		}
		if len(poses) >= cfg.MaxPositions {
			return nil, fmt.Errorf("real positions %d >= max_positions %d", len(poses), cfg.MaxPositions)
		}
	}

	if req.PriceType == "" {
		req.PriceType = cfg.PriceType
		if req.PriceType == "" {
			req.PriceType = "market"
		}
	}

	var res *OrderResult
	if req.Side == SideSell {
		res, err = c.exec.PlaceSell(req)
	} else {
		res, err = c.exec.PlaceBuy(req)
	}
	if err != nil {
		return nil, err
	}
	// 回填网关委托单号并更新状态
	if res.OrderID != "" {
		if err := c.store.UpdateRealOrderStatus(res.OrderID, "已报"); err != nil {
			log.Printf("[trading] update order status: %v", err)
		}
	}
	return res, nil
}

// Reconcile 从网关拉取全量持仓/委托并落库（对账）。网关不可达时返回错误（不落库）。
// English: Reconcile pulls full positions/orders from the gateway and persists them (reconciliation).
// Returns an error (without persisting) when the gateway is unreachable.
func (c *Controller) Reconcile() error {
	st, err := c.exec.State()
	if err != nil {
		return err
	}
	if c.store != nil && len(st.Positions) > 0 {
		if _, err := c.store.UpsertRealPositions(st.Positions); err != nil {
			return err
		}
	}
	return nil
}

// tripReasonLocked 返回熔断原因（不加锁，调用方需持锁/已检查）。
// English: tripReasonLocked returns the breaker reason without locking.
func (c *Controller) tripReasonLocked() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.tripReason
}

// Config 返回当前生效的 QMT 配置副本。
// English: Config returns a copy of the current QMT config.
func (c *Controller) Config() config.QMTConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cfg
}