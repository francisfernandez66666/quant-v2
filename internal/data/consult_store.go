// Package data — 股票咨询对话持久化存储。
// 按交易日分桶保存对话历史，跨交易日自动清空（咨询记录仅当日有效）。
package data

import (
	"encoding/json"
	"log"
	"os"
	"sync"
	"time"
)

// ConsultMessage 一条咨询对话消息。
type ConsultMessage struct {
	Role    string    `json:"role"`     // user / assistant
	Content string    `json:"content"`  // 消息内容
	Time    time.Time `json:"time"`     // 发送时间
}

// consultFile 咨询对话持久化文件结构。
// TradingDay 记录当前交易日；Messages 为当日对话历史（跨日自动清空）。
type consultFile struct {
	TradingDay string           `json:"trading_day"`
	Messages   []ConsultMessage `json:"messages"`
}

// ConsultStore 咨询对话持久化存储。
// 跨交易日自动清空旧会话，保证每个交易日从全新对话开始。
type ConsultStore struct {
	mu   sync.Mutex // 保护 file 的并发读写
	path string     // 持久化文件路径
	file consultFile
}

// NewConsultStore 创建咨询对话存储并加载本地文件；跨交易日清空历史。
func NewConsultStore(path string) *ConsultStore {
	s := &ConsultStore{path: path}
	if path == "" {
		return s
	}
	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, &s.file); err != nil {
			log.Printf("[consult] 解析失败: %v", err)
			s.file = consultFile{}
		}
	}
	if s.file.TradingDay != TradingDayDate(time.Now()) {
		s.file.Messages = nil
	}
	return s
}

// persist 将当前对话状态序列化并写入本地文件。
func (s *ConsultStore) persist() {
	if s.path == "" {
		return
	}
	s.file.TradingDay = TradingDayDate(time.Now())
	raw, err := json.MarshalIndent(s.file, "", "  ")
	if err != nil {
		return
	}
	if err := os.WriteFile(s.path, raw, 0644); err != nil {
		log.Printf("[consult] 写入失败: %v", err)
	}
}

// Append 追加一条用户或助手消息到当日对话历史（跨日自动清空）。
func (s *ConsultStore) Append(role, content string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file.TradingDay != TradingDayDate(time.Now()) {
		s.file.Messages = nil
	}
	s.file.Messages = append(s.file.Messages, ConsultMessage{
		Role:    role,
		Content: content,
		Time:    time.Now(),
	})
	s.persist()
}

// List 返回当日全部对话历史（按时间正序）。
func (s *ConsultStore) List() []ConsultMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file.TradingDay != TradingDayDate(time.Now()) {
		return nil
	}
	out := make([]ConsultMessage, len(s.file.Messages))
	copy(out, s.file.Messages)
	return out
}

// Clear 清空当日对话历史。
func (s *ConsultStore) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.file.Messages = nil
	s.persist()
}