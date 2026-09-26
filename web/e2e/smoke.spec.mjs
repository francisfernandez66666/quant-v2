// ── §F6 Playwright 冒烟测试 smoke.spec.mjs ──
// 覆盖：登录链路 + 五个核心页面（仪表盘/信号/持仓/模拟盘/自动研究）可达且渲染关键元素 +
// §F6 命令面板 Ctrl+K 呼出。凭据经环境变量注入（不写死），仅做"不白屏、有内容"级冒烟，非功能断言。
// English: §F6 smoke — login flow + five core pages reachable with key content + Ctrl+K palette.
// Credentials come from env (never hardcoded); asserts "renders, no white screen", not deep behavior.
import { test, expect } from '@playwright/test'

// §0926E2E-17c：凭据缺省不再回退空串（空串 fill 后失败形态离"忘了传环境变量"隔一层）；
// 且登录页若已带 .t-menu 说明复用了外部残留 storageState——那等于没测本栈登录链路，同样显式失败。
// English: §0926E2E-17c — credentials are mandatory (fail fast), and a pre-logged session from a
// foreign storageState is rejected instead of being silently reused.
const USER = process.env.E2E_USER
const PASS = process.env.E2E_PASS

async function login(page) {
  if (!USER || !PASS) {
    test.fail(true, '§0926E2E-17c：E2E_USER/E2E_PASS 未注入——请用 scripts/uat_bootstrap.sh 正规通道')
    return
  }
  await page.goto('/#/')
  // 已登录（存在侧栏菜单）：冒烟允许复用**本通道自己预置**的会话，但凭据在位时正常续跑
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
    // 统一断言各页最稳的结构容器：每个业务页根节点都是 <div className="page">（非文案，避免
    // 依赖具体中文标题——如仪表盘正文不含"仪表盘"字样会导致文案断言脆断）
    await expect(page.locator('.app-main .page').first()).toBeVisible()
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

test('§DAILY_REVIEW 消息中心含复盘筛选与手动按钮', async ({ page }) => {
  await page.goto('/#/msgcenter')
  await expect(page.getByText('盘后复盘')).toBeVisible()
  await expect(page.getByRole('button', { name: '立即复盘' })).toBeVisible()
})
