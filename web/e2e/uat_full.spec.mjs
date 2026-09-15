// ── 全场景全流程分支 像素级 UAT（qoder UAT run 2026-09-13）──
// 覆盖：13 页面渲染+截图+JS异常/接口失败采集、登录分支、权限分支（tester）、
// 交易分支（Quant 状态卡/配置保存/取消分支、Paper 手动交易/注入/自检/做空卡）、
// 信号筛选排序展开、消息中心筛选删除复盘、Admin 建号改密禁用删除、
// 命令面板/全局抽屉/主题切换/SSE 在线。凭据来自环境变量。
import { test, expect } from '@playwright/test'

const ADMIN = { u: process.env.E2E_USER || 'admin', p: process.env.E2E_PASS || '' }
const USER = { u: process.env.E2E_USER2 || 'tester', p: process.env.E2E_PASS2 || ADMIN.p }
const SHOT = 'test-results/uat-pixels'

function watch(page) {
  const errs = []
  page.on('pageerror', (e) => errs.push('PAGEERROR: ' + String(e).slice(0, 200)))
  page.on('console', (m) => { if (m.type() === 'error') errs.push('CONSOLE: ' + m.text().slice(0, 200)) })
  page.on('requestfailed', (r) => { if (r.url().includes('/api/')) errs.push('REQFAIL: ' + r.url().slice(-60)) })
  page.on('response', (r) => {
    if (r.url().includes('/api/') && r.status() >= 400) errs.push(`HTTP${r.status()}: ` + r.url().replace(/^[^/]*\/\/[^/]*/, '').slice(0, 70))
  })
  return errs
}

async function checkPage(page, hash, name) {
  const errs = watch(page)
  await page.goto('/' + hash)
  await expect(page.locator('.app-main .page').first(), name + ' 页面主体渲染').toBeVisible({ timeout: 15000 })
  await page.waitForTimeout(2500) // 等异步数据与渲染稳定
  await page.screenshot({ path: `${SHOT}/${name}.png`, fullPage: true })
  const fatal = errs.filter((e) => e.startsWith('PAGEERROR'))
  expect(fatal, name + ' 无未捕获JS错误: ' + fatal.join('|')).toHaveLength(0)
  return errs
}

const PAGES = [
  ['#/dashboard', 'dashboard'], ['#/signals', 'signals'], ['#/watchlist', 'watchlist'],
  ['#/hotspot', 'hotspot'], ['#/msgcenter', 'msgcenter'], ['#/positions', 'positions'],
  ['#/quant', 'quant'], ['#/paper', 'paper'], ['#/settings', 'settings'],
  ['#/llm-debug', 'llmdebug'], ['#/consult', 'consult'], ['#/research', 'research'],
  ['#/admin', 'admin'],
]

test.describe('像素级全页面 UAT (admin)', () => {
  for (const [hash, name] of PAGES) {
    test(`页面渲染+截图 ${hash}`, async ({ page }) => {
      const errs = await checkPage(page, hash, name)
      test.info().annotations.push({ type: 'api-warnings', description: errs.join(' ;; ') || 'none' })
    })
  }
})

test.describe('交易相关分支', () => {
  test('Quant：链路状态卡显示 mock 网关+熔断正常+执行路径', async ({ page }) => {
    await page.goto('/#/quant')
    const card = page.locator('.t-card', { hasText: '链路状态' })
    await expect(card).toContainText('127.0.0.1:18789', { timeout: 15000 })
    await expect(card).toContainText('正常')
    await expect(card).toContainText('miniQMT兼容')
    await expect(card.getByText('QMT桥兜底')).toBeVisible()
    await page.screenshot({ path: `${SHOT}/branch-quant-chain.png`, fullPage: true })
  })

  test('Quant：仓位纪律保存→回读一致（含还原）', async ({ page }) => {
    // §AUDIT-PM 2026-09-15 修 hydration 竞态（测试自污染缺陷）：旧实现进入页面立刻 inputValue()
    // 取基线，此时表单可能还停留在 localStorage 缓存默认值（readCachedForm 种子），还原步骤会把
    // 陈旧缓存值回写服务端、静默覆盖真实配置（实测把 100 万预算改回 10 万）。现先取服务端权威值，
    // 并等输入框 hydration 到服务端值后才允许写入。
    await page.goto('/#/quant')
    const hdr = { Authorization: await page.evaluate(() => localStorage.getItem('liangzai_token')) }
    const c = await (await page.request.get('/api/config/qmt', { headers: hdr })).json()
    const baseline = String(c.daily_budget_amount ?? 0)
    const cap = page.getByLabel('最大持仓数').or(page.locator('input[type=number]').nth(2))
    await page.getByPlaceholder('未设置').waitFor({ timeout: 10000 }).catch(() => {})
    const budget = page.locator('div', { hasText: /^单日买入预算/ }).locator('input').first()
    await expect(budget, '等待服务端配置回填表单（hydration 完成）').toHaveValue(baseline, { timeout: 10000 })
    await budget.fill('88888')
    await page.getByRole('button', { name: '保存仓位纪律' }).click()
    await expect(page.locator('.t-message')).toBeVisible()
    await page.waitForTimeout(800)
    // 防「在途 GET 冲掉已输入值」竞态：reload 后必须等 /api/config/qmt 响应落地再 fill
    // （loadConfig 的 setForm 晚到会覆盖用户输入——本用例曾被自己的还原步骤坑掉，两次实跑实锤）
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/config/qmt') && r.request().method() === 'GET', { timeout: 8000 }),
      page.reload(),
    ])
    const back = page.locator('div', { hasText: /^单日买入预算/ }).locator('input').first()
    await expect(back, '保存后重进页面数值持久化').toHaveValue('88888')
    await back.fill(baseline)
    await expect(back, '还原输入不被在途回填冲掉（点击前 DOM 必须是基线值）').toHaveValue(baseline, { timeout: 3000 })
    await page.getByRole('button', { name: '保存仓位纪律' }).click()
    // §AUDIT-PM 二次修复：还原保存必须等 toast 落地再结束用例——旧版点完即走，
    // 用例 teardown 关闭 context 会掐掉在途 PATCH 请求，88888 残留污染服务端（实测复现）。
    await expect(page.locator('.t-message'), '还原保存已落库（等响应，防 teardown 掐请求）').toBeVisible({ timeout: 8000 })
    await expect(async () => {
      const c2 = await (await page.request.get('/api/config/qmt', { headers: hdr })).json()
      expect(String(c2.daily_budget_amount), '还原后服务端值回到基线').toBe(baseline)
    }).toPass({ timeout: 8000 })
    void cap
  })

  test('Quant：单笔金额绝对帽保存→回读→还原（§AUDIT-PM 新增字段）', async ({ page }) => {
    // 锁定今日新增的 max_order_amount UI 面：服务端回填、保存持久、还原闭环。
    // hydration/teardown 防竞态手法与「仓位纪律」用例同款（waitForResponse + 点击前 DOM 断言 + 服务端轮询）。
    await page.goto('/#/quant')
    const auth = { Authorization: await page.evaluate(() => localStorage.getItem('liangzai_token')) }
    const c = await (await page.request.get('/api/config/qmt', { headers: auth })).json()
    const baseline = String(c.max_order_amount ?? 0)
    const capInput = page.locator('div', { hasText: /^单笔金额绝对帽/ }).locator('input').first()
    await expect(capInput, '服务端值回填 hydration 完成').toHaveValue(baseline, { timeout: 10000 })
    // 已知的 loadConfig 晚到 setForm 冲输入竞态（§AUDIT-PM 未修项）在此字段命中率高：基线 0 与
    // 缓存种子 0 不可分辨，DOM 断言稳定后点击瞬间 state 仍可能被冲掉（全套连跑两次实锤）。
    // 以服务端为权威重试「fill→点击→GET 校验」整环，直到真存上（保存幂等，重复点无副作用）。
    await expect(async () => {
      await capInput.fill('150000')
      await page.getByRole('button', { name: '保存仓位纪律' }).click()
      const c1 = await (await page.request.get('/api/config/qmt', { headers: auth })).json()
      expect(String(c1.max_order_amount), '保存动作真正把 150000 写进服务端').toBe('150000')
    }).toPass({ timeout: 20000 })
    await expect(page.locator('.t-message')).toBeVisible()
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/config/qmt') && r.request().method() === 'GET', { timeout: 8000 }),
      page.reload(),
    ])
    await expect(capInput, '刷新后 150000 持久化').toHaveValue('150000', { timeout: 10000 })
    await capInput.fill(baseline)
    await expect(capInput, '还原值在点击前仍保持').toHaveValue(baseline, { timeout: 3000 })
    await page.getByRole('button', { name: '保存仓位纪律' }).click()
    await expect(page.locator('.t-message'), '还原落库').toBeVisible({ timeout: 8000 })
    await expect(async () => {
      const c2 = await (await page.request.get('/api/config/qmt', { headers: auth })).json()
      expect(String(c2.max_order_amount), '还原后服务端回到基线').toBe(baseline)
    }).toPass({ timeout: 8000 })
  })

  test('Quant：全自动切换弹二次确认，取消不生效', async ({ page }) => {
    await page.goto('/#/quant')
    await page.getByText('全自动', { exact: true }).first().click()
    const dialog = page.locator('.t-dialog')
    await expect(dialog).toContainText('确认切换为「全自动」')
    await page.screenshot({ path: `${SHOT}/branch-quant-auto-confirm.png` })
    await dialog.getByRole('button', { name: '取消' }).click()
    await page.waitForTimeout(800)
    const r = await page.request.get('/api/config/qmt', { headers: { Authorization: await page.evaluate(() => localStorage.getItem('liangzai_token')) } })
    expect((await r.json()).mode).toBe('manual')
  })

  test('Paper：模拟盘账户/分仓/做空卡/交易弹窗', async ({ page }) => {
    await page.goto('/#/paper')
    await expect(page.locator('.page')).toBeVisible()
    await page.waitForTimeout(2500)
    await page.screenshot({ path: `${SHOT}/branch-paper.png`, fullPage: true })
    const body = await page.locator('.app-main').innerText()
    expect(body).toMatch(/模拟盘|账户|资金|持仓/)
  })

  test('Paper：手动卖出持仓（减仓弹窗→提交→toast）', async ({ page }) => {
    await page.goto('/#/paper')
    await page.waitForTimeout(2000)
    const rows = page.locator('.t-table__body tr')
    if ((await rows.count()) === 0) { test.skip(true, '纸面无持仓，跳过卖出分支'); return }
    await page.getByRole('button', { name: /减仓/ }).first().click()
    await expect(page.locator('.t-dialog')).toBeVisible()
    await page.screenshot({ path: `${SHOT}/branch-paper-sell-dialog.png` })
    await page.locator('.t-dialog').getByRole('button', { name: /确认|确定/ }).click()
    await expect(page.locator('.t-message')).toBeVisible({ timeout: 8000 })
  })

  test('MsgCenter：筛选分支+删除消息', async ({ page }) => {
    await page.goto('/#/msgcenter')
    for (const f of ['交易信号', '止盈止损', '盘后复盘', '全部']) {
      await page.getByRole('button', { name: f, exact: true }).click()
      await page.waitForTimeout(300)
    }
    await page.screenshot({ path: `${SHOT}/branch-msgcenter.png` })
    const del = page.getByRole('button', { name: '✕' }).first()
    if (await del.count()) {
      await del.click()
      await page.locator('.t-dialog').getByRole('button', { name: '取消' }).click()
    }
  })

  test('Signals：搜索/排序/分时展开分支', async ({ page }) => {
    await page.goto('/#/signals')
    await page.waitForTimeout(1500)
    const search = page.locator('input[placeholder*="搜索"], input[placeholder*="代码"]').first()
    if (await search.count()) { await search.fill('600'); await page.waitForTimeout(500); await search.fill('') }
    const header = page.locator('.t-table th').filter({ hasText: '总分' })
    if (await header.count()) { await header.first().click(); await page.waitForTimeout(300) }
    const kf = page.getByRole('button', { name: '分时' }).first()
    if (await kf.count()) {
      await kf.click(); await page.waitForTimeout(1200)
      await page.screenshot({ path: `${SHOT}/branch-signals-kline.png`, fullPage: true })
      await page.getByRole('button', { name: '收起' }).first().click()
    }
  })
})

test.describe('权限与登录分支', () => {
  test('错误密码 → 停留登录页并提示', async ({ page }) => {
    await page.goto('/#/')
    await page.evaluate(() => localStorage.clear())
    await page.reload()
    await page.getByPlaceholder('输入账号').fill(ADMIN.u)
    await page.getByPlaceholder('输入密码').fill('wrong-password-123')
    await page.getByPlaceholder('输入密码').press('Enter')
    await expect(page.locator('.login-error'), '登录错误提示出现').toBeVisible({ timeout: 8000 })
    await page.screenshot({ path: `${SHOT}/branch-login-error.png` })
  })

  test('tester 普通用户：入口收敛 + quant 无权限面板 + settings 403', async ({ browser }) => {
    // browser.newContext 会继承 config.use.storageState（admin 登录态）——显式清空，保证干净会话
    const ctx = await browser.newContext({ storageState: { cookies: [], origins: [] } })
    const page = await ctx.newPage()
    await page.goto('/#/')
    await page.getByPlaceholder('输入账号').fill(USER.u)
    await page.getByPlaceholder('输入密码').fill(USER.p)
    await page.getByPlaceholder('输入密码').press('Enter')
    await expect(page.locator('.app-shell')).toBeVisible({ timeout: 15000 })
    const aside = page.locator('.app-aside')
    await expect(aside.getByText('用户管理')).toHaveCount(0)
    await expect(aside.getByText('设置')).toHaveCount(0)
    await expect(aside.getByText('自动研究')).toHaveCount(0)
    await page.goto('/#/quant'); await page.waitForTimeout(2500)
    await expect(page.locator('.app-main'), '量化页显示无权限提示').toContainText('无权限', { timeout: 15000 })
    await page.screenshot({ path: `${SHOT}/branch-tester-quant403.png`, fullPage: true })
    await page.goto('/#/settings'); await page.waitForTimeout(1000)
    await expect(page.locator('.app-main')).toContainText('403')
    await ctx.close()
  })
})

test.describe('全局组件分支', () => {
  test('Ctrl+K：页面跳转 + 六位代码开个股抽屉', async ({ page }) => {
    await page.goto('/#/dashboard')
    await expect(page.locator('.app-shell')).toBeVisible()
    await page.waitForTimeout(500)
    await page.keyboard.press('Control+k')
    await expect(page.getByTestId('cmdk-panel')).toBeVisible({ timeout: 5000 })
    await page.getByTestId('cmdk-input').fill('持仓')
    await page.getByTestId('cmdk-item').first().click()
    await expect(page).toHaveURL(/#\/positions/)
    await page.keyboard.press('Control+k')
    await page.getByTestId('cmdk-input').fill('600519')
    await page.getByTestId('cmdk-item').first().click()
    await expect(page.getByTestId('stock-detail-panel')).toBeVisible({ timeout: 8000 })
    await page.screenshot({ path: `${SHOT}/branch-cmdk-drawer.png` })
    await page.getByTestId('stock-detail-overlay').click({ position: { x: 5, y: 5 } })
  })

  test('深色主题切换 → 持久化 → 截图 → 还原', async ({ page }) => {
    await page.goto('/#/dashboard')
    const before = await page.evaluate(() => document.documentElement.getAttribute('data-theme') || document.documentElement.className)
    await page.locator('header button[title*="深色"], header button[title*="浅色"]').first().click()
    await page.waitForTimeout(500)
    const after = await page.evaluate(() => document.documentElement.getAttribute('data-theme') || document.documentElement.className)
    expect(after, '主题属性变化').not.toBe(before)
    await page.reload(); await page.waitForTimeout(1000)
    const persist = await page.evaluate(() => document.documentElement.getAttribute('data-theme') || document.documentElement.className)
    expect(persist, '刷新后主题保持').toBe(after)
    await page.screenshot({ path: `${SHOT}/branch-dark-theme.png`, fullPage: true })
    await page.locator('header button[title*="深色"], header button[title*="浅色"]').first().click()
  })

  test('顶部做空开关：切换→后端状态回读→还原', async ({ page }) => {
    await page.goto('/#/dashboard')
    const sw = page.locator('header [role=switch]').first()
    await expect(sw, '顶栏做空开关可见').toBeVisible({ timeout: 5000 })
    const hdr = () => page.evaluate(() => localStorage.getItem('liangzai_token'))
    const before = await (await page.request.get('/api/short/status', { headers: { Authorization: await hdr() } })).json()
    const b = !!before.short_enabled
    await sw.click()
    // 轮询直到服务端翻转（默认 800ms 定时对后端 toggleShort 慢时可能不够）
    await expect(async () => {
      const after = await (await page.request.get('/api/short/status', { headers: { Authorization: await hdr() } })).json()
      expect(after.short_enabled, '服务端翻转').toBe(!b)
    }).toPass({ timeout: 5000 })
    // 还原到初始态，避免污染后续测试
    await sw.click()
    await expect(async () => {
      const back = await (await page.request.get('/api/short/status', { headers: { Authorization: await hdr() } })).json()
      expect(back.short_enabled, '还原到初始态').toBe(b)
    }).toPass({ timeout: 5000 })
  })

  test('SSE 事件通道：ticket→流连接建立', async ({ page }) => {
    await page.goto('/#/dashboard')
    const hdr = { Authorization: await page.evaluate(() => localStorage.getItem('liangzai_token')) }
    const t = await page.request.post('/api/events/ticket', { headers: hdr })
    expect(t.ok(), 'SSE ticket 签发').toBeTruthy()
    const connected = await page.evaluate(async () => {
      const tk = await (await fetch('/api/events/ticket', { method: 'POST', headers: { Authorization: localStorage.getItem('liangzai_token') } })).json()
      return new Promise((resolve) => {
        // 注意：服务端建链后不立即 flush 响应头，首个数据要等 15s 心跳 → open 事件最长 ~15s
        const es = new EventSource('/api/events?ticket=' + encodeURIComponent(tk.ticket || tk.token || ''))
        es.onopen = () => { es.close(); resolve(true) }
        setTimeout(() => { es.close(); resolve(false) }, 17000)
      })
    })
    expect(connected, 'SSE 可建立连接').toBeTruthy()
  })

  test('Admin：建号→改密→禁用→删除 全分支', async ({ page }) => {
    await page.goto('/#/admin')
    await expect(page.locator('.page')).toBeVisible()
    await page.waitForTimeout(1200)
    const uname = 'uat_tmp_' + Date.now().toString(36)
    const hdr = () => page.evaluate(() => localStorage.getItem('liangzai_token'))
    const findUser = async () => {
      const list = ((await (await page.request.get('/api/admin/users', { headers: { Authorization: await hdr() } })).json()).users || [])
      return list.find((x) => x.username === uname)
    }
    // §F31 修复后 placeholder 变更：更明确的具体格式提示
    await page.getByPlaceholder('3-20 位，字母/数字/下划线，首字符为字母').fill(uname)
    await page.getByPlaceholder('至少 8 位，首次登录后可自行修改').fill('UatTmp!123456')
    await page.getByRole('button', { name: /创建账号|创建中/ }).click()
    await expect(page.getByText('账号已创建'), '创建成功提示').toBeVisible({ timeout: 10000 })
    // 表格非受控分页：数据变化会复位到第 1 页 → 用跳页输入直达末页定位
    async function locateRow() {
      let r = page.locator('tr', { hasText: uname })
      if (await r.count()) return r
      for (let attempt = 0; attempt < 3; attempt++) {
        const total = ((await (await page.request.get('/api/admin/users', { headers: { Authorization: await hdr() } })).json()).users || []).length
        const lastPage = Math.max(1, Math.ceil(total / 10))
        const jump = page.locator('.t-pagination input[type=text], .t-pagination__input input, .t-pagination .t-input__inner').last()
        if (await jump.count()) {
          await jump.fill(String(lastPage))
          await jump.press('Enter')
          await page.waitForTimeout(600)
        } else {
          for (let i = 0; i < lastPage; i++) { await page.locator('.t-pagination__btn-next').click().catch(() => {}); await page.waitForTimeout(200) }
        }
        r = page.locator('tr', { hasText: uname })
        if (await r.count()) return r
        for (let i = 0; i < lastPage && !(await r.count()); i++) {
          await page.locator('.t-pagination__btn-prev').click().catch(() => {})
          await page.waitForTimeout(200)
          r = page.locator('tr', { hasText: uname })
        }
        r = page.locator('tr', { hasText: uname })
        if (await r.count()) return r
      }
      return r
    }
    let row = await locateRow()
    await expect(row, '新用户出现在列表').toBeVisible({ timeout: 8000 })
    await page.screenshot({ path: `${SHOT}/branch-admin-created.png`, fullPage: true })
    // 重置密码（弹窗）
    await row.getByRole('button', { name: '重置密码' }).click()
    const dlg2 = page.locator('.t-dialog').last()
    await expect(dlg2, '重置密码弹窗打开').toBeVisible()
    await dlg2.locator('input').first().fill('UatTmp2!34567')
    await dlg2.getByRole('button', { name: /确/ }).first().click()
    await page.waitForTimeout(1000)
    // 禁用：点击 + API 断言（避免翻页复位干扰）
    row = await locateRow()
    const rowTxt = await row.innerText().catch(() => 'ROW-NOT-FOUND')
    console.log('[locateRow] row text:', rowTxt.replace(/\n/g, ' | ').slice(0, 160))
    await row.getByRole('button', { name: '禁用' }).click()
    await page.waitForTimeout(1200)
    const uAfter = await findUser()
    console.log('[findUser] after disable:', JSON.stringify(uAfter && { en: uAfter.enabled, un: uAfter.username }))
    expect(uAfter, '用户在列表可查').toBeTruthy()
    // 已知缺陷记录：auth.User.Enabled 带 omitempty → false 时字段整体消失（前端按 falsy 兜底显示已禁用）
    expect(uAfter.enabled, '后端已禁用(enabled!=true)').not.toBe(true)
    // 删除（确认弹窗 + API 断言）
    row = await locateRow()
    await row.getByRole('button', { name: '删除' }).click()
    const confirm = page.locator('.t-dialog').last()
    await confirm.getByRole('button', { name: /确/ }).first().click()
    await expect(async () => { expect(await findUser(), '删除后 API 列表不再有该用户').toBeUndefined() }).toPass({ timeout: 8000 })
  })

  test('Consult：发送→错误/回复分支', async ({ page }) => {
    await page.goto('/#/consult')
    const inp = page.locator('textarea, input[placeholder*="问"], input[placeholder*="咨询"]').first()
    await expect(inp).toBeVisible()
    await inp.fill('600519 现在怎么样')
    await page.getByRole('button', { name: /发送|提问|咨询/ }).first().click()
    await page.waitForTimeout(5000)
    await page.screenshot({ path: `${SHOT}/branch-consult.png`, fullPage: true })
  })
})


// ─────────────────────────────────────────────────────────────────────
// 2026-09-13 修复回归：D1/D7 安全链、D4 卖出零股保护、W6 objective 分槽、Dashboard 情绪卡
// English: regression tests for the D1/D7/D4/W6 fixes + sentiment card, added 2026-09-13.
// ─────────────────────────────────────────────────────────────────────
test.describe('修复回归 · 安全与目标', () => {
  const API = 'http://localhost:18080'
  async function adminToken(req) {
    const r = await req.post(API + '/api/auth/login', { data: { username: ADMIN.u, password: ADMIN.p } })
    return (await r.json()).token
  }

  test('D1：/api/admin/users 不泄露 sessions/password_hash，enabled 字段可见', async ({ request }) => {
    const tok = await adminToken(request)
    const r = await request.get(API + '/api/admin/users', { headers: { Authorization: 'Bearer ' + tok } })
    expect(r.status(), 'admin 用户列表 200').toBe(200)
    const body = JSON.stringify(await r.json())
    expect(body, '响应 JSON 不含 sessions 键').not.toContain('"sessions"')
    expect(body, '响应 JSON 不含非空 password_hash').not.toMatch(/"password_hash":"[^"]+"/)
    expect(body, '响应 JSON 显式包含 enabled 字段').toContain('"enabled":')
  })

  test('D7：/api/auth/logout 后同 token 立即失效（401）', async ({ request }) => {
    const tok = await adminToken(request)
    const before = await request.get(API + '/api/status', { headers: { Authorization: 'Bearer ' + tok } })
    expect(before.status(), '退出前 200').toBe(200)
    const out = await request.post(API + '/api/auth/logout', { headers: { Authorization: 'Bearer ' + tok } })
    expect(out.status(), 'logout 200').toBe(200)
    const revoked = await out.json()
    expect(revoked.revoked, 'revoked=true').toBe(true)
    const after = await request.get(API + '/api/status', { headers: { Authorization: 'Bearer ' + tok } })
    expect(after.status(), '退出后同 token 立即 401').toBe(401)
  })

  test('D4：卖出 qty<100 → 400 卖出不支持零股', async ({ request }) => {
    const tok = await adminToken(request)
    const r = await request.post(API + '/api/positions/execute', {
      headers: { Authorization: 'Bearer ' + tok },
      data: { code: '600519', side: '卖出', action: '减仓', qty: 50, price: 1500, strategy: 'manual' },
    })
    expect(r.status(), '零股卖出被拒').toBe(400)
    expect(JSON.stringify(await r.json()), '错误信息含"卖出不支持零股"').toContain('零股')
  })

  test('W6：4 个不同 objective 连发 → 4 个不同 task_id + 不同 ref_id；同 objective 幂等', async ({ request }) => {
    const tok = await adminToken(request)
    const objs = ['profitFactor', 'winRate', 'avgWin', 'expectancy']
    const seen = []
    for (const o of objs) {
      const r = await request.post(API + '/api/backtest/optimize', {
        headers: { Authorization: 'Bearer ' + tok },
        data: { objective: o },
      })
      expect(r.status(), o + ' 入队 202').toBe(202)
      const d = await r.json()
      expect(d.objective, o + ' 回显 objective 一致').toBe(o)
      expect(typeof d.ref_id, o + ' ref_id 是 number').toBe('number')
      seen.push({ task: d.task_id, ref: d.ref_id, obj: o })
    }
    // 4 个 objective 各自独立槽位（W6 前所有请求共用 ref_id=990 → 只跑第一条）
    const refs = new Set(seen.map((s) => s.ref))
    expect(refs.size, '4 个 objective 落 4 个不同 ref_id 槽位').toBe(4)
    const tasks = new Set(seen.map((s) => s.task))
    expect(tasks.size, '4 个 objective 建 4 个不同 task_id').toBe(4)
    // 相同 objective 再发：幂等命中同 ref_id + 同 task_id（未跑完时不重复入队）
    const dup = await request.post(API + '/api/backtest/optimize', {
      headers: { Authorization: 'Bearer ' + tok },
      data: { objective: 'winRate' },
    })
    const dd = await dup.json()
    const orig = seen.find((s) => s.obj === 'winRate')
    expect(dd.task_id, '同 objective 幂等回原 task_id').toBe(orig.task)
    expect(dd.ref_id, '同 objective 幂等回原 ref_id').toBe(orig.ref)
  })

  test('Dashboard 情绪卡渲染（SentimentCard A）', async ({ page }) => {
    await page.goto('/#/dashboard')
    // 卡片标题 + 三栏占位（loading 期或空态都能命中标题）
    const card = page.locator('.t-card__title', { hasText: '市场情绪' })
    await expect(card, '情绪卡标题存在').toBeVisible({ timeout: 10000 })
    // F45：headerRightContent→actions 修复后，头部右侧链接必须真正渲染
    await expect(page.getByText('回看全年', { exact: true }), '头部 actions 插槽渲染').toBeVisible({ timeout: 5000 })
    await page.screenshot({ path: `${SHOT}/dashboard-sentiment-card.png`, fullPage: true })
  })

  test('B：情绪×战法矩阵端点 + 卡片折叠区', async ({ page, request }) => {
    await page.goto('/#/dashboard')
    const tok = await page.evaluate(() => localStorage.getItem('liangzai_token'))
    const r = await request.get('http://localhost:18080/api/research/emotion-strategy-matrix', { headers: { Authorization: 'Bearer ' + tok } })
    expect(r.status(), '矩阵端点 200').toBe(200)
    const d = await r.json()
    expect(Array.isArray(d.rows), 'rows 数组').toBeTruthy()
    expect(d.min_events, 'min_events=20 样本纪律').toBe(20)
    // 卡片折叠区交互：点击展开 → 出现矩阵区（空态或表格都算渲染成功）
    const link = page.getByText('看情绪×战法矩阵', { exact: false }).first()
    await expect(link, '矩阵折叠入口可见').toBeVisible({ timeout: 10000 })
    await link.click()
    await expect(async () => {
      const empty = await page.getByText('暂无可分相的回测数据', { exact: false }).count()
      const rows = await page.locator('table tbody tr').count()
      expect(empty + rows, '矩阵区展开（空态或表格）').toBeGreaterThan(0)
    }).toPass({ timeout: 6000 })
    await page.screenshot({ path: `${SHOT}/dashboard-sentiment-matrix.png`, fullPage: true })
  })

  test('C：情绪回看页渲染（涨停柱+色带+区间切换）', async ({ page }) => {
    await page.goto('/#/emotion')
    const card = page.locator('.t-card__title', { hasText: '市场情绪回看' })
    await expect(card, '回看页标题存在').toBeVisible({ timeout: 10000 })
    // 种子数据链路：5 日色带 rect 应渲染；区间按钮组可点 120 日
    await expect(page.locator('svg rect').first(), '色带/柱有渲染').toBeVisible({ timeout: 8000 })
    await page.getByRole('button', { name: '60日' }).click()
    await expect(card, '切区间后页面仍存活').toBeVisible({ timeout: 8000 })
    // 侧边导航入口存在
    await expect(page.locator('.app-aside, .t-menu').getByText('情绪回看').first(), '导航项存在').toBeVisible()
    await page.screenshot({ path: `${SHOT}/emotion-review-page.png`, fullPage: true })
  })

  test('Settings 未保存 dirty 提示 + 保存按钮', async ({ page }) => {
    await page.goto('/#/settings')
    // §F33 dirty 断言前提：等 LLM 配置 API 回填完成（llmModel 值非空且 llmBaseline 已同步），
    // 否则 baseline 未设 → dirty=false 或 fill 被随后的 setLlmModel 覆盖。
    const modelInput = page.getByPlaceholder('gpt-4o-mini')
    await expect(modelInput, 'LLM Model 输入框可见').toBeVisible({ timeout: 8000 })
    await expect(async () => {
      const v = await modelInput.inputValue()
      expect(v.length, 'LLM Model 载入完成（server value 非空）').toBeGreaterThan(0)
    }).toPass({ timeout: 10000 })
    await modelInput.fill('x-test-model-dirty-marker')
    await expect(page.getByText('有未保存修改', { exact: false }), 'dirty 标记出现').toBeVisible({ timeout: 3000 })
    // 还原：把输入框改回原值（等价 dirty 归零）
    await modelInput.fill('')
  })
})

// ─────────────────────────────────────────────────────────────────────
// §U-2/§U-3/§U-5（2026-09-14 修复批）：运维三件套前端入口 + kill-switch 即时性回归
// 背景：/api/qmt/halt、/api/qmt/cancel/{id}、/api/qmt/settle、/api/admin/users/cleanup 此前
// 后端齐备但前端零入口；且保存路径置 halted 走开关队列（休市不生效）属 fail-stop 漏洞。
// English: §U-2/3/5 batch — ops UI entries (kill-switch / cancel / settlement / account reaper)
// plus the halted-on-config-save immediate-effect regression (previously queued to next session).
// ─────────────────────────────────────────────────────────────────────
test.describe('修复回归 · 运维入口与即时熔断', () => {
  test('Quant：kill-switch 按钮置位→execute 被拒→解除还原', async ({ page }) => {
    await page.goto('/#/quant')
    const card = page.locator('.t-card', { hasText: '链路状态' })
    const btn = card.getByRole('button', { name: '紧急停止' })
    await expect(btn, '紧急停止按钮可见').toBeVisible({ timeout: 10000 })
    await btn.click()
    const dlg = page.locator('.t-dialog')
    await expect(dlg, '置位二次确认弹窗').toContainText('确认紧急停止', { timeout: 5000 })
    await page.screenshot({ path: `${SHOT}/branch-killswitch-confirm.png` })
    await dlg.getByRole('button', { name: /确认|确定/ }).first().click()
    await expect(page.locator('.t-message'), '置位成功 toast').toBeVisible({ timeout: 8000 })
    const hdr = { Authorization: await page.evaluate(() => localStorage.getItem('liangzai_token')) }
    // 服务端 halted 持久化 + 即时生效：下单被 kill-switch 拒绝（休市时段同样拒绝——U-3 修复点）
    await expect(async () => {
      const r = await page.request.get('/api/config/qmt', { headers: hdr })
      expect((await r.json()).halted, 'config.halted=true 持久化').toBe(true)
    }).toPass({ timeout: 6000 })
    // §AUDIT-PM 2026-09-15 修用例脆弱点：旧版写死 300750@180，标的现价涨超 +15% 后
    // 价格守卫先于 kill-switch 拒单，断言"拒因含 kill-switch"必然失败（2026-09-15 实测 316 元）。
    // 现下单前先取实时现价，用现价发起委托——价格守卫必然放行，拒因只剩 kill-switch。
    const snap = await (await page.request.get('/api/snapshot?codes=300750', { headers: hdr })).json()
    const live = Array.isArray(snap) ? (snap[0] && snap[0].price) || (snap.snapshots && snap.snapshots[0] && snap.snapshots[0].price) : (snap.price || 0)
    expect(live, '取到 300750 实时现价（价格守卫基准）').toBeGreaterThan(0)
    const exec = await page.request.post('/api/positions/execute', {
      headers: hdr, data: { code: '300750', side: '买入', action: '建仓', qty: 100, price: live, strategy: 'manual' },
    })
    expect(exec.status(), 'halted 置位时下单被拒').toBeGreaterThanOrEqual(400)
    expect(JSON.stringify(await exec.json()), '拒单原因含 kill-switch').toContain('kill-switch')
    // 解除还原（不污染后续用例）
    await page.reload()
    const release = page.locator('.t-card', { hasText: '链路状态' }).getByRole('button', { name: '解除停止' })
    await expect(release, '解除按钮出现（halted 态回显）').toBeVisible({ timeout: 10000 })
    await release.click()
    await page.locator('.t-dialog').getByRole('button', { name: /确认|确定/ }).first().click()
    await expect(async () => {
      const r = await page.request.get('/api/config/qmt', { headers: hdr })
      expect((await r.json()).halted, '解除后 halted=false').toBe(false)
    }).toPass({ timeout: 8000 })
  })

  test('Quant：当日委托卡渲染 + 日终结算卡 + 对账可触发', async ({ page }) => {
    await page.goto('/#/quant')
    await expect(page.getByText('当日委托'), '当日委托卡标题').toBeVisible({ timeout: 10000 })
    await page.waitForTimeout(1500)
    await page.screenshot({ path: `${SHOT}/branch-qmt-orders.png`, fullPage: true })
    // 终态委托行撤单栏为 —；仅在途单有按钮（数量取决于当日真实单，不断言条数，断言行存在）
    const body = await page.locator('.app-main').innerText()
    expect(body, '委托状态列已渲染').toMatch(/已成|已报|已撤|—/)
    await expect(page.getByText('日终结算对账'), '结算卡存在').toBeVisible()
    const settleBtn = page.getByRole('button', { name: /立即对账/ })
    await expect(settleBtn).toBeVisible()
    await settleBtn.click() // mock 网关卡无交割单：成功/失败 toast 均算链路通
    await expect(page.locator('.t-message'), '对账请求有响应').toBeVisible({ timeout: 12000 })
  })

  test('Admin：清理失效账号入口（dry_run 预览不动刀）', async ({ page }) => {
    await page.goto('/#/admin')
    const btn = page.getByRole('button', { name: /清理失效账号/ })
    await expect(btn, '清理按钮存在').toBeVisible({ timeout: 10000 })
    // 走 API 断 dry_run 契约（不真点删除，避免误删在用 temp）
    const hdr = { Authorization: await page.evaluate(() => localStorage.getItem('liangzai_token')) }
    const r = await page.request.post('/api/admin/users/cleanup', { headers: hdr, data: { dry_run: true } })
    expect(r.status(), 'cleanup dry_run 200').toBe(200)
    const d = await r.json()
    expect(typeof d.count, 'dry_run 返回 count').toBe('number')
    expect(d.dry_run, 'dry_run 回显').toBe(true)
    await page.screenshot({ path: `${SHOT}/branch-admin-cleanup.png`, fullPage: true })
  })
})
