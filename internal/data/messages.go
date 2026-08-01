// Package data — 消息中心持久化存储。
// 按稳定键（ID）去重合并消息，支持删除墓碑（当日不再自动出现）与跨交易日清理。
package data

import (
	"encoding/json"
	"log"
	"os"
	"sort"
	"sync"
	"time"
)

// MessageItem 消息中心单条消息（与前端 /api/alerts 结构一致）。
// ID 为稳定去重键（如 600519@止盈 / hold@SIGxxx），供手工删除定位。
type MessageItem struct {
	ID          string    `json:"id"`           // 稳定去重键
	Code        string    `json:"code"`         // 股票代码
	Name        string    `json:"name"`         // 股票名称
	Level       string    `json:"level"`        // 级别（如 止盈/止损/提示）
	Action      string    `json:"action"`       // 动作
	Strategy    string    `json:"strategy"`     // 关联策略
	Time        string    `json:"time"`         // 触发时间字符串
	Title       string    `json:"title"`        // 标题
	Body        string    `json:"body"`         // 正文
	Direction   string    `json:"direction"`    // 方向（利好/利空）
	GeneratedAt time.Time `json:"generated_at"` // 生成时间
}

// messageFile 消息中心持久化文件结构。
// TradingDay 记录当前交易日；Messages 为消息列表；DeletedKeys 为当日删除墓碑。
type messageFile struct {
	TradingDay  string        `json:"trading_day"`
	Messages    []MessageItem `json:"messages"`
	DeletedKeys []string      `json:"deleted_keys"`
}

// MessageStore 消息中心持久化存储。
// 按稳定键（ID）去重合并；已删除键记录墓碑（当日内不再自动出现），跨交易日自动清除墓碑。
type MessageStore struct {
	mu   sync.Mutex // 保护 file 的并发读写
	path string     // 持久化文件路径
	file messageFile
}

// NewMessageStore 创建消息存储并加载本地文件；跨交易日清除墓碑。
func NewMessageStore(path string) *MessageStore {
	s := &MessageStore{path: path}
	if path == "" {
		return s
	}
	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, &s.file); err != nil {
			log.Printf("[messages] 解析失败: %v", err)
			s.file = messageFile{}
		}
	}
	if s.file.TradingDay != TradingDayDate(time.Now()) {
		s.file.DeletedKeys = nil
	}
	return s
}

// persist 将当前消息状态序列化并写入本地文件。
// 更新 TradingDay 为当前交易日；path 为空或写文件失败时记录日志并跳过。
func (s *MessageStore) persist() {
	if s.path == "" {
		return
	}
	s.file.TradingDay = TradingDayDate(time.Now())
	raw, err := json.MarshalIndent(s.file, "", "  ")
	if err != nil {
		return
	}
	if err := os.WriteFile(s.path, raw, 0644); err != nil {
		log.Printf("[messages] 写入失败: %v", err)
	}
}

// Sync 合并本轮消息：按 ID 去重（已存在则刷新内容），被删除的键不再出现。
func (s *MessageStore) Sync(items []MessageItem) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file.TradingDay != TradingDayDate(time.Now()) {
		s.file.DeletedKeys = nil
	}
	deleted := make(map[string]bool, len(s.file.DeletedKeys))
	for _, k := range s.file.DeletedKeys {
		deleted[k] = true
	}
	byKey := make(map[string]int, len(s.file.Messages))
	for i := range s.file.Messages {
		byKey[s.file.Messages[i].ID] = i
	}
	for _, it := range items {
		if it.ID == "" {
			it.ID = it.Code + "@" + it.Level
		}
		if deleted[it.ID] {
			continue
		}
		if idx, ok := byKey[it.ID]; ok {
			kept := s.file.Messages[idx]
			kept.Name = it.Name
			kept.Level = it.Level
			kept.Action = it.Action
			kept.Strategy = it.Strategy
			kept.Time = it.Time
			kept.Title = it.Title
			kept.Body = it.Body
			kept.Direction = it.Direction
			kept.GeneratedAt = it.GeneratedAt
			s.file.Messages[idx] = kept
			continue
		}
		s.file.Messages = append(s.file.Messages, it)
		byKey[it.ID] = len(s.file.Messages) - 1
	}
	s.persist()
}

// Delete 删除单条消息：移除并记录墓碑（当日内不再自动出现）。
func (s *MessageStore) Delete(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.file.Messages[:0]
	for _, m := range s.file.Messages {
		if m.ID != key {
			out = append(out, m)
		}
	}
	s.file.Messages = out
	if key != "" {
		s.file.DeletedKeys = append(s.file.DeletedKeys, key)
	}
	s.persist()
}

// ClearAll 清空全部消息：当前消息全部移除并记录墓碑（当日内不再重新出现）。
func (s *MessageStore) ClearAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.file.Messages {
		s.file.DeletedKeys = append(s.file.DeletedKeys, m.ID)
	}
	s.file.Messages = nil
	s.persist()
}

// List 返回全部消息（按生成时间倒序）。
func (s *MessageStore) List() []MessageItem {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]MessageItem, len(s.file.Messages))
	copy(out, s.file.Messages)
	sort.Slice(out, func(i, j int) bool {
		return out[i].GeneratedAt.After(out[j].GeneratedAt)
	})
	return out
}
