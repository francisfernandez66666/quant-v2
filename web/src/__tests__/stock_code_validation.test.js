// ── 自选代码校验（2026-10-24）──
// 与服务端 internal/server/handlers_fix.go::normalizeWatchStockCode 同源：
// 当前只开放 A 股股票；指数/基金/可转债先明确拒绝，避免被当股票写入自选。
import { describe, it, expect } from 'vitest'
import { normalizeStockCode } from '../utils/stockCode.js'

describe('自选股票代码校验', () => {
  const ok = [
    ['600519', '600519', 'SH'],
    ['600519.SH', '600519', 'SH'],
    ['sh600519', '600519', 'SH'],
    ['000001', '000001', 'SZ'],
    ['000001.SZ', '000001', 'SZ'],
    ['430047', '430047', 'BJ'],
    ['920001', '920001', 'BJ'],
  ]
  const bad = ['ZZZZZZ', '399001', '510300', '110043', '123456', '600519.SZ', '60051', '']

  it('合法股票/后缀归一', () => {
    for (const [input, code, market] of ok) {
      const r = normalizeStockCode(input)
      expect(r.error, input).toBe('')
      expect(r.code, input).toBe(code)
      expect(r.market, input).toBe(market)
    }
  })

  it('非法/未开放资产类型拒绝', () => {
    for (const input of bad) {
      const r = normalizeStockCode(input)
      expect(r.error, input).not.toBe('')
    }
  })
})
