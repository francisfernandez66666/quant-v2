// ── API 封装层 index.js ──
// 提供与后端 REST 接口通信的所有方法，以及认证 / SSE / 会话管理

const BASE = ''

// localStorage 存储键名
const STORAGE_KEY = 'liangzai_token'
const STORAGE_SERVER = 'liangzai_server_url'
const STORAGE_ACCOUNT = 'liangzai_account'

// 从 localStorage 读取服务器基础地址
function baseUrl() {
  return localStorage.getItem(STORAGE_SERVER) || ''
}

// 从 localStorage 读取 JWT 令牌
function getToken() {
  return localStorage.getItem(STORAGE_KEY)
}

// 将登录成功后返回的 token、账号等信息持久化到 localStorage
function storeAuth(token, account, expiresAt) {
  localStorage.setItem(STORAGE_KEY, token)
  localStorage.setItem(STORAGE_ACCOUNT, account || '')
}

/**
 * 清除本地存储的认证信息（退出登录时调用）
 */
export function clearAuth() {
  localStorage.removeItem(STORAGE_KEY)
  localStorage.removeItem(STORAGE_ACCOUNT)
}

/**
 * 检查当前是否存在有效的登录令牌
 * @returns {boolean}
 */
export function isLoggedIn() {
  return !!getToken()
}

/**
 * 获取当前登录账号名
 * @returns {string}
 */
export function getAccount() {
  return localStorage.getItem(STORAGE_ACCOUNT) || ''
}

/**
 * 获取持久化的服务器地址
 * @returns {string}
 */
export function getStoredServer() {
  return localStorage.getItem(STORAGE_SERVER) || ''
}

/**
 * 持久化保存服务器地址
 * @param {string} url - 服务器地址
 */
export function setStoredServer(url) {
  localStorage.setItem(STORAGE_SERVER, url)
}

// ── 通用请求封装 ──

/** 请求超时时间（毫秒），防止慢接口把页面卡在空状态 */
const REQUEST_TIMEOUT = 10000

/**
 * 统一的 HTTP 请求封装，自动附加认证头、处理 401 过期
 * @param {string} path - API 路径（相对路径）
 * @param {object} [opts] - 可选参数 { method, data, headers }
 * @returns {Promise<object>} 响应 JSON
 */
async function request(path, opts = {}) {
  const url = baseUrl() + path
  const headers = { 'Content-Type': 'application/json', ...opts.headers }
  const token = getToken()
  if (token) headers['Authorization'] = 'Bearer ' + token

  const ctrl = new AbortController()
  const timer = setTimeout(() => ctrl.abort(), REQUEST_TIMEOUT)
  let res
  try {
    res = await fetch(url, {
      method: opts.method || 'GET',
      headers,
      body: opts.data ? JSON.stringify(opts.data) : undefined,
      signal: ctrl.signal,
    })
  } catch (e) {
    if (e && e.name === 'AbortError') throw new Error('请求超时')
    throw e
  } finally {
    clearTimeout(timer)
  }

  // 401 表示令牌过期，清除本地登录态
  if (res.status === 401) {
    clearAuth()
    throw new Error('登录已过期')
  }
  return res.json()
}

// ── 认证接口 ──

/**
 * 登录：提交用户名密码，存储返回的 token
 * @param {string} username
 * @param {string} password
 * @returns {Promise<object>} { token, account, expires_at }
 */
export async function login(username, password) {
  const url = baseUrl() + '/api/auth/login'
  const res = await fetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username, password }),
  })
  if (!res.ok) {
    const err = await res.json().catch(() => ({}))
    throw new Error(err.error || '登录失败')
  }
  const data = await res.json()
  storeAuth(data.token, data.account, data.expires_at)
  return data
}

// ── 策略信号 ──

/** 获取所有策略信号列表 */
export async function fetchSignals() {
  return request('/api/signals')
}

// ── 系统状态 ──

/** 获取服务端运行状态（含扫描统计、运行时长等） */
export async function fetchStatus() {
  return request('/api/status')
}

// ── 消息提醒 ──

/** 获取所有提醒/告警消息 */
export async function fetchAlerts() {
  return request('/api/alerts')
}

/** 清空消息中心全部消息 */
export async function clearAlerts() {
  return request('/api/alerts', { method: 'DELETE' })
}

/** 手工删除单条消息 */
export async function deleteAlert(id) {
  return request('/api/alerts/' + encodeURIComponent(id), { method: 'DELETE' })
}

// ── 持仓管理 ──

/** 获取当前持仓列表及可用资金 */
export async function fetchHoldings() {
  return request('/api/holdings')
}

/** 更新持仓数据（含可用资金） */
export async function updateHoldings(data) {
  return request('/api/holdings', { method: 'POST', data })
}

// ── 板块热点 ──

/** 获取热门板块数据 */
export async function fetchSectorHot() {
  return request('/api/sector/hot')
}

/** 获取当日热点板块轮次记录（历史快照） */
export async function fetchSectorHotRecords() {
  return request('/api/sector/hot/records')
}

// ── 行情快照 ──

/** 获取全市场行情快照 */
export async function fetchSnapshot() {
  return request('/api/snapshot')
}

/** 获取热门个股快照 */
export async function fetchHotSnapshot() {
  return request('/api/snapshot/hot')
}

// ── 个股评分 ──

/** 获取全市场个股多维度评分（N形/龙头/双凸/回头/动量） */
export async function fetchEvaluations() {
  return request('/api/evaluations')
}

// ── IPO 日历 ──

const IPO_CACHE_KEY = 'ipo_calendar_cache_v1'

/**
 * 获取 IPO 日历数据（按日缓存：同一天内首次调用才请求后端，其余直接读缓存）
 */
export async function fetchIPOCalendar() {
  const today = new Date().toISOString().slice(0, 10)
  try {
    const raw = localStorage.getItem(IPO_CACHE_KEY)
    if (raw) {
      const d = JSON.parse(raw)
      if (d.date === today && Array.isArray(d.data)) return d.data
    }
  } catch (_) {}
  const data = await request('/api/ipo/calendar')
  try {
    localStorage.setItem(IPO_CACHE_KEY, JSON.stringify({ date: today, data }))
  } catch (_) {}
  return data
}

// ── 个股查询 ──

/**
 * 根据代码查询个股信息（名称、现价等）
 * @param {string} code - 股票代码
 */
export async function fetchStockLookup(code) {
  return request('/api/stock/lookup?code=' + encodeURIComponent(code))
}

// ── 资讯 ──

/**
 * 获取新闻资讯
 * @param {boolean} [all] - 是否获取全部（含历史）资讯
 */
export async function fetchNews(all) {
  return request(all ? '/api/news?all=true' : '/api/news')
}

// ── 自选股 ──

/** 获取自选股列表 */
export async function fetchWatchlist() {
  return request('/api/watchlist')
}

/** 添加自选股 */
export async function addWatchlist(code) {
  return request('/api/watchlist', { method: 'POST', data: { code } })
}

/** 移除自选股 */
export async function removeWatchlist(code) {
  return request('/api/watchlist', { method: 'DELETE', data: { code } })
}

// ── 交易动作 ──

/** 对某信号执行买入 / 忽略操作 */
export async function actionSignal(code, action) {
  return request('/api/action', { method: 'POST', data: { code, action } })
}

// ── 市场时段追踪（非交易时段仅首次加载） ──

let _lastSession = -1

/** 获取上次记录的会话期号 */
export function getLastSession() { return _lastSession }

/** 设置当前会话期号 */
export function setLastSession(s) { _lastSession = s }

/** 判断当前会话是否为不同于上次的新会话 */
export function isNewSession(session) {
  if (session === _lastSession) return false
  _lastSession = session
  return true
}

/** 判断某会话是否为交易时段（早盘=1，午盘=3） */
export function isTradingSession(session) {
  return session === 1 || session === 3 // SessionMorningTrade=1, SessionAfternoonTrade=3
}

// ── SSE 服务端推送事件 ──

let sse = null
let sseCallbacks = []

/**
 * 注册 SSE 消息回调，返回取消注册的函数
 * @param {Function} fn - 消息处理回调
 * @returns {Function} unsubscribe
 */
export function onSSE(fn) {
  sseCallbacks.push(fn)
  return () => { sseCallbacks = sseCallbacks.filter(f => f !== fn) }
}

/** 建立与后端的 SSE 长连接，接收实时推送 */
export function connectSSE() {
  if (sse) return
  const token = getToken()
  if (!token) return
  sse = new EventSource(baseUrl() + '/api/events?token=' + encodeURIComponent(token))
  sse.onmessage = (e) => {
    try {
      const msg = JSON.parse(e.data)
      sseCallbacks.forEach(fn => fn(msg))
    } catch (_) {}
  }
  sse.onerror = () => {
    disconnectSSE()
    setTimeout(connectSSE, 3000)
  }
}

/** 断开 SSE 长连接 */
export function disconnectSSE() {
  if (sse) { sse.close(); sse = null }
}

// ── LLM 配置 ──

/** 获取 LLM 配置 */
export async function fetchLLMConfig() {
  return request('/api/config/llm')
}

/** 设置 LLM 配置（API URL / Key / 模型名） */
export async function setLLMConfig(cfg) {
  return request('/api/config/llm', { method: 'POST', data: cfg })
}

/** 获取 LLM 诊断调试数据 */
export async function fetchLLMDebug() {
  return request('/api/llm-debug')
}

/** 获取当日全量 LLM/Stage 轮次记录（固化到磁盘，供复盘） */
export async function fetchStageRecords() {
  return request('/api/stage-records')
}

// ── 做空开关 ──

/** 查询当前做空状态 */
export async function fetchShortStatus() {
  return request('/api/short/status')
}

/** 切换做空开关 */
export async function toggleShort(enabled) {
  return request('/api/short/toggle', { method: 'POST', data: { enabled } })
}
