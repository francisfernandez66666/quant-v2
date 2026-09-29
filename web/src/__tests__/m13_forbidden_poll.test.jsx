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
import { render, screen, cleanup, act, fireEvent } from '@testing-library/react'
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
    // §0929GATE-403：补齐 Quant 首屏余下两条 admin 只读（待核对清单、勘误台账），
    // 让 denyAll 覆盖到它们、并让 L8 能按调用数逐枚证明"成员态一条都没发"。
    qmtPendingReview: () => ({ orders: [], unresolved_count: 0 }),
    fetchFillAmendments: () => ({ amendments: [] }),
    // §0929FILL-NAME：名称旁证列的批量读（auth 面，成员也可读，不参与预过滤计数断言）
    fetchStockNames: () => ({ names: {} }),
    // §0929GATE-403「刷新身份并重试」的角色重确认腿
    refreshMe: () => ({ role: 'admin' }),
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
    // §0929GATE-403：消息中心未读提醒（L12 用它证明"成员态一条都不拨"）
    fetchAlerts: () => [],
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
    // §0929GATE-403 起，成员会话在挂载期就被预过滤挡下（一条 admin 请求都不发），
    // 本文件的 L1/L2 测的是**另一条仍然成立的路**：本地缓存说"我是 admin"、服务端却回 403
    // （角色刚被下调 / 缓存与权威值漂移）。这正是 §M13 止血不可替代的原因，
    // 所以这些用例必须以 admin 角色挂载，让首屏真的发出请求，才能验到"403 后停止轮询"。
    localStorage.setItem('liangzai_role', 'admin')
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

// ─────────────────────────────────────────────────────────────────────────────
// §M-6（FIX_PLAN_20260922PM §三 M-6 行，2026-09-22 修复批）止血残余：
// 在飞 promise 链的链尾漏发 + 挂载期壳端点 catch 吞 403。
//
// 为什么 L1 测不到：L1 的 snapshot 取在 `settle()` 之后——整条 loadTrades 链
// （trades → verdicts → risk/gates）已经跑完，比较的是「之后不再涨」；
// 缺陷发生在「403 落地那一刻链还挂在半路、链尾 fetchRiskGates 随后照发」的窗口里，
// 定时器快照对此完全失明（本轮唯一红 MP-3 的代码半就是这么漏的）。
// L5 用受控 deferred 把该窗口钉开：trades 第一步按住不 release，期间 state 的
// 403 先落地触发 noteForbidden，再 release trades——修复后链上守卫必须拦住
// verdicts 与链尾 risk/gates 两次后续请求。
// ─────────────────────────────────────────────────────────────────────────────
describe('§M-6 在飞链止血（链尾不漏发 + 挂载壳端点 403/非 403 分流）', () => {
  beforeEach(async () => {
    cleanup()
    localStorage.clear()
    // 同上（§0929GATE-403）：L5/L6 验的是"请求已发出、403 在半路落地"的止血窗口，
    // 必须让首屏真的发得出请求，故按 admin 角色挂载。
    localStorage.setItem('liangzai_role', 'admin')
    vi.useFakeTimers()
    await resetStubs()
  })
  afterEach(() => {
    vi.useRealTimers()
    cleanup()
  })

  // L5：403 在链第一步在飞期间落地 → 链后续步骤（含链尾 admin 端点 fetchRiskGates）一发都不许再出网
  it('L5 在飞 loadTrades 链：403 落地后链尾不得漏发 /api/risk/gates', async () => {
    const api = await import('../api/index.js')
    // trades 按住（可控 release），state 立即 403——真实还原"链在飞的瞬间 403 到达"
    let resolveTrades
    api.fetchQMTTrades.mockImplementation(() => new Promise((res) => { resolveTrades = res }))
    api.fetchQMTState.mockImplementation(async () => { throw ZH403() })
    render(<Quant />)
    await settle() // 让 state 的 403 完成 noteForbidden → stopPolling + pollingDeadRef 置位
    expect(screen.getByText(/无权限访问量化交易/), '前置：无权限面板已落地').toBeInTheDocument()
    // 此刻链第一步仍 pending；修复前它 release 后会连发 verdicts + risk/gates（链尾漏发）
    expect(api.fetchRiskGates, '前置：链尚未走到链尾').not.toHaveBeenCalled()
    await act(async () => { resolveTrades({ summary: {} }) })
    await flushMicrotasks()
    expect(api.fetchRiskGates, '§M-6：403 后在飞链尾仍漏发 /api/risk/gates').not.toHaveBeenCalled()
    expect(api.fetchSignalVerdicts, '§M-6：链上守卫应拦掉后续所有步骤（verdicts 同样不发）').not.toHaveBeenCalled()
    // 反向确认链首确实执行过（防"整链没跑"造成的空锁假绿）
    expect(api.fetchQMTTrades.mock.calls.length, '前置：链首 trades 确实发过').toBeGreaterThan(0)
  })

  // L6：挂载期 fetchShortStatus / fetchPaperState 的 catch 参与 noteForbidden——
  // 403 必须止血落面板；500/503 必须只降级、绝不冒成"无权限"（分流反例各锁一把）。
  it('L6 壳端点 403 参与止血；503/500 不误判成无权限', async () => {
    const api = await import('../api/index.js')
    // ① 只让 fetchShortStatus 回英文 403，其余 admin 端点全部正常——旧实现 catch(()=>{})
    //    会把它整个吞掉：面板不出、轮询不停（§M-6 修的正是这类"链外 403 漏判"）。
    api.fetchShortStatus.mockRejectedValue(EN403())
    const { unmount } = render(<Quant />)
    await settle()
    expect(screen.getByText(/无权限访问量化交易/), 'fetchShortStatus 的 403 必须触发无权限面板').toBeInTheDocument()
    const before = api.fetchQMTState.mock.calls.length
    await act(async () => { await vi.advanceTimersByTimeAsync(60000) })
    await flushMicrotasks()
    expect(api.fetchQMTState.mock.calls.length, '面板落地后轮询必须已停').toBe(before)
    unmount()

    // ② 分流反例：两个壳端点回 500（服务异常）——不得被误判成无权限
    cleanup()
    await resetStubs()
    api.fetchShortStatus.mockRejectedValue(Object.assign(new Error('short/status 炸了'), { status: 500 }))
    api.fetchPaperState.mockRejectedValue(Object.assign(new Error('paper/state 不可用'), { status: 503 }))
    render(<Quant />)
    await settle()
    expect(screen.queryByText(/无权限访问量化交易/), '500/503 绝不能分流成无权限（§M-6）').not.toBeInTheDocument()
    // 且轮询照常活着（未被误杀）：推进 60s，state 至少又打了 5 次
    const b2 = api.fetchQMTState.mock.calls.length
    await act(async () => { await vi.advanceTimersByTimeAsync(60000) })
    expect(api.fetchQMTState.mock.calls.length, '非 403 不得误停轮询').toBeGreaterThan(b2)
  })

  // L7：静态锁——stopPolling 必须同时置失效标志，链上必须存在逐步守卫（防回潮）
  it('L7 静态锁：stopPolling 置 pollingDeadRef 且 loadTrades 链上逐步检查', () => {
    const src = fs.readFileSync(path.join(HERE, '..', 'pages', 'Quant.jsx'), 'utf8')
    expect(src).toMatch(/const pollingDeadRef = useRef\(false\)/)
    // stopPolling 体内必须置位（clearInterval 管不住在飞链）
    const stopBody = src.slice(src.indexOf('function stopPolling()'), src.indexOf('function noteForbidden'))
    expect(stopBody).toMatch(/pollingDeadRef\.current = true/)
    // 链上守卫次数：入口 + 每步 await 后（≥3 处）
    const loadTradesBody = src.slice(src.indexOf('async function loadTrades()'), src.indexOf('async function loadState()'))
    const guards = loadTradesBody.match(/pollingDeadRef\.current/g) || []
    expect(guards.length, 'loadTrades 链上守卫不足（§M-6）').toBeGreaterThanOrEqual(3)
    // StrictMode 双挂载兼容：挂载 effect 必须复位标志
    expect(src).toMatch(/pollingDeadRef\.current = false/)
    // 壳端点 catch 不得再回退成吞错（catch(()=>{}) 形态）
    expect(src).toMatch(/fetchShortStatus\(\)[\s\S]{0,200}?catch\(\(e\) => \{ noteForbidden\(e\)/)
    expect(src).toMatch(/fetchPaperState\(\)[\s\S]{0,200}?catch\(\(e\) => \{ noteForbidden\(e\)/)
  })
})

// ─────────────────────────────────────────────────────────────────────────────
// §0929GATE-403（FIX_PLAN_20260929 ⑨-1）成员首屏 403 风暴预过滤
//
// 缺陷原文：成员停在 /api/quant 首屏连吃 9 条 403（state/orders/trades/broker/settle-history/
// pending-review/config/qmt/risk-gates/fill-amendments），另在 /api/dashboard 吃 /api/qmt/state、
// /api/positions 吃 /api/positions/advice、/api/consult 吃 /api/config/llm、/api/msgcenter 吃
// /api/metrics/alerts，且 App 的全局状态轮询每一轮都在后台重放最后一条。
// 后端判得对，缺的是前端"明知必拒还照样拨"。
//
// 三把锁（含两把反证）：
//  L8  成员态：admin 只读一条都不发（含 120s 后仍零增长），但成员可读的壳端点照常拉
//      ——预过滤不许被写成"成员整页瞎掉"；
//  L9  「刷新身份并重试」反证：服务器仍回 member 时，点了也**不会**解除预过滤（前端不自授权限）；
//  L10 「刷新身份并重试」正证：服务器回 admin 即拉起取数链（缓存旧值不得把人永久锁在门外）；
//  L11 静态锁：五处判据入口（Quant 单入口 / FillAmendPanel 收 canQuery / Dashboard / MsgCenter / App）
//      的接线必须都在，防"只修了 Quant 一页"的半截收口回潮。
// ─────────────────────────────────────────────────────────────────────────────
describe('§0929GATE-403 成员首屏预过滤（本地角色缓存只决定发不发，后端仍是唯一裁决）', () => {
  // 成员只读不到的那批：与 Quant.jsx startAdminReads 的取数面一一对应
  const ADMIN_READS = [
    'fetchQMTConfig', 'fetchQMTState', 'fetchQMTOrders', 'fetchQMTTrades', 'fetchQMTBroker',
    'fetchQMTSettleHistory', 'qmtPendingReview', 'fetchFillAmendments', 'fetchRiskGates',
  ]

  beforeEach(async () => {
    cleanup()
    localStorage.clear()
    // 成员会话：isAdmin 走真实实现（读 liangzai_role），缺省即 'user'
    localStorage.setItem('liangzai_role', 'user')
    vi.useFakeTimers()
    await resetStubs()
  })
  afterEach(() => {
    vi.useRealTimers()
    cleanup()
  })

  // L8：成员挂载 → admin 只读零请求 + 无权限面板在场 + 成员可读端点照常
  it('L8 成员态：admin 只读一次都不发，壳端点仍照常拉（预过滤不等于整页失明）', async () => {
    const api = await import('../api/index.js')
    render(<Quant />)
    await settle()
    await flushMicrotasks()
    expect(screen.getByText(/无权限访问量化交易/), '成员态应落无权限面板').toBeInTheDocument()
    for (const name of ADMIN_READS) {
      expect(api[name].mock.calls.length, `§0929GATE-403：成员态不得拨 ${name}`).toBe(0)
    }
    // 成员可读的两条壳端点必须照常发出（这两条挂在 authMiddleware 下，不在预过滤范围内）
    expect(api.fetchShortStatus.mock.calls.length, 'fetchShortStatus 成员可读，不得被误伤').toBeGreaterThan(0)
    expect(api.fetchPaperState.mock.calls.length, 'fetchPaperState 成员可读，不得被误伤').toBeGreaterThan(0)
    // 120s 兜底轮询窗口内仍然零请求：预过滤是"根本没起定时器"，不是"起后被 403 停掉"
    await act(async () => { await vi.advanceTimersByTimeAsync(120000) })
    await flushMicrotasks()
    for (const name of ADMIN_READS) {
      expect(api[name].mock.calls.length, `§0929GATE-403：推进 120s 后 ${name} 仍不得有请求`).toBe(0)
    }
  })

  // L9：反证——服务器仍确认是 member 时，点「刷新身份并重试」不得解除预过滤
  it('L9 重试反证：服务器仍回 member 时零请求（本地缓存不能自我授权）', async () => {
    const api = await import('../api/index.js')
    api.refreshMe.mockImplementation(async () => ({ role: 'user' }))
    render(<Quant />)
    await settle()
    await flushMicrotasks()
    fireEvent.click(screen.getByText('刷新身份并重试'))
    await settle()
    await flushMicrotasks()
    expect(api.refreshMe.mock.calls.length, '重试应向服务器重确认角色').toBeGreaterThan(0)
    for (const name of ADMIN_READS) {
      expect(api[name].mock.calls.length, `§0929GATE-403：仍为 member 时 ${name} 不得被点亮`).toBe(0)
    }
    expect(screen.getByText(/无权限访问量化交易/), '服务器未升权 → 面板保持').toBeInTheDocument()
  })

  // L10：正证——服务器回 admin（本地缓存是旧值）即拉起取数链，含勘误台账
  it('L10 重试正证：服务器确认 admin 后解除预过滤并拉起取数链', async () => {
    const api = await import('../api/index.js')
    // 与真实 refreshMe 的写侧契约同形：重确认成功后把角色写回本地缓存（否则页面判据不会翻转）
    api.refreshMe.mockImplementation(async () => {
      localStorage.setItem('liangzai_role', 'admin')
      return { role: 'admin' }
    })
    render(<Quant />)
    await settle()
    await flushMicrotasks()
    for (const name of ADMIN_READS) {
      expect(api[name].mock.calls.length, `前置：成员态 ${name} 应为零`).toBe(0)
    }
    fireEvent.click(screen.getByText('刷新身份并重试'))
    await settle()
    await flushMicrotasks()
    expect(screen.queryByText(/无权限访问量化交易/), '升权后面板应撤除').not.toBeInTheDocument()
    expect(api.fetchQMTState.mock.calls.length, '升权后应立刻拉起链路状态').toBeGreaterThan(0)
    expect(api.fetchQMTConfig.mock.calls.length, '升权后应立刻拉起实盘配置').toBeGreaterThan(0)
    expect(api.fetchFillAmendments.mock.calls.length, '升权后勘误台账才开拨').toBeGreaterThan(0)
  })

  // L11：静态锁——五处预过滤接线必须都在（防"只修一页"与"判据散落多处"两种回潮）
  it('L11 静态锁：五页预过滤接线齐备，且 Quant 判据只有单入口', () => {
    const stripComments = (src) => src
      .replace(/\/\*[\s\S]*?\*\//g, '')
      .split('\n').filter((l) => !l.trim().startsWith('//')).join('\n')
    const read = (rel) => stripComments(fs.readFileSync(path.join(HERE, '..', rel), 'utf8'))
    const quant = read('pages/Quant.jsx')
    // ① 单入口：角色判据只在 adminReadsAllowed 里出现一次，挂载与 SSE 回调都调它
    expect(quant).toMatch(/function adminReadsAllowed\(\) \{\s*return api\.isAdmin\(\)/)
    expect(quant).toMatch(/if \(adminReadsAllowed\(\)\) \{\s*startAdminReads\(\)/)
    expect(quant.match(/api\.isAdmin\(\)/g) || [], 'Quant.jsx 里 api.isAdmin() 只该有一处（判据单入口）').toHaveLength(1)
    // ② 出口：重试走服务器重确认，不是本地改角色
    expect(quant).toMatch(/await api\.refreshMe\(\)/)
    // ③ FillAmendPanel 收上层结论，自己不读角色
    const amend = read('components/FillAmendPanel.jsx')
    expect(amend).toMatch(/canQuery = true/)
    expect(amend).toMatch(/if \(!canQuery\) \{\s*setRows\(\[\]\)/)
    expect(amend.match(/api\.isAdmin\(\)/g) || [], 'FillAmendPanel 不得自行读角色缓存').toHaveLength(0)
    expect(quant).toMatch(/canQuery=\{adminReadsAllowed\(\)\}/)
    // ④ 其余三页 + App 全局轮询的早退接线
    expect(read('pages/Dashboard.jsx')).toMatch(/async function loadQMT\(\) \{\s*if \(!api\.isAdmin\(\)\) return/)
    expect(read('pages/MsgCenter.jsx')).toMatch(/async function load\(\) \{\s*if \(!admin\) \{/)
    expect(read('pages/Consult.jsx')).toMatch(/if \(!admin\) \{\s*setLlmGated\(true\)/)
    expect(read('App.jsx')).toMatch(/if \(api\.isAdmin\(\)\) \{\s*try \{\s*const alerts = await api\.fetchAlerts\(\)/)
    // ⑤ Positions：实盘建议回填并入既有 §PERM-GATE 的 admin 早退
    const pos = read('pages/Positions.jsx')
    expect(pos).toMatch(/if \(!admin\) return \/\/ §PERM-GATE/)
    expect(pos).toMatch(/if \(admin\) \{\s*api\.fetchRealAdvice\(\)/)
  })
})
