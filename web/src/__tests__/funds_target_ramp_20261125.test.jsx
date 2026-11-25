// ── D7（2026-11-25）分时释放阶梯的卡片读数 ──
//
// 事故背景：D7 把目标仓位改成 `总资产 × min(MS×TM, 天花板) × ramp`。倍率是乘法，
// 所以"为什么今天只能买七成"这件事必须**在目标仓位那一行上看得见**——否则操作人
// 只会看到 target 比昨天小，然后去怀疑 MS 或总资产，方向完全错。
//
// 本用例锁四件事：
//   1. 四档「正在压制」的格子各有自己的文案（牛市/熊市 × 上午/下午）；
//   2. `ramp_off` / `ramp_full` 一律不渲染 —— 这两种状态下 D7 对目标没有约束，
//      写一行字只会让人以为仓位还被压着；
//   3. 读数缺失或越界（NaN / 0 / >1）不渲染，**绝不画 0%**（0 是"今天不许买"，
//      与"没有这一层"完全不同）；
//   4. 卡片上这行与 MS 拼在一起（同一个目标的两次乘法），且文案来自唯一源 sideRamp.js。
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import FundsOverviewCard from '../components/FundsOverviewCard.jsx'
import { sideRampHint } from '../utils/sideRamp.js'
import { settle } from './settle.js'

const mocks = vi.hoisted(() => ({
  fetchFundsToday: vi.fn(),
  fetchPaperTarget: vi.fn(async () => ({ available: false, reason: '模拟盘引擎未接入' })),
}))
vi.mock('../api/index.js', () => ({
  fetchFundsToday: mocks.fetchFundsToday,
  fetchPaperTarget: mocks.fetchPaperTarget,
  isForbidden: (e) => !!(e && e.status === 403),
}))

/** 一帧最小可用读数：只关心 market_scale 与 D7 两键，其余给足以免卡片走空态。 */
function frame(extra = {}) {
  return {
    day: '2026-11-25',
    at: '2026-11-25T10:30:00+08:00',
    live: {
      state: 'ok',
      day: '2026-11-25',
      funds: {
        day: '2026-11-25',
        at: '2026-11-25T10:30:00+08:00',
        t_at: '2026-11-25T10:30:00+08:00',
        cash: 40000,
        frozen_cash: 0,
        total_asset: 100000,
        market_value: 60000,
        cash_at: '2026-11-25 10:29:58',
        cash_ok: true,
        target: 35000,
        cur_strat: 60000,
        room: 0,
        market_scale: 0.800,
        missing_key_input: false,
        t_quota: 0,
        cash_t: 40000,
        t_frozen_cash: 0,
        open_buy_legs: 0,
        open_sell_legs: 0,
        open_legs: 0,
        done_rounds: 0,
        pnl_net: 0,
        pnl_gross: 0,
        ...extra,
      },
    },
    paper: { available: false, reason: '模拟盘引擎未接入' },
  }
}

beforeEach(() => {
  mocks.fetchFundsToday.mockReset()
  mocks.fetchPaperTarget.mockReset()
  mocks.fetchPaperTarget.mockResolvedValue({ available: false, reason: '模拟盘引擎未接入' })
})

describe('sideRampHint 单一文案源', () => {
  it('四档压制各有自己的文案，并带自己的百分比', () => {
    const cases = [
      [{ target_ramp: 0.7, target_ramp_src: 'ramp_bull_am' }, '今日按 70% 释放（牛市上午档）'],
      [{ target_ramp: 1.0, target_ramp_src: 'ramp_bull_pm' }, '今日按 100% 释放（牛市下午档）'],
      [{ target_ramp: 0.35, target_ramp_src: 'ramp_bear_am' }, '今日按 35% 释放（熊市上午档）'],
      [{ target_ramp: 0.5, target_ramp_src: 'ramp_bear_pm' }, '今日按 50% 释放（熊市下午档）'],
    ]
    for (const [funds, expectHead] of cases) {
      const hint = sideRampHint(funds)
      expect(hint.startsWith(expectHead)).toBe(true)
    }
    // 上午档要能读出"13:00 无坏信号就升档、14:00 满额"这条时间线；
    // 下午档只说 14:00 满额（这时候再说 13:00 已经不成立了）。
    expect(sideRampHint({ target_ramp: 0.7, target_ramp_src: 'ramp_bull_am' })).toContain('14:00')
    expect(sideRampHint({ target_ramp: 0.5, target_ramp_src: 'ramp_bear_pm' })).not.toContain('13:00')
  })

  it('关闭/满额/读数缺失一律不渲染，绝不画 0%', () => {
    for (const funds of [
      null,
      undefined,
      {},
      { target_ramp: 1.0, target_ramp_src: 'ramp_off' },
      { target_ramp: 1.0, target_ramp_src: 'ramp_full' },
      { target_ramp: 0, target_ramp_src: 'ramp_bear_am' },
      { target_ramp: 1.5, target_ramp_src: 'ramp_bull_am' },
      { target_ramp: 'x', target_ramp_src: 'ramp_bull_am' },
    ]) {
      expect(sideRampHint(funds)).toBe('')
    }
  })
})

describe('资金卡目标仓位一行', () => {
  it('压制中：MS 与释放档位拼在同一个 hint 上', async () => {
    mocks.fetchFundsToday.mockResolvedValue(frame({ target_ramp: 0.35, target_ramp_src: 'ramp_bear_am' }))
    render(<FundsOverviewCard />)
    await waitFor(() => expect(mocks.fetchFundsToday).toHaveBeenCalled())
    await settle()
    expect(await screen.findByText(/MS 0\.800/)).toBeTruthy()
    expect(await screen.findByText(/今日按 35% 释放（熊市上午档）/)).toBeTruthy()
  })

  it('满额：只剩 MS 读数，不出现释放文案', async () => {
    mocks.fetchFundsToday.mockResolvedValue(frame({ target_ramp: 1.0, target_ramp_src: 'ramp_full' }))
    render(<FundsOverviewCard />)
    await waitFor(() => expect(mocks.fetchFundsToday).toHaveBeenCalled())
    await settle()
    expect(await screen.findByText(/MS 0\.800/)).toBeTruthy()
    expect(screen.queryByText(/今日按/)).toBeNull()
  })
})
