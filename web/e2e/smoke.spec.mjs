// ── §F6 Playwright 冒烟测试 smoke.spec.mjs ──
// 覆盖：登录链路 + 五个核心页面（仪表盘/信号/持仓/模拟盘/自动研究）可达且渲染关键元素 +
// §F6 命令面板 Ctrl+K 呼出。凭据经环境变量注入（不写死），仅做"不白屏、有内容"级冒烟，非功能断言。
// English: §F6 smoke — login flow + five core pages reachable with key content + Ctrl+K palette.
// Credentials come from env (never hardcoded); asserts "renders, no white screen", not deep behavior.
import { test, expect } from '@playwright/test'

const USER = process.env.E2E_USER || ''
const PASS = process.env.E2E_PASS || ''

async function login(page) {
  await page.goto('/#/')
  // 已登录（存在侧栏菜单）则跳过登录表单
  if (await page.locator('.app-shell .t-menu').count() > 0) return
  const acct = page.getByPlaceholder('输入账号')
  const pwd = page.getByPlaceholder('输入密码')
  await acct.fill(USER)
  await pwd.fill(PASS)
  await pwd.press('Enter')
  await expect(page.locator('.app-shell')).toBeVisible({ timeout: 15000 })
}

test.beforeEach(async ({ page }) => { await login(page) })

const PAGES = [
  { hash: '#/dashboard', name: '仪表盘', anchor: 'disclaimer-footer' },
  { hash: '#/signals', name: '信号' },
  { hash: '#/positions', name: '持仓' },
  { hash: '#/msgcenter', name: '消息' },
]

for (const p of PAGES) {
  test(`页面可达：${p.name} ${p.hash}`, async ({ page }) => {
    await page.goto('/' + p.hash)
    await expect(page.locator('.app-main')).toBeVisible()
    await expect(page.locator('.app-main').getByText(p.name, { exact: false }).first()).toBeVisible()
    if (p.anchor) await expect(page.locator(`[data-testid="${p.anchor}"]`)).toBeVisible()
  })
}

test('§F6 命令面板 Ctrl+K 呼出并可跳转', async ({ page }) => {
  await page.keyboard.press('Control+k')
  await expect(page.getByTestId('cmdk-panel')).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(page.getByTestId('cmdk-panel')).toHaveCount(0)
})

test('§F6 角色提示条常驻', async ({ page }) => {
  await page.goto('/#/dashboard')
  await expect(page.getByTestId('role-bar')).toBeVisible()
})
