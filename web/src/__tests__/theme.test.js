// ── §F4 主题模块单元测试 ──
// 验证 theme.js 的三大契约：initTheme 从 localStorage 恢复；setTheme 同步 DOM 属性/类 + 广播；
// toggle 浅/深互切；subscribe 回调收到最新主题。
// English: verifies the three contracts of theme.js: initTheme restores from localStorage;
// setTheme syncs the documentElement attribute/class + broadcasts; toggle flips; subscribers
// receive the latest theme.
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { THEME_KEY, initTheme, setTheme, getTheme, toggleTheme, subscribeTheme } from '../theme.js'

describe('theme (§F4 全站深色主题)', () => {
  beforeEach(() => {
    document.documentElement.removeAttribute('theme-mode')
    document.documentElement.classList.remove('dark')
    localStorage.clear()
  })
  it('默认浅色：initTheme 无存储时不设置 theme-mode', () => {
    const t = initTheme()
    expect(t).toBe('light')
    expect(document.documentElement.hasAttribute('theme-mode')).toBe(false)
  })
  it('setTheme(dark) 同时落 DOM 属性/类 + 持久化', () => {
    setTheme('dark')
    expect(document.documentElement.getAttribute('theme-mode')).toBe('dark')
    expect(document.documentElement.classList.contains('dark')).toBe(true)
    expect(localStorage.getItem(THEME_KEY)).toBe('dark')
    expect(getTheme()).toBe('dark')
  })
  it('setTheme(light) 移除属性/类', () => {
    setTheme('dark')
    setTheme('light')
    expect(document.documentElement.hasAttribute('theme-mode')).toBe(false)
    expect(document.documentElement.classList.contains('dark')).toBe(false)
  })
  it('toggleTheme 在浅/深之间往返', () => {
    setTheme('light')
    expect(toggleTheme()).toBe('dark')
    expect(toggleTheme()).toBe('light')
  })
  it('initTheme 恢复持久化主题', () => {
    localStorage.setItem(THEME_KEY, 'dark')
    expect(initTheme()).toBe('dark')
    expect(document.documentElement.getAttribute('theme-mode')).toBe('dark')
  })
  it('subscribeTheme 收到变更并返回可注销的取消函数', () => {
    const fn = vi.fn()
    const off = subscribeTheme(fn)
    setTheme('dark')
    expect(fn).toHaveBeenCalledWith('dark')
    off()
    setTheme('light')
    expect(fn).toHaveBeenCalledTimes(1) // 已注销不再触发
  })
})
