// ── §F5 useSseRefresh 钩子测试 ──
// 用 fake timers 验证：事件总线到类型消息即回调、60s 兜底 interval 触发、卸载后停止订阅与计时。
// English: fake-timer tests for useSseRefresh — a matching bus event invokes the callback, the 60s
// fallback interval fires, and unmount stops both the subscription and the timer.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { renderHook } from '@testing-library/react'
import { dispatch, __reset } from '../sseBus.js'
import useSseRefresh from '../useSseRefresh.js'

describe('useSseRefresh (§F5)', () => {
  beforeEach(() => { __reset(); vi.useFakeTimers() })
  afterEach(() => { vi.useRealTimers() })

  it('匹配类型事件到达即刷新', () => {
    const cb = vi.fn()
    renderHook(() => useSseRefresh(['scan', 'message'], cb))
    dispatch({ type: 'scan' })
    expect(cb).toHaveBeenCalledTimes(1)
    dispatch({ type: 'tick' }) // 未订阅类型 → 不触发
    expect(cb).toHaveBeenCalledTimes(1)
  })

  it('60s 兜底 interval 触发', () => {
    const cb = vi.fn()
    renderHook(() => useSseRefresh(['scan'], cb, { intervalMs: 60000 }))
    vi.advanceTimersByTime(60000)
    expect(cb).toHaveBeenCalledTimes(1)
    vi.advanceTimersByTime(60000)
    expect(cb).toHaveBeenCalledTimes(2)
  })

  it('intervalMs<=0 关闭兜底轮询', () => {
    const cb = vi.fn()
    renderHook(() => useSseRefresh(['scan'], cb, { intervalMs: 0 }))
    vi.advanceTimersByTime(120000)
    expect(cb).not.toHaveBeenCalled()
  })

  it('卸载后停止订阅与计时', () => {
    const cb = vi.fn()
    const { unmount } = renderHook(() => useSseRefresh(['scan'], cb, { intervalMs: 60000 }))
    unmount()
    dispatch({ type: 'scan' })
    vi.advanceTimersByTime(120000)
    expect(cb).not.toHaveBeenCalled()
  })

  it('enabled=false 整体停用', () => {
    const cb = vi.fn()
    renderHook(() => useSseRefresh(['scan'], cb, { enabled: false }))
    dispatch({ type: 'scan' })
    vi.advanceTimersByTime(120000)
    expect(cb).not.toHaveBeenCalled()
  })

  it('回调取最新引用（闭包不过期）', () => {
    const stale = vi.fn()
    const { rerender } = renderHook(({ cb }) => useSseRefresh(['scan'], cb, { intervalMs: 0 }), { initialProps: { cb: stale } })
    const fresh = vi.fn()
    rerender({ cb: fresh })
    dispatch({ type: 'scan' })
    expect(fresh).toHaveBeenCalledTimes(1)
    expect(stale).not.toHaveBeenCalled()
  })
})
