// ── §FUNDS-TODAY（2026-10-28）资金卡「信号仓侧发布点前移」前端回归 ──
//
// 缺陷本体（首日实测）：资金帧原先只在实盘 T 循环里写，于是做 T 没开（sizing.t.enabled=false）
// 或当天没做一笔 T 的日子，卡片永远是「今日尚未定权」——而信号仓的定权（MS/目标/剩余）与
// T 开不开毫无关系（盘中 stderr 明明有 MS=0.765）。修法：信号仓侧改由打分循环每轮发布，
// T 侧仍由 T 循环发布，T 没跑时**保持上一帧**并用 t_at 标出新鲜度。
//
// 本用例锁定前端三件事（消费端契约）：
//   1. 信号仓侧有帧时，卡片显示 MS/目标/当前/剩余（不再是「今日尚未定权」）；
//   2. T 侧没跑过（t_at 为空）时显示「T 侧未运行」而不是把 0 画成「T 仓为零」；
//   3. T 侧读数陈旧（t_at 比接口时刻旧很多）时显示「T 侧读数未更新」。
// English: §FUNDS-TODAY — the funds card must show the signal-side sizing whenever the
// frame exists, and must label the T side as idle/stale instead of drawing stale zeros.
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import FundsOverviewCard from '../components/FundsOverviewCard.jsx'
// §DET-TIME 同款：接口是 resolved promise 的 mock，排空微任务即可
import { settle } from './settle.js'

const mocks = vi.hoisted(() => ({
  fetchFundsToday: vi.fn(),
}))
vi.mock('../api/index.js', () => ({
  fetchFundsToday: mocks.fetchFundsToday,
  isForbidden: (e) => !!(e && e.status === 403),
}))

/** 造一帧资金（信号仓侧有值；T 侧按 t_at 的有无区分"没跑过"/"跑过"）。 */
function frame({ tAt, marketScale = 0.765 } = {}) {
  return {
    day: '2026-10-28',
    at: '2026-10-28T10:00:00+08:00',
    live: {
      state: 'ok',
      day: '2026-10-28',
      funds: {
        day: '2026-10-28',
        at: '2026-10-28T10:00:00+08:00',
        t_at: tAt,
        cash: 50000, frozen_cash: 1000, total_asset: 100000, market_value: 40000,
        cash_at: '2026-10-28 09:58:00', cash_ok: true,
        target: 76500, cur_strat: 40000, room: 36500,
        market_scale: marketScale, missing_key_input: false,
        t_quota: 23500, cash_t: 13500, t_frozen_cash: 0,
        open_buy_legs: 0, open_sell_legs: 0, open_legs: 0, done_rounds: 1,
        pnl_net: 120.5, pnl_gross: 130,
      },
    },
    paper: { available: false, reason: '模拟盘引擎未接入' },
  }
}

describe('FundsOverviewCard §FUNDS-TODAY 信号仓侧发布点前移', () => {
  beforeEach(() => {
    mocks.fetchFundsToday.mockReset()
  })

  it('信号仓侧有帧时显示 MS/目标/剩余（T 没跑也不算"尚未定权"）', async () => {
    mocks.fetchFundsToday.mockResolvedValue(frame({ tAt: '' }))
    render(<FundsOverviewCard />)
    await waitFor(() => expect(screen.getByText(/目标仓位/)).toBeTruthy())
    await settle()
    const text = document.body.textContent
    expect(text).toContain('MS 0.765')
    expect(text).toContain('76,500.00')
    expect(text).toContain('36,500.00')
    expect(text).not.toContain('今日尚未定权')
  })

  it('T 侧没跑过时标「T 侧未运行」，不把 0 画成 T 仓为零', async () => {
    mocks.fetchFundsToday.mockResolvedValue(frame({ tAt: '' }))
    render(<FundsOverviewCard />)
    await waitFor(() => expect(screen.getByText('T 侧未运行')).toBeTruthy())
    await settle()
    const text = document.body.textContent
    expect(text).toContain('T 侧未运行')
    expect(text).not.toContain('T 侧读数未更新')
  })

  it('T 侧读数陈旧时标「T 侧读数未更新」（T 循环停了，数字不是现状）', async () => {
    mocks.fetchFundsToday.mockResolvedValue(frame({ tAt: '2026-10-28T09:30:00+08:00' }))
    render(<FundsOverviewCard />)
    await waitFor(() => expect(screen.getByText('T 侧读数未更新')).toBeTruthy())
    await settle()
    const text = document.body.textContent
    expect(text).toContain('T 侧读数未更新')
    expect(text).not.toContain('T 侧未运行')
  })

  it('T 侧刚更新过时既不标未运行也不标未更新', async () => {
    mocks.fetchFundsToday.mockResolvedValue(frame({ tAt: '2026-10-28T09:59:55+08:00' }))
    render(<FundsOverviewCard />)
    await waitFor(() => expect(screen.getByText(/目标仓位/)).toBeTruthy())
    await settle()
    const text = document.body.textContent
    expect(text).not.toContain('T 侧未运行')
    expect(text).not.toContain('T 侧读数未更新')
    // T 侧取数时刻必须露出来（否则"没更新"这件事没法核对）
    expect(text).toContain('T 侧取数时刻')
  })
})
