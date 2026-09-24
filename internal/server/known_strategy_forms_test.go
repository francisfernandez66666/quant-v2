// known_strategy_forms_test.go — §SURVEY-COVERAGE（2026-09-23 立，2026-09-24 §MOMENTUM-LIVE-REPLAY 改）
// 实盘白名单「形态战法」与 btreplay 可排摸清单的**等值锁**。
//
// 为什么钉这条：复权口径漂移这轮全靠 strategy-survey 排摸来找受损战法，而排摸只覆盖
// btreplay 真在回放的战法。白名单里能下单、排摸却量不到的战法，若差集又不报它，
// "没排摸"就长得和"排摸过且没问题"一模一样——只在 notes 里写一句话会被巡检 grep 漏掉。
// 这里锁死三件事：
// ①两边的形态战法 ID 集合必须逐字相等（server 多列/少列立刻红）；
// ②差集由 btreplay.UnsurveyedLiveForms() 如实算出。**2026-09-24 起为空集**——动量判据已按
//
//	实盘语义重写（兜底互斥 + 当日撮合 + 只计买入档），它现在是真被排摸的战法，不再是盲区。
//	空的是"当前没有盲区"，不是"这条链没接"：所以本文件同时钉住机制本身仍然活着
//
//	（UnsurveyedLiveFormStatus 对不存在/未实现的 ID 照旧报 no_replay_adapter），
//	下一个进白名单却没有适配器的战法仍会在此显形。
//
// ③momentum 必须仍在 BuiltinStrategies() 里，且**不在** DefaultDisabledBuiltins() 里
//
//	（表达"已实现且默认参与回放"）。用"从清单里删掉"来表达停用会把状态伪装成"还没人写"。
//
// 排摸据此打 survey_unsurveyable 锚点行。不允许出现"悄悄少了一行"的静默降级。
//
// English: an equality lock between the live whitelist's form strategies and the set btreplay
// replays by default; the difference must be reported explicitly with its status. Since the
// momentum criteria were rewritten to live semantics the set is empty — but the mechanism (and
// the status distinction between "no adapter" and "adapter disabled") stays under test.
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
	// 差集当前为空：动量已按实盘语义重写判据、真的进回放集合了。这条断言钉的是"盲区必须显形"
	// 而不是"动量有罪"——将来往 knownStrategyList 加形态战法却不补适配器，差集会多出一个 ID，
	// 仍在这里报出来。
	if missing := btreplay.UnsurveyedLiveForms(); len(missing) != 0 {
		t.Errorf("未纳入默认回放集合的实盘形态战法应为空集，得到 %v（新白名单战法缺适配器，或动量被判据重写回退了）", missing)
	}
	// 状态文字要能区分"没写适配器"（要补代码）与"写好但默认停用"（要么按实盘兜底语义重写判据、
	// 要么承认量不了）：压成同一个计数就会派错工。空差集会让这段代码永不自然执行，所以拿一个
	// 不存在的 ID 直接敲它——机制死了这里就红，而不是靠"没人报"蒙绿。
	if got := btreplay.UnsurveyedLiveFormStatus("form_that_nobody_implemented"); got != "no_replay_adapter" {
		t.Errorf("未实现战法的状态=%q，期望 no_replay_adapter（盲区分类机制失效）", got)
	}
	if got := btreplay.UnsurveyedLiveFormStatus("momentum"); got != "" {
		t.Errorf("momentum 的未排摸状态=%q，期望空串（判据已按实盘语义重写、默认参与回放）", got)
	}
	// 动量自己必须在"有适配器"的清单里（否则它只是从差集换成了消失，盲区换了个形态），
	// 并且不再出现在"默认停用"清单里（那会让排摸表的 enabled 字段说谎）。
	surveyed := false
	for _, id := range btreplay.BuiltinStrategies() {
		if id == "momentum" {
			surveyed = true
		}
	}
	if !surveyed {
		t.Error("momentum 不在 btreplay.BuiltinStrategies()：适配器被从清单里删了，停用状态会伪装成缺失")
	}
	for _, id := range btreplay.DefaultDisabledBuiltins() {
		if id == "momentum" {
			t.Error("momentum 仍在 DefaultDisabledBuiltins()：实盘语义判据已落地，这会让排摸行谎报「未跑」")
		}
	}
	// 反向：所有内置（含动量）必须都在白名单里（否则排摸在量不能交易的东西）。
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
