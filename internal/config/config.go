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
	TradeTime  TradeTimeConfig  `json:"trade_time"`    // 交易时段参数
	Theme      ThemeConfig      `json:"theme"`         // 主题/黑名单配置
	RiskCtrl   RiskCtrlConfig   `json:"risk_ctrl"`     // 风控参数
	Position   PositionConfig   `json:"position"`      // 仓位管理参数
	Notify     NotifyConfig     `json:"notify"`        // 通知推送参数
}

// NotifyConfig 通知推送配置：Webhook 回调地址列表（P1 清仓/止损强提醒时异步回调）。
// （NotifyConfig holds notification settings, e.g. the Webhook callback URLs used when P1
// close-out/stop-loss alerts fire.）
type NotifyConfig struct {
	WebhookURLs []string `json:"webhook_urls,omitempty"` // Webhook 地址列表（空则只走桌面/SSE）
}

// EmotionConfig 情绪周期六个阶段（冰点/启动/发酵/高潮/背离/退潮）的判定阈值。
// 各阶段由涨停家数、炸板率、连板高度等市场情绪指标的上下限共同判定。
// （EmotionConfig holds thresholds for the six emotion-cycle stages (ice/start/ferment/climax/
// divergence/retreat), jointly determined by bounds on limit-up count, open-board rate, etc.）
type EmotionConfig struct {
	EmoIceBoardMax        int     `json:"emo_ice_board_max"`        // 冰点期：涨停家数上限
	EmoIceLimitupMax      int     `json:"emo_ice_limitup_max"`      // 冰点期：连板高度上限
	EmoIceBlastMin        float64 `json:"emo_ice_blast_min"`        // 冰点期：炸板率下限
	EmoPositionIce        float64 `json:"emo_position_ice"`         // 冰点期建议仓位比例
	EmoStartBoardMax      int     `json:"emo_start_board_max"`      // 启动期：涨停家数上限
	EmoStartLimitupMin    int     `json:"emo_start_limitup_min"`    // 启动期：连板高度下限
	EmoStartLimitupMax    int     `json:"emo_start_limitup_max"`    // 启动期：连板高度上限
	EmoStartBlastMin      float64 `json:"emo_start_blast_min"`      // 启动期：炸板率下限
	EmoStartBlastMax      float64 `json:"emo_start_blast_max"`      // 启动期：炸板率上限
	EmoPositionStart      float64 `json:"emo_position_start"`       // 启动期建议仓位比例
	EmoFermentBoardMax    int     `json:"emo_ferment_board_max"`    // 发酵期：涨停家数上限
	EmoFermentLimitupMin  int     `json:"emo_ferment_limitup_min"`  // 发酵期：连板高度下限
	EmoFermentLimitupMax  int     `json:"emo_ferment_limitup_max"`  // 发酵期：连板高度上限
	EmoFermentBlastMax    float64 `json:"emo_ferment_blast_max"`    // 发酵期：炸板率上限
	EmoPositionFerment    float64 `json:"emo_position_ferment"`     // 发酵期建议仓位比例
	EmoClimaxBoardMin     int     `json:"emo_climax_board_min"`     // 高潮期：涨停家数下限
	EmoClimaxLimitupMin   int     `json:"emo_climax_limitup_min"`   // 高潮期：连板高度下限
	EmoClimaxBlastMax     float64 `json:"emo_climax_blast_max"`     // 高潮期：炸板率上限
	EmoPositionClimax     float64 `json:"emo_position_climax"`      // 高潮期建议仓位比例
	EmoDivergeBoardDrop   int     `json:"emo_diverge_board_drop"`   // 背离期：涨停家数相对峰值回落家数
	EmoDivergeLimitupDrop int     `json:"emo_diverge_limitup_drop"` // 背离期：连板高度相对峰值回落
	EmoDivergeBlastRise   float64 `json:"emo_diverge_blast_rise"`   // 背离期：炸板率抬升幅度
	EmoPositionDiverge    float64 `json:"emo_position_diverge"`     // 背离期建议仓位比例
	EmoRetreatBoardMax    int     `json:"emo_retreat_board_max"`    // 退潮期：涨停家数上限
	EmoRetreatLimitupMax  int     `json:"emo_retreat_limitup_max"`  // 退潮期：连板高度上限
	EmoRetreatBlastMin    float64 `json:"emo_retreat_blast_min"`    // 退潮期：炸板率下限
	EmoPositionRetreat    float64 `json:"emo_position_retreat"`     // 退潮期建议仓位比例
}

// MainSectorConfig 主线板块识别配置：涨停家数、成交量排名、涨幅等阈值。
// Bull/Shock 两套阈值分别对应牛市强势行情与震荡行情的板块强度判定。
// （MainSectorConfig configures main-sector identification via limit-up count, volume rank, gain
// thresholds, etc.; Bull/Shock presets match strong bull vs. choppy sideways markets.）
type MainSectorConfig struct {
	MainSectorLimitupBull  int               `json:"main_sector_limitup_bull"`   // 牛市：板块涨停家数下限
	MainSectorVolrankBull  int               `json:"main_sector_volrank_bull"`   // 牛市：板块成交量排名阈值
	MainSectorGain2dBull   float64           `json:"main_sector_gain2d_bull"`    // 牛市：两日板块涨幅下限
	MainSectorLimitupShock int               `json:"main_sector_limitup_shock"`  // 震荡市：板块涨停家数下限
	MainSectorVolrankShock int               `json:"main_sector_volrank_shock"`  // 震荡市：板块成交量排名阈值
	MainSectorGain2dShock  float64           `json:"main_sector_gain2d_shock"`   // 震荡市：两日板块涨幅下限
	MainSectorMaxCount     int               `json:"main_sector_max_count"`      // 主线板块最大数量
	SectorEventMap         map[string]string `json:"sector_event_map,omitempty"` // 板块→事件映射（可选）
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
	// <=0 时回退默认 4；API 配额充足时调高可加快盘前新闻归因吞吐，前端可热改。
	BatchConcurrency int `json:"batch_concurrency"`
}

// StreamingEnabled 返回流式响应是否启用：未显式配置（nil）时默认开启。
// （StreamingEnabled reports whether streaming is enabled; nil (unset) means enabled by default.）
func (c *LLMConfig) StreamingEnabled() bool {
	if c == nil || c.Stream == nil {
		return true
	}
	return *c.Stream
}

// TradeTimeConfig 交易时段参数（HHMM 整数格式）。
// （TradeTimeConfig holds trading-session parameters in HHMM integer format.）
type TradeTimeConfig struct {
	FullOpen       int `json:"full_open"`       // 完整开盘时间（默认 915）
	AfternoonStart int `json:"afternoon_start"` // 下午开盘时间（默认 1300）
	TradeClose     int `json:"trade_close"`     // 收盘时间（默认 1500）
}

// ThemeConfig 主题白名单和黑名单。
// （ThemeConfig is the theme watch-list and black-list configuration.）
type ThemeConfig struct {
	WatchList []string `json:"watch_list"` // 关注主题列表
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
	StopLoss               StopLossConfig   `json:"stop_loss"`                 // 止损规则
	Compliance             ComplianceConfig `json:"compliance"`                // 合规模式
	M8Enabled              bool             `json:"m8_enabled"`                // 是否启用 M8 风控
	M8PortfolioDrawdownPct float64          `json:"m8_portfolio_drawdown_pct"` // 组合最大回撤百分比
	PerStockMax            float64          `json:"per_stock_max"`             // 单只股票最大仓位比例
}

// StopLossConfig 止损配置：买入后回撤阶梯规则。
// （StopLossConfig holds stop-loss rules as a ladder of post-buy drawdown thresholds.）
type StopLossConfig struct {
	DrawdownAfterBuy []DrawdownRule `json:"drawdown_after_buy"` // 买入后回撤阶梯规则
}

// PositionConfig 仓位配置：总仓位上限 + 持仓当日跌幅提醒阈值。
// （PositionConfig caps the total portfolio position and sets the daily-drop alert threshold.）
type PositionConfig struct {
	MaxTotalPositionPct float64 `json:"max_total_position_pct"` // 最大总仓位比例
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
	F3OneBoardDiscount     float64 `json:"f3_one_board_discount"`      // 一板龙头 F3 溢价折扣
	PullbackMaxPct         float64 `json:"pullback_max_pct"`           // 买入后最大回撤容忍比例
	BreakerSellHalfPct     float64 `json:"breaker_sell_half_pct"`      // 破板跌幅达此值减半仓
	BreakerSellAllPct      float64 `json:"breaker_sell_all_pct"`       // 破板跌幅达此值清仓
	BuyPullbackSellHalfPct float64 `json:"buy_pullback_sell_half_pct"` // 买入后回撤减半仓阈值
	BuyPullbackSellAllPct  float64 `json:"buy_pullback_sell_all_pct"`  // 买入后回撤清仓阈值
	BuyDayCloseBelow       float64 `json:"buy_day_close_below"`        // 买入日收盘低于买入价比例止损
	NextOpenIfBelow        float64 `json:"next_open_if_below"`         // 次日开盘低于此比例则卖出
	HardBreakoutOverride   bool    `json:"hard_breakout_override"`     // 是否强制以突破方式买入
}

// DoubleBumpConfig 双响炮战法参数：一突/二突放量倍数、调整周期、仓位比例等。
// （DoubleBumpConfig tunes the double-bump strategy: first/second breakout volume multiples, adjustment window, position ratios.）
type DoubleBumpConfig struct {
	FirstBreakVolumeMultiple  float64 `json:"first_break_volume_multiple"`  // 一突放量倍数阈值
	SecondBreakVolumeMultiple float64 `json:"second_break_volume_multiple"` // 二突放量倍数阈值
	BigCandleThreshold        float64 `json:"big_candle_threshold"`         // 大阳线涨幅阈值(%)
	AdjustVolRatioMax         float64 `json:"adjust_vol_ratio_max"`         // 调整期最大量比
	PullbackToEntityPct       float64 `json:"pullback_to_entity_pct"`       // 回调至前阳线实体比例
	AdjustDaysMin             int     `json:"adjust_days_min"`              // 最短调整天数
	AdjustDaysMax             int     `json:"adjust_days_max"`              // 最长调整天数
	AdjustDaysOverflow        int     `json:"adjust_days_overflow"`         // 调整超期天数（超期判弱）
	MinChangePct              float64 `json:"min_change_pct"`               // 第二波当日最低涨跌幅(%)：<=该值判无效，水下不评买入
	PositionWeight            float64 `json:"position_weight"`              // 仓位因子权重
	MAWeight                  float64 `json:"ma_weight"`                    // 均线因子权重
	SectorWeight              float64 `json:"sector_weight"`                // 板块因子权重
	VolumeWeight              float64 `json:"volume_weight"`                // 量能因子权重
	FirstBreakoutPositionPct  string  `json:"first_breakout_position_pct"`  // 一突破位仓位比例
	SecondBreakoutPositionPct string  `json:"second_breakout_position_pct"` // 二突破位仓位比例
	ThirdBreakoutPositionMode string  `json:"third_breakout_position_mode"` // 三突破位仓位模式
	DoubleBumpTakeProfitPct   float64 `json:"double_bump_take_profit_pct"`  // 双响炮止盈比例
}

// NShapeConfig N 形战法参数：D1~D4 评分阈值、旗形整理区间、突破量比等。
// （NShapeConfig tunes the N-shape strategy: D1-D4 score thresholds, flag-consolidation window, breakout volume ratios.）
type NShapeConfig struct {
	NPatternScoreThreshold  float64 `json:"n_pattern_score_threshold"`    // N 形形态总分阈值
	NShapeD1Threshold       float64 `json:"n_shape_D1_threshold"`         // D1 拉升幅度阈值
	NShapeD2MinFull         float64 `json:"n_shape_D2_min_full"`          // D2 完整回调最小幅度
	NShapeD3Over            float64 `json:"n_shape_D3_over"`              // D3 再拉升幅度阈值
	OversoldPbRatio         float64 `json:"oversold_pb_ratio"`            // 超卖回踩比例
	NShapeEntryLeftPct      float64 `json:"n_shape_entry_left_pct"`       // 左侧买点仓位比例
	NShapeEntryRightPct     float64 `json:"n_shape_entry_right_pct"`      // 右侧买点仓位比例
	NShapeTotalMaxPct       float64 `json:"n_shape_total_max_pct"`        // N 形总仓位上限
	BreakoutRatio           float64 `json:"n_shape_breakout_ratio"`       // 突破涨幅比例
	VolRatio                float64 `json:"n_shape_vol_ratio"`            // 突破量比阈值
	FlagRetreatPct          float64 `json:"n_shape_flag_retreat_pct"`     // 旗形整理最大回调比例
	NFlagVolRatioMax        float64 `json:"n_flag_vol_ratio_max"`         // 旗形整理最大量比
	NSecondBreakVolRatio    float64 `json:"n_second_break_vol_ratio"`     // 二次突破量比阈值
	NSecondBreakMacdRedBars int     `json:"n_second_break_macd_red_bars"` // 二次突破所需 MACD 红柱数
	NFlagDurationMin        int     `json:"n_flag_duration_min"`          // 旗形最短天数
	NFlagDurationMax        int     `json:"n_flag_duration_max"`          // 旗形最长天数
	NSecondBreakTimeLimit   string  `json:"n_second_break_time_limit"`    // 二次突破最晚时间(HH:MM)
	HardStopLoss            float64 `json:"hard_stop_loss"`               // 硬止损比例
	SectorGainPctMin        float64 `json:"sector_gain_pct_min"`          // 板块涨幅下限
}

// DragonReturnConfig 龙回头战法参数：回调幅度、量缩比、止盈止损、持仓天数等。
// （DragonReturnConfig tunes the dragon-return strategy: pullback depth, volume-shrink ratio, take-profit/stop-loss, hold days.）
type DragonReturnConfig struct {
	MinPullbackPct     float64 `json:"min_pullback_pct"`     // 最小回调幅度
	MaxPullbackPct     float64 `json:"max_pullback_pct"`     // 最大回调幅度
	VolumeShrinkRatio  float64 `json:"volume_shrink_ratio"`  // 回调期量缩比阈值
	ReboundVolumeRatio float64 `json:"rebound_volume_ratio"` // 反弹放量比阈值
	StopLossPct        float64 `json:"stop_loss_pct"`        // 止损比例
	TakeProfitPct      float64 `json:"take_profit_pct"`      // 止盈比例
	MaxHoldDays        int     `json:"max_hold_days"`        // 最长持仓天数
	Target1Multiplier  float64 `json:"target1_multiplier"`   // 目标价 1 倍数
	Target2Multiplier  float64 `json:"target2_multiplier"`   // 目标价 2 倍数
	TrailingDrawback   float64 `json:"trailing_drawback"`    // 移动止盈回撤幅度
}

// D1Rule D1 事件匹配规则：模式匹配、方向、评分、是否阻断。
// （D1Rule is an event-matching rule for D1 scoring: pattern, direction, score and block flag.）
type D1Rule struct {
	Pattern   string  `json:"pattern"`           // 匹配模式
	Direction string  `json:"direction"`         // 方向：利好/利空
	Score     float64 `json:"score"`             // 匹配得分
	Blocked   bool    `json:"blocked,omitempty"` // 是否阻断（负面事件）
}

// D1Config D1 事件匹配规则集。
// （D1Config is the set of D1 event-matching rules.）
type D1Config struct {
	Rules []D1Rule `json:"rules"` // D1 规则列表
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
	m.Load()
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

// Save 将当前配置序列化为 JSON 并写入文件。
// （Save serializes the current config to JSON and writes it to the file.）
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
		DailyDropAlertPct: 5,
	},
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
			F3OneBoardDiscount:     0.5,
			PullbackMaxPct:         0.05,
			BreakerSellHalfPct:     0.08,
			BreakerSellAllPct:      0.12,
			BuyPullbackSellHalfPct: 0.05,
			BuyPullbackSellAllPct:  0.08,
			BuyDayCloseBelow:       0.03,
			NextOpenIfBelow:        0.05,
		},
		DoubleBump: DoubleBumpConfig{
			FirstBreakVolumeMultiple:  1.5,
			SecondBreakVolumeMultiple: 1.5,
			BigCandleThreshold:        5,
			AdjustVolRatioMax:         3,
			PullbackToEntityPct:       50,
			AdjustDaysMin:             1,
			AdjustDaysMax:             5,
			AdjustDaysOverflow:        6,
			MinChangePct:              0,
			PositionWeight:            0.3,
			MAWeight:                  0.3,
			SectorWeight:              0.2,
			VolumeWeight:              0.4,
		},
		NShape: NShapeConfig{
			NPatternScoreThreshold:  60,
			NShapeD1Threshold:       0.5,
			NShapeD2MinFull:         15,
			NShapeD3Over:            0.5,
			OversoldPbRatio:         0.5,
			NShapeEntryLeftPct:      0.5,
			NShapeEntryRightPct:     0.5,
			NShapeTotalMaxPct:       1.0,
			BreakoutRatio:           1.05,
			VolRatio:                1.8,
			FlagRetreatPct:          0.15,
			NFlagVolRatioMax:        0.8,
			NSecondBreakVolRatio:    1.5,
			NSecondBreakMacdRedBars: 2,
			NFlagDurationMin:        2,
			NFlagDurationMax:        5,
			NSecondBreakTimeLimit:   "10:00",
			HardStopLoss:            0.08,
			SectorGainPctMin:        1.0,
		},
		DragonReturn: DragonReturnConfig{
			MinPullbackPct:     0.15,
			MaxPullbackPct:     0.30,
			VolumeShrinkRatio:  0.5,
			ReboundVolumeRatio: 1.2,
			StopLossPct:        0.05,
			TakeProfitPct:      0.25,
			MaxHoldDays:        8,
			Target1Multiplier:  1.0,
			Target2Multiplier:  1.25,
			TrailingDrawback:   0.08,
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
