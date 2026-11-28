// flow_factor_card_stock_20261128.test.jsx — §REPLAY-INPUTS 档 3b：个股资金流覆盖率那一行的三态锁。
//
// 这张卡历史上出过事的方向只有一个：**把"没读数"画成一个正常的数**。
// 个股资金流是长期日频表、唯一写口是 15:35 的 systemd timer，所以"停更"必须在这一行
// 自己喊出来，而不是安静地让 fac_175/179 拿旧数据算出看着正常的分（ths_* 静默停更一年多的形状）。
// 三态各自成句、互不折叠：① 有读数（票数+覆盖率+采集器年龄+本轮结论）；
// ② known=false（表里没有当天行）；③ known=true 但 codes=0（等同故障，按弃权处理）。
// English: locks the three render states of the per-stock money-flow coverage row —
// reading present / no row today / zero codes (a collector failure, not a market reading).
import { describe, it, expect } from 'vitest'
import { render, screen, cleanup } from '@testing-library/react'
import React from 'react'
import FlowFactorCard from '../components/FlowFactorCard.jsx'

cleanup()

const BASE = {
  available: true,
  day: '2026-11-27',
  known: true,
  market_known: true,
  market_pct: -0.6,
  market_delta: -0.2,
  vol_known: true,
  vol_state: 'surge',
  r_yest: 1.5,
  r_ma5: 1.2,
  vol_delta: 1,
  sector_count: 480,
  sector_known: true,
  sector_pct: 0.4,
  sector_delta: 0.1,
  blend: 1.08,
  blend_ok: true,
}

describe('§REPLAY-INPUTS FlowFactorCard 个股资金流行', () => {
  it('有读数时票数、覆盖率、采集器年龄与本轮结论同屏', () => {
    render(
      <FlowFactorCard
        flow={{
          ...BASE,
          stock_flow_known: true,
          stock_flow_day: '2026-11-27',
          stock_flow_codes: 4812,
          stock_flow_total: 5100,
          stock_flow_coverage_pct: 94.4,
          stock_flow_run_age_sec: 1800,
          stock_flow_run_status: 'ok',
        }}
      />,
    )
    const el = screen.getByTestId('flow-stock-agg')
    expect(el.textContent).toContain('4812 只')
    expect(el.textContent).toContain('94.4%')
    expect(el.textContent).toContain('30 分钟前')
    // 本轮结论是 ok 时不多嘴（只在 partial/failed 时才标注）
    expect(el.textContent).not.toContain('本轮')
  })

  it('known=false 显示弃权，绝不出现 0 只', () => {
    render(<FlowFactorCard flow={{ ...BASE, stock_flow_known: false }} />)
    const el = screen.getByTestId('flow-stock-agg')
    expect(el.textContent).toContain('弃权')
    expect(el.textContent).not.toContain('0 只')
    expect(el.textContent).not.toContain('0.0%')
  })

  it('known=true 但 codes=0 按弃权处理（0 只是采集器故障，不是"净流入为零"）', () => {
    render(
      <FlowFactorCard
        flow={{ ...BASE, stock_flow_known: true, stock_flow_codes: 0, stock_flow_coverage_pct: 0 }}
      />,
    )
    const el = screen.getByTestId('flow-stock-agg')
    expect(el.textContent).toContain('弃权')
    expect(el.textContent).not.toContain('0 只')
  })

  it('非 ok 的本轮结论必须点名（partial/relay_unavailable 不能藏起来）', () => {
    render(
      <FlowFactorCard
        flow={{
          ...BASE,
          stock_flow_known: true,
          stock_flow_codes: 1200,
          stock_flow_coverage_pct: 23.5,
          stock_flow_run_age_sec: 600,
          stock_flow_run_status: 'relay_unavailable',
        }}
      />,
    )
    const el = screen.getByTestId('flow-stock-agg')
    expect(el.textContent).toContain('relay_unavailable')
    expect(el.textContent).toContain('23.5%')
  })

  it('采集器没跑（无 run_age）时仍显示票数，但不编造"几分钟前"', () => {
    render(
      <FlowFactorCard
        flow={{ ...BASE, stock_flow_known: true, stock_flow_codes: 3000, stock_flow_coverage_pct: 60 }}
      />,
    )
    const el = screen.getByTestId('flow-stock-agg')
    expect(el.textContent).toContain('3000 只')
    expect(el.textContent).not.toContain('分钟前')
  })
})
