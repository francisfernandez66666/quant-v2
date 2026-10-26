// anchor_shadow_card.test.jsx — §ANCHOR-FIRST W2 影子对照卡片回归：
// 正常数据渲染总账+明细；缺文件/损坏态显式空态；读取失败显式报错。
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, cleanup, waitFor } from '@testing-library/react'

vi.mock('../api/index.js', async () => {
  const actual = await vi.importActual('../api/index.js')
  return { ...actual, fetchAnchorShadow: vi.fn() }
})

import AnchorShadowCard from '../components/AnchorShadowCard.jsx'
import * as api from '../api/index.js'

const sample = {
  account: 'u1',
  keep_days: 60,
  state: 'ok',
  days: [
    {
      day: '2026-10-25',
      books: {
        live: {
          summary: { rows: 2, shifted: 1, diverged: 1, unknown: 0 },
          rows: [{
            code: '600000.SH', name: '浦发银行',
            cost_price: 10, anchor_price: 11,
            stop_price_by_cost: 9.4, stop_price_by_anchor: 10.34,
            line_by_cost: '无', line_by_anchor: '止损',
            pnl_by_cost: -3.2, pnl_by_anchor: -2.1,
            line_changed: true, anchor_known: true,
          }],
        },
        report: { summary: { rows: 1, shifted: 0, diverged: 0, unknown: 1 }, rows: [] },
      },
    },
    { day: '2026-10-24', books: { live: { summary: { rows: 1, shifted: 0, diverged: 0, unknown: 0 }, rows: [] } } },
  ],
}

describe('AnchorShadowCard', () => {
  beforeEach(() => {
    cleanup()
    api.fetchAnchorShadow.mockReset()
  })
  afterEach(() => cleanup())

  it('正常数据：总账与逐票明细上屏，切硬会变/锚点已回填可见', async () => {
    api.fetchAnchorShadow.mockResolvedValue(sample)
    render(<AnchorShadowCard />)
    await waitFor(() => expect(api.fetchAnchorShadow).toHaveBeenCalledWith(20))
    expect(await screen.findByText('影子对照（成本 vs 首买价锚）')).toBeInTheDocument()
    expect(screen.getByText('W2 只对照不切硬')).toBeInTheDocument()
    expect(await screen.findByText('600000.SH')).toBeInTheDocument()
    expect(screen.getByText('浦发银行')).toBeInTheDocument()
    expect(screen.getByText('会变')).toBeInTheDocument()
    expect(screen.getByText('已回填')).toBeInTheDocument()
    expect(screen.getAllByText('2026-10-25').length).toBeGreaterThan(0)
  })

  it('缺文件：显示明确空态，不伪装成“没有差异”', async () => {
    api.fetchAnchorShadow.mockResolvedValue({
      account: 'u1', keep_days: 60, state: 'missing', reason: '该账号还没有影子对照数据', days: [],
    })
    render(<AnchorShadowCard />)
    expect(await screen.findByText('该账号还没有影子对照数据')).toBeInTheDocument()
  })

  it('读取失败：显式报错', async () => {
    api.fetchAnchorShadow.mockRejectedValue(new Error('接口 500'))
    render(<AnchorShadowCard />)
    expect(await screen.findByText(/读取失败：接口 500/)).toBeInTheDocument()
  })
})
