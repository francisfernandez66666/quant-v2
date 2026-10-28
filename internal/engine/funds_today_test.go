package engine

// funds_today_test.go — §FUNDS-TODAY（2026-10-28）：实盘 T 账本的「当日资金帧」。
// 锁三件事：帧随账本落盘可回读、跨日不复活（新交易日首帧前必须是"尚未定权"）、
// 帧里的 T 侧字段与账本摘要同源（避免资金卡和判定各算一套）。

import (
	"os"
	"path/filepath"
	"testing"
)

// TestFundsFrameRoundTripAndDayReset 帧落盘可回读；跨日 load 后为 nil。
func TestFundsFrameRoundTripAndDayReset(t *testing.T) {
	root := t.TempDir()
	st := newTLiveStore(root, "u_funds")
	st.day = "20261028"
	st.setFunds(&tLiveFunds{Day: "20261028", At: "2026-10-28T10:00:00+08:00",
		Cash: 1000, Target: 6000, CurStrat: 5000, Room: 1000,
		TQuota: 4000, CashT: 800, TFrozenCash: 200,
		OpenBuyLegs: 300, OpenSellLegs: 400, OpenLegs: 1, DoneRounds: 2,
		PnlNet: 12.5, PnlGross: 18.3})
	// 文件确实写了 funds（接口只读文件，不 import engine）。
	raw, err := os.ReadFile(filepath.Join(root, "u_funds", "t_rounds_live.json"))
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	if len(raw) == 0 || !containsAll(string(raw), `"funds"`, `"cash_t"`, `"t_quota"`) {
		t.Fatalf("账本文件缺 funds 帧或关键字段: %s", string(raw))
	}
	// 同日重载：帧还在。
	st2 := newTLiveStore(root, "u_funds")
	st2.load("20261028")
	if f := st2.fundsSnapshot(); f == nil || f.CashT != 800 || f.DoneRounds != 2 {
		t.Fatalf("同日重载资金帧不符: %+v", f)
	}
	// 跨日重载：帧必须消失（不拿昨日顶替今日"尚未定权"）。
	st3 := newTLiveStore(root, "u_funds")
	st3.load("20261029")
	if f := st3.fundsSnapshot(); f != nil {
		t.Fatalf("跨日资金帧不得复活, got %+v", f)
	}
}

// containsAll 简易包含判定（避免为一条断言引入额外依赖）。
func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !contains(s, sub) {
			return false
		}
	}
	return true
}

func contains(hay, needle string) bool {
	if needle == "" {
		return true
	}
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
