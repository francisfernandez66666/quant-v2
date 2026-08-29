// 文件：source_test.go
// 包名：data
// 所属模块：「行情与基础数据获取/清洗/计算」
// 模块职责：本文件属于 行情与基础数据获取/清洗/计算，负责该模块下的具体实现；
//           下文各函数/类型/方法均附有中文说明（用途、参数、返回值、副作用）。
// 说明：本文件仅补充注释，未改动任何原有代码逻辑。

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
