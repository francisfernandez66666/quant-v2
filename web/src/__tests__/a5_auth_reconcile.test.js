// ── §A5（20260918 审计批）权限对账契约单测 ──
// 锁两件事：① request() 非 2xx 抛错必须携带 HTTP status（页面级 403→/403 重路由的判定依据）；
// ② isForbidden() 只对后端 403 为真、对网络层错误（无 status）为假；③ refreshMe() 把
// 服务端权威角色/权限回写 localStorage（ProtectedRoute 强对账门的数据源）。
// English: §A5 unit locks — error status propagation, isForbidden semantics, refreshMe reconciliation.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { request, isForbidden, refreshMe } from '../api/index.js'

// 构造最小 Response 桩：fetch 全 mock，不外发真实请求
function jsonResponse(status, body) {
  return { ok: status >= 200 && status < 300, status, json: async () => body }
}

describe('§A5 request() 错误状态码透出', () => {
  const originalFetch = global.fetch
  beforeEach(() => { localStorage.clear() })
  afterEach(() => { global.fetch = originalFetch })

  it('非 2xx 抛错带 status=403，isForbidden 判真', async () => {
    global.fetch = vi.fn(async () => jsonResponse(403, { error: '无权限' }))
    const e = await request('/api/admin/users').then(() => null, (err) => err)
    expect(e).toBeInstanceOf(Error)
    expect(e.message).toBe('无权限')
    expect(e.status).toBe(403)
    expect(isForbidden(e)).toBe(true)
  })

  it('401 抛错带 status=401（登录过期分支）', async () => {
    localStorage.setItem('liangzai_token', 'tok')
    global.fetch = vi.fn(async () => jsonResponse(401, { error: 'expired' }))
    const e = await request('/api/auth/me').then(() => null, (err) => err)
    expect(e.status).toBe(401)
    expect(e.message).toBe('登录已过期')
    expect(isForbidden(e)).toBe(false)
    expect(localStorage.getItem('liangzai_token')).toBeNull() // 401 已 clearAuth
  })

  it('网络层错误无 status，isForbidden 恒假', () => {
    expect(isForbidden(new Error('请求超时'))).toBe(false)
    expect(isForbidden(null)).toBe(false)
    expect(isForbidden(undefined)).toBe(false)
  })

  it('refreshMe 用服务端权威角色覆写本地缓存（含降权方向）', async () => {
    localStorage.setItem('liangzai_token', 'tok')
    localStorage.setItem('liangzai_role', 'admin') // 篡改过的陈旧本地角色
    global.fetch = vi.fn(async () => jsonResponse(200, { id: 'u1', username: 'tester', role: 'user', perms: [] }))
    const me = await refreshMe()
    expect(me.role).toBe('user')
    expect(localStorage.getItem('liangzai_role')).toBe('user')
    expect(localStorage.getItem('liangzai_perms')).toBe('[]')
  })
})
