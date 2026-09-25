// ── §0925EVE-A2（2026-09-25）kill-switch 撤单失败前端回显回归 ──
// 缺陷本体：HaltAll 撤单失败只 log+continue，前端只拿到 cancelled 成功计数——
// "停止交易"按下后有几笔没撤掉，操作者看不到（「降级报成功」）。后端已把 failed 明细
// 随 /api/qmt/halt 响应回传（恒为数组），本用例锁定消费端两半契约：
//   1. 有失败：结果提示必须走 warning 文案，含「N 笔未撤成」与逐笔单号清单；
//   2. 全成功（failed=[]）：仍走原 success 文案，不得误报失败。
// English: §0925EVE-A2 — the Quant page kill-switch toast must surface the per-order
// cancellation failure list (count + order ids) instead of a success-only count.
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
// §DET-TIME 同款：接口全是 resolved promise 的 mock，排空微任务即可，不依赖真实定时器
import { settle } from './settle.js'

// tdesign 的 MessagePlugin 整体替换为 spy：断言文案而不弹真 toast；
// 其余组件（Card/Button/Table…）保留真实实现，保证 Quant 页照常挂载。
// vi.mock 会被提升到文件头，共享句柄必须走 vi.hoisted。
const mocks = vi.hoisted(() => ({
  message: { success: vi.fn(), warning: vi.fn(), error: vi.fn(), info: vi.fn() },
}))
vi.mock('tdesign-react', async (importOriginal) => {
  const real = await importOriginal()
  return { ...real, MessagePlugin: mocks.message }
})

// 二次确认在测试里没有真对话框：直接放行（共享封装的消费面已由其他用例覆盖）
vi.mock('../ui.jsx', async (importOriginal) => {
  const real = await importOriginal()
  return { ...real, confirmDialog: async () => true }
})

// Quant 页只读拉取 mock（口径与 uat_fixes_u2_u5 一致：挂载态不触发任何真实写操作）
vi.mock('../api/index.js', () => ({
  getAccount: () => 'admin',
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
    { order_id: 'GW-A2-9', code: '600519.SH', side: '买入', price: 1500, qty: 100, status: '已报', created_at: '2026-09-25T10:00:00+08:00' },
  ])),
  fetchQMTSettleHistory: vi.fn(async () => ({ ok: '1', diffs: [] })),
  fetchShortStatus: vi.fn(async () => ({ short_enabled: false })),
  fetchPaperState: vi.fn(async () => ({ short_book: { enabled: false } })),
  switchQMTBroker: vi.fn(async () => ({})),
  updateQMTConfig: vi.fn(async () => ({})),
  qmtHalt: vi.fn(async () => ({ ok: '1', halted: true, cancelled: 2,
    failed: [{ order_id: 'GW-A2-9', reason: 'gateway 409: 当前状态不可撤' }] })),
}))

import Quant from '../pages/Quant.jsx'
import * as api from '../api/index.js'

describe('Quant 页 kill-switch 撤单失败回显（§0925EVE-A2）', () => {
  beforeEach(() => {
    mocks.message.success.mockClear()
    mocks.message.warning.mockClear()
    mocks.message.error.mockClear()
    api.qmtHalt.mockClear()
  })

  it('响应含 failed 明细：warning 文案点明笔数与单号清单，不走成功文案', async () => {
    render(<Quant />)
    await waitFor(() => expect(screen.getByRole('button', { name: '紧急停止' })).toBeInTheDocument())
    await settle()
    screen.getByRole('button', { name: '紧急停止' }).click()
    await waitFor(() => expect(api.qmtHalt).toHaveBeenCalledWith(true))
    await settle()
    expect(mocks.message.warning).toHaveBeenCalledTimes(1)
    const text = String(mocks.message.warning.mock.calls[0][0])
    expect(text).toContain('1 笔未撤成')
    expect(text).toContain('GW-A2-9') // 单号清单：操作者据此去柜台/撤单闭环逐笔处置
    expect(text).toContain('撤销 2 笔') // 成功计数保留，不被失败明细挤掉
    expect(mocks.message.success).not.toHaveBeenCalled()
  })

  it('反证：failed 为空数组（全撤成）时仍是原 success 文案', async () => {
    api.qmtHalt.mockResolvedValueOnce({ ok: '1', halted: true, cancelled: 3, failed: [] })
    render(<Quant />)
    await waitFor(() => expect(screen.getByRole('button', { name: '紧急停止' })).toBeInTheDocument())
    await settle()
    screen.getByRole('button', { name: '紧急停止' }).click()
    await waitFor(() => expect(api.qmtHalt).toHaveBeenCalledWith(true))
    await settle()
    expect(mocks.message.warning).not.toHaveBeenCalled()
    expect(mocks.message.success).toHaveBeenCalledTimes(1)
    expect(String(mocks.message.success.mock.calls[0][0])).toContain('撤销 3 笔')
  })
})
