// ── §F6 回归：量化交易页 Quant 挂载冒烟 ──
// 锁定"Can't find variable: renderChainStatusCard"类 ReferenceError（vite/esbuild 不报未定义标识符、
// 且此前无测试渲染 Quant，导致链路状态卡函数缺失在运行时才炸、并连带 ErrorBoundary 拖垮后续 tab）。
// 本用例挂载 Quant 并断言链路状态卡的关键行渲染出内容——若再丢失/抛错则挂载即失败。
// English: §F6 regression — mount Quant and assert the link-status card renders; guards the class of
// undefined-identifier ReferenceError that the bundler can't catch and that previously had no coverage.
import { describe, it, expect, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
// §DET-TIME（2026-09-24）：等 UI 一律用 settle()（排空微任务队列）而不是 waitFor/findBy——这些用例的接口都是 resolved promise 的 mock，用真实时钟轮询在邻居负载下必偶发红（见 settle.js 文件头）。
import { settle } from './settle.js'

// 整页 API mock：挂载态仅走只读拉取，返回合法字段（不触发任何写操作）。
vi.mock('../api/index.js', () => ({
  getAccount: () => 'admin',
  // §M13：补齐 mock 缺失导出（与 api/index.js 真实实现一致，仅按状态码判定，不改变本文件既有断言）。
  isForbidden: (e) => !!(e && e.status === 403),
  fetchQMTConfig: vi.fn(async () => ({
    enabled: true, mode: 'auto', price_type: 'market', auto_sell: false,
    gateway_url: 'http://127.0.0.1:8789', token_masked: '****',
    fixed_amount: 10000, max_positions: 10, initial_capital: 100000,
    max_order_amount: 150000,
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
    await settle()
    expect(screen.getByText('熔断')).toBeInTheDocument()
    // state 异步到位后：熔断=正常、下行=连通、执行路径 active=miniQMT兼容
    await settle()
    expect(screen.getByText('正常')).toBeInTheDocument()
    expect(screen.getByText('连通')).toBeInTheDocument()
    expect(screen.getByText(/当前：miniQMT兼容/)).toBeInTheDocument()
    // 网关地址回显
    expect(screen.getByText('http://127.0.0.1:8789')).toBeInTheDocument()
  })
})

// §AUDIT-PM 2026-09-15 单笔金额绝对帽：仓位纪律卡渲染输入框、服务端值回填、
// 编辑后保存的 payload 携带 max_order_amount（后端契约面见 Go 测试，此处锁前端不回退）。
describe('Quant 页单笔金额绝对帽字段（§AUDIT-PM）', () => {
  it('渲染标签与服务端值回填，保存 payload 携带该字段', async () => {
    const api = await import('../api/index.js')
    render(<Quant />)
    await settle()
    expect(screen.getByText('单笔金额绝对帽(元)')).toBeInTheDocument()
    await settle()
    const input = screen.getByDisplayValue('150000')
    fireEvent.change(input, { target: { value: '200000' } })
    fireEvent.click(screen.getByRole('button', { name: '保存仓位纪律' }))
    await settle()
    expect(api.updateQMTConfig).toHaveBeenCalled()
    const payload = api.updateQMTConfig.mock.calls.at(-1)[0]
    expect(payload.max_order_amount).toBe(200000)
  })
})
