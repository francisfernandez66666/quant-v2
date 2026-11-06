// testclock_test.go — 测试夹具统一注入固定时钟（§SESSION-WINDOW / owner 2026-11-05 令）。
package engine

import (
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/store"
	"quant-trading-v2/internal/trading"
)

// newPinnedController 构造测试用 trading.Controller 并把时钟钉到 liveTestNow()
// （见 discipline_real_test.go：当日北京时 10:15，连续竞价中段，只钉时分、保留当日日期）。
//
// 不固定会在 09:15–09:30 与 14:52 后假红：Controller.PlaceOrder/PlaceSell 的禁挂时段闸
// 吃 c.now()，默认回退 time.Now() 真实挂钟；窗口内每一笔期望放行的新委托都被真实拒掉 ⇒
// 断言全红，且红的是真在工作的腿、不是判据错。固定时刻不是放松断言，判的还是原来那件事。
// 需要专门验证时段闸的用例，构造后自行 ctrl.SetNow(其它时刻) 覆盖本默认值即可。
// English: pin every test controller to 10:15 Beijing so the no-new-order guard is deterministic.
func newPinnedController(exec trading.Executor, db *store.DB, userID string, cfg config.QMTConfig, onAlert func(level, title, content string)) *trading.Controller {
	c := trading.NewController(exec, db, userID, cfg, onAlert)
	c.SetNow(liveTestNow)
	return c
}

// newPinnedEngine 构造测试用 Engine 并把统一时钟 e.nowTime() 钉到 liveTestNow()（当日北京时 10:15）。
//
// 不固定会在 09:15–09:30 与 14:52 后假红：autoPlaceScaled 里的禁挂闸
// ctrl.NewOrderBlockedByWindow(e.nowTime()) 吃的是**引擎**时钟 e.nowTime()，默认回退 time.Now()
// 真实挂钟，与 Controller 的 c.now() 是两套独立注入点，缺一不可；窗口内整轮「本轮不下单」
// （读数落决策快照后 return）⇒ 所有「应下单」断言假红，dynbudget 类用例还会因 0 单索引 panic。
// 只钉时分、保留当日日期；需要专门验证时段闸的用例随后自行 e.SetClock(其它时刻) 覆盖。
// English: pin the engine clock to 10:15 Beijing; Gate B reads e.nowTime(), not the controller clock.
func newPinnedEngine() *Engine {
	e := &Engine{}
	e.SetClock(liveTestNow)
	return e
}
