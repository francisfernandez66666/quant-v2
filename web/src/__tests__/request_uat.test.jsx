// ── §P2-15（2026-09-15）request() 四分支单测 ──
// 覆盖 web/src/api/index.js 的 request() 封装错误路径（此前仅覆盖认证辅助，网络分支零测试）：
//   1) 401 → clearAuth + 广播 auth:expired + 抛"登录已过期"
//   2) 超时 → AbortError 转译为"请求超时"
//   3) 自定义服务器网络失败 → 回退同源重试成功
//   4) 非 2xx JSON 错误体 {error} → 透传后端错误文案；非 JSON 体 → 状态码提示
//   5) executeRealAction 自动补 client_id 幂等键
// English: unit tests for the request() wrapper's error branches plus the execute client_id guard.
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import * as api from '../api/index.js'

const ok = (body) => new Response(JSON.stringify(body), { status: 200 })

describe('api - request() 错误分支（§P2-15）', () => {
  beforeEach(() => {
    localStorage.clear()
  })
  afterEach(() => {
    vi.unstubAllGlobals()
    vi.restoreAllMocks()
  })

  it('401 清除登录态并广播 auth:expired', async () => {
    localStorage.setItem('liangzai_token', 'tok')
    const fired = vi.fn()
    window.addEventListener('auth:expired', fired)
    vi.stubGlobal('fetch', vi.fn(async () => new Response('{}', { status: 401 })))
    await expect(api.request('/api/status')).rejects.toThrow('登录已过期')
    expect(api.isLoggedIn()).toBe(false)
    expect(fired).toHaveBeenCalledTimes(1)
    window.removeEventListener('auth:expired', fired)
  })

  it('超时中止转译为「请求超时」', async () => {
    vi.stubGlobal('fetch', vi.fn((_url, init) => new Promise((_resolve, reject) => {
      init.signal.addEventListener('abort', () => {
        const e = new Error('aborted'); e.name = 'AbortError'; reject(e)
      })
    })))
    await expect(api.request('/api/slow', { timeout: 20 })).rejects.toThrow('请求超时')
  })

  it('自定义服务器不可达时回退同源重试', async () => {
    localStorage.setItem('liangzai_server_url', 'http://dead.example:9')
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    // 第一次（自定义地址）网络层失败，第二次（同源）成功
    const f = vi.fn()
      .mockRejectedValueOnce(new TypeError('fetch failed'))
      .mockResolvedValueOnce(ok({ ok: 1 }))
    vi.stubGlobal('fetch', f)
    const r = await api.request('/api/x')
    expect(r).toEqual({ ok: 1 })
    expect(f).toHaveBeenCalledTimes(2)
    expect(f.mock.calls[1][0]).toBe('/api/x')
    warn.mockRestore()
  })

  it('非 2xx 透传后端 error 文案；非 JSON 错误体回退状态码提示', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ error: '余额不足' }), { status: 409 })))
    await expect(api.request('/api/buy')).rejects.toThrow('余额不足')
    vi.stubGlobal('fetch', vi.fn(async () => new Response('<html>502</html>', { status: 502 })))
    await expect(api.request('/api/x')).rejects.toThrow('请求失败 502')
  })

  it('executeRealAction 自动补 client_id 幂等键（显式传入时不覆盖）', async () => {
    const f = vi.fn(async (_url, init) => {
      const body = JSON.parse(init.body)
      expect(body.client_id).toBeTruthy()
      return ok({ ok: true })
    })
    vi.stubGlobal('fetch', f)
    await api.executeRealAction({ code: '600519.SH', side: '买入', action: 'buy', qty: 100 })
    const cid1 = JSON.parse(f.mock.calls[0][1].body).client_id
    expect(cid1).toMatch(/^[A-Za-z0-9_-]{1,64}$/)
    await api.executeRealAction({ code: '600519.SH', side: '买入', action: 'buy', qty: 100, client_id: 'my-id' })
    expect(JSON.parse(f.mock.calls[1][1].body).client_id).toBe('my-id')
  })
})
