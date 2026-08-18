// Package newsagent 新闻代理：负责新闻获取、Stage0 归因、Stage1 初筛、Stage2 LLM 分析和事件构建。
// （Package newsagent is the news agent handling fetching, Stage0 attribution, Stage1 screening, Stage2 LLM analysis and event building.）
package newsagent

import (
	"time"

	"quant-trading-v2/internal/data"
)

// NewsEvent 结构化新闻事件，包含 LLM 分析的级别、方向、相关板块和个股等信息。
// Score 带符号：正值利好（+0.5 中 / +0.75 强），负值利空（-0.5 中 / -0.75 强），±0.25 弱/中性。
// Direction 由 Score 符号推导，仅用于展示。
// （NewsEvent is a structured news event holding LLM-analyzed level, direction, related sectors and stocks.
// Score is signed: positive bullish, negative bearish; Direction is derived from the sign and is display-only.）
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
	Region            string   `json:"region,omitempty"`             // 事件来源地域：国内/海外（LLM 判定）
	Relation          string   `json:"relation,omitempty"`           // 海外事件与A股板块关系：对抗制裁/合作/不涉及
}

// Stage0Result Stage0 分类结果：按标题归因将新闻分为个股/板块/一般三类。
// 个股新闻：标题包含已知股票名；板块新闻：标题含行业/宏观关键词；一般新闻：其余。
// （Stage0Result is the Stage0 classification result splitting news into stock/sector/general via title attribution.）
type Stage0Result struct {
	StockIdx   []int // 个股新闻索引（标题含股票名）
	SectorIdx  []int // 板块/宏观新闻索引（含行业关键词）
	IpoIdx     []int // IPO 新闻索引（新股/申购/上市，直构事件不走 LLM）
	GeneralIdx []int // 一般新闻索引（仅展示，不进引擎）

	// Material 板块新闻是否通过价值初筛（Stage1 职责合并进 Stage0 单次调用）。
	// 键为 rawNews 索引。
	// （Material marks whether sector news passed the value screen; keys are rawNews indices.）
	Material map[int]bool
	// CorrectedTitle 标题党校正：LLM 判定标题与正文不符时给出的校正标题。
	// 键为 rawNews 索引。
	// （CorrectedTitle is the clickbait-corrected title given by LLM when title diverges from body; keys are rawNews indices.）
	CorrectedTitle map[int]string
	// FailedIdx Stage0 判定失败的批内新闻索引（该批 LLM 重试耗尽被跳过，未完成判定）。
	// 这些新闻应留在未归因队列由下一轮重试，而非被误归为一般新闻。
	// （FailedIdx holds rawNews indices whose Stage0 batch was skipped after retry exhaustion; these stay
	// in the unattributed queue for the next round rather than being misclassified as general news.）
	FailedIdx []int
	// Err Stage0 失败的底层原因（如 LLM 连不通导致整批留队重试）。成功或无异常时为 nil。
	// （Err is the underlying failure cause of Stage0, nil on success. When set, the whole batch stays in
	// the retry queue (FailedIdx) rather than being misclassified as general news.）
	Err error
}

// DebugInfo LLM 调试信息，记录 Stage0(合并)/Stage2 处理过程和中间数据。
// （DebugInfo is LLM debug info recording the merged-Stage0/Stage2 processing and intermediate data.）
type DebugInfo struct {
	Stage1Mode    string      `json:"stage1_mode"`    // 初筛方式（合并 Stage0 后固定 "combined"）
	RawCount      int         `json:"raw_count"`      // total raw titles：原始标题总数
	SelectedCount int         `json:"selected_count"` // titles after stage1：初筛后的条数
	RawTitles     []string    `json:"raw_titles"`     // all raw titles：全部原始标题
	SelectedIdx   []int       `json:"selected_idx"`   // selected indices：被选中的标题索引
	Stage2Events  []NewsEvent `json:"stage2_events"`  // analyzed events：Stage2 分析产出的事件
	ProcessTime   time.Time   `json:"process_time"`   // when this debug was captured：调试快照的时间
}

// TrackerData 新闻追踪器数据，存储已处理的标题与各来源同步时间，避免重复处理。
// （TrackerData stores already-seen titles and per-source sync times to avoid duplicate processing.）
type TrackerData struct {
	SeenTitles map[string]string `json:"seen_titles"` // md5(title) → datetime：已处理标题及其时间
	LastSync   map[string]string `json:"last_sync"`   // source → latest_datetime：各来源最近同步时间
	// PendingItems 未归因队列：已抓取但 Stage0/Stage2 尚未成功归因的新闻（完整内容），
	// 供下一轮盘前/盘中重试。成功归因后移入 SeenTitles。
	// （PendingItems is the unattributed queue: fetched news that has not yet been successfully attributed
	// by Stage0/Stage2 (full content kept), retried in later rounds; removed once attributed.）
	PendingItems []data.NewsItem `json:"pending_items,omitempty"`
}

// FrozenEvent 固化事件：保留 Stage2 分析出的利好/利空价值及其关联个股，跨盘前刷新持续存在。
// 自产生交易日保留，顺延一个交易日；期间若同板块+同方向出现新事件则整体覆盖（分数取最新），
// 否则在下一交易日保存时到期清除。
// （FrozenEvent is a frozen event preserving the Stage2 bullish/bearish value and related stocks across
// pre-session refreshes. It lives for its trading day plus one; new same-sector/same-direction events overwrite it.）
type FrozenEvent struct {
	NewsEvent        // 内嵌原始事件（Title/Score/Direction/RelatedStocks/CleanedStocks/Sectors 等）
	Day       string `json:"day"` // 固化产生交易日（YYYY-MM-DD），用于跨日到期判断
	Key       string `json:"key"` // 覆盖键：sector|direction（同板块+同方向新事件据此覆盖）
}

// frozenDB 固化事件本地持久化结构（frozen_events.json）。
// （frozenDB is the local persistence shape for frozen events (frozen_events.json).）
type frozenDB struct {
	TradingDay string        `json:"trading_day"` // 最近写入的交易日（用于跨日归档/清理）
	Events     []FrozenEvent `json:"events"`      // 当前固化的利好/利空事件列表
}
