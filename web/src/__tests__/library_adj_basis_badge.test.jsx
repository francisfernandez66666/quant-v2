// ── 战法库「基线口径已失效」红标测试 library_adj_basis_badge.test.jsx ──
// §ADJ-BASIS-2（2026-09-23）：§ADJ 把 HfqBars 的复权因子改成前向填充后，此前审批落盘的因子战法
// （如 fac_1「波动突破」）的 weights/buy_threshold 是在错误面板上拟合的，已无成立的历史依据。
// 后端 GET /api/research/library 每条带 adj_basis（应用时盖的口径戳）+ stale_adj_basis（载入侧判定），
// 前端只负责把这件事显性化。这里锁三件事：
//   ①口径戳与当前基线不匹配（含旧库根本没有 adj_basis 字段）→ 渲染「基线口径已失效」红标 + 一行说明；
//   ②口径戳就是当前基线 → 不渲染任何标记（新鲜战法不得被打扰）；
//   ③形态战法（pat_*）与因子战法对称参与：其条件同样读 CloseHfq 派生面板，漏标即留半边盲区。
// 断言用导出的 AdjBasisStaleTag 直接渲染：整页挂载 Research 要 mock 十余个端点，与这三条断言无关。
// English: badges a strategy whose adjustment-basis stamp is not current (including a legacy entry
// with no stamp at all) and stays silent for the current basis — patterns are covered symmetrically.
import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import { AdjBasisStaleTag, isAdjBasisStale } from '../pages/Research.jsx'

// 与 Go 侧 internal/research/windowed.go 的 AdjBaselineVersion 对齐（改口径必须两边同步 bump）。
const CURRENT_BASIS = 'hfq-forward-fill-1'

describe('战法库基线口径失效标记 §ADJ-BASIS-2', () => {
  it('旧库条目（无 adj_basis 字段 / 后端未给 stale 判定）→ 渲染「基线口径已失效」', () => {
    const legacy = { kind: 'factor', id: 'fac_1', name: '波动突破' } // 没有 adj_basis 字段
    expect(isAdjBasisStale(legacy)).toBe(true)
    const { container } = render(<AdjBasisStaleTag strategy={legacy} />)
    const badge = screen.getByText('基线口径已失效')
    expect(badge).toBeInTheDocument()
    // 一行说明：参数是在修正前的复权口径上拟合的（tooltip 走 title，与本页其他提示同形态）
    expect(badge.getAttribute('title')).toContain('参数是在修正前的复权口径上拟合的')
    expect(container.textContent).not.toBe('')
  })

  it('后端显式判定 stale_adj_basis=true（旧戳非当前基线）→ 渲染红标', () => {
    const s = { kind: 'factor', id: 'fac_9', adj_basis: 'some-older-basis', stale_adj_basis: true }
    expect(isAdjBasisStale(s)).toBe(true)
    render(<AdjBasisStaleTag strategy={s} />)
    expect(screen.getByText('基线口径已失效')).toBeInTheDocument()
  })

  it('当前基线条目 → 不渲染任何标记', () => {
    const fresh = { kind: 'factor', id: 'fac_2', adj_basis: CURRENT_BASIS, stale_adj_basis: false }
    expect(isAdjBasisStale(fresh)).toBe(false)
    const { container } = render(<AdjBasisStaleTag strategy={fresh} />)
    expect(screen.queryByText('基线口径已失效')).not.toBeInTheDocument()
    expect(container.firstChild).toBeNull()
  })

  it('形态战法（pat_*）对称参与：无戳判失效、带当前戳不判 §ADJ-BASIS-2P', () => {
    const legacy = { kind: 'pattern', id: 'pat_1' }
    expect(isAdjBasisStale(legacy)).toBe(true)
    render(<AdjBasisStaleTag strategy={legacy} />)
    expect(screen.getByText('基线口径已失效')).toBeInTheDocument()
    const fresh = { kind: 'pattern', id: 'pat_2', adj_basis: CURRENT_BASIS, stale_adj_basis: false }
    expect(isAdjBasisStale(fresh)).toBe(false)
    expect(screen.queryAllByText('基线口径已失效')).toHaveLength(1)
  })
})
