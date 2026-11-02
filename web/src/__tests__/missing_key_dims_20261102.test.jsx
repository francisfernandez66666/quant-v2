// missing_key_dims_20261102.test.jsx — §INDEX-BLIND（2026-11-02）盲区标签的三态契约。
//
// 钉两件事，都是 11-02 现网那笔账的直接反面：
//   ① 盲区态必须**点出缺的是哪一维**（旧文案只有"关键读数缺失"六个字，
//      值班看不出该去查状态机、情绪还是指数——这三条的处置动作完全不同）；
//   ② "兜底中"（点位来自后备腿或 last-known-good，仍真实）不许吃盲区文案，
//      也不许反过来把盲区折成"读数异常"一句（折成一句就是让人盲操作，同 J0/J2 分形那条）。
// 另加一条旧帧兼容：本次部署前发布的帧没有 dims 字段，必须退回今天那句原文案而不是渲染空白。
import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import BlindTag from '../components/BlindTag.jsx'
import { blindTag, blindDimsText } from '../utils/blindDims.js'

describe('关键读数盲区标签（三态）', () => {
  it('盲区态显示缺失维度名', () => {
    render(<BlindTag readout={{ missing_key_input: true, missing_key_dims: ['index_change'], market_scale: 0.5 }} />)
    // 维度名与结论都要在：只说"缺失"＝让人盲操作
    expect(screen.getByText(/关键读数缺失（大盘指数）/)).toBeInTheDocument()
    expect(screen.getByText(/目标封顶半仓且今日不做 T/)).toBeInTheDocument()
  })

  it('多维同时缺失时逐一列出（顺序照后端清单，不在前端重排）', () => {
    const t = blindTag({ missing_key_input: true, missing_key_dims: ['market_state', 'emotion_phase'] })
    expect(t.text).toContain('市场状态、情绪相位')
    expect(t.theme).toBe('danger')
  })

  it('陈旧兜底态不吃盲区文案，也不显示封顶结论', () => {
    render(<BlindTag readout={{ missing_key_input: false, index_source: 'sina', index_stale: true }} />)
    expect(screen.getByText(/指数为兜底读数（来源 sina，陈旧回退）/)).toBeInTheDocument()
    expect(screen.queryByText(/封顶半仓/)).not.toBeInTheDocument()
    expect(screen.queryByText(/关键读数缺失/)).not.toBeInTheDocument()
  })

  it('后备腿接管但不陈旧：仍要报来源（现场要知道现在是谁在给点位）', () => {
    render(<BlindTag readout={{ missing_key_input: false, index_source: 'tencent', index_stale: false }} />)
    expect(screen.getByText(/指数为兜底读数（来源 tencent）/)).toBeInTheDocument()
  })

  it('东财现取且齐 ⇒ 不渲染任何标签（健康态不需要一块红字）', () => {
    const { container } = render(<BlindTag readout={{ missing_key_input: false, index_source: 'eastmoney', index_stale: false }} />)
    expect(container).toBeEmptyDOMElement()
  })

  it('旧帧没有 dims 字段时退回今天那句原文案（不渲染 undefined/空白）', () => {
    render(<BlindTag readout={{ missing_key_input: true }} />)
    expect(screen.getByText('关键读数缺失：目标封顶半仓且今日不做 T')).toBeInTheDocument()
    expect(screen.queryByText(/undefined/)).not.toBeInTheDocument()
  })

  it('未知维度码原样回显，不编一个中文名字（编错名字＝指错处置方向）', () => {
    expect(blindDimsText(['index_change', 'some_new_dim'])).toBe('大盘指数、some_new_dim')
  })
})
