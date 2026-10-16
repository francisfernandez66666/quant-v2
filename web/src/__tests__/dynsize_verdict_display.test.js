// ── §DYN-SIZE（2026-10-16）动态仓位定权的展示映射回归锁 ──
// 锁三件事：
//  1. 定权硬拦（stage=sizing, hold）显示「定权硬拦」，不得与持续性探针的「待确认」混淆
//     （两者都在 hold，但一个是"风险因子命中"，一个是"还没观察够时间"——误读即误判）；
//  2. 缩放列：>1 加仓、<1 缩仓、未定权（0/1.0）显示空串（表格渲染「—」）；
//  3. sizing_reasons 拼进 tooltip，未知输入不抛错。
import { describe, it, expect } from 'vitest'
import { verdictDisplay, sizingDisplay, STAGE_SIZING } from '../pages/quantVerdicts.js'

describe('§DYN-SIZE 定权裁定展示映射', () => {
  it('定权硬拦（stage=sizing, hold）显示「定权硬拦」，与「待确认」区分', () => {
    const d = verdictDisplay({ stage: STAGE_SIZING, verdict: 'hold', reason: '动态仓位硬拦(情绪冰点)' })
    expect(d.stageLabel).toBe('买入')
    expect(d.label).toBe('定权硬拦')
    expect(d.label).not.toBe('待确认')
    expect(d.theme).toBe('danger')
  })

  it('定权环节的 pass 仍显示「放行」', () => {
    expect(verdictDisplay({ stage: STAGE_SIZING, verdict: 'pass' }).label).toBe('放行')
  })

  it('未定权（buy_scale 缺失 / 0 / 1.0）→ 空文案', () => {
    expect(sizingDisplay({}).label).toBe('')
    expect(sizingDisplay({ buy_scale: 0 }).label).toBe('')
    expect(sizingDisplay({ buy_scale: 1 }).label).toBe('')
    expect(sizingDisplay(undefined).label).toBe('')
  })

  it('缩仓 / 微缩 / 加仓三档文案与主题', () => {
    expect(sizingDisplay({ buy_scale: 0.5 }).label).toBe('缩仓 50%')
    expect(sizingDisplay({ buy_scale: 0.5 }).theme).toBe('warning')
    expect(sizingDisplay({ buy_scale: 0.9 }).label).toBe('微缩 90%')
    expect(sizingDisplay({ buy_scale: 1.15 }).label).toBe('加仓 115%')
    expect(sizingDisplay({ buy_scale: 1.15 }).theme).toBe('primary')
  })

  it('sizing_reasons 拼进 tooltip，未知输入不抛错', () => {
    const s = sizingDisplay({ buy_scale: 0.35, sizing_reasons: ['情绪相位×0.50', '大盘跌幅×0.70'] })
    expect(s.tip).toBe('情绪相位×0.50 · 大盘跌幅×0.70')
    expect(() => sizingDisplay({ buy_scale: 'x', sizing_reasons: null })).not.toThrow()
    expect(sizingDisplay({ buy_scale: 'x' }).label).toBe('')
  })

  // ── §DYN-SIZE P2 卖出加权 ──
  it('卖出加权（sell_boost）显示档位，悬停看触发条件', () => {
    const s = sizingDisplay({ sell_boost: 'L2', sizing_reasons: ['情绪相位=退潮', '风险档=Red'] })
    expect(s.label).toBe('卖出加权 L2')
    expect(s.theme).toBe('danger')
    expect(s.tip).toBe('情绪相位=退潮 · 风险档=Red')
  })

  it('卖出加权影子期标注「影子」前缀（只观察不下发）', () => {
    const s = sizingDisplay({ sell_boost: 'L2', shadow: true, sizing_reasons: ['[shadow]拟加权'] })
    expect(s.label).toBe('影子卖出加权 L2')
  })

  it('卖出加权优先于买入缩放（同一行两字段不会同时出现，防御性取加权）', () => {
    const s = sizingDisplay({ sell_boost: 'L1', buy_scale: 0.5 })
    expect(s.label).toBe('卖出加权 L1')
  })

  it('无 sell_boost 的卖出裁决行仍按买入缩放口径渲染', () => {
    expect(sizingDisplay({ stage: 'sell_discipline', buy_scale: 0.5 }).label).toBe('缩仓 50%')
    expect(sizingDisplay({ stage: 'sell_discipline' }).label).toBe('')
  })
})
