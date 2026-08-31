// Package strategy_engine 定义策略引擎相关数据结构：板块热度、个股行情、策略结果等。
// 本包是策略引擎的核心类型定义，包含：
//   - SectorHot: 热点板块信息，包含事件驱动的涨跌幅、涨停家数、资金流向等
//   - IndividualStock: 个股事件信息，包含方向（利好/利空）
//   - StockMarketData: 个股行情数据，包含实时价、K线、资金流向、分钟级量价/MACD等
//   - FinancialData: 个股最新财务指标（实盘因子战法/财务因子评分用）
//   - StrategyResult: 策略引擎评估结果，包含板块、个股、行情数据和 L1 过滤信息
//
// （Package strategy_engine defines strategy-engine data structures: sector heat, stock quotes, strategy results.）
package strategy_engine

import (
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/newsagent"
)

// SectorHot 热点板块信息，包含事件驱动的涨跌幅、涨停家数、资金流向等。
// 由 Evaluate 方法中的 attribution 函数从新闻事件归因得到，供板块验证和信号聚合使用。
// （SectorHot is hot-sector info: event-driven change, limit-up count, capital flow, etc.）
type SectorHot struct {
	Name       string   `json:"name"`                  // 板块名称（Sector name）
	Direction  string   `json:"direction"`             // 方向：利好/利空（Direction: bullish/bearish）
	Score      float64  `json:"score"`                 // 事件评分（Event score）
	ChangePct  float64  `json:"change_pct"`            // 板块涨跌幅（Sector change %）
	LimitupCnt int      `json:"limitup_cnt"`           // 涨停家数（Limit-up count）
	NetInflow  float64  `json:"net_inflow"`            // 主力净流入（Main-force net inflow）
	Reason     string   `json:"reason"`                // 上榜原因（Listing reason）
	LeadStocks []string `json:"lead_stocks,omitempty"` // 领涨/领跌股（Leading/lagging stocks）
	NewsTitles []string `json:"news_titles,omitempty"` // 关联新闻标题（Related news titles）
}

// IndividualStock 个股事件信息，包含方向（利好/利空）。
// 由 Evaluate 方法中的事件分流产生，用于标记事件驱动的个股交易方向。
// （IndividualStock is per-stock event info with direction.）
type IndividualStock struct {
	Code      string // 股票代码（Stock code）
	Name      string // 股票名称（Stock name）
	Direction string // 方向：利好/利空（Direction: bullish/bearish）
}

// StockMarketData 个股行情数据：实时价、K线、资金流向、分钟级量价/MACD等。
// 由 Evaluate / BuildScoringData 填充，供 8a/8b 打分、战法评分与信号扫描消费。
// （StockMarketData holds one stock's market data: live price, bars, money flow, minute volume/MACD. Filled by
// Evaluate / BuildScoringData and consumed by 8a/8b scoring, strategy scoring and signal scanning.）

// FinancialData 个股最新财务指标（实盘因子战法/财务因子评分用）。
// 由研究库 fina_indicator 最新报告期填充（点对时：ann_date ≤ 当日 的最新值），缺失为 0。
// 这些字段用于因子战法中的财务类因子评分（ROE质量、净利同比成长、估值等）。
// （English: a stock's latest financial indicators (for live factor-strategy financial scoring), filled
// from the research DB's latest fina_indicator report (point-in-time: latest ann_date ≤ today); 0 when missing.）
type FinancialData struct {
	Roe          float64 `json:"roe,omitempty"`            // 净资产收益率（%）
	YoyNetProfit float64 `json:"yoy_net_profit,omitempty"` // 净利同比（%）
	NetMargin    float64 `json:"net_margin,omitempty"`     // 净利率（%）
	GrossMargin  float64 `json:"gross_margin,omitempty"`   // 毛利率（%）
	DebtToAssets float64 `json:"debt_to_assets,omitempty"` // 资产负债率（%）
	Eps          float64 `json:"eps,omitempty"`            // 每股收益
	YoyOR        float64 `json:"yoy_or,omitempty"`         // 营收同比（%）
}

// StockMarketData 个股行情数据：实时价、K线、资金流向、分钟级量价/MACD等。
// 由 Evaluate / BuildScoringData 填充，供 8a/8b 打分、战法评分与信号扫描消费。
// （StockMarketData holds one stock's market data: live price, bars, money flow, minute volume/MACD. Filled by
// Evaluate / BuildScoringData and consumed by 8a/8b scoring, strategy scoring and signal scanning.）
type StockMarketData struct {
	Code        string            `json:"code"`                   // 股票代码（Stock code）
	Name        string            `json:"name"`                   // 股票名称（Stock name）
	Price       float64           `json:"price"`                  // 最新价（Latest price）
	ChangePct   float64           `json:"change_pct"`             // 涨跌幅（Change %）
	KLines      []data.KLine      `json:"k_lines,omitempty"`      // 日K线数据（近120根，趋势/均线类战法使用）（Daily bars, ~120, for trend/MA strategies）
	MoneyFlow   *data.CapitalFlow `json:"money_flow,omitempty"`   // 资金流向（主力净流入）（Capital flow, main-force net inflow）
	Quote       *data.StockInfo   `json:"quote,omitempty"`        // 实时量价快照（新浪批量/同花顺兜底）（Live quote snapshot, Sina batch / THS fallback）
	MinuteKLine []data.KLine      `json:"minute_kline,omitempty"` // 分钟K线（5分钟，用于 MACD/动量）（Minute bars, 5-min, for MACD/momentum）
	MinuteMACD  data.MACD         `json:"minute_macd,omitempty"`  // 分钟级 MACD（DIF/DEA/Bar）（Minute MACD: DIF/DEA/Bar）
	BenchChg    float64           `json:"bench_chg,omitempty"`    // 基准指数（上证）当前涨跌幅（%，供 N 形 D2 相对强度对比）（Benchmark (SSE) change %, for N-shape D2 relative strength）
	Error       string            `json:"error,omitempty"`        // 行情获取错误信息（非空表示该股行情缺失）（Quote error; non-empty means missing quotes）
	Fina        *FinancialData    `json:"fina,omitempty"`         // 最新财务指标（财务因子评分用）（Latest financials for financial-factor scoring）
}

// StrategyResult 策略引擎评估结果，包含板块、个股、行情数据和 L1 过滤信息。
// 由 Evaluate 返回，供顶层引擎做板块验证、战法扫描与信号聚合。
// 这是策略引擎的核心输出结构，包含了整个评估流程的所有关键数据。
// （StrategyResult is the strategy-engine evaluation output — sectors, stocks, market data and L1 filtering — returned
// by Evaluate for the top-level engine's sector verification, strategy scanning and signal aggregation.）
type StrategyResult struct {
	HotSectors  []SectorHot                 `json:"hot_sectors"`            // 利好板块列表（Bullish sector list）
	BearSectors []SectorHot                 `json:"bear_sectors,omitempty"` // 利空板块列表（Bearish sector list）
	BearStocks  []string                    `json:"bear_stocks,omitempty"`  // 利空个股列表（Bearish stock list）
	LongStocks  []IndividualStock           `json:"-"`                      // 做多个股（内部使用）（Long stocks, internal）
	ShortStocks []IndividualStock           `json:"-"`                      // 做空个股（内部使用）（Short stocks, internal）
	ScoringPool []string                    `json:"-"`                      // 收拢打分池：Stage2 + 持仓 + 自选（Scoring pool: Stage2 + holdings + watchlist）
	MarketData  map[string]*StockMarketData `json:"market_data,omitempty"`  // code → 行情数据（code → market data）
	L1Score     map[string]float64          `json:"l1_score,omitempty"`     // L1 过滤评分（L1 filter scores）
	L1Blocked   map[string]bool             `json:"l1_blocked,omitempty"`   // L1 过滤阻断（L1 filter blocks）
	Events      []newsagent.NewsEvent       `json:"events,omitempty"`       // 原始新闻事件（Raw news events）
}
