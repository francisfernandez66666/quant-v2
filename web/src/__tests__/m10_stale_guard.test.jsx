// ── §M-10（FIX_PLAN_20260922PM §三 M-10 行，2026-09-22 PM 批清扫）轮询「请求代号 + 后到丢弃」──
//
// 缺陷原文：Dashboard/Signals/Positions 周期轮询（10s/15s、20s、60s）无请求序列守卫，
// 慢响应可覆盖新数据（数据倒挂）；api 层 AbortController 只管超时不排序轮次。
// 用例：U1~U3 守卫工具语义（代号单调/最新胜出/旧代号判污）；
//       P1 Signals 交错轮询行为锁——第 1 轮慢响应后到时，第 2 轮新数据不得被覆盖。
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, cleanup, act } from '@testing-library/react'
import { createStaleGuard } from '../utils/staleGuard.js'

const { state } = vi.hoisted(() => ({ state: { impl: {} } }))

function okPayloads() {
  return {
    isAdmin: () => false,
    getAccount: () => 'admin',
    fetchStatus: () => ({ session: 'test', signal_count: 0 }),
    setLastSession: () => {},
    connectSSE: () => {},
    fetchSignals: () => [],
    fetchPaperState: () => ({ enabled: false }),
    fetchShortStatus: () => ({ short_enabled: false }),
    fetchAlerts: () => [],
  }
}

vi.mock('../api/index.js', async () => {
  const actual = await vi.importActual('../api/index.js')
  const stubs = {}
  for (const name of Object.keys(okPayloads())) {
    stubs[name] = vi.fn(async (...args) => state.impl[name](...args))
  }
  return { ...actual, ...stubs }
})

import Signals from '../pages/Signals.jsx'

describe('§M-10 轮询请求代号守卫', () => {
  // U1 begin 单调递增，各自独立实例互不串扰
  it('U1 begin 递增且新实例独立计数', () => {
    const a = createStaleGuard()
    const b = createStaleGuard()
    expect(a.begin()).toBe(1)
    expect(a.begin()).toBe(2)
    expect(b.begin()).toBe(1)
  })

  // U2 最新代号 isStale=false；被超越的旧代号=true
  it('U2 旧代号判污、最新代号放行', () => {
    const g = createStaleGuard()
    const t1 = g.begin()
    const t2 = g.begin()
    expect(g.isStale(t1)).toBe(true)
    expect(g.isStale(t2)).toBe(false)
  })

  // U3 交错场景语义模拟：慢请求（代号早）后完成必须被丢弃——守卫本身即该规则的全部逻辑
  it('U3 慢响应后到时按代号丢弃（规则语义）', async () => {
    const g = createStaleGuard()
    let latest = null
    const slow = (async () => {
      const t = g.begin() // 第 1 轮
      await new Promise((r) => setTimeout(r, 20))
      return { token: t, value: 'OLD' }
    })()
    const fast = (async () => {
      await new Promise((r) => setTimeout(r, 1))
      const t = g.begin() // 第 2 轮（更晚发起、更快完成）
      latest = t
      return { token: t, value: 'NEW' }
    })()
    const [s, f] = await Promise.all([slow, fast])
    if (!g.isStale(s.token)) latest = s.value // 旧代码路径绝不允许写 state
    if (!g.isStale(f.token)) latest = f.value
    expect(latest).toBe('NEW')
  })

  describe('P1 Signals 页交错轮询行为锁', () => {
    beforeEach(async () => {
      cleanup()
      localStorage.clear()
      state.impl = okPayloads()
      const api = await import('../api/index.js')
      for (const name of Object.keys(state.impl)) {
        if (typeof api[name]?.mockClear === 'function') api[name].mockClear()
      }
    })
    afterEach(() => {
      cleanup()
      vi.useRealTimers()
    })

    it('P1 第 1 轮慢响应后到，不得覆盖第 2 轮新数据', async () => {
      vi.useFakeTimers()
      let resolveSlow
      let calls = 0
      state.impl.fetchSignals = () => {
        calls += 1
        if (calls === 1) return new Promise((r) => { resolveSlow = r }) // 挂载首轮：挂起
        return Promise.resolve([{ code: '600001', name: '新轮信号', direction: '做多', price: 10 }])
      }
      render(<Signals />)
      await act(async () => { vi.advanceTimersByTime(21000) }) // 20s 轮询触发第 2 轮（快，微任务内即结算）
      // 第 2 轮数据已渲染（fake timers 下不用 findBy：waitFor 依赖真实计时器会挂死）
      expect(screen.getByText('新轮信号')).toBeInTheDocument()
      // 第 1 轮此刻才返回旧数据 → 守卫必须整包丢弃
      await act(async () => { resolveSlow([{ code: '600002', name: '旧轮信号', direction: '做多', price: 10 }]) })
      expect(screen.queryByText('旧轮信号'), '§M-10：迟到的旧轮响应不得覆盖新数据').not.toBeInTheDocument()
      expect(screen.getByText('新轮信号')).toBeInTheDocument()
    })
  })
})
