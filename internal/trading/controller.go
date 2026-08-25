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
	"strings"
	"sync"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/store"
)

// Controller 交易执行控制器。
// English: Controller is the trade execution controller.
type Controller struct {
	mu sync.RWMutex

	exec   Executor         // 下单执行器（真实网关 / noop）
	store  *store.DB        // 研究库（real_positions/orders/fills 落库）
	cfg    config.QMTConfig // 当前生效的 QMT 配置（热加载替换）
	userID string           // 归属账号（多账号模式下各引擎独立控制器）

	// 熔断状态：tripped=true 表示网关失联/心跳超时，暂停一切新下单
	tripped      bool
	tripAt       time.Time
	tripReason   string
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

// PlaceOrder 下单（幂等 + 熔断 + 前置守卫）。
//   - 熔断中：拒绝新下单并返回错误；
//   - 前置守卫（ST/单日纪律/白名单/仓位上限）全部通过后才落库占位——不打算下的单绝不写 orders 表，
//     避免被拒订单留下幽灵行污染当日统计；
//   - signal_id 已在 orders 表：返回已存在（幂等，不重复下单）。
//
// English: PlaceOrder places an order with idempotency, breaker and pre-checks — rejects while tripped;
// all guards (ST / daily discipline / whitelist / position cap) run BEFORE the pending ticket is
// persisted so rejected orders never leave phantom rows; a signal_id already in the table short-circuits.
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

	// §GAP1.6 ST/退市风险警示股一律拒绝下单（auto/manual 全路径统一收口；
	// 与信号层 combat_agent.IsSTStock 同一判定，堵住 ScanLimitUp 直出信号与手动单绕过）。
	// English: ST/delisting-risk stocks are rejected on every path (auto & manual), using the same
	// combat_agent.IsSTStock check as the signal layer — closing the ScanLimitUp / manual-order bypass.
	if combat_agent.IsSTStock(req.Name) {
		return nil, fmt.Errorf("ST/退市风险股禁止下单: %s", req.Name)
	}

	// §GAP1.7 黑名单接线：命中 qmt.blacklist（含引擎同步的 Theme.BlackList）即拒绝。
	// 纯数字与带后缀代码双向归一比对。English: §GAP1.7 blacklist wiring — normalized both ways.
	if blacklisted(cfg.Blacklist, req.Code) {
		return nil, fmt.Errorf("黑名单股票禁止下单: %s", req.Code)
	}

	// §GAP1.3/1.4 买入纪律预检（卖出不受限）：单日买入笔数上限、单日买入预算、近似可用资金。
	amount := req.Amount
	if amount <= 0 {
		amount = req.Price * float64(req.Qty)
	}
	if req.Side == SideBuy {
		if err := c.checkBuyDiscipline(cfg, req.SignalID, amount); err != nil {
			return nil, err
		}
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

	// 幂等：同一 signal_id 不重复下单。
	// §GAP 修复：占位行 order_id 用 "pend:<signal_id>"——此前恒为空串，与 order_id 主键冲突，
	// 第二笔起的新单被 INSERT OR IGNORE 误判为重复（静默不下单），网关单号回填也永不命中。
	existed, err := c.store.UpsertRealOrder(store.RealOrder{
		OrderID:   "pend:" + req.SignalID,
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
	// 回填网关委托单号并更新状态（占位行按 signal_id 定位）
	if res.OrderID != "" {
		if err := c.store.UpdateRealOrderBySignalID(req.SignalID, res.OrderID, "已报"); err != nil {
			log.Printf("[trading] backfill order id: %v", err)
		}
	}
	return res, nil
}

// blacklisted §GAP1.7 黑名单比对：条目与请求代码均剥离后缀后按纯数字前缀匹配。
// English: blacklisted matches after stripping exchange suffixes on both sides.
func blacklisted(list []string, code string) bool {
	if len(list) == 0 || code == "" {
		return false
	}
	pure := func(c string) string {
		if i := strings.IndexByte(c, '.'); i > 0 {
			c = c[:i]
		}
		return strings.TrimSpace(c)
	}
	pc := pure(code)
	for _, item := range list {
		if pi := pure(item); pi != "" && pi == pc {
			return true
		}
	}
	return false
}

// checkBuyDiscipline §GAP1.3/1.4 买入纪律预检：单日买入笔数上限、单日买入预算、近似可用资金。
// 数据源为本地 real_orders/real_positions 账本（崩溃安全，重启不丢当日累计）；
// selfSignalID 排除自身（幂等重试场景下占位行可能已存在）；守卫先于 UpsertRealOrder 执行，
// 被拒订单不落库、不污染当日统计。
// 可用资金 ≈ InitialCapital − Σ持仓成本市值 − 当日已报买单金额（近似口径，
// 精确券商可用余额待 M2 网关协议扩展 query_cash 后接入）。
// English: buy-discipline precheck — daily buy-count cap, daily budget and an estimated available-cash
// check, derived from the local real book (crash-safe). selfSignalID excludes the ticket itself
// (idempotent retries); guards run BEFORE UpsertRealOrder so rejected orders never pollute the sums.
// Exact broker cash lands with the M2 protocol extension (query_cash).
func (c *Controller) checkBuyDiscipline(cfg config.QMTConfig, selfSignalID string, amount float64) error {
	if c.store == nil {
		return nil
	}
	orders, err := c.store.RealOrders()
	if err != nil {
		return fmt.Errorf("read real orders: %w", err)
	}
	today := time.Now().Format("2006-01-02")
	buys := 0
	spent := 0.0
	for _, o := range orders {
		if o.Side != SideBuy || o.SignalID == selfSignalID {
			continue
		}
		at, perr := time.Parse(time.RFC3339, o.CreatedAt)
		if perr != nil || at.Format("2006-01-02") != today {
			continue
		}
		buys++
		spent += o.Price * float64(o.Qty)
	}
	if cfg.DailyMaxBuys > 0 && buys >= cfg.DailyMaxBuys {
		return fmt.Errorf("单日买入笔数达上限 %d（今日已报 %d 笔）", cfg.DailyMaxBuys, buys)
	}
	if cfg.DailyBudgetAmount > 0 && spent+amount > cfg.DailyBudgetAmount {
		return fmt.Errorf("单日买入预算不足: 已报 %.0f + 本次 %.0f > 预算 %.0f", spent, amount, cfg.DailyBudgetAmount)
	}
	if cfg.InitialCapital > 0 {
		pos, err := c.store.RealPositions()
		if err != nil {
			return fmt.Errorf("read real positions: %w", err)
		}
		held := 0.0
		for _, p := range pos {
			held += p.CostPrice * float64(p.Qty)
		}
		if avail := cfg.InitialCapital - held - spent; amount > avail {
			return fmt.Errorf("可用资金不足: 预估可用 %.0f（本金%.0f−持仓成本%.0f−今日已报%.0f）< 本次 %.0f",
				avail, cfg.InitialCapital, held, spent, amount)
		}
	}
	return nil
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
