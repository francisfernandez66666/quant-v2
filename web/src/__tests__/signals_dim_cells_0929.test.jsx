// ── §0929DIM2 因子/形态战法的四维格口径（2026-09-29 全量评价批 · owner 裁决"待裁决清单 #6 后半"）──
//
// 缺陷原文（读码锤实，不是"两格空着"那么轻）：
//   ① D1~D4 四格一律 `toFixed(0)`。因子/形态战法在 §D1-D4 修复后把 Top-4 因子值/条件值写进
//      Meta 的 d1..d4，这些数常落在 0~1 ⇒ 0.62 显示成 "0"，界面在**报一个不存在的 0 分**；
//   ② 后端对单键总述战法（因子/形态/做空系）的 d3_desc/d4_desc 返回空串（无四维依据），
//      界面就只剩一个裸数字，谁看都像"这两维有分但没说是什么"。
// 裁决取向：不隐藏整列（四维是**一个合并列**，隐藏会连坐四个真有四维的战法）、
//          不换内容（因子名/阈值/样本量没随信号下发，换过去是空头承诺），
//          只做"如实标注 + 别把 0.62 报成 0"。
// 用例：H1~H3 数值与模式判据（纯函数）；H4~H6 真挂载渲染（有「不适用」的行数、
//       真四维行必须一条都没有＝等值锁 + 负向清零，防把修好的行也打上标注）。
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, cleanup, act } from '@testing-library/react'

const { state } = vi.hoisted(() => ({ state: { impl: {} } }))

// 一因子行（无四维依据、强度值 0.62）+ 一龙头行（四维齐、分数是整数）——两行同表渲染才叫对照。
const FACTOR_ROW = {
  code: '600001', name: '因子股', strategy: '因子复合', strategy_type: 'factor',
  strategy_id: 'fac_1', level: '交易', remind_level: 'observe', action: 'buy', price: 10,
  total_score: 82, can_open: false,
  d1: 0.62, d2: 0.41, d3: 0.33, d4: 0.12,
  d1_desc: '因子复合分82.00', d2_desc: '科技板块', d3_desc: '', d4_desc: '',
}
const DRAGON_ROW = {
  code: '600002', name: '龙头股', strategy: '龙头首板', strategy_type: 'dragon',
  strategy_id: 'dragon-1', level: '交易', remind_level: 'strong', action: 'buy', price: 11,
  total_score: 90, can_open: true,
  d1: 88, d2: 70, d3: 65, d4: 40,
  d1_desc: '贴板强度,封单占比', d2_desc: '板块共振,3家涨停', d3_desc: '超额收益,次日溢价', d4_desc: '5日趋势,均线多头',
}

function basePayloads(list) {
  return {
    isAdmin: () => false,
    getAccount: () => 'admin',
    fetchStatus: () => ({ session: 'test', signal_count: list.length }),
    setLastSession: () => {},
    connectSSE: () => {},
    fetchSignals: () => list,
    fetchPaperState: () => ({ enabled: false }),
    fetchShortStatus: () => ({ short_enabled: false }),
    fetchAlerts: () => [],
  }
}

vi.mock('../api/index.js', async () => {
  const actual = await vi.importActual('../api/index.js')
  const stubs = {}
  for (const name of Object.keys(basePayloads([]))) {
    stubs[name] = vi.fn(async (...args) => state.impl[name](...args))
  }
  return { ...actual, ...stubs }
})

import Signals, { dimValueText, dimNaMode } from '../pages/Signals.jsx'

describe('§0929DIM2 四维格显示口径', () => {
  beforeEach(() => {
    state.impl = basePayloads([FACTOR_ROW, DRAGON_ROW])
  })
  afterEach(() => {
    cleanup()
  })

  // H1 0~1 的强度原值不再被取整成 "0"；整数分维持原样（真四维分不受影响）
  it('H1 dimValueText：亚单位原值保两位、整数不变、null 走「—」', () => {
    expect(dimValueText(0.62)).toBe('0.62')
    expect(dimValueText(-0.5)).toBe('-0.50')
    expect(dimValueText(88)).toBe('88')
    expect(dimValueText(1)).toBe('1')
    expect(dimValueText(0)).toBe('0')
    expect(dimValueText(null)).toBe('—')
    expect(dimValueText(undefined)).toBe('—')
  })

  // H2 只有 D3 缺一维（真四维战法漏键）不打「不适用」——判据是两维同时无依据
  it('H2 dimNaMode：两维同缺才算该战法无四维，单缺不算', () => {
    expect(dimNaMode(FACTOR_ROW)).toBe(true)
    expect(dimNaMode(DRAGON_ROW)).toBe(false)
    expect(dimNaMode({ ...DRAGON_ROW, d3_desc: '' })).toBe(false)
    expect(dimNaMode({})).toBe(false)
    expect(dimNaMode(null)).toBe(false)
    // 两个格子本身没数（不是"有数但没依据"）时不打标注——那是在替用户猜战法类型
    expect(dimNaMode({ d3_desc: '', d4_desc: '' })).toBe(false)
    expect(dimNaMode({ d3: 0.3, d4: 0.1 })).toBe(true)
  })

  // H3 反向验证：把口径改回"一律 toFixed(0)"时 H1 必红（锁形与判据同形，非只测新语义）
  it('H3 旧口径（一律取整）会把 0.62 报成 0——正是本用例要拦的形态', () => {
    expect((0.62).toFixed(0)).not.toBe(dimValueText(0.62))
  })

  // H4 因子行两个格子各打一次「不适用」＝恰 2（多打/漏打都算红）
  it('H4 挂载后「不适用」恰出现在因子行的 D3/D4 两格', async () => {
    await act(async () => { render(<Signals />) })
    await act(async () => { await new Promise((r) => setTimeout(r, 30)) })
    expect(screen.getAllByText('不适用').length).toBe(2)
  })

  // H5 真四维行不得被连坐：整表只出现因子行那 2 个标注（负向清零）
  it('H5 龙头行不出现「不适用」，四维依据文本照常显示', async () => {
    await act(async () => { render(<Signals />) })
    await act(async () => { await new Promise((r) => setTimeout(r, 30)) })
    expect(screen.getByText('龙头股')).toBeTruthy()
    expect(screen.getAllByText('不适用').length).toBe(2)
    expect(screen.getAllByText(/超额收益/).length).toBeGreaterThanOrEqual(1)
  })

  // H6 那个 0.62 必须真在界面上（不是被抹成空白，也不是显示成 0）
  it('H6 因子行强度值以 0.62 原样呈现', async () => {
    await act(async () => { render(<Signals />) })
    await act(async () => { await new Promise((r) => setTimeout(r, 30)) })
    expect(screen.getAllByText(/0\.62/).length).toBeGreaterThanOrEqual(1)
  })
})
