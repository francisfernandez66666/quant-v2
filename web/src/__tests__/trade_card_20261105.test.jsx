// ── §TRADE-CARD（2026-11-05）交易提示卡＝量化自动交易的数据，五个动作撤/留/加/减/走 ──
//
// owner 的口径原话：「交易提示卡完全拿量化交易的数据来做，包括买入撤单，留，加，减，走。
// 所以要做交易级别的提醒，不到交易级别不要提醒……量化自动交易准备怎么做，你就怎么提醒」，
// 以及「做 t 的也要提醒，特殊标注这是做 t」。
//
// 本文件锁的是这一批在前端的**表现层**判据（后端判据在 internal/trading 与 internal/engine）：
//   C1 撤单行必须有一个**持仓表之外**的落点：被撤的建仓买单往往根本没有持仓行，
//      只把撤单塞进"建议"列＝系统撤了一笔单而屏上一个字都不显示；
//   C2 做 T 的行必须带「做T·」前缀（同一只票同一天既做 T 又战略加仓是合法形态）；
//   C3 文案单点：页面与用例判的是同一句函数产出，不许各写一份；
//   C4 缺席不画数：本轮没有撤单动作 ⇒ 提示条整条不渲染（不是「撤单 0 笔」）。
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/react'

const { state } = vi.hoisted(() => ({
  state: {
    positions: [],
    advice: { advices: [], filtered_by_switch: undefined },
  },
}))

// api 层替身：本页会拨的读面换成受控夹具（建议走 §F-6 的 REST 回填，测试里没有 SSE 连接）。
vi.mock('../api/index.js', async () => {
  const actual = await vi.importActual('../api/index.js')
  return {
    ...actual,
    isAdmin: vi.fn(() => true),
    fetchStatus: vi.fn(async () => ({ session: 'test' })),
    setLastSession: vi.fn(),
    fetchHoldings: vi.fn(async () => ({
      holdings: [], available_balance: 100, total_realized_pnl: 0, total_unrealized_pnl: 0,
      pnl_offset: 0, total_pnl: 0,
    })),
    resetPaperPnlOffset: vi.fn(async () => ({ status: 'ok', pnl_offset: 0 })),
    updateHoldingsBalance: vi.fn(async () => ({ status: 'ok' })),
    updateHoldings: vi.fn(async () => ({ status: 'ok' })),
    fetchQMTState: vi.fn(async () => ({ enabled: true, tripped: false })),
    fetchRealPositions: vi.fn(async () => ({ positions: state.positions })),
    fetchQMTTrades: vi.fn(async () => null),
    fetchRealAdvice: vi.fn(async () => state.advice),
  }
})

import Positions from '../pages/Positions.jsx'
import {
  adviceRowLabel, cancelKindText, cancelSourceText, tradeLevelSummaryText,
} from '../utils/adviceFormat.js'

// openRealTab 点进「实盘持仓」标签；不假定表格里有没有行（C1 那支的持仓集是空的）。
async function openRealTab() {
  fireEvent.click(await screen.findByText(/实盘持仓/))
}

describe('§TRADE-CARD 文案单点 adviceFormat', () => {
  it('C2 做T 的行前缀「做T·」，非 T 的行原样', () => {
    expect(adviceRowLabel({ action: '加仓', trade_kind: 'intraday_t' })).toBe('做T·加仓')
    expect(adviceRowLabel({ action: '减仓', trade_kind: 'intraday_t' })).toBe('做T·减仓')
    expect(adviceRowLabel({ action: '撤单' })).toBe('撤单')
    expect(adviceRowLabel(null)).toBe('')
  })

  it('C3 撤单形态话术单点：认不出的形态回显原值，绝不猜成"已撤"', () => {
    expect(cancelKindText('requested')).toContain('等待交易所确认')
    expect(cancelKindText('window')).toContain('仍挂在柜台上')
    expect(cancelKindText('refused')).toContain('被拒')
    expect(cancelKindText('weird_kind')).toBe('撤单形态：weird_kind')
    expect(cancelKindText('')).toContain('请核对当日委托')
  })

  it('C3 撤单理由三档都有人话（缺 close_sweep 那档就会把英文原值顶到屏上）', () => {
    expect(cancelSourceText('sweep')).toBe('超时撤单')
    expect(cancelSourceText('signal_gone')).toBe('信号消失')
    expect(cancelSourceText('close_sweep')).toBe('收盘清单')
    expect(cancelSourceText('')).toBe('来源未标注')
  })

  it('C1/C4 提示条：已发出与仍未撤成分开计数；没有撤单行时返回 null（不画 0 笔）', () => {
    expect(tradeLevelSummaryText([])).toBeNull()
    expect(tradeLevelSummaryText([{ action: '加仓' }, { action: '持有' }])).toBeNull()
    const t = tradeLevelSummaryText([
      { action: '撤单', cancel_kind: 'requested', cancel_source: 'signal_gone' },
      { action: '撤单', cancel_kind: 'requested', cancel_source: 'sweep' },
      { action: '撤单', cancel_kind: 'window', cancel_source: 'sweep' },
    ])
    expect(t).toContain('今日撤单 3 笔')
    expect(t).toContain('已发撤单请求 2 笔')
    expect(t).toContain('另有 1 笔仍未撤成')
    expect(t).toContain('信号消失 1')
    expect(t).toContain('超时撤单 2')
  })
})

describe('§TRADE-CARD Positions 页的落点', () => {
  beforeEach(() => {
    cleanup()
    document.querySelectorAll('.t-message').forEach((n) => n.remove())
    localStorage.clear()
    state.positions = []
    state.advice = { advices: [], filtered_by_switch: undefined }
  })

  afterEach(() => {
    cleanup()
  })

  it('C1 一只**没有持仓**的票被撤了建仓买单，提示条仍然上屏（只挂表格就永远看不见）', async () => {
    state.positions = [] // 建仓单没成交 ⇒ 持仓表是空的
    state.advice = {
      advices: [{
        code: '601000', ts_code: '601000.SH', action: '撤单', level: '中',
        reason: '撤单请求已发出：信号消失即撤', order_id: 'GW-9001',
        cancel_kind: 'requested', cancel_source: 'signal_gone',
      }],
      filtered_by_switch: 0,
    }
    render(<Positions />)
    await openRealTab()
    const strip = await waitFor(() => screen.getByTestId('trade-cancel-strip'))
    expect(strip.textContent).toContain('今日撤单 1 笔')
    expect(strip.textContent).toContain('信号消失 1')
  })

  it('C1b 窗口内没撤成的那一笔要在提示条里算进"仍未撤成"（不许折成已发出）', async () => {
    state.advice = {
      advices: [{
        code: '601001', ts_code: '601001.SH', action: '撤单', level: '高',
        reason: '判定要撤，但禁撤窗口内未发请求', order_id: 'GW-9002',
        cancel_kind: 'window', cancel_source: 'signal_gone',
      }],
      filtered_by_switch: 0,
    }
    render(<Positions />)
    await openRealTab()
    const strip = await waitFor(() => screen.getByTestId('trade-cancel-strip'))
    expect(strip.textContent).toContain('另有 1 笔仍未撤成')
    expect(strip.textContent).not.toContain('已发撤单请求')
  })

  it('C2 持仓行的建议标签带「做T·」前缀，与非 T 的行一眼分得开', async () => {
    state.positions = [{ ts_code: '600000.SH', name: '浦发银行', qty: 100, cost_price: 10, highest_price: 11 }]
    state.advice = {
      advices: [{
        code: '600000', ts_code: '600000.SH', action: '加仓', level: '高',
        reason: '做T·正T（先买机动仓，计划配对卖） 状态=一腿在途 计划500股',
        order_id: 't0:600000:buy_first:20261105:1', trade_kind: 'intraday_t',
      }],
      filtered_by_switch: 0,
    }
    render(<Positions />)
    await openRealTab()
    const tag = await waitFor(() => screen.getByTestId('advice-tag'))
    expect(tag.textContent).toBe('做T·加仓')
  })

  it('C2b 非 T 的战略加仓行不许被误标成做T（后端没带 trade_kind 时前缀一个都不加）', async () => {
    state.positions = [{ ts_code: '600000.SH', name: '浦发银行', qty: 100, cost_price: 10, highest_price: 11 }]
    state.advice = {
      advices: [{
        code: '600000', ts_code: '600000.SH', action: '加仓', level: '高', reason: '信号活跃且回撤可控',
      }],
      filtered_by_switch: 0,
    }
    render(<Positions />)
    await openRealTab()
    const tag = await waitFor(() => screen.getByTestId('advice-tag'))
    expect(tag.textContent).toBe('加仓')
    expect(screen.queryByTestId('trade-cancel-strip')).not.toBeInTheDocument()
  })
})
