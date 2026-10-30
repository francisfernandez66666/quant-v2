// signal_decisions_paper.test.jsx — §W-02 B5：纸面行的「定权 MS / SS / 单笔」缺项必须显示「—」。
//
// 纸面动态预算没有个股缩放（StockScale 恒 0，由 MissingKeyInput 标），直接 toFixed(3)
// 会渲染成 "0.000"——那会被读成"个股因子算出来是 0"，是假读数。与 manual 行同一处置。
import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { settle } from './settle.js'

// 只桩决策接口：本用例只关心纸面行的定权列怎么渲染，其余导出保持真实实现。
vi.mock('../api/index.js', async () => {
  const actual = await vi.importActual('../api/index.js')
  return {
    ...actual,
    isForbidden: (e) => !!(e && e.status === 403),
    fetchSignalDecisions: async () => ({
      day: '20261030',
      items: [{
        at: '2026-10-30T10:30:00+08:00', channel: 'paper', code: '600000',
        strategy_key: 'dragon', stage: 'placed', market_scale: 0.6, stock_scale: 0,
        per_trade_pct: 0.2, total: 100000, cash: 20000, target: 60000, cur_strat: 40000,
        room: 20000, industry_room: 30000, stock_room: 50000, cluster_room: 0,
        qty: 1000, notional: 10000, missing_key_input: true,
      }],
    }),
  }
})

import SignalDecisionsCard from '../components/SignalDecisionsCard.jsx'

describe('§W-02 B5 纸面行定权列显示', () => {
  it('stock_scale=0 的纸面行显示「—」，不出现 0.000', async () => {
    render(<SignalDecisionsCard />)
    await settle()
    const text = document.body.textContent || ''
    expect(text).toContain('纸面')
    expect(text).not.toContain('0.000')
    expect(screen.getAllByText('—').length).toBeGreaterThan(0)
  })
})
