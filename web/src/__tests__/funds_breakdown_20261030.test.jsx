// ── §P1-3（2026-10-30）资金卡拆口径与文案回归 ──
//
// 事故背景：卡片只显示「当前战略仓位 58,838」一个总数，而真实持仓只有 22,238，
// 差额 36,716 是两笔卡在「撤单中」的买单——操作人看不出是谁占的，也不知道 T 额度
// 是「战略让出的空间」而不是「今天做过 T」。
//
// 本用例锁定三件事：
//   1. 战略当前必须拆成 底仓市值 / 在途买单 / 剩余空间 三行；
//   2. 在途买单要带笔数与最久一笔的状态说明（pending_buy_note）；
//   3. T 额度文案改为「T 可动用额度」，且 done_rounds=0 时明确「今日未做 T」。
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import FundsOverviewCard from '../components/FundsOverviewCard.jsx'
import { settle } from './settle.js'

const mocks = vi.hoisted(() => ({
  fetchFundsToday: vi.fn(),
}))
vi.mock('../api/index.js', () => ({
  fetchFundsToday: mocks.fetchFundsToday,
  isForbidden: (e) => !!(e && e.status === 403),
}))

/** 造一帧「卡单未清」的资金读数：战略当前 74,619、在途买单 36,716（两笔），用于拆口径断言。 */
function frame() {
  return {
    day: '2026-10-30',
    at: '2026-10-30T14:45:22+08:00',
    live: {
      state: 'ok',
      day: '2026-10-30',
      funds: {
        day: '2026-10-30',
        at: '2026-10-30T14:45:22+08:00',
        t_at: '2026-10-30T14:45:20+08:00',
        cash: 17037.92, frozen_cash: 52508.02, total_asset: 91644.52, market_value: 22108,
        cash_at: '2026-10-30 14:45:18', cash_ok: true,
        target: 62965.75, cur_strat: 74619.32, room: 0,
        market_scale: 0.687, missing_key_input: false,
        pending_buy_amount: 36716, pending_buy_n: 2,
        pending_buy_note: '300502 ×100 撤单中 0.9 分钟',
        t_quota: 28695.05, cash_t: 17037.92, t_frozen_cash: 0,
        open_buy_legs: 0, open_sell_legs: 0, open_legs: 0, done_rounds: 0,
        pnl_net: 0, pnl_gross: 0,
      },
    },
    paper: { available: false, reason: '模拟盘引擎未接入' },
  }
}

describe('FundsOverviewCard §P1-3 拆口径与 T 额度文案', () => {
  beforeEach(() => {
    mocks.fetchFundsToday.mockReset()
  })

  it('战略当前拆成底仓/在途/剩余三行，并显示在途明细', async () => {
    mocks.fetchFundsToday.mockResolvedValue(frame())
    render(<FundsOverviewCard />)
    await waitFor(() => expect(screen.getByText(/目标仓位/)).toBeTruthy())
    await settle()
    const text = document.body.textContent
    expect(text).toContain('底仓市值')
    expect(text).toContain('在途买单')
    expect(text).toContain('36,716.00')
    expect(text).toContain('2 笔')
    expect(text).toContain('300502 ×100 撤单中 0.9 分钟')
  })

  it('旧帧缺 pending 字段时按 0 显示（不给破折号，避免误判读数坏了）', async () => {
    const f = frame()
    delete f.live.funds.pending_buy_amount
    delete f.live.funds.pending_buy_n
    delete f.live.funds.pending_buy_note
    mocks.fetchFundsToday.mockResolvedValue(f)
    render(<FundsOverviewCard />)
    await waitFor(() => expect(screen.getByText(/目标仓位/)).toBeTruthy())
    await settle()
    const text = document.body.textContent
    expect(text).toContain('在途买单')
    expect(text).toContain('0.00')
  })

  it('T 额度改名为「T 可动用额度」并说明不是已做 T', async () => {
    mocks.fetchFundsToday.mockResolvedValue(frame())
    render(<FundsOverviewCard />)
    await waitFor(() => expect(screen.getByText(/目标仓位/)).toBeTruthy())
    await settle()
    const text = document.body.textContent
    expect(text).toContain('T 可动用额度')
    expect(text).toContain('今日未做 T')
    expect(text).not.toContain('T 额度28')
  })
})
