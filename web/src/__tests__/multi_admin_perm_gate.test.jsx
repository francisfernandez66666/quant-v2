// multi_admin_perm_gate.test.jsx — §MULTI-ADMIN：无 funds_admin 时 FundsOverviewCard 不发请求。
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, waitFor } from '@testing-library/react'
import { settle } from './settle.js'
import FundsOverviewCard from '../components/FundsOverviewCard.jsx'

const mocks = vi.hoisted(() => ({
  can: vi.fn(),
  fetchFundsToday: vi.fn(),
}))
vi.mock('../api/index.js', () => ({
  hasStrictPerm: mocks.can,
  fetchFundsToday: mocks.fetchFundsToday,
  isForbidden: (e) => !!(e && e.status === 403),
}))

describe('§MULTI-ADMIN 前端严格权限门控', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mocks.fetchFundsToday.mockResolvedValue({
      day: '20261031', at: '2026-10-31T20:00:00+08:00',
      live: { day: '20261031', funds: null, state: 'pending', reason: '今日尚未定权' },
      paper: { available: false, reason: '模拟盘引擎未接入' },
    })
  })

  it('无 funds_admin：整卡不渲染且不请求 funds/today', async () => {
    mocks.can.mockReturnValue(false)
    const { container } = render(<FundsOverviewCard />)
    await settle()
    await waitFor(() => expect(container.innerHTML).toBe(''))
    expect(mocks.fetchFundsToday).not.toHaveBeenCalled()
  })

  it('有 funds_admin：正常请求', async () => {
    mocks.can.mockReturnValue(true)
    render(<FundsOverviewCard />)
    await settle()
    await waitFor(() => expect(mocks.fetchFundsToday).toHaveBeenCalled())
  })
})
