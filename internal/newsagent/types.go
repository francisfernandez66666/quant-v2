// Package newsagent 新闻代理：负责新闻获取、Stage0 归因、Stage1 初筛、Stage2 LLM 分析和事件构建。
package newsagent

import "time"

// NewsEvent 结构化新闻事件，包含 LLM 分析的级别、方向、相关板块和个股等信息。
// Score 带符号：正值利好（+0.5 中 / +0.75 强），负值利空（-0.5 中 / -0.75 强），±0.25 弱/中性。
// Direction 由 Score 符号推导，仅用于展示。
type NewsEvent struct {
	Title             string   `json:"title"`                        // 新闻标题
	Content           string   `json:"content,omitempty"`            // 新闻正文/摘要
	Datetime          string   `json:"datetime"`                     // 事件时间 YYYY-MM-DD HH:MM:SS
	Source            string   `json:"source"`                       // 新闻来源（如 IPO日历/财联社）
	IsMaterial        bool     `json:"is_material"`                  // 是否通过价值初筛
	Level             string   `json:"level"`                        // 影响级别：个股/板块/宏观
	Direction         string   `json:"direction"`                    // 方向：利好/利空/中性（由 Score 推导）
	Score             float64  `json:"score"`                        // 带符号强度分，驱动引擎阈值过滤
	Sectors           []string `json:"sectors,omitempty"`            // 相关板块
	UpstreamSectors   []string `json:"upstream_sectors,omitempty"`   // 上游产业链板块
	DownstreamSectors []string `json:"downstream_sectors,omitempty"` // 下游产业链板块
	RelatedStocks     []string `json:"related_stocks,omitempty"`     // 关联个股（原始名称/代码）
	CleanedStocks     []string `json:"cleaned_stocks,omitempty"`     // 清洗后的个股 "名称|代码"
	ImpactLevel       string   `json:"impact_level,omitempty"`       // 冲击程度：高/中/低
	EventType         string   `json:"event_type,omitempty"`         // 事件类型：公司/行业/宏观...
	Urgency           string   `json:"urgency,omitempty"`            // 紧急程度：紧急/关注/一般
	Reason            string   `json:"reason,omitempty"`             // LLM 归因说明
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
	Stage1Mode    string      `json:"stage1_mode"`    // "llm" / "keyword"：Stage1 使用的初筛方式
	RawCount      int         `json:"raw_count"`      // total raw titles：原始标题总数
	SelectedCount int         `json:"selected_count"` // titles after stage1：Stage1 初筛后的条数
	RawTitles     []string    `json:"raw_titles"`     // all raw titles：全部原始标题
	SelectedIdx   []int       `json:"selected_idx"`   // selected indices：被选中的标题索引
	Stage2Events  []NewsEvent `json:"stage2_events"`  // analyzed events：Stage2 分析产出的事件
	ProcessTime   time.Time   `json:"process_time"`   // when this debug was captured：调试快照的时间
}

// TrackerData 新闻追踪器数据，存储已处理的标题与各来源同步时间，避免重复处理。
type TrackerData struct {
	SeenTitles map[string]string `json:"seen_titles"` // md5(title) → datetime：已处理标题及其时间
	LastSync   map[string]string `json:"last_sync"`   // source → latest_datetime：各来源最近同步时间
}
