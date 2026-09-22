// discipline_symmetry_test.go — §P0-C（2026-09-22 傍晚批，owner 裁决 2 = B 最小止血）对称行为锁。
//
// 锁的对象：旧执行通道（internal/trading/discipline.go，当前默认在跑）的**终态失明**——
// 窗结算命中"有信号 → 延持"时落 Settled=true/Confirmed=false，旧实现在函数顶部早退，
// 该持仓之后每轮都在此 return nil，永久不再被任何判定线评估（止损/止盈/深破全盲 = 资金级漏卖）。
//
// 本文件把同一条时间线**分别喂两条通道**（旧：probeDiscipline；新：signalctl.Controller.JudgeSell），
// 断言两侧最终都产出离场处置。它同时防"双实现漂移"与"只修一边"：
//   - 修复前：新通道绿（延持只顺延结算点、不落 Settled），旧通道红（延持后全程沉默、末轮不出卡）；
//   - 修复后：两侧都绿。
//
// 附带资金外溢守卫锁：延持态只有"本轮仍在破线且不轻于原始锁定线"时才重新出卡，
// 价格反弹回盈利区不得按锁定的止损线出卡（见 reevalAllowsSettle）。
package trading

import (
	"fmt"
	"testing"
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/signalctl"
)

// symLoc 固定时区锚（两通道结算栅格都以 09:30 为原点，测试须用可预期时区）。
var symLoc = time.FixedZone("CST", 8*3600)

// symAt 构造 2026-09-22 的 hh:mm 时刻。
func symAt(hh, mm int) time.Time {
	return time.Date(2026, 9, 22, hh, mm, 0, 0, symLoc)
}

// symStep 时间线上的一轮探针输入（两通道共用同一份）。
type symStep struct {
	at    time.Time
	price float64
	bull  bool // 有活跃做多信号
	bear  bool // 有做空/利空信号
}

// symOutcome 单轮裁决结果归一化：是否出离场处置（全平/半平）、动作名。
type symOutcome struct {
	round    int
	at       time.Time
	disposal bool
	action   string
}

// runOldChannel 用旧通道内核 probeDiscipline 跑完整时间线（状态跨轮传递，等价于
// DisciplineTracker.ProbeAll 每轮喂入同一持仓）。entry=100 固定成本。
// English: drives the whole timeline through the legacy kernel, carrying PositionState
// across rounds exactly like DisciplineTracker does.
func runOldChannel(t *testing.T, steps []symStep) []symOutcome {
	t.Helper()
	cfg := config.DefaultDisciplineConfig()
	var st *PositionState
	out := make([]symOutcome, 0, len(steps))
	for i, s := range steps {
		high := 100.0
		if st != nil {
			high = st.HighPrice
		}
		next, d := probeDiscipline(st, "600922", 100, high, s.price, s.bull, s.bear, s.at, cfg)
		st = &next
		o := symOutcome{round: i + 1, at: s.at}
		if d != nil && (d.Action == ActionClose || d.Action == ActionTrim) {
			o.disposal = true
			o.action = d.Action.String()
		}
		out = append(out, o)
	}
	return out
}

// runNewChannel 用新通道（卖出统一裁决层）跑同一份时间线。利空一律按"单源验证"喂入
// （BearVerifiedSingle 无即时硬清资格），以免新通道在第②步的即时硬清把两侧时序彻底错开
// ——本锁断言的是"延持之后会不会永久失明"，不是硬清时序。
// English: drives the same timeline through the unified sell kernel; bear hits are fed as
// single-source so the new channel's immediate hard-clear does not mask the blindness check.
func runNewChannel(t *testing.T, steps []symStep) []symOutcome {
	t.Helper()
	c := signalctl.New()
	pol := signalctl.Policy{Discipline: config.DefaultDisciplineConfig()}
	out := make([]symOutcome, 0, len(steps))
	for i, s := range steps {
		in := signalctl.SellInput{
			Code:       "600922",
			EntryPrice: 100,
			CurPrice:   s.price,
			Bull:       signalctl.SignalFresh{Active: s.bull, At: s.at},
			Bear:       signalctl.BearConfirm{Hit: s.bear, Verified: signalctl.BearVerifiedSingle},
		}
		d := c.JudgeSell(signalctl.ChannelLive, "sym-u1", in, pol, s.at)
		o := symOutcome{round: i + 1, at: s.at}
		if d != nil && (d.Action == signalctl.SellActionClose || d.Action == signalctl.SellActionTrim) {
			o.disposal = true
			o.action = d.Action
		}
		out = append(out, o)
	}
	return out
}

// dump 把逐轮结果拼成可读串（断言失败时贴出，便于比对两侧时序）。
func dump(ops []symOutcome) string {
	s := ""
	for _, o := range ops {
		verdict := "hold/无处置"
		if o.disposal {
			verdict = "离场处置(" + o.action + ")"
		}
		s += fmt.Sprintf("\n  第%d轮 %s → %s", o.round, o.at.Format("15:04"), verdict)
	}
	return s
}

// assertBothExit 对称性主断言：两侧最终都必须产出离场处置，且末轮都出卡。
// 修复前旧通道在延持后一路沉默 → 本函数第一条即红（正是本批要的那条锁）。
func assertBothExit(t *testing.T, name string, oldOps, newOps []symOutcome) {
	t.Helper()
	oldExit, newExit := 0, 0
	for _, o := range oldOps {
		if o.disposal {
			oldExit++
		}
	}
	for _, o := range newOps {
		if o.disposal {
			newExit++
		}
	}
	// ① 延持之后不得失明：两侧都至少出过一次离场处置。
	if newExit == 0 {
		t.Fatalf("%s：新通道未产出离场处置（对照基线本身失效）\n%s", name, dump(newOps))
	}
	if oldExit == 0 {
		t.Fatalf("%s：旧通道延持后永久失明——整条时间线零处置（§P0-C 终态失明）\n旧通道:%s\n新通道:%s", name, dump(oldOps), dump(newOps))
	}
	// ② 末轮（信号消失 + 跌到深破）两侧都必须在场上出卡，不接受"只剩静默"。
	if last := oldOps[len(oldOps)-1]; !last.disposal {
		t.Fatalf("%s：旧通道末轮仍无处置，终态失明未修复\n旧通道:%s", name, dump(oldOps))
	}
	if last := newOps[len(newOps)-1]; !last.disposal {
		t.Fatalf("%s：新通道末轮无处置（对照基线漂移，需复核语义）\n新通道:%s", name, dump(newOps))
	}
	// ③ 末轮两侧都必须是全平离场：深破兜底不给延持、也不给半平。
	if act := oldOps[len(oldOps)-1].action; act != "close" {
		t.Fatalf("%s：旧通道末轮处置动作应为全平 close，得 %s\n旧通道:%s", name, act, dump(oldOps))
	}
	if act := newOps[len(newOps)-1].action; act != signalctl.SellActionClose {
		t.Fatalf("%s：新通道末轮处置动作应为全平 close，得 %s", name, act)
	}
}

// TestDisciplineSymmetryTakeProfitExtendThenDeepBreach §P0-C 对称行为用例（盈利线延持支）：
// 触止盈线 → 进观察窗 → 窗内有做多信号导致延持 → 信号消失 → 跌到深破。
// 两侧在同一份输入上必须同轮出卡（本支时序天然对齐）。
func TestDisciplineSymmetryTakeProfitExtendThenDeepBreach(t *testing.T) {
	steps := []symStep{
		{at: symAt(10, 0), price: 116},                           // +16% 触止盈线 → 锁定判定线+结算点 10:15
		{at: symAt(10, 15), price: 108, bull: true},              // 结算轮有做多信号 → 延持（旧: Settled&&!Confirmed）
		{at: symAt(10, 35), price: 87, bull: false, bear: false}, // 信号消失 + 砸到 −13% 深破
		{at: symAt(10, 40), price: 86},                           // 已确认处置重放
	}
	oldOps := runOldChannel(t, steps)
	newOps := runNewChannel(t, steps)
	// 延持轮（第 2 轮）两侧都不得出卡：修复不得把延持语义改成"当轮即卖"。
	if oldOps[1].disposal || newOps[1].disposal {
		t.Fatalf("延持轮两侧都不应出离场处置\n旧通道:%s\n新通道:%s", dump(oldOps), dump(newOps))
	}
	assertBothExit(t, "止盈延持→深破", oldOps, newOps)
}

// TestDisciplineSymmetryDeepBreachExtendThenBlindness §P0-C 对称行为用例（损失线延持支，
// 最要命的一支）：旧路口径下深破窗结算若窗内有利空信号 → 延持 → 落 Settled&&!Confirmed
// → 修复前该持仓永久不再被任何判定线评估（FIX#12 事故形态）。
func TestDisciplineSymmetryDeepBreachExtendThenBlindness(t *testing.T) {
	steps := []symStep{
		{at: symAt(10, 0), price: 87},               // −13% 触深破兜底 → 首触锁定，结算点 10:15
		{at: symAt(10, 15), price: 86, bear: true},  // 窗内有利空 → 旧路口径延持
		{at: symAt(10, 20), price: 85, bear: false}, // 利空消失、价格继续跌 → 必须重新被评估
		{at: symAt(10, 30), price: 84},              // 更深
		{at: symAt(10, 35), price: 83},              // 末轮：两侧都要在场上
	}
	oldOps := runOldChannel(t, steps)
	newOps := runNewChannel(t, steps)
	assertBothExit(t, "深破延持→失明", oldOps, newOps)
	// 旧通道修复点：延持之后的破线轮（第 3 轮）必须已经出卡，不能等到末轮才醒。
	if !oldOps[2].disposal {
		t.Fatalf("旧通道延持后第 3 轮（仍深破）应出离场处置\n旧通道:%s", dump(oldOps))
	}
}

// TestDisciplineExtendHoldGuardNoSellOnRecovery §P0-C(B) 资金外溢守卫锁：
// 延持态不得放宽成"任意一轮都可能卖"——价格反弹回止盈区（原始止损破线条件已消失）时
// 必须继续沉默，只有仍破线（同族或风险升级）才重新进入结算处置。
func TestDisciplineExtendHoldGuardNoSellOnRecovery(t *testing.T) {
	cfg := config.DefaultDisciplineConfig()
	// 直接构造延持态：原始锁定线=止损线，结算点已过，窗内因反向信号延持。
	blind := &PositionState{
		Code: "600922", Line: LineStopLoss, FirstTouch: symAt(10, 0),
		SettleStart: symAt(10, 15), WindowMin: cfg.ExitConfirmMin,
		Settled: true, Confirmed: false, HighPrice: 100,
	}
	// ① 反弹进止盈区（+16%）：本轮命中的是止盈线，轻于原始止损线 → 不出卡。
	if _, d := probeDiscipline(blind, "600922", 100, blind.HighPrice, 116, false, false, symAt(10, 20), cfg); d != nil {
		t.Fatalf("延持后价格反弹进止盈区不得按止损线出卡，得 %+v", d)
	}
	// ② 回到无破线区（+2%）：本轮未命中任何线 → 不出卡。
	if _, d := probeDiscipline(blind, "600922", 100, 116, 102, false, false, symAt(10, 25), cfg); d != nil {
		t.Fatalf("延持后未破线轮不得出卡，得 %+v", d)
	}
	// ③ 仍破原始止损线（−7%）且反向信号消失 → 必须重新被结算处置（本批修复的目的）。
	_, d := probeDiscipline(blind, "600922", 100, 116, 93, false, false, symAt(10, 30), cfg)
	if d == nil || (d.Action != ActionClose && d.Action != ActionTrim) {
		t.Fatalf("延持后仍破止损线应重新出离场处置，得 %+v", d)
	}
}
