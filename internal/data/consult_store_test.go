package data

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestConsultStoreAppendList 追加 user/assistant 消息后应能按正序取回全部对话。
func TestConsultStoreAppendList(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "consult_history.json")
	s := NewConsultStore(path)

	s.Append("user", "第一问")
	s.Append("assistant", "第一答")
	s.Append("user", "第二问")

	got := s.List()
	if len(got) != 3 {
		t.Fatalf("期望 3 条消息，实际 %d", len(got))
	}
	if got[0].Role != "user" || got[0].Content != "第一问" {
		t.Fatalf("首条应正序为 user/第一问，实际 %+v", got[0])
	}
	if got[2].Role != "user" || got[2].Content != "第二问" {
		t.Fatalf("末条应为 user/第二问，实际 %+v", got[2])
	}

	// 落盘后可重新加载
	s2 := NewConsultStore(path)
	got2 := s2.List()
	if len(got2) != 3 {
		t.Fatalf("重新加载后应保留 3 条，实际 %d", len(got2))
	}
}

// TestConsultStoreCrossTradingDayClears 跨交易日后历史应自动清空。
// 通过伪造上一次交易日字段模拟隔日重启,验证读取时清空旧会话。
func TestConsultStoreCrossTradingDayClears(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "consult_history.json")
	s := NewConsultStore(path)
	s.Append("user", "昨日提问")

	after := NewConsultStore(path)
	if len(after.List()) != 1 {
		t.Fatalf("同日新实例应读到旧对话,实际 %d 条", len(after.List()))
	}

	// 伪造历史来自"昨天"：把已加载文件的交易日改成昨日
	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	after.file.TradingDay = yesterday

	// 追加时应感知交易日不匹配并清空旧会话
	after.Append("user", "今日提问")
	got := after.List()
	if len(got) != 1 {
		t.Fatalf("跨交易日后应仅剩今日本条,实际 %d 条", len(got))
	}
	if got[0].Content != "今日提问" {
		t.Fatalf("跨日后残留旧消息,实际 %+v", got[0])
	}
}

// TestConsultStoreClear 清空后当日历史应为空。
func TestConsultStoreClear(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "consult_history.json")
	s := NewConsultStore(path)
	s.Append("user", "提问")
	s.Append("assistant", "回答")

	s.Clear()
	got := s.List()
	if len(got) != 0 {
		t.Fatalf("清空后应为 0 条,实际 %d", len(got))
	}

	// 重新加载确认已持久化清空
	s2 := NewConsultStore(path)
	if len(s2.List()) != 0 {
		t.Fatalf("清空应持久化,重载后仍 %d 条", len(s2.List()))
	}
}

// TestConsultStoreFilePersisted 消息应真正写盘（检查文件存在且内容可解析）。
func TestConsultStoreFilePersisted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "consult_history.json")
	s := NewConsultStore(path)
	s.Append("user", "写盘验证")

	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatalf("持久化文件未生成: %s", path)
	}
}
