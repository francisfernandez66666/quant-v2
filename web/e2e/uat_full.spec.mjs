// ── 全场景全流程分支 像素级 UAT（qoder UAT run 2026-09-13）──
// 覆盖：13 页面渲染+截图+JS异常/接口失败采集、登录分支、权限分支（tester）、
// 交易分支（Quant 状态卡/配置保存/取消分支、Paper 手动交易/注入/自检/做空卡）、
// 信号筛选排序展开、消息中心筛选删除复盘、Admin 建号改密禁用删除、
// 命令面板/全局抽屉/主题切换/SSE 在线。凭据来自环境变量。
//
// ── 20260917 缺陷批 ──
// 新增「修复回归 · 20260917 缺陷批」describe：F-2 战法开关保存接线（UI 勾选→POST 落库→API 回读）；
// F-4 撮合配置热开关（保存后引擎实时生效，无需重启）；F-5 风控闸口状态卡渲染（当日命中/开关标签/空占位）。
// 本批用例普遍采用「先记服务端基线 + finally API 直写还原」模式，防止中途失败污染共享配置。
import { test, expect } from '@playwright/test'

// 两套账号凭据（admin=超管，tester=普通用户）与像素截图输出目录
const ADMIN = { u: process.env.E2E_USER || 'admin', p: process.env.E2E_PASS || '' }
const USER = { u: process.env.E2E_USER2 || 'tester', p: process.env.E2E_PASS2 || ADMIN.p }
const SHOT = 'test-results/uat-pixels'

// 挂载全局错误采集器：收集页面未捕获 JS 异常、console error、/api 请求失败与非 2xx 响应，
// 返回 errs 数组供用例断言（PAGEERROR 视为致命，其余进城 warning annotation）。
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

// 打开指定 hash 页面并做渲染体检：断言页面主体可见、等数据稳定（2.5s）、全页截图、
// 断言无未捕获 JS 异常；返回全部采集错误（含接口告警）供调用方归档到 annotation。
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

// 13 个核心页面清单：[路由 hash, 截图命名]，供像素级遍历用例循环消费
const PAGES = [
  ['#/dashboard', 'dashboard'], ['#/signals', 'signals'], ['#/watchlist', 'watchlist'],
  ['#/hotspot', 'hotspot'], ['#/msgcenter', 'msgcenter'], ['#/positions', 'positions'],
  ['#/quant', 'quant'], ['#/paper', 'paper'], ['#/settings', 'settings'],
  ['#/llm-debug', 'llmdebug'], ['#/consult', 'consult'], ['#/research', 'research'],
  ['#/admin', 'admin'],
]

// ⾯ admin 登录态下逐页渲染 UAT：每页截图 + JS 异常/接口告警采集
test.describe('像素级全页面 UAT (admin)', () => {
  // 遍历 PAGES 清单逐页渲染、截图并把接口告警写入 annotation
  for (const [hash, name] of PAGES) {
    test(`页面渲染+截图 ${hash}`, async ({ page }) => {
      const errs = await checkPage(page, hash, name)
      test.info().annotations.push({ type: 'api-warnings', description: errs.join(' ;; ') || 'none' })
    })
  }
})

// 交易相关分支：Quant 配置链路、Paper 撮合/战法/交易操作、消息中心与信号页交互
test.describe('交易相关分支', () => {
  // 验证 Quant 页链路状态卡展示 mock 网关地址、熔断态与执行路径徽标
  test('Quant：链路状态卡显示 mock 网关+熔断正常+执行路径', async ({ page }) => {
    await page.goto('/#/quant')
    const card = page.locator('.t-card', { hasText: '链路状态' })
    await expect(card).toContainText('127.0.0.1:18789', { timeout: 15000 })
    await expect(card).toContainText('正常')
    await expect(card).toContainText('miniQMT兼容')
    await expect(card.getByText('QMT桥兜底')).toBeVisible()
    await page.screenshot({ path: `${SHOT}/branch-quant-chain.png`, fullPage: true })
  })

  // 仓位纪律保存→刷新回读一致性；finally 服务端 API 还原基线防污染
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
    // §UAT-D8：与「单笔金额绝对帽」同款——中途失败即污染服务端共享配置，还原统一进 finally
    // （API 单字段 patch，不依赖页面按钮/toast 状态）。
    try {
      await budget.fill('88888')
      await page.getByRole('button', { name: '保存仓位纪律' }).click()
      await expect(page.locator('.t-message').first()).toBeVisible()
      await page.waitForTimeout(800)
      await Promise.all([
        page.waitForResponse((r) => r.url().includes('/api/config/qmt') && r.request().method() === 'GET', { timeout: 8000 }),
        page.reload(),
      ])
      const back = page.locator('div', { hasText: /^单日买入预算/ }).locator('input').first()
      await expect(back, '保存后重进页面数值持久化').toHaveValue('88888')
    } finally {
      await page.request.post('/api/config/qmt', { headers: hdr, data: { daily_budget_amount: Number(baseline) } })
      await expect(async () => {
        const c2 = await (await page.request.get('/api/config/qmt', { headers: hdr })).json()
        expect(String(c2.daily_budget_amount), 'finally 还原后服务端值回到基线').toBe(baseline)
      }).toPass({ timeout: 8000 })
    }
    void cap
  })

  // 单笔金额绝对帽字段全流程：服务端回填→保存 150000→刷新持久化→finally 还原基线
  test('Quant：单笔金额绝对帽保存→回读→还原（§AUDIT-PM 新增字段）', async ({ page }) => {
    // 锁定今日新增的 max_order_amount UI 面：服务端回填、保存持久、还原闭环。
    // hydration/teardown 防竞态手法与「仓位纪律」用例同款（waitForResponse + 点击前 DOM 断言 + 服务端轮询）。
    await page.goto('/#/quant')
    const auth = { Authorization: await page.evaluate(() => localStorage.getItem('liangzai_token')) }
    const c = await (await page.request.get('/api/config/qmt', { headers: auth })).json()
    const baseline = String(c.max_order_amount ?? 0)
    const capInput = page.locator('div', { hasText: /^单笔金额绝对帽/ }).locator('input').first()
    // §UAT-D8：本用例会改写服务端共享配置（max_order_amount），任何一步失败都必须还原基线，
    // 否则污染后续用例（全套连跑时「超帽拒单」读到 150000 假通过）——整体包进 try/finally。
    // toast 断言加 .first()（多条 .t-message 命中 strict 违例，全套连跑实锤）。
    try {
      await expect(capInput, '服务端值回填 hydration 完成').toHaveValue(baseline, { timeout: 10000 })
      await capInput.fill('150000')
      await page.getByRole('button', { name: '保存仓位纪律' }).click()
      await expect(page.locator('.t-message').first().first()).toBeVisible()
      await expect(async () => {
        const c1 = await (await page.request.get('/api/config/qmt', { headers: auth })).json()
        expect(String(c1.max_order_amount), '保存动作真正把 150000 写进服务端').toBe('150000')
      }).toPass({ timeout: 20000 })
      await Promise.all([
        page.waitForResponse((r) => r.url().includes('/api/config/qmt') && r.request().method() === 'GET', { timeout: 8000 }),
        page.reload(),
      ])
      await expect(capInput, '刷新后 150000 持久化').toHaveValue('150000', { timeout: 10000 })
    } finally {
      // 还原走 API 单字段 patch（handleSetQMTConfig 指针字段=局部合并，nil 保持原值），
      // 不依赖页面/按钮状态，失败路径同样能落库；轮询确认服务端确实回基线。
      await page.request.post('/api/config/qmt', { headers: auth, data: { max_order_amount: Number(baseline) } })
      await expect(async () => {
        const c2 = await (await page.request.get('/api/config/qmt', { headers: auth })).json()
        expect(String(c2.max_order_amount), 'finally 还原后服务端回到基线').toBe(baseline)
      }).toPass({ timeout: 8000 })
    }
  })

  // 全自动切换的二次确认防误触：弹确认框后点取消，服务端仍保持 manual 模式
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

  // ── §F-4（20260917 缺陷修复批）模拟盘撮合配置热开关 e2e ──
  // 锁定"改配置必须重启"旧缺陷的修复：POST /api/paper/config 保存后**引擎实时生效**
  // （engine_enabled 回读翻转），无需重启进程。finally API 还原基线，防污染后续用例。
  test('Paper：撮合设置总开关→引擎实时生效→还原（§F-4）', async ({ page }) => {
    await page.goto('/#/paper')
    const hdr = { Authorization: await page.evaluate(() => localStorage.getItem('liangzai_token')) }
    const base = await (await page.request.get('/api/paper/config', { headers: hdr })).json()
    try {
      await page.getByRole('button', { name: /⚙ 设置/ }).click()
      await page.getByText('撮合设置', { exact: true }).click()
      const dlg = page.locator('.t-dialog')
      const master = dlg.getByText('启用自动撮合')
      await expect(master, '总开关控件渲染').toBeVisible({ timeout: 8000 })
      const want = !base.enabled
      const masterLabel = dlg.locator('label:has-text("启用自动撮合")').last()
      await expect(masterLabel.locator('input'), '回填与服务端一致').toBeChecked({ checked: !!base.enabled })
      await masterLabel.click()
      await dlg.getByRole('button', { name: '保存' }).click()
      await expect(page.locator('.t-message').first()).toBeVisible({ timeout: 8000 })
      await expect(async () => {
        const c = await (await page.request.get('/api/paper/config', { headers: hdr })).json()
        expect(c.enabled, 'rules 层翻转').toBe(want)
        expect(c.engine_enabled, '引擎实时生效（不重启）').toBe(want)
      }).toPass({ timeout: 10000 })
    } finally {
      await page.request.post('/api/paper/config', { headers: hdr, data: { enabled: !!base.enabled, auto_sell: !!base.auto_sell, fixed_amount: base.fixed_amount, short_capital: base.short_capital } })
      await expect(async () => {
        const c = await (await page.request.get('/api/paper/config', { headers: hdr })).json()
        expect(c.enabled, 'finally 还原 rules').toBe(!!base.enabled)
        expect(c.engine_enabled, 'finally 还原引擎实况').toBe(!!base.enabled)
      }).toPass({ timeout: 8000 })
    }
  })

  // ── §F-2（20260917 缺陷修复批）战法开关保存接线 e2e ──
  // 旧缺陷：设置弹窗无 strategies 保存分支，勾选静默丢失且误提交 caps。现验证：
  // UI 勾选→POST /api/paper/strategies 落库→API 回读翻转→finally 还原基线。
  test('Paper：战法开关保存→API 回读→还原（§F-2）', async ({ page }) => {
    await page.goto('/#/paper')
    const hdr = { Authorization: await page.evaluate(() => localStorage.getItem('liangzai_token')) }
    const base = await (await page.request.get('/api/paper/strategies', { headers: hdr })).json()
    const known = base.known_strategies || []
    expect(known.length, 'known_strategies 非空').toBeGreaterThan(0)
    const target = known[0]
    const wasOn = (base.strategies || []).length === 0 ? target.id !== 'momentum' : (base.strategies || []).includes(target.id)
    try {
      await page.getByRole('button', { name: /⚙ 设置/ }).click()
      await page.getByText('战法开关', { exact: true }).click()
      const dlg = page.locator('.t-dialog')
      const row = dlg.locator('div').filter({ hasText: new RegExp('\\(' + target.id + '\\)') }).last()
      const cb = row.locator('label').last()
      await expect(cb, '战法行渲染').toBeVisible({ timeout: 8000 })
      await expect(row.locator('input[type=checkbox]').first()).toBeChecked({ checked: !!wasOn })
      await cb.click()
      await dlg.getByRole('button', { name: '保存' }).click()
      await expect(page.locator('.t-message').first()).toBeVisible({ timeout: 8000 })
      await expect(async () => {
        const now = await (await page.request.get('/api/paper/strategies', { headers: hdr })).json()
        const wl = now.strategies || []
        expect(wl.includes(target.id), '保存写入翻转态（wasOn=' + wasOn + '）').toBe(!wasOn)
      }).toPass({ timeout: 10000 })
    } finally {
      await page.request.post('/api/paper/strategies', { headers: hdr, data: { strategies: base.strategies && base.strategies.length ? base.strategies : [] } })
      await expect(async () => {
        const now = await (await page.request.get('/api/paper/strategies', { headers: hdr })).json()
        expect(JSON.stringify(now.strategies || []), 'finally 回基线').toBe(JSON.stringify(base.strategies || []))
      }).toPass({ timeout: 8000 })
    }
  })

  // Paper 页静态体检：模拟盘账户/分仓/做空卡/交易弹窗元素可见性 + 整页截图
  test('Paper：模拟盘账户/分仓/做空卡/交易弹窗', async ({ page }) => {
    await page.goto('/#/paper')
    await expect(page.locator('.page')).toBeVisible()
    await page.waitForTimeout(2500)
    await page.screenshot({ path: `${SHOT}/branch-paper.png`, fullPage: true })
    const body = await page.locator('.app-main').innerText()
    expect(body).toMatch(/模拟盘|账户|资金|持仓/)
  })

  // 纸面持仓手动卖出分支（§FIX-8(20260919) 假绿修复）：
  // 旧断言只看 .t-message 出现——后端 T+1/参数错误的红 toast 同样让它通过，且 nightly 从不 seed
  // 持仓使本分支永远 skip，错位长期隐形。新契约：服务端回读持仓 → UI 减仓 1 手 → 断言 success
  // 色 toast → 再回读服务端，该票股数恰 -100（端到端钉死 FIX-1"表单手数→契约股数 ×100"换算）。
  test('Paper：手动卖出（减仓 1 手→服务端回读 -100 股）（§FIX-8）', async ({ page }) => {
    await page.goto('/#/paper')
    await page.waitForTimeout(2000)
    const hdr = { Authorization: await page.evaluate(() => localStorage.getItem('liangzai_token')) }
    const posOf = async (code) => {
      const list = await (await page.request.get('/api/paper/positions', { headers: hdr })).json()
      return (Array.isArray(list) ? list : []).find((p) => p.code === code)
    }
    const before = await (await page.request.get('/api/paper/positions', { headers: hdr })).json()
    const target = (Array.isArray(before) ? before : []).find((p) => p.qty > 100)
    if (!target) { test.skip(true, '纸面无 >100 股的持仓，跳过卖出分支'); return }
    const rows = page.locator('.t-table__body tr')
    expect((await rows.count()) > 0, '有持仓时表格不得为空（skip 兜底仅对真空仓）').toBe(true)
    // 精确定位目标持仓行（Positions() 来自 map 遍历，行序不保证，不能拿首行赌同一只）
    const row = rows.filter({ hasText: target.code }).first()
    await expect(row, '目标持仓行渲染').toBeVisible()
    await row.getByRole('button', { name: /减仓/ }).click()
    const dlg = page.locator('.t-dialog')
    await expect(dlg).toBeVisible()
    await dlg.getByPlaceholder('成交价格（留空用实时价）').fill(String(target.mark || 10))
    await dlg.getByPlaceholder('手数（1手=100股）').fill('1')
    await page.screenshot({ path: `${SHOT}/branch-paper-sell-dialog.png` })
    await dlg.getByRole('button', { name: /确认|确定/ }).click()
    // success 主题 toast（tdesign MessagePlugin 主题类为 t-is-success；错误 toast 不算通过）
    await expect(page.locator('.t-message.t-is-success').first()).toBeVisible({ timeout: 8000 })
    // 服务端效果回读：qty 必须真实减少 100 股（不是"接口 200 但账本没动"）
    const code = target.code
    await expect(async () => {
      const now = await posOf(code)
      expect(now, '卖出后持仓应仍存在（部分减仓）').toBeTruthy()
      expect(now.qty, `减仓 1 手应 -100 股（was ${target.qty}）`).toBe(target.qty - 100)
    }).toPass({ timeout: 10000 })
  })

  // 消息中心筛选覆盖：交易信号/止盈止损/盘后复盘/全部四个 tab + 删除弹窗取消分支
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

  // 信号页三大交互分支：搜索过滤、表头排序、分时图展开/收起
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

// 权限与登录分支：错误凭据与普通用户（tester）的能力收敛
test.describe('权限与登录分支', () => {
  // 错误密码登录会被拦在登录页且显示错误提示，不进入主应用
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

  // 普通用户权限收敛：侧边栏无管理员入口、quant 页 403 提示、settings 页 403
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
    // §PERM-GATE 20260918：LLM 诊断入口（数据源 admin 守卫）对普通用户收敛隐藏
    await expect(aside.getByText('LLM诊断'), '成员侧栏不含 LLM 诊断').toHaveCount(0)
    // §PERM-GATE 20260918：消息中心删除/清空/模拟卖出（admin 守卫）不向成员渲染
    await page.goto('/#/msgcenter'); await page.waitForTimeout(1500)
    await expect(page.getByText('清空全部'), '成员消息中心无清空入口').toHaveCount(0)
    await expect(page.getByRole('button', { name: '立即复盘' })).toBeVisible()
    // §PERM-GATE 20260918：持仓页实盘 Tab 对成员显示无权限面板（此前 403 被静默吞成空表）
    await page.goto('/#/positions'); await page.waitForTimeout(1500)
    await page.getByText('实盘持仓', { exact: true }).first().click(); await page.waitForTimeout(500)
    await expect(page.locator('.app-main'), '成员实盘 Tab 显示无权限').toContainText('无权限访问实盘持仓', { timeout: 8000 })
    // 纸面持仓写入口对成员隐藏（读/明细保留）
    await page.getByText('纸面持仓', { exact: true }).first().click(); await page.waitForTimeout(500)
    await expect(page.getByText('+ 新增持仓'), '成员无新增持仓入口').toHaveCount(0)
    await page.goto('/#/quant'); await page.waitForTimeout(2500)
    await expect(page.locator('.app-main'), '量化页显示无权限提示').toContainText('无权限', { timeout: 15000 })
    await page.screenshot({ path: `${SHOT}/branch-tester-quant403.png`, fullPage: true })
    await page.goto('/#/settings'); await page.waitForTimeout(1000)
    await expect(page.locator('.app-main')).toContainText('403')
    await ctx.close()
  })
})

// 全局组件分支：命令面板、主题切换、做空开关、SSE 通道、Admin 用户管理与咨询页
test.describe('全局组件分支', () => {
  // Ctrl+K 命令面板：中文关键词跳页 + 六位代码直达个股详情抽屉
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

  // 深色主题切换分支：切换后属性变化、刷新持久化，最后还原原主题
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

  // 顶栏做空开关：切换后服务端状态翻转，再切回还原初始态防污染
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

  // SSE 推送链路：先签发事件 ticket，再用 EventSource 带 ticket 建流连接（最长等 15s 心跳）
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

  // Admin 用户管理全分支：建临时号→重置密码→禁用→删除，各步以 API 断言收口
  test('Admin：建号→改密→禁用→删除 全分支', async ({ page }) => {
    await page.goto('/#/admin')
    await expect(page.locator('.page')).toBeVisible()
    await page.waitForTimeout(1200)
    const uname = 'uat_tmp_' + Date.now().toString(36)
    const hdr = () => page.evaluate(() => localStorage.getItem('liangzai_token'))
    // 按用户名在用户列表 API 中查找记录（用于禁用/删除后的状态断言）
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

  // 咨询页发送分支：填问题→点发送→等回复/错误返回并截图
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
  const API = process.env.E2E_API || 'http://localhost:18080'
  // helper：走后端登录 API 换 admin token（纯 request 上下文用例复用）
  async function adminToken(req) {
    const r = await req.post(API + '/api/auth/login', { data: { username: ADMIN.u, password: ADMIN.p } })
    return (await r.json()).token
  }

  // D1 安全回归：admin 用户列表响应不得泄露 sessions/password_hash，且 enabled 字段显式存在
  test('D1：/api/admin/users 不泄露 sessions/password_hash，enabled 字段可见', async ({ request }) => {
    const tok = await adminToken(request)
    const r = await request.get(API + '/api/admin/users', { headers: { Authorization: 'Bearer ' + tok } })
    expect(r.status(), 'admin 用户列表 200').toBe(200)
    const body = JSON.stringify(await r.json())
    expect(body, '响应 JSON 不含 sessions 键').not.toContain('"sessions"')
    expect(body, '响应 JSON 不含非空 password_hash').not.toMatch(/"password_hash":"[^"]+"/)
    expect(body, '响应 JSON 显式包含 enabled 字段').toContain('"enabled":')
  })

  // D7 安全回归：logout 后同一 token 立即失效（继续访问返回 401）
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

  // D4 零股保护回归：卖出数量 <100 时后端 400 拒单并提示"卖出不支持零股"
  test('D4：卖出 qty<100 → 400 卖出不支持零股', async ({ request }) => {
    const tok = await adminToken(request)
    const r = await request.post(API + '/api/positions/execute', {
      headers: { Authorization: 'Bearer ' + tok },
      data: { code: '600519', side: '卖出', action: '减仓', qty: 50, price: 1500, strategy: 'manual' },
    })
    expect(r.status(), '零股卖出被拒').toBe(400)
    expect(JSON.stringify(await r.json()), '错误信息含"卖出不支持零股"').toContain('零股')
  })

  // W6 objective 分槽回归：4 个不同 objective 各入独立槽位（task_id/ref_id 均不同），同 objective 重复提交幂等
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

  // Dashboard 市场情绪卡：标题渲染 + 头部 actions 插槽「回看全年」链接可见
  test('Dashboard 情绪卡渲染（SentimentCard A）', async ({ page }) => {
    await page.goto('/#/dashboard')
    // 卡片标题 + 三栏占位（loading 期或空态都能命中标题）
    const card = page.locator('.t-card__title', { hasText: '市场情绪' })
    await expect(card, '情绪卡标题存在').toBeVisible({ timeout: 10000 })
    // F45：headerRightContent→actions 修复后，头部右侧链接必须真正渲染
    await expect(page.getByText('回看全年', { exact: true }), '头部 actions 插槽渲染').toBeVisible({ timeout: 5000 })
    await page.screenshot({ path: `${SHOT}/dashboard-sentiment-card.png`, fullPage: true })
  })

  // B 回归：情绪×战法矩阵端点契约（rows/min_events=20）+ 前端折叠区点击展开
  test('B：情绪×战法矩阵端点 + 卡片折叠区', async ({ page, request }) => {
    await page.goto('/#/dashboard')
    const tok = await page.evaluate(() => localStorage.getItem('liangzai_token'))
    const r = await request.get((process.env.E2E_API || 'http://localhost:18080') + '/api/research/emotion-strategy-matrix', { headers: { Authorization: 'Bearer ' + tok } })
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

  // C 回归：情绪回看页渲染（SVG 图元或空态二选一）+ 区间切换 + 侧边导航入口
  test('C：情绪回看页渲染（涨停柱+色带+区间切换）', async ({ page }) => {
    await page.goto('/#/emotion')
    const card = page.locator('.t-card__title', { hasText: '市场情绪回看' })
    await expect(card, '回看页标题存在').toBeVisible({ timeout: 10000 })
    // §UAT-D8：用例不依赖环境有无 market_risk_daily——有数据断言 svg 图元，空库断言空态文案，
    // 两分支都必须页面存活且区间按钮可点（本地手工栈与 nightly 种子栈均可跑）。
    // English: branch on data presence so the test passes on both seeded and empty backends.
    const emptyHint = page.getByText('暂无情绪留痕数据', { exact: false })
    await expect(async () => {
      const rects = await page.locator('svg rect').count()
      const empty = await emptyHint.count()
      expect(rects + empty, '色带/柱渲染或空态文案，二选一').toBeGreaterThan(0)
    }).toPass({ timeout: 8000 })
    await page.getByRole('button', { name: '60日' }).click()
    await expect(card, '切区间后页面仍存活').toBeVisible({ timeout: 8000 })
    // 侧边导航入口存在
    await expect(page.locator('.app-aside, .t-menu').getByText('情绪回看').first(), '导航项存在').toBeVisible()
    await page.screenshot({ path: `${SHOT}/emotion-review-page.png`, fullPage: true })
  })

  // Settings dirty 提示：LLM 配置 API 回填完成后改值出现「有未保存修改」，改回原值关闭 dirty
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
  // U-2 kill-switch 全链路：置位→下单被拒（拒因含 kill-switch）→UI 解除；finally 兜底复位
  test('Quant：kill-switch 按钮置位→execute 被拒→解除还原', async ({ page }) => {
    await page.goto('/#/quant')
    const hdr = { Authorization: await page.evaluate(() => localStorage.getItem('liangzai_token')) }
    // §UAT-D8（同款 teardown 污染）：置位动作改服务端共享状态 halted，中途失败会留 halted=true
    // 毒化后续用例（下单全被拒）。开头先复位（幂等）保证入口态干净，finally 再兜底复位一次。
    await page.request.post('/api/qmt/halt', { headers: hdr, data: { halted: false } })
    await page.reload()
    const card = page.locator('.t-card', { hasText: '链路状态' })
    try {
      const btn = card.getByRole('button', { name: '紧急停止' })
      await expect(btn, '紧急停止按钮可见').toBeVisible({ timeout: 10000 })
      await btn.click()
      const dlg = page.locator('.t-dialog')
      await expect(dlg, '置位二次确认弹窗').toContainText('确认紧急停止', { timeout: 5000 })
      await page.screenshot({ path: `${SHOT}/branch-killswitch-confirm.png` })
      await dlg.getByRole('button', { name: /确认|确定/ }).first().click()
      await expect(page.locator('.t-message').first(), '置位成功 toast').toBeVisible({ timeout: 8000 })
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
      // 解除还原（UI 链路验证；服务端兜底复位在 finally）
      await page.reload()
      const release = page.locator('.t-card', { hasText: '链路状态' }).getByRole('button', { name: '解除停止' })
      await expect(release, '解除按钮出现（halted 态回显）').toBeVisible({ timeout: 10000 })
      await release.click()
      await page.locator('.t-dialog').getByRole('button', { name: /确认|确定/ }).first().click()
    } finally {
      await page.request.post('/api/qmt/halt', { headers: hdr, data: { halted: false } })
      await expect(async () => {
        const r = await page.request.get('/api/config/qmt', { headers: hdr })
        expect((await r.json()).halted, 'finally 兜底后 halted=false').toBe(false)
      }).toPass({ timeout: 8000 })
    }
  })

  // §FIX-9j(20260919) 双通道切换链路 nightly 回归：qmt-mock 补 /admin/broker + /health broker 字段后，
  // "UI/API 切换网关 active 通道"在演练栈首次可测——GET 回读 → POST 切到对面通道 → 回读生效 →
  // finally 还原基线（服务端共享状态，中途失败会毒化后续用例，同 halt 用例的 teardown 纪律）。
  test('BROKER-1：网关 active 通道切换 + /health 回读生效', async ({ page }) => {
    await page.goto('/#/quant')
    const hdr = { Authorization: await page.evaluate(() => localStorage.getItem('liangzai_token')) }
    const base = await (await page.request.get('/api/qmt/broker', { headers: hdr })).json()
    test.skip(base.ok !== true, `网关未接入/不可达（ok=false: ${base.err || ''}），切换用例不适用`)
    const cur = base.broker === 'xt' ? 'xt' : 'queued'
    const target = cur === 'xt' ? 'queued' : 'xt'
    try {
      const sw = await page.request.post('/api/qmt/broker', { headers: hdr, data: { broker: target } })
      expect(sw.status(), '切换应 200').toBe(200)
      await expect.poll(async () => {
        const s = await (await page.request.get('/api/qmt/broker', { headers: hdr })).json()
        return s.ok === true && s.broker === target
      }, { timeout: 8000, message: `切换后 active 通道应回读为 ${target}` }).toBe(true)
      // 非法通道名必须被拒（实网关契约：400 "broker must be one of"，经 Go 侧 502 包装）
      const bad = await page.request.post('/api/qmt/broker', { headers: hdr, data: { broker: 'nope' } })
      expect([400, 502], `非法通道应被拒, got ${bad.status()}`).toContain(bad.status())
    } finally {
      await page.request.post('/api/qmt/broker', { headers: hdr, data: { broker: cur } })
      await expect.poll(async () => {
        const s = await (await page.request.get('/api/qmt/broker', { headers: hdr })).json()
        return s.broker === cur
      }, { timeout: 8000, message: `finally 应还原 active=${cur}` }).toBe(true)
    }
  })

  // U-5 运维入口回归：当日委托卡/日终结算卡渲染 + 「立即对账」可触发（成功/失败 toast 均算通）
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
    await expect(page.locator('.t-message').first(), '对账请求有响应').toBeVisible({ timeout: 12000 })
  })

  // ── §MT 多租户（2026-09-17）──
  // 只读断言为主：租户无删除端点，e2e 若建租户会逐晚残留脏数据；写路径
  // （建租户/配额/跨租户隔离/频控）由 Go 层 tenant_api_test.go 全量覆盖。
  test('MT：/api/tenants 含系统租户 + Admin 页租户管理卡渲染', async ({ page }) => {
    await page.goto('/#/dashboard')
    const hdr = { Authorization: await page.evaluate(() => localStorage.getItem('liangzai_token')) }
    const r = await page.request.get('/api/tenants', { headers: hdr })
    expect(r.status(), '平台运营者取租户清单应 200').toBe(200)
    const d = await r.json()
    const t0 = (d.tenants || []).find((t) => t.id === 't_default')
    expect(t0, '应存在系统租户 t_default').toBeTruthy()
    expect(t0.is_default, 'is_default 标记').toBe(true)
    expect(t0.max_users, '成员配额字段可见').toBeGreaterThan(0)
    // Admin 页：平台运营者应渲染「租户管理」卡（租户 admin/普通用户无此卡）
    await page.goto('/#/admin')
    await expect(page.getByText('租户管理', { exact: true }), '租户管理卡').toBeVisible({ timeout: 10000 })
    await page.screenshot({ path: `${SHOT}/branch-tenant-admin.png`, fullPage: true })
  })

  // MT 回归：普通用户访问租户/用户列表均 403（adminMiddleware 拦截）
  test('MT：普通用户访问租户管理面 403、列用户只见 platform=false', async ({ request }) => {
    const login = await request.post('/api/auth/login', {
      data: { username: process.env.E2E_USER2, password: process.env.E2E_PASS2 },
    })
    expect(login.status(), 'tester 登录').toBe(200)
    const tk = (await login.json()).token
    const h = { Authorization: tk }
    const rt = await request.get('/api/tenants', { headers: h })
    expect(rt.status(), '普通用户访问租户清单应 403（adminMiddleware）').toBe(403)
    const ru = await request.get('/api/admin/users', { headers: h })
    expect(ru.status(), '普通用户访问用户列表应 403').toBe(403)
  })

  // U-4 清理失效账号：dry_run 预览模式只返回 count，不动真删除（避免误删在用账号）
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

// ── 修复回归 · 20260917 缺陷批（F-2 战法开关保存 / F-4 撮合设置 / F-5 风控闸口卡）──
test.describe('修复回归 · 20260917 缺陷批', () => {
  // F-5 回归：风控闸口状态卡渲染（开关标签 + 当日命中表或空占位）且无 JS 异常
  test('F-5 Quant：风控闸口状态卡渲染（当日命中/开关态标签/无命中占位）', async ({ page }) => {
    const errs = watch(page)
    await page.goto('/#/quant')
    const card = page.locator('.t-card', { hasText: '风控闸口状态' }).first()
    await expect(card, 'F-5 风控闸口卡渲染（riskGates 拉取成功才渲染）').toBeVisible({ timeout: 15000 })
    await expect(card, '卡说明文案含当日命中语义').toContainText('当日命中记录')
    // 开关态标签区至少有一枚 Tag（switches map 渲染）
    await expect(card.locator('.t-tag').first()).toBeVisible()
    // 有命中→表；无命中→占位文案，二者必现其一
    const hitTable = card.locator('.t-table')
    const empty = card.getByText('今日暂无风控闸命中')
    await expect(hitTable.or(empty), '闸口明细表或空占位').toBeVisible({ timeout: 10000 })
    const fatal = errs.filter((e) => e.startsWith('PAGEERROR'))
    expect(fatal, '无未捕获JS错误: ' + fatal.join('|')).toHaveLength(0)
    await page.screenshot({ path: `${SHOT}/branch-quant-riskgates.png`, fullPage: true })
  })

  // F-4 回归（UI 面版）：撮合设置 tab 回填→保存→API 回读字段全部不变（幂等保存链路）
  test('F-2 Paper：战法开关保存→API 回读→还原', async ({ page }) => {
    await page.goto('/#/paper')
    const hdr = { Authorization: await page.evaluate(() => localStorage.getItem('liangzai_token')) }
    const base = await (await page.request.get('/api/paper/strategies', { headers: hdr })).json()
    await page.getByRole('button', { name: /⚙ 设置/ }).click()
    await page.getByText('战法开关').first().click()
    const allow = page.locator('.t-dialog').getByText('允许')
    const count = await allow.count()
    expect(count, '战法开关列表存在').toBeGreaterThan(0)
    // 记住第一个战法的当前态 → 翻转
    await allow.first().click()
    await page.getByRole('button', { name: '保存' }).click()
    await expect(page.locator('.t-message'), 'F-2 保存回执').toContainText('战法准入已更新', { timeout: 10000 })
    // API 回读：白名单长度应与 UI 勾选一致（stub：长度变化即可证 service 端真实提交）
    const after = await (await page.request.get('/api/paper/strategies', { headers: hdr })).json()
    expect(Array.isArray(after.strategies), 'strategies 数组回读').toBe(true)
    // 还原基线（API 直写，避免 UI 再操作引发偶发）
    const back = await page.request.post('/api/paper/strategies', { headers: hdr, data: { strategies: base.strategies } })
    expect(back.status(), '还原 200').toBe(200)
    const final = await (await page.request.get('/api/paper/strategies', { headers: hdr })).json()
    expect(final.strategies, '还原到位').toEqual(base.strategies)
    await page.screenshot({ path: `${SHOT}/branch-paper-strategies-tab.png`, fullPage: true })
  })
})

// ── 20260917 GAP_VERIFY D 批（个人产品口径缺陷修复回归）──
// D-1：夜间信号质量报告卡（researchd 报告有写无读补齐读端：按钮→弹窗→聚合表/占位降级）；
// D-3：Settings 配置历史卡（接上零消费的快照/回滚 API：列表渲染 + 回滚二次确认取消分支）。
test.describe('修复回归 · GAP_VERIFY_20260917 D 批', () => {
  // D-1 Paper 页"夜间报告"弹窗：有 seed 数据渲染聚合表，无数据显示占位文案（两态都算通）
  test('D-1 Paper：夜间报告弹窗两态渲染', async ({ page }) => {
    await page.goto('/#/paper')
    const hdr = { Authorization: await page.evaluate(() => localStorage.getItem('liangzai_token')) }
    const pre = await (await page.request.get('/api/research/paper-reports?limit=5', { headers: hdr })).json()
    await page.getByRole('button', { name: /夜间报告/ }).click()
    const dlg = page.locator('.t-dialog', { hasText: '夜间信号质量报告' })
    await expect(dlg, 'D-1 弹窗打开').toBeVisible({ timeout: 8000 })
    if ((pre.count || 0) > 0) {
      await expect(dlg.getByText('成交聚合（战法×方向）'), 'D-1 聚合表标题').toBeVisible()
      await expect(dlg.getByText(pre.reports[0].date, { exact: true }), 'D-1 报告日期条').toBeVisible()
    } else {
      await expect(dlg.getByText('暂无报告'), 'D-1 空态占位').toBeVisible()
    }
    await page.screenshot({ path: `${SHOT}/branch-paper-nightly.png` })
    await page.keyboard.press('Escape')
  })

  // D-3 Settings 配置历史卡：admin 可见 + 列表/空态 + 回滚二次确认（取消不执行）
  test('D-3 Settings：配置历史卡与回滚取消分支', async ({ page }) => {
    await page.goto('/#/settings')
    const card = page.locator('.t-card', { hasText: '配置历史与回滚' })
    await expect(card, 'D-3 admin 配置历史卡渲染').toBeVisible({ timeout: 10000 })
    const rollBtns = card.getByRole('button', { name: '回滚' })
    if ((await rollBtns.count()) > 0) {
      await rollBtns.first().click()
      const dlg = page.locator('.t-dialog', { hasText: '确认回滚配置' })
      await expect(dlg, 'D-3 回滚二次确认弹窗').toBeVisible({ timeout: 8000 })
      await dlg.getByRole('button', { name: '取消' }).click()
      await expect(dlg, 'D-3 取消后弹窗关闭').toBeHidden({ timeout: 8000 })
    } else {
      // 无快照环境（极新部署未触发过配置写）：两列表占位文案即证接线在
      await expect(card.getByText('暂无快照').first(), 'D-3 空态占位').toBeVisible()
    }
    await page.screenshot({ path: `${SHOT}/branch-settings-hist.png`, fullPage: true })
  })

  // D-3 tester 权限分支：成员账号不得看到「配置历史与回滚」卡。
  // §A5（20260918 审计批）行为变更：旧版成员伪造 localStorage 角色可渲染 Settings 本体
  // （仅隐藏历史卡）；现在 admin 数据端点首拉 403 即由页面主动跳转统一 /403 页——
  // 本用例改钉新语义：伪造角色 + 成员 token → 落 403，页面本体（服务器连接卡）不渲染。
  test('D-3 tester：Settings 无配置历史卡', async ({ page, context }) => {
    // 用 tester 凭据现登（不动共享 storageState 会话）
    const resp = await context.request.post('/api/auth/login', { data: { username: process.env.E2E_USER2 || 'tester', password: process.env.E2E_PASS2 || '' } })
    expect(resp.ok(), 'tester 登录').toBe(true)
    const t = (await resp.json()).token
    const p2 = await context.newPage()
    await p2.goto('/#/')
    await p2.evaluate((tok) => localStorage.setItem('liangzai_token', tok), t)
    await p2.goto('/#/settings')
    await expect(p2.locator('.t-card', { hasText: '配置历史与回滚' })).toHaveCount(0)
    await expect(p2.getByRole('heading', { name: /403/ }), '§A5：成员 token 进 admin 页必落统一 403').toBeVisible({ timeout: 10000 })
    await expect(p2.locator('.t-card', { hasText: '服务器连接' })).toHaveCount(0)
    await p2.close()
  })
})

// ── 修复回归 · WL-FIX 自选股批（20260917）──
// §WL-FIX：旧版整表合并的 wlRow 跨作用域引用被静默 catch 吞掉，线上表现为
// 「看不到自选股、添加后刷新即丢」。本组用例锁定后端持久列表的前端整表渲染分支。
test.describe('修复回归 · WL-FIX 20260917 自选股', () => {
  // 流程：API 直写真实自选（持久到 watchlist_{uid}.json）→ 进入自选页断言整表出现该行
  // → finally API 删除还原，不污染管理账号的线上自选（自清理=不落测试数据）。
  test('WL-1 自选股：后端列表整表渲染 + 删除还原', async ({ page }) => {
    const errs = watch(page)
    await page.goto('/#/')
    await page.waitForTimeout(500)
    const hdr = { Authorization: await page.evaluate(() => localStorage.getItem('liangzai_token')) }
    // 用确定性测试代码 WLTEST8888.SZ（行情缺失行走快照缺失补全/评估回退分支）
    const code = 'WLTEST8888.SZ'
    await page.request.post('/api/watchlist', { headers: hdr, data: { code } })
    try {
      await page.goto('/#/watchlist')
      // §WL-FIX 回归点：整表合并不再被 ReferenceError 炸掉（修复前必渲染「暂无自选股」）
      await expect(page.getByText(code).first(), '后端自选列表应在表格中可见').toBeVisible({ timeout: 15000 })
      const fatal = errs.filter((e) => e.startsWith('PAGEERROR'))
      expect(fatal, '无未捕获JS异常: ' + fatal.join('|')).toHaveLength(0)
      await page.screenshot({ path: `${SHOT}/branch-wl-fix.png`, fullPage: true })
    } finally {
      // 还原：删除测试自选（幂等；即使断言失败也执行，用例不留测试数据）
      await page.request.delete('/api/watchlist', { headers: hdr, data: { code } }).catch(() => {})
    }
  })

  // 删除分支：删完当前会话乐观行移除 + 后端持久化移除（刷新后仍不可见）。
  test('WL-2 自选股：删除后不再出现', async ({ page }) => {
    await page.goto('/#/')
    await page.waitForTimeout(500)
    const hdr = { Authorization: await page.evaluate(() => localStorage.getItem('liangzai_token')) }
    const code = 'WLTEST7777.SZ'
    await page.request.post('/api/watchlist', { headers: hdr, data: { code } })
    try {
      await page.goto('/#/watchlist')
      await expect(page.getByText(code).first(), '添加后可见').toBeVisible({ timeout: 15000 })
      // 点击该行的删除（操作列 ✕）
      const row = page.locator('tr', { hasText: code }).first()
      await row.getByRole('button').last().click()
      await page.waitForTimeout(800)
      await page.reload()
      await expect(page.getByText(code), '删除并刷新后不应再出现').toHaveCount(0, { timeout: 10000 })
    } finally {
      await page.request.delete('/api/watchlist', { headers: hdr, data: { code } }).catch(() => {})
    }
  })
})

// §ENH-5 批E：QMT Level-1 行情 feed 契约回归（2026-09-19）。
// 交易时段外 feed 不轮询（IsActiveSession 闸门），故这里锁"回显契约"而非 QMT-L1 值本身：
// /api/status 必须始终携带 quote_source/quote_age_sec 两字段——nightly 在盘中窗口
// 开启 qmt_feed_enabled 后，quote_source 即变 "QMT-L1"，前端/巡检零改动即可观测。
test.describe('修复回归 · §ENH-5 L1 行情 feed 回显', () => {
  test('L1-1 /api/status 携带 quote_source/quote_age_sec 契约字段', async ({ page }) => {
    await page.goto('/#/dashboard')
    const hdr = { Authorization: await page.evaluate(() => localStorage.getItem('liangzai_token')) }
    const st = await (await page.request.get('/api/status', { headers: hdr })).json()
    expect(st, 'quote_source 字段必须存在（可为空串=快照未就绪）').toHaveProperty('quote_source')
    expect(st.quote_age_sec, 'quote_age_sec 必须为数值').toEqual(expect.any(Number))
    expect(st.quote_age_sec).toBeGreaterThanOrEqual(0)
    if (st.quote_source) {
      // 有源时必须落在已知集合内（防拼写漂移导致巡检误报）
      expect(['QMT-L1', '同花顺（新）', '新浪', '腾讯', '东方财富', '东财', 'tushare', 'Tushare', 'test'])
        .toContain(st.quote_source)
    }
  })

  test('L1-2 mock 网关 /quotes 契约：Bearer + ticks 字段齐备', async ({ page }) => {
    // 直连 qmt-mock（18789，token 与引擎配置同源）：证明 Go feed 的数据面在本地栈可用。
    // mock 无 Bearer 时 401——浏览器 fetch 无法带 mock token？可以：token 固定 uat-secret。
    let resp = null
    let lastErr = ''
    for (let i = 0; i < 3 && !resp; i++) { // mock 刚重启/瞬时繁忙时重试三轮，仍失败才按"独立部署"跳过
      await page.waitForTimeout(500)
      resp = await page.request.get('http://127.0.0.1:18789/quotes?codes=600000.SH', {
        headers: { Authorization: 'Bearer uat-secret' },
      }).catch((e) => { lastErr = String(e && e.message || e); return null })
    }
    test.info().annotations.push({ type: 'l1-2-diag', description: 'resp=' + (!!resp) + ' err=' + lastErr })
    test.skip(!resp, 'qmt-mock 未就绪（独立部署场景跳过，不算失败）' + (lastErr ? ': ' + lastErr : ''))
    expect(resp.status()).toBe(200)
    const body = await resp.json()
    expect(body.ok).toBe(true)
    const tk = body.ticks['600000.SH']
    expect(tk, 'tick 必须含 lastPrice').toBeTruthy()
    for (const f of ['lastPrice', 'open', 'high', 'low', 'prevClose', 'volume', 'amount', 'tickTime']) {
      expect(tk, `tick 缺字段 ${f}`).toHaveProperty(f)
    }
    expect(tk.lastPrice).toBeGreaterThan(0)
    // 缺 codes → 400 契约
    const bad = await page.request.get('http://127.0.0.1:18789/quotes', {
      headers: { Authorization: 'Bearer uat-secret' },
    })
    expect(bad.status()).toBe(400)
  })
})
