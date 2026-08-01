// Package combat_agent 战法引擎的配置热加载。
// loader.go 通过 fsnotify 监听策略配置文件变更，自动解析并热更新策略参数，
// 使策略权重/阈值调整无需重启进程即可生效。
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
// 入参 path 为 JSON 配置文件路径；路径为空或初始化失败时仅记日志并返回。
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

	// 后台 goroutine 常驻监听，defer 关闭 watcher
	go func() {
		defer watcher.Close()
		var debounce *time.Timer
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					// 通道关闭 → 退出监听
					return
				}
				// 仅响应写入/创建事件（忽略权限变更等噪音）
				if event.Op&(fsnotify.Write|fsnotify.Create) != 0 {
					// 500ms 防抖：连续保存只触发最后一次重载
					if debounce != nil {
						debounce.Stop()
					}
					debounce = time.AfterFunc(500*time.Millisecond, func() { a.reloadConfig(absPath) })
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
// 读取或解析失败仅记日志，不影响当前运行中的策略参数（安全降级）。
func (a *Agent) reloadConfig(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		log.Printf("[combat_agent] 读取配置失败: %v", err)
		return
	}

	// 只关心顶层 rules 段，取出其中 Strategy 配置
	var wrapper struct {
		Rules *config.Rules `json:"rules"`
	}
	if err := json.Unmarshal(data, &wrapper); err != nil {
		log.Printf("[combat_agent] 解析配置失败: %v", err)
		return
	}
	// rules 段缺失 → 无策略配置可更新
	if wrapper.Rules == nil {
		return
	}

	// 热更新策略参数（线程安全，由 Agent.HotReload 加锁写入）
	a.HotReload(&wrapper.Rules.Strategy)
}
