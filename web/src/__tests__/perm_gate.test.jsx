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

  it('普通用户：清空全部/模拟卖出 均不渲染', async () => {
    setRole('user')
    render(<MsgCenter />)
    await waitFor(() => expect(screen.getByText(/止盈提醒·600580/)).toBeInTheDocument())
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
