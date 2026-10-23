// ── §DYN-SIZE-v3（2026-10-23）模拟盘手动交易契约 ──
// v3 起手动买入不再填股数：弹窗只保留价格，提交 qty=0 由后端动态仓位计算；
// 手动卖出统一按清仓处理（qty=0=全平），不再提供减仓入口。
// 旧 §FIX-1 的「手→股 ×100」契约随手数输入一并退场——这条测试锁的是新契约，
// 防止旧表单/旧换算被误加回来。
// English: v3 manual-trade contract — no qty input; buys submit qty=0 for dynamic sizing and
// sells always close (qty=0). The old lots→shares ×100 path is retired with the input.
import { describe, it, expect, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import { settle } from './settle.js'

const POS = {
  code: '600000.SH', name: '浦发银行', strategy: 'N形', strategy_type: '',
  qty: 500, cost_price: 9.5, mark: 9.8, pnl: 150, pnl_pct: 3.16,
  slippage_pct: 0, latency_sec: 2, signal_at: '2026-09-18 09:35:00', filled_at: '2026-09-18 09:35:02',
}

// api 层整体 mock：页面只消费 fetchPaperState 固定账本 + 空行情/空统计，聚焦新交易契约。
vi.mock('../api/index.js', () => ({
  getAccount: () => 'admin',
  isForbidden: (e) => !!(e && e.status === 403),
  fetchPaperState: vi.fn(async () => ({
    is_admin: true, enabled: true,
    stats: { initial_capital: 100000, cash: 95000, market_value: 4900, total_value: 99900, total_return_pct: -0.1, today_return_pct: 0, realized_pnl: 0, open_positions: 1, win_rate_pct: 0, filled_buys: 1, avg_slippage_pct: 0, avg_latency_sec: 2, max_latency_sec: 2, slippage_cost: 0, signal_amount_pct: 0 },
    strategy_pools: [], pools: {}, short_book: { enabled: false },
  })),
  fetchPaperPositions: vi.fn(async () => [POS]),
  fetchPaperTrades: vi.fn(async () => []),
  fetchPaperOrders: vi.fn(async () => []),
  fetchPaperEquity: vi.fn(async () => []),
  fetchPaperStrategies: vi.fn(async () => ({ strategies: [], known_strategies: [], blacklist: [], shadow_blacklist: true })),
  fetchPaperConfig: vi.fn(async () => ({ enabled: true, engine_enabled: true, auto_sell: true })),
  fetchPaperSelfCheck: vi.fn(async () => ({ enabled: true, is_admin: true })),
  fetchPaperResearchReports: vi.fn(async () => ({ reports: [], count: 0 })),
  fetchShortStatus: vi.fn(async () => ({ short_enabled: false })),
  buyPaperPosition: vi.fn(async () => ({})),
  sellPaperPosition: vi.fn(async () => ({})),
}))

import Paper from '../pages/Paper.jsx'
import * as api from '../api/index.js'

// 打开交易弹窗并确认；v3 不再有手数输入，因此不再需要填写步骤。
async function submitTrade(dir) {
  fireEvent.click(screen.getAllByText(dir === 'add' ? '加仓' : '清仓')[0])
  await settle()
  fireEvent.click(screen.getByRole('button', { name: '确定' }))
}

describe('§DYN-SIZE-v3 Paper 手动交易契约', () => {
  it('加仓不填股数：buyPaperPosition qty=0（后端动态预算），价格仍回填 mark', async () => {
    render(<Paper />)
    await settle()
    await submitTrade('add')
    await settle()
    expect(api.buyPaperPosition).toHaveBeenCalledOnce()
    const args = api.buyPaperPosition.mock.calls[0]
    expect(args[0]).toBe('600000.SH')
    expect(args[4]).toBe(9.8) // 价格回填 mark
    expect(args[5]).toBe(0) // 数量交给动态仓位
    // 旧手数输入必须彻底消失（防止换算逻辑被误加回来）。
    expect(screen.queryByPlaceholderText('手数（1手=100股）')).toBeNull()
  })

  it('卖出统一清仓：sellPaperPosition qty=0（全平）', async () => {
    render(<Paper />)
    await settle()
    await submitTrade('close')
    await settle()
    expect(api.sellPaperPosition).toHaveBeenCalledOnce()
    expect(api.sellPaperPosition.mock.calls[0]).toEqual(['600000.SH', 9.8, 0])
  })
})
