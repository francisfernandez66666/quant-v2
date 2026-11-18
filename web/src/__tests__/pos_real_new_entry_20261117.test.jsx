// ── 2026-11-17 owner 需求：持仓页「新增实盘持仓」入口加回来（纸面按钮仍隐藏）──
//
// 背景：§HIDE-PAPER-TAB（2026-11-16）把纸面持仓 tab 整体摘除后，页头那枚「+ 新增持仓（纸面）」
// 也跟着不见了，而实盘 tab 的唯一录入口是状态条里改账态的「改账·新增」——柜台可达（真单态）
// 时页面**没有任何新增入口**，想建一笔仓只能走行内加仓（要求标的已经在账上）。
//
// 设计：一个按钮两种含义，判据与行内四动作**同源**（不另立第三套判断）：
//   · manualMode（柜台不可达/手动维护）⇒ 走「改账·新增」（写 manual_*，不下单）；
//   · 否则 canTrading ⇒ 新建真单弹窗（代码可编辑，走 execute）；
//   · 都不是 ⇒ 不渲染（与行内操作列显示 '—' 同口径）。
//
// 本文件锁的是**行为**：
//  N1 真单态：按钮在位 → 点开是真单弹窗（代码可编辑）→ 提交走 executeRealAction，
//     载荷方向/动作/理由/自动补后缀四件都对（reason=manual:new 是"建仓"与"加仓"的分界）；
//  N2 改账态：同一枚按钮打开的是改账弹窗（不下单），executeRealAction **一次都不许被调用**；
//  N3 无权限且非改账态：按钮不渲染（不能给一个点了必 403 的入口）；
//  N4 纸面入口仍然隐藏：纸面 tab 与「+ 新增持仓（纸面）」都不在 DOM 里。
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/react'

const { state } = vi.hoisted(() => ({
  state: {
    bookSource: null,
    positions: [],
    account: { available_cash: 88000, updated_at: '2026-11-17 09:30:00' },
    admin: true,
    perms: [],
  },
}))

// api 层整体替身：只把持仓页真会拨的几条换成受控夹具，其余走 importActual。
// 判据钉在"产品代码确实调了哪个出口、载荷是什么"，而不是界面长得像。
vi.mock('../api/index.js', async () => {
  const actual = await vi.importActual('../api/index.js')
  return {
    ...actual,
    isAdmin: vi.fn(() => state.admin),
    hasStrictPerm: vi.fn((p) => state.perms.indexOf(p) >= 0),
    getAccount: vi.fn(() => 'admin'),
    fetchStatus: vi.fn(async () => ({ session: 'test' })),
    setLastSession: vi.fn(),
    fetchHoldings: vi.fn(async () => ({
      holdings: [], available_balance: 100,
      total_realized_pnl: 0, total_unrealized_pnl: 0, pnl_offset: 0, total_pnl: 0,
    })),
    resetPaperPnlOffset: vi.fn(async () => ({ status: 'ok', pnl_offset: 0 })),
    updateHoldingsBalance: vi.fn(async () => ({ status: 'ok' })),
    updateHoldings: vi.fn(async () => ({ status: 'ok' })),
    fetchQMTState: vi.fn(async () => ({ enabled: true, tripped: false })),
    fetchRealPositions: vi.fn(async () => ({
      positions: state.positions,
      account: state.account,
      ...(state.bookSource ? { book_source: state.bookSource } : {}),
    })),
    fetchQMTTrades: vi.fn(async () => null),
    fetchRealAdvice: vi.fn(async () => ({ advices: [] })),
    executeRealAction: vi.fn(async () => ({ status: 'ok', order_id: 'OID-1' })),
    adjustManualBook: vi.fn(async () => ({ status: 'ok', adjust: { op: 'new' } })),
  }
})

import * as api from '../api/index.js'
import Positions from '../pages/Positions.jsx'

const COUNTER = {
  cash: 'counter', positions: 'counter',
  cash_asof: '2026-11-17 09:30:00', positions_asof: '2026-11-17 09:30:00', taken_over_at: '',
}
const MANUAL = {
  cash: 'manual', positions: 'manual',
  cash_asof: '2026-11-17 09:20:00', positions_asof: '2026-11-17 09:20:00', taken_over_at: '',
}

// 取唯一一张可见弹窗里的按钮（TDesign Dialog 关闭后节点仍留在 DOM，只把容器 display 置 none；
// 直接把 getByRole 扔给整页会在"开过第二张弹窗之后"变成 multiple elements）。
function clickVisibleDialogButton(label) {
  const ctxs = [...document.querySelectorAll('.t-dialog__ctx')]
    .filter((n) => getComputedStyle(n).display !== 'none')
  expect(ctxs.length, '必须恰好一张可见弹窗（多张＝上一张没关，判据会读错对象）').toBe(1)
  const btn = [...ctxs[0].querySelectorAll('button')].find((b) => (b.textContent || '').includes(label))
  expect(btn, '可见弹窗里没有按钮：' + label).toBeTruthy()
  fireEvent.click(btn)
}

// 可见弹窗内的输入框（按 placeholder 定位；TDesign Input/InputNumber 都是原生 input）
function visibleDialogInput(placeholder) {
  const ctxs = [...document.querySelectorAll('.t-dialog__ctx')]
    .filter((n) => getComputedStyle(n).display !== 'none')
  expect(ctxs.length, '必须恰好一张可见弹窗').toBe(1)
  const el = ctxs[0].querySelector(`input[placeholder="${placeholder}"]`)
  expect(el, '可见弹窗里没有输入框：' + placeholder).toBeTruthy()
  return el
}

function fillDialog(placeholder, value) {
  fireEvent.change(visibleDialogInput(placeholder), { target: { value: String(value) } })
}

describe('2026-11-17 持仓页「新增实盘持仓」入口', () => {
  beforeEach(() => {
    cleanup()
    localStorage.clear()
    state.bookSource = null
    state.positions = []
    state.admin = true
    state.perms = []
    vi.clearAllMocks()
  })
  afterEach(() => { cleanup() })

  it('N1 真单态：按钮在位，点开是可编辑代码的真单弹窗，提交走 execute 且理由=manual:new', async () => {
    state.bookSource = COUNTER
    render(<Positions />)
    const entry = await screen.findByTestId('real-new-entry')
    fireEvent.click(entry)

    // 真单弹窗：代码可编辑（行内四动作没有这一栏，因为标的已在账上）
    await screen.findByPlaceholderText('输入代码（如 601958 或 600000.SH）')
    fillDialog('输入代码（如 601958 或 600000.SH）', '601958')
    fillDialog('成交参考价', 10.5)
    fillDialog('股数（一手=100）', 200)
    clickVisibleDialogButton('确认下单')

    await waitFor(() => expect(api.executeRealAction).toHaveBeenCalledTimes(1), { timeout: 5000 })
    const payload = api.executeRealAction.mock.calls[0][0]
    // 6 位纯数字自动补交易所后缀：建仓单的代码也必须是带后缀的真实标的
    expect(payload.code, '6 位纯数字必须补成 601958.SH（与改账新增同一个 normalizeManualCode）').toBe('601958.SH')
    expect(payload.side, '新建仓只能是买入（§SIDE-AUTH-2 方向必填）').toBe('买入')
    expect(payload.action, '动作词沿用后端词表').toBe('加仓')
    expect(payload.qty).toBe(200)
    expect(payload.price).toBe(10.5)
    expect(payload.reason, '建仓与加仓必须分键：manual:new ≠ manual:add').toBe('manual:new')
    expect(payload.client_id, '§P2-15 幂等键必须在').toBeTruthy()
  })

  it('N2 改账态：同一枚按钮打开改账入口（不下单），execute 一次都不许被调用', async () => {
    state.bookSource = MANUAL
    render(<Positions />)
    fireEvent.click(await screen.findByTestId('real-new-entry'))

    // 2026-11-18 owner 需求：改账态这枚按钮改开**批量录入**弹窗（一次 1..N 只票）；
    // 单行改账仍在状态行的「改账·新增」上。两条腿都写同一个端点、都不下单。
    // 判据保留原来的两条：弹窗自己的标识 + 确认按钮必须说清"不下单"。
    await screen.findByTestId('manual-batch-dialog')
    expect(api.executeRealAction).not.toHaveBeenCalled()
    const ctxs = [...document.querySelectorAll('.t-dialog__ctx')]
      .filter((n) => getComputedStyle(n).display !== 'none')
    expect(ctxs[0].textContent).toContain('不下单')
  })

  it('N3 无 trading 权限且非改账态：入口不渲染（不给点了必 403 的按钮）', async () => {
    state.admin = false
    state.perms = []
    state.bookSource = COUNTER
    render(<Positions />)
    await screen.findByText('持仓数据截至', { exact: false })
    expect(screen.queryByTestId('real-new-entry')).toBeNull()
  })

  it('N4 纸面入口仍然隐藏：纸面 tab 与「+ 新增持仓（纸面）」都不在 DOM', async () => {
    state.bookSource = COUNTER
    render(<Positions />)
    await screen.findByTestId('real-new-entry')
    expect(screen.queryByText('纸面持仓')).toBeNull()
    expect(screen.queryByText(/新增持仓（纸面）/)).toBeNull()
  })
})
