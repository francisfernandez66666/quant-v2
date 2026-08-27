// ── 根组件 App.jsx ──
// 主布局：侧边栏导航（TDesign Menu）+ 顶部栏（TDesign Header）+ 内容区（Routes）；未登录显示登录页。
// 全局逻辑：登录态恢复、15s 状态轮询、SSE 推送订阅、做空开关、通知测试、Toast 提示。
// 全站使用 TDesign React 组件 + 暗色主题（ConfigProvider theme="dark"）。
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
      <ConfigProvider theme="dark">
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
    <ConfigProvider theme="dark">
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
          <header className="app-header">
            <div style={{ display: 'flex', alignItems: 'center', gap: 12, fontSize: 13 }}>
              <div className="hamburger" onClick={() => setMenuOpen((o) => !o)}><span></span><span></span><span></span></div>
              <span>{inTradeTime !== null && (inTradeTime ? '🟢 交易时段' : '🔴 盘前/盘后')}</span>
              <span className="muted">{serverOnline ? '服务在线' : '离线'}</span>
            </div>
            <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
              <Switch value={shortEnabled} onChange={onShortToggle} />
              <span className="muted">{shortEnabled ? '做多+空' : '仅做多'}</span>
              <Button theme="default" variant="outline" size="small" onClick={() => {
                const sent = sendNotify('量仔期货', '通知测试成功')
                MessagePlugin.info('通知测试' + (sent ? '已发送' : (isNative() ? '（请检查系统通知权限）' : '（通知未授权）')))
              }}>🔔</Button>
              <Button theme="default" variant="outline" size="small" onClick={logout}>退出</Button>
            </div>
          </header>
          <div className="app-body">
            <aside className={'app-aside' + (menuOpen ? ' open' : '')}>
              <div className="brand-logo">量仔期货</div>
              <div style={{ flex: 1, overflowY: 'auto' }}>
                <Menu theme="dark" value={location.pathname} onChange={(v) => { navigate(v); setMenuOpen(false) }} style={{ width: '100%', background: 'transparent', borderRight: 'none' }}>
                  {navItems.map((it) => (
                    <Menu.MenuItem key={it.to} value={it.to}>
                      <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
                        <span>{it.icon}</span>
                        <span>{it.label}</span>
                        {it.badge > 0 && <Badge count={it.badge} />}
                      </span>
                    </Menu.MenuItem>
                  ))}
                </Menu>
              </div>
              <div className="sidebar-footer">
                <div className="account-name">{account}</div>
              </div>
            </aside>
            {menuOpen && <div className="sidebar-overlay" onClick={() => setMenuOpen(false)} />}
            <main className="app-main">
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
