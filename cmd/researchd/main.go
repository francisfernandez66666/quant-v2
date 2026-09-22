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

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/notify"
	"quant-trading-v2/internal/opslog"
	"quant-trading-v2/internal/scheduler"
	"quant-trading-v2/internal/store" // §ADJ P0-A：启动时经唯一入口装配数据源路由
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
	opslog.Init(filepath.Join(dataDir, "opslog"), 0)
	opslog.Logf("research", "研究调度服务启动 dataDir=%s tz=%s", dataDir, time.Local.String())

	// §数据源路由装配（§ADJ P0-A 三轮补强）：启动即按唯一入口 store.ConfigureSourceFromFile
	// 装配一次（scheduler 每个 tick 仍会重读 rules.data 以支持热生效，两者是同一条入口、
	// 同一套语义，不存在第二处赋值）。这样在任何 worker 拉起【之前】路由已确定，
	// researchd 全生命周期与 quant/手工工具同口径。
	if err := store.ConfigureSourceFromFile(filepath.Join(dataDir, "config.json")); err != nil {
		log.Printf("[researchd] 数据源路由按默认装配（旧表 baostock），继续启动: %v", err)
	}

	sch := scheduler.New(dataDir, "", "")

	// §M14（2026-09-22）同因连败熔断告警接线：researchd 此前无任何推送通道（PushGateway
	// 生产仅 quant 侧在用），任务被熔断挂起若只落 opslog 等于静默死亡。现按 config.json
	// notify 段装配推送网关（APK 极光/webhook + ntfy 运维独立通道），经 SetAlertFunc 注入
	// 调度器——熔断触发时以 LevelHigh 高优推送一次（静默时段亦放行）。
	notifier := notify.New()
	if nc := config.NewManager(filepath.Join(dataDir, "config.json")).GetNotifyConfig(); nc != nil {
		if nc.Push.Enabled {
			if nc.Push.Provider == "jpush" {
				notifier.SetGateway(notify.NewJPushGateway(nc.Push.AppKey, nc.Push.Secret, nc.Push.Alias))
			} else if nc.Push.URL != "" {
				notifier.SetGateway(notify.NewWebhookGateway(nc.Push.URL))
			}
		}
		if gw := notify.NewNtfyGateway(nc.NtfyURL, nc.NtfyTopic); gw != nil {
			notifier.SetNtfy(gw)
		}
	}
	sch.SetAlertFunc(func(title, content string) {
		notifier.PushGateway(notify.Message{Level: notify.LevelHigh, Title: title, Content: content})
	})
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
