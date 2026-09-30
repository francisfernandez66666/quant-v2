// ── §ICP/§POLICE 备案号页脚 Playwright 验收 icp_check.spec.mjs（2026-09-15 管局合规；2026-09-30 增公安备案）──
// 覆盖：登录页（未登录首页，清空会话）与仪表盘页脚（登录后首页）两处必须同时展示
// ICP 备案号「沪ICP备2026045551」（外链工信部首页 beian.miit.gov.cn）与
// 公安备案号「沪公网安备31011302009737号」（外链全国互联网安全管理服务平台 beian.mps.gov.cn 查询页 + 警徽图标）；
// 截图落 test-results/icp-pixels 供像素级人工复核。凭据来自环境变量（与其他 spec 同一注入约定）。
import { test, expect } from '@playwright/test'
const SHOT = 'test-results/icp-pixels'

// 两条备案号的期望值（与 web/src/components/IcpFooter.jsx 常量同源，改动备案号需同步此处）
const ICP_TEXT = '沪ICP备2026045551'
const ICP_HREF = 'https://beian.miit.gov.cn/'
const POLICE_TEXT = '沪公网安备31011302009737号'
const POLICE_HREF = 'https://beian.mps.gov.cn/#/query/webSearch?code=31011302009737'

// 断言一个页脚容器内两条备案号齐备：文案可见 + 各自外链指向正确的备案平台 + 新标签打开。
// 说明：这里必须按 data-testid 分别取链接——页脚现在有两个 <a>，用 icp.locator('a') 单条断言
// 会撞 Playwright 严格模式（一个定位器命中多元素直接报错）。
async function expectBothBeianLinks(page, box) {
  await expect(box).toBeVisible({ timeout: 10000 })
  await expect(box).toContainText(ICP_TEXT)
  await expect(box).toContainText(POLICE_TEXT)

  const icp = page.getByTestId('icp-link')
  await expect(icp).toHaveText(ICP_TEXT)
  await expect(icp).toHaveAttribute('href', ICP_HREF)
  await expect(icp).toHaveAttribute('target', '_blank')

  const police = page.getByTestId('police-link')
  await expect(police).toHaveText(POLICE_TEXT)
  await expect(police).toHaveAttribute('href', POLICE_HREF)
  await expect(police).toHaveAttribute('target', '_blank')
}

// 1. 登录页（清空会话）底部备案号：管局与公安都要求首页底部展示备案号并链接备案平台
test('登录页：ICP + 公安备案号展示，各自外链备案平台', async ({ browser }) => {
  const ctx = await browser.newContext({ storageState: { cookies: [], origins: [] } })
  const page = await ctx.newPage()
  await page.goto('/#/')
  const icp = page.getByTestId('icp-footer')
  await expect(icp).toBeVisible({ timeout: 8000 })
  await expectBothBeianLinks(page, icp)
  await page.screenshot({ path: `${SHOT}/login-icp-footer.png`, fullPage: true })
  await ctx.close()
})

// 2. 登录后仪表盘（首页）底部备案号（storageState 已预置登录态）
test('仪表盘：两条备案号展示在免责声明页脚下方 + 警徽图真加载', async ({ page }) => {
  await page.goto('/#/dashboard')
  await expect(page.locator('.app-shell')).toBeVisible({ timeout: 15000 })
  await page.waitForTimeout(2000)
  const icp = page.getByTestId('icp-footer')
  await expect(icp).toBeVisible({ timeout: 10000 })
  await expectBothBeianLinks(page, icp)
  // 警徽是本地托管静态资源（web/public/police-emblem.png → dist 根）：
  // 只断 <img> 存在不算验收，必须看浏览器真解码成功（naturalWidth>0），
  // 否则构建/部署漏了 public 目录时页面只会显示一张破图。
  const emblemSrc = await page.getByTestId('police-link').locator('img').getAttribute('src')
  expect(emblemSrc).toBe('/police-emblem.png')
  const loaded = await page.getByTestId('police-link').locator('img').evaluate((el) => el.naturalWidth)
  expect(loaded).toBeGreaterThan(0)
  await page.screenshot({ path: `${SHOT}/dashboard-icp-footer.png`, fullPage: true })
})
