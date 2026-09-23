// ── 战法库「停用」确认文案测试 library_disable_confirm.test.jsx ──
// §EXIT-RETAIN（2026-09-23）修复的语义：出场参数覆盖（移动止盈 exit_trail_pct / 最长持有
// exit_max_hold_days）**跟着持仓走，不跟着启停开关走**。此前后端 rule_exit_overrides.go
// 在条目 enabled=false 时直接跳过注册，于是「停用一条战法」会把它名下持仓的出场参数
// 悄悄改写成全局默认（8%/15 天）；而 stale_adj_basis_action=disable 这条无人值守路径
// 会在没人决定的情况下停用战法，方向不可预知。
// 现在停用只切断新开仓。前端必须把这件事说清楚，本文件锁四条：
//   ①open_positions > 0：文案既讲「只断新开仓」，也讲「名下 N 笔持仓继续按本战法出场参数
//     离场、不会改按全局默认」（持仓数量要真的出现在文案里）；
//   ②open_positions === 0（含老后端缺字段）：只讲断新开仓，不得谎称有持仓仍生效；
//   ③卡片「持仓 N」标记：>0 才渲染，让「停用了但覆盖仍在生效」这件事可见；
//   ④真实点击链路（整页挂载）：点「停用」→ 先弹上面这段文案，取消则**不调用**启停接口；
//     「启用」方向不弹确认。
// English: exit-parameter overrides follow positions, not the enable flag. The disable confirmation
// must spell out the new-buy-only cutoff for rules that still hold positions, and stay honest when
// there are none; cancelling the dialog must not persist anything.
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import Research, { libraryDisableConfirmBody, LibraryHeldTag } from '../pages/Research.jsx'

// 战法库两条：fac_1 名下还有 2 笔开放持仓，fac_2 一笔都没有。
// 页面会就地改 enabled（toggleLibrary 里 s.enabled = !s.enabled），所以每条用例都取浅拷贝，
// 避免用例之间互相污染。
const HELD = {
  kind: 'factor', id: 'fac_1', name: '波动突破', enabled: true, open_positions: 2,
  applied_at: '2026-09-20 16:00:00', signal_count: 12, win: 7, loss: 5, cum_return: 0.11,
}
const IDLE = {
  kind: 'factor', id: 'fac_2', name: '缩量回踩', enabled: true, open_positions: 0,
  applied_at: '2026-09-21 16:00:00', signal_count: 3, win: 1, loss: 2, cum_return: -0.02,
}
const held = () => ({ ...HELD })
const idle = () => ({ ...IDLE })

// ── 整页挂载用的模块 mock：vi.mock 工厂会被提升，共享状态统一走 vi.hoisted ──
const mocks = vi.hoisted(() => ({
  confirmDialog: vi.fn(async () => false),
  setResearchLibraryEnabled: vi.fn(async () => ({ ok: true })),
  library: { items: [] }, // 每条用例自行设置，避免用例间互相污染
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
    hasPerm: () => true, // 战法库卡片的操作按钮组仅 research_approve 可见
    fetchResearchLibrary: vi.fn(async () => ({ library: mocks.library.items })),
    setResearchLibraryEnabled: mocks.setResearchLibraryEnabled,
    // 挂载时的其他端点：一律给空响应，避免测试里真的去发 fetch（本页所有加载器都有 try/catch）
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

// 切到「战法库」页签并等指定名称的战法卡片渲染出来
async function openLibraryTab(name) {
  fireEvent.click(await screen.findByText('战法库'))
  expect(await screen.findByText(name)).toBeInTheDocument()
}

describe('战法库停用确认文案 §EXIT-RETAIN', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mocks.confirmDialog.mockResolvedValue(false) // 默认：用户点「取消」
    mocks.library.items = [held(), idle()]
  })

  it('open_positions > 0：说清「只断新开仓」+「名下持仓仍按本战法出场参数离场」', () => {
    const body = libraryDisableConfirmBody(HELD)
    // ① 停用只切断新的买入信号
    expect(body).toContain('只切断新的买入信号')
    // ② 持仓数量取自后端 open_positions，并明确这些持仓继续按本战法出场参数离场
    expect(body).toContain('仍有 2 笔开放持仓')
    expect(body).toContain('本战法的出场参数')
    expect(body).toContain('移动止盈/最长持有')
    // ③ 不能被误读成「改按全局默认」
    expect(body).toContain('不会改按全局默认')
    // ④ 撤销覆盖的唯一办法说清楚（平仓 / 删规则）
    expect(body).toContain('删除本战法')
  })

  it('open_positions === 0（含老后端缺字段）：只断新开仓，不谎称有持仓生效', () => {
    const body = libraryDisableConfirmBody(IDLE)
    expect(body).toContain('不再产生新的买入信号')
    expect(body).toContain('当前没有开放持仓挂在该战法下')
    expect(body).not.toContain('本战法的出场参数')
    // 缺字段（老后端未下发 open_positions）按 0 计
    const legacy = { kind: 'factor', id: 'fac_3', name: '缩量回踩', enabled: true }
    expect(libraryDisableConfirmBody(legacy)).toBe(body)
  })

  it('卡片「持仓 N」标记：>0 渲染、=0 不渲染', () => {
    const { container } = render(<LibraryHeldTag strategy={HELD} />)
    const tag = screen.getByText('持仓 2')
    expect(tag).toBeInTheDocument()
    expect(tag.getAttribute('title')).toContain('即使停用')
    expect(container.textContent).not.toBe('')
    const empty = render(<LibraryHeldTag strategy={IDLE} />)
    expect(screen.queryByText('持仓 0')).not.toBeInTheDocument()
    expect(empty.container.firstChild).toBeNull()
  })

  it('真实点击链路：点「停用」先弹上面这段文案，取消则不调用启停接口', async () => {
    render(<Research />)
    await openLibraryTab('波动突破')
    expect(screen.getByText('持仓 2')).toBeInTheDocument() // fac_1 有挂靠持仓
    expect(screen.queryByText('持仓 0')).not.toBeInTheDocument() // fac_2 没有

    const disableBtns = screen.getAllByText('停用')
    expect(disableBtns.length).toBe(2)

    // 有持仓的那条：文案必须点明出场覆盖仍跟着持仓走
    fireEvent.click(disableBtns[0])
    await waitFor(() => expect(mocks.confirmDialog).toHaveBeenCalledTimes(1))
    expect(mocks.confirmDialog.mock.calls[0][0]).toContain('确定停用战法 波动突破')
    expect(mocks.confirmDialog.mock.calls[0][0]).toContain('仍有 2 笔开放持仓')
    expect(mocks.confirmDialog.mock.calls[0][0]).toContain('不会改按全局默认')
    expect(mocks.setResearchLibraryEnabled).not.toHaveBeenCalled() // 取消 → 不落库

    // 无持仓的那条：只讲断新开仓
    fireEvent.click(screen.getAllByText('停用')[1])
    await waitFor(() => expect(mocks.confirmDialog).toHaveBeenCalledTimes(2))
    expect(mocks.confirmDialog.mock.calls[1][0]).toContain('确定停用战法 缩量回踩')
    expect(mocks.confirmDialog.mock.calls[1][0]).toContain('当前没有开放持仓挂在该战法下')
    expect(mocks.setResearchLibraryEnabled).not.toHaveBeenCalled()
  }, 15000)

  it('确认后才落库；「启用」方向不弹确认', async () => {
    mocks.confirmDialog.mockResolvedValue(true)
    const first = render(<Research />)
    await openLibraryTab('波动突破')
    fireEvent.click(screen.getAllByText('停用')[0])
    await waitFor(() => expect(mocks.setResearchLibraryEnabled).toHaveBeenCalledWith('fac_1', false))
    expect(mocks.confirmDialog).toHaveBeenCalledTimes(1)
    first.unmount() // 卸载整页，否则第二个实例的页签/按钮会与上一份重名

    // 已停用条目的按钮是「启用」：直接落库，不需要确认
    mocks.confirmDialog.mockClear()
    mocks.setResearchLibraryEnabled.mockClear()
    const off = held()
    off.enabled = false
    mocks.library.items = [off]
    render(<Research />)
    await openLibraryTab('波动突破')
    fireEvent.click(await screen.findByText('启用'))
    await waitFor(() => expect(mocks.setResearchLibraryEnabled).toHaveBeenCalledWith('fac_1', true))
    expect(mocks.confirmDialog).not.toHaveBeenCalled()
  }, 15000)
})
