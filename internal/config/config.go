// Package config 提供配置管理：加载/保存 JSON 配置文件，支持策略、风控、板块、LLM 等配置。
package config

import (
	"encoding/json"
	"log"
	"os"
)

// LaodengConfig Laodeng 评分系统配置。
type LaodengConfig struct {
	Enabled      bool    `json:"enabled"`       // 是否启用 Laodeng 评分修正
	MarketCapMin float64 `json:"market_cap_min"` // 最低流通市值（亿）
	PeMax        float64 `json:"pe_max"`         // 最大市盈率阈值
	TurnoverMin  float64 `json:"turnover_min"`   // 最低换手率
	TechPenalty  float64 `json:"tech_penalty"`   // 技术面扣分系数
	WeightScore  float64 `json:"weight_score"`   // 评分权重
}

// Rules 顶层规则配置，包含情绪周期、策略、板块、风控等完整配置。
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
}

// EmotionConfig 情绪周期六个阶段（冰点/启动/发酵/高潮/背离/退潮）的判定阈值。
type EmotionConfig struct {
	EmoIceBoardMax        int     `json:"emo_ice_board_max"`
	EmoIceLimitupMax      int     `json:"emo_ice_limitup_max"`
	EmoIceBlastMin        float64 `json:"emo_ice_blast_min"`
	EmoPositionIce        float64 `json:"emo_position_ice"`
	EmoStartBoardMax      int     `json:"emo_start_board_max"`
	EmoStartLimitupMin    int     `json:"emo_start_limitup_min"`
	EmoStartLimitupMax    int     `json:"emo_start_limitup_max"`
	EmoStartBlastMin      float64 `json:"emo_start_blast_min"`
	EmoStartBlastMax      float64 `json:"emo_start_blast_max"`
	EmoPositionStart      float64 `json:"emo_position_start"`
	EmoFermentBoardMax    int     `json:"emo_ferment_board_max"`
	EmoFermentLimitupMin  int     `json:"emo_ferment_limitup_min"`
	EmoFermentLimitupMax  int     `json:"emo_ferment_limitup_max"`
	EmoFermentBlastMax    float64 `json:"emo_ferment_blast_max"`
	EmoPositionFerment    float64 `json:"emo_position_ferment"`
	EmoClimaxBoardMin     int     `json:"emo_climax_board_min"`
	EmoClimaxLimitupMin   int     `json:"emo_climax_limitup_min"`
	EmoClimaxBlastMax     float64 `json:"emo_climax_blast_max"`
	EmoPositionClimax     float64 `json:"emo_position_climax"`
	EmoDivergeBoardDrop   int     `json:"emo_diverge_board_drop"`
	EmoDivergeLimitupDrop int     `json:"emo_diverge_limitup_drop"`
	EmoDivergeBlastRise   float64 `json:"emo_diverge_blast_rise"`
	EmoPositionDiverge    float64 `json:"emo_position_diverge"`
	EmoRetreatBoardMax    int     `json:"emo_retreat_board_max"`
	EmoRetreatLimitupMax  int     `json:"emo_retreat_limitup_max"`
	EmoRetreatBlastMin    float64 `json:"emo_retreat_blast_min"`
	EmoPositionRetreat    float64 `json:"emo_position_retreat"`
}

// MainSectorConfig 主线板块识别配置：涨停家数、成交量排名、涨幅等阈值。
type MainSectorConfig struct {
	MainSectorLimitupBull  int               `json:"main_sector_limitup_bull"`
	MainSectorVolrankBull  int               `json:"main_sector_volrank_bull"`
	MainSectorGain2dBull   float64           `json:"main_sector_gain2d_bull"`
	MainSectorLimitupShock int               `json:"main_sector_limitup_shock"`
	MainSectorVolrankShock int               `json:"main_sector_volrank_shock"`
	MainSectorGain2dShock  float64           `json:"main_sector_gain2d_shock"`
	MainSectorMaxCount     int               `json:"main_sector_max_count"`
	SectorEventMap         map[string]string `json:"sector_event_map,omitempty"`
}

// LLMConfig LLM 客户端连接配置。
type LLMConfig struct {
	APIURL string `json:"api_url"` // LLM API 地址
	Model  string `json:"model"`   // 模型名称
}

// TradeTimeConfig 交易时段参数（HHMM 整数格式）。
type TradeTimeConfig struct {
	FullOpen      int `json:"full_open"`      // 完整开盘时间（默认 915）
	AfternoonStart int `json:"afternoon_start"` // 下午开盘时间（默认 1300）
	TradeClose    int `json:"trade_close"`    // 收盘时间（默认 1500）
}

// ThemeConfig 主题白名单和黑名单。
type ThemeConfig struct {
	WatchList []string `json:"watch_list"` // 关注主题列表
	BlackList []string `json:"black_list"` // 排除主题黑名单
}

// DrawdownRule 回撤规则：触发阈值时执行对应操作。
type DrawdownRule struct {
	Pct    float64 `json:"pct"`    // 回撤百分比阈值
	Action string  `json:"action"` // 触发操作（如 "减仓"/"清仓"）
}

// ComplianceConfig 合规配置。
type ComplianceConfig struct {
	ComplianceMode bool `json:"compliance_mode"` // 是否启用合规模式
}

// RiskCtrlConfig 风控配置：止损规则、合规模式、组合回撤限制等。
type RiskCtrlConfig struct {
	StopLoss               StopLossConfig   `json:"stop_loss"`                  // 止损规则
	Compliance             ComplianceConfig `json:"compliance"`                 // 合规模式
	M8Enabled              bool             `json:"m8_enabled"`                 // 是否启用 M8 风控
	M8PortfolioDrawdownPct float64          `json:"m8_portfolio_drawdown_pct"`  // 组合最大回撤百分比
	PerStockMax            float64          `json:"per_stock_max"`              // 单只股票最大仓位比例
}

// StopLossConfig 止损配置：买入后回撤阶梯规则。
type StopLossConfig struct {
	DrawdownAfterBuy []DrawdownRule `json:"drawdown_after_buy"` // 买入后回撤阶梯规则
}

// PositionConfig 总仓位上限配置。
type PositionConfig struct {
	MaxTotalPositionPct float64 `json:"max_total_position_pct"` // 最大总仓位比例
}

// StrategyConfig 各策略的独立参数配置。
type StrategyConfig struct {
	Dragon       DragonConfig       `json:"dragon"`        // 龙头战法配置
	DoubleBump   DoubleBumpConfig   `json:"double_bump"`   // 双响炮战法配置
	NShape       NShapeConfig       `json:"n_shape"`       // N 形战法配置
	DragonReturn DragonReturnConfig `json:"dragon_return"` // 龙回头战法配置
}

// DragonConfig 龙头战法参数：多因子权重、回撤止盈止损阈值、买入条件等。
type DragonConfig struct {
	F1SealWeight           float64 `json:"f1_seal_weight"`
	F2ResonanceWeight      float64 `json:"f2_resonance_weight"`
	F3PremiumWeight        float64 `json:"f3_premium_weight"`
	F4RsWeight             float64 `json:"f4_rs_weight"`
	F3OneBoardDiscount     float64 `json:"f3_one_board_discount"`
	PullbackMaxPct         float64 `json:"pullback_max_pct"`
	BreakerSellHalfPct     float64 `json:"breaker_sell_half_pct"`
	BreakerSellAllPct      float64 `json:"breaker_sell_all_pct"`
	BuyPullbackSellHalfPct float64 `json:"buy_pullback_sell_half_pct"`
	BuyPullbackSellAllPct  float64 `json:"buy_pullback_sell_all_pct"`
	BuyDayCloseBelow       float64 `json:"buy_day_close_below"`
	NextOpenIfBelow        float64 `json:"next_open_if_below"`
	HardBreakoutOverride   bool    `json:"hard_breakout_override"`
}

// DoubleBumpConfig 双响炮战法参数：一突/二突放量倍数、调整周期、仓位比例等。
type DoubleBumpConfig struct {
	FirstBreakVolumeMultiple  float64 `json:"first_break_volume_multiple"`
	SecondBreakVolumeMultiple float64 `json:"second_break_volume_multiple"`
	BigCandleThreshold        float64 `json:"big_candle_threshold"`
	AdjustVolRatioMax         float64 `json:"adjust_vol_ratio_max"`
	PullbackToEntityPct       float64 `json:"pullback_to_entity_pct"`
	AdjustDaysMin             int     `json:"adjust_days_min"`
	AdjustDaysMax             int     `json:"adjust_days_max"`
	AdjustDaysOverflow        int     `json:"adjust_days_overflow"`
	PositionWeight            float64 `json:"position_weight"`
	MAWeight                  float64 `json:"ma_weight"`
	SectorWeight              float64 `json:"sector_weight"`
	VolumeWeight              float64 `json:"volume_weight"`
	FirstBreakoutPositionPct  string  `json:"first_breakout_position_pct"`
	SecondBreakoutPositionPct string  `json:"second_breakout_position_pct"`
	ThirdBreakoutPositionMode string  `json:"third_breakout_position_mode"`
	DoubleBumpTakeProfitPct   float64 `json:"double_bump_take_profit_pct"`
}

// NShapeConfig N 形战法参数：D1~D4 评分阈值、旗形整理区间、突破量比等。
type NShapeConfig struct {
	NPatternScoreThreshold   float64 `json:"n_pattern_score_threshold"`
	NShapeD1Threshold        float64 `json:"n_shape_D1_threshold"`
	NShapeD2MinFull          float64 `json:"n_shape_D2_min_full"`
	NShapeD3Over             float64 `json:"n_shape_D3_over"`
	OversoldPbRatio          float64 `json:"oversold_pb_ratio"`
	NShapeEntryLeftPct       float64 `json:"n_shape_entry_left_pct"`
	NShapeEntryRightPct      float64 `json:"n_shape_entry_right_pct"`
	NShapeTotalMaxPct        float64 `json:"n_shape_total_max_pct"`
	BreakoutRatio            float64 `json:"n_shape_breakout_ratio"`
	VolRatio                 float64 `json:"n_shape_vol_ratio"`
	FlagRetreatPct           float64 `json:"n_shape_flag_retreat_pct"`
	NFlagVolRatioMax         float64 `json:"n_flag_vol_ratio_max"`
	NSecondBreakVolRatio     float64 `json:"n_second_break_vol_ratio"`
	NSecondBreakMacdRedBars  int     `json:"n_second_break_macd_red_bars"`
	NFlagDurationMin         int     `json:"n_flag_duration_min"`
	NFlagDurationMax         int     `json:"n_flag_duration_max"`
	NSecondBreakTimeLimit    string  `json:"n_second_break_time_limit"`
	HardStopLoss             float64 `json:"hard_stop_loss"`
	SectorGainPctMin         float64 `json:"sector_gain_pct_min"`
}

// DragonReturnConfig 龙回头战法参数：回调幅度、量缩比、止盈止损、持仓天数等。
type DragonReturnConfig struct {
	MinPullbackPct      float64 `json:"min_pullback_pct"`
	MaxPullbackPct      float64 `json:"max_pullback_pct"`
	VolumeShrinkRatio   float64 `json:"volume_shrink_ratio"`
	ReboundVolumeRatio  float64 `json:"rebound_volume_ratio"`
	StopLossPct         float64 `json:"stop_loss_pct"`
	TakeProfitPct       float64 `json:"take_profit_pct"`
	MaxHoldDays         int     `json:"max_hold_days"`
	Target1Multiplier   float64 `json:"target1_multiplier"`
	Target2Multiplier   float64 `json:"target2_multiplier"`
	TrailingDrawback    float64 `json:"trailing_drawback"`
}

// D1Rule D1 事件匹配规则：模式匹配、方向、评分、是否阻断。
type D1Rule struct {
	Pattern   string  `json:"pattern"`            // 匹配模式
	Direction string  `json:"direction"`          // 方向：利好/利空
	Score     float64 `json:"score"`              // 匹配得分
	Blocked   bool    `json:"blocked,omitempty"`  // 是否阻断（负面事件）
}

// D1Config D1 事件匹配规则集。
type D1Config struct {
	Rules []D1Rule `json:"rules"` // D1 规则列表
}

// Manager 配置管理器，负责 JSON 配置文件的加载、保存和查询。
type Manager struct {
	Rules   *Rules   // 主规则配置
	D1      *D1Config // D1 事件匹配规则
	path    string    // 配置文件路径
}

// NewManager 创建配置管理器，加载指定路径的 JSON 配置文件。
func NewManager(path string) *Manager {
	m := &Manager{
		Rules: DefaultRules,
		D1:    &D1Config{},
		path:  path,
	}
	m.Load()
	return m
}

// Get 返回当前规则配置指针。
func (m *Manager) Get() *Rules { return m.Rules }

// GetStrategyConfig 返回策略参数配置。
func (m *Manager) GetStrategyConfig() *StrategyConfig {
	return &m.Rules.Strategy
}

// SetStrategyConfig 更新策略参数并持久化到文件。
func (m *Manager) SetStrategyConfig(cfg *StrategyConfig) {
	m.Rules.Strategy = *cfg
	m.Save()
}

// GetD1Config 返回 D1 事件匹配规则配置。
func (m *Manager) GetD1Config() *D1Config {
	return m.D1
}

// SetD1Config 更新 D1 规则并持久化到文件。
func (m *Manager) SetD1Config(cfg *D1Config) {
	m.D1 = cfg
	m.Save()
}

// GetLLMConfig 返回 LLM 客户端配置。
func (m *Manager) GetLLMConfig() *LLMConfig {
	return &m.Rules.LLM
}

// SetLLMConfig 更新 LLM 配置并持久化到文件。
func (m *Manager) SetLLMConfig(cfg *LLMConfig) {
	m.Rules.LLM = *cfg
	m.Save()
}

// Load 从配置文件读取并解析 JSON，更新 Rules 和 D1 配置。
func (m *Manager) Load() {
	data, err := os.ReadFile(m.path)
	if err != nil {
		return
	}
	var wrapper struct {
		Rules *Rules   `json:"rules"`
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
func (m *Manager) Save() {
	wrapper := struct {
		Rules *Rules   `json:"rules"`
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

type CalendarEvent struct {
	Date        string `json:"date"`
	Title       string `json:"title"`
	Impact      string `json:"impact"`
	DaysAdvance int    `json:"days_advance"`
}

type CalendarConfig struct {
	Enabled bool            `json:"enabled"`
	Events  []CalendarEvent `json:"events"`
}

var DefaultRules = &Rules{}
