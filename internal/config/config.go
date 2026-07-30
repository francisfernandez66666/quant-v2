package config

type Rules struct {
	Emotion    EmotionConfig    `json:"emotion_cycle"`
	Strategy   StrategyConfig   `json:"strategy"`
	MainSector MainSectorConfig `json:"main_sector"`
	LLM        LLMConfig        `json:"llm"`
	TradeTime  TradeTimeConfig  `json:"trade_time"`
	Theme      ThemeConfig      `json:"theme"`
	RiskCtrl   RiskCtrlConfig   `json:"risk_ctrl"`
	Position   PositionConfig   `json:"position"`
}

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

type LLMConfig struct {
	Model string `json:"model"`
}

type TradeTimeConfig struct {
	FullOpen      int `json:"full_open"`
	AfternoonStart int `json:"afternoon_start"`
	TradeClose    int `json:"trade_close"`
}

type ThemeConfig struct {
	WatchList []string `json:"watch_list"`
	BlackList []string `json:"black_list"`
}

type DrawdownRule struct {
	Pct    float64 `json:"pct"`
	Action string  `json:"action"`
}

type ComplianceConfig struct {
	ComplianceMode bool `json:"compliance_mode"`
}

type RiskCtrlConfig struct {
	StopLoss               StopLossConfig   `json:"stop_loss"`
	Compliance             ComplianceConfig `json:"compliance"`
	M8Enabled              bool             `json:"m8_enabled"`
	M8PortfolioDrawdownPct float64          `json:"m8_portfolio_drawdown_pct"`
	PerStockMax            float64          `json:"per_stock_max"`
}

type StopLossConfig struct {
	DrawdownAfterBuy []DrawdownRule `json:"drawdown_after_buy"`
}

type PositionConfig struct {
	MaxTotalPositionPct float64 `json:"max_total_position_pct"`
}

type StrategyConfig struct {
	Dragon       DragonConfig       `json:"dragon"`
	DoubleBump   DoubleBumpConfig   `json:"double_bump"`
	NShape       NShapeConfig       `json:"n_shape"`
	DragonReturn DragonReturnConfig `json:"dragon_return"`
}

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

type Manager struct {
	Rules *Rules
}

func NewManager(path string) *Manager {
	return &Manager{Rules: DefaultRules}
}

func (m *Manager) Get() *Rules { return m.Rules }

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
