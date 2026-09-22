// windowed_fail_trace_test.go — §M-8/N-6（2026-09-22 PM 批）：逐窗装配失败必须留下降级行。
// 旧实现五处 BuildPanels 失败静默 continue，IC/触发率/反推结论照常基于缺窗样本产出，
// 与全窗成功完全同形。noteWindowFail 是这一族的统一留痕闸，行为在此钉死。
// English: §M-8/N-6 — window assembly failures must emit a degraded line; the old bare `continue`
// made a missing-window result indistinguishable from a full-range one.
package research

import (
	"bytes"
	"errors"
	"log"
	"strings"
	"testing"
)

// TestNoteWindowFailTrace noteWindowFail 行为锁：有失败必打降级行、零失败不打扰。
func TestNoteWindowFailTrace(t *testing.T) {
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(old)

	noteWindowFail("复合IC", 12, 3, errors.New("build panels: disk I/O timeout"))
	got := buf.String()
	if !strings.Contains(got, "复合IC") || !strings.Contains(got, "3/12") || !strings.Contains(got, "降级") {
		t.Fatalf("缺窗应打出「阶段+失败数/总数+降级」留痕行，got %q", got)
	}
	if !strings.Contains(got, "disk I/O timeout") {
		t.Fatalf("留痕行应带首个失败原因供定位, got %q", got)
	}
	buf.Reset()
	noteWindowFail("反推泛化", 12, 0, nil)
	if buf.Len() != 0 {
		t.Fatalf("零失败不得打降级行（免噪音），got %q", buf.String())
	}
}
