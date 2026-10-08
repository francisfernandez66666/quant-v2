// settlement_not_verified_test.go — §P2-E（2026-10-06 修复批 波 5）告警面三把锁。
//
// 缺陷原文：三方对账的三条出口里，「执行器不支持交割单」与「网关未连接」两条是**静默跳过**
// （返回 (nil, nil)），而 SettleDay 旧契约还把差异量规写成 0，于是"今天压根没比对"在告警面
// 上长得和"今天比对干净"一模一样（§0929 全量审计 ⑪-4 同族：该有的数没了）。
//
// 修法是量规 settlement_state 三态（0 未跑 / 1 已验证 / 2 网关未连接 / 3 执行器不支持）
// + 规则 settlement_not_verified（ge 2 → p2）+ 路由 RouteDaily（§CAL-GATE 判例：
// 持续性状态走必推会每 30 分钟刷满）。本文件从被消费侧把这三件套钉死：
//  1. 规则已注册且判据/等级/持续窗都对（注册了但判据写错＝永不触发或天天触发）；
//  2. 路由表**显式**列出 RouteDaily（漏列会掉进 DefaultRoute=RoutePush，即"注册了但走错道"）；
//  3. 阈值按语义成立：已验证(1)与未跑(0)在任何时长下都不触发；未验证(2/3)要连续满 300s 才 fire、
//     fire 后不重复、回到 1 成对 recover（否则告警只 fire 不 recover 等于没有恢复通知）。
//
// English: §P2-E alerting locks — the settlement "not verified" state must have a registered rule,
// an explicit RouteDaily entry, and threshold semantics that stay quiet for 0/1 while a sustained
// 2/3 fires exactly once after the 300s window and recovers when verification returns.
package metrics

import (
	"testing"
	"time"
)

// settlementStateGauge 量规键名，与 trading/settlement.go 里的字面量严格同源
// （§DEADGAUGE 纪律：键名不用 const 别名，别名会让"通用守卫扫字面量"失明）。
const settlementStateGauge = "settlement_state"

// settlementNotVerifiedRule 从默认规则表里取「对账未验证」那条。
func settlementNotVerifiedRule(t *testing.T) AlertRule {
	t.Helper()
	for _, r := range DefaultAlertRules() {
		if r.Metric == settlementStateGauge {
			return r
		}
	}
	t.Fatalf("默认规则表里没有量规 %s 对应的规则——三态量规会退化成「有读数、无告警」的死指标", settlementStateGauge)
	return AlertRule{}
}

// TestSettlementNotVerifiedRuleRegisteredAndRouted 锁 1+2：规则注册（ge 2 / p2 / For 300s）
// 且在路由表里显式列为日汇总。
func TestSettlementNotVerifiedRuleRegisteredAndRouted(t *testing.T) {
	r := settlementNotVerifiedRule(t)
	if r.Name != "settlement_not_verified" {
		t.Errorf("规则名应为 settlement_not_verified（路由表按名索引），got %q", r.Name)
	}
	// ge 2 是唯一正确判据：0（当日还没到对账时刻）与 1（已验证）都不能报，
	// 2/3（未连接/不支持）都必须报。写成 gt 2 会漏掉"网关未连接"这条最常见的现网形态。
	if r.Op != "ge" || r.Threshold != 2 {
		t.Errorf("判据应为 ge 2（2/3 都算未验证），got %s%g", opText(r.Op), r.Threshold)
	}
	if r.Level != "p2" {
		t.Errorf("等级应为 p2（当天要知道、不必当场打断），got %q", r.Level)
	}
	if r.For != "300s" {
		t.Errorf("For 应为 300s（重启首轮对账恰好赶上网关拨号中的毛刺不该立刻报警），got %q", r.For)
	}
	rt, ok := DefaultAlertRouting().Routes[r.Name]
	if !ok {
		t.Fatalf("规则 %s 未在路由表中显式列出——会掉进 DefaultRoute，日汇总口径悄悄变成必推", r.Name)
	}
	if rt != RouteDaily {
		t.Errorf("未验证属持续性状态，应 RouteDaily（走必推会每 30 分钟一条刷满），got %s", rt)
	}
}

// TestSettlementNotVerifiedThresholdSemantics 锁 3：注入时钟跑纯评估器，
// 逐值验「0/1 永不触发、2/3 满窗触发一次、回 1 成对销案」。
func TestSettlementNotVerifiedThresholdSemantics(t *testing.T) {
	rule := settlementNotVerifiedRule(t)
	_, restore := captureLog(t) // 评估器会 opslog 留痕，测试里只关心返回值
	t.Cleanup(restore)
	var elapsed int64 // 假时钟累计秒数（同包测试可直接注入 Alerter.now）
	a := NewAlerter()
	a.now = func() time.Time {
		return time.Unix(1_700_000_000, 0).Add(time.Duration(elapsed) * time.Second)
	}
	eval := func(v int64) []AlertEvent {
		return a.Evaluate([]AlertRule{rule}, map[string]int64{settlementStateGauge: v})
	}
	countKind := func(evs []AlertEvent, kind string) int {
		n := 0
		for _, e := range evs {
			if e.Kind == kind {
				n++
			}
		}
		return n
	}

	// ① 已验证(1) 与 未跑(0)：连续 10 轮（≈20 分钟）都不许 fire。
	for _, v := range []int64{1, 0, 1, 0} {
		elapsed += 120
		if n := countKind(eval(v), "fire"); n != 0 {
			t.Fatalf("量规=%d 属「不适用/已验证」，不得触发（ge 2 判据被写坏的形状），fire=%d", v, n)
		}
	}

	// ② 未验证(2)：不满窗不 fire（防重启拨号毛刺），满 300s 才 fire 且只 fire 一次。
	elapsed += 60
	if n := countKind(eval(2), "fire"); n != 0 {
		t.Fatalf("刚越界不满 For=300s 不得 fire（毛刺防抖失效），got %d", n)
	}
	elapsed += 240 // 累计 300s
	evs := eval(2)
	if n := countKind(evs, "fire"); n != 1 {
		t.Fatalf("越界满 300s 应 fire 1 条，got %d（%+v）", n, evs)
	}
	// 3（执行器不支持）与 2 同属未验证：持续期内不得重复 fire，也不得插入假 recover。
	elapsed += 60
	if n := countKind(eval(3), "fire"); n != 0 {
		t.Fatalf("已处于触发态不得重复 fire（去重失效＝刷屏），got %d", n)
	}

	// ③ 回到已验证(1)：必须成对 recover，否则告警永远停在触发态。
	elapsed += 60
	evs = eval(1)
	if n := countKind(evs, "recover"); n != 1 {
		t.Fatalf("恢复验证应发 1 条 recover，got %d（%+v）", n, evs)
	}
	if n := countKind(evs, "fire"); n != 0 {
		t.Fatalf("销案本轮不得同时 fire，got %d", n)
	}
}
