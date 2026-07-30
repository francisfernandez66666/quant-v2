// Package data — 自选股管理。提供自选股列表的增删查和 JSON 持久化。
package data

import (
	"encoding/json"
	"log"
	"os"
	"sync"
)

// WatchlistManager 自选股管理器，线程安全，持久化到 watchlist.json。
type WatchlistManager struct {
	mu   sync.RWMutex // 读写锁
	dir  string       // 数据目录
	list []string     // 自选股代码列表
}

// NewWatchlistManager 创建自选股管理器并加载已有数据。
func NewWatchlistManager(dir string) *WatchlistManager {
	w := &WatchlistManager{dir: dir}
	w.load()
	return w
}

// List 返回自选股列表副本（线程安全）。
func (w *WatchlistManager) List() []string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	out := make([]string, len(w.list))
	copy(out, w.list)
	return out
}

// Add 添加股票到自选列表（去重），返回 true 表示新增成功。
func (w *WatchlistManager) Add(code string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, c := range w.list {
		if c == code {
			return false
		}
	}
	w.list = append(w.list, code)
	w.save()
	return true
}

// Remove 从自选列表中移除指定股票，返回 true 表示移除成功。
func (w *WatchlistManager) Remove(code string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	for i, c := range w.list {
		if c == code {
			w.list = append(w.list[:i], w.list[i+1:]...)
			w.save()
			return true
		}
	}
	return false
}

// load 从 watchlist.json 读取自选股列表。
func (w *WatchlistManager) load() {
	data, err := os.ReadFile(w.dir + "/watchlist.json")
	if err != nil {
		return
	}
	json.Unmarshal(data, &w.list)
}

// save 将自选股列表写入 watchlist.json。
func (w *WatchlistManager) save() {
	data, _ := json.MarshalIndent(w.list, "", "  ")
	if err := os.WriteFile(w.dir+"/watchlist.json", data, 0644); err != nil {
		log.Printf("[watchlist] 保存失败: %v", err)
	}
}
