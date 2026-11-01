// runtime_switches_test.go — §SUBSCRIPTION-A（2026-11-01）行情 feed 凭据取值与启动开关清单的行为锁。
//
// 这批为什么值得单独测：取值顺序错了不报错。旧实现只吃全局 rules.qmt.token，而生产的全局位
// 按 N1 已被清空 ⇒ 每 3s 一次 401、feed 永不连接、行情照旧走外部源、日志 60s 一条——
// 现场看上去"功能开着"，实际一秒都没生效。这是本仓反复踩过的静默无效形状，
// 所以这里测的不是"能不能拿到字符串"，而是**三档取值方向 + 拿不到时不许硬启**。
package main

import (
	"bytes"
	"log"
	"path/filepath"
	"strings"
	"testing"

	"quant-trading-v2/internal/auth"
	"quant-trading-v2/internal/config"
)

// newCfgWithStore 起一个带 auth 存储的 config.Manager（生产就是这种装配）。
func newCfgWithStore(t *testing.T) (*config.Manager, string) {
	t.Helper()
	dir := t.TempDir()
	mgr := auth.NewManager(dir)
	if err := mgr.Init(); err != nil {
		t.Fatalf("auth init: %v", err)
	}
	if _, err := mgr.CreateUser("op_admin", "pw-123456", auth.RoleAdmin, nil, 0); err != nil {
		t.Fatalf("create admin: %v", err)
	}
	cfgMgr := config.NewManager(filepath.Join(dir, "config.json"))
	cfgMgr.SetStore(mgr)
	cfgMgr.SetOperatorID(mgr.AdminID())
	return cfgMgr, mgr.AdminID()
}

// TestFeedCredentialPrefersOperatorStoredKey 运营账号自己落过盘的钥匙必须赢过全局位。
// 反过来的旧形状（先读全局）在生产就是 401 空转：全局位按凭证收口已被清空。
func TestFeedCredentialPrefersOperatorStoredKey(t *testing.T) {
	cfgMgr, opID := newCfgWithStore(t)
	// 全局位放一把"别的钥匙"（模拟本地/测试环境里 config.json 仍有值）
	cfg := cfgMgr.Get()
	cfg.QMT.Token = "global-key"
	if err := cfgMgr.Save(); err != nil {
		t.Fatalf("save global rules: %v", err)
	}
	if err := cfgMgr.SetQMTConfigFor(opID, &config.QMTConfig{Token: "operator-stored-key", Enabled: true}); err != nil {
		t.Fatalf("set operator qmt config: %v", err)
	}

	tok, from := feedCredential(cfgMgr)
	if tok != "operator-stored-key" {
		t.Fatalf("应优先取运营账号在册快照钥匙, got %q（来源 %s）", tok, from)
	}
	if from != "运营账号在册快照" {
		t.Fatalf("凭据来源必须如实标出（这条要进启动日志，不能让人考古）, got %q", from)
	}
}

// TestFeedCredentialGlobalFallbackIsLabelled 无账号存储（本地/UAT 裸配置）时才允许吃全局兜底，
// 且来源必须标成"全局兜底"——现网出现这条就说明配置回到了旧扩散形态，要看得见。
func TestFeedCredentialGlobalFallbackIsLabelled(t *testing.T) {
	cfgMgr := config.NewManager(filepath.Join(t.TempDir(), "config.json")) // 没有 store ⇒ 只有全局位
	cfg := cfgMgr.Get()
	cfg.QMT.Token = "global-key"
	if err := cfgMgr.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}
	tok, from := feedCredential(cfgMgr)
	if tok != "global-key" || from != "全局兜底" {
		t.Fatalf("全局兜底路径异常: tok=%q from=%q", tok, from)
	}
}

// TestFeedCredentialNoneNeverStartsQuietly 一把钥匙都拿不到 ⇒ 返回空串，调用方必须**显式停用**
// 并打状态日志，而不是硬启一个每轮 401 的循环。
func TestFeedCredentialNoneNeverStartsQuietly(t *testing.T) {
	cfgMgr := config.NewManager(filepath.Join(t.TempDir(), "config.json"))
	tok, from := feedCredential(cfgMgr)
	if tok != "" {
		t.Fatalf("全局位为空时不该编出钥匙, got %q", tok)
	}
	if from != "无可用钥匙" {
		t.Fatalf("取值失败要如实回报, got %q", from)
	}
}

// TestSwitchInventoryCoversEveryKey 追加裁决 15：关≠不存在。启动清单必须**逐键点名**，
// 新增开关却不进清单＝"上了功能没人知道有"的复发形态。这里真捕获日志正文逐键断言，
// 而不是只调一次不 panic 就算过（那种断言是摆设）。
func TestSwitchInventoryCoversEveryKey(t *testing.T) {
	cfgMgr, _ := newCfgWithStore(t)

	var buf bytes.Buffer
	origOut, origFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	logRuntimeSwitches(cfgMgr)
	log.SetOutput(origOut)
	log.SetFlags(origFlags)

	out := buf.String()
	want := []string{
		"runtime.qmt_feed_enabled",
		"runtime.qmt_feed_primary",
		"runtime.qmt_feed_coverage_min_pct",
		"runtime.qmt_feed_volume_to_shares",
		"runtime.monitor_pool_hot_max",
		"runtime.quote_per_code_source",
		"rules.qmt.subscription_recognize",
		"rules.qmt.subscription_cancel_guard",
		"网关侧 subscription_recognize",
	}
	for _, k := range want {
		if !strings.Contains(out, k) {
			t.Fatalf("启动清单缺键 %q（新增开关必须同批进清单，门禁 §162 另有 8 条等值锁）:\n%s", k, out)
		}
	}
	// 出厂态必须逐项写成 enabled/disabled，让人一眼看出这项到底跑不跑（2026-11-01 owner 改判：
	// 行情四键出厂开＝券商数据源要"通畅地上线"，申购两键仍出厂关＝等盘中探针与段表校准）。
	for _, k := range []string{
		"runtime.qmt_feed_enabled=enabled",
		"runtime.qmt_feed_primary=enabled",
		"runtime.quote_per_code_source=enabled",
		"runtime.quote_hub_consult=enabled",
	} {
		if !strings.Contains(out, k) {
			t.Fatalf("行情四键出厂态必须是 enabled（缺键=开，指针位语义）: 缺 %q:\n%s", k, out)
		}
	}
	if !strings.Contains(out, "subscription_recognize=disabled") {
		t.Fatalf("申购开关缺省态没写进清单:\n%s", out)
	}
}

// TestSwitchInventoryExplicitFalseIsDisabled 显式写 false 必须打成 disabled——
// 指针位的"出厂开"不能把"人明确关掉"也读成开，否则设置页的关是假关。
func TestSwitchInventoryExplicitFalseIsDisabled(t *testing.T) {
	cfgMgr, _ := newCfgWithStore(t)
	off := false
	rt := cfgMgr.Get().Runtime
	rt.QMTFeedPrimary = &off
	rt.QuoteHubConsult = &off
	if err := cfgMgr.SetRuntimeConfig(&rt); err != nil {
		t.Fatalf("set runtime: %v", err)
	}

	var buf bytes.Buffer
	origOut, origFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	logRuntimeSwitches(cfgMgr)
	log.SetOutput(origOut)
	log.SetFlags(origFlags)

	out := buf.String()
	for _, k := range []string{"runtime.qmt_feed_primary=disabled", "runtime.quote_hub_consult=disabled"} {
		if !strings.Contains(out, k) {
			t.Fatalf("显式 false 必须打成 disabled:\n%s", out)
		}
	}
}
