// controller.go — 交易执行控制器：监听 QMT 配置热加载，维护网关连接/熔断状态，执行下单并落库幂等。
// 网关失联/心跳超时 → 熔断暂停全部下单并告警；恢复自动解熔。本地订单以 signal_id 唯一键幂等。
// English: trade execution controller — hot-reads the QMT config, tracks gateway connectivity/circuit
// breaking, places orders and persists them idempotently. Gateway loss / heartbeat timeout trips a
// circuit breaker that pauses all orders and alerts; recovery auto-unbreaks. Local orders dedupe on
// the signal_id unique key.
package trading

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/metrics"
	"quant-trading-v2/internal/opslog"
	"quant-trading-v2/internal/risk"
	"quant-trading-v2/internal/store"
)

// execHolder 统一 atomic.Value 存储载荷：同一具体类型避免 Store 不同类型触发 panic。
// English: uniform payload for atomic.Value so the stored concrete type never changes.
type execHolder struct{ e Executor }

// Controller 交易执行控制器。
// English: Controller is the trade execution controller.
type Controller struct {
	mu sync.RWMutex // 保护控制器状态字段的读写锁

	// §R3-1 P0-C 下单互斥：PlaceOrder 的 守卫检查→占位落库→网关下单→单号回填 整段串行化。
	// 此前三步之间存在 TOCTOU 窗口——两个不同 signal_id 的并发请求都能通过预算预检后
	// 双双真实下单（超预算）；HTTP 手动入口与引擎自动入口并存时是现实资损风险。
	// 只串行化下单路径（HealthCheck/StateSnapshot 等读路径不取该锁），单实盘账户场景无吞吐损失。
	orderMu sync.Mutex // 下单路径互斥锁

	exec            atomic.Value     // 下单执行器（真实网关 / noop）——§修复 FIX#7 原子引用，持 Executor 接口
	store           *store.DB        // 研究库（real_positions/orders/fills 落库）
	cfg             config.QMTConfig // 当前生效的 QMT 配置（热加载替换）
	userID          string           // 归属账号（多账号模式下各引擎独立控制器）
	lastReconcileAt time.Time        // §W6-a 上次主动对账时间（节流用）

	// §QMT-PENDING 待生效配置（开关队列）：普通配置变更（enabled/mode/白名单/纪律）先入队，
	// 由引擎在交易时段（scoreCycle 的 IsActiveSession 门控内）调用 ApplyPendingConfig 才真正
	// 应用到 c.cfg 并重建 executor（Noop↔QMTClient）。避免休市时配置变更立即翻转实盘行为、
	// 以及"构建时固化 Noop 后永远不下单"的 executor 固化 bug。halt（kill-switch）走 UpdateConfig
	// 立即生效，不进队列——紧急停止语义必须即时。
	// English: pending config (switch queue). Ordinary config changes are queued and only applied at
	// trading session via ApplyPendingConfig, which also rebuilds the executor (Noop↔QMTClient) to avoid
	// the build-time-Noop-stuck bug. halt (kill-switch) bypasses the queue via UpdateConfig — immediate.
	pendingCfg *config.QMTConfig // 待生效配置（nil=无待应用变更）

	// 熔断状态：tripped=true 表示网关失联/心跳超时，暂停一切新下单
	tripped      bool      // 是否处于熔断状态
	tripAt       time.Time // 熔断触发时间
	tripReason   string    // 熔断原因
	lastHealthAt time.Time // 最近一次健康探测时间（节流）
	lastHealthy  bool      // 最近一次健康探测是否成功
	lastFailAt   time.Time // §CB-RUNSTART 本轮连续失联的起点时间（探测成功即清零；非"最近一次失败时间"）

	// 互通健康展示数据（仪表盘-系统）：下行=首尔探测广州网关，上行=广州网关回报到首尔。
	lastLatencyMs  int64     // 最近一次健康探测往返时延（毫秒）
	lastReportAt   time.Time // 最近一次收到网关回报时间（上行通道新鲜度）
	lastReportKind string    // 最近一次回报类型（trade/order/positions/disconnect）

	// §ROBUST 早期预警：健康→失败的首跳立即告警（区别于熔断的 high 级），恢复后复位。
	warnedUnhealthy bool // 是否已发出过健康转失败早期预警

	// §R4-1 撤单闭环节流状态（mu 保护）
	lastSweepAt       time.Time // 最近一次 SweepOrders 执行时间（30s 节流）
	lastCloseSweepDay string    // 最近一次执行收盘清单的交易日（每日一次）
	// §C1b 跨日陈旧买单无条件清扫的独立节流戳（mu 保护）：不受 Enabled/Tripped 管辖，
	// 故不能复用 lastSweepAt（后者在早退分支之前不被推进）。
	lastStaleSweepAt time.Time

	// §WS-B 交割单对账节流：lastSettleDay 记录最近对账交易日（每日一次，mu 保护）
	// §D4（2026-09-22 修复批）语义收紧：**只在 SettleDay 成功后**才写本字段（失败回滚/不置位），
	// 否则一次网关故障就把当日对账永久跳过。English: only a successful settlement marks the day done.
	lastSettleDay string // 最近一次 SettleDay **成功**的交易日
	// §D4 失败节流重试：lastSettleAttemptAt=最近一次对账尝试时刻（无论成败，防死循环重投），
	// settleFailDay/settleFailCount=当日失败次数（opslog 留痕可数）。三处均 mu 保护。
	// English: §D4 retry throttle + per-day failure counter (attempt stamp guards against hot looping).
	lastSettleAttemptAt time.Time // 最近一次对账尝试时刻（成功或失败都推进）
	settleFailDay       string    // 失败计数归属的交易日
	settleFailCount     int       // 该交易日累计失败次数

	// §WS-C 风控闸口统一入口：placeOrder 的全部前置守卫收敛到 risk.Gate.CheckLiveOrder
	//（controller 只保留 orderMu 串行、幂等与 executor 分发）。新闸命中即记录+告警。
	// English: §WS-C unified risk gate — all pre-order guards live in risk.Gate.CheckLiveOrder.
	gate *risk.Gate

	// 通知回调（告警熔断/恢复）：由上层注入（SSE/notify）。
	onAlert func(level, title, content string) // 告警回调（可空）
}

// NewController 创建控制器。onAlert 可空。
// English: NewController builds a controller; onAlert may be nil.
func NewController(exec Executor, db *store.DB, userID string, cfg config.QMTConfig, onAlert func(level, title, content string)) *Controller {
	if exec == nil {
		exec = NoopExecutor{}
	}
	c := &Controller{
		store:   db,
		userID:  userID,
		cfg:     cfg,
		onAlert: onAlert,
	}
	c.exec.Store(execHolder{exec}) // §FIX#7 executor 走原子引用
	// §WS-C 风控闸：告警回调复用 onAlert（新闸高优告警；存量守卫静默）。
	c.gate = risk.NewGate(db, userID, onAlert)
	return c
}

// execRef 返回当前生效的下单执行器（原子读；nil 兜底 Noop）。
// §修复 FIX#7（2026-09-04）：executor 由 ApplyPendingConfig 在交易时段被替换，其余路径并发读取。
// 旧实现裸字段读写构成 data race（go test -race 可复现），且替换窗口内入队任务可能命中错误执行器
// （如真单任务在切换成 Noop 后被当作业务拒单静默丢弃）。统一走原子引用后替换与读取天然互斥。
// English: §FIX#7 — the executor is swapped at trading session by ApplyPendingConfig while other
// goroutines (worker / sweep / health) read it concurrently; the old plain field raced and could hand a
// real order to the wrong executor mid-swap. Reads now go through this atomic getter.
func (c *Controller) execRef() Executor {
	v := c.exec.Load()
	if v == nil {
		return NoopExecutor{}
	}
	return v.(execHolder).e
}

// UpdateConfig 立即应用配置（紧急语义）：halt（kill-switch）/ 测试直接生效，不进队列。
// 注意：只更新 c.cfg，不重建 executor——executor 类型由构建或 ApplyPendingConfig 决定。
// English: UpdateConfig applies config immediately (emergency semantics) — halt/kill-switch and tests
// take effect right away, bypassing the queue. Only updates c.cfg; the executor type is decided at
// build or by ApplyPendingConfig.
func (c *Controller) UpdateConfig(cfg config.QMTConfig) {
	c.mu.Lock()
	c.cfg = cfg
	c.mu.Unlock()
}

// QueueConfigUpdate 将配置变更放入待生效队列（开关队列）：仅记录，不立即应用。
// 由引擎每轮热同步调用（GetQMTConfigFor 最新值），交易时段由 ApplyPendingConfig 消费。
// English: QueueConfigUpdate queues a config change (switch queue) without applying it. Called by the
// engine's hot-sync each cycle with the latest GetQMTConfigFor; consumed by ApplyPendingConfig at the
// trading session.
func (c *Controller) QueueConfigUpdate(cfg config.QMTConfig) {
	c.mu.Lock()
	cp := cfg
	c.pendingCfg = &cp
	c.mu.Unlock()
}

// ApplyPendingConfig 在交易时段应用待生效配置并重建执行器（Noop↔QMTClient）。
// 返回是否真的有配置被应用。enabled && gateway_url 非空 → 真实网关客户端；否则回退 Noop。
// 这修复了"构建时 qmt.enabled=false 固化 NoopExecutor 后，即使配置开启也永不下单、
// Health 恒失败"的 executor 固化 bug。
// English: ApplyPendingConfig applies the queued config at the trading session and rebuilds the
// executor (Noop↔QMTClient) accordingly. Returns whether a config was actually applied. It fixes the
// build-time-Noop-stuck bug: once fixed to NoopExecutor, the chain could never place orders or pass
// Health even after the config was enabled.
func (c *Controller) ApplyPendingConfig() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pendingCfg == nil {
		return false
	}
	cfg := *c.pendingCfg
	c.pendingCfg = nil
	c.cfg = cfg
	needClient := cfg.Enabled && cfg.GatewayURL != ""
	_, isClient := c.exec.Load().(execHolder).e.(*QMTClient)
	switch {
	case needClient && !isClient:
		c.exec.Store(execHolder{NewQMTClient(cfg.GatewayURL, cfg.Token, time.Duration(cfg.TimeoutSec)*time.Second, 1)})
		log.Printf("[trading] QMT 实盘已启用，executor 切换为真实网关 (%s)", cfg.GatewayURL)
	case !needClient && isClient:
		c.exec.Store(execHolder{NoopExecutor{}})
		log.Printf("[trading] QMT 实盘已停用，executor 回退 Noop")
	}
	return true
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

// AvailableCash 返回最近一次网关上报的可用资金与其新鲜度（§M12-A 三态口径，2026-09-22 owner 定调）。
// 资产由广州网关每分钟对账上报（real_account 表，§M1）；返回值语义：
//   - (value, true)：账本可查且 30 分钟内有回报——value 是**真值**，0 就是真没钱；
//   - (value/0, false)：未接账本库/查询失败/回报超过 30 分钟——**资金口径不可得**。
//
// 调用方（engine 自动买入腿）对 fresh=false 的处理是 **fail-close：不自动买**（保留手动通道），
// 并经 /api/qmt/state 的 cash_stale 暴露给前端降级横幅。旧两态口径（未知/过期一律返回 0、
// 调用方视为「不设限」放行）已废弃——H-4 事故证明冻结/断供的资金值与真零混在一起时，
// 要么全拦要么全放，两个方向都是事故。
// English: §M12-A three-state cash basis (owner verdict A, fail-close). fresh=false means the
// ledger basis is unavailable (store nil / query error / report older than 30min) and the auto-buy
// leg must NOT fire; manual orders stay available and the state endpoint exposes cash_stale for a
// frontend banner. The old two-state contract (0 = "no cap") is retired.
func (c *Controller) AvailableCash() (float64, bool) {
	if c.store == nil {
		return 0, false // 未接账本库=资金口径不可得（§M12-A：调用方 fail-close，不自动买）
	}
	acc, err := c.store.GetRealAccount(c.userID)
	if err != nil {
		return 0, false // 查询失败同为口径不可得
	}
	// 对账数据超过 30 分钟视为过期：带着原值返回 fresh=false，由调用方裁决。
	// §0925EVE-W3-F（⑰/D2 时间口径收口）：UpdatedAt 是网关按北京时间写入的墙钟串
	// （cntime 包声明的全系统唯一时区源），此处必须用 cntime.Loc 解析——此前误用
	// time.Local，UTC 主机上北京墙钟串会被当作 UTC 时刻读成「未来 8 小时」，
	// Since 为负恒判新鲜，40 分钟前的过期资金照旧放行 fresh=true（H-4 同族口径漂移）。
	updated, perr := time.ParseInLocation("2006-01-02 15:04:05", acc.UpdatedAt, cntime.Loc)
	if perr != nil || time.Since(updated) > 30*time.Minute {
		return acc.AvailableCash, false
	}
	return acc.AvailableCash, true // 新鲜真值：0 即真无可用资金
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

// SetCrossPriceSource §XCHECK 2026-09-22 C批：为风控闸注入价格复核的独立复核源
// （透明委托给 risk.Gate——复核逻辑全部在闸内，控制器只做装配透传）。gate 在
// NewController 恒非 nil（见构造函数 :107 装配），无需判空；fn 传 nil 即令本闸保持跳过。
// English: §XCHECK wires the independent cross-check price source through to the risk gate
// (pass-through only — all logic lives in the gate; the gate is always non-nil here).
func (c *Controller) SetCrossPriceSource(fn func(code string) (float64, error)) {
	c.gate.SetCrossPriceSource(fn)
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
	// §M12-A（2026-09-22 owner 裁决 A）：资金三态口径随快照暴露——cash_stale=true 表示
	// 资金口径不可得（未接账本/查询失败/回报超 30 分钟），此时自动买入已被 fail-close 拦下，
	// 前端据此渲染降级横幅而不是把「没有买入」误读成「没有信号」。cash 是最近一次的原始值
	// （过期时该值仅供参考，不代表当下真数）。
	// English: §M12-A — the three-state cash basis is exposed on the snapshot: cash_stale=true
	// means auto-buy is fail-closed (ledger basis unavailable/stale) and the frontend shows a
	// degradation banner; cash is the raw last-known value (informational while stale).
	Cash      float64 `json:"cash"`
	CashStale bool    `json:"cash_stale"`
	// PendingEnabled §FIX-2：待生效队列中的 enabled（§QMT-PENDING）。非 nil 表示存在一笔尚未在
	// 交易时段被 ApplyPendingConfig 消费的开关变更——前端据此显示"已配置，将于下一交易时段生效"，
	// 而不是把延迟生效误判为"开关没打开"。为 nil 表示当前无待生效变更（已收敛到 c.cfg.Enabled）。
	// English: PendingEnabled — the enabled value sitting in the §QMT-PENDING queue; non-nil means a
	// switch change is queued but not yet consumed at a trading session (so the UI can show
	// "configured, takes effect next session" instead of misreading deferred apply as "switch off").
	PendingEnabled *bool `json:"pending_enabled"`
}

// Snapshot 返回当前互通健康快照（纯读，不加锁副作用）。
// （Snapshot returns the current connectivity snapshot; read-only.）
func (c *Controller) Snapshot() StateSnapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var pendingEnabled *bool
	if c.pendingCfg != nil {
		v := c.pendingCfg.Enabled
		pendingEnabled = &v
	}
	// §M12-A：资金三态随快照暴露（AvailableCash 不取锁，此处 RLock 内调用安全）。
	cash, cashFresh := c.AvailableCash()
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
		Cash:           cash, // §M12-A 原始最近值（过期时仅供参考）
		CashStale:      !cashFresh,
		PendingEnabled: pendingEnabled,
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
		metrics.BreakerTripped()              // §R4-9 熔断计数
		metrics.SetGauge("breaker_active", 1) // §WS-L 熔断状态量规（告警规则 breaker_open）
		onAlert("high", "QMT 实盘熔断", reason)
	} else {
		metrics.SetGauge("breaker_active", 0)
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

	// 真正探活：计时调用柜台 Health 并记下往返耗时（前端健康条用），
	// 配置与 miss 窗口都现场重取，保证热更新 MissHeartbeatSec 后本轮就生效。
	started := time.Now()
	ok, err := c.execRef().Health()
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
		// §CB-RUNSTART（2026-09-21 误熔实录）：探测成功即代表链路此刻连通，清零失联窗口起点。
		// 旧实现 lastFailAt 从不清零，桥心跳"爆发式"推进（QMT 回调只在行情动时跑）时，
		// 两次被成功隔开的失败也能凑满 2 分钟窗口而误熔（09:10:27 即此形态）。
		c.mu.Lock()
		c.lastFailAt = time.Time{}
		c.mu.Unlock()
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
	// 探测失败：lastFailAt 语义为「本轮连续失联起点」——首个失败只开窗并返回，
	// 窗口内后续不再刷新起点，只有连续失败时长 ≥ miss 才熔断。旧实现每轮都覆写为
	// "最近一次失败时间"，相邻失败间隔恒等于探测周期（miss/2），真·彻底断线反而永不熔断。
	c.mu.Lock()
	prevHealthy := c.lastHealthy
	runStart := c.lastFailAt
	if runStart.IsZero() {
		c.lastFailAt = time.Now()
	}
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
	if runStart.IsZero() {
		return // 本轮失联开窗：首个失败只记账，不计入失联时长
	}
	if time.Since(runStart) >= miss {
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

	// 两道最前置的拒绝先过：熔断开着就把熔断原因带回调用方（前端能直接显示"为什么不下单"），
	// 配置在读锁下取快照，避免热更新途中读到半份配置。
	if c.Tripped() {
		// §D5：currentTripReason 自带 RLock，此处调用方未持锁（安全）；旧名 tripReasonLocked 误导。
		return nil, fmt.Errorf("qmt circuit-breaker open: %s", c.currentTripReason())
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

	// §WS-C 风控闸口统一入口：ST/黑名单/T+1/涨跌停不可追单/行情新鲜度硬闸/日内已实现亏损熔断/
	// 单票集中度/买入纪律/白名单/仓位上限 全部收敛到 risk.Gate.CheckLiveOrder 单一权威入口——
	// controller 不再散装守卫（新增闸口只改 gate，不可能漏装）。命中即拒单并记录 risk_gates 命中；
	// 新闸（涨跌停/新鲜度/熔断/集中度）额外高优告警。无数据字段（PrevClose/StalenessMs 等）对应闸
	// fail-open 跳过。English: §WS-C unified risk gate — every pre-order guard lives in one authoritative
	// entry (adding a gate means editing only the gate, it can't be forgotten); hits are recorded in
	// risk_gates; new gates also alert; missing quote fields fail their gates open.
	lo := risk.LiveOrder{
		SignalID:     req.SignalID,
		Code:         req.Code,
		Name:         req.Name,
		Strategy:     req.Strategy,
		StrategyID:   req.StrategyID,
		StrategyType: req.StrategyType,
		Side:         req.Side,
		Price:        req.Price,
		Qty:          req.Qty,
		Amount:       req.Amount,
		StalenessMs:  req.StalenessMs,
		CurrentPrice: req.CurrentPrice,
		PrevClose:    req.PrevClose,
	}
	if v := c.gate.CheckLiveOrder(cfg, lo); !v.Pass {
		return nil, fmt.Errorf("%s", v.Reason)
	}

	// 幂等：同一 signal_id 不重复下单。
	// §GAP 修复：占位行 order_id 用 "pend:<signal_id>"——此前恒为空串，与 order_id 主键冲突，
	// 第二笔起的新单被 INSERT OR IGNORE 误判为重复（静默不下单），网关单号回填也永不命中。
	// §GAP2-W1 重试放行：命中已有行时先尝试把可重放行重置为"已报"——发送失败（§GAP2-W1）
	// 与「已撤且零成交」（§H1-MG）同语义放行；真正的重复（已报待回报/部分成交/已成/已撤有成交）
	// 仍被唯一键拦截，返回 duplicate。
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
		res, err = c.execRef().PlaceSell(req)
	} else {
		res, err = c.execRef().PlaceBuy(req)
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
	st, err := c.execRef().State()
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
// report_url 未配时双向对账均不存在）：按 interval 节流（默认 5min）主动拉网关全量持仓落库。
//
// §D5（2026-09-22 修复批）注释与实现对齐：旧注释写「差异仅记日志告警——自动纠偏留给显式人工/
// 后续策略，避免误覆盖在途状态」，而实现走的正是 Reconcile → store.ReconcilePositionsForUser
// **以网关快照为唯一真值的全量覆盖**（本账号行 upsert + 快照里没有的行删除）——也就是说
// "自动纠偏"从来就发生了，注释把它否认掉会让后来人按错误心智模型改这段代码（例如以为
// 覆盖要人工授权而在别处重复实现纠偏）。按实现改注释：
//   - 持仓腿：**自动全量覆盖**（券商为准），可信前提是 Reconcile 内部的"空快照 + 未连接 → 拒清"
//     守卫（§R3-8 P1-G）；
//   - 委托腿：仅比对条数、记差异日志，**不**自动纠偏（状态推进以回报线程为准）。
//
// English: §D5 — the comment now matches the code: the positions leg IS auto-corrected (gateway
// snapshot wins, via user-scoped ReconcilePositionsForUser, guarded by the empty-snapshot/
// not-connected check), while only the orders leg is log-and-wait-for-reports.
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
	if err := c.execRef().Cancel(orderID); err != nil {
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

// HaltAllFailure §0925EVE-A2：kill-switch 批量撤单中单笔失败的明细。
// 为什么要带 order_id+原因：旧实现撤单失败只 log+continue，操作者按下"停止交易"后
// 只看到一个成功计数——没撤掉的单仍在网关挂着，却对触发者完全不可见（本仓主题「降级报成功」）。
// English: one per-order cancel failure (order id + reason) surfaced to the caller.
type HaltAllFailure struct {
	OrderID string `json:"order_id"` // 撤单失败的委托号；"-"/非单号值表示批量前置环节失败（如清单读取）
	Reason  string `json:"reason"`   // 失败原因（网关错误文本），供前端逐条展示
}

// HaltAllResult §0925EVE-A2：HaltAll 的结构化返回。
// Cancelled 保留旧"成功笔数"语义（HTTP 响应字段名不变，向后兼容）；Failed 是新增的
// 失败明细数组——恒非 nil（JSON 序列化成 []而非 null），前端据此无条件渲染。
// English: structured HaltAll result; Cancelled keeps the old meaning, Failed is always
// non-nil so the JSON contract shows an empty array (not null) on full success.
type HaltAllResult struct {
	Cancelled int              `json:"cancelled"` // 成功撤销并推进本地行为"已撤"的笔数
	Failed    []HaltAllFailure `json:"failed"`    // 撤单失败明细（空数组=全部撤成）
}

// HaltAll §R4-1 kill-switch 配套：撤销本地账本中全部"已报"/"部成"未成交委托（非占位行）。
// 占位行（pend:，从未到达网关）不撤——由 MarkRealOrderSendFailed 降级口径处理。
// §0925EVE-A2：返回值从裸 int（只有成功数）升级为 HaltAllResult——失败笔同样必须到达
// 操作者眼前（HTTP 响应 + 前端提示 + p1 量规告警三腿），log+continue 只是留痕不是告知。
// 量规 halt_cancel_fail_count 每轮以 len(Failed) 覆写（含清零）：清零这步不能省，否则
// 上次置位的失败会一直挂着让告警处于 firing 态、恢复（resolved）永远发不出去。
// 多账号下各 Controller 覆写同一进程级量规与 settlement_diff_count 同口径（单实盘账户场景，可接受）。
// English: §R4-1 kill-switch companion — cancels every unfilled 已报/部成 (non-placeholder) order.
// §0925EVE-A2: returns a structured result (cancelled count + per-order failure list) instead of
// a bare count, and always overwrites the halt_cancel_fail_count gauge so the alert can resolve.
func (c *Controller) HaltAll() HaltAllResult {
	// failed 预分配为空切片：保证 JSON 侧是 [] 而不是 null（前端/门禁都按数组消费）
	res := HaltAllResult{Failed: make([]HaltAllFailure, 0)}
	// store==nil：控制器无账本可撤（研究环境/降级装配），按"零撤单零失败"返回，量规同步清零
	if c.store == nil {
		metrics.SetGauge("halt_cancel_fail_count", 0)
		return res
	}
	orders, err := c.store.RealOrdersForUser(c.userID)
	if err != nil {
		// 清单都读不出来时旧实现静默 return 0——kill-switch 按下后一笔没撤却显示"撤销 0 笔
		// 成功"，正是本缺陷族里最迷惑的形态。现在记为一条前置失败（OrderID 用占位符 "-"），
		// 让操作者在响应与告警里看到"为什么连清单都读不到"。
		log.Printf("[trading] HaltAll 委托清单读取失败(用户=%s): %v", c.userID, err)
		res.Failed = append(res.Failed, HaltAllFailure{OrderID: "-", Reason: "委托清单读取失败: " + err.Error()})
		metrics.SetGauge("halt_cancel_fail_count", int64(len(res.Failed)))
		return res
	}
	for _, o := range orders {
		// §P1-7 kill-switch 同样覆盖部成，撤销剩余未成交部分。
		if (o.Status != "已报" && o.Status != "部成") || strings.HasPrefix(o.OrderID, "pend:") {
			continue
		}
		if err := c.execRef().Cancel(o.OrderID); err != nil {
			// §0925EVE-A2：失败不再只 log——记入明细，随响应回传前端并进 p1 量规。
			// 本地行状态不动（可能仍"已报"），撤不掉的留给 SweepOrders 闭环/人工处置。
			log.Printf("[trading] HaltAll 撤单失败 %s: %v", o.OrderID, err)
			res.Failed = append(res.Failed, HaltAllFailure{OrderID: o.OrderID, Reason: err.Error()})
			continue
		}
		if ok, err := c.store.UpdateRealOrderStatusMonotonic(c.userID, o.OrderID, "已撤"); err == nil && ok {
			res.Cancelled++
		}
	}
	metrics.SetGauge("halt_cancel_fail_count", int64(len(res.Failed)))
	return res
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
// §C1b：函数最前面有一段**不受** Enabled/Tripped/cancel_stale_sec 管辖的跨日陈旧买单无条件
// 降级（纯本地账），下面的网关撤单主循环才受既有早退约束。
func (c *Controller) SweepOrders(now time.Time) *SweepResult {
	// §C1b（2026-09-22 修复批）跨日陈旧买单无条件清扫：A 股委托收盘即失效，前日的
	// 已报/部成 买单必然是僵尸行；此路解冻不依赖网关可达（旧行为在熔断/关闭/
	// cancel_stale_sec=-1 下整体早退，网关崩溃遗留挂单永无兜底）。30s 独立节流。
	if c.store != nil {
		c.mu.RLock()
		lastStale := c.lastStaleSweepAt
		c.mu.RUnlock()
		if now.Sub(lastStale) >= 30*time.Second {
			c.mu.Lock()
			c.lastStaleSweepAt = now
			c.mu.Unlock()
			beforeDay := cntime.In(now).Format("2006-01-02")
			n, err := c.store.SweepStaleBuyOrders(c.userID, beforeDay)
			if err != nil {
				log.Printf("[trading] §C1b 跨日陈旧买单清扫失败: %v", err)
			} else if n > 0 {
				log.Printf("[trading] §C1b 跨日陈旧买单降级废单 %d 笔（账户=%s，早于 %s）", n, c.userID, beforeDay)
				opslog.Logf("quant", "跨日陈旧买单无条件降级 %d笔 账户=%s 早于%s（网关撤单路未覆盖部分的本地兜底）", n, c.userID, beforeDay)
			}
		}
	}
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
		if err := c.execRef().Cancel(o.OrderID); err != nil {
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

// currentTripReason 返回熔断原因（**自带 RLock**，调用方无需持锁）。
// §D5（2026-09-22 修复批）注释与命名对齐实现：旧名 tripReasonLocked 的 `Locked` 后缀在 Go 惯例里
// 表示"调用方需持锁、本函数不加锁"，而注释也这么写（"不加锁，调用方需持锁/已检查"），实现却自己
// `c.mu.RLock()`——注释与名字双双说谎。当前唯一调用点 placeOrder 在未持锁状态下调用，暂时没死锁，
// 但按名传锁（未来有人在持有 c.mu 的路径上复用）就是自锁死锁（RWMutex 不可重入）。
// 二选一里取了「按实现改注释+改名」：不改成真正的 unlocked 版本，因为 placeOrder 调用点没有现成的
// 持锁上下文（那里紧接着才 RLock 取 cfg 快照），做成 unlocked 版本反而扩大临界区。
// 未直接叫 tripReason：与同名字段 c.tripReason 冲突（Go 不允许类型上字段与方法同名），故用 current- 前缀。
// English: §D5 — renamed to match the implementation: the function DOES lock (RLock) itself, so the
// `Locked` suffix and its "caller must hold the lock" comment were both wrong and could have caused a
// self-deadlock if reused from a locked path. `currentTripReason` (not `tripReason`) because the
// controller already has a field with that exact name.
func (c *Controller) currentTripReason() string {
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

// gatewayClient 构建一个直连网关的临时客户端（读当前 cfg 的 gateway_url/token）。
// §QMT-DUAL：broker 切换/状态读取不经过下单执行器抽象，独立建客户端避免耦合 Executor 接口。
// English: builds a throwaway gateway client from the current config for broker status/switch,
// bypassing the order executor abstraction.
func (c *Controller) gatewayClient() *QMTClient {
	c.mu.RLock()
	cfg := c.cfg
	c.mu.RUnlock()
	if !cfg.Enabled || cfg.GatewayURL == "" {
		return nil
	}
	return NewQMTClient(cfg.GatewayURL, cfg.Token, 8*time.Second, 0)
}

// GatewayBrokerStatus 查询网关 active 通道与双路径状态（§QMT-DUAL，admin 观察用）。
// English: queries the gateway's active broker and dual-path liveness for admin observability.
func (c *Controller) GatewayBrokerStatus() (*GatewayBrokerStatus, error) {
	gc := c.gatewayClient()
	if gc == nil {
		return nil, errors.New("qmt not enabled or gateway_url not set")
	}
	return gc.BrokerStatus()
}

// SwitchGatewayBroker 切换网关 active 通道（xt=miniqmt / queued=qmt 桥），§QMT-DUAL admin 入口。
// English: switches the gateway's active broker between xt (miniQMT) and queued (QMT bridge).
func (c *Controller) SwitchGatewayBroker(broker string) error {
	if broker != "xt" && broker != "queued" {
		return fmt.Errorf("broker must be xt|queued, got %q", broker)
	}
	gc := c.gatewayClient()
	if gc == nil {
		return errors.New("qmt not enabled or gateway_url not set")
	}
	if err := gc.SwitchBroker(broker); err != nil {
		return err
	}
	log.Printf("[trading] admin 切换网关 active 通道 -> %s (用户=%s)", broker, c.userID)
	return nil
}
