// 独立研究调度服务（quant-research systemd unit 入口）。
//
// 与量化主程序（cmd/quant）完全解耦，按时段切换调度自动研究：
//   - 交易时段：只跑 dataload 增量下载（绝不回测/研究），不争抢盘中 CPU；
//   - 盘后/周末：跑完整夜间研究作业（dataload → sector-rebuild → discover-factors
//     → discover-patterns → list）；
//   - 下一交易日盘前 8:30 自动终止遗留作业，CPU 交还量化主程序。
//
// 配置：从 QUANT_DATA_DIR/config.json 读取 rules.scheduler（每次调度 tick 重读，热生效）。
// 幂等状态：QUANT_DATA_DIR/research_state.json（断点续跑，跨天重置）。
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"quant-trading-v2/internal/scheduler"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	// 时区加固：A 股按北京时间（与 cmd/quant 双保险，服务器在海外也不偏移）。
	// English: force Asia/Shanghai so trading-session windows align with A-share hours even on
	// overseas hosts; an explicit TZ env var overrides the default.
	if os.Getenv("TZ") == "" {
		os.Setenv("TZ", "Asia/Shanghai")
		if loc, err := time.LoadLocation("Asia/Shanghai"); err == nil {
			time.Local = loc
		}
		log.Printf("[researchd] 进程时区已固定为 Asia/Shanghai (北京时间), 当前 %s",
			time.Now().Format("2006-01-02 15:04:05 -07:00"))
	}

	dataDir := os.Getenv("QUANT_DATA_DIR")
	if dataDir == "" {
		home, _ := os.UserHomeDir()
		dataDir = filepath.Join(home, ".quant-trading-v2")
	}
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		log.Printf("[researchd] 创建数据目录失败: %v", err)
	}
	log.Printf("[researchd] 数据目录: %s", dataDir)

	sch := scheduler.New(dataDir, "", "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 优雅停机：SIGTERM/SIGINT 取消调度循环（会一并 kill 正在运行的作业子进程）。
	stop := make(chan os.Signal, 2)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-stop
		log.Println("[researchd] 收到退出信号，正在停止研究调度…")
		sch.PreemptForShutdown() // §先标抢占再取消：运行任务落 preempted 断点续跑，不落 error
		cancel()
	}()

	log.Println("[researchd] quant-research 调度服务已启动")
	sch.Run(ctx)
}
