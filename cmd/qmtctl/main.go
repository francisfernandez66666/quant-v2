// qmtctl —— MiniQMT 时段性启停控制器（广州单机全合一迁移方案）。
//
// 与引擎共用 internal/data 的会话/交易日历模型（同花顺交易日历，含节假日/临时休市），
// 决定当前是否应运行 MiniQMT：交易日 08:45~15:05 运行，其余时段（含周末/节假日）关闭。
// 用途：交易时段 MiniQMT 在线承接下单；非交易时段关闭释放 ~1G 内存给 researchd 夜间研究，
// 实现"时间分片硬互斥"（见 docs/MIGRATION_GUANGZHOU_ALLINONE.md §1/§3.3）。
//
// 子命令：
//
//	qmtctl ensure-miniqmt   按当前时段自决策：应运行且未运行→启动；不应运行且运行→taskkill
//	qmtctl status           仅报告 MiniQMT 进程是否存在（+ 可选 -gateway-url 查 broker 状态）
//	qmtctl check            打印"当前应运行? / 进程在? / 决策"用于人工排查
//	qmtctl readiness        交易日 09:00 后且应运行但 broker 未就绪 → 退出码 2（供告警任务判断）
//
// 注意：MiniQMT 必须运行在交互登录会话（GUI 客户端），故 qmtctl 由"仅在用户登录时运行"
// 的 Windows 任务计划触发；请勿用 NSSM/SYSTEM 会话拉起（会导致无法完成券商登录）。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	_ "time/tzdata"

	"quant-trading-v2/internal/data"
)

const miniQMTImage = "XtMiniQmt.exe"

// 运行窗口：交易日 08:45（给 30 分钟登录+行情就绪到 9:15）~ 15:05（收盘后留 5 分钟缓冲再关）。
func shouldRunWindow(hm int) bool {
	return hm >= 8*60+45 && hm < 15*60+5
}

// main 时段性启停控制器入口：解析全局 flags 与子命令，加载交易日历，按当前时段
// 自决策 MiniQMT 的启停（ensure-miniqmt / status / check / readiness 四子命令），
// 实现交易时段运行、非交易时段关闭以释放内存给 researchd 的时间分片硬互斥。
func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	// 时区加固：与引擎一致，A 股按北京时间（即便宿主机为 KST/UTC 也不偏移）。
	if os.Getenv("TZ") == "" {
		os.Setenv("TZ", "Asia/Shanghai")
		if loc, err := time.LoadLocation("Asia/Shanghai"); err == nil {
			time.Local = loc
		}
	}

	var (
		miniPath             = flag.String("path", defaultMiniPath(), "XtMiniQmt.exe 完整路径")
		gatewayURL           = flag.String("gateway-url", "", "qmt_gateway /state 地址（status/readiness 用）")
		gatewayTok           = flag.String("token", os.Getenv("QMT_TOKEN"), "qmt_gateway Bearer token")
		dryRun               = flag.Bool("dry", false, "只打印决策不实际启停")
		startHolidayFallback = flag.Bool("weekday-fallback", true, "交易日历未加载时按周末口径兜底（周一到周五视为交易日）")
	)
	flag.Parse()

	cmd := flag.Arg(0)
	if cmd == "" {
		cmd = "ensure-miniqmt"
	}

	// 尝试加载交易日历（与引擎同口径）。失败仅记日志，按 weekday-fallback 兜底。
	if err := data.RefreshTradingCalendar(); err != nil {
		log.Printf("[qmtctl] 交易日历加载失败（兜底=%v）: %v", *startHolidayFallback, err)
	}

	now := time.Now()
	run := computeShouldRun(now, *startHolidayFallback)
	running := isProcessRunning(miniQMTImage)

	switch cmd {
	case "ensure-miniqmt":
		act := decide(run, running)
		if *dryRun {
			log.Printf("[qmtctl] dry-run 决策=%s (应运行=%v 进程在=%v)", act, run, running)
			return
		}
		switch act {
		case "start":
			if err := startMiniQMT(*miniPath); err != nil {
				log.Printf("[qmtctl] 启动失败: %v", err)
				os.Exit(1)
			}
			log.Printf("[qmtctl] 已启动 %s", *miniPath)
		case "stop":
			if err := stopMiniQMT(); err != nil {
				log.Printf("[qmtctl] 停止失败: %v", err)
				os.Exit(1)
			}
			log.Printf("[qmtctl] 已停止 %s", miniQMTImage)
		default:
			log.Printf("[qmtctl] 无需操作 (应运行=%v 进程在=%v)", run, running)
		}
	case "status":
		connected := ""
		if *gatewayURL != "" {
			connected = fmt.Sprintf(" broker_connected=%v", queryBrokerConnected(*gatewayURL, *gatewayTok))
		}
		fmt.Printf("running=%v image=%s%s\n", running, miniQMTImage, connected)
	case "check":
		fmt.Printf("now=%s should_run=%v process_running=%v decision=%s\n",
			now.Format("2006-01-02 15:04:05 -07:00"), run, running, decide(run, running))
	case "readiness":
		// 仅交易日 09:00 后、应运行但进程不在 / broker 未就绪 → 退出码 2（供告警任务判断）。
		hm := now.Hour()*60 + now.Minute()
		if !run {
			fmt.Println("ok: 非运行窗口")
			return
		}
		if hm < 9*60 {
			fmt.Println("ok: 尚未到 09:00 就绪检查点")
			return
		}
		if !running {
			fmt.Println("ALERT: 应运行但 MiniQMT 进程不在")
			os.Exit(2)
		}
		if *gatewayURL != "" && !queryBrokerConnected(*gatewayURL, *gatewayTok) {
			fmt.Println("ALERT: 应运行但 broker 未连接（QMT 登录/行情未就绪）")
			os.Exit(2)
		}
		fmt.Println("ok: MiniQMT 就绪")
	default:
		log.Fatalf("未知子命令: %s", cmd)
	}
}

// computeShouldRun 当前是否应运行 MiniQMT。
func computeShouldRun(now time.Time, weekdayFallback bool) bool {
	// IsTradingDay 已消费交易日历；未加载时恒 false(周末口径) → 这里按兜底策略：
	// 日历未加载时，把"工作日(Mon-Fri)"视为交易日，避免误关真交易日（宁可节日多开，不可交易日漏开）。
	if data.IsTradingDay(now) {
		return shouldRunWindow(now.Hour()*60 + now.Minute())
	}
	if weekdayFallback {
		w := now.Weekday()
		if w >= time.Monday && w <= time.Friday {
			return shouldRunWindow(now.Hour()*60 + now.Minute())
		}
	}
	return false
}

// decide 纯函数决策。
func decide(shouldRun, running bool) string {
	switch {
	case shouldRun && !running:
		return "start"
	case !shouldRun && running:
		return "stop"
	default:
		return "noop"
	}
}

// isProcessRunning 通过 tasklist 判断 MiniQMT 进程是否存在。
func isProcessRunning(image string) bool {
	out, err := exec.Command("tasklist", "/FI", "IMAGENAME eq "+image, "/NH").CombinedOutput()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), image)
}

// startMiniQMT 在交互会话中启动 MiniQMT（DETACHED，qmtctl 退出后继续存活）。
func startMiniQMT(path string) error {
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("MiniQMT 路径不存在: %s", path)
	}
	cmd := exec.Command(path)
	setDetached(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	// 不 Wait：交由 MiniQMT 自身常驻 + QMT-Daily-Restart/看门狗看护
	return nil
}

// stopMiniQMT 树杀 MiniQMT 进程（含孙进程）。
func stopMiniQMT() error {
	out, err := exec.Command("taskkill", "/T", "/F", "/IM", miniQMTImage).CombinedOutput()
	if err != nil {
		// 已不存在视为成功
		if !isProcessRunning(miniQMTImage) {
			return nil
		}
		return fmt.Errorf("taskkill 失败: %v (%s)", err, string(out))
	}
	return nil
}

// queryBrokerConnected 查询 qmt_gateway /health 的 broker_connected 字段（免鉴权，本机 127.0.0.1 访问）。
func queryBrokerConnected(url, token string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var st struct {
		BrokerConnected bool `json:"broker_connected"`
	}
	if err := json.Unmarshal(body, &st); err != nil {
		return false
	}
	return st.BrokerConnected
}

// defaultMiniPath 常见安装位置探测（可被子命令 -path 覆盖）。
// 先匹配常见精确路径；未命中则在若干安装根目录做有界递归搜索，避免把中文安装路径硬编码进来。
func defaultMiniPath() string {
	cands := []string{
		`C:\QMT\userdata_mini\bin\XtMiniQmt.exe`,
		`D:\QMT\userdata_mini\bin\XtMiniQmt.exe`,
		`C:\Program Files\QMT\userdata_mini\bin\XtMiniQmt.exe`,
		`C:\Program Files (x86)\QMT\userdata_mini\bin\XtMiniQmt.exe`,
	}
	for _, c := range cands {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	roots := []string{
		`C:\Program Files (x86)`,
		`C:\Program Files`,
		`C:\QMT`,
		`D:\QMT`,
	}
	for _, root := range roots {
		if _, err := os.Stat(root); err != nil {
			continue
		}
		if f := searchFile(root, miniQMTImage, 6); f != "" {
			return f
		}
	}
	return `C:\QMT\userdata_mini\bin\XtMiniQmt.exe`
}

// searchFile 在 root 下做深度受限的递归搜索，找到第一个名为 name 的文件即返回其完整路径。
func searchFile(root, name string, maxDepth int) string {
	var result string
	rootDepth := strings.Count(root, `\`)
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			if strings.Count(path, `\`)-rootDepth > maxDepth {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.EqualFold(info.Name(), name) {
			result = path
			return errors.New("found")
		}
		return nil
	})
	return result
}
