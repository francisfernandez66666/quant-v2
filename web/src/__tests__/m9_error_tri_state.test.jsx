// ── §M-9（FIX_PLAN_20260922PM §三 M-9 行，2026-09-22 修复批）前端「错误伪装空态/加载态」三态分离四例 ──
//
// 缺陷四处（后端 500 时不得出现空态文案）：
//  ① Signals.jsx  load() catch 吞错 → 表格落到 empty="暂无信号"（错误伪装成空态）；
//  ② MsgCenter.jsx load() catch 吞错 → 「暂无消息」（同族）；
//  ③ Quant.jsx    loadOrders 失败不 setOrders 也不记错 → 永远「加载委托列表…」（错误伪装成加载态）；
//  ④ Dashboard.jsx 三个健康端点仅挂载拉一次且 catch(()=>{}) 吞错、不在 10s 轮询里 →
//     健康点二态 `x?'●':'○'` 把「从未拉到」画成 ○（伪故障）、成功后永不更新——
//     修法参照同文件 §M3 newsMark 三态样板：并入轮询 + healthMark unknown 显示「–」。
//
// 用例命名 E1~E5：四页各一把行为锁 + Dashboard 轮询并入锁。
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, cleanup, act } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'

// 每页挂载触达的端点默认回「管理员正常空载荷」，用例只覆盖自己要制造异常的那一个面。
const { state } = vi.hoisted(() => ({ state: {} }))

function okPayloads() {
  return {
    isAdmin: () => false,
    getAccount: () => 'admin',
    fetchStatus: () => ({ session: 'test', signal_count: 0 }),
    setLastSession: () => {},
    connectSSE: () => {},
    fetchSignals: () => [],
    fetchPaperState: () => ({ enabled: false }),
    fetchShortStatus: () => ({ short_enabled: false }),
    fetchAlerts: () => [],
    // Quant
    fetchQMTConfig: () => ({ enabled: false, known_strategies: [], strategies: [] }),
    fetchQMTState: () => ({ enabled: false, tripped: false }),
    fetchQMTOrders: () => [],
    fetchQMTTrades: () => ({}),
    fetchQMTBroker: () => ({ broker: 'xt' }),
    fetchQMTSettleHistory: () => ({ diffs: [] }),
    fetchRiskGates: () => ({ gates: [], switches: {} }),
    fetchSignalVerdicts: () => ({ verdicts: [] }),
    // Dashboard
    fetchNews: () => [],
    fetchSectorHot: () => [],
    fetchHotSnapshot: () => [],
    fetchIPOCalendar: () => [],
    fetchDashboard: () => ({}),
    fetchDataSourceHealth: () => ({ eastmoney: true, sina: true, tencent: true, ths: true }),
    fetchNewsSourceHealth: () => ({}),
    fetchEngineHealth: () => ({ engine: {} }),
    fetchEmotionHistory: () => ({ series: [] }),
    fetchEmotionStrategyMatrix: () => ({ rows: [] }),
  }
}

vi.mock('../api/index.js', async () => {
  const actual = await vi.importActual('../api/index.js')
  const stubs = {}
  for (const name of Object.keys(okPayloads())) {
    stubs[name] = vi.fn(async (...args) => state.impl[name](...args))
  }
  return { ...actual, ...stubs }
})

import Signals from '../pages/Signals.jsx'
import MsgCenter from '../pages/MsgCenter.jsx'
import Quant from '../pages/Quant.jsx'
import Dashboard from '../pages/Dashboard.jsx'

const ERR500 = (msg) => Object.assign(new Error(msg), { status: 500 })

// resetStubs 复位默认载荷并清空调用记录（用例间隔离）。
async function resetStubs() {
  state.impl = okPayloads()
  const api = await import('../api/index.js')
  for (const name of Object.keys(state.impl)) {
    if (typeof api[name]?.mockClear === 'function') api[name].mockClear()
  }
}

describe('§M-9 三态分离：后端 500 不得伪装成空态/加载态', () => {
  beforeEach(async () => {
    cleanup()
    localStorage.clear()
    await resetStubs()
  })
  afterEach(() => cleanup())

  // E1 Signals：fetchSignals 500 → 出现可重试错误提示，绝不出现「暂无信号」
  it('E1 Signals 首载 500 → 错误态独立渲染（不出现「暂无信号」空态）', async () => {
    state.impl.fetchSignals = () => { throw ERR500('信号接口 500') }
    render(<Signals />)
    // 错误横幅与表格空行 error 分支都含「信号加载失败」字样：分别按精确文本断言，
    // 且「暂无信号」空态文案必须缺席（修复前 catch 吞错直接落空态，必红）。
    const banner = await screen.findByText(/⚠ 信号加载失败：/, {}, { timeout: 5000 })
    expect(banner).toBeInTheDocument()
    expect(screen.getByText('信号加载失败，请点上方「重试」')).toBeInTheDocument() // 表格空行三态的 error 分支
    expect(screen.queryByText('暂无信号'), '§M-9：错误不得伪装成空态').not.toBeInTheDocument()
    expect(screen.getAllByRole('button', { name: '重试' }).length).toBeGreaterThan(0)
  })

  // E2 MsgCenter：fetchAlerts 500 → 错误+重试，绝不出现「暂无消息」
  it('E2 MsgCenter 首载 500 → 错误态独立渲染（不出现「暂无消息」空态）', async () => {
    state.impl.fetchAlerts = () => { throw ERR500('消息接口 500') }
    render(<MsgCenter />)
    const err = await screen.findByText(/消息加载失败：消息接口 500/, {}, { timeout: 5000 })
    expect(err).toBeInTheDocument()
    expect(screen.queryByText('暂无消息'), '§M-9：错误不得伪装成空态').not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: '重试' })).toBeInTheDocument()
  })

  // E3 Quant：loadOrders 500（非 403）→ 委托卡渲染错误+重试，不再永远「加载委托列表…」
  it('E3 Quant 委托 500 → 错误态独立渲染（不永远卡在「加载委托列表…」）', async () => {
    state.impl.fetchQMTOrders = () => { throw ERR500('委托服务暂不可用') }
    render(<Quant />)
    const err = await screen.findByText(/委托服务暂不可用/, {}, { timeout: 5000 })
    expect(err).toBeInTheDocument()
    expect(screen.queryByText('加载委托列表…'), '§M-9：错误不得伪装成永久加载态').not.toBeInTheDocument()
  })

  // E4 Dashboard：三个健康端点全挂 → 健康点必须画 '–'（unknown），不得伪装 ● 也不得画成故障 ○；
  // 参照同文件 newsMark 三态样板（unknown=–）。
  it('E4 Dashboard 健康端点 500 → 三态「–」，不出现伪故障「○」也不出现伪健康「●」', async () => {
    state.impl.fetchDataSourceHealth = () => { throw ERR500('ds down') }
    state.impl.fetchNewsSourceHealth = () => { throw ERR500('ns down') }
    state.impl.fetchEngineHealth = () => { throw ERR500('eh down') }
    const { container } = render(<MemoryRouter><Dashboard /></MemoryRouter>)
    await waitForText(container, /数据源：东财–/)
    const sys = container.textContent || ''
    expect(sys).toMatch(/数据源：东财– 新浪– 腾讯– 同花顺–/)
    expect(sys, '§M-9：未取到的健康位不得画成确证故障 ○').not.toMatch(/数据源：.*○/)
    expect(sys).toMatch(/流程引擎：新闻抓取–/)
  })

  // E5 Dashboard：健康端点已并入 load() 的 10s 主轮询（修复前只挂载拉一次）
  it('E5 Dashboard 健康端点随 10s 轮询重复拉取（不再只挂载一次）', async () => {
    vi.useFakeTimers()
    try {
      const api = await import('../api/index.js')
      const { container } = render(<MemoryRouter><Dashboard /></MemoryRouter>)
      await act(async () => { await vi.advanceTimersByTimeAsync(50) })
      const first = api.fetchDataSourceHealth.mock.calls.length
      expect(first, '挂载即应拉一次').toBeGreaterThanOrEqual(1)
      await act(async () => { await vi.advanceTimersByTimeAsync(10500) })
      expect(api.fetchDataSourceHealth.mock.calls.length, '§M-9：健康端点必须并入 10s 轮询').toBeGreaterThan(first)
      expect(container).toBeTruthy()
    } finally {
      vi.useRealTimers()
    }
  })
})

// waitForText 轮询等容器文本就绪（fake/real timers 混用区只在本文件 E4 用真实定时器）。
async function waitForText(container, re) {
  const deadline = Date.now() + 5000
  for (;;) {
    if (re.test(container.textContent || '')) return
    if (Date.now() > deadline) throw new Error('等待文本超时: ' + re)
    await act(async () => { await Promise.resolve() })
  }
}
