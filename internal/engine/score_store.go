// Package engine 8a/8b 打分持久化：scores.json 存当日最新分（无信号也持续写盘）。
// 启动时 Load 回填聚合器，重启后前端立即可见上次打分结果。
// 打分数据结构为 map[股票代码]打分明细，包含四战法得分、动量分、D1评分等。
// 按交易日分桶存储，跨交易日自动重置。
// English: Package engine 8a/8b score persistence: scores.json stores the latest score of the day
// (written continuously even without signals). On startup Load backfills the aggregator, so the
// frontend immediately sees the last scoring result after a restart.
package engine

import (
	"encoding/json"
	"log"
	"os"
	"sync"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/data"
)

// scoreStoreFile scores.json 磁盘结构（按交易日分桶）。
// English: scoreStoreFile is the on-disk structure of scores.json (bucketed by trading day).
type scoreStoreFile struct {
	TradingDay string `json:"trading_day"` // 交易日（YYYY-MM-DD）
	// English: trading day (YYYY-MM-DD).
	Scores map[string]combat_agent.StockScores `json:"scores"` // 当日各股最新打分（code → 打分明细）
	// English: latest score per stock for the day (code -> score detail).
}

// scoreStore 打分持久化存储，Save 覆盖写当日最新分，Load 返回最近一次结果。
// English: scoreStore is the score persistence store; Save overwrites the day's latest score,
// and Load returns the most recent result.
type scoreStore struct {
	path string // scores.json 磁盘路径（空表示不持久化）
	// English: on-disk path of scores.json (empty = not persisted).
	mu sync.RWMutex // 保护 day/scores 的读写
	// English: guards reads/writes of day/scores.
	day string // 当前交易日（YYYY-MM-DD）
	// English: current trading day (YYYY-MM-DD).
	scores map[string]combat_agent.StockScores // 当日各股最新打分（code → 打分明细）
	// English: latest score per stock for the day (code -> score detail).
}

// newScoreStore 创建打分存储并加载已有文件（不存在时忽略）。
// path 为空表示纯内存模式（不落盘），常用于测试或未配置 dataDir 的场景。
// English: newScoreStore creates the score store and loads the existing file (ignored when absent).
// An empty path means in-memory-only mode (not persisted), often used in tests or when dataDir is unset.
func newScoreStore(path string) *scoreStore {
	s := &scoreStore{path: path, scores: make(map[string]combat_agent.StockScores)}
	if path != "" {
		s.load()
	}
	return s
}

// Save 持久化当日最新分（调用方已持有最新 map，直接覆盖写盘）。
// 内存与磁盘同时更新：先更新内存供 Load 快速返回，再整文件覆盖写盘保证重启可恢复。
// English: Save persists the day's latest score (the caller already holds the latest map, so it
// overwrites the file directly). Both memory and disk are updated: memory first so Load returns
// quickly, then the whole file is overwritten to be recoverable after a restart.
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
		log.Printf("[engine] scores 序列化失败: %v", err)
		return
	}
	// §E3 原子写：防 crash/OOM 截断（此前失败静默 return，磁盘满无感知）
	mustAtomicWrite("scores", s.path, raw)
}

// Load 读取磁盘打分记录（跨交易日保留最近一次，前端由新轮次覆盖）。
// English: Load reads the score record (keeps the most recent across trading days; overwritten by the frontend in new rounds).
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
// §修复 P2#19：跨交易日桶校验——旧实现无条件装载 f.TradingDay 的分数，收盘后跨日重启
// 会把昨日打分当"当日"展示给前端（端口口气源，昨日强分误导今日决策），直到下轮打分覆盖。
// 现与 signal_store 同口径：仅装载当前交易日的桶，跨日自动重置为空分。
// English: P2#19 — cross-trading-day bucket check. The old load accepted whatever TradingDay the file
// carried, so a restart after close would show yesterday's scores as "today's" (a stale-signal source)
// until the next scoring round overwrote them. Now only the current trading day's bucket is loaded;
// a rollover resets to an empty map, matching signal_store's day-bucket semantics.
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
	// §修复 P2#19：交易日桶不匹配 → 视为空桶（当日从零开始），不展示旧日强分。
	// English: P2#19 — a mismatched trading-day bucket counts as empty (start fresh today).
	if f.TradingDay != data.TradingDayDate(time.Now()) {
		s.day = f.TradingDay
		s.scores = map[string]combat_agent.StockScores{}
	} else {
		s.day = f.TradingDay
		s.scores = f.Scores
	}
	s.mu.Unlock()
}
