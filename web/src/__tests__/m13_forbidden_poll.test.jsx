// ── §M13（FIX_PLAN_20260922 §3 表 M13 行，2026-09-22 修复批 K）前端权限一致性反例锁 ──
//
// 缺陷原文（两条同族）：
//  ① Quant 页对成员账号（adminMiddleware 全 403）渲染了「无权限」面板，但挂载副作用里的
//    10s（链路状态/当日委托）与 30s（交易流水）轮询定时器照跑——每 10 秒再打三发 403，
//    把 opslog 灌成噪声（"降级不停摆"族缺陷的前端侧）；
//  ② Quant.jsx 原 :238 / Paper.jsx 原 :433 判定 403 用的是 e.message.indexOf('无权限')——
//    adminMiddleware 回中文「无权限」恰好命中，但 permMiddleware 回英文
//    "no permission: <perm>"（internal/server/server.go permMiddleware）必然漏判；
//    api/index.js 早已导出 §A5 的状态码判定 isForbidden()，两页却没用。
//
// 本文件四把锁（前三把行为锁 + 最后一把防复活静态锁）：
//  L1 成员进 Quant 页 → 403 → fake timers 推进 90s，admin 端点调用数一次都不许再增长；
//  L2 403 判定只认状态码：英文 403 也落无权限面板（L2a）；中文文案 + 非 403 状态码不落面板（L2b）；
//  L3 Paper 页同口径（英文 403 → 无权限面板；中文文案非 403 → 不面板）；
//  L4 源码静态锁：两页不得再出现 indexOf('无权限')/includes('无权限')，且必须用 api.isForbidden。
//
// 注：本文件只新增，不改写第一批既有测试（quant.test.jsx / perm_gate.test.jsx 等）的行为。
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, cleanup, act } from '@testing-library/react'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const HERE = path.dirname(fileURLToPath(import.meta.url))

// 403 错误工厂：adminMiddleware 的中文形态与 permMiddleware 的英文形态各一份，status 均为 403。
// 修复前两页只认中文那份 —— 这就是 L2a 的反例来源。
const ZH403 = () => Object.assign(new Error('无权限'), { status: 403 })
const EN403 = () => Object.assign(new Error('no permission: qmt.manage'), { status: 403 })

// OK 载荷：默认所有被桩端点回"管理员正常态"，各用例只覆盖自己要制造异常的那一个面。
// ⚠ vi.mock 工厂会被提升到文件顶部，工厂里引用的变量必须经 vi.hoisted 先就位，
//    否则运行期报 "Cannot access 'OK_PAYLOADS' before initialization"。
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
    fetchRiskGates: () => ({ gates: [], switches: {} }),
    fetchShortStatus: () => ({ short_enabled: false }),
    fetchPaperState: () => ({ enabled: false, is_admin: true, short_book: { enabled: false } }),
    fetchPaperPositions: () => [],
    fetchPaperTrades: () => [],
    fetchPaperOrders: () => [],
    fetchPaperEquity: () => [],
    fetchPaperStrategies: () => ({ strategies: [], known_strategies: [], blacklist: [] }),
    fetchPaperConfig: () => ({ enabled: false, engine_enabled: false, auto_sell: false }),
    fetchSignalVerdicts: () => ({ verdicts: [] }),
  },
}))

// vi.mock 会被提升到文件顶部：isForbidden 取真实实现（防"锁自己造假"），
// 端点函数全部预置为 OK 载荷的可控 vi.fn（用例里按需 mockRejectedValue 制造异常）。
vi.mock('../api/index.js', async () => {
  const actual = await vi.importActual('../api/index.js')
  const stubs = {}
  for (const [name, make] of Object.entries(OK_PAYLOADS)) {
    const fn = vi.fn(async () => make())
    fn.__make = make
    stubs[name] = fn
  }
  return { ...actual, ...stubs }
})

import Quant from '../pages/Quant.jsx'
import Paper from '../pages/Paper.jsx'

// flushMicrotasks：fake timers 下把 Promise 链跑干（不推进定时器）。
async function flushMicrotasks() {
  await act(async () => {
    await Promise.resolve()
    await Promise.resolve()
    await Promise.resolve()
  })
}

// settle 推进假时钟并 flush React 更新。fake timers 下不能用 testing-library 的
// findByText/waitFor —— 它们自己的超时也挂在假时钟上，永远等不到，最终撞 testTimeout。
async function settle(ms = 100) {
  await act(async () => { await vi.advanceTimersByTimeAsync(ms) })
}

// 成员账号也会 200 的端点（authMiddleware 只读面）：denyAll 时保持放行，
// 免得把"只有 admin 端点会 403"这一真实形态抹成"全站 403"，让 L1 失去区分度。
const NEVER_DENIED = new Set(['fetchSignalVerdicts'])

// resetStubs 复位所有桩到默认 OK 载荷并清空调用记录（用例间隔离）。
async function resetStubs() {
  const api = await import('../api/index.js')
  for (const [name, make] of Object.entries(OK_PAYLOADS)) {
    if (typeof api[name]?.mockImplementation !== 'function') continue
    api[name].mockClear()
    api[name].mockImplementation(async () => make())
  }
}

// denyAll 把首屏端点整体切成 403（模拟成员账号真实形态：adminMiddleware 全拒）。
async function denyAll(makeErr) {
  const api = await import('../api/index.js')
  for (const name of Object.keys(OK_PAYLOADS)) {
    if (NEVER_DENIED.has(name)) continue
    if (typeof api[name]?.mockImplementation !== 'function') continue
    api[name].mockImplementation(async () => { throw makeErr() })
  }
}

describe('§M13 前端权限一致性（403 停轮询 + 状态码判定）', () => {
  beforeEach(async () => {
    cleanup()
    localStorage.clear()
    vi.useFakeTimers()
    await resetStubs()
  })
  afterEach(() => {
    vi.useRealTimers()
    cleanup()
  })

  // L1：forbidden 生效后 10s/30s 三根定时器必须彻底停下（推进 90s，调用数一次都不涨）
  it('L1 成员进 Quant 页 403 后：admin 端点轮询请求数不再增长', async () => {
    const api = await import('../api/index.js')
    await denyAll(ZH403)
    render(<Quant />)
    await settle()
    // 无权限面板已渲染（面板文案含 emoji 与插值，故用正则匹配）
    expect(screen.getByText(/无权限访问量化交易/)).toBeInTheDocument()

    const snapshot = () => ({
      state: api.fetchQMTState.mock.calls.length,
      orders: api.fetchQMTOrders.mock.calls.length,
      trades: api.fetchQMTTrades.mock.calls.length,
      config: api.fetchQMTConfig.mock.calls.length,
      gates: api.fetchRiskGates.mock.calls.length,
      broker: api.fetchQMTBroker.mock.calls.length,
    })
    const before = snapshot()
    // 反向确认这不是空锁：首屏确实打过这些端点
    expect(before.state, '首屏应已拉过 /api/qmt/state').toBeGreaterThan(0)
    expect(before.config, '首屏应已拉过 /api/config/qmt').toBeGreaterThan(0)
    // 修复前的形态：stateTimer/ordersTimer 10s、tradesTimer 30s → 90s 内至少 +9/+9/+3 次
    await act(async () => { await vi.advanceTimersByTimeAsync(90000) })
    await flushMicrotasks()
    expect(snapshot(), '§M13：forbidden 后仍在轮询 admin 端点').toEqual(before)
  })

  // L2a：英文 403（permMiddleware 形态）也必须落无权限面板 —— 旧 indexOf('无权限') 在此漏判
  it('L2a 英文 no permission 的 403 同样判定为无权限（状态码判定，非文案判定）', async () => {
    const api = await import('../api/index.js')
    await denyAll(EN403)
    render(<Quant />)
    await settle()
    expect(screen.getByText(/无权限访问量化交易/)).toBeInTheDocument()
    // 且同样止血：轮询已停
    const before = api.fetchQMTState.mock.calls.length
    await act(async () => { await vi.advanceTimersByTimeAsync(60000) })
    await flushMicrotasks()
    expect(api.fetchQMTState.mock.calls.length).toBe(before)
  })

  // L2b：文案含「无权限」但状态码不是 403（此处 500）——不得误判成无权限面板（不认文案）
  it('L2b 中文文案 + 非 403 状态码不再被当成无权限', async () => {
    const api = await import('../api/index.js')
    api.fetchQMTConfig.mockRejectedValue(Object.assign(new Error('服务端提示：无权限（文案样本）'), { status: 500 }))
    render(<Quant />)
    await settle()
    expect(screen.queryByText(/无权限访问量化交易/)).not.toBeInTheDocument()
    // 走的是"本机缓存不可信"告警分支，而不是权限面板
    expect(screen.getByText(/实盘配置加载失败/)).toBeInTheDocument()
  })

  // L3：Paper 页同口径（该页只有一根 SSE 兜底轮询，403 时同样停用）
  it('L3 Paper 页：英文 403 落无权限面板，中文文案非 403 不落', async () => {
    const api = await import('../api/index.js')
    api.fetchPaperState.mockRejectedValue(EN403())
    const { unmount } = render(<Paper />)
    await settle()
    expect(screen.getByText(/无权限访问模拟盘/)).toBeInTheDocument()
    // §M13：SSE 兜底轮询随 forbidden 停用（推进 90s 不再拉 state）
    const before = api.fetchPaperState.mock.calls.length
    await act(async () => { await vi.advanceTimersByTimeAsync(90000) })
    await flushMicrotasks()
    expect(api.fetchPaperState.mock.calls.length, 'forbidden 后 useSseRefresh 兜底轮询仍在跑').toBe(before)
    unmount()

    cleanup()
    await resetStubs()
    api.fetchPaperState.mockRejectedValue(Object.assign(new Error('无权限（文案样本）'), { status: 401 }))
    render(<Paper />)
    await settle()
    expect(screen.queryByText(/无权限访问模拟盘/)).not.toBeInTheDocument()
  })

  // L4：源码静态负向锁——防"文案匹配"复活（回归测试会漂移，负向锁不会）
  it('L4 静态锁：两页不得按文案判权限，必须用 api.isForbidden，Quant 必须有停轮询函数', () => {
    // 先剥掉注释（本批修复的注释里会引用旧写法作为说明，负向锁只看真实代码）
    const stripComments = (src) => src
      .replace(/\/\*[\s\S]*?\*\//g, '')
      .split('\n').filter((l) => !l.trim().startsWith('//')).join('\n')
    const quant = stripComments(fs.readFileSync(path.join(HERE, '..', 'pages', 'Quant.jsx'), 'utf8'))
    const paper = stripComments(fs.readFileSync(path.join(HERE, '..', 'pages', 'Paper.jsx'), 'utf8'))
    const msgMatch = /indexOf\(\s*['"]无权限|includes\(\s*['"]无权限/
    expect(msgMatch.test(quant), 'Quant.jsx 复活了按文案判 403 的写法（§M13）').toBe(false)
    expect(msgMatch.test(paper), 'Paper.jsx 复活了按文案判 403 的写法（§M13）').toBe(false)
    expect(quant).toMatch(/api\.isForbidden\(/)
    expect(paper).toMatch(/api\.isForbidden\(/)
    // 止血面：必须存在统一清除三根定时器的函数，且 403 分支会调用它
    expect(quant).toMatch(/function stopPolling\(\)/)
    expect(quant).toMatch(/noteForbidden/)
    expect(quant).toMatch(/setForbidden\(true\)/)
  })
})
