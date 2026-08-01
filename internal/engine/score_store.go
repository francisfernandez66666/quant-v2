// Package engine 8a/8b 打分持久化：scores.json 存当日最新分（无信号也持续写盘）。
// 启动时 Load 回填聚合器，重启后前端立即可见上次打分结果。
package engine

import (
	"encoding/json"
	"os"
	"sync"

	"quant-trading-v2/internal/combat_agent"
)

// scoreStoreFile scores.json 磁盘结构（按交易日分桶）。
type scoreStoreFile struct {
	TradingDay string                              `json:"trading_day"`
	Scores     map[string]combat_agent.StockScores `json:"scores"`
}

// scoreStore 打分持久化存储，Save 覆盖写当日最新分，Load 返回最近一次结果。
type scoreStore struct {
	path   string                              // scores.json 磁盘路径（空表示不持久化）
	mu     sync.RWMutex                        // 保护 day/scores 的读写
	day    string                              // 当前交易日（YYYY-MM-DD）
	scores map[string]combat_agent.StockScores // 当日各股最新打分（code → 打分明细）
}

// newScoreStore 创建打分存储并加载已有文件（不存在时忽略）。
// path 为空表示纯内存模式（不落盘），常用于测试或未配置 dataDir 的场景。
func newScoreStore(path string) *scoreStore {
	s := &scoreStore{path: path, scores: make(map[string]combat_agent.StockScores)}
	if path != "" {
		s.load()
	}
	return s
}

// Save 持久化当日最新分（调用方已持有最新 map，直接覆盖写盘）。
// 内存与磁盘同时更新：先更新内存供 Load 快速返回，再整文件覆盖写盘保证重启可恢复。
func (s *scoreStore) Save(day string, scores map[string]combat_agent.StockScores) {
	if s == nil || s.path == "" {
		return
	}
	if scores == nil {
		scores = map[string]combat_agent.StockScores{}
	}
	s.mu.Lock()
	s.day = day
	s.scores = scores
	s.mu.Unlock()

	raw, err := json.MarshalIndent(scoreStoreFile{TradingDay: day, Scores: scores}, "", "  ")
	if err != nil {
		return
	}
	if err := os.WriteFile(s.path, raw, 0644); err != nil {
		return
	}
}

// Load 读取磁盘打分记录（跨交易日保留最近一次，前端由新轮次覆盖）。
func (s *scoreStore) Load() map[string]combat_agent.StockScores {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]combat_agent.StockScores, len(s.scores))
	for k, v := range s.scores {
		out[k] = v
	}
	return out
}

// load 从磁盘加载 scores.json。文件不存在或损坏时静默保留空 map（不 panic）。
func (s *scoreStore) load() {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var f scoreStoreFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return
	}
	s.mu.Lock()
	s.day = f.TradingDay
	s.scores = f.Scores
	s.mu.Unlock()
}
