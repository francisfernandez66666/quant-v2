// ── §MT（2026-09-17）多租户前端回归 ──
// 平台运营者视角：Admin 页应渲染租户管理卡（配额/用量/启停），账号行显示租户标签，
// 建号表单出现租户下拉。租户 admin（platform=false）不得出现租户管理入口。
// English: §MT frontend regression — platform admins see the tenant card (quota/usage/toggle),
// tenant tags on account rows and the tenant picker; tenant admins see none of it.
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'

const adminUsers = vi.fn()
const fetchTenants = vi.fn()

vi.mock('../api/index.js', () => ({
  getAccount: () => 'admin',
  isAdmin: () => true,
  fetchAdminUsers: () => adminUsers(),
  fetchOpslogDates: vi.fn(async () => ({ dates: [] })),
  fetchOpslog: vi.fn(async () => ({ lines: [], total: 0, truncated: false })),
  cleanupAdminUsers: vi.fn(async () => ({ deleted: [], count: 0, dry_run: true })),
  fetchTenants: () => fetchTenants(),
  createTenant: vi.fn(async () => ({})),
  updateTenant: vi.fn(async () => ({})),
}))

import Admin from '../pages/Admin.jsx'

const TENANTS = [
  { id: 't_default', name: '系统租户', enabled: true, max_users: 20, api_rate_per_min: 600, used_users: 2, is_default: true },
  { id: 't_x', name: '某某私募', enabled: true, max_users: 3, api_rate_per_min: 300, used_users: 3, is_default: false },
]

beforeEach(() => {
  adminUsers.mockReset()
  fetchTenants.mockReset()
})

describe('Admin 页 §MT 多租户', () => {
  it('平台运营者：租户管理卡 + 配额用量 + 租户标签渲染', async () => {
    adminUsers.mockResolvedValue({
      users: [{ id: 'u1', username: 'ub', role: 'user', enabled: true, perms: [], tenant_id: 't_x', created_at: 0 }],
      perms: [],
      tenant_names: { t_default: '系统租户', t_x: '某某私募' },
      platform: true,
    })
    fetchTenants.mockResolvedValue({ tenants: TENANTS })
    render(<Admin />)
    await waitFor(() => expect(screen.getByText('租户管理')).toBeInTheDocument())
    expect(screen.getAllByText('某某私募').length).toBeGreaterThanOrEqual(1)
    expect(screen.getByText('3/3')).toBeInTheDocument() // 配额用满
    expect(fetchTenants).toHaveBeenCalled()
    // 账号行租户标签
    expect(screen.getAllByText('某某私募').length).toBeGreaterThanOrEqual(2)
    // 建号表单出现租户下拉选项
    expect(screen.getByText('创建租户')).toBeInTheDocument()
  })

  it('租户 admin（platform=false）：不渲染租户管理入口', async () => {
    adminUsers.mockResolvedValue({
      users: [{ id: 'u1', username: 'ub', role: 'user', enabled: true, perms: [], tenant_id: 't_x', created_at: 0 }],
      perms: [],
      tenant_names: { t_x: '某某私募' },
      platform: false,
    })
    render(<Admin />)
    await waitFor(() => expect(screen.getByText('账号列表')).toBeInTheDocument())
    expect(screen.queryByText('租户管理')).not.toBeInTheDocument()
    expect(fetchTenants).not.toHaveBeenCalled()
  })
})
