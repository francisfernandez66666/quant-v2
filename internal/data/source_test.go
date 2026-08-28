package data

import (
	"testing"
)

// TestSourceNameTracksLastSuccess 验证 SourceName 在 GetQuote 命中后返回实际源，
// 而不是硬编码的 "Sina"（P1-10 回归测试）。
func TestSourceNameTracksLastSuccess(t *testing.T) {
	dc := NewDataCoordinator(nil, nil)
	if got := dc.SourceName(); got != "" {
		t.Fatalf("未命中行情时期望空串，得到 %q", got)
	}

	// 直接模拟命中不同源
	dc.setLastSource("hithink")
	if got := dc.SourceName(); got != "hithink" {
		t.Fatalf("期望 hithink，得到 %q", got)
	}

	dc.setLastSource("eastmoney")
	if got := dc.SourceName(); got != "eastmoney" {
		t.Fatalf("期望 eastmoney，得到 %q", got)
	}
}
