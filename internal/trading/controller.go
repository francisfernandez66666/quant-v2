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
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/metrics"
	"quant-trading-v2/internal/opslog"
	"quant-trading-v2/internal/store"
)

// Controller 交易执行控制器。
// English: Controller is the trade execution controller.
type Controller struct {
	mu sync.RWMutex // 保护控制器状态字段的读写锁

	// §R3-1 P0-C 下单互斥：PlaceOrder 的 守卫检查→占位落库→网关下单→单号回填 整段串行化。
	// 此前三步之间存在 TOCTOU 窗口——两个不同 signal_id 的并发请求都能通过预算预检后
	// 双双真实下单（超预算）；HTTP 手动入口与引擎自动入口并存时是现实资损风险。
	// 只串行化下单路径（HealthCheck/StateSnapshot 等读路径不取该锁），单实盘账户场景无吞吐损失。
	orderMu sync.Mutex // 下单路径互斥锁

	exec            Executor         // 下单执行器（真实网关 / noop）
	store           *store.DB        // 研究库（real_positions/orders/fills 落库）
	cfg             config.QMTConfig // 当前生效的 QMT 配置（热加载替换）
	userID          string           // 归属账号（多账号模式下各引擎独立控制器）
	lastReconcileAt time.Time        // §W6-a 上次主动对账时间（节流用）

	// 熔断状态：tripped=true 表示网关失联/心跳超时，暂停一切新下单
	tripped      bool      // 是否处于熔断状态
	tripAt       time.Time // 熔断触发时间
	tripReason   string    // 熔断原因
	lastHealthAt time.Time // 最近一次健康探测时间（节流）
	lastHealthy  bool      // 最近一次健康探测是否成功
	lastFailAt   time.Time // 最近一次失败探测时间（熔断判定窗口用）

	// 互通健康展示数据（仪表盘-系统）：下行=首尔探测广州网关，上行=广州网关回报到首尔。
	lastLatencyMs  int64     // 最近一次健康探测往返时延（毫秒）
	lastReportAt   time.Time // 最近一次收到网关回报时间（上行通道新鲜度）
	lastReportKind string    // 最近一次回报类型（trade/order/positions/disconnect）

	// §ROBUST 早期预警：健康→失败的首跳立即告警（区别于熔断的 high 级），恢复后复位。
	warnedUnhealthy bool // 是否已发出过健康转失败早期预警

	// §R4-1 撤单闭环节流状态（mu 保护）
	lastSweepAt       time.Time // 最近一次 SweepOrders 执行时间（30s 节流）
	lastCloseSweepDay string    // 最近一次执行收盘清单的交易日（每日一次）

	// 通知回调（告警熔断/恢复）：由上层注入（SSE/notify）。
	onAlert func(level, title, content string) // 告警回调（可空）
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

// AvailableCash 返回最近一次网关上报的可用资金；未知/过期/查询失败返回 0（调用方视为"不设限"）。
// 资产由广州网关每分钟对账上报（real_account 表，§M1）；超过 30 分钟未刷新视为过期——
// 此时宁可不降档（维持 fixed_amount 行为），让柜台做最终裁决，也不拿陈旧数字误拦订单。
// English: returns the latest gateway-reported available cash; 0 when unknown/stale(>30min)/error —
// callers treat 0 as "no cap", keeping the pre-existing fixed_amount behavior instead of gating
// orders on stale numbers.
func (c *Controller) AvailableCash() float64 {
	if c.store == nil {
		return 0
	}
	acc, err := c.store.GetRealAccount(c.userID)
	if err != nil || acc.AvailableCash <= 0 {
		return 0
	}
	updated, err := time.ParseInLocation("2006-01-02 15:04:05", acc.UpdatedAt, time.Local)
	if err != nil || time.Since(updated) > 30*time.Minute {
		return 0
	}
	return acc.AvailableCash
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
	Enabled        bool      `json:"enabled"`          // 是否启用
	Mode           string    `json:"mode"`             // 执行模式
	Tripped        bool      `json:"tripped"`          // 是否熔断
	TripReason     string    `json:"trip_reason"`      // 熔断原因
	TripAt         time.Time `json:"trip_at"`          // 熔断触发时间
	GatewayURL     string    `json:"gateway_url"`      // 网关地址
	LastProbeAt    time.Time `json:"last_probe_at"`    // 最近一次探测时间
	LastProbeOK    bool      `json:"last_probe_ok"`    // 最近一次探测是否成功
	LastLatencyMs  int64     `json:"last_latency_ms"`  // 最近一次探测延迟毫秒
	LastReportAt   time.Time `json:"last_report_at"`   // 最近一次上行回报时间
	LastReportKind string    `json:"last_report_kind"` // 最近一次上行回报类型
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
		metrics.BreakerTripped() // §R4-9 熔断计数
		onAlert("high", "QMT 实盘熔断", reason)
	} else {
		onAlert("info", "QMT 实盘恢复", "网关连接恢复，自动解熔")
	}
	log.Printf("[trading] circuit breaker %v: %s", tripped, reason)
	// §DAILY_OPSLOG 熔断/恢复是资金安全的分水岭事件，必须留档
	opslog.Logf("quant", "熔断器 %s: %s", map[bool]string{true: "触发", false: "恢复"}[tripped], reason)
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

// PlaceOrder 下单入口（§R4-9 指标接线）：统计成功/被拒后委托 placeOrder 执行真实守卫链。
// English: PlaceOrder wraps placeOrder, counting accepted vs rejected orders for metrics.
func (c *Controller) PlaceOrder(req OrderRequest) (*OrderResult, error) {
	res, err := c.placeOrder(req)
	if err != nil {
		metrics.OrdersRejected()
	} else if res == nil || !res.OK {
		metrics.OrdersRejected()
	} else {
		metrics.OrdersPlaced()
	}
	return res, err
}

// placeOrder 下单（幂等 + 熔断 + 前置守卫）。
//   - 熔断中：拒绝新下单并返回错误；
//   - 前置守卫（ST/单日纪律/白名单/仓位上限）全部通过后才落库占位——不打算下的单绝不写 orders 表，
//     避免被拒订单留下幽灵行污染当日统计；
//   - signal_id 已在 orders 表：返回已存在（幂等，不重复下单）。
//
// English: placeOrder runs the full guard chain with idempotency + breaker + pre-checks — rejects
// while tripped; all guards (ST / daily discipline / whitelist / position cap) run BEFORE the pending
// ticket is persisted so rejected orders never leave phantom rows; a signal_id already in the table
// short-circuits.
func (c *Controller) placeOrder(req OrderRequest) (*OrderResult, error) {
	// §R3-1 P0-C 下单互斥：整段串行化（见结构体字段注释）。
	c.orderMu.Lock()
	defer c.orderMu.Unlock()

	if c.Tripped() {
		return nil, fmt.Errorf("qmt circuit-breaker open: %s", c.tripReasonLocked())
	}
	c.mu.RLock()
	cfg := c.cfg
	c.mu.RUnlock()
	if !cfg.Enabled {
		return nil, fmt.Errorf("qmt disabled")
	}
	// §R4-1 kill-switch（人工紧急停止）：置位时拒绝一切新下单（auto 与手动全路径）。
	// 已报未成交委托由 SweepOrders 撤单闭环 / HaltAll 处理；卖出同样被拦——紧急停止语义下
	// 一切柜台动作都停，宁可人工接手也不让系统在未知状态下继续动作。
	// English: §R4-1 kill switch — when engaged, ALL new orders (auto & manual, both sides) are
	// rejected; unfilled tickets are handled by SweepOrders/HaltAll. Deliberate fail-stop semantics.
	if cfg.Halted {
		return nil, fmt.Errorf("qmt kill-switch engaged (halted=true)：人工紧急停止中，拒绝一切新下单")
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

	// 白名单过滤：strategies 非空且不含该策略时拒绝。**仅作用于买入方向**——
	// §安全 T2（2026-08-29）：此前无 SideBuy 限制，一旦配置了白名单且不含持仓战法，
	// auto 止损卖单 / M8 清仓卖单会被拒 → 止损不执行，资损。卖出不受白名单约束（退出的持仓
	// 其战法可能未在白名单，但退出是既有风险敞口的了结，不应被拦）。
	// §UAT-FIX 2026-08-31：白名单条目是战法 ID（n_shape/fac_1…），req.Strategy 是中文显示名——
	// 只比显示名会让 ID 白名单永远拒绝。现同时匹配 StrategyID 与显示名。
	if req.Side == SideBuy && len(cfg.Strategies) > 0 && req.Strategy != "" {
		allowed := false
		for _, s := range cfg.Strategies {
			if s == req.Strategy || s == req.StrategyID {
				allowed = true
				break
			}
		}
		if !allowed {
			return nil, fmt.Errorf("strategy %q not in qmt whitelist", req.Strategy)
		}
	}

	// 仓位上限校验：max_positions>0 且当前持仓数已达上限时，仅允许卖出。
	// §R3-1 P0-B 按账号过滤：此前读全表 RealPositions()，多账号部署下 A 的持仓上限被 B 的
	// 持仓占满（误拒）。与插入侧 §W2-10 的 UserID 盖章对齐，读取侧统一走 ForUser。
	if cfg.MaxPositions > 0 && req.Side == SideBuy {
		poses, err := c.store.RealPositionsForUser(c.userID)
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
		reset, rerr := c.store.ResetFailedRealOrder(c.userID, req.SignalID)
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
		if merr := c.store.MarkRealOrderSendFailed(c.userID, req.SignalID); merr != nil {
			log.Printf("[trading] mark send-failed %s: %v", req.SignalID, merr)
		}
		opslog.Logf("quant", "下单发送失败(降级可重试) %s %s %s qty=%d: %v", req.SignalID, req.Side, req.Code, req.Qty, err)
		return nil, err
	}
	// §R3-1 P0-A 业务拒单兜底：网关返回 200+ok:false（券商侧拒绝：资金不足/废单等）时
	// err 为 nil，旧实现直接落到"回填单号"分支——占位行永远停留"已报"，既虚耗当日买入纪律
	// 预算（幽灵已报行），又永不满足 ResetFailedRealOrder 的重置条件（该 signal_id 整天封死）。
	// 与 §GAP2-W1 的传输失败降级同口径处理：降级为"发送失败"，下次同键重试可放行。
	// duplicate 形态在上方 existed 分支已经提前返回，不会进入这里。
	// English: R3-1 P0-A — on a 200+ok:false business rejection (nil error), demote the placeholder
	// from 已报 to 发送失败 so it stops polluting buy-discipline accounting and the same signal_id can
	// be retried later; duplicates never reach here (handled above).
	if res != nil && !res.OK {
		if merr := c.store.MarkRealOrderSendFailed(c.userID, req.SignalID); merr != nil {
			log.Printf("[trading] mark send-failed (business reject) %s: %v", req.SignalID, merr)
		}
		log.Printf("[trading] %s 网关业务拒单: %s（占位行已降级发送失败，可重试）", req.SignalID, res.Err)
		opslog.Logf("quant", "网关业务拒单 %s %s %s qty=%d price=%.2f: %s",
			req.SignalID, req.Side, req.Code, req.Qty, req.Price, res.Err)
		return res, nil
	}
	// 回填网关委托单号并更新状态（占位行按 signal_id 定位）
	if res.OrderID != "" {
		if err := c.store.UpdateRealOrderBySignalID(c.userID, req.SignalID, res.OrderID, "已报"); err != nil {
			log.Printf("[trading] backfill order id: %v", err)
		}
	}
	// §DAILY_OPSLOG 下单受理（网关已收单）——含策略归因与金额口径
	opslog.Logf("quant", "下单受理 %s %s %s qty=%d price=%.2f amount=%.0f 策略=%s/%s order=%s",
		req.SignalID, req.Side, req.Code, req.Qty, req.Price, req.Amount, req.StrategyID, req.Strategy, res.OrderID)
	return res, nil
}

// blacklisted §GAP1.7 黑名单比对：统一委托给 config.CodeInBlacklist（§R3-8 P1-H 唯一权威实现，
// 后缀剥离双向归一）。保留包内别名以兼容既有调用与测试。
// English: §GAP1.7 blacklist check — delegates to the canonical config.CodeInBlacklist
// (R3-8 P1-H) so the risk layer and the execution layer share one matching semantic.
func blacklisted(list []string, code string) bool {
	return config.CodeInBlacklist(list, code)
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
	orders, err := c.store.RealOrdersForUser(c.userID)
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
		pos, err := c.store.RealPositionsForUser(c.userID)
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
	// §R4-3 真实可用资金回灌：网关 account 事件（broker.query_asset）落库的券商口径可用资金，
	// 在新鲜（10 分钟内）时作为额外硬约束。券商 cash 口径已扣除其侧冻结（含已报未成交单），
	// 故此处不再扣减本地 spent，避免与券商冻结双重扣减；近似口径检查仍然并行生效——
	// 两道闸都过才放行，任一拒绝即拒单。
	// English: §R4-3 feeds the broker-side available cash (gateway account report) back into the
	// buy discipline as an extra hard gate when fresh (≤10min). Broker cash already nets out its
	// own frozen amounts, so local `spent` is NOT subtracted again (no double-count); the legacy
	// approximation still runs in parallel — both gates must pass.
	// §R4-3 真实可用资金回灌：网关 account 事件（broker.query_asset）落库的券商口径可用资金，
	// 在新鲜（10 分钟内）时作为额外硬约束。券商 cash 口径已扣除其侧冻结（含已报未成交单），
	// 故此处不再扣减本地 spent，避免与券商冻结双重扣减；近似口径检查仍然并行生效——
	// 两道闸都过才放行，任一拒绝即拒单。
	// 说明（2026-08-29）：AvailableCash<=0 视为"券商尚未回报可用资金/未接通"→ 跳过本闸，
	// 由上方 InitialCapital-held-spent 近似口径继续守卫（fail-open 但有近似闸兜底）。
	// 若需"券商明确回报 0 即拒单"，需新增"已回报"标志位区分，避免阻断 mock/刚连场景的合法买入。
	if acc, err := c.store.GetRealAccount(c.userID); err == nil && acc.AvailableCash > 0 {
		at, perr := time.ParseInLocation("2006-01-02 15:04:05", acc.UpdatedAt, cntime.Loc)
		if perr == nil {
			// 保守口径：快照新鲜（≤10min）用全额可用；陈旧（断开/长时间未上报）则用 50%
			// 作为上限，避免券商侧已冻结/已用资金未知时过量下单。
			cap := acc.AvailableCash
			fresh := time.Since(at) <= 10*time.Minute
			if !fresh {
				cap = acc.AvailableCash * 0.5
			}
			label := "实时"
			if !fresh {
				label = "陈旧保守折算50%"
			}
			if amount > cap {
				return fmt.Errorf("可用资金不足(券商口径%s): 上限 %.2f < 本次 %.2f（上报于 %s）",
					label, cap, amount, acc.UpdatedAt)
			}
		} else if acc.AvailableCash > 0 {
			// UpdatedAt 无法解析：同样走保守 50%% 折算。
			if amount > acc.AvailableCash*0.5 {
				return fmt.Errorf("可用资金不足(券商口径, 时间戳异常保守折算50%%): 上限 %.2f < 本次 %.2f",
					acc.AvailableCash*0.5, amount)
			}
		}
	}
	return nil
}

// Reconcile 从网关拉取全量持仓并落库（对账）。网关不可达时返回错误（不落库）。
// §R3-8 P1-G 三处收口：
//  1. 空快照守卫——网关 /state 在通道断连时也返回空列表（broker.py），不可信快照
//     禁止清账：仅 Connected=true 时才接受"全平"语义；
//  2. 清仓落库——此前 len==0 直接跳过，本地 real_positions 永不清除，陈旧行持续影响
//     Advise/仓位上限；现走用户隔离的 ReconcilePositionsForUser 清理本账号行；
//  3. 用户隔离——只动 本账号 ∪ 遗留全局 行，绝不清其他账号的 scoped 行。
//
// English: R3-8 P1-G — empty snapshots are only trusted when the gateway reports connected;
// a fully-flat book now clears local rows via user-scoped reconciliation (legacy rows included,
// other accounts' rows never touched).
func (c *Controller) Reconcile() error {
	st, err := c.exec.State()
	if err != nil {
		return err
	}
	if c.store == nil {
		return nil
	}
	if len(st.Positions) == 0 && !st.Connected {
		log.Printf("[trading] 对账跳过: 网关未连接且持仓快照为空（不可信，禁止清账）")
		return nil
	}
	if _, err := c.store.ReconcilePositionsForUser(c.userID, st.Positions); err != nil {
		return err
	}
	// 委托流水对账（此前 State 拉回即丢）：仅记日志差异告警，自动纠偏仍留给回报线程。
	if len(st.Orders) > 0 {
		local, _ := c.store.RealOrdersForUser(c.userID)
		if len(local) != len(st.Orders) {
			log.Printf("[trading] 对账差异: 网关委托 %d 笔 vs 本地 %d 笔（以回报线程为准，仅告警）",
				len(st.Orders), len(local))
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

// CancelOrder §R4-1 手动撤单：撤网关委托并把本地行推进为"已撤"。撤单失败如实返回错误
// （网关 409=已成交/已撤/无法撤，交由回报线程推进状态），绝不吞掉失败让首尔误判成功。
// English: §R4-1 manual cancel — cancels at the gateway and marks the local row 已撤 on success;
// failures surface as errors (gateway 409 = filled/cancelled/uncancellable → let report thread progress).
func (c *Controller) CancelOrder(orderID string) error {
	c.mu.RLock()
	enabled := c.cfg.Enabled
	c.mu.RUnlock()
	if !enabled {
		return fmt.Errorf("qmt disabled")
	}
	if err := c.exec.Cancel(orderID); err != nil {
		return err
	}
	if c.store != nil {
		if ok, err := c.store.UpdateRealOrderStatusMonotonic(c.userID, orderID, "已撤"); err != nil {
			log.Printf("[trading] cancel mark local %s: %v", orderID, err)
		} else if !ok {
			log.Printf("[trading] cancel mark local %s skipped: already at same/higher rank", orderID)
		}
	}
	return nil
}

// HaltAll §R4-1 kill-switch 配套：撤销本地账本中全部"已报"/"部成"未成交委托（非占位行），
// 返回成功撤销数。占位行（pend:，从未到达网关）不撤——由 MarkRealOrderSendFailed 降级口径处理。
// English: §R4-1 kill-switch companion — cancels every unfilled 已报/部成 (non-placeholder) order;
// returns the successfully cancelled count. Placeholders are left to the send-failed demotion path.
func (c *Controller) HaltAll() int {
	if c.store == nil {
		return 0
	}
	orders, err := c.store.RealOrdersForUser(c.userID)
	if err != nil {
		return 0
	}
	n := 0
	for _, o := range orders {
		// §P1-7 kill-switch 同样覆盖部成，撤销剩余未成交部分。
		if (o.Status != "已报" && o.Status != "部成") || strings.HasPrefix(o.OrderID, "pend:") {
			continue
		}
		if err := c.exec.Cancel(o.OrderID); err != nil {
			log.Printf("[trading] HaltAll 撤单失败 %s: %v", o.OrderID, err)
			continue
		}
		if ok, err := c.store.UpdateRealOrderStatusMonotonic(c.userID, o.OrderID, "已撤"); err == nil && ok {
			n++
		}
	}
	return n
}

// SweepResult 撤单闭环单轮执行摘要。
// （SweepResult is one cancel-sweep round's summary.）
type SweepResult struct {
	Cancelled  int  // 成功撤销的未成交单数
	Demoted    int  // 占位行降级为发送失败的笔数
	Skipped    int  // 时间不可解析等跳过数
	Errors     int  // 撤单失败笔数（已成交被拒/网关暂不可撤等，留回报线程处理）
	CloseSweep bool // 本轮是否执行了收盘清单
}

// SweepOrders §R4-1 撤单闭环主入口（30s 节流，由引擎 5s 循环调用）：
//  1. 未成交超时自动撤：orders 表 状态=已报 且滞留超过 cancel_stale_sec（默认 120s）→
//     调网关撤单，成功即把本地行推进为"已撤"。撤单被拒（已成交/交易所号未回报）不重试强撤，
//     交由回报线程（§R4-4 单调状态机）推进真实状态；
//  2. 占位行清理：pend: 占位（从未到达网关）滞留超时 → MarkRealOrderSendFailed 降级，
//     释放同 signal_id 重试通道，不再整天虚耗买入纪律预算；
//  3. 收盘清单：到达 close_sweep_at（北京时，默认 14:52）且当日未执行过 → 对当日全部
//     "已报"未成交委托撤单（A 股收盘前清掉悬置单，资金/持仓状态归零进清算）。
//
// 熔断中跳过（网关不可达时撤单必然失败，避免错误风暴）。English: §R4-1 cancel-loop entry
// (30s throttled, called from the engine's 5s cycle): stale unfilled auto-cancel, placeholder
// demotion, and the once-a-day pre-close cancel list. Skipped while the breaker is open.
func (c *Controller) SweepOrders(now time.Time) *SweepResult {
	c.mu.RLock()
	cfg := c.cfg
	last := c.lastSweepAt
	c.mu.RUnlock()
	if !cfg.Enabled || c.store == nil || c.Tripped() {
		return nil
	}
	if now.Sub(last) < 30*time.Second {
		return nil
	}
	c.mu.Lock()
	c.lastSweepAt = now
	c.mu.Unlock()

	// 阈值解析：-1=关闭自动撤单；0=默认 120s
	staleSec := cfg.CancelStaleSec
	if staleSec == 0 {
		staleSec = 120
	}

	// 收盘清单判定：北京时到达 close_sweep_at（默认 1452）且本交易日未执行过
	bj := cntime.In(now)
	day := bj.Format("20060102")
	hhmm := bj.Hour()*100 + bj.Minute()
	closeAt := cfg.CloseSweepAt
	if closeAt == 0 {
		closeAt = 1452
	}
	closeSweep := false
	if closeAt > 0 && hhmm >= closeAt && data.IsTradingDay(bj) {
		c.mu.RLock()
		lastDay := c.lastCloseSweepDay
		c.mu.RUnlock()
		if lastDay != day {
			c.mu.Lock()
			c.lastCloseSweepDay = day
			c.mu.Unlock()
			closeSweep = true
		}
	}
	if staleSec < 0 && !closeSweep {
		return nil // 自动撤单已关闭且未到收盘清单时刻
	}

	orders, err := c.store.RealOrdersForUser(c.userID)
	if err != nil {
		log.Printf("[trading] 撤单闭环读取订单失败: %v", err)
		return nil
	}
	res := &SweepResult{CloseSweep: closeSweep}
	staleAfter := time.Duration(staleSec) * time.Second
	for _, o := range orders {
		// §P1-7 超时撤单范围扩展到部成：已报/部成 均可能剩余未成交，需自动撤销。
		if o.Status != "已报" && o.Status != "部成" {
			continue
		}
		at, perr := time.Parse(time.RFC3339, o.CreatedAt)
		if perr != nil {
			res.Skipped++
			continue
		}
		age := now.Sub(cntime.In(at))
		if strings.HasPrefix(o.OrderID, "pend:") {
			// 占位行从未到达网关：超时降级为"发送失败"（同 signal_id 可重试）
			if staleSec > 0 && age > staleAfter {
				if err := c.store.MarkRealOrderSendFailed(c.userID, o.SignalID); err == nil {
					res.Demoted++
				} else {
					res.Errors++
				}
			}
			continue
		}
		if !((staleSec > 0 && age > staleAfter) || closeSweep) {
			continue
		}
		if err := c.exec.Cancel(o.OrderID); err != nil {
			res.Errors++
			// 典型失败：交易所委托号尚未回报（网关暂不可撤）/ 已成交（撤单被拒）——
			// 不强试，交由回报线程推进真实状态，下轮再评估
			log.Printf("[trading] 自动撤单被拒 %s(%s %s qty=%d, 滞留%s): %v",
				o.OrderID, o.Code, o.Side, o.Qty, age.Round(time.Second), err)
			continue
		}
		if ok, err := c.store.UpdateRealOrderStatusMonotonic(c.userID, o.OrderID, "已撤"); err != nil || !ok {
			res.Errors++
			continue
		}
		res.Cancelled++
		metrics.OrdersCancelled() // §R4-9 撤单计数
		log.Printf("[trading] 自动撤单成功 %s %s %s qty=%d (滞留%s, 收盘清单=%v)",
			o.OrderID, o.Code, o.Side, o.Qty, age.Round(time.Second), closeSweep)
	}
	if res.Cancelled+res.Demoted > 0 || closeSweep {
		log.Printf("[trading] 撤单闭环本轮: 撤销=%d 占位降级=%d 失败=%d 跳过=%d 收盘清单=%v",
			res.Cancelled, res.Demoted, res.Errors, res.Skipped, closeSweep)
		// §DAILY_OPSLOG 收盘清单属每日留档节点；常规轮次仅在确有动作时记
		if closeSweep || res.Cancelled+res.Demoted > 0 {
			opslog.Logf("quant", "撤单闭环 撤销=%d 占位降级=%d 失败=%d 收盘清单=%v",
				res.Cancelled, res.Demoted, res.Errors, closeSweep)
		}
	}
	return res
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
