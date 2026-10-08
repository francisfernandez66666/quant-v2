// alert_routing.go §高-3（收窄版，2026-09-22 傍晚审计批）：给「指标型告警规则」补上出口。
//
// 为什么要这个文件：alerter.go 的阈值规则一直在评估，但 RunAlertEvaluation() 拿到事件后
// 只做了 log.Printf 就结束——规则触发 = 没有任何人知道。本仓把这种形态叫「静默失效」，
// 而且 R7 验收单（docs/archive/R7_HARDENING_PLAN_20260908.md:466/:470）当初勾过 ✅，
// 属于「声称已做」。本文件把这一条腿真正接上。
//
// 口径收窄（别修过头）：**只处理指标型规则**（由 gauge + 阈值派生的回撤/新鲜度/延迟类）。
// 事件型推送腿（熔断/清仓守卫/结算差异/备份失败等经 internal/notify 的广播与推送）本来就在
// 正常工作，本文件不碰、不包装、不转发它——否则会做出双份推送。
// 因此映射表里凡是「事件型腿已经在推」的规则（settlement_diff / settle_failed），
// 一律只进日汇总做量化留痕，不再即时推送。
//
// 路由口径（owner 裁决 4）：熔断 / 回撤 / 降级（新鲜度·延迟·上行停摆）/ 备份失败这类
// 「此刻正疼」的规则必推（LevelHigh 由注入方映射到既有高优通道）；其余指标型规则进日汇总
// （当天触发次数、峰值、首次/末次时间）。汇总的发送时刻：**由下一次评估 tick 检测到本地
// 日历日变更时补发前一日**——本包没有调度权，不新起定时器；而唯一调用点
// （internal/engine/scoring_loop.go 的 30s 节流）在会话门禁之前，盘后/休市也照跑，
// 所以日切最多延迟一个 tick（30s）。
// §W7-E（2026-10-06 修复批 波 7）：「进程停机则重启后首个 tick 补发（按记录的日期标注）」
// 这句承诺以前是**假的**——四份状态全在内存，重启后聚合日直接换成今天，前一日永远不再出现，
// 且挂起的销案连同"已报未销"标记一起丢失（只报不销）。现在 day/daily/pendingR/announced
// （另加 lastF/lastR/suppressed）随每轮 tick 原子落盘到 dataDir 下的 journal 文件，
// 装配期由 SetAlertRouterStatePath 灌回，实现与注释从本批起才真正对齐。详见 alert_router_state.go。
//
// 限频（照 §C9 推送风暴抑制的思路）：同一规则同一状态（fire / resolved）出站后进冷却窗，
// 窗内重复破线不再刷第二条；恢复消息成对补发——被冷却挡下的 resolved 会挂起，
// 窗到期后补发，保证 alert/resolved 必成对、不会只报不销。
//
// 依赖方向（已核实）：internal/notify 只 import internal/{opslog,strategy,fileutil}，
// 不 import internal/metrics，所以反向依赖不会立刻成环；但 metrics 是被 notify 的兄弟层
// 共用的度量面，一旦 import notify 就把「指标评估」焊死在「推送实现」上（还带来
// engine→metrics→notify→engine 注入的间接环风险）。故出口以最小函数类型 AlertSink 注入。
// 依赖口径（§W7-E 起写实，替换原来那句「本包零新增依赖」）：本包对**项目内部包**只多引了两个
// 叶节点——fileutil（原子写）与 opslog（落盘失败可见），二者都不 import 项目内任何其他包
// （已核实，见各自文件头），因此不构成环；仍然不 import store / notify / engine。
// 未注入出口时：首轮评估打一条明确 Warn 说明「已评估、出口未接线」，
// 且每条投递继续落日志兜底——静默丢失正是本批要消灭的东西。
//
// English: §HIGH-3 (narrowed) — adds the missing egress for METRIC-type alert rules only.
// The event-type push leg (breaker / liquidation guard / settlement diff / backup failure via
// internal/notify) already works and is deliberately not re-wrapped here. Push-class rules are
// rate-limited per rule+state with a cooldown window and always emit paired alert/resolved;
// the rest are aggregated into a daily summary flushed when an evaluation tick observes a local
// calendar-day change. The sink is injected as a minimal function type so metrics never imports notify.
// §W7-E: the router's four state groups are journaled to one atomic JSON file under the data dir and
// rehydrated during assembly, so a restart neither swallows yesterday's summary nor leaves a fired
// alert permanently unresolved. metrics then depends only on the leaf packages fileutil / opslog.
package metrics

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"
)

// AlertRoute 一条规则的事件去向。
// English: where a rule's events go.
type AlertRoute string

// 三种去向：必推（带冷却限频）/ 日汇总 / 仅日志（旧行为）。
const (
	RoutePush  AlertRoute = "push"  // 立即出站，受冷却窗限频
	RouteDaily AlertRoute = "daily" // 当天聚合，跨日补发
	RouteLog   AlertRoute = "log"   // 仅日志留痕（不占用推送配额）
)

// 出站消息种类：alert 与 resolved 成对，daily_summary 一天至多一条。
const (
	KindAlert        = "alert"         // 规则触发
	KindResolved     = "resolved"      // 规则恢复（销案）
	KindDailySummary = "daily_summary" // 日汇总
)

// AlertDelivery 一条待出站的投递（已渲染成人可读的标题+正文）。
// Level 沿用规则里的 "p1"/"p2" 字面量，由注入方映射到 notify 的级别（p1→LevelHigh 走既有
// 高优通道，p2→LevelMedium），本包不依赖 notify 的枚举。
// English: one outbound delivery; Level stays the rule's "p1"/"p2" literal so this package does
// not depend on notify's enum.
type AlertDelivery struct {
	Kind      string    // alert | resolved | daily_summary
	Rule      string    // 规则名（日汇总为空）
	Level     string    // p1 | p2
	Title     string    // 单行标题
	Body      string    // 正文（含触发值/聚合明细）
	EventAt   time.Time // 事件时刻（评估时钟）
	FireCount int       // 仅日汇总：当日进汇总的规则条目数
}

// String 投递的单行可读形式（日志兜底用）。
func (d AlertDelivery) String() string {
	if d.Kind == KindDailySummary {
		return fmt.Sprintf("[%s] %s\n%s", d.Kind, d.Title, d.Body)
	}
	return fmt.Sprintf("[%s/%s] %s — %s", d.Kind, d.Level, d.Title, d.Body)
}

// AlertSink 出口的最小注入面（依赖倒置）：一个只吃 AlertDelivery 的函数。
// 将来接线只需一行闭包，例如
//
//	metrics.SetAlertSink(func(d metrics.AlertDelivery) {
//	    lvl := notify.LevelMedium
//	    if d.Level == "p1" { lvl = notify.LevelHigh } // 必推走既有高优通道
//	    notifier.Push(notify.Message{Level: lvl, Title: d.Title, Content: d.Body})
//	})
//
// English: minimal injected egress — a function taking one delivery.
type AlertSink func(d AlertDelivery)

// AlertRoutingConfig 路由表 + 限频参数（可在包内被 ConfigureAlertRouting 覆盖）。
type AlertRoutingConfig struct {
	Routes           map[string]AlertRoute // 规则名 → 去向
	DefaultRoute     AlertRoute            // 未列出的新规则默认去向（默认必推：宁多报不漏报）
	FireCooldown     time.Duration         // 同规则「触发」出站后的冷却窗
	ResolvedCooldown time.Duration         // 同规则「恢复」出站后的冷却窗（超出的挂起补发）
}

// 冷却窗取值理由：评估节拍 30s，触发窗 30 分钟 = 同一条规则持续破线最多 2 条/小时的消息量，
// 既能挡住震荡（flapping）造成的风暴，又不至于让长期故障失去存在感；恢复窗 10 分钟比触发窗
// 短——销案要尽量快地跟上报（超窗的那条会挂起补发，不会丢）。
const (
	defaultFireCooldown     = 30 * time.Minute
	defaultResolvedCooldown = 10 * time.Minute
)

// DefaultAlertRouting 出厂路由表（owner 裁决 4 口径），覆盖 DefaultAlertRules() 全部规则（现 20 条：
// §0925EVE-A2 加入 halt_cancel_failed、§0925EVE-C1 加入实盘战法库闸两条、§0929HB-1/-2/-3
// 加入三条"应有值缺失"型业务心跳、§0929HB-4 加入休市日增量型心跳＝第 19 条、
// §P2-E（波 5）加入 settlement_not_verified＝第 20 条；2026-10-09 波 7 按脚本实数把这段的
// "现 19 条"同步成 20，门禁 §106 那条行为腿文案里的 19 也一并跟上，两侧同批动），
// alert_routing_test 有等值锁（路由条数 == 规则条数），漏一条即判红）。
// English: factory routing table covering all DefaultAlertRules() entries.
func DefaultAlertRouting() AlertRoutingConfig {
	return AlertRoutingConfig{
		Routes: map[string]AlertRoute{
			// —— 必推：此刻正在疼 ——
			"breaker_open":          RoutePush, // 熔断：实盘网关停止下单
			"order_fail_rate":       RoutePush, // 下单链路降级
			"quote_stale":           RoutePush, // 行情新鲜度降级
			"uplink_stale":          RoutePush, // 网关→引擎上行回报停摆（§UPDLINK）
			"sse_broadcast_skipped": RoutePush, // SSE 广播锁超预算丢推送（§UPDLINK 同族）
			// §0929HB-1/-2（2026-09-29 全量审计批 ⑪-4）：两条"缺失型"心跳都属"今天就要处理"——
			// 零固化信号意味着当天决策面空转（收盘再知道已经晚了），已实现盈亏恒 0 意味着
			// 亏损熔断闸当天放水。持续破线由评估器 Firing 态去重 + 30 分钟冷却窗挡刷屏，
			// 写回 0 时成对销案（resolved）。
			"signal_zero_in_session":       RoutePush,
			"realized_pnl_zero_with_sells": RoutePush,
			// §0925EVE-A2（2026-09-25）：kill-switch 撤单失败属"此刻正疼"——紧急停止没撤干净的单
			// 还挂在网关，操作者必须当场知道，不等日汇总。持续破线由评估器 Firing 态去重 +
			// 下面 30 分钟触发冷却窗挡刷屏；下次 HaltAll 全成功写 0 时成对销案（resolved）。
			"halt_cancel_failed": RoutePush,
			// §0925EVE-C1（2026-09-25）：实盘战法库闸两态均属"此刻正疼"——该账号引擎的战法腿此刻
			// 已 fail-close（零新建议），owner 必须当场知道是"库挂了"还是"库空了"。持续破线由
			// 评估器 Firing 态去重 + 30 分钟触发冷却窗挡刷屏；下次装配恢复正常写 0 成对销案。
			"live_strategy_library_not_loaded": RoutePush, // 读库失败腿（区别于下面"库里没规则"腿）
			"live_strategy_no_enabled_rules":   RoutePush, // 零条启用腿
			// —— 日汇总：事件型腿已即时推送，指标面只补「量化留痕」，避免双份 ——
			"settlement_diff": RouteDaily, // 交割单差异（engine/settlement 的 notify 腿已在推）
			"settle_failed":   RouteDaily, // 三方对账失败（同上：10 分钟节流重试自带播报）
			// §P2-E（2026-10-06 修复批 波 5）：对账"本轮未验证"（网关未连接 / 执行器不支持）是
			// **持续性状态**而非瞬时疼——未连接一整天时每 30s 评估都成立，走必推就是每 30 分钟
			// 一条刷满推送；日汇总保证 owner 每天至少看到一次"今天到现在为止没比对成"。
			// 与 trading_calendar_not_loaded / library_stale_days 同一条纪律（§CAL-GATE 判例）。
			// 缺这条路由条目会掉进 DefaultRoute=RoutePush，即"注册了但走错道"——门禁按等值锁盯它。
			"settlement_not_verified": RouteDaily,
			// §CAL-GATE（2026-09-25 D-25-1）：日历未加载属"持续性状态"而非瞬时疼——
			// 若 API 长期不可用（缺 key），恒触发下推通道会变成每 30 分钟一条的刷屏；
			// 日汇总既保证 owner 每天看到一次"现在处于 fail-open"，又不淹没其它必推。
			"trading_calendar_not_loaded": RouteDaily,
			// —— 日汇总：趋势型/容量型，单条不疼、反复才疼 ——
			"llm_cooldown":   RouteDaily, // LLM 冷却数
			"buy_queue_high": RouteDaily, // 买入队列积压
			// §0929HB-3（2026-09-29 全量审计批 ⑪-4）：战法库陈旧是"库多久没动过"的天尺度状态，
			// 破线后每 30s 评估都会持续成立——走必推会每 30 分钟一条刷满推送；
			// 日汇总保证 owner 每天看到一次"夜间研究链没产出"，与 trading_calendar_not_loaded
			// 同一条纪律（§CAL-GATE 的判例）。
			"library_stale_days": RouteDaily,
			// §0929HB-4（2026-09-29 晚批）：休市日仍有当日新固化信号。与 HB-3 同一条纪律——
			// 这是**持续状态**而非瞬时疼：日历 fail-open 时整个休市日每 30s 都成立，走必推就是
			// 每 30 分钟一条刷满长假。而且休市日网关不下单，异常不会当场变成钱损，
			// 属于"当天要知道、不必当天几点都知道"。与 trading_calendar_not_loaded 同路由。
			"signal_pinned_on_closed_day": RouteDaily,
			// —— 必推：§ADJ-BASIS-2 战法参数基线失效（不推就是"owner 永远不知道这批权重的历史依据没了"）——
			"applied_factor_stale_basis":  RoutePush,
			"applied_pattern_stale_basis": RoutePush, // §ADJ-BASIS-2P 形态侧同规
		},
		DefaultRoute:     RoutePush,
		FireCooldown:     defaultFireCooldown,
		ResolvedCooldown: defaultResolvedCooldown,
	}
}

// ConfigureAlertRouting 幂等地替换路由表/限频参数（传零值字段=保留当前值）。
// 目前**没有**外部接线点：表在包内自持，改配置走这里或以后由 config 层注入。
// English: idempotently overrides the routing table / cooldown windows (zero fields kept as-is).
func ConfigureAlertRouting(cfg AlertRoutingConfig) {
	globalAlertRouter.mu.Lock()
	defer globalAlertRouter.mu.Unlock()
	if len(cfg.Routes) > 0 {
		globalAlertRouter.cfg.Routes = cfg.Routes
	}
	if cfg.DefaultRoute != "" {
		globalAlertRouter.cfg.DefaultRoute = cfg.DefaultRoute
	}
	if cfg.FireCooldown > 0 {
		globalAlertRouter.cfg.FireCooldown = cfg.FireCooldown
	}
	if cfg.ResolvedCooldown > 0 {
		globalAlertRouter.cfg.ResolvedCooldown = cfg.ResolvedCooldown
	}
}

// SetAlertSink 注入出口（幂等；传 nil = 退回「仅日志」并把 Warn 标记复位，便于自测重放）。
// English: injects the alert egress (idempotent; nil falls back to log-only and re-arms the warn).
func SetAlertSink(s AlertSink) { alertOutput.setSink(s) }

// AlertSinkInjected 出口是否已接线（供健康检查/verify 只读探测）。
// English: reports whether an egress has been injected.
func AlertSinkInjected() bool { return alertOutput.hasSink() }

// dailyStat 一条规则当日的聚合统计（触发次数 / 峰值 / 首次 / 末次 / 当前是否仍在触发）。
type dailyStat struct {
	Level       string
	Message     string
	Count       int
	Peak        float64
	First       time.Time
	Last        time.Time
	StillFiring bool
}

// alertRouter 出站路由器：事件 → 去向 + 限频 + 日汇总 + alert/resolved 配对。
// now 可注入，单测不 sleep。
type alertRouter struct {
	mu sync.Mutex
	// cfg 路由表与冷却窗（ConfigureAlertRouting 可覆盖）。
	cfg        AlertRoutingConfig
	now        func() time.Time
	rules      map[string]AlertRule  // 规则名 → 规则快照（渲染正文/取文案用）
	lastF      map[string]time.Time  // 规则名 → 上次 alert 出站时刻
	lastR      map[string]time.Time  // 规则名 → 上次 resolved 出站时刻
	announced  map[string]bool       // 规则名 → 已报未销（决定是否需要发 resolved）
	pendingR   map[string]AlertEvent // 被恢复冷却窗挡下、待补发的 resolved
	suppressed map[string]int        // 冷却窗内被抑制的重复触发计数（诊断风暴抑制量）
	day        string                // 当前聚合的本地日历日
	daily      map[string]*dailyStat
	// §W7-E（2026-10-06 修复批 波 7）跨重启持久化：上面四份状态（day/daily + pendingR +
	// announced，另加 lastF/lastR 冷却锚与 suppressed 诊断计数）此前只活在内存里，
	// 重启即丢 ⇒ 前一日汇总被吞、已报未销的规则永远补不出销案。落盘实现见 alert_router_state.go。
	statePath      string     // 非空=启用落盘（由 SetAlertRouterStatePath 在装配期注入）
	saveMu         sync.Mutex // 落盘串行闸：多引擎并存时同一份 journal 只有一个写者在前
	lastWritten    []byte     // 上次成功落盘的字节（内容未变即跳过写盘，非脏标记）
	persistFailSeq int        // 连续落盘失败计数（日志节流，成功即清零）
}

// newAlertRouter 构造路由器（now 为 nil 时用 time.Now）。
func newAlertRouter(now func() time.Time) *alertRouter {
	if now == nil {
		now = time.Now
	}
	return &alertRouter{
		cfg:        DefaultAlertRouting(),
		now:        now,
		rules:      map[string]AlertRule{},
		lastF:      map[string]time.Time{},
		lastR:      map[string]time.Time{},
		announced:  map[string]bool{},
		pendingR:   map[string]AlertEvent{},
		suppressed: map[string]int{},
		daily:      map[string]*dailyStat{},
	}
}

// Route 对一轮评估事件做路由，返回本轮真正出站的投递列表（可能含跨日补发的汇总）。
// 即使 events 为空也必须每轮调用：被冷却挂起的 resolved 和跨日汇总都靠这个节拍放行。
// §W7-E：每轮结尾把整份状态快照落盘（启用路径时）——放在这里而不是调用方，是为了让
// 「改过状态就必须落盘」这条不变量由唯一的状态变更入口保证，调用方无从遗漏。
// English: routes one evaluation round; must be called every tick even with no events, since
// cooldown-deferred resolutions and the day-rollover summary are released on this cadence.
// §W7-E also persists the whole state snapshot at the end of every round (when a path is set).
func (r *alertRouter) Route(events []AlertEvent) []AlertDelivery {
	r.mu.Lock()
	out := r.routeEventsLocked(events)
	path := r.statePath
	snap := r.snapshotLocked()
	r.mu.Unlock()
	// 文件 IO 一律在 mu 之外（见 writeState 的 saveMu 说明）：Route 的 mu 还被
	// ConfigureAlertRouting 与启动 rehydrate 共用，占着它写盘等于让路由表热更新排队等磁盘。
	if path != "" {
		r.writeState(path, snap)
	}
	return out
}

// routeEventsLocked 一轮路由的实体（调用方须持 mu）：日切 → 逐事件路由 → 放行悬置销案。
func (r *alertRouter) routeEventsLocked(events []AlertEvent) []AlertDelivery {
	now := r.now()
	var out []AlertDelivery
	// 先处理日切：跨日的上一日汇总排在最前，语义上属于"昨天"。
	if d := r.rollDayLocked(now); d != nil {
		out = append(out, *d)
	}
	for _, e := range events {
		r.rememberRuleLocked(e)
		switch r.routeOfLocked(e.Name) {
		case RoutePush:
			if d := r.routePushLocked(e, now); d != nil {
				out = append(out, *d)
			}
		case RouteDaily:
			r.recordDailyLocked(e, now)
		default: // RouteLog：交给调用方的兜底日志，不产生投递
		}
	}
	// 悬置的恢复：冷却到期且规则确实已恢复正常 → 补发（保证成对销案）。
	out = append(out, r.flushPendingLocked(now)...)
	return out
}

// routePushLocked 必推路径的限频：同规则 alert 窗内重复破线只计数不发消息。
func (r *alertRouter) routePushLocked(e AlertEvent, now time.Time) *AlertDelivery {
	key := e.Name
	if e.Kind == "recover" {
		// 只有"报过且未销"的规则才需要销案；未报（从未推 / 已被抑制）不发孤立 resolved。
		if !r.announced[key] {
			return nil
		}
		if last, ok := r.lastR[key]; ok && now.Sub(last) < r.cfg.ResolvedCooldown {
			r.pendingR[key] = e // 挂起，到期补发
			return nil
		}
		delete(r.pendingR, key)
		r.announced[key] = false
		r.lastR[key] = now
		return &AlertDelivery{Kind: KindResolved, Rule: key, Level: e.Level,
			Title:   "【指标告警恢复】" + e.Message,
			Body:    fmt.Sprintf("规则 %s 恢复正常，当前值 %.2f（销案）", key, e.Value),
			EventAt: now}
	}
	if last, ok := r.lastF[key]; ok && now.Sub(last) < r.cfg.FireCooldown {
		// 冷却窗内重复破线：§C9 式抑制——只累计，不刷第二条。
		r.suppressed[key]++
		return nil
	}
	r.lastF[key] = now
	r.announced[key] = true
	delete(r.pendingR, key) // 又坏了：上一轮挂起的销案作废（问题未真的解决）
	rule := r.rules[key]
	return &AlertDelivery{Kind: KindAlert, Rule: key, Level: e.Level,
		Title: fmt.Sprintf("【指标告警/%s】%s", strings.ToUpper(e.Level), e.Message),
		Body: fmt.Sprintf("规则 %s 触发：metric=%s 当前值 %.2f，阈值 %s%g，持续要求 %s",
			key, rule.Metric, e.Value, opText(rule.Op), rule.Threshold, orNone(rule.For)),
		EventAt: now}
}

// flushPendingLocked 放行已过恢复冷却窗的悬置销案。
func (r *alertRouter) flushPendingLocked(now time.Time) []AlertDelivery {
	if len(r.pendingR) == 0 {
		return nil
	}
	var out []AlertDelivery
	for name, e := range r.pendingR {
		last, ok := r.lastR[name]
		if ok && now.Sub(last) < r.cfg.ResolvedCooldown {
			continue
		}
		delete(r.pendingR, name)
		r.announced[name] = false
		r.lastR[name] = now
		out = append(out, AlertDelivery{Kind: KindResolved, Rule: name, Level: e.Level,
			Title:   "【指标告警恢复】" + e.Message,
			Body:    fmt.Sprintf("规则 %s 恢复正常（冷却窗后补发的销案）", name),
			EventAt: now})
	}
	return out
}

// recordDailyLocked 日汇总聚合：计数 + 峰值 + 首次/末次 + 当前态。
func (r *alertRouter) recordDailyLocked(e AlertEvent, now time.Time) {
	st := r.daily[e.Name]
	if st == nil {
		st = &dailyStat{Level: e.Level, Message: e.Message, Peak: e.Value}
		r.daily[e.Name] = st
	}
	if e.Kind == "recover" {
		st.StillFiring = false
		st.Last = now
		return
	}
	st.Count++
	if e.Value > st.Peak {
		st.Peak = e.Value
	}
	if st.First.IsZero() {
		st.First = now
	}
	st.Last = now
	st.StillFiring = true
}

// rollDayLocked 检测本地日历日变更：换日时产出**前一日**的汇总并清空聚合桶。
// 空桶（整天没有任何日汇总类规则触发）不发，避免无意义噪声。
// English: on a local calendar-day change, emit the PREVIOUS day's summary (empty days stay silent).
func (r *alertRouter) rollDayLocked(now time.Time) *AlertDelivery {
	cur := now.Format("2006-01-02")
	if r.day == "" {
		r.day = cur
		return nil
	}
	if cur == r.day {
		return nil
	}
	prev := r.day
	count := 0
	for _, st := range r.daily { // 先取总量，再渲染（渲染会读同一批桶）
		count += st.Count
	}
	lines := r.renderDailyLocked()
	r.daily = map[string]*dailyStat{}
	r.day = cur
	if len(lines) == 0 {
		return nil
	}
	return &AlertDelivery{Kind: KindDailySummary, Level: "p2",
		Title:     "【指标告警日汇总】" + prev,
		Body:      strings.Join(lines, "\n"),
		EventAt:   now,
		FireCount: count} // 当日进汇总的规则触发总次数
}

// renderDailyLocked 把当日聚合桶渲染成按规则名排序的明细行。
func (r *alertRouter) renderDailyLocked() []string {
	names := make([]string, 0, len(r.daily))
	for n := range r.daily {
		names = append(names, n)
	}
	sort.Strings(names)
	var lines []string
	for _, n := range names {
		st := r.daily[n]
		line := fmt.Sprintf("- %s（%s）触发 %d 次，峰值 %.2f，首次 %s，末次 %s",
			n, st.Level, st.Count, st.Peak,
			st.First.Format("15:04:05"), st.Last.Format("15:04:05"))
		if st.StillFiring {
			line += "，截至汇总时刻仍在触发"
		}
		if msg := strings.TrimSpace(st.Message); msg != "" {
			line += "｜" + msg
		}
		lines = append(lines, line)
	}
	return lines
}

// routeOfLocked 查规则去向；未列出的规则走 DefaultRoute（默认必推）。
func (r *alertRouter) routeOfLocked(name string) AlertRoute {
	if rt, ok := r.cfg.Routes[name]; ok {
		return rt
	}
	if r.cfg.DefaultRoute != "" {
		return r.cfg.DefaultRoute
	}
	return RoutePush
}

// rememberRuleLocked 缓存本轮事件对应的规则快照（AlertEvent 只带 Name/Message）。
func (r *alertRouter) rememberRuleLocked(e AlertEvent) {
	if _, ok := r.rules[e.Name]; ok {
		return
	}
	for _, rule := range DefaultAlertRules() { // 9 条，线性查一次即可
		if rule.Name == e.Name {
			r.rules[e.Name] = rule
			return
		}
	}
	r.rules[e.Name] = AlertRule{Name: e.Name, Message: e.Message, Level: e.Level}
}

// alertDispatcher 出站分发器：持有注入的 sink，并保证「未接线」这件事本身是响的。
type alertDispatcher struct {
	mu     sync.Mutex
	sink   AlertSink
	warned bool // Warn 只在首轮打一次（之后每条投递仍有兜底日志，不会静默）
}

var alertOutput = &alertDispatcher{}

// setSink 注入/清空出口。
func (p *alertDispatcher) setSink(s AlertSink) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sink = s
	if s == nil {
		p.warned = false // 复位，便于测试重放与运行期重新接线后再掉线时再报一次
	}
}

// hasSink 是否已接线。
func (p *alertDispatcher) hasSink() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sink != nil
}

// emit 送出一条投递。sink 为 nil 时不是"丢掉",而是：首轮一条明确 Warn + 每条兜底日志——
// 本批要消灭的正是「有评估、无出口、且没人知道」这种静默失效形态。
// English: emits one delivery; with no sink injected it logs a one-time explicit warning plus a
// fallback line per delivery, so events can never be dropped silently.
func (p *alertDispatcher) emit(d AlertDelivery) {
	p.mu.Lock()
	sink := p.sink
	warn := sink == nil && !p.warned // 只在"真的没接线"时报警，接了线就闭嘴
	if warn {
		p.warned = true
	}
	p.mu.Unlock()
	if warn {
		log.Printf("[metrics][WARN] §高-3 指标告警已评估但出口未接线：AlertSink 尚未注入，" +
			"必推类规则（熔断/降级）与日汇总现在只会落日志、不会推送。" +
			"请在启动装配处调用 metrics.SetAlertSink(...) 接到 notify（p1→LevelHigh 高优通道）")
	}
	log.Printf("[metrics] 告警出站 kind=%s rule=%s %s", d.Kind, d.Rule, strings.ReplaceAll(d.Body, "\n", " | "))
	if sink != nil {
		sink(d)
	}
}

// 包级单例：路由器随 globalAlerter 一样进程级共享（评估节拍由 engine 的 30s 节流驱动）。
var globalAlertRouter = newAlertRouter(nil)

// opText 比较符的人读文案。
func opText(op string) string {
	switch op {
	case "gt":
		return ">"
	case "ge":
		return ">="
	case "lt":
		return "<"
	case "le":
		return "<="
	}
	return op
}

// orNone 空字符串显示为 "-"（for=空=立即）。
func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}
