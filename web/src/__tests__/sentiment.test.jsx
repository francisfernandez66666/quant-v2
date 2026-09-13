// ── §情绪面板 A/B/C 回归（2026-09-13）──
// 锁死：F45（TDesign Card 头部插槽 actions 非 headerRightContent——修复前"回看全年"/区间按钮组
// 静默不渲染）、B 矩阵懒加载纪律（折叠不请求、展开才拉）、hit_rate 0-1 比例展示 ×100、
// C 页区间切换触发 days 参数、SVG 柱+色带渲染数。
// English: sentiment panel regressions — card header slot (actions), matrix lazy-load discipline,
// hit-rate fraction ×100 display, review page range switching, SVG element counts.
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'

vi.mock('../sseBus.js', () => ({ on: () => () => {} }))

const hist = [
  { date: '2026-09-07', emotion: '冰点', limit_up_count: 15, ladder_height: 2, max_pos_pct: 0.1 },
  { date: '2026-09-10', emotion: '高潮', limit_up_count: 120, ladder_height: 6, max_pos_pct: 0.6 },
]
const matrix = {
  phases: ['冰点', '启动', '发酵', '高潮', '退潮', '背离'],
  min_events: 20,
  rows: [{
    candidate_id: 7, kind: 'factor', name: '测试战法甲', horizons: [5], total_events: 3,
    cells: [
      { phase: '冰点', events: 2, thin: true, avg_excess: { 5: 1.5 }, hit_rate: { 5: 0.6 }, avg_limit_up: 15 },
      { phase: '高潮', events: 1, thin: true, avg_excess: { 5: 2.0 }, hit_rate: { 5: 0.9 }, avg_limit_up: 120 },
    ],
  }],
}

let mockHistory, mockMatrix, mockEquity
vi.mock('../api/index.js', () => ({
  fetchEmotionHistory: (...a) => mockHistory(...a),
  fetchEmotionStrategyMatrix: (...a) => mockMatrix(...a),
  fetchPaperEquity: (...a) => mockEquity(...a),
}))

import SentimentCard from '../components/SentimentCard.jsx'
import EmotionReview from '../pages/EmotionReview.jsx'

beforeEach(() => {
  mockHistory = vi.fn(async () => ({ days: 30, series: hist }))
  mockMatrix = vi.fn(async () => matrix)
  mockEquity = vi.fn(async () => [])
})

describe('SentimentCard（§情绪面板 A+B）', () => {
  it('渲染相位徽章/色带/指标，头部 actions 插槽含回看入口（F45）', async () => {
    render(<MemoryRouter><SentimentCard /></MemoryRouter>)
    expect(await screen.findByText('高潮')).toBeInTheDocument() // 最近一日相位
    expect(screen.getByText('120')).toBeInTheDocument()          // 涨停家数
    expect(screen.getByText('60%')).toBeInTheDocument()          // 建议仓位 0.6×100
    // F45 核心：Card 头部右侧插槽必须真正渲染（headerRightContent 时代此处为空）
    expect(screen.getByRole('button', { name: /回看全年/ })).toBeInTheDocument()
  })

  it('矩阵折叠不请求、展开才拉；hit_rate 按 0-1 比例 ×100 展示', async () => {
    render(<MemoryRouter><SentimentCard /></MemoryRouter>)
    await screen.findByText('高潮')
    expect(mockMatrix).not.toHaveBeenCalled() // 懒加载纪律：默认收起不占接口
    fireEvent.click(screen.getByText(/看情绪×战法矩阵/))
    await waitFor(() => expect(mockMatrix).toHaveBeenCalledTimes(1))
    expect(await screen.findByText('测试战法甲')).toBeInTheDocument()
    // 格值=主 horizon 平均超额；tooltip 命中率 ×100（0.6→60%）
    const cell = screen.getByTitle(/事件 2/)
    expect(cell.getAttribute('title')).toContain('命中 60%')
    expect(cell.getAttribute('title')).toContain('样本<20')
    expect(cell.textContent).toBe('+1.5%')
  })
})

describe('EmotionReview（§情绪面板 C）', () => {
  it('SVG 柱+色带按日渲染，区间切换重拉 days', async () => {
    render(<MemoryRouter><EmotionReview /></MemoryRouter>)
    await screen.findByText('悬停柱子看当日明细')
    // 每日 2 个 rect（涨停柱 + 色带格）：2 日 → 4 rect
    expect(document.querySelectorAll('svg rect').length).toBe(4)
    expect(screen.getByText(/冰点 ×1/)).toBeInTheDocument() // 相位图例
    fireEvent.click(screen.getByRole('button', { name: '250日' })) // F45：头部按钮组存在
    await waitFor(() => expect(mockHistory).toHaveBeenLastCalledWith(250))
  })

  it('空数据渲染占位文案不抛错', async () => {
    mockHistory = vi.fn(async () => ({ days: 120, series: [] }))
    render(<MemoryRouter><EmotionReview /></MemoryRouter>)
    expect(await screen.findByText(/暂无情绪留痕数据/)).toBeInTheDocument()
  })
})
