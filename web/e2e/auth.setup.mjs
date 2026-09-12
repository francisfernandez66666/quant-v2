// ── §F6 Playwright 登录态预置（setup project）──
// 全链路仅登录一次，把 localStorage(liangzai_token 等) 存为 storageState 供各用例复用，
// 规避后端匿名端点 IP 频控（login 5/分钟）——每个用例各自登录会在 6 连跑时触发 429 假失败。
// English: log in ONCE, persist localStorage as storageState for all specs; avoids the backend's
// 5/min anonymous-login IP limiter that made per-test logins spuriously fail on the 5th run.
import { test, expect } from '@playwright/test'

const USER = process.env.E2E_USER || ''
const PASS = process.env.E2E_PASS || ''

test('预置登录态 storageState', async ({ page }) => {
  await page.goto('/#/')
  // 已是登录态（复用旧 state）则先登出，确保本次用真实凭据建立干净的会话
  const acct = page.getByPlaceholder('输入账号')
  await acct.waitFor({ timeout: 15000 })
  await acct.fill(USER)
  await page.getByPlaceholder('输入密码').fill(PASS)
  await page.getByPlaceholder('输入密码').press('Enter')
  await expect(page.locator('.app-shell')).toBeVisible({ timeout: 15000 })
  await page.context().storageState({ path: '.auth/state.json' })
})
