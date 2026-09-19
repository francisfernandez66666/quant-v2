// §RFIX-5 回归测试：crashMarkerLine 确定性崩溃特征提取（fail-fast 判定的纯函数核心）。
package scheduler

import (
	"strings"
	"testing"
)

func TestCrashMarkerLinePanicWithFrame(t *testing.T) {
	// 还原生产 #264 实录的子进程原始输出形态（rs.out 存裸行，[task#…] 前缀只存在于
	// scheduler 自身的 log.Printf，不进 crashMarkerLine 输入）。
	out := strings.Join([]string{
		"回测进度 90%",
		"panic: runtime error: index out of range [760] with length 760",
		"goroutine 1 [running]:",
		"quant-trading-v2/internal/btreplay.(*nShapeAdapter).Trigger(...)",
		"\t/Users/x/repo/internal/btreplay/replay.go:200 +0x57e",
	}, "\n")
	got := crashMarkerLine(out)
	if !strings.HasPrefix(got, "panic: runtime error: index out of range") {
		t.Fatalf("应提取 panic 首行，实际: %q", got)
	}
	if !strings.Contains(got, "replay.go:200") {
		t.Fatalf("应附带首个 .go: 栈帧，实际: %q", got)
	}
	if len(got) > 300 {
		t.Fatalf("摘要须截存 ≤300 字节，实际 %d", len(got))
	}
}

func TestCrashMarkerLineFatalAndNegative(t *testing.T) {
	if got := crashMarkerLine("x\nfatal error: all goroutines are asleep - deadlock!\n"); !strings.HasPrefix(got, "fatal error:") {
		t.Fatalf("fatal error 特征应命中，实际: %q", got)
	}
	for _, out := range []string{
		"",
		"exit status 1\n下载失败: connection refused\n",
		"panic 描述出现在普通文案里 panic: 不存在\n", // 无 "panic: runtime error:" 精确特征不误伤
	} {
		if got := crashMarkerLine(out); got != "" {
			t.Fatalf("普通失败输出不应命中崩溃特征，输入 %q 实际: %q", out, got)
		}
	}
}
