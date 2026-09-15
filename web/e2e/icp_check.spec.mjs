// ── §ICP 备案号页脚 Playwright 验收 icp_check.spec.mjs（2026-09-15 管局合规要求）──
// 覆盖：登录页（未登录首页，清空会话）与仪表盘页脚（登录后首页）两处必须展示备案号
// 「沪ICP备2026045551」并外链工信部首页 beian.miit.gov.cn；截图落 test-results/icp-pixels
// 供像素级人工复核。凭据来自环境变量（与其他 spec 同一注入约定）。
import { test, expect } from '@playwright/test'
const SHOT = 'test-results/icp-pixels'

// 1. 登录页（清空会话）底部备案号：管局要求首页底部展示备案号并链接工信部首页
test('登录页：备案号展示 + 工信部外链', async ({ browser }) => {
  const ctx = await browser.newContext({ storageState: { cookies: [], origins: [] } })
  const page = await ctx.newPage()
  await page.goto('/#/')
  const icp = page.getByTestId('icp-footer')
  await expect(icp).toBeVisible({ timeout: 8000 })
  await expect(icp).toContainText('沪ICP备2026045551')
  const link = icp.locator('a')
  await expect(link).toHaveAttribute('href', 'https://beian.miit.gov.cn/')
  await expect(link).toHaveAttribute('target', '_blank')
  await page.screenshot({ path: `${SHOT}/login-icp-footer.png`, fullPage: true })
  await ctx.close()
})

// 2. 登录后仪表盘（首页）底部备案号（storageState 已预置登录态）
test('仪表盘：备案号展示在免责声明页脚下方', async ({ page }) => {
  await page.goto('/#/dashboard')
  await expect(page.locator('.app-shell')).toBeVisible({ timeout: 15000 })
  await page.waitForTimeout(2000)
  const icp = page.getByTestId('icp-footer')
  await expect(icp).toBeVisible({ timeout: 10000 })
  await expect(icp).toContainText('沪ICP备2026045551')
  await expect(icp.locator('a')).toHaveAttribute('href', 'https://beian.miit.gov.cn/')
  await page.screenshot({ path: `${SHOT}/dashboard-icp-footer.png`, fullPage: true })
})
