// Package scheduler 研究调度器：按交易时段/盘后/周末调度 dataload、回测、因子挖掘等研究任务，管理子进程生命周期。
package scheduler

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/config"
)

// mustConfig 写一个临时 config.json（含 rules.scheduler）并返回路径。
func mustConfig(t *testing.T, sc config.SchedulerConfig) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	b, err := json.Marshal(sc)
	if err != nil {
		t.Fatalf("序列化配置失败: %v", err)
	}
	raw := `{"rules":{"scheduler":` + string(b) + `}}`
	if err := os.WriteFile(path, []byte(raw), 0644); err != nil {
		t.Fatalf("写配置失败: %v", err)
	}
	return path
}

// cfgSamples 供各测试复用的默认配置。
func cfgSamples(fakeBin, db string) config.SchedulerConfig {
	return config.SchedulerConfig{
		Enabled:         true,
		ResearchBin:     fakeBin,
		DataloadBin:     fakeBin,
		DB:              db,
		PrimarySource:   "baostock", // 测试默认走旧表，不受数据源路由影响
		OptimizeEnabled: false,      // 测试不含寻优步骤（有专门的测试覆盖）
		Nightly: config.NightlyConfig{
			StartHHMM:        1530,
			WeekendStartHHMM: 1530,
			Steps:            []string{"dataload"},
			AbortOnError:     false,
		},
		DataloadDuringTrade: config.DataloadDuringTradeConfig{Enabled: false, IntervalMinutes: 30},
	}
}

// TestNightlyEligible NightlyEligible。
func TestNightlyEligible(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	cfg := cfgSamples("fake", "/tmp/x.db")

	// 统一口径（用户约定）：仅交易日交易窗口（9:15~收盘，含午休）禁止；其余全放行。
	// English: unified rule — blocked only inside the trading-day window (9:15 to close, lunch incl.);
	// everything else (nights, pre-open, weekends) is eligible.
	cases := []struct {
		t    time.Time
		want bool
		note string
	}{
		{time.Date(2026, 8, 18, 16, 0, 0, 0, loc), true, "周二16:00 盘后"},
		{time.Date(2026, 8, 18, 10, 0, 0, 0, loc), false, "周二10:00 上午盘"},
		{time.Date(2026, 8, 18, 15, 5, 0, 0, loc), true, "周二15:05 收盘即放行"},
		// §W4-a 盘前门：8:30~9:15 属 SessionPreMarket，改为禁止（兑现"8:30 终止遗留作业"的文档承诺）
		{time.Date(2026, 8, 18, 9, 0, 0, 0, loc), false, "周二09:00 盘前冲刺段（§W4-a 禁止）"},
		{time.Date(2026, 8, 18, 8, 29, 0, 0, loc), true, "周二08:29 盘前门外（凌晨放行）"},
		{time.Date(2026, 8, 18, 9, 20, 0, 0, loc), false, "周二09:20 已开盘禁止"},
		{time.Date(2026, 8, 18, 12, 0, 0, 0, loc), false, "周二12:00 午休仍在窗口内"},
		{time.Date(2026, 8, 18, 14, 59, 0, 0, loc), false, "周二14:59 收盘前禁止"},
		{time.Date(2026, 8, 19, 2, 0, 0, 0, loc), true, "周三02:00 凌晨休市"},
		{time.Date(2026, 8, 22, 16, 0, 0, 0, loc), true, "周六16:00"},
		{time.Date(2026, 8, 22, 10, 0, 0, 0, loc), true, "周六上午10:00（非交易日全天放行）"},
	}
	for _, tc := range cases {
		if got := NightlyEligible(tc.t, cfg); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.note, got, tc.want)
		}
	}

	// StartHHMM 已废弃：盘后时段不受自定义启动时刻拦截
	cfg2 := cfg
	cfg2.Nightly.StartHHMM = 2000
	if !NightlyEligible(time.Date(2026, 8, 18, 18, 0, 0, 0, loc), cfg2) {
		t.Error("18:00 已是盘后，StartHHMM=2000 不应再拦截")
	}
}

// TestTradingDataloadDue TradingDataloadDue。
func TestTradingDataloadDue(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	base := time.Date(2026, 8, 18, 10, 0, 0, 0, loc) // 周二 10:00 盘中
	off := config.SchedulerConfig{}
	off.DataloadDuringTrade = config.DataloadDuringTradeConfig{Enabled: false, IntervalMinutes: 30}
	if TradingDataloadDue(base, time.Time{}, off) {
		t.Error("开关关闭时不应触发下载")
	}
	on := off
	on.DataloadDuringTrade = config.DataloadDuringTradeConfig{Enabled: true, IntervalMinutes: 30}
	if !TradingDataloadDue(base, time.Time{}, on) {
		t.Error("盘中且从未下载过应立即触发")
	}
	last := base.Add(-10 * time.Minute)
	if TradingDataloadDue(base, last, on) {
		t.Error("10 分钟前刚下载过(<30min)不应再触发")
	}
	last2 := base.Add(-31 * time.Minute)
	if !TradingDataloadDue(base, last2, on) {
		t.Error("31 分钟前下载过(≥30min)应触发")
	}
	// 非交易时段不应触发
	after := time.Date(2026, 8, 18, 16, 0, 0, 0, loc)
	if TradingDataloadDue(after, time.Time{}, on) {
		t.Error("盘后不应触发交易时段下载")
	}
	// interval<=0 回退默认 30
	on2 := on
	on2.DataloadDuringTrade.IntervalMinutes = 0
	if !TradingDataloadDue(base, base.Add(-31*time.Minute), on2) {
		t.Error("interval=0 应回退默认 30min")
	}
}

// TestLoadSchedulerConfigMerge 加载Scheduler配置合并。
func TestLoadSchedulerConfigMerge(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	// 只显式配少数字段，其余应回退默认
	raw := `{"rules":{"scheduler":{"enabled":true,"dataload_bin":"/opt/x/dl","dataload_during_trading":{"enabled":true,"interval_minutes":15}}}}`
	if err := os.WriteFile(path, []byte(raw), 0644); err != nil {
		t.Fatal(err)
	}
	got := config.LoadSchedulerConfig(path)
	if !got.Enabled {
		t.Error("enabled 应为 true")
	}
	if got.DataloadBin != "/opt/x/dl" {
		t.Errorf("dataload_bin 应覆盖为 /opt/x/dl, 实际 %q", got.DataloadBin)
	}
	if got.ResearchBin != "research" {
		t.Errorf("research_bin 应回退默认 research, 实际 %q", got.ResearchBin)
	}
	if got.Nightly.StartHHMM != 1530 {
		t.Errorf("nightly.start_hhmm 应回退默认 1530, 实际 %d", got.Nightly.StartHHMM)
	}
	if !got.DataloadDuringTrade.Enabled || got.DataloadDuringTrade.IntervalMinutes != 15 {
		t.Errorf("dataload_during_trading 应覆盖为 enabled+15min, 实际 %+v", got.DataloadDuringTrade)
	}
}

// TestLoadSchedulerConfigBacktest 加载Scheduler配置Backtest。
func TestLoadSchedulerConfigBacktest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	raw := `{"rules":{"scheduler":{"nightly":{"backtest_enabled":true,"backtest_events":50}}}}`
	if err := os.WriteFile(path, []byte(raw), 0644); err != nil {
		t.Fatal(err)
	}
	got := config.LoadSchedulerConfig(path)
	if !got.Nightly.BacktestEnabled {
		t.Error("backtest_enabled 应解析为 true")
	}
	if got.Nightly.BacktestEvents != 50 {
		t.Errorf("backtest_events 应解析为 50, 实际 %d", got.Nightly.BacktestEvents)
	}
	// 未配置时默认关闭
	dir2 := t.TempDir()
	p2 := filepath.Join(dir2, "config.json")
	if err := os.WriteFile(p2, []byte(`{"rules":{"scheduler":{}}}`), 0644); err != nil {
		t.Fatal(err)
	}
	got2 := config.LoadSchedulerConfig(p2)
	if got2.Nightly.BacktestEnabled {
		t.Error("默认 backtest_enabled 应为 false")
	}
}

// TestNightlyBacktestStepInserted NightlyBacktestStepInserted。
func TestNightlyBacktestStepInserted(t *testing.T) {
	t.Setenv("FAKE_SLEEP", "0")
	logPath := filepath.Join(t.TempDir(), "fake.log")
	dir := t.TempDir()
	fake := fakeScript(t, logPath)
	db := filepath.Join(dir, "trading.db")
	cfg := cfgSamples(fake, db)
	cfg.Nightly.BacktestEnabled = true
	cfgPath := mustConfig(t, cfg)
	statePath := filepath.Join(dir, "research_state.json")
	s := New(dir, cfgPath, statePath)
	loc := time.FixedZone("CST", 8*3600)
	s.setNow(func() time.Time { return time.Date(2026, 8, 22, 16, 0, 0, 0, loc) }) // 周六

	s.tick()
	waitFor(t, 5*time.Second, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.state.Done && s.state.Day == "20260822"
	}, "夜间作业应完成")
	// 步骤默认不含 backtest，BacktestEnabled 时应插入 backtest + library_replay
	// （子系统统一改造后：B4 回测 + 战法库因子/形态回放）。
	// 默认 cfgSamples steps=["dataload"]，追加后为 3 步，fake 应被调用 3 次
	if got := callCount(t, logPath); got != 3 {
		t.Errorf("BacktestEnabled 应追加 backtest+library_replay 使调用次数=3, 实际 %d", got)
	}
	waitIdleAndSettle(t, s)
}

// TestInsertAfter InsertAfter。
func TestInsertAfter(t *testing.T) {
	got := insertAfter([]string{"a", "discover_factors", "c"}, "discover_factors", "backtest")
	if len(got) != 4 || got[2] != "backtest" {
		t.Errorf("应在 discover_factors 后插入 backtest, 得 %v", got)
	}
	// anchor 不存在 → 追加末尾
	got2 := insertAfter([]string{"a", "b"}, "missing", "backtest")
	if len(got2) != 3 || got2[2] != "backtest" {
		t.Errorf("anchor 不存在应追加末尾, 得 %v", got2)
	}
	// 已含 backtest 不重复
	if containsStep(got, "backtest") != true {
		t.Error("containsStep 应识别 backtest")
	}
}

// TestLoadSchedulerConfigMissing 加载Scheduler配置Missing。
func TestLoadSchedulerConfigMissing(t *testing.T) {
	got := config.LoadSchedulerConfig("/nonexistent/config.json")
	if !got.Enabled {
		t.Error("文件缺失应回退默认(默认 enabled=true)")
	}
}

// fakeScript 生成一个可记录调用并可选 sleep 的假二进制；logPath 为该脚本的写入目标
// （与调用方的 callCount 使用同一路径）。§flaky 根修（R3-8 P1-M）：日志路径直接烘焙进
// 脚本内容——此前脚本运行时读进程级 $FAKE_LOG，而上一条测试遗留的后台 goroutine
// （dataload/worker 排水协程）可能在本测试已改写 FAKE_LOG 后才执行其旧脚本，把调用行
// 追加进【当前测试】的日志文件污染计数（全量跑挂、单跑过）。烘焙后跨测试零串扰。
func fakeScript(t *testing.T, logPath string) string {
	t.Helper()
	script := filepath.Join(filepath.Dir(logPath), "fakebin.sh")
	content := "#!/bin/sh\n" +
		"echo \"FAKE $@\" >> " + shQuote(logPath) + "\n" +
		"if [ -n \"$FAKE_SLEEP\" ]; then exec sleep \"$FAKE_SLEEP\"; fi\n"
	if err := os.WriteFile(script, []byte(content), 0755); err != nil {
		t.Fatal(err)
	}
	return script
}

// shQuote 单引号包裹 shell 字符串（测试路径不含单引号，简单转义足够）。
func shQuote(s string) string {
	return "'" + s + "'"
}

// callCount 统计日志文件中 FAKE 前缀行数（验证作业执行次数）。
func callCount(t *testing.T, logPath string) int {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil {
		return 0
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	n := 0
	for _, l := range lines {
		if strings.HasPrefix(l, "FAKE ") {
			n++
		}
	}
	return n
}

// waitFor 轮询等待条件满足，超时即测试失败。
func waitFor(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("超时: %s", msg)
}

// waitIdleAndSettle §flaky 根修（R3-8 P1-M）收尾助手：等待调度器全部后台作业结束
// （busy + 交易时段下载通道均空闲），再沉降一小段让尾部的状态/日志落盘完成。
// 背景：worker/dataload 的异步 goroutine 若在 t.TempDir() 清理之后仍写文件，
// RemoveAll 报 "directory not empty" 直接判 FAIL——断言全过也挂，且只在全量跑时
// 因前序测试拖慢时序而偶发。所有手动 tick 驱动后台作业的测试收尾都应调用本助手。
func waitIdleAndSettle(t *testing.T, s *Scheduler) {
	t.Helper()
	waitFor(t, 30*time.Second, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return !s.busy && !s.tradingDLBusy
	}, "后台作业应全部结束（TempDir 清理前）")
	time.Sleep(250 * time.Millisecond)
}

// busyLocked 竞争安全读取 busy 标志（§flaky 修复：后台排水 goroutine 在 s.mu 锁内写 busy，
// 测试轮询必须持锁读，否则 -race 报数据竞争且偶发误判）。
// English: race-safe read of the busy flag — the self-drain goroutine writes it under s.mu,
// so test polls must lock too.
func (s *Scheduler) busyLocked() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.busy
}

// TestNightlyJobRunsAndCompletes NightlyJobRuns和Completes。
func TestNightlyJobRunsAndCompletes(t *testing.T) {
	t.Setenv("FAKE_SLEEP", "0")
	logPath := filepath.Join(t.TempDir(), "fake.log")
	dir := t.TempDir()
	fake := fakeScript(t, logPath)
	db := filepath.Join(dir, "trading.db")
	cfgPath := mustConfig(t, cfgSamples(fake, db))
	statePath := filepath.Join(dir, "research_state.json")
	s := New(dir, cfgPath, statePath)
	loc := time.FixedZone("CST", 8*3600)
	s.setNow(func() time.Time { return time.Date(2026, 8, 22, 16, 0, 0, 0, loc) }) // 周六 16:00

	s.tick()
	waitFor(t, 5*time.Second, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.state.Done && s.state.Day == "20260822"
	}, "夜间作业应完成且运行日正确")
	if callCount(t, logPath) < 1 {
		t.Errorf("假二进制应被调用至少一次, 实际 %d", callCount(t, logPath))
	}
	waitIdleAndSettle(t, s)
}

// TestNightlyJobKilledAtTradingOpen NightlyJobKilled在TradingOpen。
func TestNightlyJobKilledAtTradingOpen(t *testing.T) {
	t.Setenv("FAKE_SLEEP", "5") // 步骤长时间运行，方便观察 kill
	logPath := filepath.Join(t.TempDir(), "fake.log")
	dir := t.TempDir()
	fake := fakeScript(t, logPath)
	db := filepath.Join(dir, "trading.db")
	cfgPath := mustConfig(t, cfgSamples(fake, db))
	statePath := filepath.Join(dir, "research_state.json")
	s := New(dir, cfgPath, statePath)
	loc := time.FixedZone("CST", 8*3600)
	now := time.Date(2026, 8, 22, 16, 0, 0, 0, loc) // 周六 16:00
	s.setNow(func() time.Time { return now })
	s.tick()
	waitFor(t, 5*time.Second, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.busy
	}, "作业应已启动")

	// 下一交易日周一 9:20（已进入交易窗口 9:15~收盘）→ 应 kill 作业
	now = time.Date(2026, 8, 24, 9, 20, 0, 0, loc)
	s.tick()
	waitFor(t, 5*time.Second, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return !s.busy
	}, "交易时段应终止夜间作业")
	waitIdleAndSettle(t, s)
}

// TestTradingDataloadThrottled TradingDataloadThrottled。
func TestTradingDataloadThrottled(t *testing.T) {
	t.Setenv("FAKE_SLEEP", "0")
	logPath := filepath.Join(t.TempDir(), "fake.log")
	dir := t.TempDir()
	fake := fakeScript(t, logPath)
	db := filepath.Join(dir, "trading.db")
	cfg := cfgSamples(fake, db)
	cfg.DataloadDuringTrade = config.DataloadDuringTradeConfig{Enabled: true, IntervalMinutes: 30}
	cfgPath := mustConfig(t, cfg)
	statePath := filepath.Join(dir, "research_state.json")
	s := New(dir, cfgPath, statePath)
	loc := time.FixedZone("CST", 8*3600)
	now := time.Date(2026, 8, 18, 10, 0, 0, 0, loc) // 周二 10:00 盘中
	s.setNow(func() time.Time { return now })
	// 交易时段 dataload 为异步 goroutine，用轮询等待计数到位（避免竞态）。
	s.tick()
	waitFor(t, 5*time.Second, func() bool { return callCount(t, logPath) == 1 },
		"首次 tick 应触发 1 次下载")
	// 5 分钟后：间隔 30min 内，不应再触发
	now = now.Add(5 * time.Minute)
	s.tick()
	time.Sleep(300 * time.Millisecond)
	if got := callCount(t, logPath); got != 1 {
		t.Fatalf("30min 间隔内不应重复下载, 实际 %d", got)
	}
	// 31 分钟后：应再触发
	now = now.Add(31 * time.Minute)
	s.tick()
	waitFor(t, 5*time.Second, func() bool { return callCount(t, logPath) == 2 },
		"超间隔应再下载一次")
	s.mu.Lock()
	ts1 := s.state.LastDataload
	s.mu.Unlock()

	// 盘后：交易时段下载通道不再触发（LastDataload 不变）；夜间作业启动并跑首个 dataload 步骤
	now = time.Date(2026, 8, 18, 16, 0, 0, 0, loc)
	s.tick()
	waitFor(t, 5*time.Second, func() bool { return callCount(t, logPath) == 3 },
		"盘后夜间作业应启动并执行 dataload 步骤")
	s.mu.Lock()
	afterLast := s.state.LastDataload
	s.mu.Unlock()
	if afterLast != ts1 {
		t.Fatalf("盘后不应更新交易时段下载时间戳: %d vs %d", afterLast, ts1)
	}

	// §flaky 根修收尾：等待全部后台作业排空再返回——此前异步 dataload/worker goroutine
	// 可能在 t.TempDir() 清理【之后】仍向日志文件写入（echo 重建文件），RemoveAll 报
	// "directory not empty" 直接判 FAIL（断言全过也挂：全量跑挂、单跑过的真凶）。
	waitIdleAndSettle(t, s)
}

// TestNightlyCrossDayReplacesJob 周五作业跨天仍在跑 → 周六 15:30 应被终止并启动周六作业（保证周末各一轮）。
func TestNightlyCrossDayReplacesJob(t *testing.T) {
	t.Setenv("FAKE_SLEEP", "5")
	logPath := filepath.Join(t.TempDir(), "fake.log")
	dir := t.TempDir()
	fake := fakeScript(t, logPath)
	db := filepath.Join(dir, "trading.db")
	cfgPath := mustConfig(t, cfgSamples(fake, db))
	statePath := filepath.Join(dir, "research_state.json")
	s := New(dir, cfgPath, statePath)
	loc := time.FixedZone("CST", 8*3600)
	now := time.Date(2026, 8, 21, 16, 0, 0, 0, loc) // 周五 16:00 盘后
	s.setNow(func() time.Time { return now })
	s.tick()
	waitFor(t, 5*time.Second, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.busy && s.state.Day == "20260821"
	}, "周五作业应启动")

	// 周六 15:30：跨天仍在跑 → 终止周五作业并启动周六作业
	now = time.Date(2026, 8, 22, 15, 30, 0, 0, loc)
	s.tick()
	waitFor(t, 5*time.Second, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.state.Day == "20260822" && s.busy
	}, "周六 15:30 应替换为周六作业")

	// §flaky 根修：120s 长睡的子进程仍在跑，必须显式终止再等排空，否则 TempDir 清理时
	// 进程仍持有目录内文件 → "directory not empty" 判 FAIL。preemptCurrent 会 SIGKILL 子进程
	// 并落终态使 busy 归位（仅置 cancelReq 无效——那是给控制轮询 goroutine 用的，测试直接置位
	// 不会触发 killProcessGroup）。
	s.preemptCurrent("测试收尾")
	waitIdleAndSettle(t, s)
}

// TestRunCancelsJob 验证 Run(ctx) 退出时取消正在运行的作业。
func TestRunCancelsJob(t *testing.T) {
	// Run(ctx) 退出时应 kill 正在运行的作业（CommandContext 取消）
	t.Setenv("FAKE_SLEEP", "5")
	logPath := filepath.Join(t.TempDir(), "fake.log")
	dir := t.TempDir()
	fake := fakeScript(t, logPath)
	db := filepath.Join(dir, "trading.db")
	cfgPath := mustConfig(t, cfgSamples(fake, db))
	statePath := filepath.Join(dir, "research_state.json")
	s := New(dir, cfgPath, statePath)
	loc := time.FixedZone("CST", 8*3600)
	s.setNow(func() time.Time { return time.Date(2026, 8, 22, 16, 0, 0, 0, loc) }) // 周六

	ctx, cancel := context.WithCancel(context.Background())
	go s.Run(ctx)
	waitFor(t, 5*time.Second, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.busy
	}, "Run 应启动作业")
	cancel()
	waitFor(t, 5*time.Second, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return !s.busy
	}, "Run 取消应终止作业")
}
