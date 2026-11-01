// ── §SUBSCRIPTION（2026-11-01）委托列表「申购」标注的前端消费契约 ──
// 裁决本体：券商委托表里的新股/新债申购行**要能看见，不许隐藏**，只做标注。
// 本用例锁三件事（都是真实风险，不是排版）：
//   1. 带 subscription=true 的行**仍然出现在列表里**，并渲染「申购」标签（隐藏形状一复活就红）；
//   2. 申购行**不显示撤单按钮**——后端护栏本来就不对它发撤单，按钮能点而请求必被柜台拒，
//      那是"UI 承诺了后端不兑现"的形状；
//   3. 字段缺席（识别开关关着 / 旧后端）时必须退回今天的样子：无标签、撤单按钮照常，
//      即前端把 undefined 当"不知道"，绝不当成"是申购"。
// English: the order list must *label* subscription rows (never hide them), must not offer a
// cancel button for them, and must fall back to today's UI when the field is absent.
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'

const mocks = vi.hoisted(() => ({
  message: { success: vi.fn(), warning: vi.fn(), error: vi.fn(), info: vi.fn() },
}))
vi.mock('tdesign-react', async (importOriginal) => {
  const real = await importOriginal()
  return { ...real, MessagePlugin: mocks.message }
})
vi.mock('../ui.jsx', async (importOriginal) => {
  const real = await importOriginal()
  return { ...real, confirmDialog: async () => true }
})

// 夹具：一笔真实在途买单 + 一笔申购登记行。
// 申购行的状态刻意用「已报」而不是「未知(86)」：状态闸本来就不撤未知态，用它是假绿——
// 本用例要证的是**申购标注本身**把撤单按钮拿掉了（正是"哪天柜台给了申购行一个可读状态"那一形）。
const subRow = {
  order_id: 'GW-SUB-1', signal_id: 'ext:GW-SUB-1', code: '370181.SZ', side: '买入',
  price: 0, qty: 0, status: '已报', created_at: '2026-11-02T09:31:00+08:00',
  subscription: true, subscription_reason: 'only-segment',
}
const realRow = {
  order_id: 'GW-REAL-1', signal_id: 'buy:600000:dragon', code: '600000.SH', side: '买入',
  price: 10, qty: 100, status: '已报', created_at: '2026-11-02T09:35:00+08:00',
}

// 接口桩：本页读的端点全部给 resolved promise（照 cancel_state_20261028.test.jsx 的同款工装），
// 只有 fetchQMTOrders 是本用例真正要喂形状的那一条——两个用例共用同一夹具，差异只在
// subscription 两个键在不在（键缺席＝后端识别开关关着，前端必须退回今天的样子）。
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
  fetchQMTOrders: vi.fn(async () => ([{ ...subRow }, { ...realRow }])),
  fetchQMTSettleHistory: vi.fn(async () => ({ ok: '1', diffs: [] })),
  fetchShortStatus: vi.fn(async () => ({ short_enabled: false })),
  fetchPaperState: vi.fn(async () => ({ short_book: { enabled: false } })),
  switchQMTBroker: vi.fn(async () => ({})),
  updateQMTConfig: vi.fn(async () => ({})),
  qmtCancel: vi.fn(async () => ({ ok: '1', status: '撤单中', order_id: 'GW-REAL-1' })),
}))

import Quant from '../pages/Quant.jsx'
import * as api from '../api/index.js'
import { MemoryRouter } from 'react-router-dom'

describe('Quant 页「申购」标注（§SUBSCRIPTION）', () => {
  beforeEach(() => {
    mocks.message.success.mockClear()
    api.qmtCancel.mockClear()
  })

  it('申购行照常列出并标「申购」，且不给撤单按钮', async () => {
    render(<MemoryRouter><Quant /></MemoryRouter>)
    await waitFor(() => expect(screen.getByText('当日委托')).toBeInTheDocument())

    // ① 两行都在（隐藏形状一复活，这里就只剩一行）
    expect(await screen.findByText('GW-SUB-1')).toBeInTheDocument()
    expect(screen.getByText('GW-REAL-1')).toBeInTheDocument()
    // ② 标注在位，且"手动"标记没被顶掉（申购行同时也是券商 App 手动单，两个标记并存才有信息量）
    expect(screen.getByText('申购')).toBeInTheDocument()
    expect(screen.getByText('手动')).toBeInTheDocument()
    // ③ 撤单按钮只有真实在途那一笔
    const cancelBtns = screen.getAllByRole('button', { name: '撤单' })
    expect(cancelBtns.length).toBe(1)
    cancelBtns[0].click()
    await waitFor(() => expect(api.qmtCancel).toHaveBeenCalledWith('GW-REAL-1'))
    expect(api.qmtCancel.mock.calls.length).toBe(1)
  })

  it('字段缺席（识别开关关着/旧后端）时退回今天的样子', async () => {
    // 同一夹具去掉 subscription 两个键：证"没有字段 ⇒ 撤单按钮照旧出现"（关状态＝今天的行为）
    api.fetchQMTOrders.mockResolvedValueOnce([
      { ...subRow, subscription: undefined, subscription_reason: undefined },
      { ...realRow },
    ])
    render(<MemoryRouter><Quant /></MemoryRouter>)
    await waitFor(() => expect(screen.getByText('当日委托')).toBeInTheDocument())
    expect(await screen.findByText('GW-SUB-1')).toBeInTheDocument()
    // 无标签：undefined 绝不能被当成"是申购"
    expect(screen.queryByText('申购')).not.toBeInTheDocument()
    // 两行都可撤（旧后端的委托列表行为必须与今天一致）
    expect(screen.getAllByRole('button', { name: '撤单' }).length).toBe(2)
  })
})
