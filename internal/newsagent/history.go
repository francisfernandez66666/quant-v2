// history.go — §ENH-3(20260919 批B) 历史事件归档与检索：
// news_events.json 原本跨交易日直接清空，历史事件从不留存（AI 顾问没有"记忆"、
// 事件因子研究也没有原料）。本文件在换日归档点把旧事件追加到
// <dataDir>/news_event_history.jsonl（一行一条 {"trading_day","event"}），
// 并提供按个股代码过滤的轻量检索（量级 ≤200 条/日，全量线性扫即可，无需向量库）。
// English: appends previous-day events to a JSONL archive on rollover and offers a
// lightweight code-filtered search (file scale makes vectors unnecessary).
package newsagent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"sync"
)

// eventHistoryEntry 归档行格式：事件 + 其所属交易日（YYYYMMDD）。
// English: one archived line: event plus its trading day.
type eventHistoryEntry struct {
	TradingDay string    `json:"trading_day"`
	Event      NewsEvent `json:"event"`
}

// HistoryHit 检索命中：归档事件 + 发生交易日。
// English: a search hit: archived event + trading day.
type HistoryHit struct {
	Day   string
	Event NewsEvent
}

// appendEventHistory 把一批事件追加写入归档文件（换日归档点调用）。
// 失败仅记日志——归档是增强项，绝不允许拖垮新闻主链路的落盘。
// English: appends events to the history file; failures log only (never blocks the main news flow).
func (a *Agent) appendEventHistory(day string, events []NewsEvent) {
	if a.historyPath == "" || len(events) == 0 {
		return
	}
	f, err := os.OpenFile(a.historyPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		log.Printf("[newsagent] 事件历史归档打开失败（跳过本次归档）: %v", err)
		return
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)
	n := 0
	for _, e := range events {
		if err := enc.Encode(eventHistoryEntry{TradingDay: day, Event: e}); err != nil {
			log.Printf("[newsagent] 事件历史归档序列化失败（已写 %d 条）: %v", n, err)
			break
		}
		n++
	}
	if err := w.Flush(); err != nil {
		log.Printf("[newsagent] 事件历史归档写入失败: %v", err)
	}
}

// historyCache 归档全量内存索引（按 mtime+size 失效），咨询检索高频复用时避免每请求重扫文件。
// English: in-memory archive cache invalidated by mtime+size.
type historyCache struct {
	mu       sync.Mutex
	key      string // "mtime:size"，变化即重读
	entries  []eventHistoryEntry
	loadFail bool // 上次读取报错——保持沉默期，不逐请求刷日志
}

// loadHistory 读取全量归档（文件不存在=空，非错误）。
// English: loads all archived entries (absent file = empty, not an error).
func (a *Agent) loadHistory() []eventHistoryEntry {
	c := &a.histCache
	c.mu.Lock()
	defer c.mu.Unlock()
	st, err := os.Stat(a.historyPath)
	if err != nil {
		c.entries, c.key = nil, ""
		return nil
	}
	key := fmt.Sprintf("%d:%d", st.ModTime().UnixNano(), st.Size())
	if c.key == key && c.entries != nil {
		return c.entries // 未变化，命中缓存
	}
	var entries []eventHistoryEntry
	f, err := os.Open(a.historyPath)
	if err != nil {
		if !c.loadFail {
			log.Printf("[newsagent] 事件历史归档读取失败: %v", err)
			c.loadFail = true
		}
		return c.entries // 沿用旧缓存
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024) // 单行=一条事件 JSON，放宽到 1MB
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e eventHistoryEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue // 半行/坏行容忍跳过（进程崩溃可能留下残行）
		}
		entries = append(entries, e)
	}
	c.entries, c.key, c.loadFail = entries, key, false
	return entries
}

// eventMatchesCodes 事件是否关联给定个股之一：清洗股 "名称|代码" 与原始股 "名称(代码)" 双口径包含即中。
// English: whether the event references any given stock code (both cleaned and raw formats).
func eventMatchesCodes(e NewsEvent, codes []string) bool {
	if len(codes) == 0 {
		return false
	}
	hay := strings.Join(e.CleanedStocks, "\n") + "\n" + strings.Join(e.RelatedStocks, "\n")
	for _, c := range codes {
		if c != "" && strings.Contains(hay, c) {
			return true
		}
	}
	return false
}

// SearchEventHistory 检索历史同类事件：命中任一代码、且交易日早于 excludeFrom（YYYYMMDD，
// 当日事件属"今日新闻"不该混进历史段）。排序=|score| 降序、同日按 Datetime 降序，取 topN。
// 同一标题在归档中可能因换日竞态重复，这里按 (day,截断标题) 去重。
// English: code-filtered history search with recency/strength ranking and light dedup.
func (a *Agent) SearchEventHistory(codes []string, excludeFrom string, topN int) []HistoryHit {
	if a.historyPath == "" || topN <= 0 {
		return nil
	}
	entries := a.loadHistory()
	seen := make(map[string]bool)
	var hits []HistoryHit
	for _, en := range entries {
		if len(en.TradingDay) != 8 {
			continue // 非 YYYYMMDD 的坏行直接丢弃（调用方按日切片渲染，长度必须先保证）
		}
		if excludeFrom != "" && en.TradingDay >= excludeFrom {
			continue // 只取严格早于当日的历史
		}
		if !eventMatchesCodes(en.Event, codes) {
			continue
		}
		key := en.TradingDay + "|" + truncTitle(en.Event.Title)
		if seen[key] {
			continue
		}
		seen[key] = true
		hits = append(hits, HistoryHit{Day: en.TradingDay, Event: en.Event})
	}
	// 排序口径：影响强度 |score| 优先（顾问最该看到的是"大事"），同分先近日后新时刻。
	// English: rank by |score| desc, then newer trading day, then newer datetime.
	sort.SliceStable(hits, func(i, j int) bool {
		// 取绝对值比较：利空大事件与利好大事件同权重
		si, sj := abs(hits[i].Event.Score), abs(hits[j].Event.Score)
		if si != sj {
			return si > sj
		}
		// 同分先看日期（YYYYMMDD 字典序=时间序）
		if hits[i].Day != hits[j].Day {
			return hits[i].Day > hits[j].Day
		}
		// 同日再比事件时刻（HH:MM:SS 字典序可比）
		return hits[i].Event.Datetime > hits[j].Event.Datetime
	})
	// 截断到请求方给定的 topN（咨询侧固定 4 条，防 prompt 膨胀）
	if len(hits) > topN {
		hits = hits[:topN]
	}
	return hits
}

// abs 小工具：score 绝对值。English: absolute value helper.
func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
