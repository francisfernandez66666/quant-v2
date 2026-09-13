// ── §DAILY_REVIEW 盘后复盘 UI 测试 ──
// 覆盖：纯函数（filters 含"盘后复盘"页签、levelTagTheme 复盘取色）+ MsgCenter 挂载
// （复盘消息渲染：倾向 Tag/标题/量化事实正文；"立即复盘"按钮调用 api.reviewPositions）。
// 整页 API mock 走 vi.mock，网络与 SSE 均不外联。
// English: §DAILY_REVIEW UI tests — pure helpers plus MsgCenter mount asserting the review
// card rendering and the manual-trigger button wiring, with a page-level api mock.
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import * as api from '../api/index.js'
import { filters, levelTagTheme } from '../pages/MsgCenter.jsx'

describe('§DAILY_REVIEW 纯函数', () => {
  it('filters 页签含"盘后复盘"（key=review）', () => {
    const f = filters.find((x) => x.key === 'review')
    expect(f).toBeTruthy()
    expect(f.label).toBe('盘后复盘')
  })

  it('levelTagTheme：复盘=primary（强调色，与告警等级区分）', () => {
    expect(levelTagTheme('复盘')).toBe('primary')
  })
})

// ── MsgCenter 挂载（复盘渲染与按钮联动）──
// 整页 API mock：仅一条复盘消息；立即复盘按钮走 reviewPositions。
vi.mock('../api/index.js', async (orig) => {
  const actual = await orig()
  return {
    ...actual,
    getAccount: () => 'admin',
    fetchAlerts: vi.fn(async () => [{
      id: 'pos-review@u1@600519@2026-09-14',
      level: '复盘',
      direction: '中性',
      title: '收盘复盘·600519(贵州茅台)',
      body: '测试复盘：均线纠缠、量能平稳。\n—— 量化事实 ——\n600519 ｜来源:持仓｜现价1275.16',
      code: '600519',
      created_at: '2026-09-14 15:05:00',
    }]),
    fetchShortStatus: vi.fn(async () => ({ short_enabled: false })),
    reviewPositions: vi.fn(async () => ({ reviewed: 1 })),
    deleteAlert: vi.fn(async () => ({})),
    clearAlerts: vi.fn(async () => ({})),
    sellPaperPosition: vi.fn(async () => ({})),
  }
})

import MsgCenter from '../pages/MsgCenter.jsx'

describe('§DAILY_REVIEW MsgCenter 复盘渲染', () => {
  beforeEach(() => { vi.clearAllMocks() })

  it('渲染复盘消息卡：倾向 Tag + 标题 + 量化事实正文', async () => {
    render(<MsgCenter />)
    // 异步 load 后出现复盘标题与倾向标记
    await waitFor(() => expect(screen.getByText(/收盘复盘·600519/)).toBeInTheDocument())
    expect(screen.getByText('中性')).toBeInTheDocument()
    expect(screen.getByText(/量化事实/)).toBeInTheDocument()
  })

  it('存在"盘后复盘"筛选页签与"立即复盘"按钮，点击后调用 API', async () => {
    render(<MsgCenter />)
    await waitFor(() => expect(screen.getByText('盘后复盘')).toBeInTheDocument())
    const btn = screen.getByText('立即复盘')
    fireEvent.click(btn)
    await waitFor(() => expect(api.reviewPositions).toHaveBeenCalledTimes(1))
  })
})
