// ── §WMQ-3（20260917）情绪回看 React 重复 key 回归 ──
// 缺口：X 轴日期刻度索引 [0, floor(n/2), n-1] 在区间天数 ≤2 时三点重合（如 n=2 → [0,1,1]），
// React 渲染 "two children with the same key t1" 告警（当日 UAT 实录）。修复为 Set 去重。
// 本用例用 2 天小样本触发/calendar 该分支，断言不再出现重复 key 警告。
// English: WMQ-3 regression — X-axis tick indices collide when the review window has ≤2 days;
// fixed via Set dedupe; this test asserts React no longer logs a duplicate-key warning.
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'

vi.mock('../sseBus.js', () => ({ on: () => () => {} }))

// 最小数据：仅 2 天（n=2 → 原实现索引 1 重复，正是告警分支）
const hist = [
  { date: '2026-09-16', emotion: '冰点', limit_up_count: 10, ladder_height: 2, max_pos_pct: 0.1 },
  { date: '2026-09-17', emotion: '启动', limit_up_count: 30, ladder_height: 3, max_pos_pct: 0.2 },
]
const matrix = { phases: ['冰点', '启动'], min_events: 20, rows: [] }

let mockHistory, mockMatrix, mockEquity
vi.mock('../api/index.js', () => ({
  fetchEmotionHistory: (...a) => mockHistory(...a),
  fetchEmotionStrategyMatrix: (...a) => mockMatrix(...a),
  fetchPaperEquity: (...a) => mockEquity(...a),
}))

import EmotionReview from '../pages/EmotionReview.jsx'

beforeEach(() => {
  mockHistory = vi.fn(async () => ({ days: 30, series: hist }))
  mockMatrix = vi.fn(async () => matrix)
  mockEquity = vi.fn(async () => ({ series: [], min: 0, max: 1, hasEq: false }))
})

describe('EmotionReview §WMQ-3：区间天数≤2 的 X 轴刻度不再产生重复 key 告警', () => {
  it('2 日区间渲染无 React same-key console.error', async () => {
    const errors = []
    const spy = vi.spyOn(console, 'error').mockImplementation((...a) => { errors.push(a.join(' ')) })
    try {
      render(<MemoryRouter><EmotionReview /></MemoryRouter>)
      // 等待首帧渲染完成后再断言
      await waitFor(() => expect(mockHistory).toHaveBeenCalled(), { timeout: 3000 })
      const dup = errors.filter((s) => s.includes('same key'))
      expect(dup, '不应出现重复 key 告警: ' + dup.join('|')).toHaveLength(0)
    } finally {
      spy.mockRestore()
    }
  })
})
