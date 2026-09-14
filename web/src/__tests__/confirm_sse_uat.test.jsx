// ── §P1-9 / §P1-10（2026-09-15）前端收口回归测试 ──
// 覆盖本轮两项前端修复：
//   1) 共享 confirmDialog 的 resolve-once 守卫（§P1-9）：TDesign 的 d.hide() 会同步触发
//      onClose——旧裸实现 onConfirm 先 hide 再 resolve(true) 时，onClose 的 resolve(false)
//      先一步生效，"确认"被当作"取消"（实盘总开关点了启用却存不进）。共享版必须保证
//      首次 resolve 定型且只定型一次。
//   2) connectSSE 建链并发守卫（§P1-10）：取票据 await 期间 sse 仍为 null，两个页面
//      effect 并发调用会开出两条 EventSource。共享 connecting promise 后必须只建一条链。
// English: regression tests for the shared resolve-once confirmDialog (P1-9) and the
// connectSSE concurrency guard (P1-10) added in today's UAT fixes.
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'

// TDesign 桩：DialogPlugin.confirm 捕获回调句柄，供测试直接触发 onConfirm/onClose
const dialogHandles = []
vi.mock('tdesign-react', () => ({
  MessagePlugin: { info: vi.fn(), success: vi.fn(), warning: vi.fn(), error: vi.fn() },
  NotificationPlugin: { warning: vi.fn() },
  DialogPlugin: {
    confirm: (opts) => {
      dialogHandles.push(opts)
      return { hide: () => {} }
    },
  },
}))

import { confirmDialog } from '../ui.jsx'
import * as api from '../api/index.js'

describe('confirmDialog resolve-once 守卫（§P1-9）', () => {
  beforeEach(() => {
    dialogHandles.length = 0
    localStorage.clear()
  })

  it('onConfirm 先 hide 再 onClose 时，确认结果不被取消覆盖', async () => {
    const p = confirmDialog('确认启用实盘？', '确认')
    // 守卫生效前提：onConfirm 同步 resolve(true)（d.hide 同步触发 onClose 的场景由
    // 先后调用顺序模拟——真实 TDesign 中 hide() 内部同步回调 onClose）
    dialogHandles[0].onConfirm()
    dialogHandles[0].onClose() // 迟到的取消必须被忽略
    await expect(p).resolves.toBe(true)
  })

  it('先取消后确认：取消结果定型，确认被忽略', async () => {
    const p = confirmDialog('确认删除？', '删除')
    dialogHandles[0].onClose()
    dialogHandles[0].onConfirm()
    await expect(p).resolves.toBe(false)
  })

  it('只 resolve 一次：多次回调不产生多余 resolve', async () => {
    let resolved = 0
    const p = confirmDialog('正文').then((v) => { resolved++; return v })
    dialogHandles[0].onConfirm()
    dialogHandles[0].onClose()
    dialogHandles[0].onConfirm()
    await expect(p).resolves.toBe(true)
    // Promise 本身只 settle 一次；这里断言守卫路径未抛异常且结果正确
    expect(resolved).toBe(1)
  })
})

describe('connectSSE 并发守卫（§P1-10）', () => {
  let sources
  beforeEach(() => {
    localStorage.clear()
    localStorage.setItem('liangzai_token', 'tok')
    sources = []
    vi.stubGlobal('EventSource', class {
      constructor(url) {
        this.url = url
        sources.push(this)
      }
      close() {}
    })
    // 票据签发走 request() → fetch：返回固定一次性票据
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ ticket: 't-1' }), { status: 200 })))
  })
  afterEach(() => {
    vi.unstubAllGlobals()
    vi.restoreAllMocks()
  })

  it('并发调用只签发一次票据、只建一条 EventSource', async () => {
    const [a, b] = [api.connectSSE(), api.connectSSE()]
    await Promise.all([a, b])
    expect(sources.length).toBe(1)
    expect(sources[0].url).toContain('ticket=t-1')
    api.disconnectSSE()
  })

  it('连接已建立时重复调用直接返回（不重建）', async () => {
    await api.connectSSE()
    await api.connectSSE()
    expect(sources.length).toBe(1)
    api.disconnectSSE()
  })
})
