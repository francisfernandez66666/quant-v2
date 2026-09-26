// ── §F6 Playwright 登录态预置（setup project）──
// 全链路仅登录一次，把 localStorage(liangzai_token 等) 存为 storageState 供各用例复用，
// 规避后端匿名端点 IP 频控（login 5/分钟）——每个用例各自登录会在 6 连跑时触发 429 假失败。
// English: log in ONCE, persist localStorage as storageState for all specs; avoids the backend's
// 5/min anonymous-login IP limiter that made per-test logins spuriously fail on the 5th run.
import { test, expect } from '@playwright/test'

// §0926E2E-17c：凭据缺省不再回退空串——空串照样 fill+Enter，失败形态是"登录报错/会话没建起来"，
// 离根因（忘了传 E2E_USER/E2E_PASS）隔了一层；这里开局即显式失败，一句话指向缺的环境变量。
// English: §0926E2E-17c — no empty-string credential defaults; fail fast with a pointed message.
const USER = process.env.E2E_USER
const PASS = process.env.E2E_PASS

test('预置登录态 storageState', async ({ page }) => {
  if (!USER || !PASS) {
    test.fail(true, '§0926E2E-17c：E2E_USER/E2E_PASS 未注入——Playwright 只准经 scripts/uat_bootstrap.sh 正规通道起（见记忆 uat-env-from-bootstrap）')
    return
  }
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
