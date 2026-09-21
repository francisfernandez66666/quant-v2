// sell_test.go — 卖出统一裁决内核（sell.go）分支矩阵测试（§SELLPOINT-UNIFY 2026-09-21）。
//
// 覆盖基准（docs/REFACTOR_UNIFIED_SELL_20260921.md §六/§七 owner 拍板语义）：
//   - 语义①：触止损线 + 利空双源验证 = 当轮即时硬清；单源/未验证利空无硬清资格；
//   - 语义②：延持只认「新鲜做多信号」（利空延持方向写反的旧缺陷回归锁）；
//   - 边界④：深破窗结算无条件全平，无视任何信号；
//   - 边界⑤：首触锁定判定线与固定栅格结算点，不随价格波动滚动；
//   - 边界⑥：做多信号超 SellSignalMaxAgeSec 视为无信号，不得延持；
//   - 旧缺陷b回归锁：延持后状态机持续运转——信号消失必处置，绝不永久失明。
package signalctl

import (
	"testing"
	"time"

	"quant-trading-v2/internal/config"
)

// sellTestLoc 北京时间锚（结算栅格以 09:30 为原点，测试须用可预期时区）。
var sellTestLoc = time.FixedZone("CST", 8*3600)

// st time 测试时刻快捷构造。
func st(h, m int) time.Time {
	return time.Date(2026, 9, 21, h, m, 0, 0, sellTestLoc)
}

// sellIn 构造单轮探针输入：成本固定 10 元，price 为现价。
func sellIn(code string, price float64) SellInput {
	return SellInput{Code: code, EntryPrice: 10, CurPrice: price}
}

// freshBull 活跃且新鲜的做多信号。
func freshBull(now time.Time) SignalFresh {
	return SignalFresh{Active: true, At: now}
}

// judge 用默认纪律参数跑一轮裁决。
func judge(c *Controller, code string, in SellInput, now time.Time) *SellDisposal {
	return c.JudgeSell(ChannelLive, "u1", in, Policy{}, now)
}

// stateOf 取内部状态（同包测试特权）。
func stateOf(t *testing.T, c *Controller, code string) *sellState {
	t.Helper()
	s, ok := c.sellStates[sellKey{ChannelLive, "u1", code}]
	if !ok {
		t.Fatalf("代码 %s 无裁决状态", code)
	}
	return s
}

// TestSellStopLossWindowSettleTrim 止损触线进入观察窗，窗结算无信号 → 半平（trim）。
// 09:30 锚定 15 分钟窗：10:00 首触 → 结算点 10:15。
func TestSellStopLossWindowSettleTrim(t *testing.T) {
	c := New()
	in := sellIn("600001", 9.3) // -7% 触 −6 止损线
	if d := judge(c, "600001", in, st(10, 0)); d != nil {
		t.Fatalf("首触不应即卖，得 %+v", d)
	}
	s := stateOf(t, c, "600001")
	if s.Line != SellLineStopLoss || !s.SettleStart.Equal(st(10, 15)) {
		t.Fatalf("首触锁定异常: %+v", s)
	}
	d := judge(c, "600001", in, st(10, 15))
	if d == nil || d.Action != SellActionTrim {
		t.Fatalf("窗结算无信号应半平，得 %+v", d)
	}
	// 留痕：处置必入裁定环（Stage=sell_discipline, Verdict=pass）。
	rec := c.Recent(5)
	found := false
	for _, r := range rec {
		if r.Stage == StageSell && r.Code == "600001" && r.Verdict == VerdictPass {
			found = true
		}
	}
	if !found {
		t.Fatal("处置未留痕")
	}
}

// TestSellTakeProfitFreshBullExtendsNoWindow 止盈线 + 新鲜做多信号 → 延持且不开窗（旧口径）。
// At 零值（未注入时刻）按新鲜放行，仅限测试/存量装配。
func TestSellTakeProfitFreshBullExtendsNoWindow(t *testing.T) {
	c := New()
	now := st(10, 0)
	in := sellIn("600002", 11.6) // +16% ≥ 止盈 15
	in.Bull = freshBull(now)
	if d := judge(c, "600002", in, now); d != nil {
		t.Fatalf("止盈+做多信号应延持，得 %+v", d)
	}
	s := stateOf(t, c, "600002")
	if s.Line != SellLineNone {
		t.Fatalf("止盈延持不应开窗，得线 %v", s.Line)
	}
	// 信号消失 → 下一轮才首触开窗。
	in2 := sellIn("600002", 11.6)
	if d := judge(c, "600002", in2, st(10, 5)); d != nil {
		t.Fatalf("首触轮不应处置，得 %+v", d)
	}
	s = stateOf(t, c, "600002")
	if s.Line != SellLineTakeProfit {
		t.Fatalf("信号消失应锁定止盈线，得 %v", s.Line)
	}
	// 零时刻宽松：At 零值按不过期。
	c2 := New()
	in3 := sellIn("600003", 11.6)
	in3.Bull = SignalFresh{Active: true}
	if d := judge(c2, "600003", in3, now); d != nil {
		t.Fatalf("At 零值应按新鲜延持，得 %+v", d)
	}
}

// TestSellBearDualImmediateHardClear 语义①：触止损线 + 双源验证利空 → 当轮全平，不等窗。
func TestSellBearDualImmediateHardClear(t *testing.T) {
	c := New()
	in := sellIn("600004", 9.3)
	in.Bear = BearConfirm{Hit: true, Verified: BearVerifiedDual}
	d := judge(c, "600004", in, st(10, 0))
	if d == nil || d.Action != SellActionClose {
		t.Fatalf("利空双源+触线应即时全平，得 %+v", d)
	}
	if !containsStr(d.Reason, "双源") {
		t.Fatalf("即时硬清理由应注明双源，得 %q", d.Reason)
	}
	// 未触线的利空（−4% 在 −6 线内）无处置资格。
	c2 := New()
	in2 := sellIn("600005", 9.6)
	in2.Bear = BearConfirm{Hit: true, Verified: BearVerifiedDual}
	if d2 := judge(c2, "600005", in2, st(10, 0)); d2 != nil {
		t.Fatalf("未触线利空不得处置，得 %+v", d2)
	}
}

// TestSellBearSingleNoHardClear 验证不足双源（单源/未验证）→ 无即时硬清资格，走正常窗。
// 旧缺陷a回归锁的另一面：延持只认做多——利空命中不得把结算推向延持。
func TestSellBearSingleNoHardClear(t *testing.T) {
	for _, level := range []string{BearVerifiedSingle, BearUnverified, ""} {
		c := New()
		in := sellIn("600006", 9.3)
		in.Bear = BearConfirm{Hit: true, Verified: level}
		if d := judge(c, "600006", in, st(10, 0)); d != nil {
			t.Fatalf("等级 %q 不应即时硬清，得 %+v", level, d)
		}
		// 窗结算：只有利空、没有做多 → 照常处置（trim），绝不因利空延持。
		if d := judge(c, "600006", in, st(10, 15)); d == nil || d.Action != SellActionTrim {
			t.Fatalf("等级 %q 窗结算应处置，得 %+v", level, d)
		}
	}
}

// TestSellStaleBullNoExtend 边界⑥：做多信号超龄（默认 300s）按无信号，窗结算必处置。
func TestSellStaleBullNoExtend(t *testing.T) {
	c := New()
	in := sellIn("600007", 9.3)
	if d := judge(c, "600007", in, st(10, 0)); d != nil {
		t.Fatalf("首触不应处置，得 %+v", d)
	}
	// 10:15 结算：信号产出时刻 10:05（10 分钟前 > 5 分钟上限）→ 陈旧不延持。
	in.Bull = freshBull(st(10, 5))
	if d := judge(c, "600007", in, st(10, 15)); d == nil {
		t.Fatal("超龄做多信号不得延持")
	} else if d.Action != SellActionTrim {
		t.Fatalf("超龄信号结算应半平，得 %+v", d)
	}
	// 对照：同结算时刻，信号产出 10:11（4 分钟前 ≤ 5 分钟上限）→ 新鲜延持。
	c2 := New()
	if d := judge(c2, "600007b", sellIn("600007b", 9.3), st(10, 0)); d != nil {
		t.Fatalf("对照轮首触不应处置，得 %+v", d)
	}
	in2 := sellIn("600007b", 9.3)
	in2.Bull = freshBull(st(10, 11))
	if d := judge(c2, "600007b", in2, st(10, 15)); d != nil {
		t.Fatalf("新鲜信号应延持，得 %+v", d)
	}
}

// TestSellExtendThenSettleDisposes 旧缺陷b回归锁：延持只顺延结算点，状态机持续运转——
// 下一格信号消失必须处置，绝不「一次延持永久失明」。
func TestSellExtendThenSettleDisposes(t *testing.T) {
	c := New()
	in := sellIn("600008", 9.3)
	judge(c, "600008", in, st(10, 0)) // 首触，结算点 10:15
	// 10:15 结算遇新鲜做多 → 延持一格到 10:30。
	in1 := in
	in1.Bull = freshBull(st(10, 15))
	if d := judge(c, "600008", in1, st(10, 15)); d != nil {
		t.Fatalf("有信号应延持，得 %+v", d)
	}
	s := stateOf(t, c, "600008")
	if !s.SettleStart.Equal(st(10, 30)) || s.Extends != 1 {
		t.Fatalf("延持应顺延一格且不落 Settled: %+v", s)
	}
	if s.Settled {
		t.Fatal("延持不得置 Settled（旧缺陷b的根源）")
	}
	// 10:30 信号消失 → 必须处置。
	if d := judge(c, "600008", in, st(10, 30)); d == nil || d.Action != SellActionTrim {
		t.Fatalf("延持后信号消失必须处置，得 %+v", d)
	}
}

// TestSellDeepBreachUnconditional 边界④：延持期跌破深破线（−12），窗结算无条件全平，无视做多信号。
func TestSellDeepBreachUnconditional(t *testing.T) {
	c := New()
	in := sellIn("600009", 9.3)
	judge(c, "600009", in, st(10, 0))
	inB := in
	inB.Bull = freshBull(st(10, 15))
	judge(c, "600009", inB, st(10, 15)) // 延持到 10:30
	// 10:30 现价 8.5（−15% ≤ 深破 −12）且仍有新鲜做多 → 仍然全平。
	deep := sellIn("600009", 8.5)
	deep.Bull = freshBull(st(10, 30))
	d := judge(c, "600009", deep, st(10, 30))
	if d == nil || d.Action != SellActionClose || d.Line != SellLineDeepBreach {
		t.Fatalf("深破结算必须无条件全平，得 %+v", d)
	}
	// 延持期利空升级双源 → 即时硬清同样生效（结算前当轮即拦）。
	c2 := New()
	judge(c2, "600010", sellIn("600010", 9.3), st(10, 0))
	inHard := sellIn("600010", 9.3)
	inHard.Bull = freshBull(st(10, 15))
	judge(c2, "600010", inHard, st(10, 15)) // 延持
	bearNow := sellIn("600010", 9.0)
	bearNow.Bear = BearConfirm{Hit: true, Verified: BearVerifiedDual}
	if d := judge(c2, "600010", bearNow, st(10, 20)); d == nil || d.Action != SellActionClose {
		t.Fatalf("延持期触线+双源利空应即时硬清，得 %+v", d)
	}
}

// TestSellFirstTouchFixedGrid 边界⑤：结算点首触锁定在 09:30 栅格，窗内价格反复不滚动、线不解锁。
func TestSellFirstTouchFixedGrid(t *testing.T) {
	c := New()
	in := sellIn("600011", 9.3)
	judge(c, "600011", in, st(10, 0))
	lock := stateOf(t, c, "600011").SettleStart
	// 10:05 价格回血（−2% 脱离触发区）：线仍锁定止损、结算点不动。
	if d := judge(c, "600011", sellIn("600011", 9.8), st(10, 5)); d != nil {
		t.Fatalf("窗内价格回血不应处置，得 %+v", d)
	}
	s := stateOf(t, c, "600011")
	if !s.SettleStart.Equal(lock) || s.Line != SellLineStopLoss {
		t.Fatalf("结算点/判定线必须锁定: %+v", s)
	}
	// 10:10 再跌回 −7%：也不重开新窗（同一结算点强制结算）。
	judge(c, "600011", sellIn("600011", 9.3), st(10, 10))
	if !stateOf(t, c, "600011").SettleStart.Equal(lock) {
		t.Fatal("窗内重复触线不得滚动重置")
	}
	// 10:15 到点强制结算（价格已回到 −2% 也照判——窗固定不无限延）。
	if d := judge(c, "600011", sellIn("600011", 9.8), st(10, 15)); d == nil {
		t.Fatal("到点必须强制结算")
	}
}

// TestSellTrailLine 移动止盈：有利润后自最高价回撤超阈值 → 45 分钟长窗；延持只认做多。
func TestSellTrailLine(t *testing.T) {
	c := New()
	in := sellIn("600012", 11.2)
	in.HighPrice = 12 // 最高回撤 6.7% ≥ 6，涨幅 12% 未触止盈线
	judge(c, "600012", in, st(10, 0))
	s := stateOf(t, c, "600012")
	if s.Line != SellLineTrail || s.WindowMin != 45 {
		t.Fatalf("应锁定移动止盈+45分钟窗: %+v", s)
	}
	// 结算点 09:30+45=10:15；遇新鲜做多 → 顺延一格到 11:00（45 分钟窗长）。
	inB := in
	inB.Bull = freshBull(st(10, 15))
	if d := judge(c, "600012", inB, st(10, 15)); d != nil {
		t.Fatalf("移动止盈遇做多应延持，得 %+v", d)
	}
	if got := stateOf(t, c, "600012").SettleStart; !got.Equal(st(11, 0)) {
		t.Fatalf("移动止盈延持应顺延一个 45 分钟窗格，结算点 %v", got)
	}
	// 11:00 无信号 → 止盈全平。
	if d := judge(c, "600012", in, st(11, 0)); d == nil || d.Action != SellActionClose {
		t.Fatalf("移动止盈结算无信号应全平，得 %+v", d)
	}
}

// TestSellConfirmedReplay 已确认处置每轮重放（执行层幂等兜底），动作与首结论一致。
func TestSellConfirmedReplay(t *testing.T) {
	c := New()
	in := sellIn("600013", 9.3)
	judge(c, "600013", in, st(10, 0))
	first := judge(c, "600013", in, st(10, 15)) // trim
	if first == nil {
		t.Fatal("结算未出处置")
	}
	again := judge(c, "600013", sellIn("600013", 9.2), st(10, 20))
	if again == nil || again.Action != SellActionTrim {
		t.Fatalf("确认态重放应保持半平，得 %+v", again)
	}
	// 重放期间更深（−13% ≤ 深破）：已确认止损仍是 trim，深破只在未确认前升级线。
	deep := judge(c, "600013", sellIn("600013", 8.7), st(10, 25))
	if deep == nil || deep.Action != SellActionTrim {
		t.Fatalf("已确认处置不得被后续轮次改写，得 %+v", deep)
	}
}

// TestSellInvalidPriceNoConclusion 停牌/行情缺失（价格非法）→ 不下结论且状态原样保留。
func TestSellInvalidPriceNoConclusion(t *testing.T) {
	c := New()
	in := sellIn("600014", 9.3)
	judge(c, "600014", in, st(10, 0))
	lock := stateOf(t, c, "600014").SettleStart
	if d := judge(c, "600014", sellIn("600014", 0), st(10, 15)); d != nil {
		t.Fatalf("无效价格不得处置，得 %+v", d)
	}
	if !stateOf(t, c, "600014").SettleStart.Equal(lock) {
		t.Fatal("无效价格轮不得改动状态")
	}
	// 新代码首见无效价：不落状态（不建僵尸记录）。
	if d := judge(c, "600015", sellIn("600015", 0), st(10, 0)); d != nil {
		t.Fatal("无效价格不应建态")
	}
	if _, ok := c.sellStates[sellKey{ChannelLive, "u1", "600015"}]; ok {
		t.Fatal("无效价格首见不得落状态")
	}
}

// TestSellPruneAndReset 平仓清理与全量重置：只删目标通道/账号，其余不受影响。
func TestSellPruneAndReset(t *testing.T) {
	c := New()
	judge(c, "600021", sellIn("600021", 9.3), st(10, 0))
	c.JudgeSell(ChannelPaper, "u1", sellIn("600021", 9.3), Policy{}, st(10, 0))
	c.JudgeSell(ChannelLive, "u2", sellIn("600022", 9.3), Policy{}, st(10, 0))
	c.PruneSellStates(ChannelLive, "u1", map[string]bool{"600099": true})
	if _, ok := c.sellStates[sellKey{ChannelLive, "u1", "600021"}]; ok {
		t.Fatal("live/u1 已不在持仓应被清理")
	}
	if _, ok := c.sellStates[sellKey{ChannelPaper, "u1", "600021"}]; !ok {
		t.Fatal("paper 通道状态不得被 live 清理误删")
	}
	if _, ok := c.sellStates[sellKey{ChannelLive, "u2", "600022"}]; !ok {
		t.Fatal("其他账号状态不得被误删")
	}
	c.ResetSell()
	if len(c.sellStates) != 0 {
		t.Fatal("ResetSell 应清空全部状态")
	}
}

// TestSellParamsFromConfig 前端纪律参数热改生效（自定义线/窗/信号新鲜期）。
func TestSellParamsFromConfig(t *testing.T) {
	c := New()
	pol := Policy{Discipline: config.DisciplineConfig{
		StopLossPct: 3, TakeProfitPct: 8, MaxPullbackPct: 4, DeepStopMult: 3,
		ExitConfirmMin: 30, TrailConfirmMin: 60, SellSignalMaxAgeSec: 60,
	}}
	// −5% 在自定义 −3 线下：触线。
	if d := c.JudgeSell(ChannelLive, "u1", sellIn("600031", 9.5), pol, st(10, 0)); d != nil {
		t.Fatalf("首触不应即卖，得 %+v", d)
	}
	// 信号产出已 5 分钟 > 60s 新鲜期 → 不延持，30 分钟窗（10:00→结算点 10:30）到点即判。
	in := sellIn("600031", 9.5)
	in.Bull = freshBull(st(10, 29))
	if d := c.JudgeSell(ChannelLive, "u1", in, pol, st(10, 34)); d == nil || d.Action != SellActionTrim {
		t.Fatalf("超新鲜期信号不得延持（自定义 60s），得 %+v", d)
	}
	// 双源利空即时硬清在自定义 −3 线下同样生效。
	in2 := sellIn("600032", 9.7) // −3% 触线
	in2.Bear = BearConfirm{Hit: true, Verified: BearVerifiedDual}
	if d := c.JudgeSell(ChannelLive, "u1", in2, pol, st(10, 0)); d == nil || d.Action != SellActionClose {
		t.Fatalf("自定义参数下双源利空应即时硬清，得 %+v", d)
	}
}

// containsStr 轻量子串断言（避免测试引 strings 依赖歧义）。
func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
