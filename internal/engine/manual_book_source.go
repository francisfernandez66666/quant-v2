// manual_book_source.go — §MANUAL-BOOK-UNIFY（2026-11-06 晚批，owner 裁决 1/5）账户维度输入的单一 resolver。
//
// 沿革（两段口径相反，必须都留着，否则下一个人会把"为什么不进硬闸"当成没被改过）：
//   - 11-06 上午 §MANUAL-BOOK-FALLBACK：桥断后手动账只当**计算输入**，硬边界写着"绝不进任何必须新鲜硬闸"。
//   - 11-06 下午 owner 裁决 1：**手动账喂买卖硬闸**——柜台连不上就以手工维护的持仓与可用资金为准。
//     本文件因此从一个全局开关升级成**两维各自选源**，可用性的两条前置（asof 属当前交易日、
//     未被柜台接管覆盖）收在 store.ManualBookViewFor 一处判。
//
// 仍然成立的三条边界（改的是作用维度，不是方向）：
//  1. 只做**读数选择**：手动值绝不写回 real_*——"柜台为准"这条规则的前提是柜台账只由回报与真实成交推进；
//  2. 不伪造柜台读数：不填 UpdatedAt、不填 CanUseQty。手动态的 T+1 可卖量走本地推算
//     （持仓 − 当日委托买入 − 当日改账买入 − 在途卖），不伪造一个"柜台说可卖/不可卖"的结论；
//  3. 有名字的静默：切了手动账会打日志并带 asof，读数面标 source=manual；
//     两本账都拿不到时保持 fail-closed，绝不拿空账当"零仓位/零资金"放行。
//
// English: one resolver for account-level inputs with per-dimension source selection. Counter
// readings win while they are actually being reported; otherwise a same-day manual book (not
// superseded by a counter take-over) supplies cash and/or positions — in memory only, never
// written back to the counter tables, never faking counter freshness or sellable qty.
package engine

import (
	"errors"
	"log"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/store"
)

// errRealBookAbsent 账本未装配：与 cash_scope.go 的 "real book not available" 同义，
// 单独命名是为了让 accountBookFor 能把"读不到"显式带出去而不是折成空仓。
var errRealBookAbsent = errors.New("real book not available")

const (
	accountSourceCounter = "counter"
	accountSourceManual  = "manual"
)

// accountBook 一轮计算所需的账户维度快照。PositionsErr 保留"读不到"这个事实，
// 让调用方继续 fail-closed；把读错折成空仓正是 §DYN-SIZE v4 修过的老缺陷。
//
// 为什么两维分开记（CashSource / PositionsSource）：柜台可能资金还在报而持仓快照停在昨天
// （或反过来），一个全局开关会把"哪个数是谁给的"重新糊掉——而这两个数一个决定买多少、
// 一个决定能不能卖。Source 是合成展示值（任一维 manual 即 manual），给日志与前端标注用。
type accountBook struct {
	Account         store.RealAccount
	Positions       []store.RealPosition
	PositionsErr    error
	Source          string
	CashSource      string
	PositionsSource string
	AsOf            string // 合成展示时刻（两维里较新的那个）
	CashAsOf        string
	PositionsAsOf   string
	TakenOverAt     string // 最近一次被柜台接管的时刻（读数面据此显示"已被柜台接管"）
	Manual          bool   // 任一维来自手动账
}

// counterAccount 按账号口径读柜台资金（与 cash_scope.go 同形，避免第二本账）。
func (e *Engine) counterAccount(uid string) (store.RealAccount, error) {
	e.mu.RLock()
	ctrl := e.qmtCtrl
	st := e.realStore
	e.mu.RUnlock()
	if ctrl != nil {
		return ctrl.GetRealAccountFor(uid)
	}
	if st != nil {
		return st.GetRealAccountOwn(uid)
	}
	return store.RealAccount{}, errRealBookAbsent
}

// accountBookFor 返回本轮该用的账户读数，两维各自按"柜台是否在报数"选源。
//
// 判据一律复用既有那两把尺，不新造第三把（收尺成因写在 internal/store/freshness.go 头注）：
//   - 资金维：§M12-A 的 30 分钟窗（store.CashReportFresh）；
//   - 持仓维：§A1-FRESH 的"快照属于当前交易日"（store.CounterPositionsReporting）。
//
// 手动维是否可用由 store.ManualBookViewFor 判（asof 属今日 + 未被接管覆盖），本函数不重复判一次——
// 同一个 freshness 在两处判，迟早一处严一处松，而松的那一处会放行买入。
func (e *Engine) accountBookFor(uid string) accountBook {
	book := accountBook{Source: accountSourceCounter, CashSource: accountSourceCounter, PositionsSource: accountSourceCounter}
	e.mu.RLock()
	st := e.realStore
	ctrl := e.qmtCtrl
	e.mu.RUnlock()

	acc, accErr := e.counterAccount(uid)
	book.Account = acc
	if st == nil {
		book.PositionsErr = errRealBookAbsent
		return book
	}
	poses, err := st.RealPositionsForUser(uid)
	book.Positions, book.PositionsErr = poses, err

	now := cntime.Now()
	day := cntime.DayOf(now)
	// "柜台可达"在这里是**两个**条件：这台引擎装配了实盘控制器，且控制器背后那本柜台账还在报数。
	// 只看时间戳不够——real_account 是网关回报直接写库的，qmt 未启用/控制器未装配时库里照样可能有一行
	// 时间戳很新的旧数据（测试夹具就是这么种的），把它读成"柜台在报数"会让手动兜底永远接不上。
	view, err := st.ManualBookViewFor(uid, day)
	if err != nil {
		// 读失败＝两维都保持柜台原值（哪怕陈旧），调用方照旧 fail-closed。
		log.Printf("[manual-book] 手动账读取失败，两维保持柜台口径（fail-closed）uid=%s: %v", uid, err)
		return book
	}
	// 选源规则本体在 store.SelectBookSources（引擎与持仓页读面共用一把尺，成因见 freshness.go 头注）。
	sel := store.SelectBookSources(acc, accErr, book.Positions, book.PositionsErr, view, day, now, ctrl != nil)
	if !sel.AnyManual {
		return book // 逐字今天的行为：两维都在报数，或手动账该维不可用
	}

	if sel.Cash == accountSourceManual {
		// UpdatedAt 故意留空：手动账不是券商回报时刻，写进去就等于用"刚回报过"的形态
		// 去解开 §M12-A 新鲜度闸。本批要的回落走的是 source 判据，不是伪造时间戳。
		book.Account = store.RealAccount{
			UserID:        uid,
			AvailableCash: view.Cash.Account.AvailableCash,
			TotalAsset:    view.Cash.Account.TotalAsset,
		}
		book.CashSource = accountSourceManual
		book.CashAsOf = view.CashAsOf
		book.Manual = true
		log.Printf("[manual-book] 柜台资金回报不可信，改用手动实盘资金：uid=%s 可用=%.0f 总资产=%.0f asof=%s",
			uid, book.Account.AvailableCash, book.Account.TotalAsset, view.CashAsOf)
	}

	if sel.Positions == accountSourceManual {
		converted := make([]store.RealPosition, 0, len(view.Positions))
		for _, p := range view.Positions {
			// CanUseQty 留 nil：手动账没有柜台 T+1 可卖量，伪造它等于替券商下结论。
			converted = append(converted, store.RealPosition{
				TsCode: p.TsCode, Name: p.Name, Qty: p.Qty, CostPrice: p.CostPrice,
			})
		}
		book.Positions = converted
		book.PositionsErr = nil
		book.PositionsSource = accountSourceManual
		book.PositionsAsOf = view.PositionsAsOf
		book.Manual = true
		log.Printf("[manual-book] 柜台持仓快照不属于今日，改用手动实盘持仓：uid=%s 持仓=%d 行 asof=%s",
			uid, len(converted), view.PositionsAsOf)
	}

	if book.Manual {
		book.Source = accountSourceManual
		book.TakenOverAt = view.TakenOverAt
		if book.CashAsOf > book.PositionsAsOf {
			book.AsOf = book.CashAsOf
		} else {
			book.AsOf = book.PositionsAsOf
		}
	}
	return book
}
