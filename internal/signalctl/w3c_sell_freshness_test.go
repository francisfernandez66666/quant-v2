// w3c_sell_freshness_test.go — §0926E2E-11A（2026-09-26 三波）已确认处置的重放新鲜度闸。
//
// 缺陷 11 定性（二复核收窄后）：sellStates 纯内存、重启即清，跨进程无旧账复活；真实窗口=
// 同进程 sell_unified_mode shadow→on 热切换时，shadow 期已 Confirmed 的状态被直接重放进
// live 执行。修法 A：confirmedReplay 前置"确认当日（北京日历日）"闸——过期结论不执行、
// 状态机复位按最新价格重裁。本文件钉四件事：
//
//	① 当日重放照旧（幂等兜底语义不回归）；
//	② 跨北京日历日不过期重放——不得再出处置单，且当轮即重开观察窗（重裁而非留裸）；
//	③ 无时间戳的确认态（闸上线前遗留/测试直插）一律按过期处理；
//	④ 日界按北京时区判定，与宿主机时区无关。
package signalctl

import (
	"testing"
	"time"

	"quant-trading-v2/internal/cntime"
)

// dtime 指定北京日 09-XX 日 h:m 时刻（2026-09 内跨日快捷构造）。
func dtime(day, h, m int) time.Time {
	return time.Date(2026, 9, day, h, m, 0, 0, sellTestLoc)
}

func TestW3cSameDayReplayStillWorks(t *testing.T) {
	c := New()
	in := sellIn("600031", 9.3) // -7% 触止损线
	if d := judge(c, "600031", in, dtime(21, 10, 0)); d != nil {
		t.Fatalf("首触不应即卖：%+v", d)
	}
	d := judge(c, "600031", in, dtime(21, 10, 15))
	if d == nil || d.Action != SellActionTrim {
		t.Fatalf("窗结算应 trim，得 %+v", d)
	}
	// 同日晚些重放：仍是同一张 trim 处置单（日级幂等兜底语义）
	d2 := judge(c, "600031", in, dtime(21, 14, 0))
	if d2 == nil || d2.Action != SellActionTrim {
		t.Fatalf("当日重放必须与首结论一致，得 %+v", d2)
	}
}

func TestW3cCrossDayConfirmationIsNotReplayed(t *testing.T) {
	c := New()
	in := sellIn("600032", 9.3)
	judge(c, "600032", in, dtime(21, 10, 0))
	if d := judge(c, "600032", in, dtime(21, 10, 15)); d == nil {
		t.Fatal("前置：首日窗结算必须已确认")
	}
	s := stateOf(t, c, "600032")
	if !s.Confirmed || s.ConfirmedAt.IsZero() || cntime.DayOf(s.ConfirmedAt) != "2026-09-21" {
		t.Fatalf("前置：确认戳应为 09-21，实得 %+v", s)
	}
	// 次日 10:00 探针：昨天的 trim 不得今天照单执行
	v := c.JudgeSellView(ChannelLive, "u1", in, Policy{}, dtime(22, 10, 0))
	if v.Disposal != nil {
		t.Fatalf("跨日陈旧结论被重放进执行：%+v", v.Disposal)
	}
	// 复位即重裁：价格仍触线 → 重新锁线开窗（不裸奔、不再信旧结论）
	s2 := stateOf(t, c, "600032")
	if s2.Confirmed || s2.Settled || !s2.ConfirmedAt.IsZero() || s2.Line != SellLineStopLoss {
		t.Fatalf("过期确认后状态机应复位并重锁新线，实得 %+v", s2)
	}
	if s2.SettleStart.IsZero() || cntime.DayOf(s2.SettleStart) != "2026-09-22" {
		t.Fatalf("新结算点必须落在当日栅格，实得 %v", s2.SettleStart)
	}
	// 当日窗结算按最新价格重新出结论（链条完整可用，非一死了之）
	d := judge(c, "600032", in, s2.SettleStart)
	if d == nil || d.Action != SellActionTrim {
		t.Fatalf("次日窗结算应按现价重裁出 trim，得 %+v", d)
	}
}

func TestW3cUndatedConfirmationNeverReplays(t *testing.T) {
	c := New()
	in := sellIn("600033", 9.3)
	judge(c, "600033", in, dtime(21, 10, 0))
	if d := judge(c, "600033", in, dtime(21, 10, 15)); d == nil {
		t.Fatal("前置：应已确认")
	}
	// 模拟"新鲜度闸上线前遗留的无时间戳确认态"（同包特权直改）
	stateOf(t, c, "600033").ConfirmedAt = time.Time{}
	v := c.JudgeSellView(ChannelLive, "u1", in, Policy{}, dtime(21, 10, 20))
	if v.Disposal != nil {
		t.Fatalf("无确认时刻的存量态一律不得重放进执行（宁重裁不背书）：%+v", v.Disposal)
	}
}

func TestW3cDayBoundaryIsBeijingNotHostTZ(t *testing.T) {
	// 同一北京日：确认戳用 UTC 表示（09-21 02:15 UTC = 10:15 CST），探针时刻 CST 同日 11:00
	// ——若按宿主机时区/朴素 time.Time 日期比较会误判跨日，这里钉死"北京日历日"口径。
	if !sellConfirmedStale(time.Now().AddDate(0, 0, -1), time.Now()) {
		t.Fatal("昨天确认今天必须判过期")
	}
	utcSame := time.Date(2026, 9, 21, 2, 15, 0, 0, time.UTC) // = 09-21 10:15 北京
	cstSame := dtime(21, 11, 0)
	if sellConfirmedStale(utcSame, cstSame) {
		t.Fatal("同一北京日不同时区表示不得判过期")
	}
	cstNext := dtime(22, 0, 30) // 北京已过零点 → 次日
	if !sellConfirmedStale(utcSame, cstNext) {
		t.Fatal("跨北京零点即过期（00:30 的探针不为昨天结论背书）")
	}
}
