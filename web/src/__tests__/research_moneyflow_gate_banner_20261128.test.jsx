// ── 研究页「资金流采集闸」横幅测试 research_moneyflow_gate_banner_20261128.test.jsx ──
// §REPLAY-INPUTS 档 3b（2026-11-28）：夜间链出队前会等个股资金流采集的**当日完成标记**，
// 等不到就到兜底时限（17:00）放行。这道闸的生效值与标记状态只在 researchd 日志里出现的话，
// 现场形态就是"今晚零产出，第二天没人分得清是被闸拦的还是队列坏了"——
// 本仓反复吃过这一类"后端有键、页面看不见"的亏（§FLOW-DASH / §BOX-HEARTBEAT 同族）。
//
// 三条断言：
//   ① 三个键齐全 ⇒ 横幅出现，且闸门态与标记日/状态逐字对上；
//   ② 旧 researchd 没写这三个键 ⇒ **整行不渲染**（不许把 undefined 画成"关"，
//      那是"没读数"冒充"坏读数"，与"缺数不许当 0"同一条纪律）；
//   ③ 显式关闭（false）⇒ 必须渲染成"关"——布尔 false 不能被 in 判据之外的假值判断吞掉。
// English: the money-flow gate readout must render when the keys exist, stay absent when the
// backend is older, and show 关 (not "missing") for an explicit false.
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import Research from '../pages/Research.jsx'

// 每次用例要换 scheduler 返回值，用 vi.hoisted 建一个可变槽。
const slot = vi.hoisted(() => ({ sched: {} }))

vi.mock('../ui.jsx', async (orig) => {
  const actual = await orig()
  return { ...actual, showToast: vi.fn(), confirmDialog: vi.fn(async () => true) }
})

// 整页挂载：研究页会打十几个端点，全部给空响应（每个加载器都有 try/catch），
// 只有 getSchedulerStatus 走可变槽——本文件测的就是它带不带那三个键的三种分形。
vi.mock('../api/index.js', async (orig) => {
  const actual = await orig()
  const empty = vi.fn(async () => ({}))
  return {
    ...actual,
    hasPerm: () => true,
    fetchResearchLibrary: vi.fn(async () => ({ library: { items: [] } })),
    fetchResearchFactors: empty,
    fetchResearchProgress: empty,
    fetchResearchCandidates: empty,
    fetchBacktestToggle: empty,
    fetchAllBacktests: empty,
    fetchRunningBacktests: empty,
    fetchResearchEventFactor: empty,
    fetchOptimizations: empty,
    fetchPaperState: empty,
    getSchedulerStatus: vi.fn(async () => slot.sched),
  }
})

describe('研究页资金流采集闸横幅 §REPLAY-INPUTS 档 3b', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('① 三键齐全 ⇒ 横幅点名闸门与今日标记', async () => {
    slot.sched = {
      reason: '空闲',
      busy: false,
      beijing_now: '2026-11-28 16:05:00',
      mem_avail_mb: 1200,
      mem_gate_open: true,
      in_trading_window: false,
      moneyflow_gate_on: true,
      moneyflow_marker_day: '20261127',
      moneyflow_marker_status: 'partial',
    }
    render(<Research />)
    const line = await screen.findByTestId('moneyflow-gate')
    expect(line.textContent).toContain('资金流采集闸=开')
    // 标记日与状态必须一起出——只出"有标记"读不出那一轮到底采到了什么。
    expect(line.textContent).toContain('20261127')
    expect(line.textContent).toContain('partial')
  })

  it('② 旧后端没有这三键 ⇒ 整行不渲染（不拿 undefined 冒充"关"）', async () => {
    slot.sched = { reason: '空闲', busy: false, beijing_now: '2026-11-28 16:05:00' }
    render(<Research />)
    // 先等调度横幅本体出现，否则"没渲染"可能只是页面还没加载完。
    await screen.findByText(/研究调度状态/)
    await waitFor(() => expect(screen.queryByTestId('moneyflow-gate')).toBeNull())
  })

  it('③ 显式关闭 ⇒ 渲染成"关"而不是缺席', async () => {
    slot.sched = { reason: '空闲', moneyflow_gate_on: false }
    render(<Research />)
    const line = await screen.findByTestId('moneyflow-gate')
    expect(line.textContent).toContain('资金流采集闸=关')
    // 关状态下没有标记也是合法态，文案要读得出"未跑"而不是空串。
    expect(line.textContent).toContain('无（未跑或读不到）')
  })
})
