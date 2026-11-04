// lane_alive_b6_test.go — §B6（2026-11-05 通知增强）必推通道在位读数与宕机播报复用。
//
// 钉两条：① PushLaneAlive 在"一条通道都没配"时判 false（否则出站方会以为消息送到了手机）；
// ② AnnounceChannelDown 复用 §C9 既有的"存活通道播报宕机"，只走站内 WS/Webhook，
// 不经网关扇出——报丧鸟自己不能把自己再点一遍（递归防护）。
package notify

import (
	"testing"
	"time"
)

// fakeGateway 记录网关收到几条消息，用来证明宕机播报没有再走网关那一路。
// English: fakeGateway counts gateway deliveries, proving the announcement skips the gateway lane.
type fakeGateway struct{ sent []Message }

func (g *fakeGateway) Send(msg Message) error {
	g.sent = append(g.sent, msg)
	return nil
}

// TestPushLaneAliveReflectsConfiguredChannels 在位判据：什么都没配＝false，任一通道在位＝true。
func TestPushLaneAliveReflectsConfiguredChannels(t *testing.T) {
	n := New()
	if n.PushLaneAlive() {
		t.Fatal("一个通道都没配却判「通道在位」——必推消息会被当成已经送达")
	}
	// 站内 WS 一条就够（WS 是最后那条还能说话的通道）
	n.RegisterWS("u_1")
	if !n.PushLaneAlive() {
		t.Fatal("已有 WS 客户端却判通道不可用")
	}
	n.UnregisterWS("u_1")
	if n.PushLaneAlive() {
		t.Fatal("注销后仍判在位")
	}
	// 网关 / ntfy / Webhook 任一在位都算通道在位
	n.SetGateway(&fakeGateway{})
	if !n.PushLaneAlive() {
		t.Fatal("配置了推送网关却判通道不可用")
	}
	n.SetGateway(nil)
	n.SetWebhooks([]string{"http://127.0.0.1:1/hook"})
	if !n.PushLaneAlive() {
		t.Fatal("配置了 Webhook 却判通道不可用")
	}
	n.SetWebhooks(nil)
	if n.PushLaneAlive() {
		t.Fatal("清空全部通道后仍判在位")
	}
	// nil 接收器不许 panic（出站方可能在引擎未装配时拿到 nil）
	var nilNotifier *Notifier
	if nilNotifier.PushLaneAlive() {
		t.Error("nil 推送器不该判在位")
	}
}

// TestAnnounceChannelDownUsesSurvivingChannel 宕机播报走站内存活通道，不碰网关。
func TestAnnounceChannelDownUsesSurvivingChannel(t *testing.T) {
	n := New()
	gw := &fakeGateway{}
	n.SetGateway(gw) // 网关"在位但已短路"：播报不许再往它身上压消息
	ch := n.RegisterWS("u_1")

	n.AnnounceChannelDown("必推通道不可用", "网关/ntfy/Webhook 均不在位")

	select {
	case msg := <-ch:
		if msg.Level != LevelHigh {
			t.Errorf("宕机播报应是高级别，实得 %v", msg.Level)
		}
		if msg.Title == "" || msg.Content == "" {
			t.Errorf("播报少了标题或正文，值班读不出因果：%+v", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("必推通道全 trip 时没有用存活通道播报（§5A.3 第 3 条）")
	}
	// 网关一路不经：AnnounceChannelDown 只走 fanoutLocal（递归防护，同 §C9）
	if len(gw.sent) != 0 {
		t.Errorf("宕机播报又压给了网关（%+v）——会把一次故障放大成队列风暴", gw.sent)
	}
	// nil 接收器不许 panic
	var nilNotifier *Notifier
	nilNotifier.AnnounceChannelDown("x", "y")
}
