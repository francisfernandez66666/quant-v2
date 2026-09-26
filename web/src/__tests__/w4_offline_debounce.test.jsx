// ── §0926E2E-13（FIX_PLAN_20260926E2E 四波 13 项）前端离线判定去抖 回归用例 ──
//
// 缺陷原文：App.refreshStatus 的 catch 里一次 fetchStatus 失败即 setServerOnline(false)——
// 单次网络/服务端抖动当场把页面闪成「离线」并弹出断联横幅（也是 UAT 并发负载下假红的成因之一）。
//
// 修法锁定的三条行为（判定阈值 OFFLINE_AFTER_FAILS=2，见 App.jsx）：
//  L1 会话中首轮失败：保持「服务在线」+ 灰条「展示上次数据」，断联横幅不得出现；
//  L2 连续第二轮失败：才转「离线」+ 琥珀断联横幅，灰条让位（不双条叠显）；
//  L3 成功一轮清零计数：失败→成功→失败（隔轮两败）仍是在线+灰条，绝不因累计失败判离线。
//
// 反向防自伤说明：灰条/横幅断言用正则整句片段匹配 App.jsx 现文案；文案改版须同步本锁，
// 不许把断言放宽成 queryAll 或去文案化（放宽=假绿）。
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { StrictMode } from 'react'
import { render, screen, act, cleanup } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'

// 状态轮询 API mock 基座：fetchStatus 由用例注入序列桩，其余端点默认全成功，
// 保证「离线」判定只由 fetchStatus 的失败序列驱动（排除 fetchAlerts/fetchShortStatus 干扰）。
function apiMockBase(fetchStatusImpl) {
  return {
    isLoggedIn: () => true,
    getAccount: () => 'alice',
    getStoredServer: () => '',
    setStoredServer: () => {},
    hasPerm: () => false,
    isAdmin: () => false,
    refreshMe: vi.fn(async () => ({ role: 'user', perms: [] })),
    login: vi.fn(async () => ({})),
    logout: vi.fn(),
    syncPushAccount: vi.fn(),
    fetchStatus: fetchStatusImpl,
    fetchAlerts: vi.fn(async () => []),
    fetchShortStatus: vi.fn(async () => ({ short_enabled: false })),
    fetchPaperState: vi.fn(async () => ({ enabled: false })),
    toggleShort: vi.fn(async () => ({ short_enabled: false })),
    connectSSE: vi.fn(async () => {}),
    disconnectSSE: vi.fn(),
    onSSE: vi.fn(() => () => {}),
  }
}

// fake timers 下把挂载链跑干：照 settle.js 的 12 轮微任务排空（App.checkAuth → refreshMe →
// startPolling → fetchStatus 链厚，单轮 act 只推进一层；轮数确定性，与墙钟无关）。
async function flush(rounds = 12) {
  for (let i = 0; i < rounds; i++) await act(async () => {})
}

// 推进一根 60s 轮询间隔并排空在飞 promise。
async function tickPoll() {
  await act(async () => { await vi.advanceTimersByTimeAsync(60000) })
  await flush()
}

const OK_STATUS = { signal_count: 0, in_trade_time: false, active: false, build_commit: 'same' }

// fetchSeq：按 'ok'/'fail' 序列驱动的 fetchStatus 桩；序列耗尽后恒回 ok（防止无关后续轮次
// 污染断言窗口）。第 1 轮恒为 'ok'：模拟"会话已在线"基线——这正是缺陷现场（用着用着闪离线）。
function fetchSeq(seq) {
  let i = 0
  return vi.fn(async () => {
    const step = i < seq.length ? seq[i] : 'ok'
    i += 1
    if (step === 'fail') throw new Error('模拟网络失败')
    return { ...OK_STATUS }
  })
}

async function mountApp(seq) {
  vi.resetModules()
  vi.doMock('../api/index.js', () => apiMockBase(fetchSeq(seq)))
  // 仪表盘整页解耦：本用例只锁 App 壳层的离线判定，不引 Dashboard 的接口面
  vi.doMock('../pages/Dashboard.jsx', () => ({
    default: () => <div data-testid="dashboard-stub">仪表盘</div>,
  }))
  const { default: App } = await import('../App.jsx')
  render(
    <MemoryRouter initialEntries={['/dashboard']}>
      <App />
    </MemoryRouter>
  )
  await flush() // 走完 checkAuth → startPolling → 首轮 refreshStatus
}

describe('§0926E2E-13 连续失败才转离线（去抖 + 灰条 + 计数清零）', () => {
  beforeEach(() => {
    localStorage.clear()
    vi.useFakeTimers()
  })
  afterEach(() => {
    vi.useRealTimers()
    cleanup()
  })

  // L1：会话中单次失败——在线 + 灰条，不得出现断联横幅；下一轮成功灰条收回
  it('L1 单次失败保持在线并出灰条（旧实现当场判离线→必红）', async () => {
    await mountApp(['ok', 'fail', 'ok'])
    expect(screen.getByText('服务在线')).toBeInTheDocument() // 基线：首轮成功在线
    expect(screen.queryByText(/网络瞬时抖动/), '未失败时不得出灰条').not.toBeInTheDocument()

    await tickPoll() // 第 2 轮失败：单次抖动
    expect(screen.getByText('服务在线'), '首轮失败后仍应判定在线（旧实现当场判离线）').toBeInTheDocument()
    expect(screen.getByText(/网络瞬时抖动/), '首轮失败必须出灰条知情提示').toBeInTheDocument()
    expect(screen.queryByText(/无法连接服务器/), '单轮失败不得弹断联横幅').not.toBeInTheDocument()

    await tickPoll() // 第 3 轮成功：灰条收回
    expect(screen.queryByText(/网络瞬时抖动/), '成功后灰条必须收回').not.toBeInTheDocument()
    expect(screen.getByText('服务在线')).toBeInTheDocument()
  })

  // L2：连续两轮失败——第二轮才转离线 + 断联横幅，灰条让位不叠显
  it('L2 连续两轮失败才出现「离线」与断联横幅', async () => {
    await mountApp(['ok', 'fail', 'fail'])
    await tickPoll() // 第 1 败：仍是灰条通道
    expect(screen.getByText('服务在线')).toBeInTheDocument()
    // 第 2 败：连败满阈值才判离线
    await tickPoll()
    expect(screen.getByText('离线'), '连续两轮失败必须转离线态').toBeInTheDocument()
    expect(screen.getByText(/无法连接服务器/), '离线态必须出断联横幅').toBeInTheDocument()
    expect(screen.queryByText(/网络瞬时抖动/), '离线横幅与灰条不得双条叠显').not.toBeInTheDocument()
  })

  // L3：成功一轮清零——「失败→成功→失败」累计两败但从不连续，绝不离线
  it('L3 成功后计数复位：隔轮的单次失败不累计判死', async () => {
    await mountApp(['ok', 'fail', 'ok', 'fail', 'ok'])
    await tickPoll() // 第 1 败（累计 1）
    expect(screen.getByText('服务在线')).toBeInTheDocument()
    await tickPoll() // 成功 → 计数清零
    expect(screen.queryByText(/网络瞬时抖动/)).not.toBeInTheDocument()
    await tickPoll() // 第 2 次失败（累计 2、连续 1）：仍走灰条通道
    expect(screen.getByText('服务在线'), '隔轮失败不得转离线（计数未随成功清零的回归）').toBeInTheDocument()
    expect(screen.getByText(/网络瞬时抖动/), '隔轮失败仍走灰条通道').toBeInTheDocument()
    expect(screen.queryByText(/无法连接服务器/), '累计两败不得出断联横幅').not.toBeInTheDocument()
    await tickPoll() // 成功收口
    expect(screen.getByText('服务在线')).toBeInTheDocument()
    expect(screen.queryByText(/网络瞬时抖动/)).not.toBeInTheDocument()
  })

  // L4：§0926E2E-13b（09-27 Playwright 双发事故反照）——StrictMode 双挂载下，
  // 挂载 effect 的两遍「异步恢复登录态→拉起轮询」只允许第二遍落地：
  // 一次挂载恰好发 1 根 fetchStatus（旧实现=2 根连发，启动瞬间把 §13 计数打满、
  // 且第一遍的 60s 定时器句柄被覆盖后永久泄漏）。反证：摘掉 App.jsx 的 active 守卫→必红。
  it('L4 StrictMode 双挂载只拉起一份轮询（首发 fetchStatus 恰 1 根）', async () => {
    vi.resetModules()
    const fetchStatus = vi.fn(async () => ({ ...OK_STATUS }))
    vi.doMock('../api/index.js', () => apiMockBase(fetchStatus))
    vi.doMock('../pages/Dashboard.jsx', () => ({
      default: () => <div data-testid="dashboard-stub">仪表盘</div>,
    }))
    const { default: App } = await import('../App.jsx')
    render(
      <StrictMode>
        <MemoryRouter initialEntries={['/dashboard']}>
          <App />
        </MemoryRouter>
      </StrictMode>
    )
    await flush() // 两遍 checkAuth 的 then 都已落地（轮数远超双挂载所需）
    expect(fetchStatus).toHaveBeenCalledTimes(1)
  })
})
