// Package engine 当日战法信号固化存储：
// 记录每个 code@strategy 最近一次 Pass 的交易信号（做多/做空），跨重启持久化到磁盘。
// 目标：信号"固化一天"——除非有新的评分信号替换，否则当日持续展示；重启后自动恢复。
// English: per-day fixed storage of strategy signals: records the latest Passed trade signal
// (long/short) for each code@strategy and persists it to disk across restarts. Goal: signals stay
// pinned for the whole day — kept visible until replaced by a newer score, and restored after a restart.
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
// English: on-disk layout of the pinned signals for the day (bucketed by trading day).
type signalStoreFile struct {
	TradingDay string                `json:"trading_day"` // 交易日
	Signals    []combat_agent.Signal `json:"signals"`     // 当日固化信号
	// 失效墓碑 key 集合（code@strategy）：买入前提已破坏的固化信号当日不再复活。
	// English: tombstoned keys (code@strategy) whose buy premise broke; never re-pinned that day.
	Invalidated []string `json:"invalidated,omitempty"`
}

// signalStore 当日战法信号固化存储，键为 code@strategy。
// 加载时校验交易日，跨天自动重置为当日空桶。
// 核心功能：
//   - Upsert: 合并本轮 Pass 信号，仅交易型（做多/做空）入库存
//   - Invalidate: 标记信号失效（失效墓碑），当日该 key 不再允许固化
//   - IsInvalidated: 判断 code@strategy 今日是否已被失效墓碑标记
//   - List: 返回当日固化信号列表供聚合器展示
// English: pinned-signal store keyed by code@strategy; validates the trading day on load and
// automatically resets to an empty day-bucket when the day rolls over.
type signalStore struct {
	mu    sync.Mutex                                // 保护 byKey/invalidated 的互斥锁
	path  string                                    // 磁盘持久化路径（空=不落盘）
	byKey map[string]combat_agent.Signal            // code@strategy → 最近一次 Pass 信号
	// invalidated 失效墓碑集合：已被标记失效的 key 当日不允许重新固化（防"假信号复活"）。
	// 一旦标记，即使后续轮次重新产生同 key 信号也会被跳过。
	// English: tombstone set — invalidated keys can't be re-pinned for the rest of the day.
	invalidated map[string]bool
}

// newSignalStore 创建当日信号固化存储并从磁盘加载（仅保留当前交易日的数据）。
// English: creates the pinned-signal store and loads it from disk (keeps only the current day's data).
func newSignalStore(path string) *signalStore {
	s := &signalStore{path: path, byKey: make(map[string]combat_agent.Signal), invalidated: make(map[string]bool)}
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
	for _, k := range f.Invalidated {
		if k != "" {
			s.invalidated[k] = true
		}
	}
	return s
}

// Upsert 合并本轮 Pass 信号：仅交易型（做多/做空）入库存，按 code@strategy 覆盖；
// 新信号不早于已存信号时替换（刷新价格/理由）。提醒/止盈止损不入库存（消息中心已单独持久化）。
// 已被失效墓碑标记的 key 直接跳过，保证"失效即死、当日不复活"。
// English: merges this round's Passed signals — only trade signals (long/short) are stored, keyed by
// code@strategy; a signal replaces the stored one unless it is older (refreshing price/reason).
// Alerts (take-profit/stop-loss) are not stored here because the message center persists them separately.
// Keys marked by an invalidation tombstone are skipped so a dead signal can't resurrect that day.
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
		if s.invalidated[key] {
			continue
		}
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

// Invalidate 标记 code@strategy 的信号失效（失效墓碑）：删除固化信号并记录墓碑，
// 当日该 key 不再允许固化（防假信号复活），调用方应同时删除消息中心对应条目。
// English: marks a signal invalid (invalidation tombstone): removes the pinned signal and records the
// tombstone so that key can't be re-pinned for the day (blocking false-signal resurrection). The caller
// should also delete the matching message-center item.
func (s *signalStore) Invalidate(code, strategy string) {
	if s == nil || code == "" || strategy == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := code + "@" + strategy
	if _, ok := s.byKey[key]; !ok && s.invalidated[key] {
		return
	}
	delete(s.byKey, key)
	s.invalidated[key] = true
	s.save()
}

// IsInvalidated 判断 code@strategy 今日是否已被失效墓碑标记。
// English: reports whether code@strategy has been tombstoned for today.
func (s *signalStore) IsInvalidated(code, strategy string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.invalidated[code+"@"+strategy]
}

// List 返回当日固化信号列表（供聚合器展示用，键去重后的当前集合）。
// English: returns the day's pinned signals (deduplicated current set) for the dashboard aggregator.
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
// English: writes the day's pinned signals to disk (overwrite, marked with the trading day).
func (s *signalStore) save() {
	if s.path == "" {
		return
	}
	f := signalStoreFile{
		TradingDay:  data.TradingDayDate(time.Now()),
		Signals:     make([]combat_agent.Signal, 0, len(s.byKey)),
		Invalidated: make([]string, 0, len(s.invalidated)),
	}
	for _, sig := range s.byKey {
		f.Signals = append(f.Signals, sig)
	}
	for k := range s.invalidated {
		f.Invalidated = append(f.Invalidated, k)
	}
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return
	}
	// §E3 原子写：固化信号/墓碑是 autoPlace 幂等依据，截断=假信号复活可再次下单
	mustAtomicWrite("signals_today", s.path, raw)
}

// mergeSignals 合并当前轮信号与当日固化信号（固化集合在前，冲突由聚合器按 code 去重裁决）。
// English: merges the current round's signals with the day's pinned signals (pinned set first; the
// aggregator resolves per-code conflicts afterwards).
func mergeSignals(cur, persisted []combat_agent.Signal) []combat_agent.Signal {
	out := make([]combat_agent.Signal, 0, len(cur)+len(persisted))
	out = append(out, persisted...)
	out = append(out, cur...)
	return out
}
