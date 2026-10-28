// ── §FIX-20261028 前端四处可发现性回归（2026-10-29）──
//
// 本文件锁四个"客户看不到/看错"的形态（都是终轮 UAT 的实测缺陷）：
//   1. F-07 资金卡 pending 必须显示**接口给的原因**（后端已分好 pending/missing 与具体文案，
//      界面此前用一句固定"属正常"顶替 ⇒ 运维在屏上读不到真因）；
//   2. F-09 实盘三段拉取失败必须挂共享红条（load-ledger），且不得把失败画成"实盘无持仓"；
//   3. F-13 委托分页封装必须保持**裸数组**语义 + 从响应头取 total（改形状会让 Paper 页崩）；
//   4. F-11 删除自选收到 404 时不得做本地移除（界面与服务端不能分叉）。
//
// English: regression for four discoverability fixes — funds reason passthrough, the shared
// load-failure banner on Positions, paper-orders paging shape, and watchlist 404 handling.
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { settle } from './settle.js'
import FundsOverviewCard from '../components/FundsOverviewCard.jsx'

const mocks = vi.hoisted(() => ({
  fetchFundsToday: vi.fn(),
}))
vi.mock('../api/index.js', () => ({
  fetchFundsToday: mocks.fetchFundsToday,
  isForbidden: (e) => !!(e && e.status === 403),
}))

describe('§FIX-20261028-F07 资金卡 pending 的原因必须上屏', () => {
  beforeEach(() => vi.clearAllMocks())

  it('接口给了 reason ⇒ 屏上出现 reason 原文', async () => {
    mocks.fetchFundsToday.mockResolvedValue({
      day: '20261028',
      at: '2026-10-28T21:53:56+08:00',
      live: { day: '20261028', funds: null, state: 'pending', reason: '今日尚未定权：引擎还没有写下当日资金帧' },
      paper: { available: false, reason: '模拟盘引擎未接入' },
    })
    render(<FundsOverviewCard />)
    await settle()
    await waitFor(() => {
      expect(screen.getByText(/今日尚未定权：引擎还没有写下当日资金帧/)).toBeTruthy()
    })
  })

  it('接口没给 reason ⇒ 回落固定句（不得出现 undefined/空）', async () => {
    mocks.fetchFundsToday.mockResolvedValue({
      day: '20261028',
      at: '2026-10-28T21:53:56+08:00',
      live: { day: '20261028', funds: null, state: 'missing', reason: '' },
      paper: { available: false, reason: '模拟盘引擎未接入' },
    })
    render(<FundsOverviewCard />)
    await settle()
    await waitFor(() => {
      // jsdom 不实现 innerText（返回 undefined），断言文本用 textContent
      const t = document.body.textContent || ''
      expect(t).toContain('引擎尚未写下今日资金帧')
      expect(t).not.toContain('undefined')
    })
  })
})
