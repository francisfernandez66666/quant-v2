// Package combat_agent 定义战法引擎的数据结构：扫描输入、信号输出等类型。
package combat_agent

import (
	"time"

	"quant-trading-v2/internal/sector_agent"
	"quant-trading-v2/internal/strategy_engine"
)

// ScanInput 战法扫描输入，包含已验证板块、行情数据、D1评分等信息。
type ScanInput struct {
	Sectors          []sector_agent.VerifiedSector            // 已验证的板块列表
	L1Score          map[string]float64                       // L1 过滤评分结果
	L1Blocked        map[string]bool                          // L1 过滤阻塞标记
	IndividualStocks []string                                 // 个股直接输入（跳过板块验证）
	MarketData       map[string]*strategy_engine.StockMarketData // 行情数据（Engine传过来）
	D1Scores         map[string]D1Score                       // D1评分结果
}

// Signal 战法引擎输出的信号，包含方向、操作、置信度等信息。
type Signal struct {
	ID          string    `json:"id"`                     // 信号唯一标识
	Code        string    `json:"code"`                   // 股票代码
	Name        string    `json:"name"`                   // 股票名称
	Strategy    string    `json:"strategy"`               // 策略名称
	Direction   string    `json:"direction"`              // 方向：做多/做空/提醒
	Action      string    `json:"action"`                 // 操作：买入/卖出/watch
	AlertType   string    `json:"alert_type,omitempty"`   // 提醒类型：止盈/止损
	Price       float64   `json:"price"`                  // 触发价格
	Confidence  float64   `json:"confidence"`             // 置信度（0~1）
	Reason      string    `json:"reason"`                 // 信号生成原因
	Sector      string    `json:"sector"`                 // 所属板块
	GeneratedAt time.Time `json:"generated_at"`           // 信号生成时间
}
