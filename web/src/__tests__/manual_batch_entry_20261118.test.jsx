// ── 2026-11-18 owner 需求：改账态「录入实盘持仓」支持一次性录入多只股票 ──
//
// 现场（facai2026 11-18 09:36–09:42 实录）：手动账跨日失效后要把十来只票重新录一遍，
// 而当时的入口只有「改账·新增」——一只票一张弹窗、点 6 分钟。这一批把入口换成
// 批量弹窗（粘贴多行），提交仍是**逐笔 op=new**（服务端 adjust 一次只接一条，
// 且每笔各自留流水与资金联动），前端只把"点十次"自动化，不引入批次事务语义。
//
// 本文件锁三层：
//  ① parseManualBatch 纯函数：分隔符/后缀/去重/整批退回四条判据；
//  ② 行为：改账态点「录入」→ 粘贴两行 → 确认 → adjustManualBook 恰好两次且载荷是 op=new；
//  ③ 负锁：有认不出来的行时整批不提交（adjustManualBook 一次都不许被调用）。
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/react'

const { state } = vi.hoisted(() => ({
  state: {
    positions: [{ ts_code: '600000.SH', name: '浦发银行', qty: 1000, cost_price: 8.5 }],
    account: { available_cash: 88000, updated_at: '2026-11-18 09:30:00' },
  },
}))

// api 层整体替身：只把持仓页真会拨的几条换成受控夹具，其余走 importActual。
// 判据钉在"产品代码确实调了哪个出口、载荷是什么"，而不是界面长得像——批量录入的重点
// 是"几笔 op=new、每笔载荷对不对"，把它换成一个"看起来提交成功了"的假象就等于没测。
vi.mock('../api/index.js', async () => {
  const actual = await vi.importActual('../api/index.js')
  return {
    ...actual,
    isAdmin: vi.fn(() => true),
    hasStrictPerm: vi.fn(() => true),
    getAccount: vi.fn(() => 'admin'),
    fetchStatus: vi.fn(async () => ({ session: 'test' })),
    setLastSession: vi.fn(),
    fetchHoldings: vi.fn(async () => ({
      holdings: [], available_balance: 100,
      total_realized_pnl: 0, total_unrealized_pnl: 0, pnl_offset: 0, total_pnl: 0,
    })),
    resetPaperPnlOffset: vi.fn(async () => ({ status: 'ok', pnl_offset: 0 })),
    updateHoldingsBalance: vi.fn(async () => ({ status: 'ok' })),
    updateHoldings: vi.fn(async () => ({ status: 'ok' })),
    fetchQMTState: vi.fn(async () => ({ enabled: true, tripped: false })),
    fetchRealPositions: vi.fn(async () => ({
      positions: state.positions,
      account: state.account,
      book_source: {
        cash: 'manual', positions: 'manual',
        cash_asof: '2026-11-18 09:30:00', positions_asof: '2026-11-18 09:30:00', taken_over_at: '',
      },
    })),
    fetchQMTTrades: vi.fn(async () => null),
    fetchRealAdvice: vi.fn(async () => ({ advices: [] })),
    executeRealAction: vi.fn(async () => ({ status: 'ok' })),
    adjustManualBook: vi.fn(async () => ({ status: 'ok', adjust: { op: 'new' } })),
  }
})

import * as api from '../api/index.js'
import Positions, { parseManualBatch } from '../pages/Positions.jsx'

// 可见弹窗里的 textarea（TDesign Textarea 在 Dialog 内渲染成原生 textarea）
function visibleTextarea() {
  const ctxs = [...document.querySelectorAll('.t-dialog__ctx')]
    .filter((n) => getComputedStyle(n).display !== 'none')
  expect(ctxs.length, '必须恰好一张可见弹窗').toBe(1)
  const el = ctxs[0].querySelector('textarea')
  expect(el, '可见弹窗里没有 textarea').toBeTruthy()
  return el
}

function clickVisibleDialogButton(label) {
  const ctxs = [...document.querySelectorAll('.t-dialog__ctx')]
    .filter((n) => getComputedStyle(n).display !== 'none')
  expect(ctxs.length, '必须恰好一张可见弹窗').toBe(1)
  const btn = [...ctxs[0].querySelectorAll('button')].find((b) => (b.textContent || '').includes(label))
  expect(btn, '可见弹窗里没有按钮：' + label).toBeTruthy()
  fireEvent.click(btn)
}

async function openRealBook() {
  await screen.findByText('持仓数据截至', { exact: false })
  await waitFor(() => expect(screen.getByTestId('real-book-source')).toBeInTheDocument())
}

describe('parseManualBatch：文本 → 逐笔请求的纯函数判据', () => {
  it('三列解析：空格/逗号/Tab 都认，纯数字码按板块补后缀，井号行与空行忽略', () => {
    const { rows, errors } = parseManualBatch([
      '# 代码 数量 价格',
      '600000 1000 8.5',
      '000001.SZ,500,11.2',
      'sz002093\t400\t7.00',
      '',
    ].join('\n'))
    expect(errors).toEqual([])
    expect(rows.map((r) => [r.ts_code, r.qty, r.price])).toEqual([
      ['600000.SH', 1000, 8.5],
      ['000001.SZ', 500, 11.2],
      ['002093.SZ', 400, 7],
    ])
  })

  it('认不出来的行列出行号与原因（不许静默跳过）', () => {
    const { rows, errors } = parseManualBatch([
      '600000 1000',
      '600000 1000 8.5 多余列',
      'WRONG 100 1',
      '600000 0 8.5',
      '600000 1000 -1',
    ].join('\n'))
    expect(rows).toEqual([])
    expect(errors.map((e) => e.line)).toEqual([1, 2, 3, 4, 5])
    expect(errors[0].reason).toContain('3 列')
    expect(errors[2].reason).toContain('认不出来')
    expect(errors[3].reason).toContain('正整数')
    expect(errors[4].reason).toContain('大于 0')
  })

  it('同一批里重复的代码必须报错：后一条会静默覆盖前一条（账上少一只）', () => {
    const { rows, errors } = parseManualBatch('600000 100 8.5\n600000.SH 200 9.0')
    expect(rows).toHaveLength(1)
    expect(errors).toHaveLength(1)
    expect(errors[0].reason).toContain('重复')
  })
})

describe('批量录入弹窗：一次提交多只票，仍逐笔 op=new', () => {
  beforeEach(() => {
    cleanup()
    localStorage.clear()
    vi.clearAllMocks()
  })
  afterEach(() => { cleanup() })

  it('粘贴两行 → 预览两行 → 确认后 adjustManualBook 恰好两次（不下单）', async () => {
    render(<Positions />)
    await openRealBook()

    // 状态行的批量入口（同一枚按钮在页头也指向它，这里点状态行那枚更贴近实际路径）
    fireEvent.click(await screen.findByTestId('manual-batch-entry'))
    const ta = visibleTextarea()
    fireEvent.change(ta, { target: { value: '002093 400 7.00\n000417 400 5.96' } })

    // 预览：两行、都能认出（有错行时预览不渲染，改渲染错误清单）
    await waitFor(() => expect(screen.getByTestId('manual-batch-preview')).toBeInTheDocument())
    expect(screen.getByTestId('manual-batch-preview').textContent).toContain('将录入 2 只')

    clickVisibleDialogButton('确认录入 · 不下单')

    await waitFor(() => expect(api.adjustManualBook).toHaveBeenCalledTimes(2), { timeout: 5000 })
    expect(api.adjustManualBook.mock.calls.map((c) => c[0])).toEqual([
      { op: 'new', ts_code: '002093.SZ', qty: 400, price: 7 },
      { op: 'new', ts_code: '000417.SZ', qty: 400, price: 5.96 },
    ])
    // 批量录入绝不许走真单腿
    expect(api.executeRealAction).not.toHaveBeenCalled()
  })

  it('负锁：有认不出来的行 ⇒ 整批不提交（adjustManualBook 一次都不许被调用）', async () => {
    render(<Positions />)
    await openRealBook()
    fireEvent.click(await screen.findByTestId('manual-batch-entry'))
    fireEvent.change(visibleTextarea(), { target: { value: '002093 400 7.00\n000417 400' } })

    await waitFor(() => expect(screen.getByTestId('manual-batch-errors')).toBeInTheDocument())
    expect(screen.getByTestId('manual-batch-errors').textContent).toContain('整批不会提交')

    clickVisibleDialogButton('确认录入 · 不下单')
    // 给异步留一拍：确认函数里只有"发现有错就直接 return"，任何一次调用都会在这里被抓到
    await new Promise((r) => setTimeout(r, 50))
    expect(api.adjustManualBook).not.toHaveBeenCalled()
  })
})
