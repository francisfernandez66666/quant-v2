// ── 用户管理页 Admin.jsx（仅 admin 可见）──
// Admin page (admin only): account creation, role/perm config, password reset,
// enable/disable, expiry, and per-account strategy param delegation.
import React, { useState, useEffect, useRef } from 'react'
import { Button, Switch, Input, Select, DialogPlugin } from 'tdesign-react'
import * as api from '../api/index.js'
import { showToast } from '../ui.jsx'
import './Admin.css'

const PERM_LABELS = { research_approve: '研究审批' }

// 战法参数分组定义（与 Vue 版 Settings/Admin 一致）
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

// 将时间戳格式化为 YYYY-MM-DD
function fmtTime(ts) {
  if (!ts) return '-'
  const d = new Date(ts * 1000)
  const p = (n) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`
}

// 根据用户有效期生成中文说明
function expiryText(u) {
  if (!u.expires_at) return '有效期：永久'
  const now = Date.now()
  const exp = u.expires_at * 1000
  if (exp < now) return '有效期：已到期'
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

  // 安全读取用户权限数组
  function uPerms(u) {
    return Array.isArray(u.perms) ? u.perms : []
  }

  // 加载全部用户与权限列表
  async function loadUsers() {
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

  // 勾选/取消勾选用户权限
  async function togglePerm(u, p, ev) {
    const next = ev.target.checked
      ? [...uPerms(u), p]
      : uPerms(u).filter((x) => x !== p)
    try {
      await api.setAdminUserPerms(u.id, next)
      setUsers(users.map((x) => x.id === u.id ? { ...x, perms: next } : x))
    } catch (e) {
      showToast('权限更新失败: ' + (e.message || e), 'error')
      ev.target.checked = !ev.target.checked
    }
  }

  // 提示输入并重置用户密码
  function askResetPassword(u) {
    const pw = window.prompt('为 ' + u.username + ' 设置新密码（留空取消）')
    if (!pw) return
    api.setAdminUserPassword(u.id, pw)
      .then(() => showToast(u.username + ' 密码已重置', 'success'))
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

  // 提示设置账号有效期天数
  function askSetExpiry(u) {
    const cur = u.expires_at ? Math.ceil((u.expires_at * 1000 - Date.now()) / 86400000) : 0
    const input = window.prompt(
      '为 ' + u.username + ' 设置有效期天数（输入 0 表示永久；当前' + (u.expires_at ? '剩余约 ' + (cur > 0 ? cur : '已到期') + ' 天' : '永久') + '）：',
      String(cur > 0 ? cur : 0)
    )
    if (input === null) return
    const days = parseInt(input, 10)
    if (isNaN(days) || days < 0) { window.alert('请输入非负整数天数'); return }
    api.setAdminUserExpiry(u.id, days)
      .then(() => {
        setUsers(users.map((x) => x.id === u.id ? { ...x, expires_at: days > 0 ? Math.floor(Date.now() / 1000) + days * 86400 : 0 } : x))
        showToast(u.username + ' 有效期已更新', 'success')
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

  useEffect(() => { loadUsers() }, [])

  return (
    <div className="admin-page">
      <h2>用户管理</h2>

      <div className="card">
        <div className="card-header">开通新账号</div>
        <div className="form-grid">
          <div className="form-row">
            <label>用户名</label>
            <input value={newUser.username} onChange={(e) => setNewUser({ ...newUser, username: e.target.value })} placeholder="登录名" />
          </div>
          <div className="form-row">
            <label>初始密码</label>
            <input value={newUser.password} onChange={(e) => setNewUser({ ...newUser, password: e.target.value })} placeholder="首次登录用" type="password" />
          </div>
          <div className="form-row">
            <label>角色</label>
            <select value={newUser.role} onChange={(e) => setNewUser({ ...newUser, role: e.target.value })}>
              <option value="user">普通用户</option>
              <option value="admin">管理员</option>
            </select>
          </div>
          <div className="form-row">
            <label>权限</label>
            <div className="perm-checks">
              {allPerms.map((p) => (
                <label key={p} className="perm-check">
                  <input
                    type="checkbox"
                    checked={newUser.perms.indexOf(p) >= 0}
                    onChange={(e) => {
                      const perms = e.target.checked
                        ? [...newUser.perms, p]
                        : newUser.perms.filter((x) => x !== p)
                      setNewUser({ ...newUser, perms })
                    }}
                  />
                  {permLabel(p)}
                </label>
              ))}
            </div>
          </div>
          <div className="form-row">
            <label>有效期</label>
            <div className="expiry-row">
              <input
                type="number" min="0" placeholder="天数"
                disabled={newUser.permanent}
                value={newUser.expiresDays}
                onChange={(e) => setNewUser({ ...newUser, expiresDays: parseInt(e.target.value, 10) || 0 })}
              />
              <label className="perm-check">
                <input type="checkbox" checked={newUser.permanent} onChange={(e) => setNewUser({ ...newUser, permanent: e.target.checked })} /> 永久
              </label>
            </div>
            <span className="field-hint">填天数表示到期后自动失效；勾选"永久"则不过期</span>
          </div>
        </div>
        <button className="btn-primary" onClick={createUser} disabled={creating}>
          {creating ? '创建中...' : '创建账号'}
        </button>
        {createMsg && <span className={['feedback', createMsgType].join(' ')}>{createMsg}</span>}
      </div>

      {users.length > 0 && (
        <div className="card">
          <div className="card-header">账号列表</div>
          <div className="user-table">
            {users.map((u) => (
              <div className="user-row" key={u.id}>
                <div className="user-main">
                  <div className="user-name">
                    {u.username}
                    <span className={['role-tag', u.role === 'admin' ? 'role-admin' : 'role-user'].join(' ')}>
                      {u.role === 'admin' ? '管理员' : '用户'}
                    </span>
                    {!u.enabled && <span className="role-tag role-disabled">已禁用</span>}
                  </div>
                  <div className="user-meta">ID: {u.id} · 创建于 {fmtTime(u.created_at)} · {expiryText(u)}</div>
                  <div className="perm-checks">
                    {allPerms.map((p) => (
                      <label key={p} className="perm-check">
                        <input
                          type="checkbox"
                          checked={uPerms(u).indexOf(p) >= 0}
                          disabled={u.role === 'admin'}
                          onChange={(ev) => togglePerm(u, p, ev)}
                        />
                        {permLabel(p)}
                      </label>
                    ))}
                  </div>
                </div>
                <div className="user-actions">
                  <button className="btn-mini" onClick={() => toggleRole(u)}>
                    {u.role === 'admin' ? '降为普通用户' : '设为管理员'}
                  </button>
                  <button className="btn-mini" onClick={() => askResetPassword(u)}>重置密码</button>
                  <button className="btn-mini" disabled={u.role === 'admin'} onClick={() => toggleEnabled(u)}>
                    {u.enabled ? '禁用' : '启用'}
                  </button>
                  <button className="btn-mini" disabled={u.role === 'admin'} onClick={() => askSetExpiry(u)}>设置有效期</button>
                  <button className="btn-mini" onClick={() => openStrategy(u)}>配战法参数</button>
                  <button className="btn-mini btn-danger" disabled={u.role === 'admin'} onClick={() => askDeleteUser(u)}>删除</button>
                </div>
              </div>
            ))}
          </div>
        </div>
      )}

      {activeUser && (
        <div className="modal-mask" onClick={closeStrategy}>
          <div className="modal" onClick={(e) => e.stopPropagation()}>
            <div className="modal-header">
              <span>为 {activeUser.username} 配置战法参数</span>
              <button className="btn-close" onClick={closeStrategy}>✕</button>
            </div>
            <div className="strategy-groups">
              {strategyGroups.map((group) => (
                <div key={group.key} className="card group-card">
                  <div className="card-header">{group.title}</div>
                  {group.fields.map((f) => (
                    <div className="form-row" key={f.k}>
                      <label title={f.hint || ''}>{f.label}</label>
                      {f.type === 'switch' ? (
                        <label className="switch">
                          <input
                            type="checkbox"
                            checked={!!(activeStrategy[group.key] && activeStrategy[group.key][f.k])}
                            onChange={(e) => setActiveStrategy({
                              ...activeStrategy,
                              [group.key]: { ...activeStrategy[group.key], [f.k]: e.target.checked },
                            })}
                          />
                          <span className="slider"></span>
                        </label>
                      ) : (
                        <input
                          type="number"
                          step={f.step || 'any'}
                          placeholder="0"
                          value={(activeStrategy[group.key] && activeStrategy[group.key][f.k]) ?? ''}
                          onChange={(e) => setActiveStrategy({
                            ...activeStrategy,
                            [group.key]: { ...activeStrategy[group.key], [f.k]: parseFloat(e.target.value) },
                          })}
                        />
                      )}
                    </div>
                  ))}
                </div>
              ))}
            </div>
            <div className="modal-footer">
              <button className="btn-primary" onClick={saveStrategy} disabled={strategySaving}>
                {strategySaving ? '保存中...' : '保存该账号战法参数'}
              </button>
              {strategyMsg && <span className={['feedback', strategyMsgType].join(' ')}>{strategyMsg}</span>}
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
