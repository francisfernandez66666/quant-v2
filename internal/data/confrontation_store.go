// Package data — 政策反制事件持久化存储。
// 记录关税反制/出口管制/稀土限制等涉外政策反制事件，按交易日分桶，
// 跨交易日自动清空（政策反制仅当日有效，供消息中心与前端日历展示）。
package data

import (
	"encoding/json"
	"log"
	"os"
	"sync"
	"time"
)

// ConfrontationEvent 一条政策反制事件。
// 由 newsagent 从涉外政策新闻关键词推导，方向多为利空（对受影响板块施压）。
type ConfrontationEvent struct {
	Title     string   `json:"title"`             // 事件标题
	Content   string   `json:"content,omitempty"` // 事件正文/摘要
	Datetime  string   `json:"datetime"`          // 事件时间 YYYY-MM-DD HH:MM:SS
	Sectors   []string `json:"sectors,omitempty"` // 受影响的板块
	Direction string   `json:"direction"`         // 方向：利好/利空
	Impact    string   `json:"impact"`            // 影响级别：高/中/低
	Source    string   `json:"source"`            // 来源（通常为"政策反制"）
}

// confrontationFile 政策反制持久化文件结构。
// TradingDay 记录当前交易日；Events 为当日政策反制事件（跨日自动清空）。
type confrontationFile struct {
	TradingDay string               `json:"trading_day"`
	Events     []ConfrontationEvent `json:"events"`
}

// ConfrontationStore 政策反制事件持久化存储。
// 跨交易日自动清空旧事件，保证每个交易日从全新记录开始。
type ConfrontationStore struct {
	mu   sync.Mutex // 保护 file 的并发读写
	path string     // 持久化文件路径
	file confrontationFile
}

// NewConfrontationStore 创建政策反制存储并加载本地文件；跨交易日清空历史。
func NewConfrontationStore(path string) *ConfrontationStore {
	s := &ConfrontationStore{path: path}
	if path == "" {
		return s
	}
	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, &s.file); err != nil {
			log.Printf("[confront] 解析失败: %v", err)
			s.file = confrontationFile{}
		}
	}
	if s.file.TradingDay != TradingDayDate(time.Now()) {
		s.file.Events = nil
	}
	return s
}

// persist 将当前事件状态序列化并写入本地文件。
func (s *ConfrontationStore) persist() {
	if s.path == "" {
		return
	}
	s.file.TradingDay = TradingDayDate(time.Now())
	raw, err := json.MarshalIndent(s.file, "", "  ")
	if err != nil {
		return
	}
	if err := os.WriteFile(s.path, raw, 0644); err != nil {
		log.Printf("[confront] 写入失败: %v", err)
	}
}

// HasTitle 判断指定标题的反制事件是否已存在（跨日不参与，仅判断当日）。
func (s *ConfrontationStore) HasTitle(title string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file.TradingDay != TradingDayDate(time.Now()) {
		return false
	}
	for _, ev := range s.file.Events {
		if ev.Title == title {
			return true
		}
	}
	return false
}

// Append 追加一条政策反制事件到当日记录（跨日自动清空）。
func (s *ConfrontationStore) Append(ev ConfrontationEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file.TradingDay != TradingDayDate(time.Now()) {
		s.file.Events = nil
	}
	s.file.Events = append(s.file.Events, ev)
	s.persist()
}

// List 返回当日全部政策反制事件（按时间正序）。
func (s *ConfrontationStore) List() []ConfrontationEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file.TradingDay != TradingDayDate(time.Now()) {
		return nil
	}
	out := make([]ConfrontationEvent, len(s.file.Events))
	copy(out, s.file.Events)
	return out
}
