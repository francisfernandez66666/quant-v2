// cash_scope.go — §MULTI-ADMIN P1：引擎侧资金读取统一走账号口径。
// 运营账号允许 user_id=” 遗留行兜底；其他账号只读自己的 real_account 行，
// 避免“没有自己的行时读到运营/遗留资金”把动态预算和 T 账本算错。
// English: engine-side cash lookup with account scoping — legacy empty-user row only for the owner.
package engine

import (
	"fmt"

	"quant-trading-v2/internal/store"
)

// cashAccount 读指定账号的账户快照：优先控制器口径，无控制器时按“只读自己的行”处理。
func (e *Engine) cashAccount(uid string) (store.RealAccount, error) {
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
	return store.RealAccount{}, fmt.Errorf("real book not available")
}

// totalAssetsFor 总资产（现金+持仓市值），口径同 cashAccount。
func (e *Engine) totalAssetsFor(uid string) float64 {
	e.mu.RLock()
	ctrl := e.qmtCtrl
	st := e.realStore
	e.mu.RUnlock()
	if ctrl != nil {
		if v, err := ctrl.TotalAssetsFor(uid); err == nil {
			return v
		}
	}
	if st != nil {
		if v, err := st.TotalAssetsScoped(uid, false); err == nil {
			return v
		}
	}
	return 0
}
