// ── §PERM-GATE 20260918 权限门控回归 ──
// 覆盖：后端 admin 守卫的写入口（MsgCenter 删除/清空/模拟卖出、Signals 模拟买入）
// 在普通用户下不得渲染，管理员下正常出现——修复"成员点了必 403"的档位不齐。
// 角色由 localStorage(liangzai_role) 控制，isAdmin 走真实实现，其余 API 整页 mock。
// English: admin-guarded write affordances must not render for a normal user and must
// render for an admin; role is driven via localStorage so the real isAdmin() applies.
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'

vi.mock('../api/index.js', async (orig) => {
  const actual = await orig()
  return {
    ...actual,
    getAccount: () => 'someone',
    fetchAlerts: vi.fn(async () => [{
      id: 'a1', level: '清仓', title: '止盈提醒·600580', body: 'body',
      code: '600580.SH', name: '卧龙电驱', created_at: '2026-09-18 15:05:00',
    }]),
    fetchShortStatus: vi.fn(async () => ({ short_enabled: false })),
    deleteAlert: vi.fn(async () => ({})),
    clearAlerts: vi.fn(async () => ({})),
    sellPaperPosition: vi.fn(async () => ({})),
    reviewPositions: vi.fn(async () => ({ reviewed: 0 })),
  }
})

import MsgCenter from '../pages/MsgCenter.jsx'

function setRole(r) { localStorage.setItem('liangzai_role', r) }

describe('§PERM-GATE MsgCenter 写入口按角色显隐', () => {
  beforeEach(() => { localStorage.clear(); vi.clearAllMocks() })
  afterEach(() => { localStorage.clear() })

  it('普通用户：提醒一条都不拨，写入口不渲染', async () => {
    // §0929GATE-403：本页的提醒列表来自 GET /api/metrics/alerts（server.go:617 adminMiddleware，
    // 成员态后端实测 403——见 internal/server/r7_endpoints_test.go:221 的方向锁）。
    // 旧形态是"成员进来吃一发 403，再由 SSE 刷新与 60s 兜底反复吃"；现在按角色在 load() 里
    // 早退，所以成员态**根本不会有提醒卡**，原用例那句"提醒卡能渲染"在新语义下不成立。
    // 这里把它换成更强的三条锁：零请求 + 「未拉取」独立态 + 写入口缺席，
    // 写入口的正常渲染由下面 admin 用例反证（同一批控件，管理员必须出现）。
    // English: member sessions now never dial the admin-only alerts endpoint; assert
    // zero requests + the dedicated "not fetched" state + absent write entries.
    setRole('user')
    render(<MsgCenter />)
    await waitFor(() => expect(screen.getByText(/系统提醒未拉取/)).toBeInTheDocument())
    const api = await import('../api/index.js')
    expect(api.fetchAlerts).toHaveBeenCalledTimes(0)
    expect(screen.queryByText(/止盈提醒·600580/)).not.toBeInTheDocument()
    expect(screen.queryByText('清空全部')).not.toBeInTheDocument()
    expect(screen.queryByText('模拟卖出')).not.toBeInTheDocument()
    // 只读入口保留：立即复盘对成员可见（POST /api/review/positions 为 auth 守卫）
    expect(screen.getByText('立即复盘')).toBeInTheDocument()
  })

  it('管理员：清空全部/模拟卖出 正常渲染', async () => {
    setRole('admin')
    render(<MsgCenter />)
    await waitFor(() => expect(screen.getByText(/止盈提醒·600580/)).toBeInTheDocument())
    expect(screen.getByText('清空全部')).toBeInTheDocument()
    expect(screen.getByText('模拟卖出')).toBeInTheDocument()
  })
})
