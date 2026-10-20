// ── 发布闸 #4：SSE 长连接断线重连（真实浏览器）──
// 判据：第一次 /api/events 连接被强制中断后，浏览器 EventSource 必须自动发起第二次建链；
// 前端 §H6 策略是“原生重连优先”，所以这里不调用任何页面 API，只观察网络层是否真的重连。
// 运行：起本机 UAT 栈后
//   E2E_BASE_URL=http://localhost:5173 E2E_API=http://127.0.0.1:18080 \
//   E2E_USER=admin E2E_PASS='Nightly!2026' npx playwright test e2e/sse_reconnect.spec.mjs
import { test, expect } from '@playwright/test'

test.describe('发布闸 #4 · SSE 断线重连', () => {
  test('首个 SSE 连接被中断后浏览器自动重连', async ({ page }) => {
    const hits = []
    let abortedFirst = false
    await page.route('**/api/events*', async (route) => {
      hits.push(route.request().url())
      if (!abortedFirst) {
        abortedFirst = true
        await route.abort('failed')
        return
      }
      await route.continue()
    })

    await page.goto('/')
    // 等应用完成登录态恢复并建链；重连由浏览器按 retry 退避发起
    await expect.poll(() => hits.length, {
      timeout: 25000,
      message: '断线后 25s 内必须出现第二次 /api/events 建链',
    }).toBeGreaterThanOrEqual(2)
    expect(abortedFirst, '首次连接必须被本用例真的中断过').toBe(true)
  })
})
