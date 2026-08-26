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

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/store"
)

// Controller 交易执行控制器。
// English: Controller is the trade execution controller.
type Controller struct {
	mu sync.RWMutex

	exec            Executor         // 下单执行器（真实网关 / noop）
	store           *store.DB        // 研究库（real_positions/orders/fills 落库）
	cfg             config.QMTConfig // 当前生效的 QMT 配置（热加载替换）
	userID          string           // 归属账号（多账号模式下各引擎独立控制器）
	lastReconcileAt time.Time        // §W6-a 上次主动对账时间（节流用）

	// 熔断状态：tripped=true 表示网关失联/心跳超时，暂停一切新下单
	tripped      bool
	tripAt       time.Time
	tripReason   string
	lastHealthAt time.Time // 最近一次健康探测时间（节流）
	lastHealthy  bool
	lastFailAt   time.Time // 最近一次失败探测时间（熔断判定窗口用）

	// 互通健康展示数据（仪表盘-系统）：下行=首尔探测广州网关，上行=广州网关回报到首尔。
	lastLatencyMs  int64     // 最近一次健康探测往返时延（毫秒）
	lastReportAt   time.Time // 最近一次收到网关回报时间（上行通道新鲜度）
	lastReportKind string    // 最近一次回报类型（trade/order/positions/disconnect）

	// §ROBUST 早期预警：健康→失败的首跳立即告警（区别于熔断的 high 级），恢复后复位。
	warnedUnhealthy bool

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

// SetLastReport 记录最近一次网关回报（上行通道 广州→首尔 的新鲜度证据，供 /api/qmt/state
// 与仪表盘-系统互通健康行展示；kind ∈ trade/order/positions/disconnect）。
// English: records the latest gateway report kind/time — evidence of the uplink (gateway→Seoul)
// freshness, surfaced via /api/qmt/state and the dashboard system row.
func (c *Controller) SetLastReport(kind string) {
	c.mu.Lock()
	c.lastReportAt = time.Now()
	c.lastReportKind = kind
	c.mu.Unlock()
}

// StateSnapshot 互通健康快照：下行（首尔探测网关）+ 上行（网关回报到首尔）两侧状态，
// 供 /api/qmt/state、仪表盘系统行与量化交易页消费。零值时间表示"从未发生"。
// English: connectivity snapshot for the dashboard/system row and quant page — downlink probe
// state plus uplink report freshness; zero times mean "never happened".
type StateSnapshot struct {
	Enabled        bool      `json:"enabled"`
	Mode           string    `json:"mode"`
	Tripped        bool      `json:"tripped"`
	TripReason     string    `json:"trip_reason"`
	TripAt         time.Time `json:"trip_at"`
	GatewayURL     string    `json:"gateway_url"`
	LastProbeAt    time.Time `json:"last_probe_at"`
	LastProbeOK    bool      `json:"last_probe_ok"`
	LastLatencyMs  int64     `json:"last_latency_ms"`
	LastReportAt   time.Time `json:"last_report_at"`
	LastReportKind string    `json:"last_report_kind"`
}

// Snapshot 返回当前互通健康快照（纯读，不加锁副作用）。
// （Snapshot returns the current connectivity snapshot; read-only.）
func (c *Controller) Snapshot() StateSnapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return StateSnapshot{
		Enabled:        c.cfg.Enabled,
		Mode:           c.cfg.Mode,
		Tripped:        c.tripped,
		TripReason:     c.tripReason,
		TripAt:         c.tripAt,
		GatewayURL:     c.cfg.GatewayURL,
		LastProbeAt:    c.lastHealthAt,
		LastProbeOK:    c.lastHealthy,
		LastLatencyMs:  c.lastLatencyMs,
		LastReportAt:   c.lastReportAt,
		LastReportKind: c.lastReportKind,
	}
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

	started := time.Now()
	ok, err := c.exec.Health()
	c.mu.Lock()
	c.lastLatencyMs = time.Since(started).Milliseconds()
	c.mu.Unlock()
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
			c.warnedUnhealthy = false
			c.mu.Unlock()
		} else {
			c.setTripped(false, "")
		}
		return
	}
	// 探测失败：连续失败超过 miss 窗口才真正熔断
	c.mu.Lock()
	prevHealthy := c.lastHealthy
	lastFail := c.lastFailAt
	c.lastFailAt = time.Now()
	c.lastHealthy = false
	firstFailAfterHealthy := prevHealthy && !c.warnedUnhealthy
	if firstFailAfterHealthy {
		c.warnedUnhealthy = true // 首跳告警只发一次，直到恢复
	}
	c.mu.Unlock()
	if firstFailAfterHealthy {
		// §ROBUST 早期预警：不等熔断，健康→失败的第一次跳变立即提醒（medium 级）
		c.fireOnAlert("medium", "QMT 网关探测失败",
			fmt.Sprintf("最近一次 /health 探测失败: %v（连续失联将熔断暂停下单）", err))
	}
	if lastFail.IsZero() {
		return
	}
	if time.Since(lastFail) >= miss {
		c.setTripped(true, "网关心跳连续失联超过 "+miss.String())
	}
}

// fireOnAlert 触发上层告警回调（可空安全）。
// （fireOnAlert invokes the injected alert callback if present.）
func (c *Controller) fireOnAlert(level, title, content string) {
	c.mu.RLock()
	cb := c.onAlert
	c.mu.RUnlock()
	if cb != nil {
		cb(level, title, content)
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

	// §GAP1.6 ST/退市风险警示股一律拒绝买入（auto/manual 全路径统一收口；
	// 与信号层 combat_agent.IsSTStock 同一判定，堵住 ScanLimitUp 直出信号与手动单绕过）。
	// §GAP2-W1 修复：守卫仅作用于买入方向——持仓股盘中被戴帽 ST/进黑名单属于"风险暴露已存在"，
	// 拦截卖出等于强迫扛单，与止损/风控目标背道而驰；此前双向拦截会让这类仓位永远无法退出。
	// English: §GAP1.6 ST/delisting-risk stocks are rejected on BUY only (both auto & manual paths),
	// using the same combat_agent.IsSTStock check as the signal layer. §GAP2-W1 fix: sells are exempt —
	// a held stock getting ST-flagged/blacklisted mid-day is existing exposure; blocking its exit would
	// force holding, the opposite of risk control.
	if req.Side == SideBuy && combat_agent.IsSTStock(req.Name) {
		return nil, fmt.Errorf("ST/退市风险股禁止买入: %s", req.Name)
	}

	// §GAP1.7 黑名单接线（仅买方向，理由同上）：命中 qmt.blacklist（含引擎同步的 Theme.BlackList）即拒。
	// 纯数字与带后缀代码双向归一比对。English: §GAP1.7 blacklist wiring (buy-only) — normalized both ways.
	if req.Side == SideBuy && blacklisted(cfg.Blacklist, req.Code) {
		return nil, fmt.Errorf("黑名单股票禁止买入: %s", req.Code)
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
	// §GAP2-W1 重试放行：命中已有行时先尝试把"发送失败"占位行重置为"已报"——只有确认发送失败的
	// 单才允许同键重发；真正的重复（已报待回报/部分成交/已成/已撤）仍被唯一键拦截，返回 duplicate。
	existed, err := c.store.UpsertRealOrder(store.RealOrder{
		OrderID:   "pend:" + req.SignalID,
		SignalID:  req.SignalID,
		Code:      req.Code,
		Side:      req.Side,
		Status:    "已报",
		Price:     req.Price,
		Qty:       req.Qty,
		CreatedAt: req.CreatedAt,
		UserID:    c.userID, // §W2-10 委托行打归属账号（多账号审计/后续租户读过滤）
	})
	if err != nil {
		return nil, err
	}
	if existed {
		reset, rerr := c.store.ResetFailedRealOrder(req.SignalID)
		if rerr != nil {
			return nil, rerr
		}
		if !reset {
			return &OrderResult{OK: false, Err: "duplicate signal_id (already ordered)"}, nil
		}
		log.Printf("[trading] %s 此前发送失败，本次重试放行", req.SignalID)
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
		// §GAP2-W1 发送失败降级：占位行从"已报"改为"发送失败"——
		// ①不再冒充已报污染买入纪律统计（幽灵单虚耗当日预算的根因）；
		// ②同 signal_id 下次重试经 ResetFailedRealOrder 放行，止损自动单不会因一次网络抖动被封死整天。
		// 带状态守卫：若回报线程已推进到 部分成交/已成（超时但券商实际受理），绝不回退真实进度。
		// English: §GAP2-W1 on a send error, demote the placeholder from 已报 to 发送失败 so it stops
		// polluting buy-discipline accounting and can be retried under the same signal_id. Status-guarded:
		// fills that already landed via report callbacks are never rolled back.
		if merr := c.store.MarkRealOrderSendFailed(req.SignalID); merr != nil {
			log.Printf("[trading] mark send-failed %s: %v", req.SignalID, merr)
		}
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
// §GAP2-W1 状态过滤：只统计 已报/部成/已成 三类真实占用资金的状态——"发送失败"占位降级行与
// 已撤/废单不再虚耗当日预算与笔数（幽灵单根因之一）。
// §TZ1 日期口径：委托时间统一换算北京时区再取日（此前用服务器本地时区，首尔 KST 主机在
// 北京时间 08:00 翻转日期，恰好落在盘前窗口）。
// 可用资金 ≈ InitialCapital − Σ持仓成本市值 − 当日已报买单金额（近似口径，
// 精确券商可用余额待 M2 网关协议扩展 query_cash 后接入）。
// English: buy-discipline precheck — daily buy-count cap, daily budget and an estimated available-cash
// check, derived from the local real book (crash-safe). selfSignalID excludes the ticket itself
// (idempotent retries); guards run BEFORE UpsertRealOrder so rejected orders never pollute the sums.
// §GAP2-W1 status filter: only 已报/部成/已成 count as real capital usage — send-failed placeholders,
// cancelled and rejected orders no longer eat the daily budget. §TZ1: order timestamps are converted to
// Beijing time before extracting the date. Exact broker cash lands with the M2 extension (query_cash).
func (c *Controller) checkBuyDiscipline(cfg config.QMTConfig, selfSignalID string, amount float64) error {
	if c.store == nil {
		return nil
	}
	orders, err := c.store.RealOrders()
	if err != nil {
		return fmt.Errorf("read real orders: %w", err)
	}
	today := cntime.In(time.Now()).Format("2006-01-02")
	buys := 0
	spent := 0.0
	for _, o := range orders {
		if o.Side != SideBuy || o.SignalID == selfSignalID {
			continue
		}
		// §GAP2-W1 状态白名单：仅真实占款状态计入；发送失败/已撤等跳过。
		// English: §GAP2-W1 only capital-consuming statuses count.
		switch o.Status {
		case "已报", "部成", "已成":
		default:
			continue
		}
		at, perr := time.Parse(time.RFC3339, o.CreatedAt)
		if perr != nil || cntime.In(at).Format("2006-01-02") != today {
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

// MaybeReconcile §W6-a 周期对账接线（此前 Controller.Reconcile 是零调用死代码，
// report_url 未配时双向对账均不存在）：按 interval 节流（默认 5min）主动拉网关全量持仓
// 落库（券商为准），差异仅记日志告警——自动纠偏留给显式人工/后续策略，避免误覆盖在途状态。
// English: §W6-A wires the previously-dead Reconcile into a throttled periodic call; diffs are logged
// as warnings while auto-correction is deliberately left out to avoid stomping in-flight states.
func (c *Controller) MaybeReconcile(interval time.Duration) {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	c.mu.RLock()
	last := c.lastReconcileAt
	enabled := c.cfg.Enabled
	c.mu.RUnlock()
	if !enabled || time.Since(last) < interval {
		return
	}
	c.mu.Lock()
	c.lastReconcileAt = time.Now()
	c.mu.Unlock()
	go func() {
		if err := c.Reconcile(); err != nil {
			log.Printf("[trading] 周期对账失败（下次窗口重试）: %v", err)
			return
		}
		if poses, err := c.store.RealPositionsForUser(c.userID); err == nil {
			log.Printf("[trading] 周期对账完成: %d 持仓已与网关同步", len(poses))
		}
	}()
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
