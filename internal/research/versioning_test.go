package research

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/store"
)

// TestVersioningRoundTrip §WS-H C2：写前快照 → 修改 → 回滚 → 恢复原值；快照列表倒序。
func TestVersioningRoundTrip(t *testing.T) {
	dir := t.TempDir()
	// 初始 applied_factors.json 内容
	orig := []byte(`[{"id":"fac_1","name":"老参数","factors":["EP_ttm"],"weights":{"EP_ttm":1},"enabled":true}]`)
	if err := data.AtomicWrite(filepath.Join(dir, "applied_factors.json"), orig, 0o644); err != nil {
		t.Fatal(err)
	}

	// 写前快照（捕获"老参数"状态）
	ts, err := SnapshotStrategies(dir)
	if err != nil {
		t.Fatalf("快照失败: %v", err)
	}
	if ts == "" {
		t.Fatal("快照时间戳为空")
	}
	// 快照文件确实落盘
	snapPath := filepath.Join(dir, "config_history", "strategies", ts+".json")
	if _, err := os.Stat(snapPath); err != nil {
		t.Fatalf("快照文件缺失: %v", err)
	}

	// 修改（新参数）
	changed := []byte(`[{"id":"fac_1","name":"新参数","factors":["EP_ttm"],"weights":{"EP_ttm":1},"enabled":true}]`)
	if err := data.AtomicWrite(filepath.Join(dir, "applied_factors.json"), changed, 0o644); err != nil {
		t.Fatal(err)
	}

	// 回滚到快照
	if err := RestoreSnapshot(dir, ts); err != nil {
		t.Fatalf("回滚失败: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "applied_factors.json"))
	if err != nil {
		t.Fatal(err)
	}
	// JSON 语义相等（快照文件经 MarshalIndent 可能重新缩进，字节序可不同）
	if !jsonEqual(got, orig) {
		t.Fatalf("回滚后应恢复旧值:\n got %s\nwant %s", got, orig)
	}

	// 快照列表：倒序且含刚写入的快照
	list, err := ListSnapshots(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].SnapshotTS != ts {
		t.Fatalf("快照列表异常: %+v (want %s)", list, ts)
	}

	// 不存在的快照回滚 → 报错
	if err := RestoreSnapshot(dir, "20200101_000000"); err == nil {
		t.Fatal("不存在快照回滚应报错")
	}
}

// TestApplyWeightsSnapshotsBeforeWrite ApplyWeights 写前自动快照：调用后快照目录出现新条目。
func TestApplyWeightsSnapshotsBeforeWrite(t *testing.T) {
	dir := t.TempDir()
	before, _ := ListSnapshots(dir)
	if len(before) != 0 {
		t.Fatalf("初始快照应为空, got %d", len(before))
	}
	if err := ApplyWeights(dir, &store.Candidate{
		Kind: "weights", Factors: `["EP_ttm","BP"]`,
		Weights: `{"EP_ttm":0.6,"BP":0.4}`, Horizon: 5, IR: 0.3,
	}); err != nil {
		t.Fatalf("ApplyWeights: %v", err)
	}
	after, err := ListSnapshots(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 1 {
		t.Fatalf("ApplyWeights 应产生 1 条写前快照, got %d", len(after))
	}
}

// jsonEqual 比较两段 JSON 的语义等价性（解析为 interface{} 后深度相等）。
func jsonEqual(a, b []byte) bool {
	var va, vb interface{}
	if json.Unmarshal(a, &va) != nil || json.Unmarshal(b, &vb) != nil {
		return string(a) == string(b)
	}
	return reflect.DeepEqual(va, vb)
}
