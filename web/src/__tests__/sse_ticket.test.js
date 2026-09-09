// ── WS-F C4a SSE 一次性票据流程单元测试 ──
// 覆盖 web/src/api/index.js 的 connectSSE 变更：先 POST /api/events/ticket 取 60s 一次性票据，
// 再用 /api/events?ticket=<tk> 建链（URL 不再携带长期 token）；票据失败走 auth 探测。
// 通过 mock EventSource 与全局 fetch 验证建链 URL 与鉴权头。
import { describe, it, expect, beforeEach, vi } from 'vitest'
import * as api from '../api/index.js'

describe('api - SSE 一次性票据（§WS-F C4a）', () => {
  let openedUrl = ''

  class FakeEventSource {
    constructor(url) {
      openedUrl = url
      this.url = url
      this.onmessage = null
      this.onerror = null
      this.close = vi.fn()
    }
  }

  function mockFetchTicketOk(ticket) {
    global.fetch = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ ticket, expires_in: 60 }),
    })
  }

  beforeEach(() => {
    openedUrl = ''
    localStorage.clear()
    vi.restoreAllMocks()
    vi.stubGlobal('EventSource', FakeEventSource)
    vi.stubGlobal('fetch', undefined)
    // 重置模块内 sse 连接态（闭包变量）：重新动态 import 拿新实例
    vi.resetModules()
  })

  it('登录后 connectSSE 先取票再以 ticket 建链（URL 无长期 token）', async () => {
    localStorage.setItem('liangzai_token', 'test-token')
    mockFetchTicketOk('tk_abc123')
    const mod = await import('../api/index.js')
    await mod.connectSSE()

    // 第一步：POST /api/events/ticket 取票
    const [ticketUrl, opts] = global.fetch.mock.calls[0]
    expect(ticketUrl.endsWith('/api/events/ticket')).toBe(true)
    expect((opts && opts.method) || 'GET').toBe('POST')
    expect(opts.headers.Authorization).toBe('Bearer test-token')

    // 第二步：EventSource 以 ticket 建链，URL 不含 token
    expect(openedUrl).toContain('/api/events?ticket=tk_abc123')
    expect(openedUrl).not.toContain('token=')
  })

  it('未登录不建链（connectSSE 直接返回）', async () => {
    const mod = await import('../api/index.js')
    await mod.connectSSE()
    expect(openedUrl).toBe('')
    expect(global.fetch).toBeUndefined()
  })

  it('取票失败（如 token 失效）触发 auth 探测而非建链', async () => {
    localStorage.setItem('liangzai_token', 'expired-token')
    global.fetch = vi.fn().mockResolvedValue({
      ok: false,
      status: 401,
      json: async () => ({ error: '登录已过期' }),
    })
    const mod = await import('../api/index.js')
    await mod.connectSSE()
    // 票据请求失败后：不再打开 SSE，且发起 /api/status 探测
    expect(openedUrl).toBe('')
    expect(global.fetch).toHaveBeenCalled()
  })
})
