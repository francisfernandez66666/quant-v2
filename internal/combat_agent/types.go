// Package combat_agent 定义战法引擎的数据结构：扫描输入、信号输出等类型。
// ScanInput 是三大扫描路径（ScanLong/ScanShort/Scan）与持续打分（ScorePool）的统一入参，
// Signal 是引擎对外输出的统一信号模型，StockScores 承载 8a/8b 持续打分结果。
package combat_agent

import (
	"time"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/sector_agent"
	"quant-trading-v2/internal/strategy_engine"
)

// NewsBrief 个股关联新闻简报（供预期差检测使用）。
type NewsBrief struct {
	Title    string // 新闻标题
	Positive bool   // 事件方向：true=利好 false=利空
	Time     string // 新闻时间（YYYY-MM-DD HH:MM:SS）
}

// ScanInput 战法扫描输入，包含已验证板块、行情数据、D1评分等信息。
// 由上层 Engine 组装传入；其中 Scores 由 Engine 初始化、扫描过程中被写入，
// 供 8a/8b 前端展示持续评分。
type ScanInput struct {
	Sectors          []sector_agent.VerifiedSector               // 已验证的板块列表（含方向利好/利空）
	L1Score          map[string]float64                          // L1 过滤评分结果
	L1Blocked        map[string]bool                             // L1 过滤阻塞标记（true=该股被拦截）
	IndividualStocks []string                                    // 个股直接输入（跳过板块验证）
	MarketData       map[string]*strategy_engine.StockMarketData // 行情数据（Engine传过来）
	D1Scores         map[string]D1Score                          // D1评分结果
	LimitUpPool      []data.LimitUpStock                         // 当日涨停池（龙头识别/涨停分类）
	News             map[string][]NewsBrief                      // code → 关联新闻简报（预期差）
	Scores           map[string]StockScores                      // 8a/8b 打分输出（engine 初始化，扫描写入）
	EmotionPhase     string                                      // 情绪阶段（供 N 形评分）
}

// StockScores 单只股票的四战法原始分 + 动量分（8a/8b 持续打分输出）。
// 无论战法是否通过都记录原始分，前端据此展示自选/持仓的持续评分。
// JSON 标签供前端按稳定 key 取值展示。
type StockScores struct {
	Code              string          `json:"code"`          // 股票代码
	NScore            float64         `json:"n_score"`       // N 形战法分（0~100）
	DragonScore       float64         `json:"dragon_score"`  // 龙头战法分（0~100）
	DoubleBumpScore   float64         `json:"db_score"`      // 双响炮战法分（0~100）
	DragonReturnScore float64         `json:"dr_score"`      // 龙回头战法分（0~100）
	MomentumScore     float64         `json:"m_score"`       // 动量分（量价+MACD+走势，0~100）
	MomentumValid     bool            `json:"m_valid"`       // 动量分数据是否完整（量价/走势/MACD 任一缺失为 false）
	SignalActive      bool            `json:"signal_active"` // 本轮是否有该股信号
	DataGaps          map[string]bool `json:"data_gaps"`     // 数据缺口标记：key=战法类型，true=该战法输入数据不足（0 分不代表真实 0）
	UpdatedAt         time.Time       `json:"updated_at"`    // 打分时间
}

// Signal 战法引擎输出的信号，包含方向、操作、置信度等信息。
// 三大来源：战法评分（ScanLong/ScanShort/Scan/ScorePool）、持仓止盈止损提醒、
// 涨停池增强（龙头识别/预期差）。上层据 Direction/Action 分发处理。
type Signal struct {
	ID          string    `json:"id"`                   // 信号唯一标识
	Code        string    `json:"code"`                 // 股票代码
	Name        string    `json:"name"`                 // 股票名称
	Strategy    string    `json:"strategy"`             // 策略名称
	Direction   string    `json:"direction"`            // 方向：做多/做空/提醒
	Action      string    `json:"action"`               // 操作：买入/卖出/watch
	AlertType   string    `json:"alert_type,omitempty"` // 提醒类型：止盈/止损
	Price       float64   `json:"price"`                // 触发价格
	Confidence  float64   `json:"confidence"`           // 置信度（0~1）
	Reason      string    `json:"reason"`               // 信号生成原因
	Sector      string    `json:"sector"`               // 所属板块
	GeneratedAt time.Time `json:"generated_at"`         // 信号生成时间
}

// SignalLog 单轮策略信号批次快照，记录该轮产出的全部信号与产出时间。
// 供前端"信号日志"弹窗按批次（时间分组）展示，用于复盘信号历史。
type SignalLog struct {
	ProcessTime time.Time `json:"process_time"` // 信号批次产出时间
	RawCount    int       `json:"raw_count"`    // 本轮原始新闻条数
	Signals     []Signal  `json:"signals"`      // 本轮全部信号（做多/做空/提醒）
}
