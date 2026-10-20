// §N-09 usePolling：隐藏暂停、恢复立即补一次、卸载清理。
import { render } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { usePolling } from '../utils/usePolling.js'

function Probe({ fn, enabled = true }) {
  usePolling(fn, 60000, enabled)
  return null
}

describe('§N-09 usePolling', () => {
  beforeEach(() => { vi.useFakeTimers() })
  afterEach(() => {
    vi.useRealTimers()
    Object.defineProperty(document, 'hidden', { configurable: true, value: false })
  })

  it('hidden 跨两个周期不再触发；visible 立即补一次后恢复周期', () => {
    const fn = vi.fn()
    const { unmount } = render(<Probe fn={fn} />)
    // 正常周期触发
    vi.advanceTimersByTime(60000)
    expect(fn).toHaveBeenCalledTimes(1)
    // 切后台：两个周期 0 次
    Object.defineProperty(document, 'hidden', { configurable: true, value: true })
    document.dispatchEvent(new Event('visibilitychange'))
    vi.advanceTimersByTime(120000)
    expect(fn).toHaveBeenCalledTimes(1)
    // 恢复可见：立即补一次 + 恢复周期
    Object.defineProperty(document, 'hidden', { configurable: true, value: false })
    document.dispatchEvent(new Event('visibilitychange'))
    expect(fn).toHaveBeenCalledTimes(2)
    vi.advanceTimersByTime(60000)
    expect(fn).toHaveBeenCalledTimes(3)
    unmount()
  })

  it('enabled=false 不建定时器；卸载后不再触发', () => {
    const fn = vi.fn()
    const { unmount } = render(<Probe fn={fn} enabled={false} />)
    vi.advanceTimersByTime(180000)
    expect(fn).not.toHaveBeenCalled()
    unmount()
  })
})
