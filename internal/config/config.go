// Package config 提供配置管理：加载/保存 JSON 配置文件，支持策略、风控、板块、LLM 等配置。
// 本文件定义了量化交易系统所需的全部配置结构体，包括：
//   - 情绪周期阶段阈值（冰点/启动/发酵/高潮/背离/退潮）
//   - 四大战法（龙头/双响炮/N形/龙回头）的独立参数
//   - D1 事件评分规则集
//   - 风控/止损/仓位管理参数
//   - 模拟盘/实盘 QMT 交易配置
//   - 运行时内存治理/数据源/调度器/通知推送等
// 顶层 Rules 结构体聚合所有配置，Manager 负责加载/保存/按账号隔离。
package config

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"log"
	"os"
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
	// 运行时内存治理配置
	Runtime RuntimeConfig `json:"runtime"`
	// 数据源配置（§HITHINK_DATA_SOURCE_PLAN）
	Data DataConfig `json:"data"`
	// 宏观日历补充事件（§R3-8 P1-J 接线：此前类型定义存在但从未挂到 Rules）
	Calendar CalendarConfig `json:"calendar"`
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
	// ScoringIntervalSec 近实时 8a/8b 打分循环间隔（秒，默认 0 → 回退 5s）。
	// 降低可让战法信号翻转更快被检出并触发下单（信号→交易链路优化 A+B 之 B）。
	// English: near-realtime 8a/8b scoring-loop interval in seconds (0 → fallback 5s). Lowering it
	// detects strategy signal flips (and fires orders) sooner (signal→trade optimization A+B / B).
	ScoringIntervalSec int `json:"scoring_interval_sec"`
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
	// 与模拟盘 PoolBuyRule.MaxDailyBuys 同语义：防止单日信号风暴打满资金。
	// English: max buy orders per day for the real book (0 = unlimited, default 20) — mirrors the
	// paper book's MaxDailyBuys discipline against signal storms.
	// 日最大买入笔数
	DailyMaxBuys int `json:"daily_max_buys"`
	// DailyBudgetAmount 单日累计买入金额预算（元；0=不设限，默认 100000）。
	// 按当日已报买单 Price×Qty 累计，超出后拒绝新买入（卖出不受影响）。
	// English: daily buy budget in yuan (0 = unlimited, default 100000); accumulates today's placed
	// buy orders (Price×Qty) and rejects new buys past the cap (sells unaffected).
	// 日买入预算
	DailyBudgetAmount float64 `json:"daily_budget_amount"`
	// AutoSell 实盘卖出自动化开关（§GAP1.1，默认开启）：mode=auto 时，止损级建议
	// （止损/清仓类）自动全仓卖出，止盈/减仓保持提醒半自动。signal_id 按日幂等防重。
	// English: auto-sell switch for the real book (default on): in auto mode, stop-loss-class advice
	// closes the position automatically; TP/trim stay reminder-only. Idempotent per day via signal_id.
	// 自动卖出
	AutoSell bool `json:"auto_sell"`
	// Blacklist §GAP1.7 下单黑名单（纯数字或带后缀代码均可）：命中即拒绝下单。
	// 引擎每轮把 Theme.BlackList 一并同步进来；也可在 qmt 段单独配置。
	// English: §GAP1.7 order blacklist; the engine merges Theme.BlackList in every cycle.
	// 黑名单
	Blacklist []string `json:"blacklist,omitempty"`
	// Advice 持仓处理分析层（实盘持仓）规则参数。
	// 持仓处理分析层规则参数
	Advice QMTAdviceConfig `json:"advice"`
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
		AutoSell:          true,
		Advice: QMTAdviceConfig{
			AddReopenDrawdownPct: -5,
			AddSignalActive:      true,
			HoldMinProfitPct:     0,
		},
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
	// 择优结果自动应用（默认 false=推荐制需人工审批）
	OptimizeAutoApply bool `json:"optimize_auto_apply"`
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
			// 默认夜间研究步骤序列：行情装载 → 板块重建 → 因子挖掘 → 形态挖掘 → 模拟盘研究
			// （读取盘后落库的模拟盘成交/净值生成信号质量报告）→ 候选列表汇总。
			// backtest 由 BacktestEnabled 开关控制追加。
			// English: default nightly steps — dataload → sector rebuild → factor discovery → pattern
			// discovery → paper research (reads the post-close paper fills/snapshots for a signal-quality
			// report) → candidate listing. The backtest step is appended by the BacktestEnabled toggle.
			Steps:           []string{"dataload", "sector_rebuild", "discover_factors", "discover_patterns", "paper_research", "list"},
			AbortOnError:    false,
			BacktestEnabled: false,
			BacktestEvents:  0,
		},
		DataloadDuringTrade: DataloadDuringTradeConfig{
			Enabled:         true,
			IntervalMinutes: 30,
		},
		TrimIntervalMin: 15,
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
	// MaxRetryTimes D1 评分 LLM 调用轮询重试次数（含首次）。<=0 时回退默认 5。
	// 重试防丢信号：LLM 偶发超时/限流时不再轻易丢弃重要 D1 评分。
	// LLM 调用最大重试次数
	MaxRetryTimes int `json:"max_retry_times"`
	// BatchConcurrency 新闻归因（Stage0/Stage2）LLM 批量分析的最大并发批次数量。
	// <=0 时回退默认 8；API 配额充足时调高可加快盘前新闻归因吞吐，前端可热改。
	// LLM 批量分析最大并发批次
	BatchConcurrency int `json:"batch_concurrency"`
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
	// Daily鉴权TokenBudget
	DailyTokenBudget int64 `json:"daily_token_budget"`
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
	// BuySignalThreshold 动量买入阈值：动量分 ≥ 此值且数据有效时发 buy 级信号（进模拟盘自动撮合，
	// 归动量池）。默认 75（高于 watch 阈值 60 一档，避免动量信号大量直接转买单）；≤0 时回退默认。
	// English: momentum BUY threshold — score at/above this (with valid data) emits a buy signal that
	// the paper engine auto-fills into the momentum pool. Default 75; <=0 falls back to default.
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
	SetConfig(userID, key, value string) error
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
	// 主规则配置
	Rules *Rules
	// D1 事件匹配规则
	D1    *D1Config
	path  string       // 配置文件路径（全局默认）
	mu    sync.RWMutex // 保护 store/规则指针的读写锁
	store KVStore      // per-user 配置存储（可为 nil，表示不支持账号级隔离）
	// operatorID 运营数据归属账号（管理员）ID。所有运营数据（量化/模拟盘/看板/告警/LLM）
	// 统一归属该账号，后端按角色做访问控制：管理员可读写，子账号仅可读公开部分。
	// 由 main 在启动后注入 auth.AdminID()。为空时回退到调用方传入的 userID（兼容旧路径）。
	operatorID string
}

// NewManager 创建配置管理器，加载指定路径的 JSON 配置文件。
// （NewManager creates a config manager and loads the JSON config from the given path.）
func NewManager(path string) *Manager {
	m := &Manager{
		Rules: DefaultRules,
		D1:    &D1Config{},
		path:  path,
	}
	normalizeD1(m.D1)
	m.Load()
	normalizeD1(m.D1)
	return m
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
func (m *Manager) userRules(userID string) *Rules {
	if m.store == nil || userID == "" {
		return m.Rules
	}
	m.mu.RLock()
	raw, ok := m.store.GetConfig(userID, perUserKey)
	m.mu.RUnlock()
	if !ok || raw == "" {
		// §回退系统级覆盖：历史版本曾把账号级配置写到 userID="" 的键下。
		// 若本账号无独立覆盖，则回退到系统级键，避免配置在重启/重载后“丢失”
		// （表现为开关被自动关闭）。这属于兼容回退，不影响正常账号级覆盖优先级。
		// English: fall back to the system-level (empty userID) override so legacy
		// configs written under "" are still honored and survive restarts.
		m.mu.RLock()
		sysRaw, sysOk := m.store.GetConfig("", perUserKey)
		m.mu.RUnlock()
		if sysOk && sysRaw != "" {
			raw = sysRaw
			ok = true
		}
	}
	if !ok || raw == "" {
		cp := new(Rules)
		*cp = *m.Rules
		return cp
	}
	r := new(Rules)
	if err := json.Unmarshal([]byte(raw), r); err != nil {
		log.Printf("[config] 账号 %s 配置反序列化失败, 回退全局: %v", userID, err)
		cp := new(Rules)
		*cp = *m.Rules
		return cp
	}
	return r
}

// saveUserRules 将账号规则快照持久化到 KVStore。
// （saveUserRules persists an account's rules snapshot to the KVStore.）
func (m *Manager) saveUserRules(userID string, r *Rules) {
	if m.store == nil || userID == "" {
		return
	}
	data, err := json.Marshal(r)
	if err != nil {
		log.Printf("[config] 账号 %s 配置序列化失败: %v", userID, err)
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.store.SetConfig(userID, perUserKey, string(data)); err != nil {
		log.Printf("[config] 账号 %s 配置保存失败: %v", userID, err)
	}
}

// Get 返回当前全局规则配置指针。
// （Get returns a pointer to the current global rules config.）
func (m *Manager) Get() *Rules { return m.Rules }

// GetRulesFor 返回指定账号的交易规则快照（账号级覆盖优先，否则全局）。
// English: returns the trading-rules snapshot for a user (per-user override first, else global).
func (m *Manager) GetRulesFor(userID string) *Rules {
	if m.store == nil || userID == "" {
		return m.Rules
	}
	return m.userRules(userID)
}

// GetStrategyConfigFor 返回指定账号的策略参数配置（账号级覆盖优先，否则全局）。
// （GetStrategyConfigFor returns the strategy config for a user (account override wins, else global).）
// GetStrategyConfigFor 返回运营数据归属账号（管理员）的策略参数（运营配置系统级共享）。
func (m *Manager) GetStrategyConfigFor(userID string) *StrategyConfig {
	return &m.userRules(m.ownerOf(userID)).Strategy
}

// SetStrategyConfigFor 更新运营数据归属账号（管理员）的策略参数并持久化（系统级共享）。
func (m *Manager) SetStrategyConfigFor(userID string, cfg *StrategyConfig) {
	oid := m.ownerOf(userID)
	if m.store == nil || oid == "" {
		m.Rules.Strategy = *cfg
		m.Save()
		return
	}
	r := m.userRules(oid)
	r.Strategy = *cfg
	m.saveUserRules(oid, r)
}

// GetLLMConfigFor 返回运营数据归属账号（管理员）的 LLM 配置（运营配置系统级共享）。
func (m *Manager) GetLLMConfigFor(userID string) *LLMConfig {
	return &m.userRules(m.ownerOf(userID)).LLM
}

// SetLLMConfigFor 更新运营数据归属账号（管理员）的 LLM 配置并持久化（系统级共享）。
func (m *Manager) SetLLMConfigFor(userID string, cfg *LLMConfig) {
	oid := m.ownerOf(userID)
	if m.store == nil || oid == "" {
		m.Rules.LLM = *cfg
		m.Save()
		return
	}
	r := m.userRules(oid)
	r.LLM = *cfg
	m.saveUserRules(oid, r)
}

// GetQMTConfigFor 返回运营数据归属账号（管理员）的 QMT 实盘配置。
// 运营数据系统级共享，后端按角色鉴权：所有账号读到的是同一份（归属管理员）配置。
// English: returns the operator's (admin's) QMT live-trading config — operational data is
// system-scoped; every account reads the same owner config, access gated by role at the API layer.
func (m *Manager) GetQMTConfigFor(userID string) *QMTConfig {
	return &m.userRules(m.ownerOf(userID)).QMT
}

// SetQMTConfigFor 更新运营数据归属账号（管理员）的 QMT 实盘配置并持久化（5s 热加载生效）。
// 调用方负责校验取值合法性（mode/price_type 枚举、白名单过滤等），这里只做落库。
// English: persists the operator's QMT live-trading config (hot-reloaded within 5s);
// callers must validate enum/whitelist values — this method only stores.
func (m *Manager) SetQMTConfigFor(userID string, cfg *QMTConfig) {
	oid := m.ownerOf(userID)
	if m.store == nil || oid == "" {
		m.Rules.QMT = *cfg
		m.Save()
		return
	}
	r := m.userRules(oid)
	r.QMT = *cfg
	m.saveUserRules(oid, r)
}

// GetD1ConfigFor 返回运营数据归属账号（管理员）的 D1 事件匹配规则（运营配置系统级共享）。
func (m *Manager) GetD1ConfigFor(userID string) *D1Config {
	oid := m.ownerOf(userID)
	if m.store == nil || oid == "" {
		return m.D1
	}
	m.mu.RLock()
	raw, ok := m.store.GetConfig(oid, perUserD1Key)
	m.mu.RUnlock()
	if !ok || raw == "" {
		return m.D1
	}
	var d D1Config
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		log.Printf("[config] 账号 %s D1 配置反序列化失败, 回退全局: %v", oid, err)
		return m.D1
	}
	normalizeD1(&d)
	return &d
}

// SetD1ConfigFor 更新运营数据归属账号（管理员）的 D1 规则并持久化（系统级共享）。
func (m *Manager) SetD1ConfigFor(userID string, cfg *D1Config) {
	oid := m.ownerOf(userID)
	if m.store == nil || oid == "" {
		m.D1 = cfg
		m.Save()
		return
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		log.Printf("[config] 账号 %s D1 配置序列化失败: %v", oid, err)
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.store.SetConfig(oid, perUserD1Key, string(data)); err != nil {
		log.Printf("[config] 账号 %s D1 配置保存失败: %v", oid, err)
	}
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
func (m *Manager) SetLongShortConfigFor(userID string, c LongShortConfig) {
	oid := m.ownerOf(userID)
	if m.store == nil || oid == "" {
		return
	}
	data, err := json.Marshal(c)
	if err != nil {
		log.Printf("[config] 账号 %s 做多/做空配置序列化失败: %v", oid, err)
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.store.SetConfig(oid, perUserLongShortKey, string(data)); err != nil {
		log.Printf("[config] 账号 %s 做多/做空配置保存失败: %v", oid, err)
	}
}

// GetStrategyConfig 返回全局策略参数配置（无账号隔离时使用）。
// （GetStrategyConfig returns the global strategy config.）
func (m *Manager) GetStrategyConfig() *StrategyConfig {
	return &m.Rules.Strategy
}

// SetStrategyConfig 更新全局策略参数并持久化到文件。
// （SetStrategyConfig updates the global strategy params and persists them.）
func (m *Manager) SetStrategyConfig(cfg *StrategyConfig) {
	m.Rules.Strategy = *cfg
	m.Save()
}

// GetD1Config 返回全局 D1 事件匹配规则配置。
// （GetD1Config returns the global D1 event-matching rules config.）
func (m *Manager) GetD1Config() *D1Config {
	return m.D1
}

// SetSchedulerConfig 更新全局研究调度器配置（rules.scheduler）并持久化到文件。
// 用于前端"全量回测全局开关"等调度选项的读写。
// English: updates the global research-scheduler config (rules.scheduler) and persists it, used by the
// frontend "full-backtest global toggle" and other scheduler options.
func (m *Manager) SetSchedulerConfig(cfg *SchedulerConfig) {
	if cfg == nil {
		return
	}
	m.mu.Lock()
	m.Rules.Scheduler = *cfg
	m.mu.Unlock()
	m.Save()
}

// SetD1Config 更新全局 D1 规则并持久化到文件。
// （SetD1Config updates the global D1 rules and persists them.）
func (m *Manager) SetD1Config(cfg *D1Config) {
	m.D1 = cfg
	m.Save()
}

// GetLLMConfig 返回全局 LLM 客户端配置。
// （GetLLMConfig returns the global LLM client config.）
func (m *Manager) GetLLMConfig() *LLMConfig {
	return &m.Rules.LLM
}

// GetNotifyConfig 返回通知推送配置。
// （GetNotifyConfig returns the notification config.）
func (m *Manager) GetNotifyConfig() *NotifyConfig {
	return &m.Rules.Notify
}

// SetNotifyConfig 更新通知配置并持久化到文件。
// （SetNotifyConfig updates the notification config and persists it.）
func (m *Manager) SetNotifyConfig(cfg *NotifyConfig) {
	m.Rules.Notify = *cfg
	m.Save()
}

// SetLLMConfig 更新全局 LLM 配置并持久化到文件。
// （SetLLMConfig updates the global LLM config and persists it.）
func (m *Manager) SetLLMConfig(cfg *LLMConfig) {
	m.Rules.LLM = *cfg
	m.Save()
}

// Load 从配置文件读取并解析 JSON，更新 Rules 和 D1 配置。
// （Load reads and parses the JSON config file, updating the Rules and D1 config.）
func (m *Manager) Load() {
	data, err := os.ReadFile(m.path)
	if err != nil {
		return
	}
	var wrapper struct {
		Rules *Rules    `json:"rules"`
		D1    *D1Config `json:"d1"`
	}
	if err := json.Unmarshal(data, &wrapper); err != nil {
		log.Printf("[config] 解析配置文件失败: %v", err)
		return
	}
	if wrapper.Rules != nil {
		m.Rules = wrapper.Rules
	}
	if wrapper.D1 != nil {
		m.D1 = wrapper.D1
	}
	log.Printf("[config] 已加载配置文件: %s", m.path)
}

// Save 将当前配置序列化为 JSON 并写入文件。// （Save serializes the current config to JSON and writes it to the file.）
func (m *Manager) Save() {
	wrapper := struct {
		Rules *Rules    `json:"rules"`
		D1    *D1Config `json:"d1"`
	}{
		Rules: m.Rules,
		D1:    m.D1,
	}
	data, err := json.MarshalIndent(wrapper, "", "  ")
	if err != nil {
		log.Printf("[config] 序列化失败: %v", err)
		return
	}
	// §W3-c 统一原子写（fsync+唯一临时名）：config.json 由 quant 与 researchd 双进程写，
	// 固定 .tmp 名会互相踩踏；截断则全部账号配置回退默认。
	if err := fileutil.AtomicWrite(m.path, data, 0644); err != nil {
		log.Printf("[config] 写入失败: %v", err)
		return
	}
	log.Printf("[config] 已保存配置文件: %s", m.path)
}

// Watch §P1-6 配置热重载：轮询配置文件（默认 30s），内容变更（sha256 比对）时自动调用 Load()
// 重载全局 rules/d1，无需重启进程。ctx 取消即停止轮询。采用轮询而非 fsnotify 以避免引入额外依赖，
// 且对网络挂载/容器卷等 inotify 不可靠场景更稳健。
// English: P1-6 hot-reload — polls the config file (default 30s); on content change (sha256 compare)
// it reloads global rules/d1 without a restart. Polling avoids extra deps and works on volumes where
// inotify is unreliable. Stop on ctx cancellation.
func (m *Manager) Watch(ctx context.Context, interval time.Duration) {
	if m.path == "" {
		return
	}
	if interval <= 0 {
		interval = 30 * time.Second
	}
	last := m.checksum()
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cur := m.checksum()
				if cur == "" || cur == last {
					continue
				}
				last = cur
				m.Load()
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
	var wrapper struct {
		Rules struct {
			Scheduler json.RawMessage `json:"scheduler"`
			Data      struct {
				PrimarySource   string  `json:"primary_source"`
				ThsFactorsReady bool    `json:"ths_factors_ready"`
				OptimizeEnabled *bool   `json:"optimize_enabled"`
				HithinkQPS      float64 `json:"hithink_qps"`
			} `json:"data"`
		} `json:"rules"`
	}
	if err := json.Unmarshal(data, &wrapper); err != nil {
		log.Printf("[scheduler] 解析配置 %s 失败(用默认): %v", path, err)
		return def
	}
	if wrapper.Rules.Data.OptimizeEnabled != nil {
		def.OptimizeEnabled = *wrapper.Rules.Data.OptimizeEnabled
	}
	if wrapper.Rules.Data.PrimarySource != "" {
		def.PrimarySource = wrapper.Rules.Data.PrimarySource
	}
	def.ThsFactorsReady = wrapper.Rules.Data.ThsFactorsReady
	raw := wrapper.Rules.Scheduler
	if len(raw) == 0 || string(raw) == "null" {
		return def
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		log.Printf("[scheduler] 解析 scheduler 段失败(用默认): %v", err)
		return def
	}
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
		if v, ok := cfgStrs(sub, "steps"); ok && len(v) > 0 {
			out.Nightly.Steps = v
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
	Paper:     PaperConfig{Enabled: false, FixedAmount: 10000, MaxPositions: 10, InitialCapital: 100000},
	QMT:       DefaultQMTConfig(),
	Runtime:   RuntimeConfig{TrimAfterHours: true, TrimIntervalMin: 15},
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
