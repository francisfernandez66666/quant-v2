// 文件：w4c_tick_panic_test.go
// 职责：§0926E2E-15（FIX_PLAN_20260926E2E 四波 15 项）调度 tick panic 隔离与归因可见性。
//
// 旧行为：Scheduler.tick 体内任一腿 panic 直接把 researchd 进程崩掉——归因面上只剩
// "服务莫名重启"（看门狗冷启），panic 现场无日志、无 opslog 审计行、无对外计数。
// 新行为：tick 外套 recover 壳 → 服务日志全栈 + opslog 一行 + tickPanics 计数
// （随 scheduler_status.json 快照可见）+ 告警回调报一次；本轮作废，下一轮照常。
//
// 注入缝：测试时钟 nowFn（既有 setNow 缝）——让 s.nowTime() 直接 panic，命中
// tickInner 主体，无需真实炸点。反证链：recover 失效则本用例会以进程崩溃形态红。
package scheduler

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/opslog"
	"quant-trading-v2/internal/store"
)

func TestW4CTickPanicIsolation(t *testing.T) {
	dir := t.TempDir()
	cfgPath := mustConfig(t, cfgSamples(filepath.Join(dir, "noop.sh"), filepath.Join(dir, "trading.db")))
	s := New(dir, cfgPath, filepath.Join(dir, "research_state.json"))
	loc := time.FixedZone("CST", 8*3600)
	normal := func() time.Time { return time.Date(2026, 8, 22, 16, 0, 0, 0, loc) } // 周六盘后
	s.setNow(normal)

	// opslog 指到独立临时目录：断言审计行真实落文件（不靠"以为写了"）。
	oDir := t.TempDir()
	opslog.Init(oDir, 0)

	var alerts []string
	s.SetAlertFunc(func(title, content string) { alerts = append(alerts, title+"|"+content) })

	// ① 注入必炸时钟后 tick：进程不得崩（recover 失效=本测试进程当场红）。
	s.setNow(func() time.Time { panic("测试注入：tick 内某腿炸裂") })
	s.tick()

	if got := s.tickPanics.Load(); got != 1 {
		t.Fatalf("§0926E2E-15 panic 应计数 1 次，实际 tickPanics=%d", got)
	}
	if len(alerts) != 1 || !strings.Contains(alerts[0], "调度 tick panic 隔离") {
		t.Fatalf("§0926E2E-15 告警回调应收到 1 次隔离通报，实际 %q", alerts)
	}
	// opslog 审计行必须真实落盘（当日文件唯一，扫目录取 opslog-*）
	files, err := filepath.Glob(filepath.Join(oDir, "opslog-*.log"))
	if err != nil || len(files) == 0 {
		t.Fatalf("§0926E2E-15 opslog 应已落文件，glob=%v err=%v", files, err)
	}
	var joined strings.Builder
	for _, f := range files {
		raw, rerr := os.ReadFile(f)
		if rerr == nil {
			joined.Write(raw)
		}
	}
	// 审计行文案与 scheduler.go 的 opslog.Logf 格式逐字对齐（改版须同步本锁）
	if !strings.Contains(joined.String(), "调度 tick panic 隔离") || !strings.Contains(joined.String(), "#1：") {
		t.Fatalf("§0926E2E-15 opslog 缺隔离审计行，内容：%s", joined.String())
	}

	// ② 恢复常规时钟再 tick：调度循环活着、计数保留并随状态快照对外可见。
	// §0926E2E 全量跑批锤出的测试卫生修正：夜间链入队改走 §M15 测试缝的**空入队桩**——
	// 本用例断言的是 tick 壳的存活与归因可见性，不测夜链执行本身；周六 16:00 的常规轮
	// 若真实入队，后台排水 goroutine（无停机缝）会拿 noop.sh 缺失走"失败→回队尾重试"
	// 的无限循环，与 t.TempDir 清退抢目录（实测 TempDir RemoveAll: directory not empty 判红）。
	s.nightlyEnqueueOverride = func(t *store.ResearchTask) (int64, error) { return 0, nil }
	s.setNow(normal)
	s.tick()
	if got := s.tickPanics.Load(); got != 1 {
		t.Fatalf("正常轮不得改动 panic 计数，实际 %d", got)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "scheduler_status.json"))
	if err != nil {
		t.Fatalf("正常 tick 应落 scheduler_status.json 快照: %v", err)
	}
	var st struct {
		TickPanics int64 `json:"tick_panics"`
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatalf("状态快照解析失败: %v (%s)", err, raw)
	}
	if st.TickPanics != 1 {
		t.Fatalf("§0926E2E-15 状态快照应带 tick_panics=1，得到 %s", raw)
	}
	// 告警不重复：第二次 tick 无 panic，alerts 仍为 1 条
	if len(alerts) != 1 {
		t.Fatalf("正常轮不得再发隔离告警，alerts=%q", alerts)
	}
}
