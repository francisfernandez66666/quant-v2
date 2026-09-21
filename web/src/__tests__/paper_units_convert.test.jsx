// ── §FIX-1(20260919) 模拟盘手动交易"手→股"换算契约 ──
// Paper 页交易弹窗表单口径=手数（label"手数（1手=100股）"），HTTP 契约口径=股数：
// 断言 confirmTrade 提交前唯一换算点 ×100 生效——加仓 3 手 → payload qty=300；
// 减仓 2 手 → sellPaperPosition qty=200；清仓 → qty=0（全平语义不变）。
// 修复前实证：UI 传 3（手）被引擎按 3 股记账，账本 100 倍错位（UAT_CONSULT_CONTRACT_20260919 P0-1）。
// English: §FIX-1 — the modal speaks lots, the API speaks shares; assert the single ×100 conversion
// point in confirmTrade (add 3 lots → qty 300; trim 2 → 200; close → 0).
import { describe, it, expect, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'

const POS = {
  code: '600000.SH', name: '浦发银行', strategy: 'N形', strategy_type: '',
  qty: 500, cost_price: 9.5, mark: 9.8, pnl: 150, pnl_pct: 3.16,
  slippage_pct: 0, latency_sec: 2, signal_at: '2026-09-18 09:35:00', filled_at: '2026-09-18 09:35:02',
}

// api 层整体 mock：页面只消费 fetchPaperState 固定账本 + 空行情/空统计，聚焦"手→股"换算呈现。
vi.mock('../api/index.js', () => ({
  getAccount: () => 'admin',
  // §M13：补齐 mock 缺失导出（与 api/index.js 真实实现一致，仅按状态码判定，不改变本文件既有断言）。
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

// 打开指定方向的交易弹窗、填入手数并点确定，返回对应 API 调用断言所需的等待句柄
async function submitTrade(dir, lots) {
  fireEvent.click(screen.getAllByText(dir === 'add' ? '加仓' : dir === 'trim' ? '减仓' : '清仓')[0])
  if (dir !== 'close') {
    const input = await screen.findByPlaceholderText('手数（1手=100股）')
    fireEvent.change(input, { target: { value: String(lots) } })
  }
  fireEvent.click(await screen.findByRole('button', { name: '确定' }))
}

describe('§FIX-1 Paper 交易弹窗手→股换算', () => {
  it('加仓 3 手 → buyPaperPosition qty=300（股）', async () => {
    render(<Paper />)
    await screen.findByText('浦发银行')
    await submitTrade('add', 3)
    await waitFor(() => expect(api.buyPaperPosition).toHaveBeenCalledOnce())
    const args = api.buyPaperPosition.mock.calls[0]
    expect(args[0]).toBe('600000.SH')
    expect(args[5]).toBe(300) // qty 位=股数，非表单里的 3
    expect(args[4]).toBe(9.8) // 价格回填 mark，未被换算污染
  })

  it('减仓 2 手 → sellPaperPosition qty=200；清仓 → qty=0', async () => {
    render(<Paper />)
    await screen.findByText('浦发银行')
    await submitTrade('trim', 2)
    await waitFor(() => expect(api.sellPaperPosition).toHaveBeenCalledOnce())
    expect(api.sellPaperPosition.mock.calls[0]).toEqual(['600000.SH', 9.8, 200])

    await submitTrade('close', 0)
    await waitFor(() => expect(api.sellPaperPosition).toHaveBeenCalledTimes(2))
    expect(api.sellPaperPosition.mock.calls[1]).toEqual(['600000.SH', 9.8, 0])
  })
})
