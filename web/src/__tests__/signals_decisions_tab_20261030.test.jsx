// signals_decisions_tab_20261030.test.jsx — §W-04 信号页「动态仓位」子页（2026-10-30 夜批收尾）。
//
// D4 锁两件事：①默认页只有信号表（宽表不再把列表挤出首屏）；②点「动态仓位」后决策卡可见，
// 且其表头「定权 MS / SS / 单笔」在场（同时覆盖 D6 的 DYN-D1 断言口径）。
import { describe, it, expect, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { settle } from './settle.js'

const SIGNAL_ROW = {
  code: '600002', name: '龙头股', strategy: '龙头首板', strategy_type: 'dragon',
  strategy_id: 'dragon-1', level: '交易', remind_level: 'strong', action: 'buy', price: 11,
  total_score: 90, can_open: true,
  d1: 88, d2: 70, d3: 65, d4: 40,
  d1_desc: '贴板强度', d2_desc: '板块共振', d3_desc: '超额收益', d4_desc: '均线多头',
}

// 信号页挂载所需的只读接口全部回合法空/单行载荷；本用例只断言子页签与卡片可见性。
vi.mock('../api/index.js', async () => {
  const actual = await vi.importActual('../api/index.js')
  return {
    ...actual,
    isAdmin: () => true,
    getAccount: () => 'admin',
    isForbidden: (e) => !!(e && e.status === 403),
    fetchStatus: async () => ({ session: 'test', signal_count: 1 }),
    setLastSession: () => {},
    connectSSE: () => {},
    fetchSignals: async () => [SIGNAL_ROW],
    fetchPaperState: async () => ({ enabled: false }),
    fetchShortStatus: async () => ({ short_enabled: false }),
    fetchAlerts: async () => [],
    fetchSignalDecisions: async () => ({
      day: '20261030',
      items: [{
        at: '2026-10-30T10:30:00+08:00', channel: 'live', code: '600002',
        strategy_key: 'dragon-1', stage: 'evaluated', market_scale: 0.6, stock_scale: 1,
        per_trade_pct: 0.2, total: 200000, cash: 50000, target: 120000, cur_strat: 100000,
        room: 20000, industry_room: 30000, stock_room: 50000, cluster_room: 0,
        qty: 1000, notional: 11000,
      }],
    }),
  }
})

import Signals from '../pages/Signals.jsx'

const CARD_TITLE = /今日动态仓位/

describe('§W-04 信号页动态仓位子页', () => {
  it('D4 默认页只有信号表，切到动态仓位后决策卡可见', async () => {
    render(<MemoryRouter><Signals /></MemoryRouter>)
    await settle()
    // 默认页：信号行在场，决策卡不在场。
    expect(screen.getByText('龙头股')).toBeInTheDocument()
    expect(screen.queryByText(CARD_TITLE)).toBeNull()

    fireEvent.click(screen.getByText('动态仓位'))
    await settle()
    expect(screen.getByText(CARD_TITLE)).toBeInTheDocument()
    expect(screen.getByText('定权 MS / SS / 单笔')).toBeInTheDocument()

    // 切回信号列表：决策卡卸载，信号表仍在。
    fireEvent.click(screen.getByText('信号列表'))
    await settle()
    expect(screen.getByText('龙头股')).toBeInTheDocument()
    expect(screen.queryByText(CARD_TITLE)).toBeNull()
  })
})
