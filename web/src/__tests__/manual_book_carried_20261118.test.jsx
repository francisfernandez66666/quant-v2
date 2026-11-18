// §MANUAL-CARRIED（2026-11-18 owner 裁决）：「手动账跨日不失效，但必须自报是哪天的账」的前端锁。
//
// 背景（11-18 早上的现场）：facai2026 前夜手敲的 11 只票一过零点全被判过期，柜台又从不回报，
// 屏上回落成 11-16 冻住的旧柜台行——两只票看起来"被删了"，其实一行没删。
// owner 因此撤掉"必须当日"这条可用性判据；本次前端补的是它的**代价**：
// 一本不是今天录的账必须明写「非今日 · 已 N 天未更新」，不许长得像今早刚核对过的。
//
// 四格：
//  ① 纯函数：天数换算（不可解析返回 null，绝不返回 0 冒充"今天"）；
//  ② 携带态：状态行出现「非今日 · 已 N 天未更新」；
//  ③ 负锁：后端没标 carried ⇒ 屏上不许出现「非今日」（不许前端自己猜一个）；
//  ④ 过期态（unknown）：文案点名"已超 14 天未更新"，且不渲染任何旧柜台行。
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, waitFor, cleanup } from '@testing-library/react'
import { manualCarriedDays, manualCarriedNote } from '../utils/manualSource.js'

const { state } = vi.hoisted(() => ({
  state: { bookSource: null, positions: [], account: null },
}))

// api 层整体替身：只把实仓页真会拨的几条换成受控夹具，其余走 importActual。
// book_source 的两态（携带 / unknown）由 state 逐条喂——页面只认后端单点装配的结论，
// 不在前端自己再判一次新鲜度（否则跨日判据会在屏上与决策链上各走一套）。
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

// daysAgoAsOf 造一个 N 个自然日前的同日时刻串（asof 的落库形态）。
function daysAgoAsOf(n) {
  const d = new Date(Date.now() - n * 86400000)
  const p = (x) => String(x).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} 09:30:00`
}

async function openRealBook() {
  await screen.findByText('持仓数据截至', { exact: false })
  await waitFor(() => expect(screen.getByTestId('real-book-source')).toBeInTheDocument())
}

describe('§MANUAL-CARRIED 跨日携带的呈现', () => {
  beforeEach(() => {
    cleanup()
    localStorage.clear()
    state.bookSource = null
    state.positions = []
    state.account = null
  })
  afterEach(() => cleanup())

  it('纯函数：天数可算，不可解析返回 null（绝不返回 0 冒充「今天」）', () => {
    expect(manualCarriedDays(daysAgoAsOf(0))).toBe(0)
    expect(manualCarriedDays(daysAgoAsOf(1))).toBe(1)
    expect(manualCarriedDays(daysAgoAsOf(9))).toBe(9)
    for (const bad of ['', '  ', '2026-11', 'not-a-date', null, undefined]) {
      expect(manualCarriedDays(bad), `asof=${String(bad)} 应判不可算`).toBeNull()
    }
    // 不可算时只留「非今日」半句，不编一个天数出来。
    expect(manualCarriedNote('not-a-date')).toBe('非今日')
    expect(manualCarriedNote(daysAgoAsOf(2))).toBe('非今日 · 已 2 天未更新')
    // 同一自然日录的账不是"跨日"：只留前半句。
    expect(manualCarriedNote(daysAgoAsOf(0))).toBe('非今日')
  })

  it('携带态：状态行明写「非今日 · 已 N 天未更新」，行角标也带同一句', async () => {
    state.bookSource = {
      cash: 'manual', positions: 'manual',
      cash_asof: daysAgoAsOf(2), positions_asof: daysAgoAsOf(2),
      cash_carried: true, positions_carried: true, taken_over_at: '',
    }
    state.positions = [{ ts_code: '600000.SH', name: '浦发银行', qty: 1000, cost_price: 8.5, updated_at: daysAgoAsOf(2) }]
    state.account = { available_cash: 88000, updated_at: daysAgoAsOf(2) }
    render(<Positions />)
    await openRealBook()
    const strip = await screen.findByTestId('real-book-source')
    // 身份句与新鲜度警告必须同时在位：前者说"谁给的"，后者说"不是今天的"。
    expect(strip.textContent).toContain('非柜台实时读数')
    expect(strip.textContent).toContain('非今日 · 已 2 天未更新')
    expect(screen.getByTestId('manual-carried')).toBeInTheDocument()
    // 行级角标 title 也带同一句（点不到状态行时也能看见）。
    expect(screen.getByTestId('manual-row-badge')).toBeInTheDocument()
    expect(document.querySelector('[title*="非今日 · 已 2 天未更新"]'),
      '行级角标必须同带「非今日」的 title').not.toBeNull()
  })

  it('负锁：后端没标 carried ⇒ 屏上不许出现「非今日」（前端不自己猜新鲜度）', async () => {
    const today = daysAgoAsOf(0)
    state.bookSource = {
      cash: 'manual', positions: 'manual',
      cash_asof: today, positions_asof: today, taken_over_at: '',
    }
    state.positions = [{ ts_code: '600000.SH', name: '浦发银行', qty: 1000, cost_price: 8.5, updated_at: today }]
    state.account = { available_cash: 88000, updated_at: today }
    render(<Positions />)
    await openRealBook()
    const strip = await screen.findByTestId('real-book-source')
    expect(strip.textContent).toContain('柜台：不可达 · 改账态')
    expect(strip.textContent).not.toContain('非今日')
    expect(screen.queryByTestId('manual-carried')).not.toBeInTheDocument()
  })

  it('过期态：随日携带超上限 ⇒ 读数未知，文案点名「已超 14 天未更新」且不渲染旧柜台行', async () => {
    // 后端在两维都不可用时下发 unknown + unavailable，且**不下发任何旧行**（positions 为空）。
    state.bookSource = {
      cash: 'unknown', positions: 'unknown',
      cash_asof: '', positions_asof: '', taken_over_at: '',
      unavailable: true, last_manual_asof: daysAgoAsOf(20),
    }
    state.positions = []
    state.account = { available_cash: 0, updated_at: '' }
    render(<Positions />)
    await openRealBook()
    const strip = await screen.findByTestId('real-book-source')
    expect(strip.textContent).toContain('读数未知')
    expect(strip.textContent).not.toContain('非今日')
    // 空态文案必须说清"没有可用手动账"的两种成因（未录 / 超上限），否则用户只能干等柜台。
    expect(screen.getByText(/已超 14 天未更新/, { exact: false })).toBeInTheDocument()
    // 最近手动账数据截至仍然可见（给人去核对的落点），但它不是今天的读数。
    expect(screen.getByText(/最近手动账数据截至/, { exact: false })).toBeInTheDocument()
    // 屏上不许出现旧柜台行（真值由 positions 为空承载）。
    expect(screen.queryByTestId('manual-row-badge')).not.toBeInTheDocument()
  })
})
