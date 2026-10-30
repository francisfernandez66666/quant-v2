// ── §CANCEL-STATE（2026-10-28）前端「撤单中」状态机回归 ──
// 事故本体（600362 / 江西铜业）：14:57:46 发的买单，15:00:03 的撤单撞上尾盘集合竞价被交易所
// 拒绝；旧实现把"撤单请求被受理"直接记成「已撤」，于是本地显示已撤、实际已成交，
// 既没有 fills 也没有持仓。修法是引入中间态「撤单中」，只有交易所确认才转终态。
//
// 本用例锁定前端三件事（消费端契约）：
//   1. 「撤单中」行渲染出来且**不再有撤单按钮**（重复发撤单只会再被交易所拒一次）；
//   2. 状态标签带「撤单请求」留痕提示（何时/谁发的：manual/sweep/halt/gateway）；
//   3. 点撤单的提示语是「等待交易所确认」，不得再宣称"已提交就算撤成"。
// English: §CANCEL-STATE — the Quant page must render 撤单中 as a non-cancelable in-flight
// state with a cancel-trace tooltip, and the cancel toast must say "awaiting exchange confirmation".
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
// §DET-TIME 同款：接口全是 resolved promise 的 mock，排空微任务即可，不依赖真实定时器
import { settle } from './settle.js'

// tdesign 的 MessagePlugin 整体替换为 spy：断言文案而不弹真 toast
const mocks = vi.hoisted(() => ({
  message: { success: vi.fn(), warning: vi.fn(), error: vi.fn(), info: vi.fn() },
}))
vi.mock('tdesign-react', async (importOriginal) => {
  const real = await importOriginal()
  return { ...real, MessagePlugin: mocks.message }
})

// 二次确认在测试里没有真对话框：直接放行
vi.mock('../ui.jsx', async (importOriginal) => {
  const real = await importOriginal()
  return { ...real, confirmDialog: async () => true }
})

// 当日委托三条：已报（可撤）/ 撤单中（不可撤，带留痕）/ 已成（终态）
vi.mock('../api/index.js', () => ({
  getAccount: () => 'admin',
  isAdmin: () => true,
  isForbidden: (e) => !!(e && e.status === 403),
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
    { order_id: 'GW-OPEN', code: '600362.SH', side: '买入', price: 36.56, qty: 400, status: '已报', created_at: '2026-10-28T14:57:46+08:00' },
    {
      order_id: 'GW-CANCELING', code: '600362.SH', side: '买入', price: 36.56, qty: 400, status: '撤单中',
      created_at: '2026-10-28T14:57:46+08:00',
      cancel_requested_at: '2026-10-28T14:58:10+08:00', cancel_source: 'manual', cancel_reason: '手动撤单请求已发出，等待交易所确认',
    },
    { order_id: 'GW-DONE', code: '600362.SH', side: '买入', price: 36.56, qty: 400, status: '已成', created_at: '2026-10-28T14:57:46+08:00' },
  ])),
  fetchQMTSettleHistory: vi.fn(async () => ({ ok: '1', diffs: [] })),
  fetchShortStatus: vi.fn(async () => ({ short_enabled: false })),
  fetchPaperState: vi.fn(async () => ({ short_book: { enabled: false } })),
  switchQMTBroker: vi.fn(async () => ({})),
  updateQMTConfig: vi.fn(async () => ({})),
  qmtCancel: vi.fn(async () => ({ ok: '1', status: '撤单中', order_id: 'GW-OPEN' })),
}))

import Quant from '../pages/Quant.jsx'
import * as api from '../api/index.js'
import { MemoryRouter } from 'react-router-dom' // §W-04 子页签读 URL query，测试必须有 Router 上下文

describe('Quant 页「撤单中」状态（§CANCEL-STATE）', () => {
  beforeEach(() => {
    mocks.message.success.mockClear()
    mocks.message.warning.mockClear()
    mocks.message.error.mockClear()
    api.qmtCancel.mockClear()
  })

  it('「撤单中」行不可重复撤单，且状态标签带撤单请求留痕提示', async () => {
    render(<MemoryRouter><Quant /></MemoryRouter>)
    await waitFor(() => expect(screen.getByText('当日委托')).toBeInTheDocument())
    expect(await screen.findByText('GW-CANCELING')).toBeInTheDocument()

    // 「撤单中」标签渲染出来，且用 warning 配色（非终态蓝、非终态红）
    const label = screen.getByText('撤单中')
    const tag = label.closest('[title]')
    expect(tag, '撤单中标签须带 title 留痕').not.toBeNull()
    expect(tag.getAttribute('title')).toContain('2026-10-28 14:58:10') // 撤单请求时刻
    expect(tag.getAttribute('title')).toContain('手动') // 撤单来源

    // 撤单按钮只在「已报」行出现：已报(1) + 撤单中(不可撤) + 已成(终态不可撤)
    const cancelBtns = screen.getAllByRole('button', { name: '撤单' })
    expect(cancelBtns.length).toBe(1)
  })

  it('点撤单：提示「等待交易所确认」而不是宣称已撤成', async () => {
    render(<MemoryRouter><Quant /></MemoryRouter>)
    await waitFor(() => expect(screen.getByText('当日委托')).toBeInTheDocument())
    await screen.findByText('GW-OPEN')
    screen.getAllByRole('button', { name: '撤单' })[0].click()
    await waitFor(() => expect(api.qmtCancel).toHaveBeenCalledWith('GW-OPEN'))
    await settle()
    expect(mocks.message.error).not.toHaveBeenCalled()
    expect(mocks.message.success).toHaveBeenCalledTimes(1)
    const text = String(mocks.message.success.mock.calls[0][0])
    expect(text).toContain('撤单请求已发出')
    expect(text).toContain('等待交易所确认')
    expect(text).not.toContain('已撤') // 受理 ≠ 撤成
  })
})
