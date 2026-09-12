// ── §F6 合规文案/角色条/命令面板 单元测试 ──
// filterCommands 子串+子序列匹配；Disclaimer 三变体文本；RoleBar 角色/账号渲染；
// CommandPalette 渲染条目、输入六位代码出现"查看个股详情"命令、点击触发 onOpenStock。
// English: unit tests for §F6 — filterCommands (substring + subsequence), Disclaimer variants,
// RoleBar role/account rendering, and CommandPalette rows including the 6-digit open-stock command.
import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { filterCommands } from '../components/CommandPalette.jsx'
import Disclaimer, { DISCLAIMER_TEXT } from '../components/Disclaimer.jsx'
import RoleBar from '../components/RoleBar.jsx'
import CommandPalette from '../components/CommandPalette.jsx'

// MarketStatusBar/图表等组件不在本测试内；RoleBar 依赖 useTheme→theme.js→localStorage（jsdom 提供）。
describe('filterCommands (§F6)', () => {
  const items = [{ label: '仪表盘' }, { label: '信号' }, { label: 'Watchlist' }]
  it('空查询返回全部', () => {
    expect(filterCommands(items, '')).toHaveLength(3)
  })
  it('完全子串优先于子序列', () => {
    const r = filterCommands([{ label: 'abc' }, { label: 'axbxc' }], 'abc')
    expect(r[0].item.label).toBe('abc')
    expect(r[0].score).toBe(0)
    expect(r[1].score).toBe(1)
  })
  it('大小写不敏感 + 无匹配返回空', () => {
    expect(filterCommands([{ label: 'Dashboard' }], 'dash')).toHaveLength(1)
    expect(filterCommands([{ label: '仪表盘' }], 'zzz')).toHaveLength(0)
  })
})

describe('Disclaimer (§F6)', () => {
  it('login 变体展示完整文案', () => {
    render(<Disclaimer variant="login" />)
    expect(screen.getByTestId('disclaimer-login').textContent).toContain('不构成任何投资建议')
    expect(DISCLAIMER_TEXT).toContain('风险')
  })
  it('inline 变体展示精简尾注', () => {
    render(<Disclaimer variant="inline" />)
    expect(screen.getByTestId('disclaimer-inline')).toBeInTheDocument()
  })
})

describe('RoleBar (§F6)', () => {
  it('管理员显示管理员徽标与账号', () => {
    render(<RoleBar account="alice" isAdmin canResearch paperEnabled />)
    expect(screen.getByTestId('role-bar')).toHaveTextContent('alice')
    expect(screen.getByText('管理员')).toBeInTheDocument()
    expect(screen.getByText(/用户管理/)).toBeInTheDocument()
  })
  it('普通用户不显设置/用户管理入口', () => {
    render(<RoleBar account="bob" isAdmin={false} canResearch={false} paperEnabled={false} />)
    expect(screen.getByText('普通用户')).toBeInTheDocument()
    expect(screen.queryByText(/用户管理/)).not.toBeInTheDocument()
  })
})

describe('CommandPalette (§F6)', () => {
  const pages = [{ to: '/dashboard', label: '仪表盘' }, { to: '/signals', label: '信号' }]
  it('渲染页面命令列表', () => {
    render(<MemoryRouter><CommandPalette pages={pages} onClose={() => {}} /></MemoryRouter>)
    expect(screen.getByTestId('cmdk-panel')).toBeInTheDocument()
    expect(screen.getAllByTestId('cmdk-item').length).toBe(2)
  })
  it('输入六位代码出现"查看个股详情"命令并可触发', () => {
    const onOpenStock = vi.fn(), onClose = vi.fn()
    render(<MemoryRouter><CommandPalette pages={pages} onOpenStock={onOpenStock} onClose={onClose} /></MemoryRouter>)
    fireEvent.change(screen.getByTestId('cmdk-input'), { target: { value: '600519' } })
    const rows = screen.getAllByTestId('cmdk-item')
    expect(rows[0]).toHaveTextContent('600519')
    fireEvent.click(rows[0])
    expect(onOpenStock).toHaveBeenCalledWith('600519')
    expect(onClose).toHaveBeenCalled()
  })
  it('Esc 关闭', () => {
    const onClose = vi.fn()
    render(<MemoryRouter><CommandPalette pages={pages} onClose={onClose} /></MemoryRouter>)
    fireEvent.keyDown(screen.getByTestId('cmdk-input'), { key: 'Escape' })
    expect(onClose).toHaveBeenCalled()
  })
})
