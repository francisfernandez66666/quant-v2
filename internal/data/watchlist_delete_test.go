// §F-15 删号自选清理：先备份逐字节回验再删；空/越界 userID 拒绝。
package data

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWatchlistDeleteBackupThenRemove(t *testing.T) {
	dir := t.TempDir()
	w := NewWatchlistManager(dir)
	if !w.Add("u_test", "600000") {
		t.Fatalf("Add 失败")
	}
	orig, err := os.ReadFile(filepath.Join(dir, "watchlist_u_test.json"))
	if err != nil {
		t.Fatalf("read orig: %v", err)
	}

	removed, backup, err := w.Delete("u_test")
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if !removed {
		t.Fatalf("应报告 removed=true")
	}
	if _, err := os.Stat(filepath.Join(dir, "watchlist_u_test.json")); !os.IsNotExist(err) {
		t.Fatalf("自选文件仍存在: %v", err)
	}
	back, err := os.ReadFile(backup)
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if string(back) != string(orig) {
		t.Fatalf("备份内容与原文件不等")
	}
	if !strings.Contains(string(back), "600000") {
		t.Fatalf("备份缺代码: %s", back)
	}
}

func TestWatchlistDeleteGuards(t *testing.T) {
	dir := t.TempDir()
	w := NewWatchlistManager(dir)
	if _, _, err := w.Delete(""); err == nil {
		t.Fatalf("空 userID 必须拒绝（会删平台所有者 watchlist.json）")
	}
	if _, _, err := w.Delete("../evil"); err == nil {
		t.Fatalf("含路径分隔/.. 的 userID 必须拒绝")
	}
	// 文件不存在是合法态（missing），不算错误
	removed, backup, err := w.Delete("u_missing")
	if err != nil || removed || backup != "" {
		t.Fatalf("missing 形态应 (false,\"\",nil)，got (%v,%q,%v)", removed, backup, err)
	}
}
