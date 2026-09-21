// ── §SELLPOINT-UNIFY（2026-09-21）卖出裁决留痕的展示映射回归锁 ──
// 锁 owner 拍板语义在前端不被翻译错：卖出统一裁决（stage=sell_discipline）与买入准入裁定
// 共用同一条留痕环，若沿用旧买入口径（block→拦截、其余→待确认），卖出「窗结算处置」
// 会被显示成"待确认"——用户据此判断"该卖的没卖"，误读即误操作。
// 本测试锁四件事：
//  1. 卖出 pass → 「处置」（danger），绝不出现"待确认"；
//  2. 卖出 hold → 「观察窗预警」（触线进窗/未触线利空预警共用）；
//  3. 买入 block/hold 旧口径原样保留（拦截/影子拦截/待确认）；
//  4. 环节列按 stage 区分买入/卖出，未知 verdict 兜底不崩。
import { describe, it, expect } from 'vitest'
import { verdictDisplay, STAGE_SELL } from '../pages/quantVerdicts.js'

describe('§SELLPOINT-UNIFY 裁定留痕展示映射', () => {
  it('卖出 pass 显示「处置」，不得回落成买入的"待确认"', () => {
    const d = verdictDisplay({ stage: STAGE_SELL, verdict: 'pass', reason: '止损窗结算无做多信号，止损离场' })
    expect(d.stageLabel).toBe('卖出')
    expect(d.label).toBe('处置')
    expect(d.label).not.toBe('待确认')
    expect(d.theme).toBe('danger')
  })

  it('卖出 hold 显示「观察窗预警」（warning）', () => {
    const d = verdictDisplay({ stage: STAGE_SELL, verdict: 'hold' })
    expect(d.stageLabel).toBe('卖出')
    expect(d.label).toBe('观察窗预警')
    expect(d.theme).toBe('warning')
  })

  it('买入 block/hold 旧口径不变：拦截/影子拦截/待确认', () => {
    expect(verdictDisplay({ stage: 'strategy', verdict: 'block' }).label).toBe('拦截')
    expect(verdictDisplay({ stage: 'strategy', verdict: 'block', shadow: true }).label).toBe('影子拦截')
    const hold = verdictDisplay({ stage: 'confirm', verdict: 'hold' })
    expect(hold.stageLabel).toBe('买入')
    expect(hold.label).toBe('待确认')
  })

  it('异常输入兜底：未知 verdict/空行不抛错', () => {
    expect(() => verdictDisplay({ stage: STAGE_SELL, verdict: 'weird' })).not.toThrow()
    expect(verdictDisplay({ stage: STAGE_SELL, verdict: 'weird' }).stageLabel).toBe('卖出')
    expect(verdictDisplay(undefined).stageLabel).toBe('买入')
    expect(verdictDisplay({}).label).toBe('放行')
  })
})
