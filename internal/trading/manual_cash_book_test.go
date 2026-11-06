// manual_cash_book_test.go — §MANUAL-BOOK-UNIFY（2026-11-06，owner 裁决 1）资金回落腿的行为锁。
//
// 这一条锁的是"柜台连不上就以手工维护的可用资金为准"到底成不成立，以及它在什么情况下**必须不成立**：
//
//	① 柜台新鲜 ⇒ source=counter，值与改前逐字相同（本批不许动正常链路，一个字节都不动）；
//	② 柜台陈旧 + 手动账当日可用 ⇒ source=manual、fresh=true（裁决 1 的正腿；没有它整个功能等于没接）；
//	③ 柜台陈旧 + 手动账 asof 停在昨天 ⇒ fresh=false，保持 §M12-A fail-close
//	   （"几天前手敲的余额"不许在下次桥断时顶上——方向是放钱，必须响亮地不可得）；
//	④ 柜台陈旧 + 手动账已被柜台接管且 asof 早于接管 ⇒ 同 ③（裁决 5 的防复活腿）；
//	⑤ 手动真 0 ⇒ fresh=true 且值就是 0（真 0 与不可得不得混写，§M12-A 那条纪律对手动维同样成立）。
//
// 时钟口径：判新鲜用的是**真实挂钟**（与改前 AvailableCash 的 time.Since 同形），
// 夹具的 newPinnedController 只钉时段闸那把钟，不许顺手把这里也钉——
// 一钉就会把"40 分钟前"种成"未来 4 小时"，fresh 恒真（§0925EVE-W3-F 同族事故）。
package trading

import (
	"testing"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/store"
)

// manualCashCtrl 构造一个带库的控制器，并返回库句柄。
func manualCashCtrl(t *testing.T, uid string) (*Controller, *store.DB) {
	t.Helper()
	db := testDB(t)
	return newPinnedController(guardServer(), db, uid, config.DefaultQMTConfig(), nil), db
}

// seedCounterCash 按"距今 age"种一行柜台资金。
func seedCounterCash(t *testing.T, db *store.DB, uid string, cash float64, age time.Duration) {
	t.Helper()
	if err := db.UpsertRealAccount(store.RealAccount{UserID: uid, AvailableCash: cash,
		UpdatedAt: time.Now().In(cntime.Loc).Add(-age).Format("2006-01-02 15:04:05")}); err != nil {
		t.Fatalf("seed counter cash: %v", err)
	}
}

// manualTodayAsof 今天的 asof（手动账可用性的前置判据之一）。
func manualTodayAsof() string { return cntime.DayOf(cntime.Now()) + " 09:35:00" }

// manualYesterdayAsof 昨天的 asof。
func manualYesterdayAsof() string {
	return cntime.DayOf(cntime.Now().AddDate(0, 0, -1)) + " 09:35:00"
}

func TestAvailableCashBookCounterWins(t *testing.T) {
	uid := "u_book1"
	ctrl, db := manualCashCtrl(t, uid)
	seedCounterCash(t, db, uid, 5000, time.Minute)
	if err := db.PutManualAccount(uid, 999999, true, 0, manualTodayAsof()); err != nil {
		t.Fatalf("写手动资金: %v", err)
	}
	// 柜台新鲜时，手动账哪怕数值差 200 倍也一个字不许参与（裁决 1 的前半句：以柜台为准）。
	cash, fresh, source := ctrl.AvailableCashBook()
	if cash != 5000 || !fresh || source != "counter" {
		t.Fatalf("柜台新鲜必须吃柜台，got (%.0f,%v,%q)", cash, fresh, source)
	}
}

func TestAvailableCashBookFallsBackToManual(t *testing.T) {
	uid := "u_book2"
	ctrl, db := manualCashCtrl(t, uid)
	seedCounterCash(t, db, uid, 5000, 40*time.Minute) // 桥断：柜台停在 40 分钟前
	if err := db.PutManualAccount(uid, 32000, true, 0, manualTodayAsof()); err != nil {
		t.Fatalf("写手动资金: %v", err)
	}
	cash, fresh, source := ctrl.AvailableCashBook()
	if cash != 32000 || !fresh || source != "manual" {
		t.Fatalf("柜台陈旧+手动当日必须回落到手动账，got (%.0f,%v,%q)", cash, fresh, source)
	}
	// AvailableCash 的老签名必须与 Book 同值（8 个调用点靠这条才自动吃到回落腿）。
	if c, f := ctrl.AvailableCash(); c != 32000 || !f {
		t.Fatalf("AvailableCash 转调后应与 Book 同值，got (%.0f,%v)", c, f)
	}
}

func TestAvailableCashBookStaleManualDoesNotUnlock(t *testing.T) {
	uid := "u_book3"
	ctrl, db := manualCashCtrl(t, uid)
	seedCounterCash(t, db, uid, 5000, 40*time.Minute)
	if err := db.PutManualAccount(uid, 32000, true, 0, manualYesterdayAsof()); err != nil {
		t.Fatalf("写昨天的手动资金: %v", err)
	}
	cash, fresh, source := ctrl.AvailableCashBook()
	if fresh || source != "none" {
		t.Fatalf("asof 停在昨天的手动账不许解开 §M12-A fail-close，got (%.0f,%v,%q)", cash, fresh, source)
	}
	if cash != 5000 {
		t.Fatalf("回落失败时必须仍带柜台原值供前端横幅展示（cash_stale 语义），got %.0f", cash)
	}
}

func TestAvailableCashBookTakenOverDoesNotResurrect(t *testing.T) {
	uid := "u_book4"
	ctrl, db := manualCashCtrl(t, uid)
	// 手动账是今天 09:35 敲的，随后柜台在 15:00 恢复回报并接管（裁决 5）。
	if err := db.PutManualAccount(uid, 32000, true, 0, manualTodayAsof()); err != nil {
		t.Fatalf("写手动资金: %v", err)
	}
	today := cntime.DayOf(cntime.Now())
	if _, err := db.MarkManualTakenOver(uid, today+" 15:00:00"); err != nil {
		t.Fatalf("接管打标: %v", err)
	}
	seedCounterCash(t, db, uid, 5000, 40*time.Minute) // 之后又断了
	cash, fresh, source := ctrl.AvailableCashBook()
	if fresh || source != "none" {
		t.Fatalf("被柜台接管过的旧手动账不许在下次断链时自动复活，got (%.0f,%v,%q)", cash, fresh, source)
	}
	// 接管之后 owner 又改了一笔（asof 晚于接管）⇒ 重新可用。
	if err := db.PutManualAccount(uid, 41000, true, 0, today+" 16:20:00"); err != nil {
		t.Fatalf("接管后再改一笔: %v", err)
	}
	cash, fresh, source = ctrl.AvailableCashBook()
	if cash != 41000 || !fresh || source != "manual" {
		t.Fatalf("接管后重新维护的手动账必须重新可用，got (%.0f,%v,%q)", cash, fresh, source)
	}
}

func TestAvailableCashBookManualTrueZero(t *testing.T) {
	uid := "u_book5"
	ctrl, db := manualCashCtrl(t, uid)
	seedCounterCash(t, db, uid, 5000, 40*time.Minute)
	if err := db.PutManualAccount(uid, 0, true, 0, manualTodayAsof()); err != nil {
		t.Fatalf("写真 0 手动资金: %v", err)
	}
	cash, fresh, source := ctrl.AvailableCashBook()
	if cash != 0 || !fresh || source != "manual" {
		t.Fatalf("owner 明确说可用资金是 0：必须 (0,true,manual)，不许折成不可得，got (%.0f,%v,%q)", cash, fresh, source)
	}
	// 有行但没填（cash_filled=0）＝"还没录"，与"录了 0"是两件事。
	uid2 := "u_book5b"
	ctrl2, db2 := manualCashCtrl(t, uid2)
	seedCounterCash(t, db2, uid2, 5000, 40*time.Minute)
	if err := db2.PutManualAccount(uid2, 0, false, 0, manualTodayAsof()); err != nil {
		t.Fatalf("写未填资金: %v", err)
	}
	if _, fresh, source := ctrl2.AvailableCashBook(); fresh || source != "none" {
		t.Fatalf("有行但没填可用资金＝还没录，必须 fail-close，got fresh=%v source=%q", fresh, source)
	}
}
