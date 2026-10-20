// ── 发布闸 #3：跨域 CORS 真实浏览器验证 ──
// 为什么必须用浏览器：curl 只能看到响应头，看不到浏览器是否真的放行（ACAO 回显错源、
// 预检失败、缺 Vary 被缓存复用，这几种在 curl 下都可能“看起来对”）。
//
// 默认跳过现网拨测；发布前显式开：
//   E2E_ALLOW_PROD=1 npx playwright test e2e/cors_browser.spec.mjs
//
// 覆盖三条：
//   ① 同 Host 跨端口（http://quant-trading.top:8080 → https://quant-trading.top/api/status）
//      必须被浏览器放行；后端按 Host 同源回显 ACAO。
//   ② 带 Authorization 的跨域 GET 必须先过 OPTIONS 预检，最终拿到 401（不是 CORS TypeError）。
//   ③ 非白名单 Host（127.0.0.1 页面 → quant-trading.top API）必须被浏览器拦截。
import { test, expect } from '@playwright/test'
import http from 'node:http'

const ALLOW_PROD = process.env.E2E_ALLOW_PROD === '1'
const PAGE_URL = process.env.CORS_PAGE_URL || 'http://quant-trading.top:8080/'
const API_URL = process.env.CORS_API_URL || 'https://quant-trading.top/api/status'

test.describe('发布闸 #3 · CORS 真实浏览器', () => {
  test.skip(!ALLOW_PROD, '现网跨域拨测需显式 E2E_ALLOW_PROD=1')

  test('同 Host 跨端口 GET 被浏览器放行', async ({ page }) => {
    await page.goto(PAGE_URL, { waitUntil: 'domcontentloaded' })
    const r = await page.evaluate(async (url) => {
      try {
        const resp = await fetch(url, { method: 'GET' })
        return { ok: true, status: resp.status }
      } catch (e) {
        return { ok: false, err: String(e && e.message ? e.message : e) }
      }
    }, API_URL)
    expect(r.ok, '浏览器侧 fetch 不应被 CORS 拦截: ' + JSON.stringify(r)).toBe(true)
    // /api/status 需要鉴权：401 正好证明请求已到业务 handler（CORS 已放行），不是网络/CORS 失败
    expect([200, 401]).toContain(r.status)
  })

  test('带 Authorization 的跨域 GET 预检通过（最终 401，不是 CORS TypeError）', async ({ page }) => {
    await page.goto(PAGE_URL, { waitUntil: 'domcontentloaded' })
    const r = await page.evaluate(async (url) => {
      try {
        const resp = await fetch(url, { method: 'GET', headers: { Authorization: 'Bearer e2e-cors-probe' } })
        return { ok: true, status: resp.status }
      } catch (e) {
        return { ok: false, err: String(e && e.message ? e.message : e) }
      }
    }, API_URL)
    expect(r.ok, '预检失败会让 fetch 直接抛 TypeError: ' + JSON.stringify(r)).toBe(true)
    expect(r.status).toBe(401)
  })

  test('非白名单 Host 的跨域请求被浏览器拦截', async ({ page }) => {
    const srv = http.createServer((req, res) => {
      res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' })
      res.end('<!DOCTYPE html><html><body>cors probe</body></html>')
    })
    await new Promise((resolve) => srv.listen(0, '127.0.0.1', resolve))
    const port = srv.address().port
    try {
      await page.goto(`http://127.0.0.1:${port}/`, { waitUntil: 'domcontentloaded' })
      const r = await page.evaluate(async (url) => {
        try {
          const resp = await fetch(url)
          return { ok: true, status: resp.status }
        } catch (e) {
          return { ok: false, err: String(e && e.message ? e.message : e) }
        }
      }, API_URL)
      expect(r.ok, '非白名单 Origin 不应拿到响应: ' + JSON.stringify(r)).toBe(false)
    } finally {
      await new Promise((resolve) => srv.close(resolve))
    }
  })
})
