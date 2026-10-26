import { describe, it, expect, vi } from 'vitest'
import { createClientErrorReporter } from '../errorReporter.js'

function makeReporter({ token = 'tok', start = 1000 } = {}) {
  let t = start
  const calls = []
  const fetchImpl = vi.fn((url, opts) => { calls.push({ url, opts }); return Promise.resolve({ ok: true }) })
  const reporter = createClientErrorReporter({
    getToken: () => token,
    fetchImpl,
    now: () => t,
    build: 'b1',
    route: () => '/paper',
  })
  return { reporter, calls, advance: (ms) => { t += ms }, fetchImpl }
}

describe('client error reporter', () => {
  it('登录后同指纹 60s 内只发一次，载荷带 token/route/build', () => {
    const { reporter, calls, advance, fetchImpl } = makeReporter()
    const err = new Error('boom')
    reporter.report(err, { source: 'app.js', line: 1, col: 2 })
    reporter.report(err, { source: 'app.js', line: 1, col: 2 })
    expect(fetchImpl).toHaveBeenCalledTimes(1)
    expect(calls[0].url).toBe('/api/client-errors')
    expect(calls[0].opts.headers.Authorization).toBe('Bearer tok')
    const body = JSON.parse(calls[0].opts.body)
    expect(body.message).toBe('boom')
    expect(body.source).toBe('app.js')
    expect(body.build).toBe('b1')
    expect(body.route).toBe('/paper')
    advance(61_000)
    reporter.report(err, { source: 'app.js', line: 1, col: 2 })
    expect(fetchImpl).toHaveBeenCalledTimes(2)
  })

  it('未登录不发', () => {
    const { reporter, fetchImpl } = makeReporter({ token: '' })
    reporter.report(new Error('boom'))
    expect(fetchImpl).not.toHaveBeenCalled()
  })

  it('每分钟最多 10 条（防错误风暴）', () => {
    const { reporter, fetchImpl } = makeReporter()
    for (let i = 0; i < 15; i++) {
      reporter.report(new Error('boom-' + i))
    }
    expect(fetchImpl).toHaveBeenCalledTimes(10)
  })
})
