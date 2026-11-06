// testclock_test.go — 测试夹具统一注入固定时钟（§SESSION-WINDOW / owner 2026-11-05 令）。
package trading

import (
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/store"
)

// newPinnedController 构造测试用 Controller 并把时钟钉到 fixedSessionNow()
// （见 guards_test.go：当日北京时 10:15，连续竞价中段，只钉时分、保留当日日期）。
//
// 不固定会在 09:15–09:30 与 14:52 后假红：Controller.PlaceOrder/PlaceSell 的禁挂时段闸
// 吃 c.now()，默认回退 time.Now() 真实挂钟；09:15–09:30（开盘集合竞价）与 14:52–15:00
// （收盘收摊）这两段窗口内，每一笔期望放行的新委托都会被真实拒掉 ⇒ 断言全红，
// 且红的是真在工作的腿、不是判据错。固定时刻不是放松断言，判的还是原来那件事；
// 只钉时分不改日期，day-keyed 幂等键与当日结算口径一字不变。
//
// 需要专门验证时段闸的用例，构造后自行 ctrl.SetNow(其它时刻) 覆盖本默认值即可。
// English: pin every test controller to 10:15 Beijing so the no-new-order guard is deterministic.
func newPinnedController(exec Executor, db *store.DB, userID string, cfg config.QMTConfig, onAlert func(level, title, content string)) *Controller {
	c := NewController(exec, db, userID, cfg, onAlert)
	c.SetNow(fixedSessionNow)
	return c
}
