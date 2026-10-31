// strict_perm_api.test.js — §MULTI-ADMIN：hasStrictPerm 只认显式权限位，admin 不隐式放行。
import { describe, it, expect, beforeEach } from 'vitest'
import { hasPerm, hasStrictPerm } from '../api/index.js'

describe('§MULTI-ADMIN 前端权限判定', () => {
  beforeEach(() => {
    localStorage.clear()
    localStorage.setItem('liangzai_role', 'admin')
    localStorage.setItem('liangzai_perms', JSON.stringify([]))
  })

  it('admin 无显式 bit：hasPerm 兼容为 true，hasStrictPerm 必须 false', () => {
    expect(hasPerm('funds_admin')).toBe(true)
    expect(hasStrictPerm('funds_admin')).toBe(false)
  })

  it('显式授予后 hasStrictPerm 才为 true', () => {
    localStorage.setItem('liangzai_perms', JSON.stringify(['funds_admin']))
    expect(hasStrictPerm('funds_admin')).toBe(true)
    expect(hasStrictPerm('trading_admin')).toBe(false)
  })
})
