// §N-10 请求层：GET 去重、5xx/网络单次重试、POST 永不重试、外部取消。
import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest'
import { request } from '../api/index.js'

const jsonRes = (body, status = 200) => ({
  ok: status >= 200 && status < 300,
  status,
  json: async () => body,
})

describe('§N-10 request 层', () => {
  beforeEach(() => {
    localStorage.clear()
    vi.restoreAllMocks()
  })
  afterEach(() => {
    vi.restoreAllMocks()
    delete global.fetch
  })

  it('并发相同 GET 只发一条网络请求', async () => {
    let release
    const gate = new Promise((r) => { release = r })
    global.fetch = vi.fn(async () => { await gate; return jsonRes({ ok: true }) })
    const p1 = request('/api/x')
    const p2 = request('/api/x')
    release()
    await Promise.all([p1, p2])
    expect(global.fetch).toHaveBeenCalledTimes(1)
  })

  it('去重键含 token：跨账号并发不会共享响应', async () => {
    global.fetch = vi.fn(async () => jsonRes({ ok: true }))
    localStorage.setItem('liangzai_token', 'A')
    const p1 = request('/api/x')
    localStorage.setItem('liangzai_token', 'B')
    const p2 = request('/api/x')
    await Promise.all([p1, p2])
    expect(global.fetch).toHaveBeenCalledTimes(2)
  })

  it('GET 502 重试一次后成功；POST 502 不重试', async () => {
    global.fetch = vi.fn()
      .mockResolvedValueOnce(jsonRes({ error: 'bad gateway' }, 502))
      .mockResolvedValueOnce(jsonRes({ ok: true }))
    await expect(request('/api/get')).resolves.toEqual({ ok: true })
    expect(global.fetch).toHaveBeenCalledTimes(2)

    global.fetch.mockClear()
    global.fetch.mockResolvedValue(jsonRes({ error: 'bad gateway' }, 502))
    await expect(request('/api/post', { method: 'POST', data: {} })).rejects.toMatchObject({ status: 502 })
    expect(global.fetch).toHaveBeenCalledTimes(1)
  })

  it('外部 AbortSignal 取消后抛 AbortError，且不共享去重 promise', async () => {
    global.fetch = vi.fn((url, opts) => new Promise((_, reject) => {
      opts.signal.addEventListener('abort', () => {
        const e = new Error('aborted')
        e.name = 'AbortError'
        reject(e)
      })
    }))
    const ctl = new AbortController()
    const p = request('/api/slow', { signal: ctl.signal })
    ctl.abort()
    await expect(p).rejects.toMatchObject({ name: 'AbortError' })
  })
})
