// fetcher_auction_calgate_20260925_test.go — §0925EVE-W3-E（C6）：InAuctionWindow 是
// §CAL-GATE「14 判据统一」的漏网第 15 个——纯 HHMM 钟面比较、无 IsTradingDay 闸，
// 休市日（周末/法定节假日恰为工作日）早晨 9:15-9:26 照样放行，maybeFetchAuction
// 每天向 hithink 空转一次全池竞价请求。本用例与 trade_time_calendar_test.go 同锚点
// （2026-09-25 中秋休市，恰为周五），钉「休市日窗口内返回 false」+ 普通交易日不误杀。
// English: §0925EVE-W3-E — the 15th session gate joins the CAL-GATE discipline: closed days
// must report false inside the nominal auction window; ordinary trading days must not regress.
package data

import (
	"testing"
)

// TestInAuctionWindowRespectsTradingDay 三分支：休市工作日 false（修复本体）、
// 周末 false、普通交易日窗口内 true（等值锁：闸不许把真竞价窗也判死）。
func TestInAuctionWindowRespectsTradingDay(t *testing.T) {
	SetClosedDays([]string{"20260925"}) // 中秋：周五但休市（生产实录日期，同 §CAL-GATE 锚点）
	defer SetClosedDays(nil)

	// ① 休市日窗口内：旧实现纯钟面判据会给 true → 空转发请求；现在必须 false
	if InAuctionWindow(at(2026, 9, 25, 9, 20)) {
		t.Error("休市日（2026-09-25 中秋，周五）9:20 不得判为竞价窗口（第 15 判据漏网回归）")
	}
	// ② 周六窗口内：周末天然休市，同样必须 false
	if InAuctionWindow(at(2026, 9, 26, 9, 20)) {
		t.Error("周六 9:20 不得判为竞价窗口")
	}
	// ③ 普通交易日（2026-08-04 周二）窗口内：必须仍 true——闸只关休市日，不误杀真窗
	if !InAuctionWindow(at(2026, 8, 4, 9, 20)) {
		t.Error("普通交易日 9:20 必须在竞价窗口内（等值锁：防单向闸把盘中也判死）")
	}
	// ④ 普通交易日窗口外边界：9:14 前 / 9:27 后仍为 false（原钟面语义保留）
	if InAuctionWindow(at(2026, 8, 4, 9, 14)) || InAuctionWindow(at(2026, 8, 4, 9, 27)) {
		t.Error("窗口外钟面判定不得被本改动放宽")
	}
	// ⑤ 日历未加载时 fail-open（与 §CAL-GATE 缺省方向一致：法定节假日暂按交易日跑）——
	// 显式清空注入后，09-25 这一刻必须回到窗口内放行的旧形态。
	SetClosedDays(nil)
	if !InAuctionWindow(at(2026, 9, 25, 9, 20)) {
		t.Error("日历未注入（SetClosedDays(nil) 后）应维持 fail-open：窗口内按交易日放行")
	}
}
