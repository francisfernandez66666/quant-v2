// ── §SIDE-AUTH-2（2026-09-23 夜间批）实盘手动下单方向必填锁 ──
// 背景：后端 POST /api/positions/execute 旧实现对"没传方向"缺省成买入——残余 fail-open；
// 09-22 一笔真实卖出被方向猜测记成买入，回款/预算/已实现盈亏三本账全线污染。
// 后端已改"方向必填、缺即 400"，前端封装层 executeRealAction 同步只放行显式的
// 买入/卖出——本文件锁住"发出的请求一定带方向"这一不变量（Positions.jsx 手动下单
// 本就按用户点击显式传方向，这里防的是未来新增调用点漏传/序列化丢字段）。
// English: §SIDE-AUTH-2 — the manual-order wrapper must refuse to send any request whose
// side is not explicitly 买入/卖出, so the removed server-side default can never come back.
import { describe, it, expect, afterEach, vi } from 'vitest'
import * as api from '../api/index.js'

const ok = (body) => new Response(JSON.stringify(body), { status: 200 })

describe('executeRealAction 方向必填（§SIDE-AUTH-2）', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  for (const side of ['买入', '卖出']) {
    it(`显式「${side}」照常发出请求，且请求体带方向`, async () => {
      const f = vi.fn(async () => ok({ ok: true }))
      vi.stubGlobal('fetch', f)
      await api.executeRealAction({ code: '603468.SH', side, action: '清仓', qty: 100, price: 22.55 })
      expect(f).toHaveBeenCalledTimes(1)
      const body = JSON.parse(f.mock.calls[0][1].body)
      expect(body.side).toBe(side)
    })
  }

  // 反例族：缺键/空串/非规范值一律前端抛错，且**一个请求都不发**（后端 400 的镜像防线）
  for (const bad of [undefined, '', '   ', 'buy', 'SELL', '买']) {
    it(`方向 ${JSON.stringify(bad)} 被拦截且零外发`, async () => {
      const f = vi.fn(async () => ok({ ok: true }))
      vi.stubGlobal('fetch', f)
      const payload = { code: '603468.SH', action: '加仓', qty: 100, price: 22.55 }
      if (bad !== undefined) payload.side = bad
      await expect(api.executeRealAction(payload)).rejects.toThrow('方向')
      expect(f).not.toHaveBeenCalled()
    })
  }
})
