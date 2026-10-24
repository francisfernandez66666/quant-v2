// ── §DYN-TRIM（2026-10-24 owner 裁决）模拟盘手动交易契约 ──
// v3 起手动买入不填股数（qty=0 交给后端动态仓位）；**减仓改为真实部分卖出**：
// 弹窗填手数、提交 qty=手数×100，且可卖量（受 T+1 约束）上屏；只有清仓才是 qty=0。
// 改前的形态是"减仓也发 qty=0，后端 dynamic 口径又忽略 qty ⇒ 点减仓把整仓清掉"（UAT U-2/U-4）。
// 这条测试锁的就是新契约，防止旧表单/旧"减仓即清仓"口径被误加回来。
// English: §DYN-TRIM manual-trade contract — buys submit qty=0 (dynamic sizing), trims submit
// qty = lots×100 with the sellable quantity on screen, and only a close sends qty=0.
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import { settle } from './settle.js'

const POS = {
  code: '600000.SH', name: '浦发银行', strategy: 'N形', strategy_type: '',
  qty: 500, sellable_qty: 500, cost_price: 9.5, mark: 9.8, pnl: 150, pnl_pct: 3.16,
  slippage_pct: 0, latency_sec: 2, signal_at: '2026-09-18 09:35:00', filled_at: '2026-09-18 09:35:02',
}

// api 层整体 mock：页面只消费 fetchPaperState 固定账本 + 空行情/空统计，聚焦交易契约。
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
  sellPaperPosition: vi.fn(async () => ({ ok: true, sold: 200, remaining: 300, sellable: 500 })),
}))

import Paper from '../pages/Paper.jsx'
import * as api from '../api/index.js'

// 打开指定方向的交易弹窗并确认。
// 按钮文案与弹窗标题同名，getAllByText 取第一个（表格操作列在前）。
async function submitTrade(dir) {
  const label = dir === 'add' ? '加仓' : dir === 'trim' ? '减仓' : '清仓'
  fireEvent.click(screen.getAllByText(label)[0])
  await settle()
  fireEvent.click(screen.getByRole('button', { name: '确定' }))
}

describe('§DYN-TRIM Paper 手动交易契约', () => {
  // 三个用例共用同一份模块级 mock，调用次数会跨用例累加 ⇒ 每条断言前必须清账
  // （否则"清仓只调一次"会被上一条减仓用例的调用顶成 2 次）。
  // English: the mocks are module-level and shared, so call counts accumulate across cases.
  beforeEach(() => { vi.clearAllMocks() })

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
  })

  it('减仓是真实部分卖出：qty=手数×100（默认可卖量的一半），不是 qty=0 清仓', async () => {
    render(<Paper />)
    await settle()
    await submitTrade('trim')
    await settle()
    expect(api.sellPaperPosition).toHaveBeenCalledOnce()
    // 可卖 500 股 ⇒ 5 手 ⇒ 默认一半 = 2 手 ⇒ 200 股（§DYN-TRIM 唯一换算点）
    expect(api.sellPaperPosition.mock.calls[0]).toEqual(['600000.SH', 9.8, 200])
  })

  it('清仓仍是 qty=0（全平），与减仓严格区分', async () => {
    render(<Paper />)
    await settle()
    await submitTrade('close')
    await settle()
    expect(api.sellPaperPosition).toHaveBeenCalledOnce()
    expect(api.sellPaperPosition.mock.calls[0]).toEqual(['600000.SH', 9.8, 0])
  })
})
