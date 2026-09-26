// ── §0926E2E-W1C（2026-09-26 全量审计批）手动单"现价不可得显式确认"前端锁 ──
// 背景：后端 /api/positions/execute 旧实现对"拉不到实时现价"直接跳过 ±15% 偏离校验放行
// ——行情故障=错价可成交（真钱路径上的数据缺口放水）。现改显式确认制：未确认 400
// （文案固定含「无法获取实时现价」），带 confirm_no_quote 才受理并落 opslog。
// 本文件两层：①封装层字段透传（confirm_no_quote 必须原样进请求体，否则弹窗确认后仍被拒）；
// ②Positions.jsx 接线锁（源扫描）：错误识别、二次确认弹窗、确认后带确认位重发、
// 重发复用同一幂等键——缺一即形态回退，锁红。
// English: §0926E2E-W1C — wrapper must pass confirm_no_quote through, and Positions.jsx
// must keep the detect → confirm → retry-with-same-client_id wiring.
import { describe, it, expect, afterEach, vi } from 'vitest'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import * as api from '../api/index.js'

// HERE 口径与 m13_forbidden_poll.test.jsx 同款（jsdom 下 new URL 相对基址解析会脱 file: 方案）
const HERE = path.dirname(fileURLToPath(import.meta.url))

const ok = (body) => new Response(JSON.stringify(body), { status: 200 })

describe('executeRealAction 透传 confirm_no_quote（§0926E2E-W1C）', () => {
  afterEach(() => { vi.unstubAllGlobals() })

  it('带确认位的请求原样落进请求体，方向必填防线不受影响', async () => {
    const f = vi.fn(async () => ok({ ok: true }))
    vi.stubGlobal('fetch', f)
    await api.executeRealAction({ code: '600000.SH', side: '卖出', action: '清仓', qty: 100, price: 9.9, confirm_no_quote: true })
    const body = JSON.parse(f.mock.calls[0][1].body)
    expect(body.confirm_no_quote).toBe(true)
    expect(body.side).toBe('卖出')
  })

  it('未带确认位时请求体不得凭空出现 confirm_no_quote（防默认置真）', async () => {
    const f = vi.fn(async () => ok({ ok: true }))
    vi.stubGlobal('fetch', f)
    await api.executeRealAction({ code: '600000.SH', side: '买入', action: '加仓', qty: 100, price: 9.9 })
    const body = JSON.parse(f.mock.calls[0][1].body)
    expect(body.confirm_no_quote).toBeUndefined()
  })
})

describe('Positions.jsx 行情不可得二次确认接线（§0926E2E-W1C 源锁）', () => {
  const src = fs.readFileSync(path.join(HERE, '..', 'pages', 'Positions.jsx'), 'utf8')

  it('识别后端固定前缀「无法获取实时现价」后才走确认分支', () => {
    expect(src).toContain('无法获取实时现价')
  })
  it('二次确认弹窗+确认后带 confirm_no_quote 重发', () => {
    expect(src).toMatch(/DialogPlugin\.confirm\(/)
    expect(src).toMatch(/confirm_no_quote:\s*true/)
  })
  it('一次确认只生成一次幂等键（重试复用同 client_id，防双单）', () => {
    // client_id 在提交流程里以变量形式传入 payload，而非让封装层每次调用各自生成
    expect(src).toMatch(/client_id:\s*clientID/)
    expect(src).toMatch(/crypto\.randomUUID\(\)/)
  })
  it('用户取消二次确认=静默未下单，不得误报"下单失败"', () => {
    expect(src).toContain('__no_quote_cancel__')
  })
})
