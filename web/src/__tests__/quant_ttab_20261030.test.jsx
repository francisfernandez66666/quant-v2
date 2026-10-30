// quant_ttab_20261030.test.jsx — §W-04 量化页「T 仓」子页（2026-10-30 夜批收尾）。
//
// 三条锁对应方案 D1~D3：
//   D1 默认停在「实盘与配置」，资金卡不在默认页（它已被整卡迁进 T 仓子页）；
//   D2 点「T 仓」后资金卡与 T 回合卡可见；
//   D3 切回默认页后资金卡必须**卸载**（destroyOnHide）——否则两个 10s 轮询器会打同一个接口。
import { describe, it, expect, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { settle } from './settle.js'

vi.mock('../api/index.js', () => ({
  getAccount: () => 'admin',
  isAdmin: () => true,
  isForbidden: (e) => !!(e && e.status === 403),
  fetchQMTConfig: vi.fn(async () => ({
    enabled: true, mode: 'auto', price_type: 'market', auto_sell: false,
    gateway_url: 'http://127.0.0.1:8789', token_masked: '****',
    fixed_amount: 10000, max_positions: 10, initial_capital: 100000,
    max_order_amount: 150000,
    daily_max_buys: 20, daily_budget_amount: 100000, miss_heartbeat_sec: 120,
    known_strategies: [{ id: 'dragon', name: '龙头识别', kind: 'form' }],
    strategies: [], strategy_amounts: {},
  })),
  fetchQMTState: vi.fn(async () => ({
    enabled: true, mode: 'auto', tripped: false,
    gateway_url: 'http://127.0.0.1:8789',
    last_probe_at: '2026-09-13 04:00:00', last_probe_ok: true, last_latency_ms: 3,
    last_report_at: '2026-09-13 03:59:00', last_report_kind: 'heartbeat',
  })),
  fetchQMTBroker: vi.fn(async () => ({ ok: true, broker: 'xt', broker_connected: true, xt_connected: true, queued_connected: false })),
  fetchQMTTrades: vi.fn(async () => ({})),
  fetchShortStatus: vi.fn(async () => ({ short_enabled: false })),
  fetchPaperState: vi.fn(async () => ({ short_book: { enabled: false } })),
  switchQMTBroker: vi.fn(async () => ({})),
  updateQMTConfig: vi.fn(async () => ({})),
  toggleShort: vi.fn(async () => ({ short_enabled: true })),
  fetchFundsToday: vi.fn(async () => ({
    at: '2026-10-30 10:30:00',
    live: { state: 'ok', funds: { cash: 50000, frozen_cash: 0, total_asset: 200000, market_value: 150000, cash_at: '2026-10-30 10:30:00' }, account: null },
    paper: { available: false },
  })),
  fetchPaperTRounds: vi.fn(async () => ({ book: 'paper', enabled: true, rounds: [], pnl: { net: 0, gross: 0, done_rounds: 0, fee_est_share: 0 } })),
  fetchLiveTRounds: vi.fn(async () => ({ book: 'live', state: 'missing', rounds: [], pnl: { net: 0, gross: 0, done_rounds: 0, fee_est_share: 0 } })),
}))

import Quant from '../pages/Quant.jsx'

const FUNDS_TITLE = '今日资金（信号仓 / T 仓）'

function renderPage() {
  return render(<MemoryRouter><Quant /></MemoryRouter>)
}

describe('§W-04 量化页 T 仓子页', () => {
  it('D1 默认页只有实盘与配置，资金卡不在场', async () => {
    renderPage()
    await settle()
    expect(screen.getByText('链路状态')).toBeInTheDocument()
    expect(screen.queryByText(FUNDS_TITLE)).toBeNull()
  })

  it('D2 切到 T 仓后资金卡与 T 回合卡可见', async () => {
    renderPage()
    await settle()
    fireEvent.click(screen.getByText('T 仓'))
    await settle()
    expect(screen.getByText(FUNDS_TITLE)).toBeInTheDocument()
    expect(screen.getByText(/日内回转/)).toBeInTheDocument()
  })

  it('D3 切回默认页后资金卡卸载（destroyOnHide，无幽灵轮询）', async () => {
    renderPage()
    await settle()
    fireEvent.click(screen.getByText('T 仓'))
    await settle()
    expect(screen.getByText(FUNDS_TITLE)).toBeInTheDocument()
    fireEvent.click(screen.getByText('实盘与配置'))
    await settle()
    expect(screen.queryByText(FUNDS_TITLE)).toBeNull()
  })
})
