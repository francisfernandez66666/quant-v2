package data

import (
	"path/filepath"
	"testing"
	"time"
)

// TestRefreshNameByCode 验证 RefreshNameByCode：按代码刷新股票名称并持久化，
// 其他代码/其他字段不受影响；空 code 或 name 不操作。
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
	s.RefreshNameByCode("600519", "")

	// 重新从磁盘加载验证持久化
	s2 := NewMessageStore(path)
	for _, m := range s2.List() {
		if m.Code == "600519" && m.Name != "贵州茅台" {
			t.Fatalf("持久化后名称应为 贵州茅台, got %q", m.Name)
		}
	}
}
