// ── §F6 回归：量化交易页 Quant 挂载冒烟 ──
// 锁定"Can't find variable: renderChainStatusCard"类 ReferenceError（vite/esbuild 不报未定义标识符、
// 且此前无测试渲染 Quant，导致链路状态卡函数缺失在运行时才炸、并连带 ErrorBoundary 拖垮后续 tab）。
// 本用例挂载 Quant 并断言链路状态卡的关键行渲染出内容——若再丢失/抛错则挂载即失败。
// English: §F6 regression — mount Quant and assert the link-status card renders; guards the class of
// undefined-identifier ReferenceError that the bundler can't catch and that previously had no coverage.
import { describe, it, expect, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'

// 整页 API mock：挂载态仅走只读拉取，返回合法字段（不触发任何写操作）。
vi.mock('../api/index.js', () => ({
  getAccount: () => 'admin',
  fetchQMTConfig: vi.fn(async () => ({
    enabled: true, mode: 'auto', price_type: 'market', auto_sell: false,
    gateway_url: 'http://127.0.0.1:8789', token_masked: '****',
    fixed_amount: 10000, max_positions: 10, initial_capital: 100000,
    daily_max_buys: 20, daily_budget_amount: 100000, miss_heartbeat_sec: 120,
    known_strategies: [{ id: 'dragon', name: '龙头识别', kind: 'form' }],
    strategies: [], strategy_amounts: {},
  })),
  fetchQMTState: vi.fn(async () => ({
    enabled: true, mode: 'auto', tripped: false,
    gateway_url: 'http://127.0.0.1:8789',
    last_probe_at: '2026-09-13 04:00:00', last_probe_ok: true, last_latency_ms: 3,
    last_report_at: '2026-09-13 03:59:00', last_report_kind: 'heartbeat',
  })),
  fetchQMTBroker: vi.fn(async () => ({ ok: true, broker: 'xt', broker_connected: true, xt_connected: true, queued_connected: false })),
  fetchQMTTrades: vi.fn(async () => ({})),
  fetchShortStatus: vi.fn(async () => ({ short_enabled: false })),
  fetchPaperState: vi.fn(async () => ({ short_book: { enabled: false } })),
  switchQMTBroker: vi.fn(async () => ({})),
  updateQMTConfig: vi.fn(async () => ({})),
  toggleShort: vi.fn(async () => ({ short_enabled: true })),
}))

import Quant from '../pages/Quant.jsx'

describe('Quant 页挂载（§链路状态卡回归）', () => {
  it('渲染链路状态卡关键行，不再因 renderChainStatusCard 缺失而抛错', async () => {
    render(<Quant />)
    // 卡片与各行标签存在
    expect(screen.getByText('链路状态')).toBeInTheDocument()
    await waitFor(() => expect(screen.getByText('熔断')).toBeInTheDocument())
    // state 异步到位后：熔断=正常、下行=连通、执行路径 active=miniQMT兼容
    await waitFor(() => expect(screen.getByText('正常')).toBeInTheDocument())
    expect(screen.getByText('连通')).toBeInTheDocument()
    expect(screen.getByText(/当前：miniQMT兼容/)).toBeInTheDocument()
    // 网关地址回显
    expect(screen.getByText('http://127.0.0.1:8789')).toBeInTheDocument()
  })
})
