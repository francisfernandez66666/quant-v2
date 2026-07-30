package combat_agent

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
	"quant-trading-v2/internal/config"
)

// StartHotReload 启动配置文件热加载，监听文件变化自动重载策略参数。
// 使用 fsnotify 监控文件写入/创建事件，500ms 防抖后执行重载。
func (a *Agent) StartHotReload(path string) {
	if path == "" {
		return
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		log.Printf("[combat_agent] 热加载路径无效: %v", err)
		return
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("[combat_agent] fsnotify创建失败: %v", err)
		return
	}

	if err := watcher.Add(absPath); err != nil {
		log.Printf("[combat_agent] 监听失败: %v", err)
		watcher.Close()
		return
	}

	go func() {
		defer watcher.Close()
		var debounce time.Timer
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				if event.Op&(fsnotify.Write|fsnotify.Create) != 0 {
					if debounce.Stop() {
						select {
						case <-debounce.C:
						default:
						}
					}
					debounce = *time.NewTimer(500 * time.Millisecond)
					go func() {
						<-debounce.C
						a.reloadConfig(absPath)
					}()
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				log.Printf("[combat_agent] fsnotify错误: %v", err)
			}
		}
	}()

	log.Printf("[combat_agent] 热加载已启动: %s", absPath)
}

// reloadConfig 读取并解析 JSON 配置文件，提取策略规则后热更新。
func (a *Agent) reloadConfig(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		log.Printf("[combat_agent] 读取配置失败: %v", err)
		return
	}

	var wrapper struct {
		Rules *config.Rules `json:"rules"`
	}
	if err := json.Unmarshal(data, &wrapper); err != nil {
		log.Printf("[combat_agent] 解析配置失败: %v", err)
		return
	}
	if wrapper.Rules == nil {
		return
	}

	a.HotReload(&wrapper.Rules.Strategy)
}
