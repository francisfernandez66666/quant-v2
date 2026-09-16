package data

import (
	"testing"
	"time"
)

// TestHithinkDumpDownloadTimeoutFloor 锁定 dump 下载超时下限（≥30 分钟）。
// 历史故障：DownloadDumpFile 与行情接口共用 30s 超时客户端，10 年全量 daily-k dump
// （数百 MB）必被 context deadline 掐断，ths_daily 空洞长期补不上（2026-09-16 修复）。
// 若后续改动把该超时收紧回分钟级，本测试即失败。
func TestHithinkDumpDownloadTimeoutFloor(t *testing.T) {
	if hithinkDumpDownloadTimeout < 30*time.Minute {
		t.Fatalf("dump 下载超时必须 ≥30min, got %v", hithinkDumpDownloadTimeout)
	}
}
