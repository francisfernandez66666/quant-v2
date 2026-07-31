package data

import "time"

// HotSectorRecord 热门板块单条快照（同花顺板块行情表匹配后的展示数据）。
type HotSectorRecord struct {
	Name       string   `json:"name"`
	Code       string   `json:"code"`
	Score      float64  `json:"score"`
	ChangePct  float64  `json:"change_pct"`
	D1         float64  `json:"d1"`
	Direction  string   `json:"direction"`
	LimitupCnt int      `json:"limitup_cnt"`
	NetInflow  float64  `json:"net_inflow"`
	Reason     string   `json:"reason"`
	NewsTitles []string `json:"news_titles"`
}

// HotRecord 一轮热点板块记录（与 Stage 记录同一固化节奏：跨交易日清除）。
type HotRecord struct {
	ProcessTime time.Time         `json:"process_time"`
	Sectors     []HotSectorRecord `json:"sectors"`
}
