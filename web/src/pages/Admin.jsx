// ── 用户管理页 Admin.jsx（仅 admin 可见）──
// Admin page (admin only): account creation, role/perm config, password reset,
// enable/disable, expiry, and per-account strategy param delegation.
import React, { useState, useEffect } from 'react'
import {
  Button, Input, InputNumber, Select, Dialog, DialogPlugin,
  Table, Tag, Card, Form, Checkbox,
} from 'tdesign-react'
import ToggleSw from '../components/ToggleSw'
import * as api from '../api/index.js'
import { showToast } from '../ui.jsx'

// 从 api 模块导入权限判断工具：isAdmin() 读取 localStorage 中缓存的 role（由 App 的 refreshMe 写入）
// 守卫判断来源：web/src/api/index.js 的 isAdmin()（基于 STORAGE_ROLE），与 App.jsx 中侧边栏 canAdmin 一致
import { isAdmin } from '../api/index.js'

// 权限位中文标签映射：把后端下发的英文权限标识翻译为界面可读文案（当前仅"研究审批"一项）
const PERM_LABELS = { research_approve: '研究审批' }

// 战法参数分组定义（与 Vue 版 Settings/Admin 一致）；每个 group 的 fields 决定代配弹窗中展示的输入框与步长
const strategyGroups = [
  {
    key: 'dragon', title: '龙头战法（权重合计≤1）',
    fields: [
      { k: 'f1_seal_weight', label: 'F1 首封权重', step: 0.05 },
      { k: 'f2_resonance_weight', label: 'F2 共振权重', step: 0.05 },
      { k: 'f3_premium_weight', label: 'F3 溢价权重', step: 0.05 },
      { k: 'f4_rs_weight', label: 'F4 强度权重', step: 0.05 },
      { k: 'pullback_max_pct', label: '最大回撤%', step: 0.01 },
      { k: 'breaker_sell_half_pct', label: '炸板减半%', step: 0.01 },
      { k: 'breaker_sell_all_pct', label: '炸板清仓%', step: 0.01 },
      { k: 'buy_pullback_sell_half_pct', label: '买入回撤减半%', step: 0.01 },
      { k: 'buy_pullback_sell_all_pct', label: '买入回撤清仓%', step: 0.01 },
      { k: 'buy_day_close_below', label: '买入日收盘低于%', step: 0.01 },
      { k: 'next_open_if_below', label: '次日开盘低于%', step: 0.01 },
      { k: 'take_profit_pct', label: '止盈%', step: 1 },
    ],
  },
  {
    key: 'double_bump', title: '双响炮战法',
    fields: [
      { k: 'first_break_volume_multiple', label: '一突量比', step: 0.1 },
      { k: 'second_break_volume_multiple', label: '二突量比', step: 0.1 },
      { k: 'adjust_vol_ratio_max', label: '调整量比上限', step: 0.5 },
      { k: 'position_weight', label: '调整深度权重', step: 0.05 },
      { k: 'ma_weight', label: '均线权重', step: 0.05 },
      { k: 'volume_weight', label: '量能权重', step: 0.05 },
      { k: 'double_bump_take_profit_pct', label: '止盈%', step: 0.01 },
    ],
  },
  {
    key: 'n_shape', title: 'N 形战法',
    fields: [
      { k: 'n_pattern_score_threshold', label: 'N 形态分阈值', step: 1 },
      { k: 'hard_stop_loss', label: '硬止损%', step: 0.01 },
    ],
  },
  {
    key: 'dragon_return', title: '龙回头战法',
    fields: [
      { k: 'stop_loss_pct', label: '止损%', step: 0.01 },
      { k: 'take_profit_pct', label: '止盈%', step: 0.01 },
      { k: 'max_hold_days', label: '最长持仓天数', step: 1 },
      { k: 'target1_multiplier', label: '目标1倍数', step: 0.05 },
      { k: 'target2_multiplier', label: '目标2倍数', step: 0.05 },
      { k: 'trailing_drawback', label: '移动止损回撤%', step: 0.01 },
    ],
  },
  {
    key: 'momentum', title: '动量分权重（合计建议=100）',
    fields: [
      { k: 'volume_price_weight', label: '量价权重', step: 5 },
      { k: 'macd_weight', label: 'MACD权重', step: 5 },
      { k: 'trend_weight', label: '走势权重', step: 5 },
      { k: 'momentum_gate_enabled', label: '动量提升才提醒', type: 'switch', hint: '开启后仅当动量分提升(或回落≤容忍差)才放行 双响炮/龙头/龙回头 战法信号；N形不受影响' },
      { k: 'momentum_delta_tol', label: '回落容忍差(分)', step: 1, hint: '动量分相对上一轮回落 ≤ 该值仍视为提升；设为0表示需严格不回落' },
    ],
  },
]

// 将权限标识翻译为中文
function permLabel(p) {
  return PERM_LABELS[p] || p
}

// 时间戳格式化为 YYYY-MM-DD
function fmtTime(ts) {
  if (!ts) return '-'
  const d = new Date(ts * 1000)
  const p = (n) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`
}

// 根据用户有效期生成中文说明（永久 / 已到期 / 剩余天数）
function expiryText(u) {
  if (!u.expires_at) return '有效期：永久'
  const now = Date.now()
  const exp = u.expires_at * 1000
  if (exp < now) return '有效期：已到期'
  // 86400000 = 一天的毫秒数，用于换算剩余天数
  const days = Math.ceil((exp - now) / 86400000)
  return '有效期：' + fmtTime(u.expires_at) + '（剩 ' + days + ' 天）'
}

// 封装 tdesign 确认对话框为 Promise
function confirmDialog(body, header = '确认') {
  return new Promise((resolve) => {
    const d = DialogPlugin.confirm({
      header,
      body,
      theme: 'warning',
      onConfirm: () => { d.hide(); resolve(true) },
      onClose: () => { d.hide(); resolve(false) },
    })
  })
}

/**
 * 用户管理页面组件（仅 admin 可见）
 * 负责账号创建、角色/权限/有效期管理、密码重置与战法参数下发。
 * @returns {JSX.Element}
 */
export default function Admin() {
  const [users, setUsers] = useState([])
  const [allPerms, setAllPerms] = useState([])
  const [creating, setCreating] = useState(false)
  const [createMsg, setCreateMsg] = useState('')
  const [createMsgType, setCreateMsgType] = useState('ok')
  const [newUser, setNewUser] = useState({ username: '', password: '', role: 'user', perms: [], expiresDays: 0, permanent: true })

  const [activeUser, setActiveUser] = useState(null)
  const [activeStrategy, setActiveStrategy] = useState({})
  const [strategySaving, setStrategySaving] = useState(false)
  const [strategyMsg, setStrategyMsg] = useState('')
  const [strategyMsgType, setStrategyMsgType] = useState('ok')

  // 重置密码弹窗状态
  const [pwUser, setPwUser] = useState(null)
  const [pwValue, setPwValue] = useState('')
  // 设置有效期弹窗状态
  const [expUser, setExpUser] = useState(null)
  const [expDays, setExpDays] = useState(0)

  // 安全读取用户权限数组
  function uPerms(u) {
    return Array.isArray(u.perms) ? u.perms : []
  }

  // 加载全部用户与权限列表（仅管理员调用；非管理员直接返回，避免越权请求）
  async function loadUsers() {
    // 守卫二次校验：不是管理员则直接中止数据加载
    if (!isAdmin()) return
    try {
      const res = await api.fetchAdminUsers()
      setUsers(res.users || [])
      setAllPerms(res.perms || [])
    } catch (e) {
      showToast('加载用户失败: ' + (e.message || e), 'error')
    }
  }

  // 创建新账号并清空表单
  function createUser() {
    if (!newUser.username || !newUser.password) {
      setCreateMsg('用户名和密码必填'); setCreateMsgType('err'); return
    }
    setCreating(true); setCreateMsg('')
    api.createAdminUser({
      username: newUser.username,
      password: newUser.password,
      role: newUser.role,
      perms: newUser.perms,
      expires_days: newUser.permanent ? 0 : (newUser.expiresDays || 0),
    }).then(() => {
      setCreateMsg('账号已创建'); setCreateMsgType('ok')
      setNewUser({ username: '', password: '', role: 'user', perms: [], expiresDays: 0, permanent: true })
      loadUsers()
    }).catch((e) => {
      setCreateMsg('创建失败: ' + (e.message || e)); setCreateMsgType('err')
    }).finally(() => setCreating(false))
  }

  // 切换用户 admin/user 角色
  async function toggleRole(u) {
    const role = u.role === 'admin' ? 'user' : 'admin'
    try {
      await api.setAdminUserRole(u.id, role)
      setUsers(users.map((x) => x.id === u.id ? { ...x, role } : x))
    } catch (e) {
      showToast('操作失败: ' + (e.message || e), 'error')
    }
  }

  // 全量更新某用户权限位
  async function setUserPerms(u, next) {
    try {
      await api.setAdminUserPerms(u.id, next)
      setUsers(users.map((x) => x.id === u.id ? { ...x, perms: next } : x))
    } catch (e) {
      showToast('权限更新失败: ' + (e.message || e), 'error')
    }
  }

  // 勾选/取消勾选用户权限
  function togglePerm(u, val) {
    setUserPerms(u, val)
  }

  // 提交重置密码
  function doResetPassword() {
    if (!pwValue) { showToast('密码不能为空', 'warning'); return }
    api.setAdminUserPassword(pwUser.id, pwValue)
      .then(() => { showToast(pwUser.username + ' 密码已重置', 'success'); setPwUser(null) })
      .catch((e) => showToast('重置失败: ' + (e.message || e), 'error'))
  }

  // 启用/禁用账号
  async function toggleEnabled(u) {
    try {
      await api.setAdminUserEnabled(u.id, !u.enabled)
      setUsers(users.map((x) => x.id === u.id ? { ...x, enabled: !x.enabled } : x))
    } catch (e) {
      showToast('操作失败: ' + (e.message || e), 'error')
    }
  }

  // 打开设置有效期弹窗
  function askSetExpiry(u) {
    const cur = u.expires_at ? Math.ceil((u.expires_at * 1000 - Date.now()) / 86400000) : 0
    setExpUser(u)
    setExpDays(cur > 0 ? cur : 0)
  }

  // 提交设置有效期
  function doSetExpiry() {
    if (isNaN(expDays) || expDays < 0) { showToast('请输入非负整数天数', 'warning'); return }
    const days = expDays || 0
    api.setAdminUserExpiry(expUser.id, days)
      .then(() => {
        // 86400 = 一天的秒数；days 为 0 表示永久，有效期置 0
      setUsers(users.map((x) => x.id === expUser.id ? { ...x, expires_at: days > 0 ? Math.floor(Date.now() / 1000) + days * 86400 : 0 } : x))
        showToast(expUser.username + ' 有效期已更新', 'success')
        setExpUser(null)
      })
      .catch((e) => showToast('设置失败: ' + (e.message || e), 'error'))
  }

  // 确认并删除用户
  function askDeleteUser(u) {
    confirmDialog('确定删除用户 ' + u.username + '？该操作不可恢复。', '删除用户').then((ok) => {
      if (!ok) return
      api.deleteAdminUser(u.id)
        .then(() => { setUsers(users.filter((x) => x.id !== u.id)); showToast(u.username + ' 已删除', 'success') })
        .catch((e) => showToast('删除失败: ' + (e.message || e), 'error'))
    })
  }

  // 打开指定用户的战法参数配置弹窗并加载其专属配置
  async function openStrategy(u) {
    setActiveUser(u)
    setActiveStrategy({ dragon: {}, double_bump: {}, n_shape: {}, dragon_return: {}, momentum: {} })
    setStrategyMsg('')
    try {
      const sc = await api.fetchAdminStrategyConfig(u.id)
      if (sc) {
        const next = { dragon: {}, double_bump: {}, n_shape: {}, dragon_return: {}, momentum: {} }
        for (const group of strategyGroups) {
          const src = sc[group.key]
          if (src) Object.assign(next[group.key], src)
        }
        setActiveStrategy(next)
      }
    } catch (e) {
      setStrategyMsg('读取配置失败: ' + (e.message || e)); setStrategyMsgType('err')
    }
  }

  // 关闭战法参数代配弹窗：清空当前选中用户
  function closeStrategy() { setActiveUser(null) }

  // 保存当前选中用户的战法参数下发配置
  async function saveStrategy() {
    if (!activeUser) return
    setStrategySaving(true); setStrategyMsg('')
    try {
      await api.setAdminStrategyConfig(activeUser.id, activeStrategy)
      setStrategyMsg('已保存，该账号热更新即时生效'); setStrategyMsgType('ok')
    } catch (e) {
      setStrategyMsg('保存失败: ' + (e.message || e)); setStrategyMsgType('err')
    }
    setStrategySaving(false)
  }

  // 用户列表表格列定义：用户信息 / 权限勾选 / 操作按钮组
  const userColumns = [
    {
      colKey: 'user', title: '用户', width: 220,
      cell: ({ row }) => (
        <div>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
            <span style={{ fontSize: 15, fontWeight: 600 }}>{row.username}</span>
            <Tag theme={row.role === 'admin' ? 'success' : 'primary'} variant="light">
              {row.role === 'admin' ? '管理员' : '用户'}
            </Tag>
            {!row.enabled && <Tag theme="danger" variant="light">已禁用</Tag>}
          </div>
          <div style={{ fontSize: 12, color: '#888', marginTop: 4 }}>
            ID: {row.id} · 创建于 {fmtTime(row.created_at)} · {expiryText(row)}
          </div>
        </div>
      ),
    },
    {
      colKey: 'perms', title: '权限', width: 220,
      cell: ({ row }) => (
        <Checkbox.Group
          value={uPerms(row)}
          disabled={row.role === 'admin'}
          onChange={(val) => togglePerm(row, val)}
        >
          {allPerms.map((p) => (
            <Checkbox key={p} value={p}>{permLabel(p)}</Checkbox>
          ))}
        </Checkbox.Group>
      ),
    },
    {
      colKey: 'ops', title: '操作', width: 360,
      cell: ({ row }) => (
        <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6 }}>
          <Button size="small" theme="default" onClick={() => toggleRole(row)}>
            {row.role === 'admin' ? '降为普通用户' : '设为管理员'}
          </Button>
          <Button size="small" theme="default" onClick={() => { setPwUser(row); setPwValue('') }}>重置密码</Button>
          <Button size="small" theme="default" disabled={row.role === 'admin'} onClick={() => toggleEnabled(row)}>
            {row.enabled ? '禁用' : '启用'}
          </Button>
          <Button size="small" theme="default" disabled={row.role === 'admin'} onClick={() => askSetExpiry(row)}>设置有效期</Button>
          <Button size="small" theme="primary" onClick={() => openStrategy(row)}>配战法参数</Button>
          <Button size="small" theme="danger" disabled={row.role === 'admin'} onClick={() => askDeleteUser(row)}>删除</Button>
        </div>
      ),
    },
  ]

  useEffect(() => { loadUsers() }, [])

  // 权限守卫：非管理员（api.isAdmin() 返回 false）直接渲染"无权限访问"，
  // 既禁止通过 URL 直接访问，也避免越权渲染管理界面（数据由 loadUsers 守卫拦截）
  if (!isAdmin()) {
    return (
      <div className="page" style={{ textAlign: 'center', paddingTop: 80 }}>
        <h2 style={{ fontSize: 18, fontWeight: 600 }}>无权限访问</h2>
        <p style={{ color: '#888', fontSize: 13, marginTop: 8 }}>
          该页面仅限管理员访问，请联系管理员或使用管理员账号登录。
        </p>
      </div>
    )
  }

  return (
    <div className="page">
      <h2 style={{ fontSize: 18, fontWeight: 600, marginBottom: 16 }}>用户管理</h2>

      <Card title="开通新账号" style={{ marginBottom: 12 }}>
        <Form layout="vertical">
          <Form.FormItem label="用户名">
            <Input value={newUser.username} onChange={(v) => setNewUser({ ...newUser, username: v })} placeholder="登录名" />
          </Form.FormItem>
          <Form.FormItem label="初始密码">
            <Input type="password" value={newUser.password} onChange={(v) => setNewUser({ ...newUser, password: v })} placeholder="首次登录用" />
          </Form.FormItem>
          <Form.FormItem label="角色">
            <Select value={newUser.role} onChange={(v) => setNewUser({ ...newUser, role: v })} style={{ width: 200 }}>
              <Select.Option value="user">普通用户</Select.Option>
              <Select.Option value="admin">管理员</Select.Option>
            </Select>
          </Form.FormItem>
          <Form.FormItem label="权限">
            <Checkbox.Group value={newUser.perms} onChange={(val) => setNewUser({ ...newUser, perms: val })}>
              {allPerms.map((p) => (
                <Checkbox key={p} value={p}>{permLabel(p)}</Checkbox>
              ))}
            </Checkbox.Group>
          </Form.FormItem>
          <Form.FormItem label="有效期">
            <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
              <InputNumber
                value={newUser.expiresDays}
                min={0}
                disabled={newUser.permanent}
                onChange={(v) => setNewUser({ ...newUser, expiresDays: v || 0 })}
                placeholder="天数"
                style={{ width: 140 }}
              />
              <ToggleSw checked={newUser.permanent} onChange={(v) => setNewUser({ ...newUser, permanent: v })} />
              <span style={{ fontSize: 13, color: '#888' }}>永久</span>
            </div>
            <span style={{ fontSize: 12, color: '#888' }}>填天数表示到期后自动失效；开启"永久"则不过期</span>
          </Form.FormItem>
        </Form>
        <Button theme="primary" loading={creating} onClick={createUser}>
          {creating ? '创建中...' : '创建账号'}
        </Button>
        {createMsg && (
          <span style={{ marginLeft: 10, fontSize: 13, color: createMsgType === 'ok' ? '#00a870' : '#e34d59' }}>{createMsg}</span>
        )}
      </Card>

      <Card title="账号列表" style={{ marginBottom: 12 }}>
        <Table
          rowKey="id"
          data={users}
          columns={userColumns}
          bordered={false}
          size="medium"
          pagination={{ defaultPageSize: 10, showJumper: true }}
        />
      </Card>

      {/* 重置密码弹窗 */}
      <Dialog
        visible={!!pwUser}
        header={pwUser ? '为 ' + pwUser.username + ' 重置密码' : '重置密码'}
        onClose={() => setPwUser(null)}
        onConfirm={doResetPassword}
        confirmBtn="确定重置"
      >
        <Input type="password" value={pwValue} onChange={(v) => setPwValue(v)} placeholder="输入新密码（留空取消）" />
      </Dialog>

      {/* 设置有效期弹窗 */}
      <Dialog
        visible={!!expUser}
        header={expUser ? '为 ' + expUser.username + ' 设置有效期' : '设置有效期'}
        onClose={() => setExpUser(null)}
        onConfirm={doSetExpiry}
        confirmBtn="确定"
      >
        <div style={{ marginBottom: 8, fontSize: 13, color: '#888' }}>
          输入天数（0 表示永久）{expUser && expUser.expires_at ? '；当前剩余约 ' + (expUser.expires_at ? Math.ceil((expUser.expires_at * 1000 - Date.now()) / 86400000) : 0) + ' 天' : '；当前永久'}
        </div>
        <InputNumber value={expDays} min={0} onChange={(v) => setExpDays(v || 0)} style={{ width: 200 }} />
      </Dialog>

      {/* 代配战法参数弹窗 */}
      <Dialog
        visible={!!activeUser}
        header={activeUser ? '为 ' + activeUser.username + ' 配置战法参数' : '配置战法参数'}
        onClose={closeStrategy}
        onConfirm={saveStrategy}
        confirmBtn={strategySaving ? '保存中...' : '保存该账号战法参数'}
        width={560}
        footer={strategySaving ? null : undefined}
      >
        {strategyGroups.map((group) => (
          <Card key={group.key} title={group.title} style={{ marginBottom: 10 }}>
            {group.fields.map((f) => (
              <Form.FormItem key={f.k} label={f.label} style={{ marginBottom: 8 }}>
                {f.type === 'switch' ? (
                  <ToggleSw
                    checked={!!(activeStrategy[group.key] && activeStrategy[group.key][f.k])}
                    onChange={(v) => setActiveStrategy({
                      ...activeStrategy,
                      [group.key]: { ...activeStrategy[group.key], [f.k]: v },
                    })}
                  />
                ) : (
                  <InputNumber
                    step={f.step || 'any'}
                    value={(activeStrategy[group.key] && activeStrategy[group.key][f.k]) ?? ''}
                    onChange={(v) => setActiveStrategy({
                      ...activeStrategy,
                      [group.key]: { ...activeStrategy[group.key], [f.k]: v },
                    })}
                    placeholder="0"
                    style={{ width: 200 }}
                  />
                )}
              </Form.FormItem>
            ))}
          </Card>
        ))}
        {strategyMsg && (
          <span style={{ fontSize: 13, color: strategyMsgType === 'ok' ? '#00a870' : '#e34d59' }}>{strategyMsg}</span>
        )}
      </Dialog>
    </div>
  )
}
