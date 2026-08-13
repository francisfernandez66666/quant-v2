// P1 清仓/止损强提醒推送：新告警首次出现时推送，重复出现不重复推。
// （Push tests for P1 close-out/stop-loss strong alerts: pushed once on first appearance, quiet on repeats.）
package engine

import (
	"testing"
	"time"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/notify"
)

func item(id, code, level, body string) data.MessageItem {
	return data.MessageItem{ID: id, Code: code, Name: code, Level: level, Body: body, GeneratedAt: time.Now()}
}

// TestPushCriticalFirstOnly 同一去重键的清仓/止损告警仅在首次出现时推送。
func TestPushCriticalFirstOnly(t *testing.T) {
	e := &Engine{msgStore: data.NewMessageStore(""), notifier: notify.New()}
	ch := e.notifier.RegisterWS("t1")
	defer e.notifier.UnregisterWS("t1")

	items := []data.MessageItem{
		item("600001@清仓", "600001", "清仓", "N形硬止损"),
		item("600002@止损", "600002", "止损", "破MA5"),
	}
	e.pushCriticalAlerts(items)
	e.msgStore.Sync(items)

	for i := 0; i < 2; i++ {
		select {
		case m := <-ch:
			if m.Level != notify.LevelHigh {
				t.Errorf("P1 告警应 LevelHigh, got %+v", m)
			}
		case <-time.After(200 * time.Millisecond):
			t.Errorf("第 %d 条新告警未推送", i+1)
		}
	}

	// 再次推送相同键：消息中心已存在，不再重复推送
	e.pushCriticalAlerts(items)
	select {
	case m := <-ch:
		t.Errorf("重复告警不应再推送, got %+v", m)
	case <-time.After(200 * time.Millisecond):
	}
}

// TestPushCriticalSkipsLowLevel 非清仓/止损级别（如持仓提示）不推送强提醒。
func TestPushCriticalSkipsLowLevel(t *testing.T) {
	e := &Engine{msgStore: data.NewMessageStore(""), notifier: notify.New()}
	ch := e.notifier.RegisterWS("t1")
	defer e.notifier.UnregisterWS("t1")

	e.pushCriticalAlerts([]data.MessageItem{item("600001@持仓提示", "600001", "持仓提示", "持仓中")})
	select {
	case m := <-ch:
		t.Errorf("非关键级别不应推送, got %+v", m)
	case <-time.After(200 * time.Millisecond):
	}
}

// TestPushCriticalNilNotifier 未配置推送器时静默跳过，不 panic。
func TestPushCriticalNilNotifier(t *testing.T) {
	e := &Engine{msgStore: data.NewMessageStore("")}
	e.pushCriticalAlerts([]data.MessageItem{item("600001@清仓", "600001", "清仓", "N形硬止损")})
}
