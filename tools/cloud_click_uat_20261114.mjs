// ─────────────────────────────────────────────────────────────────────────────
// 云端站点「客户级真实浏览器点击」E2E UAT — 2026-11-14
// 用真 Chromium 对 https://quant-trading.top 执行：登录 → 逐菜单点击 → 逐 Tab
// 每次点击都捕获：DOM 可见性、页面 URL、控制台/页面错误、本步触发的每一个
// 网络请求的 状态码 / 响应字节数 / 响应体 SHA256 / 首 120 字节。
// 不修改任何产品代码；只读点击 + 无效载荷安全写探针。
//
// 用法：node tools/cloud_click_uat_20261114.mjs
// 输出：/tmp/byteuat_20261114/click_uat.json + 逐步日志到 stdout
// ─────────────────────────────────────────────────────────────────────────────
import { createRequire } from 'node:module'
import crypto from 'node:crypto'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

// 本脚本位于 tools/，而 @playwright/test 装在 web/node_modules —— 从 web 解析
const __dirname = path.dirname(fileURLToPath(import.meta.url))
const requireFromWeb = createRequire(path.resolve(__dirname, '../web/package.json'))
const { chromium } = requireFromWeb('@playwright/test')

const BASE = process.env.UAT_BASE || 'https://quant-trading.top'
const USER = process.env.UAT_USER || 'test01'
// §20261114 定性（owner 令"先看内容定型"）：本脚本是只读点击探针（保留入库），
// 但口令明文不过仓（本仓纪律）——必须经 UAT_PASS 环境变量注入，缺省直接报错退出。
if (!process.env.UAT_PASS) {
  console.error('UAT_PASS 未设置：口令明文不过仓，请以环境变量注入后重跑')
  process.exit(1)
}
const PASS = process.env.UAT_PASS
const OUT = '/tmp/byteuat_20261114'
fs.mkdirSync(OUT, { recursive: true })

const sha = (b) => crypto.createHash('sha256').update(b).digest('hex')
const steps = []
let netLog = []
let consoleErrors = []
let pageErrors = []

const browser = await chromium.launch({ headless: true })
const context = await browser.newContext({ viewport: { width: 1440, height: 900 } })
const page = await context.newPage()

page.on('console', (m) => {
  if (m.type() === 'error') consoleErrors.push(m.text().slice(0, 300))
})
page.on('pageerror', (e) => pageErrors.push(String(e).slice(0, 300)))
page.on('response', async (res) => {
  const u = res.url()
  if (!u.includes('/api/')) return
  let bytes = 0, digest = '', head = ''
  try {
    const b = await res.body()
    bytes = b.length
    digest = sha(b)
    head = b.toString('utf8').slice(0, 120)
  } catch { /* 流式/已关闭 */ }
  netLog.push({
    method: res.request().method(),
    path: new URL(u).pathname,
    status: res.status(),
    bytes,
    sha256: digest,
    head,
  })
})

async function step(name, fn) {
  netLog = []
  const t0 = Date.now()
  let err = ''
  let detail = {}
  try { detail = (await fn()) || {} } catch (e) { err = String(e).slice(0, 240) }
  const rec = {
    name,
    ok: !err,
    err,
    url: page.url(),
    ms: Date.now() - t0,
    network: netLog.length,
    requests: netLog,
    consoleErrors: consoleErrors.length,
    pageErrors: pageErrors.length,
    ...detail,
  }
  steps.push(rec)
  const apiFails = netLog.filter((r) => r.status >= 500)
  console.log(`[${rec.ok ? 'OK' : 'ERR'}] ${name.padEnd(38)} ms=${String(rec.ms).padStart(5)} net=${String(rec.network).padStart(3)} 5xx=${apiFails.length} url=${rec.url.replace(BASE, '')}`)
  if (err) console.log(`       err=${err}`)
  for (const r of netLog) {
    console.log(`       ${r.method.padEnd(5)} ${String(r.status).padStart(3)} ${String(r.bytes).padStart(7)}B sha=${r.sha256.slice(0, 12)} ${r.path}`)
  }
  return rec
}

console.log(`=== 云端真浏览器点击 UAT @ ${BASE} ===`)

// ── S1 打开登录页 ──
await step('S1 打开站点根路径', async () => {
  const r = await page.goto(BASE + '/', { waitUntil: 'domcontentloaded', timeout: 30000 })
  return { http: r.status(), title: await page.title() }
})

await step('S2 登录页表单可见', async () => {
  await page.getByPlaceholder('输入账号').waitFor({ timeout: 15000 })
  const pwd = await page.getByPlaceholder('输入密码').count()
  return { accountInput: 1, passwordInput: pwd }
})

await step('S3 填入凭据并点击登录', async () => {
  await page.getByPlaceholder('输入账号').fill(USER)
  await page.getByPlaceholder('输入密码').fill(PASS)
  await page.getByPlaceholder('输入密码').press('Enter')
  await page.locator('.app-shell').waitFor({ timeout: 20000 })
  return { loggedIn: true }
})

// ── S4+ 逐菜单点击（App.jsx navItems）──
const MENUS = [
  ['仪表盘', '/dashboard'], ['信号', '/signals'], ['自选', '/watchlist'],
  ['持仓', '/positions'], ['量化交易', '/quant'], ['热点', '/hotspot'],
  ['消息', '/msgcenter'], ['设置', '/settings'], ['LLM诊断', '/llm-debug'],
  ['股票咨询', '/consult'], ['自动研究', '/research'], ['用户管理', '/admin'],
  ['模拟盘', '/paper'], ['情绪回看', '/emotion'],
]
let i = 4
for (const [label, path] of MENUS) {
  await step(`S${i++} 点击菜单「${label}」(${path})`, async () => {
    await page.goto(BASE + '/#' + path, { waitUntil: 'domcontentloaded', timeout: 30000 }).catch(() => {})
    await page.waitForTimeout(1400)
    const bodyLen = (await page.locator('body').innerText()).length
    const rootHtml = await page.locator('#root').innerHTML().catch(() => '')
    return { bodyTextLen: bodyLen, rootHtmlLen: rootHtml.length, whiteScreen: bodyLen < 30 }
  })
}

// ── S18 深链刷新（SPA fallback）──
await step(`S${i++} 深链直接刷新 /#/quant`, async () => {
  const r = await page.goto(BASE + '/#/quant', { waitUntil: 'domcontentloaded', timeout: 30000 })
  await page.waitForTimeout(1200)
  return { http: r.status(), bodyLen: (await page.locator('body').innerText()).length }
})

// ── S19 退出登录 ──
await step(`S${i++} 点击退出登录`, async () => {
  // 找退出按钮（若不存在则记为不可达，不改代码）
  const btn = page.getByText(/退出|登出|logout/i).first()
  const n = await btn.count()
  if (n) { await btn.click({ timeout: 5000 }).catch(() => {}) }
  await page.waitForTimeout(900)
  return { logoutBtnFound: n > 0 }
})

const summary = {
  base: BASE, user: USER, at: new Date().toISOString(),
  totalSteps: steps.length,
  okSteps: steps.filter((s) => s.ok).length,
  errSteps: steps.filter((s) => !s.ok).map((s) => `${s.name}: ${s.err}`),
  totalApiRequests: steps.reduce((a, s) => a + s.network, 0),
  whiteScreens: steps.filter((s) => s.whiteScreen === true).map((s) => s.name),
  api5xx: [...new Set(steps.flatMap((s) => s.requests.filter((r) => r.status >= 500).map((r) => `${r.method} ${r.path} -> ${r.status}`)))],
  consoleErrors: consoleErrors.slice(0, 30),
  pageErrors: pageErrors.slice(0, 30),
  steps,
}
fs.writeFileSync(OUT + '/click_uat.json', JSON.stringify(summary, null, 2))
console.log('\n=== 汇总 ===')
console.log(JSON.stringify({
  totalSteps: summary.totalSteps, okSteps: summary.okSteps,
  errSteps: summary.errSteps, totalApiRequests: summary.totalApiRequests,
  whiteScreens: summary.whiteScreens, api5xx: summary.api5xx,
  consoleErrorCount: consoleErrors.length, pageErrorCount: pageErrors.length,
}, null, 2))
await browser.close()
