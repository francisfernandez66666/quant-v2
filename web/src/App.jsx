// ── 根组件 App.jsx ──
// 主布局：侧边栏导航（TDesign Menu）+ 顶部栏（TDesign Header）+ 内容区（Routes）；未登录显示登录页。
// 全局逻辑：登录态恢复、15s 状态轮询、SSE 推送订阅、做空开关、通知测试、Toast 提示。
// 全站使用 TDesign React 组件 + 浅色主题（默认，不设置 theme 即为浅色）。
import React, { useState, useEffect, useRef, Suspense, lazy } from 'react'
import { NavLink, Routes, Route, useNavigate, useLocation, Navigate } from 'react-router-dom'
import { ConfigProvider, Menu, Button, Badge, MessagePlugin, Input } from 'tdesign-react'
// §F1 导航图标：emoji（📊⚡💬…）在 Windows Server / APK WebView 渲染不一致且显廉价，
// 统一换 tdesign-icons-react（package.json 已装但此前全站零使用）。
import { DashboardIcon, ThunderIcon, StarIcon, TrendingUpIcon, NotificationIcon, WalletIcon,
  ChartLineIcon, RocketIcon, SettingIcon, TerminalIcon, ChatBubble1Icon, SearchIcon, UsergroupIcon } from 'tdesign-icons-react'
import ToggleSw from './components/ToggleSw'
import * as api from './api/index.js'
import { isNative, canNotify, requestPermission, notify as sendNotify, notifyThrottled } from './notify.js'
import { showToast, showNotify } from './ui.jsx'

import Dashboard from './pages/Dashboard.jsx'
import ErrorBoundary from './components/ErrorBoundary.jsx'

// §R4-10 路由级代码分割：除首屏落地页 Dashboard 外，其余页面全部 React.lazy 按需加载，
// Vite 自动按页面拆 chunk——首屏 bundle 只含外壳+Dashboard，进入对应路由时才拉取该页代码
//（旧实现 13 个页面全量打进单包，954KB 首屏一次拉完）。
// English: §R4-10 route-level code splitting — every page except the landing Dashboard is lazy-loaded
// so the first-screen bundle only carries the shell + Dashboard; each route chunk is fetched on demand.
const Signals = lazy(() => import('./pages/Signals.jsx'))
const Watchlist = lazy(() => import('./pages/Watchlist.jsx'))
const Positions = lazy(() => import('./pages/Positions.jsx'))
const Quant = lazy(() => import('./pages/Quant.jsx'))
const Hotspot = lazy(() => import('./pages/Hotspot.jsx'))
const MsgCenter = lazy(() => import('./pages/MsgCenter.jsx'))
const Settings = lazy(() => import('./pages/Settings.jsx'))
const LLMDebug = lazy(() => import('./pages/LLMDebug.jsx'))
const Consult = lazy(() => import('./pages/Consult.jsx'))
const Research = lazy(() => import('./pages/Research.jsx'))
const Admin = lazy(() => import('./pages/Admin.jsx'))
const Paper = lazy(() => import('./pages/Paper.jsx'))

// 懒加载路由切换时的加载占位（页面 chunk 拉取间隙的兜底 UI）
function PageFallback() {
  return (
    <div style={{ padding: 32, color: '#888', fontSize: 14 }}>页面加载中…</div>
  )
}

// 无权限兜底页：渲染 403 提示，并提供返回仪表盘的入口
function Forbidden() {
  const navigate = useNavigate()
  return (
    <div style={{ padding: 48, textAlign: 'center', color: '#888' }}>
      <h2>403 · 无访问权限</h2>
      <p>当前账号无权访问此页面。</p>
      <Button theme="default" variant="outline" size="small" onClick={() => navigate('/dashboard')}>返回仪表盘</Button>
    </div>
  )
}

// 路由级权限守卫：根据后端已下发的角色/权限位决定是否渲染，无权限则重定向到 403。
// 后端接口已做鉴权兜底，此处仅作体验层保护（避免直接渲染无权页面）。
//   admin: 仅管理员可访问；perm: 指定权限位（管理员隐式全部）。
function ProtectedRoute({ admin, perm, children }) {
  // §P1-13 路由守卫：未登录先跳转登录页（/ 由顶层 loggedIn 切换为登录视图），
  // 已登录但权限/角色不足才落到 /403，避免未登录直接暴露 401 空页面。
  if (!api.isLoggedIn()) {
    return <Navigate to="/" replace />
  }
  const allowed = admin ? api.isAdmin() : perm ? api.hasPerm(perm) : true
  return allowed ? children : <Navigate to="/403" replace />
}

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
  const [activeWindow, setActiveWindow] = useState(null)
  const [signalCount, setSignalCount] = useState(0)
  const [alertCount, setAlertCount] = useState(0)
  const [menuOpen, setMenuOpen] = useState(false)
  const [shortEnabled, setShortEnabled] = useState(false)
  const [canResearch, setCanResearch] = useState(false)
  const [canAdmin, setCanAdmin] = useState(false)
  // 权限入口状态位：研究审批/管理员/模拟盘三个入口由后端角色与开关决定
  const [paperEnabled, setPaperEnabled] = useState(false)

  const [serverUrl, setServerUrl] = useState(api.getStoredServer() || '')
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [logging, setLogging] = useState(false)
  const [loginError, setLoginError] = useState('')

  const statusTimer = useRef(null) // 状态轮询定时器句柄
  const unsubSSE = useRef(null)     // SSE 取消订阅函数引用

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
      // 调用后端登录：成功后恢复账号与角色，并启动轮询 + 请求通知权限
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
      setActiveWindow(st.active)
    } catch (_) { setServerOnline(false) }
    // 独立轮询未读消息数：失败不影响主状态展示
    try {
      const alerts = await api.fetchAlerts()
      setAlertCount(alerts?.length || 0)
    } catch (_) {}
    // 独立轮询做空开关状态：与服务端保持一致
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
      // 扫描批次有新信号时组装中文提示并节流推送（做多/做空分别计数）
      if (bull > 0 || bear > 0) {
        const parts = []
        if (bull > 0) parts.push('做多 ' + bull + ' 条')
        if (bear > 0) parts.push('做空 ' + bear + ' 条')
        const text = '新交易信号: ' + parts.join('、') + (msg.time ? ' (' + msg.time + ')' : '')
        showToast(text, 'warning')
        notifyThrottled('scan', '量仔 交易信号', text)
      }
      refreshStatus()
      return
    }
    if (msg && msg.type === 'message' && msg.item) {
      const level = msg.item.level || ''
      // 系统弹窗仅限交易/风控关键级别；持仓提示、卖点评估等低级别只进消息中心，避免打扰
      // English: only trading/risk-critical levels raise system notifications; low-level notices stay in the message center.
      const critical = level.indexOf('止盈') >= 0 || level.indexOf('止损') >= 0 || level.indexOf('清仓') >= 0 || level.indexOf('交易信号') >= 0
      if (critical) {
        const code = msg.item.code || ''
        const name = msg.item.name || ''
        const title = level ? ('量仔 ' + level) : '量仔 提醒'
        // 组装通知正文：代码+名称+标题/内容，按 (code@level) 做去重节流
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
            {/*
             * 登录卡片：品牌标题 + 三项表单 + 登录按钮 */}
            <h1>量仔</h1>
            <p className="subtitle">量化交易辅助工具</p>
            {/*
             * 表单域一：服务器地址，留空即用当前域名 */}
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
            {/*
             * 登录按钮：loading 期间防止重复提交；错误内容即时展示 */}
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
    { to: '/dashboard', icon: <DashboardIcon size="18px" />, label: '仪表盘' },
    { to: '/signals', icon: <ThunderIcon size="18px" />, label: '信号', badge: signalCount },
    { to: '/watchlist', icon: <StarIcon size="18px" />, label: '自选' },
    { to: '/hotspot', icon: <TrendingUpIcon size="18px" />, label: '热点' },
    { to: '/msgcenter', icon: <NotificationIcon size="18px" />, label: '消息', badge: alertCount },
    { to: '/positions', icon: <WalletIcon size="18px" />, label: '持仓' },
    { to: '/quant', icon: <ChartLineIcon size="18px" />, label: '量化交易' },
    paperEnabled ? { to: '/paper', icon: <RocketIcon size="18px" />, label: '模拟盘' } : null,
    canAdmin ? { to: '/settings', icon: <SettingIcon size="18px" />, label: '设置' } : null,
    { to: '/llm-debug', icon: <TerminalIcon size="18px" />, label: 'LLM诊断' },
    { to: '/consult', icon: <ChatBubble1Icon size="18px" />, label: '股票咨询' },
    canResearch ? { to: '/research', icon: <SearchIcon size="18px" />, label: '自动研究' } : null,
    // 条件项为 null 时由下方 filter(Boolean) 剔除，实现入口按权限显隐
    canAdmin ? { to: '/admin', icon: <UsergroupIcon size="18px" />, label: '用户管理' } : null,
  ].filter(Boolean)

  // §安全 F1（2026-08-29）：全局 ErrorBoundary 包裹整个应用（登录页/顶部栏/侧边栏/路由出口），
  // 任意位置渲染抛错均显示中文兜底 UI，避免整页白屏。此前仅包裹主内容区路由出口，
  // 顶栏/侧栏/登录页仍在边界外。
  return (
    <ErrorBoundary>
    <ConfigProvider>
      {/* 安全兜底：理论上进入主布局时 loggedIn 必为 true，此处保留登录页分支以防状态竞态 */}
      {/* 登录兜底分支：表单结构（服务器地址/账号/密码/登录按钮）与上方未登录视图一致，
          防止主布局阶段登录态被意外置空导致白屏 */}
      {!loggedIn ? (
        <div className="login-page">
          <div className="login-box">
            {/*
             * 登录卡片标题与副标题 */}
            <h1>量仔</h1>
            <p className="subtitle">量化交易辅助工具</p>
            {/*
             * 表单域一：服务器地址（留空使用当前域名） */}
            <div className="form-group">
              <label>服务器地址</label>
              <Input value={serverUrl} onChange={(v) => setServerUrl(v)} placeholder="留空表示使用当前域名" />
            </div>
            {/*
             * 表单域二：登录账号 */}
            <div className="form-group">
              <label>账号</label>
              <Input value={username} onChange={(v) => setUsername(v)} placeholder="输入账号" />
            </div>
            {/*
             * 表单域三：登录密码（支持回车直接登录） */}
            <div className="form-group">
              <label>密码</label>
              <Input type="password" value={password} onChange={(v) => setPassword(v)} placeholder="输入密码" onEnter={handleLogin} />
            </div>
            {/*
             * 登录按钮与错误提示 */}
            <Button theme="primary" loading={logging} onClick={handleLogin} block>登录</Button>
            {loginError && <p className="login-error">{loginError}</p>}
          </div>
        </div>
      ) : (
        /* 主布局外壳：顶部栏 + 断联横幅 + 侧边栏 + 内容路由区三段式结构 */
        <div className="app-shell">
          {/* 顶部栏：左侧为汉堡菜单按钮 + 交易时段指示 + 服务在线状态，右侧为做空开关 + 通知测试 + 退出 */}
          <header className="app-header">
            {/*
             * 左侧状态区：汉堡按钮、量化活跃窗口指示、后端连通状态 */}
            <div style={{ display: 'flex', alignItems: 'center', gap: 12, fontSize: 13 }}>
              {/* 汉堡按钮：点击切换侧边栏显隐（移动端抽屉式） */}
              <div className="hamburger" onClick={() => setMenuOpen((o) => !o)}><span></span><span></span><span></span></div>
              {/* 量化活跃窗口指示：active=true 表示交易日 9:15-15:30（广州生产节点）引擎活跃；否则静默释放性能 */}
              <span>{activeWindow !== null && (activeWindow ? '🟢 量化活跃 9:15-15:30' : '🌙 静默释放')}</span>
              {/* 后端服务连通状态文字提示 */}
              <span className="muted">{serverOnline ? '服务在线' : '离线'}</span>
            </div>
            <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
              {/*
               * 右侧操作区：做空开关、通知测试、退出登录 */}
              {/* 做空开关：开启后允许"做多+做空"，关闭则"仅做多" */}
              <ToggleSw checked={shortEnabled} onChange={onShortToggle} />
              <span className="muted">{shortEnabled ? '做多+空' : '仅做多'}</span>
              {/* 通知测试按钮：验证系统/原生通知通道是否可用 */}
              <Button theme="default" variant="outline" size="small" onClick={() => {
                const sent = sendNotify('量仔', '通知测试成功')
                MessagePlugin.info('通知测试' + (sent ? '已发送' : (isNative() ? '（请检查系统通知权限）' : '（通知未授权）')))
              }}>🔔</Button>
              {/* 退出登录 */}
              <Button theme="default" variant="outline" size="small" onClick={logout}>退出</Button>
            </div>
            {/*
             * 顶部栏右侧操作项至此排布完毕，header 标签收口 */}
           </header>
           {/* 后端断联横幅：登录态可能因缓存令牌保留，但所有数据接口失败。
               显式提示用户检查「设置→服务器地址」（留空=使用当前域名），避免误以为"后端没给数据"。 */}
           {loggedIn && !serverOnline && (
             // 通栏提示框：仅在登录态且最近一次状态轮询失败时渲染
             <div style={{ margin: '8px 12px 0', padding: '8px 12px', borderRadius: 6, background: '#fdecea', border: '1px solid #f5c6c2', color: '#b71c1c', fontSize: 13 }}>
               ⚠ 无法连接服务器：页面可打开但后端数据未加载。请到「设置 → 服务器连接」确认服务器地址——
               若填了自定义地址请改为留空（使用当前域名 quant-trading.top），或确认该地址可达。
             </div>
           )}
           <div className="app-body">
            {/* 中部主体注释起点：以下为 app-body（左栏 aside + 右栏 main） */}
            {/*
             * 中部主体：左侧侧边栏 + 右侧内容区（移动端侧栏折叠为抽屉） */}
            {/* 侧边栏：品牌 logo + 导航菜单 + 底部当前账号；menuOpen 控制移动端抽屉展开 */}
            <aside className={'app-aside' + (menuOpen ? ' open' : '')}>
              {/*
               * 品牌标识区：logo 文字 */}
              <div className="brand-logo">量仔</div>
              {/*
               * 可滚动导航区：TDesign Menu 渲染 navItems，当前路由高亮 */}
              <div style={{ flex: 1, overflowY: 'auto' }}>
                {/* 根据 navItems 渲染导航项，当前路由高亮；点击后跳转并收起抽屉 */}
                {/*
                 * Menu 配置：value 取当前路由路径，选中项即导航并收起抽屉 */}
                <Menu theme="light" value={location.pathname} onChange={(v) => { navigate(v); setMenuOpen(false) }} style={{ width: '100%', background: 'transparent', borderRight: 'none' }}>
                  {/* 导航项循环渲染：icon + 文案 + 可选未读角标 */}
                  {navItems.map((it) => (
                    <Menu.MenuItem key={it.to} value={it.to}>
                      <span style={{ display: 'inline-flex', alignItems: 'center', gap: 8 }}>
                        {it.icon}
                        <span>{it.label}</span>
                        {/* 存在未读数量时展示角标（信号/消息数） */}
                        {it.badge > 0 && <Badge count={it.badge} />}
                      </span>
                    </Menu.MenuItem>
                  ))}
                  {/*
                   * 上述循环渲染完毕：Menu 至此闭合，转入底部账号区 */}
                </Menu>
              </div>
              {/* 侧边栏底部固定显示登录账号名与角色：后端下发的身份，前端只展示，
                  便于一眼确认当前是管理员还是普通用户（量化/模拟盘仅管理员可操作）。 */}
              {/* 底部账号区渲染：当前登录账号 + 管理员/普通用户徽标 */}
              <div className="sidebar-footer">
                <div className="account-name">{account || '未登录'}</div>
                <div className={canAdmin ? 'role-badge role-admin' : 'role-badge role-user'}>
                  {canAdmin ? '管理员' : '普通用户'}
                </div>
              </div>
            </aside>
            {/*
             * 移动端抽屉遮罩：menuOpen 为真时渲染全屏遮罩，点击即收起侧栏 */}
            {/* 移动端抽屉展开时，点击遮罩收起侧边栏 */}
            {menuOpen && <div className="sidebar-overlay" onClick={() => setMenuOpen(false)} />}
            <main className="app-main">
              {/*
               * 右侧内容区：全局 ErrorBoundary > Suspense > Routes 三层包裹 */}
              {/* 路由出口注释：path 相配即渲染对应页面组件；根路径重定向仪表盘 */}
              {/* 路由出口：根据 path 渲染对应页面组件；根路径重定向到仪表盘 */}
              {/* 惰性加载兜底：lazy 页面拉取期间显示 PageFallback 占位 */}
              {/* 用全局 ErrorBoundary 包裹路由出口：任意页面渲染抛错时显示中文兜底 UI，避免整页白屏 */}
              <ErrorBoundary>
                {/* §R4-10 Suspense 兜底：lazy 页面 chunk 加载期间显示占位，避免白屏 */}
                <Suspense fallback={<PageFallback />}>
                  {/*
                   * 路由表注释起点：下方 <Routes> 逐条登记 13 个业务页面 + 403 兜底 */}
                  {/* 路由表：/settings /research /admin 均套 ProtectedRoute 权限守卫，
                      其余页面默认全员可见（后端接口仍有鉴权兜底） */}
                  <Routes>
                    {/*
                     * 根路径重定向到仪表盘 */}
                    <Route path="/" element={<Navigate to="/dashboard" replace />} />
                    {/*
                     * 首屏直接引入的落地页 Dashboard（不参与懒加载） */}
                    <Route path="/dashboard" element={<Dashboard />} />
                    {/*
                     * 信号页：策略评级信号列表，徽标角标展示未读数 */}
                    <Route path="/signals" element={<Signals />} />
                    {/*
                     * 自选股页 */}
                    <Route path="/watchlist" element={<Watchlist />} />
                    {/*
                     * 持仓页：实盘持仓管理 */}
                    <Route path="/positions" element={<Positions />} />
                    {/*
                     * 量化交易页：策略运行与调仓入口 */}
                    <Route path="/quant" element={<Quant />} />
                    {/*
                     * 热点页：热点板块/评分排名/宏观日历/IPO/资讯 */}
                    <Route path="/hotspot" element={<Hotspot />} />
                    {/*
                     * 消息中心：系统提醒与低级别通知（角标展示 alertCount 未读数） */}
                    <Route path="/msgcenter" element={<MsgCenter />} />
                    {/*
                     * 设置页：仅管理员（ProtectedRoute admin 守卫） */}
                    <Route path="/settings" element={<ProtectedRoute admin><Settings /></ProtectedRoute>} />
                    {/*
                     * LLM 诊断页：查看大模型调用与结构化输出 */}
                    <Route path="/llm-debug" element={<LLMDebug />} />
                    {/*
                     * 股票咨询页：自然语言问询个股/板块 */}
                    <Route path="/consult" element={<Consult />} />
                    {/*
                     * 自动研究页：需 research_approve 权限（研究闭环审批入口） */}
                    <Route path="/research" element={<ProtectedRoute perm="research_approve"><Research /></ProtectedRoute>} />
                    {/*
                     * 用户管理页：仅管理员 */}
                    <Route path="/admin" element={<ProtectedRoute admin><Admin /></ProtectedRoute>} />
                    {/*
                     * 模拟盘页：纸面交易记账/自动撮合（入口受 paperEnabled 开关控制） */}
                    <Route path="/paper" element={<Paper />} />
                    {/*
                     * 403 无权限兜底页 */}
                    <Route path="/403" element={<Forbidden />} />
                  </Routes>
                  {/*
                   * Routes 结束：以上覆盖全部 13 个业务页面 + 403 兜底（含懒加载兜底说明） */}
                </Suspense>
              </ErrorBoundary>
            </main>
            {/* app-main 内容区注释锚点：右栏至此整体闭合 */}
          </div>
          {/* app-body 结语：左侧导航 + 右栏已有路由，外层 div 自此闭合 */}
        </div>
      )}
      {/*
       * ConfigProvider 提供全局主题与组件上下文；ErrorBoundary 兜底任意渲染错误，避免白屏，下方标签逐一闭合 */}
    </ConfigProvider>
    {/*
     * ErrorBoundary 收尾：主布局包裹层闭合 */}
    </ErrorBoundary>
  )
  // 根组件 App 渲染结束：登录页或主布局（顶部栏/侧边栏/路由出口）
  // JSX 树整体闭合，App 组件定义到此结束 ── App.jsx 全文完 ──
}
