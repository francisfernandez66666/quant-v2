// dynamic_position_20261125.test.jsx — 已占用动态仓位读数的口径与两处上屏（owner 2026-11-25）。
//
// 口径钉死：当前战略仓位 ÷ **当前动态目标**，不是占总资产。
// 两处上屏：资金卡「已占用动态仓位」行 + 首页「市场资金流与量能」卡。
// 缺数一律画「—」，绝不画 0%（0 是"真的一分没占"，与"没帧/没定权"不是一件事）。
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen } from '@testing-library/react'
import FundsOverviewCard from '../components/FundsOverviewCard.jsx'
import FlowFactorCard from '../components/FlowFactorCard.jsx'
import { dynamicUsedPct, formatDynamicUsedPct, formatDynamicUsedProcess, formatTargetProcess, formatSlotRatioProcess } from '../utils/dynamicPosition.js'

const mocks = vi.hoisted(() => ({
  fetchFundsToday: vi.fn(),
  fetchPaperTarget: vi.fn(async () => ({ available: false, reason: '模拟盘引擎未接入' })),
}))
vi.mock('../api/index.js', () => ({
  fetchFundsToday: mocks.fetchFundsToday,
  fetchPaperTarget: mocks.fetchPaperTarget,
  isForbidden: (e) => !!(e && e.status === 403),
}))

// 造一份 /api/funds/today 的资金帧：默认字段取 11-25 盘中实读形状，
// 用例只覆盖自己要断言的那几枚（day/at 固定，避免时钟漂移让用例隔天变红）。
function frame(funds = {}) {
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
        target: 35000,
        cur_strat: 60000,
        room: 0,
        market_scale: 1.0,
        target_multiplier: 1.0,
        target_ceiling: 1.0,
        target_ramp: 0.35,
        target_ramp_src: 'ramp_bear_am',
        ...funds,
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

describe('dynamicUsedPct 单一口径', () => {
  it('分母是动态目标，不是总资产', () => {
    expect(dynamicUsedPct({ cur_strat: 60000, target: 35000, total_asset: 100000 })).toBeCloseTo(171.428, 2)
    expect(formatDynamicUsedPct({ cur_strat: 60000, target: 35000 })).toBe('171.4%')
    expect(formatDynamicUsedProcess({ cur_strat: 60000, target: 35000 })).toBe('60,000.00 ÷ 35,000.00 = 171.4%')
  })

  it('当前时段动态额度 = 本时段目标 ÷ 未分时段的战略额度', () => {
    const funds = frame().live.funds
    expect(formatSlotRatioProcess(funds)).toBe('35,000.00 ÷ 100,000.00 = 35%')
    expect(formatSlotRatioProcess({ ...funds, target: 50000 })).toBe('50,000.00 ÷ 100,000.00 = 50%')
    expect(formatSlotRatioProcess({ ...funds, target: 100000 })).toBe('100,000.00 ÷ 100,000.00 = 100%')
    expect(formatSlotRatioProcess({ ...funds, target: 0 })).toBe('—')
  })

  it('动态目标算式摆出四个因子，缺因子画「—」', () => {
    expect(formatTargetProcess(frame().live.funds)).toBe('100,000.00 × min(1.000 × 1.00, 1.00) × 0.35 = 35,000.00')
    expect(formatTargetProcess({ total_asset: 100000, market_scale: 1 })).toBe('—')
  })

  it('缺帧/缺数/目标非正一律弃权，绝不画 0%', () => {
    for (const funds of [null, {}, { cur_strat: 60000 }, { target: 35000 }, { cur_strat: 60000, target: 0 },
      { cur_strat: 'x', target: 35000 }]) {
      expect(dynamicUsedPct(funds)).toBeNull()
      expect(formatDynamicUsedPct(funds)).toBe('—')
    }
  })
})

describe('两处上屏', () => {
  it('资金卡「已占用动态仓位」行显示 171.4%', async () => {
    mocks.fetchFundsToday.mockResolvedValue(frame())
    render(<FundsOverviewCard />)
    expect(await screen.findByText('已占用动态仓位')).toBeTruthy()
    expect(screen.getByText('60,000.00 ÷ 35,000.00 = 171.4%')).toBeTruthy()
    expect(screen.getByText('当前时段动态额度')).toBeTruthy()
    expect(screen.getByText('35,000.00 ÷ 100,000.00 = 35%')).toBeTruthy()
  })

  it('首页量能卡也显示同一个百分比', () => {
    const funds = frame().live.funds
    render(<FlowFactorCard flow={{ known: false }} funds={funds} />)
    expect(screen.getByTestId('flow-target-process').textContent).toContain('100,000.00 × min(1.000 × 1.00, 1.00) × 0.35 = 35,000.00')
    expect(screen.getByTestId('flow-slot-ratio').textContent).toContain('当前时段动态额度 35,000.00 ÷ 100,000.00 = 35%（熊市上午档）')
    expect(screen.getByTestId('flow-dynamic-used').textContent).toContain('当前已占用动态仓位 60,000.00 ÷ 35,000.00 = 171.4%')
  })

  it('资金帧没取到时首页画「—」而不是 0%', () => {
    render(<FlowFactorCard flow={{ known: false }} funds={null} />)
    expect(screen.getByTestId('flow-dynamic-used').textContent).toContain('当前已占用动态仓位 —')
  })
})
