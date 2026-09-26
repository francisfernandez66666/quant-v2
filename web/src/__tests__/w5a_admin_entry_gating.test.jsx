// ── §0926E2E-17d（FIX_PLAN_20260926E2E 四波 17d 项）成员可见 admin 入口灰化 回归用例 ──
//
// 缺陷原文：Quant 页写侧端点全在 adminMiddleware 下；成员会话首屏拿到 403、落了「无权限」面板，
// 但面板下方的实盘开关/保存按钮/紧急停止/撤单/待核对改判等入口照旧可点——"能点却必 403"
// 本身就是审计报告点名的体验缺陷（§五-17 灰化条）。
//
// 三把锁：
//  G1 成员态（403 → forbidden）：写控件一律 disabled + tooltip 原因；点击「保存网关参数」
//     不得向 updateQMTConfig 发任何请求（逻辑层 patch 守卫兜底，双闸同锁）。
//  G2 管理员态反证：同一批控件必须 enabled——灰化只属于已锤实的 403 会话，
//     防"把成员体验修成管理员功能残废"的单向闸（等值锁，非单向锁）。
//  G3 静态锁：forbiddenHintProps/ADMIN_ONLY_HINT 必须真实存在于 Quant.jsx（防回潮）。
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, cleanup, act, fireEvent } from '@testing-library/react'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const HERE = path.dirname(fileURLToPath(import.meta.url))

// 首屏端点默认「管理员正常空载荷」（与 §17A 锁同源）
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
    updateQMTConfig: vi.fn(async () => ({})),
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
import { __reset as resetBus } from '../sseBus.js'

async function settle(ms = 100) {
  await act(async () => { await vi.advanceTimersByTimeAsync(ms) })
}
async function stubs() {
  return await import('../api/index.js')
}

// TDesign Button 在 disabled=true 时将根节点降级为 <div type="button">（无 button role），
// getByRole 找不到——这正是灰化生效的形态证明。按文本定位后回看根节点属性。
function rootOfText(text) {
  const el = screen.getByText(text)
  return el.closest('div[type="button"], button') || el.parentElement
}

describe('§0926E2E-17d 成员可见 admin 入口灰化', () => {
  beforeEach(async () => {
    cleanup()
    localStorage.clear()
    resetBus()
    vi.useFakeTimers()
    const api = await stubs()
    for (const fn of Object.values(api)) {
      if (typeof fn?.mockClear === 'function') fn.mockClear()
    }
    // mockClear 不清实现：G1 在 fetchQMTState 上抛过 403，须逐例复位回默认载荷，
    // 否则 G2 会继承上一条的"毒实现"落进无权限面板（同文件用例互染的自伤形态）。
    for (const [name, make] of Object.entries(OK_PAYLOADS)) {
      if (typeof api[name]?.mockImplementation === 'function') {
        api[name].mockImplementation(async () => (typeof make === 'function' && make._isMockFunction ? {} : make()))
      }
    }
  })
  afterEach(() => {
    vi.useRealTimers()
    cleanup()
    resetBus()
  })

  // G1：403 会话 → 面板 + 可见写控件全灰 + 点击不发出任何保存请求 + 开关灰化
  it('G1 成员态：可见写入口 disabled+tooltip，点击「保存网关参数」零请求', async () => {
    const api = await stubs()
    const ZH403 = Object.assign(new Error('无权限'), { status: 403 })
    api.fetchQMTState.mockImplementation(async () => { throw ZH403 })
    render(<Quant />)
    await settle()
    expect(screen.getByText(/无权限访问量化交易/), '前置：无权限面板').toBeInTheDocument()
    api.updateQMTConfig.mockClear()
    // 成员态下确定在 DOM 的三枚写按钮（其余受 state 加载闸控制，走 G1b 静态腿）
    for (const label of ['保存网关参数', '保存仓位纪律', '已同步']) {
      const btn = rootOfText(label)
      expect(btn, `前置：成员态「${label}」应在页面`).not.toBeNull()
      expect('disabled' in btn ? btn.disabled : btn.hasAttribute('disabled'),
        `成员态「${label}」必须禁用（§0926E2E-17d）`).toBe(true)
      expect(btn.getAttribute('title'), `「${label}」禁用须带原因 tooltip`).toContain('仅管理员')
    }
    // 逻辑层第二道闸：即便绕过 disabled 直接触发点击，也不得发出写请求
    fireEvent.click(rootOfText('保存网关参数'))
    await settle()
    expect(api.updateQMTConfig.mock.calls.length, '成员态保存请求一次都不许发出').toBe(0)
    // 实盘总开关（role=switch）同样灰化
    const sw = screen.getAllByRole('switch')[0]
    expect(sw.disabled, '成员态实盘总开关必须禁用').toBe(true)
  })

  // G1b：受 state 加载闸控制的写入口（紧急停止/撤单/改判/对账/切券商/分段开关）——
  // 成员态下 state 恒 null、DOM 打不到这些控件（真实路由打不到的死支不写 HTTP 断言），
  // 改按源码接线逐枚静态锁定：disabled 表达式必须消费 forbidden。
  it('G1b 成员态（state 未就绪打不到的写入口）：源码逐枚静态禁能锁', () => {
    const src = fs.readFileSync(path.join(HERE, '..', 'pages', 'Quant.jsx'), 'utf8')
    // 下列正则已在落码源码上逐枚验证（JSX 里 onClick={() => ...} 的括号/大括号须逐个转义）
    for (const [label, re] of [
      ['紧急停止', /disabled={forbidden}\s*title={forbidden \? ADMIN_ONLY_HINT : undefined}/],
      ['立即对账', /loading={settleBusy} disabled={forbidden}/],
      ['撤单', /disabled={forbidden} title={forbidden \? ADMIN_ONLY_HINT : undefined} onClick=\{\(\) => cancelOrder/],
      ['柜台无此单', /disabled={confirmBusy \|\| forbidden} title={forbidden \? ADMIN_ONLY_HINT : undefined} onClick=\{\(\) => confirmPendingOrder\(row, 'released'\)/],
      ['柜台有此单', /disabled={confirmBusy \|\| forbidden} title={forbidden \? ADMIN_ONLY_HINT : undefined} onClick=\{\(\) => confirmPendingOrder\(row, 'settled'\)/],
      ['切到 miniQMT', /disabled={active === 'xt' \|\| forbidden}/],
      ['切到 QMT桥', /disabled={active === 'queued' \|\| forbidden}/],
      ['保存战法开关', /disabled={!strategyDirty \|\| saving \|\| forbidden}/],
      ['实盘总开关灰化', /checked={form.enabled} disabled={forbidden}/],
      ['自动卖出灰化', /checked={form.auto_sell} disabled={forbidden}/],
    ]) {
      expect(re.test(src), `§0926E2E-17d：「${label}」的 forbidden 禁能接线被拆除`).toBe(true)
    }
    // 分段切换（span 形态）灰化接线 + 逻辑层 patch 守卫双在
    expect(src).toMatch(/\.\.\.segBtnForbiddenPatch/)
    expect(src).toMatch(/if \(forbidden\) \{ MessagePlugin\.warning\(ADMIN_ONLY_HINT\); return \}/)
  })

  // G2：管理员态反证——同一批控件必须可用（灰化不得误伤 admin）
  it('G2 admin 态反证：同一批写控件全部 enabled', async () => {
    render(<Quant />)
    await settle()
    expect(screen.queryByText(/无权限访问量化交易/), '前置：admin 正常态').not.toBeInTheDocument()
    for (const label of ['保存网关参数', '保存仓位纪律']) {
      const btn = rootOfText(label)
      expect(btn.hasAttribute('disabled'), `admin 态「${label}」不得被灰化（§0926E2E-17d 等值锁）`).toBe(false)
      expect(btn.getAttribute('title'), `admin 态「${label}」不应挂禁用 tooltip`).toBeNull()
    }
    const sw = screen.getAllByRole('switch')[0]
    expect(sw.disabled, 'admin 态实盘总开关不得被灰化').toBe(false)
  })

  // G3：静态防回潮锁——灰化机制的两枚关键字面量必须还在（注释行除外）
  it('G3 静态锁：Quant.jsx 保留 forbiddenHintProps 与 ADMIN_ONLY_HINT 接线', () => {
    const src = fs.readFileSync(path.join(HERE, '..', 'pages', 'Quant.jsx'), 'utf8')
      .split('\n').filter((l) => !l.trim().startsWith('//')).join('\n')
    expect(src).toMatch(/const ADMIN_ONLY_HINT\s*=/)
    expect(src).toMatch(/const forbiddenHintProps\s*=\s*forbidden/)
    expect(src, 'patch() 逻辑层守卫必须在位').toMatch(/if \(forbidden\) \{ MessagePlugin\.warning\(ADMIN_ONLY_HINT\); return \}/)
  })
})
