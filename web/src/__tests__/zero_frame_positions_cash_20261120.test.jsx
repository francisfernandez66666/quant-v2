// ── §ZERO-FRAME（2026-11-20 现网实录）── 可用资金「¥0.00」与「读数未知」必须分开
//
// 现场：载体每隔约 30 分钟把一次 `query_asset` 全 0 的占位帧当成 synced=True 上报，
// 云端当新鲜真值覆盖 real_account，页面上的「可用资金」反复跳成 ¥0.00，约 6 分钟后才恢复。
// 后端已加"全 0 帧＝无读数"闸（internal/server/qmt.go 的 §ZERO-FRAME）；本文件锁前端那一半：
//
//   Z1 资金维度 unknown ⇒ 打「读数未知」，**绝不**画 ¥0.00（"不知道"与"没钱"是两件事）；
//   Z2 柜台真报 0 ⇒ 照常打 ¥0.00（不许把真 0 也吞成未知，那是另一个方向的说谎）；
//   Z3 柜台报正数 ⇒ 打那个数（本批不许把正常路径改坏）；
//   Z4 结构锁 ⇒ 判据只认后端 book_source.cash，前端不再自己判一次新鲜度。
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, waitFor, cleanup } from '@testing-library/react'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const HERE = path.dirname(fileURLToPath(import.meta.url))

const { state } = vi.hoisted(() => ({
  state: {
    positions: [],
    account: null,
    bookSource: null,
  },
}))

// api 层整体替身：只把页面真会拨的那几腿换成受控夹具（持仓/账户/book_source 三格逐条喂），
// 其余走 importActual——「替身没定义」不能被读成「页面不调用它」。
// 三格正是本缺陷的现场矩阵：account=全 0 + book_source.cash=unknown / counter 真 0 / counter 正数。
vi.mock('../api/index.js', async () => {
  const actual = await vi.importActual('../api/index.js')
  return {
    ...actual,
    isAdmin: vi.fn(() => true),
    fetchStatus: vi.fn(async () => ({ session: 'test' })),
    setLastSession: vi.fn(),
    fetchHoldings: vi.fn(async () => ({
      holdings: [], available_balance: 100,
      total_realized_pnl: 0, total_unrealized_pnl: 0,
      pnl_offset: 0, total_pnl: 0,
    })),
    resetPaperPnlOffset: vi.fn(async () => ({ status: 'ok', pnl_offset: 0 })),
    updateHoldingsBalance: vi.fn(async () => ({ status: 'ok' })),
    updateHoldings: vi.fn(async () => ({ status: 'ok' })),
    fetchQMTState: vi.fn(async () => ({ enabled: true, tripped: false })),
    fetchRealPositions: vi.fn(async () => ({
      positions: state.positions,
      account: state.account || undefined,
      book_source: state.bookSource || undefined,
      counter_frame_at: '',
      counter_frozen: false,
    })),
    fetchQMTTrades: vi.fn(async () => null),
    fetchRealAdvice: vi.fn(async () => ({ advices: [] })),
  }
})

import Positions from '../pages/Positions.jsx'

// 一条在场持仓：让 hasReal 成立（页头的实盘读数只在有实盘数据时渲染）。
const ONE_POSITION = [{
  ts_code: '600519.SH', name: '贵州茅台', qty: 100, cost_price: 1500, cur_price: 1500,
  amount: 150000, updated_at: '2026-11-20 11:54:16',
}]

async function ready() {
  await screen.findByText('持仓数据截至', { exact: false })
  await waitFor(() => expect(screen.queryAllByText(/可用资金/, { exact: false }).length).toBeGreaterThan(0))
}

function cashText() {
  const nodes = screen.getAllByText(/可用资金/, { exact: false })
  return nodes.map((n) => n.textContent || '').join(' | ')
}

describe('§ZERO-FRAME 可用资金读数未知 vs 真 0', () => {
  beforeEach(() => {
    state.positions = ONE_POSITION
    state.account = null
    state.bookSource = null
  })
  afterEach(() => cleanup())

  it('Z1 资金维度 unknown ⇒ 「读数未知」，不打 ¥0.00', async () => {
    state.positions = ONE_POSITION
    // 后端在 cash=unknown 时下发零值账户 + updated_at 空（见 handleRealPositions）
    state.account = { available_cash: 0, total_asset: 0, market_value: 0, updated_at: '' }
    state.bookSource = { cash: 'unknown', positions: 'counter', cash_asof: '', positions_asof: '2026-11-20 11:54:16' }
    render(<Positions />)
    await ready()
    const txt = cashText()
    expect(txt).toContain('读数未知')
    expect(txt).not.toContain('¥0.00')
  })

  it('Z2 柜台真报 0 ⇒ 照常打 ¥0.00（真 0 不许被吞成未知）', async () => {
    state.positions = ONE_POSITION
    state.account = { available_cash: 0, total_asset: 150000, market_value: 150000, updated_at: '2026-11-20 11:54:16' }
    state.bookSource = { cash: 'counter', positions: 'counter', cash_asof: '2026-11-20 11:54:16', positions_asof: '2026-11-20 11:54:16' }
    render(<Positions />)
    await ready()
    const txt = cashText()
    expect(txt).toContain('¥0.00')
    expect(txt).not.toContain('读数未知')
  })

  it('Z3 柜台报正数 ⇒ 打正数（正常路径不许被本批改坏）', async () => {
    state.positions = ONE_POSITION
    state.account = { available_cash: 48565.54, total_asset: 196723.54, market_value: 148158, updated_at: '2026-11-20 11:54:16' }
    state.bookSource = { cash: 'counter', positions: 'counter', cash_asof: '2026-11-20 11:54:16', positions_asof: '2026-11-20 11:54:16' }
    render(<Positions />)
    await ready()
    expect(cashText()).toContain('48,565.54')
  })

  it('Z4 结构锁：判据只认 book_source.cash，页内不得自判新鲜度', () => {
    const src = fs.readFileSync(path.join(HERE, '..', 'pages', 'Positions.jsx'), 'utf8')
    expect(src).toMatch(/const realCashUnknown = hasReal && cashSrc === 'unknown'/)
    // 负锁：不许在页面上按 updated_at 的年龄自己判一次（两处判一个迟早一处漂）
    expect(src).not.toMatch(/available_cash\s*!=\s*null\s*&&\s*\(\s*Date\.now\(\)/)
  })
})
