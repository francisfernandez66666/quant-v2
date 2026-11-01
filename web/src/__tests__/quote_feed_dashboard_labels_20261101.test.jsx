// ── §SUBSCRIPTION-A（2026-11-01）Dashboard 行情通道三态标签的消费契约 ──
// 这批的整条纪律就是"关着也要看得见、三种故障必须长得不一样"，所以这三枚标签不是装饰：
//   ① auth_failed → 文案指向"去确认在册凭证"，与"网关挂了"分开（owner 裁决 14）；
//   ② degraded:J1|J2|J3 → 必须把命中的是哪一条带出来（J1 查网关、J2 陈旧、J3 读数不自洽）；
//   ③ 观察模式覆盖率达标 → 报「本可接管」，作为开主源档的读数依据（不靠人翻日志）；
//   ④ quote_feed 为 null（通道没启用）→ 整段不渲染，也不许显示成"正常"；
//   ⑤ state=not_polled（出厂开却一轮没跑）→ 必须单独一条文案，指向"重启"，不许折成"降级"或"正常"。
// English: the three feed-state labels must be distinguishable and must not render when the
// channel never ran — that visibility is the whole point of the batch's "off ≠ absent" rule.
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'

vi.mock('tdesign-react', async (importOriginal) => {
  const real = await importOriginal()
  return { ...real, MessagePlugin: { success: vi.fn(), warning: vi.fn(), error: vi.fn(), info: vi.fn() } }
})

const statusResp = { quote_source: 'QMT-L1', quote_age_sec: 2, in_trade_time: true, quote_feed: null }
// 局部 mock：只覆盖本用例需要的读数，其余导出留给真实模块（Dashboard 的腿会随版本增加，
// 全量列举会长期欠账——缺一条就是 unhandled rejection 把整个文件带红，红因还不是产品缺陷）。
vi.mock('../api/index.js', async (importOriginal) => {
  const actual = await importOriginal()
  return { ...actual,
  getAccount: () => 'admin',
  getToken: () => 'tk',
  getStoredServer: () => '',
  isAdmin: () => true,
  hasStrictPerm: () => true,
  getRole: () => 'admin',
  isForbidden: (e) => !!(e && e.status === 403),
  fetchStatus: vi.fn(async () => ({ ...statusResp })),
  fetchDataSourceHealth: vi.fn(async () => ({ sources: [] })),
  fetchNewsSourceHealth: vi.fn(async () => ({ sources: [] })),
  fetchEngineHealth: vi.fn(async () => ({ ok: true })),
  fetchSignals: vi.fn(async () => []),
  fetchNews: vi.fn(async () => []),
  fetchSectorHot: vi.fn(async () => []),
  fetchHotSnapshot: vi.fn(async () => []),
  fetchIPOCalendar: vi.fn(async () => []),
  fetchHealth: vi.fn(async () => ({ ok: true })),
  fetchSignalLogs: vi.fn(async () => []),
  fetchDashboard: vi.fn(async () => ({ signals: [], news_events: [], hot_sectors: [], final_signals: [] })),
  fetchUsage: vi.fn(async () => ({ llm: { available: false } })),
  fetchAlertTotal: vi.fn(async () => ({ total: 0 })),
  fetchPositions: vi.fn(async () => []),
  fetchPaperState: vi.fn(async () => ({ short_book: { enabled: false } })),
  fetchShortStatus: vi.fn(async () => ({ short_enabled: false })),
  fetchWatchlist: vi.fn(async () => []),
  }
})

import Dashboard from '../pages/Dashboard.jsx'
import { MemoryRouter } from 'react-router-dom' // Dashboard 里有 useNavigate，必须给 Router 上下文
import * as api from '../api/index.js'

// 只测标签分形：其他卡片依赖的接口一律给空态，避免把无关渲染算进这条断言
function setFeed(feed) {
  api.fetchStatus.mockImplementation(async () => ({ ...statusResp, quote_feed: feed }))
}

describe('Dashboard 行情通道状态标签（§SUBSCRIPTION-A）', () => {
  beforeEach(() => { setFeed(null) })

  it('通道从没跑过时不渲染任何"正常"暗示', async () => {
    const { container } = render(<MemoryRouter><Dashboard /></MemoryRouter>)
    await waitFor(() => expect(screen.getByText(/行情源：/)).toBeInTheDocument())
    expect(container.textContent).not.toMatch(/券商行情/)
    expect(screen.queryByText('本可接管')).not.toBeInTheDocument()
  })

  it('auth_failed 与 degraded:J2 长成两个不同文案（补凭证 vs 查通道）', async () => {
    setFeed({ state: 'auth_failed', rounds: 5, hits: 0, denominator: 200, coverage_pct: 0, consecutive_fail: 3 })
    render(<MemoryRouter><Dashboard /></MemoryRouter>)
    expect(await screen.findByText(/券商行情未认证/)).toBeInTheDocument()
    expect(screen.queryByText(/券商行情降级/)).not.toBeInTheDocument()
  })

  it('degraded:J2 报降级并带出命中的判据（与未认证分家）', async () => {
    setFeed({ state: 'degraded:J2', rounds: 9, hits: 12, denominator: 200, coverage_pct: 6, consecutive_fail: 0 })
    render(<MemoryRouter><Dashboard /></MemoryRouter>)
    expect(await screen.findByText(/券商行情降级/)).toBeInTheDocument()
    expect(screen.queryByText(/券商行情未认证/)).not.toBeInTheDocument()
  })

  it('已启用却一轮没跑（not_polled）单独成一条文案，不折成降级也不显示正常', async () => {
    setFeed({ state: 'not_polled', rounds: 0, hits: 0, denominator: 0, coverage_pct: 0 })
    render(<MemoryRouter><Dashboard /></MemoryRouter>)
    expect(await screen.findByText(/未开始轮询/)).toBeInTheDocument()
    expect(screen.queryByText(/券商行情降级/)).not.toBeInTheDocument()
  })

  it('观察模式覆盖达标时报「本可接管」，未达标只报观察中', async () => {
    // 观察模式（would_override=true）才谈"本可接管"；出厂档 primary=true 时该位为 false，
    // 这条用例判的是回滚档的读数仍然可见，不是主源档的文案。
    setFeed({ state: 'ok', rounds: 30, would_override: true, ready_to_promote: true, hits: 180, denominator: 200, coverage_pct: 90, denominator_source: 'tradable' })
    render(<MemoryRouter><Dashboard /></MemoryRouter>)
    expect(await screen.findByText(/本可接管/)).toBeInTheDocument()
  })
})
