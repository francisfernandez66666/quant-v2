// Package strategy_engine 定义策略引擎相关数据结构：板块热度、个股行情、策略结果等。
package strategy_engine

import (
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/newsagent"
)

// SectorHot 热点板块信息，包含事件驱动的涨跌幅、涨停家数、资金流向等。
type SectorHot struct {
	Name       string   `json:"name"`                  // 板块名称
	Direction  string   `json:"direction"`             // 方向：利好/利空
	Score      float64  `json:"score"`                 // 事件评分
	ChangePct  float64  `json:"change_pct"`            // 板块涨跌幅
	LimitupCnt int      `json:"limitup_cnt"`           // 涨停家数
	NetInflow  float64  `json:"net_inflow"`            // 主力净流入
	Reason     string   `json:"reason"`                // 上榜原因
	LeadStocks []string `json:"lead_stocks,omitempty"` // 领涨/领跌股
	NewsTitles []string `json:"news_titles,omitempty"` // 关联新闻标题
}

// IndividualStock 个股事件信息，包含方向（利好/利空）。
type IndividualStock struct {
	Code      string // 股票代码
	Name      string // 股票名称
	Direction string // 方向：利好/利空
}

// StockMarketData 个股行情数据：实时价、K线、资金流向、分钟级量价/MACD等。
// 由 Evaluate / BuildScoringData 填充，供 8a/8b 打分、战法评分与信号扫描消费。
type StockMarketData struct {
	Code        string            `json:"code"`                   // 股票代码
	Name        string            `json:"name"`                   // 股票名称
	Price       float64           `json:"price"`                  // 最新价
	ChangePct   float64           `json:"change_pct"`             // 涨跌幅
	KLines      []data.KLine      `json:"k_lines,omitempty"`      // 日K线数据（近120根，趋势/均线类战法使用）
	MoneyFlow   *data.CapitalFlow `json:"money_flow,omitempty"`   // 资金流向（主力净流入）
	Quote       *data.StockInfo   `json:"quote,omitempty"`        // 实时量价快照（新浪批量/同花顺兜底）
	MinuteKLine []data.KLine      `json:"minute_kline,omitempty"` // 分钟K线（5分钟，用于 MACD/动量）
	MinuteMACD  data.MACD         `json:"minute_macd,omitempty"`  // 分钟级 MACD（DIF/DEA/Bar）
	BenchChg    float64           `json:"bench_chg,omitempty"`    // 基准指数（上证）当前涨跌幅（%，供 N 形 D2 相对强度对比）
	Error       string            `json:"error,omitempty"`        // 行情获取错误信息（非空表示该股行情缺失）
}

// StrategyResult 策略引擎评估结果，包含板块、个股、行情数据和 L1 过滤信息。
// 由 Evaluate 返回，供顶层引擎做板块验证、战法扫描与信号聚合。
type StrategyResult struct {
	HotSectors  []SectorHot                 `json:"hot_sectors"`            // 利好板块列表
	BearSectors []SectorHot                 `json:"bear_sectors,omitempty"` // 利空板块列表
	BearStocks  []string                    `json:"bear_stocks,omitempty"`  // 利空个股列表
	LongStocks  []IndividualStock           `json:"-"`                      // 做多个股（内部使用）
	ShortStocks []IndividualStock           `json:"-"`                      // 做空个股（内部使用）
	ScoringPool []string                    `json:"-"`                      // 收拢打分池：Stage2 + 持仓 + 自选
	MarketData  map[string]*StockMarketData `json:"market_data,omitempty"`  // code → 行情数据
	L1Score     map[string]float64          `json:"l1_score,omitempty"`     // L1 过滤评分
	L1Blocked   map[string]bool             `json:"l1_blocked,omitempty"`   // L1 过滤阻断
	Events      []newsagent.NewsEvent       `json:"events,omitempty"`       // 原始新闻事件
}
