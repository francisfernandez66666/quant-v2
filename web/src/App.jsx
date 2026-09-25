// ── 根组件 App.jsx ──
// 主布局：侧边栏导航（TDesign Menu）+ 顶部栏（TDesign Header）+ 内容区（Routes）；未登录显示登录页。
// 全局逻辑：登录态恢复、60s 状态轮询、SSE 推送订阅、做空开关、通知测试、Toast 提示。
// 全站使用 TDesign React 组件 + 浅色主题（默认，不设置 theme 即为浅色）。
import React, { useState, useEffect, useRef, Suspense, lazy } from 'react'
import { NavLink, Routes, Route, useNavigate, useLocation, Navigate } from 'react-router-dom'
import { ConfigProvider, Menu, Button, Badge, MessagePlugin, Input } from 'tdesign-react'
// §F1 导航图标：emoji（📊⚡💬…）在 Windows Server / APK WebView 渲染不一致且显廉价，
// 统一换 tdesign-icons-react（package.json 已装但此前全站零使用）。
import { DashboardIcon, ThunderIcon, StarIcon, TrendingUpIcon, NotificationIcon, WalletIcon,
  ChartLineIcon, RocketIcon, SettingIcon, TerminalIcon, ChatBubble1Icon, SearchIcon, UsergroupIcon } from 'tdesign-icons-react'
import ToggleSw from './components/ToggleSw'
import MarketStatusBar from './components/MarketStatusBar'
import RoleBar from './components/RoleBar'
import CommandPalette from './components/CommandPalette'
import StockDetailDrawer from './components/StockDetailDrawer'
import Disclaimer from './components/Disclaimer'
import IcpFooter from './components/IcpFooter'
import ThemeToggle from './components/ThemeToggle'
import { useTheme } from './theme.js'
import * as api from './api/index.js'
import { dispatch as sseDispatch } from './sseBus.js'
import { isNative, canNotify, requestPermission, notify as sendNotify, notifyThrottled } from './notify.js'
import { showToast, showNotify } from './ui.jsx'
import { sseOpsAlert, versionMismatchNotice } from './utils.js'

// §A7（20260918 审计批）本地构建指纹：vite define 在构建期把 __BUILD_COMMIT__ 文本替换为
// git 短 SHA 字符串字面量（见 vite.config.js）。dev/undefined 走哨兵值不参与比对。
// English: §A7 — build-time git SHA injected by vite define; falls back to the 'dev' sentinel.
const APP_BUILD_COMMIT = typeof __BUILD_COMMIT__ !== 'undefined' ? __BUILD_COMMIT__ : 'dev'

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
// §情绪面板 C：市场情绪回看页（涨停柱+净值折线+相位色带），懒加载不进首屏包
const EmotionReview = lazy(() => import('./pages/EmotionReview.jsx'))

// 懒加载路由切换时的加载占位（页面 chunk 拉取间隙的兜底 UI）
function PageFallback() {
  return (
    <div style={{ padding: 32, color: 'var(--app-muted)', fontSize: 14 }}>页面加载中…</div>
  )
}

// 无权限兜底页：渲染 403 提示，并提供返回仪表盘的入口
function Forbidden() {
  const navigate = useNavigate()
  return (
    <div style={{ padding: 48, textAlign: 'center', color: 'var(--app-muted)' }}>
      <h2>403 · 无访问权限</h2>
      <p>当前账号无权访问此页面。</p>
      <Button theme="default" variant="outline" size="small" onClick={() => navigate('/dashboard')}>返回仪表盘</Button>
    </div>
  )
}

// 路由级权限守卫：根据后端已下发的角色/权限位决定是否渲染，无权限则重定向到 403。
// 后端接口已做鉴权兜底，此处仅作体验层保护（避免直接渲染无权页面）。
//   admin: 仅管理员可访问；perm: 指定权限位（管理员隐式全部）。
//   §A5（20260918 审计批）checked: 服务端权威角色是否已完成对账（/api/auth/me 首拉返回）。
//   未完成前不放行也不重定向（渲染加载占位），杜绝篡改 localStorage 角色后
//   admin 壳先渲染、逐屏吃 403 的窗口期。
//   English: §A5 — guarded routes stay on a loading placeholder until the authoritative role
//   from GET /api/auth/me has been reconciled into storage.
function ProtectedRoute({ admin, perm, checked, children }) {
  // §P1-13 路由守卫：未登录先跳转登录页（/ 由顶层 loggedIn 切换为登录视图），
  // 已登录但权限/角色不足才落到 /403，避免未登录直接暴露 401 空页面。
  if (!api.isLoggedIn()) {
    return <Navigate to="/" replace />
  }
  if (!checked) {
    return <PageFallback />
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
  // §A5（20260918 审计批）服务端权威角色对账完成位：ProtectedRoute 在其为 false 前不放行
  const [meChecked, setMeChecked] = useState(false)
  // §A7（20260918 审计批）版本漂移告警文案：refreshStatus 比对本地构建指纹与 /api/status
  // build_commit 得出；null=一致或双方有哨兵值（dev/unknown/缺字段）不告警。
  const [versionNotice, setVersionNotice] = useState(null)
  // 权限入口状态位：研究审批/管理员/模拟盘三个入口由后端角色与开关决定
  const [paperEnabled, setPaperEnabled] = useState(false)
  // §MARKET_RISK_GATE F2：市场环境条状态（情绪相位/市场状态/仓位档/风险档），由 SSE `score` 广播驱动
  // English: F2 market-environment bar state, driven by the SSE `score` broadcast.
  const [marketEnv, setMarketEnv] = useState({ emotion: '', marketState: '', maxPosPct: 0, riskTier: '', riskReasons: [] })
  // §F4 全局主题（浅/深），驱动 TDesign Menu 的 theme 属性与顶栏切换按钮状态。
  const [theme] = useTheme()
  // §F6 命令面板（Ctrl/Cmd+K）开关 + 全局个股详情抽屉目标（复用 F3 组件，任意页可呼出）。
  const [paletteOpen, setPaletteOpen] = useState(false)
  const [globalDetail, setGlobalDetail] = useState(null)

  const [serverUrl, setServerUrl] = useState(api.getStoredServer() || '')
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [logging, setLogging] = useState(false)
  const [loginError, setLoginError] = useState('')

  const statusTimer = useRef(null) // 状态轮询定时器句柄
  const unsubSSE = useRef(null)     // SSE 取消订阅函数引用
  // §H7（2026-09-22 修复批）auth:expired 闭包死亡：挂载 effect 只注册一次监听器，旧实现的
  // onAuthExpired 直接捕获渲染态 loggedIn——首帧为 false 被永久冻结在闭包里，登录成功
  // （checkAuth 异步置真 / handleLogin）之后 auth:expired 事件到达仍恒早退，
  // 登出提示在部分路径永不出现在。改法：latest 值镜像进 ref，回调只读 ref。
  // English: §H7 — mirror the latest render values into refs so the once-registered
  // auth:expired listener never runs against the stale first-frame closure.
  const loggedInRef = useRef(false)
  // §H7：同样镜像最新 onAuthExpired 闭包（内含最新 logout/navigate），挂载 effect 经 ref 转发调用
  const authExpiredHandler = useRef(null)

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
      // §A5（20260918 审计批）强对账门：受 ProtectedRoute 保护的路由在 meChecked 置真前
      // 只显示加载占位——localStorage 角色（可被手工篡改）不再决定 admin 壳是否渲染，
      // 一切以 GET /api/auth/me 回读的服务端权威角色为准。对账失败（服务器不可达）
      // 仍放行：可用性优先，后端逐接口鉴权兜底不变；401 由 request() 广播 auth:expired 回登录页。
      // English: §A5 — guarded routes wait for the authoritative GET /api/auth/me reconciliation;
      // a failed reconcile (offline server) still lets the app render with cached roles.
      try { await api.refreshMe() } catch (_) {}
      setMeChecked(true)
      applyRoleGates()
      // §M-11（2026-09-22 修复批）恢复登录态时把账号补报原生壳（仅 WebView 桥存在时生效）：
      // 覆盖旧版 APK 升级/首装迁移后壳侧从未收到账号、极光别名仍停在默认 quant_owner 的窗口。
      // typeof 守卫：测试桩/裁剪版 api 模块可能整体未导出该函数，缺即跳过不炸登录链。
      if (typeof api.syncPushAccount === 'function') api.syncPushAccount()
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
      // §A5：登录响应本身即服务端权威角色（storeAuth 落盘 role/perms），对账门直接放行
      setMeChecked(true)
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
  // §D7 修复：调用 api.logout() 触发服务端 POST /api/auth/logout 吊销当前会话——
  // 旧行为只 clearAuth 清 localStorage，Sessions 里那条哈希仍在，被截获可在 TTL 内复用。
  // 前端立刻本地清并跳登录，网络请求异步进行（失败兜底靠 TTL 过期，不阻塞 UX）。
  function logout() {
    api.logout()  // 内部会 clearAuth；不 await 以免网络抖动拖住退出体验
    stopPolling()
    setLoggedIn(false)
    setMenuOpen(false)
    setMeChecked(false) // §A5：换账号后对账门重置，下次恢复登录态必须重新过 /api/auth/me
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
      // §A7：APK/页面向导比对——服务器 build_commit 与本地构建指纹不一致即顶栏横幅告警
      setVersionNotice(versionMismatchNotice(APP_BUILD_COMMIT, st.build_commit))
    } catch (_) { setServerOnline(false); setVersionNotice(null) }
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
      const next = res.short_enabled || false
      setShortEnabled(next)
      // §F26 修复：广播做空状态变更，让 MsgCenter 等订阅页与顶栏保持同一份真相；
      // 旧行为各页 mount 独立 fetch，顶栏切"仅做多"进 MsgCenter 又显示"做多+空"。
      // English: F26 — broadcast short_enabled so consumers (MsgCenter etc.) stay in sync with
      // the header toggle instead of refetching stale state on mount.
      window.dispatchEvent(new CustomEvent('short:toggled', { detail: { enabled: next } }))
      MessagePlugin.info(next ? '做空已开启' : '做空已关闭')
    } catch (_) {
      setShortEnabled(!val)
      MessagePlugin.error('做空开关切换失败')
    }
  }

  // 处理 SSE 推送：scan 信号、重要消息提醒、单条新信号
  function handleSSE(msg) {
    // §F5 单连接扇出：先转投事件总线，供各页按类型订阅即时刷新（页面不再各自起高频轮询）。
    // English: fan out on the single connection so pages subscribing by type refresh on event.
    sseDispatch(msg)
    // §UAT-D1（2026-09-16）：资损/运维级事件（熔断翻转、持仓清空守卫、交割对账差异、实时放量触发）
    // 后端一直在广播，但前端零消费，只能靠 10s 轮询间接感知——现统一收敛成全局 Toast + 系统通知，
    // 命中即返回（不与下方 scan/message 分支重复弹）。映射逻辑在 utils.sseOpsAlert（纯函数、单测覆盖）。
    // English: surface the previously-broadcast-but-unconsumed ops/trading-safety SSE events via a
    // global Toast + system notification; handled-and-return so they do not double-toast below.
    const ops = sseOpsAlert(msg)
    if (ops) {
      showToast(ops.body, ops.tone)
      notifyThrottled(ops.key, ops.title, ops.body)
      return
    }
    // §MARKET_RISK_GATE F2：每轮评分完成广播的 `score` 消息携带市场环境（情绪/状态/仓位档/风险档），
    // 驱动顶部市场环境条；score 不含信号字段，更新后立即返回，不影响下方 scan/message 分支。
    // English: each scoring round's `score` message carries the environment snapshot to drive the F2 bar.
    if (msg && msg.type === 'score') {
      setMarketEnv({
        emotion: msg.emotion || '',
        marketState: msg.market_state || '',
        maxPosPct: msg.max_pos_pct || 0,
        riskTier: msg.risk_tier || '',
        riskReasons: Array.isArray(msg.risk_reasons) ? msg.risk_reasons : [],
      })
      return
    }
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
    // §0925EVE-W3-I（条目 E4a）删除原此处 `if (msg.signal) { showToast('新信号: …'); refreshStatus() }`
    // 死分支。读码锤实全仓唯一带顶层 signal 字段的 SSE 载荷是放量急拉广播
    // （internal/trigger/trigger.go:187-190 `{type:"trigger", signal}`），且其 signal.code/signal.msg
    // 恒非空 → 必被上方 sseOpsAlert（utils.js case 'trigger'）消费并提前 return，永走不到这里；
    // scan/message/score 载荷均无顶层 signal 字段（engine.go:3462/4783，grep 全仓证毕）。
    // English: §0925EVE-W3-I — removed the provably-dead `msg.signal` branch: the only SSE payload
    // carrying a top-level `signal` key is the trigger event, which always has code/msg and is
    // consumed earlier by sseOpsAlert with an early return.
  }

  // 全局认证过期事件回调：提示并安全退出
  function onAuthExpired() {
    // §H7：改读 ref 最新登录态——旧实现在此读渲染态 loggedIn（挂载闭包里恒为首帧 false），
    // 登录成功后的 auth:expired 事件被错误早退，登出提示丢失。未登录时仍应静默跳过。
    if (!loggedInRef.current) return
    MessagePlugin.error('登录已过期，请重新登录')
    logout()
  }

  // §H7：每次渲染后把最新值/最新闭包镜像进 ref（无 deps，随每个 commit 执行）
  useEffect(() => {
    loggedInRef.current = loggedIn
    authExpiredHandler.current = onAuthExpired
  })

  // 启动状态轮询并订阅 SSE 推送
  function startPolling() {
    refreshStatus()
    statusTimer.current = setInterval(refreshStatus, 60000)
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
    // §H7：deps 保持 []（监听器只在挂载注册一次），但注册的是转发壳——
    // 经 authExpiredHandler.current 取最新闭包执行，彻底绕开首帧陈旧闭包；
    // 卸载时按同一壳引用注销。
    const onExpired = () => {
      if (authExpiredHandler.current) authExpiredHandler.current()
    }
    window.addEventListener('auth:expired', onExpired)
    return () => {
      window.removeEventListener('auth:expired', onExpired)
      stopPolling()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // §F6 Ctrl/Cmd+K 呼出命令面板（仅登录后全局监听；输入框内也可用，避免吞正常输入需判断焦点？
  // 组合键本身罕见误触，直接 preventDefault 打开面板）。
  useEffect(() => {
    if (!loggedIn) return
    // 全局快捷键：Ctrl/⌘+K 唤起/收起命令面板
    const onKey = (e) => {
      if ((e.ctrlKey || e.metaKey) && (e.key === 'k' || e.key === 'K')) {
        e.preventDefault()
        setPaletteOpen((o) => !o)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [loggedIn])

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
            <Disclaimer variant="login" />
            {/* ICP 备案号页脚（管局要求：首页底部展示备案号并链接工信部首页） */}
            <IcpFooter variant="login" />
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
    // §情绪面板 C：情绪回看入口（涨停柱+净值+相位色带）
    { to: '/emotion', icon: <ChartLineIcon size="18px" />, label: '情绪回看' },
    { to: '/msgcenter', icon: <NotificationIcon size="18px" />, label: '消息', badge: alertCount },
    { to: '/positions', icon: <WalletIcon size="18px" />, label: '持仓' },
    { to: '/quant', icon: <ChartLineIcon size="18px" />, label: '量化交易' },
    paperEnabled ? { to: '/paper', icon: <RocketIcon size="18px" />, label: '模拟盘' } : null,
    canAdmin ? { to: '/settings', icon: <SettingIcon size="18px" />, label: '设置' } : null,
    // §PERM-GATE 20260918：LLM 诊断两个主数据源均为 admin 守卫（server.go:683/692），
    // 成员常显入口进页必 403 —— 收敛为仅管理员可见。
    canAdmin ? { to: '/llm-debug', icon: <TerminalIcon size="18px" />, label: 'LLM诊断' } : null,
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
            <Disclaimer variant="login" />
            {/* ICP 备案号页脚（管局要求：首页底部展示备案号并链接工信部首页） */}
            <IcpFooter variant="login" />
          </div>
        </div>
      ) : (
        <>
        {/* 主布局外壳：顶部栏 + 断联横幅 + 侧边栏 + 内容路由区三段式结构 */}
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
              {/* §F4 主题切换按钮：浅/深色（持久化到 localStorage，切换即时全站换肤） */}
              <ThemeToggle />
              {/* 退出登录 */}
              <Button theme="default" variant="outline" size="small" onClick={logout}>退出</Button>
            </div>
            {/*
             * 顶部栏右侧操作项至此排布完毕，header 标签收口 */}
           </header>
            {/* §MARKET_RISK_GATE F2 市场环境条：全站唯一市场环境展示位，紧贴顶部栏下方（情绪相位/市场状态/风险档） */}
            <MarketStatusBar env={marketEnv} />
            {/* §F6 角色提示条：常驻一行说明当前账号/角色/可见入口（UAT 2.1 防误操作困惑） */}
            <RoleBar account={account} isAdmin={canAdmin} canResearch={canResearch} paperEnabled={paperEnabled} />
           {/* 后端断联横幅：登录态可能因缓存令牌保留，但所有数据接口失败。
               显式提示用户检查「设置→服务器地址」（留空=使用当前域名），避免误以为"后端没给数据"。 */}
           {loggedIn && !serverOnline && (
             // 通栏提示框：仅在登录态且最近一次状态轮询失败时渲染
              <div style={{ margin: '8px 12px 0', padding: '8px 12px', borderRadius: 6, background: 'var(--app-warn-bg)', border: '1px solid var(--app-warn-border)', color: 'var(--app-warn-text)', fontSize: 13 }}>
               ⚠ 无法连接服务器：页面可打开但后端数据未加载。请到「设置 → 服务器连接」确认服务器地址——
               若填了自定义地址请改为留空（使用当前域名 quant-trading.top），或确认该地址可达。
             </div>
           )}
           {/* §A7 版本漂移横幅：内嵌前端构建指纹与后端 /api/status build_commit 不一致时常驻提示
               （典型场景：APK 未随服务器重新打包）。样式沿用断联横幅的 warn 变量。
               English: §A7 — persistent banner when the embedded build id lags the server's. */}
           {versionNotice && (
             <div style={{ margin: '8px 12px 0', padding: '8px 12px', borderRadius: 6, background: 'var(--app-warn-bg)', border: '1px solid var(--app-warn-border)', color: 'var(--app-warn-text)', fontSize: 13 }}>
               🔄 {versionNotice}
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
                <Menu theme={theme === 'dark' ? 'dark' : 'light'} value={location.pathname} onChange={(v) => { navigate(v); setMenuOpen(false) }} style={{ width: '100%', background: 'transparent', borderRight: 'none' }}>
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
                {/* §A7 内嵌前端构建指纹常驻展示（APK 排查"这份壳到底是什么版本打包的"一眼可辨） */}
                <div style={{ fontSize: 11, opacity: .6, marginTop: 4 }}>build {APP_BUILD_COMMIT}</div>
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
                    <Route path="/settings" element={<ProtectedRoute admin checked={meChecked}><Settings /></ProtectedRoute>} />
                    {/*
                     * LLM 诊断页：查看大模型调用与结构化输出
                     * §PERM-GATE 20260918：数据源 admin 守卫，套 ProtectedRoute admin 与侧栏一致 */}
                    <Route path="/llm-debug" element={<ProtectedRoute admin checked={meChecked}><LLMDebug /></ProtectedRoute>} />
                    {/*
                     * 股票咨询页：自然语言问询个股/板块 */}
                    <Route path="/consult" element={<Consult />} />
                    {/*
                     * 自动研究页：需 research_approve 权限（研究闭环审批入口） */}
                    <Route path="/research" element={<ProtectedRoute perm="research_approve" checked={meChecked}><Research /></ProtectedRoute>} />
                    {/*
                     * 用户管理页：仅管理员 */}
                    <Route path="/admin" element={<ProtectedRoute admin checked={meChecked}><Admin /></ProtectedRoute>} />
                    {/*
                     * 模拟盘页：纸面交易记账/自动撮合（入口受 paperEnabled 开关控制） */}
                    <Route path="/paper" element={<Paper />} />
                    {/*
                     * 情绪回看页：涨停柱+账户净值+相位色带三合一（§情绪面板 C） */}
                    <Route path="/emotion" element={<EmotionReview />} />
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
        {/* §F6 命令面板（Ctrl/Cmd+K）：页面跳转 + 六位代码查看个股详情（复用 F3 全局抽屉） */}
        {paletteOpen && (
          <CommandPalette
            pages={navItems.map((n) => ({ to: n.to, label: n.label }))}
            onOpenStock={(code) => setGlobalDetail({ code })}
            onClose={() => setPaletteOpen(false)}
          />
        )}
        {/* §F6 全局个股详情抽屉（命令面板/未来任意入口共用），仅此处一份实例避免多页重复 */}
        <StockDetailDrawer open={!!globalDetail} code={globalDetail?.code}
          onClose={() => setGlobalDetail(null)} />
        </>
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
