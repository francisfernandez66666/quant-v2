// ── §F7-20261114 /api/review/positions 前端超时（契约锁）──
//
// 事故形态：后端手动复盘实测 41.5s（全持仓 LLM 同步执行），前端 reviewPositions()
// 没传 timeout ⇒ 吃通用 10s 默认 ⇒ 浏览器 10s abort、后端还在跑——用户重试又叠一次。
// 修法（方案 A，只动前端这一个端点）：REVIEW_TIMEOUT_MS=120000（41.5s + 未来增长余量）。
// 这里锁三件事：① 常量 ≥ 后端实测；② 常量 > 通用默认（防手抖写成 10000）；
// ③ reviewPositions() 真的带上了它（历史上"只有 LLM 三端点加了长超时、复盘漏了"
// 正是本次故障）。邻座 llm_probe_timeout 不得被顺手改坏。
// English: locks that reviewPositions() carries a 120s timeout covering the backend's
// measured 41.5s review pass, instead of inheriting the generic 10s default.
import { describe, it, expect, afterEach, vi } from 'vitest'
import * as api from '../api/index.js'

// 后端实测读数（docs/UAT_FIX_PLAN_20261114.md §7）：一次复盘 41.5s。
const BACKEND_MEASURED_MS = 41500
const GENERIC_DEFAULT_MS = 10000

function captureTimeout() {
  const delays = []
  const orig = globalThis.setTimeout
  vi.stubGlobal('setTimeout', (fn, ms) => {
    delays.push(ms)
    return orig(fn, ms)
  })
  return delays
}

const okResp = () => new Response(JSON.stringify({ reviewed: 0 }), { status: 200 })

describe('api - 复盘端点超时（§F7-20261114）', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
    vi.restoreAllMocks()
  })

  it('REVIEW_TIMEOUT_MS 必须 ≥ 后端实测 41.5s', () => {
    expect(api.REVIEW_TIMEOUT_MS).toBeGreaterThanOrEqual(BACKEND_MEASURED_MS)
  })

  it('REVIEW_TIMEOUT_MS 必须大于通用默认 10s（防手抖写回默认值）', () => {
    expect(api.REVIEW_TIMEOUT_MS).toBeGreaterThan(GENERIC_DEFAULT_MS)
  })

  it('reviewPositions() 必须显式带长超时，不能吃通用 10s 默认', async () => {
    const f = vi.fn(async () => okResp())
    vi.stubGlobal('fetch', f)
    const delays = captureTimeout()

    await api.reviewPositions()

    expect(f).toHaveBeenCalledTimes(1)
    expect(delays).toContain(api.REVIEW_TIMEOUT_MS)
    expect(delays).not.toContain(GENERIC_DEFAULT_MS)
  })

  it('别改坏邻居：LLM 三端点的长超时契约仍绿', async () => {
    expect(api.LLM_PROBE_TIMEOUT_MS).toBeGreaterThanOrEqual(90000)
  })
})
