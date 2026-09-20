// ── §2026-09-20 LLM 配置类接口的请求超时（前端侧契约） ──
//
// 事故形态：后端在 /api/config/llm/probe 里**真的出网逐把探测**候选密钥
// （单把 45s、并发 4 路、总预算 90s；生产实测一次 54~60s），前端却走通用默认 10s
// ⇒ 浏览器在 10s 就 abort，用户看到「探测失败: 请求超时」，而后端日志里那次探测
// 其实是成功的（probe=53975ms / 58214ms / 59421ms）。
//
// 这里锁两件事：
//   ① 常量必须覆盖后端总预算（改了后端预算就必须同步改前端这一侧）；
//   ② 三个会触发探测的接口（测试连接 / 保存 / 回滚）都真的带上了它 ——
//      历史上正是"只有 LLM 咨询加了长超时、这三个漏了"才出的这次故障。
// English: locks that the LLM-config endpoints carry a request timeout covering the backend probe
// budget (90s), instead of silently inheriting the generic 10s default.
import { describe, it, expect, afterEach, vi } from 'vitest'
import * as api from '../api/index.js'

// internal/llm/probe.go::ProbeMaxTotalBudget —— 后端一次探测的硬上界。
const BACKEND_PROBE_BUDGET_MS = 90000
// api/index.js::REQUEST_TIMEOUT —— 通用默认值（就是它导致本次故障）。
const GENERIC_DEFAULT_MS = 10000

// captureTimeout 记录 request() 内部设的超时值，同时保留原行为
// （成功路径会 clearTimeout，不会留下悬空计时器）。
function captureTimeout() {
  const delays = []
  const orig = globalThis.setTimeout
  vi.stubGlobal('setTimeout', (fn, ms) => {
    delays.push(ms)
    return orig(fn, ms)
  })
  return delays
}

const okResp = () => new Response(JSON.stringify({ status: 'ok', result: {} }), { status: 200 })

describe('api - LLM 配置接口超时（§2026-09-20）', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
    vi.restoreAllMocks()
  })

  it('导出的超时常量必须 ≥ 后端探测总预算', () => {
    expect(api.LLM_PROBE_TIMEOUT_MS).toBeGreaterThanOrEqual(BACKEND_PROBE_BUDGET_MS)
  })

  const cases = [
    ['测试连接 /api/config/llm/probe', () => api.probeLLMConfig({ api_url: 'u', model: 'm', api_key: 'k' })],
    ['保存 /api/config/llm', () => api.setLLMConfig({ api_url: 'u', model: 'm', api_key: 'k' })],
    ['回滚 /api/config/llm/rollback', () => api.rollbackLLMConfig()],
  ]

  for (const [name, call] of cases) {
    it(`${name} 必须显式带长超时，不能吃通用 10s 默认`, async () => {
      const f = vi.fn(async () => okResp())
      vi.stubGlobal('fetch', f)
      const delays = captureTimeout()

      await call()

      expect(f).toHaveBeenCalledTimes(1)
      expect(delays).toContain(api.LLM_PROBE_TIMEOUT_MS)
      expect(Math.max(...delays)).toBeGreaterThanOrEqual(BACKEND_PROBE_BUDGET_MS)
      // 负向断言：绝不能还是那个害死人的默认值
      expect(delays).not.toContain(GENERIC_DEFAULT_MS)
    })
  }

  it('反向护栏：普通接口仍用 10s 默认（别把整个 app 都拖成两分钟）', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => okResp()))
    const delays = captureTimeout()
    await api.fetchStrategyConfig()
    expect(delays).toEqual([GENERIC_DEFAULT_MS])
  })
})
