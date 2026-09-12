// ── §F5 sseBus 单元测试 ──
// 验证：按类型分发、'*' 通配、多类型订阅、取消订阅、异常隔离、非对象消息忽略。
// English: verifies type routing, '*' wildcard, multi-type subs, unsubscribe, error isolation, and
// that non-object messages are ignored.
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { on, dispatch, size, __reset } from '../sseBus.js'

describe('sseBus (§F5)', () => {
  beforeEach(() => __reset())

  it('按类型分发到对应订阅者', () => {
    const scan = vi.fn(), msg = vi.fn()
    on('scan', scan); on('message', msg)
    expect(dispatch({ type: 'scan', bull: 2 })).toBe(1)
    expect(scan).toHaveBeenCalledWith({ type: 'scan', bull: 2 })
    expect(msg).not.toHaveBeenCalled()
  })

  it("'*' 通配收所有类型", () => {
    const all = vi.fn()
    on('*', all)
    dispatch({ type: 'tick' }); dispatch({ type: 'score' })
    expect(all).toHaveBeenCalledTimes(2)
  })

  it('多类型订阅共享一个回调', () => {
    const fn = vi.fn()
    on(['scan', 'message'], fn)
    dispatch({ type: 'scan' }); dispatch({ type: 'message' }); dispatch({ type: 'tick' })
    expect(fn).toHaveBeenCalledTimes(2)
  })

  it('取消订阅后不再触发', () => {
    const fn = vi.fn()
    const off = on('scan', fn)
    dispatch({ type: 'scan' })
    off()
    dispatch({ type: 'scan' })
    expect(fn).toHaveBeenCalledTimes(1)
  })

  it('单个回调抛错不影响其余', () => {
    const bad = vi.fn(() => { throw new Error('x') })
    const good = vi.fn()
    on('scan', bad); on('scan', good)
    expect(() => dispatch({ type: 'scan' })).not.toThrow()
    expect(good).toHaveBeenCalled()
  })

  it('无 type 消息仅发通配；非对象忽略', () => {
    const wild = vi.fn()
    on('*', wild)
    expect(dispatch({ foo: 1 })).toBe(1)   // 无 type → 仅通配
    expect(dispatch(null)).toBe(0)
    expect(dispatch('str')).toBe(0)
    expect(wild).toHaveBeenCalledTimes(1)
  })

  it('size 统计已订阅类型数，清空后归零', () => {
    const off1 = on('scan', () => {})
    on('message', () => {})
    expect(size()).toBe(2)
    off1()
    __reset()
    expect(size()).toBe(0)
  })
})
