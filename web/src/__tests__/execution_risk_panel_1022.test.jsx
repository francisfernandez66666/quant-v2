// execution_risk_panel_1022.test.jsx — 执行层风控面板最小契约锁。
//
// 覆盖：GET 回显 → 表单渲染；保存载荷包含 risk_gate / VWAP / 卖出自动化；概念覆盖率展示。
// English: minimal contract lock for the execution-risk panel — load echoes, save payload shape,
// and the concept coverage readout.
import React from 'react'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach } from 'vitest'

const mocks = vi.hoisted(() => ({
  fetchQMTConfig: vi.fn(),
  fetchConceptStatus: vi.fn(),
  updateQMTConfig: vi.fn(),
}))

vi.mock('../api/index.js', () => ({
  fetchQMTConfig: mocks.fetchQMTConfig,
  fetchConceptStatus: mocks.fetchConceptStatus,
  updateQMTConfig: mocks.updateQMTConfig,
}))

vi.mock('../ui.jsx', () => ({ showToast: () => {} }))

import ExecutionRiskPanel from '../components/ExecutionRiskPanel.jsx'

beforeEach(() => {
  mocks.updateQMTConfig.mockReset().mockResolvedValue({ status: 'ok' })
  mocks.fetchQMTConfig.mockReset().mockResolvedValue({
    mode: 'auto',
    auto_sell: true,
    auto_sell_all_sources: true,
    auto_risk_caution: false,
    sell_unified_mode: 'shadow',
    min_order_amount: 10000,
    entry_pullback_pct: 0,
    entry_timeout_sec: 0,
    max_signal_vwap_premium_pct: 0,
    risk_gate: {
      sector_value_pct: 30,
      sector_value_shadow: false,
      concept_value_pct: 25,
      concept_value_shadow: true,
      risk_per_trade_pct: 0.5,
      risk_budget_shadow: false,
      limit_up_block_buy: true,
      limit_down_block_sell: true,
      day_loss_limit_pct: 3,
      single_stock_value_pct: 20,
      stale_quote_ms: 30000,
    },
  })
  mocks.fetchConceptStatus.mockReset().mockResolvedValue({
    synced_concepts: 342, total_concepts: 360, coverage_pct: 95, last_success: '2026-10-22T15:00:00+08:00', failed_concepts: [],
  })
})

describe('§C1/C2/C3/C5/C6/B7 执行层面板', () => {
  it('加载回显并保存完整执行层载荷', async () => {
    render(<ExecutionRiskPanel />)
    await waitFor(() => expect(screen.getByText('执行层风控与执行质量')).toBeInTheDocument())
    expect(screen.getByText('95.0%')).toBeInTheDocument()
    expect(screen.getByTestId('exec-mode').value).toBe('auto')

    fireEvent.click(screen.getByRole('button', { name: '保存执行层配置' }))
    await waitFor(() => expect(mocks.updateQMTConfig).toHaveBeenCalledTimes(1))
    const body = mocks.updateQMTConfig.mock.calls[0][0]
    expect(body.mode).toBe('auto')
    expect(body.auto_sell_all_sources).toBe(true)
    expect(body.sell_unified_mode).toBe('shadow')
    expect(body.min_order_amount).toBe(10000)
    expect(body.max_signal_vwap_premium_pct).toBe(0)
    expect(body.risk_gate.sector_value_pct).toBe(30)
    expect(body.risk_gate.sector_value_shadow).toBe(false)
    expect(body.risk_gate.concept_value_pct).toBe(25)
    expect(body.risk_gate.risk_per_trade_pct).toBe(0.5)
    expect(body.risk_gate.limit_up_block_buy).toBe(true)
    expect(body.risk_gate.stale_quote_ms).toBe(30000)
  })
})
