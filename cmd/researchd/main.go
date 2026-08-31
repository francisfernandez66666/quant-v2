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
	_ "time/tzdata" // §TZ1 内嵌 IANA 时区库：Windows/精简容器保证 Asia/Shanghai 可加载

	"quant-trading-v2/internal/opslog"
	"quant-trading-v2/internal/scheduler"
)

// main 研究调度服务入口：固定进程时区为 Asia/Shanghai，确定数据目录，启动 scheduler 调度循环，
// 并在收到 SIGTERM/SIGINT 时优雅停机（先抢占遗留作业再取消，保证断点续跑）。
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

	// §DAILY_OPSLOG 每日系统运行日志：research 侧与 quant 共写同目录（按日核心记录）。
	opslog.Init(dataDir, 0)
	opslog.Logf("research", "研究调度服务启动 dataDir=%s tz=%s", dataDir, time.Local.String())

	sch := scheduler.New(dataDir, "", "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 优雅停机：SIGTERM/SIGINT 取消调度循环（会一并 kill 正在运行的作业子进程）。
	stop := make(chan os.Signal, 2)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-stop
		log.Println("[researchd] 收到退出信号，正在停止研究调度…")
		opslog.Logf("research", "收到退出信号，停止调度（运行任务标抢占续跑）")
		sch.PreemptForShutdown() // §先标抢占再取消：运行任务落 preempted 断点续跑，不落 error
		cancel()
	}()

	log.Println("[researchd] quant-research 调度服务已启动")
	sch.Run(ctx)
}
