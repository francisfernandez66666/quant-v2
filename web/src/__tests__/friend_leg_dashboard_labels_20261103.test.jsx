// ── §MULTI-ACCT-B 批5（2026-11-03）Dashboard「远端执行腿」三态文案的消费契约 ──
// 这批新增的是"朋友的腿在远端机器"这一事实，而它最坏的形态恰恰是看起来正常：
// 朋友的机器照常把成交报上来（页面数字是新鲜的），但广州已经发不进指令。
// 所以三态必须长得不一样、且**运营单机（state=0）不得多出任何新字**：
//   1 远端腿正常 / 2 不可达（查开机·QMT·overlay）/ 3 撤销未触达（人工停腿清待发）/ 0 不加文案。
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'

vi.mock('tdesign-react', async (importOriginal) => {
  const real = await importOriginal()
  return { ...real, MessagePlugin: { success: vi.fn(), warning: vi.fn(), error: vi.fn(), info: vi.fn() } }
})

vi.mock('../api/index.js', async (importOriginal) => {
  const actual = await importOriginal()
  return { ...actual,
    getAccount: () => 'admin',
    getToken: () => 'tk',
    getStoredServer: () => '',
    isAdmin: () => true,
    hasStrictPerm: () => true,
    getRole: () => 'admin',
    isForbidden: (e) => !!(e && e.status === 403),
    fetchStatus: vi.fn(async () => ({ quote_source: 'sina', quote_age_sec: 2, in_trade_time: true, quote_feed: null })),
    fetchDashboard: vi.fn(async () => ({ signals: [], news_events: [], hot_sectors: [], final_signals: [] })),
    fetchSignals: vi.fn(async () => []),
    fetchNews: vi.fn(async () => []),
    fetchHealth: vi.fn(async () => ({ ok: true })),
    fetchAccount: vi.fn(async () => ({ total_asset: 100000, available: 50000 })),
    fetchQMTState: vi.fn(async () => null),
  }
})

import Dashboard from '../pages/Dashboard.jsx'
import * as apiMod from '../api/index.js'

const base = { enabled: true, mode: 'auto', tripped: false, last_probe_ok: true, gateway_url: 'http://10.77.0.2:8789' }

describe('Dashboard 远端执行腿三态文案（§MULTI-ACCT-B 批5）', () => {
  beforeEach(() => { apiMod.fetchQMTState.mockResolvedValue(null) })

  const renderExpect = async (text, present) => {
    render(<MemoryRouter><Dashboard /></MemoryRouter>)
    if (present) await waitFor(() => expect(screen.getByText(new RegExp(text.slice(0, 12)))).toBeTruthy())
    else {
      // 反向用例不能等"mock 被调过"（Dashboard 的实盘轮询有节流，5s 内可能还没发枪，
      // 于是假红成"组件坏了"）；改成等指示文本真的渲染出来，再断言没有腿文案。
      await waitFor(() => expect(screen.getAllByText(/正常|不可达|未触达/).length).toBeGreaterThan(0))
      expect(screen.queryByText(new RegExp(text.slice(0, 12)))).toBeNull()
    }
  }

  it('state=2 显示"远端腿不可达"并点名查哪三样', () => {
    apiMod.fetchQMTState.mockResolvedValue({ ...base, friend_leg_state: 2 })
    return renderExpect('远端腿不可达', true)
  })

  it('state=3 显示"撤销未触达"，且不吃 state=2 的文案（两件事的下一步不同）', () => {
    apiMod.fetchQMTState.mockResolvedValue({ ...base, friend_leg_state: 3 })
    return renderExpect('远端腿撤销未触达', true)
  })

  it('state=1 显示"远端腿正常"', () => {
    apiMod.fetchQMTState.mockResolvedValue({ ...base, friend_leg_state: 1 })
    return renderExpect('远端腿正常', true)
  })

  it('state=0（运营单机、没有远端腿）不加任何腿文案——今天那行字不许被改', () => {
    apiMod.fetchQMTState.mockResolvedValue({ ...base, friend_leg_state: 0 })
    return renderExpect('远端腿', false)
  })
})
