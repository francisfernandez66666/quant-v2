// ── §0929FILL-NAME（FIX_PLAN_20260929 ⑦ P2-2）实盘成交流水「名称」旁证列 ──
//
// 缺陷原文：GET /api/qmt/trades 的每一行都没有 name 键（fills 表根本没有这一列），
// 交易流水页只有代码，人工核对柜台回单时得拿代码去反查名字。
// owner 裁决走**零风险方案**：前端按代码向本地股票池表（GET /api/stock/names）问名字，
// 只作展示旁证——不给账本加列、更不让名称参与幂等锚（那是 §M4 的出事形态）。
//
// 本文件的五把锁：
//  T1 渲染形状：命中的代码出名字，查不到的出「—」（不是空串、不是代码复读）；
//  T2 批量与去重：一次流水刷新只发**一个**请求，同一代码出现两笔也只问一次；
//  T3 不重复拨号：轮询/SSE 再触发 loadTrades 时，已问过的代码不得再打后端；
//  T4 旁证故障不牵主链路：名称查询失败→流水照常渲染、轮询照常走、不弹 403 止血；
//  T5 边界负锁：名称绝不进提交体/定位锚（rowKey 仍是 order_id，勘误提交体仍只三键），
//     且封装层对空代码集一个请求都不发。
//
// English: five locks on the read-only name side-evidence column — honest rendering, one batched
// request, no re-dialling of known codes, failures degrade without touching the main chain, and
// names never enter any submit body or identity anchor.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, cleanup, act } from '@testing-library/react'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const HERE = path.dirname(fileURLToPath(import.meta.url))

// 首屏只读端点的「管理员正常空载荷」；fetchQMTTrades 这里带回两笔成交（同码两笔 + 一笔未知码）。
// vi.hoisted 是必需的：vi.mock 工厂会被提到文件顶部，工厂里引用的常量必须同批提前初始化
// （否则 "Cannot access 'OK_PAYLOADS' before initialization"——被 mock 的模块解析期就炸）。
const { FILLS, OK_PAYLOADS } = vi.hoisted(() => {
  const FILLS = [
    { id: 1, order_id: 'O-1', code: '600000.SH', side: '买入', price: 10, qty: 100, amount: 1000, traded_at: '2026-09-29T09:31:00', strategy: 'dragon', fee: 5, stamp_tax: 0 },
    { id: 2, order_id: 'O-2', code: '600000.SH', side: '卖出', price: 11, qty: 100, amount: 1100, traded_at: '2026-09-29T10:11:00', strategy: 'dragon', fee: 5, stamp_tax: 5 },
    { id: 3, order_id: 'O-3', code: '999999.SZ', side: '买入', price: 8, qty: 200, amount: 1600, traded_at: '2026-09-29T10:21:00', strategy: 'manual', fee: 4, stamp_tax: 0 },
  ]
  const OK_PAYLOADS = {
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
    fetchQMTTrades: () => ({ summary: { total_pnl: 0, realized: 0, unrealized: 0, win_rate: 0 }, by_strategy: [], fills: FILLS }),
    fetchQMTBroker: () => ({ ok: true, broker: 'xt', broker_connected: true }),
    fetchQMTSettleHistory: () => ({ diffs: [] }),
    qmtPendingReview: () => ({ orders: [], unresolved_count: 0 }),
    fetchRiskGates: () => ({ gates: [], switches: {} }),
    fetchShortStatus: () => ({ short_enabled: false }),
    fetchPaperState: () => ({ enabled: false, is_admin: true, short_book: { enabled: false } }),
    fetchSignalVerdicts: () => ({ verdicts: [] }),
    fetchFillAmendments: () => ({ ok: '1', amendments: [] }),
    fetchFillConservation: () => ({ ok: '1', report: null }),
    // 名称旁证默认只命中一个码（999999.SZ 刻意不在返回里 → 走「—」腿）
    fetchStockNames: (codes) => {
      const names = {}
      ;(codes || []).forEach((c) => { if (c === '600000.SH') names[c] = '浦发银行' })
      return { names }
    },
  }
  return { FILLS, OK_PAYLOADS }
})

vi.mock('../api/index.js', async () => {
  const actual = await vi.importActual('../api/index.js')
  const stubs = {}
  for (const [name, make] of Object.entries(OK_PAYLOADS)) {
    stubs[name] = vi.fn(async (...args) => make(...args))
  }
  return { ...actual, ...stubs }
})

import Quant from '../pages/Quant.jsx'
import * as api from '../api/index.js'
import { dispatch, __reset as resetBus } from '../sseBus.js'

async function flushMicrotasks() {
  await act(async () => {
    await Promise.resolve(); await Promise.resolve(); await Promise.resolve(); await Promise.resolve()
  })
}
async function settle(ms = 100) {
  await act(async () => { await vi.advanceTimersByTimeAsync(ms) })
}

// 取某一笔成交所在行（按代码定位），用于断言该行的名称单元格。
function rowOf(code) {
  const all = screen.getAllByText(code)
  expect(all.length, `找不到代码 ${code} 所在行`).toBeGreaterThan(0)
  const tr = all[0] && all[0].closest ? all[0].closest('tr') : null
  expect(tr, `代码 ${code} 的单元格不在表格行内`).not.toBeNull()
  return tr
}

describe('§0929FILL-NAME 成交流水名称旁证列', () => {
  beforeEach(() => {
    cleanup()
    localStorage.clear()
    // §0929GATE-403：本文件测的是"实盘流水表里的名称列"，只有 admin 会话才拉得到流水，
    // 故按管理员角色挂载（isAdmin 走 importActual 的真实实现，判据即这条 localStorage）。
    localStorage.setItem('liangzai_role', 'admin')
    resetBus()
    vi.useFakeTimers()
    for (const fn of Object.values(api)) {
      if (typeof fn?.mockClear === 'function') fn.mockClear()
    }
  })
  afterEach(() => {
    vi.useRealTimers()
    cleanup()
    resetBus()
  })

  // T1：命中给名字、缺失给「—」，且绝不把代码复读成名字。
  it('T1 命中行显示股票名，查不到的行显示「—」', async () => {
    render(<Quant />)
    await settle()
    await flushMicrotasks()
    expect(screen.getAllByText('浦发银行'), '命中的 600000.SH 两笔都要显示股票名').toHaveLength(2)
    const r = rowOf('999999.SZ')
    expect(r.textContent).toContain('—')
    expect(r.textContent, '空串会让单元格看起来像"有名字但是空白"').not.toContain('undefined')
  })

  // T2：一次刷新一个批量请求，同码两笔只问一次（防成 N 行打 N 次的洪峰形态）。
  it('T2 一批代码只发一个请求，重复代码去重', async () => {
    render(<Quant />)
    await settle()
    await flushMicrotasks()
    expect(api.fetchStockNames).toHaveBeenCalledTimes(1)
    const arg = api.fetchStockNames.mock.calls[0][0]
    expect(Array.from(new Set(arg)).sort()).toEqual(['600000.SH', '999999.SZ'])
    expect(arg.length, '600000.SH 两笔成交只能问一次').toBe(2)
  })

  // T3：SSE 事件再触发 loadTrades → 已问过的代码不得重复拨号。
  it('T3 事件驱动二次刷新不再重复问同一批代码', async () => {
    render(<Quant />)
    await settle()
    await flushMicrotasks()
    expect(api.fetchStockNames).toHaveBeenCalledTimes(1)
    await act(async () => { dispatch({ type: 'qmt_report', order_id: 'O-1', status: '已成' }) })
    await flushMicrotasks()
    expect(api.fetchQMTTrades.mock.calls.length, '前置：事件确实二次拉了流水').toBeGreaterThan(1)
    expect(api.fetchStockNames).toHaveBeenCalledTimes(1)
  })

  // T4：名称通道故障＝流水照常、轮询照常、不触发 403 止血。
  it('T4 名称查询失败只让该列退化，不牵动流水与轮询', async () => {
    api.fetchStockNames.mockImplementation(async () => { throw Object.assign(new Error('boom'), { status: 500 }) })
    render(<Quant />)
    await settle()
    await flushMicrotasks()
    expect(screen.getByText('交易流水与整体盈亏')).toBeInTheDocument()
    expect(screen.queryByText(/无权限访问量化交易/), '§M13：旁证故障不是权限信号，不得触发止血面板').not.toBeInTheDocument()
    // 名称列整列退化后，成交行仍必须在（主链路数据不受牵连）
    expect(screen.getByText('999999.SZ')).toBeInTheDocument()
    api.fetchQMTTrades.mockClear()
    await act(async () => { dispatch({ type: 'qmt_report', order_id: 'O-2', status: '已成' }) })
    await flushMicrotasks()
    expect(api.fetchQMTTrades.mock.calls.length, '止血未误触发：数据面轮询照常').toBeGreaterThan(0)
  })

  // T5a：静态边界——rowKey 仍是 order_id（名称列不得变成行主键：两只股票同名即 React key 冲突）。
  it('T5a 流水表行主键仍是 order_id，未改用名称', () => {
    const src = fs.readFileSync(path.join(HERE, '..', 'pages', 'Quant.jsx'), 'utf8')
    const m = src.match(/<Table data=\{trades\.fills\} columns=\{fillsColumns\} rowKey="([^"]+)"/)
    expect(m, '找不到成交流水表的 rowKey 声明').not.toBeNull()
    expect(m[1]).toBe('order_id')
    // 名称列的取值只来自 fillNames 映射，不来自行对象（行对象里根本没有 name）
    const col = src.match(/colKey: 'name', title: '名称'[\s\S]{0,160}/)
    expect(col, '成交流水名称列丢失').not.toBeNull()
    expect(col[0]).toContain('fillNames[fillNameKey(row.code)]')
    expect(col[0], '名称列不得回退成读行对象里的 name').not.toContain('row.name')
  })

  // T5b：静态边界——勘误提交体仍是三键，名称与锚点都不得进入（与 store 侧幂等锚负锁同源）。
  it('T5b 勘误提交体不含 name，锚点仍由服务端读出', () => {
    const src = fs.readFileSync(path.join(HERE, '..', 'api', 'index.js'), 'utf8')
    expect(src).toContain('data: { fill_id: fillId, new_side: newSide, reason }')
    expect(/data: \{[^}]*name/.test(src.match(/data: \{ fill_id[\s\S]{0,80}/)[0]), '勘误提交体又长出名称字段').toBe(false)
  })

  // T5c：封装层边界——空代码集一个请求都不发；非空必须走 GET /api/stock/names 且逗号已编码。
  it('T5c 封装层：空集合不发请求，非空走 GET /api/stock/names', async () => {
    vi.useRealTimers()
    const calls = []
    vi.stubGlobal('fetch', vi.fn(async (url, opts) => {
      calls.push([url, opts && opts.method])
      return new Response(JSON.stringify({ names: {} }), { status: 200 })
    }))
    // 取**未 mock 的真实实现**：本文件顶部把整个 api 模块桩化了，
    // 直接 import 会拿到 vi.fn 桩，锁的就成了测试自己写的假封装。
    const real = await vi.importActual('../api/index.js')
    await real.fetchStockNames([])
    await real.fetchStockNames(['', '   '])
    await real.fetchStockNames(null)
    expect(calls.length, '空代码集不许打后端').toBe(0)
    const r = await real.fetchStockNames(['600000.SH', '000001.SZ'])
    expect(calls.length).toBe(1)
    expect(calls[0][0]).toContain('/api/stock/names?codes=')
    expect(calls[0][0], '逗号已编码成 %2C，裸逗号会让查询串歧义').not.toContain(',')
    expect(calls[0][1], '旁证查询必须是 GET').toBe('GET')
    expect(r).toEqual({ names: {} })
    vi.unstubAllGlobals()
  })
})
