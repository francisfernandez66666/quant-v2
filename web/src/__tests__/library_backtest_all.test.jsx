// ── 战法库「全量重测所有战法」入口测试 library_backtest_all.test.jsx ──
// §P4-ALL（2026-11-27，PLAN_FACTOR_FIX_P0_20261126 §5）：战法库以前只能逐条点「回测」，
// 全量重测要点 N 次。现在「回测」tab 有一个一键入口，id 传字符串 "all"，后端
// handleLibraryBacktest 认出 "all" 就入队一条 kind=all 的 backtest_strategy（与 CLI
// `research backtest-strategy --strategy all` 同一条 btreplay 执行路径）。
// 本文件锁三条：
//   ① 入口在「回测」tab 里渲染，文案是「全量重测所有战法」；
//   ② 点它先弹确认（耗时较长，不能误触），确认后**只**调一次
//      api.backtestLibraryRule('all', {start, end, quality:true})——id 必须是字符串 "all"，
//      不是 0/空串/列表，否则后端会当成一条规则 ID 去查库并回 400；
//   ③ 取消则一次都不调（不能"点了取消照样跑一晚上"）。
// English: the library-wide backtest entry must confirm first, then call the existing
// backtest endpoint with the literal id "all"; cancelling must send nothing.
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import Research from '../pages/Research.jsx'

// ── 整页挂载用的模块 mock：vi.mock 工厂会被提升，共享状态统一走 vi.hoisted ──
const mocks = vi.hoisted(() => ({
  confirmDialog: vi.fn(async () => true),
  backtestLibraryRule: vi.fn(async () => ({ status: 'queued', kind: 'all', task_id: 7 })),
}))

vi.mock('../ui.jsx', async (orig) => {
  const actual = await orig()
  return { ...actual, showToast: vi.fn(), confirmDialog: mocks.confirmDialog }
})

vi.mock('../api/index.js', async (orig) => {
  const actual = await orig()
  const empty = vi.fn(async () => ({}))
  return {
    ...actual,
    hasPerm: () => true,
    backtestLibraryRule: mocks.backtestLibraryRule,
    fetchResearchLibrary: vi.fn(async () => ({ library: { items: [] } })),
    // 挂载与切 tab 会打到的其他端点：一律给空响应（本页所有加载器都有 try/catch）
    fetchResearchFactors: empty,
    fetchResearchProgress: empty,
    fetchResearchCandidates: empty,
    getSchedulerStatus: empty,
    fetchBacktestToggle: empty,
    fetchAllBacktests: empty,
    fetchRunningBacktests: empty,
    fetchResearchEventFactor: empty,
    fetchOptimizations: empty,
    fetchPaperState: empty,
  }
})

async function openBacktestTab() {
  fireEvent.click(await screen.findByText('回测'))
  return screen.findByText('全量重测所有战法')
}

describe('战法库全量重测 §P4-ALL', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mocks.confirmDialog.mockResolvedValue(true)
  })

  it('入口在「回测」tab 里，确认后按 id="all" 发起一次', async () => {
    render(<Research />)
    const btn = await openBacktestTab()
    fireEvent.click(btn)
    // 确认框文案必须点明范围与耗时（因子+形态+内置五形态、盘后执行）
    await waitFor(() => expect(mocks.confirmDialog).toHaveBeenCalledTimes(1))
    const body = mocks.confirmDialog.mock.calls[0][0]
    expect(body).toContain('全量重测所有战法')
    expect(body).toContain('内置五形态')
    // 只调一次，且 id 是字符串 "all"（后端按这个字面量分派 kind=all）
    await waitFor(() => expect(mocks.backtestLibraryRule).toHaveBeenCalledTimes(1))
    const [id, opts] = mocks.backtestLibraryRule.mock.calls[0]
    expect(id).toBe('all')
    expect(opts).toMatchObject({ quality: true })
    expect(typeof opts.start).toBe('string')
    expect(typeof opts.end).toBe('string')
  })

  it('取消则一次都不调', async () => {
    mocks.confirmDialog.mockResolvedValue(false)
    render(<Research />)
    fireEvent.click(await openBacktestTab())
    await waitFor(() => expect(mocks.confirmDialog).toHaveBeenCalledTimes(1))
    expect(mocks.backtestLibraryRule).not.toHaveBeenCalled()
  }, 15000)
})
