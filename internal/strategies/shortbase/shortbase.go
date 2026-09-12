// Package shortbase 定义做空战法（8b 反向信号）的共享输入数据结构。
//
// 四个做空战法（高位滞涨 high_churn / 放量破位 break_down / 龙头断板 leader_decay /
// 利好兑现砸盘 good_news_fade）消费同一份由 combat_agent 适配层从行情派生的 Data，
// 避免各战法重复计算均线/量能/连板等指标。字段全部为"派生后事实"，战法只做评分。
//
// 设计口径（见 docs/SHORT_STRATEGIES_PLAN_20260912.md）：
//   - 做空信号定位 = 卖出侧决策：持多个股 sell（开关开时走自动卖出通道）、非持仓 watch；
//   - 涨停口径板感知（data.LimitUpPct：ST4.9/双创19.9/北交29.9/主板9.9）；
//   - 日K 尾部已由引擎 attachLiveBar 合成当日实时 bar，盘中即可评分。
//
// （Package shortbase holds the shared scoring input for the four bear-side (short) tactics.
// All derived indicators are computed once by the combat_agent adapter; strategies only score.）
package shortbase

import "quant-trading-v2/internal/data"

// Data 做空战法共享评分输入（由适配层 buildShortData 派生，战法只读）。
// 各字段含义：
//   - 基础：Code/Name/Price/PrevClose/ChangePct 为个股标识与实时量价；
//   - 均线族：MA5/MA10/MA20；
//   - 位置：High60（近60日最高收盘）、Low20（近20日最低收盘）、Gain20（近20日累计涨幅）；
//   - 量能：Vol5/Vol20（均量）、VolRatio5_20（5日/20日均量比，放量>1.5 缩量<0.7）、TodayVolVs5d（当日量/5日均量）；
//   - 滞涨/走弱：Last3RangePct（近3日收盘区间涨跌幅%）、UpperShadowPct（末根上影占比0~1）、
//     BelowMA5（收盘跌破5日线）、ConsecDownDays（连续阴线天数）；
//   - 破位：BreakMA20/BreakLow20（收盘破20日线/破20日最低）、BreakDepthPct（破位深度%，正数）；
//   - 断板：LimitUpPct（板感知涨停幅%）、ConsecBoards（今日之前连板数）、TouchedBoardToday（今日盘中触板）、
//     SealedToday（今日收盘封板）、AfternoonReseal（分钟K 14:30 后回封）；
//   - 事件窗（利好兑现）：EventInWindow（近5交易日有该股利好事件）、EventAgeDays（事件距今交易日数）、
//     EventScore（事件 |score| 0~1）、EventPropagation（产业链传导类事件）、EventDayClose（事件日收盘价）；
//   - 情绪：EmotionPhase 当前市场情绪相位（启动/发酵/高潮/退潮…，可空）；
//   - 板块退潮：SectorLimitUpDropPct（所属板块涨停家数较昨日下降比例 0~1，可空=0）；
//   - 持仓：Held 当前账号是否持有该股（决定 sell/watch）；
//   - KLines：日K 引用（战法需要更细粒度时使用，勿修改）。
//
// （Data is the shared, adapter-derived scoring input consumed by all short tactics.）
type Data struct {
	Code      string  // 股票代码（Stock code）
	Name      string  // 股票名称（Stock name）
	Price     float64 // 现价（Latest price）
	PrevClose float64 // 昨收（Previous close）
	ChangePct float64 // 涨跌幅%（change percent, e.g. -3.2 = -3.2%）

	MA5  float64 // 5日均线（5-day MA）
	MA10 float64 // 10日均线（10-day MA）
	MA20 float64 // 20日均线（20-day MA）

	High60  float64 // 近60日最高收盘（60-day highest close）
	Low20   float64 // 近20日最低收盘（20-day lowest close）
	Gain20  float64 // 近20日累计涨幅（20-day cumulative gain ratio, e.g. 0.3 = +30%）
	PosHigh float64 // 现价相对60日最高位置（price/high60, 1.0 = 在最高点）

	Vol5         float64 // 5日均量（5-day avg volume）
	Vol20        float64 // 20日均量（20-day avg volume）
	VolRatio5_20 float64 // 5日/20日均量比（volume energy ratio）
	TodayVolVs5d float64 // 当日量/5日均量（today volume vs 5-day avg）

	Last3RangePct  float64 // 近3日收盘区间涨跌幅%（3-day close range change, %）
	UpperShadowPct float64 // 末根上影占比0~1（last bar upper-shadow ratio）
	BelowMA5       bool    // 收盘跌破5日线（close below MA5）
	ConsecDownDays int     // 连续阴线天数（consecutive down bars）

	BreakMA20     bool    // 收盘跌破20日线（close below MA20）
	BreakLow20    bool    // 收盘跌破近20日最低（close below 20-day low）
	BreakDepthPct float64 // 破位深度%（breakdown depth vs the broken level, %）

	LimitUpPct        float64 // 板感知涨停幅度%（board-aware limit-up pct）
	ConsecBoards      int     // 今日之前连板数（consecutive limit-ups before today）
	TouchedBoardToday bool    // 今日盘中触及涨停价（touched limit-up intraday）
	SealedToday       bool    // 今日收盘封住涨停（sealed at limit-up on close）
	AfternoonReseal   bool    // 分钟K显示14:30后回封（re-sealed after 14:30 per minute bars）

	EventInWindow    bool    // 近5交易日有该股利好事件（bullish event within 5 trading days）
	EventAgeDays     int     // 事件距今交易日数（event age in trading days）
	EventScore       float64 // 事件强度 |score|（event strength 0~1）
	EventPropagation bool    // 产业链传导类事件（propagated supply-chain event）
	EventDayClose    float64 // 事件日收盘价（close on the event day）

	EmotionPhase         string  // 市场情绪相位（market emotion phase, may be empty）
	SectorLimitUpDropPct float64 // 板块涨停家数较昨日下降比例（sector limit-up count drop ratio）

	Held   bool         // 当前账号是否持有（whether the account holds this stock）
	KLines []data.KLine // 日K 引用（daily bars, read-only）
}
