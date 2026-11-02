// friend_leg_alert_test.go — §FRIEND-LEG（2026-11-02）远端执行腿告警三件套的回归锁。
//
// 钉四件事（都是本仓被锤过的形状）：
//  1. 规则在册且判据是 ge 2 —— 0（这台机器没有远端腿）与 1（远端腿可达）都必须在阈值下，
//     否则运营单机部署天天响、启动到首轮探测之间那几秒也会响；
//  2. 路由表**显式**列它为 RoutePush（漏条目会掉进 DefaultRoute，owner 改默认路由时它会被悄悄
//     挪道——§高-3 要消灭的正是"注册了但没人管它去哪儿"）；
//  3. 文案按值分形（值=2 查链路 / 值=3 人去停腿清待发）——两档处置方向不同，
//     折成一句就是让人拿着"ping 不通"的结论去做"停那台机器"的动作，或反过来；
//  4. 0 恒不触发、2 持续满 For 窗才触发（防毛刺），回落成对销案。
package metrics

import (
	"strings"
	"testing"
	"time"
)

// friendLegGauge 与 engine/scoring_loop.go 的写端逐字同名（键名打错＝又一条永不触发的死规则）。
const friendLegGauge = "friend_leg_state"

// friendLegRule 从默认规则表里取远端腿那条。
func friendLegRule(t *testing.T) AlertRule {
	t.Helper()
	for _, r := range DefaultAlertRules() {
		if r.Name == "friend_leg_unavailable" {
			return r
		}
	}
	t.Fatalf("默认规则表里没有 friend_leg_unavailable——它会变成一条永不触发的死规则")
	return AlertRule{}
}

// TestFriendLegRuleShape 规则的判据/等级/指标名/文案四要素。
func TestFriendLegRuleShape(t *testing.T) {
	r := friendLegRule(t)
	if r.Metric != friendLegGauge {
		t.Errorf("规则读的键名是 %q，写端是 %q（两边必须同源，打错就是死规则）", r.Metric, friendLegGauge)
	}
	if r.Op != "ge" || r.Threshold != 2 {
		t.Errorf("判据应为 ge 2（0=没有远端腿、1=可达都不得触发），got %s %v", opText(r.Op), r.Threshold)
	}
	if r.For != "300s" {
		t.Errorf("For 应为 300s（对面重启客户端/瞬时断 overlay 会在一两分钟内自愈），got %q", r.For)
	}
	if r.Level != "p2" {
		t.Errorf("Level 应为 p2，got %q", r.Level)
	}
	// 文案必须按值分形：两档的处置方向完全不同（查链路 vs 人工停腿清待发）。
	for _, needle := range []string{"值=2", "值=3"} {
		if !strings.Contains(r.Message, needle) {
			t.Fatalf("告警文案缺少 %s 那一档的处置方向（折成一句就是把盲操作交给读者）：%s", needle, r.Message)
		}
	}
}

// TestFriendLegRuleRoutedPush 路由表显式列它，且是必推。
func TestFriendLegRuleRoutedPush(t *testing.T) {
	cfg := DefaultAlertRouting()
	rt, ok := cfg.Routes["friend_leg_unavailable"]
	if !ok {
		t.Fatal("friend_leg_unavailable 未在路由表中显式列出（缺条目＝走默认路由，owner 改默认时会被漏掉）")
	}
	if rt != RoutePush {
		t.Errorf("远端腿失联/撤销未触达属「此刻正疼、钱在对面手上」，应必推，got %s", rt)
	}
	// 等值锁的腿：路由表条目数必须仍等于规则数（本包 alert_routing_test 也有同一把，这里补一枚
	// 针对本批的形状锁——加规则忘加路由时，红在这里而不是在读者眼睛上）。
	if len(cfg.Routes) != len(DefaultAlertRules()) {
		t.Errorf("路由表 %d 条 ≠ 规则表 %d 条", len(cfg.Routes), len(DefaultAlertRules()))
	}
}

// TestFriendLegZeroNeverFires 值 0（这台机器上没有远端腿）与 1（可达）在任意时长下都不触发——
// 这一条就是"运营单机部署永不报红"的机器锁：今天没人配过 remote_leg，量规恒 0。
func TestFriendLegZeroNeverFires(t *testing.T) {
	rule := friendLegRule(t)
	rules := []AlertRule{rule}
	for _, v := range []int64{0, 1} {
		a := NewAlerter()
		base := time.Now()
		for i := 0; i <= 20; i++ {
			a.now = func() time.Time { return base.Add(time.Duration(i) * time.Minute) }
			if evs := a.Evaluate(rules, map[string]int64{friendLegGauge: v}); len(evs) != 0 {
				t.Fatalf("值 %d 第 %d 分钟不应有任何事件（0/1 都在阈值下），got %v", v, i, evs)
			}
		}
	}
}

// TestFriendLegTwoFiresAfterForWindow 值 2 未满 For 不 fire、满窗 fire 一次、回落到 1 成对销案；
// 值 3 走同一条判据（撤销未触达同样必推）。
func TestFriendLegTwoFiresAfterForWindow(t *testing.T) {
	rule := friendLegRule(t)
	rules := []AlertRule{rule}
	for _, v := range []int64{2, 3} {
		a := NewAlerter()
		base := time.Now()
		vals := map[string]int64{friendLegGauge: v}

		a.now = func() time.Time { return base }
		if evs := a.Evaluate(rules, vals); len(evs) != 0 {
			t.Fatalf("值 %d 首轮（未满 300s）不应 fire，got %v", v, evs)
		}
		a.now = func() time.Time { return base.Add(299 * time.Second) }
		if evs := a.Evaluate(rules, vals); len(evs) != 0 {
			t.Fatalf("值 %d 差 1 秒未满窗不应 fire，got %v", v, evs)
		}
		a.now = func() time.Time { return base.Add(301 * time.Second) }
		evs := a.Evaluate(rules, vals)
		if len(evs) != 1 || evs[0].Kind != "fire" {
			t.Fatalf("值 %d 满 300s 应 fire 一次，got %v", v, evs)
		}
		// 去重：持续破线不刷屏。
		a.now = func() time.Time { return base.Add(400 * time.Second) }
		if evs := a.Evaluate(rules, vals); len(evs) != 0 {
			t.Fatalf("值 %d 已 fire 后重复评估不得再推，got %v", v, evs)
		}
		// 回落（腿恢复可达，或键被摘掉）→ 成对销案。
		vals[friendLegGauge] = 1
		a.now = func() time.Time { return base.Add(420 * time.Second) }
		evs = a.Evaluate(rules, vals)
		if len(evs) != 1 || evs[0].Kind != "recover" {
			t.Fatalf("值 %d 回落应 recover 一次，got %v", v, evs)
		}
	}
}
