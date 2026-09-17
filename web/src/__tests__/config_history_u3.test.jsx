// ── §D-3（GAP_VERIFY_20260917_PM）Settings 配置历史/回滚卡 ──
// 锁定"API 就绪但 UI 未接"的补齐：admin 进入即拉快照列表，回滚走二次确认（取消不调写端点），
// 成员账号整卡隐藏。后端契约与权限在 Go 侧已有测试，本文件只锁前端半边接线。
import { describe, it, expect, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'

let ROLE = 'admin'
const SNAP = { snapshots: [{ snapshot_ts: '2026-09-17T10:00:00Z', path: '/x/rules-20260917.json' }, { snapshot_ts: '2026-09-16T09:00:00Z', path: '/x/rules-20260916.json' }] }

vi.mock('../api/index.js', () => ({
  getStoredServer: () => 'http://127.0.0.1:8080',
  setStoredServer: vi.fn(),
  getAccount: () => 'admin',
  getRole: () => ROLE,
  fetchStatus: vi.fn(async () => ({})),
  fetchLLMConfig: vi.fn(async () => ({ api_url: '', model: '', classifier_model: '', batch_concurrency: 4, d1_max_tokens: 2048, api_keys: [], configured: false })),
  fetchStrategyConfig: vi.fn(async () => ({ dragon: {}, double_bump: {}, n_shape: {}, dragon_return: {}, momentum: {} })),
  fetchNewsShowAllStatus: vi.fn(async () => ({ show_all: false })),
  fetchConfigHistory: vi.fn(async () => SNAP),
  fetchStrategySnapshots: vi.fn(async () => ({ snapshots: [{ snapshot_ts: '2026-09-15T08:00:00Z', path: '/y' }] })),
  rollbackConfig: vi.fn(async () => ({ status: 'ok' })),
  rollbackStrategyParams: vi.fn(async () => ({ status: 'ok' })),
}))

import Settings from '../pages/Settings.jsx'

describe('Settings 配置历史卡（§D-3）', () => {
  it('admin：进页拉快照列表，渲染两类快照 ts', async () => {
    ROLE = 'admin'
    const api = await import('../api/index.js')
    render(<Settings />)
    expect(await screen.findByText('配置历史与回滚')).toBeInTheDocument()
    await waitFor(() => expect(api.fetchConfigHistory).toHaveBeenCalled())
    expect(screen.getByText('2026-09-17T10:00:00Z')).toBeInTheDocument()
    expect(screen.getByText('2026-09-15T08:00:00Z')).toBeInTheDocument()
    expect(screen.getByText(/共 2 个规则快照 \/ 1 个战法参数快照/)).toBeInTheDocument()
  })

  it('回滚按钮 → 二次确认弹窗；取消不触达写端点', async () => {
    ROLE = 'admin'
    const api = await import('../api/index.js')
    render(<Settings />)
    await screen.findByText('2026-09-17T10:00:00Z') // 等列表数据落地（按钮在行内）
    fireEvent.click(screen.getAllByText('回滚')[0])
    expect(await screen.findByText('确认回滚配置')).toBeInTheDocument()
    expect(screen.getByText(/原子恢复到快照/)).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: '取消' }))
    // jsdom 不触发 CSS transition 的 closed 回调，弹窗元素滞留 DOM——以隐藏为关闭判据
    await waitFor(() => expect(screen.getByText('确认回滚配置')).not.toBeVisible())
    expect(api.rollbackConfig).not.toHaveBeenCalled()
    expect(api.rollbackStrategyParams).not.toHaveBeenCalled()
  })

  it('成员账号整卡隐藏（写端点 admin-only）', async () => {
    ROLE = 'user'
    render(<Settings />)
    // 等其它卡片渲染完成后仍无历史卡
    await screen.findByText('服务器连接')
    expect(screen.queryByText('配置历史与回滚')).not.toBeInTheDocument()
  })
})
