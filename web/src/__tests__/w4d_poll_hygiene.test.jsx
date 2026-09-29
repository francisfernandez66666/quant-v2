// ── §0926E2E-17A（FIX_PLAN_20260926E2E 四波 17a 项）轮询统一 回归用例 ──
//
// 缺陷原文：Dashboard 10s/15s、Quant 10s/10s/30s/30s 高频轮询游离在 §F5 的
// "SSE 事件驱动 + 60s 兜底"统一口径之外；后端早已定向广播的 qmt_report/real_order/
// qmt_halt/settlement_diff/positions_clear_guard 五类事件前端数据面零消费。
//
// 三把锁：
//  P1 事件即时性：dispatch({type:'qmt_report'}) 后四路数据立刻重拉（不等任何定时器）；
//  P2 止血闭环：403 落面板后再来事件，不得借回调复活 admin 端点请求（§M13 语义延伸）；
//  P3 静态负锁：Dashboard/Quant 源码真实代码里不得再出现 5s/10s/15s/30s 的 setInterval。
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, cleanup, act } from '@testing-library/react'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const HERE = path.dirname(fileURLToPath(import.meta.url))

// 首屏端点默认「管理员正常空载荷」；形态与 §M13 锁同源，另补 qmtPendingReview（待核对卡）。
const { OK_PAYLOADS } = vi.hoisted(() => ({
  OK_PAYLOADS: {
    fetchQMTConfig: () => ({
      enabled: false, mode: 'manual', price_type: 'limit', auto_sell: false,
      gateway_url: 'http://127.0.0.1:18789', token_masked: '****',
      fixed_amount: 10000, max_positions: 5, initial_capital: 500000,
      daily_max_buys: 3, daily_budget_amount: 100000, miss_heartbeat_sec: 120, max_order_amount: 50000,
      known_strategies: [{ id: 'dragon', name: '龙头识别', kind: 'form' }],
      strategies: [], strategy_amounts: {},
    }),
    fetchQMTState: () => ({ enabled: false, tripped: false, gateway_url: 'http://127.0.0.1:18789' }),
    fetchQMTOrders: () => [],
    fetchQMTTrades: () => ({}),
    fetchQMTBroker: () => ({ ok: true, broker: 'xt', broker_connected: true }),
    fetchQMTSettleHistory: () => ({ diffs: [] }),
    qmtPendingReview: () => ({ orders: [], unresolved_count: 0 }),
    fetchRiskGates: () => ({ gates: [], switches: {} }),
    fetchShortStatus: () => ({ short_enabled: false }),
    fetchPaperState: () => ({ enabled: false, is_admin: true, short_book: { enabled: false } }),
    fetchSignalVerdicts: () => ({ verdicts: [] }),
  },
}))

vi.mock('../api/index.js', async () => {
  const actual = await vi.importActual('../api/index.js')
  const stubs = {}
  for (const [name, make] of Object.entries(OK_PAYLOADS)) {
    stubs[name] = vi.fn(async () => make())
  }
  return { ...actual, ...stubs }
})

import Quant from '../pages/Quant.jsx'
import { dispatch, __reset as resetBus } from '../sseBus.js'

// 假时钟下排空微任务链（同 §M13 口径：不用 waitFor，避免墙钟依赖）。
async function flushMicrotasks() {
  await act(async () => {
    await Promise.resolve()
    await Promise.resolve()
    await Promise.resolve()
    await Promise.resolve()
  })
}
async function settle(ms = 100) {
  await act(async () => { await vi.advanceTimersByTimeAsync(ms) })
}

async function stubs() {
  const api = await import('../api/index.js')
  return api
}

describe('§0926E2E-17A 轮询统一（SSE 事件驱动 + 60s 兜底）', () => {
  beforeEach(async () => {
    cleanup()
    localStorage.clear()
    // §0929GATE-403：P1/P2 测的都是 **admin 会话**的取数链（P2 的 403 止血正是"缓存说 admin、
    // 服务端仍拒"这一残余形态——预过滤拦不住它，所以 §M13 那条腿必须继续在 admin 角色下跑）。
    // 不写这行则挂载即被预过滤挡下，四路端点首屏一次都不发，本文件的增量断言会空转成假绿。
    localStorage.setItem('liangzai_role', 'admin')
    resetBus()
    vi.useFakeTimers()
    const api = await stubs()
    for (const fn of Object.values(api)) {
      if (typeof fn?.mockClear === 'function') fn.mockClear()
    }
  })
  afterEach(() => {
    vi.useRealTimers()
    cleanup()
    resetBus()
  })

  // P1：qmt_report 事件到达 → 链路状态/当日委托/待核对/流水四路立即重拉（零定时器推进）
  it('P1 SSE 事件即时刷新：dispatch 后四路数据端点调用数增长且从未推进时钟', async () => {
    const api = await stubs()
    render(<Quant />)
    await settle()
    expect(screen.queryByText(/无权限访问量化交易/), '前置：admin 正常态').not.toBeInTheDocument()
    // 清掉首屏调用，只看事件驱动的增量
    api.fetchQMTState.mockClear()
    api.fetchQMTOrders.mockClear()
    api.qmtPendingReview.mockClear()
    api.fetchQMTTrades.mockClear()
    await act(async () => { dispatch({ type: 'qmt_report', order_id: 'O-P1', status: '已成' }) })
    await flushMicrotasks()
    expect(api.fetchQMTState.mock.calls.length, '事件后应即时重拉 /api/qmt/state').toBeGreaterThan(0)
    expect(api.fetchQMTOrders.mock.calls.length, '事件后应即时重拉当日委托').toBeGreaterThan(0)
    expect(api.qmtPendingReview.mock.calls.length, '事件后应即时重拉待核对清单').toBeGreaterThan(0)
    expect(api.fetchQMTTrades.mock.calls.length, '事件后应即时重拉交易流水').toBeGreaterThan(0)
    // 反向确认不是定时器偷跑：本用例除 settle(100ms) 外未推进过任何定时器
  })

  // P2：403 止血落面板后，事件回调不得复活 admin 端点请求（stopPolling 已退订 SSE）
  it('P2 403 止血后再来事件：四路端点一次都不许再发', async () => {
    const api = await stubs()
    const ZH403 = Object.assign(new Error('无权限'), { status: 403 })
    api.fetchQMTState.mockImplementation(async () => { throw ZH403 })
    render(<Quant />)
    await settle()
    await flushMicrotasks()
    expect(screen.getByText(/无权限访问量化交易/), '前置：无权限面板已落地').toBeInTheDocument()
    api.fetchQMTState.mockClear()
    api.fetchQMTOrders.mockClear()
    api.qmtPendingReview.mockClear()
    api.fetchQMTTrades.mockClear()
    await act(async () => { dispatch({ type: 'qmt_report', order_id: 'O-P2', status: '已报' }) })
    await flushMicrotasks()
    expect(api.fetchQMTState.mock.calls.length, '§17A：止血后事件回调不得再拉 state').toBe(0)
    expect(api.fetchQMTOrders.mock.calls.length, '§17A：止血后事件回调不得再拉 orders').toBe(0)
    expect(api.qmtPendingReview.mock.calls.length, '§17A：止血后事件回调不得再拉 pending-review').toBe(0)
    expect(api.fetchQMTTrades.mock.calls.length, '§17A：止血后事件回调不得再拉 trades').toBe(0)
  })

  // P3：静态负锁——两页真实代码里不得复活 5s/10s/15s/30s 的 setInterval（防回潮）
  it('P3 静态锁：Dashboard/Quant 不再存在亚 60s 轮询字面量', () => {
    const stripComments = (src) => src
      .replace(/\/\*[\s\S]*?\*\//g, '')
      .split('\n').filter((l) => !l.trim().startsWith('//')).join('\n')
    const dash = stripComments(fs.readFileSync(path.join(HERE, '..', 'pages', 'Dashboard.jsx'), 'utf8'))
    const quant = stripComments(fs.readFileSync(path.join(HERE, '..', 'pages', 'Quant.jsx'), 'utf8'))
    const subMinute = /setInterval\s*\([^)]*?,\s*(5000|10000|15000|20000|30000)\s*\)/
    expect(subMinute.test(dash), 'Dashboard.jsx 复活了亚 60s 轮询（§0926E2E-17A）').toBe(false)
    expect(subMinute.test(quant), 'Quant.jsx 复活了亚 60s 轮询（§0926E2E-17A）').toBe(false)
    // 正向半锁：Quant 必须真的订阅了五类实盘事件（防"只删轮询不接事件"的单向收口）
    expect(quant).toMatch(/sseOn\(/)
    expect(quant).toMatch(/'qmt_report'/)
  })
})
