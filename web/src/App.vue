<!--
  根组件 App.vue
  主布局：侧边栏导航 + 顶部栏 + 内容区（router-view）
  未登录时显示登录页

  文件职责：
  1. 已登录：渲染应用主界面（侧边栏导航 + 顶部栏 + 内容区）；
  2. 未登录：渲染登录页（服务器地址 / 账号 / 密码表单）；
  3. 应用生命周期管理：登录态恢复、15 秒状态轮询、SSE 实时推送订阅、
     做空开关、通知测试、Toast 提示等全局逻辑都集中在本组件。
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
        <router-link to="/settings" class="nav-item" active-class="active" @click="menuOpen = false">
          <span class="nav-icon">⚙</span> 设置
        </router-link>
        <router-link to="/llm-debug" class="nav-item" active-class="active" @click="menuOpen = false">
          <span class="nav-icon">🧠</span> LLM诊断
        </router-link>
        <router-link to="/consult" class="nav-item" active-class="active" @click="menuOpen = false">
          <span class="nav-icon">🎯</span> 股票咨询
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
        <input v-model="serverUrl" placeholder="http://127.0.0.1:8080" />
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
      <!-- 登录按钮：登录中显示“登录中...”并禁用，防止重复提交 -->
      <button class="btn-login" @click="handleLogin" :disabled="logging">
        {{ logging ? '登录中...' : '登录' }}
      </button>
      <!-- 登录错误提示：失败时展示后端返回的错误信息 -->
      <p class="login-error" v-if="loginError">{{ loginError }}</p>
    </div>
  </div>
</template>

<script setup>
// ── 依赖导入 ──
// ref 定义响应式数据；onMounted / onUnmounted 注册组件生命周期钩子
import { ref, onMounted, onUnmounted } from 'vue'
// useRouter 获取路由实例，用于退出登录后编程式跳转
import { useRouter } from 'vue-router'
// 后端 API 方法统一挂载在 api 命名空间下
import * as api from './api/index.js'

// 路由实例：logout 时跳转回根路由（登录页）
const router = useRouter()

// ── 响应式状态 ──
const loggedIn = ref(false)          // 是否已登录
const account = ref('')              // 当前账号名
const serverOnline = ref(false)      // 后端服务是否在线
const inTradeTime = ref(null)        // 是否处于交易时段
const signalCount = ref(0)           // 未读信号数
const alertCount = ref(0)            // 未读提醒数
const toasts = ref([])               // Toast 消息队列
const menuOpen = ref(false)          // 移动端侧栏是否展开
const shortEnabled = ref(false)      // 做空开关状态

// ── 登录表单状态 ──
// 服务器地址初始值优先取本地持久化值，否则用默认本地地址
const serverUrl = ref(api.getStoredServer() || 'http://127.0.0.1:8080')
const username = ref('')
const password = ref('')
const logging = ref(false)           // 是否正在登录中
const loginError = ref('')           // 登录错误提示

// ── 定时器 & SSE 引用 ──
// statusTimer：15 秒状态轮询的定时器句柄
let statusTimer = null
// unsubSSE：SSE 回调的注销函数（由 api.onSSE 返回），退出时调用以解除订阅
let unsubSSE = null

/** 弹出 Toast 消息，3 秒后自动消失 */
// @param {string} msg  - 消息文本内容
// @param {string} type - 提示类型：info（默认）/ success / warning / err，决定样式
function addToast(msg, type = 'info') {
  toasts.value.push({ msg, type })
  // 3 秒后从队列移除最旧的消息
  setTimeout(() => { toasts.value.shift() }, 3000)
}

/** 测试浏览器通知功能 */
// 说明：已授权通知权限时直接弹出系统通知；无论是否授权都弹出 Toast 提示结果
async function testNotify() {
  if ('Notification' in window && Notification.permission === 'granted') {
    new Notification('量仔期货', { body: '通知测试成功', icon: '' })
  }
  addToast('通知测试' + (Notification.permission === 'granted' ? '已发送' : '（通知未授权）'), 'info')
}

/** 切换做空开关，失败时回滚 UI 状态 */
// 说明：切换开关本质是调用后端接口持久化开关状态；
//       失败时 v-model 已将 UI 开关值改变，这里需回滚到原值并给出错误提示
async function onShortToggle() {
  try {
    // 调用后端接口切换做空开关
    const res = await api.toggleShort(shortEnabled.value)
    addToast(res.short_enabled ? '做空已开启' : '做空已关闭', 'info')
  } catch (_) {
    // 失败时回滚开关状态并提示
    shortEnabled.value = !shortEnabled.value
    addToast('做空开关切换失败', 'err')
  }
}

/** 检查本地 token 是否存在，恢复登录态 */
// 说明：组件挂载时调用；本地存在 token 则直接进入主界面并同步服务器地址，
//       否则停留在登录页，由用户手动登录。
async function checkAuth() {
  if (api.isLoggedIn()) {
    // 本地已有登录态则直接恢复界面
    loggedIn.value = true
    account.value = api.getAccount()
    api.setStoredServer(serverUrl.value)
    return true
  }
  loggedIn.value = false
  return false
}

/** 执行登录：提交凭据，成功后启动轮询 */
// 说明：登录成功后持久化服务器地址、启动 15 秒轮询 + SSE 连接，
//       并顺带请求浏览器通知权限；失败时把后端错误信息展示在登录页。
async function handleLogin() {
  logging.value = true
  loginError.value = ''
  // 先把服务器地址持久化，后续所有请求都基于该地址拼接
  api.setStoredServer(serverUrl.value)
  try {
    // 提交登录凭据到后端
    await api.login(username.value, password.value)
    account.value = api.getAccount()
    loggedIn.value = true
    // 登录成功后启动轮询，并顺带请求通知权限
    startPolling()
    addToast('登录成功', 'success')
    // 浏览器通知权限为“默认”时主动申请，便于新信号及时提醒
    if ('Notification' in window && Notification.permission === 'default') {
      Notification.requestPermission()
    }
  } catch (e) {
    loginError.value = e.message || '登录失败'
  } finally {
    logging.value = false
  }
}

/** 退出登录：清除数据并停止轮询 */
// 说明：清除本地认证信息、停止轮询与 SSE 连接、关闭移动端菜单，
//       并跳转回根路由（此时因 loggedIn 为 false 显示登录页）。
function logout() {
  // 清除认证并停止后台任务
  api.clearAuth()
  stopPolling()
  loggedIn.value = false
  menuOpen.value = false
  router.push('/')
}

/** 刷新服务端状态、信号数、提醒数和做空状态 */
// 说明：三个接口各自 try/catch 独立执行，互不阻塞；
//       任一失败只影响对应状态（如状态接口失败则将服务标记为离线）。
async function refreshStatus() {
  try {
    // 拉取服务状态与信号数
    const st = await api.fetchStatus()
    serverOnline.value = true
    signalCount.value = st.signal_count || 0
    inTradeTime.value = st.in_trade_time
  } catch (_) { serverOnline.value = false }
  try {
    // 拉取未读提醒数
    const alerts = await api.fetchAlerts()
    alertCount.value = alerts?.length || 0
  } catch (_) {}
  try {
    // 拉取做空开关状态
    const ss = await api.fetchShortStatus()
    shortEnabled.value = ss.short_enabled || false
  } catch (_) {}
}

/** SSE 消息处理器：新信号时弹 Toast 并刷新状态 */
// 说明：作为回调注册到 api.onSSE()；收到含 signal 字段的推送时，
//       弹出新信号提示并触发一次状态刷新，保证界面实时同步。
function handleSSE(msg) {
  if (msg.signal) {
    // 新信号到来时弹 Toast 并刷新状态栏
    addToast('新信号: ' + (msg.signal.code || ''), 'warning')
    refreshStatus()
  }
}

/** 启动定时轮询和 SSE 连接 */
// 说明：先立即刷新一次状态，随后每 15 秒轮询一次；
//       同时建立与后端的 SSE 长连接，并注册新信号的消息回调。
function startPolling() {
  // 立即刷新一次，随后每 15 秒轮询
  refreshStatus()
  statusTimer = setInterval(refreshStatus, 15000)
  // 订阅后端 SSE 推送
  api.connectSSE()
  unsubSSE = api.onSSE(handleSSE)
}

/** 停止定时轮询并断开 SSE */
// 说明：清除轮询定时器、断开 SSE 连接并注销回调；
//       在退出登录与组件卸载时调用，防止定时器与长连接泄漏。
function stopPolling() {
  // 清除轮询定时器并断开 SSE 连接
  if (statusTimer) { clearInterval(statusTimer); statusTimer = null }
  api.disconnectSSE()
  if (unsubSSE) { unsubSSE(); unsubSSE = null }
}

/** 挂载时检查登录态，已登录则开始轮询 */
// 生命周期：组件挂载完成后先恢复登录态，成功则启动后台轮询与 SSE
onMounted(async () => {
  // 恢复登录态，成功则启动后台任务
  const ok = await checkAuth()
  if (ok) startPolling()
})
/** 卸载时停止所有后台任务 */
// 生命周期：组件卸载前停止轮询与 SSE，避免后台任务泄漏
onUnmounted(stopPolling)
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
