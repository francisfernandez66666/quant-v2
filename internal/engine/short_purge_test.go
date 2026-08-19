package engine

import (
	"path/filepath"
	"testing"
	"time"

	"quant-trading-v2/internal/data"
)

// TestSetShortEnabledPurgesShortAlerts 验证做空开关关闭(回到仅做多)时，消息中心残留的
// 做空方向消息(Level/Direction=做空)会被立即清除，而开启做空时不清除。
// English: TestSetShortEnabledPurgesShortAlerts verifies that when the short toggle is turned OFF (back to long-only), stale short-direction messages remaining in the message center (Level/Direction=short) are purged immediately, while turning short on does not purge.
// simulates a stale short message left behind by a previous test or historical run,
// then asserts turning the short toggle OFF purges exactly those short entries.
func TestSetShortEnabledPurgesShortAlerts(t *testing.T) {
	e := &Engine{msgStore: data.NewMessageStore(filepath.Join(t.TempDir(), "messages.json"))}
	base := time.Now()
	e.msgStore.Sync([]data.MessageItem{
		{ID: "000001@做空", Code: "000001", Name: "平安", Level: "做空", Direction: "做空", GeneratedAt: base},
		{ID: "600519@交易信号@动量", Code: "600519", Name: "茅台", Level: "交易信号", Direction: "做空", GeneratedAt: base},
		{ID: "000003@减仓", Code: "000003", Name: "", Level: "减仓", Direction: "提醒", GeneratedAt: base},
	})

	// 开启做空开关不应清理任何消息
	// English: Enabling the short toggle should not purge any messages.
	e.SetShortEnabled(true)
	if n := len(e.msgStore.List()); n != 3 {
		t.Fatalf("开启做空后应保留 3 条, got %d", n)
	}

	// 关闭做空开关(仅做多)应清除 2 条做空方向消息
	// English: Turning the short toggle off (long-only) should purge 2 short-direction messages.
	e.SetShortEnabled(false)
	got := e.msgStore.List()
	if len(got) != 1 {
		t.Fatalf("关闭做空后应剩 1 条, got %d: %+v", len(got), got)
	}
	if got[0].ID != "000003@减仓" {
		t.Fatalf("应为 000003@减仓, got %q", got[0].ID)
	}
}
