import { describe, it, expect } from 'vitest'
import { shouldShowPaperTab } from '../utils/paperNav.js'

// §PAPER-CFG-DESYNC：管理员关掉模拟盘后仍必须看到入口（开关在本页设置里），
// 普通用户只在启用时看到。两条判据分别锁死两个方向。
describe('模拟盘菜单可见性', () => {
  it('管理员在模拟盘关闭时仍可见入口', () => {
    expect(shouldShowPaperTab(false, true)).toBe(true)
  })

  it('普通用户在模拟盘关闭时不可见入口', () => {
    expect(shouldShowPaperTab(false, false)).toBe(false)
  })

  it('普通用户在模拟盘启用时可见入口', () => {
    expect(shouldShowPaperTab(true, false)).toBe(true)
  })
})
