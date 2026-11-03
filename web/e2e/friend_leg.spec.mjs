// ── §MULTI-ACCT-B 批5（2026-11-03）远端执行腿可观测面与「量化交易」权限位的 E2E 契约 ──
//
// 为什么要有这个文件而不是只留 Go 单测（与 §INDEX-BLIND 同一取向）：
//   Go 用例证的是"四态判据本身对"（friend_leg_state_test.go）与"规则注册上了"（alerter 侧），
//   证不了三件端到端才看得见的事：
//     ① `/api/qmt/state` 响应体里**真的有** friend_leg_state 这个键——批5 把它挂进 StateSnapshot
//        而不是新端点，一旦装配点漏填（§FUNDS-HEARTBEAT 那次"心跳装配缺 ctrl"就是同形事故），
//        前端拿到的恒是 undefined，四态文案静默退化成"永远不渲染"，比报错更难发现；
//     ② 第 24 条规则的值=2/3 分形文案在前端**各有各的一句话**——两条折成一句就是让人盲操作；
//     ③ equals-legacy 的承诺：运营那台单机（未配 remote_leg）看到的那行字必须与今天**逐字一致**，
//        一个字都不许多。这条只能从页面判，不能从 Go 判。
//
// 纪律：
//   - 全程只读。要腿状态就用 page.route 把 `/api/qmt/state` 打进用例（N-1 同款手法），
//     不改现网/CI 的 rules.qmt 配置——remote_leg 是"这台腿在不在"的事实，不是测试夹具；
//   - 词表不复制：告警规则总数、必推数一律从后端读面取，spec 只点名本批那一条；
//   - 凭据走环境变量（E2E_USER/E2E_PASS、E2E_USER2/E2E_PASS2），缺失即显式失败不伪造通过。
import { test, expect } from '@playwright/test'

const API = process.env.E2E_API || 'http://localhost:18080'
const ADMIN = { u: process.env.E2E_USER, p: process.env.E2E_PASS }
const USER = { u: process.env.E2E_USER2, p: process.env.E2E_PASS2 }

// tokenOf 取本会话 token（storageState 已由 auth.setup 预置，避免各自登录撞 5/min 频控）。
async function tokenOf(page) {
  return page.evaluate(() => localStorage.getItem('liangzai_token'))
}

// loginToken 用指定账号现换一把 token（权限面用例需要"另一种身份"，不能复用 admin 会话）。
async function loginToken(request, username, password) {
  const r = await request.post(API + '/api/auth/login', { data: { username, password } })
  expect(r.status(), `${username} 登录必须 200`).toBe(200)
  const tk = (await r.json()).token
  expect(tk, `${username} 登录响应含 token`).toBeTruthy()
  return tk
}

// gotoWithLegState 把一份"远端腿"快照喂给真实轮询链路：只覆盖本批那一个读数，
// 其余字段照现网形状给（enabled/mode/tripped/last_probe_ok），避免 mock 自己造出新形态。
// remote_leg 是"这台腿在不在"的事实，不该由用例去改服务端配置来造档位——所以走 route 拦截。
async function gotoWithLegState(page, state, extra = {}) {
  const body = {
    enabled: true, mode: 'auto', tripped: false, last_probe_ok: true,
    friend_leg_state: state, ...extra,
  }
  await page.route('**/api/qmt/state', (route) => route.fulfill({
    status: 200, contentType: 'application/json', body: JSON.stringify(body),
  }))
  await page.goto('/#/dashboard')
}

test('§FRIEND-LEG ① 读面契约：/api/qmt/state 带 friend_leg_state 且与 enabled 自洽', async ({ page }) => {
  await page.goto('/#/')
  const hdr = { Authorization: await tokenOf(page) }
  const resp = await page.request.get('/api/qmt/state', { headers: hdr })
  expect(resp.status(), 'GET /api/qmt/state 必须 200').toBe(200)
  const body = await resp.json()
  // 键必须存在：批5 走快照而不是新端点，装配点漏填时这里是 undefined，
  // 表现是"页面少一句话"而不是报错——这正是 §FUNDS-HEARTBEAT 那族静默装配事故。
  expect(Object.prototype.hasOwnProperty.call(body, 'friend_leg_state'),
    '响应体必须含 friend_leg_state（缺键＝装配点没把它写进 StateSnapshot）').toBe(true)
  expect([0, 1, 2, 3], `腿状态只能是 0/1/2/3 四档，读到 ${body.friend_leg_state}`).toContain(body.friend_leg_state)
  // 自洽方向只有一条是结构性的：实盘整体未启用 ⇒ 这台机器上没有远端腿，必须恒 0。
  // 反向（enabled=true 时是几）取决于 remote_leg 键，CI/现网运营机都是缺省 false ⇒ 0，
  // 所以这里不许把"0"写死成断言（真起第一根远端腿那天它会变成 1，那不是红）。
  if (!body.enabled) {
    expect(body.friend_leg_state, '实盘未启用却报出远端腿档位＝没腿也报警（会制造无人认领的必推）').toBe(0)
  }
})

test('§FRIEND-LEG ② 权限面：匿名 401、无位成员 403、admin 200（any-perms 中间件真拦得住）', async ({ page, request }) => {
  // 匿名：结构性不能读——不是"读了报错"而是连门都进不来（B-N4 同取向）。
  const anon = await request.get(API + '/api/qmt/state')
  expect(anon.status(), '未登录读实盘状态必须 401').toBe(401)

  if (!USER.u || !USER.p) {
    test.fail(true, '§FRIEND-LEG ② 需要 E2E_USER2/E2E_PASS2（无位成员那一档），缺失时不许伪通过')
    return
  }
  // tester 在 nightly 栈里是 role=user 且没有任何严格权限位 ⇒ 既无 trading_admin 也无 quant_trade
  const tk = await loginToken(request, USER.u, USER.p)
  const asUser = await request.get(API + '/api/qmt/state', { headers: { Authorization: 'Bearer ' + tk } })
  expect(asUser.status(), '无 trading_admin/quant_trade 位的成员读实盘状态必须 403').toBe(403)

  // admin（批 5 迁移后持五位的账号）走的是同一条路由的任一-perm 分支 ⇒ 200。
  const adminTk = await loginToken(request, ADMIN.u, ADMIN.p)
  const asAdmin = await request.get(API + '/api/qmt/state', { headers: { Authorization: 'Bearer ' + adminTk } })
  expect(asAdmin.status(), '持有 trading_admin 的账号必须能读到（403 才是问题）').toBe(200)
})

test('§FRIEND-LEG ③ 告警腿在册：第 24 条规则的五元组逐字对', async ({ page }) => {
  await page.goto('/#/')
  const hdr = { Authorization: await tokenOf(page) }
  const resp = await page.request.get('/api/metrics/alerts', { headers: hdr })
  expect(resp.status(), 'GET /api/metrics/alerts 必须 200（ops_admin）').toBe(200)
  const body = await resp.json()
  const rules = body.rules || []
  // 总数等值：本仓的纪律是"改告警面必须同批改被等值锁盯着的四处文案"，
  // 夜跑这里就是那把尺子的运行态腿——代码静态锁（§106）绿而现网跑的是旧二进制，只有这条能抓。
  expect(rules.length, `告警规则总数应 24（实际 ${rules.length}）：加规则要同批同步 alert_routing 头注释与 main.go 两行`).toBe(24)
  const leg = rules.find((x) => x.name === 'friend_leg_unavailable')
  expect(leg, 'friend_leg_unavailable 必须在册').toBeTruthy()
  // 五元组逐字对，而不是"存在即可"：阈值 2 才是"不可达"，写成 1 会把健康腿报成事故；
  // For=300s 吃掉进程刚起尚未探测的短暂 2，去掉它会让每次重启都必推一条。
  expect(leg.metric, '量规名必须与 metrics.SetGauge 那一条同源').toBe('friend_leg_state')
  expect(leg.op).toBe('ge')
  expect(leg.threshold, '阈值=2（1 是健康，0 是不适用）').toBe(2)
  expect(leg.for, '持续窗 300s：启动初期还没探过会短暂读成 2').toBe('300s')
  expect(leg.level).toBe('p2')
  // 文案必须把 2 与 3 的处置方向分开写——折成"腿异常"一句就是让人去 ping 一条 ping 得通但不听命令的链路。
  expect(leg.message, '文案要点名值=3 的处置（钱还在对面手上）').toMatch(/值=3/)
  expect(leg.message, '文案必须声明本读数只报不断').toMatch(/只报不断/)
})

// 三态各占一条用例：同一个 page 里改 mock 档位再走一次，改的是 hash/轮询而不是文档，
// React 不重载 ⇒ 新档位要等下一次 15s 轮询才生效，15s 超时正好擦着边界假红（11-03 实跑撞到）。
// 拆成独立用例后每条页面都是新建的，首帧即拿到本档读数，断言与轮询节拍解耦。
test('§FRIEND-LEG ④a 仪表盘：值=1 只给中性文案，不串到不可达', async ({ page }) => {
  await gotoWithLegState(page, 1)
  await expect(page.getByText(/实盘链路：● 自动 正常 远端腿正常/), '值=1 报"远端腿正常"').toBeVisible({ timeout: 15000 })
  await expect(page.getByText(/远端腿不可达/), '值=1 不许串到不可达文案').toHaveCount(0)
})

test('§FRIEND-LEG ④b 仪表盘：值=2 给查链路方向，不串到撤销', async ({ page }) => {
  await gotoWithLegState(page, 2)
  await expect(page.getByText(/⚠远端腿不可达/), '值=2 报"不可达"并给查链路方向').toBeVisible({ timeout: 15000 })
  await expect(page.getByText(/远端腿撤销未触达/), '值=2 不许串到撤销文案').toHaveCount(0)
})

test('§FRIEND-LEG ④c 仪表盘：值=3 给停腿清待发方向，且与熔断各说各的', async ({ page }) => {
  const errs = []
  page.on('pageerror', (e) => errs.push('PAGEERROR:' + e.message))
  await gotoWithLegState(page, 3, { tripped: true, trip_reason: 'gateway disconnected' })
  await expect(page.getByText(/⚠远端腿撤销未触达/), '值=3 报"撤销未触达"').toBeVisible({ timeout: 15000 })
  await expect(page.getByText(/⚠熔断/), '熔断指示与腿状态各说各的，不许互相吞掉').toBeVisible({ timeout: 15000 })
  await expect(page.getByText(/undefined/), '系统行不许出现 undefined').toHaveCount(0)
  expect(errs, '无未捕获 JS 错误：' + errs.join('|')).toHaveLength(0)
})

test('§FRIEND-LEG ⑤ equals-legacy：值=0（运营单机形态）那行字与今天逐字一致，整页无「远端腿」', async ({ page }) => {
  // 这条是本批"缺省＝今天字节行为"承诺的唯一运行态证据：
  // 未配 remote_leg 的账号（现网运营机、CI 栈）不许多出一个字。
  await gotoWithLegState(page, 0)
  await expect(page.getByText('实盘链路：● 自动 正常'), '值=0 的那行文本必须与原样一致').toBeVisible({ timeout: 15000 })
  await expect(page.getByText(/远端腿/), '值=0 不加任何词（0＝这台机器没有远端腿）').toHaveCount(0)
})

test('§FRIEND-LEG ⑥ 反证腿：未定义的档位不许有任何一句话（证明分支不是 >= 形状）', async ({ page }) => {
  // 这条是三态用例唯一能自带的"有牙"证据：把三档里任一支写成范围比较（>=1、===0 兜底），
  // 这里就会冒出一句话而判红——而三态各自的正向断言只证"该说的说了"，证不了"不该说的没说"。
  // 值=4 不是本批的四态之一（读面①已把合法域钉成 0/1/2/3），出现它说明判据被改动过。
  await gotoWithLegState(page, 4)
  await expect(page.getByText(/实盘链路：● 自动 正常/), '基础那行照旧渲染').toBeVisible({ timeout: 15000 })
  await expect(page.getByText(/远端腿/), '非法档位不许被任何一档接住').toHaveCount(0)
})

test('§FRIEND-LEG ⑦ 契约不符只报不接单：读面三读数同现同缺，不许拿零值冒充已知', async ({ page }) => {
  await page.goto('/#/')
  const hdr = { Authorization: await tokenOf(page) }
  const resp = await page.request.get('/api/qmt/broker', { headers: hdr })
  expect(resp.status(), 'GET /api/qmt/broker 必须 200（未接入也回 200+ok:false）').toBe(200)
  const body = await resp.json()
  if (body.ok !== true) {
    // 未接入实盘/网关不可达：本批没给这条链新增任何字段，如实报"本轮无可断言对象"。
    test.fixme(true, '网关探测未成功（ok=false），⑥ 无契约读数可判')
    return
  }
  // contract_ok 是指针三态：键缺席＝这版网关没发这个字段（老网关/别的部署），**不许**当成"契约没问题"
  // （§0925EVE-W3-E 的"读不到不装已知"姿势）。有键则必须是布尔，且与 contract_fp 同现。
  const hasOK = Object.prototype.hasOwnProperty.call(body, 'contract_ok')
  const hasFP = Object.prototype.hasOwnProperty.call(body, 'contract_fp')
  expect(hasOK === hasFP, 'contract_ok 与 contract_fp 必须同现同缺（一半＝解析只接了一半）').toBe(true)
  if (hasOK) {
    expect(typeof body.contract_ok, 'contract_ok 必须是布尔（字符串"true"会让前端判据走偏）').toBe('boolean')
    expect(typeof body.contract_fp).toBe('string')
    expect(body.contract_fp.length, '有 contract_ok 就必须带指纹原值').toBeGreaterThan(0)
    // 只报不接单的结构性边界（批4 裁决）：契约不符**不碰**健康线与熔断，
    // 所以即便 contract_ok=false，这份双通道快照仍必须照常报出通道名与在线态——
    // 快照被契约判定带空就是"把观测面变成了数据面"，那正是批4 特意避开的形状。
    if (body.contract_ok === false) {
      expect(typeof body.broker, '契约不符不许把通道读数带空（只拒 /order 与 /cancel）').toBe('string')
      expect(typeof body.broker_connected, '契约不符不许把在线态带空').toBe('boolean')
    }
  }
})
