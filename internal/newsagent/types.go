// Package newsagent 新闻代理：负责新闻获取、Stage1 初筛、Stage2 LLM 分析和事件构建。
package newsagent

import "time"

// NewsEvent 结构化新闻事件，包含 LLM 分析的级别、方向、相关板块和个股等信息。
type NewsEvent struct {
	Title             string   `json:"title"`
	Content           string   `json:"content,omitempty"`
	Datetime          string   `json:"datetime"`
	Source            string   `json:"source"`
	IsMaterial        bool     `json:"is_material"`
	Level             string   `json:"level"`
	Direction         string   `json:"direction"`
	Score             float64  `json:"score"`
	Sectors           []string `json:"sectors,omitempty"`
	UpstreamSectors   []string `json:"upstream_sectors,omitempty"`
	DownstreamSectors []string `json:"downstream_sectors,omitempty"`
	RelatedStocks     []string `json:"related_stocks,omitempty"`
	CleanedStocks     []string `json:"cleaned_stocks,omitempty"`
	ImpactLevel       string   `json:"impact_level,omitempty"`
	EventType         string   `json:"event_type,omitempty"`
	Urgency           string   `json:"urgency,omitempty"`
	Reason            string   `json:"reason,omitempty"`
}

// AnalysisResult 新闻分析结果，包含事件列表和统计信息。
type AnalysisResult struct {
	Events        []NewsEvent `json:"events"`
	RawCount      int         `json:"raw_count"`
	MaterialCount int         `json:"material_count"`
	CatchUpSince  time.Time   `json:"catch_up_since"`
}

// DebugInfo LLM 调试信息，记录 Stage1/Stage2 处理过程和中间数据。
type DebugInfo struct {
	Stage1Mode     string      `json:"stage1_mode"`     // "llm" / "keyword"
	RawCount       int         `json:"raw_count"`       // total raw titles
	SelectedCount  int         `json:"selected_count"`  // titles after stage1
	RawTitles      []string    `json:"raw_titles"`      // all raw titles
	SelectedIdx    []int       `json:"selected_idx"`    // selected indices
	Stage2Events   []NewsEvent `json:"stage2_events"`   // analyzed events
	ProcessTime    time.Time   `json:"process_time"`    // when this debug was captured
}

// TrackerData 新闻追踪器数据，用于记录已处理的新闻标题和最后同步时间，避免重复处理。
type TrackerData struct {
	SeenTitles  map[string]string `json:"seen_titles"`   // md5(title) → datetime
	LastSync    map[string]string `json:"last_sync"`     // source → latest_datetime
}
