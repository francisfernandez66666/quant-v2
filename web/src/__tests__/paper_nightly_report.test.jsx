// ── §D-1（GAP_VERIFY_20260917_PM）Paper 页夜间信号质量报告卡 ──
// 锁定读端接线：按钮 → fetchPaperResearchReports → 弹窗渲染成交聚合/归因/情绪相位，
// 空表降级占位文案（有写无读修复的前端半边；Go 端点契约在 internal/server/paper_reports_test.go）。
import { describe, it, expect, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'

const REPORT = {
  reports: [{
    date: '2026-09-17', user_id: 'u1', created_at: '2026-09-17 03:00:00',
    summary: {
      generated_at: '2026-09-17 03:00:00',
      trades: [
        { strategy_type: 'dragon', side: 'buy', count: 4, total_amount: 80000, avg_price: 10.5, avg_slippage: 0.123, avg_latency: 2.4 },
        { strategy_type: 'n_shape', side: 'sell', count: 2, total_amount: 21000, avg_price: 11.2, avg_slippage: -0.05, avg_latency: 3.1 },
      ],
      attribution: [
        { UserID: 'u1', Strategy: 'dragon', Count: 4, TotalAmount: 80000, AvgSlippage: 0.12, AvgLatency: 2.4, BuyCount: 3, SellCount: 1 },
      ],
      emotion: { days: 22, last_phase: '冰点', phase_days: { 冰点: 5, 修复: 4 } },
    },
  }], count: 1,
}

// Paper 页首屏所需端点整体打桩：账户概览 fetchPaperState 给一份「模拟盘已开启」的完整 stats，
// 持仓/成交/委托/净值等列表端点一律返回空数组，夜间报告 fetchPaperResearchReports 返回上面的 REPORT 夹具；
// 第二个用例再用 mockResolvedValueOnce 换成空表，验证降级占位文案。
vi.mock('../api/index.js', () => ({
  getAccount: () => 'admin',
  fetchPaperState: vi.fn(async () => ({
    is_admin: true, enabled: true,
    stats: { initial_capital: 100000, cash: 100000, market_value: 0, total_value: 100000, total_return_pct: 0, today_return_pct: 0, realized_pnl: 0, open_positions: 0, win_rate_pct: 0, filled_buys: 0, avg_slippage_pct: 0, avg_latency_sec: 0, max_latency_sec: 0, slippage_cost: 0, signal_amount_pct: 0 },
    strategy_pools: [], pools: {}, short_book: { enabled: false },
  })),
  fetchPaperPositions: vi.fn(async () => []),
  fetchPaperTrades: vi.fn(async () => []),
  fetchPaperOrders: vi.fn(async () => []),
  fetchPaperEquity: vi.fn(async () => []),
  fetchPaperStrategies: vi.fn(async () => ({ strategies: [], known_strategies: [], blacklist: [], shadow_blacklist: true })),
  fetchPaperConfig: vi.fn(async () => ({ enabled: true, engine_enabled: true, auto_sell: true })),
  fetchPaperSelfCheck: vi.fn(async () => ({ enabled: true, is_admin: true })),
  fetchPaperResearchReports: vi.fn(async () => REPORT),
  fetchShortStatus: vi.fn(async () => ({ short_enabled: false })),
}))

import Paper from '../pages/Paper.jsx'

describe('Paper 页夜间信号质量报告（§D-1）', () => {
  it('按钮→拉取→弹窗渲染成交聚合行/归因行/情绪相位', async () => {
    render(<Paper />)
    const btn = await screen.findByRole('button', { name: /夜间报告/ })
    fireEvent.click(btn)
    // 成交聚合：战法与滑点数字渲染（dragon 同时出现在聚合行与归因行，故用 getAll）
    expect(await screen.findByText('夜间信号质量报告（researchd）')).toBeInTheDocument()
    await waitFor(() => expect(screen.getAllByText('dragon').length).toBeGreaterThanOrEqual(2))
    expect(screen.getByText('n_shape')).toBeInTheDocument()
    expect(screen.getByText(/0.123/)).toBeInTheDocument()
    // 归因行（大写字段兼容：Go 侧无 json tag 的 PaperAttribution 结构）
    expect(screen.getByText('信号承接归因')).toBeInTheDocument()
    expect(screen.getByText('3/1')).toBeInTheDocument()
    // 情绪相位锚点
    expect(screen.getByText(/最近/)).toBeInTheDocument()
    expect(screen.getByText('冰点')).toBeInTheDocument()
  })

  it('空表渲染占位文案而非报错', async () => {
    const api = await import('../api/index.js')
    api.fetchPaperResearchReports.mockResolvedValueOnce({ reports: [], count: 0 })
    render(<Paper />)
    const btn = await screen.findByRole('button', { name: /夜间报告/ })
    fireEvent.click(btn)
    expect(await screen.findByText(/暂无报告/)).toBeInTheDocument()
  })
})
