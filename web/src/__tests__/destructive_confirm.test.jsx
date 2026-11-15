// §F2-20261114 / §2 验收：所有破坏性前端 api 调用必须显式携带 confirm 令牌（或幂等 client_id），
// 否则后端会 400/拒绝。本用例以运行时方式锁定该契约——与 scripts/verify_changes.sh §184 的静态锁互补：
// 静态锁防「接线被删」，本用例防「接线被改成空值/错误令牌」。
// English: §F2 destructive-confirm runtime contract. Mirrors the §184 static grep-lock but at runtime,
// so a silently-emptied or mistyped confirm token still fails here.
import { describe, it, expect, vi, beforeEach } from 'vitest'

const calls = []
let fetchMock

beforeEach(() => {
  calls.length = 0
  fetchMock = vi.fn(async (_url, opts = {}) => {
    calls.push({ url: String(_url), opts })
    return {
      ok: true,
      status: 200,
      json: async () => ({ ok: true }),
      text: async () => 'ok',
    }
  })
  // request() 最终调用全局 fetch，拦截它即可捕获真实发出的 URL/method/body
  globalThis.fetch = fetchMock
})

// 真实 api 模块（其内部 request() 透传到全局 fetch，故被捕获取代）
import {
  clearAlerts,
  resetPaperPool,
  resetPaper,
  clearConsultHistory,
  reanalyzeNews,
} from '../api/index.js'

describe('破坏性前端 api 必须显式带 confirm 令牌', () => {
  it('clearAlerts 带 CLEAR_ALERTS query 且为 DELETE', async () => {
    await clearAlerts()
    expect(calls).toHaveLength(1)
    expect(calls[0].url).toContain('/api/alerts')
    expect(calls[0].url).toContain('confirm=CLEAR_ALERTS')
    expect(calls[0].opts.method).toBe('DELETE')
  })

  it('resetPaperPool 带 RESET_POOL', async () => {
    await resetPaperPool('alpha')
    expect(calls[0].url).toBe('/api/paper/pool/reset')
    expect(calls[0].opts.method).toBe('POST')
    const body = JSON.parse(calls[0].opts.body)
    expect(body.confirm).toBe('RESET_POOL')
    expect(body.pool).toBe('alpha')
  })

  it('resetPaper 两分支都带 RESET_PAPER', async () => {
    await resetPaper(0, 0)
    expect(calls[0].url).toBe('/api/paper/reset')
    expect(calls[0].opts.method).toBe('POST')
    const body = JSON.parse(calls[0].opts.body)
    expect(body.confirm).toBe('RESET_PAPER')
  })

  it('clearConsultHistory 带 CLEAR_CONSULT_HISTORY query 且为 DELETE', async () => {
    await clearConsultHistory()
    expect(calls[0].url).toContain('/api/consult/history')
    expect(calls[0].url).toContain('confirm=CLEAR_CONSULT_HISTORY')
    expect(calls[0].opts.method).toBe('DELETE')
  })

  it('reanalyzeNews 带幂等 client_id', async () => {
    await reanalyzeNews()
    expect(calls[0].url).toContain('/api/news/reanalyze')
    expect(calls[0].url).toContain('client_id=')
    expect(calls[0].opts.method).toBe('POST')
  })
})
