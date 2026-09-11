// e2e 测试时钟固定：CI(UTC) 与本地(Asia/Shanghai) 差 8 小时曾导致增量 D1 复用、
// 盘前批量窗口等测试偶发 FAIL。全仓语义按 A 股北京墙钟设计，这里统一把 time.Local
// 固定为 Asia/Shanghai；缺 tzdata 时（极端精简容器）跳过保持原行为。
package e2e

import (
	"os"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if loc, err := time.LoadLocation("Asia/Shanghai"); err == nil {
		time.Local = loc
	}
	os.Exit(m.Run())
}
