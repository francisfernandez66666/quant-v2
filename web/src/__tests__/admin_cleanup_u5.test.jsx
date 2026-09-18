// ── §U-5（2026-09-14 像素级 UAT 修复批）Admin 页"清理失效账号"入口回归 ──
// 挂载 Admin 并断言账号列表卡右上角的清理按钮存在（后端端点早已就绪、前端此前零入口）。
// English: §U-5 regression — mount Admin and assert the stale-account cleanup entry now exists on
// the account-list card (the backend endpoint existed with no frontend entry before).
import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
// §A5：Admin 页接入 useNavigate（首拉 403 跳 /403），渲染需 Router 上下文
import { MemoryRouter } from 'react-router-dom'

// api 层整体打桩：Admin 首屏要拉账号列表/日志日期/日志明细，这里统一给空数据返回值，
// 让用例只锁「清理失效账号」入口是否渲染，不触达真实后端；cleanupAdminUsers 仅用于断言未被误调。
// English: the whole api module is stubbed with empty payloads so the case only pins the entry rendering.
vi.mock('../api/index.js', () => ({
  getAccount: () => 'admin',
  isAdmin: () => true,
  fetchAdminUsers: vi.fn(async () => ({ users: [], perms: [] })),
  fetchOpslogDates: vi.fn(async () => ({ dates: [] })),
  fetchOpslog: vi.fn(async () => ({ lines: [], total: 0, truncated: false })),
  cleanupAdminUsers: vi.fn(async () => ({ deleted: [], count: 0, dry_run: true })),
}))

import Admin from '../pages/Admin.jsx'

describe('Admin 页 §U-5 清理失效账号入口', () => {
  it('账号列表卡渲染"清理失效账号"按钮', () => {
    render(<MemoryRouter><Admin /></MemoryRouter>)
    expect(screen.getByText('账号列表')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /清理失效账号/ })).toBeInTheDocument()
  })
})
