// carrier_update_20261104.test.jsx — §CARRIER-UPDATE 载体更新面板的行为锁（2026-11-04）。
//
// 这个面板是"云端点了之后到底落下去没有"的唯一读面，所以四件事必须钉死：
//  ① 读数缺失/known=false ⇒ 显示「读数未知」，**绝不折成「已是最新」**
//     （§0925EVE-W3-E「读不到不装已知」；清单读不出时静默显示"正常"是最贵的假绿）；
//  ② 期望段没填齐、或已是 pending ⇒ 按钮禁用（不造永远等不到答复的死格、不重复刷 requested_at）；
//  ③ 点击成功只报「已置为待更新」，不报「已更新成功」——落盘在物理机那一侧，
//     把 200 渲染成装好就是典型的「声称已做」；
//  ④ 装的≠跑的（installed_matches_desired=false）必须点名，不许混在同一句状态里。
// English: locks for the carrier manifest panel; unknown never reads as up-to-date,
// a successful click means queued-for-the-box, never installed.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/react'
import CarrierUpdatePanel, { carrierStateText } from '../components/CarrierUpdatePanel.jsx'

vi.mock('../api/index.js', () => ({ requestCarrierUpdate: vi.fn() }))
import { requestCarrierUpdate } from '../api/index.js'

const READY = {
  known: true,
  desired_ready: true,
  request_state: 'idle',
  strategy_version: 'v02-20261104',
  installed: { strategy_version: 'v01-20261103' },
  installed_matches_desired: false,
  last_ack: null,
}

beforeEach(() => {
  vi.clearAllMocks()
  requestCarrierUpdate.mockResolvedValue({ ok: true })
})
afterEach(() => cleanup())

describe('carrierStateText 五档分形', () => {
  it('unknown / known=false 一律读数未知，绝不出现「已是最新」', () => {
    expect(carrierStateText('unknown', true)).toContain('读数未知')
    expect(carrierStateText('idle', false)).toContain('读数未知')
    expect(carrierStateText('unknown', true)).not.toContain('已是最新')
  })

  it('pending / acked / failed 各指各的处置方向，不是同一句话', () => {
    expect(carrierStateText('pending', true)).toContain('等物理机')
    expect(carrierStateText('acked', true)).toContain('盒子答话')
    expect(carrierStateText('failed', true)).toContain('更新失败')
    expect(new Set(['pending', 'acked', 'failed'].map(s => carrierStateText(s, true))).size).toBe(3)
  })
})

describe('CarrierUpdatePanel 渲染与触发', () => {
  it('没有读数：显示读数未知、按钮禁用、一次请求都不发', () => {
    render(<CarrierUpdatePanel carrier={null} />)
    expect(screen.getByTestId('carrier-panel').textContent).toContain('读数未知')
    expect(screen.getByTestId('carrier-panel').textContent).not.toContain('已是最新')
    expect(screen.getByRole('button').disabled).toBe(true)
    expect(requestCarrierUpdate).not.toHaveBeenCalled()
  })

  it('读数齐备：点击只发一次请求，文案是「已置为待更新」而不是「已更新成功」', async () => {
    render(<CarrierUpdatePanel carrier={READY} />)
    fireEvent.click(screen.getByRole('button'))
    await waitFor(() => expect(requestCarrierUpdate).toHaveBeenCalledTimes(1))
    await waitFor(() => expect(screen.getByTestId('carrier-msg').textContent).toContain('已置为待更新'))
    expect(screen.getByTestId('carrier-msg').textContent).not.toContain('已更新成功')
    expect(screen.getByTestId('carrier-want').textContent).toContain('v02-20261104')
  })

  it('装的≠跑的：单独点名，不与状态文案混在一起', () => {
    render(<CarrierUpdatePanel carrier={READY} />)
    expect(screen.getByTestId('carrier-mismatch').textContent).toContain('装的不等于跑的')
  })

  it('期望段没填齐 / 已 pending：按钮禁用，不制造死格也不重复点击', () => {
    const { unmount } = render(<CarrierUpdatePanel carrier={{ ...READY, desired_ready: false }} />)
    expect(screen.getByRole('button').disabled).toBe(true)
    unmount()
    render(<CarrierUpdatePanel carrier={{ ...READY, request_state: 'pending' }} />)
    expect(screen.getByRole('button').disabled).toBe(true)
    expect(requestCarrierUpdate).not.toHaveBeenCalled()
  })

  it('请求失败：显式失败文案，绝不落在成功那一档', async () => {
    requestCarrierUpdate.mockRejectedValueOnce(new Error('403 无权限'))
    render(<CarrierUpdatePanel carrier={READY} />)
    fireEvent.click(screen.getByRole('button'))
    await waitFor(() => expect(screen.getByTestId('carrier-msg').textContent).toContain('触发失败'))
    expect(screen.getByTestId('carrier-msg').textContent).not.toContain('已置为待更新')
  })
})
