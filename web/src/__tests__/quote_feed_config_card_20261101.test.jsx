// ── §SUBSCRIPTION-A（2026-11-01 出厂开）行情通道开关卡的三条消费契约 ──
// 这批把行情四键改成出厂即开，卡上就多了一个会要命的形状：勾选框若从本地 false 起步，
// 用户一进卡、什么都没动就点保存，稀疏 merge 会把这三枚键当成"改动"发出去 ⇒ 悄悄把主源档关掉。
//   ① 勾选框初值必须取后端下发的**生效值**（缺键=true）；
//   ② 未改动时保存必须一条键都不发（api.updateRuntimeConfig 不得被调用）；
//   ③ state=not_polled 在设置页也要单独可见（与 Dashboard 同一口径：指向重启，不折成降级）。
// English: the checkboxes must be seeded from the effective values, a no-op save must send an
// empty patch, and the never-polled state must read as its own condition on the settings card.
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor, fireEvent } from '@testing-library/react'

vi.mock('tdesign-react', async (importOriginal) => {
  const real = await importOriginal()
  return { ...real, MessagePlugin: { success: vi.fn(), warning: vi.fn(), error: vi.fn(), info: vi.fn() } }
})

// 出厂态：后端只下发生效值（四枚指针位在 config.json 里根本没写过）
const factoryView = () => ({
  qmt_feed_enabled: true,
  qmt_feed_primary: true,
  quote_per_code_source: true,
  quote_hub_consult: true,
  qmt_feed_coverage_min_pct: 0,
  coverage_min_pct_effective: 80,
  qmt_feed_volume_to_shares: 0,
  volume_to_shares_effective: 1,
  monitor_pool_hot_max: 0,
  monitor_pool_hot_max_effective: 400,
  qmt_feed_interval_sec: 0,
  hot_keys: ['qmt_feed_primary'],
  restart_required: ['qmt_feed_enabled', 'qmt_feed_interval_sec'],
  feed_state: 'ok',
  feed_rounds: 12,
  feed_hits: 180,
  feed_denominator: 200,
  feed_coverage_pct: 90,
  feed_volume_aligned: true,
  feed_volume_observed_unit: 1,
  feed_ready_to_promote: false,
  // J0 两栏（2026-11-02）：本轮回包条数与连续空回包轮数——只有"命中 0"分不出病因
  feed_returned: 200,
  feed_empty_rounds: 0,
  quote_source_mixed: 0,
})

const updateRuntimeConfig = vi.fn(async (patch) => ({ ...factoryView(), ...patch, changed: Object.keys(patch) }))

vi.mock('../api/index.js', () => ({
  getAccount: () => 'admin',
  getToken: () => 'tk',
  getStoredServer: () => '',
  isAdmin: () => true,
  hasStrictPerm: () => true,
  getRole: () => 'admin',
  fetchRuntimeConfig: vi.fn(async () => factoryView()),
  updateRuntimeConfig: vi.fn(async () => ({})),
}))

import QuoteFeedConfigCard from '../pages/QuoteFeedConfigCard.jsx'
import * as api from '../api/index.js'

describe('行情通道开关卡（出厂开语义）', () => {
  beforeEach(() => {
    api.fetchRuntimeConfig.mockImplementation(async () => factoryView())
    api.updateRuntimeConfig.mockImplementation(updateRuntimeConfig)
  })

  it('三枚开关按后端生效值勾选（缺键=true，不得从 false 起步）', async () => {
    render(<QuoteFeedConfigCard />)
    await waitFor(() => expect(screen.getByText(/行情通道（券商 L1）/)).toBeInTheDocument())
    const boxes = screen.getAllByRole('checkbox')
    expect(boxes.length).toBeGreaterThanOrEqual(3)
    expect(boxes.map((b) => b.checked)).toEqual([true, true, true])
  })

  it('什么都没动就点保存 ⇒ 一次写请求都不发（稀疏 merge 不得把出厂开悄悄关掉）', async () => {
    render(<QuoteFeedConfigCard />)
    await waitFor(() => expect(screen.getByRole('button', { name: /保存/ })).toBeInTheDocument())
    fireEvent.click(screen.getByRole('button', { name: /保存/ }))
    await waitFor(() => expect(screen.getByText(/没有改动可保存/)).toBeInTheDocument())
    expect(api.updateRuntimeConfig).not.toHaveBeenCalled()
  })

  it('只动一枚键 ⇒ 补丁里只有那一枚（其余键必须原样保留，不被折成 false/0）', async () => {
    render(<QuoteFeedConfigCard />)
    await waitFor(() => expect(screen.getByRole('button', { name: /保存/ })).toBeInTheDocument())
    fireEvent.click(screen.getAllByRole('checkbox')[0]) // 关掉主源档
    fireEvent.click(screen.getByRole('button', { name: /保存/ }))
    await waitFor(() => expect(api.updateRuntimeConfig).toHaveBeenCalled())
    expect(api.updateRuntimeConfig.mock.calls[0][0]).toEqual({ qmt_feed_primary: false })
  })

  it('not_polled 在设置页单独成一条文案（指向重启，不折成"正常"或"降级"）', async () => {
    api.fetchRuntimeConfig.mockImplementation(async () => ({ ...factoryView(), feed_state: 'not_polled', feed_rounds: 0 }))
    render(<QuoteFeedConfigCard />)
    expect(await screen.findByText(/一轮都没跑/)).toBeInTheDocument()
    expect(screen.queryByText('正常')).not.toBeInTheDocument()
  })

  it('量纲未确认时给出告警标签（未对齐＝不接管量/额，VWAP 差 100 倍的那道闸）', async () => {
    api.fetchRuntimeConfig.mockImplementation(async () => ({ ...factoryView(), feed_volume_aligned: false, feed_volume_observed_unit: 100 }))
    render(<QuoteFeedConfigCard />)
    expect(await screen.findByText(/量纲未确认/)).toBeInTheDocument()
  })

  // J0（2026-11-02 补的零回包档）：现网真实形态是"网关活着、每 3 秒回 200、ticks:{}"，
  // 而它过去在任何一档都不沾 ⇒ 面板一路显示 ok、必推规则一次没响。
  // 这两条用例锁的是"看得见"落在前端：文案要指向上游无服务（不是一句泛泛的"降级"），
  // 且回包条数/连续空轮数要出现——只写"命中 0"分不出 J2（回包非空但超龄）与 J0（一条都没回）。
  it('零回包（J0）显示独立文案并带上连续空回包轮数', async () => {
    api.fetchRuntimeConfig.mockImplementation(async () => ({
      ...factoryView(), feed_state: 'degraded:J0', feed_hits: 0, feed_returned: 0, feed_empty_rounds: 24,
    }))
    render(<QuoteFeedConfigCard />)
    expect(await screen.findByText(/零回包/)).toBeInTheDocument()
    expect(screen.getByText(/上游无行情服务/)).toBeInTheDocument()
    expect(screen.getByText(/连续空回包 24 轮/)).toBeInTheDocument()
    // 不许被折成通用"券商行情降级"：处置方向不同（J0 查券商通道，J2 查陈旧）
    expect(screen.queryByText(/券商行情降级/)).not.toBeInTheDocument()
  })

  it('回包非空但零命中（J2）不吃 J0 文案，也不显示连续空轮数', async () => {
    api.fetchRuntimeConfig.mockImplementation(async () => ({
      ...factoryView(), feed_state: 'degraded:J2', feed_hits: 0, feed_returned: 180, feed_empty_rounds: 0,
    }))
    render(<QuoteFeedConfigCard />)
    expect(await screen.findByText(/券商行情降级/)).toBeInTheDocument()
    expect(screen.queryByText(/零回包/)).not.toBeInTheDocument()
    expect(screen.queryByText(/连续空回包/)).not.toBeInTheDocument()
    expect(screen.getByText(/本轮回包 180 条/)).toBeInTheDocument()
  })
})
