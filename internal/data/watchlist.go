// Package data — 自选股管理。提供自选股列表的增删查和 JSON 持久化。
// Package data — watchlist management: add/remove/query of a stock watchlist
// with JSON persistence.
package data

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"
)

// WatchlistManager 自选股管理器，线程安全，持久化到 JSON。
// 支持多账号隔离：每个账号一份文件 watchlist_{userID}.json；空 userID（未登录/系统级）
// 使用 watchlist.json 兼容原有单账号数据。
// WatchlistManager manages the watchlist thread-safely, persisting to JSON, with per-account
// isolation (watchlist_{userID}.json); empty userID falls back to watchlist.json for legacy data.
type WatchlistManager struct {
	mu  sync.RWMutex // 读写锁
	dir string       // 数据目录
}

// watchlistFile 返回指定账号的自选股文件路径。
// watchlistFile returns the watchlist file path for a user.
func (w *WatchlistManager) watchlistFile(userID string) string {
	if userID == "" {
		return w.dir + "/watchlist.json"
	}
	return w.dir + "/watchlist_" + userID + ".json"
}

// NewWatchlistManager 创建自选股管理器。
// NewWatchlistManager creates a manager.
func NewWatchlistManager(dir string) *WatchlistManager {
	return &WatchlistManager{dir: dir}
}

// List 返回指定账号的自选股列表副本（线程安全）。
// List returns a thread-safe copy of a user's watchlist.
func (w *WatchlistManager) List(userID string) []string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	var list []string
	if data, err := os.ReadFile(w.watchlistFile(userID)); err == nil {
		_ = json.Unmarshal(data, &list)
	}
	out := make([]string, len(list))
	copy(out, list)
	return out
}

// Add 添加股票到指定账号的自选列表（去重），返回 true 表示新增成功。
// Add appends a code (deduplicated) for a user; returns true if newly added.
func (w *WatchlistManager) Add(userID, code string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	var list []string
	if data, err := os.ReadFile(w.watchlistFile(userID)); err == nil {
		_ = json.Unmarshal(data, &list)
	}
	for _, c := range list {
		if c == code {
			return false
		}
	}
	list = append(list, code)
	w.save(w.watchlistFile(userID), list)
	return true
}

// Remove 从指定账号的自选列表中移除股票，返回 true 表示移除成功。
// Remove deletes a code from a user's watchlist; returns true if removed.
func (w *WatchlistManager) Remove(userID, code string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	var list []string
	if data, err := os.ReadFile(w.watchlistFile(userID)); err == nil {
		_ = json.Unmarshal(data, &list)
	}
	for i, c := range list {
		if c == code {
			list = append(list[:i], list[i+1:]...)
			w.save(w.watchlistFile(userID), list)
			return true
		}
	}
	return false
}

// save 将自选股列表写入指定文件。
// save writes the watchlist to the given file.
func (w *WatchlistManager) save(path string, list []string) {
	data, _ := json.MarshalIndent(list, "", "  ")
	if err := atomicWrite(path, data, 0644); err != nil {
		log.Printf("[watchlist] 保存失败: %v", err)
	}
}

// Delete §F-15：删号时删除该账号的自选文件（先备份并逐字节回验，备份不成立就不删）。
// 空 userID 必须拒绝——那会打到平台所有者自己的 watchlist.json。
// 返回 (是否真的删了文件, 备份路径, 错误)；文件本来就不存在时返回 (false,"",nil)。
// English: deletes a user's watchlist file after a byte-verified backup; empty userID is rejected
// because it would target the platform owner's legacy watchlist.json.
func (w *WatchlistManager) Delete(userID string) (bool, string, error) {
	if userID == "" {
		return false, "", fmt.Errorf("refuse to delete watchlist with empty user id")
	}
	if strings.ContainsAny(userID, `/\`) || strings.Contains(userID, "..") {
		return false, "", fmt.Errorf("invalid user id for watchlist delete")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	path := w.watchlistFile(userID)
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, "", nil
		}
		return false, "", err
	}
	backup := fmt.Sprintf("%s/deleted_watchlist_%s_%s.json", w.dir, userID, time.Now().Format("20060102_150405"))
	if err := atomicWrite(backup, raw, 0o600); err != nil {
		return false, "", fmt.Errorf("write watchlist backup: %w", err)
	}
	back, err := os.ReadFile(backup)
	if err != nil {
		return false, "", fmt.Errorf("read watchlist backup: %w", err)
	}
	if !bytes.Equal(back, raw) {
		return false, "", fmt.Errorf("watchlist backup verify failed")
	}
	if err := os.Remove(path); err != nil {
		return false, "", err
	}
	log.Printf("[watchlist] 删号清理自选 user=%s bytes=%d backup=%s", userID, len(raw), backup)
	return true, backup, nil
}

// All 返回全部账号自选股的并集（引擎打分/行情采集用）。
// 读取数据目录下所有 watchlist*.json 文件合并去重。
// All returns the union of all users' watchlists (used by the engine for scoring/quoting).
func (w *WatchlistManager) All() []string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	merged := map[string]bool{}
	entries, err := os.ReadDir(w.dir)
	if err == nil {
		for _, e := range entries {
			name := e.Name()
			if !strings.HasPrefix(name, "watchlist") || !strings.HasSuffix(name, ".json") {
				continue
			}
			if data, err := os.ReadFile(w.dir + "/" + name); err == nil {
				var list []string
				if json.Unmarshal(data, &list) == nil {
					for _, c := range list {
						merged[c] = true
					}
				}
			}
		}
	}
	// 兜底：确保至少加载默认文件（.All 可能找不到时返回空）
	out := make([]string, 0, len(merged))
	for c := range merged {
		out = append(out, c)
	}
	return out
}
