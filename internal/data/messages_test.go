// messages_test.go — 消息中心持久化单元测试：验证按代码刷新名称、清除做空方向消息及墓碑/隔离逻辑。
package data

import (
	"path/filepath"
	"testing"
	"time"
)

// TestRefreshNameByCode 验证 RefreshNameByCode：按代码刷新股票名称并持久化，
// 其他代码/其他字段不受影响；空 code 或 name 不操作。
// English: TestRefreshNameByCode verifies RefreshNameByCode: refreshes the stock name by code and persists it,
// English: other codes/other fields are unaffected; empty code or name does nothing.
func TestRefreshNameByCode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "messages.json")
	s := NewMessageStore(path)
	base := time.Now()
	s.Sync([]MessageItem{
		{ID: "600519@提示", Code: "600519", Name: "老名", Level: "提示", GeneratedAt: base},
		{ID: "600519@止盈", Code: "600519", Name: "", Level: "止盈", GeneratedAt: base},
		{ID: "000001", Code: "000001", Name: "平安", Level: "提示", GeneratedAt: base},
	})

	s.RefreshNameByCode("600519", "贵州茅台")

	// 600519 两条都应刷新为权威名（含空名）
	// English: both 600519 entries should be refreshed to the authoritative name (including the empty one)
	for _, m := range s.List() {
		if m.Code == "600519" {
			if m.Name != "贵州茅台" {
				t.Fatalf("600519 名称应刷新为 贵州茅台, got %q", m.Name)
			}
		}
		if m.Code == "000001" && m.Name != "平安" {
			t.Fatalf("其他代码名称不应被改动, got %q", m.Name)
		}
	}

	// 空 name 不操作：不 panic 且不受影响
	// English: empty name does nothing: no panic and no effect
	s.RefreshNameByCode("600519", "")

	// 重新从磁盘加载验证持久化
	// English: reload from disk to verify persistence
	s2 := NewMessageStore(path)
	for _, m := range s2.List() {
		if m.Code == "600519" && m.Name != "贵州茅台" {
			t.Fatalf("持久化后名称应为 贵州茅台, got %q", m.Name)
		}
	}
}

// TestPurgeShortLevel 验证 PurgeShortLevel：仅清除做空方向消息（Level/Direction=做空），
// 不写墓碑（deleted_keys），做多/其他方向消息不受影响，可持久化。
// English: TestPurgeShortLevel verifies PurgeShortLevel: only clears short-direction messages (Level/Direction=做空),
// English: writes no tombstones (deleted_keys), long/other-direction messages are unaffected, and it can be persisted.
func TestPurgeShortLevel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "messages.json")
	s := NewMessageStore(path)
	base := time.Now()
	s.Sync([]MessageItem{
		{ID: "000001@做空", Code: "000001", Name: "平安", Level: "做空", Direction: "做空", GeneratedAt: base},
		{ID: "000002@交易信号@动量", Code: "000002", Name: "", Level: "交易信号", Direction: "做空", GeneratedAt: base},
		{ID: "600519@交易信号@动量", Code: "600519", Name: "茅台", Level: "交易信号", Direction: "做多", GeneratedAt: base},
		{ID: "000003@减仓", Code: "000003", Name: "", Level: "减仓", Direction: "提醒", GeneratedAt: base},
	})

	if n := s.PurgeShortLevel(); n != 2 {
		t.Fatalf("应清除 2 条做空, got %d", n)
	}
	got := s.List()
	if len(got) != 2 {
		t.Fatalf("应剩 2 条, got %d: %+v", len(got), got)
	}
	for _, m := range got {
		if m.Level == "做空" || m.Direction == "做空" {
			t.Fatalf("做空消息不应残留: %+v", m)
		}
	}
	if len(s.file.DeletedKeys) != 0 {
		t.Fatalf("不应写墓碑(切回可重同步), got %v", s.file.DeletedKeys)
		// English: no tombstones should be written (so it can be re-synced)
	}
	if len(s.file.Messages) != 2 {
		t.Fatalf("持久化消息数应 2, got %d", len(s.file.Messages))
	}
}
