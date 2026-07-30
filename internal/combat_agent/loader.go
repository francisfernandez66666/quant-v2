package combat_agent

import (
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

// StartHotReload 启动策略参数热加载（fsnotify）
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

func (a *Agent) reloadConfig(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		log.Printf("[combat_agent] 读取配置失败: %v", err)
		return
	}

	// 这里应该解析 strategies.yaml 到 StrategyConfig
	// 简化：暂时只记录日志，具体解析格式由后续完善
	log.Printf("[combat_agent] 配置文件已变更 (%d bytes), 等待实现完整解析", len(data))

	_ = data
}
