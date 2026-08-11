// Package engine 当日战法信号固化存储：
// 记录每个 code@strategy 最近一次 Pass 的交易信号（做多/做空），跨重启持久化到磁盘。
// 目标：信号"固化一天"——除非有新的评分信号替换，否则当日持续展示；重启后自动恢复。
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

// signalStoreFile 当日固化信号磁盘结构（按交易日分桶）。
type signalStoreFile struct {
	TradingDay string                `json:"trading_day"`
	Signals    []combat_agent.Signal `json:"signals"`
}

// signalStore 当日战法信号固化存储，键为 code@strategy。
// 加载时校验交易日，跨天自动重置为当日空桶。
type signalStore struct {
	mu    sync.Mutex
	path  string
	byKey map[string]combat_agent.Signal
}

// newSignalStore 创建当日信号固化存储并从磁盘加载（仅保留当前交易日的数据）。
func newSignalStore(path string) *signalStore {
	s := &signalStore{path: path, byKey: make(map[string]combat_agent.Signal)}
	if path == "" {
		return s
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return s
	}
	var f signalStoreFile
	if err := json.Unmarshal(raw, &f); err != nil {
		log.Printf("[engine] signal_store 解析失败: %v", err)
		return s
	}
	if f.TradingDay != data.TradingDayDate(time.Now()) {
		return s
	}
	for _, sig := range f.Signals {
		if sig.Code != "" && sig.Strategy != "" {
			s.byKey[sig.Code+"@"+sig.Strategy] = sig
		}
	}
	return s
}

// Upsert 合并本轮 Pass 信号：仅交易型（做多/做空）入库存，按 code@strategy 覆盖；
// 新信号不早于已存信号时替换（刷新价格/理由）。提醒/止盈止损不入库存（消息中心已单独持久化）。
func (s *signalStore) Upsert(sigs []combat_agent.Signal) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	for _, sig := range sigs {
		if sig.Code == "" || sig.Strategy == "" {
			continue
		}
		if sig.Direction != "做多" && sig.Direction != "做空" {
			continue
		}
		key := sig.Code + "@" + sig.Strategy
		if cur, ok := s.byKey[key]; ok && !sig.GeneratedAt.IsZero() && !cur.GeneratedAt.IsZero() && sig.GeneratedAt.Before(cur.GeneratedAt) {
			continue
		}
		s.byKey[key] = sig
		changed = true
	}
	if changed {
		s.save()
	}
}

// List 返回当日固化信号列表（供聚合器展示用，键去重后的当前集合）。
func (s *signalStore) List() []combat_agent.Signal {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]combat_agent.Signal, 0, len(s.byKey))
	for _, sig := range s.byKey {
		out = append(out, sig)
	}
	return out
}

// save 将当日固化信号写盘（覆盖写，交易日标记）。
func (s *signalStore) save() {
	if s.path == "" {
		return
	}
	f := signalStoreFile{
		TradingDay: data.TradingDayDate(time.Now()),
		Signals:    make([]combat_agent.Signal, 0, len(s.byKey)),
	}
	for _, sig := range s.byKey {
		f.Signals = append(f.Signals, sig)
	}
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return
	}
	if err := os.WriteFile(s.path, raw, 0644); err != nil {
		log.Printf("[engine] signal_store 写入失败: %v", err)
	}
}

// mergeSignals 合并当前轮信号与当日固化信号（固化集合在前，冲突由聚合器按 code 去重裁决）。
func mergeSignals(cur, persisted []combat_agent.Signal) []combat_agent.Signal {
	out := make([]combat_agent.Signal, 0, len(cur)+len(persisted))
	out = append(out, persisted...)
	out = append(out, cur...)
	return out
}
