// ── §H-2（FIX_PLAN_20260922PM §二 H-2 行，2026-09-22 修复批）Positions 保存可用资金失败收口 ──
//
// 缺陷三原（挂载即踩）：
//  ① editBalanceSave 的失败分支调用 showToast，但导入区没有 ui.jsx —— 一失败就
//    ReferenceError（异步未捕获），用户零提示；
//  ② 失败不回滚乐观写，界面长期挂着没保存成功的余额；
//  ③ persistCache(:231 的随 state 自动持久化) 把乐观脏值写进 pos_cache_v1，
//    挂载时 :39/41/47 回读 —— 错误余额跨刷新持久污染。
// 修法：补 import；catch 回滚编辑前值；保存在途/失败期间 persistCache 走
// balancePendingRef 守卫，缓存只落服务端确认值。
//
// 本文件两把挂载锁（旧 Positions 整页零挂载测试，H-2 正是从这片真空里溜走的）：
//  T1 mock 保存接口回 500 → toast 出现 + 界面回滚 + pos_cache_v1 仍是干净余额；
//  T2 保存成功 → 无报错 toast、界面与缓存最终同步到新值（守卫不把正常路径写坏）。
// 另附静态负向锁 T3：pages/*.jsx 调用 showToast 的必须在导入区引 ui.jsx（防同类漏导入复发）。
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/react'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const HERE = path.dirname(fileURLToPath(import.meta.url))

// api 模块替身：保留真实导出（isForbidden/getAccount 等语义不造假），只覆盖本页触达的端点。
// 工厂经 vi.hoisted 提供可变响应（服务端确认余额 balance 等），用例里按需改 reject 形态。
const { state } = vi.hoisted(() => ({
  state: {
    balance: 100,
    balanceErr: null, // 非 null 时 updateHoldingsBalance 抛出该错误
    // §E1 单轨用例可变量：后端汇总读数与校准调用记录
    holdings: [],
    totalPnl: 0,
    pnlOffset: 0,
    resetCalls: 0,
    resetErr: null,
  },
}))

// 只替身 api 层（保留 importActual 的真实现），把网络边界掐掉，让断言集中在组件的状态收敛上。
vi.mock('../api/index.js', async () => {
  const actual = await vi.importActual('../api/index.js')
  return {
    ...actual,
    isAdmin: vi.fn(() => true),
    fetchStatus: vi.fn(async () => ({ session: 'test' })),
    setLastSession: vi.fn(),
    fetchHoldings: vi.fn(async () => ({
      holdings: state.holdings, available_balance: state.balance,
      total_realized_pnl: 0, total_unrealized_pnl: 0,
      // §E1：后端算好的总盈亏与入库偏移（旧契约里没有这两个字段）
      pnl_offset: state.pnlOffset, total_pnl: state.totalPnl,
    })),
    // §E1 清零端点替身：记录调用次数；resetErr 非空时抛错（失败必须 toast、不得静默）
    resetPaperPnlOffset: vi.fn(async () => {
      state.resetCalls += 1
      if (state.resetErr) throw state.resetErr
      state.totalPnl = 0
      return { status: 'ok', pnl_offset: 0 }
    }),
    updateHoldingsBalance: vi.fn(async (v) => {
      if (state.balanceErr) throw state.balanceErr
      state.balance = v
      return { status: 'ok' }
    }),
    fetchQMTState: vi.fn(async () => ({ enabled: false })),
    fetchRealPositions: vi.fn(async () => ({ positions: [] })),
    fetchQMTTrades: vi.fn(async () => null),
    fetchRealAdvice: vi.fn(async () => ({ advices: [] })),
  }
})

import Positions from '../pages/Positions.jsx'

// §E1 页头读数读取器：总盈亏 span/div 文本（只认页头这一处，避免与持仓表格行里的数字互相误伤）
function headerPnlText() {
  const el = screen.getByText(/总盈亏/, { exact: false })
  return el.textContent || ''
}


// 打开「可用资金」编辑 → 输入新值 → blur 触发 editBalanceSave（与线上交互同路径）
// 注：TDesign InputNumber 的 input 在 jsdom 下不保证映射 role=spinbutton，直接取 DOM。
async function saveBalanceAs(nextVal) {
  const cell = await screen.findByText(/可用资金: ¥/)
  fireEvent.click(cell)
  const input = await waitFor(() => {
    const el = document.querySelector('input.t-input__inner, .t-input-number input')
    if (!el) throw new Error('编辑态 InputNumber 未出现')
    return el
  })
  fireEvent.change(input, { target: { value: String(nextVal) } })
  fireEvent.blur(input)
}

describe('§H-2 Positions 保存可用资金失败（toast + 回滚 + 缓存不写脏）', () => {
  beforeEach(() => {
    cleanup()
    // TDesign MessagePlugin 的 toast 挂在 document.body（不经 React 容器），
    // cleanup 不会带走——上一条用例的失败 toast 会残影进本条断言，先物理清场。
    document.querySelectorAll('.t-message').forEach((n) => n.remove())
    localStorage.clear()
    state.balance = 100
    state.balanceErr = null
    // §E1 用例对照组复位：读数回默认
    state.holdings = []
    state.totalPnl = 0
    state.pnlOffset = 0
    state.resetCalls = 0
    state.resetErr = null
    // 预置一份与后端一致的干净缓存：断言「失败后它仍干净」才有对照组
    localStorage.setItem('pos_cache_v1', JSON.stringify({ holdings: [], balance: 100 }))
  })
  afterEach(() => {
    state.balanceErr = null
    cleanup()
  })

  // T1：保存 500 → toast 真出现（修复前此处 ReferenceError 零提示）、余额回滚 100、缓存不被写脏
  it('T1 保存失败：toast 出现 + 乐观写回滚 + pos_cache_v1 未被写入脏余额', async () => {
    state.balanceErr = Object.assign(new Error('服务端 500：资金保存失败'), { status: 500 })
    render(<Positions />)
    await screen.findByText(/可用资金: ¥100\.00/)
    await saveBalanceAs(999)

    // ① toast（showToast 补导入后才会真的出现）
    const toast = await screen.findByText(/保存可用资金失败：服务端 500/, {}, { timeout: 5000 })
    expect(toast, '§H-2：失败必须给出可见 toast（旧码 ReferenceError 零提示）').toBeInTheDocument()

    // ② 回滚到编辑前值
    await screen.findByText(/可用资金: ¥100\.00/, {}, { timeout: 5000 })
    expect(screen.queryByText(/可用资金: ¥999/), '§H-2：失败分支必须回滚乐观写').not.toBeInTheDocument()

    // ③ 缓存禁写脏：等在途/回滚引发的最后一次 persist 落地后再取
    await waitFor(() => {
      const raw = JSON.parse(localStorage.getItem('pos_cache_v1') || '{}')
      expect(raw.balance, '§H-2：错误余额绝不能进 pos_cache_v1（跨刷新污染）').toBe(100)
    })
    expect(String(localStorage.getItem('pos_cache_v1'))).not.toContain('999')
  })

  // T2：保存成功 → 无失败 toast、界面更新、60s 轮询后缓存同步到新确认值（守卫不伤正常路径）
  it('T2 保存成功：界面无失败 toast 且余额更新', async () => {
    render(<Positions />)
    await screen.findByText(/可用资金: ¥100\.00/)
    await saveBalanceAs(999)
    await screen.findByText(/可用资金: ¥999\.00/, {}, { timeout: 5000 })
    expect(screen.queryByText(/保存可用资金失败/)).not.toBeInTheDocument()
  })

  // T3：静态负向锁——pages 目录里任何调用 showToast( 的文件，导入区必须含 ui.jsx
  //（FIX_PLAN §H-2「为什么 UAT 没抓到」：vitest 无未定义标识符静态面，用文件级锁补上）
  it('T3 静态锁：showToast 调用者必须已导入 ui.jsx', () => {
    const pagesDir = path.join(HERE, '..', 'pages')
    for (const f of fs.readdirSync(pagesDir)) {
      if (!f.endsWith('.jsx')) continue
      const src = fs.readFileSync(path.join(pagesDir, f), 'utf8')
      if (!/\bshowToast\(/.test(src)) continue
      // 剥掉注释行后仍调用 showToast 的，导入区必须有 showToast from '…/ui.jsx'
      const code = src.split('\n').filter((l) => !l.trim().startsWith('//')).join('\n')
      if (!/\bshowToast\(/.test(code)) continue
      expect(code, `§H-2：${f} 调用 showToast 但未导入 ui.jsx`).toMatch(/import\s*\{[^}]*\bshowToast\b[^}]*\}\s*from\s*['"][^'"]*ui\.jsx['"]/)
    }
  })
})

// ── §E1 盈亏单轨（owner 裁决 2026-09-26「盈亏前端自算与后端两套账：要做」）──
// 旧账本：页头总盈亏由前端逐持仓自算（∑(现价−成本)×数量+已实现−localStorage 偏移），
// 与后端汇总是**两套账**；清零校准只写浏览器本地、换设备即丢且无痕迹。
// 现在：页头读数=后端 total_pnl（前端不再自算）、清零走 POST /api/holdings/pnl-offset 入库留痕。
// T4 是"单轨"的核心等值锁：故意让后端读数与前端若自算的结果**不一致**，
//     页面必须显示后端值——若有人把本地算式改回来，这条必红。
// T5 清零点击必须真的打后端端点（旧版只写 localStorage，resetCalls 恒 0 即假绿）。
// T6 静态负向锁：'pnl_offset' 这个 localStorage 键不得在页面代码里复活（剥注释后 grep）。
describe('§E1 Positions 盈亏单轨：页头读数=后端 total_pnl，清零入库留痕', () => {
  beforeEach(() => {
    cleanup()
    document.querySelectorAll('.t-message').forEach((n) => n.remove())
    localStorage.clear()
    state.balance = 100
    state.balanceErr = null
    state.holdings = []
    state.totalPnl = 0
    state.pnlOffset = 0
    state.resetCalls = 0
    state.resetErr = null
  })
  afterEach(() => cleanup())

  it('T4 页头等值锁：显示后端 total_pnl（-3.50），不显示前端自算值（200.00）', async () => {
    // 若走旧前端算式：∑(12−10)×100 = +200；后端权威读数是 -3.50 —— 两者必须能区分
    state.holdings = [{ code: '600999', name: 'E1票', quantity: 100, cost_price: 10, cur_price: 12 }]
    state.totalPnl = -3.5
    state.pnlOffset = 0
    render(<Positions />)
    await waitFor(() => {
      const txt = headerPnlText()
      expect(txt, '§E1：页头应显示后端 total_pnl').toContain('¥-3.50')
      expect(txt, '§E1：前端自算腿必须删干净（页头出现 200=双轨复发）').not.toContain('200')
    }, { timeout: 5000 })
  })

  it('T5 清零：调用后端校准端点，成功后页头归 0', async () => {
    state.holdings = [{ code: '600999', name: 'E1票', quantity: 100, cost_price: 10, cur_price: 12 }]
    state.totalPnl = 200
    render(<Positions />)
    await waitFor(() => expect(headerPnlText()).toContain('¥200.00'), { timeout: 5000 })
    fireEvent.click(screen.getByRole('button', { name: '清零' }))
    await waitFor(() => {
      expect(state.resetCalls, '§E1：清零必须打 POST /api/holdings/pnl-offset（不再只写本地）').toBe(1)
      expect(headerPnlText()).toContain('¥0.00')
    }, { timeout: 5000 })
  })

  it('T5b 清零失败：toast 可见提示，绝不静默（静默=用户以为已校准）', async () => {
    state.resetErr = new Error('403 无权限')
    render(<Positions />)
    await screen.findByText(/总盈亏/, {}, { timeout: 5000 })
    fireEvent.click(screen.getByRole('button', { name: '清零' }))
    const toast = await screen.findByText(/盈亏校准失败/, {}, { timeout: 5000 })
    expect(toast).toBeInTheDocument()
  })

  it('T6 静态负向锁：pnl_offset 键不得复活；空头浮盈不得保留本地公式', () => {
    const src = fs.readFileSync(path.join(HERE, '..', 'pages', 'Positions.jsx'), 'utf8')
    const code = src.split('\n').filter((l) => !l.trim().startsWith('//')).join('\n')
    // 带引号的 'pnl_offset' 只可能是 localStorage 键名（后端 JSON 字段写作 data.pnl_offset，不带引号）
    expect(code, '§E1：pnl_offset 已收编入库，localStorage 键不得复活').not.toMatch(/["'`]pnl_offset["'`]/)
    expect(code).not.toMatch(/localStorage\.(getItem|setItem)\(\s*['"`]pnl/)
    // 融券卡浮盈同样不得在前端重写公式（§E1 第二处两套账）
    const paper = fs.readFileSync(path.join(HERE, '..', 'pages', 'Paper.jsx'), 'utf8')
    const pcode = paper.split('\n').filter((l) => !l.trim().startsWith('//')).join('\n')
    expect(pcode, '§E1：空头浮盈应直读后端 float_pnl，不得保留 (开仓价−现价)×数量−费用 的本地算式')
      .not.toMatch(/row\.open_price - row\.mark/)
  })
})
