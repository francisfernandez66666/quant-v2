// ── §H7（2026-09-22 修复批）auth:expired 闭包死亡 回归用例 ──
// 锁死行为：挂载 effect 注册的 auth:expired 监听器必须能读到「最新」登录态——
// 旧实现首帧闭包捕获 loggedIn=false 恒早退，登录成功后 expired 事件到达时
// 登出提示/登出动作永不出现在部分路径。修法为 ref 镜像 + 转发壳注册。
// English: §H7 regression — the auth:expired listener registered once at mount must observe
// the latest loggedIn state (previously frozen at the first-frame false), so a logged-in
// session really logs out when the event arrives.
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import React from 'react'
import { render, screen, waitFor, act, cleanup } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'

// 每个用例独立控制 isLoggedIn 的返回值（vi.doMock 工厂在动态 import 时才求值，闭包安全）
let loggedFlag = true

// §H7 用例共用 API mock 基座：默认全接口成功，单测按需覆写个别方法；loggedFlag 控制登录态。
function apiMockBase() {
  return {
    isLoggedIn: () => loggedFlag,
    getAccount: () => 'alice',
    getStoredServer: () => '',
    setStoredServer: () => {},
    hasPerm: () => false,
    isAdmin: () => false,
    refreshMe: vi.fn(async () => ({ role: 'user', perms: [] })),
    login: vi.fn(async () => ({})),
    logout: vi.fn(),
    // §M-11（2026-09-22 修复批）App.checkAuth 恢复登录态后会补报推送账号（api.syncPushAccount），
    // mock 基座必须覆盖该导出，否则属性访问即抛「No export defined on mock」。
    syncPushAccount: vi.fn(),
    fetchStatus: vi.fn(async () => ({ signal_count: 0, in_trade_time: false, active: false })),
    fetchAlerts: vi.fn(async () => []),
    fetchShortStatus: vi.fn(async () => ({ short_enabled: false })),
    fetchPaperState: vi.fn(async () => ({ enabled: false })),
    toggleShort: vi.fn(async () => ({ short_enabled: false })),
    connectSSE: vi.fn(async () => {}),
    disconnectSSE: vi.fn(),
    onSSE: vi.fn(() => () => {}),
  }
}

async function mountApp() {
  vi.resetModules()
  const api = apiMockBase()
  vi.doMock('../api/index.js', () => api)
  // 首屏页 Dashboard 与全部业务接口解耦掉：本用例只锁 App 壳层的 expired 行为
  vi.doMock('../pages/Dashboard.jsx', () => ({
    default: () => <div data-testid="dashboard-stub">仪表盘</div>,
  }))
  const { default: App } = await import('../App.jsx')
  const utils = render(
    <MemoryRouter initialEntries={['/dashboard']}>
      <App />
    </MemoryRouter>
  )
  return { api, utils }
}

describe('App - auth:expired 事件读取最新登录态（§H7）', () => {
  beforeEach(() => {
    localStorage.clear()
    loggedFlag = true
  })
  // §H7 收尾核验加固：用例超时（高负载下 App+tdesign 导入链 >20s）会跳过默认清理，
  // 残留首挂载体让下一条用例的 getByText 命中「multiple elements」级联红。显式 cleanup 斩断。
  afterEach(() => { cleanup() })

  it('loggedIn false→true 后收到 expired 事件：执行登出并回到登录页（旧闭包恒早退→必红）', { timeout: 60000 }, async () => {
    const { api } = await mountApp()
    // checkAuth 异步恢复登录态 → 主布局渲染（顶栏「退出」按钮出现）
    await waitFor(() => expect(screen.getByText('退出')).toBeInTheDocument())
    expect(api.connectSSE).toHaveBeenCalled() // 登录态链路已拉起

    // 模拟 request() 401 后广播的全局过期事件
    await act(async () => { window.dispatchEvent(new Event('auth:expired')) })

    // §H7 断言：事件必须真正走到登出（旧实现首帧 loggedIn=false 早退，永远不会调 logout）
    expect(api.logout).toHaveBeenCalledTimes(1)
    await waitFor(() => expect(screen.getByText('量化交易辅助工具')).toBeInTheDocument())
  })

  it('未登录态收到 expired 事件：仍然静默跳过，不误触发登出', { timeout: 60000 }, async () => {
    loggedFlag = false
    const { api } = await mountApp()
    // 登录页渲染（无「退出」按钮）
    await waitFor(() => expect(screen.getByText('量化交易辅助工具')).toBeInTheDocument())
    expect(screen.queryByText('退出')).not.toBeInTheDocument()

    await act(async () => { window.dispatchEvent(new Event('auth:expired')) })
    // 守卫语义保留：未登录时不该调用 logout / 弹错误提示
    expect(api.logout).not.toHaveBeenCalled()
  })
})
