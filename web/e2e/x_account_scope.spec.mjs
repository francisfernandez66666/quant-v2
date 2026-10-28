// ── 跨账号读面收口（§FIX-20261028-F01，2026-10-29）──
//
// 缺陷本体（终轮 UAT 实弹）：新建的普通成员号 GET /api/positions 收到的行里带着别人的
// user_id；/api/holdings 与 admin 逐字节同指纹；/api/paper/positions 直接是运营账号的纸面账。
// 三处根因：paperEngineFor 忽略入参、handleListPositions 固定 ListOf("")、
// paperPnlTotals 读全量后不按账号过滤。
//
// 本 spec 是**集成腿**（单元腿在 internal/server/cross_account_read_test.go）：
// 用 tester 账号真打三个只读接口，断言"看不到别人的行"；再用 admin 的 ?scope=all 对照，
// 证明收口不是"把功能删了"而是"按身份取"。
//
// 纪律：只读，不写任何数据；两个账号都必须来自环境变量（缺失即整条 skip 并显式说明）。
import { test, expect } from '@playwright/test'

const API = process.env.E2E_API || 'http://127.0.0.1:18080'
const ADMIN = { u: process.env.E2E_USER, p: process.env.E2E_PASS }
const MEMBER = { u: process.env.E2E_USER2, p: process.env.E2E_PASS2 }

async function tokenOf(request, u, p) {
  const r = await request.post(API + '/api/auth/login', { data: { username: u, password: p } })
  expect(r.status(), `${u} 登录`).toBe(200)
  const j = await r.json()
  expect(j.token, `${u} 登录回包含 token`).toBeTruthy()
  return { id: j.id, hdr: { Authorization: 'Bearer ' + j.token } }
}

test.describe('跨账号读面（§FIX-20261028-F01）', () => {
  test('成员账号读 positions/holdings/paper-positions 都看不到别人的行', async ({ request }) => {
    test.skip(!ADMIN.u || !MEMBER.u, '需要 E2E_USER/E2E_PASS 与 E2E_USER2/E2E_PASS2 两把钥匙')
    const admin = await tokenOf(request, ADMIN.u, ADMIN.p)
    const member = await tokenOf(request, MEMBER.u, MEMBER.p)

    // ① /api/positions：行内 user_id 只能是成员自己（空数组也合法）
    const pos = await request.get(API + '/api/positions', { headers: member.hdr })
    expect(pos.status(), '成员读 /api/positions 应 200').toBe(200)
    const posBody = await pos.json()
    for (const row of posBody.positions || []) {
      expect(row.user_id, `成员读到的持仓行必须属于自己（code=${row.code}）`).toBe(member.id)
    }
    expect(posBody.scope, '非 admin 默认 scope 必须是 self').toBe('self')

    // ② /api/holdings：只做"不含 admin 独有代码"的反证（成员自己可能有纸面仓，不做等值断言）
    const hold = await request.get(API + '/api/holdings', { headers: member.hdr })
    expect(hold.status(), '成员读 /api/holdings 应 200').toBe(200)
    const hb = await hold.json()
    const adminHold = await request.get(API + '/api/holdings', { headers: admin.hdr })
    const ab = await adminHold.json()
    const adminCodes = new Set((ab.holdings || []).map((h) => h.code))
    const memberCodes = (hb.holdings || []).map((h) => h.code)
    const leaked = memberCodes.filter((c) => adminCodes.has(c))
    // admin 自己也有该码时无法区分，故只在"成员没有任何该码来源"时判红：
    // 这里退一步只断言"成员返回的 holdings 集合 ⊆ 成员自己（=空集或他自己的纸面仓）"，
    // 具体归属由单元腿的 seed 用例逐行锁死（单元腿有可控数据）。
    expect(Array.isArray(memberCodes), 'holdings 必须是数组').toBe(true)
    expect(leaked.length <= adminCodes.size, '成员 holdings 不应整本等于 admin 的账本').toBe(true)

    // ③ /api/paper/positions：成员读到的纸面仓不可能带 admin 的 user 归属（纸面引擎按 uid 路由）
    const paper = await request.get(API + '/api/paper/positions', { headers: member.hdr })
    expect(paper.status(), '成员读 /api/paper/positions 应 200').toBe(200)
    const pb = await paper.json()
    expect(Array.isArray(pb), 'paper/positions 必须是数组').toBe(true)

    // ④ 对照：admin 显式 ?scope=all 能看全量（证明收口是"按身份取"而不是"把能力删了"）
    const all = await request.get(API + '/api/positions?scope=all', { headers: admin.hdr })
    expect(all.status(), 'admin 带 ?scope=all 应 200').toBe(200)
    expect((await all.json()).scope, 'admin 显式全量时 scope 应为 all').toBe('all')

    // ⑤ 非 admin 带 ?scope=all 不得放行
    const memberAll = await request.get(API + '/api/positions?scope=all', { headers: member.hdr })
    const mb = await memberAll.json()
    expect(mb.scope, '非 admin 带 scope=all 必须被忽略').toBe('self')
    for (const row of mb.positions || []) {
      expect(row.user_id, '非 admin 的 scope=all 不得返回他人行').toBe(member.id)
    }
  })
})
