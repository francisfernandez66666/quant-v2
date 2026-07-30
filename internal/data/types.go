// Package data 提供行情数据获取、多数据源协调、情绪面分析、筹码分析、板块扫描等核心数据能力。
// 所有行情 API 调用均通过 net/http 直连，不引入第三方行情库。
package data

import "time"

// StockInfo 个股实时行情快照。
// 由多数据源（东方财富 push2、新浪、同花顺）统一填充，缺失字段留零值。
type StockInfo struct {
	Code      string  `json:"code"`       // 股票代码（如 "600519"）
	Name      string  `json:"name"`       // 股票名称
	Price     float64 `json:"price"`      // 当前价（元）
	Open      float64 `json:"open"`       // 今日开盘价（元）
	High      float64 `json:"high"`       // 当日最高价（元）
	Low       float64 `json:"low"`        // 当日最低价（元）
	Close     float64 `json:"close"`      // 最新价 / 昨收价（依数据源而定）
	Volume    float64 `json:"volume"`     // 成交量（股）
	Amount    float64 `json:"amount"`     // 成交额（元）
	ChangePct float64 `json:"change_pct"` // 涨跌幅（%，如 1.23 表示 +1.23%）
	Turnover  float64 `json:"turnover"`   // 换手率（%，如 5.67 表示 5.67%）
	NetInflow float64 `json:"net_inflow"` // 主力净流入（元），东方财富口径
	Sector    string  `json:"sector"`     // 所属板块名称
}

// KLine K 线数据。
// Date 为交易日，其余字段含义与 StockInfo 同名字段一致。
type KLine struct {
	Date   time.Time `json:"date"`   // 交易日
	Open   float64   `json:"open"`   // 开盘价（元）
	High   float64   `json:"high"`   // 最高价（元）
	Low    float64   `json:"low"`    // 最低价（元）
	Close  float64   `json:"close"`  // 收盘价（元）
	Volume float64   `json:"volume"` // 成交量（股）
	Amount float64   `json:"amount"` // 成交额（元）
}

// KLineClose 提取 K 线收盘价，用于策略指标计算（如 MA、EMA）。
func KLineClose(k KLine) float64 { return k.Close }

// KLineHigh 提取 K 线最高价。
func KLineHigh(k KLine) float64 { return k.High }

// KLineLow 提取 K 线最低价。
func KLineLow(k KLine) float64 { return k.Low }

// KLineOpen 提取 K 线开盘价。
func KLineOpen(k KLine) float64 { return k.Open }

// KLineVolume 提取 K 线成交量。
func KLineVolume(k KLine) float64 { return k.Volume }

// SectorInfo 板块行情快照。
// 来源于东方财富行业板块行情列表，包含涨跌幅、涨停家数、资金流向等。
type SectorInfo struct {
	Code       string  `json:"code"`                 // 板块代码（BKXXXX）
	Name       string  `json:"name"`                 // 板块名称（如 "半导体"）
	ChangePct  float64 `json:"change_pct"`           // 板块涨跌幅（%）
	LimitupCnt int     `json:"limitup_cnt"`          // 板块内涨停家数
	VolumeRank int     `json:"volume_rank"`          // 成交量排名
	Amount     float64 `json:"amount"`               // 板块总成交额（元）
	Gain2d     float64 `json:"gain_2d"`              // 两日涨幅（%）
	EventDesc  string  `json:"event_desc,omitempty"` // 事件描述（D1 匹配结果，空时省略）
	NetInflow  float64 `json:"net_inflow,omitempty"` // 主力净流入（东财口径，元）
}

// EmotionData 市场情绪综合数据。
// 用于六阶段情绪判断（冰点/启动/发酵/高潮/背离/退潮）。
type EmotionData struct {
	Stage       string  `json:"stage"`        // 情绪阶段名称："冰点"/"启动"/"发酵"/"高潮"/"背离"/"退潮"
	LimitupCnt  int     `json:"limitup_cnt"`  // 涨停家数（含新股）
	BoardHeight int     `json:"board_height"` // 连板高度（最高连板数）
	BlastRate   float64 `json:"blast_rate"`   // 炸板率（%），炸板数/(涨停+炸板)
	UpCount     int     `json:"up_count"`     // 上涨家数
	DownCount   int     `json:"down_count"`   // 下跌家数
	IndexPrice  float64 `json:"index_price"`  // 上证指数当前价
	IndexMA20   float64 `json:"index_ma20"`   // 上证指数 20 日均线
}

// CapitalFlow 资金流向数据。
// 按超大单/大单/中单/小单分维度统计，来源于东方财富。
type CapitalFlow struct {
	Code          string    `json:"code"`            // 股票代码
	NetInflow     float64   `json:"net_inflow"`      // 主力净流入（元），超大单+大单净流入
	SuperLargeIn  float64   `json:"super_large_in"`  // 超大单流入（元），>=500 万元
	SuperLargeOut float64   `json:"super_large_out"` // 超大单流出（元）
	LargeIn       float64   `json:"large_in"`        // 大单流入（元），>=100 万元且 <500 万元
	LargeOut      float64   `json:"large_out"`       // 大单流出（元）
	MediumIn      float64   `json:"medium_in"`       // 中单流入（元），>=20 万元且 <100 万元
	MediumOut     float64   `json:"medium_out"`      // 中单流出（元）
	SmallIn       float64   `json:"small_in"`        // 小单流入（元），<20 万元
	SmallOut      float64   `json:"small_out"`       // 小单流出（元）
	Time          time.Time `json:"time"`            // 数据获取时间
}

// NewsItem 财经快讯 / 新闻条目。
// 可来自东方财富快讯、新浪财经、Tushare 新闻等源。
type NewsItem struct {
	Title          string   `json:"title"`                  // 新闻标题
	Content        string   `json:"content,omitempty"`      // 正文摘要（可能为空）
	Datetime       string   `json:"datetime"`               // 发布时间字符串
	Source         string   `json:"source"`                 // 来源标识（如 "东方财富""新浪财经""上市公司公告"）
	SentimentScore float64  `json:"sentiment_score"`        // LLM 情感得分(0~1, 0.5=中性), 0表示未分析
	Sentiment      string   `json:"sentiment,omitempty"`    // LLM 情感倾向 正面/负面/中性
	ImpactLevel    string   `json:"impact_level,omitempty"` // 影响程度 高/中/低
	EventType      string   `json:"event_type,omitempty"`   // 事件类型 政策/财报/行业/公司/宏观/事件驱动
	Urgency        string   `json:"urgency,omitempty"`      // 紧急程度 立即/关注/观察
	Direction      string   `json:"direction,omitempty"`    // 方向 利好/利空/中性
	Sectors        []string `json:"sectors,omitempty"`      // 关联板块
	Stocks         []string `json:"stocks,omitempty"`       // 关联个股
	Strategy       string   `json:"strategy,omitempty"`     // 匹配策略
	Reason         string   `json:"reason,omitempty"`       // 分析理由
}

// IPOEvent 新股日历事件。
// 来源：Tushare new_share API（首选）、东方财富数据中心（降级）。
type IPOEvent struct {
	Code        string  `json:"code"`         // 股票代码（6位）
	Name        string  `json:"name"`         // 股票名称
	IPODate     string  `json:"ipo_date"`     // 申购日期 YYYYMMDD
	ListingDate string  `json:"listing_date"` // 上市日期 YYYYMMDD
	IssuePrice  float64 `json:"issue_price"`  // 发行价（元）
	ListStatus  string  `json:"list_status"`  // L=已上市 U=未上市
	Sector      string  `json:"sector"`       // 所属板块名称（IPO时可能空缺）
}

// ThsSectorInfo 同花顺板块指数信息（来自 Tushare ths_index）。
type ThsSectorInfo struct {
	Code  string // THS 板块代码（如 "TS886001"）
	Name  string // 板块名称
	Count int    // 成分股数量
}

// ThsMemberInfo 同花顺板块成分股信息（来自 Tushare ths_member）。
type ThsMemberInfo struct {
	ConCode string // 成分股代码
	Name    string // 成分股名称
}

// FinancialInfo 个股基本面指标。
// 用于风控筛选和估值判断，仅包含可量化的关键财务字段。
type FinancialInfo struct {
	Code            string  `json:"code"`                   // 股票代码
	IsST            bool    `json:"is_st"`                  // 是否 ST/*ST 股票
	HasPenalty12m   bool    `json:"has_penalty_12m"`        // 近 12 个月是否有违规处罚
	GoodwillRatio   float64 `json:"goodwill_ratio"`         // 商誉占净资产比例
	PledgeRatio     float64 `json:"pledge_ratio"`           // 股权质押比例
	ConsecutiveLoss int     `json:"consecutive_loss_years"` // 连续亏损年数
	UnlockRatio30d  float64 `json:"unlock_ratio_30d"`       // 未来 30 天解禁比例
	PE              float64 `json:"pe"`                     // 市盈率
	PB              float64 `json:"pb"`                     // 市净率
	ROE             float64 `json:"roe"`                    // 净资产收益率（%）
	MarketCap       float64 `json:"market_cap"`             // 总市值（元）
}
