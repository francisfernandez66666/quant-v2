// Package config 提供配置管理：加载/保存 JSON 配置文件，支持策略、风控、板块、LLM 等配置。
// （Package config provides configuration management: loading/saving a JSON config file,
// covering strategy, risk control, sector, LLM and other settings.）
package config

import (
	"encoding/json"
	"log"
	"os"
	"sync"
)

// LaodengConfig Laodeng 评分系统配置。
// （LaodengConfig is the configuration for the Laodeng scoring system.）
type LaodengConfig struct {
	Enabled      bool    `json:"enabled"`        // 是否启用 Laodeng 评分修正
	MarketCapMin float64 `json:"market_cap_min"` // 最低流通市值（亿）
	PeMax        float64 `json:"pe_max"`         // 最大市盈率阈值
	TurnoverMin  float64 `json:"turnover_min"`   // 最低换手率
	TechPenalty  float64 `json:"tech_penalty"`   // 技术面扣分系数
	WeightScore  float64 `json:"weight_score"`   // 评分权重
}

// Rules 顶层规则配置，包含情绪周期、策略、板块、风控等完整配置。
// （Rules is the top-level rules config aggregating emotion cycle, strategy, sector, risk control, etc.）
type Rules struct {
	Emotion    EmotionConfig    `json:"emotion_cycle"` // 情绪周期阶段阈值
	Strategy   StrategyConfig   `json:"strategy"`      // 各策略参数
	Laodeng    LaodengConfig    `json:"laodeng"`       // Laodeng 评分
	MainSector MainSectorConfig `json:"main_sector"`   // 主线板块配置
	LLM        LLMConfig        `json:"llm"`           // LLM 客户端配置
	Theme      ThemeConfig      `json:"theme"`         // 主题/黑名单配置
	RiskCtrl   RiskCtrlConfig   `json:"risk_ctrl"`     // 风控参数
	Position   PositionConfig   `json:"position"`      // 仓位管理参数
	Notify     NotifyConfig     `json:"notify"`        // 通知推送参数
	Scheduler  SchedulerConfig  `json:"scheduler"`     // 研究调度器配置（quant-research 服务读取）
	Paper      PaperConfig      `json:"paper"`         // 模拟盘/纸面交易配置
	QMT        QMTConfig        `json:"qmt"`           // 东莞证券 MiniQMT 实盘交易配置
	Runtime    RuntimeConfig    `json:"runtime"`       // 运行时内存治理配置
	Data       DataConfig       `json:"data"`          // 数据源配置（§HITHINK_DATA_SOURCE_PLAN）
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
	TrimAfterHours bool `json:"trim_after_hours"`
	// TrimIntervalMin 盘后释放节流间隔（分钟，默认 15）。
	// English: after-hours trim throttle interval in minutes (default 15).
	TrimIntervalMin int `json:"trim_interval_min"`
}

// PaperConfig 模拟盘（纸面交易）配置：把 buy 信号按实时价自动撮合成虚拟持仓，独立于真实持仓。
// 默认关闭（Enabled=false），开启后引擎在每轮信号产出时自动撮合。
// English: paper-trading config — auto-fills buy signals at the live price into virtual positions,
// isolated from the real book. Off by default; when enabled the engine fills each signal round.
type PaperConfig struct {
	Enabled        bool    `json:"enabled"`         // 总开关（默认 false）
	FixedAmount    float64 `json:"fixed_amount"`    // 每票固定买入资金（元，默认 10000）
	MaxPositions   int     `json:"max_positions"`   // 最大并行持仓数（默认 10）
	InitialCapital float64 `json:"initial_capital"` // 初始资金（元，默认 100000）
	// AutoSell 卖出信号自动成交开关（阶段1.1 全自动执行）：nil/未配置=开启。开启时 清仓/减仓/
	// 硬止盈/硬止损 告警直接在模拟盘自动平仓；关闭则卖出仅提醒、仍需手动。
	// English: auto-sell switch (full-auto execution); nil/unset = on. When on, 清仓/减仓/hard-TP/hard-SL
	// alerts close paper positions automatically; when off, sells stay reminder-only (manual).
	AutoSell *bool `json:"auto_sell,omitempty"`
}

// QMTAdviceConfig 持仓处理分析层（实盘持仓）规则参数：加仓/格局判定阈值。
// English: position-advice layer rules for the real book: add-position and hold(格局) thresholds.
type QMTAdviceConfig struct {
	// AddReopenDrawdownPct 加仓判定：现价相对持仓最高价（highest_price）回撤不超过该值才允许加仓
	// （负值表示回撤幅度上限，如 -5 表示回撤超 5% 后不再建议加仓）。
	AddReopenDrawdownPct float64 `json:"add_reopen_drawdown_pct"`
	// AddSignalActive 加仓判定：是否要求该股信号仍活跃（StockScores.SignalActive）。
	AddSignalActive bool `json:"add_signal_active"`
	// HoldMinProfitPct 格局判定：现价相对成本盈利不低于该值（%）才建议格局（继续持有）。
	HoldMinProfitPct float64 `json:"hold_min_profit_pct"`
}

// QMTConfig 东莞证券 MiniQMT 实盘交易配置：把首尔侧的决策（信号/持仓建议）转发给国内 Windows 网关执行真实下单，
// 网关回报（成交/持仓/断线）经 /api/qmt/report 回传。与纸面账本（PaperConfig）完全独立（双账本并存）。
// English: Guoxin MiniQMT live-trading config — forwards Seoul-side decisions (signals / position advice)
// to the domestic Windows gateway for real orders; gateway reports (fills/positions/disconnect) come back
// via /api/qmt/report. Fully independent of the paper book (PaperConfig); the two books coexist.
type QMTConfig struct {
	// Enabled 总开关：是否传递信号/建议给交易服务器（热加载）。false 时实盘链路整体停用，纸面不受影响。
	Enabled bool `json:"enabled"`
	// Mode auto=全自动（信号 emit 直接下单）/ manual=半自动（前端确认后下单）。默认 manual。
	Mode string `json:"mode"`
	// GatewayURL 国内网关地址（如 https://<国内IP>:8789）。
	GatewayURL string `json:"gateway_url"`
	// Token 与网关双向鉴权的 Bearer token。
	Token string `json:"token"`
	// PriceType market=对手价（网关取实时盘口）/ limit=按信号参考价限价。默认 market。
	PriceType string `json:"price_type"`
	// FixedAmount 单票买入金额（元，默认 10000）。
	FixedAmount float64 `json:"fixed_amount"`
	// MaxPositions 最大并行实盘持仓数（默认 10，双端校验）。
	MaxPositions int `json:"max_positions"`
	// InitialCapital 初始实盘资金（元，默认 100000，用于仓位约束预检）。
	InitialCapital float64 `json:"initial_capital"`
	// Strategies 转发策略白名单（空=全部）。
	Strategies []string `json:"strategies"`
	// TimeoutSec 下单请求超时秒数（默认 10）。
	TimeoutSec int `json:"timeout_sec"`
	// MissHeartbeatSec 心跳超时秒数：网关 /health 连续失联超过该值 → 熔断暂停下单并告警（默认 120）。
	MissHeartbeatSec int `json:"miss_heartbeat_sec"`
	// Advice 持仓处理分析层（实盘持仓）规则参数。
	Advice QMTAdviceConfig `json:"advice"`
}

// DefaultQMTConfig 返回 QMT 实盘配置出厂默认：enabled=false（默认关闭）、manual 半自动、对手价。
// English: returns factory-default QMT live-trading config: disabled, manual mode, market price.
func DefaultQMTConfig() QMTConfig {
	return QMTConfig{
		Enabled:          false,
		Mode:             "manual",
		PriceType:        "market",
		FixedAmount:      10000,
		MaxPositions:     10,
		InitialCapital:   100000,
		TimeoutSec:       10,
		MissHeartbeatSec: 120,
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
	PrimarySource     string `json:"primary_source"`      // hithink(同花顺(新)优先) | baostock(旧表)
	ThsFactorsReady   bool   `json:"ths_factors_ready"`   // 复权因子对账门禁（默认 false）
	OptimizeEnabled   bool   `json:"optimize_enabled"`    // 夜间链自动追加全库寻优步骤（默认 false=推荐制手动触发）
	OptimizeAutoApply bool   `json:"optimize_auto_apply"` // 择优结果自动应用（默认 false=推荐制需人工审批）
}

// SchedulerConfig 按时段切换的研究调度器配置（由独立的 quant-research 服务读取）。
// 交易时段：只做 dataload 增量下载（绝不回测/研究）；盘后/周末：跑完整夜间研究作业。
// English: session-based research scheduler config (read by the standalone quant-research service).
// Trading hours run dataload incremental download only (never backtest/research); after-hours and
// weekends run the full nightly research job.
type SchedulerConfig struct {
	Enabled             bool                      `json:"enabled"`                 // 总开关（默认 true）
	ResearchBin         string                    `json:"research_bin"`            // research 二进制路径
	DataloadBin         string                    `json:"dataload_bin"`            // dataload 二进制路径
	DB                  string                    `json:"db"`                      // 研究库路径（trading.db）
	PyURL               string                    `json:"pyurl"`                   // baostock sidecar 地址（默认 http://127.0.0.1:8787）
	Nightly             NightlyConfig             `json:"nightly"`                 // 盘后/周末夜间作业
	DataloadDuringTrade DataloadDuringTradeConfig `json:"dataload_during_trading"` // 交易时段增量下载
	// StepTimeoutMin 夜间作业单步超时（分钟，默认 90，0=用默认）：超时 kill 子进程并记 error，
	// 防止单步挂死拖死整链（曾发生 dataload 因 baostock 封 IP 卡 21h、step_index 停在 0）。
	// English: per-step timeout for the nightly job (minutes, default 90, 0 = default): on expiry the
	// child is killed and the step errors out, so one hung step can't stall the whole chain (a dataload
	// once hung 21h on a baostock IP ban with step_index stuck at 0).
	StepTimeoutMin int `json:"step_timeout_min"`
	// TrimIntervalMin 盘中内存释放节流间隔（分钟，默认 15）：活跃时段 researchd 自身
	// 定时 runtime.GC()+debug.FreeOSMemory() 并防御性清理残留的 research/discover 子进程，
	// 保证研究绝不残留盘中（物理内存让给盘中的 quant 常驻服务）。
	// English: in-session trim throttle in minutes (default 15): during active sessions the researchd
	// daemon periodically GC+FreeOSMemory itself and defensively kills leftover research/discover child
	// processes, so research never lingers during trading hours (leaving RAM to the quant engine).
	TrimIntervalMin int `json:"trim_interval_min"`
	// MinFreeMemMB 内存总闸阈值(MB)：系统可用内存低于该值时调度器不出队，任务留队列。
	// English: memory gate — tasks stay queued when system MemAvailable drops below this.
	MinFreeMemMB int `json:"min_free_mem_mb"`
	// §数据源路由（§HITHINK_DATA_SOURCE_PLAN）：研究/回测取数主源与复权门禁。
	PrimarySource   string `json:"primary_source"`    // hithink | baostock（默认 baostock=旧表，安全）
	ThsFactorsReady bool   `json:"ths_factors_ready"` // 复权对账门禁：通过后置 true，HfqBars 才走 ths 因子
	OptimizeEnabled bool   `json:"optimize_enabled"` // 夜间自动寻优开关（默认 true，推荐制）
}

// NightlyConfig 夜间研究作业配置（盘后/周末触发）。
type NightlyConfig struct {
	StartHHMM        int      `json:"start_hhmm"`         // 交易日盘后启动时间 HHMM（默认 1530）
	WeekendStartHHMM int      `json:"weekend_start_hhmm"` // 周末启动时间 HHMM（默认 1530，周六周日各跑一次）
	Steps            []string `json:"steps"`              // 步骤序列（dataload/sector_rebuild/discover_factors/discover_patterns/list）
	AbortOnError     bool     `json:"abort_on_error"`     // 单步失败是否终止整链（默认 false=记录后继续）
	// BacktestEnabled 是否在发现因子候选后追加一次 B4 全链路回测，把候选的「回测超额」
	// （avg_excess）填上（前端原本显示"未测"）。默认 false（省时省 CPU）。
	// English: when true, after factor discovery the nightly job also runs a B4 full-chain backtest
	// on the newest proposed factor candidate, filling in its "回测超额" (avg_excess) — the field the
	// UI shows as "未测" otherwise. Default false to save time/CPU.
	BacktestEnabled bool `json:"backtest_enabled"`
	// BacktestEvents B4 回测事件数上限（backtest_enabled 时生效；0=用默认合理值）。
	BacktestEvents int `json:"backtest_events"`
}

// DataloadDuringTradeConfig 交易时段增量下载配置（只下载，不含任何研究/回测）。
type DataloadDuringTradeConfig struct {
	Enabled         bool `json:"enabled"`          // 开关（默认 true）
	IntervalMinutes int  `json:"interval_minutes"` // 间隔分钟（默认 30）
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
	WebhookURLs []string   `json:"webhook_urls,omitempty"` // Webhook 地址列表（空则只走桌面/SSE）
	Push        PushConfig `json:"push,omitempty"`         // 外部推送网关配置（APK 后台/离线触达）
}

// PushConfig 外部推送网关配置。
// Provider 为 "jpush" 时使用极光 REST API（AppKey+Secret 鉴权，Alias 指定推送目标设备别名）；
// 否则使用通用 webhook 网关（URL 指向接收 JSON 的推送地址）。
// Enabled 关闭时不启用推送网关。
// （PushConfig configures the external push gateway. Provider "jpush" uses the JPush REST API
// (AppKey+Secret auth, Alias targets the device alias); otherwise the generic webhook gateway
// POSTs JSON to URL. Enabled=false disables the gateway.）
type PushConfig struct {
	Enabled  bool   `json:"enabled"`           // 是否启用外部推送网关
	Provider string `json:"provider"`          // 网关类型：jpush | webhook（默认 webhook）
	URL      string `json:"url,omitempty"`     // webhook 推送接收地址（JSON POST）
	AppKey   string `json:"app_key,omitempty"` // 极光 AppKey（服务端推送鉴权用）
	Secret   string `json:"secret,omitempty"`  // 极光 Master Secret（服务端推送鉴权用，勿入库/勿进 APK）
	Alias    string `json:"alias,omitempty"`   // 极光推送目标设备别名（默认 quant_owner）
}

// EmotionConfig 情绪周期六个阶段（冰点/启动/发酵/高潮/背离/退潮）的判定阈值。
// 各阶段由涨停家数、炸板率、连板高度等市场情绪指标的上下限共同判定。
// （EmotionConfig holds thresholds for the six emotion-cycle stages (ice/start/ferment/climax/
// divergence/retreat), jointly determined by bounds on limit-up count, open-board rate, etc.）
type EmotionConfig struct {
	EmoIceBoardMax        int     `json:"emo_ice_board_max"`        // 冰点期：涨停家数上限
	EmoIceLimitupMax      int     `json:"emo_ice_limitup_max"`      // 冰点期：连板高度上限
	EmoIceBlastMin        float64 `json:"emo_ice_blast_min"`        // 冰点期：炸板率下限
	EmoStartBoardMax      int     `json:"emo_start_board_max"`      // 启动期：涨停家数上限
	EmoStartLimitupMin    int     `json:"emo_start_limitup_min"`    // 启动期：连板高度下限
	EmoStartLimitupMax    int     `json:"emo_start_limitup_max"`    // 启动期：连板高度上限
	EmoStartBlastMin      float64 `json:"emo_start_blast_min"`      // 启动期：炸板率下限
	EmoStartBlastMax      float64 `json:"emo_start_blast_max"`      // 启动期：炸板率上限
	EmoFermentBoardMax    int     `json:"emo_ferment_board_max"`    // 发酵期：涨停家数上限
	EmoFermentLimitupMin  int     `json:"emo_ferment_limitup_min"`  // 发酵期：连板高度下限
	EmoFermentLimitupMax  int     `json:"emo_ferment_limitup_max"`  // 发酵期：连板高度上限
	EmoFermentBlastMax    float64 `json:"emo_ferment_blast_max"`    // 发酵期：炸板率上限
	EmoClimaxBoardMin     int     `json:"emo_climax_board_min"`     // 高潮期：涨停家数下限
	EmoClimaxLimitupMin   int     `json:"emo_climax_limitup_min"`   // 高潮期：连板高度下限
	EmoClimaxBlastMax     float64 `json:"emo_climax_blast_max"`     // 高潮期：炸板率上限
	EmoDivergeBoardDrop   int     `json:"emo_diverge_board_drop"`   // 背离期：涨停家数相对峰值回落家数
	EmoDivergeLimitupDrop int     `json:"emo_diverge_limitup_drop"` // 背离期：连板高度相对峰值回落
	EmoDivergeBlastRise   float64 `json:"emo_diverge_blast_rise"`   // 背离期：炸板率抬升幅度
	EmoRetreatBoardMax    int     `json:"emo_retreat_board_max"`    // 退潮期：涨停家数上限
	EmoRetreatLimitupMax  int     `json:"emo_retreat_limitup_max"`  // 退潮期：连板高度上限
	EmoRetreatBlastMin    float64 `json:"emo_retreat_blast_min"`    // 退潮期：炸板率下限
	// BlockBuyPhases 禁止开仓的情绪周期阶段列表（C5）：这些阶段下四战法均不发买入信号
	// （降级为 watch 观察）。空列表时默认仅 ["衰退"]（与 N 形既有情绪硬闸一致）。
	// English: emotion phases in which buying is forbidden (C5) — all four strategies downgrade buy
	// signals to watch under these phases. Empty falls back to ["衰退"] (matching N-shape's hard gate).
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
	SectorConstituentTopN int `json:"sector_constituent_top_n"`
}

// LLMConfig LLM 客户端连接配置。
// （LLMConfig is the LLM client connection configuration.）
type LLMConfig struct {
	APIURL     string `json:"api_url"`     // LLM API 地址
	Model      string `json:"model"`       // 模型名称
	TimeoutSec int    `json:"timeout_sec"` // 单次请求超时（秒），缺省 60
	// Stream 流式（SSE）响应开关。nil（缺省/未配置）= 开启（推理模型非流式首字极慢，
	// 恒开流式 + 内部回落为默认策略）；显式 false = 关闭，走一次性非流式。
	Stream *bool `json:"stream,omitempty"`
	// MaxRetryTimes D1 评分 LLM 调用轮询重试次数（含首次）。<=0 时回退默认 5。
	// 重试防丢信号：LLM 偶发超时/限流时不再轻易丢弃重要 D1 评分。
	MaxRetryTimes int `json:"max_retry_times"`
	// BatchConcurrency 新闻归因（Stage0/Stage2）LLM 批量分析的最大并发批次数量。
	// <=0 时回退默认 8；API 配额充足时调高可加快盘前新闻归因吞吐，前端可热改。
	BatchConcurrency int `json:"batch_concurrency"`
	// ClassifierModel 可选：新闻归因分类（Stage0/1 合并调用等"快速分类/初筛"）专用模型。
	// 配置轻量/快速模型可显著加快分类吞吐，把主模型留给 D1/Stage2 等深度分析；留空与主模型一致。
	// English: optional dedicated model for news-attribution classification (Stage0/1 combined calls and
	// other cheap screening). A lighter/faster model speeds classification while the main model stays on
	// deep work (D1/Stage2); empty falls back to the main model.
	ClassifierModel string `json:"classifier_model"`
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
	BlackList []string `json:"black_list"` // 排除主题黑名单
}

// DrawdownRule 回撤规则：触发阈值时执行对应操作。
// （DrawdownRule defines a drawdown rule: when the threshold is hit, the action fires.）
type DrawdownRule struct {
	Pct    float64 `json:"pct"`    // 回撤百分比阈值
	Action string  `json:"action"` // 触发操作（如 "减仓"/"清仓"）
}

// ComplianceConfig 合规配置。
// （ComplianceConfig is the compliance configuration.）
type ComplianceConfig struct {
	ComplianceMode bool `json:"compliance_mode"` // 是否启用合规模式
}

// RiskCtrlConfig 风控配置：止损规则、合规模式、组合回撤限制等。
// （RiskCtrlConfig is the risk-control config: stop-loss rules, compliance mode, portfolio drawdown cap, etc.）
type RiskCtrlConfig struct {
	Compliance             ComplianceConfig `json:"compliance"`                // 合规模式
	M8Enabled              bool             `json:"m8_enabled"`                // 是否启用 M8 风控
	M8PortfolioDrawdownPct float64          `json:"m8_portfolio_drawdown_pct"` // 组合最大回撤百分比
	PerStockMax            float64          `json:"per_stock_max"`             // 单只股票最大仓位比例
}

// StopLossConfig 止损配置：买入后回撤阶梯规则。
// （StopLossConfig holds stop-loss rules as a ladder of post-buy drawdown thresholds.）
type StopLossConfig struct {
}

// PositionConfig 仓位配置：总仓位上限 + 持仓当日跌幅提醒阈值。
// （PositionConfig caps the total portfolio position and sets the daily-drop alert threshold.）
type PositionConfig struct {
	MaxTotalPositionPct float64 `json:"max_total_position_pct"` // 最大总仓位比例
	// AutoTrackSignals 买入信号自动纸面开仓（C3）：置真时引擎把 buy 信号写入持仓记录，
	// 激活 CheckPositionsExits 离场路径（止盈/止损/超期提醒）。仅纸面记录，不真实下单。
	// （AutoTrackSignals auto-paper-opens a position on buy signals (C3): the engine writes the buy into
	// the holding log so the CheckPositionsExits exit path activates. Paper-only, never really orders.）
	AutoTrackSignals bool `json:"auto_track_signals"`
	// ATREnabled ATR 动态止损开关（C4）：置真时以 ATRStopMult×ATR 替代固定百分比止损
	// （龙头全出/双凸硬止损/N形硬止损/龙回头止损）。默认开。
	// （ATREnabled turns on ATR-based dynamic stops (C4): ATRStopMult×ATR replaces the fixed-percentage
	// stops — dragon full-out / double-bump hard stop / n-shape hard stop / dragon-return stop-loss.）
	ATREnabled bool `json:"atr_enabled"`
	// ATRStopMult ATR 止损倍数（止损距离 = 该倍数 × ATR，默认 2.5）。
	// （ATRStopMult is the ATR stop multiplier — stop distance = multiplier × ATR, default 2.5.）
	ATRStopMult float64 `json:"atr_stop_mult"`
	// DailyDropAlertPct 持仓当日跌幅(%)提醒阈值：当日涨跌幅 ≤ -该值 时，
	// 无论成本盈亏是否触及止损线，都在持仓提醒中提示（<=0 用默认 5）。
	// （DailyDropAlertPct is the intraday daily-drop alert threshold for holdings: when a held stock's
	// daily change ≤ -threshold, a holding alert fires regardless of cost-based P/L (<=0 defaults to 5).）
	DailyDropAlertPct float64 `json:"daily_drop_alert_pct"`
}

// StrategyConfig 各策略的独立参数配置。
// （StrategyConfig holds the per-strategy parameter configuration.）
type StrategyConfig struct {
	Dragon       DragonConfig       `json:"dragon"`        // 龙头战法配置
	DoubleBump   DoubleBumpConfig   `json:"double_bump"`   // 双响炮战法配置
	NShape       NShapeConfig       `json:"n_shape"`       // N 形战法配置
	DragonReturn DragonReturnConfig `json:"dragon_return"` // 龙回头战法配置
	Momentum     MomentumConfig     `json:"momentum"`      // 动量分权重配置
	MacroGate    MacroGateConfig    `json:"macro_gate"`    // 宏观利空门控配置
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
	Enabled bool `json:"enabled"`
	// Levels 触发门控的宏观事件级别（如 ["contract"]）；空时默认 ["contract"]。
	// English: macro-event levels that trigger the gate (e.g. ["contract"]); empty defaults to ["contract"].
	Levels []string `json:"levels"`
	// MinConfidence 放行买入信号的最低置信度（0~1，默认 0.85；低于此置信度的买入降级为 watch）。
	// English: minimum confidence for a buy signal to pass the gate (0~1, default 0.85); buys below are downgraded to watch.
	MinConfidence float64 `json:"min_confidence"`
	// BlockNShape 交割日是否对 N 形超短一律拦截（默认 true：超短对交割日波动最敏感）。
	// English: whether N-shape ultra-short is always blocked on delivery days (default true — ultra-short is most sensitive to delivery-day swings).
	BlockNShape bool `json:"block_n_shape"`
	// BlockMomentum 交割日是否拦截动量 watch 观察信号（默认 true）。
	// English: whether the momentum watch signal is also blocked on delivery days (default true).
	BlockMomentum bool `json:"block_momentum"`
}

// MomentumConfig 动量分权重配置（默认 量价40 + MACD30 + 走势30，合计≤100）。
// （MomentumConfig defines momentum-score weights; defaults: price-volume 40 + MACD 30 + trend 30, total ≤ 100.）
type MomentumConfig struct {
	VolumePriceWeight float64 `json:"volume_price_weight"` // 量价分权重（0~100）
	MACDWeight        float64 `json:"macd_weight"`         // MACD分权重（0~100）
	TrendWeight       float64 `json:"trend_weight"`        // 走势分权重（0~100）
	SignalThreshold   float64 `json:"signal_threshold"`    // 动量分触发信号阈值（默认 60）
	// MomentumGateEnabled 动量分"提升才提醒"门槛开关：开启后仅当动量分明显提升时
	// 才放行 double_bump/龙头/龙回头 战法信号（N 形不套用）。可热更新，前端 Settings 动量分组内开关控制。
	// English: momentum-gate switch — when on, only a meaningful momentum-score improvement lets the
	// double-bump / dragon / dragon-return strategies pass their signal (N-shape is exempt).
	MomentumGateEnabled bool `json:"momentum_gate_enabled"`
	// MomentumDeltaTol 动量分回落容忍差：当前动量分 ≥ 上一轮 − 该值 视为"未明显回落"，仍算提升。
	// 默认 5 分。English: momentum delta tolerance — current score >= prior - tolerance still counts as
	// an improvement (no obvious fall). Default 5.
	MomentumDeltaTol float64 `json:"momentum_delta_tol"`
}

// DragonConfig 龙头战法参数：多因子权重、回撤止盈止损阈值、买入条件等。
// （DragonConfig tunes the dragon-leader strategy: multi-factor weights, drawdown/take-profit/stop-loss thresholds, buy conditions.）
type DragonConfig struct {
	F1SealWeight           float64 `json:"f1_seal_weight"`             // F1 封单强度权重
	F2ResonanceWeight      float64 `json:"f2_resonance_weight"`        // F2 板块共振权重
	F3PremiumWeight        float64 `json:"f3_premium_weight"`          // F3 溢价权重
	F4RsWeight             float64 `json:"f4_rs_weight"`               // F4 相对强度(RS)权重
	PullbackMaxPct         float64 `json:"pullback_max_pct"`           // 买入后最大回撤容忍比例
	BreakerSellHalfPct     float64 `json:"breaker_sell_half_pct"`      // 破板跌幅达此值减半仓
	BreakerSellAllPct      float64 `json:"breaker_sell_all_pct"`       // 破板跌幅达此值清仓
	BuyPullbackSellHalfPct float64 `json:"buy_pullback_sell_half_pct"` // 买入后回撤减半仓阈值
	BuyPullbackSellAllPct  float64 `json:"buy_pullback_sell_all_pct"`  // 买入后回撤清仓阈值
	BuyDayCloseBelow       float64 `json:"buy_day_close_below"`        // 买入日收盘低于买入价比例止损
	NextOpenIfBelow        float64 `json:"next_open_if_below"`         // 次日开盘低于此比例则卖出
	TakeProfitPct          float64 `json:"take_profit_pct"`            // 止盈比例(%)，浮盈达此值落袋（默认 10）
	// §扫参应用（STRATEGY_OPTIMIZE_PLAN）：移动止盈回撤%(从阶段高点)与最长持仓天数。
	// 0=不启用（保持既有退出规则不变）；>0 时由 CheckExit 在既有规则之前执行——
	// 与扫参的统一出场引擎同语义，寻优冠军参数可一键应用到实盘且口径一致。
	// English: sweep-aligned trailing-stop %% and max-hold-days knobs; 0 = disabled (legacy rules only).
	TrailingDrawbackPct float64 `json:"trailing_drawback_pct,omitempty"`
	MaxHoldDays         int     `json:"max_hold_days,omitempty"`
}

// DoubleBumpConfig 双响炮战法参数：一突/二突放量倍数、调整周期、仓位比例等。
// （DoubleBumpConfig tunes the double-bump strategy: first/second breakout volume multiples, adjustment window, position ratios.）
type DoubleBumpConfig struct {
	FirstBreakVolumeMultiple  float64 `json:"first_break_volume_multiple"`  // 一突放量倍数阈值
	SecondBreakVolumeMultiple float64 `json:"second_break_volume_multiple"` // 二突放量倍数阈值
	AdjustVolRatioMax         float64 `json:"adjust_vol_ratio_max"`         // 调整期最大量比
	AdjustDaysOverflow        int     `json:"adjust_days_overflow"`         // 调整超期天数（超期判弱）
	MinChangePct              float64 `json:"min_change_pct"`               // 第二波当日最低涨跌幅(%)：<=该值判无效，水下不评买入
	PositionWeight            float64 `json:"position_weight"`              // 仓位因子权重
	MAWeight                  float64 `json:"ma_weight"`                    // 均线因子权重
	VolumeWeight              float64 `json:"volume_weight"`                // 量能因子权重
	DoubleBumpTakeProfitPct   float64 `json:"double_bump_take_profit_pct"`  // 双响炮止盈比例
	// §扫参应用（STRATEGY_OPTIMIZE_PLAN）：移动止盈回撤%(从阶段高点)与最长持仓天数。
	// 0=不启用（保持既有退出规则不变）；>0 时由 CheckExit 在既有规则之前执行——
	// 与扫参的统一出场引擎同语义，寻优冠军参数可一键应用到实盘且口径一致。
	// English: sweep-aligned trailing-stop %% and max-hold-days knobs; 0 = disabled (legacy rules only).
	TrailingDrawbackPct float64 `json:"trailing_drawback_pct,omitempty"`
	MaxHoldDays         int     `json:"max_hold_days,omitempty"`
}

// NShapeConfig N 形战法参数：D1~D4 评分阈值、旗形整理区间、突破量比等。
// （NShapeConfig tunes the N-shape strategy: D1-D4 score thresholds, flag-consolidation window, breakout volume ratios.）
type NShapeConfig struct {
	NPatternScoreThreshold float64 `json:"n_pattern_score_threshold"` // N 形形态总分阈值
	HardStopLoss           float64 `json:"hard_stop_loss"`            // 硬止损比例
	// §扫参应用（STRATEGY_OPTIMIZE_PLAN）：移动止盈回撤%(从阶段高点)与最长持仓天数。
	// 0=不启用（保持既有退出规则不变）；>0 时由 CheckExit 在既有规则之前执行——
	// 与扫参的统一出场引擎同语义，寻优冠军参数可一键应用到实盘且口径一致。
	// English: sweep-aligned trailing-stop %% and max-hold-days knobs; 0 = disabled (legacy rules only).
	TrailingDrawbackPct float64 `json:"trailing_drawback_pct,omitempty"`
	MaxHoldDays         int     `json:"max_hold_days,omitempty"`
}

// DragonReturnConfig 龙回头战法参数：回调幅度、量缩比、止盈止损、持仓天数等。
// （DragonReturnConfig tunes the dragon-return strategy: pullback depth, volume-shrink ratio, take-profit/stop-loss, hold days.）
type DragonReturnConfig struct {
	StopLossPct       float64 `json:"stop_loss_pct"`      // 止损比例
	TakeProfitPct     float64 `json:"take_profit_pct"`    // 止盈比例
	MaxHoldDays       int     `json:"max_hold_days"`      // 最长持仓天数
	Target1Multiplier float64 `json:"target1_multiplier"` // 目标价 1 倍数
	Target2Multiplier float64 `json:"target2_multiplier"` // 目标价 2 倍数
	TrailingDrawback  float64 `json:"trailing_drawback"`  // 移动止盈回撤幅度
}

// D1Rule D1 事件匹配规则：模式匹配、方向、评分、是否阻断。
// （D1Rule is an event-matching rule for D1 scoring: pattern, direction, score and block flag.）
type D1Rule struct {
	Direction string  `json:"direction"`         // 方向：利好/利空
	Score     float64 `json:"score"`             // 匹配得分
	Blocked   bool    `json:"blocked,omitempty"` // 是否阻断（负面事件）
}

// D1Config D1 事件匹配规则集 + 四战法软加成配置。
// （D1Config is the set of D1 event-matching rules plus the C1 cross-strategy soft-boost settings.）
type D1Config struct {
	Rules []D1Rule `json:"rules"` // D1 规则列表

	// BoostWeight 四战法 D1 软加成权重（C1）：非 N 战法总分 ×(1+BoostWeight×D1/40)，封顶 100。
	// ≤0 表示关闭加成（默认 0.15）。
	// （BoostWeight is the C1 soft-boost weight applied to non-N strategy totals; ≤0 disables it.）
	BoostWeight float64 `json:"boost_weight,omitempty"`
	// BoostThreshold 加成门槛：D1 分（0~40）低于该值时不做加成（默认 8）。
	// （BoostThreshold is the minimum D1 score (0~40) to trigger the soft boost.）
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
	LongEnabled  bool `json:"long_enabled"`  // 做多开关（默认开）
	ShortEnabled bool `json:"short_enabled"` // 做空开关（默认关）
}

// Manager 配置管理器，负责 JSON 配置文件的加载、保存和查询。
// 全局默认配置来自文件；每个账号可在 KVStore 中保存独立覆盖（多账号多配置）。
// （Manager is the config manager responsible for loading, saving and querying the JSON config file.
// Global defaults come from the file; each account may store its own override in the KVStore.）
type Manager struct {
	Rules *Rules    // 主规则配置
	D1    *D1Config // D1 事件匹配规则
	path  string    // 配置文件路径（全局默认）
	mu    sync.RWMutex
	store KVStore // per-user 配置存储（可为 nil，表示不支持账号级隔离）
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

// userRules 返回指定账号的规则快照；未配置账号级覆盖时回退全局 Rules。
// 快照来自 KVStore 中的 JSON，反序列化为副本，避免污染全局。
// （userRules returns the rules snapshot for a user, falling back to global Rules when
// the account has no override; the snapshot is a deserialized copy.）
func (m *Manager) userRules(userID string) *Rules {
	if m.store == nil || userID == "" {
		return m.Rules
	}
	m.mu.RLock()
	raw, ok := m.store.GetConfig(userID, perUserKey)
	m.mu.RUnlock()
	if !ok || raw == "" {
		return m.Rules
	}
	var r Rules
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		log.Printf("[config] 账号 %s 配置反序列化失败, 回退全局: %v", userID, err)
		return m.Rules
	}
	return &r
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

// Get 返回当前规则配置指针。
// （Get returns a pointer to the current rules config.）
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
func (m *Manager) GetStrategyConfigFor(userID string) *StrategyConfig {
	return &m.userRules(userID).Strategy
}

// SetStrategyConfigFor 更新指定账号的策略参数并持久化（账号级覆盖）。
// （SetStrategyConfigFor updates and persists a user's strategy params as an account override.）
func (m *Manager) SetStrategyConfigFor(userID string, cfg *StrategyConfig) {
	if m.store == nil || userID == "" {
		m.Rules.Strategy = *cfg
		m.Save()
		return
	}
	r := m.userRules(userID)
	r.Strategy = *cfg
	m.saveUserRules(userID, r)
}

// GetLLMConfigFor 返回指定账号的 LLM 配置（账号级覆盖优先，否则全局）。
func (m *Manager) GetLLMConfigFor(userID string) *LLMConfig {
	return &m.userRules(userID).LLM
}

// SetLLMConfigFor 更新指定账号的 LLM 配置并持久化（账号级覆盖）。
func (m *Manager) SetLLMConfigFor(userID string, cfg *LLMConfig) {
	if m.store == nil || userID == "" {
		m.Rules.LLM = *cfg
		m.Save()
		return
	}
	r := m.userRules(userID)
	r.LLM = *cfg
	m.saveUserRules(userID, r)
}

// GetD1ConfigFor 返回指定账号的 D1 事件匹配规则（账号级覆盖优先，否则全局）。
func (m *Manager) GetD1ConfigFor(userID string) *D1Config {
	if m.store == nil || userID == "" {
		return m.D1
	}
	m.mu.RLock()
	raw, ok := m.store.GetConfig(userID, perUserD1Key)
	m.mu.RUnlock()
	if !ok || raw == "" {
		return m.D1
	}
	var d D1Config
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		log.Printf("[config] 账号 %s D1 配置反序列化失败, 回退全局: %v", userID, err)
		return m.D1
	}
	normalizeD1(&d)
	return &d
}

// SetD1ConfigFor 更新指定账号的 D1 规则并持久化（账号级覆盖）。
func (m *Manager) SetD1ConfigFor(userID string, cfg *D1Config) {
	if m.store == nil || userID == "" {
		m.D1 = cfg
		m.Save()
		return
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		log.Printf("[config] 账号 %s D1 配置序列化失败: %v", userID, err)
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.store.SetConfig(userID, perUserD1Key, string(data)); err != nil {
		log.Printf("[config] 账号 %s D1 配置保存失败: %v", userID, err)
	}
}

// GetLongShortConfigFor 返回指定账号的做多/做空开关（账号级覆盖优先，否则全局默认：
// 做多开/做空关）。
// （GetLongShortConfigFor returns a user's long/short toggles (account override wins,
// else the global default: long on / short off).）
func (m *Manager) GetLongShortConfigFor(userID string) LongShortConfig {
	def := LongShortConfig{LongEnabled: true, ShortEnabled: false}
	if m.store == nil || userID == "" {
		return def
	}
	m.mu.RLock()
	raw, ok := m.store.GetConfig(userID, perUserLongShortKey)
	m.mu.RUnlock()
	if !ok || raw == "" {
		return def
	}
	var c LongShortConfig
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		log.Printf("[config] 账号 %s 做多/做空配置反序列化失败, 回退默认: %v", userID, err)
		return def
	}
	return c
}

// SetLongShortConfigFor 更新指定账号的做多/做空开关并持久化（账号级覆盖）。
// （SetLongShortConfigFor updates and persists a user's long/short toggles as an account override.）
func (m *Manager) SetLongShortConfigFor(userID string, c LongShortConfig) {
	if m.store == nil || userID == "" {
		return
	}
	data, err := json.Marshal(c)
	if err != nil {
		log.Printf("[config] 账号 %s 做多/做空配置序列化失败: %v", userID, err)
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.store.SetConfig(userID, perUserLongShortKey, string(data)); err != nil {
		log.Printf("[config] 账号 %s 做多/做空配置保存失败: %v", userID, err)
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
	if err := os.WriteFile(m.path, data, 0644); err != nil {
		log.Printf("[config] 写入失败: %v", err)
		return
	}
	log.Printf("[config] 已保存配置文件: %s", m.path)
}

// LoadSchedulerConfig 从配置文件读取 rules.scheduler（供独立研究服务 quant-research 使用）。
// 只覆盖 JSON 中显式出现的字段，其余回退 DefaultSchedulerConfig；文件缺失/解析失败整体回退默认。
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
	Date        string `json:"date"`         // 事件日期（YYYY-MM-DD）
	Title       string `json:"title"`        // 事件标题
	Impact      string `json:"impact"`       // 影响程度（high/medium/low）
	DaysAdvance int    `json:"days_advance"` // 提前提醒天数
}

// CalendarConfig 宏观日历配置。
// （CalendarConfig is the macro-calendar configuration.）
type CalendarConfig struct {
	Enabled bool            `json:"enabled"` // 是否启用日历告警
	Events  []CalendarEvent `json:"events"`  // 事件列表
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
			MomentumGateEnabled: true, // 动量"提升才提醒"默认开启
			MomentumDeltaTol:    5,
		},
	}
}
