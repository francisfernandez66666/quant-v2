// sell_extend_cap_test.go — 延持次数上限守护测试（owner 2026-10-12 裁决，反例锁）。
//
// 背景（000063 中兴通讯事故）：止损触线（-6.4%）后战法 fac_138 持续喊买，旧口径
// 「反复延持=按设计拿着」一路延持 22 次（10:00→15:15），只靠 -12% 深破线兜底，回撤成本过高。
// 裁决：延持最多 MaxExtends 次（出厂默认 2），超限后即使做多信号仍在也按无信号处置
// （止损线→半平、止盈/移动止盈→全平），处置理由必须可归因（审计侧能分清"真没信号"与
// "信号在但延持额度用完"）。
// 三态口径锁：0=出厂默认 2（存量纪律快照缺键反序列化得 0，必须兜默认而非旁路）；
// >0=显式配置；<0=显式恢复旧"无上限"行为（逃生阀）。
// 摘锁反证：删掉 sellProbe ⑤ 的 capped 分支 → 本文件除逃生阀腿外全部转红。
//
// English: guard tests for the hold-extension cap (owner ruling 2026-10-12, after the 000063
// incident where a stop-loss hold was extended 22 times because the strategy kept shouting buy).
// Default cap = 2; explicit >0 configures; <0 restores the old unlimited behavior; the capped
// disposal reason must stay attributable. Removing the cap branch turns every leg but the
// escape-hatch one red.
package signalctl

import (
	"strings"
	"testing"

	"quant-trading-v2/internal/config"
)

// TestSellExtendCapDefaultsToTwo 默认纪律（Policy{} 零值=存量快照缺键兜默认）下最多延持 2 次：
// 第 3 次结算即使信号新鲜也按无信号处置（止损线=半平），且理由可归因、状态固化确认。
func TestSellExtendCapDefaultsToTwo(t *testing.T) {
	c := New()
	in := sellIn("600063", 9.3) // -7% 触 −6 止损线
	// 10:00 首触锁定 → 固定结算点 10:15。
	if d := judge(c, "600063", in, st(10, 0)); d != nil {
		t.Fatalf("首触不应处置: %+v", d)
	}
	// 第 1、2 次结算：新鲜做多信号 → 正常延持（10:15→10:30→10:45）。
	in.Bull = freshBull(st(10, 15))
	if d := judge(c, "600063", in, st(10, 15)); d != nil {
		t.Fatalf("第1次结算有信号应延持: %+v", d)
	}
	in.Bull = freshBull(st(10, 30))
	if d := judge(c, "600063", in, st(10, 30)); d != nil {
		t.Fatalf("第2次结算有信号应延持: %+v", d)
	}
	// 第 3 次结算：信号仍新鲜，但默认额度（2）用尽 → 按无信号处置，止损线首触未深破=半平。
	in.Bull = freshBull(st(10, 45))
	d := judge(c, "600063", in, st(10, 45))
	if d == nil || d.Action != SellActionTrim {
		t.Fatalf("延持达上限应按无信号处置（止损线=半平），得 %+v", d)
	}
	if !strings.Contains(d.Reason, "延持已达2次上限") {
		t.Fatalf("处置理由必须可归因（含延持上限说明），得 %q", d.Reason)
	}
	if s := stateOf(t, c, "600063"); !s.Settled || !s.Confirmed || s.Extends != 2 {
		t.Fatalf("达上限处置后应固化确认态且不虚增延持计数: %+v", s)
	}
}

// TestSellExtendCapExplicitConfig 显式配置 MaxExtends=1：第 2 次结算即按无信号处置。
func TestSellExtendCapExplicitConfig(t *testing.T) {
	c := New()
	pol := Policy{Discipline: config.DisciplineConfig{MaxExtends: 1}}
	in := sellIn("600064", 9.3)
	if d := c.JudgeSell(ChannelLive, "u1", in, pol, st(10, 0)); d != nil {
		t.Fatalf("首触不应处置: %+v", d)
	}
	in.Bull = freshBull(st(10, 15))
	if d := c.JudgeSell(ChannelLive, "u1", in, pol, st(10, 15)); d != nil {
		t.Fatalf("额度1的第1次结算应延持: %+v", d)
	}
	in.Bull = freshBull(st(10, 30))
	d := c.JudgeSell(ChannelLive, "u1", in, pol, st(10, 30))
	if d == nil || d.Action != SellActionTrim || !strings.Contains(d.Reason, "延持已达1次上限") {
		t.Fatalf("显式上限1应在第2次结算处置，得 %+v", d)
	}
}

// TestSellExtendUnlimitedEscapeHatch MaxExtends=-1（显式负数）=恢复旧"无上限"行为：
// 连续四格结算有信号均延持、永不处置（逃生阀口径锁；摘掉 capped 分支此腿仍绿，其余腿红）。
func TestSellExtendUnlimitedEscapeHatch(t *testing.T) {
	c := New()
	pol := Policy{Discipline: config.DisciplineConfig{MaxExtends: -1}}
	in := sellIn("600065", 9.3)
	if d := c.JudgeSell(ChannelLive, "u1", in, pol, st(10, 0)); d != nil {
		t.Fatalf("首触不应处置: %+v", d)
	}
	for i := 15; i <= 60; i += 15 { // 连续 4 格结算
		in.Bull = freshBull(st(10, i))
		if d := c.JudgeSell(ChannelLive, "u1", in, pol, st(10, i)); d != nil {
			t.Fatalf("无上限口径 %d 分结算不应处置: %+v", i, d)
		}
	}
	if s := stateOf(t, c, "600065"); s.Extends != 4 {
		t.Fatalf("无上限口径应累计延持 4 次，得 %d", s.Extends)
	}
}

// TestSellCapDeepBreachStillOverrides 优先序锁：延持额度用尽后砸穿深破线（-12%），
// 仍必须无条件全平（边界④不受上限口径影响），绝不落成"达上限半平"。
func TestSellCapDeepBreachStillOverrides(t *testing.T) {
	c := New()
	in := sellIn("600066", 9.3)
	if d := judge(c, "600066", in, st(10, 0)); d != nil {
		t.Fatalf("首触不应处置: %+v", d)
	}
	in.Bull = freshBull(st(10, 15))
	if d := judge(c, "600066", in, st(10, 15)); d != nil {
		t.Fatalf("第1次结算应延持: %+v", d)
	}
	in.Bull = freshBull(st(10, 30))
	if d := judge(c, "600066", in, st(10, 30)); d != nil {
		t.Fatalf("第2次结算应延持: %+v", d)
	}
	// 第 3 格砸到 -13%：深破升级 → 无条件全清，不走"达上限半平"分支。
	in.CurPrice = 8.7
	in.Bull = freshBull(st(10, 45))
	d := judge(c, "600066", in, st(10, 45))
	if d == nil || d.Action != SellActionClose || !strings.Contains(d.Reason, "深破") {
		t.Fatalf("深破应无条件全平（优先于延持上限），得 %+v", d)
	}
}
