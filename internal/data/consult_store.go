// Package data — 股票咨询对话持久化存储。
// 按交易日分桶保存对话历史，跨交易日自动清空（咨询记录仅当日有效）。
package data

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"sync"
	"time"
)

// ErrStoreUnavailable §FIX-10(20260919 批四)：账号隔离存储不可用（accountsRoot 未注入等）。
// 咨询历史**绝不回退共享 store**——宁可拒绝服务（HTTP 503）也不可 A 的对话漏进 B 的历史
// （串号）。engine.ConsultLLM 用 %w 包装本哨兵，server 用 errors.Is 映射 503。
// English: per-user consult isolation is unavailable; refuse service rather than leak
// conversations across accounts (mapped to HTTP 503).
var ErrStoreUnavailable = errors.New("咨询历史存储不可用（账号隔离未就绪），已拒绝服务以防串号")

// consultStoreMaxMessages §FIX-10：单账号当日历史条数上限（user+assistant 各算一条）。
// 无上限时"每轮两次全量重写"的代价随对话长度线性膨胀（大文件 fsync + 付费 prompt 注入）；
// 超限后丢弃最旧条目，保留最近对话。
// （cap on per-day stored messages; oldest entries are trimmed beyond the limit.）
const consultStoreMaxMessages = 500

// ConsultMessage 一条咨询对话消息。
type ConsultMessage struct {
	Role    string    `json:"role"`    // user / assistant（用户 / 助手）
	Content string    `json:"content"` // 消息内容
	Time    time.Time `json:"time"`    // 发送时间
}

// consultFile 咨询对话持久化文件结构。
// TradingDay 记录当前交易日；Messages 为当日对话历史（跨日自动清空）。
type consultFile struct {
	TradingDay string           `json:"trading_day"` // 当前交易日
	Messages   []ConsultMessage `json:"messages"`    // 当日对话历史
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
	if err := atomicWrite(s.path, raw, 0644); err != nil {
		log.Printf("[consult] 写入失败: %v", err)
	}
}

// Append 追加一条用户或助手消息到当日对话历史（跨日自动清空，超上限淘汰最旧）。
func (s *ConsultStore) Append(role, content string) {
	s.AppendPair(ConsultMessage{Role: role, Content: content})
}

// AppendPair §FIX-10(20260919 批四)：一轮咨询的"提问+回复"合并为一次落盘。
// 旧实现每轮 Append×2 = 两次全量 atomicWrite/fsync，且两条之间存在"半轮"窗口
// （进程崩溃时历史里留下无回复的提问）。整轮合并后原子落盘。
// （append a whole turn in ONE persist — halves fsync traffic and removes the half-turn window.）
func (s *ConsultStore) AppendPair(msgs ...ConsultMessage) {
	if len(msgs) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file.TradingDay != TradingDayDate(time.Now()) {
		s.file.Messages = nil
	}
	now := time.Now()
	for _, m := range msgs {
		m.Time = now
		s.file.Messages = append(s.file.Messages, m)
	}
	// 条数上限：超限丢最旧（保留最近 consultStoreMaxMessages 条）。
	if over := len(s.file.Messages) - consultStoreMaxMessages; over > 0 {
		s.file.Messages = append([]ConsultMessage(nil), s.file.Messages[over:]...)
	}
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
