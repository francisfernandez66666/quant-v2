// 登录分码的前端话术回归（§FIX-20261028-F04，2026-10-29）。
//
// 缺陷本体：后端把"账号到期/停用"与"口令错"压成同一句 401 invalid credentials，
// 用户被锁死却看不到"该续期"。后端修成 403 分码之后，前端必须**同批**在
// LOGIN_ERR_TEXT 里登记这两条，否则新错误码会落到兜底句「登录失败，请稍后重试」——
// 等于后端改了、屏上还是看不懂。
//
// 本文件锁四件事：
//  1. account_expired ⇒ 「账号已过期，请联系管理员续期」（等值，不是正则）；
//  2. account_disabled ⇒ 「账号已停用，请联系管理员」；
//  3. invalid credentials 仍 ⇒ 「账号或密码不正确」（分码不得误伤口令错文案）；
//  4. 未知错误码 ⇒ 兜底句（不抛原始英文，不空白）。
//
// English: locks the login error wording for the new 403 codes and that the legacy 401 wording
// is unchanged (no regression on the anti-enumeration path).
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { login } from '../api/index.js'

// 每个用例自造一个最小 Response，避免真打网络。
function mockFetchOnce(status, body) {
  global.fetch = vi.fn(async () => ({
    ok: status >= 200 && status < 300,
    status,
    json: async () => body,
  }))
}

describe('登录失败话术（§FIX-20261028-F04）', () => {
  beforeEach(() => {
    localStorage.clear()
    vi.spyOn(console, 'warn').mockImplementation(() => {})
  })
  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('account_expired ⇒ 提示联系管理员续期（等值断言）', async () => {
    mockFetchOnce(403, { error: 'account_expired', message: '账号已过期，请联系管理员续期' })
    await expect(login('u', 'p')).rejects.toThrow('账号已过期，请联系管理员续期')
  })

  it('account_disabled ⇒ 提示账号已停用', async () => {
    mockFetchOnce(403, { error: 'account_disabled', message: '账号已停用，请联系管理员' })
    await expect(login('u', 'p')).rejects.toThrow('账号已停用，请联系管理员')
  })

  it('invalid credentials 仍 ⇒ 账号或密码不正确（分码不误伤口令错）', async () => {
    mockFetchOnce(401, { error: 'invalid credentials' })
    await expect(login('u', 'p')).rejects.toThrow('账号或密码不正确')
  })

  it('未知错误码 ⇒ 兜底句，不把英文原文抛给用户', async () => {
    mockFetchOnce(500, { error: 'something_new' })
    const err = await login('u', 'p').catch((e) => e)
    expect(err.message).toBe('登录失败，请稍后重试')
    expect(err.message).not.toContain('something_new')
  })
})
