// ── §H6（2026-09-22 修复批）+ §M5（PM 批）SSE 断线续传回归用例 ──
// 锁死行为：断流回调（onerror）触发时，只要 EventSource 还没被判 CLOSED（浏览器正在原生
// 自动重连、唯一能携带 Last-Event-ID 请求头的通道），前端不得手动 close+新建打断它；
// 仅当实例 CLOSED（典型：断流超过票据 60s TTL 后重连 401）才走「换票 + 退避重建」兜底，
// 且 §M5 起重建 URL 必须带 ?last_event_id=<最后收到的事件序号>——新 EventSource 附加不了
// 请求头，query 是这条路径唯一可达的补发载体（服务端 handlers_fix.go 头缺位时收 query）。
// English: §H6 — native reconnect must not be interrupted; §M5 — the fatal-CLOSED rebuild must
// carry the resume position as ?last_event_id= so the server-side replay ring stays reachable.
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'

describe('api - SSE 断流不再手动打断原生重连（§H6）', () => {
  /** 所有被 new 出来的 EventSource 替身实例，按建链顺序排列 */
  let instances = []
  let ticketSeq = 0

  // EventSource 替身：记录实例顺序与 close 调用；readyState 由用例显式摆位
  //（0=CONNECTING 浏览器原生重连中 / 1=OPEN / 2=CLOSED 彻底放弃）。
  class FakeEventSource {
    constructor(url) {
      this.url = url
      this.readyState = 1 // 建链成功态
      this.onmessage = null
      this.onerror = null
      this.close = vi.fn(() => { this.readyState = 2 })
      instances.push(this)
    }
  }

  // fetch 替身：/api/events/ticket 每次签发票据（tk1/tk2/…），其余端点返回空 JSON
  function mockFetch() {
    global.fetch = vi.fn(async (url) => {
      if (String(url).includes('/api/events/ticket')) {
        ticketSeq += 1
        return { ok: true, status: 200, json: async () => ({ ticket: 'tk' + ticketSeq, expires_in: 60 }) }
      }
      return { ok: true, status: 200, json: async () => ({}) }
    })
  }

  function fetchCallsTo(fragment) {
    return global.fetch.mock.calls.filter(c => String(c[0]).includes(fragment)).length
  }

  beforeEach(() => {
    vi.resetModules()          // 每个用例拿全新的模块闭包（sse/sseRetry/sseReconnectTimer 复位）
    instances = []
    ticketSeq = 0
    localStorage.clear()
    localStorage.setItem('liangzai_token', 'test-token')
    vi.stubGlobal('EventSource', FakeEventSource)
    vi.useFakeTimers()
    mockFetch()
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })

  it('onerror 处于 CONNECTING（原生重连中）：不 close、不重建，把 Last-Event-ID 通道留给浏览器', async () => {
    const mod = await import('../api/index.js')
    await mod.connectSSE()
    expect(instances.length).toBe(1)
    const es1 = instances[0]

    // 收到正常消息：回调分发 + 重连计数复位
    let got = null
    mod.onSSE(m => { got = m })
    es1.onmessage({ data: JSON.stringify({ type: 'scan', bull: '2' }) })
    expect(got && got.type).toBe('scan')

    // 断流：真实浏览器里 EventSource 进入 CONNECTING 自行退避重连（自动带 Last-Event-ID 头）
    es1.readyState = 0
    es1.onerror(new Event('error'))

    // §H6 核心断言：旧实现在此刻已 disconnectSSE()+setTimeout 重建；现在必须完全不触碰
    expect(es1.close).not.toHaveBeenCalled()
    await vi.advanceTimersByTimeAsync(60000) // 远超旧实现 3s 首跳重连窗口
    expect(instances.length).toBe(1)         // 没有手动新建实例打断原生通道
    expect(fetchCallsTo('/api/events/ticket')).toBe(1) // 也没有提前换票
  })

  it('实例 CLOSED 后：才走兜底重建，换新票据且带 ?last_event_id= 续传（§M5）', async () => {
    const mod = await import('../api/index.js')
    await mod.connectSSE()
    const es1 = instances[0]
    expect(es1.url).not.toContain('last_event_id=') // 首连没有历史序号，不带续传参数

    // 先正常收到一条带序号的事件（服务端 `id:` 行 → e.lastEventId）
    es1.onmessage({ data: JSON.stringify({ type: 'score' }), lastEventId: '42' })

    // 断流超过票据 TTL 后重连 401 → 浏览器彻底放弃（CLOSED）→ 触发兜底重建路径
    es1.readyState = 2
    es1.onerror(new Event('error'))
    expect(es1.close).toHaveBeenCalled() // 回收旧实例引用（disconnectSSE）
    expect(instances.length).toBe(1)     // 但不是立即新建，须先过退避

    await vi.advanceTimersByTimeAsync(2999)
    expect(instances.length).toBe(1)     // 3s 退避未到
    await vi.advanceTimersByTimeAsync(2)
    expect(instances.length).toBe(2)     // 退避到点，兜底重建
    expect(instances[1].url).toContain('ticket=tk2') // 重建必换新票（旧票可能已过期）
    expect(instances[1].url).not.toBe(instances[0].url)
    // §M5 核心断言：手动重建附加不了 Last-Event-ID 请求头，续读位置必须以 query 携带，
    // 否则服务端补发环对这条路径不可达、断流期间错过的事件永久丢失（旧行为）。
    expect(instances[1].url).toContain('last_event_id=42')
  })

  it('连续 5 次重连失败探测一次 /api/status 登录态（保留 §P1-9 过期 token 终止语义）', async () => {
    const mod = await import('../api/index.js')
    await mod.connectSSE()
    const es1 = instances[0]

    es1.onmessage({ data: JSON.stringify({ type: 'score' }) }) // 计数复位到 0
    for (let i = 0; i < 5; i++) {
      es1.readyState = 0 // 每轮都模拟「浏览器原生重连中再断」
      es1.onerror(new Event('error'))
    }
    expect(fetchCallsTo('/api/status')).toBe(1)  // 恰好第 5 次触发一次探测
    expect(es1.close).not.toHaveBeenCalled()     // 探测不拆原生重连通道
    expect(instances.length).toBe(1)
  })

  it('主动 disconnectSSE 清掉待触发的兜底重建定时器（登出后旧定时器不得复活连接）', async () => {
    const mod = await import('../api/index.js')
    await mod.connectSSE()
    const es1 = instances[0]
    es1.readyState = 2
    es1.onerror(new Event('error'))  // 进入 CLOSED 兜底：已挂 3s 重建定时器
    expect(instances.length).toBe(1)

    mod.disconnectSSE()              // 此刻用户登出/卸载
    await vi.advanceTimersByTimeAsync(10000)
    expect(instances.length).toBe(1) // §H6：定时器被 disconnectSSE 一并清除，连接未复活
  })
})
