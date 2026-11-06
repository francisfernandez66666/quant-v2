// manual_book_source.go — §MANUAL-BOOK-FALLBACK（2026-11-06）：账户维度计算输入的单一读数器。
//
// owner 现场（11-06 09:43）：券商禁令生效、云上 QMT 登录被禁，物理机 Win 可能回传不了数据；
// 「量化交易并推送买卖要用持仓 tab 里的实盘做兜底，我来手动维护持仓实盘结果，
//
//	量化交易读取持仓实盘结果来计算动态仓位和推送买卖信号」。
//
// 这正是 11-03 方案 §2.12 里写了但一直没接线的那一句：手动账的表/接口/前端都在，
// 钱路径却始终只读桥回报的 real_account / real_positions。本文件补的就是那个"接线"。
//
// 三条不许越过的边界（方案 §2.12 + store/manual_book.go 的既有锁）：
//  1. 只做**计算输入**：手动数据只在内存里转成同形状读数，绝不写回 real_* 表；
//  2. 不冒充柜台真值：不填 UpdatedAt、不填 CanUseQty，也不会让 §M12-A 的
//     "必须新鲜"硬闸以为券商刚回报过；卖出 T+1 可卖量仍只读 real_*；
//  3. 有名字的静默：切了手动账会打日志并带 asof，读数面标 source=manual；
//     没维护手动账时保持今天的 fail-closed，绝不拿空账当"零仓位"放行。
//
// English: one resolver for account-level inputs. Counter reads win while fresh;
// when the bridge is stale AND the owner maintained the manual book, its rows
// become in-memory computation inputs only, never persisted as counter truth.
package engine

import (
	"errors"
	"log"

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
type accountBook struct {
	Account      store.RealAccount
	Positions    []store.RealPosition
	PositionsErr error
	Source       string
	AsOf         string
	Manual       bool
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

// accountBookFor 返回本轮该用的账户读数。判据只有一条且与硬闸同源：
// ctrl.AvailableCash() 的 fresh 标志。桥新鲜 ⇒ 逐字今天的柜台口径；
// 桥不新鲜且 owner 维护了手动账 ⇒ 切手动输入；否则原样返回（调用方照旧 fail-closed）。
func (e *Engine) accountBookFor(uid string) accountBook {
	book := accountBook{Source: accountSourceCounter}
	e.mu.RLock()
	st := e.realStore
	ctrl := e.qmtCtrl
	e.mu.RUnlock()

	if acc, err := e.counterAccount(uid); err == nil {
		book.Account = acc
	}
	if st != nil {
		poses, err := st.RealPositionsForUser(uid)
		book.Positions, book.PositionsErr = poses, err
	} else {
		book.PositionsErr = errRealBookAbsent
	}

	// 与 §M12-A 「必须新鲜」硬闸用同一把尺，不另造新鲜度定义。
	fresh := false
	if ctrl != nil {
		_, fresh = ctrl.AvailableCash()
	}
	if fresh || st == nil {
		return book
	}

	// 桥断了：只有真的维护过手动账才允许切换。Present/Filled 三态把
	// "owner 说没钱" 与 "owner 还没录" 分开，空账绝不当成零仓位。
	state, err := st.ManualAccountForUser(uid)
	if err != nil {
		log.Printf("[manual-book] 手动资金读取失败，保持柜台口径（fail-closed）uid=%s: %v", uid, err)
		return book
	}
	rows, err := st.ManualPositionsForUser(uid)
	if err != nil {
		log.Printf("[manual-book] 手动持仓读取失败，保持柜台口径（fail-closed）uid=%s: %v", uid, err)
		return book
	}
	if !state.Present && len(rows) == 0 {
		return book
	}
	asof, err := st.ManualAsOf(uid)
	if err != nil {
		asof = ""
	}

	// 只填计算需要的字段。UpdatedAt 故意留空：手动账不是券商回报时刻，
	// 写进去就等于用"刚回报过"的形态解开新鲜度硬闸（方案明令禁止）。
	if state.Present {
		book.Account = store.RealAccount{
			UserID:        uid,
			AvailableCash: state.Account.AvailableCash,
			TotalAsset:    state.Account.TotalAsset,
		}
	}
	converted := make([]store.RealPosition, 0, len(rows))
	for _, p := range rows {
		// CanUseQty 留 nil：手动账没有柜台 T+1 可卖量，卖出硬闸仍只吃 real_*。
		converted = append(converted, store.RealPosition{
			TsCode: p.TsCode, Name: p.Name, Qty: p.Qty, CostPrice: p.CostPrice,
		})
	}
	book.Positions = converted
	book.PositionsErr = nil
	book.Source = accountSourceManual
	book.AsOf = asof
	book.Manual = true
	log.Printf("[manual-book] 桥回报不新鲜，动态仓位/买卖推送改用手动实盘：uid=%s 持仓=%d 行 可用资金=%.0f 总资产=%.0f asof=%s",
		uid, len(converted), book.Account.AvailableCash, book.Account.TotalAsset, asof)
	return book
}
