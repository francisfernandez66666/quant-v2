// ── 根组件 App.jsx ──
// 主布局：侧边栏导航（TDesign Menu）+ 顶部栏（TDesign Header）+ 内容区（Routes）；未登录显示登录页。
// 全局逻辑：登录态恢复、15s 状态轮询、SSE 推送订阅、做空开关、通知测试、Toast 提示。
// 全站使用 TDesign React 组件 + 浅色主题（默认，不设置 theme 即为浅色）。
import React, { useState, useEffect, useRef } from 'react'
import { NavLink, Routes, Route, useNavigate, useLocation, Navigate } from 'react-router-dom'
import { ConfigProvider, Menu, Switch, Button, Badge, MessagePlugin, Input } from 'tdesign-react'
import * as api from './api/index.js'
import { isNative, canNotify, requestPermission, notify as sendNotify, notifyThrottled } from './notify.js'
import { showToast, showNotify } from './ui.jsx'

import Dashboard from './pages/Dashboard.jsx'
import Signals from './pages/Signals.jsx'
import Watchlist from './pages/Watchlist.jsx'
import Positions from './pages/Positions.jsx'
import Quant from './pages/Quant.jsx'
import Hotspot from './pages/Hotspot.jsx'
import MsgCenter from './pages/MsgCenter.jsx'
import Settings from './pages/Settings.jsx'
import LLMDebug from './pages/LLMDebug.jsx'
import Consult from './pages/Consult.jsx'
import Research from './pages/Research.jsx'
import Admin from './pages/Admin.jsx'
import Paper from './pages/Paper.jsx'

/**
 * 应用根组件
 * 负责登录态、全局状态轮询、SSE 推送、角色权限、侧边栏与路由渲染。
 * @returns {JSX.Element} 登录页或主布局
 */
export default function App() {
  const navigate = useNavigate()
  const location = useLocation()
  const [loggedIn, setLoggedIn] = useState(false)
  const [account, setAccount] = useState('')
  const [serverOnline, setServerOnline] = useState(false)
  const [inTradeTime, setInTradeTime] = useState(null)
  const [signalCount, setSignalCount] = useState(0)
  const [alertCount, setAlertCount] = useState(0)
  const [menuOpen, setMenuOpen] = useState(false)
  const [shortEnabled, setShortEnabled] = useState(false)
  const [canResearch, setCanResearch] = useState(false)
  const [canAdmin, setCanAdmin] = useState(false)
  const [paperEnabled, setPaperEnabled] = useState(false)

  const [serverUrl, setServerUrl] = useState(api.getStoredServer() || '')
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [logging, setLogging] = useState(false)
  const [loginError, setLoginError] = useState('')

  const statusTimer = useRef(null)
  const unsubSSE = useRef(null)

  // 根据当前用户权限刷新研究/管理入口可见性
  function applyRoleGates() {
    setCanResearch(api.hasPerm('research_approve'))
    setCanAdmin(api.isAdmin())
  }

  // 初始化时校验本地 token，若有效则恢复登录态并刷新角色
  async function checkAuth() {
    if (api.isLoggedIn()) {
      setLoggedIn(true)
      setAccount(api.getAccount())
      api.setStoredServer(serverUrl)
      try { await api.refreshMe() } catch (_) {}
      applyRoleGates()
      return true
    }
    setLoggedIn(false)
    applyRoleGates()
    return false
  }

  // 处理用户登录：设置服务器地址、请求登录、启动轮询与通知权限
  async function handleLogin() {
    setLogging(true)
    setLoginError('')
    api.setStoredServer(serverUrl)
    try {
      await api.login(username, password)
      setAccount(api.getAccount())
      setLoggedIn(true)
      applyRoleGates()
      startPolling()
      MessagePlugin.success('登录成功')
      requestPermission()
    } catch (e) {
      setLoginError(e.message || '登录失败')
      MessagePlugin.error(e.message || '登录失败')
    } finally {
      setLogging(false)
    }
  }

  // 清除认证、停止轮询并返回登录页
  function logout() {
    api.clearAuth()
    stopPolling()
    setLoggedIn(false)
    setMenuOpen(false)
    applyRoleGates()
    navigate('/')
  }

  // 轮询服务器状态、未读信号数、未读消息数与做空开关状态
  async function refreshStatus() {
    try {
      const st = await api.fetchStatus()
      setServerOnline(true)
      setSignalCount(st.signal_count || 0)
      setInTradeTime(st.in_trade_time)
    } catch (_) { setServerOnline(false) }
    try {
      const alerts = await api.fetchAlerts()
      setAlertCount(alerts?.length || 0)
    } catch (_) {}
    try {
      const ss = await api.fetchShortStatus()
      setShortEnabled(ss.short_enabled || false)
    } catch (_) {}
  }

  // 同步后端切换做空开关状态
  async function onShortToggle(val) {
    try {
      const res = await api.toggleShort(val)
      setShortEnabled(res.short_enabled || false)
      MessagePlugin.info(res.short_enabled ? '做空已开启' : '做空已关闭')
    } catch (_) {
      setShortEnabled(!val)
      MessagePlugin.error('做空开关切换失败')
    }
  }

  // 处理 SSE 推送：scan 信号、重要消息提醒、单条新信号
  function handleSSE(msg) {
    if (msg && msg.type === 'scan') {
      const bull = parseInt(msg.bull || '0', 10)
      const bear = parseInt(msg.bear || '0', 10)
      if (bull > 0 || bear > 0) {
        const parts = []
        if (bull > 0) parts.push('做多 ' + bull + ' 条')
        if (bear > 0) parts.push('做空 ' + bear + ' 条')
        const text = '新交易信号: ' + parts.join('、') + (msg.time ? ' (' + msg.time + ')' : '')
        showToast(text, 'warning')
        notifyThrottled('scan', '量仔期货 交易信号', text)
      }
      refreshStatus()
      return
    }
    if (msg && msg.type === 'message' && msg.item) {
      const level = msg.item.level || ''
      const critical = level.indexOf('止盈') >= 0 || level.indexOf('止损') >= 0 || level.indexOf('清仓') >= 0 || level.indexOf('交易信号') >= 0 || level.indexOf('买入') >= 0
      if (critical) {
        const code = msg.item.code || ''
        const name = msg.item.name || ''
        const title = level ? ('量仔期货 ' + level) : '量仔期货 提醒'
        const body = (code ? code + ' ' : '') + (name || '') + (msg.item.title || msg.item.body || '')
        notifyThrottled(code + '@' + level, title, body)
      }
      refreshStatus()
      return
    }
    if (msg.signal) {
      showToast('新信号: ' + (msg.signal.code || ''), 'warning')
      refreshStatus()
    }
  }

  // 全局认证过期事件回调：提示并安全退出
  function onAuthExpired() {
    if (!loggedIn) return
    MessagePlugin.error('登录已过期，请重新登录')
    logout()
  }

  // 启动状态轮询并订阅 SSE 推送
  function startPolling() {
    refreshStatus()
    statusTimer.current = setInterval(refreshStatus, 15000)
    api.connectSSE()
    unsubSSE.current = api.onSSE(handleSSE)
  }

  // 停止轮询并断开 SSE 连接
  function stopPolling() {
    if (statusTimer.current) { clearInterval(statusTimer.current); statusTimer.current = null }
    api.disconnectSSE()
    if (unsubSSE.current) { unsubSSE.current(); unsubSSE.current = null }
  }

  // 组件挂载：自动登录、启动轮询、监听认证过期；卸载时清理
  useEffect(() => {
    checkAuth().then((ok) => { if (ok) startPolling() })
    api.fetchPaperState().then(d => setPaperEnabled(!!d.enabled)).catch(() => setPaperEnabled(false))
    window.addEventListener('auth:expired', onAuthExpired)
    return () => {
      window.removeEventListener('auth:expired', onAuthExpired)
      stopPolling()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // ── 登录页 ──
  if (!loggedIn) {
    return (
      <ConfigProvider>
        <div className="login-page">
          <div className="login-box t-card">
            <h1>量仔期货</h1>
            <p className="subtitle">量化交易辅助工具</p>
            <div className="form-group">
              <label>服务器地址</label>
              <Input value={serverUrl} onChange={(v) => setServerUrl(v)} placeholder="留空表示使用当前域名" />
            </div>
            <div className="form-group">
              <label>账号</label>
              <Input value={username} onChange={(v) => setUsername(v)} placeholder="输入账号" />
            </div>
            <div className="form-group">
              <label>密码</label>
              <Input type="password" value={password} onChange={(v) => setPassword(v)} placeholder="输入密码"
                onEnter={handleLogin} />
            </div>
            <Button theme="primary" loading={logging} onClick={handleLogin} block>登录</Button>
            {loginError && <p className="login-error">{loginError}</p>}
          </div>
        </div>
      </ConfigProvider>
    )
  }

  // ── 主界面 ──
  // 根据权限（canResearch/canAdmin）与模拟盘开关（paperEnabled）动态生成侧边栏导航项，
  // 过滤掉当前角色无权访问或功能未开启的入口，再交给下方 Menu 渲染
  const navItems = [
    { to: '/dashboard', icon: '📊', label: '仪表盘' },
    { to: '/signals', icon: '⚡', label: '信号', badge: signalCount },
    { to: '/watchlist', icon: '👁', label: '自选' },
    { to: '/hotspot', icon: '🔥', label: '热点' },
    { to: '/msgcenter', icon: '💬', label: '消息', badge: alertCount },
    { to: '/positions', icon: '💼', label: '持仓' },
    { to: '/quant', icon: '📈', label: '量化交易' },
    paperEnabled ? { to: '/paper', icon: '🧪', label: '模拟盘' } : null,
    canAdmin ? { to: '/settings', icon: '⚙', label: '设置' } : null,
    { to: '/llm-debug', icon: '🧠', label: 'LLM诊断' },
    { to: '/consult', icon: '🎯', label: '股票咨询' },
    canResearch ? { to: '/research', icon: '🔬', label: '自动研究' } : null,
    canAdmin ? { to: '/admin', icon: '👥', label: '用户管理' } : null,
  ].filter(Boolean)

  return (
    <ConfigProvider>
      {/* 安全兜底：理论上进入主布局时 loggedIn 必为 true，此处保留登录页分支以防状态竞态 */}
      {!loggedIn ? (
        <div className="login-page">
          <div className="login-box">
            <h1>量仔期货</h1>
            <p className="subtitle">量化交易辅助工具</p>
            <div className="form-group">
              <label>服务器地址</label>
              <Input value={serverUrl} onChange={(v) => setServerUrl(v)} placeholder="留空表示使用当前域名" />
            </div>
            <div className="form-group">
              <label>账号</label>
              <Input value={username} onChange={(v) => setUsername(v)} placeholder="输入账号" />
            </div>
            <div className="form-group">
              <label>密码</label>
              <Input type="password" value={password} onChange={(v) => setPassword(v)} placeholder="输入密码" onEnter={handleLogin} />
            </div>
            <Button theme="primary" loading={logging} onClick={handleLogin} block>登录</Button>
            {loginError && <p className="login-error">{loginError}</p>}
          </div>
        </div>
      ) : (
        <div className="app-shell">
          {/* 顶部栏：左侧为汉堡菜单按钮 + 交易时段指示 + 服务在线状态，右侧为做空开关 + 通知测试 + 退出 */}
          <header className="app-header">
            <div style={{ display: 'flex', alignItems: 'center', gap: 12, fontSize: 13 }}>
              {/* 汉堡按钮：点击切换侧边栏显隐（移动端抽屉式） */}
              <div className="hamburger" onClick={() => setMenuOpen((o) => !o)}><span></span><span></span><span></span></div>
              {/* 交易时段指示：inTradeTime 为 true 显示"交易时段"，false 显示"盘前/盘后" */}
              <span>{inTradeTime !== null && (inTradeTime ? '🟢 交易时段' : '🔴 盘前/盘后')}</span>
              {/* 后端服务连通状态文字提示 */}
              <span className="muted">{serverOnline ? '服务在线' : '离线'}</span>
            </div>
            <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
              {/* 做空开关：开启后允许"做多+做空"，关闭则"仅做多" */}
              <Switch value={shortEnabled} onChange={onShortToggle} />
              <span className="muted">{shortEnabled ? '做多+空' : '仅做多'}</span>
              {/* 通知测试按钮：验证系统/原生通知通道是否可用 */}
              <Button theme="default" variant="outline" size="small" onClick={() => {
                const sent = sendNotify('量仔期货', '通知测试成功')
                MessagePlugin.info('通知测试' + (sent ? '已发送' : (isNative() ? '（请检查系统通知权限）' : '（通知未授权）')))
              }}>🔔</Button>
              {/* 退出登录 */}
              <Button theme="default" variant="outline" size="small" onClick={logout}>退出</Button>
            </div>
          </header>
          <div className="app-body">
            {/* 侧边栏：品牌 logo + 导航菜单 + 底部当前账号；menuOpen 控制移动端抽屉展开 */}
            <aside className={'app-aside' + (menuOpen ? ' open' : '')}>
              <div className="brand-logo">量仔期货</div>
              <div style={{ flex: 1, overflowY: 'auto' }}>
                {/* 根据 navItems 渲染导航项，当前路由高亮；点击后跳转并收起抽屉 */}
                <Menu theme="light" value={location.pathname} onChange={(v) => { navigate(v); setMenuOpen(false) }} style={{ width: '100%', background: 'transparent', borderRight: 'none' }}>
                  {navItems.map((it) => (
                    <Menu.MenuItem key={it.to} value={it.to}>
                      <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
                        <span>{it.icon}</span>
                        <span>{it.label}</span>
                        {/* 存在未读数量时展示角标（信号/消息数） */}
                        {it.badge > 0 && <Badge count={it.badge} />}
                      </span>
                    </Menu.MenuItem>
                  ))}
                </Menu>
              </div>
              {/* 侧边栏底部固定显示登录账号名 */}
              <div className="sidebar-footer">
                <div className="account-name">{account}</div>
              </div>
            </aside>
            {/* 移动端抽屉展开时，点击遮罩收起侧边栏 */}
            {menuOpen && <div className="sidebar-overlay" onClick={() => setMenuOpen(false)} />}
            <main className="app-main">
              {/* 路由出口：根据 path 渲染对应页面组件；根路径重定向到仪表盘 */}
              <Routes>
                <Route path="/" element={<Navigate to="/dashboard" replace />} />
                <Route path="/dashboard" element={<Dashboard />} />
                <Route path="/signals" element={<Signals />} />
                <Route path="/watchlist" element={<Watchlist />} />
                <Route path="/positions" element={<Positions />} />
                <Route path="/quant" element={<Quant />} />
                <Route path="/hotspot" element={<Hotspot />} />
                <Route path="/msgcenter" element={<MsgCenter />} />
                <Route path="/settings" element={<Settings />} />
                <Route path="/llm-debug" element={<LLMDebug />} />
                <Route path="/consult" element={<Consult />} />
                <Route path="/research" element={<Research />} />
                <Route path="/admin" element={<Admin />} />
                <Route path="/paper" element={<Paper />} />
              </Routes>
            </main>
          </div>
        </div>
      )}
    </ConfigProvider>
  )
}
