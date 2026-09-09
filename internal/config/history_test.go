package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"quant-trading-v2/internal/fileutil"
)

// TestValidateQMT 枚举/范围/引用一致性。
func TestValidateQMT(t *testing.T) {
	valid := func() *Rules {
		return &Rules{QMT: QMTConfig{
			Mode: "auto", PriceType: "market", Enabled: true,
			GatewayURL: "https://127.0.0.1:8789", Token: "secret",
			FixedAmount: 10000, MaxPositions: 5, DailyMaxBuys: 20,
			DailyBudgetAmount: 100000, MissHeartbeatSec: 120,
		}}
	}
	if err := Validate(valid()); err != nil {
		t.Fatalf("合法配置应通过: %v", err)
	}
	bad := valid()
	bad.QMT.PriceType = "garbage"
	if err := Validate(bad); err == nil {
		t.Error("非法 price_type 应报错")
	}
	bad = valid()
	bad.QMT.Mode = "semi"
	if err := Validate(bad); err == nil {
		t.Error("非法 mode 应报错")
	}
	bad = valid()
	bad.QMT.Enabled = true
	bad.QMT.GatewayURL = ""
	bad.QMT.Token = "secret-only" // 网关↔token 一致性不强校验（mock/掩码向后兼容），应合法
	if err := Validate(bad); err != nil {
		t.Errorf("enabled 且 token 已配但 gateway_url 空应合法（一致性不强校验）: %v", err)
	}
	bad = valid()
	bad.QMT.Enabled = true
	bad.QMT.GatewayURL = ""
	bad.QMT.Token = ""
	if err := Validate(bad); err != nil {
		t.Errorf("enabled 且两者全空（mock/未接入）应合法: %v", err)
	}
	// 网关已配而 token 空（掩码/本地默认）→ 合法（向后兼容）
	bad = valid()
	bad.QMT.Enabled = true
	bad.QMT.GatewayURL = "https://127.0.0.1:8789"
	bad.QMT.Token = ""
	if err := Validate(bad); err != nil {
		t.Errorf("网关已配 token 空（masked）应合法: %v", err)
	}
	bad = valid()
	bad.QMT.DailyMaxBuys = -1
	if err := Validate(bad); err == nil {
		t.Error("负数 daily_max_buys 应报错")
	}
	bad = valid()
	bad.QMT.MaxPositions = 99
	if err := Validate(bad); err == nil {
		t.Error("超范围 max_positions 应报错")
	}
	// 未启用时不强制网关（本地默认配置合法）
	off := valid()
	off.QMT.Enabled = false
	off.QMT.GatewayURL = ""
	off.QMT.Token = ""
	if err := Validate(off); err != nil {
		t.Fatalf("disabled 时允许空网关: %v", err)
	}
}

// TestValidatePercent 百分比字段越界报错。
func TestValidatePercent(t *testing.T) {
	r := &Rules{RiskCtrl: RiskCtrlConfig{M8PortfolioDrawdownPct: 150}}
	if err := Validate(r); err == nil {
		t.Error(">100 的百分比应报错")
	}
	r = &Rules{Position: PositionConfig{MaxTotalPositionPct: -1}}
	if err := Validate(r); err == nil {
		t.Error("负数百分比应报错")
	}
}

// TestDiffRules 字段级 diff 输出变更路径与新旧值。
func TestDiffRules(t *testing.T) {
	before := []byte(`{"rules":{"qmt":{"mode":"auto","price_type":"market","enabled":false}}}`)
	after := []byte(`{"rules":{"qmt":{"mode":"manual","price_type":"market","enabled":true}}}`)
	d, err := DiffRules(before, after)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(d, "qmt.mode: auto -> manual") {
		t.Errorf("diff 应含 mode 变更: %q", d)
	}
	if !strings.Contains(d, "qmt.enabled: false -> true") {
		t.Errorf("diff 应含 enabled 变更: %q", d)
	}
	// 无变更
	d2, _ := DiffRules(before, before)
	if d2 != "(无变更)" {
		t.Errorf("无变更应输出 (无变更), got %q", d2)
	}
}

// TestSnapshotAndRollbackRules 快照→修改→回滚→恢复原文。
func TestSnapshotAndRollbackRules(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	orig := []byte(`{"rules":{"qmt":{"mode":"auto","price_type":"market","enabled":false}}}`)
	if err := fileutil.AtomicWrite(path, orig, 0o644); err != nil {
		t.Fatal(err)
	}
	m := NewManager(path)
	ts, err := SnapshotRules(m)
	if err != nil {
		t.Fatalf("快照失败: %v", err)
	}
	// 修改当前文件
	changed := []byte(`{"rules":{"qmt":{"mode":"manual","price_type":"market","enabled":true}}}`)
	if err := fileutil.AtomicWrite(path, changed, 0o644); err != nil {
		t.Fatal(err)
	}
	// 快照列表应有 1 条
	list, err := ListRuleSnapshots(m)
	if err != nil || len(list) != 1 || list[0].SnapshotTS != ts {
		t.Fatalf("快照列表异常: %+v err=%v", list, err)
	}
	// 回滚
	b, err := RestoreRulesContent(m, ts)
	if err != nil {
		t.Fatalf("读快照失败: %v", err)
	}
	if err := WriteConfigFile(m, b); err != nil {
		t.Fatalf("写回失败: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(orig) {
		t.Fatalf("回滚后应恢复原文:\n got %s\nwant %s", got, orig)
	}
	// 不存在的快照 → 报错
	if _, err := RestoreRulesContent(m, "20200101_000000"); err == nil {
		t.Fatal("不存在快照应报错")
	}
}
