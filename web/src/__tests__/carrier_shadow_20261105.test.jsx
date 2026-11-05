// carrier_shadow_20261105.test.jsx — §CARRIER-SHADOW 影子对照读数的行为锁（2026-11-05）。
//
// 这块读数是「武装」按钮的证据，所以它的失效形态不是"少一行字"，而是**让 owner 按错按钮**：
//  ① known=false 必须显示「读数未知」，且屏上**不许出现**任何"窗口已满/可以"的字样——
//     把"构建不出来"渲染成 0 天，读起来就像"跑了但没命中"（missing≠empty）；
//  ② 四种状态各自一句、互不折叠（no_report 与 no_hits 的下一步动作完全不同：
//     一个查载体/上行腿，一个什么都不用查）；
//  ③ 窗口凑满的文案只说"对照满 N 个交易日"，**不许说成"可以武装"**——
//     按下那枚按钮是 owner 的决定，我们只摆证据；
//  ④ 三种"零"分开数（有命中 / 零命中 / 未上报），折成一个数字就再也问不出缺的是哪一种。
// English: locks for the shadow-comparison readout; unknown never renders as zero days, the
// four per-day states keep four distinct sentences, and a filled window is never worded as
// "safe to arm".
import { describe, it, expect } from 'vitest'
import { render, screen, cleanup } from '@testing-library/react'
import React from 'react'
import CarrierShadowPanel from '../components/CarrierShadowPanel.jsx'
import { shadowDayText, windowProgress, kindRow, SHADOW_DAY_STATE } from '../utils/carrierShadow.js'

cleanup()

const VIEW = {
  known: true,
  as_of_day: '20261105',
  window_days: 10,
  days_reported: 6,
  required_days: 5,
  kinds: [
    {
      kind: 'dragon_return',
      days_with_data: 5, days_no_hits: 3, days_no_report: 2,
      carrier_rows: 14, engine_agreed: 11, required_days: 5, window_filled: true,
      days: [
        { day: '20261103', state: 'agreeing', carrier_rows: 3, engine_agreed: 3, reported_rows: 5 },
        { day: '20261104', state: 'diverged', carrier_rows: 2, engine_agreed: 0, reported_rows: 4 },
        { day: '20261105', state: 'no_report', carrier_rows: 0, engine_agreed: 0, reported_rows: 0 },
      ],
    },
    {
      kind: 'break_down',
      days_with_data: 2, days_no_hits: 8, days_no_report: 0,
      carrier_rows: 4, engine_agreed: 4, required_days: 5, window_filled: false,
      days: [{ day: '20261105', state: 'no_hits', carrier_rows: 0, engine_agreed: 0, reported_rows: 6 }],
    },
  ],
}

describe('windowProgress 未知档', () => {
  it('known=false ⇒ 读数未知，绝不出现"窗口已满"或任何天数', () => {
    const p = windowProgress({ known: false, reason: 'ledger_unavailable' })
    expect(p.text).toContain('读数未知')
    expect(p.known).toBe(false)
    expect(p.text).not.toMatch(/窗口已满|可以/)
    expect(p.text).not.toMatch(/\b0 个交易日/)
  })

  it('缺整个对象（后端还没给这枚键）与 known=false 同档，不许折成空窗口', () => {
    expect(windowProgress(null).text).toContain('读数未知')
    expect(windowProgress(undefined).known).toBe(false)
  })

  it('known=true 而 kinds 为空：说"还没产出判定"，不说"一致"也不说"未知"', () => {
    const p = windowProgress({ known: true, window_days: 10, days_reported: 0, kinds: [] })
    expect(p.text).toContain('还没产出任何影子判定')
    expect(p.text).not.toContain('读数未知')
  })

  it('满窗那句只讲证据够不够，不替 owner 决定武装', () => {
    const p = windowProgress(VIEW)
    expect(p.text).toContain('1 类已对照满 5 个交易日')
    expect(p.text).not.toMatch(/可以武装|应当武装|建议武装/)
  })
})

describe('四种状态四句话', () => {
  it('每档各有自己的句子，且互不为子串（折叠成一语气泡就没了）', () => {
    const texts = Object.values(SHADOW_DAY_STATE).map((s) => shadowDayText(s).text)
    texts.forEach((t) => expect(t.length).toBeGreaterThan(0))
    expect(new Set(texts).size).toBe(4)
    expect(shadowDayText(SHADOW_DAY_STATE.NO_REPORT).text).toContain('未上报')
    expect(shadowDayText(SHADOW_DAY_STATE.NO_HITS).text).toContain('零命中')
    expect(shadowDayText(SHADOW_DAY_STATE.DIVERGED).text).toContain('分歧')
    expect(shadowDayText(SHADOW_DAY_STATE.AGREEING).text).toContain('一致')
  })

  it('未登记的态判读数未知，不许挑一档冒充', () => {
    expect(shadowDayText('whatever').text).toContain('读数未知')
    expect(shadowDayText(undefined).text).toContain('读数未知')
    // 主题色也分开：未上报与分歧是黄，一致是绿——折成同一色就分不出轻重
    expect(shadowDayText(SHADOW_DAY_STATE.AGREEING).theme).toBe('success')
    expect(shadowDayText(SHADOW_DAY_STATE.NO_REPORT).theme).toBe('warning')
  })
})

describe('kindRow 三种零分开', () => {
  it('有命中/零命中/未上报三个数各写各的，且不折进同一句', () => {
    const r = kindRow(VIEW.kinds[0])
    expect(r.daysWithData).toBe(5)
    expect(r.daysNoHits).toBe(3)
    expect(r.daysNoReport).toBe(2)
    expect(r.text).toContain('有命中 5/5 天')
    expect(r.text).toContain('零命中 3 天')
    expect(r.text).toContain('未上报 2 天')
    // 分母是"报过的行"与"引擎认的行"两枚，合成一枚就看不出是谁不认
    expect(r.text).toContain('载体 14 条 / 引擎认 11 条')
  })

  it('required_days 缺席时退回 5（与 Go 侧 carrierShadowRequiredDays 同值）', () => {
    expect(kindRow({ kind: 'x' }).required).toBe(5)
  })
})

describe('面板渲染', () => {
  it('shadow=null（还没拿到读数）不炸屏，也不显示任何天数', () => {
    render(<CarrierShadowPanel shadow={null} />)
    screen.getByText(/读数未知/)
    expect(document.body.textContent).not.toMatch(/窗口已满/)
    cleanup()
  })

  it('known=false 时整块只显示未知，不渲染 kinds 表格', () => {
    render(<CarrierShadowPanel shadow={{ known: false, kinds: VIEW.kinds }} />)
    screen.getByText(/读数未知/)
    expect(document.body.textContent).not.toContain('dragon_return')
    cleanup()
  })

  it('两类规则都上屏：满窗的那类标窗口已满，未满的那类仍显示缺几天', () => {
    render(<CarrierShadowPanel shadow={VIEW} />)
    const body = document.body.textContent
    expect(body).toContain('dragon_return')
    expect(body).toContain('break_down')
    expect(body).toContain('窗口未满')
    expect(body).toContain('已上报 6 天 / 需要 5 天')
    expect(body).toContain('1105·no_report')
    cleanup()
  })
})
