// known_strategy_forms_test.go — §SURVEY-COVERAGE（2026-09-23）实盘白名单「形态战法」与
// btreplay 可排摸清单的**等值锁**。
//
// 为什么钉这条：复权口径漂移这轮全靠 strategy-survey 排摸来找受损战法，而排摸只覆盖
// btreplay 有回放适配器的战法。momentum 在实盘白名单里能下单、在排摸表里却根本没有那一行——
// "没排摸"长得和"排摸过且没问题"一模一样，只在 notes 里写一句话会被巡检 grep 漏掉。
// 这里锁死两件事：①两边的形态战法 ID 集合必须逐字相等（server 多列/少列立刻红）；
// ②差集必须由 btreplay.UnsurveyedLiveForms() 如实报出（当前=1，momentum），
// 排摸据此打 survey_unsurveyable 锚点行。将来给 momentum 补适配器时，本测试会变红一次，
// 提醒同步把锚点期望值改掉——不允许出现"悄悄少了一行"的静默降级。
//
// English: an equality lock between the live whitelist's form strategies and the set btreplay can
// survey; the difference must be reported explicitly as unsurveyable (momentum today).
package server

import (
	"sort"
	"testing"

	"quant-trading-v2/internal/btreplay"
)

func TestLiveWhitelistFormsMatchSurveyCoverage(t *testing.T) {
	s := &Server{} // researchDir 空 ⇒ knownStrategyList 退化为仅内置战法（不含 fac_*/pat_*）
	var live []string
	for _, k := range s.knownStrategyList() {
		if k.Kind == "form" {
			live = append(live, k.ID)
		}
	}
	want := btreplay.LiveFormStrategies()
	sort.Strings(live)
	sort.Strings(want)
	if len(live) != len(want) {
		t.Fatalf("实盘白名单形态战法 %+v 与 btreplay.LiveFormStrategies() %+v 数量不一致", live, want)
	}
	for i := range live {
		if live[i] != want[i] {
			t.Fatalf("实盘白名单形态战法与排摸口径不一致：位置 %d 为 %s vs %s（%+v / %+v）", i, live[i], want[i], live, want)
		}
	}
	// 差集必须被排摸侧如实报出（当前恰好 1 个：momentum）。
	missing := btreplay.UnsurveyedLiveForms()
	if len(missing) != 1 || missing[0] != "momentum" {
		t.Errorf("无回放适配器的实盘形态战法应为 [momentum]，得到 %v", missing)
	}
	// 反向：适配器覆盖的四个内置必须都在白名单里（否则排摸在量不能交易的东西）。
	inLive := map[string]bool{}
	for _, id := range want {
		inLive[id] = true
	}
	for _, id := range btreplay.BuiltinStrategies() {
		if !inLive[id] {
			t.Errorf("内置回放战法 %s 不在实盘白名单，排摸覆盖面与实盘脱节", id)
		}
	}
}
