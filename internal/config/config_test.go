// config 包单元测试：默认规则、配置读取/保存往返、子配置 Set/Get、LLM 流式开关。
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestDefaultRules 出厂默认参数完整性：四战法权重与动量分归一、D1 规则可初始化。
func TestDefaultRules(t *testing.T) {
	d := DefaultRules
	if d == nil {
		t.Fatal("DefaultRules 为 nil")
	}
	s := d.Strategy
	w := s.Dragon.F1SealWeight + s.Dragon.F2ResonanceWeight + s.Dragon.F3PremiumWeight + s.Dragon.F4RsWeight
	if w != 1.0 {
		t.Errorf("Dragon 四因子权重合计应=1.0（e2e 验证组合）, got %.2f", w)
	}
	mo := s.Momentum
	if mo.VolumePriceWeight+mo.MACDWeight+mo.TrendWeight != 100 {
		t.Errorf("动量三分权重合计应=100, got %.0f", mo.VolumePriceWeight+mo.MACDWeight+mo.TrendWeight)
	}
	if s.NShape.NPatternScoreThreshold != 60 {
		t.Errorf("N形总分阈值默认应=60, got %.0f", s.NShape.NPatternScoreThreshold)
	}
}

// TestManagerLoadSaveRoundTrip 配置写入磁盘后二次加载仍能还原关键字段（D1 规则 + 策略覆盖）。
func TestManagerLoadSaveRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	m := NewManager(path)

	// 修改策略与 D1 后持久化
	newStrat := defaultStrategyConfig()
	newStrat.Dragon.F1SealWeight = 0.5
	m.SetStrategyConfig(&newStrat)

	d1 := &D1Config{Rules: []D1Rule{{Direction: "利好", Score: 0.8}}}
	m.SetD1Config(d1)

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("Set 后应落盘: %v", err)
	}

	// 用全新管理器从磁盘加载（§0925EVE-D1：rules/d1 字段转私有，断言经加锁访问器读取，语义不变）
	m2 := NewManager(path)
	if m2.Get().Strategy.Dragon.F1SealWeight != 0.5 {
		t.Errorf("重载后 F1SealWeight 应=0.5, got %.2f", m2.Get().Strategy.Dragon.F1SealWeight)
	}
	if len(m2.GetD1Config().Rules) != 1 || m2.GetD1Config().Rules[0].Direction != "利好" {
		t.Errorf("重载后 D1 规则丢失: %+v", m2.GetD1Config().Rules)
	}
}

// TestManagerMissingFileMissing 指向不存在路径时使用默认值而非崩溃。
func TestManagerLoadMissingFileUsesDefaults(t *testing.T) {
	m := NewManager(filepath.Join(t.TempDir(), "nonexistent.json"))
	if m.Get() == nil {
		t.Fatal("缺失文件时应保有默认 Rules")
	}
	if m.GetStrategyConfig() == nil {
		t.Fatal("缺失文件时应保有默认策略")
	}
}

// TestManagerSetGet 各配置子项的 Get/Set 往返读取。
func TestManagerSetGet(t *testing.T) {
	m := NewManager(filepath.Join(t.TempDir(), "cfg.json"))

	llm := &LLMConfig{APIURL: "https://x/v1", Model: "m", TimeoutSec: 42}
	m.SetLLMConfig(llm)
	if got := m.GetLLMConfig(); got.APIURL != "https://x/v1" || got.Model != "m" || got.TimeoutSec != 42 {
		t.Errorf("LLM Get/Set 往返失败: %+v", got)
	}
}

// TestStreamingEnabled LLM 流式开关：nil 默认开、显式 true/false 生效。
func TestStreamingEnabled(t *testing.T) {
	tr := true
	var fl bool
	if defaultLLM().StreamingEnabled() != true {
		t.Error("nil Stream 应默认开启流式（推理模型非流式首字极慢）")
	}
	c := &LLMConfig{Stream: &tr}
	if !c.StreamingEnabled() {
		t.Error("显式 true 应开启流式")
	}
	c2 := &LLMConfig{Stream: &fl}
	if c2.StreamingEnabled() {
		t.Error("显式 false 应关闭流式")
	}
	var nilCfg *LLMConfig
	if !nilCfg.StreamingEnabled() {
		t.Error("nil 接收者应返回默认开启")
	}
}

// defaultLLM 返回默认 LLM 配置（测试辅助）。
func defaultLLM() *LLMConfig { return &LLMConfig{} }

// TestRuntimeConfigIntervalDefaults A+B 快速执行器配置：默认回退 5s，可配置更低值。
// English: A+B fast-executor config: default falls back to 5s, lower values are configurable.
func TestRuntimeConfigIntervalDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	m := NewManager(path)

	// 显式设为 1s 并持久化（深拷贝避免污染共享 DefaultRules）
	// §0925EVE-D1：指针替换走与 Load 相同的 m.mu 发布口径（虽然是单协程 setup，也不新开裸写通道）
	rules := *m.Get()
	rules.Runtime = RuntimeConfig{FeedIntervalSec: 1, ScoringIntervalSec: 1}
	m.mu.Lock()
	m.rules = &rules
	m.mu.Unlock()
	m.Save()
	m2 := NewManager(path)
	if m2.Get().Runtime.FeedIntervalSec != 1 {
		t.Errorf("FeedIntervalSec=%d, want 1", m2.Get().Runtime.FeedIntervalSec)
	}
	if m2.Get().Runtime.ScoringIntervalSec != 1 {
		t.Errorf("ScoringIntervalSec=%d, want 1", m2.Get().Runtime.ScoringIntervalSec)
	}
	// 未设置时默认 0（调用方回退 5s）
	m3 := NewManager(filepath.Join(t.TempDir(), "cfg2.json"))
	if m3.Get().Runtime.FeedIntervalSec != 0 || m3.Get().Runtime.ScoringIntervalSec != 0 {
		t.Errorf("默认应为 0（回退 5s）, got %d/%d", m3.Get().Runtime.FeedIntervalSec, m3.Get().Runtime.ScoringIntervalSec)
	}
}

// TestLoadSchedulerConfigResourceThrottles 内存闸门/回放节流配置应被 LoadSchedulerConfig 解析：
// 此前 min_free_mem_mb 漏解析恒走默认 400，无法按需为 quant 留内存余量（修复 #A）。
// English: min_free_mem_mb / replay_throttle_ms must round-trip through LoadSchedulerConfig —
// min_free_mem_mb was previously never parsed (always the 400 default) and is now honored.
func TestLoadSchedulerConfigResourceThrottles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	raw := `{"rules":{"scheduler":{
		"min_free_mem_mb": 600,
		"replay_throttle_ms": 80,
		"step_timeout_min": 360
	}}}`
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := LoadSchedulerConfig(path)
	if cfg.MinFreeMemMB != 600 {
		t.Errorf("MinFreeMemMB=%d, want 600", cfg.MinFreeMemMB)
	}
	if cfg.ReplayThrottleMs != 80 {
		t.Errorf("ReplayThrottleMs=%d, want 80", cfg.ReplayThrottleMs)
	}
	if cfg.StepTimeoutMin != 360 {
		t.Errorf("StepTimeoutMin=%d, want 360", cfg.StepTimeoutMin)
	}
}

// memKVStore 内存版 KVStore（测试用）：按 (userID,key) 存取 JSON 快照。
// English: in-memory KVStore for tests — stores snapshots by (userID, key).
type memKVStore struct {
	m map[string]string
}

// 桩：把配置写入内存 KV（非真实数据库）。
func (m *memKVStore) SetConfig(userID, key, value string) error {
	if m.m == nil {
		m.m = map[string]string{}
	}
	m.m[userID+"\x00"+key] = value
	return nil
}

// 桩：从内存 KV 读取配置。
func (m *memKVStore) GetConfig(userID, key string) (string, bool) {
	if m.m == nil {
		return "", false
	}
	v, ok := m.m[userID+"\x00"+key]
	return v, ok
}

// TestQMTConfigPerAccount §2026-09-07 多账号实盘：QMT 实盘配置按账号隔离——
// 账号自身覆盖优先；无覆盖回退运营账号（存量单账号行为）；两者皆无回退全局 rules.qmt。
// English: per-account QMT live-trading config — account override first, then operator, then global.
func TestQMTConfigPerAccount(t *testing.T) {
	m := NewManager(filepath.Join(t.TempDir(), "config.json"))
	kv := &memKVStore{}
	m.SetStore(kv)
	m.SetOperatorID("u_op")

	opCfg := DefaultQMTConfig()
	opCfg.Enabled = true
	opCfg.GatewayURL = "http://127.0.0.1:8789"
	opCfg.Token = "op-token"
	opCfg.FixedAmount = 20000
	m.SetQMTConfigFor("u_op", &opCfg)

	// 子账号无覆盖 → 回退运营账号配置（存量行为不变）
	if got := m.GetQMTConfigFor("u_sub"); got == nil || got.FixedAmount != 20000 || got.Token != "op-token" {
		t.Errorf("子账号应回退运营账号配置, got %+v", got)
	}

	// 子账号配置自己的实盘 → 覆盖运营账号，且不影响运营账号
	subCfg := DefaultQMTConfig()
	subCfg.Enabled = true
	subCfg.GatewayURL = "http://127.0.0.1:8790"
	subCfg.Token = "sub-token"
	subCfg.FixedAmount = 50000
	m.SetQMTConfigFor("u_sub", &subCfg)

	if got := m.GetQMTConfigFor("u_sub"); got == nil || got.GatewayURL != "http://127.0.0.1:8790" || got.FixedAmount != 50000 {
		t.Errorf("子账号应返回自身配置, got %+v", got)
	}
	if got := m.GetQMTConfigFor("u_op"); got == nil || got.GatewayURL != "http://127.0.0.1:8789" {
		t.Errorf("运营账号配置不应被子账号覆盖, got %+v", got)
	}

	// 无 store（未接入账号隔离）→ 回退全局 rules.qmt
	m2 := NewManager(filepath.Join(t.TempDir(), "config2.json"))
	m2.Get().QMT = DefaultQMTConfig() // §0925EVE-D1：经加锁访问器取活体指针后写（语义同旧字段直写）
	m2.Get().QMT.GatewayURL = "http://127.0.0.1:8888"
	if got := m2.GetQMTConfigFor("any"); got == nil || got.GatewayURL != "http://127.0.0.1:8888" {
		t.Errorf("无 store 应回退全局 rules.qmt, got %+v", got)
	}
}

// TestStoredLLMConfig §UI-AUTHORITATIVE 修复：启动装配需要区分「运营账号在设置页真实
// 保存过 LLM 配置」与「无保存回退全局」——前者重启后必须优先于环境变量恢复。
// （无保存 → ok=false；SetLLMConfigFor 后 → ok=true 且快照为保存值，子账号解析到运营配置。）
// English: distinguishes an explicitly saved operator LLM config from the global fallback,
// so startup can give UI-saved values precedence over process env after restarts.
func TestStoredLLMConfig(t *testing.T) {
	m := NewManager(filepath.Join(t.TempDir(), "config.json"))
	m.SetStore(&memKVStore{})
	m.SetOperatorID("u_op")

	// 未保存过：ok=false（启动应回落 env/全局链）
	if _, ok := m.StoredLLMConfig("u_op"); ok {
		t.Fatalf("无保存时不应报告已有账号级 LLM 配置")
	}

	m.SetLLMConfigFor("u_op", &LLMConfig{APIURL: "https://kiraai.vn/v1/chat/completions", Model: "qwen3.8-flash-free"})
	saved, ok := m.StoredLLMConfig("u_op")
	if !ok || saved.APIURL != "https://kiraai.vn/v1/chat/completions" || saved.Model != "qwen3.8-flash-free" {
		t.Fatalf("保存后应可读出快照, got %+v ok=%v", saved, ok)
	}
	// 子账号经 ownerOf 解析到运营账号同一份保存（系统级共享语义）
	if s2, ok2 := m.StoredLLMConfig("u_sub"); !ok2 || s2.Model != "qwen3.8-flash-free" {
		t.Fatalf("子账号应解析到运营保存配置, got %+v ok=%v", s2, ok2)
	}
}

// TestNotifyNtfyChannelRoundTrip §HARDENING ntfy 运维告警通道配置键：JSON 序列化往返不丢键，
// 默认零值=通道关闭（main.go 的 Topic!="" 启用守卫依赖此语义）。
// 注意：Manager.rules（§0925EVE-D1 转私有）初始指向包级单例 DefaultRules（运行时全局单实例语义），
// SetNotifyConfig 会污染全局默认——本测试走纯 JSON 往返，不触碰全局状态。
func TestNotifyNtfyChannelRoundTrip(t *testing.T) {
	nc := NotifyConfig{
		NtfyURL:   "https://ntfy.sh",
		NtfyTopic: "topic-abc123",
		Push:      PushConfig{Enabled: true, Provider: "jpush"}, // 并行通道不受影响
	}
	b, err := json.Marshal(Rules{Notify: nc})
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	var back Rules
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("反序列化失败: %v", err)
	}
	if back.Notify.NtfyURL != "https://ntfy.sh" || back.Notify.NtfyTopic != "topic-abc123" {
		t.Fatalf("往返后 ntfy 键丢失: url=%q topic=%q", back.Notify.NtfyURL, back.Notify.NtfyTopic)
	}
	if !back.Notify.Push.Enabled || back.Notify.Push.Provider != "jpush" {
		t.Fatalf("ntfy 键不应影响 push 网关配置: %+v", back.Notify.Push)
	}

	// 零值往返：omitempty 不落盘，反序列化回空 = 通道关闭
	b2, _ := json.Marshal(Rules{})
	var zero Rules
	if err := json.Unmarshal(b2, &zero); err != nil {
		t.Fatalf("零值反序列化失败: %v", err)
	}
	if zero.Notify.NtfyTopic != "" {
		t.Fatalf("默认配置不应启用 ntfy 通道, got %q", zero.Notify.NtfyTopic)
	}
}
