<!--
  根组件 App.vue (Root component)
  主布局：侧边栏导航 + 顶部栏 + 内容区（router-view）
  Layout: sidebar navigation + top bar + content area (router-view)
  未登录时显示登录页
  Shows the login page when not authenticated.

  文件职责：
  1. 已登录：渲染应用主界面（侧边栏导航 + 顶部栏 + 内容区）；
  2. 未登录：渲染登录页（服务器地址 / 账号 / 密码表单）；
  3. 应用生命周期管理：登录态恢复、15 秒状态轮询、SSE 实时推送订阅、
     做空开关、通知测试、Toast 提示等全局逻辑都集中在本组件。

  File responsibilities:
  1. Logged in: render the main UI (sidebar navigation + top bar + content area);
  2. Not logged in: render the login page (server URL / account / password form);
  3. App lifecycle management: auth restore, 15s status polling, SSE push subscription,
     short-selling toggle, notification test, Toast messages — all global logic lives here.
-->
<template>
  <!-- 已登录：主界面 -->
  <div class="app" v-if="loggedIn">
    <!-- 移动端汉堡菜单按钮：点击切换侧边栏展开/收起 -->
    <div class="hamburger" @click="menuOpen = !menuOpen">
      <span></span><span></span><span></span>
    </div>
    <!-- 移动端侧栏遮罩层：点击遮罩即关闭侧边栏 -->
    <div class="sidebar-overlay" v-if="menuOpen" @click="menuOpen = false"></div>
    <!-- 侧边栏导航：通过 router-link 切换页面，点击后关闭移动端菜单 -->
    <aside class="sidebar" :class="{ open: menuOpen }">
      <div class="logo">量仔期货</div>
      <nav class="nav">
        <router-link to="/dashboard" class="nav-item" active-class="active" @click="menuOpen = false">
          <span class="nav-icon">📊</span> 仪表盘
        </router-link>
        <!-- 信号入口：signalCount > 0 时展示未读信号角标 -->
        <router-link to="/signals" class="nav-item" active-class="active" @click="menuOpen = false">
          <span class="nav-icon">⚡</span> 信号
          <span class="badge" v-if="signalCount > 0">{{ signalCount }}</span>
        </router-link>
        <router-link to="/watchlist" class="nav-item" active-class="active" @click="menuOpen = false">
          <span class="nav-icon">👁</span> 自选
        </router-link>
        <router-link to="/hotspot" class="nav-item" active-class="active" @click="menuOpen = false">
          <span class="nav-icon">🔥</span> 热点
        </router-link>
        <!-- 消息入口：alertCount > 0 时展示未读提醒角标 -->
        <router-link to="/msgcenter" class="nav-item" active-class="active" @click="menuOpen = false">
          <span class="nav-icon">💬</span> 消息
          <span class="badge" v-if="alertCount > 0">{{ alertCount }}</span>
        </router-link>
        <router-link to="/positions" class="nav-item" active-class="active" @click="menuOpen = false">
          <span class="nav-icon">💼</span> 持仓
        </router-link>
        <router-link to="/paper" class="nav-item" active-class="active" @click="menuOpen = false" v-if="paperEnabled">
          <span class="nav-icon">🧪</span> 模拟盘
        </router-link>
        <!-- §GAP2-W2 权限收口：全局策略/D1/LLM 写权限已收敛到 admin，普通用户隐藏设置入口 -->
        <router-link to="/settings" class="nav-item" active-class="active" @click="menuOpen = false" v-if="canAdmin">
          <span class="nav-icon">⚙</span> 设置
        </router-link>
        <router-link to="/llm-debug" class="nav-item" active-class="active" @click="menuOpen = false">
          <span class="nav-icon">🧠</span> LLM诊断
        </router-link>
        <router-link to="/consult" class="nav-item" active-class="active" @click="menuOpen = false">
          <span class="nav-icon">🎯</span> 股票咨询
        </router-link>
        <router-link to="/research" class="nav-item" active-class="active" @click="menuOpen = false" v-if="canResearch">
          <span class="nav-icon">🔬</span> 自动研究
        </router-link>
        <router-link to="/admin" class="nav-item" active-class="active" @click="menuOpen = false" v-if="canAdmin">
          <span class="nav-icon">👥</span> 用户管理
        </router-link>
      </nav>
      <!-- 侧栏底部：服务状态 & 账号 -->
      <div class="sidebar-footer">
        <!-- 后端服务在线状态：serverOnline 控制绿色“在线”/灰色“离线” -->
        <div class="server-status" :class="{ online: serverOnline }">
          {{ serverOnline ? '服务在线' : '离线' }}
        </div>
        <!-- 当前登录账号名 -->
        <div class="account-name">{{ account }}</div>
      </div>
    </aside>
    <!-- 主内容区 -->
    <main class="main">
      <!-- 顶部栏：交易时段、做空开关、通知测试、退出 -->
      <div class="topbar">
        <!-- 交易时段标识：inTradeTime 为 null 时不展示（尚未拉到状态） -->
        <div class="trade-time" v-if="inTradeTime !== null">
          {{ inTradeTime ? '🟢 交易时段' : '🔴 盘前/盘后' }}
        </div>
        <!-- 顶部右侧操作区：做空开关 / 通知测试 / 退出登录 -->
        <div class="topbar-right">
          <!-- 做空开关：v-model 绑定 shortEnabled，切换时调用 onShortToggle 持久化 -->
          <label class="short-toggle" :class="{ active: shortEnabled }">
            <input type="checkbox" v-model="shortEnabled" @change="onShortToggle" />
            <span class="toggle-track"><span class="toggle-thumb"></span></span>
            <span class="toggle-label">{{ shortEnabled ? '做多+空' : '仅做多' }}</span>
          </label>
          <!-- 通知测试按钮：点击弹出浏览器系统通知 -->
          <button class="btn-notify" @click="testNotify">🔔</button>
          <!-- 退出登录按钮 -->
          <button class="btn-logout" @click="logout">退出</button>
        </div>
      </div>
      <!-- 内容区：由当前路由对应的页面组件填充（KeepAlive：切换 tab 不卸载页面、保留数据缓存） -->
      <div class="content">
        <router-view v-slot="{ Component }">
          <keep-alive>
            <component :is="Component" />
          </keep-alive>
        </router-view>
      </div>
    </main>

    <!-- Toast 消息容器：按添加顺序堆叠展示，type 决定样式（info/warning/success/err） -->
    <!-- Toast 全局提示容器：App 层所有接口成功/失败反馈都经 addToast 在此堆叠展示 -->
    <div class="toast-container">
      <div v-for="(t, i) in toasts" :key="i" :class="['toast', t.type]">{{ t.msg }}</div>
    </div>
  </div>
  <!-- 未登录：登录页 -->
  <div class="app login-page" v-else>
    <div class="login-box">
      <h1>量仔期货</h1>
      <p class="subtitle">量化交易辅助工具</p>
      <!-- 服务器地址：所有 API 请求的基础地址，保存后供 api.baseUrl() 使用 -->
      <div class="form-group">
        <label>服务器地址</label>
        <input v-model="serverUrl" placeholder="留空表示使用当前域名" />
      </div>
      <!-- 账号 / 密码：登录凭据；密码框回车可直接触发登录 -->
      <div class="form-group">
        <label>账号</label>
        <input v-model="username" placeholder="输入账号" />
      </div>
      <div class="form-group">
        <label>密码</label>
        <input v-model="password" type="password" placeholder="输入密码" @keyup.enter="handleLogin" />
      </div>
      <!-- §D7-B 注册已关闭：账号由管理员后台创建，登录页保持纯登录形态 -->
      <button class="btn-login" @click="handleLogin" :disabled="logging">
        {{ logging ? '登录中...' : '登录' }}
      </button>
      <!-- 登录错误提示：失败时展示后端返回的错误信息 -->
      <p class="login-error" v-if="loginError">{{ loginError }}</p>
    </div>
  </div>
</template>

<script setup>
// ── 依赖导入 ── (Imports)
// ref 定义响应式数据；onMounted / onUnmounted 注册组件生命周期钩子 (ref for reactive data; onMounted/onUnmounted for lifecycle hooks)
import { ref, onMounted, onUnmounted } from 'vue'
// useRouter 获取路由实例，用于退出登录后编程式跳转 (useRouter for programmatic navigation after logout)
import { useRouter } from 'vue-router'
// 后端 API 方法统一挂载在 api 命名空间下 (all backend API methods are namespaced under api)
import * as api from './api/index.js'
// 通知工具：APK WebView 走原生桥，桌面浏览器走标准 Notification API
// (Notification helper: native bridge in the APK WebView, standard API on desktop)
import { isNative, canNotify, requestPermission, notify as sendNotify, notifyThrottled } from './notify.js'

// 路由实例：logout 时跳转回根路由（登录页）(router instance: navigate back to root on logout)
const router = useRouter()

// ── 响应式状态 ── (Reactive state)
const loggedIn = ref(false)          // 是否已登录 (whether logged in)
const account = ref('')              // 当前账号名 (current account name)
const serverOnline = ref(false)      // 后端服务是否在线 (whether backend service is online)
const inTradeTime = ref(null)        // 是否处于交易时段 (whether currently in trading session)
const signalCount = ref(0)           // 未读信号数 (unread signal count)
const alertCount = ref(0)            // 未读提醒数 (unread alert count)
const toasts = ref([])               // Toast 消息队列 (Toast message queue)
const menuOpen = ref(false)          // 移动端侧栏是否展开 (whether mobile sidebar is expanded)
const shortEnabled = ref(false)      // 做空开关状态 (short-selling toggle state)

// ── 权限门禁 ──
// ── Permission gates ──
// 说明：canResearch / canAdmin 用 ref 而非 computed —— computed 依赖 api.isAdmin()/hasPerm()
//       读取的是 localStorage（非响应式），退出再换账号登录时 computed 缓存不会失效，
//       导致管理员 tab（自动研究/用户管理）残留或丢失。故改为响应式 ref，
//       在登录成功 / 恢复会话 / 退出时显式调用 applyRoleGates() 重算。
// Note: canResearch / canAdmin are refs, not computed — a computed depending on api.isAdmin()/hasPerm()
//       reads localStorage (non-reactive), so its cache never invalidates when the account is switched
//       after logout, leaving the admin tabs (自动研究 / 用户管理) stale or missing. They are therefore
//       reactive refs, recomputed explicitly via applyRoleGates() on login / session restore / logout.
// 是否可进入"自动研究"页（拥有 research_approve 权限位或 admin）
// Whether the "自动研究" page is reachable (holds the research_approve bit or is admin)
const canResearch = ref(false)
// 是否可进入"用户管理"页（仅 admin）
// Whether the "用户管理" page is reachable (admin only)
const canAdmin = ref(false)

// 依据当前账号角色/权限位刷新侧栏权限门禁（换账号/登录/退出后必须调用，保证 tab 与账号一致）
// Recomputes the sidebar permission gates from the current account's role/perms; must be called after
// account switch / login / logout so the tabs always match the logged-in account.
function applyRoleGates() {
  canResearch.value = api.hasPerm('research_approve')
  canAdmin.value = api.isAdmin()
}
// 是否展示"模拟盘"入口：后端启用模拟盘时才显示（仅一次探测，避免多余请求）
const paperEnabled = ref(false)
api.fetchPaperState().then(d => { paperEnabled.value = !!d.enabled }).catch(() => { paperEnabled.value = false })

// ── 登录表单状态 ── (Login form state)
// 服务器地址初始值优先取本地持久化值，否则用默认本地地址 (server URL defaults to persisted value, otherwise localhost)
const serverUrl = ref(api.getStoredServer() || '')
const username = ref('')
const password = ref('')
const logging = ref(false)           // 是否正在登录中 (whether login is in progress)
const loginError = ref('')           // 登录错误提示 (login error message)

// ── 定时器 & SSE 引用 ── (Timer & SSE references)
// statusTimer：15 秒状态轮询的定时器句柄 (handler for the 15s status polling timer)
let statusTimer = null
// unsubSSE：SSE 回调的注销函数（由 api.onSSE 返回），退出时调用以解除订阅 (unsubscribe fn returned by api.onSSE; called on logout)
let unsubSSE = null

/** 弹出 Toast 消息，3 秒后自动消失 (Show a Toast message that auto-dismisses after 3s) */
// @param {string} msg  - 消息文本内容 (message text content)
// @param {string} type - 提示类型：info（默认）/ success / warning / err，决定样式 (type: info(default)/success/warning/err, decides styling)
function addToast(msg, type = 'info') {
  toasts.value.push({ msg, type })
  // 3 秒后从队列移除最旧的消息 (remove the oldest toast from the queue after 3s)
  setTimeout(() => { toasts.value.shift() }, 3000)
}

/** 测试通知功能 (Test the notification feature) */
// 说明：通过原生桥（APK）或浏览器通知（桌面）发送系统通知，并弹出 Toast 提示结果
// (Fire a system notification via the native bridge (APK) or browser API (desktop); always show a Toast with the outcome)
function testNotify() {
  const sent = sendNotify('量仔期货', '通知测试成功')
  addToast('通知测试' + (sent ? '已发送' : (isNative() ? '（请检查系统通知权限）' : '（通知未授权）')), 'info')
}

/** 切换做空开关，失败时回滚 UI 状态 (Toggle short-selling; roll back UI state on failure) */
// 说明：切换开关本质是调用后端接口持久化开关状态；
//       失败时 v-model 已将 UI 开关值改变，这里需回滚到原值并给出错误提示
// (Persists the toggle via the backend API; on failure v-model already flipped the UI, so revert and show an error)
async function onShortToggle() {
  try {
    // 调用后端接口切换做空开关 (call backend API to toggle short-selling)
    const res = await api.toggleShort(shortEnabled.value)
    addToast(res.short_enabled ? '做空已开启' : '做空已关闭', 'info')
  } catch (_) {
    // 失败时回滚开关状态并提示 (on failure, roll back the toggle and notify)
    shortEnabled.value = !shortEnabled.value
    addToast('做空开关切换失败', 'err')
  }
}

/** 检查本地 token 是否存在，恢复登录态 (Check local token and restore the logged-in state) */
// 说明：组件挂载时调用；本地存在 token 则直接进入主界面并同步服务器地址，
//       否则停留在登录页，由用户手动登录。
// (Called on mount; if a token exists, enter the main UI and sync the server URL, otherwise stay on login)
async function checkAuth() {
  if (api.isLoggedIn()) {
    // 本地已有登录态则直接恢复界面 (restore the UI directly if already logged in locally)
    loggedIn.value = true
    account.value = api.getAccount()
    api.setStoredServer(serverUrl.value)
    // 静默刷新当前用户角色/权限位（页面刷新后兜底，失败不影响主界面）
    // Silently refresh role/perms after a page reload (best-effort; failure keeps the UI usable)
    try { await api.refreshMe() } catch (_) {}
    applyRoleGates()
    return true
  }
  loggedIn.value = false
  applyRoleGates()
  return false
}

/** 执行登录：提交凭据，成功后启动轮询 (Perform login: submit credentials, start polling on success) */
// 说明：登录成功后持久化服务器地址、启动 15 秒轮询 + SSE 连接，
//       并顺带请求浏览器通知权限；失败时把后端错误信息展示在登录页。
// (On success: persist server URL, start 15s polling + SSE connection, request notification permission; on failure show the error)
async function handleLogin() {
  logging.value = true
  loginError.value = ''
  // 先把服务器地址持久化，后续所有请求都基于该地址拼接 (persist the server URL first; all later requests use it)
  api.setStoredServer(serverUrl.value)
  try {
    // 提交登录凭据到后端 (submit login credentials to the backend)
    await api.login(username.value, password.value)
    account.value = api.getAccount()
    loggedIn.value = true
    // 登录成功后按新账号角色/权限位刷新侧栏权限门禁（换账号后管理员 tab 正确显隐）
    // After login, recompute the sidebar gates from the new account's role/perms (admin tabs show/hide correctly)
    applyRoleGates()
    // 登录成功后启动轮询，并顺带请求通知权限 (start polling after login and request notification permission)
    startPolling()
    addToast('登录成功', 'success')
    // 请求通知权限（APK 内走原生桥无需浏览器权限；桌面浏览器主动弹窗申请）
    // (Request notification permission; no browser permission needed on the native bridge)
    requestPermission()
  } catch (e) {
    loginError.value = e.message || '登录失败'
  } finally {
    logging.value = false
  }
}

/** 退出登录：清除数据并停止轮询 (Logout: clear data and stop polling) */
// 说明：清除本地认证信息、停止轮询与 SSE 连接、关闭移动端菜单，
//       并跳转回根路由（此时因 loggedIn 为 false 显示登录页）。
// (Clears local auth, stops polling and the SSE connection, closes the mobile menu, then routes to "/")
function logout() {
  // 清除认证并停止后台任务 (clear auth and stop background tasks)
  api.clearAuth()
  stopPolling()
  loggedIn.value = false
  menuOpen.value = false
  // 退出后清空侧栏权限门禁（避免下次登录其他账号时残留管理员 tab）
  // Clear the sidebar gates on logout so a later login as another account never shows stale admin tabs
  applyRoleGates()
  router.push('/')
}

/** 刷新服务端状态、信号数、提醒数和做空状态 (Refresh server status, signal count, alert count and short toggle) */
// 说明：三个接口各自 try/catch 独立执行，互不阻塞；
//       任一失败只影响对应状态（如状态接口失败则将服务标记为离线）。
// (Three API calls each run in their own try/catch so a failure of one does not block the others)
async function refreshStatus() {
  try {
    // 拉取服务状态与信号数 (fetch server status and signal count)
    const st = await api.fetchStatus()
    serverOnline.value = true
    signalCount.value = st.signal_count || 0
    inTradeTime.value = st.in_trade_time
  } catch (_) { serverOnline.value = false }
  try {
    // 拉取未读提醒数 (fetch unread alert count)
    const alerts = await api.fetchAlerts()
    alertCount.value = alerts?.length || 0
  } catch (_) {}
  try {
    // 拉取做空开关状态 (fetch short-selling toggle state)
    const ss = await api.fetchShortStatus()
    shortEnabled.value = ss.short_enabled || false
  } catch (_) {}
}

/** SSE 消息处理器：新交易信号时弹浏览器通知 + Toast 并刷新状态 (SSE handler: notify + refresh on new trading signals) */
// 说明：识别 scan 事件里的 bull（做多）与 bear（做空）信号数量，
//       有交易信号时触发系统通知并弹 Toast；同时刷新状态栏与消息中心。
// (Reads bull(long) and bear(short) signal counts from "scan" events; shows system notification + Toast, then refreshes status)
function handleSSE(msg) {
  if (msg && msg.type === 'scan') {
    const bull = parseInt(msg.bull || '0', 10)
    const bear = parseInt(msg.bear || '0', 10)
    if (bull > 0 || bear > 0) {
      const parts = []
      if (bull > 0) parts.push('做多 ' + bull + ' 条')
      if (bear > 0) parts.push('做空 ' + bear + ' 条')
      const text = '新交易信号: ' + parts.join('、') + (msg.time ? ' (' + msg.time + ')' : '')
      addToast(text, 'warning')
      notifyTradeSignal(text)
    }
    refreshStatus()
    return
  }
  if (msg && msg.type === 'message' && msg.item) {
    // 后端推送的关键消息（D2）：止盈/止损/清仓/交易信号等，刷新消息中心并对关键级别弹系统通知
    const level = msg.item.level || ''
    const critical = level.indexOf('止盈') >= 0 || level.indexOf('止损') >= 0 || level.indexOf('清仓') >= 0 || level.indexOf('交易信号') >= 0 || level.indexOf('买入') >= 0
    if (critical) notifyCriticalMessage(msg.item)
    refreshStatus()
    return
  }
  if (msg.signal) {
    // 新信号到来时弹 Toast 并刷新状态栏 (show a Toast and refresh status when a new signal arrives)
    addToast('新信号: ' + (msg.signal.code || ''), 'warning')
    refreshStatus()
  }
}

/** 发送交易信号系统通知（APK 走原生桥，桌面需已授权浏览器通知；按 scan 批次限流） (Send a trading-signal system notification via native bridge or browser; throttled per scan batch) */
function notifyTradeSignal(text) {
  // 按 "scan" 维度限流：同一轮信号批次窗口内只弹一次，避免每轮刷新反复提醒
  notifyThrottled('scan', '量仔期货 交易信号', text)
}

/** 发送关键消息系统通知（止盈/止损/清仓等），按 "code@level" 限流 (Send a critical message system notification, throttled per code@level) */
// 说明：后端经 SSE 推送的 message 事件（D2）携带消息明细，这里对止盈/止损/清仓等
//       关键级别弹系统通知；按 code@level 限流避免同一标的同级别反复弹。
function notifyCriticalMessage(item) {
  if (!item) return
  const level = item.level || ''
  const code = item.code || ''
  const name = item.name || ''
  const title = level ? ('量仔期货 ' + level) : '量仔期货 提醒'
  const body = (code ? code + ' ' : '') + (name || '') + (item.title || item.body || '')
  notifyThrottled(code + '@' + level, title, body)
}

// Android 原生端申请通知权限后的回调（MainActivity 调用；原生桥不依赖浏览器权限，故为空操作）
// (Callback invoked by MainActivity after requesting POST_NOTIFICATIONS; the native bridge doesn't need browser permission, so it's a no-op)
window.onNotifyPermissionChange = function () {}

/** 启动定时轮询和 SSE 连接 (Start the polling timer and SSE connection) */
// 说明：先立即刷新一次状态，随后每 15 秒轮询一次；
//       同时建立与后端的 SSE 长连接，并注册新信号的消息回调。
// (Refreshes immediately, then polls every 15s; opens an SSE connection and registers the signal callback)
function startPolling() {
  // 立即刷新一次，随后每 15 秒轮询 (refresh right away, then poll every 15s)
  refreshStatus()
  statusTimer = setInterval(refreshStatus, 15000)
  // 订阅后端 SSE 推送 (subscribe to backend SSE push events)
  api.connectSSE()
  unsubSSE = api.onSSE(handleSSE)
}

/** 停止定时轮询并断开 SSE (Stop the polling timer and disconnect SSE) */
// 说明：清除轮询定时器、断开 SSE 连接并注销回调；
//       在退出登录与组件卸载时调用，防止定时器与长连接泄漏。
// (Clears the timer, disconnects SSE and unsubscribes the callback to prevent leaks; called on logout & unmount)
function stopPolling() {
  // 清除轮询定时器并断开 SSE 连接 (clear the timer and disconnect the SSE connection)
  if (statusTimer) { clearInterval(statusTimer); statusTimer = null }
  api.disconnectSSE()
  if (unsubSSE) { unsubSSE(); unsubSSE = null }
}

/** 挂载时检查登录态，已登录则开始轮询 (On mount, check auth state and start polling if logged in) */
// 生命周期：组件挂载完成后先恢复登录态，成功则启动后台轮询与 SSE (lifecycle: restore auth first, then start polling & SSE)
onMounted(async () => {
  // 恢复登录态，成功则启动后台任务 (restore auth; if ok, start background tasks)
  const ok = await checkAuth()
  if (ok) startPolling()
  // 监听"登录过期"事件：后端 401 时（含 SSE 重连探测）自动退出回到登录页，终止对过期 token 的无限重连
  // Listen for the auth-expired event: on a 401 (including SSE reconnect probing), log out automatically
  // to return to the login page and stop reconnecting with a dead token.
  window.addEventListener('auth:expired', onAuthExpired)
})
/** 卸载时停止所有后台任务 (Stop all background tasks on unmount) */
// 生命周期：组件卸载前停止轮询与 SSE，避免后台任务泄漏 (lifecycle: stop polling & SSE before unmount to avoid leaks)
onUnmounted(() => {
  window.removeEventListener('auth:expired', onAuthExpired)
  stopPolling()
})

/** 登录过期处理：复用退出登录逻辑，清除数据、停止后台任务并回到登录页 */
function onAuthExpired() {
  if (!loggedIn.value) return
  addToast('登录已过期，请重新登录', 'err')
  logout()
}
</script>

<style>
/* 全局样式：去除浏览器默认内外边距，统一盒模型 */
* { margin: 0; padding: 0; box-sizing: border-box; }
body {
  font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', 'PingFang SC', sans-serif;
  background: #0f0f23; color: #e0e0e0;
  -webkit-tap-highlight-color: transparent;
}

/* ====== Login ====== */
.app { display: flex; height: 100vh; width: 100vw; }
.login-page { align-items: center; justify-content: center; }
.login-box {
  background: #1a1a2e; padding: 40px 28px; border-radius: 12px; width: 90%; max-width: 380px;
}
.login-box h1 { font-size: 26px; margin-bottom: 4px; color: #FF4D4F; text-align: center; }
.login-box .subtitle { color: #888; margin-bottom: 24px; font-size: 13px; text-align: center; }
.form-group { margin-bottom: 14px; }
.form-group label { display: block; font-size: 12px; color: #999; margin-bottom: 5px; }
.form-group input {
  width: 100%; padding: 11px 12px; border-radius: 8px; border: 1px solid #333;
  background: #0f0f23; color: #e0e0e0; font-size: 15px; outline: none;
  -webkit-appearance: none;
}
.form-group input:focus { border-color: #FF4D4F; }
.btn-login {
  width: 100%; padding: 12px; border-radius: 8px; border: none;
  background: #FF4D4F; color: #fff; font-size: 16px; cursor: pointer; margin-top: 6px;
  -webkit-appearance: none;
}
.btn-login:disabled { opacity: 0.5; }
.login-error { color: #FF4D4F; font-size: 13px; margin-top: 12px; text-align: center; }

/* ====== Hamburger ====== */
.hamburger {
  display: none; position: fixed; top: 0; left: 0; z-index: 1001;
  width: 44px; height: 44px; padding: 12px 10px; cursor: pointer;
  flex-direction: column; justify-content: center; gap: 5px;
}
.hamburger span { display: block; height: 2px; background: #999; border-radius: 1px; transition: 0.2s; }
.hamburger span:nth-child(2) { width: 70%; }

/* ====== Sidebar ====== */
.sidebar-overlay {
  display: none; position: fixed; inset: 0; z-index: 998;
  background: rgba(0,0,0,0.5);
}
.sidebar {
  width: 200px; background: #1a1a2e; display: flex; flex-direction: column;
  border-right: 1px solid #2a2a3e; flex-shrink: 0;
}
.logo {
  padding: 20px 16px; font-size: 18px; font-weight: 700; color: #FF4D4F;
  border-bottom: 1px solid #2a2a3e;
}
.nav { flex: 1; padding: 8px 0; }
.nav-item {
  display: flex; align-items: center; gap: 8px; padding: 12px 16px;
  color: #999; text-decoration: none; font-size: 14px; position: relative;
  transition: all 0.2s;
}
.nav-item:hover { background: rgba(255,77,79,0.06); color: #e0e0e0; }
.nav-item.active { color: #FF4D4F; background: rgba(255,77,79,0.1); }
.nav-icon { font-size: 16px; }
.badge {
  position: absolute; right: 12px; background: #FF4D4F; color: #fff;
  font-size: 11px; min-width: 18px; height: 18px; border-radius: 9px;
  display: flex; align-items: center; justify-content: center;
}
.sidebar-footer {
  padding: 14px 16px; border-top: 1px solid #2a2a3e;
}
.server-status { font-size: 12px; color: #888; margin-bottom: 4px; }
.server-status.online { color: #4caf50; }
.account-name { font-size: 13px; color: #e0e0e0; }

/* ====== Main ====== */
.main { flex: 1; display: flex; flex-direction: column; overflow: hidden; min-width: 0; }
.topbar {
  height: 44px; display: flex; align-items: center; justify-content: space-between;
  padding: 0 14px; background: #1a1a2e; border-bottom: 1px solid #2a2a3e;
  flex-shrink: 0;
}
.trade-time { font-size: 12px; }
.topbar-right { display: flex; gap: 6px; }
.btn-notify, .btn-logout {
  padding: 5px 12px; border-radius: 6px; border: 1px solid #333;
  background: transparent; color: #999; font-size: 12px; cursor: pointer;
}
.btn-notify:hover, .btn-logout:hover { background: #2a2a3e; color: #e0e0e0; }
.short-toggle { display: flex; align-items: center; gap: 5px; cursor: pointer; user-select: none; }
.short-toggle input { display: none; }
.toggle-track {
  width: 32px; height: 18px; border-radius: 9px; background: #333;
  position: relative; transition: background 0.2s;
}
.short-toggle.active .toggle-track { background: #FF4D4F; }
.toggle-thumb {
  position: absolute; top: 2px; left: 2px;
  width: 14px; height: 14px; border-radius: 50%; background: #999;
  transition: all 0.2s;
}
.short-toggle.active .toggle-thumb { left: 16px; background: #fff; }
.toggle-label { font-size: 11px; color: #888; white-space: nowrap; }
.short-toggle.active .toggle-label { color: #FF4D4F; }
.content { flex: 1; overflow-y: auto; padding: 14px; }

/* ====== Toast ====== */
.toast-container { position: fixed; top: 50px; right: 12px; z-index: 9999; max-width: 90vw; }
.toast {
  padding: 10px 16px; border-radius: 6px; margin-bottom: 8px; font-size: 13px;
  animation: slideIn 0.3s; word-break: break-word;
}
.toast.info { background: #1a1a2e; border: 1px solid #333; color: #e0e0e0; }
.toast.warning { background: rgba(255,77,79,0.15); border: 1px solid #FF4D4F; color: #FF4D4F; }
.toast.success { background: rgba(76,175,80,0.15); border: 1px solid #4caf50; color: #4caf50; }
@keyframes slideIn { from { transform: translateX(100%); opacity: 0; } to { transform: translateX(0); opacity: 1; } }

/* ====== Mobile ====== */
@media (max-width: 768px) {
  .hamburger { display: flex; position: fixed; top: 36px; left: 0; z-index: 1001; }
  .sidebar-overlay { display: block; }
  .sidebar {
    position: fixed; top: 0; left: 0; bottom: 0; z-index: 999;
    transform: translateX(-100%); transition: transform 0.25s ease;
  }
  .sidebar.open { transform: translateX(0); }
  .main { position: relative; z-index: 0; clip-path: inset(0); }
  .topbar {
    position: fixed; top: 36px; left: 0; right: 0; z-index: 100;
    padding: 12px 14px 12px 52px; height: 44px; margin-top: 0;
    background: #1a1a2e; border-bottom: 1px solid #2a2a3e;
  }
  .content {
    padding: 8px; margin-top: 80px; padding-top: 8px;
    background: #0f0f23; overscroll-behavior: contain;
  }
  .toast-container { right: 8px; top: 50px; }
}

@media (min-width: 769px) {
  .hamburger { display: none; top: auto; }
  .sidebar-overlay { display: none; }
  .sidebar { position: relative; transform: none; }
}
</style>
