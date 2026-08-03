// Package data — 热门板块记录固化。
// 定义热点板块快照与轮次记录的结构，供跨交易日持久化使用。
package data

import "time"

// HotSectorRecord 热门板块单条快照（同花顺板块行情表匹配后的展示数据）。
type HotSectorRecord struct {
	Name       string   `json:"name"`        // 板块名称
	Code       string   `json:"code"`        // 板块代码
	Score      float64  `json:"score"`       // 综合评分
	ChangePct  float64  `json:"change_pct"`  // 板块涨跌幅（%）
	D1         float64  `json:"d1"`          // D1 事件评分
	Direction  string   `json:"direction"`   // 方向（领涨/领跌等）
	LimitupCnt int      `json:"limitup_cnt"` // 板块内涨停家数
	NetInflow  float64  `json:"net_inflow"`  // 主力净流入（元）
	Reason     string   `json:"reason"`      // 上榜原因描述
	NewsTitles []string `json:"news_titles"` // 关联新闻标题列表
}

// HotRecord 一轮热点板块记录（与 Stage 记录同一固化节奏：跨交易日清除）。
type HotRecord struct {
	ProcessTime time.Time         `json:"process_time"` // 记录生成时间
	Sectors     []HotSectorRecord `json:"sectors"`      // 本轮热点板块快照列表
}
