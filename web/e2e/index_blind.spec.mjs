// ── §INDEX-BLIND（2026-11-02）大盘指数多源腿与盲区指认的 E2E 契约 ──
//
// 为什么要有这个文件而不是只留 Go 单测：Go 用例证明"函数对"，证不了"卡上看得见"。
// 11-02 那天的现网形态恰恰是——代码全对、MS 被封顶 0.50、T 全天关闭，
// 而**页面上只有"关键读数缺失"六个字**，看不出掉的是哪一维、也看不出点位是谁给的。
// 本文件把三件事钉成端到端断言：
//   ① 写面契约：三枚指数键出厂开（值为 true，不是 null）且都归"保存即生效"，
//      并回显**实际**降级段序（不是按开关推算的第二份）；
//   ② 读面自洽：盲区态必须同时给出维度清单，且目标缩放被封顶（≤0.50）——
//      这两件事在代码里是同一个判定，端到端只看到其中一半就是链路断了；
//   ③ UI 契约：设置卡六枚勾选框 + 段序一行真的渲染出来，页面上不许出现 undefined/空标签。
//
// 词表纪律（§M1/§F4 的老账）：源名枚举一律读 qmt_gateway/contract/quote_sources.json，
// 本文件**不复制**任何一份中文/英文源名列表——复制一份就是漂移的病根。
// 凭据一律走环境变量（E2E_USER/E2E_PASS），不写死。
import { test, expect } from '@playwright/test'

const INDEX_KEYS = ['index_fallback_sources', 'index_breaker_scope', 'quote_feed_index_enabled']

// tokenOf 取本会话 token（storageState 已由 auth.setup 预置，避免各自登录撞 5/min 频控）。
async function tokenOf(page) {
  return page.evaluate(() => localStorage.getItem('liangzai_token'))
}

test('§INDEX-BLIND ① runtime 写面：三枚指数键出厂开、属热更类，并回显实际段序', async ({ page }) => {
  await page.goto('/#/')
  const hdr = { Authorization: await tokenOf(page) }
  const resp = await page.request.get('/api/config/runtime', { headers: hdr })
  expect(resp.status(), 'GET /api/config/runtime 必须 200').toBe(200)
  const body = await resp.json()

  for (const k of INDEX_KEYS) {
    // 指针位的出厂态必须是布尔 true：下发原始指针时前端会收到 null，
    // 表现是"开关看起来坏了"，而日志里它其实开着——这形状 §161 已锁过，这里端到端再钉一次。
    expect(body[k], `出厂态 ${k} 应为 true（缺键=开）`).toBe(true)
    expect(body.hot_keys, `${k} 应点名在"保存即生效"名单里`).toContain(k)
  }
  // 段序两栏：declared（全部合法腿）与 actual（本轮真的依次试谁）都由后端一处给出，
  // spec 不复制任何词表。为什么不用快照 quote_source 的 golden 对：那是"个股价由谁给"的
  // 命名空间，指数腿是另一套语义（腾讯只做指数、不进个股快照）——11-02 混用过，
  // 红在"tencent 不在 golden 里"，而代码是对的。
  expect(Array.isArray(body.index_legs), 'index_legs 应为数组').toBe(true)
  expect(Array.isArray(body.index_legs_actual), 'index_legs_actual 应为数组').toBe(true)
  expect(body.index_legs_actual.length, '实际段序不许为空（空＝读面没接上段表）').toBeGreaterThan(0)
  for (const leg of body.index_legs_actual) {
    expect(body.index_legs, `实际段序里出现未声明的腿 ${leg}`).toContain(leg)
  }
  expect(body.index_legs, '声明集必须含券商腿（§A7-3 先落地，等 QMT）').toContain('QMT-L1')
  // 东财永远在段序里（它是价格口径的基准，谁排第一可变、它不可缺席）
  expect(body.index_legs_actual, '段序必须保留东财这一段').toContain('eastmoney')
})

test('§INDEX-BLIND ② funds/today 读面：盲区态必须同时给维度清单且缩放被封顶', async ({ page }) => {
  await page.goto('/#/')
  const hdr = { Authorization: await tokenOf(page) }
  const resp = await page.request.get('/api/funds/today', { headers: hdr })
  expect(resp.status(), 'GET /api/funds/today 必须 200').toBe(200)
  const body = await resp.json()
  const funds = body?.live?.funds
  if (!funds || !funds.at) {
    // 帧没落过（引擎刚起/非交易日）：如实报告为"本轮无可断言"，但不许伪造成通过——
    // 用 fixme 显式标记，跑完会在报表里看得见"这条今天没测到"。
    test.fixme(true, '资金帧当日未发布（state=pending），② 无可断言对象')
    return
  }
  // 点位来源必须是已声明的腿名之一（从写面拿声明集，spec 不写死词表）
  const cfgBody = await (await page.request.get('/api/config/runtime', { headers: hdr })).json()
  const declared = cfgBody.index_legs || []
  if (typeof funds.index_source === 'string' && funds.index_source !== '') {
    expect(declared, `index_source 出现未声明的腿名 ${funds.index_source}`).toContain(funds.index_source)
  }
  if (funds.missing_key_input) {
    expect(Array.isArray(funds.missing_key_dims) && funds.missing_key_dims.length > 0,
      '盲区态必须点名缺了哪一维（只有 bool 就是让值班的人猜）').toBe(true)
    for (const d of funds.missing_key_dims) {
      expect(typeof d).toBe('string')
    }
    // 封顶生效的现场证据：盲区 ⇒ MS 不可能高于 0.50（安全垫，不是可调参数）
    expect(funds.market_scale, '盲区态 MS 必须被封顶 0.50').toBeLessThanOrEqual(0.5001)
  } else {
    expect(funds.missing_key_dims == null || funds.missing_key_dims.length === 0,
      '非盲区却还挂着维度清单＝陈旧账没清').toBe(true)
  }
})

test('§INDEX-BLIND ③ 设置卡：六枚勾选框按生效值勾选，并渲染实际段序一行', async ({ page }) => {
  await page.goto('/#/settings')
  const hdr = { Authorization: await tokenOf(page) }
  const cfg = await (await page.request.get('/api/config/runtime', { headers: hdr })).json()
  const card = page.locator('div.t-card', { hasText: '行情通道（券商 L1）' }).first()
  await expect(card, '设置页应有「行情通道（券商 L1）」卡').toBeVisible({ timeout: 15000 })
  // 3 枚行情勾选 + 3 枚指数勾选；总开关 qmt_feed_enabled 属重启类，不在本卡
  await expect(card.locator('input[type=checkbox]'), '勾选框数=6（本批 3→6）').toHaveCount(6)
  const checked = await card.locator('input[type=checkbox]').evaluateAll((els) => els.map((e) => e.checked))
  // 与后端生效值**逐一对照**（而不是"全 true"）：将来谁把某枚键拨成 false 存过，
  // 全 true 的断言会假红，而对照断言仍然说真话。
  const want = [cfg.qmt_feed_primary, cfg.quote_per_code_source, cfg.quote_hub_consult,
    cfg.index_fallback_sources, cfg.index_breaker_scope, cfg.quote_feed_index_enabled].map(Boolean)
  expect(checked, '勾选框初值必须逐一对应后端生效值（从 false 起步会把出厂开悄悄关掉）').toEqual(want)
  await expect(card.getByText(/指数降级段序/), '卡上必须回显实际段序').toBeVisible()
  await expect(card.getByText(/undefined/), '卡上不许出现 undefined').toHaveCount(0)
})

test('§INDEX-BLIND ④ 仪表盘：盲区标签三态不出现空括号或 undefined', async ({ page }) => {
  await page.goto('/#/dashboard')
  await expect(page.locator('.app-shell')).toBeVisible({ timeout: 15000 })
  // 标签要么不渲染，要么带完整文案：空括号（"关键读数缺失（）"）说明 dims 取空却没退回原文案
  await expect(page.getByText(/关键读数缺失（）/), '盲区标签不许渲染成空括号').toHaveCount(0)
  await expect(page.getByText(/undefined/), '仪表盘不许出现 undefined').toHaveCount(0)
})
