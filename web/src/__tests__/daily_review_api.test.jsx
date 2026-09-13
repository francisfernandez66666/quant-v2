// ── §DAILY_REVIEW 复盘 API 客户端形状测试 ──
// 直接走真实 api 模块 + 全局 fetch mock：验证 POST /api/review/positions 的 URL/方法，
// 以及非 2xx（如 502"复盘开关已关闭"）时按后端 error 字段抛出可读错误。
// 独立成文件：MsgCenter 挂载测试需要整页 vi.mock('../api/index.js')，模块 mock 对本文件所有
// import 生效会覆盖真实实现，故两类用例分开。
// English: client-shape tests for reviewPositions (URL/method/502-error), kept separate from the
// MsgCenter mount test because the page-level api module mock would override the real client here.
import { describe, it, expect, afterEach, vi } from 'vitest'
import * as api from '../api/index.js'

describe('§DAILY_REVIEW api.reviewPositions 请求形状', () => {
  afterEach(() => { vi.restoreAllMocks(); delete global.fetch })

  it('POST /api/review/positions 并回传 {reviewed}', async () => {
    global.fetch = vi.fn().mockResolvedValue({
      status: 200, ok: true, json: async () => ({ reviewed: 2 }),
    })
    const r = await api.reviewPositions()
    expect(r.reviewed).toBe(2)
    const [url, opts] = global.fetch.mock.calls[0]
    expect(String(url)).toContain('/api/review/positions')
    expect(opts.method).toBe('POST')
  })

  it('502（开关关闭/引擎报错）时抛出后端 error 文案', async () => {
    global.fetch = vi.fn().mockResolvedValue({
      status: 502, ok: false, json: async () => ({ error: '复盘开关已关闭' }),
    })
    await expect(api.reviewPositions()).rejects.toThrow('复盘开关已关闭')
  })
})
