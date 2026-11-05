// ── §ADVICE-TACTICS（2026-11-05）实盘建议行的两条 owner 裁决在前端的表现锁 ──
//
// 被测缺陷（owner 原话的两条）：
//   ① 「持仓/交易的建议根本不管我在量化交易页切了哪些战法」——战法开关此前只在执行侧生效，
//      建议链一次都没问过它（Go 侧的过滤与 filtered_by_switch 读数在 internal/trading 那批锁里）；
//   ② 「建议行没有股数，屏上没有任何一个数是从我的持仓算出来的」——
//      旧 applyAdviceMap 只留 {action,label,ref_price,reason,level} 五个字段，
//      后端算好的定权结果在到达浏览器的那一刻又被前端丢了一次。
//
// 本文件三把尺子（页面级 + 文案级）：
//   F1 建议列带出「建议 300 股 ≈3,150 元（受限：target）」——数、金额、受限项三件都在；
//   F2 后端没带 suggest_qty 时**必须**显示「未计算」，屏上不许出现一个像测量值的 0；
//   F3 被战法开关滤掉的条数上屏（「另有 N 条因战法未启用被过滤」），
//      载荷没带这个键时整行不渲染（未知 ≠ 0）。
//   外加 utils/adviceFormat.js 的纯函数用例（文案单点，页面与用例判的是同一句）。
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/react'

const { state } = vi.hoisted(() => ({
  state: {
    // 实盘持仓夹具（建议列按 ts_code 挂到这一行上）
    positions: [{ ts_code: '600000.SH', name: '浦发银行', qty: 100, cost_price: 10, highest_price: 11 }],
    advice: { advices: [], filtered_by_switch: undefined },
  },
}))

// api 层替身：只把本页会拨的那几个函数换成受控夹具，其余走 importActual。
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
    // §F-6 挂载 REST 回填就是本页建议的唯一来源（SSE 在测试里没有连接）
    fetchRealAdvice: vi.fn(async () => state.advice),
  }
})

import Positions from '../pages/Positions.jsx'
import { adviceSuggestText, adviceFilteredText } from '../utils/adviceFormat.js'

// openRealBook 点进「实盘持仓」标签：建议列在实盘表格里，必须等表格真渲染出来。
async function openRealBook() {
  fireEvent.click(await screen.findByText(/实盘持仓/))
  await screen.findByText('600000.SH')
}

describe('§ADVICE-TACTICS 文案单点 adviceFormat', () => {
  it('F1 有定权读数：股数/金额/受限项三件都在同一句里', () => {
    const t = adviceSuggestText({
      action: '加仓', suggest_qty: 300, suggest_amount: 3150, capped_by: 'target',
    })
    expect(t).toBe('建议 300 股 ≈3,150 元（受限：target）')
  })

  it('F1b 金额缺席时只报股数（两个读数各自缺席，不互相冒充）', () => {
    expect(adviceSuggestText({ action: '加仓', suggest_qty: 200 })).toBe('建议 200 股')
  })

  it('F2 缺席不是 0：没有 suggest_qty ⇒「未计算」，被帽挡住时把帽名一并点名', () => {
    expect(adviceSuggestText({ action: '加仓' })).toBe('未计算')
    expect(adviceSuggestText({ action: '加仓', suggest_qty: 0, capped_by: 'per_trade' })).toBe('未计算（受限：per_trade）')
    // suggest_qty:0 与"字段不存在"必须走同一支（后端 omitempty 让 0 根本不出现）
    expect(adviceSuggestText({ action: '加仓', suggest_qty: 0 })).toBe('未计算')
  })

  it('F2b 卖出方向不挂买入量（signal_decisions 记的是买入定权结果，方向不同源）', () => {
    expect(adviceSuggestText({ action: '止盈', suggest_qty: 300 })).toBeNull()
    expect(adviceSuggestText({ action: '减仓' })).toBeNull()
    expect(adviceSuggestText(null)).toBeNull()
  })

  it('F3 过滤条数：>0 才有一句，0 与"没带这个键"都不画数字', () => {
    expect(adviceFilteredText(3)).toBe('另有 3 条因战法未启用被过滤')
    expect(adviceFilteredText(0)).toBeNull()
    expect(adviceFilteredText(undefined)).toBeNull()
    expect(adviceFilteredText(null)).toBeNull()
  })
})

describe('§ADVICE-TACTICS Positions 页建议列与过滤条数', () => {
  beforeEach(() => {
    cleanup()
    document.querySelectorAll('.t-message').forEach((n) => n.remove())
    localStorage.clear()
    state.positions = [{ ts_code: '600000.SH', name: '浦发银行', qty: 100, cost_price: 10, highest_price: 11 }]
    state.advice = { advices: [], filtered_by_switch: undefined }
  })

  afterEach(() => {
    cleanup()
  })

  it('F1 页面上建议格带出「建议 300 股 ≈3,150 元（受限：target）」', async () => {
    state.advice = {
      advices: [{
        code: '600000', ts_code: '600000.SH', action: '加仓', label: '加仓', level: '高',
        reason: '信号活跃且回撤可控', ref_price: 10.5,
        suggest_qty: 300, suggest_amount: 3150, capped_by: 'target', sizing_at: '2026-11-05T10:31:00+08:00',
      }],
      filtered_by_switch: 0,
    }
    render(<Positions />)
    await openRealBook()
    const cell = await screen.findByTestId('advice-suggest')
    expect(cell.textContent).toBe('建议 300 股 ≈3,150 元（受限：target）')
    // 0 条被过滤 ⇒ 那一行不出现（不是"另有 0 条"）
    expect(screen.queryByTestId('advice-filtered')).not.toBeInTheDocument()
  })

  it('F2 后端没带 suggest_qty ⇒ 页面显示「未计算」，且整页不出现"0 股"这种冒充值', async () => {
    state.advice = {
      advices: [{
        code: '600000', ts_code: '600000.SH', action: '加仓', label: '加仓', level: '高',
        reason: '信号活跃且回撤可控', ref_price: 10.5,
      }],
      filtered_by_switch: 0,
    }
    render(<Positions />)
    await openRealBook()
    const cell = await screen.findByTestId('advice-suggest')
    expect(cell.textContent).toBe('未计算')
    expect(cell.textContent).not.toMatch(/0\s*股/)
  })

  it('F3 载荷带 filtered_by_switch ⇒ 条数上屏；不带这个键 ⇒ 整行不渲染（未知 ≠ 0）', async () => {
    state.advice = {
      advices: [{ code: '600000', ts_code: '600000.SH', action: '加仓', label: '加仓', reason: 'r', level: '高' }],
      filtered_by_switch: 2,
    }
    render(<Positions />)
    await openRealBook()
    const line = await screen.findByTestId('advice-filtered')
    expect(line.textContent).toBe('另有 2 条因战法未启用被过滤')
    // 换一份"根本没带这个键"的载荷（旧一轮广播的形状）：重新挂载后那一行必须消失，
    // 而不是留住上一次的 2、也不是折成「另有 0 条」。
    cleanup()
    state.advice = { advices: state.advice.advices }
    render(<Positions />)
    await openRealBook()
    await waitFor(() => expect(screen.queryByTestId('advice-filtered')).not.toBeInTheDocument())
  })
})
