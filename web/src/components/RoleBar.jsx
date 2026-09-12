// ── §F6 角色提示条 RoleBar.jsx ──
// 主布局顶部一行小徽标：当前账号 + 角色 + 可见入口范围。UAT 2.1 反馈"看不出自己是管理员/普通用户"，
// 在顶栏和市场环境条之间常驻一行说明，避免误操作后困惑。仅登录主界面渲染（App 调用）。
// English: §F6 role-awareness bar — a slim strip under the header showing the current account and what
// its role can reach. Fixes UAT 2.1 ("can't tell if I'm admin") so users never mis-operate as wrong role.
import React from 'react'
import { useTheme } from '../theme.js'
import { Tag } from 'tdesign-react'

export default function RoleBar({ account, isAdmin, canResearch, paperEnabled }) {
  const [theme] = useTheme()
  // 依据角色与开关拼入口可见性文案（与 App.jsx 侧 navItems 过滤规则一致）。
  const scope = []
  scope.push('仪表盘/信号/自选/热点/消息/持仓/量化')
  if (paperEnabled) scope.push('模拟盘')
  if (canResearch) scope.push('自动研究')
  if (isAdmin) scope.push('设置/用户管理')
  return (
    <div
      data-testid="role-bar"
      style={{
        display: 'flex', alignItems: 'center', gap: 8, padding: '6px 12px', flexWrap: 'wrap',
        fontSize: 12, background: 'var(--app-surface-2)', color: 'var(--app-text-2)',
        borderBottom: '1px solid var(--app-divider)',
      }}
    >
      <span style={{ color: 'var(--app-muted-2)' }}>当前账号</span>
      <span style={{ fontFamily: 'monospace', color: 'var(--app-text)' }}>{account || '—'}</span>
      <Tag size="small" theme={isAdmin ? 'primary' : 'default'} variant="light">
        {isAdmin ? '管理员' : '普通用户'}
      </Tag>
      {theme === 'dark' && <Tag size="small" theme="default" variant="light">深色</Tag>}
      <span style={{ color: 'var(--app-muted)' }}>可见入口：{scope.join(' · ')}</span>
    </div>
  )
}
