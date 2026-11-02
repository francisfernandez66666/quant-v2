// quant_trade_perm.test.jsx — §MULTI-ACCT-B 批5（2026-11-02）：第六枚权限位 quant_trade「量化交易」的前端接线回归。
//
// 后端本批把 11 条「自己的实盘读面 + 自己的实盘动作」改成「trading_admin 或 quant_trade 之一」，
// 前端必须**同源**跟上，否则会出现两种都很坏的形状：
//   ① 页面仍按 trading_admin 判 → 成员接口 200、入口进不去（新位被做成哑权限）；
//   ② 页面直接放宽成"任何登录用户" → quant_trade 成员一挂载就连打注定 403 的运营端点，
//      §0929GATE-403 花一整批消灭的 403 风暴回潮。
// 本文件因此两头都锁：该发的发（自己的账），不该发的零请求（运营专属腿）。
//
// English: the sixth permission bit must light up exactly the self-scoped live surfaces and stay
// out of the operator-only ones; both under-wiring and over-dialing are pinned here.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, cleanup, act } from '@testing-library/react'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { MemoryRouter } from 'react-router-dom'

const HERE = path.dirname(fileURLToPath(import.meta.url))

// 首屏端点默认「管理员正常空载荷」：只关心**拨没拨**，不关心渲染细节，载荷一律取合法空值。
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
    fetchQMTState: () => ({ enabled: true, tripped: false, gateway_url: 'http://127.0.0.1:18789' }),
    fetchQMTOrders: () => [],
    fetchQMTTrades: () => ({}),
    fetchQMTBroker: () => ({ ok: true, broker: 'xt', broker_connected: true }),
    fetchQMTSettleHistory: () => ({ diffs: [] }),
    qmtPendingReview: () => ({ orders: [], unresolved_count: 0 }),
    fetchRiskGates: () => ({ gates: [], switches: {} }),
    fetchCloseSnapshot: () => ({ available: false, reason: '今日尚未产出' }),
    fetchFillAmendments: () => [],
    fetchShortStatus: () => ({ short_enabled: false }),
    fetchPaperState: () => ({ enabled: false, is_admin: false, short_book: { enabled: false } }),
    fetchSignalVerdicts: () => ({ verdicts: [] }),
    fetchFundsToday: () => ({ day: '20261102', live: { state: 'pending' }, paper: { available: false } }),
    fetchLiveTRounds: () => ({ book: 'live', state: 'missing', rounds: [] }),
    updateQMTConfig: () => ({}),
  },
}))

vi.mock('../api/index.js', async () => {
  const actual = await vi.importActual('../api/index.js')
  const stubs = {}
  for (const [name, make] of Object.entries(OK_PAYLOADS)) {
    stubs[name] = typeof make === 'function' && make.mock ? make : vi.fn(async () => make())
  }
  return { ...actual, ...stubs }
})

import Quant from '../pages/Quant.jsx'
import FundsOverviewCard from '../components/FundsOverviewCard.jsx'
import { canStrict, canStrictAny, canLiveTrading, LIVE_TRAADING_PERMS } from '../utils/perm.js'
import { __reset as resetBus } from '../sseBus.js'

// 默认推进 120ms 够首轮取数落地；LONG_MS 越过 60s 一轮兜底轮询——
// 收盘对账那条腿只挂轮询（不在 startAdminReads 里立即打），不推进过 60s 就永远看不到它发没发，
// 那样 Q2/Q4 的"零请求"断言会变成假绿（用例自身没有牙）。
const LONG_MS = 70000
async function settle(ms = 120) {
  await act(async () => { await vi.advanceTimersByTimeAsync(ms) })
}
async function stubs() {
  return await import('../api/index.js')
}

// 会话构造：role 一律 'user'（canStrict 在 test 模式对 admin 有兼容兜底，只有 role=user
// 才测得出"判据是显式位、不是角色"），perms 直接写本地缓存——与真实 hasStrictPerm 同源。
function setPerms(perms) {
  localStorage.setItem('liangzai_role', 'user')
  localStorage.setItem('liangzai_perms', JSON.stringify(perms || []))
}

// 「本批放行的自账腿」与「仍在 trading_admin 名下的运营腿」——两份清单必须互斥且穷尽本页取数。
const SELF_LIVE_READS = ['fetchQMTState', 'fetchQMTOrders', 'fetchQMTTrades', 'fetchQMTConfig']
const OPERATOR_ONLY_READS = ['qmtPendingReview', 'fetchQMTBroker', 'fetchQMTSettleHistory', 'fetchRiskGates', 'fetchCloseSnapshot', 'fetchFillAmendments']

describe('§MULTI-ACCT-B 批5 quant_trade 权限接线', () => {
  beforeEach(() => {
    cleanup()
    localStorage.clear()
    vi.useFakeTimers()
    resetBus()
  })
  afterEach(() => {
    vi.useRealTimers()
    cleanup()
  })

  // Q1：perm.js 的"任一位"判定——quant_trade 单独成立，且它绝不等价于 trading_admin。
  it('Q1 canStrictAny/canLiveTrading：quant_trade 单独即可，且不隐式等于 trading_admin', () => {
    setPerms(['quant_trade'])
    expect(canLiveTrading(), '只持 quant_trade 应能进实盘读面').toBe(true)
    expect(canStrict('quant_trade')).toBe(true)
    expect(canStrict('trading_admin'), 'quant_trade 不得被读成 trading_admin').toBe(false)
    expect(canStrictAny([]), '空列表不得放行').toBe(false)
    setPerms(['trading_admin'])
    expect(canLiveTrading()).toBe(true)
    setPerms([])
    expect(canLiveTrading(), '零权限位成员仍不进（判据按位、不按角色）').toBe(false)
    setPerms(['funds_admin'])
    expect(canLiveTrading(), 'funds_admin 不该顺带进实盘面').toBe(false)
    // 单点真源：位名单只在 perm.js 写一次，页面不许各抄一份
    expect(LIVE_TRAADING_PERMS).toEqual(['trading_admin', 'quant_trade'])
  })

  // Q2：只持 quant_trade 的成员进 Quant 页——自账腿照常拉，运营腿零请求，无「无权限」面板。
  it('Q2 quant_trade 成员：自账腿点亮、运营专属腿一条不发', async () => {
    setPerms(['quant_trade'])
    const api = await stubs()
    for (const n of [...SELF_LIVE_READS, ...OPERATOR_ONLY_READS]) api[n].mockClear()
    render(<MemoryRouter><Quant /></MemoryRouter>)
    await settle(LONG_MS)
    expect(screen.queryByText(/无权限访问量化交易/), '持 quant_trade 应进得了页').not.toBeInTheDocument()
    for (const n of SELF_LIVE_READS) {
      expect(api[n].mock.calls.length, `本批放行的自账腿 ${n} 必须被点亮`).toBeGreaterThan(0)
    }
    for (const n of OPERATOR_ONLY_READS) {
      expect(api[n].mock.calls.length, `运营专属腿 ${n} 不得被 quant_trade 拨出（§0929GATE-403）`).toBe(0)
    }
    // 运营专属的三块面板（待核对人工改判 / 日终结算 / 通道切换行）整块不渲染，
    // 而不是渲染成"加载中"或"○○"那种**冒充有读数**的空壳。
    expect(screen.queryByText(/待核对委托（第三态人工收敛）/)).not.toBeInTheDocument()
    expect(screen.queryByText('日终结算对账')).not.toBeInTheDocument()
    expect(screen.queryByText('执行路径')).not.toBeInTheDocument()
    expect(screen.getByText(/收盘对账：当前权限（量化交易）不含该读数/), '收盘对账那一态必须如实说"不含"').toBeInTheDocument()
  })

  // Q3：equals-legacy——只持 trading_admin 的运营账号在本批之后**一条腿都没少**（正向对照）。
  //     少了这条，Q2 的"运营腿零请求"可能只是把整页取数都写坏了也能通过。
  it('Q3 trading_admin 等值对照：运营腿照常点亮（本批没削任何人的面）', async () => {
    setPerms(['trading_admin'])
    const api = await stubs()
    for (const n of [...SELF_LIVE_READS, ...OPERATOR_ONLY_READS]) api[n].mockClear()
    render(<MemoryRouter><Quant /></MemoryRouter>)
    await settle(LONG_MS)
    expect(screen.queryByText(/无权限访问量化交易/)).not.toBeInTheDocument()
    for (const n of [...SELF_LIVE_READS, ...OPERATOR_ONLY_READS]) {
      expect(api[n].mock.calls.length, `持 trading_admin 时 ${n} 应照旧被点亮`).toBeGreaterThan(0)
    }
    expect(screen.getByText('日终结算对账')).toBeInTheDocument()
    expect(screen.getByText('执行路径')).toBeInTheDocument()
  })

  // Q4：反证——零权限位成员仍然一条实盘请求都不发、无权限面板在场（本批没把页面降级成公开）。
  it('Q4 零权限位成员：实盘腿全零 + 无权限面板（放行只按位）', async () => {
    setPerms([])
    const api = await stubs()
    for (const n of [...SELF_LIVE_READS, ...OPERATOR_ONLY_READS]) api[n].mockClear()
    render(<MemoryRouter><Quant /></MemoryRouter>)
    await settle(LONG_MS)
    expect(screen.getByText(/无权限访问量化交易/), '零权限位应落无权限面板').toBeInTheDocument()
    for (const n of [...SELF_LIVE_READS, ...OPERATOR_ONLY_READS]) {
      expect(api[n].mock.calls.length, `${n} 不该被零权限位成员拨出`).toBe(0)
    }
  })

  // Q5：今日资金卡（GET /api/funds/today 本批改判为 funds_admin 或 quant_trade）同源跟上。
  it('Q5 资金卡：quant_trade 成员读得到自己那一帧，零权限位不空打', async () => {
    const api = await stubs()
    api.fetchFundsToday.mockClear()
    setPerms(['quant_trade'])
    render(<FundsOverviewCard />)
    await settle()
    expect(api.fetchFundsToday.mock.calls.length, 'quant_trade 应点亮资金帧').toBeGreaterThan(0)
    api.fetchFundsToday.mockClear()
    cleanup()
    setPerms([])
    render(<FundsOverviewCard />)
    await settle()
    expect(api.fetchFundsToday.mock.calls.length, '零权限位不得空打资金接口').toBe(0)
    api.fetchFundsToday.mockClear()
    cleanup()
    setPerms(['funds_admin'])
    render(<FundsOverviewCard />)
    await settle()
    expect(api.fetchFundsToday.mock.calls.length, 'funds_admin 的旧可达面不变').toBeGreaterThan(0)
  })

  // Q6：静态锁——权限清单渲染必须有「量化交易」中文标签；实盘页路由与侧栏入口都用任一档判据。
  //     这类"文案/接线"锁是防回潮：漏一处就是勾选框显示英文位名、或朋友持位却看不见入口。
  it('Q6 静态锁：标签文案 + 路由守卫 + 判据单点', () => {
    const read = (rel) => fs.readFileSync(path.join(HERE, '..', rel), 'utf8')
    const admin = read('pages/Admin.jsx')
    expect(admin, '用户管理页必须把 quant_trade 翻成「量化交易」').toMatch(/quant_trade:\s*'量化交易'/)
    const app = read('App.jsx')
    expect(app, '/quant 路由必须走 strictAny（trading_admin 或 quant_trade）')
      .toMatch(/path="\/quant" element=\{<ProtectedRoute strictAny=\{\['trading_admin', 'quant_trade'\]\}/)
    expect(app, '侧栏「量化交易」入口按 canLiveTrade（不是 canTrading）').toMatch(/canLiveTrade \? \{ to: '\/quant'/)
    expect(app, '设置页入口不许跟着放宽（那一页背后全是运营端点）').toMatch(/\(canConfig \|\| canTrading\) \? \{ to: '\/settings'/)
    const perm = read('utils/perm.js')
    expect(perm, '位名单单点定义在 perm.js，页面只 import').toMatch(/LIVE_TRAADING_PERMS = \['trading_admin', 'quant_trade'\]/)
    const quant = read('pages/Quant.jsx')
    expect(quant, 'Quant 页必须 import 单点判据而不是自抄位名').toMatch(/from '\.\.\/utils\/perm\.js'/)
    // 位名的**字符串字面量**只许出现在 perm.js 的名单里：页面里再抄一份 'quant_trade'，
    // 就意味着以后加/改名要逐页找（单点判据的同族教训：判据手抄子集会静默失效）。
    expect((quant.match(/'quant_trade'/g) || []).length, 'Quant.jsx 不得自写 quant_trade 字面量').toBe(0)
    expect((read('pages/Positions.jsx').match(/'quant_trade'/g) || []).length, 'Positions.jsx 不得自写位名').toBe(0)
    expect((read('pages/Dashboard.jsx').match(/'quant_trade'/g) || []).length, 'Dashboard.jsx 不得自写位名').toBe(0)
  })
})
