// Package data 提供股票数据清洗、查询和跟踪功能。
// StockTracker 负责管理被跟踪的个股列表（利好/利空事件驱动），支持持久化到 JSON 文件。
// Package data provides stock cleaning, querying and tracking.
// StockTracker manages an event-driven (bullish/bearish) tracked-stock pool with
// JSON file persistence.
package data

import (
	"encoding/json"
	"log"
	"os"
	"sync"
)

// TrackedStock 表示一条被跟踪的个股记录。
// 包含方向（利好/利空）、有效期、进入理由等信息，用于策略的持续监控。
// TrackedStock is one tracked-stock record (direction, expiry, reason, etc.)
// maintained for continuous strategy monitoring.
type TrackedStock struct {
	Code          string `json:"code"`            // 股票代码
	Name          string `json:"name"`            // 股票名称
	Direction     string `json:"direction"`       // 利好/利空
	EntryTD       string `json:"entry_td"`        // 进入跟踪日期（YYYY-MM-DD）
	ExpiryTD      string `json:"expiry_td"`       // 过期日期，超过后不再跟踪
	Reason        string `json:"reason"`          // 进入跟踪的理由
	LastSeenTD    string `json:"last_seen_td"`    // 最近一次策略周期日期
	LastHadSignal bool   `json:"last_had_signal"` // 最近周期是否产生信号
}

// StockTracker 个股跟踪器，维护被跟踪的个股池，支持自动过期清理和文件持久化。
// 线程安全（使用 sync.Mutex 保护数据）。
// StockTracker tracks watched stocks with auto-expiry cleanup and file
// persistence; thread-safe via sync.Mutex.
type StockTracker struct {
	mu     sync.Mutex
	stocks map[string]*TrackedStock
	path   string
}

// NewStockTracker 创建 StockTracker 实例。
// path: 持久化文件路径（JSON 格式），为空则不进行持久化。
// 创建后自动从文件加载已有跟踪数据。
// NewStockTracker creates a tracker; path is the JSON persistence file (empty to
// disable persistence) and existing data is loaded automatically.
func NewStockTracker(path string) *StockTracker {
	t := &StockTracker{
		stocks: make(map[string]*TrackedStock),
		path:   path,
	}
	t.load()
	return t
}

// Add 添加或更新一条个股跟踪记录。
// code: 股票代码。若已存在相同 code 的记录，则更新其字段。
// direction: 方向（"利好"/"利空"）。
// reason: 跟踪理由。
// entryTD: 进入日期（格式如 "2026-01-15"）。
// expiryTD: 过期日期，超过该日期后不再跟踪。
// Add creates or updates a tracked record: an existing code is updated in place,
// keeping the earlier EntryTD and reusing it otherwise.
func (t *StockTracker) Add(code, name, direction, reason, entryTD, expiryTD string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if existing, ok := t.stocks[code]; ok {
		existing.Name = name
		existing.Direction = direction
		existing.Reason = reason
		existing.ExpiryTD = expiryTD
		if entryTD > existing.EntryTD {
			existing.EntryTD = entryTD
		}
		return
	}
	t.stocks[code] = &TrackedStock{
		Code:      code,
		Name:      name,
		Direction: direction,
		Reason:    reason,
		EntryTD:   entryTD,
		ExpiryTD:  expiryTD,
	}
	t.save()
}

// GetActive 获取指定日期下所有未过期的跟踪个股。
// td: 当前日期字符串（格式 "2006-01-02"）。
// 返回 ExpiryTD >= td 的所有跟踪记录。
// GetActive returns all tracked stocks not yet expired on date td.
func (t *StockTracker) GetActive(td string) []*TrackedStock {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []*TrackedStock
	for _, s := range t.stocks {
		if td <= s.ExpiryTD {
			out = append(out, s)
		}
	}
	return out
}

// GetActiveByDirection 按方向过滤获取未过期的跟踪个股。
// td: 当前日期字符串。
// direction: 方向过滤条件（"利好"/"利空"）。
// 返回 ExpiryTD >= td 且 Direction 匹配的记录。
// GetActiveByDirection returns non-expired records matching a direction on td.
func (t *StockTracker) GetActiveByDirection(td, direction string) []*TrackedStock {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []*TrackedStock
	for _, s := range t.stocks {
		if td <= s.ExpiryTD && s.Direction == direction {
			out = append(out, s)
		}
	}
	return out
}

// OnCycleDone 在每个策略运行周期结束时调用，用于更新跟踪状态和清理过期记录。
// td: 当前周期日期。
// signaledCodes: 本周期产生信号的个股代码列表。
// 逻辑：
//  1. 更新所有跟踪记录的最后出现日期和信号状态。
//  2. 删除已过期的记录（td > ExpiryTD）。
//  3. 删除到期日当天且未产生信号的记录。
//
// OnCycleDone runs at the end of each strategy cycle: updates last-seen/signal
// status, then removes expired records and same-day expired records without a signal.
func (t *StockTracker) OnCycleDone(td string, signaledCodes []string) {
	sigSet := make(map[string]bool)
	for _, c := range signaledCodes {
		sigSet[c] = true
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, s := range t.stocks {
		s.LastSeenTD = td
		s.LastHadSignal = sigSet[s.Code]
	}
	for code, s := range t.stocks {
		if td > s.ExpiryTD {
			delete(t.stocks, code)
			continue
		}
		if td == s.ExpiryTD && !s.LastHadSignal {
			delete(t.stocks, code)
		}
	}
	t.save()
}

// load 从 JSON 文件加载持久化的跟踪个股数据。
// 如果 path 为空或文件不存在/解析失败，静默返回。
// load reads tracked stocks from the JSON file; silently returns when the path
// is empty or the file is missing/fails to parse.
func (t *StockTracker) load() {
	if t.path == "" {
		return
	}
	data, err := os.ReadFile(t.path)
	if err != nil {
		return
	}
	var stocks []*TrackedStock
	if err := json.Unmarshal(data, &stocks); err != nil {
		log.Printf("[stock_tracker] 解析失败: %v", err)
		return
	}
	for _, s := range stocks {
		t.stocks[s.Code] = s
	}
	log.Printf("[stock_tracker] 已加载 %d 条跟踪个股", len(stocks))
}

// save 将当前跟踪个股数据持久化到 JSON 文件。
// 如果 path 为空则跳过，序列化或写入失败时记录日志。
// save persists the tracked stocks to the JSON file, skipping when path is empty
// and logging any serialization/write failures.
func (t *StockTracker) save() {
	if t.path == "" {
		return
	}
	stocks := make([]*TrackedStock, 0, len(t.stocks))
	for _, s := range t.stocks {
		stocks = append(stocks, s)
	}
	data, err := json.MarshalIndent(stocks, "", "  ")
	if err != nil {
		log.Printf("[stock_tracker] 序列化失败: %v", err)
		return
	}
	if err := atomicWrite(t.path, data, 0644); err != nil {
		log.Printf("[stock_tracker] 写入失败: %v", err)
	}
}
