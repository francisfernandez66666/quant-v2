// Package newsagent 新闻代理：负责新闻获取、Stage0 归因、Stage1 初筛、Stage2 LLM 分析和事件构建。
package newsagent

import "time"

// NewsEvent 结构化新闻事件，包含 LLM 分析的级别、方向、相关板块和个股等信息。
// Score 带符号：正值利好（+0.5 中 / +0.75 强），负值利空（-0.5 中 / -0.75 强），±0.25 弱/中性。
// Direction 由 Score 符号推导，仅用于展示。
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

// Stage0Result Stage0 分类结果：按标题归因将新闻分为个股/板块/一般三类。
// 个股新闻：标题包含已知股票名；板块新闻：标题含行业/宏观关键词；一般新闻：其余。
type Stage0Result struct {
	StockIdx   []int // 个股新闻索引（标题含股票名）
	SectorIdx  []int // 板块/宏观新闻索引（含行业关键词）
	IpoIdx     []int // IPO 新闻索引（新股/申购/上市，直构事件不走 LLM）
	GeneralIdx []int // 一般新闻索引（仅展示，不进引擎）

	// Material 板块新闻是否通过价值初筛（Stage1 职责合并进 Stage0 单次调用）。
	// 键为 rawNews 索引。
	Material map[int]bool
	// CorrectedTitle 标题党校正：LLM 判定标题与正文不符时给出的校正标题。
	// 键为 rawNews 索引。
	CorrectedTitle map[int]string
}

// DebugInfo LLM 调试信息，记录 Stage1/Stage2 处理过程和中间数据。
type DebugInfo struct {
	Stage1Mode    string      `json:"stage1_mode"`    // "llm" / "keyword"
	RawCount      int         `json:"raw_count"`      // total raw titles
	SelectedCount int         `json:"selected_count"` // titles after stage1
	RawTitles     []string    `json:"raw_titles"`     // all raw titles
	SelectedIdx   []int       `json:"selected_idx"`   // selected indices
	Stage2Events  []NewsEvent `json:"stage2_events"`  // analyzed events
	ProcessTime   time.Time   `json:"process_time"`   // when this debug was captured
}

// TrackerData 新闻追踪器数据，用于记录已处理的新闻标题和最后同步时间，避免重复处理。
type TrackerData struct {
	SeenTitles map[string]string `json:"seen_titles"` // md5(title) → datetime
	LastSync   map[string]string `json:"last_sync"`   // source → latest_datetime
}
