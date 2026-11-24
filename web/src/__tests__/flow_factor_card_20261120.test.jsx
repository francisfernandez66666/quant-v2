// flow_factor_card_20261120.test.jsx — §FLOW-QUADRANT 首页读数卡的行为锁（2026-11-20）。
//
// 这张卡的失效形态不是"少一行字"，而是**把弃权画成中性**：缺数的净流入画成 0.00%、
// 缺数的量比画成「平量」，读起来就像这条腿真的参与过合乘而且方向中性——那会直接误导
// 对仓位的判断（本仓反复出事的"静默无效族"）。所以锁的是四态互不折叠：
//   ① 接口没返回 / available=false / known=false 三句各不相同；
//   ② 单腿缺数只弃权那一条腿，另一条腿照常显示；
//   ③ 量态文案只来自后端枚举，未知不回落「平量」；
//   ④ 合成乘子直接吃后端 blend，blend_ok=false 时显示"弃权"，绝不显示 ×1.00。
// English: locks that missing readings render as explicit abstain, never as 0.0% / "flat",
// and that each of the four states keeps its own sentence.
import { describe, it, expect } from 'vitest'
import { render, screen, cleanup } from '@testing-library/react'
import React from 'react'
import FlowFactorCard, { volStateLabel, fmtSignedPct, fmtRatio } from '../components/FlowFactorCard.jsx'

cleanup()

// FULL 两腿齐全的形态：净流入 -0.60%（delta -0.20）、放量（r_yest 1.50 / r_ma5 1.20）、
// 合成乘子 1.08（大盘 -0.2 与量能 +1 均分 ⇒ b=0.4 ⇒ 1+0.2×0.4）。
const FULL = {
  available: true,
  day: '2026-11-20',
  known: true,
  market_known: true,
  market_pct: -0.6,
  market_delta: -0.2,
  vol_known: true,
  vol_state: 'surge',
  r_yest: 1.5,
  r_ma5: 1.2,
  vol_delta: 1,
  sector_count: 0,
  blend: 1.08,
  blend_ok: true,
}

describe('§FLOW-QUADRANT FlowFactorCard', () => {
  it('接口未返回时只说"尚未取到"，不画任何数值', () => {
    render(<FlowFactorCard flow={undefined} />)
    expect(screen.getByTestId('flow-unavailable')).toBeTruthy()
    expect(screen.queryByText(/0\.00%/)).toBeNull()
    expect(screen.queryByText('平量')).toBeNull()
  })

  it('available=false（服务端未挂载读数库）与"今天没数据"分开成句', () => {
    render(<FlowFactorCard flow={{ available: false, known: false, day: '2026-11-20' }} />)
    const el = screen.getByTestId('flow-unavailable')
    expect(el.textContent).toContain('未接入')
    expect(screen.queryByTestId('flow-abstain')).toBeNull()
  })

  it('known=false（今日无行）显式弃权，绝不出现 0.00% 或平量', () => {
    render(<FlowFactorCard flow={{ available: true, known: false, day: '2026-11-20' }} />)
    const el = screen.getByTestId('flow-abstain')
    expect(el.textContent).toContain('弃权')
    expect(screen.queryByText(/0\.00%/)).toBeNull()
    expect(screen.queryByText('平量')).toBeNull()
    expect(screen.getByText(/读数日 2026-11-20/)).toBeTruthy()
  })

  it('两腿齐全：净流入带符号百分比 + 归一强度 + 放量 + 合成乘子', () => {
    render(<FlowFactorCard flow={FULL} />)
    expect(screen.getByText('-0.60%')).toBeTruthy()
    expect(screen.getByText(/归一强度 -0\.20/)).toBeTruthy()
    expect(screen.getByTestId('flow-vol-state').textContent).toBe('放量')
    expect(screen.getByText(/昨日 ×1\.50 · 五日均 ×1\.20/)).toBeTruthy()
    // 乘子与幅度都由后端下发，前端只做展示（这里同时钉住"没有前端另算一份"）
    const blend = screen.getByTestId('flow-blend')
    expect(blend.textContent).toContain('×1.08')
    expect(blend.textContent).toContain('+8.0%')
    expect(blend.textContent).toContain('±20% 封顶')
    // 板块 0 行是如实读数，必须点明"弃权"而不是留白
    expect(screen.getByText(/板块读数 0 个（板块逐票腿今日无数据，弃权）/)).toBeTruthy()
  })


  // §FLOW-SECTOR-AGG（owner 2026-11-20）：板块聚合两列的展示纪律 —— 有值报两位小数（带符号），
  // 缺数只说"弃权"。0 在这里是"板块中性"的意思，绝不能拿它顶替"没有读数"。
  it('板块聚合：有值时显示合计净流入与等权强度', () => {
    render(<FlowFactorCard flow={{ ...FULL, sector_known: true, sector_pct: -2.345, sector_delta: -0.7 }} />)
    const el = screen.getByTestId('flow-sector-agg')
    expect(el.textContent).toContain('-2.35%')
    expect(el.textContent).toContain('-0.70')
  })

  it('板块聚合：缺数时只说弃权，不画 0.00% / 0.00', () => {
    render(<FlowFactorCard flow={{ ...FULL, sector_known: false }} />)
    const el = screen.getByTestId('flow-sector-agg')
    expect(el.textContent).toContain('弃权')
    expect(el.textContent).not.toContain('0.00%')
    expect(el.textContent).not.toContain('+0.00')
  })

  it('缩量 / 平量各用各的文案（不折叠成同一句）', () => {
    render(<FlowFactorCard flow={{ ...FULL, vol_state: 'shrink', r_yest: 0.7, r_ma5: 0.8, vol_delta: -1 }} />)
    expect(screen.getByTestId('flow-vol-state').textContent).toBe('缩量')
    cleanup()
    // 平量是**有效读数**（delta=0 计入合乘），不是弃权：必须显示「平量」而不是"无读数"
    render(<FlowFactorCard flow={{ ...FULL, vol_state: 'neutral', r_yest: 1.05, r_ma5: 0.95, vol_delta: 0 }} />)
    expect(screen.getByTestId('flow-vol-state').textContent).toBe('平量')
    expect(screen.queryByTestId('flow-volume-abstain')).toBeNull()
  })

  it('单腿缺数只弃权那一条腿，另一条照常显示', () => {
    render(<FlowFactorCard flow={{ ...FULL, market_known: false, market_pct: null, market_delta: null }} />)
    expect(screen.getByTestId('flow-market-abstain').textContent).toContain('净流入：今日无读数（弃权）')
    // 量能腿不受影响
    expect(screen.getByTestId('flow-vol-state').textContent).toBe('放量')
    expect(screen.queryByText(/0\.00%/)).toBeNull()
    cleanup()
    render(<FlowFactorCard flow={{ ...FULL, vol_known: false, vol_state: undefined, r_yest: null, r_ma5: null }} />)
    expect(screen.getByTestId('flow-volume-abstain').textContent).toContain('量能：今日无读数（弃权）')
    expect(screen.getByText('-0.60%')).toBeTruthy()
  })

  it('blend_ok=false 时明说"弃权"，不显示 ×1.00 假装参与过', () => {
    render(<FlowFactorCard flow={{ ...FULL, blend: undefined, blend_ok: false }} />)
    const blend = screen.getByTestId('flow-blend')
    expect(blend.textContent).toContain('弃权')
    expect(blend.textContent).not.toContain('×1.00')
  })

  it('侧策略快照：显示模式、准入、目标乘子与 13:00 latch', () => {
    render(<FlowFactorCard flow={{ ...FULL, side_policy: {
      known: true, enabled: true, stale: false, status: 'known',
      mode: 'bear', level: 2, allow_new: false, allow_add: false,
      target_multiplier: 0.7, latch_active: true,
    } }} />)
    const el = screen.getByTestId('flow-side-policy')
    expect(el.textContent).toContain('熊市收紧')
    expect(el.textContent).toContain('new 禁')
    expect(el.textContent).toContain('add 禁')
    expect(el.textContent).toContain('×0.70')
    expect(el.textContent).toContain('14:00')
  })

  it('侧策略缺快照时明说未启用，不画默认乘子', () => {
    render(<FlowFactorCard flow={FULL} />)
    const el = screen.getByTestId('flow-side-policy')
    expect(el.textContent).toContain('未启用')
    expect(el.textContent).not.toContain('×1.00')
  })

  it('侧策略明确 disabled 时只显示未启用，不画 ×1.00', () => {
    render(<FlowFactorCard flow={{ ...FULL, side_policy: {
      known: true, enabled: false, stale: false, status: 'disabled', target_multiplier: 1,
    } }} />)
    const el = screen.getByTestId('flow-side-policy')
    expect(el.textContent.trim()).toBe('侧策略：未启用')
    expect(el.textContent).not.toContain('×1.00')
  })

  it('侧策略 stale 时显示弃权，不展示过期乘子', () => {
    render(<FlowFactorCard flow={{ ...FULL, side_policy: {
      known: true, enabled: true, stale: true, status: 'stale', mode: 'bull', level: 1,
      allow_new: false, allow_add: true, target_multiplier: 1,
    } }} />)
    const el = screen.getByTestId('flow-side-policy')
    expect(el.textContent).toContain('弃权')
    expect(el.textContent).not.toContain('×1.00')
    expect(el.textContent).not.toContain('牛市镜像')
  })

  it('未知量态回落占位符「—」，绝不猜成平量', () => {
    expect(volStateLabel('surge')).toBe('放量')
    expect(volStateLabel('shrink')).toBe('缩量')
    expect(volStateLabel('neutral')).toBe('平量')
    expect(volStateLabel(undefined)).toBe('—')
    expect(volStateLabel('wat')).toBe('—')
  })

  it('格式化函数不把 null/NaN 画成 0', () => {
    expect(fmtSignedPct(null)).toBeNull()
    expect(fmtSignedPct(undefined)).toBeNull()
    expect(fmtSignedPct('abc')).toBeNull()
    expect(fmtSignedPct(0)).toBe('0.00%') // 0 是有效读数，照常显示（不带 + 号）
    expect(fmtSignedPct(1.234)).toBe('+1.23%')
    expect(fmtRatio(null)).toBeNull()
    expect(fmtRatio(0)).toBe('0.00')
    expect(fmtRatio('1.5')).toBe('1.50')
  })
})
