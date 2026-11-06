// §MANUAL-BOOK-UNIFY（2026-11-06 晚批）实仓单一出入口的行为锁。
//
// 方案 §7.2 五条前端锁在这里落成**行为**断言（门禁 §173 那组是静态源码锁，两条腿互补）：
//  ① 独立手动维护面板已删（组件文件与挂载点双双不在册）；
//  ② 柜台可达＝下真单、不可达＝改账，两个态的确认按钮文案**不同形**；
//  ③ 改账态才有「改账·新增」与「直接改资金」两个入口；
//  ④ 来源角标按 book_source 渲染，unknown 态显示「读数未知」、绝不显示 0；
//  ⑤ manualSource.js 两句身份文案仍是全站唯一源（在 manual_book_signal_card 用例里锁）。
//
// 为什么行为腿只断言状态行/入口、列标题与弹窗文案走静态源码：TDesign Table 在 jsdom 下
// 不保证展开单元格，把锁挂在它身上会变成"夹具绿而代码没验"；状态行与入口挂在 Tabs 内的
// 普通 div 上，与 §W7-G 那组页头断言同一形状，稳定可达。
// English: behaviour tests for the three book-source states; table-cell shape is locked in source.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/react'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const HERE = path.dirname(fileURLToPath(import.meta.url))

const { state } = vi.hoisted(() => ({
  state: {
    bookSource: null,
    positions: [{ ts_code: '600000.SH', name: '浦发银行', qty: 1000, cost_price: 8.5 }],
    account: { available_cash: 88000, updated_at: '2026-11-06 09:30:00' },
  },
}))

// api 层整体替身：只把实仓页真会拨的几条换成受控夹具，其余走 importActual，
// 免得「替身没定义」被读成「页面不调用它」。book_source 三态由 state 逐条喂，
// 页面只认它分流，不在前端再判一次新鲜度（方案 §7.2 第 4 条）。
// English: controlled API stub; the three book-source states are injected per case.
vi.mock('../api/index.js', async () => {
  const actual = await vi.importActual('../api/index.js')
  return {
    ...actual,
    isAdmin: vi.fn(() => true),
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
    executeRealAction: vi.fn(async () => ({ status: 'ok' })),
    adjustManualBook: vi.fn(async () => ({ status: 'ok', adjust: { op: 'add' } })),
  }
})

import Positions from '../pages/Positions.jsx'

// openRealBook 与 §W7-G 同姿势：点击「实盘持仓」标签触发 loadReal（轮询与 SSE 都从这一下开始）。
async function openRealBook() {
  fireEvent.click(await screen.findByText(/实盘持仓/))
  await waitFor(() => expect(screen.getByTestId('real-book-source')).toBeInTheDocument())
}

// 三种后端单点装配的来源读数：两维都 counter / 两维都 manual / 载荷不带键（unknown）。
const COUNTER = { cash: 'counter', positions: 'counter', cash_asof: '', positions_asof: '', taken_over_at: '' }
const MANUAL = {
  cash: 'manual', positions: 'manual',
  cash_asof: '2026-11-06 09:30:00', positions_asof: '2026-11-06 09:30:00', taken_over_at: '',
}

describe('§MANUAL-BOOK-UNIFY 实仓三态：可达下真单／不可达改账／未知不猜', () => {
  beforeEach(() => {
    cleanup()
    localStorage.clear()
    state.positions = [{ ts_code: '600000.SH', name: '浦发银行', qty: 1000, cost_price: 8.5 }]
    state.account = { available_cash: 88000, updated_at: '2026-11-06 09:30:00' }
    state.bookSource = COUNTER
  })
  afterEach(() => cleanup())

  it('柜台可达：状态行报「可达」，改账两个入口都不出现（裁决 2 的下真单态）', async () => {
    render(<Positions />)
    await openRealBook()
    const strip = await screen.findByTestId('real-book-source')
    expect(strip.textContent).toContain('柜台：可达')
    expect(screen.getByTestId('counter-reachable')).toBeInTheDocument()
    expect(screen.queryByTestId('counter-unreachable')).not.toBeInTheDocument()
    expect(screen.queryByTestId('manual-new-entry')).not.toBeInTheDocument()
    expect(screen.queryByTestId('manual-set-cash-entry')).not.toBeInTheDocument()
  })

  it('柜台不可达：状态行报「改账态」，两个改账入口都出现且带来源身份句', async () => {
    state.bookSource = MANUAL
    render(<Positions />)
    await openRealBook()
    const strip = await screen.findByTestId('real-book-source')
    expect(strip.textContent).toContain('柜台：不可达 · 改账态')
    expect(strip.textContent).toContain('非柜台实时读数')
    expect(screen.getByTestId('counter-unreachable')).toBeInTheDocument()
    expect(screen.getByTestId('manual-new-entry')).toBeInTheDocument()
    expect(screen.getByTestId('manual-set-cash-entry')).toBeInTheDocument()
    expect(screen.queryByTestId('counter-reachable')).not.toBeInTheDocument()
  })

  it('读数未知：显示「读数未知」四个字，绝不显示 0，也不自动切改账', async () => {
    state.bookSource = null
    render(<Positions />)
    await openRealBook()
    const strip = await screen.findByTestId('real-book-source')
    expect(strip.textContent).toContain('读数未知')
    expect(screen.getByTestId('book-unknown')).toBeInTheDocument()
    // 「没读到」与「读到 0」是两件事：状态行不许出现任何数字金额。
    expect(strip.textContent).not.toMatch(/[¥￥]\s*0|0\.00/)
    expect(screen.queryByTestId('manual-new-entry')).not.toBeInTheDocument()
    expect(screen.queryByTestId('counter-reachable')).not.toBeInTheDocument()
  })

  it('负锁：独立面板文件与挂载点都不在册（裁决 2 的单一出入口）', () => {
    const panel = path.join(HERE, '..', 'components', 'ManualBookPanel.jsx')
    expect(fs.existsSync(panel), 'ManualBookPanel.jsx 必须已删除').toBe(false)
    const raw = fs.readFileSync(path.join(HERE, '..', 'pages', 'Positions.jsx'), 'utf-8')
    expect(raw.includes('ManualBookPanel'), '挂载点与注释都不许再提这个组件').toBe(false)
  })

  it('源码锁：列标题随态分流、两个态确认按钮不同形、行级来源角标按 posSrc 渲染', () => {
    const raw = fs.readFileSync(path.join(HERE, '..', 'pages', 'Positions.jsx'), 'utf-8')
    const src = raw.replace(/^[ \t]*\/\/.*$/gm, '').replace(/\{?\/\*[\s\S]*?\*\/\}?/g, '')
    expect(src.length, '夹具自检：源码要读到位').toBeGreaterThan(1000)
    // ① 列标题与按钮分流都挂在 manualMode 上（改回一律真单会让这条红）。
    expect((src.match(/title: manualMode \? '改账' : '操作'/g) || []).length).toBe(1)
    expect((src.match(/manualMode \? openManualAdjust\(row, 'add'\) : openRealAction\(row, 'add'\)/g) || []).length).toBe(1)
    // ② 真单＝「确认下单」，改账＝「确认改账 · 不下单」，两串必须同时在位且不等。
    expect(src).toContain('确认下单')
    expect(src).toContain('确认改账 · 不下单')
    expect((src.match(/disabled=\{manualMode \? false : realTripped\}/g) || []).length,
      '桥断熔断时四个改账按钮必须仍可点（这才是不达态唯一能用上的时刻）').toBe(4)
    // ③ 行级来源角标只在持仓维确认为 manual 时挂。
    expect((src.match(/posSrc === 'manual'/g) || []).length).toBeGreaterThanOrEqual(1)
    expect((src.match(/data-testid="manual-row-badge"/g) || []).length).toBe(1)
  })
})
