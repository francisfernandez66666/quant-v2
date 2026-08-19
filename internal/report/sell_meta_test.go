// seller meta 测试：入场评分快照、阶段最高价、卖出原因与旧 JSON 兼容性。
package report

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestLogSignalMetaDefaults 开仓默认记录 EntryMeta{highest_price=入场价} 与 HighestPrice=入场价。
func TestLogSignalMetaDefaults(t *testing.T) {
	r := New("")
	r.LogSignal("s1", "600276", "恒瑞医药", "做多", "dragon", 20.0, 50, 10)
	l := r.FindBySignalID("s1")
	if l == nil {
		t.Fatal("记录应存在")
	}
	if l.HighestPrice != 20.0 {
		t.Errorf("HighestPrice 应=入场价20, got %.2f", l.HighestPrice)
	}
	if l.EntryMeta == nil || l.EntryMeta["highest_price"] != 20.0 {
		t.Errorf("EntryMeta 应含 highest_price=20, got %+v", l.EntryMeta)
	}
}

// TestLogSignalWithMeta 显式 meta 不被覆盖，highest_price 缺失时自动补。
func TestLogSignalWithMeta(t *testing.T) {
	r := New("")
	r.LogSignalWithMeta("s2", "600580", "卧龙", "做多", "n_shape", 10.0, 8, 5,
		map[string]float64{"entry_nphase": 4, "vol_ratio": 0.8})
	l := r.FindBySignalID("s2")
	if l.EntryMeta["entry_nphase"] != 4 || l.EntryMeta["vol_ratio"] != 0.8 {
		t.Errorf("显式 meta 应保留, got %+v", l.EntryMeta)
	}
	if l.EntryMeta["highest_price"] != 10.0 {
		t.Errorf("缺失的 highest_price 应补为入场价, got %+v", l.EntryMeta)
	}

	// 显式提供 highest_price 时不得覆盖
	r.LogSignalWithMeta("s3", "600519", "茅台", "做多", "dragon_return", 100.0, 20, 5,
		map[string]float64{"highest_price": 120.0})
	l3 := r.FindBySignalID("s3")
	if l3.HighestPrice != 120.0 || l3.EntryMeta["highest_price"] != 120.0 {
		t.Errorf("显式 highest_price 应保留, got high=%.2f meta=%+v", l3.HighestPrice, l3.EntryMeta)
	}
}

// TestLogExitReason 平仓时记录卖出原因；未传原因为空串不影响状态。
func TestLogExitReason(t *testing.T) {
	r := New("")
	r.LogSignal("s1", "600000", "浦发", "做多", "dragon_return", 10.0, 20, 5)
	r.LogExit("s1", 9.5, "龙回头移动止盈")
	l := r.FindBySignalID("s1")
	if l.ExitReason != "龙回头移动止盈" {
		t.Errorf("ExitReason 应记录原因, got %q", l.ExitReason)
	}
	if l.Status != "已止损" {
		t.Errorf("亏损平仓应标 已止损, got %s", l.Status)
	}

	r.LogSignal("s2", "300750", "宁德", "做多", "double_bump", 20.0, 15, 5)
	r.LogExit("s2", 23.0)
	l2 := r.FindBySignalID("s2")
	if l2.ExitReason != "" {
		t.Errorf("未传原因时应为空串, got %q", l2.ExitReason)
	}
}

// TestRaiseHighest 仅当价格创阶段新高时更新并返回 true，否则保持原值返回 false。
func TestRaiseHighest(t *testing.T) {
	r := New("")
	r.LogSignal("s1", "600276", "恒瑞", "做多", "dragon_return", 20.0, 20, 5)

	if !r.RaiseHighest("s1", 21.5) {
		t.Error("创新高应返回 true")
	}
	if l := r.FindBySignalID("s1"); l.HighestPrice != 21.5 {
		t.Errorf("阶段高点应升到21.5, got %.2f", l.HighestPrice)
	}
	if r.RaiseHighest("s1", 20.5) {
		t.Error("未创新高不应返回 true")
	}
	if l := r.FindBySignalID("s1"); l.HighestPrice != 21.5 {
		t.Errorf("未创新高时最高点应保持不变, got %.2f", l.HighestPrice)
	}
	// 已平仓的持仓不更新
	r.LogExit("s1", 22.0, "手动")
	if r.RaiseHighest("s1", 30.0) {
		t.Error("已平仓持仓不应更新阶段高点")
	}
}

// TestLegacyJSONCompatibility 旧版持久化 JSON（无新增字段）解析后零值不报错。
func TestLegacyJSONCompatibility(t *testing.T) {
	legacy := `[
  {
    "signal_id": "pos-1",
    "code": "600206",
    "name": "有研新材",
    "direction": "做多",
    "strategy": "dragon",
    "entry_at": "2026-08-10T10:00:00Z",
    "entry_price": 40,
    "status": "持仓中",
    "take_profit_pct": 8,
    "stop_loss_pct": 5,
    "quantity": 1,
    "lots": [{"price": 40, "quantity": 1, "at": "2026-08-10T10:00:00Z"}]
  }
]`
	dir := t.TempDir()
	path := filepath.Join(dir, "positions.json")
	if err := os.WriteFile(path, []byte(legacy), 0644); err != nil {
		t.Fatal(err)
	}
	r := New(path)
	held := r.HeldPositions()
	if len(held) != 1 {
		t.Fatalf("旧 JSON 应解析出 1 条持仓, got %d", len(held))
	}
	p := held[0]
	if p.EntryMeta != nil {
		t.Errorf("旧数据 EntryMeta 应为零值 nil, got %+v", p.EntryMeta)
	}
	if p.HighestPrice != 0 || p.ExitReason != "" {
		t.Errorf("旧数据新字段应为零值, got high=%.2f reason=%q", p.HighestPrice, p.ExitReason)
	}
	// 序列化往返：新字段在旧数据上不产生破坏
	data, err := json.Marshal(r.List())
	if err != nil {
		t.Fatal(err)
	}
	var back []ExecLog
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("序列化往返失败: %v", err)
	}
}
