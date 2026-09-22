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
  },
}))

vi.mock('../api/index.js', async () => {
  const actual = await vi.importActual('../api/index.js')
  return {
    ...actual,
    isAdmin: vi.fn(() => true),
    fetchStatus: vi.fn(async () => ({ session: 'test' })),
    setLastSession: vi.fn(),
    fetchHoldings: vi.fn(async () => ({ holdings: [], available_balance: state.balance, total_realized_pnl: 0 })),
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
