package data

import (
	"testing"
)

// TestConfrontationStoreAppendList 政策反制事件追加后可读回，且重启后保留。
func TestConfrontationStoreAppendList(t *testing.T) {
	dir := t.TempDir()
	store := NewConfrontationStore(dir + "/confrontation.json")
	store.Append(ConfrontationEvent{
		Title: "中国宣布对美加征关税实施精准反制", Content: "反制内容", Datetime: "2026-08-05 10:00:00",
		Sectors: []string{"稀土永磁", "农业"}, Direction: "利空", Impact: "高", Source: "政策反制",
	})
	if got := store.List(); len(got) != 1 {
		t.Fatalf("政策反制事件应=1条, got %d", len(got))
	}
	ev := store.List()[0]
	if ev.Direction != "利空" || ev.Impact != "高" || ev.Source != "政策反制" {
		t.Errorf("事件字段不符: %+v", ev)
	}
	if len(ev.Sectors) != 2 || ev.Sectors[0] != "稀土永磁" {
		t.Errorf("板块不符: %v", ev.Sectors)
	}
	// 重启后保留
	reload := NewConfrontationStore(dir + "/confrontation.json")
	if got := reload.List(); len(got) != 1 {
		t.Fatalf("重启后应保留1条, got %d", len(got))
	}
}

// TestConfrontationStoreHasTitle 已有相同标题的事件不重复。
func TestConfrontationStoreHasTitle(t *testing.T) {
	dir := t.TempDir()
	store := NewConfrontationStore(dir + "/confrontation.json")
	if store.HasTitle("不存在") {
		t.Fatal("空存储 HasTitle 应为 false")
	}
	store.Append(ConfrontationEvent{Title: "中国宣布对美加征关税实施精准反制", Direction: "利空", Impact: "高", Source: "政策反制"})
	if !store.HasTitle("中国宣布对美加征关税实施精准反制") {
		t.Fatal("追加后 HasTitle 应为 true")
	}
	if store.HasTitle("其他标题") {
		t.Fatal("不同标题 HasTitle 应为 false")
	}
}
