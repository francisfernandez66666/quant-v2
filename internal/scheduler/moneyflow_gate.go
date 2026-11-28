// moneyflow_gate.go — §REPLAY-INPUTS 2026-11-28（档 3b）：夜间链等个股资金流采集完成标记。
//
// 为什么需要这道闸（owner 2026-11-28 裁决）：个股资金流是**长期日频数据**，
// 采集器 15:35 起一轮（约 25–40 分钟，派生自现网 sector-map 同量级实测锚点）。
// 如果夜间链在采集之前就把 `discover_factors / backtest` 排出去，
// 这轮回测量出的 `fac_175/179` 用的就是**昨天的资金流末行**——
// 数字看着正常，实际是"每天少一天"的稳定偏差，而这类偏差在读数上完全看不出来。
//
// 三条边界（都写成用例钉住，见 moneyflow_gate_test.go）：
//  1. **只推迟、不改任务状态**：命中拦截时既不 Defer 也不 Error，下一轮 tick 再判一次。
//     写成 defer 会把"采集慢半小时"记成"任务顺延一夜"，统计与队列语义全乱。
//  2. **必须有兜底时限**：方向是"推迟研究"，误判一次的代价是整晚不跑。
//     超过 `config.MoneyflowGateGraceHHMM` 就照常放行并留痕——保险丝不能比被保护的电路还脆。
//  3. **非交易日直接放行**：采集器 timer 是 Mon..Fri，节假日根本没有标记可读。
//     漏了这条，本批会自己造出"每个节假日夜间链白等到 17:00"的假红
//     （§CAL-GATE 那批"节假日被当禁止窗口"的同型缺陷，方向反过来）。
//
// ★ 刻意**不碰 `NightlyEligible`**：那条判据在 §MUTUAL-BACKOFF-20261112 被特意去钟点化
//
//	（"门控完全由系统会话模型驱动，不含任何写死钟点"），本批的钟点只作用在
//	"资金流消费步骤的出队"这一件小事上，塞进总闸会让那次改判作废。
//
// English: the nightly chain waits for today's money-flow collection marker before dequeuing
// flow-consuming steps — deferral only (no state change), with a grace deadline, skipped on
// non-trading days, and deliberately kept out of NightlyEligible's session-driven gate.
package scheduler

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/opslog"
	"quant-trading-v2/internal/store"
)

// moneyflowRun 与 cmd/backfill_stock_moneyflow 的 runMarker **跨包同名同键**。
//
// 两边不共享结构体（一个是采集进程、一个是调度器，硬共享会把 cmd 包拖进 scheduler 依赖），
// 靠 JSON 键对齐——所以这里的每个 tag 都必须与采集侧逐字相同，
// 改名必须两侧同批改（§跨侧契约锁形：两侧各写一份、只有真跑才炸）。
// 读不到的字段留零值并如实反映在 status=""（＝"没有今天的结论"），不猜。
type moneyflowRun struct {
	Day         string  `json:"day"`
	Mode        string  `json:"mode"`
	Status      string  `json:"status"`
	CodesTotal  int     `json:"codes_total"`
	CodesOK     int     `json:"codes_ok"`
	CoveragePct float64 `json:"coverage_pct"`
	ElapsedSec  float64 `json:"elapsed_sec"`
	FinishedAt  string  `json:"finished_at"`
}

// moneyflowConsumes 判定某任务类型是否吃个股资金流。
//
// 白名单式（默认放行）而不是黑名单式：漏登记一条"吃资金流的步骤"，
// 后果只是那条步骤早跑半小时（数据旧一天）；而漏登记一条"不吃资金流的步骤"，
// 后果是 dataload/lifecycle 这些一律被拖到 17:00——后者的破坏面大得多。
// 新步骤若开始读 stock_moneyflow_daily，必须同批登记进来（门禁 §261 ⑧ 数这份清单）。
func moneyflowConsumes(kind string) bool {
	switch kind {
	// 注意 `optimize` 步骤映射到的也是 TaskBacktestStrategy（worker.go 的 stepTask），
	// 所以这里不需要单独的"optimize"常量——按任务类型判，不按步骤名判，
	// 否则将来同一个类型被两条步骤复用时又会漏判。
	case store.TaskDiscoverFactors, store.TaskBacktestNightly,
		store.TaskBacktestStrategy, store.TaskBacktestCandidate,
		store.TaskPaperResearch:
		return true
	}
	return false
}

// flowGateAllows 判断某条待出队任务现在能不能跑。
//
// 返回 (allow=true, reason) 时 reason 只作留痕；allow=false 表示"本轮不出队"，
// 调用方必须**直接 return**，不动任务状态（边界 1）。
//
// 判定顺序（每一步都是"能少等就少等"）：
//  1. 总开关关掉（显式 false）⇒ 放行，reason=moneyflow_gate_off_by_switch；
//  2. 不吃资金流的步骤 ⇒ 放行（dataload/minute_sync/sector_rebuild/lifecycle/list 一律不等）；
//  3. 非交易日 ⇒ 放行（采集器今天根本不跑）；
//  4. 手动 high 优先级 ⇒ 放行（运维通道不能被自己新加的闸封死，同 §NIGHTCAP 对 high 的取向）；
//  5. 标记 day==今日 ⇒ 放行（**无论 status 是什么**，见文件头边界 2）；
//  6. 过兜底时限 ⇒ 放行并留痕 timeout；否则拦下。
func (s *Scheduler) flowGateAllows(next *store.ResearchTask, cfg config.SchedulerConfig, now time.Time) (bool, string) {
	if next == nil {
		return true, ""
	}
	if !cfg.MoneyflowGateOn() {
		return true, "moneyflow_gate_off_by_switch"
	}
	if !moneyflowConsumes(next.Type) {
		return true, ""
	}
	if !data.IsTradingDay(now) {
		return true, ""
	}
	if next.Priority != "" && next.Priority != "low" {
		return true, "moneyflow_gate_high_exempt"
	}
	today := cntime.DayCompactOf(now)
	run, ok := readMoneyflowRun(dirOfDB(cfg))
	if ok && run.Day == today {
		// 有今天的结论就放行——哪怕那轮是 partial/relay_unavailable。
		// 等"成功"会把一次上游抖动放大成整晚研究停摆；采集失败的处置在告警与读数那一侧。
		return true, ""
	}
	if pastGrace(now) {
		return true, fmt.Sprintf("moneyflow_gate_timeout marker_day=%s marker_status=%s", runMarkerDay(run, ok), runMarkerStatus(run, ok))
	}
	return false, fmt.Sprintf("moneyflow_gate_wait marker_day=%s marker_status=%s", runMarkerDay(run, ok), runMarkerStatus(run, ok))
}

// MoneyflowGateReport 给回放报告头/运维读数算这道闸**此刻**的结论：pass / wait / timeout / none。
//
// 公式只有这一份：tick 侧的 flowGateAllows 与本函数共用 readMoneyflowRun / pastGrace，
// 所以"报告头写着 wait、调度器其实放行了"这种自相矛盾不可能出现。
// 非交易日这道闸本就不生效（flowGateAllows 第一支直接放行），这里同样返回 pass；
// dbDir 为空（拿不到数据目录）返回 none——**不拿 pass 顶班**，"没这道闸"与"这道闸放行"不是一件事。
func MoneyflowGateReport(dbDir string, now time.Time) string {
	if strings.TrimSpace(dbDir) == "" {
		return "none"
	}
	if !data.IsTradingDay(now) {
		return "pass"
	}
	run, ok := readMoneyflowRun(dbDir)
	if ok && run.Day == cntime.DayCompactOf(now) {
		return "pass"
	}
	if pastGrace(now) {
		return "timeout"
	}
	return "wait"
}

// readMoneyflowRun 读标记；文件缺失/JSON 半截/字段缺 ⇒ (零值, false)。// 零值一律按"今天没有结论"处理，绝不按"结论是好的"处理（fail-closed）。
func readMoneyflowRun(dbDir string) (moneyflowRun, bool) {
	if dbDir == "" {
		return moneyflowRun{}, false
	}
	b, err := os.ReadFile(filepath.Join(dbDir, "moneyflow_run.json"))
	if err != nil {
		return moneyflowRun{}, false
	}
	var run moneyflowRun
	if err := json.Unmarshal(b, &run); err != nil {
		return moneyflowRun{}, false
	}
	if strings.TrimSpace(run.Day) == "" {
		return run, false
	}
	return run, true
}

// pastGrace 是否已过兜底时限（默认 17:00，来自 config 常量而不是内联字面量）。
func pastGrace(now time.Time) bool {
	h, m, ok := parseHHMM(config.MoneyflowGateGraceHHMM)
	if !ok {
		// 常量配错时**放行**而不是拦死：这道闸的默认责任是"不要拖累研究整晚"。
		return true
	}
	t := cntime.In(now)
	return t.Hour()*60+t.Minute() >= h*60+m
}

// parseHHMM 解析 "17:00"；形状不对返回 ok=false（由 pastGrace 的放行策略兜住）。
func parseHHMM(v string) (int, int, bool) {
	parts := strings.Split(strings.TrimSpace(v), ":")
	if len(parts) != 2 {
		return 0, 0, false
	}
	var h, m int
	if _, err := fmt.Sscanf(parts[0], "%d", &h); err != nil {
		return 0, 0, false
	}
	if _, err := fmt.Sscanf(parts[1], "%d", &m); err != nil {
		return 0, 0, false
	}
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, 0, false
	}
	return h, m, true
}

func runMarkerDay(run moneyflowRun, ok bool) string {
	if !ok {
		return "none"
	}
	return run.Day
}

func runMarkerStatus(run moneyflowRun, ok bool) string {
	if !ok {
		return "none"
	}
	if strings.TrimSpace(run.Status) == "" {
		return "unknown"
	}
	return run.Status
}

// noteFlowGateWait 只在"同一原因连续出现"时打一条日志（每 30s 一次 tick 会重复命中，
// 全量刷日志会把真正有用的行埋掉——§_note_skip 的同款取向）。
func (s *Scheduler) noteFlowGateWait(taskID int64, kind, reason string) {
	key := fmt.Sprintf("%d|%s", taskID, reason)
	s.mu.Lock()
	if s.flowGateNoted == nil {
		s.flowGateNoted = map[string]bool{}
	}
	fresh := !s.flowGateNoted[key]
	if fresh {
		s.flowGateNoted[key] = true
	}
	s.mu.Unlock()
	if !fresh {
		return
	}
	log.Printf("[scheduler] §MONEYFLOW-GATE 任务 #%d(%s) 暂不出队：%s", taskID, kind, reason)
	opslog.Logf("research", "§MONEYFLOW-GATE 任务 #%d(%s) 等个股资金流采集：%s", taskID, kind, reason)
}
