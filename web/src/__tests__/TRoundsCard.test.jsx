// TRoundsCard.test.jsx — §DYN-T 做 T 卡的行为锁（2026-10-26 批 4/9 收尾）。
//
// 三条锁全部打在"读数不许骗人"上：
//   · T1 做 T 收益只显式报**净额与完成轮数**，且这张卡里没有任何一处把它加进总收益；
//   · T2 实盘账本 state=corrupt 必须渲染成损坏态，**不能**渲染成"今天没做 T"
//      （把坏文件当空表是本项目的招牌缺陷形态）；
//   · T3 纸面被拦分布要按 step 逐行出来，并带上"次数"——灰度期调参靠的就是这个分布形状。
//
// English: locks for the §DYN-T card — the standalone T P&L is shown as such, a corrupt live file
// renders as corrupt instead of "no rounds", and the paper block-step distribution is visible.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, cleanup, fireEvent } from '@testing-library/react'
import { settle } from './settle.js'

const { state } = vi.hoisted(() => ({ state: {} }))

function paperView() {
  return {
    book: 'paper',
    day: '20261026',
    enabled: true,
    shadow: false,
    day_stats: { day: '20261026', opened: 3, done: 2, unwound: 1, converted: 0, loss_breaker: 0,
      t_pnl_net: 186.4, t_pnl_gross: 200.0, open_legs: 1, frozen_cash: 5200, sell_notional: 9800,
      converted_qty: 0, block_by_step: {} },
    rounds: [
      { id: 't0:600000:正T:20261026:1', code: '600000', name: '浦发', mode: '正T', leg: 'buy', leg_qty: 1000,
        leg_price: 12.1, pair_qty: 1000, pair_price: 12.35, state: 'done', t_pnl_net: 186.4, t_pnl_gross: 200,
        v: 12.28, dev_pct: -1.47, used_signal_id: 'sig-77', reason: '正T配对完成' },
      { id: 't0:600001:反T:20261026:1', code: '600001', name: '样例', mode: '反T', leg: 'sell', leg_qty: 500,
        leg_price: 9.9, state: 'open', frozen_cash: 5200, reason: '等回踩' },
    ],
    extra: {
      target: { market_scale: 0, ratio_used: 0, target: 0, cur_strat: 0, room: 0, t_quota: 50000,
        cash_t: 42000, frozen_cash: 5200, missing_key_input: false },
      sell_cap_pct: 20, sell_cap_value: 8000, sell_cap_denominator: 40000, sell_used_pct: 24.5,
    },
    block_steps: [
      { step: 'step4', code: '600519', n: 12 },
      { step: 'step2', code: '600000', n: 3 },
    ],
    pnl: { net: 186.4, gross: 200.0, done_rounds: 2, fee_est_share: 0.5 },
  }
}

function liveView(stateKind) {
  if (stateKind === 'corrupt') {
    return { book: 'live', state: 'corrupt', reason: '实盘回转账本文件损坏（接口未改动原文件，需人工核对）', rounds: [], pnl: { net: 0, gross: 0, done_rounds: 0, fee_est_share: 0 } }
  }
  return { book: 'live', state: 'ok', rounds: [{ id: 't0:600002:正T:20261026:1', code: '600002', dir: 'buy_first', status: 'done', qty: 300, t_pnl_net: 55.5 }],
    pnl: { net: 55.5, gross: 60, done_rounds: 1, fee_est_share: 0 } }
}

function stubs(liveKind) {
  return {
    fetchPaperTRounds: () => paperView(),
    fetchLiveTRounds: () => liveView(liveKind),
  }
}

vi.mock('../api/index.js', async () => {
  const actual = await vi.importActual('../api/index.js')
  const s = {}
  for (const name of Object.keys(stubs('ok'))) {
    s[name] = vi.fn((...args) => {
      const fn = state.impl[name]
      return typeof fn === 'function' ? fn(...args) : Promise.resolve(null)
    })
  }
  return { ...actual, ...s }
})

import TRoundsCard from '../components/TRoundsCard.jsx'

const text = () => document.body.textContent || ''

describe('§DYN-T 做 T 卡：收益独立口径 / 损坏态 / 被拦分布', () => {
  beforeEach(() => {
    cleanup()
    localStorage.clear()
    state.impl = stubs('ok')
  })
  afterEach(() => cleanup())

  it('T1 纸面净额与完成轮数如实回显，并标注费用估算占比', async () => {
    render(<TRoundsCard />)
    await settle()
    expect(text()).toContain('做 T 净额（累计 done 轮）')
    expect(text()).toContain('186.40')
    expect(text()).toContain('完成轮数：2')
    expect(text()).toContain('费用为估算入账的占比 50%')
  })

  it('T2 实盘文件损坏：显示损坏态，绝不渲染成"暂无回转记录"', async () => {
    state.impl = stubs('corrupt')
    render(<TRoundsCard />)
    await settle()
    // 先切到实盘页签
    const tab = [...document.querySelectorAll('[role="tab"],div,button')].find((n) => (n.textContent || '').trim() === '实盘账本')
    fireEvent.click(tab)
    await settle()
    expect(text()).toContain('实盘回转账本文件损坏')
    expect(text()).not.toContain('暂无实盘回转记录')
  })

  it('T3 纸面被拦分布逐行出来（step 编号 + 人话 + 次数），并按次数倒序', async () => {
    render(<TRoundsCard />)
    await settle()
    expect(text()).toContain('日内口径不给（振幅/偏离/极值/涨跌停）')
    expect(text()).toContain('当日次数已用尽')
    const idx4 = text().indexOf('日内口径不给')
    const idx2 = text().indexOf('当日次数已用尽')
    expect(idx4).toBeLessThan(idx2)
  })

  it('T5 未注入定权时显示"本轮尚未定权"，绝不把 market_scale=0 渲染成中性 1.00', async () => {
    render(<TRoundsCard />)
    await settle()
    expect(text()).toContain('本轮尚未定权')
    expect(text()).not.toContain('市场缩放 MS 1.000')
  })

  it('T6 反 T 总量帽按 min(总资产, 持仓市值) 的分母给已用比例', async () => {
    render(<TRoundsCard />)
    await settle()
    expect(text()).toContain('反T总量帽 24.5%')
    expect(text()).toContain('帽 8000.00 元')
  })

  it('T4 回转终态用人话标出（在途/已配对/转战略/已收摊），并显示冻结资金角标', async () => {
    render(<TRoundsCard />)
    await settle()
    expect(text()).toContain('在途冻结 5200.00 元')
    expect(text()).toContain('已配对')
    expect(text()).toContain('在途')
  })
})
