package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDefaultHithinkTmpPathCrossPlatform 锁定 hithink dump 默认临时路径的跨平台契约：
// 必须基于 os.TempDir() 动态解析（Windows/Linux 各自可写目录），且不得回退成写死的
// "/tmp/..." Linux 绝对路径——广州迁移（Windows 生产机）后写死路径让 dump 从未落盘成功，
// ths_daily 停摆近一个月才定位（2026-09-16）。
func TestDefaultHithinkTmpPathCrossPlatform(t *testing.T) {
	got := defaultHithinkTmpPath()
	want := filepath.Join(os.TempDir(), "hithink_dump.parquet")
	if got != want {
		t.Fatalf("默认临时路径应为 os.TempDir 派生, got %q want %q", got, want)
	}
	if !filepath.IsAbs(got) {
		t.Fatalf("默认临时路径必须为绝对路径, got %q", got)
	}
	// 防回归：不允许写死 Linux 风格 "/tmp/" 前缀（Windows 上该目录不存在）。
	if strings.HasPrefix(got, "/tmp/") {
		t.Fatalf("禁止写死 Linux /tmp 路径, got %q", got)
	}
	if filepath.Base(got) != "hithink_dump.parquet" {
		t.Fatalf("文件名应保持 hithink_dump.parquet, got %q", got)
	}
}
