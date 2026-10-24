// Package config 提供配置管理：加载/保存 JSON 配置文件，支持策略、风控、板块、LLM 等配置。
// 本文件定义了量化交易系统所需的全部配置结构体，包括：
//   - 情绪周期阶段阈值（冰点/启动/发酵/高潮/背离/退潮）
//   - 四大战法（龙头/双响炮/N形/龙回头）的独立参数
//   - D1 事件评分规则集
//   - 风控/止损/仓位管理参数
//   - 模拟盘/实盘 QMT 交易配置
//   - 运行时内存治理/数据源/调度器/通知推送等
//
// 顶层 Rules 结构体聚合所有配置，Manager 负责加载/保存/按账号隔离。
//
// 本包职责概览：
//   - 配置加载：Load 从 JSON 文件读取 rules/d1 两段（文件缺失/损坏时保留内存现状）。
//   - 配置保存：Save 以原子写（fsync+唯一临时名）落盘，quant 与 researchd 双进程共享同一文件。
//   - 配置热更新：Watch 轮询文件内容变化（sha256 比对，默认 30s）自动重载，免重启生效。
//   - 多账号隔离：Manager 借助 KVStore（auth.Manager 实现）为每个账号保存独立的 Rules 快照，
//     读走 userRules（账号覆盖→系统级""键兼容回退→全局副本），写走各 SetXxxConfigFor 落库。
//   - 运营数据归属：SetOperatorID 注入管理员 ID，运营配置（策略/LLM/D1/模拟盘等）系统级
//     共享归属管理员；逐账号独享配置（QMT 实盘）走 GetQMTConfigFor/SetQMTConfigFor 按 ID 隔离。
//   - 模拟盘配置更新：SetPaperStrategyFor 只改战法白名单+个股黑名单（§SIGNAL_CONTROLLER），
//     SetPaperConfigFor（§F-4 20260917 缺陷修复批）按回调局部更新其余 paper 字段
//     （总开关/自动卖出/资金规模/做空侧等），两者语义分离、互不踩踏。
//   - 出厂默认：DefaultRules/DefaultSchedulerConfig/DefaultQMTConfig/DefaultDisciplineConfig
//     提供零配置可运行的合理默认值；LoadSchedulerConfig 供独立研究服务按键增量解析。
package config

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"quant-trading-v2/internal/fileutil"
)

// LaodengConfig Laodeng 评分系统配置。
// （LaodengConfig is the configuration for the Laodeng scoring system.）
type LaodengConfig struct {
	// 是否启用 Laodeng 评分修正
	Enabled bool `json:"enabled"`
	// 最低流通市值（亿）
	MarketCapMin float64 `json:"market_cap_min"`
	// 最大市盈率阈值
	PeMax float64 `json:"pe_max"`
	// 最低换手率
	TurnoverMin float64 `json:"turnover_min"`
	// 技术面扣分系数
	TechPenalty float64 `json:"tech_penalty"`
	// 评分权重
	WeightScore float64 `json:"weight_score"`
}

// Rules 顶层规则配置，包含情绪周期、策略、板块、风控等完整配置。
// （Rules is the top-level rules config aggregating emotion cycle, strategy, sector, risk control, etc.）
type Rules struct {
	// 情绪周期阶段阈值
	Emotion EmotionConfig `json:"emotion_cycle"`
	// 各策略参数
	Strategy StrategyConfig `json:"strategy"`
	// Laodeng 评分
	Laodeng LaodengConfig `json:"laodeng"`
	// 主线板块配置
	MainSector MainSectorConfig `json:"main_sector"`
	// LLM 客户端配置
	LLM LLMConfig `json:"llm"`
	// 主题/黑名单配置
	Theme ThemeConfig `json:"theme"`
	// 风控参数
	RiskCtrl RiskCtrlConfig `json:"risk_ctrl"`
	// 仓位管理参数
	Position PositionConfig `json:"position"`
	// 通知推送参数
	Notify NotifyConfig `json:"notify"`
	// 研究调度器配置（quant-research 服务读取）
	Scheduler SchedulerConfig `json:"scheduler"`
	// 模拟盘/纸面交易配置
	Paper PaperConfig `json:"paper"`
	// 东莞证券 MiniQMT 实盘交易配置
	QMT QMTConfig `json:"qmt"`
	// AppRelease §APPVER 2026-09-22 C批：APK 服务端驱动强制更新发布单（公开端点
	// GET /api/app/version 的唯一数据源）。零值=未发布（min=0 时客户端不拦，行为与无此
	// 配置完全一致）；强制更新自 versionCode≥2 的 APK 起生效（v1 包不发版本头、无法自报
	// 版本，只能由 owner 重装过渡）。供 APK 原生壳在**登录前**拉取做更新检查，故该端点
	// 必须免鉴权（见 server.registerRoutes 注释）。
	// English: §APPVER APK release manifest backing the public GET /api/app/version endpoint.
	// Zero value = nothing published (min=0 → client never blocks); enforcement only applies from
	// versionCode 2 upward; the endpoint stays auth-free because the native shell checks before login.
	AppRelease AppReleaseConfig `json:"app_release,omitempty"`
	// 运行时内存治理配置
	Runtime RuntimeConfig `json:"runtime"`
	// 数据源配置（§HITHINK_DATA_SOURCE_PLAN）
	Data DataConfig `json:"data"`
	// 宏观日历补充事件（§R3-8 P1-J 接线：此前类型定义存在但从未挂到 Rules）
	Calendar CalendarConfig `json:"calendar"`
	// 信号与战法增强开关组（§SIGNAL_EDGE_ENHANCEMENT_PLAN）：全部默认关闭，
	// 关闭时对应逻辑零行为变化（零值 = 全禁用，向后兼容存量配置）。
	// English: signal & tactic enhancement toggles (see SIGNAL_EDGE_ENHANCEMENT_PLAN).
	// All default off; zero value = all disabled, backward compatible with existing configs.
	Enhance EnhanceConfig `json:"enhance"`
	// 信号控制器灰度配置（§SIGNAL_CONTROLLER_PLAN_20260917）：准入/持续性监测统一组件的
	// 新行为观察期开关。战法白名单不受影响（始终硬拦），仅黑名单补齐类新行为可灰度。
	// English: signal-controller rollout knobs — shadow-observation for newly enforced gates
	// (blacklists on the signal side); strategy whitelist is always hard, unaffected.
	SignalCtl SignalCtlConfig `json:"signal_ctl"`
	// §ADJ-BASIS-2 研究产物（已应用因子战法）配置：复权口径基线失效时的处置策略。
	// English: research-product knobs — what to do with applied factor strategies whose fitted
	// parameters were derived on a now-invalid price-adjustment basis.
	Research ResearchConfig `json:"research"`
	// §DYN-SIZE 动态仓位合成配置（docs/DYN_SIZE_FINAL_PLAN_20261016.md）：把"每笔买多少"从
	// 固定金额改成由「个股信号 × 宏观事件 × 大盘情绪 × 节假日 × 个股波动率」合成的动态权重决定。
	// 全部默认安全：Enabled=false 时 signalctl 零行为变更（只走 shadow 读数）。
	// English: §DYN-SIZE dynamic position-sizing config. Safe by default — Enabled=false keeps every
	// admission verdict unchanged (shadow readouts only).
	Sizing SizingConfig `json:"sizing"`
	// Concept §C1-V2 概念库每日同步与敞口开关（独立于 SizingConfig，避免 0 被 WithDefaults 补默认）。
	// English: §C1-V2 concept-library sync/exposure switches, independent of SizingConfig.
	Concept ConceptConfig `json:"concept"`
}

// ConceptConfig §C1-V2 概念库同步与自动接线。
// 默认关闭；覆盖率达阈值且数据未过期后，由后台任务或 admin 显式开启。
// English: §C1-V2 concept sync; off by default, enabled only after coverage/staleness checks.
type ConceptConfig struct {
	Enabled           bool    `json:"enabled"`
	Shadow            bool    `json:"shadow"`
	MinCoveragePct    float64 `json:"min_coverage_pct"`
	MaxStalenessHours int     `json:"max_staleness_hours"`
}

// MinCoverage §C1-V2 覆盖率阈值（<=0 回退 95%）。
// English: coverage threshold (<=0 → 95%).
func (c ConceptConfig) MinCoverage() float64 {
	if c.MinCoveragePct <= 0 {
		return 95
	}
	return c.MinCoveragePct
}

// MaxStaleness §C1-V2 数据最大陈旧小时数（<=0 回退 48h）。
// English: max data staleness (<=0 → 48h).
func (c ConceptConfig) MaxStaleness() int {
	if c.MaxStalenessHours <= 0 {
		return 48
	}
	return c.MaxStalenessHours
}

// ResearchConfig §ADJ-BASIS-2（2026-09-23）研究战法库相关配置。目前只有一项：
// 复权口径基线失效后的处置动作。
// English: research-library config; today only the stale-adjustment-basis action.
type ResearchConfig struct {
	// StaleAdjBasisActionConfig 已应用因子战法的复权口径基线失效时怎么办（JSON 键 stale_adj_basis_action）：
	//   - "shadow"（缺省：nil / 空串 / 未知值都落这里）= 只标记 + 告警，照旧参与实盘；
	//   - "disable" =  fail-close，把基线失效的战法从 enabled 集合里剔除（不再产生新买入信号）。
	// 为什么默认是 shadow（**这里刻意不做资本行为变更**）：§ADJ 修复后重算实测，fac_1 的四个
	// 分量里三个的分层能力从 3~4pp 塌到 ≈0 甚至反号（STOA −2.609%→+0.372%），也就是说它的
	// weights/buy_threshold 已经没有成立的历史依据——但"停掉一条正在跑的战法"是直接改资金行为，
	// 必须由 owner 看过重跑结果后拍板，不能由一次口径修复顺带替 owner 决定。故本仓只把失效
	// 状态显性化（战法库红标 + p1 告警），处置权留在 owner 手里。
	// English: "shadow" (default for nil/empty/unknown) keeps trading and only marks+alerts; "disable"
	// fail-closes stale entries out of the enabled set. Default stays shadow on purpose: dropping a
	// live strategy changes capital behaviour and is the owner's call, not a side effect of a data fix.
	// 用 *string 而非 string：让"配置里没有这一项"与"显式写了 shadow"可区分（本仓 *bool/*string
	// 可选位的既有约定，见 SignalCtlConfig.ShadowBlacklist）。
	// 字段名带 Config 后缀：归一化取值走方法 StaleAdjBasisOrDefault()（Go 不允许字段与方法重名，
	// 2026-09-23 修编译冲突时定的）；JSON 键仍是 stale_adj_basis_action，配置零变化。
	// English: the raw pointer field is *Config-suffixed because the normalized accessor keeps the plain
	// concept name; the JSON key (and thus every existing config file) is unchanged.
	StaleAdjBasisActionConfig *string `json:"stale_adj_basis_action,omitempty"`
}

// StaleAdjBasisOrDefault 归一化 rules.research.stale_adj_basis_action 的取值。
// 任何无法识别的值（含未配置）都回退 "shadow"——未知值绝不能变成停战法。
// 字面量与 internal/research 的 StaleAdjBasisDisable/StaleAdjBasisShadow 保持一致。
// English: normalizes the config value; unknown/absent always falls back to "shadow" (an unknown
// string must never be interpreted as "stop trading").
func (r ResearchConfig) StaleAdjBasisOrDefault() string {
	if r.StaleAdjBasisActionConfig == nil {
		return "shadow"
	}
	switch strings.TrimSpace(*r.StaleAdjBasisActionConfig) {
	case "disable":
		return "disable"
	case "shadow":
		return "shadow"
	default:
		return "shadow"
	}
}

// AppReleaseConfig §APPVER 2026-09-22 C批：APK 版本发布单，经公开端点
// GET /api/app/version 暴露给原生壳做登录前强制更新检查。
// （§APPVER APK release manifest, served by the public pre-auth version endpoint.）
type AppReleaseConfig struct {
	// MinVersionCode 最低可接受 versionCode：客户端 BuildConfig.VERSION_CODE 低于此值即弹
	// 不可取消的强制更新框。0=未发布/不拦（默认，旧配置零行为变化）。
	// 强制更新自 versionCode≥2 的 APK 起生效（v1 无版本头，服务端无法远程识别）。
	// Minimum acceptable versionCode; 0 = nothing enforced (unpublished default).
	MinVersionCode int `json:"min_version_code"`
	// LatestVersionCode 当前最新发布 versionCode（客户端展示"最新版"用，不参与拦截判定）。
	// Latest published versionCode (informational; never blocks by itself).
	LatestVersionCode int `json:"latest_version_code"`
	// ApkURL 新版 APK 下载地址（如 https://…/dl/quant-latest.apk）；空则客户端不显示下载按钮。
	// Download URL of the latest APK; empty hides the download action in the update dialog.
	ApkURL string `json:"apk_url"`
	// Note 更新说明（弹窗文案，如"安全更新：登录凭据迁入系统级加密存储，请升级"）。
	// Release note shown verbatim in the forced-update dialog.
	Note string `json:"note"`
}

// SignalCtlConfig 信号控制器灰度配置（§SIGNAL_CONTROLLER_PLAN_20260917 §八）。
// （Rollout knobs for the unified signal controller.）
type SignalCtlConfig struct {
	// ShadowBlacklist 黑名单影子模式（nil/true=默认影子）：个股/板块黑名单在信号控制器的命中
	// 仅留痕观察不拦截——黑名单此前只在实盘下单侧消费，补齐到两通道属新行为，按方案要求
	// 影子期验证后再切正式。战法白名单/确认窗不受本标志影响。
	// 置 false 后控制器与风控闸的黑名单命中立即生效（重启/热更下一轮即达）。
	// English: nil/true = blacklist hits on the signal side are recorded but not enforced (new
	// behavior under observation per the plan); strategy whitelist & confirm windows are unaffected.
	ShadowBlacklist *bool `json:"shadow_blacklist,omitempty"`
}

// BlacklistShadow 黑名单影子标志（nil 默认开=只留痕不拦）。
// （Reports whether blacklist enforcement is still in shadow mode; nil defaults to shadow-on.）
func (s SignalCtlConfig) BlacklistShadow() bool {
	return s.ShadowBlacklist == nil || *s.ShadowBlacklist
}

// RuntimeConfig 运行时内存治理配置：盘后释放常驻服务内存，避免与夜间研究作业叠加触发 OOM。
// 服务器物理内存仅 1.6GiB：quant 常驻服务盘后仅需展示数据快照（无需跑全量性能），
// 主动把 Go 堆/缓存归还 OS，把物理内存让给盘后 research 作业。
// English: runtime memory-governance config — releases the resident engine's memory after hours so it
// doesn't stack with the nightly research job on the 1.6GiB box. After hours the engine only serves
// data snapshots (no heavy work), so the Go heap/cache is returned to the OS for research to use.
type RuntimeConfig struct {
	// TrimAfterHours 盘后内存释放总开关（默认 true）：非活跃时段（盘后/休市）定时
	// runtime.GC()+debug.FreeOSMemory() 把堆归还 OS；盘中不触发，不影响性能。
	// English: after-hours memory-trim master switch (default true): outside active sessions the engine
	// periodically runs runtime.GC()+debug.FreeOSMemory() to return the heap to the OS; never during
	// trading hours, so live performance is unaffected.
	// 盘后内存释放总开关
	TrimAfterHours bool `json:"trim_after_hours"`
	// TrimIntervalMin 盘后释放节流间隔（分钟，默认 15）。
	// English: after-hours trim throttle interval in minutes (default 15).
	// 盘后释放节流间隔（分钟）
	TrimIntervalMin int `json:"trim_interval_min"`
	// FeedIntervalSec 行情快照刷新间隔（秒，默认 0 → 回退 5s）。
	// 降低可缩短"行情变化→信号检测"的感知延迟（信号→交易链路优化 A+B 之 B 快速执行器）。
	// 注意：过低会加大上游行情源请求频率与 CPU 占用，生产需结合服务器资源验证后再启用。
	// English: quote-snapshot refresh interval in seconds (0 → fallback 5s). Lowering it shortens the
	// market-change → signal-detection latency (signal→trade optimization A+B / B: fast executor).
	// Too-low values raise upstream request rate & CPU; validate against server resources in prod.
	FeedIntervalSec int `json:"feed_interval_sec"`
	// QMTFeedEnabled §ENH-5 批E：QMT Level-1 全推行情 feed 总开关（默认 false）。
	// 开启且 qmt.gateway_url 非空时，引擎按 qmt_feed_interval_sec 轮询网关 /quotes，
	// 命中代码覆盖行情快照（Source=QMT-L1）；feed 故障静默回退既有 5s 新浪链，不参与交易熔断。
	// 仅生产决策机（Windows + qmt_gateway + xtquant）有意义，mac 开发态保持关闭。
	// English: §ENH-5 master switch for the QMT Level-1 quote feed (default off, prod-only).
	QMTFeedEnabled bool `json:"qmt_feed_enabled"`
	// QMTFeedIntervalSec L1 feed 轮询间隔（秒，默认 0 → 取 3s；建议 1~3）。
	// English: L1 feed polling interval in seconds (0 → 3s default; 1-3 recommended).
	QMTFeedIntervalSec int `json:"qmt_feed_interval_sec"`
	// ScoringIntervalSec 近实时 8a/8b 打分循环间隔（秒，默认 0 → 回退 5s）。
	// 降低可让战法信号翻转更快被检出并触发下单（信号→交易链路优化 A+B 之 B）。
	// English: near-realtime 8a/8b scoring-loop interval in seconds (0 → fallback 5s). Lowering it
	// detects strategy signal flips (and fires orders) sooner (signal→trade optimization A+B / B).
	ScoringIntervalSec int `json:"scoring_interval_sec"`
	// ReviewEnabled §DAILY_REVIEW 盘后持仓综合复盘总开关（默认开）：交易日收盘后对
	// "自选 ∪ 实盘持仓 ∪ 模拟盘持仓 ∪ 当日信号股"逐票算量化事实（量能/MACD/量价/均线/位置）
	// 交 LLM 生成综合复盘正文 + 后市倾向，写入消息中心（每日一份、按日去重覆盖）。
	// 需配置 LLM Key 才实际调用；无 Key 时静默跳过。显式 false 关闭盘后复盘。
	// English: §DAILY_REVIEW per-account after-hours LLM position review. Once per trading day after
	// close it reviews watchlist ∪ real held ∪ paper held ∪ today's signals with a merged LLM call
	// (facts computed in Go: volume/MACD/price-volume/MA/position) into a review card per stock in the
	// message center. No-ops when no LLM key is configured.
	ReviewEnabled *bool `json:"review_enabled,omitempty"`
	// ReviewMaxStocks §DAILY_REVIEW 单次复盘最多覆盖的股票数（默认 24，上限 50，<=0 用默认）：
	// 持仓/信号/自选去重后按优先级（实盘>模拟>信号>自选）截断，约束单次合并 LLM 调用的上下文规模。
	// English: max distinct stocks per review run (default 24, capped 50); truncates the deduped universe
	// by priority real>paper>signal>watchlist to bound the merged LLM prompt size.
	ReviewMaxStocks int `json:"review_max_stocks"`
}

// ReviewOn §DAILY_REVIEW 盘后复盘是否启用（*bool 语义：未配置=默认开启，显式 false 关闭）。
// English: §DAILY_REVIEW switch — nil (absent in config) means ON; explicit false disables.
func (r RuntimeConfig) ReviewOn() bool { return r.ReviewEnabled == nil || *r.ReviewEnabled }

// ReviewMax §DAILY_REVIEW 单次复盘股票数上限（<=0 回退默认 24，硬上限 50 护栏）。
// English: review universe cap per run; <=0 → default 24, hard-capped at 50.
func (r RuntimeConfig) ReviewMax() int {
	if r.ReviewMaxStocks <= 0 {
		return 24
	}
	if r.ReviewMaxStocks > 50 {
		return 50
	}
	return r.ReviewMaxStocks
}

// EnhanceConfig 信号与战法增强开关组（§SIGNAL_EDGE_ENHANCEMENT_PLAN_20260909 §7）。
// 全部默认关闭；开启后才注入对应信号/战术逻辑，关闭路径零行为变化。
// English: signal & tactic enhancement toggles. All default off; enabled toggles inject the
// corresponding signal/tactic logic, disabled paths keep zero behavior change.
type EnhanceConfig struct {
	// NewsDecay 新闻时效衰减（P1.1）：事件按类型半衰期随年龄降权。
	NewsDecay bool `json:"news_decay_enabled"`
	// NewsDecayHalfLifeMin 半衰期覆盖（分钟，0=用类型默认表）。
	NewsDecayHalfLifeMin int `json:"news_decay_half_life_min"`
	// AuctionSignal 竞价信号（P1.2）：竞价强度分进打分池预排名/开盘确认。
	AuctionSignal bool `json:"auction_signal_enabled"`
	// FactorDedup 因子相关度去重（P1.3）。
	FactorDedup bool `json:"factor_dedup_enabled"`
	// NewsImpact 新闻影响率模型（P2.1）。
	NewsImpact bool `json:"news_impact_enabled"`
	// SectorLinkage 板块联动交易（P2.2）。
	SectorLinkage bool `json:"sector_linkage_enabled"`
	// MarketState 市场状态机（P2.3）。
	MarketState bool `json:"market_state_enabled"`
	// OrderFlow 盘口微观结构（P2.4）。
	OrderFlow bool `json:"orderflow_enabled"`
	// DynWeight 信号质量动态权重（P2.5）。
	DynWeight bool `json:"dynweight_enabled"`
	// T0 底仓 T+0（P3）。
	T0 bool `json:"t0_enabled"`
	// RiskPoolShadow §C6 跌停池/炸板池影子读数（nil/true=默认只读影子）：只记录家数与
	// “若纳入风险合成是否会改档”，不改变 RiskTier。缺失数据弃权，不把取不到当 0。
	// English: §C6 risk-pool shadow readout (nil/true = shadow by default) — records counts and
	// whether the tier would change; missing data abstains.
	RiskPoolShadow *bool `json:"risk_pool_shadow,omitempty"`
	// TechFilter §C4 KDJ/长均线买入过滤（默认关/影子）。
	// English: §C4 KDJ / long-MA buy filter (off / shadow by default).
	TechFilter TechFilterConfig `json:"tech_filter,omitempty"`
}

// RiskPoolShadowOn §C6 风险池影子读数是否开启（nil/true=开，显式 false 关）。
// English: §C6 risk-pool shadow switch (nil/true = on).
func (e EnhanceConfig) RiskPoolShadowOn() bool { return e.RiskPoolShadow == nil || *e.RiskPoolShadow }

// TechFilterConfig §C4 买入技术过滤配置。
//   - KDJJMax >0：J 值高于该阈值 → 不新开仓；
//   - MAFilter：off / ma20 / ma60 / ma120_250；
//   - Shadow：nil/true=只记录“本应拦截”，不改裁定。
//
// English: §C4 buy-side technical filter. KDJJMax rejects J above the threshold; MAFilter selects
// the MA rule; Shadow (nil/true) records-only, explicit false enforces.
type TechFilterConfig struct {
	KDJJMax  float64 `json:"kdj_j_max"`
	MAFilter string  `json:"ma_filter"`
	Shadow   *bool   `json:"shadow,omitempty"`
}

// ShadowOn §C4 是否影子模式（nil/true=影子）。
// English: §C4 whether the filter is shadow-only (nil/true = shadow).
func (t TechFilterConfig) ShadowOn() bool { return t.Shadow == nil || *t.Shadow }

// MAFilterOf §C4 归一化均线过滤档位：off/ma20/ma60/ma120_250；未知值回退 off（不误拦）。
// English: §C4 normalizes the MA filter; unknown values fall back to off (never over-block).
func (t TechFilterConfig) MAFilterOf() string {
	switch strings.ToLower(strings.TrimSpace(t.MAFilter)) {
	case "ma20", "ma60", "ma120_250", "ma120250":
		if strings.EqualFold(strings.TrimSpace(t.MAFilter), "ma120250") {
			return "ma120_250"
		}
		return strings.ToLower(strings.TrimSpace(t.MAFilter))
	}
	return "off"
}

// Enabled §C4 过滤是否配置了至少一条规则（两条都未配=关闭，零行为变化）。
// English: §C4 reports whether at least one rule is configured (both unset = disabled).
func (t TechFilterConfig) Enabled() bool {
	return t.KDJJMax > 0 || t.MAFilterOf() != "off"
}

// PaperConfig 模拟盘（纸面交易）配置：把 buy 信号按实时价自动撮合成虚拟持仓，独立于真实持仓。
// 默认关闭（Enabled=false），开启后引擎在每轮信号产出时自动撮合。
// English: paper-trading config — auto-fills buy signals at the live price into virtual positions,
// isolated from the real book. Off by default; when enabled the engine fills each signal round.
type PaperConfig struct {
	// 总开关（默认 false）
	Enabled bool `json:"enabled"`
	// 每票固定买入资金（元，默认 10000）
	FixedAmount float64 `json:"fixed_amount"`
	// 最大并行持仓数（默认 10）
	MaxPositions int `json:"max_positions"`
	// 初始资金（元，默认 100000）
	InitialCapital float64 `json:"initial_capital"`
	// AutoSell 卖出信号自动成交开关（阶段1.1 全自动执行）：nil/未配置=开启。开启时 清仓/减仓/
	// 硬止盈/硬止损 告警直接在模拟盘自动平仓；关闭则卖出仅提醒、仍需手动。
	// English: auto-sell switch (full-auto execution); nil/unset = on. When on, 清仓/减仓/hard-TP/hard-SL
	// alerts close paper positions automatically; when off, sells stay reminder-only (manual).
	// 自动卖出
	AutoSell *bool `json:"auto_sell,omitempty"`
	// Discipline 统一止盈止损纪律（探针+扳机）参数。实盘与模拟盘共用同一套口径。
	// English: unified stop-loss/take-profit discipline (probe+trigger) parameters; shared by real and paper.
	Discipline DisciplineConfig `json:"discipline,omitempty"`
	// §SIGNAL_CONTROLLER 模拟盘战法白名单（20260917，与实盘 /api/config/qmt.strategies 同构语义）：
	// 空=默认全集（内置四形态+已启用库规则），动量/未知来源战法必须显式列名才允许撮合；
	// 非空=显式权威（只有列名的战法买入信号可进模拟盘）。由信号控制器（internal/signalctl）
	// 在 paper 通道执行准入，模拟盘引擎不再持有交易裁决逻辑。
	// 战法池资金分配模板同步跟随：momentum 仅在显式列名时开立动量池（不再恒开）。
	// English: paper-side strategy whitelist mirroring the live one (§SIGNAL_CONTROLLER_PLAN):
	// empty = default set (4 built-in forms + enabled library rules; momentum must be explicit);
	// non-empty = only listed strategies may fill. Drives the pool template too (momentum pool
	// opened only when explicitly named).
	Strategies []string `json:"strategies,omitempty"`
	// §SIGNAL_CONTROLLER 模拟盘个股黑名单（纯代码比对，与实盘同口径 config.CodeInBlacklist）。
	// 此前黑名单仅实盘下单侧消费；信号控制器把它统一进两通道（灰度由 rules.signal_ctl.shadow_blacklist 控制）。
	// English: paper-side code blacklist, enforced on the signal-controller paper channel (shadow-rolled).
	Blacklist []string `json:"blacklist,omitempty"`
	// §SHORT-3 融券做空侧（决策④）：ShortCapital>0 才开设做空池（默认 0=整侧关闭，影子期安全）。
	// 保证金率/年化费率/单笔名义预算/止损涨幅可配，零值走 paper.DefaultConfig 真实券商口径。
	// English: §SHORT-3 margin-short side — the pool is funded only when ShortCapital>0 (0 = side off,
	// the safe shadow default); rate/budget/stop normalize to broker-real defaults when zero.
	ShortEnabled     *bool   `json:"short_enabled,omitempty"` // 融券开关（nil=启用，配合 ShortCapital>0）
	ShortCapital     float64 `json:"short_capital"`           // 做空池预算（元；0=不开设）
	ShortMarginRate  float64 `json:"short_margin_rate"`       // 保证金率（默认 0.5）
	ShortFeeAnnual   float64 `json:"short_fee_annual"`        // 融券年化费率（默认 0.083）
	ShortFixedAmount float64 `json:"short_fixed_amount"`      // 单笔融券名义预算（元；0=回退 FixedAmount）
	ShortStopLossPct float64 `json:"short_stop_loss_pct"`     // 做空止损涨幅%（默认 8）
}

// QMTAdviceConfig 持仓处理分析层（实盘持仓）规则参数：加仓/格局判定阈值。
// English: position-advice layer rules for the real book: add-position and hold(格局) thresholds.
type QMTAdviceConfig struct {
	// AddReopenDrawdownPct 加仓判定：现价相对持仓最高价（highest_price）回撤不超过该值才允许加仓
	// （负值表示回撤幅度上限，如 -5 表示回撤超 5% 后不再建议加仓）。
	// 加仓判定：现价相对最高价回撤上限(%)
	AddReopenDrawdownPct float64 `json:"add_reopen_drawdown_pct"`
	// AddSignalActive 加仓判定：是否要求该股信号仍活跃（StockScores.SignalActive）。
	// 加仓是否要求信号仍活跃
	AddSignalActive bool `json:"add_signal_active"`
	// HoldMinProfitPct 格局判定：现价相对成本盈利不低于该值（%）才建议格局（继续持有）。
	// 格局判定：相对成本最低盈利(%)
	HoldMinProfitPct float64 `json:"hold_min_profit_pct"`
}

// QMTConfig 东莞证券 MiniQMT 实盘交易配置：把首尔侧的决策（信号/持仓建议）转发给国内 Windows 网关执行真实下单，
// 网关回报（成交/持仓/断线）经 /api/qmt/report 回传。与纸面账本（PaperConfig）完全独立（双账本并存）。
// English: Guoxin MiniQMT live-trading config — forwards Seoul-side decisions (signals / position advice)
// to the domestic Windows gateway for real orders; gateway reports (fills/positions/disconnect) come back
// via /api/qmt/report. Fully independent of the paper book (PaperConfig); the two books coexist.
type QMTConfig struct {
	// Enabled 总开关：是否传递信号/建议给交易服务器（热加载）。false 时实盘链路整体停用，纸面不受影响。
	// 是否启用
	Enabled bool `json:"enabled"`
	// Mode auto=全自动（信号 emit 直接下单）/ manual=半自动（前端确认后下单）。默认 manual。
	// 模式
	Mode string `json:"mode"`
	// GatewayURL 国内网关地址（如 https://<国内IP>:8789）。
	// 国内 QMT 网关地址
	GatewayURL string `json:"gateway_url"`
	// Token 与网关双向鉴权的 Bearer token。
	// 鉴权Token
	Token string `json:"token"`
	// PriceType market=对手价（网关取实时盘口）/ limit=按信号参考价限价。默认 market。
	// 价格类型
	PriceType string `json:"price_type"`
	// FixedAmount 单票买入金额（元，默认 10000）。
	// 固定金额
	FixedAmount float64 `json:"fixed_amount"`
	// MaxPositions 最大并行实盘持仓数（默认 10，双端校验）。
	// 最大持仓数
	MaxPositions int `json:"max_positions"`
	// InitialCapital 初始实盘资金（元，默认 100000，用于仓位约束预检）。
	// 初始资金
	InitialCapital float64 `json:"initial_capital"`
	// Strategies 转发策略白名单（空=全部）。
	// 策略白名单
	Strategies []string `json:"strategies"`
	// StrategyAmounts 每战法单票金额覆盖（元）：键=战法名（与 strategies 白名单同源），
	// 缺省或 <=0 时回落全局 fixed_amount。供量化交易页「每个战法独立仓位大小」。
	// 战法Amounts
	StrategyAmounts map[string]float64 `json:"strategy_amounts,omitempty"`
	// TimeoutSec 下单请求超时秒数（默认 10）。
	// 超时秒数
	TimeoutSec int `json:"timeout_sec"`
	// MissHeartbeatSec 心跳超时秒数：网关 /health 连续失联超过该值 → 熔断暂停下单并告警（默认 120）。
	// 心跳超时秒数
	MissHeartbeatSec int `json:"miss_heartbeat_sec"`
	// DailyMaxBuys 单日累计买入笔数上限（§GAP1.4 实盘买入纪律；0=不设限，默认 20）。
	// **按今日已成交笔数计**（fills 表，同一委托的多次部分成交算 1 笔）——2026-09-18 口径修正：
	// 原按「今日已报」委托数计，报单即占额度，一笔被券商废掉或挂着没成交的报单同样吃掉一天额度，
	// 会把买入权锁死。金额类闸（DailyBudgetAmount 与可用资金）仍按已报金额+在途冻结占用额度，
	// 信号风暴由金额闸兜住。
	// English: daily cap on *filled* buys for the real book (0 = unlimited, default 20) — counts
	// today's fills, deduped per order, so an unfilled/rejected submission does not burn the quota;
	// the amount-based gates still reserve in-flight submissions.
	// 日最大买入笔数
	DailyMaxBuys int `json:"daily_max_buys"`
	// DailyBudgetAmount 单日累计买入金额预算（元；0=不设限，默认 100000）。
	// 按当日已报买单 Price×Qty 累计，超出后拒绝新买入（卖出不受影响）。
	// English: daily buy budget in yuan (0 = unlimited, default 100000); accumulates today's placed
	// buy orders (Price×Qty) and rejects new buys past the cap (sells unaffected).
	// 日买入预算
	DailyBudgetAmount float64 `json:"daily_budget_amount"`
	// MinBuyPrice 最低买入价闸（元；owner 2026-10-12 裁决）：现价低于该值的"杂毛股"不做自动买入。
	// 0=按出厂默认 5 生效（存量配置快照没有该键时反序列化得 0，必须兜到默认而非旁路）；
	// 负数=显式关闭本闸。生效读数走 EffectiveMinBuyPrice（两处兜底口径单点收敛）。
	// English: minimum buy price floor (owner ruling 2026-10-12) — skip auto-buys priced below it.
	// 0 = factory default 5 (old config snapshots lack the key and would silently bypass the gate
	// if 0 meant "off"); negative = explicitly disable. Read via EffectiveMinBuyPrice.
	// 最低买入价
	MinBuyPrice float64 `json:"min_buy_price"`
	// EntryPullbackPct §C2 软回踩：>0 时挂单价 = min(信号价, VWAP×(1−pct/100))；0=关（默认）。
	// 只改限价参考，不改变股数上限；未成交仍由既有超时撤单兜底。
	// English: §C2 soft pullback — limit price = min(signal, VWAP×(1−pct/100)); 0 = off.
	EntryPullbackPct float64 `json:"entry_pullback_pct"`
	// EntryTimeoutSec §C2 软回踩单超时秒数（0=沿用 qmt.cancel_stale_sec 的既有撤单闭环）。
	// English: §C2 pullback order timeout (0 = use the existing cancel_stale_sec sweep).
	EntryTimeoutSec int `json:"entry_timeout_sec"`
	// MaxSignalVWAPPremiumPct §C2 硬过滤：信号价高于 VWAP 超过该 % 时放弃本次买入；0=关（默认）。
	// 上线前先影子统计误杀率。
	// English: §C2 hard filter — skip the buy when the signal price is above VWAP by more than this
	// percent; 0 = off (default).
	MaxSignalVWAPPremiumPct float64 `json:"max_signal_vwap_premium_pct"`
	// MaxSignalVWAPPremiumShadow §C2 硬过滤影子模式（nil/true=默认影子）：命中只记录“本应放弃”，
	// 不真的跳过；显式 false 才硬过滤。先影子统计误杀率。
	// English: §C2 hard-filter shadow (nil/true = shadow by default): record the would-be skip only;
	// explicit false enforces it.
	MaxSignalVWAPPremiumShadow *bool `json:"max_signal_vwap_premium_shadow,omitempty"`
	// MinOrderAmount 单笔买入最低成交额闸（元；owner 2026-10-12 裁决）：成交额=股价×最终买入股数，
	// 低于该值的单子（典型=降档后的 1 手 2 手碎单）不做自动买入。判据在现金降档**之后**、
	// 组单之前取最终股数核算。0=按出厂默认 5000 生效（同上：存量快照缺键必须兜默认而非旁路）；
	// 负数=显式关闭本闸。生效读数走 EffectiveMinOrderAmount。
	// English: per-order minimum notional floor (owner ruling 2026-10-12) — notional = price × FINAL
	// lot count, checked AFTER the cash-downgrade block and BEFORE order assembly. 0 = factory
	// default 5000; negative = explicitly disable. Read via EffectiveMinOrderAmount.
	// 单笔最低成交额
	MinOrderAmount float64 `json:"min_order_amount"`
	// AutoSellAllSources §B7（owner 2026-10-22）全卖出自动化开关（nil/false=保持旧行为）：
	// true 时战法自带来源的止盈/减仓也自动执行（止损本就任意来源自动）。默认 false 便于回滚。
	// English: §B7 all-source auto-sell (nil/false = legacy): when true, strategy-native TP/trim
	// sources also auto-execute (stop-loss already auto from any source). Off by default for rollback.
	AutoSellAllSources *bool `json:"auto_sell_all_sources,omitempty"`
	// AutoSell 实盘卖出自动化开关（§GAP1.1，默认开启）：mode=auto 时，止损级建议
	// （止损/清仓类）自动全仓卖出，止盈/减仓保持提醒半自动。signal_id 按日幂等防重。
	// English: auto-sell switch for the real book (default on): in auto mode, stop-loss-class advice
	// closes the position automatically; TP/trim stay reminder-only. Idempotent per day via signal_id.
	// 自动卖出
	AutoSell bool `json:"auto_sell"`
	// SellUnifiedMode §SELLPOINT-UNIFY（2026-09-21）卖出统一裁决灰度开关：
	// shadow（默认）=signalctl 卖出裁决层影子运行，只留痕对照、不改现行展示与执行；
	// on = 卖出建议/自动卖出全部改由裁决层 pass 处置驱动（五路直连拼装退役）。
	// English: §SELLPOINT-UNIFY staged rollout — "shadow" (default) runs the unified sell judge in
	// signalctl for audit comparison only; "on" switches advice + auto-sell to judge-driven disposals.
	// 卖出统一裁决模式
	SellUnifiedMode string `json:"sell_unified_mode,omitempty"`
	// Blacklist §GAP1.7 下单黑名单（纯数字或带后缀代码均可）：命中即拒绝下单。
	// 引擎每轮把 Theme.BlackList 一并同步进来；也可在 qmt 段单独配置。
	// English: §GAP1.7 order blacklist; the engine merges Theme.BlackList in every cycle.
	// 黑名单
	Blacklist []string `json:"blacklist,omitempty"`
	// Advice 持仓处理分析层（实盘持仓）规则参数。
	// 持仓处理分析层规则参数
	Advice QMTAdviceConfig `json:"advice"`
	// Discipline 统一止盈止损纪律（探针+扳机）参数，实盘与模拟盘共用同一套口径。
	// English: unified stop-loss/take-profit discipline (probe+trigger) params, shared with the paper book.
	Discipline DisciplineConfig `json:"discipline,omitempty"`
	// Halted §R4-1 kill-switch（人工紧急停止）：true 时拒绝一切新下单（auto 与手动全路径），
	// 已报未成交委托由撤单闭环/停机清单处理。经 POST /api/qmt/halt 切换并持久化。
	// English: §R4-1 kill switch — when true every new order (auto & manual) is rejected;
	// unfilled tickets are handled by the cancel sweep / close list. Toggled via POST /api/qmt/halt.
	Halted bool `json:"halted"`
	// CancelStaleSec §R4-1 未成交自动撤单阈值（秒）：已报超过该秒数仍未成交/未推进状态即自动撤单
	//（0=默认 120；-1=关闭自动撤单，仅保留收盘清单）。
	// English: §R4-1 stale-order auto-cancel threshold in seconds (0 = default 120; -1 disables).
	CancelStaleSec int `json:"cancel_stale_sec"`
	// CloseSweepAt §R4-1 收盘清单时刻（北京时 HHMM）：到达后对当日全部"已报"未成交委托撤单
	//（0=默认 1452；-1=关闭收盘清单）。
	// English: §R4-1 close-list time (Beijing HHMM) — cancels all unfilled 已报 orders of the day
	// (0 = default 1452; -1 disables).
	CloseSweepAt int `json:"close_sweep_at"`
	// EnforceT1 §WS-A T+1 可卖量守卫开关：nil=默认开启（A 股 T+1 制度恒真）；显式 false 关闭，
	// 退回券商柜台终裁（当日买入误卖会收到废单回报，不作为本地硬闸）。
	// English: §WS-A T+1 sell-guard switch — nil means enabled by default (A-share T+1 is universal);
	// explicit false disables the local hard gate and falls back to the broker's rejection.
	EnforceT1 *bool `json:"enforce_t1,omitempty"`
	// Money §WS-M 资金管理：现金缓冲 + 本地在途冻结动态占用。
	// Money = capital buffer + dynamic local frozen-in-transit accounting (§WS-M).
	Money MoneyMgmtConfig `json:"money,omitempty"`
	// Settle §WS-B 券商交割单三方对账（默认关闭：启用后才拉交割单+告警+可选补记）。
	// English: §WS-B daily three-way settlement reconciliation (off by default; enabling pulls the
	// broker settlement, persists diffs, alerts, and optionally backfills missing fills).
	Settle SettleConfig `json:"settle,omitempty"`
	// RiskGate §WS-C 风控闸口参数（零值=全部关闭，保持现状行为；开启的闸命中即拒单+记录+告警）。
	// English: §WS-C risk-gate params (zero-value = all off, legacy behavior; an enabled gate that
	// trips rejects the order, records a hit and alerts).
	RiskGate RiskGateConfig `json:"risk_gate,omitempty"`
	// AutoRiskCaution §MARKET_RISK_GATE P4 auto-buy 谨慎层第二道闸（裁决④：默认关闭，需影子验证后开启）。
	// 开启后 auto 模式对做多买入叠加风险档处置：Red 当日拒开新仓、Yellow 按仓位档缩放买入金额。
	// 与信号侧风险档（P3，默认开）独立——这里是"真金白银下单层"，保守默认关。
	// English: P4 auto-buy caution layer (ruling ④: default OFF until shadow-validated). When on, auto mode
	// adds tier handling to real buy orders — reject new opens on Red, scale buy amount on Yellow. Independent
	// of the signal-side tier (P3, default on): this is the money path, so it stays conservative by default.
	// 自动买谨慎层开关
	AutoRiskCaution *bool `json:"auto_risk_caution,omitempty"`
	// StrategyAllocs 每战法每日资金分配（元）：键=**规范战法 ID**（内置五件套 dragon /
	// double_bump / n_shape / dragon_return / momentum，与战法库已启用规则的 fac_*/pat_* ID），
	// 值=该战法单日可花费的最高预算额。未配置或 =0 时不限制该战法。
	// Σallocs 不需要等于任何总额——总预算仍由 DailyBudgetAmount 控制；
	// StrategyAllocs 只是"切蛋糕"：每个战法单独封顶，超出「当日该战法已成交 + 在途冻结 + 本次」即拒绝。
	//
	// §STRATEGY-FIX（2026-10-06 波 1）铲掉两条与实现不符的口径：
	//   - 旧注释写「键=战法 ID / 战法名」并列出「匹配顺序：先按 StrategyID，回退到显示名」——
	//     显示名那一支**从来就不是一条可用路径**：写入侧按 knownStrategyIDSet 白名单校验，
	//     非规范 ID 的键（含任何中文显示名）直接 400，配置里存不出来；而匹配侧拿显示名去查
	//     规范键的表必然恒不命中。两支合起来的实际效果是「五个内置战法的日预算恒不触发」，
	//     而这条注释是该错误口径的出处之一（幻觉注释族）。
	//   - 现取键单点：signalctl.StrategyKeyOf（规范 ID → 库规则 ID → 显示名**归一映射**到规范键），
	//     与买入幂等键、实盘准入探针同源（§C6 约定的第三处收编）。
	//     "显示名"仍可作为订单字段出现，但它只作为**映射输入**，不再作为存储键。
	// 与 StrategyAmounts 正交：后者管"每次买多少"，前者管"今天这个战法最多能花多少"。
	// English: per-strategy daily budget allocation in yuan; key = the CANONICAL strategy id (five
	// built-ins plus enabled fac_*/pat_* library rules), value = that strategy's max spend for the
	// day. Missing or zero means no cap. The old "id or display name" wording was wrong twice over:
	// the write path rejects non-canonical keys with a 400, so a display name can never be stored,
	// while the match path looked the display name up in the canonical table — which together made
	// the gate silently inert for all five built-in strategies. Lookup now derives from
	// signalctl.StrategyKeyOf, the same single source as the buy idempotency key and the admission probe.
	StrategyAllocs map[string]float64 `json:"strategy_allocs,omitempty"`
	// YellowPosScale Yellow 档买入金额缩放系数（默认 0.35，对齐状态机 range 档；0=用默认）。
	// 实际系数优先取状态机 MaxPosPct（>0 时），否则回退本值。
	// YellowPosScale buy-amount scale on Yellow (default 0.35, aligned with the range tier; the live
	// state-machine MaxPosPct takes precedence when >0).
	// Yellow 档降额系数
	YellowPosScale float64 `json:"yellow_pos_scale,omitempty"`
}

// AutoCautionOn P4 自动买谨慎层是否启用（nil 默认 false=保守关闭，需显式开启）。
// English: whether the P4 auto-buy caution layer is on (nil = false, conservative; must be enabled explicitly).
func (q QMTConfig) AutoCautionOn() bool { return q.AutoRiskCaution != nil && *q.AutoRiskCaution }

// VWAPPremiumShadowOn §C2 VWAP 硬过滤是否仍处影子期（nil/true=只记录）。
// English: §C2 whether the VWAP premium hard filter is shadow-only (nil/true = record only).
func (q QMTConfig) VWAPPremiumShadowOn() bool {
	return q.MaxSignalVWAPPremiumShadow == nil || *q.MaxSignalVWAPPremiumShadow
}

// AutoSellAllSourcesOn §B7 是否放开战法自带来源的止盈/减仓自动执行（nil/false=旧行为）。
// English: §B7 whether strategy-native TP/trim sources may auto-execute (nil/false = legacy).
func (q QMTConfig) AutoSellAllSourcesOn() bool {
	return q.AutoSellAllSources != nil && *q.AutoSellAllSources
}

// YellowScale Yellow 档降额系数（默认 0.35）。
func (q QMTConfig) YellowScale() float64 {
	if q.YellowPosScale <= 0 || q.YellowPosScale > 1 {
		return 0.35
	}
	return q.YellowPosScale
}

// RiskGateConfig §WS-C 机构级风控闸口参数。零值 = 闸全部关闭（现状行为不变，可随时开启、可回滚）。
// 唯一例外（owner 裁决 2026-09-26）：LimitDownBlockSell 跌停追卖闸为常开默认（nil=开，显式 false 关）。
// 目标补齐：日内已实现亏损熔断、单票市值集中度、涨停/跌停不可追单、行情新鲜度硬闸——
// 全部经 RiskGate.CheckLiveOrder 单一权威入口消费，命中即拒单并记录 risk_gates 命中计数。
// English: §WS-C institutional risk-gate params. Zero-value = all gates off (legacy behavior; each
// gate can be enabled independently and reverted). Sole exception (owner ruling 2026-09-26): the
// limit-down sell-chase gate is on by default (nil = on, explicit false = off). Covers: intraday
// realized-loss circuit breaker, single-stock value concentration, limit-up/down chase blocking,
// and quote-staleness hard gate — all consumed by the single authoritative RiskGate.CheckLiveOrder
// entry point, recording hits.
type RiskGateConfig struct {
	// DayLossLimitPct 日内已实现亏损熔断阈值（%）：0=关（默认）。
	// 已实现亏损 = Σ今日卖出成交(fill价−成本)×数量；达到阈值 → 熔断当日新买入（卖出/清仓放行）+ P1 告警。
	// English: intraday realized-loss circuit breaker threshold (%). 0 = off (default). Realized loss =
	// Σ today's sell fills (fillPrice − cost)×qty; at threshold the day's new buys are circuit-broken
	// (sells/liquidation stay open) with a P1 alert.
	DayLossLimitPct float64 `json:"day_loss_limit_pct"`
	// SectorValuePct §C1 行业敞口上限（%）：买入后目标行业预计市值/总资产超阈值 → 拒新买。
	// 0=关（默认）。未知行业/总资产不可得 → fail-open（弃权，不误拦）。
	// English: §C1 sector exposure cap (%) — projected sector market value / total assets; 0 = off.
	// Unknown industry or unavailable total assets fail open.
	SectorValuePct float64 `json:"sector_value_pct"`
	// SectorValueShadow §C1 行业敞口影子模式（nil/true=默认影子）：命中仅 risk_gates 留痕并放行；
	// 显式 false 才硬拒。与 cross_check_shadow 同款灰度约定。
	SectorValueShadow *bool `json:"sector_value_shadow,omitempty"`
	// ConceptValuePct §C1-V2 概念敞口上限（%）：0=关（默认）。一只股票多概念时取命中的
	// **最大敞口概念**检查，不做重叠求和；概念未知/覆盖率不足/数据过期 → 弃权。
	// English: §C1-V2 concept exposure cap (%); 0 = off. Multi-concept stocks are checked against
	// the single largest exposure (no overlap sum); unknown/low-coverage/stale data abstains.
	ConceptValuePct float64 `json:"concept_value_pct"`
	// ConceptValueShadow §C1-V2 概念敞口影子模式（nil/true=默认影子）。
	ConceptValueShadow *bool `json:"concept_value_shadow,omitempty"`
	// RiskPerTradePct §C3 单笔交易风险预算（% 账户权益）：0=关（默认）。
	// 股数 = equity × risk_pct/100 / (ATR × ATRStopMult)，再与金额股数取 min；ATR/权益缺失弃权。
	// English: §C3 per-trade risk budget (% of equity); 0 = off. Qty is capped by
	// equity×pct/(ATR×stopMult) and min()'d with the amount-derived qty; missing inputs abstain.
	RiskPerTradePct float64 `json:"risk_per_trade_pct"`
	// RiskBudgetShadow §C3 影子模式（nil/true=默认影子）：只记录“原本会缩到多少”，不改 qty；
	// 显式 false 才真正收紧股数。
	RiskBudgetShadow *bool `json:"risk_budget_shadow,omitempty"`
	// SingleStockValuePct 单票市值/总资产 集中度上限（%）：0=关（默认）。
	// 买入后该股预计市值（持仓+本单）/ 总资产（可用现金+持仓市值）超过阈值 → 拒新买。
	// English: single-stock market value / total assets concentration cap (%). 0 = off (default).
	// If the projected value of the stock after this buy (held + this order) over total assets
	// (available cash + held value) exceeds the cap, the buy is rejected.
	SingleStockValuePct float64 `json:"single_stock_value_pct"`
	// StaleQuoteMs 行情新鲜度硬闸（毫秒）：0=关（默认）。下单时快照陈旧度超过该值 → 拒单 + 告警
	// （原 fetcher 只打日志不拦单）。
	// English: quote-staleness hard gate (ms). 0 = off (default). If the snapshot staleness at order
	// time exceeds this, the order is rejected + alerted (the fetcher used to only log).
	StaleQuoteMs int64 `json:"stale_quote_ms"`
	// LimitUpBlockBuy 涨停不可追买（默认关）：买入参考价 ≥ 昨收×(1+板感知涨停%) → 拒单。
	// English: block chasing a limit-up buy (default off): buy reference price ≥ prevClose×(1+board
	// limit-up%) → reject.
	LimitUpBlockBuy bool `json:"limit_up_block_buy"`
	// LimitDownBlockSell 跌停不可追卖：**常开**（owner 裁决 2026-09-26「跌停追卖闸是否常开：是」）。
	// 语义与 EnforceT1 同款 *bool：nil（未配置）→ 默认开；显式 false 才关闭。判定：卖出参考价
	// ≤ 昨收×(1−板感知涨停%) → 拒单（防在跌停板上割肉追卖）。注意 fail-open 语义不变：
	// 无昨收（PrevClose≤0）时仍跳过交由柜台判定——所以打分链路必须注入昨收（见
	// scoring_loop 的 PrevClose 腿），否则本闸形同虚设。
	// English: block chasing a limit-down sell — ON by default (owner ruling 2026-09-26). Same *bool
	// convention as EnforceT1: unset = on, explicit false opts out. Fail-open on unknown prevClose
	// is unchanged, so the scoring loop must carry PrevClose for this gate to have teeth.
	LimitDownBlockSell *bool `json:"limit_down_block_sell,omitempty"`
	// MaxOrderAmount 单笔委托金额绝对帽（元，默认 0=关）：本单金额（qty×参考价，缺省回退）
	// 超过该值 → 拒一切新委托（买卖双向）。定位：手动下单入口（/api/positions/execute）不设
	// sizing 通道天然限额，胖手误（多打一个 0）唯一封顶手段；自动单同享此帽做双保险。
	// English: absolute per-order amount cap (0 = off). Rejects any new order whose amount
	// (qty × ref price) exceeds it — the only hard ceiling on the manual entry point.
	MaxOrderAmount float64 `json:"max_order_amount"`
	// CrossCheckPct §XCHECK 2026-09-22 C批：价格复核闸阈值（%），0=关（默认）。
	// 委托参考价与独立复核源价（DataCoordinator.CrossCheckPrice，多源行情链独立于本单快照）
	// 的偏离上限：|参考价−复核价|/复核价×100 超过该值即命中。为什么要这道闸：既有闸只判
	// 「快照陈旧度」，判不了「两源同刻价差」（单源脏数据/除权错位价），复核闸补上这一维。
	// 注意执行位点：本闸在 controller orderMu 锁内同步调一次行情 HTTP（GetRealtimeQuote 自带
	// 超时链），低频可接受（下单本身即低频动作，一次秒级超时远小于锁内其它 DB 往返的累计耗时）。
	// 影子期语义见 CrossCheckShadow：默认影子=命中仅 risk_gates 留痕放行，正式化后才拒单。
	// English: §XCHECK price cross-check threshold in % (0 = off, default). Deviation cap between
	// the order reference price and an independent quote source; runs synchronously inside the
	// controller orderMu lock as one HTTP call with its own timeout — acceptable at order frequency.
	CrossCheckPct float64 `json:"cross_check_pct"`
	// CrossCheckShadow §XCHECK 2026-09-22 C批：价格复核闸影子模式（nil/true=默认影子）：
	// 命中仅写 risk_gates 留痕（原因带 [shadow] 前缀）并放行，供 owner 观察误拦率；
	// 置 false 切正式——命中即拒单+高优告警。仿 SignalCtlConfig.ShadowBlacklist 惯例：
	// 新行为先影子验证再硬拦，零配置永不改变现有下单行为，可随时回滚。
	// English: nil/true = cross-check hits are recorded ([shadow]-prefixed) but not enforced;
	// explicit false turns the gate into a hard reject. Mirrors the shadow_blacklist convention.
	CrossCheckShadow *bool `json:"cross_check_shadow,omitempty"`
}

// CrossCheckEnforce §XCHECK 价格复核闸是否已切正式（拒单）模式：仅显式 false 时为 true；
// nil/true 均维持影子（命中留痕放行）。判定姿势与 ShadowBlacklist 同款。
// English: reports whether the cross-check gate has left shadow mode and hard-rejects — only an
// explicit cross_check_shadow=false enforces; unset/true keep record-and-pass behavior.
func (r RiskGateConfig) CrossCheckEnforce() bool {
	return r.CrossCheckShadow != nil && !*r.CrossCheckShadow
}

// SectorValueShadowOn §C1 行业敞口是否仍处影子期：nil/true = 影子（留痕放行），显式 false = 硬拒。
// English: §C1 sector gate shadow switch — nil/true means record-and-pass; explicit false enforces.
func (r RiskGateConfig) SectorValueShadowOn() bool {
	return r.SectorValueShadow == nil || *r.SectorValueShadow
}

// RiskBudgetShadowOn §C3 ATR 风险预算是否仍处影子期：nil/true=只记录，显式 false=真收紧。
// English: §C3 risk-budget shadow switch — nil/true records only; explicit false enforces.
func (r RiskGateConfig) RiskBudgetShadowOn() bool {
	return r.RiskBudgetShadow == nil || *r.RiskBudgetShadow
}

// ConceptValueShadowOn §C1-V2 概念敞口影子开关（nil/true=影子）。
// English: §C1-V2 concept exposure shadow switch (nil/true = shadow).
func (r RiskGateConfig) ConceptValueShadowOn() bool {
	return r.ConceptValueShadow == nil || *r.ConceptValueShadow
}

// LimitDownBlockSellEnabled §A5-常开（owner 裁决 2026-09-26）返回跌停追卖拒单闸是否生效：
// nil（未配置）→ true 常开；显式布尔按值。判定姿势与 EnforceT1Enabled 同款。
// English: reports whether the limit-down sell-chase block is active — unset defaults to true
// (always-on per owner ruling 2026-09-26); an explicit bool wins. Same convention as EnforceT1.
func (r RiskGateConfig) LimitDownBlockSellEnabled() bool {
	if r.LimitDownBlockSell == nil {
		return true
	}
	return *r.LimitDownBlockSell
}

// AnyEnabled 是否开启了至少一道 RiskGate（供 UI/健康展示与短路）。
// 注意：跌停追卖闸自 §A5-常开起默认生效，故零值配置下 AnyEnabled 恒为 true——
// 该值只表示"至少一道闸开"，不再等价于"全部闸关"。
// English: AnyEnabled reports whether at least one risk gate is enabled. Since §A5-always-on the
// limit-down gate defaults to on, so zero config now reports true — the flag means "at least one
// gate active", no longer "all gates off".
func (r RiskGateConfig) AnyEnabled() bool {
	return r.DayLossLimitPct > 0 || r.SingleStockValuePct > 0 || r.StaleQuoteMs > 0 ||
		r.LimitUpBlockBuy || r.LimitDownBlockSellEnabled() || r.MaxOrderAmount > 0 ||
		r.CrossCheckPct > 0 || r.SectorValuePct > 0 || r.ConceptValuePct > 0 || r.RiskPerTradePct > 0
}

// defaultOffGateEntries §0926E2E-4B（2026-09-26 全量评价批，裁决=选项B）：六道"出厂默认关"
// 风控闸的键名与判定闭包（键名与 /api/risk/gates 的 switches 口径逐一对齐，前端按键取中文名）。
// 跌停追卖闸不列入默认关组——§A5-常开起其未配置即生效，"没配置"≠"没防守"。
// English: the six factory-default-closed gates (key + closed-predicate); the limit-down gate is
// excluded because since §A5 it is always-on unless explicitly disabled.
var defaultOffGateEntries = []struct {
	Key    string
	Closed func(r RiskGateConfig) bool
}{
	{"day_loss", func(r RiskGateConfig) bool { return r.DayLossLimitPct <= 0 }},
	{"concentration", func(r RiskGateConfig) bool { return r.SingleStockValuePct <= 0 }},
	{"stale_quote", func(r RiskGateConfig) bool { return r.StaleQuoteMs <= 0 }},
	{"limit_up_block_buy", func(r RiskGateConfig) bool { return !r.LimitUpBlockBuy }},
	{"max_order_amount", func(r RiskGateConfig) bool { return r.MaxOrderAmount <= 0 }},
	{"cross_check", func(r RiskGateConfig) bool { return r.CrossCheckPct <= 0 }},
}

// DisabledGates 返回当前**关闭**的风控闸键名（默认关六闸按现值判定；跌停追卖闸仅在显式
// 配 false 时计入——未配置=常开不算关）。供 /api/risk/gates 数据面与"缺配置"告警卡消费。
// English: lists currently-closed risk-gate keys; the limit-down gate only appears when explicitly
// disabled (unset means always-on per §A5).
func (r RiskGateConfig) DisabledGates() []string {
	out := []string{}
	for _, e := range defaultOffGateEntries {
		if e.Closed(r) {
			out = append(out, e.Key)
		}
	}
	if !r.LimitDownBlockSellEnabled() {
		out = append(out, "limit_down_block_sell")
	}
	return out
}

// GatesConfigUnset 六道默认关闸是否**全部仍处默认关**（= 该账号一条都没配过风控闸）。
// §0926E2E-4B 的健康度信号：保持"默认关"出厂语义不动（开闸会在存量账号上产生新拒单，
// 风险>收益，owner 裁决），但把"裸奔"状态从无人可见变成一屏告警——前端据此亮黄条，
// 开闸建议见 docs/RUNBOOK_QMT_DAILY.md §4.3。
// English: true when all six default-closed gates are still closed — the account never configured
// any of them. Kept default-closed by owner ruling; surfaced as a UI health warning instead.
func (r RiskGateConfig) GatesConfigUnset() bool {
	for _, e := range defaultOffGateEntries {
		if !e.Closed(r) {
			return false
		}
	}
	return true
}

// SettleConfig §WS-B 交割单三方对账参数。
// English: §WS-B settlement params.
type SettleConfig struct {
	// Enabled 启用每日盘后交割单对账（默认 false）。
	Enabled bool `json:"enabled"`
	// Mode report_only=只出差异+告警（默认）；sync_fills=同时补记券商有本地无的成交。
	Mode string `json:"mode"`
	// At 每日对账时刻（北京时 HHMM，默认 1530）。
	At int `json:"at"`
}

// MoneyMgmtConfig §WS-M 资金管理参数（默认零值=现状行为：无缓冲、无本地冻结叠加）。
// 目标：①可配置"最低保留现金"（防把可用打到 0 满仓）；②在券商口径不可信时，
// 用本地账本（已报未成交买单）显式计冻结，防止并发多信号超买；③明确不双扣——
// 券商口径新鲜时信任其侧冻结，本地冻结仅在券商口径过期/缺失分支生效。
// English: §WS-M money-management params (zero-value = legacy behavior). Adds an optional cash
// reserve and, when broker cash is stale/missing, an explicit local frozen-in-transit deduction
// (never double-counts — the broker's own freeze is trusted while its report is fresh).
type MoneyMgmtConfig struct {
	// CashReserveRatio 保留现金比例（0~1）：可下单资金 = 可用 × (1−ratio)。0=无缓冲（现状）。
	// English: fraction of available cash held as a reserve (0~1). 0 = no buffer (legacy).
	CashReserveRatio float64 `json:"cash_reserve_ratio"`
	// MinReserveAmount 最低保留现金绝对值（元）：生效值取 max(ratio×可用, MinReserveAmount)。
	// English: absolute floor for the cash reserve (yuan); effective reserve = max(ratio, min).
	MinReserveAmount float64 `json:"min_reserve_amount"`
	// TrustBrokerFreeze 券商口径新鲜（≤10min）时信任其侧已含冻结，不再叠加本地在途冻结。
	// 默认 true（避免双扣）；false 时始终叠加本地冻结（更保守）。
	// English: when the broker cash snapshot is fresh, trust that it already nets its own freeze and
	// skip the local frozen-in-transit deduction. Default true (no double count); false always deducts.
	TrustBrokerFreeze *bool `json:"trust_broker_freeze,omitempty"`
}

// TrustBrokerFreezeEnabled §WS-M：是否信任券商侧冻结（nil 默认 true）。
// English: reports whether the broker-side freeze is trusted; unset defaults to true.
func (m MoneyMgmtConfig) TrustBrokerFreezeEnabled() bool {
	if m.TrustBrokerFreeze == nil {
		return true
	}
	return *m.TrustBrokerFreeze
}

// EffectiveReserve 计算生效保留额：max(可用×ratio, MinReserveAmount)。
// English: effective reserve = max(available×ratio, MinReserveAmount).
func (m MoneyMgmtConfig) EffectiveReserve(available float64) float64 {
	r := m.CashReserveRatio
	if r > 1 {
		r = 1
	}
	if r < 0 {
		r = 0
	}
	byRatio := available * r
	if m.MinReserveAmount > byRatio {
		return m.MinReserveAmount
	}
	return byRatio
}

// EnforceT1Enabled §WS-A 返回 T+1 守卫是否生效：nil（未配置）→ true（默认开）；显式布尔按值。
// English: §WS-A reports whether the T+1 sell guard is active; unset defaults to true.
func (c QMTConfig) EnforceT1Enabled() bool {
	if c.EnforceT1 == nil {
		return true
	}
	return *c.EnforceT1
}

// DisciplineConfig 统一止盈止损纪律参数（探针5s扫描 + 扳机确认窗）。
// 判定线全部相对买入价：止损-6 / 止盈+15 / 移动止盈=最高价-6（突破+15后动态上移）/ 深破=-12(2×止损线)。
// 触发后进入固定观察窗（不滚动）：窗内持续有同向信号→跟随；窗结算仍无信号→离场。
// 实盘与模拟盘统一严格执行；战法自带止盈止损降级为触发通知。
type DisciplineConfig struct {
	// 止损线（%）：浮亏达此值触发止损判定（默认 6）
	StopLossPct float64 `json:"stop_loss_pct"`
	// 止盈线（%）：盈利达此值触发止盈判定（默认 15）
	TakeProfitPct float64 `json:"take_profit_pct"`
	// 移动止盈最大回撤（%）：突破止盈线后，按最高价-此值动态上移止盈点（默认 6）
	MaxPullbackPct float64 `json:"max_pullback_pct"`
	// 深破倍数：浮亏达 止损线×此倍数 触发深破判定（默认 2 → -12%），同样走观察窗确认
	DeepStopMult float64 `json:"deep_stop_mult"`
	// 低置信买入确认（分钟）：置信度<高置信阈值的买入信号需持续存在该时长才下单（默认 5）
	BuyConfirmMin int `json:"buy_confirm_min"`
	// 高置信买入观察（秒）：置信度≥高置信阈值的买入信号至少观察该秒数才下单（默认 30）
	BuyConfirmHighSec int `json:"buy_confirm_high_sec"`
	// 止损/止盈结算窗（分钟）：首触判定线锁死一个固定窗口，结算仍无信号即离场（默认 15）
	ExitConfirmMin int `json:"exit_confirm_min"`
	// 移动止盈洗盘过滤窗（分钟）：移动止盈用更长窗过滤主升浪中的回撤洗盘（默认 45，30-60 可选）
	TrailConfirmMin int `json:"trail_confirm_min"`
	// 探针频率（秒）：实时快照扫描间隔（默认 5）
	ProbeSec int `json:"probe_sec"`
	// 高置信度阈值（百分数）：≥此值视为高置信买入（默认 85）
	HighConfThreshold float64 `json:"high_conf_threshold"`
	// SellSignalMaxAgeSec §SELLPOINT-UNIFY 边界⑥（延持信号必须新鲜）：卖出观察窗内用做多
	// SignalActive 判定延持时，信号产出时刻超过该秒数即按"无信号"处理（默认 300=一个打分周期，
	// 防 5s 探针读到陈旧缓存信号导致该离场不退）。
	// English: §unify boundary ⑥ — a bull signal older than this many seconds (default 300, one scoring
	// cycle) counts as absent when the sell window decides whether to extend the hold.
	// 卖出延持信号新鲜度上限秒数
	SellSignalMaxAgeSec int `json:"sell_signal_max_age_sec"`
	// MaxExtends 延持次数上限（owner 2026-10-12 裁决）：卖出观察窗结算时，"仍有新鲜做多信号→延持"
	// 最多执行该次数；超限后即使信号仍在也按无信号处置（止损线→半平、止盈/移动止盈→全平）。
	// 背景：000063 中兴通讯止损触线（-6.4%）后战法 fac_138 持续喊买，旧口径"反复延持=按设计拿着"
	// 一路延持 22 次（10:00→15:15），要等 -12% 深破线才无条件兜底，回撤成本过高。
	// 0=按出厂默认 2 生效（存量纪律快照没有该键时反序列化得 0，必须兜到默认而非旁路）；
	// 负数=显式恢复"无上限"旧行为（逃生阀，不推荐）。
	// English: extension cap for the sell-hold window (owner ruling 2026-10-12) — "fresh bull signal →
	// extend" may run at most this many times; past the cap the settle treats the signal as absent
	// (stop-loss → trim half; TP/trail → close). 0 = factory default 2 (legacy discipline snapshots
	// lack the key); negative = restore the old unlimited behavior (escape hatch, discouraged).
	// 延持次数上限
	MaxExtends int `json:"max_extends"`
}

// DefaultQMTConfig 返回 QMT 实盘配置出厂默认：enabled=false（默认关闭）、manual 半自动、对手价。
// English: returns factory-default QMT live-trading config: disabled, manual mode, market price.
func DefaultQMTConfig() QMTConfig {
	return QMTConfig{
		Enabled:           false,
		Mode:              "manual",
		PriceType:         "market",
		FixedAmount:       10000,
		MaxPositions:      10,
		InitialCapital:    100000,
		TimeoutSec:        10,
		MissHeartbeatSec:  120,
		DailyMaxBuys:      20,
		DailyBudgetAmount: 100000,
		MinBuyPrice:       5,
		MinOrderAmount:    5000,
		AutoSell:          true,
		Advice: QMTAdviceConfig{
			AddReopenDrawdownPct: -5,
			AddSignalActive:      true,
			HoldMinProfitPct:     0,
		},
		Discipline: DefaultDisciplineConfig(),
	}
}

// DataConfig 数据源配置（§HITHINK_DATA_SOURCE_PLAN）。
// PrimarySource：回测/研究取数的主源——hithink=同花顺（新）ths_ 表优先、缺口回退 baostock 旧表；
// baostock=完全走旧表（一键切回开关）。
// ThsFactorsReady：同花顺复权因子对账门禁——未通过(false)时 HfqBars 仍走旧表。
type DataConfig struct {
	// hithink(同花顺(新)优先) | baostock(旧表)
	PrimarySource string `json:"primary_source"`
	// 复权因子对账门禁（默认 false）
	ThsFactorsReady bool `json:"ths_factors_ready"`
	// 夜间链自动追加全库寻优步骤（默认 false=推荐制手动触发）
	OptimizeEnabled bool `json:"optimize_enabled"`
	// （已删除 §0926E2E-16）原 OptimizeAutoApply `json:"optimize_auto_apply"`：定义后全仓
	// 零读取点的死开关——注释承诺"择优自动应用"从未接线。删除而非实装：owner 既有裁决是
	// "研究→实盘必过人工审批"，实装等于新开自动通道（危险方向）。旧 config.json 里若仍带
	// optimize_auto_apply 键，encoding/json 对未知键静默忽略（全仓无 DisallowUnknownFields），
	// 读取不报错、值无处生效——与死开关时期的真实行为逐字一致。
	// RiskSourceHithink 风险因子盘口主源开关（§MARKET_RISK_GATE P0）：
	// nil 或 true = 涨停/跌停/炸板池与涨跌家数以同花顺（新）hithink 为主源、东财兜底；
	// 显式 false = 应急回退阀，全部改走旧的东财直连（行为与本方案前一致）。
	// English: risk-factor board primary-source switch — nil/true = hithink primary + EastMoney
	// fallback; explicit false = emergency rollback to the old EastMoney-direct path.
	RiskSourceHithink *bool `json:"risk_source_hithink,omitempty"`
}

// RiskHithinkPrimary 返回风险盘口是否以同花顺为主源（nil 视为默认 true）。
// English: whether the risk boards use hithink as primary (nil defaults to true).
func (d DataConfig) RiskHithinkPrimary() bool {
	return d.RiskSourceHithink == nil || *d.RiskSourceHithink
}

// SchedulerConfig 按时段切换的研究调度器配置（由独立的 quant-research 服务读取）。
// 交易时段：只做 dataload 增量下载（绝不回测/研究）；盘后/周末：跑完整夜间研究作业。
// English: session-based research scheduler config (read by the standalone quant-research service).
// Trading hours run dataload incremental download only (never backtest/research); after-hours and
// weekends run the full nightly research job.
type SchedulerConfig struct {
	// 总开关（默认 true）
	Enabled bool `json:"enabled"`
	// research 二进制路径
	ResearchBin string `json:"research_bin"`
	// dataload 二进制路径
	DataloadBin string `json:"dataload_bin"`
	// 研究库路径（trading.db）
	DB string `json:"db"`
	// baostock sidecar 地址（默认 http://127.0.0.1:8787）
	PyURL string `json:"pyurl"`
	// 盘后/周末夜间作业
	Nightly NightlyConfig `json:"nightly"`
	// 交易时段增量下载
	DataloadDuringTrade DataloadDuringTradeConfig `json:"dataload_during_trading"`
	// StepTimeoutMin 夜间作业单步超时（分钟，默认 90，0=用默认）：超时 kill 子进程并记 error，
	// 防止单步挂死拖死整链（曾发生 dataload 因 baostock 封 IP 卡 21h、step_index 停在 0）。
	// English: per-step timeout for the nightly job (minutes, default 90, 0 = default): on expiry the
	// child is killed and the step errors out, so one hung step can't stall the whole chain (a dataload
	// once hung 21h on a baostock IP ban with step_index stuck at 0).
	// 夜间作业单步超时（分钟）
	StepTimeoutMin int `json:"step_timeout_min"`
	// TrimIntervalMin 盘中内存释放节流间隔（分钟，默认 15）：活跃时段 researchd 自身
	// 定时 runtime.GC()+debug.FreeOSMemory() 并防御性清理残留的 research/discover 子进程，
	// 保证研究绝不残留盘中（物理内存让给盘中的 quant 常驻服务）。
	// English: in-session trim throttle in minutes (default 15): during active sessions the researchd
	// daemon periodically GC+FreeOSMemory itself and defensively kills leftover research/discover child
	// processes, so research never lingers during trading hours (leaving RAM to the quant engine).
	// 盘中内存释放节流间隔（分钟）
	TrimIntervalMin int `json:"trim_interval_min"`
	// MinFreeMemMB 内存总闸阈值(MB)：系统可用内存低于该值时调度器不出队，任务留队列。
	// English: memory gate — tasks stay queued when system MemAvailable drops below this.
	// 内存总闸阈值（MB）
	MinFreeMemMB int `json:"min_free_mem_mb"`
	// PerTaskNightMaxMin §NIGHTCAP（2026-10-14）单条任务单夜占用上限（分钟）：0=用内置默认
	// 360（store.NightCapDefaultMin，两处等值由门禁 §117 锁）；负数=不限（逃生口，不建议）；
	// 仅对 low 优先级生效。夜窗只有一个消费槽，历史上一条三年窗因子发现每夜吃光 8 小时步长
	// 却永不落终态，队首后面 54 条一条都排不动。上限落地后单条任务一夜最多占一个上限值，
	// 到点让位（进度/断点缓存保留，顺延下一夜续跑），后面的链任务得以排水；起跑前余额
	// 不足 MinNightStartMin（15 分钟）时同样直接让位。
	// 240→360（§QRESUME 2026-10-17 owner 裁决）：权重优化阶段补齐 trial 级断点后续跑语义
	// 从"整阶段作废"变为"次夜从断点继续"——长任务占满 6h 的占用有缓存收益而非零产出；
	// 同时仍给链上其余任务留半窗以上，不重蹈 #416"8h 单步独占队首"覆辙。
	// English: per-task nightly occupancy cap (minutes): 0 = built-in default 360, negative =
	// uncapped escape hatch; low priority only. One never-finishing task used to monopolise
	// whole nights on the single worker slot; the cap turns it into a bounded share with a
	// yield-and-resume hand-off. 240→360 (§QRESUME 2026-10-17) — with trial-level checkpoint
	// resume, a longer share now leaves cache instead of wasted work.
	PerTaskNightMaxMin int `json:"per_task_night_max_min"`
	// ReplayThrottleMs 战法库全量回放逐股节流（毫秒/只）：>0 时回放循环每处理完一只股票
	// sleep 该时长——盘后十几个小时足够，用拉长总时长换取对 quant/系统的瞬时 CPU 挤压最小化
	// （2 核 4G 服务器上全池回放瞬时会把可用内存打到熔断线以下）。0=不节流。
	// English: per-stock throttle (ms) for full-library replay — sleeping between stocks stretches
	// the runtime over the long post-close window in exchange for much smaller instantaneous CPU/mem
	// pressure on the box (a 2-core/4G server). 0 = no throttling.
	ReplayThrottleMs int `json:"replay_throttle_ms"`
	// §数据源路由（§HITHINK_DATA_SOURCE_PLAN）：研究/回测取数主源与复权门禁。
	// hithink | baostock（默认 baostock=旧表，安全）
	PrimarySource string `json:"primary_source"`
	// 复权对账门禁：通过后置 true，HfqBars 才走 ths 因子
	ThsFactorsReady bool `json:"ths_factors_ready"`
	// 夜间自动寻优开关（默认 true，推荐制）
	OptimizeEnabled bool `json:"optimize_enabled"`
}

// DiscoverConfig 因子/形态发现 + B4 回测 + 护栏参数（rules.nightly.discover）。
// 2026-09-05 多轮自动发现：每夜按 variants 个变体任务 × 每变体 top_n 个排他最优组合产出互异候选。
// English: factor/pattern discovery, B4 backtest and guardrail parameters (rules.nightly.discover).
// Since 2026-09-05 multi-round discovery: `variants` variant tasks per night × `top_n` exclusive
// top combos per variant produce mutually-distinct candidates.
type DiscoverConfig struct {
	// Horizons 多轮变体参数：前瞻天数列表（轮次 i 用 Horizons[i]，越界用默认 5）。
	Horizons []int `json:"horizons"`
	// StartWindows 多轮变体参数：窗口年数列表（1/2/3=近 N 年）。窗轮换使样本内每夜真前进，从源头避免滑窗贪心每晚选同一组合。
	StartWindows []int `json:"start_windows"`
	// Metrics 多轮变体参数：优化目标列表（ir|ic），轮次 i 用 Metrics[i]。
	Metrics []string `json:"metrics"`
	// FactorPools 多轮变体参数：风格子池名列表（""=全池；预设见 cmd/research 的 resolveFactorPool）。
	FactorPools []string `json:"factor_pools"`
	// TopN §S1 每变体排他重跑产出的互异最优组合数（默认 2）。与 variants 相乘 = 每晚候选数。
	TopN int `json:"top_n"`
	// MinStocks 每日最小样本（默认 20）
	MinStocks int `json:"min_stocks"`
	// MaxFactors 组合最大因子数（默认 8）
	MaxFactors int `json:"max_factors"`
	// Split 样本内占比（默认 0.7）
	Split float64 `json:"split"`
	// MinIR 护栏 |IR| 下限（默认 0.3）
	MinIR float64 `json:"min_ir"`
	// MinDays 护栏有效日下限（默认 30）
	MinDays int `json:"min_days"`
	// MinGenT 反推泛化 Welch t 护栏（默认 -2）
	MinGenT float64 `json:"min_gen_t"`
	// MinTrigger 形态战法最小触发次数（默认 20）
	MinTrigger int `json:"min_trigger"`
	// MinExcess 形态战法护栏最小平均超额（默认 0.01）
	MinExcess float64 `json:"min_excess"`
	// MinLimitUps B4 合成事件行业涨停家数下限（默认 3）
	MinLimitUps int `json:"min_limit_ups"`
	// TopK B4 每事件选股数（默认 5）
	TopK int `json:"top_k"`
	// MaxPerDay B4 每交易日最多事件数（默认 3，对齐 backtest.DefaultOptions。>1 才有多事件）。
	MaxPerDay int `json:"max_per_day"`
	// GuardStrong §C2 护栏分级：|IR| ≥ 记 strong（可审批）
	GuardStrong float64 `json:"guard_strong"`
	// GuardWeak §C2 护栏分级：|IR| ≥ 记 weak（进灰度观察区）
	GuardWeak float64 `json:"guard_weak"`
	// MinYrSign §C3a 分年度 IR 符号一致最少年数（0=不启用）
	MinYrSign int `json:"min_yr_sign"`
	// MinBtEvents §C3b B4 回测事件数护栏（< 则 reason 标注统计意义弱；0=不启用）
	MinBtEvents int `json:"min_bt_events"`
	// DedupJaccard §S2 近似重复 Jaccard 阈值（集合交/并 ≥ 则判定重复候选，默认 0.8）
	DedupJaccard float64 `json:"dedup_jaccard"`
	// ChangeGate §S3 变化门：最优组合与最近 pending 候选相同且新鲜时跳过（默认关→经观察后开）
	ChangeGate bool `json:"change_gate"`
	// StalenessDays §S3 变化门冷却（组合相同但候选超过该天数可重发，默认 30）
	StalenessDays int `json:"staleness_days"`
	// Hysteresis §C4 滞回：与已应用候选组合相同且 |ΔIR| < 时跳过（防边际改进顶掉已应用战法，默认 0.05）
	Hysteresis float64 `json:"hysteresis"`
}

// NightlyConfig 夜间研究作业配置（盘后/周末触发）。
type NightlyConfig struct {
	// 交易日盘后启动时间 HHMM（默认 1530）
	StartHHMM int `json:"start_hhmm"`
	// 周末启动时间 HHMM（默认 1530，周六周日各跑一次）
	WeekendStartHHMM int `json:"weekend_start_hhmm"`
	// 步骤序列（dataload/sector_rebuild/discover_factors/discover_patterns/list）
	Steps []string `json:"steps"`
	// 单步失败是否终止整链（默认 false=记录后继续）
	AbortOnError bool `json:"abort_on_error"`
	// BacktestEnabled 是否在发现因子候选后追加一次 B4 全链路回测，把候选的「回测超额」
	// （avg_excess）填上（前端原本显示"未测"）。默认 false（省时省 CPU）。
	// English: when true, after factor discovery the nightly job also runs a B4 full-chain backtest
	// on the newest proposed factor candidate, filling in its "回测超额" (avg_excess) — the field the
	// UI shows as "未测" otherwise. Default false to save time/CPU.
	// Backtest是否启用
	BacktestEnabled bool `json:"backtest_enabled"`
	// BacktestEvents B4 回测事件数上限（backtest_enabled 时生效；0=用默认合理值）。
	// B4 回测事件数上限
	BacktestEvents int `json:"backtest_events"`
	// ResearchRounds 每晚因子发现候选轮数（默认 4 = 2 变体 × top_n=2 排他）。
	// English: nightly factor-discovery candidate rounds (default 4 = 2 variants × top_n=2 exclusive).
	ResearchRounds int `json:"research_rounds"`
	// Discover 发现/护栏参数（多轮变体 + 护栏分级 + 去重阈值）。
	Discover DiscoverConfig `json:"discover"`
	// Lifecycle §GAP-P1 20260915：steps 含 "lifecycle" 时的衰退降级阈值
	// （零值 = research.DemoteOpts 内置默认：连续 3 日低于线/胜率<35%/样本<3）。
	// English: thresholds for the nightly lifecycle step (zero = evaluator defaults).
	Lifecycle LifecycleConfig `json:"lifecycle"`
}

// LifecycleConfig 夜间生命周期任务（衰退自动降级）阈值；零值回落内置默认。
// English: nightly lifecycle (strategy demotion) thresholds; zero values use built-in defaults.
type LifecycleConfig struct {
	// ConsecDays 连续低于阈值的交易日数（默认 3）
	ConsecDays int `json:"consec_days"`
	// MinIR 滚动 IR 下限（默认 0）
	MinIR float64 `json:"min_ir"`
	// MinWinRate 胜率下限（%，默认 35）
	MinWinRate float64 `json:"min_win_rate"`
	// MinDailyTrades 单日样本下限（默认 3）
	MinDailyTrades int `json:"min_daily_trades"`
	// DryRun 只报告不落库（上线初期观察用；确认误杀率后再关）
	DryRun bool `json:"dry_run"`
	// ZeroObsDays §RFIX-3 零观测告警阈值（天）：已启用战法连续零成交 ≥ 该天数 → 推送告警。
	// 0=走 research 内置默认 30，负数=关闭告警。
	ZeroObsDays int `json:"zero_obs_days"`
	// PendingExpireDays §RFIX-4 寻优 pending 过期天数：夜间 lifecycle 步骤把超过该天数
	// 仍未审批的 optimization_results 行置 expired。0=默认 30。
	PendingExpireDays int `json:"pending_expire_days"`
}

// DataloadDuringTradeConfig 交易时段增量下载配置（只下载，不含任何研究/回测）。
type DataloadDuringTradeConfig struct {
	// 开关（默认 true）
	Enabled bool `json:"enabled"`
	// 间隔分钟（默认 30）
	IntervalMinutes int `json:"interval_minutes"`
}

// DefaultSchedulerConfig 返回研究调度器出厂默认配置。
// English: returns factory-default research-scheduler config.
func DefaultSchedulerConfig() SchedulerConfig {
	return SchedulerConfig{
		Enabled:         true,
		ResearchBin:     "research",
		DataloadBin:     "dataload",
		PrimarySource:   "baostock", // 安全默认：旧表；对账门禁通过后配置切 hithink
		ThsFactorsReady: false,
		OptimizeEnabled: true, // §O1 夜间自动寻优默认开启（推荐制——结果需人工审批应用）
		PyURL:           "http://127.0.0.1:8787",
		Nightly: NightlyConfig{
			StartHHMM:        1530,
			WeekendStartHHMM: 1530,
			// 默认夜间研究步骤序列：行情装载 → 分钟 K 日增（§MINUTE-K，收盘后只补库里已有的票，
			// 分钟表为空时该环自动跳过并如实打印）→ 板块重建 → 因子挖掘 → 形态挖掘 → 模拟盘研究
			// （读取盘后落库的模拟盘成交/净值生成信号质量报告）→ 生命周期评估（§GAP-P1
			// 20260915：实盘战法衰退自动降级 + 灰度晋升候选生成）→ 候选列表汇总。
			// backtest 由 BacktestEnabled 开关控制追加。
			// English: default nightly steps — dataload → minute-bar sync (incremental) → sector rebuild
			// → factor discovery → pattern discovery → paper research → lifecycle (auto-demote declining
			// strategies + promotion candidates) → candidate listing. The backtest step is appended by
			// BacktestEnabled.
			Steps:           []string{"dataload", "minute_sync", "sector_rebuild", "discover_factors", "discover_patterns", "paper_research", "lifecycle", "list"},
			AbortOnError:    false,
			BacktestEnabled: false,
			BacktestEvents:  0,
			// 多轮自动发现：默认 4 轮 = 2 变体 × top_n=2 排他。
			// English: multi-round default 4 = 2 variants × top_n=2 exclusive reruns.
			ResearchRounds: 4,
			Discover: DiscoverConfig{
				Horizons:      []int{5, 10},
				StartWindows:  []int{3, 1},
				Metrics:       []string{"ir", "ir"},
				FactorPools:   []string{"", "mom_liq"},
				TopN:          2,
				MinStocks:     20,
				MaxFactors:    8,
				Split:         0.7,
				MinIR:         0.3,
				MinDays:       30,
				MinGenT:       -2,
				MinTrigger:    20,
				MinExcess:     0.01,
				MinLimitUps:   3,
				TopK:          5,
				MaxPerDay:     3,
				GuardStrong:   0.45,
				GuardWeak:     0.20,
				MinYrSign:     0,
				MinBtEvents:   0,
				DedupJaccard:  0.8,
				ChangeGate:    false,
				StalenessDays: 30,
				Hysteresis:    0.05,
			},
		},
		DataloadDuringTrade: DataloadDuringTradeConfig{
			Enabled:         true,
			IntervalMinutes: 30,
		},
		TrimIntervalMin: 15,
		// §NIGHTCAP（2026-10-14）单条任务单夜占用上限默认 360 分钟：与 store.NightCapDefaultMin
		// 同源（门禁 §117 等值锁钉两处一致）。默认即开启——夜窗只有唯一消费槽，不设上限就是
		// 允许一条跑不完的任务占光整夜（现网 #416 实录：六夜排不出一条）。
		// 240→360（§QRESUME 2026-10-17）：trial 级断点补齐后让位=有缓存收益，长任务 6h
		// 占用两夜可完成三年全池发现；仍留半窗以上给链上其余任务。
		// English: per-task nightly occupancy cap default 360 minutes, kept equal to
		// store.NightCapDefaultMin (gate-locked); on by default because the night has a single
		// consumer slot. 240→360 (§QRESUME 2026-10-17): with trial-level checkpoint resume, a
		// longer share leaves cache instead of waste and still leaves half the night for the chain.
		PerTaskNightMaxMin: 360,
	}
}

// NotifyConfig 通知推送配置：Webhook 回调地址列表（P1 清仓/止损强提醒时异步回调）。
// （NotifyConfig holds notification settings, e.g. the Webhook callback URLs used when P1
// close-out/stop-loss alerts fire.）
type NotifyConfig struct {
	// Webhook 地址列表（空则只走桌面/SSE）
	WebhookURLs []string `json:"webhook_urls,omitempty"`
	// 外部推送网关配置（APK 后台/离线触达）
	Push PushConfig `json:"push,omitempty"`
	// §GAP5.2 静默时段："HH:MM"~"HH:MM"（可跨午夜，如 22:00~08:00）；任一为空=不启用。
	// 窗口内仅高级别（LevelHigh：交易信号/清仓/止损）放行，低/中级别本地日志留痕不推送。
	// 静默时段开始（HH:MM）
	QuietStart string `json:"quiet_start,omitempty"`
	// 静默时段结束（HH:MM）
	QuietEnd string `json:"quiet_end,omitempty"`
	// §HARDENING ntfy 运维告警通道（与 APK 推送网关并行的独立冗余通道）：
	// 主题名即凭证（随机串）；Topic 为空=不启用。URL 空默认公共服务器 https://ntfy.sh，
	// 自托管后改此处即可，代码零改动。
	NtfyURL string `json:"ntfy_url,omitempty"`
	// ntfy 订阅主题（随机串，泄露=可伪造告警，勿入日志/前端）
	NtfyTopic string `json:"ntfy_topic,omitempty"`
}

// PushConfig 外部推送网关配置。
// Provider 为 "jpush" 时使用极光 REST API（AppKey+Secret 鉴权，Alias 指定推送目标设备别名）；
// 否则使用通用 webhook 网关（URL 指向接收 JSON 的推送地址）。
// Enabled 关闭时不启用推送网关。
// （PushConfig configures the external push gateway. Provider "jpush" uses the JPush REST API
// (AppKey+Secret auth, Alias targets the device alias); otherwise the generic webhook gateway
// POSTs JSON to URL. Enabled=false disables the gateway.）
type PushConfig struct {
	// 是否启用外部推送网关
	Enabled bool `json:"enabled"`
	// 网关类型：jpush | webhook（默认 webhook）
	Provider string `json:"provider"`
	// webhook 推送接收地址（JSON POST）
	URL string `json:"url,omitempty"`
	// 极光 AppKey（服务端推送鉴权用）
	AppKey string `json:"app_key,omitempty"`
	// 极光 Master Secret（服务端推送鉴权用，勿入库/勿进 APK）
	Secret string `json:"secret,omitempty"`
	// 极光推送目标设备别名（默认 quant_owner）
	Alias string `json:"alias,omitempty"`
}

// EmotionConfig 情绪周期六个阶段（冰点/启动/发酵/高潮/背离/退潮）的判定阈值。
// 各阶段由涨停家数、炸板率、连板高度等市场情绪指标的上下限共同判定。
// （EmotionConfig holds thresholds for the six emotion-cycle stages (ice/start/ferment/climax/
// divergence/retreat), jointly determined by bounds on limit-up count, open-board rate, etc.）
type EmotionConfig struct {
	// 冰点期：涨停家数上限
	EmoIceBoardMax int `json:"emo_ice_board_max"`
	// 冰点期：连板高度上限
	EmoIceLimitupMax int `json:"emo_ice_limitup_max"`
	// 冰点期：炸板率下限
	EmoIceBlastMin float64 `json:"emo_ice_blast_min"`
	// 启动期：涨停家数上限
	EmoStartBoardMax int `json:"emo_start_board_max"`
	// 启动期：连板高度下限
	EmoStartLimitupMin int `json:"emo_start_limitup_min"`
	// 启动期：连板高度上限
	EmoStartLimitupMax int `json:"emo_start_limitup_max"`
	// 启动期：炸板率下限
	EmoStartBlastMin float64 `json:"emo_start_blast_min"`
	// 启动期：炸板率上限
	EmoStartBlastMax float64 `json:"emo_start_blast_max"`
	// 发酵期：涨停家数上限
	EmoFermentBoardMax int `json:"emo_ferment_board_max"`
	// 发酵期：连板高度下限
	EmoFermentLimitupMin int `json:"emo_ferment_limitup_min"`
	// 发酵期：连板高度上限
	EmoFermentLimitupMax int `json:"emo_ferment_limitup_max"`
	// 发酵期：炸板率上限
	EmoFermentBlastMax float64 `json:"emo_ferment_blast_max"`
	// 高潮期：涨停家数下限
	EmoClimaxBoardMin int `json:"emo_climax_board_min"`
	// 高潮期：连板高度下限
	EmoClimaxLimitupMin int `json:"emo_climax_limitup_min"`
	// 高潮期：炸板率上限
	EmoClimaxBlastMax float64 `json:"emo_climax_blast_max"`
	// 背离期：涨停家数相对峰值回落家数
	EmoDivergeBoardDrop int `json:"emo_diverge_board_drop"`
	// 背离期：连板高度相对峰值回落
	EmoDivergeLimitupDrop int `json:"emo_diverge_limitup_drop"`
	// 背离期：炸板率抬升幅度
	EmoDivergeBlastRise float64 `json:"emo_diverge_blast_rise"`
	// 退潮期：涨停家数上限
	EmoRetreatBoardMax int `json:"emo_retreat_board_max"`
	// 退潮期：连板高度上限
	EmoRetreatLimitupMax int `json:"emo_retreat_limitup_max"`
	// 退潮期：炸板率下限
	EmoRetreatBlastMin float64 `json:"emo_retreat_blast_min"`
	// §MARKET_RISK_GATE P1 情绪广度纠偏（涨停池口径只看涨停家数/连板，看不到全市场普跌）：
	// 以下跌幅占比阈值 >0 时才生效（0/未配置=不纠偏，保持既有行为）；涨跌家数缺失时同样弃权。
	//   下跌家数/(涨+跌) ≥ IceDownRatio → 强制"冰点"；≥ RetreatDownRatio → 至少降为"退潮"。
	// English: P1 breadth correction — the limit-up-pool phase ignores market-wide declines. These
	// thresholds only apply when >0 (0/unset = no correction, preserving existing behavior), and the
	// correction abstains when up/down counts are missing. down/(up+down) ≥ Ice → force "ice";
	// ≥ Retreat → demote to at least "retreat".
	EmoBreadthIceDownRatio     float64 `json:"emo_breadth_ice_down_ratio"`     // 下跌占比达到此值强制判定"冰点"（0/未配置=不启用）
	EmoBreadthRetreatDownRatio float64 `json:"emo_breadth_retreat_down_ratio"` // 下跌占比达到此值降级至少为"退潮"（0/未配置=不启用）
	// BlockBuyPhases 禁止开仓的情绪周期阶段列表（C5）：这些阶段下四战法均不发买入信号
	// （降级为 watch 观察）。空列表时默认仅 ["衰退"]（与 N 形既有情绪硬闸一致）。
	// English: emotion phases in which buying is forbidden (C5) — all four strategies downgrade buy
	// signals to watch under these phases. Empty falls back to ["衰退"] (matching N-shape's hard gate).
	// 禁止开仓的情绪周期阶段列表
	BlockBuyPhases []string `json:"block_buy_phases,omitempty"`
}

// MainSectorConfig 主线板块识别配置：涨停家数、成交量排名、涨幅等阈值。
// Bull/Shock 两套阈值分别对应牛市强势行情与震荡行情的板块强度判定。
// （MainSectorConfig configures main-sector identification via limit-up count, volume rank, gain
// thresholds, etc.; Bull/Shock presets match strong bull vs. choppy sideways markets.）
type MainSectorConfig struct {
	// SectorConstituentTopN 每板块纳入可操作成分股的数量（板块→个股传播/成分股评分）。
	// 默认 20：扩大覆盖使同板块强势股（如剑桥科技）能进打分池，避免只覆盖龙头前10而漏选。
	// English: number of constituents per sector treated as actionable (sector→stock propagation /
	// constituent scoring). Default 20 to widen coverage so more same-sector leaders like Cambridge reach
	// the pool instead of only the top-10 leaders.
	// 每板块纳入可操作成分股数量
	SectorConstituentTopN int `json:"sector_constituent_top_n"`
}

// LLMConfig LLM 客户端连接配置。
// （LLMConfig is the LLM client connection configuration.）
type LLMConfig struct {
	// LLM API 地址
	APIURL string `json:"api_url"`
	// 模型名称
	Model string `json:"model"`
	// 单次请求超时（秒），缺省 60
	TimeoutSec int `json:"timeout_sec"`
	// Stream 流式（SSE）响应开关。nil（缺省/未配置）= 开启（推理模型非流式首字极慢，
	// 恒开流式 + 内部回落为默认策略）；显式 false = 关闭，走一次性非流式。
	// 是否启用流式响应
	Stream *bool `json:"stream,omitempty"`
	// MaxRetryTimes D1 评分 LLM 调用轮询重试次数（含首次）。<=0 时回退默认 2（§信号速度 S5）。
	// 当轮不反复死磕：失败股置 RetryPending 并入下轮重试队列，不丢分；配合增量 D1 提速。
	// 重试防丢信号：LLM 偶发超时/限流时不再轻易丢弃重要 D1 评分。
	// LLM 调用最大重试次数
	MaxRetryTimes int `json:"max_retry_times"`
	// BatchConcurrency 新闻归因（Stage0/Stage2）LLM 批量分析的最大并发批次数量。
	// <=0 时回退默认 8；API 配额充足时调高可加快盘前新闻归因吞吐，前端可热改。
	// LLM 批量分析最大并发批次
	BatchConcurrency int `json:"batch_concurrency"`
	// D1MaxTokens D1 评分 LLM 单次调用的 max_tokens（推理长度上限，§信号速度 S3）。
	// <=0 时回退默认 2048。D1 评分输出为结构化 JSON，无需过长思维链，
	// 限制长度可显著降低单股评分耗时（原非流式硬编码 4096）。前端可热改。
	// English: max_tokens for D1 scoring LLM calls (reasoning-length cap, §speed S3). <=0 falls back to the
	// default 2048. D1 outputs structured JSON needing no long chain-of-thought, so capping length cuts
	// per-stock latency (previously hardcoded 4096 on the non-streaming path). Hot-adjustable in the UI.
	D1MaxTokens int `json:"d1_max_tokens"`
	// ClassifierModel 可选：新闻归因分类（Stage0/1 合并调用等"快速分类/初筛"）专用模型。
	// 配置轻量/快速模型可显著加快分类吞吐，把主模型留给 D1/Stage2 等深度分析；留空与主模型一致。
	// English: optional dedicated model for news-attribution classification (Stage0/1 combined calls and
	// other cheap screening). A lighter/faster model speeds classification while the main model stays on
	// deep work (D1/Stage2); empty falls back to the main model.
	// 新闻分类专用模型
	ClassifierModel string `json:"classifier_model"`
	// §GAP5.1 成本治理：当日调用次数 / token 总量预算（0=不设限）。超限后当日新请求熔断，
	// 次日自动恢复。LLM 是系统最大可变成本，此前用量完全不可见、无任何上限。
	// 当日 LLM 调用次数预算
	DailyCallBudget int64 `json:"daily_call_budget"`
	// 当日 token 总量预算（0=不设限），超限当日熔断、次日自动恢复（§FIX-7 接线）。
	// Daily token budget; 0 = unlimited; trips today, auto-recovers next day.
	DailyTokenBudget int64 `json:"daily_token_budget"`
	// StreamIdleTimeoutSec 流式"相邻分片空闲"阈值（秒）。<=0 时 llm 包回落默认 60s。
	// §FIX-3(20260919) 语义修正后该值恢复本义：分片到达即重置，只掐"真的停止吐字"的卡流，
	// 不再是整段硬超时（旧实现把慢而正常的长推理流式误杀）。
	// Stream idle timeout in seconds
	StreamIdleTimeoutSec int `json:"stream_idle_timeout_sec"`
	// ConsultDailyCalls §FIX-7(20260919)：AI 顾问（咨询）单独的当日调用预算（0=不设限）。
	// 咨询出呼走运营账号统一密钥、全体用户共用，总日预算之外再给咨询封顶，
	// 防高频问答吃光新闻归因/D1 当日配额。超限当日 429，次日自动恢复。
	// Consultation daily call budget
	ConsultDailyCalls int64 `json:"consult_daily_calls"`
}

// StreamingEnabled 返回流式响应是否启用：未显式配置（nil）时默认开启。
// （StreamingEnabled reports whether streaming is enabled; nil (unset) means enabled by default.）
func (c *LLMConfig) StreamingEnabled() bool {
	if c == nil || c.Stream == nil {
		return true
	}
	return *c.Stream
}

// ThemeConfig 主题白名单和黑名单。
// （ThemeConfig is the theme watch-list and black-list configuration.）
type ThemeConfig struct {
	// 排除主题黑名单
	BlackList []string `json:"black_list"`
}

// DrawdownRule 回撤规则：触发阈值时执行对应操作。
// （DrawdownRule defines a drawdown rule: when the threshold is hit, the action fires.）
type DrawdownRule struct {
	// 回撤百分比阈值
	Pct float64 `json:"pct"`
	// 触发操作（如 "减仓"/"清仓"）
	Action string `json:"action"`
}

// ComplianceConfig 合规配置。
// （ComplianceConfig is the compliance configuration.）
type ComplianceConfig struct {
	// 是否启用合规模式
	ComplianceMode bool `json:"compliance_mode"`
}

// RiskCtrlConfig 风控配置：止损规则、合规模式、组合回撤限制等。
// （RiskCtrlConfig is the risk-control config: stop-loss rules, compliance mode, portfolio drawdown cap, etc.）
type RiskCtrlConfig struct {
	// 合规模式
	Compliance ComplianceConfig `json:"compliance"`
	// 是否启用 M8 风控
	M8Enabled bool `json:"m8_enabled"`
	// 组合最大回撤百分比
	M8PortfolioDrawdownPct float64 `json:"m8_portfolio_drawdown_pct"`
	// 单只股票最大仓位比例
	PerStockMax float64 `json:"per_stock_max"`
}

// StopLossConfig 止损配置：买入后回撤阶梯规则。
// （StopLossConfig holds stop-loss rules as a ladder of post-buy drawdown thresholds.）
type StopLossConfig struct {
}

// PositionConfig 仓位配置：总仓位上限 + 持仓当日跌幅提醒阈值。
// （PositionConfig caps the total portfolio position and sets the daily-drop alert threshold.）
type PositionConfig struct {
	// 最大总仓位比例
	MaxTotalPositionPct float64 `json:"max_total_position_pct"`
	// AutoTrackSignals 买入信号自动纸面开仓（C3）：置真时引擎把 buy 信号写入持仓记录，
	// 激活 CheckPositionsExits 离场路径（止盈/止损/超期提醒）。仅纸面记录，不真实下单。
	// （AutoTrackSignals auto-paper-opens a position on buy signals (C3): the engine writes the buy into
	// the holding log so the CheckPositionsExits exit path activates. Paper-only, never really orders.）
	// 买入信号自动纸面开仓
	AutoTrackSignals bool `json:"auto_track_signals"`
	// ATREnabled ATR 动态止损开关（C4）：置真时以 ATRStopMult×ATR 替代固定百分比止损
	// （龙头全出/双凸硬止损/N形硬止损/龙回头止损）。默认开。
	// （ATREnabled turns on ATR-based dynamic stops (C4): ATRStopMult×ATR replaces the fixed-percentage
	// stops — dragon full-out / double-bump hard stop / n-shape hard stop / dragon-return stop-loss.）
	// ATR是否启用
	ATREnabled bool `json:"atr_enabled"`
	// ATRStopMult ATR 止损倍数（止损距离 = 该倍数 × ATR，默认 2.5）。
	// （ATRStopMult is the ATR stop multiplier — stop distance = multiplier × ATR, default 2.5.）
	// ATR 止损倍数
	ATRStopMult float64 `json:"atr_stop_mult"`
	// DailyDropAlertPct 持仓当日跌幅(%)提醒阈值：当日涨跌幅 ≤ -该值 时，
	// 无论成本盈亏是否触及止损线，都在持仓提醒中提示（<=0 用默认 5）。
	// （DailyDropAlertPct is the intraday daily-drop alert threshold for holdings: when a held stock's
	// daily change ≤ -threshold, a holding alert fires regardless of cost-based P/L (<=0 defaults to 5).）
	// 持仓当日跌幅提醒阈值(%)
	DailyDropAlertPct float64 `json:"daily_drop_alert_pct"`
}

// ── §DYN-SIZE 动态仓位合成配置 ──────────────────────────────────────────────────
//
// SizingConfig 动态仓位合成配置（docs/DYN_SIZE_FINAL_PLAN_20261016.md §4）。
//
// 分层合成（这是本方案的核心口径，勿改成纯连乘）：
//
//		BuyScale  = clamp( min( Π权重因子 , m_上限因子1 , m_上限因子2 , ... ) , Floor , Cap )
//		Downgrade = 任一硬拦因子命中（情绪冰点 / 风险档Red / D1利空且风险档≥Yellow / 连续止损熔断）
//
//	  - 权重/倾向类（信号质量、Kelly、波动率、情绪、状态、风险档、宏观影响期/前瞻、节前、大盘跌幅、
//	    LLM利空、广度）**乘性连乘 Π**——它们各自描述"这件事让仓位该多大"；
//	  - 上限类（总仓位、权益回撤）各自算一个 0~1 乘子后**取 min**——它们描述"不能超过某条线"，
//	    连乘会把同一风险（波动高与相关性高常是同一件事的投影）惩罚两次。
//
// §DYN-GLOBAL（2026-10-24 owner 裁决一）起出厂默认改为**下发态**：Enabled=true、
// BudgetMode=dynamic、Shadow=false。三把键必须同时成立才算真下发（见 EnabledOn/
// BudgetModeOf/ShadowOn），任何一把没到位都只是影子读数。
// 零值字段一律由 WithDefaults 补出厂默认（缺键=跟随新全局口径；显式 legacy/false=账号级退路）。
//
// English: since §DYN-GLOBAL (owner ruling 1, 2026-10-24) the factory default is the *delivering*
// state: Enabled=true, BudgetMode=dynamic, Shadow=false. All three keys must hold for the scale to
// reach real orders; any one missing leaves shadow-only reads. WithDefaults fills missing keys with
// the new defaults, while an explicit legacy/false stays as the account-level escape hatch.
type SizingConfig struct {
	// Enabled 总开关。§DYN-GLOBAL 后默认开（nil=开，见 EnabledOn）。
	// 改成指针的原因：旧 `bool` 让 WithDefaults 无法区分"这个键没写过"和"显式关了"——
	// 而 `json:"enabled"` 无 omitempty，任何物化过的账号载荷里都躺着 `"enabled":false`。
	// 若只把出厂默认翻成 true，存量账号会**静默留在关闭态**，正是本项目最忌的
	// "以为开了其实没开"。指针 + 访问器把这两态真正分开。
	// English: master switch, on by default (nil = on). A pointer is required because the old plain
	// bool could not tell "key absent" from "explicitly false" — and every materialized payload
	// carries "enabled":false, so flipping the factory default alone would silently leave existing
	// accounts disabled.
	Enabled *bool `json:"enabled,omitempty"`
	// BudgetMode 预算口径（账号级）：dynamic=动态仓位是唯一预算决策者（v3 定稿，新默认）；
	// legacy=旧固定金额/战法分配/每日闸，只作为**账号级显式退路**保留。
	// 空串按新默认 dynamic；其余无法识别的取值按 legacy 处理并由 BudgetModeDrift 出声报警。
	// English: account-scoped budget pipeline. "dynamic" (the new default) makes dynamic sizing the
	// sole budget authority; "legacy" is kept only as an explicit per-account escape hatch. An empty
	// value follows the new default; any other unrecognized value is treated as legacy and reported.
	BudgetMode string `json:"budget_mode,omitempty"`
	// Mode 动态仓位合成口径：product=旧连乘（默认，逐字回归）；power_mean=分组幂平均（v3）。
	// English: factor synthesis mode — "product" is the legacy byte-identical path, "power_mean"
	// is the v3 grouped power mean.
	Mode string `json:"mode,omitempty"`
	// PowerP 幂平均指数（默认 0.5；0=几何平均）。仅 power_mean 生效。
	// 用指针区分“未设置”（nil→0.5）与显式 0（几何平均）——旧 float 实现把 0 当缺省，
	// 导致 v3 文档承诺的 p=0 永远无法生效。
	// English: power-mean exponent (nil defaults to 0.5; explicit 0 = geometric mean). A pointer is
	// required to tell "unset" apart from the meaningful zero.
	PowerP *float64 `json:"power_p,omitempty"`
	// GroupFloors 分组低于此值时抬到地板，避免单组全军覆没把仓位压到不可交易。
	// 键：market（市场组，默认 0.45）/ stock（个股组，默认 0.35）。
	// English: per-group floors — market (0.45) and stock (0.35) by default.
	GroupFloors map[string]float64 `json:"group_floors,omitempty"`
	// Shadow 影子模式（§DYN-GLOBAL 后出厂默认 false=真下发；nil 由 WithDefaults 补成出厂值）。
	// 与 Enabled 相互独立：Enabled=true 且 Shadow=true 时合成照算、只留痕不下发，用于灰度对照。
	// English: shadow mode; the factory default is now false (deliver). Independent from Enabled —
	// Enabled=true with Shadow=true still computes the scale but only logs it, for gray comparison.
	Shadow *bool `json:"shadow,omitempty"`
	// PolicyVersion §DYN-GLOBAL 迁移标记：由 MigrateSizingToGlobalDefault 写入（=1），
	// 作用是**只迁一次**——迁移之后账号再显式改回 legacy，重启不会被再次抬走。
	// English: §DYN-GLOBAL migration marker, written once by the startup migration so that a later
	// deliberate account-level switch back to legacy is never overwritten again on restart.
	PolicyVersion int `json:"policy_version,omitempty"`
	// Floor 合成缩放地板（默认 0.15）——任何利空叠加也不会把单笔仓位压到该值以下。
	Floor float64 `json:"floor"`
	// Cap 合成缩放上限（默认 1.15）——允许"进攻档"（比基准加仓）但设硬顶。
	Cap float64 `json:"cap"`
	// KellyLambda Fractional Kelly 系数 λ（默认 0.5，López de Prado 半凯利）。
	KellyLambda float64 `json:"kelly_lambda"`
	// KellyMinProb 最小优势概率（默认 0.52）：置信度低于此值视为"无优势"→固定 0.6 缩放。
	KellyMinProb float64 `json:"kelly_min_prob"`
	// VolTargetPct 个股波动率目标（ATR 占现价百分比，默认 2.0）：低波动股放大、高波动股缩小。
	VolTargetPct float64 `json:"vol_target_pct"`
	// VolClampLo/VolClampHi 波动率乘子限幅（默认 0.5 / 1.5）。
	VolClampLo float64 `json:"vol_clamp_lo"`
	VolClampHi float64 `json:"vol_clamp_hi"`
	// EmotionScales 情绪相位映射（键：退潮/背离/高潮/发酵/启动；冰点走硬拦不进本表）。
	EmotionScales map[string]float64 `json:"emotion_scales,omitempty"`
	// StateScales 市场状态映射（键：bull/range/bear；状态机关闭时该因子弃权）。
	StateScales map[string]float64 `json:"state_scales,omitempty"`
	// TierScales 风险档金额权重（键：Yellow/Red）。Red 默认 0.30（§N6-RISK 2026-10-21 owner 裁决：
	// 用权重收束而不是一刀切硬拦；只有 red_block_new_buy=true 才在信号层整笔拒绝）。
	// English: risk-tier money weights (Yellow/Red). Red defaults to 0.30 — the owner prefers
	// tightening over blanket rejection; full rejection only when red_block_new_buy is on.
	TierScales map[string]float64 `json:"tier_scales,omitempty"`
	// BreadthMissingScale 广度读数缺失时的小幅降权乘子（默认 0.90 = 10%，owner 2026-10-21 裁决：
	// 不按 bull→range→bear 整档下调，只做小百分比因子降权；数据恢复即回到真实广度乘子）。
	// English: small multiplicative haircut when breadth data is missing (default 0.90 = 10%);
	// restored to the real breadth factor as soon as the reading returns.
	BreadthMissingScale float64 `json:"breadth_missing_scale"`
	// MacroWindowScales 宏观影响期缩放（键：high/medium/low）。
	MacroWindowScales map[string]float64 `json:"macro_window_scales,omitempty"`
	// MacroLeadDays 事件前瞻交易日数（默认 5）：事件前 N 个交易日起渐进收仓。
	MacroLeadDays int `json:"macro_lead_days"`
	// MacroLeadCurve 前瞻曲线，下标 0 = 距事件 MacroLeadDays 个交易日（默认 [0.9,0.85,0.8,0.7,0.5]）。
	MacroLeadCurve []float64 `json:"macro_lead_curve,omitempty"`
	// MacroEventDayScale 事件当日（T日）缩放（默认 0.30）——与前瞻曲线尾部相接。
	MacroEventDayScale float64 `json:"macro_event_day_scale"`
	// HolidayScales 节前分级（键：weekend/small1/small2/long1/long2/long3，数字=节前第 N 个交易日）。
	// 未加载日历（CalendarLoaded=false）时该因子**弃权**（绝不把"未知"当"普通日"）。
	HolidayScales map[string]float64 `json:"holiday_scales,omitempty"`
	// HolidayBlockNew 长假前最后一日禁止新开仓（默认 false，owner 风险偏好项）。
	HolidayBlockNew *bool `json:"holiday_block_new,omitempty"`
	// IndexScaleThresholds 大盘跌幅映射（键：-1 / -2 / deep，deep 表示 ≤-2% 档）。
	IndexScaleThresholds map[string]float64 `json:"index_scale_thresholds,omitempty"`
	// LLMBearishScale 个股 LLM 利空缩放（默认 0.5，首次接入买入侧）。
	LLMBearishScale float64 `json:"llm_bearish_scale"`
	// MaxTotalPositionPct 总仓位上限（默认 0.80；0=不启用）。接通既有死字段 Position.MaxTotalPositionPct。
	MaxTotalPositionPct float64 `json:"max_total_position_pct"`
	// MaxDrawdownPct 账户权益回撤熔断（默认 0.10；0=不启用）：回撤达此比例时按 pysystemtrade
	// risk_overlay 口径缩仓（mult = limit/max(limit, drawdown)）。
	MaxDrawdownPct float64 `json:"max_drawdown_pct"`
	// StoplossGuardCount 连续止损熔断次数（默认 4；0=不启用）：回看窗内止损达该次数即硬拦新开仓。
	StoplossGuardCount int `json:"stoploss_guard_count"`
	// StoplossGuardWindowDays 连续止损回看窗（交易日，默认 1）。
	StoplossGuardWindowDays int `json:"stoploss_guard_window_days"`
	// SLOffsetPerPct 止损放宽系数（pt / 大盘每跌 1%，默认 3.0）：大盘 -2% → 止损下浮 3pt。
	SLOffsetPerPct float64 `json:"sl_offset_per_pct"`
	// SLOffsetCap 止损放宽上限（pt，默认 4.0）。
	SLOffsetCap float64 `json:"sl_offset_cap"`
	// TPOffsetPerPct 止盈上浮系数（pt / 大盘每跌 1%，默认 3.0）。
	TPOffsetPerPct float64 `json:"tp_offset_per_pct"`
	// TPOffsetCap 止盈上浮上限（pt，默认 5.0）。
	TPOffsetCap float64 `json:"tp_offset_cap"`
	// SLOffsetMinIndexDrop 触发止损放宽的大盘跌幅门槛（%，默认 1.0）。
	SLOffsetMinIndexDrop float64 `json:"sl_offset_min_index_drop"`
	// BearSLTightenPt 趋势破位时止损收紧（pt，默认 1.0）——两态判别中的"趋势下跌"腿。
	BearSLTightenPt float64 `json:"bear_sl_tighten_pt"`
	// SLOffsetRiskBudgetKeep 放宽止损强制同比例缩仓（默认 true）：止损 6%→9% ⇒ 仓位 ×6/9。
	// 不缩仓等于股灾日把单笔最大亏损放大 50%（owner 已裁决强制）。
	SLOffsetRiskBudgetKeep *bool `json:"sl_offset_risk_budget_keep,omitempty"`
	// SellBoostLevel 卖出加权档位（L1/L2/L3，默认 L2）。
	SellBoostLevel string `json:"sell_boost_level"`
}

// sizingBool 返回布尔值的指针，供 §DYN-GLOBAL 出厂默认区分"显式开/关"与"键缺失"。
// English: pointer factory for the tri-state bool switches (explicit on/off vs absent).
func sizingBool(v bool) *bool { return &v }

// DefaultSizingConfig §DYN-SIZE 出厂默认。
//
// §DYN-GLOBAL（2026-10-24 owner 裁决一「dynamic 全局，legacy 开关账户级别」）把三把下发键从
// "安全侧" 抬到 "下发侧"：Enabled=true / BudgetMode=dynamic / Shadow=false。
// 其余因子参数一律保持原值——本次裁决只改**是否生效**，不改**生效时怎么算**。
// Mode 留在 "product"（Q1 推荐项：先把口径切换做干净，幂平均另批再谈，避免一次动两个变量）。
//
// English: §DYN-SIZE factory defaults. §DYN-GLOBAL (owner ruling 1) moves the three delivery keys
// from the safe side to the delivering side (enabled/budget_mode=dynamic/shadow=false) and changes
// nothing else — the ruling changes whether the scale applies, not how it is computed. Mode stays
// "product" so only one variable moves per batch.
func DefaultSizingConfig() SizingConfig {
	defaultP := 0.5
	return SizingConfig{
		Enabled:                 sizingBool(true),
		BudgetMode:              "dynamic",
		Mode:                    "product",
		Shadow:                  sizingBool(false),
		PowerP:                  &defaultP,
		GroupFloors:             map[string]float64{"market": 0.45, "stock": 0.35},
		Floor:                   0.15,
		Cap:                     1.15,
		KellyLambda:             0.5,
		KellyMinProb:            0.52,
		VolTargetPct:            2.0,
		VolClampLo:              0.5,
		VolClampHi:              1.5,
		EmotionScales:           map[string]float64{"退潮": 0.50, "背离": 0.60, "高潮": 0.80, "发酵": 1.00, "启动": 1.00},
		StateScales:             map[string]float64{"bull": 1.00, "range": 0.70, "bear": 0.40},
		TierScales:              map[string]float64{"Yellow": 0.60, "Red": 0.30},
		BreadthMissingScale:     0.90,
		MacroWindowScales:       map[string]float64{"high": 0.50, "medium": 0.80, "low": 1.00},
		MacroLeadDays:           5,
		MacroLeadCurve:          []float64{0.90, 0.85, 0.80, 0.70, 0.50},
		MacroEventDayScale:      0.30,
		HolidayScales:           map[string]float64{"weekend": 0.85, "small1": 0.70, "small2": 0.85, "long1": 0.40, "long2": 0.60, "long3": 0.75},
		IndexScaleThresholds:    map[string]float64{"-1": 1.00, "-2": 0.70, "deep": 0.40},
		LLMBearishScale:         0.5,
		MaxTotalPositionPct:     0.80,
		MaxDrawdownPct:          0.10,
		StoplossGuardCount:      4,
		StoplossGuardWindowDays: 1,
		SLOffsetPerPct:          3.0,
		SLOffsetCap:             4.0,
		TPOffsetPerPct:          3.0,
		TPOffsetCap:             5.0,
		SLOffsetMinIndexDrop:    1.0,
		BearSLTightenPt:         1.0,
		SellBoostLevel:          "L2",
	}
}

// NormalizeLegacyTotalPosPct §DYN-SIZE P6：把既有死字段 `PositionConfig.MaxTotalPositionPct`
// 归一化为**比例**（0~1]，供 `SizingConfig.MaxTotalPositionPct` 回落使用。
//
// 为什么需要归一化：该字段是历史遗留，校验只查 0~100（validate.go:85），而 `backtest_params.json`
// 里写的是 0.8——两种写法都在"合法"区间内。若把 80 当比例传进 `totalPosCap`，`limit/max(limit,risk)`
// 会恒等于 1.0（限额大到永远不约束），接了个寂寞。约定：>1 视为百分数，÷100。
// 返回 0 表示"未设置"（调用方应回落到 `SizingConfig` 自己的默认 0.80）。
// English: normalizes the legacy dead field to a ratio in (0,1]. The validator accepts 0..100 while
// the shipped config writes 0.8, so both conventions are "legal"; a raw 80 would make the cap a
// permanent no-op. Values >1 are therefore read as percentages. 0 means "unset".
func NormalizeLegacyTotalPosPct(v float64) float64 {
	if v <= 0 {
		return 0
	}
	if v > 1 {
		return v / 100
	}
	return v
}

// EnabledOn 动态仓位合成是否处于"允许生效"态（nil = 出厂默认 = 开）。
// English: whether the sizing synthesis is allowed to take effect (nil = factory default = on).
func (s SizingConfig) EnabledOn() bool { return s.Enabled == nil || *s.Enabled }

// BudgetModeOf 归一化预算口径。§DYN-GLOBAL 后的判据：
//   - 空串/未设置 → "dynamic"（新全局默认：动态仓位是唯一预算决策者）；
//   - "dynamic"（大小写不敏感）→ "dynamic"；
//   - 其余一律 "legacy" —— 含显式 "legacy"（账号级退路，owner 裁决一保留）与脏值。
//     脏值按 legacy 是**保守侧**（旧闸全部继续生效），并由 BudgetModeDrift 单独出声。
//
// English: normalizes the budget pipeline. Empty means the new global default "dynamic";
// "dynamic" is dynamic; everything else is legacy — both the explicit account-level escape hatch
// and any dirty value. Dirty values fall to the conservative side and are reported by BudgetModeDrift.
func (s SizingConfig) BudgetModeOf() string {
	v := strings.ToUpper(strings.TrimSpace(s.BudgetMode))
	switch v {
	case "":
		return "dynamic"
	case "DYNAMIC":
		return "dynamic"
	default:
		return "legacy"
	}
}

// BudgetModeDrift 报告 budget_mode 里"既不是空、也不是 legacy/dynamic 认得写法"的脏值。
// 脏值被 BudgetModeOf 保守地当成 legacy，但那是"配置写错了"而不是"账号选了 legacy"，
// 必须出声——本项目历史上吃过多次"配置错误被伪装成网络抖动/静默失效"的亏。
// 返回空串表示无漂移。
// English: surfaces a budget_mode value that is neither absent nor a recognized spelling. Such a
// value is treated as legacy (conservative) but is a typo, not a choice, and must be logged.
func (s SizingConfig) BudgetModeDrift() string {
	v := strings.TrimSpace(s.BudgetMode)
	if v == "" {
		return ""
	}
	switch strings.ToUpper(v) {
	case "LEGACY", "DYNAMIC":
		return ""
	}
	return v
}

// DynamicBudgetOn 动态预算是否开启。
// English: whether the dynamic budget pipeline is active.
func (s SizingConfig) DynamicBudgetOn() bool { return s.BudgetModeOf() == "dynamic" }

// ModeOf 归一化合成口径：仅 "power_mean" 走新合成，其余按 product。
// English: normalizes the synthesis mode; anything other than "power_mean" is product.
func (s SizingConfig) ModeOf() string {
	if strings.EqualFold(strings.TrimSpace(s.Mode), "power_mean") {
		return "power_mean"
	}
	return "product"
}

// PowerPOf 返回幂平均指数（未设置时用默认 0.5；显式 0 = 几何平均）。
// English: returns the power-mean exponent; nil defaults to 0.5 while an explicit 0 is kept
// (geometric mean).
func (s SizingConfig) PowerPOf() float64 {
	if s.PowerP != nil {
		return *s.PowerP
	}
	return 0.5
}

// GroupFloor 返回分组 floor（未设置时 market=0.45、stock=0.35，其余=0）。
// English: per-group floor (market 0.45 / stock 0.35 by default; other groups 0).
func (s SizingConfig) GroupFloor(group string) float64 {
	if v, ok := s.GroupFloors[group]; ok && v > 0 {
		if v > 1 {
			return 1 // §5.6：组 floor 是“下限”，>1 等于凭空加杠杆，钳到 1。
		}
		return v
	}
	switch group {
	case "market":
		return 0.45
	case "stock":
		return 0.35
	}
	return 0
}

// ShadowOn 影子模式是否开启。
//
// 注意分层：这里是**未归一结构体**的兜底（nil=影子开，宁可只留痕），不是出厂默认——
// 出厂默认是 Shadow=false（真下发），由 WithDefaults 在键缺失时补上。因此真下发需要
// 走归一化入口（GetSizingConfigFor/DefaultSizingConfig），而不是裸结构体。
// English: shadow flag for a *non-normalized* struct — nil defaults to ON (prefer logging over
// trading). The factory default is the opposite (Shadow=false) and is applied by WithDefaults, so
// real delivery requires going through the normalizing entry points, not a bare struct.
func (s SizingConfig) ShadowOn() bool { return s.Shadow == nil || *s.Shadow }

// HolidayBlockNewOn 长假前最后一日是否禁止新开仓（nil = 默认 false）。
// English: whether new opens are banned on the last trading day before a long holiday (nil = false).
func (s SizingConfig) HolidayBlockNewOn() bool { return s.HolidayBlockNew != nil && *s.HolidayBlockNew }

// RiskBudgetKeepOn 放宽止损是否强制同比例缩仓（nil = 默认 true）。
// English: whether loosening the stop must shrink the position proportionally (nil = true).
func (s SizingConfig) RiskBudgetKeepOn() bool {
	return s.SLOffsetRiskBudgetKeep == nil || *s.SLOffsetRiskBudgetKeep
}

// SellBoostLevelOf 归一化卖出加权档位：返回 "L1"/"L2"/"L3"，空串表示**显式关闭**（"off"/"none"/"0"）。
// 未识别值（含空串，若调用方未走 WithDefaults）→ 回退默认 "L2"。大小写不敏感、容忍空白。
// English: normalizes the sell-boost tier to L1/L2/L3; an explicit off/none/0 disables it; any
// unrecognized value falls back to the default L2.
func (s SizingConfig) SellBoostLevelOf() string {
	switch strings.ToUpper(strings.TrimSpace(s.SellBoostLevel)) {
	case "L1", "L2", "L3":
		return strings.ToUpper(strings.TrimSpace(s.SellBoostLevel))
	case "OFF", "NONE", "NO", "0":
		return ""
	}
	return "L2"
}

// WithDefaults 返回补齐出厂默认后的副本：所有数值字段 <=0 用默认，映射字段与默认**合并**
// （用户只需覆盖关心的键），Enabled/Shadow 两把下发键在**键缺失**时补出厂值（§DYN-GLOBAL），
// 其余指针开关（HolidayBlockNew/RiskBudgetKeepOn/PowerP）仍由各自访问器解析。
// 引擎装配 Regime 时调用一次，signalctl 侧因此可直接读平字段、无需逐字段兜默认。
// English: returns a copy with every zero numeric field defaulted and every map merged over the
// defaults; the Enabled/Shadow delivery switches take the factory value only when the key is absent
// (§DYN-GLOBAL), other pointer switches keep their own *On accessors. Called once by the engine.
func (s SizingConfig) WithDefaults() SizingConfig {
	d := DefaultSizingConfig()
	out := s
	if strings.TrimSpace(out.BudgetMode) == "" {
		out.BudgetMode = d.BudgetMode
	}
	// §DYN-GLOBAL：两把指针开关的"键缺失"态必须补成出厂值。不补的话存量/新建载荷里
	// Shadow==nil 会永远读成影子开，出厂默认 Shadow=false 就成了写在纸上却走不到的分支。
	// 显式写过的值（含显式 false/true）一律原样保留——那是账号自己的选择。
	// English: absent pointer switches inherit the factory defaults; explicit values are kept.
	if out.Enabled == nil {
		out.Enabled = d.Enabled
	}
	if out.Shadow == nil {
		out.Shadow = d.Shadow
	}
	if strings.TrimSpace(out.Mode) == "" {
		out.Mode = d.Mode
	}
	if out.PowerP == nil {
		out.PowerP = d.PowerP
	}
	out.GroupFloors = mergeScales(d.GroupFloors, out.GroupFloors)
	if out.Floor <= 0 {
		out.Floor = d.Floor
	}
	if out.Cap <= 0 {
		out.Cap = d.Cap
	}
	if out.KellyLambda <= 0 {
		out.KellyLambda = d.KellyLambda
	}
	if out.KellyMinProb <= 0 {
		out.KellyMinProb = d.KellyMinProb
	}
	if out.VolTargetPct <= 0 {
		out.VolTargetPct = d.VolTargetPct
	}
	if out.VolClampLo <= 0 {
		out.VolClampLo = d.VolClampLo
	}
	if out.VolClampHi <= 0 {
		out.VolClampHi = d.VolClampHi
	}
	if out.BreadthMissingScale <= 0 {
		out.BreadthMissingScale = d.BreadthMissingScale
	}
	out.EmotionScales = mergeScales(d.EmotionScales, out.EmotionScales)
	out.StateScales = mergeScales(d.StateScales, out.StateScales)
	out.TierScales = mergeScales(d.TierScales, out.TierScales)
	out.MacroWindowScales = mergeScales(d.MacroWindowScales, out.MacroWindowScales)
	out.HolidayScales = mergeScales(d.HolidayScales, out.HolidayScales)
	out.IndexScaleThresholds = mergeScales(d.IndexScaleThresholds, out.IndexScaleThresholds)
	if out.MacroLeadDays <= 0 {
		out.MacroLeadDays = d.MacroLeadDays
	}
	if len(out.MacroLeadCurve) == 0 {
		out.MacroLeadCurve = d.MacroLeadCurve
	}
	if out.MacroEventDayScale <= 0 {
		out.MacroEventDayScale = d.MacroEventDayScale
	}
	if out.LLMBearishScale <= 0 {
		out.LLMBearishScale = d.LLMBearishScale
	}
	if out.MaxTotalPositionPct <= 0 {
		out.MaxTotalPositionPct = d.MaxTotalPositionPct
	}
	if out.MaxDrawdownPct <= 0 {
		out.MaxDrawdownPct = d.MaxDrawdownPct
	}
	if out.StoplossGuardCount <= 0 {
		out.StoplossGuardCount = d.StoplossGuardCount
	}
	if out.StoplossGuardWindowDays <= 0 {
		out.StoplossGuardWindowDays = d.StoplossGuardWindowDays
	}
	if out.SLOffsetPerPct <= 0 {
		out.SLOffsetPerPct = d.SLOffsetPerPct
	}
	if out.SLOffsetCap <= 0 {
		out.SLOffsetCap = d.SLOffsetCap
	}
	if out.TPOffsetPerPct <= 0 {
		out.TPOffsetPerPct = d.TPOffsetPerPct
	}
	if out.TPOffsetCap <= 0 {
		out.TPOffsetCap = d.TPOffsetCap
	}
	if out.SLOffsetMinIndexDrop <= 0 {
		out.SLOffsetMinIndexDrop = d.SLOffsetMinIndexDrop
	}
	if out.BearSLTightenPt <= 0 {
		out.BearSLTightenPt = d.BearSLTightenPt
	}
	if strings.TrimSpace(out.SellBoostLevel) == "" {
		out.SellBoostLevel = d.SellBoostLevel
	}
	return out
}

// mergeScales 把 override 覆盖到 base 之上（base 先拷贝，override 的键优先）；返回新映射，不改入参。
// English: overlays override onto a copy of base (override wins); returns a fresh map.
func mergeScales(base, override map[string]float64) map[string]float64 {
	out := make(map[string]float64, len(base)+len(override))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range override {
		out[k] = v
	}
	return out
}

// StrategyConfig 各策略的独立参数配置。
// （StrategyConfig holds the per-strategy parameter configuration.）
type StrategyConfig struct {
	// 龙头战法配置
	Dragon DragonConfig `json:"dragon"`
	// 双响炮战法配置
	DoubleBump DoubleBumpConfig `json:"double_bump"`
	// N 形战法配置
	NShape NShapeConfig `json:"n_shape"`
	// 龙回头战法配置
	DragonReturn DragonReturnConfig `json:"dragon_return"`
	// 动量分权重配置
	Momentum MomentumConfig `json:"momentum"`
	// 宏观利空门控配置
	MacroGate MacroGateConfig `json:"macro_gate"`
	// §SHORT-1 做空四战法配置（高位滞涨/放量破位/龙头断板/利好兑现砸盘）
	Short ShortStrategiesConfig `json:"short"`
	// UpdatedAt §中-6 配置版本戳（2026-09-22 傍晚批）：最近一次服务端写入时间（RFC3339Nano，UTC）。
	// 由 MergeStrategyConfig/SetStrategyConfig 统一盖戳，**客户端提交值不参与 merge**（只用于
	// 写请求的乐观锁比对）——两个管理员先后保存时，后写者版本对不上即回 409，不再静默覆盖。
	// 空串=历史配置尚无版本，首个写请求跳过比对并补盖。
	// English: §mid-6 server-stamped version of the strategy config; POST bodies echo it back for
	// optimistic-locking (mismatch → 409 instead of silent last-write-wins overwrite).
	UpdatedAt string `json:"updated_at,omitempty"`
}

// ShortStrategiesConfig §SHORT-1 做空战法配置（docs/SHORT_STRATEGIES_PLAN_20260912.md）。
// 做空信号定位=卖出侧决策：受全局 short_enabled 门控，本配置为战法层参数。
// 阈值字段 ≤0 时走代码默认（60/60/65/60），保证旧配置零迁移兼容。
// （ShortStrategiesConfig configures the four bear-side tactics; zero thresholds fall back to code
// defaults so existing configs need no migration.）
type ShortStrategiesConfig struct {
	// Enabled 做空战法层开关（缺省 nil=启用；实际还受全局 short_enabled 总闸门控）
	Enabled *bool `json:"enabled"`
	// HighChurnMin 高位滞涨通过门槛（默认 60）
	HighChurnMin float64 `json:"high_churn_min"`
	// BreakDownMin 放量破位通过门槛（默认 60）
	BreakDownMin float64 `json:"break_down_min"`
	// LeaderDecayMin 龙头断板通过门槛（默认 65，四战法最严——断板有反包风险）
	LeaderDecayMin float64 `json:"leader_decay_min"`
	// GoodNewsFadeMin 利好兑现砸盘通过门槛（默认 60）
	GoodNewsFadeMin float64 `json:"good_news_fade_min"`
	// MarginRate 模拟盘融券开仓保证金率（默认 0.5=50%，占用=数量×开仓价×保证金率）
	MarginRate float64 `json:"margin_rate"`
	// AnnualFeeRate 融券年化费率（默认 0.083=8.3%，按自然日对开仓名义额计提）
	AnnualFeeRate float64 `json:"annual_fee_rate"`
}

// ShortTacticsEnabled 返回做空战法层开关（nil=默认启用）。
// （ShortTacticsEnabled reports the tactic-layer switch; nil defaults to enabled.）
func (s *ShortStrategiesConfig) ShortTacticsEnabled() bool {
	return s.Enabled == nil || *s.Enabled
}

// MacroGateConfig 宏观利空门控（E1）：股指期货交割日等高影响宏观事件作为整体利空，
// 当日处于影响期时买入信号统一降级，仅超高置信度（"特别高质量信号"）放行。
// 默认：交割日（contract）开启门控，放行置信度 ≥0.85，N 形超短在交割日一律 watch。
// （MacroGateConfig configures the E1 macro bearish gate: on contract-delivery (交割日) or other
// high-impact macro days, buy signals are downgraded as a whole unless they are exceptionally
// high-confidence ("特别高质量信号"). Defaults: gate on for contract days, allow-through confidence
// ≥0.85, and N-shape ultra-short is always watch on delivery days.）
type MacroGateConfig struct {
	// Enabled 总开关（默认 false：未配置时行为不变，保证向后兼容）。
	// English: master switch (default false — no config means no behavior change, backward compatible).
	// 是否启用
	Enabled bool `json:"enabled"`
	// Levels 触发门控的宏观事件级别（如 ["contract"]）；空时默认 ["contract"]。
	// English: macro-event levels that trigger the gate (e.g. ["contract"]); empty defaults to ["contract"].
	// 触发门控的宏观事件级别列表
	Levels []string `json:"levels"`
	// MinConfidence 放行买入信号的最低置信度（0~1，默认 0.85；低于此置信度的买入降级为 watch）。
	// English: minimum confidence for a buy signal to pass the gate (0~1, default 0.85); buys below are downgraded to watch.
	// 宏观门控放行买入最低置信度
	MinConfidence float64 `json:"min_confidence"`
	// BlockNShape 交割日是否对 N 形超短一律拦截（默认 true：超短对交割日波动最敏感）。
	// nil 表示使用默认 true；显式 false 才取消拦截。
	// English: whether N-shape ultra-short is always blocked on delivery days (default true — ultra-short is most sensitive to delivery-day swings). nil means default true; only an explicit false disables it.
	// 交割日是否拦截 N 形超短
	BlockNShape *bool `json:"block_n_shape,omitempty"`
	// BlockMomentum 交割日是否拦截动量 watch 观察信号（默认 true）。
	// nil 表示使用默认 true；显式 false 才取消拦截。
	// English: whether the momentum watch signal is also blocked on delivery days (default true). nil means default true; only an explicit false disables it.
	// 交割日是否拦截动量观察信号
	BlockMomentum *bool `json:"block_momentum,omitempty"`
	// RedBlockNewBuy Red 档是否禁止一切新开仓买入（默认 false：owner 2026-10-21 裁决「更倾向提高
	// 信号门槛收束，而不是直接全部拒绝」）。true 时 Red 日全部 buy（含龙头/N 形/策略）在**信号生成层**
	// 降级 watch；执行层不感知该开关，仍只执行信号给出的动作。
	// English: Red-day new-buy kill switch (default false — the owner prefers raising signal thresholds
	// over blanket rejection). When true, every buy is degraded to watch at the signal-generation layer;
	// execution stays dumb and only acts on the final action.
	// Red 档禁止新开仓
	RedBlockNewBuy *bool `json:"red_block_new_buy,omitempty"`

	// ── §MARKET_RISK_GATE P3 风险档扩展（全部向后兼容：未配置即取下方默认）──

	// RiskGateEnabled 分层风险档总开关（裁决④/§九：默认 true=上线即保护）。
	// nil 或 true=启用 Red/Yellow 风险档收紧；显式 false=应急一键回退（风险档不参与信号降级，退回旧 E1 纯宏观门控语义）。
	// English: master switch for the tiered risk gate. nil/true = Red/Yellow tightening active (protection on by default); an explicit false = emergency rollback to the old E1 pure-macro-gate behavior.
	// 风险档总开关
	RiskGateEnabled *bool `json:"risk_gate_enabled,omitempty"`
	// EmotionEnabled 情绪相位是否参与风险档合成（裁决④：默认 true）。false 时仅宏观事件+市场状态定档，
	// 冰点/退潮/背离不再触发降级——这是"情绪维度误伤"的独立热回退阀。
	// English: whether the emotion phase feeds the tier. false = only macro events + market state decide, i.e. an independent hot-rollback for over-eager emotion downgrades.
	// 情绪相位参与风险档开关
	EmotionEnabled *bool `json:"emotion_enabled,omitempty"`
	// EmotionLevels 参与风险档合成的情绪相位集合（默认 ["冰点","退潮","背离"]）。
	// EmotionLevels emotion phases that feed tier synthesis (default ice/retreat/divergence).
	// 参与合成的情绪相位列表
	EmotionLevels []string `json:"emotion_levels,omitempty"`
	// YellowConfidence Yellow 档放行买入的最低置信度（默认 0.90；低于此降级 watch）。
	// YellowConfidence minimum buy confidence to pass a Yellow day (default 0.90).
	// Yellow 档放行置信度门槛
	YellowConfidence float64 `json:"yellow_confidence,omitempty"`
	// RedConfidence Red 档放行买入的最低置信度（默认 0.92；系统性风险日门槛更高）。
	// RedConfidence minimum buy confidence to pass a Red day (default 0.92 — stricter on systemic-risk days).
	// Red 档放行置信度门槛
	RedConfidence float64 `json:"red_confidence,omitempty"`
	// BreadthWeakUpRatio "震荡且上涨占比偏弱"判定的上涨占比阈值（默认 0.40；range 且 up/(up+down) < 此值 → Yellow）。
	// 家数缺失（upRatio=NaN）时该条件弃权，不触发。
	// BreadthWeakUpRatio up-share threshold for the "range + weak breadth" Yellow trigger (default 0.40); abstains when the ratio is NaN (counts missing).
	// range 弱势广度阈值
	BreadthWeakUpRatio float64 `json:"breadth_weak_up_ratio,omitempty"`
	// MacroSectorMap 板块级映射（中观层，差距5）：宏观事件级别/情绪 → 受影响板块名列表；
	// 风险日命中这些板块的买入信号门槛再上浮一档（Yellow→按 Red 对待）。默认空=板块层不生效（显式配置才启用，避免上线即误伤）。
	// MacroSectorMap maps a macro level / emotion to affected sector names; on risk days buys in those
	// sectors take one tier stricter (Yellow→treated as Red). Default empty = sector layer off until configured.
	// 板块级风险映射
	MacroSectorMap map[string][]string `json:"macro_sector_map,omitempty"`
	// HighImpactLevels 视为"高影响"的宏观事件级别（默认 ["cpi","fomc","nfp"]；用于 Red 的"高影响事件×退潮/背离"组合与 Yellow）。
	// HighImpactLevels event levels treated as high-impact (default cpi/fomc/nfp) for the Red combo and Yellow.
	// 高影响宏观事件级别列表
	HighImpactLevels []string `json:"high_impact_levels,omitempty"`
	// ContractLevel 股指期货交割日的事件级别名（默认 "contract"）——Red 的"交割日当日"与 Yellow 的"交割影响期"据此识别。
	// ContractLevel is the event-level name for index-futures delivery day (default "contract").
	// 交割日事件级别名
	ContractLevel string `json:"contract_level,omitempty"`
	// CalibrateEnabled §MARKET_RISK_GATE P6 日历校准开关（默认 true）：true=每日一次用外部API/LLM 校准
	// CPI/FOMC/NFP/交割日真实发布日（失败静默降级公式，不阻断）；false=纯公式估算（行为=校准前）。
	// nil 视为默认 true。
	// English: P6 calendar-calibration switch (nil/true = calibrate real CPI/FOMC/delivery dates once per
	// day via external API/LLM with silent formula fallback; false = formula-only, the pre-calibration behavior).
	// 日历校准开关
	CalibrateEnabled *bool `json:"calibrate_enabled,omitempty"`
	// CalibrateMonths 校准前瞻月数（默认 3，仅取未来 N 个月事件节省 LLM token）。
	// CalibrateMonths is the forward horizon in months for calibration (default 3).
	// 校准前瞻月数
	CalibrateMonths int `json:"calibrate_months,omitempty"`
	// CalibrateAPIURL 外部宏观日历 API（返回 JSON 事件数组；空=不用 API，仅走 LLM）。
	// CalibrateAPIURL is an optional external macro-calendar API (JSON array); empty = LLM only.
	// 外部日历API地址
	CalibrateAPIURL string `json:"calibrate_api_url,omitempty"`
	// WarnDays §三 作用层6（差距4）前瞻预警窗口：距高影响事件（交割日/CPI/FOMC/NFP）≤ 该天数时，
	// 每日出一条"临近高影响事件"提醒（默认 3；<=0 回退 3，上限 7）。
	// English: the P5 forward-warning window — when a high-impact event (delivery/CPI/FOMC/NFP) is at most
	// this many days away (default 3, clamped 1~7), a daily "upcoming event" reminder is emitted.
	// 前瞻预警窗口天数
	WarnDays int `json:"warn_days,omitempty"`
}

// WarnWindow 前瞻预警窗口（默认 3，夹在 1~7）。
func (m MacroGateConfig) WarnWindow() int {
	if m.WarnDays <= 0 {
		return 3
	}
	if m.WarnDays > 7 {
		return 7
	}
	return m.WarnDays
}

// CalibrateOn 日历校准是否启用（nil 默认 true）。
func (m MacroGateConfig) CalibrateOn() bool { return m.CalibrateEnabled == nil || *m.CalibrateEnabled }

// CalibrateHorizonMonths 校准前瞻月数（默认 3）。
func (m MacroGateConfig) CalibrateHorizonMonths() int {
	if m.CalibrateMonths <= 0 || m.CalibrateMonths > 12 {
		return 3
	}
	return m.CalibrateMonths
}

// ── §MARKET_RISK_GATE P3 默认值访问器（nil/零值 → 出厂默认，保证向后兼容且默认 ON）──

// RiskGateOn 风险档总开关（nil 默认 true）。
func (m MacroGateConfig) RiskGateOn() bool { return m.RiskGateEnabled == nil || *m.RiskGateEnabled }

// EmotionOn 情绪维度是否参与合成（nil 默认 true）。
func (m MacroGateConfig) EmotionOn() bool { return m.EmotionEnabled == nil || *m.EmotionEnabled }

// EmotionLevelSet 参与合成的情绪相位集合（默认 冰点/退潮/背离）。
func (m MacroGateConfig) EmotionLevelSet() map[string]bool {
	levels := m.EmotionLevels
	if len(levels) == 0 {
		levels = []string{"冰点", "退潮", "背离"}
	}
	set := make(map[string]bool, len(levels))
	for _, l := range levels {
		set[l] = true
	}
	return set
}

// YellowMinConf Yellow 档放行置信度（默认 0.90）。
func (m MacroGateConfig) YellowMinConf() float64 {
	if m.YellowConfidence <= 0 {
		return 0.90
	}
	return m.YellowConfidence
}

// RedMinConf Red 档放行置信度（默认 0.92）。
func (m MacroGateConfig) RedMinConf() float64 {
	if m.RedConfidence <= 0 {
		return 0.92
	}
	return m.RedConfidence
}

// RedBlockNewBuyOn Red 档是否禁止新开仓（nil 默认 false）。
// English: Red-day new-buy block (nil defaults to false).
func (m MacroGateConfig) RedBlockNewBuyOn() bool {
	return m.RedBlockNewBuy != nil && *m.RedBlockNewBuy
}

// WeakBreadthUpRatio range 弱势广度阈值（默认 0.40）。
func (m MacroGateConfig) WeakBreadthUpRatio() float64 {
	if m.BreadthWeakUpRatio <= 0 {
		return 0.40
	}
	return m.BreadthWeakUpRatio
}

// HighImpactSet 高影响事件级别集合（默认 cpi/fomc/nfp）。
func (m MacroGateConfig) HighImpactSet() map[string]bool {
	levels := m.HighImpactLevels
	if len(levels) == 0 {
		levels = []string{"cpi", "fomc", "nfp"}
	}
	set := make(map[string]bool, len(levels))
	for _, l := range levels {
		set[l] = true
	}
	return set
}

// ContractLevelName 交割日级别名（默认 contract）。
func (m MacroGateConfig) ContractLevelName() string {
	if m.ContractLevel == "" {
		return "contract"
	}
	return m.ContractLevel
}

// MomentumConfig 动量分权重配置（默认 量价40 + MACD30 + 走势30，合计≤100）。
// （MomentumConfig defines momentum-score weights; defaults: price-volume 40 + MACD 30 + trend 30, total ≤ 100.）
type MomentumConfig struct {
	// 量价分权重（0~100）
	VolumePriceWeight float64 `json:"volume_price_weight"`
	// MACD分权重（0~100）
	MACDWeight float64 `json:"macd_weight"`
	// 走势分权重（0~100）
	TrendWeight float64 `json:"trend_weight"`
	// 动量分触发信号阈值（默认 60）
	SignalThreshold float64 `json:"signal_threshold"`
	// BuySignalThreshold 动量买入阈值：动量分 ≥ 此值且数据有效时发 buy 级信号（经信号控制器
	// 白名单准入后归动量池撮合；动量永不在默认白名单全集内，需在战法开关面板显式开启）。
	// 默认 75（高于 watch 阈值 60 一档，避免动量信号大量直接转买单）；≤0 时回退默认。
	// English: momentum BUY threshold — score at/above this (with valid data) emits a buy signal that,
	// once admitted by the signal controller whitelist (momentum is never in the default set), routes
	// to the momentum pool. Default 75; <=0 falls back to default.
	// 动量买入阈值
	BuySignalThreshold float64 `json:"buy_signal_threshold"`
	// MomentumGateEnabled 动量分"提升才提醒"门槛开关：开启后仅当动量分明显提升时
	// 才放行 double_bump/龙头/龙回头 战法信号（N 形不套用）。可热更新，前端 Settings 动量分组内开关控制。
	// English: momentum-gate switch — when on, only a meaningful momentum-score improvement lets the
	// double-bump / dragon / dragon-return strategies pass their signal (N-shape is exempt).
	// MomentumGate是否启用
	MomentumGateEnabled bool `json:"momentum_gate_enabled"`
	// MomentumDeltaTol 动量分回落容忍差：当前动量分 ≥ 上一轮 − 该值 视为"未明显回落"，仍算提升。
	// 默认 5 分。English: momentum delta tolerance — current score >= prior - tolerance still counts as
	// an improvement (no obvious fall). Default 5.
	// 动量分回落容忍差
	MomentumDeltaTol float64 `json:"momentum_delta_tol"`
}

// DragonConfig 龙头战法参数：多因子权重、回撤止盈止损阈值、买入条件等。
// （DragonConfig tunes the dragon-leader strategy: multi-factor weights, drawdown/take-profit/stop-loss thresholds, buy conditions.）
type DragonConfig struct {
	// F1 封单强度权重
	F1SealWeight float64 `json:"f1_seal_weight"`
	// F2 板块共振权重
	F2ResonanceWeight float64 `json:"f2_resonance_weight"`
	// F3 溢价权重
	F3PremiumWeight float64 `json:"f3_premium_weight"`
	// F4 相对强度(RS)权重
	F4RsWeight float64 `json:"f4_rs_weight"`
	// 买入后最大回撤容忍比例
	PullbackMaxPct float64 `json:"pullback_max_pct"`
	// 破板跌幅达此值减半仓
	BreakerSellHalfPct float64 `json:"breaker_sell_half_pct"`
	// 破板跌幅达此值清仓
	BreakerSellAllPct float64 `json:"breaker_sell_all_pct"`
	// 买入后回撤减半仓阈值
	BuyPullbackSellHalfPct float64 `json:"buy_pullback_sell_half_pct"`
	// 买入后回撤清仓阈值
	BuyPullbackSellAllPct float64 `json:"buy_pullback_sell_all_pct"`
	// 买入日收盘低于买入价比例止损
	BuyDayCloseBelow float64 `json:"buy_day_close_below"`
	// 次日开盘低于此比例则卖出
	NextOpenIfBelow float64 `json:"next_open_if_below"`
	// 止盈比例(%)，浮盈达此值落袋（默认 10）
	TakeProfitPct float64 `json:"take_profit_pct"`
	// §扫参应用（STRATEGY_OPTIMIZE_PLAN）：移动止盈回撤%(从阶段高点)与最长持仓天数。
	// 0=不启用（保持既有退出规则不变）；>0 时由 CheckExit 在既有规则之前执行——
	// 与扫参的统一出场引擎同语义，寻优冠军参数可一键应用到实盘且口径一致。
	// English: sweep-aligned trailing-stop %% and max-hold-days knobs; 0 = disabled (legacy rules only).
	// 移动止盈回撤幅度(%)
	TrailingDrawbackPct float64 `json:"trailing_drawback_pct,omitempty"`
	// 最长持仓天数
	MaxHoldDays int `json:"max_hold_days,omitempty"`
}

// DoubleBumpConfig 双响炮战法参数：一突/二突放量倍数、调整周期、仓位比例等。
// （DoubleBumpConfig tunes the double-bump strategy: first/second breakout volume multiples, adjustment window, position ratios.）
type DoubleBumpConfig struct {
	// 一突放量倍数阈值
	FirstBreakVolumeMultiple float64 `json:"first_break_volume_multiple"`
	// 二突放量倍数阈值
	SecondBreakVolumeMultiple float64 `json:"second_break_volume_multiple"`
	// 调整期最大量比
	AdjustVolRatioMax float64 `json:"adjust_vol_ratio_max"`
	// 调整超期天数（超期判弱）
	AdjustDaysOverflow int `json:"adjust_days_overflow"`
	// 第二波当日最低涨跌幅(%)：<=该值判无效，水下不评买入
	MinChangePct float64 `json:"min_change_pct"`
	// 仓位因子权重
	PositionWeight float64 `json:"position_weight"`
	// 均线因子权重
	MAWeight float64 `json:"ma_weight"`
	// 量能因子权重
	VolumeWeight float64 `json:"volume_weight"`
	// 双响炮止盈比例
	DoubleBumpTakeProfitPct float64 `json:"double_bump_take_profit_pct"`
	// §扫参应用（STRATEGY_OPTIMIZE_PLAN）：移动止盈回撤%(从阶段高点)与最长持仓天数。
	// 0=不启用（保持既有退出规则不变）；>0 时由 CheckExit 在既有规则之前执行——
	// 与扫参的统一出场引擎同语义，寻优冠军参数可一键应用到实盘且口径一致。
	// English: sweep-aligned trailing-stop %% and max-hold-days knobs; 0 = disabled (legacy rules only).
	// 移动止盈回撤幅度(%)
	TrailingDrawbackPct float64 `json:"trailing_drawback_pct,omitempty"`
	// 最长持仓天数
	MaxHoldDays int `json:"max_hold_days,omitempty"`
}

// NShapeConfig N 形战法参数：D1~D4 评分阈值、旗形整理区间、突破量比等。
// （NShapeConfig tunes the N-shape strategy: D1-D4 score thresholds, flag-consolidation window, breakout volume ratios.）
type NShapeConfig struct {
	// N 形形态总分阈值
	NPatternScoreThreshold float64 `json:"n_pattern_score_threshold"`
	// 硬止损比例
	HardStopLoss float64 `json:"hard_stop_loss"`
	// §扫参应用（STRATEGY_OPTIMIZE_PLAN）：移动止盈回撤%(从阶段高点)与最长持仓天数。
	// 0=不启用（保持既有退出规则不变）；>0 时由 CheckExit 在既有规则之前执行——
	// 与扫参的统一出场引擎同语义，寻优冠军参数可一键应用到实盘且口径一致。
	// English: sweep-aligned trailing-stop %% and max-hold-days knobs; 0 = disabled (legacy rules only).
	// 移动止盈回撤幅度(%)
	TrailingDrawbackPct float64 `json:"trailing_drawback_pct,omitempty"`
	// 最长持仓天数
	MaxHoldDays int `json:"max_hold_days,omitempty"`
}

// DragonReturnConfig 龙回头战法参数：回调幅度、量缩比、止盈止损、持仓天数等。
// （DragonReturnConfig tunes the dragon-return strategy: pullback depth, volume-shrink ratio, take-profit/stop-loss, hold days.）
type DragonReturnConfig struct {
	// 止损比例
	StopLossPct float64 `json:"stop_loss_pct"`
	// 止盈比例
	TakeProfitPct float64 `json:"take_profit_pct"`
	// 最长持仓天数
	MaxHoldDays int `json:"max_hold_days"`
	// 目标价 1 倍数
	Target1Multiplier float64 `json:"target1_multiplier"`
	// 目标价 2 倍数
	Target2Multiplier float64 `json:"target2_multiplier"`
	// 移动止盈回撤幅度
	TrailingDrawback float64 `json:"trailing_drawback"`
}

// D1Rule D1 事件匹配规则：模式匹配、方向、评分、是否阻断。
// （D1Rule is an event-matching rule for D1 scoring: pattern, direction, score and block flag.）
type D1Rule struct {
	// 方向：利好/利空
	Direction string `json:"direction"`
	// 匹配得分
	Score float64 `json:"score"`
	// 是否阻断（负面事件）
	Blocked bool `json:"blocked,omitempty"`
}

// D1Config D1 事件匹配规则集 + 四战法软加成配置。
// （D1Config is the set of D1 event-matching rules plus the C1 cross-strategy soft-boost settings.）
type D1Config struct {
	// D1 规则列表
	Rules []D1Rule `json:"rules"`

	// BoostWeight 四战法 D1 软加成权重（C1）：非 N 战法总分 ×(1+BoostWeight×D1/40)，封顶 100。
	// ≤0 表示关闭加成（默认 0.15）。
	// （BoostWeight is the C1 soft-boost weight applied to non-N strategy totals; ≤0 disables it.）
	// 四战法 D1 软加成权重
	BoostWeight float64 `json:"boost_weight,omitempty"`
	// BoostThreshold 加成门槛：D1 分（0~40）低于该值时不做加成（默认 8）。
	// （BoostThreshold is the minimum D1 score (0~40) to trigger the soft boost.）
	// D1 软加成门槛
	BoostThreshold float64 `json:"boost_threshold,omitempty"`
}

// normalizeD1 填充 D1Config 缺失的默认值（零值视为未配置）。
// （normalizeD1 fills D1Config defaults when fields are left at zero.）
func normalizeD1(d *D1Config) {
	if d.BoostWeight <= 0 {
		d.BoostWeight = 0.15
	}
	if d.BoostThreshold <= 0 {
		d.BoostThreshold = 8
	}
}

// KVStore 配置持久化抽象：按 userID 读写任意 key-value。
// 由 auth.Manager 实现（其 auth.json 已支持 per-user 配置项），使 config.Manager
// 可为每个账号保存独立的 Rules/D1 快照，实现多账号多配置。
// （KVStore abstracts per-user key-value persistence, implemented by auth.Manager so that
// config.Manager can keep an independent Rules/D1 snapshot per account.）
type KVStore interface {
	// SetConfig 写入某账号指定 key 的配置值。
	SetConfig(userID, key, value string) error
	// GetConfig 读取某账号指定 key 的配置值（不存在时 ok=false）。
	GetConfig(userID, key string) (string, bool)
}

// perUserKey 每账号配置在 KVStore 中的键。
// （perUserKey is the KVStore key holding a per-account config snapshot.）
const perUserKey = "quant_config_json_v1"

// perUserD1Key 每账号 D1 规则在 KVStore 中的键。
// （perUserD1Key is the KVStore key holding a per-account D1 rules snapshot.）
const perUserD1Key = "quant_config_d1_v1"

// perUserLongShortKey 每账号做多/做空开关在 KVStore 中的键。
// （perUserLongShortKey is the KVStore key holding a per-account long/short toggle snapshot.）
const perUserLongShortKey = "quant_config_longshort_v1"

// perUserSizingKey §A0（2026-10-22）账号级仓位配置专用键。
// 只存 SizingConfig 原始值，不写完整 Rules，避免把该账号其余配置一并冻结。
// English: §A0 account-scoped sizing config key — stores SizingConfig only, never a full Rules
// snapshot, so saving sizing cannot freeze the account's other config sections.
const perUserSizingKey = "quant_config_sizing_v1"

// LongShortConfig 每账号做多/做空开关状态。
// （LongShortConfig holds the per-account long/short toggle state.）
type LongShortConfig struct {
	// 做多开关（默认开）
	LongEnabled bool `json:"long_enabled"`
	// 做空开关（默认关）
	ShortEnabled bool `json:"short_enabled"`
}

// Manager 配置管理器，负责 JSON 配置文件的加载、保存和查询。
// 全局默认配置来自文件；每个账号可在 KVStore 中保存独立覆盖（多账号多配置）。
// （Manager is the config manager responsible for loading, saving and querying the JSON config file.
// Global defaults come from the file; each account may store its own override in the KVStore.）
type Manager struct {
	// §0925EVE-D1（2026-09-25 晚批）：Rules/D1 由导出字段转私有（rules/d1），指针的发布
	// （Load 热重载 / SetD1Config 族整体替换）与读取统一收进 m.mu 保护的访问器
	// （Get / GetD1Config / GetRulesFor 等）。旧写法下 Watch 后台协程调 Load() 无锁裸重赋值
	// m.Rules、m.D1 指针、读方（打分循环、HTTP handler、cmd 装配）同样无锁取值，是锤实的
	// data race——§CFGSMASH 那轮修的是 Strategy 族热更通道，Load/Watch 这条没进扫描面。
	// 锁口径沿用 §CFGSMASH 既有约定：m.mu 只保护**指针替换与结构体读写**；跨 goroutine
	// 长期持有活体指针再逐字段读的通道，热字段一律走拷贝式 getter（GetStrategyConfig 等）。
	// English: rules/d1 are now unexported and every pointer publish/read goes through
	// m.mu-protected accessors, closing the Watch(Load) vs readers data race.
	rules *Rules
	// D1 事件匹配规则
	d1    *D1Config
	path  string       // 配置文件路径（全局默认）
	mu    sync.RWMutex // 保护 store/规则指针的读写锁
	store KVStore      // per-user 配置存储（可为 nil，表示不支持账号级隔离）
	// operatorID 运营数据归属账号（管理员）ID。所有运营数据（量化/模拟盘/看板/告警/LLM）
	// 统一归属该账号，后端按角色做访问控制：管理员可读写，子账号仅可读公开部分。
	// 由 main 在启动后注入 auth.AdminID()。为空时回退到调用方传入的 userID（兼容旧路径）。
	operatorID string
}

// cloneDefaultRules 给每个 Manager 一份**独立**的出厂规则实例。
// §DEFECT-CLONE（2026-10-21 §N6-RISK 测试批锤出）：此前 NewManager 直接把进程级全局
// `DefaultRules` 指针存进实例，而全部 setter 都是 `m.rules.<段> = next` 这种**字段重绑**
// （strategy/llm/qmt/scheduler/notify/enhance/sizing 共 10 处）——配置文件不存在时 Load()
// 会原样保留这个别名指针，于是第一次配置写就把"出厂默认表"本身改掉了：同进程里后建的
// Manager 会把自己的起点认成"别人刚写的值"，测试串跑时前一条用例写 Red 权重 0.25、后一条
// 用例读到的"默认"就变成 0.25（单跑各自绿、串跑必红＝跨实例状态泄漏的指纹）。
// 浅拷贝足够：全仓没有任何就地改写 rules 内层 map/slice 的写法（已核），字段重绑只动
// 实例自己那份结构体。
// English: each Manager now owns a private copy of the factory rules. The old code aliased the
// process-global DefaultRules pointer, so the first write on a fresh install mutated the factory
// defaults themselves and leaked state into every later manager in the same process.
func cloneDefaultRules() *Rules {
	cp := *DefaultRules
	return &cp
}

// NewManager 创建配置管理器，加载指定路径的 JSON 配置文件。
// （NewManager creates a config manager and loads the JSON config from the given path.）
func NewManager(path string) *Manager {
	m := &Manager{
		rules: cloneDefaultRules(),
		d1:    &D1Config{},
		path:  path,
	}
	normalizeD1(m.d1)
	m.Load()
	normalizeD1(m.d1)
	return m
}

// NewManagerWithRules 以调用方给定的全局规则快照构造 Manager（D1 用出厂默认）。
// §0925EVE-D1：rules/d1 转私有后，测试与程序化装配不再直接构造 config.Manager{Rules:...}
// 字面量，统一经本构造函数注入快照，保证读侧只有一个口径（Get/GetD1Config）。
// English: builds a Manager from a caller-supplied global rules snapshot — the sanctioned
// programmatic/test constructor now that the fields are unexported.
func NewManagerWithRules(rules *Rules) *Manager {
	if rules == nil {
		rules = cloneDefaultRules()
	}
	d1 := &D1Config{}
	normalizeD1(d1) // 与 NewManager 同口径：空 D1 补出厂规则集
	return &Manager{rules: rules, d1: d1}
}

// SetStore 注入 per-user 配置存储（auth.Manager）。
// （SetStore injects the per-user config store, i.e. the auth.Manager.）
func (m *Manager) SetStore(s KVStore) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.store = s
}

// SetOperatorID 设置运营数据归属账号（管理员）ID。注入后，量化/模拟盘等运营配置统一
// 归属该账号，后端按角色鉴权：管理员可读写，子账号按权限仅可读公开部分。
// （SetOperatorID sets the operator (admin) account that owns all operational data.）
func (m *Manager) SetOperatorID(id string) {
	m.mu.Lock()
	m.operatorID = id
	m.mu.Unlock()
}

// ownerOf 返回运营数据归属账号：已注入 operatorID 时优先，否则回退到调用方传入的 userID。
// 这是“运营数据系统级共享、后端按角色鉴权”的核心：所有账号看到的量化/模拟盘/看板/告警/LLM
// 都是同一份（归属管理员），前端只负责展示，权限由后端在接口层判定。
// （ownerOf resolves the operational-data owner: injected operatorID first, else the caller's userID.）
func (m *Manager) ownerOf(userID string) string {
	if m.operatorID != "" {
		return m.operatorID
	}
	return userID
}

// userRules 返回指定账号的规则快照；未配置账号级覆盖时回退全局 Rules。
// 快照来自 KVStore 中的 JSON，反序列化为副本，避免污染全局。
// （userRules returns the rules snapshot for a user, falling back to global Rules when
// the account has no override; the snapshot is a deserialized copy.）
// userRules 返回账号级规则快照（账号级覆盖优先，否则返回全局配置的堆副本）。
// 关键不变量：
//  1. 始终返回堆分配的 *Rules，避免调用方通过 &userRules(userID).X 取到悬垂指针（此前反序列化分支返回局部变量地址，属未定义行为）。
//  2. 无账号级覆盖时返回全局 m.Rules 的副本而非其地址，避免 SetXxxConfigFor 改账号级配置时副作用改写全局（§全局指针副作用）。
//
// English: returns the account's rules snapshot (per-user override first, else a heap copy of global).
// Invariants: (1) always heap-allocated so &userRules(userID).X is never dangling; (2) when there is no
// per-user override we return a COPY of global (not its address) so account-scoped setters never mutate the
// global rules as a side effect.
// storedUserRules 读取账号自身已持久化的规则快照（不含回退）；无覆盖或解析失败返回 (nil,false)。
// §2026-09-07 多账号实盘：供 GetQMTConfigFor 等区分「账号自身覆盖」与「回退」，实现逐账号实盘配置。
// English: reads an account's own persisted rules snapshot without fallbacks — lets per-account
// readers (e.g. QMT config) tell "account override exists" apart from "fall back".
func (m *Manager) storedUserRules(userID string) (*Rules, bool) {
	if m.store == nil || userID == "" {
		return nil, false
	}
	m.mu.RLock()
	raw, ok := m.store.GetConfig(userID, perUserKey)
	m.mu.RUnlock()
	if !ok || raw == "" {
		return nil, false
	}
	r := new(Rules)
	if err := json.Unmarshal([]byte(raw), r); err != nil {
		log.Printf("[config] 账号 %s 配置反序列化失败, 回退全局: %v", userID, err)
		return nil, false
	}
	return r, true
}

// userRules 返回指定账号的规则快照（账号级覆盖优先，否则回退系统级键/全局副本）。
func (m *Manager) userRules(userID string) *Rules {
	if m.store == nil || userID == "" {
		return m.Get()
	}
	if r, ok := m.storedUserRules(userID); ok {
		return r
	}
	// §回退系统级覆盖：历史版本曾把账号级配置写到 userID="" 的键下。
	// 若本账号无独立覆盖，则回退到系统级键，避免配置在重启/重载后“丢失”
	// （表现为开关被自动关闭）。这属于兼容回退，不影响正常账号级覆盖优先级。
	// English: fall back to the system-level (empty userID) override so legacy
	// configs written under "" are still honored and survive restarts.
	if r, ok := m.storedUserRules(""); ok {
		return r
	}
	cp := new(Rules)
	m.mu.RLock() // §0925EVE-D1：拷贝全局快照须在 RLock 内，与 Load 的指针发布配对
	*cp = *m.rules
	m.mu.RUnlock()
	return cp
}

// setConfigVerifiedLocked §0926E2E-W1B（2026-09-26 全量审计批，owner 令按推荐执行）：
// 账号级 KV 持久化的**统一自证出口**——SetConfig 写完立刻 GetConfig 复读并逐字节比对，
// 写失败或复读不匹配一律返回 error。背景：旧 saveUserRules/SetD1/SetLongShort 三处都是
// "SetConfig 出错只 log 照回 200"，前端显示"已保存"、磁盘没落——正是 §ROBUST 明令消灭的
// "降级报成功"家族；口径仿 §0926ROT 写后复读自证。
// 锁纪律：调用方必须已持有 m.mu 写锁（SetConfig/GetConfig 都走 store 自身互斥，
// 且经 m.mu 与读路径 RLock 互斥；本函数绝不再取 m.mu，防自死锁）。
// English: single verified-write exit for per-user KV persistence — re-read and
// byte-compare right after SetConfig; any mismatch is an error (killing the
// "swallow failure, still answer 200" family). Caller must already hold m.mu.
func (m *Manager) setConfigVerifiedLocked(userID, key string, data []byte) error {
	if err := m.store.SetConfig(userID, key, string(data)); err != nil {
		return fmt.Errorf("持久化失败: %w", err)
	}
	raw, ok := m.store.GetConfig(userID, key)
	if !ok {
		return fmt.Errorf("写后复读缺失该键（key=%s）", key)
	}
	if raw != string(data) {
		return fmt.Errorf("写后复读与写入不一致（key=%s，写入 %d 字节/复读 %d 字节）", key, len(data), len(raw))
	}
	return nil
}

// saveUserRules 将账号规则快照持久化到 KVStore，并做写后复读自证（§0926E2E-W1B 起返回 error：
// 序列化失败/落库失败/复读不匹配都不再吞错，调用方必须把失败如实透出到 HTTP 500）。
// （saveUserRules persists an account's rules snapshot to the KVStore with read-back verification;
// since §0926E2E-W1B it returns error instead of logging-and-forgetting.）
func (m *Manager) saveUserRules(userID string, r *Rules) error {
	if m.store == nil || userID == "" {
		return nil
	}
	data, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("账号 %s 配置序列化失败: %w", userID, err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.setConfigVerifiedLocked(userID, perUserKey, data); err != nil {
		return fmt.Errorf("账号 %s 配置%w", userID, err)
	}
	return nil
}

// AccountKVRaw 返回账号级 KV 里某个配置键的**原始字节**与其是否存在
// （§0929CFG-HIST：账号级配置的写前快照/字段级 diff 必须拿原始文档，不能拿反序列化再
// 序列化过的副本——那样"快照"与"当前值"会因字段顺序/缺省省略而产生一堆假差异行）。
// English: raw bytes of one account-level KV config document (for snapshot/diff), so the two
// compared sides are byte-homomorphic rather than re-serialized copies.
func (m *Manager) AccountKVRaw(userID, key string) ([]byte, bool) {
	if m.store == nil || userID == "" || key == "" {
		return nil, false
	}
	m.mu.RLock()
	raw, ok := m.store.GetConfig(userID, key)
	m.mu.RUnlock()
	if !ok || raw == "" {
		return nil, false
	}
	return []byte(raw), true
}

// SetAccountKVRaw 原样写入账号级某个配置键（回滚恢复专用），带写后复读自证。
// 与 saveUserRules / SetD1ConfigFor 的区别只在"不再序列化"：回滚必须把快照字节原样放回，
// 否则回滚本身就成了一次改写。写失败/复读不匹配一律返回 error 透出（同 §0926E2E-W1B 口径）。
// English: writes exact bytes into one account KV slot with read-back verification — used by
// rollback, where re-serializing would itself be a mutation.
func (m *Manager) SetAccountKVRaw(userID, key string, data []byte) error {
	if m.store == nil {
		return fmt.Errorf("账号级配置存储不可用")
	}
	if userID == "" || key == "" {
		return fmt.Errorf("需要账号与配置键")
	}
	// 先验 JSON 合法：回滚载荷来自磁盘，坏文件不该把该键整键写坏
	// （读侧解析失败会静默回退全局，用户看到的是"回滚没生效"而不是"回滚失败"）。
	var probe any
	if err := json.Unmarshal(data, &probe); err != nil {
		return fmt.Errorf("快照内容不是合法 JSON: %w", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.setConfigVerifiedLocked(userID, key, data); err != nil {
		return fmt.Errorf("账号 %s 配置键 %s 回滚%w", userID, key, err)
	}
	return nil
}

// Get 返回当前全局规则配置指针。
// §0925EVE-D1：本方法是 rules 指针的**唯一全局读口径**（原导出字段 Rules 转私有），
// RLock 只保证「取到的指针」与 Watch/Load 的指针发布无竞态；返回的仍是活体指针，
// 跨 goroutine 逐字段读热更新敏感字段请继续走 §CFGSMASH 的拷贝式 getter。
// English: the single accessor for the global rules pointer, read under RLock so it can
// never race with hot-reload publication.
func (m *Manager) Get() *Rules {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.rules
}

// GetRulesFor 返回指定账号的交易规则快照（账号级覆盖优先，否则全局）。
// English: returns the trading-rules snapshot for a user (per-user override first, else global).
func (m *Manager) GetRulesFor(userID string) *Rules {
	if m.store == nil || userID == "" {
		return m.Get() // §0925EVE-D1：统一走加锁访问器，不再裸读字段
	}
	return m.userRules(userID)
}

// GetStrategyConfigFor 返回运营数据归属账号（管理员）的策略参数（运营配置系统级共享）。
// §N-4/§CFGSMASH-concurrency（2026-09-22 傍晚批）：与 GetStrategyConfig 同口径，返回**快照
// 拷贝的指针**而非内部活体——store 未注入时 userRules 直接回别名 m.Rules，旧写法等于把全局
// Rules.Strategy 的活体指针交给打分循环跨 goroutine 持有。拷贝语义钉死在方法上，调用方零改动。
// English: per-owner strategy config, returned as a pointer to a snapshot COPY (never the live
// internal field) so long-held pointers cannot race the config writers.
func (m *Manager) GetStrategyConfigFor(userID string) *StrategyConfig {
	r := m.userRules(m.ownerOf(userID))
	cp := r.Strategy
	return &cp
}

// SetStrategyConfigFor 更新运营数据归属账号（管理员）的策略参数并持久化（系统级共享）。
// §CFGSMASH-concurrency：无 store 的 fallback 分支旧实现裸写 m.Rules.Strategy 不持 m.mu
// （三轮补强锤实的不对称锁遗漏），现补锁 + 统一盖 §中-6 版本戳。HTTP 管理端整份 POST 的
// 全量替换语义暂保留（见 N-4 报告的同族漏网清单），但不再是无锁裸写。
// English: per-owner full-replace writer; the no-store fallback branch now takes m.mu (it was an
// unguarded cross-goroutine write) and stamps the mid-6 version.
// §0926E2E-W1B：返回 error（持久化失败如实透出，HTTP 端点据此回 500）。
func (m *Manager) SetStrategyConfigFor(userID string, cfg *StrategyConfig) error {
	if cfg == nil {
		return fmt.Errorf("策略配置为空，未保存")
	}
	oid := m.ownerOf(userID)
	next := *cfg
	next.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano) // §中-6 任何服务端写入都推进版本
	if m.store == nil || oid == "" {
		m.mu.Lock()
		m.rules.Strategy = next
		m.mu.Unlock()
		return m.Save()
	}
	r := m.userRules(oid)
	r.Strategy = next
	return m.saveUserRules(oid, r)
}

// GetLLMConfigFor 返回运营数据归属账号（管理员）的 LLM 配置（运营配置系统级共享）。
//
// §警告 返回的是**内部结构的指针**，不是拷贝：调用方必须**立即取值拷贝**再使用，
// 更不能通过它写字段（那样会绕过持久化，只在内存里生效，进程重启即丢）。
// 需要改配置一律走 SetLLMConfigFor（传值拷贝进去）。
// English: returns a pointer INTO the live config — copy the value out immediately and never
// mutate through it; all writes must go through SetLLMConfigFor.
func (m *Manager) GetLLMConfigFor(userID string) *LLMConfig {
	return &m.userRules(m.ownerOf(userID)).LLM
}

// ConfigOwnerID 返回该账号的运营数据归属账号：所有账号的 LLM / 量化 / 看板配置都写入
// 归属账号名下（见 ownerOf）。管理端在改某个账号的 LLM 配置前必须据此判断"这次写入到底
// 会不会改变全局运行时配置"——归属账号就是运营账号时，它与设置页改的是同一份配置，
// 就必须走同一套「探测 → 热切换 → 落库」，否则会出现"管理端改了不生效"的第二个入口。
// English: resolves which account actually owns the config; used by the admin endpoint to tell
// whether a write touches the global runtime LLM config (and therefore must hot-apply too).
func (m *Manager) ConfigOwnerID(userID string) string {
	return m.ownerOf(userID)
}

// SetLLMConfigFor 更新运营数据归属账号（管理员）的 LLM 配置并持久化（系统级共享）。
// §0926E2E-W1B：返回 error（持久化失败/写后复读不匹配如实透出）。
func (m *Manager) SetLLMConfigFor(userID string, cfg *LLMConfig) error {
	oid := m.ownerOf(userID)
	if m.store == nil || oid == "" {
		m.mu.Lock() // §0925EVE-D1：全局回退分支写活体字段补锁（与 §CFGSMASH setter 同口径）
		m.rules.LLM = *cfg
		m.mu.Unlock()
		return m.Save()
	}
	r := m.userRules(oid)
	r.LLM = *cfg
	return m.saveUserRules(oid, r)
}

// StoredLLMConfig 返回该账号（解析到运营归属账号）是否显式保存过 LLM 配置及其快照。
// 供启动装配区分「UI 真实保存过」与「无保存回退全局」——§UI-AUTHORITATIVE 修复：
// 设置页保存过的配置重启后必须优先于环境变量，不再被 NSSM 里硬编码的旧 LLM_* 顶掉。
// English: reports whether the account (resolved to its operator owner) explicitly saved an
// LLM config, plus the snapshot; used to give UI-saved config precedence over env at startup.
func (m *Manager) StoredLLMConfig(userID string) (*LLMConfig, bool) {
	oid := m.ownerOf(userID)
	if m.store == nil || oid == "" {
		return nil, false
	}
	if r, ok := m.storedUserRules(oid); ok {
		cp := r.LLM // 拷贝返回，避免调用方改写内存快照
		return &cp, true
	}
	return nil, false
}

// GetQMTConfigFor 返回指定账号的 QMT 实盘配置（§2026-09-07 多账号实盘）。
// 解析优先级：① 账号自身覆盖 → ② 运营账号覆盖（存量单账号行为：所有账号共享运营账号配置）
// → ③ 全局 rules.qmt。引擎构建/热同步（registry.go:706、engine.go syncAccountConfig）按此
// 取每账号独立 gateway/token/资金，逐账号独立下单。
// English: returns an account's QMT live-trading config — account override first, then the operator's
// (legacy single-account behavior), then global. Every engine's controller is wired from this so each
// account can trade against its own gateway/capital.
func (m *Manager) GetQMTConfigFor(userID string) *QMTConfig {
	if m.store == nil || userID == "" {
		return &m.Get().QMT // §0925EVE-D1：指针经加锁访问器取得，字段地址跟随该快照不变
	}
	if r, ok := m.storedUserRules(userID); ok {
		return &r.QMT
	}
	if oid := m.ownerOf(userID); oid != "" && oid != userID {
		if r, ok := m.storedUserRules(oid); ok {
			return &r.QMT
		}
	}
	return &m.Get().QMT
}

// SetQMTConfigFor 更新指定账号的 QMT 实盘配置并持久化到该账号的规则快照（5s 热加载生效）。
// §2026-09-07 从「运营账号系统级共享」改为「按账号落库」：管理员经
// /api/admin/users/{id}/config/qmt 逐账号配置；账号所有者经 /api/config/qmt 配自己的。
// 调用方负责校验取值合法性（mode/price_type 枚举、白名单过滤等），这里只做落库。
// English: persists an account's QMT live-trading config to that account's rules snapshot
// (hot-reloaded within 5s). Callers must validate enum/whitelist values — this method only stores.
// §0926E2E-W1B：返回 error（含写后复读自证），applySetQMTConfig 据此回 500——
// 旧行为"写失败只 log 照回 200"会让安全参数（enabled/熔断/预算）磁盘没落而前端显示已保存，
// 重启即回退旧值。
func (m *Manager) SetQMTConfigFor(userID string, cfg *QMTConfig) error {
	if m.store == nil || userID == "" {
		m.mu.Lock() // §0925EVE-D1：无 store 全局回退分支写活体字段补锁
		m.rules.QMT = *cfg
		m.mu.Unlock()
		return m.Save()
	}
	r := m.userRules(userID)
	r.QMT = *cfg
	return m.saveUserRules(userID, r)
}

// SetPaperStrategyFor 更新指定账号的模拟盘战法准入配置（§SIGNAL_CONTROLLER P3：
// rules.paper.strategies 白名单 + rules.paper.blacklist 个股黑名单），仅改这两个字段、
// 其余 paper 配置保持原值，落该账号规则快照（信号控制器 paper 通道 5s 内读到新值）。
// 调用方负责白名单条目合法性校验（knownStrategyIDSet），这里只做落库。
// English: persists an account's paper-side strategy admission (whitelist + code blacklist),
// leaving the rest of rules.paper untouched; the signal controller picks it up on its next feed.
// §0926E2E-W1B：返回 error（持久化失败如实透出）。
func (m *Manager) SetPaperStrategyFor(userID string, strategies, blacklist []string) error {
	if m.store == nil || userID == "" {
		m.mu.Lock() // §0925EVE-D1：全局回退分支写活体字段补锁
		m.rules.Paper.Strategies = strategies
		m.rules.Paper.Blacklist = blacklist
		m.mu.Unlock()
		return m.Save()
	}
	r := m.userRules(userID)
	r.Paper.Strategies = strategies
	r.Paper.Blacklist = blacklist
	return m.saveUserRules(userID, r)
}

// SetPaperConfigFor §F-4（20260917 缺陷修复批）：按回调局部更新指定账号的 rules.paper
// （总开关/自动卖出/单笔资金/初始资金/做空侧参数等），战法白名单与黑名单不经此路径
// （走 SetPaperStrategyFor，语义已独立）。store 缺席时落全局快照（测试/单文件部署）。
// English: §F-4 — mutator-style partial update of an account's rules.paper (master switch,
// auto-sell, sizing, short-side params); strategies/blacklist keep their dedicated setter.
// §0926E2E-W1B：返回 error（持久化失败/写后复读不匹配如实透出）。
func (m *Manager) SetPaperConfigFor(userID string, mutate func(*PaperConfig)) error {
	if mutate == nil {
		return nil
	}
	if m.store == nil || userID == "" {
		m.mu.Lock() // §0925EVE-D1：全局回退分支的 mutate 持锁执行，与 Save 的 RLock marshal 配对
		mutate(&m.rules.Paper)
		m.mu.Unlock()
		return m.Save()
	}
	r := m.userRules(userID)
	mutate(&r.Paper)
	return m.saveUserRules(userID, r)
}

// GetD1ConfigFor 返回运营数据归属账号（管理员）的 D1 事件匹配规则（运营配置系统级共享）。
func (m *Manager) GetD1ConfigFor(userID string) *D1Config {
	oid := m.ownerOf(userID)
	if m.store == nil || oid == "" {
		return m.GetD1Config() // §0925EVE-D1：统一走加锁访问器
	}
	m.mu.RLock()
	raw, ok := m.store.GetConfig(oid, perUserD1Key)
	m.mu.RUnlock()
	if !ok || raw == "" {
		return m.GetD1Config()
	}
	var d D1Config
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		log.Printf("[config] 账号 %s D1 配置反序列化失败, 回退全局: %v", oid, err)
		return m.GetD1Config()
	}
	normalizeD1(&d)
	return &d
}

// SetD1ConfigFor 更新运营数据归属账号（管理员）的 D1 规则并持久化（系统级共享）。
// §0926E2E-W1B：返回 error，且落库走 setConfigVerifiedLocked 写后复读自证（旧写法 SetConfig
// 出错只 log 照回 200，属"降级报成功"家族）。
func (m *Manager) SetD1ConfigFor(userID string, cfg *D1Config) error {
	oid := m.ownerOf(userID)
	if m.store == nil || oid == "" {
		m.mu.Lock() // §0925EVE-D1：D1 指针整体替换是发布动作，必须持锁（与 Load 同口径）
		m.d1 = cfg
		m.mu.Unlock()
		return m.Save()
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("账号 %s D1 配置序列化失败: %w", oid, err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.setConfigVerifiedLocked(oid, perUserD1Key, data); err != nil {
		return fmt.Errorf("账号 %s D1 配置%w", oid, err)
	}
	return nil
}

// GetLongShortConfigFor 返回运营数据归属账号（管理员）的做多/做空开关（运营配置系统级共享，
// 默认做多开/做空关）。
func (m *Manager) GetLongShortConfigFor(userID string) LongShortConfig {
	def := LongShortConfig{LongEnabled: true, ShortEnabled: false}
	oid := m.ownerOf(userID)
	if m.store == nil || oid == "" {
		return def
	}
	m.mu.RLock()
	raw, ok := m.store.GetConfig(oid, perUserLongShortKey)
	m.mu.RUnlock()
	if !ok || raw == "" {
		return def
	}
	var c LongShortConfig
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		log.Printf("[config] 账号 %s 做多/做空配置反序列化失败, 回退默认: %v", oid, err)
		return def
	}
	return c
}

// SetLongShortConfigFor 更新运营数据归属账号（管理员）的做多/做空开关并持久化（系统级共享）。
// §0926E2E-W1B：返回 error，且落库走 setConfigVerifiedLocked 写后复读自证。
func (m *Manager) SetLongShortConfigFor(userID string, c LongShortConfig) error {
	oid := m.ownerOf(userID)
	if m.store == nil || oid == "" {
		return nil
	}
	data, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("账号 %s 做多/做空配置序列化失败: %w", oid, err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.setConfigVerifiedLocked(oid, perUserLongShortKey, data); err != nil {
		return fmt.Errorf("账号 %s 做多/做空配置%w", oid, err)
	}
	return nil
}

// GetStrategyConfig 返回全局策略参数配置（无账号隔离时使用）。
// §N-4/§CFGSMASH-concurrency（2026-09-22 傍晚批）：本 getter 起返回**快照拷贝的指针**，
// 不再别名内部活体 —— 旧实现 `return &m.Rules.Strategy` 让跨 goroutine 的持有者
// （combat_agent 打分循环、engine registry）与 setter 的裸写构成数据 race；拷贝语义下
// 调用方可自由解引用/长期持有，看到的热更新由 config.json fsnotify 通道（Agent.StartHotReload）
// 与 registry 指纹重建保证，不依赖别名实时透写。签名保持 *StrategyConfig，波及面为零。
// English: returns a pointer to a SNAPSHOT COPY (not the live internal field) — holders may
// dereference/retain across goroutines without racing the setters.
func (m *Manager) GetStrategyConfig() *StrategyConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	cp := m.rules.Strategy
	return &cp
}

// StrategyConfigSnapshot 返回全局策略参数配置的深拷贝值（推荐的新读取入口，语义与
// GetStrategyConfig 相同，命名上明确"这是快照"）。
// English: value-form snapshot of the global strategy config (preferred new read entry).
func (m *Manager) StrategyConfigSnapshot() StrategyConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.rules.Strategy
}

// ErrStrategyVersionConflict §中-6 乐观锁哨兵：写请求携带的 updated_at 与服务端当前版本
// 不一致（另一个管理员先落了一步）时 MergeStrategyConfig 返回本错误，handler 映射为 409。
// English: sentinel for the §mid-6 optimistic-lock conflict (client base version != server).
var ErrStrategyVersionConflict = errors.New("strategy config version conflict")

// SetStrategyConfig 更新全局策略参数并持久化到文件（**全量替换**语义，供内部程序化入口
// 使用，如寻优一键应用 optimizations.go）。
// §CFGSMASH-concurrency 补锁：旧实现裸写 `m.Rules.Strategy = *cfg` 不持 m.mu，而同文件
// 其余 6 处 setter 是加锁的——加锁不对称属遗漏而非设计（三轮补强锤实）。HTTP 端点不再走
// 本方法（改走 MergeStrategyConfig 稀疏 merge），但保留全量语义给"构造完整快照后落盘"的调用方。
// English: full-replace global strategy config under m.mu (internal programmatic entry; the
// HTTP endpoint now uses MergeStrategyConfig instead).
func (m *Manager) SetStrategyConfig(cfg *StrategyConfig) {
	if cfg == nil {
		return
	}
	m.mu.Lock()
	next := *cfg
	next.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano) // §中-6 服务端统一盖版本戳
	m.rules.Strategy = next
	m.mu.Unlock()
	m.Save()
}

// MergeStrategyConfig §N-4（2026-09-22 傍晚批 §CFGSMASH）稀疏 merge 写入口：
// 只更新 patch 中出现的键，未出现的键保留旧值——修复「前端一次失败加载 + 一次整份保存 =
// 五套战法阈值落 0 且已落库、重启救不回」的写端点半边（§LLM 族已钉过"缺失/空值不得折叠成
// 写 0"，strategy 端点是该族漏网）。判定粒度=**逐字段递归**（JSON 语义深合并）：
//  1. 当前配置 marshal 成 map 基底，patch（body 顶层键→原始 JSON）逐层覆盖——
//     对象遇对象递归合并，标量/数组整体替换；
//  2. 合并结果 typed-unmarshal 回 StrategyConfig：body 里的未知键被自然丢弃，等效白名单，
//     无需手写反射或字段清单（战法分组以后增删字段这里零维护）；
//  3. updated_at 为服务端专属字段：从 patch 剔除、由本方法盖戳，body 值只作 §中-6 版本比对。
//
// baseVersion（客户端 GET 到的 updated_at）非空且与服务端不一致 → 返回 (当前快照,
// ErrStrategyVersionConflict)，**不落盘**；baseVersion 为空 = 不比对（兼容旧调用方/脚本直 POST）。
// 全程持 m.mu（写侧互斥 + 与快照读配对），落盘 Save 在锁外（与 SetSchedulerConfig 同范式）。
// English: sparse-merge writer for the global strategy config — only keys present in `patch`
// are updated (object-into-object merges recursively; scalars/arrays replace wholesale; unknown
// keys are dropped by the final typed unmarshal). baseVersion implements the §mid-6 optimistic
// lock: mismatch => ErrStrategyVersionConflict without persisting.
func (m *Manager) MergeStrategyConfig(patch map[string]json.RawMessage, baseVersion string) (StrategyConfig, error) {
	delete(patch, "updated_at") // 版本字段服务端所有：body 值只用于比对，绝不参与 merge
	m.mu.Lock()
	cur := m.rules.Strategy
	if baseVersion != "" && cur.UpdatedAt != "" && baseVersion != cur.UpdatedAt {
		m.mu.Unlock()
		return cur, ErrStrategyVersionConflict
	}
	// ① 当前值 → map 基底（json round-trip，保证与 patch 同域可递归合并）
	baseMap := map[string]any{}
	if raw, err := json.Marshal(cur); err == nil {
		if err := json.Unmarshal(raw, &baseMap); err != nil {
			m.mu.Unlock()
			return cur, fmt.Errorf("当前策略配置序列化异常: %w", err)
		}
	}
	// ② patch 原始 JSON → any，任一顶层键 JSON 非法即 400（不吞、不带病落盘）
	patchMap := make(map[string]any, len(patch))
	for k, raw := range patch {
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			m.mu.Unlock()
			return cur, fmt.Errorf("字段 %s 不是合法 JSON: %w", k, err)
		}
		patchMap[k] = v
	}
	deepMergeAny(baseMap, patchMap)
	// ③ 合并结果回 typed struct：类型不符/结构非法 → 400，旧配置原样保留
	mergedRaw, err := json.Marshal(baseMap)
	if err != nil {
		m.mu.Unlock()
		return cur, fmt.Errorf("合并结果序列化失败: %w", err)
	}
	var next StrategyConfig
	if err := json.Unmarshal(mergedRaw, &next); err != nil {
		m.mu.Unlock()
		return cur, fmt.Errorf("策略参数校验失败: %w", err)
	}
	next.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	m.rules.Strategy = next
	m.mu.Unlock()
	m.Save()
	return next, nil
}

// GetEnhanceConfig 返回全局增强开关（rules.enhance）快照。
// English: returns the global enhance switches (rules.enhance).
func (m *Manager) GetEnhanceConfig() EnhanceConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.rules.Enhance
}

// GetSizingConfig 返回动态仓位/卖出加权配置（rules.sizing），零值字段按出厂默认补齐——
// 前端展示必须看到"实际生效值"，而不是配置文件里没写的 0。
// English: returns rules.sizing with factory defaults filled in, so the UI shows effective values.
func (m *Manager) GetSizingConfig() SizingConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.rules.Sizing.WithDefaults()
}

// MergeEnhanceConfig 稀疏 merge rules.enhance：只改 patch 中出现的键，未出现=保留旧值。
// English: sparse-merge writer for rules.enhance (absent keys keep the stored value).
func (m *Manager) MergeEnhanceConfig(patch map[string]json.RawMessage) (EnhanceConfig, error) {
	m.mu.Lock()
	base := m.rules.Enhance
	var next EnhanceConfig
	if err := mergeStructPatch(base, patch, &next); err != nil {
		m.mu.Unlock()
		return base, err
	}
	// §C4：技术过滤阈值/档位在写入口校验，非法值 400 而不是静默落库。
	if err := validateEnhance(&next); err != nil {
		m.mu.Unlock()
		return base, err
	}
	m.rules.Enhance = next
	m.mu.Unlock()
	m.Save()
	return next, nil
}

// MergeSizingConfig 稀疏 merge rules.sizing（与 Enhance 同一语义）。落盘保留"只写过的键"，
// 展示层由 GetSizingConfig 补默认。
// English: sparse-merge writer for rules.sizing; stored raw, defaults filled on read.
func (m *Manager) MergeSizingConfig(patch map[string]json.RawMessage) (SizingConfig, error) {
	m.mu.Lock()
	base := m.rules.Sizing
	var next SizingConfig
	if err := mergeStructPatch(base, patch, &next); err != nil {
		m.mu.Unlock()
		return base, err
	}
	m.rules.Sizing = next
	m.mu.Unlock()
	m.Save()
	return next, nil
}

// storedSizing 读取账号专用 sizing 原始值（无回退、不补默认）。
// §A0：专用键只存 SizingConfig，解析失败按“无专用配置”处理（读侧继续回退旧账号/全局），
// 不让一份坏文件把该账号的仓位配置整块拉黑。
// English: reads the account's dedicated sizing document without fallback/defaults; a parse
// failure is treated as "no dedicated config" so the caller can fall back safely.
func (m *Manager) storedSizing(userID string) (SizingConfig, bool) {
	if m.store == nil || userID == "" {
		return SizingConfig{}, false
	}
	m.mu.RLock()
	raw, ok := m.store.GetConfig(userID, perUserSizingKey)
	m.mu.RUnlock()
	if !ok || raw == "" {
		return SizingConfig{}, false
	}
	var sc SizingConfig
	if err := json.Unmarshal([]byte(raw), &sc); err != nil {
		log.Printf("[config] 账号 %s sizing 专用键反序列化失败，回退旧配置: %v", userID, err)
		return SizingConfig{}, false
	}
	return sc, true
}

// accountRulesSnapshot 返回账号自身规则快照（账号键优先，兼容历史系统级 "" 键），无覆盖返回 nil。
// 与 userRules 的区别：这里不返回全局副本，调用方需要区分“账号有覆盖”与“纯全局”。
// English: returns the account's own rules snapshot (per-account key, then legacy system-level
// key), or nil when there is no account override — callers need to tell override from global.
func (m *Manager) accountRulesSnapshot(userID string) *Rules {
	if m.store == nil || userID == "" {
		return nil
	}
	if r, ok := m.storedUserRules(userID); ok {
		return r
	}
	if r, ok := m.storedUserRules(""); ok {
		return r
	}
	return nil
}

// sizingBaseRaw 返回账号 sizing 的原始基准（专用键 → 账号旧 sizing → 全局），不补默认。
// 返回值 dedicated 表示是否存在账号专用键，供 API 回显 sizing_scope/是否有覆盖。
// English: raw sizing base (dedicated key → account legacy sizing → global) without defaults.
func (m *Manager) sizingBaseRaw(userID string) (sc SizingConfig, acct *Rules, dedicated bool) {
	if v, ok := m.storedSizing(userID); ok {
		return v, m.accountRulesSnapshot(userID), true
	}
	if a := m.accountRulesSnapshot(userID); a != nil {
		return a.Sizing, a, false
	}
	m.mu.RLock()
	sc = m.rules.Sizing
	m.mu.RUnlock()
	return sc, nil, false
}

// legacySizingBridgeLog 记录“旧 position 字段被桥接为 sizing 上限”的日级证据。
// 只在读到百分数写法（>1）时记录一次/账号/日，避免每 5 秒刷一行。
var (
	legacySizingBridgeMu  sync.Mutex
	legacySizingBridgeDay = map[string]string{}
)

func logLegacySizingBridge(userID string, legacy, normalized float64) {
	if legacy <= 1 {
		return
	}
	day := time.Now().Format("2006-01-02")
	legacySizingBridgeMu.Lock()
	if legacySizingBridgeDay[userID] == day {
		legacySizingBridgeMu.Unlock()
		return
	}
	legacySizingBridgeDay[userID] = day
	legacySizingBridgeMu.Unlock()
	log.Printf("[config] §A0 总仓位上限桥接 账号=%s rules.position.max_total_position_pct=%.4g 按百分数归一为 %.4f（rules.sizing.max_total_position_pct 或账号专用键可显式覆盖）",
		userID, legacy, normalized)
}

// GetSizingConfigFor §A0 返回账号的 effective sizing 配置。
//
// 读取优先级：`quant_config_sizing_v1` 专用键 → 账号旧 `rules.sizing` → 全局 `rules.sizing` →
// 出厂默认。`max_total_position_pct<=0` 时在 `WithDefaults` **之前**回退账号/全局
// `rules.position.max_total_position_pct`，否则 0 会先被补成 0.80，旧字段永远读不到。
// 第二个返回值表示是否存在账号专用键（API 回显用），不影响配置内容。
// English: effective account sizing — dedicated key → account legacy sizing → global; the legacy
// position cap is bridged BEFORE WithDefaults so a zero never masks the old field.
func (m *Manager) GetSizingConfigFor(userID string) (SizingConfig, bool) {
	sc, acct, dedicated := m.sizingBaseRaw(userID)
	if sc.MaxTotalPositionPct <= 0 {
		legacy := 0.0
		if acct != nil {
			legacy = acct.Position.MaxTotalPositionPct
		}
		if legacy <= 0 {
			m.mu.RLock()
			legacy = m.rules.Position.MaxTotalPositionPct
			m.mu.RUnlock()
		}
		sc.MaxTotalPositionPct = NormalizeLegacyTotalPosPct(legacy)
		logLegacySizingBridge(userID, legacy, sc.MaxTotalPositionPct)
	}
	return sc.WithDefaults(), dedicated
}

// MergeSizingConfigFor §A0 稀疏 merge 账号级 sizing 并持久化到专用键。
//
// 基准取“当前 effective 原始值”（专用键 → 账号旧 sizing → 全局），只应用 patch 中出现的键；
// 然后整体写入 `quant_config_sizing_v1`。首次保存会把当前继承值物化到账号键，此后该账号
// sizing 独立于全局——这是 R2 账号独立的预期语义，面板与运维文档已注明。
// 落库走 setConfigVerifiedLocked：写失败/复读不一致如实返回 error，不静默成功。
// English: sparse-merge the account's sizing into the dedicated key. The base is the current
// effective raw value; the first save materializes the inherited values, after which the account
// is independent of global sizing (the R2 semantics). Persistence is read-back verified.
func (m *Manager) MergeSizingConfigFor(userID string, patch map[string]json.RawMessage) (SizingConfig, error) {
	if m.store == nil {
		return SizingConfig{}, fmt.Errorf("账号级配置存储不可用")
	}
	if userID == "" {
		return SizingConfig{}, fmt.Errorf("需要账号 ID")
	}
	base, _, _ := m.sizingBaseRaw(userID)
	var next SizingConfig
	if err := mergeStructPatch(base, patch, &next); err != nil {
		return base, err
	}
	data, err := json.Marshal(next)
	if err != nil {
		return base, fmt.Errorf("sizing 序列化失败: %w", err)
	}
	m.mu.Lock()
	err = m.setConfigVerifiedLocked(userID, perUserSizingKey, data)
	m.mu.Unlock()
	if err != nil {
		return base, fmt.Errorf("账号 %s sizing 配置%w", userID, err)
	}
	return next, nil
}

// GetConceptConfig §C1-V2 返回全局概念库配置快照。
// English: §C1-V2 returns the global concept-library config snapshot.
func (m *Manager) GetConceptConfig() ConceptConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.rules.Concept
}

// MergeConceptConfig §C1-V2 稀疏 merge rules.concept（系统级，仅 admin 写）。
// English: §C1-V2 sparse-merges the system-level rules.concept section.
func (m *Manager) MergeConceptConfig(patch map[string]json.RawMessage) (ConceptConfig, error) {
	m.mu.Lock()
	base := m.rules.Concept
	var next ConceptConfig
	if err := mergeStructPatch(base, patch, &next); err != nil {
		m.mu.Unlock()
		return base, err
	}
	if next.MinCoveragePct < 0 || next.MinCoveragePct > 100 {
		m.mu.Unlock()
		return base, fmt.Errorf("concept.min_coverage_pct 超出范围 0-100（实际 %.2f）", next.MinCoveragePct)
	}
	if next.MaxStalenessHours < 0 || next.MaxStalenessHours > 720 {
		m.mu.Unlock()
		return base, fmt.Errorf("concept.max_staleness_hours 超出范围 0-720（实际 %d）", next.MaxStalenessHours)
	}
	m.rules.Concept = next
	m.mu.Unlock()
	m.Save()
	return next, nil
}

// mergeStructPatch 把 base 序列化成 map 后按 patch 深合并，再反序列化回 out（类型不符即报错）。
// English: deep-merges patch onto base's JSON map, then decodes back into out (type mismatch errors).
func mergeStructPatch(base any, patch map[string]json.RawMessage, out any) error {
	baseMap := map[string]any{}
	raw, err := json.Marshal(base)
	if err != nil {
		return fmt.Errorf("当前配置序列化异常: %w", err)
	}
	if err := json.Unmarshal(raw, &baseMap); err != nil {
		return fmt.Errorf("当前配置解析异常: %w", err)
	}
	patchMap := make(map[string]any, len(patch))
	for k, pr := range patch {
		var v any
		if err := json.Unmarshal(pr, &v); err != nil {
			return fmt.Errorf("字段 %s 不是合法 JSON: %w", k, err)
		}
		patchMap[k] = v
	}
	deepMergeAny(baseMap, patchMap)
	merged, err := json.Marshal(baseMap)
	if err != nil {
		return fmt.Errorf("合并结果序列化失败: %w", err)
	}
	if err := json.Unmarshal(merged, out); err != nil {
		return fmt.Errorf("配置校验失败: %w", err)
	}
	return nil
}

// deepMergeAny 递归合并 src 进 dst：两侧同为对象→逐键递归；否则 src 整体替换
// （数组与标量不做元素级合并——阈值数组这类字段半合更危险）。nil 值按"显式 null"处理，
// 由下游 typed-unmarshal 决定语义（数值字段 null 会被 Go json 忽略=保留旧值，不折叠成 0）。
// English: recursive JSON-map merge; object×object merges key-by-key, anything else replaces.
func deepMergeAny(dst, src map[string]any) {
	for k, sv := range src {
		if sm, ok := sv.(map[string]any); ok {
			if dm, ok := dst[k].(map[string]any); ok {
				deepMergeAny(dm, sm)
				continue
			}
		}
		dst[k] = sv
	}
}

// GetD1Config 返回全局 D1 事件匹配规则配置。
// §0925EVE-D1：d1 指针的唯一全局读口径（原导出字段 D1 转私有），RLock 保证与
// Watch/Load 的指针发布无竞态；返回活体指针，整体替换一律走 SetD1Config 族 setter。
// （GetD1Config returns the global D1 event-matching rules config under RLock.）
func (m *Manager) GetD1Config() *D1Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.d1
}

// SetSchedulerConfig 更新全局研究调度器配置（rules.scheduler）并持久化到文件。
// 用于前端"全量回测全局开关"等调度选项的读写。
// §0929CFG-D1：起返回 error（同 §0926E2E-W1B 对 Strategy/D1 两条 setter 的收口口径）——
// 旧写法把 Save 的错误吞掉，调用方与用户都只能看见"已保存"，磁盘上到底落没落无人知晓。
// 调用方作为语句调用（不接返回值）在 Go 里合法，因此既有测试与脚本无需改动即可编译，
// 但**新增**调用方必须像 HTTP 层那样把失败如实透出，别把这条 error 再吞一遍。
// English: updates the global scheduler config and persists it; since §0929CFG-D1 the persistence
// error is surfaced instead of swallowed (the caller must answer 500 on failure).
func (m *Manager) SetSchedulerConfig(cfg *SchedulerConfig) error {
	if cfg == nil {
		return fmt.Errorf("调度配置为空，未保存")
	}
	m.mu.Lock()
	m.rules.Scheduler = *cfg
	m.mu.Unlock()
	return m.Save()
}

// SetD1Config 更新全局 D1 规则并持久化到文件。
// §0925EVE-D1：D1 指针整体替换属于「发布」动作，旧实现无锁赋值与 Watch/Load 的
// 重载通道同族，现持 Lock 写入（与 Load 的发布段同口径）。
// （SetD1Config updates the global D1 rules under m.mu and persists them.）
func (m *Manager) SetD1Config(cfg *D1Config) {
	m.mu.Lock()
	m.d1 = cfg
	m.mu.Unlock()
	m.Save()
}

// MergeD1Config §0929CFG-D1（09-29 全量审计批 P1-1，owner 令照推荐项执行）：
// D1 规则通道的**稀疏 merge**——「没传=保留旧值」，与 §N-4/§CFGSMASH 的战法参数通道同族同口径。
//
// 为什么必须有这一条：旧 handleSetD1Config 把 body 解成强类型 config.D1Config 后交
// SetD1Config 整体指针替换，而解码器没开 DisallowUnknownFields（全仓该调用为 0 处）⇒
// 一份只含未知键的补丁会把已配置的规则与权重全部折叠成零值再落盘。本轮在正规 UAT 栈实测复现：
// 先写一份真配置，再发 {"totally_unknown_key":1} → HTTP 200 ok，读回 {"rules": null}
// （证据日志 /tmp/probe3_0929.log）。这次事故 09-22 傍晚在战法阈值上锤过一次并已修，
// D1 这条通道当时留在了事故之前的语义上。
//
// 语义边界（钉死在这里，别在调用方各解一遍）：
//   - 未知顶层键**不参与** merge，只作为 ignored_keys 回报给调用方（拼错键名不再等于清零）；
//   - 显式传某个键一定更新该键（要关加成请明写 boost_weight:0）；merge 后**不跑** normalizeD1，
//     否则"明写 0"会被出厂默认顶掉，与本文件 §N-4 的口径一致；
//   - rules 是数组：整键替换（不做元素级合并）——规则集是一份清单，合并语义会造出重复规则；
//   - 合并结果回 typed struct 失败＝400 级错误，旧配置原样保留、一个字节都不落盘。
//
// English: sparse merge for the D1 rules channel (§0929CFG-D1). Absent keys keep the stored value;
// unknown keys are reported as ignored instead of zeroing the whole document; no normalizeD1 run
// after merge so an explicit zero stays zero; arrays replace wholesale.
func (m *Manager) MergeD1Config(patch map[string]json.RawMessage) (next D1Config, ignored []string, err error) {
	// 取当前值（RLock 内拷贝，与 Load 的指针发布互斥）
	m.mu.RLock()
	cur := D1Config{}
	if m.d1 != nil {
		cur = *m.d1
	}
	m.mu.RUnlock()

	// ① 当前值 → map 基底（json round-trip，保证与 patch 同域可递归合并）
	raw, err := json.Marshal(cur)
	if err != nil {
		return cur, nil, fmt.Errorf("当前 D1 配置序列化异常: %w", err)
	}
	mergedRaw, ignored, err := mergeRawWithPatch(raw, patch, knownD1TopLevelKeys())
	if err != nil {
		return cur, ignored, err
	}
	if err := json.Unmarshal(mergedRaw, &next); err != nil {
		return cur, ignored, fmt.Errorf("D1 规则校验失败: %w", err)
	}
	// ② 持锁整体替换 + 落盘（SetD1Config 旧实现吞 Save 错误，这里如实透出）
	m.mu.Lock()
	m.d1 = &next
	m.mu.Unlock()
	if err := m.Save(); err != nil {
		return next, ignored, fmt.Errorf("D1 配置持久化失败: %w", err)
	}
	return next, ignored, nil
}

// mergeRawWithPatch 稀疏合并的共用内核：基准 JSON 文档 + 「顶层键→原始补丁」→ 合并后的 JSON 文档，
// 外加被判定为未知的键清单。战法参数、D1 两族 merge 都走这里，避免同一套"未知键/非法 JSON/
// 递归合并"的规则在两处各自演化（§N-4 之后每一次新增 merge 通道都必须经这个内核）。
// 未知键只登记不合并；已知键的 JSON 非法立即报错（不吞、不带病落盘）；数组整键替换。
// English: the shared sparse-merge kernel (base document + top-level raw patch → merged document
// plus the list of unknown keys); every merge channel must go through it.
func mergeRawWithPatch(baseRaw []byte, patch map[string]json.RawMessage, known map[string]bool) ([]byte, []string, error) {
	baseMap := map[string]any{}
	if err := json.Unmarshal(baseRaw, &baseMap); err != nil {
		return nil, nil, fmt.Errorf("基准配置解析异常: %w", err)
	}
	var ignored []string
	patchMap := make(map[string]any, len(patch))
	for k, v := range patch {
		if known != nil {
			if _, ok := known[k]; !ok {
				ignored = append(ignored, k)
				continue
			}
		}
		var av any
		if err := json.Unmarshal(v, &av); err != nil {
			return nil, sortedStrings(ignored), fmt.Errorf("字段 %s 不是合法 JSON: %w", k, err)
		}
		patchMap[k] = av
	}
	sort.Strings(ignored)
	deepMergeAny(baseMap, patchMap)
	out, err := json.Marshal(baseMap)
	if err != nil {
		return nil, ignored, fmt.Errorf("合并结果序列化失败: %w", err)
	}
	return out, ignored, nil
}

// D1WriteTargetFor 判定"这次 D1 写入应该落哪本账"（§0929CFG-D1 写读同源收口）。
// 运行时读侧 GetD1ConfigFor 的优先级是：**账号级 perUserD1Key 覆盖 → 全局 m.d1**。
// 旧写通道 POST /api/config/d1 无条件写全局，于是只要该账号存在过任何一次账号级 D1 覆盖
// （由 /api/admin/users/{id}/config/d1 那条代配通道创建），设置页保存就成了
// "界面照常显示已保存、引擎照旧吃旧副本"的静默空写——这正是 09-26 网关口令五源里
// 「只洗到全局、账号覆盖层留旧值」的同族形态，区别是那次是钥匙、这次是打分规则。
// 本函数把这个优先级显式化：写侧按读侧的落点走，两侧不可能再分叉。
// 返回 key=="" 表示落全局文件（现有行为），否则返回该账号级 KV 键。
// English: resolves where a D1 write must land so the writer and the engine's reader can never
// diverge again (account override present => write the override, else the global file).
func (m *Manager) D1WriteTargetFor(userID string) (oid string, key string) {
	oid = m.ownerOf(userID)
	if m.store == nil || oid == "" {
		return oid, "" // 无账号级存储：本来就只有全局这一本账
	}
	if _, ok := m.AccountKVRaw(oid, perUserD1Key); ok {
		return oid, perUserD1Key
	}
	return oid, ""
}

// MergeD1ConfigScoped 按 D1WriteTargetFor 判定的落点做稀疏 merge，并回报本次真实落点。
// target 取值为 "global" 或 "account"，供 HTTP 层写审计与前端如实展示（绝不回报一个没写过的地方）。
// §0929CFG-D1：账号级分支的合并基准是**该键的原始 JSON**（含历史遗留的未知字段，原样保留），
// 全局级分支复用 MergeD1Config 的 typed 口径。
// English: sparse-merges D1 into whichever document the runtime actually reads, and reports
// which one that was — so a write can never silently land where nobody reads.
func (m *Manager) MergeD1ConfigScoped(userID string, patch map[string]json.RawMessage) (next D1Config, ignored []string, target string, err error) {
	oid, key := m.D1WriteTargetFor(userID)
	if key == "" {
		merged, ign, merr := m.MergeD1Config(patch)
		if merr != nil {
			return merged, ign, "global", merr
		}
		return merged, ign, "global", nil
	}
	base, _ := m.AccountKVRaw(oid, key)
	mergedRaw, ign, merr := mergeRawWithPatch(base, patch, knownD1TopLevelKeys())
	if merr != nil {
		var cur D1Config
		_ = json.Unmarshal(base, &cur)
		return cur, ign, "account", merr
	}
	var parsed D1Config
	if err := json.Unmarshal(mergedRaw, &parsed); err != nil {
		return D1Config{}, ign, "account", fmt.Errorf("D1 规则校验失败: %w", err)
	}
	m.mu.Lock()
	perr := m.setConfigVerifiedLocked(oid, key, mergedRaw)
	m.mu.Unlock()
	if perr != nil {
		return parsed, ign, "account", fmt.Errorf("账号 %s D1 配置%w", oid, perr)
	}
	return parsed, ign, "account", nil
}

// MergeD1ConfigForAccount §0929CFG-D1：管理员**指名某账号**代配 D1 时的稀疏合并通道。
// 落点仍经 ownerOf 归并到运营数据归属账号（与读侧 GetD1ConfigFor 完全同口径）——这不是简化，
// 而是本系统的既有设计：量化/LLM/D1 这类运营配置**系统级共享**，"代配某个成员"实际写的是
// 归属账号那份。若这里按传入 id 原样写键，就会造出一把"没有任何读腿会去读"的死配置，
// 而界面照样回 200（正是 §0929CFG-D1 要消灭的写读分叉形态）。
// 该归属账号尚无 D1 覆盖时，以"它当前生效的那份"为基准建立覆盖：这既保留了旧实现"写账号键"
// 的落点，又消灭了同一个抹平形态（旧实现同样是 typed 解码后整份替换，一个未知键就把规则清零）。
// created 回报本次是否新建了覆盖，供审计与前端如实说明"从此全局改动影响不到这份配置"。
// English: sparse merge for the account slot named by an admin proxy edit; the destination still
// resolves through ownerOf because operational config is system-shared — writing the raw caller id
// would create a document no reader ever consults.
func (m *Manager) MergeD1ConfigForAccount(accountID string, patch map[string]json.RawMessage) (next D1Config, ignored []string, created bool, err error) {
	oid := m.ownerOf(accountID)
	if m.store == nil || oid == "" {
		return D1Config{}, nil, false, fmt.Errorf("账号级配置存储不可用，无法代配 D1")
	}
	base, existed := m.AccountKVRaw(oid, perUserD1Key)
	if !existed {
		// 无覆盖：基准取该账号当前生效值（走读侧同一口径），保证"只改传进来的键"成立。
		base, err = m.effectiveAccountValue(oid, perUserD1Key)
		if err != nil {
			return D1Config{}, nil, false, err
		}
	}
	mergedRaw, ign, merr := mergeRawWithPatch(base, patch, knownD1TopLevelKeys())
	if merr != nil {
		var cur D1Config
		_ = json.Unmarshal(base, &cur)
		return cur, ign, false, merr
	}
	if err := json.Unmarshal(mergedRaw, &next); err != nil {
		return D1Config{}, ign, false, fmt.Errorf("D1 规则校验失败: %w", err)
	}
	m.mu.Lock()
	perr := m.setConfigVerifiedLocked(oid, perUserD1Key, mergedRaw)
	m.mu.Unlock()
	if perr != nil {
		return next, ign, false, fmt.Errorf("账号 %s D1 配置%w", oid, perr)
	}
	return next, ign, !existed, nil
}

// GetD1EffectiveSource 回报当前生效的 D1 来自哪本账（"global"/"account"），供 GET 侧如实回显。
// §0929CFG-D1：设置页拿到这个字段才能区分"我改的这份就是引擎吃的那份"和"引擎吃的是账号副本"，
// 也给了运维一条不用翻 auth.json 就能自证接线的读腿。
// English: which ledger the runtime D1 currently resolves from, surfaced on the GET side.
func (m *Manager) GetD1EffectiveSource(userID string) string {
	_, key := m.D1WriteTargetFor(userID)
	if key != "" {
		return "account"
	}
	return "global"
}

// knownD1TopLevelKeys 返回 D1Config 的顶层 JSON 键集合（稀疏 merge 的白名单）。
// 与结构体 tag 手工对齐是刻意的：宁可这里漏一个键（merge 会把它报成 ignored、调用方当场发现），
// 也不要靠反射把 omitempty 的缺省语义卷进白名单判定。改 D1Config 字段必须同步改这张表
// ——本批门禁有一条两侧键集合等值锁兜住漂移。
// English: the whitelist of top-level D1 keys, kept in sync with the struct tags by a gate lock.
func knownD1TopLevelKeys() map[string]bool {
	return map[string]bool{
		"rules":           true,
		"boost_weight":    true,
		"boost_threshold": true,
	}
}

// sortedStrings 返回升序副本（错误路径上也要给调用方稳定顺序，便于逐字节断言）。
// English: ascending copy — stable ordering matters on the error path too.
func sortedStrings(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

// HasD1Override 报告该账号是否存在**账号级 D1 覆盖**（即"全局改动已经影响不到这个账号"）。
// §0929CFG-D1：这个布尔值必须能在 HTTP 侧读到——代配通道一旦建立覆盖，设置页后续所有全局改动
// 对该账号都不再生效，而这在旧形态下只体现在 auth.json 里多了一个键，界面上完全看不出来。
// English: whether the account has its own D1 override (i.e. global edits no longer reach it).
func (m *Manager) HasD1Override(userID string) bool {
	oid := m.ownerOf(userID)
	if m.store == nil || oid == "" {
		return false
	}
	_, ok := m.AccountKVRaw(oid, perUserD1Key)
	return ok
}

// HasAccountOverrideFor 报告该账号在 KV 里是否**已有**某个配置键的独立覆盖
// （§0929CFG-HIST：回滚端点要据此如实回报"本次是否新建了覆盖"——新建覆盖意味着此后
// 全局改动不再自动影响这个账号，这个副作用必须可见，不能等人事后翻 auth.json 才发现）。
// English: whether an account already has its own override for a KV key (rollback reports the
// side effect of creating one).
func (m *Manager) HasAccountOverrideFor(userID, key string) bool {
	if m.store == nil || userID == "" || key == "" {
		return false
	}
	_, ok := m.AccountKVRaw(userID, key)
	return ok
}

// GetLLMConfig 返回全局 LLM 客户端配置。
// §0925EVE-D1：rules 指针经加锁访问器 Get() 取得后再取字段地址，语义不变（活体字段指针）。
// （GetLLMConfig returns the global LLM client config.）
func (m *Manager) GetLLMConfig() *LLMConfig {
	return &m.Get().LLM
}

// GetNotifyConfig 返回通知推送配置。
// §0925EVE-D1：同 GetLLMConfig，指针读取收进 Get()。
// （GetNotifyConfig returns the notification config.）
func (m *Manager) GetNotifyConfig() *NotifyConfig {
	return &m.Get().Notify
}

// SetNotifyConfig 更新通知配置并持久化到文件。
// §0925EVE-D1：全局活体字段的无锁裸写补持 m.mu（与 §CFGSMASH setter 锁口径对齐，
// 也与 Save 的 RLock marshal 形成互斥）。
// （SetNotifyConfig updates the notification config and persists it.）
func (m *Manager) SetNotifyConfig(cfg *NotifyConfig) {
	m.mu.Lock()
	m.rules.Notify = *cfg
	m.mu.Unlock()
	m.Save()
}

// SetLLMConfig 更新全局 LLM 配置并持久化到文件。
// §0925EVE-D1：同 SetNotifyConfig，无锁裸写补锁。
// （SetLLMConfig updates the global LLM config and persists it.）
func (m *Manager) SetLLMConfig(cfg *LLMConfig) {
	m.mu.Lock()
	m.rules.LLM = *cfg
	m.mu.Unlock()
	m.Save()
}

// Load 从配置文件读取并解析 JSON，更新 Rules 和 D1 配置。
// 文件缺失/不可读时静默保留内存现状（首次启动即用 DefaultRules）；
// 解析失败仅记日志不清空已有配置；未出现的段不覆盖。
// §0925EVE-D1（本缺陷主体）：指针发布段收进**单次 m.mu.Lock 临界区**——rules 与 d1
// 要么一起保持旧值、要么一起换上本次解析出的新值，不存在「半新一旧」被读到的窗口。
// 注意边界：wrapper 语义本身允许文件里只写 rules 段或只写 d1 段——此时只有出现的段被
// 替换，另一个保持旧值，这是**配置语义**使然而非并发破口；「成对一致」保证的是
// 同一次 Load 内两段发布原子完成。读方若在同一次业务里先后各取一次 Get()/GetD1Config()，
// 跨两次调用可能被夹入一次新的 Load（各次调用内部无竞态，跨调用不保证成对）；
// 需要严格成对时用 RulesD1Snapshot 一次取回。
// English: publishes the reloaded rules/d1 pointers inside ONE m.mu.Lock critical
// section, so readers holding m.mu (Get/GetD1Config/Save/RulesD1Snapshot) never observe a
// torn pointer swap. Re-reads of GetD1Config etc. are lock-free field derefs of immutable
// snapshots: Load only swaps pointers, never mutates the previously published structs.
func (m *Manager) Load() {
	data, err := os.ReadFile(m.path)
	if err != nil {
		return // 文件不存在/不可读：保留内存中的当前配置（首次启动即用出厂默认）
	}
	// wrapper 外层包装：JSON 根对象按 {"rules":..., "d1":...} 两段解析，未出现的段保留原值。
	var wrapper struct {
		Rules *Rules    `json:"rules"`
		D1    *D1Config `json:"d1"`
	}
	if err := json.Unmarshal(data, &wrapper); err != nil {
		log.Printf("[config] 解析配置文件失败: %v", err)
		return
	}
	// §0925EVE-D1 发布段：新结构体（json.Unmarshal 私有对象）解析完成后才入锁，锁内只做
	// 指针赋值，不触发任何回调/IO——与 Get/GetD1Config/Save 的 RLock 读侧严格配对。
	m.mu.Lock()
	if wrapper.Rules != nil {
		m.rules = wrapper.Rules
	}
	if wrapper.D1 != nil {
		m.d1 = wrapper.D1
	}
	m.mu.Unlock()
	log.Printf("[config] 已加载配置文件: %s", m.path)
}

// RulesD1Snapshot 一次 RLock 同取 rules 与 d1 两份全局快照指针。
// §0925EVE-D1：供「同一轮判断里必须同时看两份配置」的调用方使用——Load 的发布段是
// 单临界区，本方法也是，因此返回值对必然来自同一版本（或同为更早版本），不会跨版本混搭。
// English: returns both global snapshots under a single RLock — guaranteed pair-consistent.
func (m *Manager) RulesD1Snapshot() (*Rules, *D1Config) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.rules, m.d1
}

// Save 将当前配置序列化为 JSON 并写入文件。
// §N-4/并发补强（2026-09-22 傍晚批）：marshal 阶段收进 m.mu.RLock——序列化会逐字段遍历
// Rules/D1 活体内存，旧实现在锁外 marshal，与「A 持锁写、B 已解锁进 Save」的并发保存构成
// 数据 race（-race「边保存边打分」用例实测触发）。全部既有调用点都是**先解锁再 Save**，
// 此处取 RLock 无重入死锁风险；磁盘 IO 留在锁外，读路径（打分循环）不被写盘拖住。
// English: marshalling now runs under RLock (it walks the live Rules tree); the atomic file
// write stays outside the lock. All existing call sites already release the lock before Save.
// §0926E2E-W1B：返回 error（序列化/原子写失败如实透出；旧写法只 log，全局回退分支的
// HTTP 保存路径同样"降级报成功"）。既有语句式调用点（cmd/测试/内部程序化入口）忽略
// 返回值在 Go 里合法，行为不变；HTTP 端点侧已逐个接错。
func (m *Manager) Save() error {
	wrapper := struct {
		Rules *Rules    `json:"rules"` // 全局规则配置段
		D1    *D1Config `json:"d1"`    // D1 事件匹配规则段
	}{}
	m.mu.RLock()
	wrapper.Rules = m.rules
	wrapper.D1 = m.d1
	data, err := json.MarshalIndent(wrapper, "", "  ")
	m.mu.RUnlock()
	if err != nil {
		log.Printf("[config] 序列化失败: %v", err)
		return fmt.Errorf("配置序列化失败: %w", err)
	}
	// §W3-c 统一原子写（fsync+唯一临时名）：config.json 由 quant 与 researchd 双进程写，
	// 固定 .tmp 名会互相踩踏；截断则全部账号配置回退默认。
	if err := fileutil.AtomicWrite(m.path, data, 0644); err != nil {
		log.Printf("[config] 写入失败: %v", err)
		return fmt.Errorf("配置文件写入失败: %w", err)
	}
	log.Printf("[config] 已保存配置文件: %s", m.path)
	return nil
}

// Watch §P1-6 配置热重载：轮询配置文件（默认 30s），内容变更（sha256 比对）时自动调用 Load()
// 重载全局 rules/d1，无需重启进程。ctx 取消即停止轮询。采用轮询而非 fsnotify 以避免引入额外依赖，
// 且对网络挂载/容器卷等 inotify 不可靠场景更稳健。
// English: P1-6 hot-reload — polls the config file (default 30s); on content change (sha256 compare)
// it reloads global rules/d1 without a restart. Polling avoids extra deps and works on volumes where
// inotify is unreliable. Stop on ctx cancellation.
func (m *Manager) Watch(ctx context.Context, interval time.Duration) {
	if m.path == "" {
		return // 无配置文件路径则无需监听
	}
	if interval <= 0 {
		interval = 30 * time.Second // 默认轮询周期 30s
	}
	last := m.checksum()
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return // 上下文取消即停止监听
			case <-ticker.C:
				cur := m.checksum()
				if cur == "" || cur == last {
					continue // 未变化或读取失败则跳过
				}
				last = cur
				m.Load() // 内容变更 → 热重载
			}
		}
	}()
}

// checksum 返回配置文件的 sha256（用于变更检测）；文件不可读时返回空串。
func (m *Manager) checksum() string {
	data, err := os.ReadFile(m.path)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return string(sum[:])
}

// LoadSchedulerConfig 从配置文件读取 rules.scheduler（供独立研究服务 quant-research 使用）。
// 只覆盖 JSON 中显式出现的字段，其余回退 DefaultSchedulerConfig；文件缺失/解析失败整体回退默认。
// 解析策略：先读 rules.data（数据源路由），再按 key 逐个解析 scheduler 段内的子对象。
// English: reads rules.scheduler from the config file (for the standalone quant-research service).
// Only fields explicitly present in JSON are applied; the rest fall back to DefaultSchedulerConfig;
// a missing/unparseable file returns defaults wholesale.
func LoadSchedulerConfig(path string) SchedulerConfig {
	def := DefaultSchedulerConfig()
	data, err := os.ReadFile(path)
	if err != nil {
		log.Printf("[scheduler] 读取配置 %s 失败(用默认): %v", path, err)
		return def
	}
	// wrapper 解析根对象：rules.scheduler 段留作原始 JSON 逐键覆盖，rules.data 段做数据源路由。
	var wrapper struct {
		Rules struct {
			Scheduler json.RawMessage `json:"scheduler"` // scheduler 段原文（延迟解析，保缺省字段不覆盖默认）
			Data      struct {
				PrimarySource   string  `json:"primary_source"`    // 研究取数主源：hithink | baostock
				ThsFactorsReady bool    `json:"ths_factors_ready"` // 复权因子对账门禁
				OptimizeEnabled *bool   `json:"optimize_enabled"`  // 夜间自动寻优开关（nil=未配置，不覆盖默认）
				HithinkQPS      float64 `json:"hithink_qps"`       // 同花顺源限流阈值（本段未消费，仅解析透传）
			} `json:"data"`
		} `json:"rules"`
	}
	if err := json.Unmarshal(data, &wrapper); err != nil {
		log.Printf("[scheduler] 解析配置 %s 失败(用默认): %v", path, err)
		return def
	}
	// 覆盖 data 段字段（仅显式出现的项生效）。
	if wrapper.Rules.Data.OptimizeEnabled != nil {
		def.OptimizeEnabled = *wrapper.Rules.Data.OptimizeEnabled
	}
	if wrapper.Rules.Data.PrimarySource != "" {
		def.PrimarySource = wrapper.Rules.Data.PrimarySource
	}
	def.ThsFactorsReady = wrapper.Rules.Data.ThsFactorsReady
	// scheduler 段缺失/为 null 时直接返回默认值。
	raw := wrapper.Rules.Scheduler
	if len(raw) == 0 || string(raw) == "null" {
		return def
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		log.Printf("[scheduler] 解析 scheduler 段失败(用默认): %v", err)
		return def
	}
	// 逐项覆盖顶层 scheduler 字段与嵌套 nightly / dataload_during_trading 子段。
	out := def
	if v, ok := cfgBool(m, "enabled"); ok {
		out.Enabled = v
	}
	if v, ok := cfgBool(m, "optimize_enabled"); ok {
		out.OptimizeEnabled = v
	}
	if v, ok := cfgStr(m, "research_bin"); ok && v != "" {
		out.ResearchBin = v
	}
	if v, ok := cfgStr(m, "dataload_bin"); ok && v != "" {
		out.DataloadBin = v
	}
	if v, ok := cfgStr(m, "db"); ok && v != "" {
		out.DB = v
	}
	if v, ok := cfgStr(m, "pyurl"); ok && v != "" {
		out.PyURL = v
	}
	if sub, ok := cfgObject(m, "nightly"); ok {
		if v, ok := cfgInt(sub, "start_hhmm"); ok {
			out.Nightly.StartHHMM = v
		}
		if v, ok := cfgInt(sub, "weekend_start_hhmm"); ok {
			out.Nightly.WeekendStartHHMM = v
		}
		if v, ok := cfgBool(sub, "abort_on_error"); ok {
			out.Nightly.AbortOnError = v
		}
		if v, ok := cfgBool(sub, "backtest_enabled"); ok {
			out.Nightly.BacktestEnabled = v
		}
		if v, ok := cfgInt(sub, "backtest_events"); ok {
			out.Nightly.BacktestEvents = v
		}
		if v, ok := cfgInt(sub, "research_rounds"); ok && v > 0 {
			out.Nightly.ResearchRounds = v
		}
		if v, ok := cfgStrs(sub, "steps"); ok && len(v) > 0 {
			out.Nightly.Steps = v
		}
		// §2026-09-05 多轮发现/护栏参数子块 rules.nightly.discover。
		// English: multi-round discovery + guardrail parameter block rules.nightly.discover.
		if dsub, ok := cfgObject(sub, "discover"); ok {
			applyDiscoverConfig(&out.Nightly.Discover, dsub)
		}
	}
	if sub, ok := cfgObject(m, "dataload_during_trading"); ok {
		if v, ok := cfgBool(sub, "enabled"); ok {
			out.DataloadDuringTrade.Enabled = v
		}
		if v, ok := cfgInt(sub, "interval_minutes"); ok {
			out.DataloadDuringTrade.IntervalMinutes = v
		}
	}
	// 单步超时（分钟）：此前漏解析导致配置值永远不生效、worker 恒走 90min 兜底，
	// discover_factors 全市场窗口在 90min 处被误杀（实录 #45/#66 两次超时）。
	if v, ok := cfgInt(m, "step_timeout_min"); ok {
		out.StepTimeoutMin = v
	}
	if v, ok := cfgInt(m, "trim_interval_min"); ok {
		out.TrimIntervalMin = v
	}
	// 内存总闸阈值（MB）：此前遗漏解析导致配置值永远不生效、恒走默认 400——
	// 服务器实际空闲可用内存仅几百 MB，400 兜底无法按需留足 quant 余量。
	if v, ok := cfgInt(m, "min_free_mem_mb"); ok {
		out.MinFreeMemMB = v
	}
	// §NIGHTCAP（2026-10-14）单条任务单夜占用上限（分钟）：同上，解析遗漏＝配置值永远不
	// 生效——这里必须与字段默认值（240，见 DefaultSchedulerConfig）分开写，等值由门禁 §117 锁。
	// English: per-task nightly occupancy cap (minutes) — parse here so the JSON key actually
	// takes effect; the built-in default lives in DefaultSchedulerConfig.
	if v, ok := cfgInt(m, "per_task_night_max_min"); ok {
		out.PerTaskNightMaxMin = v
	}
	if v, ok := cfgInt(m, "replay_throttle_ms"); ok {
		out.ReplayThrottleMs = v
	}
	return out
}

// cfgStr 返回字符串字段（非字符串或不存在时 ok=false）。
func cfgStr(m map[string]json.RawMessage, key string) (string, bool) {
	raw, ok := m[key]
	if !ok {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}

// cfgBool 返回布尔字段（非布尔或不存在时 ok=false）。
func cfgBool(m map[string]json.RawMessage, key string) (bool, bool) {
	raw, ok := m[key]
	if !ok {
		return false, false
	}
	var v bool
	if err := json.Unmarshal(raw, &v); err != nil {
		return false, false
	}
	return v, true
}

// cfgInt 返回整数字段（非整数或不存在时 ok=false）。
func cfgInt(m map[string]json.RawMessage, key string) (int, bool) {
	raw, ok := m[key]
	if !ok {
		return 0, false
	}
	var v int
	if err := json.Unmarshal(raw, &v); err != nil {
		return 0, false
	}
	return v, true
}

// cfgStrs 返回字符串数组字段（非数组或不存在时 ok=false）。
func cfgStrs(m map[string]json.RawMessage, key string) ([]string, bool) {
	raw, ok := m[key]
	if !ok {
		return nil, false
	}
	var v []string
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, false
	}
	return v, true
}

// cfgInts 返回整数数组字段（非数组或不存在时 ok=false）。
// English: reads an int-array field; ok=false when missing or not an int array.
func cfgInts(m map[string]json.RawMessage, key string) ([]int, bool) {
	raw, ok := m[key]
	if !ok {
		return nil, false
	}
	var v []int
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, false
	}
	return v, true
}

// cfgFloats 返回浮点数组字段（非数组或不存在时 ok=false）。
// English: reads a float-array field; ok=false when missing or not a float array.
func cfgFloats(m map[string]json.RawMessage, key string) ([]float64, bool) {
	raw, ok := m[key]
	if !ok {
		return nil, false
	}
	var v []float64
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, false
	}
	return v, true
}

// cfgFloat 返回浮点字段（非数值或不存在时 ok=false）。
func cfgFloat(m map[string]json.RawMessage, key string) (float64, bool) {
	raw, ok := m[key]
	if !ok {
		return 0, false
	}
	var v float64
	if err := json.Unmarshal(raw, &v); err != nil {
		return 0, false
	}
	return v, true
}

// applyDiscoverConfig 把 rules.nightly.discover 子块各字段覆盖到默认 DiscoverConfig。
// 仅显式出现的 JSON 键生效；负数/空数组跳过（保留默认）。
// English: overlays rules.nightly.discover onto the default DiscoverConfig; only explicitly
// present keys apply, empty/negative values keep factory defaults.
func applyDiscoverConfig(d *DiscoverConfig, m map[string]json.RawMessage) {
	if v, ok := cfgInts(m, "horizons"); ok && len(v) > 0 {
		d.Horizons = v
	}
	if v, ok := cfgInts(m, "start_windows"); ok && len(v) > 0 {
		d.StartWindows = v
	}
	if v, ok := cfgStrs(m, "metrics"); ok && len(v) > 0 {
		d.Metrics = v
	}
	if v, ok := cfgStrs(m, "factor_pools"); ok && len(v) > 0 {
		d.FactorPools = v
	}
	if v, ok := cfgInt(m, "top_n"); ok && v > 0 {
		d.TopN = v
	}
	if v, ok := cfgInt(m, "min_stocks"); ok && v > 0 {
		d.MinStocks = v
	}
	if v, ok := cfgInt(m, "max_factors"); ok && v > 0 {
		d.MaxFactors = v
	}
	if v, ok := cfgFloat(m, "split"); ok && v > 0 && v < 1 {
		d.Split = v
	}
	if v, ok := cfgFloat(m, "min_ir"); ok && v > 0 {
		d.MinIR = v
	}
	if v, ok := cfgInt(m, "min_days"); ok && v > 0 {
		d.MinDays = v
	}
	if v, ok := cfgFloat(m, "min_gen_t"); ok && v < 0 {
		d.MinGenT = v
	}
	if v, ok := cfgInt(m, "min_trigger"); ok && v > 0 {
		d.MinTrigger = v
	}
	if v, ok := cfgFloat(m, "min_excess"); ok {
		d.MinExcess = v
	}
	if v, ok := cfgInt(m, "min_limit_ups"); ok && v > 0 {
		d.MinLimitUps = v
	}
	if v, ok := cfgInt(m, "top_k"); ok && v > 0 {
		d.TopK = v
	}
	if v, ok := cfgInt(m, "max_per_day"); ok && v > 0 {
		d.MaxPerDay = v
	}
	if v, ok := cfgFloat(m, "guard_strong"); ok && v >= 0 {
		d.GuardStrong = v
	}
	if v, ok := cfgFloat(m, "guard_weak"); ok && v >= 0 {
		d.GuardWeak = v
	}
	if v, ok := cfgInt(m, "min_yr_sign"); ok {
		d.MinYrSign = v
	}
	if v, ok := cfgInt(m, "min_bt_events"); ok {
		d.MinBtEvents = v
	}
	if v, ok := cfgFloat(m, "dedup_jaccard"); ok && v > 0 && v <= 1 {
		d.DedupJaccard = v
	}
	if v, ok := cfgBool(m, "change_gate"); ok {
		d.ChangeGate = v
	}
	if v, ok := cfgInt(m, "staleness_days"); ok && v > 0 {
		d.StalenessDays = v
	}
	if v, ok := cfgFloat(m, "hysteresis"); ok && v >= 0 {
		d.Hysteresis = v
	}
}

// cfgObject 返回子对象字段的 map（不存在或非对象时 ok=false）。
func cfgObject(m map[string]json.RawMessage, key string) (map[string]json.RawMessage, bool) {
	raw, ok := m[key]
	if !ok {
		return nil, false
	}
	var sub map[string]json.RawMessage
	if err := json.Unmarshal(raw, &sub); err != nil {
		return nil, false
	}
	return sub, true
}

// CalendarEvent 宏观日历事件条目。
// （CalendarEvent is an entry of a macro-calendar event.）
type CalendarEvent struct {
	// 事件日期（YYYY-MM-DD）
	Date string `json:"date"`
	// 事件标题
	Title string `json:"title"`
	// 影响程度（high/medium/low）
	Impact string `json:"impact"`
	// 提前提醒天数
	DaysAdvance int `json:"days_advance"`
}

// CalendarConfig 宏观日历配置。
// （CalendarConfig is the macro-calendar configuration.）
type CalendarConfig struct {
	// 是否启用日历告警
	Enabled bool `json:"enabled"`
	// 事件列表
	Events []CalendarEvent `json:"events"`
}

// DefaultRules 默认交易规则实例（未初始化字段为零值）。
// （DefaultRules is the default trading-rules instance; unset fields retain zero values.）
var DefaultRules = &Rules{
	Strategy: defaultStrategyConfig(),
	Position: PositionConfig{
		AutoTrackSignals:  true,
		ATREnabled:        true,
		ATRStopMult:       2.5,
		DailyDropAlertPct: 5,
	},
	Scheduler: DefaultSchedulerConfig(),
	Paper:     PaperConfig{Enabled: false, FixedAmount: 10000, MaxPositions: 10, InitialCapital: 100000, Discipline: DefaultDisciplineConfig()},
	QMT:       DefaultQMTConfig(),
	Runtime:   RuntimeConfig{TrimAfterHours: true, TrimIntervalMin: 15, ReviewMaxStocks: 24},
}

// DefaultDisciplineConfig 统一止盈止损纪律的出厂默认（可后台配置覆盖）：
// 止损-6 / 止盈+15 / 移动回撤-6 / 深破2×(-12) / 低置信买入确认5min / 高置信观察30s /
// 止损止盈结算窗15min / 移动止盈洗盘过滤45min / 探针5s / 高置信阈值85 / 延持上限2次。
func DefaultDisciplineConfig() DisciplineConfig {
	return DisciplineConfig{
		StopLossPct:       6,
		TakeProfitPct:     15,
		MaxPullbackPct:    6,
		DeepStopMult:      2,
		BuyConfirmMin:     5,
		BuyConfirmHighSec: 30,
		ExitConfirmMin:    15,
		TrailConfirmMin:   45,
		ProbeSec:          5,
		HighConfThreshold: 85,
		// §SELLPOINT-UNIFY 边界⑥：延持信号默认一个打分周期（5min）内有效
		SellSignalMaxAgeSec: 300,
		// 延持次数上限（owner 2026-10-12 裁决，000063 事故）：默认最多延持 2 次
		MaxExtends: 2,
	}
}

// EffectiveMinBuyPrice 最低买入价闸的生效读数（单点收敛，engine 下单路径只吃这里）：
// >0=闸开（返回值即阈值）；0=关闭（负数显式关闭 或 未配置且默认也被关）。缺省 5 元。
// 为什么 0 兜默认而不是当"未配置"旁路：存量账号规则快照（DB 里的 per-user rules JSON）
// 没有该键，typed-unmarshal 得 0——若 0 语义为"关"，发版即静默放行全部低价杂毛股，
// 正是"有代码无生效"的事故家族。
// English: effective min-buy-price floor (>0 = gate on; 0 = off). Zero falls back to the
// factory default 5 because legacy per-account rule snapshots lack the key — treating 0 as
// "off" would silently disable the gate on deploy.
func (c QMTConfig) EffectiveMinBuyPrice() float64 {
	switch {
	case c.MinBuyPrice > 0:
		return c.MinBuyPrice
	case c.MinBuyPrice < 0:
		return 0 // 显式负数=关闭本闸
	}
	return 5
}

// EffectiveMinOrderAmount 单笔买入最低成交额闸的生效读数（单点收敛）：>0=闸开；0=关闭。缺省 5000 元。
// 判据口径=股价×**最终**买入股数（现金降档之后核算），与 owner 原话「成交额=股价*手数」一致。
// English: effective per-order minimum notional floor (>0 = gate on; 0 = off; default 5000).
// Notional = price × FINAL lot count (checked after the cash downgrade), matching the owner's
// definition 成交额=股价*手数.
func (c QMTConfig) EffectiveMinOrderAmount() float64 {
	switch {
	case c.MinOrderAmount > 0:
		return c.MinOrderAmount
	case c.MinOrderAmount < 0:
		return 0
	}
	return 5000
}

// defaultStrategyConfig 四战法出厂默认参数（可在前端 Settings 调整并持久化）。
// Dragon 权重为 e2e 验证过的 F1~F4 组合；DoubleBump 权重用于总分构成（Volume/Position/MA）。
// （defaultStrategyConfig returns the factory-default parameters for the four strategies, tunable
// and persistable from the frontend Settings. Dragon weights are the e2e-verified F1-F4 combo.）
func defaultStrategyConfig() StrategyConfig {
	return StrategyConfig{
		Dragon: DragonConfig{
			F1SealWeight:           0.30,
			F2ResonanceWeight:      0.25,
			F3PremiumWeight:        0.20,
			F4RsWeight:             0.25,
			PullbackMaxPct:         0.05,
			BreakerSellHalfPct:     0.08,
			BreakerSellAllPct:      0.12,
			BuyPullbackSellHalfPct: 0.05,
			BuyPullbackSellAllPct:  0.08,
			BuyDayCloseBelow:       0.03,
			NextOpenIfBelow:        0.05,
			TakeProfitPct:          10,
		},
		DoubleBump: DoubleBumpConfig{
			FirstBreakVolumeMultiple:  1.5,
			SecondBreakVolumeMultiple: 1.5,
			AdjustVolRatioMax:         3,
			AdjustDaysOverflow:        6,
			MinChangePct:              0,
			PositionWeight:            0.3,
			MAWeight:                  0.3,
			VolumeWeight:              0.4,
		},
		NShape: NShapeConfig{
			NPatternScoreThreshold: 60,
			HardStopLoss:           0.08,
		},
		DragonReturn: DragonReturnConfig{
			StopLossPct:       0.05,
			TakeProfitPct:     0.25,
			MaxHoldDays:       8,
			Target1Multiplier: 1.0,
			Target2Multiplier: 1.25,
			TrailingDrawback:  0.08,
		},
		Momentum: MomentumConfig{
			VolumePriceWeight:   40,
			MACDWeight:          30,
			TrendWeight:         30,
			SignalThreshold:     60,
			BuySignalThreshold:  75,   // 动量买入阈值：≥75 发 buy 进模拟盘动量池（§动量入模拟盘）
			MomentumGateEnabled: true, // 动量"提升才提醒"默认开启
			MomentumDeltaTol:    5,
		},
	}
}
