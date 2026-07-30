// Package data — 自选股持久化管理。
// 自选股列表以 JSON 文件存储于日志目录下，支持运行时增删。
package data

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// WatchlistManager 自选股管理器。
// 线程安全，支持加载/保存/增删；文件格式为 JSON 字符串数组。
type WatchlistManager struct {
	mu     sync.RWMutex
	path   string   // 自选股 JSON 文件路径
	stocks []string // 自选股代码列表
}

// NewWatchlistManager 创建自选股管理器。
// logDir 为日志目录，自选股文件名为 custom_watchlist.json。
func NewWatchlistManager(logDir string) *WatchlistManager {
	return &WatchlistManager{
		path:   filepath.Join(logDir, "custom_watchlist.json"),
		stocks: []string{},
	}
}

// Load 从磁盘加载自选股列表。文件不存在时返回空列表而非错误。
func (wm *WatchlistManager) Load() error {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	data, err := os.ReadFile(wm.path)
	if err != nil {
		if os.IsNotExist(err) {
			wm.stocks = []string{}
			return nil
		}
		return err
	}
	return json.Unmarshal(data, &wm.stocks)
}

// Save 将当前自选股列表写入磁盘。
func (wm *WatchlistManager) Save() error {
	data, err := json.Marshal(wm.stocks)
	if err != nil {
		return err
	}
	return os.WriteFile(wm.path, data, 0644)
}

// List 返回自选股列表的副本。
func (wm *WatchlistManager) List() []string {
	wm.mu.RLock()
	defer wm.mu.RUnlock()
	out := make([]string, len(wm.stocks))
	copy(out, wm.stocks)
	return out
}

// Add 添加一只股票到自选股。已存在时直接返回 nil。
func (wm *WatchlistManager) Add(code string) error {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	for _, c := range wm.stocks {
		if c == code {
			return nil
		}
	}
	wm.stocks = append(wm.stocks, code)
	return wm.save()
}

// Remove 从自选股中移除一只股票。不存在时直接返回 nil。
func (wm *WatchlistManager) Remove(code string) error {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	idx := -1
	for i, c := range wm.stocks {
		if c == code {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil
	}
	wm.stocks = append(wm.stocks[:idx], wm.stocks[idx+1:]...)
	return wm.save()
}

// save 内部方法：将 stocks 序列化并写入磁盘文件。
func (wm *WatchlistManager) save() error {
	data, err := json.Marshal(wm.stocks)
	if err != nil {
		return err
	}
	return os.WriteFile(wm.path, data, 0644)
}
