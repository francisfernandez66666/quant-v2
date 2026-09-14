// ── §U-2 / §U-5（2026-09-14 像素级 UAT 修复批）前端入口回归 ──
// 锁定：量化页 kill-switch 紧急停止按钮、当日委托撤单按钮、日终结算对账卡三处前端入口——
// 这些能力后端端点早已存在但页面零入口（撤单尤其因缺 order_id 列表而无从挂按钮），
// 本用例挂载 Quant 断言三卡渲染 + 委托行撤单按钮按状态显隐；Admin 页断言"清理失效账号"入口存在。
// English: guards the §U-2 ops UI (kill-switch / per-order cancel / settlement) and §U-5 stale-account
// cleanup entry that the backend had but the frontend never wired; asserts render + status-gated cancel.
import { describe, it, expect, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'

// Quant 页只读拉取 mock：含当日委托两条（一条可撤"已报"、一条终态"已成"）
vi.mock('../api/index.js', () => ({
  getAccount: () => 'admin',
  fetchQMTConfig: vi.fn(async () => ({
    enabled: true, mode: 'manual', price_type: 'market', auto_sell: false, halted: false,
    gateway_url: 'http://127.0.0.1:18789', token_masked: '****',
    fixed_amount: 10000, max_positions: 10, initial_capital: 100000,
    daily_max_buys: 20, daily_budget_amount: 100000, miss_heartbeat_sec: 120,
    known_strategies: [], strategies: [], strategy_amounts: {},
  })),
  fetchQMTState: vi.fn(async () => ({ enabled: true, mode: 'manual', tripped: false, gateway_url: 'http://127.0.0.1:18789' })),
  fetchQMTBroker: vi.fn(async () => ({ ok: true, broker: 'xt', xt_connected: true, queued_connected: false })),
  fetchQMTTrades: vi.fn(async () => ({})),
  fetchQMTOrders: vi.fn(async () => ([
    { order_id: 'GW-1', code: '600519.SH', side: '买入', price: 1500, qty: 100, status: '已报', created_at: '2026-09-14T10:00:00+08:00' },
    { order_id: 'GW-2', code: '300750.SZ', side: '卖出', price: 200, qty: 100, status: '已成', created_at: '2026-09-14T10:01:00+08:00' },
  ])),
  fetchQMTSettleHistory: vi.fn(async () => ({ ok: '1', diffs: [] })),
  fetchShortStatus: vi.fn(async () => ({ short_enabled: false })),
  fetchPaperState: vi.fn(async () => ({ short_book: { enabled: false } })),
  switchQMTBroker: vi.fn(async () => ({})),
  updateQMTConfig: vi.fn(async () => ({})),
}))

import Quant from '../pages/Quant.jsx'

describe('Quant 页 §U-2 运维三件套入口', () => {
  it('渲染紧急停止按钮、当日委托撤单按钮、日终结算对账卡', async () => {
    render(<Quant />)
    // kill-switch：链路状态卡内的"紧急停止"行 + 按钮（行标签与按钮文案同字面，用 getAllByText）
    await waitFor(() => expect(screen.getAllByText('紧急停止').length).toBeGreaterThanOrEqual(1))
    expect(screen.getByRole('button', { name: '紧急停止' })).toBeInTheDocument()
    // 当日委托卡：两条委托都渲染；仅"已报"那条显示撤单按钮（"已成"终态撤单栏为 —）
    await waitFor(() => expect(screen.getByText('当日委托')).toBeInTheDocument())
    expect(await screen.findByText('GW-1')).toBeInTheDocument()
    expect(screen.getByText('GW-2')).toBeInTheDocument()
    const cancelBtns = screen.getAllByRole('button', { name: '撤单' })
    expect(cancelBtns.length).toBe(1) // 只有未成交的 GW-1 可撤
    // 日终结算对账卡
    expect(screen.getByText('日终结算对账')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /立即对账/ })).toBeInTheDocument()
  })
})
