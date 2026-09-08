// 实盘持仓止盈/止损/减仓建议 → 消息中心 + P1 强提醒（与 auto_sell 开关无关）：
// 用户关闭自动交易时，实时持仓触发止盈止损判定仍能收到强提醒手动处理。
// English: live-position TP/SL/trim advices → message center + P1 strong push, independent of the
// auto-sell switch — with auto trading off, a live TP/SL trip still gets a strong reminder to act.
package engine

import (
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/notify"
	"quant-trading-v2/internal/trading"
)

// liveAdvice 构造一条实盘持仓处理建议（测试辅助）。
// English: builds one live-position advice (test helper).
func liveAdvice(code, name, action, reason string) trading.PositionAdvice {
	return trading.PositionAdvice{
		Code:        code,
		Name:        name,
		Action:      action,
		Level:       "高",
		RefPrice:    10.5,
		ProfitPct:   -6.8,
		DrawdownPct: -6.8,
		Reason:      reason,
		Source:      "discipline",
		Strategy:    "n_shape",
		GeneratedAt: time.Now(),
	}
}

// TestSyncLiveAdviceAlertsStrongPush 止损级实盘建议 → P1 LevelHigh 强提醒 + 私有消息落库 +
// 同日重复静默（交易日键去重，5s 循环不轰炸）。
// English: stop-loss live advice → one P1 LevelHigh strong push + private-scope message persisted +
// quiet on same-trading-day repeat (day-key dedup keeps the 5s loop silent).
func TestSyncLiveAdviceAlertsStrongPush(t *testing.T) {
	e := &Engine{msgStore: data.NewMessageStore(""), notifier: notify.New()}
	ch := e.notifier.RegisterWS("live")
	defer e.notifier.UnregisterWS("live")

	adv := []trading.PositionAdvice{liveAdvice("600519", "贵州茅台", "止损", "止损窗结算无信号，止损离场")}
	e.syncLiveAdviceAlerts("u123", adv, false)

	// 首次出现：P1 LevelHigh 桌面/Webhook 强提醒
	// English: first appearance → one P1 LevelHigh alert.
	select {
	case m := <-ch:
		if m.Level != notify.LevelHigh {
			t.Errorf("止损强提醒应为 LevelHigh, got %+v", m)
		}
		if !strings.Contains(m.Title, "止损") || !strings.Contains(m.Content, "手动处理") {
			t.Errorf("强提醒内容不符: %+v", m)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("止损建议未触发强提醒")
	}

	// 消息落库且私有作用域（Scope=主账号），标题/级别/正文齐全
	// English: message persisted at private scope with full title/level/body.
	list := e.msgStore.ListVisible("u123")
	if len(list) != 1 {
		t.Fatalf("应落库 1 条私有消息, got %d", len(list))
	}
	it := list[0]
	if it.Scope != "u123" || it.Level != "止损" || it.Action != "卖出" {
		t.Errorf("消息字段不符: %+v", it)
	}
	if !strings.Contains(it.Body, "止损") || !strings.Contains(it.Body, "手动处理") {
		t.Errorf("消息正文应带纪律原因与手动处理提示: %s", it.Body)
	}

	// 同日重复：交易日键已存在 → 不再重复推送
	// English: same-day repeat → day key already present → silent.
	e.syncLiveAdviceAlerts("u123", adv, false)
	select {
	case m := <-ch:
		t.Errorf("同日重复不应再推强提醒, got %+v", m)
	case <-time.After(200 * time.Millisecond):
	}
}

// TestSyncLiveAdviceAlertsAutoActiveTag auto 开启时正文带"已触发自动卖出"、Scope 公共账号不发私有前缀。
// English: when auto-sell is active the body carries 已触发自动卖出; empty sendTo stays public-scoped.
func TestSyncLiveAdviceAlertsAutoActiveTag(t *testing.T) {
	e := &Engine{msgStore: data.NewMessageStore("")}
	adv := []trading.PositionAdvice{liveAdvice("000001", "平安银行", "止盈", "止盈窗结算无信号，止盈离场")}
	e.syncLiveAdviceAlerts("10086", adv, true)

	list := e.msgStore.ListVisible("10086")
	if len(list) != 1 {
		t.Fatalf("应落库 1 条, got %d", len(list))
	}
	if !strings.Contains(list[0].Body, "已触发自动卖出") {
		t.Errorf("auto 开启应标注已触发自动卖出, got: %s", list[0].Body)
	}
	if !strings.HasPrefix(list[0].ID, "u10086|discipline@000001@止盈@") {
		t.Errorf("私有作用域 ID 应带 u<uid>|discipline@ 前缀, got: %s", list[0].ID)
	}
}

// TestSyncLiveAdviceAlertsTrimPushes 减仓（纪律首触止损未深破→半平）同样触发 P1 强提醒。
// English: 减仓/trim (discipline first-SL-touch half-out) also fires a P1 strong alert.
func TestSyncLiveAdviceAlertsTrimPushes(t *testing.T) {
	e := &Engine{msgStore: data.NewMessageStore(""), notifier: notify.New()}
	ch := e.notifier.RegisterWS("live")
	defer e.notifier.UnregisterWS("live")

	e.syncLiveAdviceAlerts("u123", []trading.PositionAdvice{liveAdvice("600519", "贵州茅台", "减仓", "首触止损未深破，减半仓")}, false)
	select {
	case m := <-ch:
		if m.Level != notify.LevelHigh {
			t.Errorf("减仓强提醒应为 LevelHigh, got %+v", m)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("减仓建议未触发强提醒")
	}
}

// TestSyncLiveAdviceAlertsSkipsNonSell 加仓/格局/持有 建议不产生消息与提醒。
// English: add/hold/格局 advices produce no messages or alerts.
func TestSyncLiveAdviceAlertsSkipsNonSell(t *testing.T) {
	e := &Engine{msgStore: data.NewMessageStore(""), notifier: notify.New()}
	ch := e.notifier.RegisterWS("live")
	defer e.notifier.UnregisterWS("live")

	e.syncLiveAdviceAlerts("u123", []trading.PositionAdvice{
		liveAdvice("600001", "测试A", "加仓", "信号活跃且回撤可控"),
		liveAdvice("600002", "测试B", "格局", "盈利且趋势完好"),
		liveAdvice("600003", "测试C", "持有", "观察窗内等待信号"),
	}, false)

	if n := len(e.msgStore.ListVisible("u123")); n != 0 {
		t.Errorf("非卖出建议不应落库, got %d", n)
	}
	select {
	case m := <-ch:
		t.Errorf("非卖出建议不应强提醒, got %+v", m)
	case <-time.After(200 * time.Millisecond):
	}
}

// TestSyncLiveAdviceAlertsNoStorePerRound nil 消息存储/空建议 静默跳过，不 panic。
// English: nil message store or empty advices are silent no-ops without panicking.
func TestSyncLiveAdviceAlertsNoStorePerRound(t *testing.T) {
	e := &Engine{}
	e.syncLiveAdviceAlerts("u123", nil, false)
}
