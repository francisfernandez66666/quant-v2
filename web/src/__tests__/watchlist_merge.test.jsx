// ── §WL-FIX（20260917）自选股整表合并回归 ──
// 根因回归：旧版把 wlRow 定义在 load() 内、被组件级 buildDisplayList 跨作用域调用，
// 首屏整表合并必抛 ReferenceError(wlRow is not defined) 且被静默 catch 吞掉 → 永远「暂无自选股」，
// 线上表现为「看不到自选股 / 添加后本次会话可见、刷新即丢」。
// 本用例锁定合并语义：快照覆盖的行 + 快照未覆盖（评估回退）的行都必须出现在列表里。
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor, cleanup } from '@testing-library/react'

vi.mock('../api/index.js', () => ({
  // §F29 账号隔离缓存用到的 getAccount
  getAccount: () => 'tester',
  fetchStatus: vi.fn(async () => ({ session: 3, session_label: '交易中' })),
  isTradingSession: (s) => s === 1 || s === 3,
  setLastSession: () => {},
  fetchSnapshot: vi.fn(async () => [
    { code: '000001', name: '平安银行', price: 11.61, change_pct: -0.77 },
  ]),
  fetchWatchlist: vi.fn(async () => ({
    // 混合形态：已含完整行情的行 + 裸代码行（快照/评估回退补齐）
    stocks: [{ code: '000001', name: '平安银行', price: 11.61, change_pct: -0.77 }, { code: '600036' }],
  })),
  fetchEvaluations: vi.fn(async () => [
    { code: '600036', name: '招商银行', price: 40.6, n_score: 70, n_pass: true, dragon_score: 80, dragon_pass: true, db_score: 0, db_pass: false, dr_score: 60, dr_pass: true, m_score: 55, m_pass: false },
  ]),
  addWatchlist: vi.fn(), removeWatchlist: vi.fn(), fetchStockLookup: vi.fn(),
}))

import Watchlist from '../pages/Watchlist.jsx'

describe('自选股 §WL-FIX：整表合并不再被跨作用域 wlRow 炸掉', () => {
  beforeEach(() => { localStorage.clear(); cleanup() })
  it('快照覆盖的行 + 快照未覆盖（评估回退）的行都要显示', async () => {
    render(<Watchlist />)
    await waitFor(() => {
      expect(screen.getByText('000001')).toBeTruthy()
      expect(screen.getByText('600036')).toBeTruthy()
      expect(screen.getByText('招商银行')).toBeTruthy()
    }, { timeout: 3000 })
  })
})
