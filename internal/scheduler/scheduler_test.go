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
		Enabled:     true,
		ResearchBin: fakeBin,
		DataloadBin: fakeBin,
		DB:          db,
		Nightly: config.NightlyConfig{
			StartHHMM:        1530,
			WeekendStartHHMM: 1530,
			Steps:            []string{"dataload"},
			AbortOnError:     false,
		},
		DataloadDuringTrade: config.DataloadDuringTradeConfig{Enabled: false, IntervalMinutes: 30},
	}
}

func TestNightlyEligible(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	cfg := cfgSamples("fake", "/tmp/x.db")
	tue := time.Date(2026, 8, 18, 16, 0, 0, 0, loc) // 周二 16:00 盘后
	if !NightlyEligible(tue, cfg) {
		t.Error("工作日盘后 16:00 应可启动夜间作业")
	}
	morning := time.Date(2026, 8, 18, 10, 0, 0, 0, loc) // 周二 10:00 盘中
	if NightlyEligible(morning, cfg) {
		t.Error("盘中 10:00 不应启动夜间作业")
	}
	early := time.Date(2026, 8, 18, 15, 10, 0, 0, loc) // 15:10 盘后但未到 15:30
	if NightlyEligible(early, cfg) {
		t.Error("15:10 未到启动时间(15:30)，不应启动")
	}
	sat := time.Date(2026, 8, 22, 16, 0, 0, 0, loc) // 周六 16:00
	if !NightlyEligible(sat, cfg) {
		t.Error("周末 16:00 应可启动夜间作业")
	}
	// 自定义交易日启动时间 20:00
	cfg2 := cfg
	cfg2.Nightly.StartHHMM = 2000
	if NightlyEligible(time.Date(2026, 8, 18, 18, 0, 0, 0, loc), cfg2) {
		t.Error("自定义启动时间 20:00，18:00 不应启动")
	}
	if !NightlyEligible(time.Date(2026, 8, 18, 20, 1, 0, 0, loc), cfg2) {
		t.Error("20:01 应启动")
	}
}

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

func TestNightlyBacktestStepInserted(t *testing.T) {
	t.Setenv("FAKE_SLEEP", "0")
	logPath := filepath.Join(t.TempDir(), "fake.log")
	t.Setenv("FAKE_LOG", logPath)
	dir := t.TempDir()
	fake := fakeScript(t, dir)
	db := filepath.Join(dir, "trading.db")
	cfg := cfgSamples(fake, db)
	cfg.Nightly.BacktestEnabled = true
	cfgPath := mustConfig(t, cfg)
	statePath := filepath.Join(dir, "research_state.json")
	s := New(dir, cfgPath, statePath)
	loc := time.FixedZone("CST", 8*3600)
	s.now = func() time.Time { return time.Date(2026, 8, 22, 16, 0, 0, 0, loc) } // 周六

	s.tick()
	waitFor(t, 5*time.Second, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.state.Done && s.state.Day == "20260822"
	}, "夜间作业应完成")
	// 步骤默认不含 backtest，BacktestEnabled 时应插入（fake 只记录调用，无法区分步骤，但应多一次调用）
	// 默认 cfgSamples steps=["dataload"]，追加 backtest 后为 2 步，fake 应被调用 2 次
	if got := callCount(t, logPath); got != 2 {
		t.Errorf("BacktestEnabled 应追加 backtest 步骤使调用次数=2, 实际 %d", got)
	}
}

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

func TestLoadSchedulerConfigMissing(t *testing.T) {
	got := config.LoadSchedulerConfig("/nonexistent/config.json")
	if !got.Enabled {
		t.Error("文件缺失应回退默认(默认 enabled=true)")
	}
}

// fakeScript 生成一个可记录调用并可选 sleep 的假二进制。
func fakeScript(t *testing.T, dir string) string {
	t.Helper()
	script := filepath.Join(dir, "fakebin.sh")
	content := `#!/bin/sh
echo "FAKE $@" >> "$FAKE_LOG"
if [ -n "$FAKE_SLEEP" ]; then sleep "$FAKE_SLEEP"; fi
`
	if err := os.WriteFile(script, []byte(content), 0755); err != nil {
		t.Fatal(err)
	}
	return script
}

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

func TestNightlyJobRunsAndCompletes(t *testing.T) {
	t.Setenv("FAKE_SLEEP", "0")
	logPath := filepath.Join(t.TempDir(), "fake.log")
	t.Setenv("FAKE_LOG", logPath)
	dir := t.TempDir()
	fake := fakeScript(t, dir)
	db := filepath.Join(dir, "trading.db")
	cfgPath := mustConfig(t, cfgSamples(fake, db))
	statePath := filepath.Join(dir, "research_state.json")
	s := New(dir, cfgPath, statePath)
	loc := time.FixedZone("CST", 8*3600)
	s.now = func() time.Time { return time.Date(2026, 8, 22, 16, 0, 0, 0, loc) } // 周六 16:00

	s.tick()
	waitFor(t, 5*time.Second, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.state.Done && s.state.Day == "20260822"
	}, "夜间作业应完成且运行日正确")
	if callCount(t, logPath) < 1 {
		t.Errorf("假二进制应被调用至少一次, 实际 %d", callCount(t, logPath))
	}
}

func TestNightlyJobKilledAtTradingOpen(t *testing.T) {
	t.Setenv("FAKE_SLEEP", "120") // 步骤长时间运行，方便观察 kill
	logPath := filepath.Join(t.TempDir(), "fake.log")
	t.Setenv("FAKE_LOG", logPath)
	dir := t.TempDir()
	fake := fakeScript(t, dir)
	db := filepath.Join(dir, "trading.db")
	cfgPath := mustConfig(t, cfgSamples(fake, db))
	statePath := filepath.Join(dir, "research_state.json")
	s := New(dir, cfgPath, statePath)
	loc := time.FixedZone("CST", 8*3600)
	now := time.Date(2026, 8, 22, 16, 0, 0, 0, loc) // 周六 16:00
	s.now = func() time.Time { return now }

	s.tick()
	waitFor(t, 5*time.Second, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.busy
	}, "作业应已启动")

	// 下一交易日周一 8:40 盘前 → 应 kill 作业
	now = time.Date(2026, 8, 24, 8, 40, 0, 0, loc)
	s.tick()
	waitFor(t, 5*time.Second, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return !s.busy
	}, "交易时段应终止夜间作业")
}

func TestTradingDataloadThrottled(t *testing.T) {
	t.Setenv("FAKE_SLEEP", "0")
	logPath := filepath.Join(t.TempDir(), "fake.log")
	t.Setenv("FAKE_LOG", logPath)
	dir := t.TempDir()
	fake := fakeScript(t, dir)
	db := filepath.Join(dir, "trading.db")
	cfg := cfgSamples(fake, db)
	cfg.DataloadDuringTrade = config.DataloadDuringTradeConfig{Enabled: true, IntervalMinutes: 30}
	cfgPath := mustConfig(t, cfg)
	statePath := filepath.Join(dir, "research_state.json")
	s := New(dir, cfgPath, statePath)
	loc := time.FixedZone("CST", 8*3600)
	now := time.Date(2026, 8, 18, 10, 0, 0, 0, loc) // 周二 10:00 盘中
	s.now = func() time.Time { return now }

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
}

// TestNightlyCrossDayReplacesJob 周五作业跨天仍在跑 → 周六 15:30 应被终止并启动周六作业（保证周末各一轮）。
func TestNightlyCrossDayReplacesJob(t *testing.T) {
	t.Setenv("FAKE_SLEEP", "120")
	logPath := filepath.Join(t.TempDir(), "fake.log")
	t.Setenv("FAKE_LOG", logPath)
	dir := t.TempDir()
	fake := fakeScript(t, dir)
	db := filepath.Join(dir, "trading.db")
	cfgPath := mustConfig(t, cfgSamples(fake, db))
	statePath := filepath.Join(dir, "research_state.json")
	s := New(dir, cfgPath, statePath)
	loc := time.FixedZone("CST", 8*3600)
	now := time.Date(2026, 8, 21, 16, 0, 0, 0, loc) // 周五 16:00 盘后
	s.now = func() time.Time { return now }

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
}

func TestRunCancelsJob(t *testing.T) {
	// Run(ctx) 退出时应 kill 正在运行的作业（CommandContext 取消）
	t.Setenv("FAKE_SLEEP", "120")
	logPath := filepath.Join(t.TempDir(), "fake.log")
	t.Setenv("FAKE_LOG", logPath)
	dir := t.TempDir()
	fake := fakeScript(t, dir)
	db := filepath.Join(dir, "trading.db")
	cfgPath := mustConfig(t, cfgSamples(fake, db))
	statePath := filepath.Join(dir, "research_state.json")
	s := New(dir, cfgPath, statePath)
	loc := time.FixedZone("CST", 8*3600)
	s.now = func() time.Time { return time.Date(2026, 8, 22, 16, 0, 0, 0, loc) } // 周六

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
