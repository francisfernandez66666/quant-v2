// ── API 封装层 index.js ──
// API wrapper layer index.js
// 提供与后端 REST 接口通信的所有方法，以及认证 / SSE / 会话管理
// Provides all methods for talking to the backend REST API, plus auth / SSE / session management
//
// 职责说明：
// Responsibilities:
// 1. 统一请求封装：自动拼接服务器地址、附加 JWT 认证头、超时中断、401 自动登出；
// 1. Unified request wrapper: auto-joins server URL, attaches JWT header, timeout abort, auto-logout on 401;
// 2. 登录态管理：token / 账号 / 服务器地址在 localStorage 的读写与清除；
// 2. Auth state: read/write/clear token, account and server URL in localStorage;
// 3. 业务接口：信号、状态、消息、持仓、板块热点、行情快照、个股评分、
//     IPO 日历、个股查询、资讯、自选股、交易动作、LLM 配置、战法参数、做空开关等；
// 3. Business endpoints: signals, status, alerts, holdings, sector hot, snapshot, evaluations,
//    IPO calendar, stock lookup, news, watchlist, trade actions, LLM config, strategy params, short toggle etc.;
// 4. SSE 长连接：订阅后端实时推送（如新信号），断线自动重连；
// 4. SSE long connection: subscribe to backend realtime pushes (e.g. new signals), auto-reconnect on drop;
// 5. 市场会话追踪：记录 session 期号，辅助判断非交易时段的首次加载。
// 5. Market session tracking: records session id to help decide first load in non-trading hours.
//
// 路径拼接原理：接口函数均传入相对路径（如 '/api/signals'），
// URL joining: every API function takes a relative path (e.g. '/api/signals'),
// 最终请求地址 = baseUrl()（用户配置的服务器地址）+ 相对路径，
// final request URL = baseUrl() (user-configured server) + relative path,
// 使前端代码与具体后端地址解耦，便于切换服务器。
// decoupling frontend code from a specific backend address so the server can be switched easily.

// 全局基础路径前缀（预留，当前为空串；实际前缀来自用户配置的服务器地址）
// Global base path prefix (reserved; currently empty since the real prefix comes from the user-configured server)
const BASE = ''

// localStorage 存储键名
// localStorage storage keys
// 统一带 'liangzai_' 前缀，避免键名与其他应用冲突
// Namespaced with 'liangzai_' prefix to avoid collisions with other apps
// STORAGE_KEY    存储 JWT 访问令牌
// STORAGE_KEY    holds the JWT access token
// STORAGE_SERVER 存储用户配置的后端服务器地址
// STORAGE_SERVER holds the user-configured backend server URL
// STORAGE_ACCOUNT 存储当前登录账号名
// STORAGE_ACCOUNT holds the currently logged in account name
const STORAGE_KEY = 'liangzai_token'
const STORAGE_SERVER = 'liangzai_server_url'
const STORAGE_ACCOUNT = 'liangzai_account'
const STORAGE_ROLE = 'liangzai_role'
const STORAGE_PERMS = 'liangzai_perms'

// 从 localStorage 读取服务器基础地址
// Reads the base server URL from localStorage
// 说明：所有请求均基于该地址拼接相对路径；未配置时返回空串，表示使用同源相对请求
// Note: every request joins relative paths onto this base; returns '' when unset, meaning same-origin relative requests
// 追加兜底：在移动端 WebView（https://appassets.androidplatform.net 同源内嵌）里，
// 若 localStorage 尚无 server_url（例如首次安装且 JS 初始化早于原生注入），
// 直接回退到默认服务器地址，避免请求打到 appassets 无效源而报 "Failed to fetch"。
// (Fallback for the mobile WebView: when running on the appassets origin with no stored
// server URL yet, use the default server so fetches don't hit the invalid appassets origin.)
function baseUrl() {
  const stored = localStorage.getItem(STORAGE_SERVER)
  if (stored) return stored
  if (typeof location !== 'undefined' && location.origin &&
      location.origin.indexOf('appassets.androidplatform.net') >= 0) {
    return 'https://quant-trading.top'
  }
  return ''
}

// 从 localStorage 读取 JWT 令牌
// Reads the JWT token from localStorage
// 说明：令牌由登录接口写入，后续每个请求都会携带该令牌完成鉴权
// Note: the token is written by the login endpoint; every later request carries it for authentication
function getToken() {
  return localStorage.getItem(STORAGE_KEY)
}

// 将登录成功后返回的 token、账号等信息持久化到 localStorage
// Persists the token / account returned after a successful login into localStorage
// @param {string} token      - JWT 访问令牌
// @param {string} token      - JWT access token
// @param {string} account    - 登录账号名（可空）
// @param {string} account    - logged-in account name (may be empty)
// @param {string} expiresAt  - 令牌过期时间（预留参数，当前未使用，用于将来做本地过期校验）
// @param {string} expiresAt  - token expiry time (reserved, currently unused, for future local expiry checks)
function storeAuth(token, account, expiresAt, role, perms) {
  localStorage.setItem(STORAGE_KEY, token)
  localStorage.setItem(STORAGE_ACCOUNT, account || '')
  localStorage.setItem(STORAGE_ROLE, role || 'user')
  localStorage.setItem(STORAGE_PERMS, JSON.stringify(perms || []))
}

/**
 * 清除本地存储的认证信息（退出登录时调用）
 * Clears locally stored auth info (called on logout)
 * 同时移除 token 与账号，使 isLoggedIn() 立即失效
 * Removes both token and account so isLoggedIn() immediately becomes false
 */
export function clearAuth() {
  localStorage.removeItem(STORAGE_KEY)
  localStorage.removeItem(STORAGE_ACCOUNT)
  localStorage.removeItem(STORAGE_ROLE)
  localStorage.removeItem(STORAGE_PERMS)
}

/**
 * 检查当前是否存在有效的登录令牌
 * Checks whether a login token currently exists
 * 说明：只判断 token 是否存在（非空），不校验其真实有效性；
 * Note: only checks the token's presence (non-empty), not its real validity;
 *       令牌是否过期由后端返回 401 时统一判定（见 request()）。
 *       actual expiry is judged when the backend returns 401 (see request()).
 * @returns {boolean} true 表示已登录
 * @returns {boolean} true if logged in
 */
export function isLoggedIn() {
  return !!getToken()
}

/**
 * 获取当前登录账号名
 * Returns the current logged-in account name
 * @returns {string} 账号名，未登录时返回空字符串
 * @returns {string} account name, empty string when not logged in
 */
export function getAccount() {
  return localStorage.getItem(STORAGE_ACCOUNT) || ''
}

/**
 * 获取当前登录用户角色（admin / user）
 * Returns the current logged-in user's role (admin / user)
 */
export function getRole() {
  return localStorage.getItem(STORAGE_ROLE) || 'user'
}

/**
 * 获取当前登录用户的权限位列表
 * Returns the current logged-in user's permission bits
 * @returns {string[]} 权限位数组
 */
export function getPerms() {
  try {
    return JSON.parse(localStorage.getItem(STORAGE_PERMS) || '[]')
  } catch (_) {
    return []
  }
}

/**
 * 当前用户是否为管理员
 * Whether the current user is an admin
 */
export function isAdmin() {
  return getRole() === 'admin'
}

/**
 * 当前用户是否拥有指定权限位（管理员隐式全部）
 * Whether the current user holds a permission bit (admin implies all)
 * @param {string} perm - 权限位名
 */
export function hasPerm(perm) {
  if (isAdmin()) return true
  return getPerms().indexOf(perm) >= 0
}

/**
 * 拉取并缓存当前登录用户信息（角色/权限位）
 * Fetches and caches the current user's profile (role / permission bits)
 * @returns {Promise<object>} { id, username, role, perms, enabled }
 */
export async function refreshMe() {
  const me = await request('/api/auth/me')
  localStorage.setItem(STORAGE_ROLE, me.role || 'user')
  localStorage.setItem(STORAGE_PERMS, JSON.stringify(me.perms || []))
  return me
}

/**
 * 获取持久化的服务器地址
 * Returns the persisted server URL
 * @returns {string} 服务器地址，未配置时返回空字符串
 * @returns {string} server URL, empty string when not configured
 */
export function getStoredServer() {
  return localStorage.getItem(STORAGE_SERVER) || ''
}

/**
 * 持久化保存服务器地址
 * Persists the server URL
 * 说明：写入 localStorage，供 baseUrl() 在拼接所有请求时读取
 * Note: written to localStorage; read by baseUrl() when building every request
 * @param {string} url - 服务器地址
 * @param {string} url - server URL
 */
export function setStoredServer(url) {
  localStorage.setItem(STORAGE_SERVER, url)
}

// ── 通用请求封装 ──
// ── Generic request wrapper ──

/** 请求超时时间（毫秒），防止慢接口把页面卡在空状态 */
/** Request timeout (ms) to keep slow endpoints from leaving the page stuck empty */
// 说明：超过该时长仍未得到响应，则通过 AbortController 中止请求并抛出“请求超时”
// Note: if no response within this window, the request is aborted via AbortController and "请求超时" (request timeout) is thrown
const REQUEST_TIMEOUT = 10000

/**
 * 统一的 HTTP 请求封装，自动附加认证头、处理 401 过期
 * Unified HTTP request wrapper that auto-attaches the auth header and handles 401 expiry
 * 原理：
 * How it works:
 *  - URL = baseUrl() + path，即用户配置的服务器地址拼接相对路径；
 *  - URL = baseUrl() + path, i.e. the user-configured server URL joined with the relative path;
 *  - 若本地存在 token，则自动附加 Authorization: Bearer <token> 请求头；
 *  - if a token exists locally, the Authorization: Bearer <token> header is attached automatically;
 *  - 使用 AbortController + setTimeout 实现超时中断，超时后抛“请求超时”；
 *  - an AbortController + setTimeout implements the timeout; "请求超时" (request timeout) is thrown on abort;
 *  - 响应为 401 时视为令牌过期，自动清除本地登录态并抛出“登录已过期”；
 *  - a 401 response means the token expired: local auth state is cleared and "登录已过期" (login expired) is thrown;
 *  - 默认以 JSON 格式发送 / 接收数据。
 *  - data is sent / received as JSON by default.
 * @param {string} path - API 路径（相对路径）
 * @param {string} path - API path (relative)
 * @param {object} [opts] - 可选参数 { method, data, headers }
 * @param {object} [opts] - optional args { method, data, headers }
 *   method:  HTTP 方法，默认 GET
 *   method:  HTTP method, GET by default
 *   data:    请求体对象，会被 JSON.stringify 序列化后作为 body
 *   data:    request body object, serialized by JSON.stringify as body
 *   headers: 附加请求头，与默认头合并（同名可覆盖默认值）
 *   headers: extra headers merged with defaults (same key overrides the default)
 * @returns {Promise<object>} 响应 JSON
 * @returns {Promise<object>} response JSON
 */
async function request(path, opts = {}) {
  // 拼接完整请求地址：服务器基础地址 + 相对路径
  // Build the full request URL: server base URL + relative path
  const url = baseUrl() + path
  // 默认 JSON 头，允许调用方通过 opts.headers 覆盖 / 追加
  // Default JSON header; callers may override / append via opts.headers
  const headers = { 'Content-Type': 'application/json', ...opts.headers }
  const token = getToken()
  // 已登录时附加 Bearer 令牌，供后端鉴权
  // Attach the Bearer token when logged in so the backend can authenticate
  if (token) headers['Authorization'] = 'Bearer ' + token

  // 设置请求超时（默认 REQUEST_TIMEOUT，可通过 opts.timeout 覆盖，如 LLM 咨询需要更长等待），超时后中止请求
  // Set a request timeout (default REQUEST_TIMEOUT, overridable via opts.timeout, e.g. LLM consult needs to wait longer); abort on timeout
  const ctrl = new AbortController()
  const timer = setTimeout(() => ctrl.abort(), opts.timeout || REQUEST_TIMEOUT)
  let res
  try {
    res = await fetch(url, {
      method: opts.method || 'GET',
      headers,
      body: opts.data ? JSON.stringify(opts.data) : undefined,
      signal: ctrl.signal,
    })
  } catch (e) {
    // 区分“超时中止”与真正的网络错误：超时抛出明确的中文提示
    // Distinguish a "timeout abort" from a real network error: throw a clear message on timeout
    if (e && e.name === 'AbortError') throw new Error('请求超时')
    throw e
  } finally {
    // 无论成功与否都清理定时器，避免计时器泄漏
    // Always clear the timer to avoid a leaked timer
    clearTimeout(timer)
  }

  // 401 表示令牌过期，清除本地登录态
  // 401 means the token expired; clear local auth state
  if (res.status === 401) {
    clearAuth()
    // 广播"登录过期"事件，让 App 层监听后退出登录回到登录页（配合 SSE 无限重连的兜底）
    if (typeof window !== 'undefined') {
      window.dispatchEvent(new Event('auth:expired'))
    }
    throw new Error('登录已过期')
  }
  // 非 2xx：尝试解析后端错误信息，解析失败则给出明确状态码提示，
  // Non-2xx: try to parse the backend error message; fall back to a clear status-code note if parsing fails,
  // 避免下游 res.json() 对 HTML 等非 JSON 响应抛出的隐晦异常。
  // avoiding cryptic errors from downstream res.json() on non-JSON responses such as HTML.
  if (!res.ok) {
    let msg = '请求失败 ' + res.status
    try {
      const e = await res.json()
      if (e && e.error) msg = e.error
    } catch (_) {}
    throw new Error(msg)
  }
  return res.json()
}

// ── 认证接口 ──
// ── Authentication ──

/**
 * 登录：提交用户名密码，存储返回的 token
 * Login: submit username/password, store the returned token
 * 说明：登录接口不使用统一 request() 封装，因为登录时尚未持有 token，
 * Note: login does not use the unified request() wrapper because no token is held yet,
 *       且需要单独处理非 2xx 响应的错误信息提取（后端返回 err 字段）。
 *       and it extracts the error message from non-2xx responses separately (backend returns an err field).
 * @param {string} username - 用户名
 * @param {string} username - username
 * @param {string} password - 密码
 * @param {string} password - password
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
    // 非 2xx 响应时提取后端错误信息并抛出
    // On non-2xx responses, extract the backend error message and throw
    const err = await res.json().catch(() => ({}))
    throw new Error(err.error || '登录失败')
  }
  const data = await res.json()
  // 登录成功后持久化 token 与账号
  // Persist token and account after a successful login
  storeAuth(data.token, data.account, data.expires_at, data.role, data.perms)
  return data
}

// ── 策略信号 ──
// ── Strategy signals ──

/** 获取所有策略信号列表 */
/** Fetch the full list of strategy signals */
// 对应后端 GET /api/signals，返回策略信号数组（含 code、action、score 等字段）
// Maps to backend GET /api/signals; returns an array of strategy signals (code, action, score, etc.)
export async function fetchSignals() {
  return request('/api/signals', { timeout: 20000 })
}

/** 获取个股 K 线数据 */
/** Fetch a stock's K-line data */
// 对应 GET /api/kline；code 必填，period 默认日线，count 默认 90，返回 [{ date, open, high, low, close, volume, amount }]
// Maps to GET /api/kline; code is required, period defaults to daily, count defaults to 90; returns [{ date, open, high, low, close, volume, amount }]
export async function fetchKline(code, period, count) {
  const p = period || '101'
  const c = count || 90
  return request('/api/kline?code=' + encodeURIComponent(code) + '&period=' + encodeURIComponent(p) + '&count=' + c)
}

/** 获取个股分时数据（分时价格 + 成交量 + MACD） */
/** Fetch a stock's intraday (分时) data: price line + volume + MACD */
// 对应 GET /api/minute；code 必填，scale 分钟数（默认 1），count 点数（默认 241）；
// Maps to GET /api/minute; code is required, scale defaults to 1 minute, count defaults to 241;
// 返回 { code, name, prev_close, points: [{ time, open, high, low, close, volume, amount, dif, dea, bar }] }
// returns { code, name, prev_close, points: [{ time, open, high, low, close, volume, amount, dif, dea, bar }] }
export async function fetchMinute(code, scale, count) {
  const s = scale || 1
  const c = count || 241
  return request('/api/minute?code=' + encodeURIComponent(code) + '&scale=' + encodeURIComponent(s) + '&count=' + c)
}

/** 获取个股盘口快照（买卖五档 + 派生因子） */
/** Fetch a stock's order-book snapshot (5 bid/ask levels + derived factors) */
// 对应 GET /api/depth/{code}；返回 { code, name, price, prev_close, time, source, bids, asks, levels, factors }
// Maps to GET /api/depth/{code}; returns { code, name, price, prev_close, time, source, bids, asks, levels, factors }
// bids/asks 下标 0 为最优档（买一/卖一），长度 levels（免费源填充前五档，其余零值）
// bids/asks index 0 is the best level (bid1/ask1), length is `levels` (free source fills five, rest zero)
export async function fetchDepth(code) {
  return request('/api/depth/' + encodeURIComponent(code), { timeout: 15000 })
}

// ── 系统状态 ──
// ── System status ──

/** 获取服务端运行状态（含扫描统计、运行时长等） */
/** Fetch the server runtime status (scan stats, uptime, etc.) */
// 对应 GET /api/status，返回 { signal_count, in_trade_time, ... }，
// Maps to GET /api/status; returns { signal_count, in_trade_time, ... },
// 顶部状态栏与 15 秒轮询均依赖该接口
// used by the top status bar and the 15s polling
export async function fetchStatus() {
  return request('/api/status')
}

// 获取仪表盘汇总数据（含按战法分组的胜率统计）
// 对应 GET /api/dashboard，返回 { report_stats: { by_strategy: {...} }, ... }；
// by_strategy 按战法聚合胜率/平均盈亏/盈亏比，用于绩效归因展示。
export async function fetchDashboard() {
  return request('/api/dashboard')
}

// 获取流程引擎子系统健康状况
// Fetch engine health status
// 对应 GET /api/engine_health，返回各子系统连通性
// Maps to GET /api/engine_health, returns connectivity of each subsystem
export async function fetchEngineHealth() {
  return request('/api/engine_health')
}

// ── 消息提醒 ──
// ── Alerts ──

/** 获取所有提醒/告警消息 */
/** Fetch all reminder/alert messages */
// 对应 GET /api/alerts，返回消息数组；消息中心据此展示，未读数用于导航角标
// Maps to GET /api/alerts; returns an array of messages shown by the message center; unread count feeds the nav badge
export async function fetchAlerts() {
  return request('/api/alerts')
}

/** 清空消息中心全部消息 */
/** Clear all messages in the message center */
// 对应 DELETE /api/alerts，一次性删除所有提醒
// Maps to DELETE /api/alerts; deletes every alert at once
export async function clearAlerts() {
  return request('/api/alerts', { method: 'DELETE' })
}

/** 手工删除单条消息 */
/** Manually delete a single message */
// 对应 DELETE /api/alerts/{id}，id 经 encodeURIComponent 编码后拼入路径，
// Maps to DELETE /api/alerts/{id}; id is encoded with encodeURIComponent before being joined into the path,
// 防止特殊字符破坏 URL 结构
// so special characters cannot break the URL structure
export async function deleteAlert(id) {
  return request('/api/alerts/' + encodeURIComponent(id), { method: 'DELETE' })
}

// ── 持仓管理 ──
// ── Holdings ──

/** 获取当前持仓列表及可用资金 */
/** Fetch the current holdings list and available cash */
// 对应 GET /api/holdings，返回 { holdings: [...], available_cash } 等
// Maps to GET /api/holdings; returns { holdings: [...], available_cash } etc.
export async function fetchHoldings() {
  return request('/api/holdings')
}

/** 模拟盘：总开关与绩效/信号质量统计 */
/** Paper trading: master switch plus performance/signal-quality stats */
// 对应 GET /api/paper/state，返回 { enabled, stats:{...} }
// Maps to GET /api/paper/state; returns { enabled, stats:{...} }
export async function fetchPaperState() {
  return request('/api/paper/state')
}

/** 模拟盘：当前持仓（含实时估值价/浮盈/信号价参照） */
/** Paper trading: open positions (with live mark price, floating P/L and signal-price reference) */
// 对应 GET /api/paper/positions
export async function fetchPaperPositions() {
  return request('/api/paper/positions')
}

/** 模拟盘：成交记录（最新在前） */
/** Paper trading: fill records (newest first) */
// 对应 GET /api/paper/trades
export async function fetchPaperTrades() {
  return request('/api/paper/trades')
}

/** 模拟盘：净值曲线 */
/** Paper trading: equity curve */
// 对应 GET /api/paper/equity
export async function fetchPaperEquity() {
  return request('/api/paper/equity')
}

/** 模拟盘：手动按实时价买入（信号页"模拟买入"按钮触发） */
/** Paper trading: manually buy one stock at the live price (signal-page "paper buy" button) */
// 对应 POST /api/paper/buy，data: { code, name, strategy, signal_price }
export async function buyPaperPosition(code, name, strategy, signalPrice) {
  return request('/api/paper/buy', { method: 'POST', data: { code, name, strategy, signal_price: signalPrice || 0 } })
}

/** 模拟盘：手动按实时价卖出持仓（清仓） */
/** Paper trading: manually sell a position at the live price */
// 对应 POST /api/paper/sell，data: { code }
export async function sellPaperPosition(code) {
  return request('/api/paper/sell', { method: 'POST', data: { code } })
}

/** 模拟盘：清盘重置（按最后估值价平仓，重置现金/成交/净值；initialCapital>0 时自定义初始资金，
 *  maxPositions>0 时自定义持仓上限，0=不设限） */
/** Paper trading: liquidate and reset (liquidate at last mark, reset cash/trades/equity; a positive
 *  initialCapital also customizes the starting capital; a positive maxPositions sets a position cap,
 *  0 = unlimited) */
// 对应 POST /api/paper/reset
export async function resetPaper(initialCapital, maxPositions) {
  const data = {}
  if (initialCapital > 0) data.initial_capital = initialCapital
  if (maxPositions > 0) data.max_positions = maxPositions
  return request('/api/paper/reset', { method: 'POST', data })
}

/** 更新持仓数据（含可用资金） */
/** Update holdings data (including available cash) */
// 对应 POST /api/holdings，data 为完整持仓快照，整体覆盖保存
// Maps to POST /api/holdings; data is a full holdings snapshot saved as an overwrite
export async function updateHoldings(data) {
  return request('/api/holdings', { method: 'POST', data })
}

/** 增量买入/加仓：追加一笔(价格,数量)，后端按加权平均重算成本与累计数量 */
/** Incremental buy / add: append a lot (price, quantity); backend recalculates weighted-average cost and total quantity */
// 对应 POST /api/holdings/{code}/add，返回 { holding: {...} }
// Maps to POST /api/holdings/{code}/add; returns { holding: {...} }
export async function addHoldingLot(code, price, quantity) {
  return request('/api/holdings/' + encodeURIComponent(code) + '/add', { method: 'POST', data: { price, quantity } })
}

/** 更新成本价：直接设置该持仓成本（批次明细重建为一条合成批次） */
/** Update the cost price: set the holding cost directly (lot details are rebuilt as one synthetic lot) */
// 对应 POST /api/holdings/{code}/cost，返回 { holding: {...} }
// Maps to POST /api/holdings/{code}/cost; returns { holding: {...} }
export async function setHoldingCost(code, price) {
  return request('/api/holdings/' + encodeURIComponent(code) + '/cost', { method: 'POST', data: { price } })
}

/** 清仓：按清仓价卖出该持仓，后端计算并记录盈亏 */
/** Close out: sell the holding at the close price; backend computes and records the P&L */
// 对应 POST /api/holdings/{code}/close，返回 { profit_pct, profit_amount, ... }
// Maps to POST /api/holdings/{code}/close; returns { profit_pct, profit_amount, ... }
export async function closeHolding(code, price) {
  return request('/api/holdings/' + encodeURIComponent(code) + '/close', { method: 'POST', data: { price } })
}

/** 减仓：按卖出价卖出部分数量（FIFO 扣减批次，重算加权成本） */
/** Trim position: sell a partial quantity at the sell price (FIFO lot deduction, recalculated weighted cost) */
// 对应 POST /api/holdings/{code}/sell，返回 { holding: {...} }；全部卖出时返回 { closed: true }
// Maps to POST /api/holdings/{code}/sell; returns { holding: {...} }, or { closed: true } when fully sold
export async function sellHoldingLot(code, price, quantity) {
  return request('/api/holdings/' + encodeURIComponent(code) + '/sell', { method: 'POST', data: { price, quantity } })
}

// ── 板块热点 ──
// ── Sector hotspots ──

/** 获取热门板块数据 */
/** Fetch hot sector data */
// 对应 GET /api/sector/hot，返回当前热门板块及其涨幅等
// Maps to GET /api/sector/hot; returns the current hot sectors and their gains etc.
export async function fetchSectorHot() {
  return request('/api/sector/hot')
}

/** 获取当日热点板块轮次记录（历史快照） */
/** Fetch today's hot sector rotation records (historical snapshots) */
// 对应 GET /api/sector/hot/records，返回各轮次板块快照，用于复盘
// Maps to GET /api/sector/hot/records; returns per-round sector snapshots for review
export async function fetchSectorHotRecords() {
  return request('/api/sector/hot/records')
}

// ── 行情快照 ──
// ── Market snapshot ──

/** 获取全市场行情快照 */
/** Fetch the whole-market quote snapshot */
// 对应 GET /api/snapshot，返回全市场标的的最新行情
// Maps to GET /api/snapshot; returns the latest quotes for all instruments
export async function fetchSnapshot() {
  return request('/api/snapshot')
}

/** 获取热门个股快照 */
/** Fetch the hot-stock snapshot */
// 对应 GET /api/snapshot/hot，返回热度较高的个股行情子集
// Maps to GET /api/snapshot/hot; returns a subset of quotes for high-turnover stocks
export async function fetchHotSnapshot() {
  return request('/api/snapshot/hot')
}

// ── 个股评分 ──
// ── Stock evaluations ──

/** 获取全市场个股多维度评分（N形/龙头/双凸/回头/动量） */
/** Fetch whole-market multi-dimension stock scores (N-shape / dragon / double-bump / dragon-return / momentum) */
// 对应 GET /api/evaluations，返回个股在各战法维度下的评分结果
// Maps to GET /api/evaluations; returns per-stock scores for each strategy dimension
export async function fetchEvaluations() {
  return request('/api/evaluations')
}

// ── IPO 日历 ──
// ── IPO calendar ──

// 本地缓存键：用于按天缓存 IPO 日历，减少不必要的后端请求
// Local cache key for caching the IPO calendar per day to reduce unnecessary backend requests
const IPO_CACHE_KEY = 'ipo_calendar_cache_v1'

/**
 * 获取 IPO 日历数据（按日缓存：同一天内首次调用才请求后端，其余直接读缓存）
 * Fetch IPO calendar data (cached per day: only the first call of a day hits the backend, later reads use the cache)
 * 原理：
 * How it works:
 *  - 以当天日期（YYYY-MM-DD，UTC 日期）作为缓存标识；
 *  - the current date (YYYY-MM-DD, UTC) is used as the cache key;
 *  - 命中缓存且数据结构合法则直接返回本地数据，避免重复请求；
 *  - a valid cached structure returns the local data directly, avoiding duplicate requests;
 *  - 未命中则请求 GET /api/ipo/calendar，成功后把当天日期与数据一并写回 localStorage。
 *  - otherwise it requests GET /api/ipo/calendar and writes back both the date and data to localStorage on success.
 */
export async function fetchIPOCalendar() {
  const today = new Date().toISOString().slice(0, 10)
  try {
    const raw = localStorage.getItem(IPO_CACHE_KEY)
    if (raw) {
      const d = JSON.parse(raw)
      // 命中当日缓存则直接返回，避免重复请求
      // Return the cached data when it matches today, avoiding a duplicate request
      if (d.date === today && Array.isArray(d.data)) return d.data
    }
  } catch (_) {}
  const data = await request('/api/ipo/calendar')
  try {
    // 拉取成功后按当天日期写入缓存
    // Write the cache under today's date after a successful fetch
    localStorage.setItem(IPO_CACHE_KEY, JSON.stringify({ date: today, data }))
  } catch (_) {}
  return data
}

// ── 个股查询 ──
// ── Stock lookup ──

/**
 * 根据代码查询个股信息（名称、现价等）
 * Look up stock info by code (name, current price, etc.)
 * 说明：code 经 encodeURIComponent 编码后作为查询参数拼入 URL，
 * Note: code is encoded with encodeURIComponent and appended as a query parameter,
 *       避免股票代码中的特殊字符干扰请求。
 *       so special characters in the code do not break the request.
 * @param {string} code - 股票代码
 * @param {string} code - the stock code
 */
export async function fetchStockLookup(code) {
  return request('/api/stock/lookup?code=' + encodeURIComponent(code))
}

// ── 资讯 ──
// ── News ──

/**
 * 获取新闻资讯
 * Fetch news
 * @param {boolean} [all] - 是否获取全部（含历史）资讯；true 时追加 ?all=true 查询参数
 * @param {boolean} [all] - whether to fetch all (including historical) news; appends ?all=true when true
 */
export async function fetchNews(all) {
  return request(all ? '/api/news?all=true' : '/api/news')
}

/**
 * 手动 LLM 补推：强制重新拉取最近新闻并重跑 Stage0+Stage2 分析。
 * Manual LLM re-push: force a refetch of recent news and rerun the Stage0+Stage2 analysis.
 * 用于早盘/盘中 LLM 上游抖动导致整批新闻未被分析时的补救。
 * A remedy for when upstream LLM flakiness during the morning/intraday caused a whole batch to be left unanalyzed.
 * 异步执行，返回 202 表示已触发。
 * Runs asynchronously; a 202 response means it has been triggered.
 */
export async function reanalyzeNews() {
  return request('/api/news/reanalyze', { method: 'POST' })
}

// ── 自选股 ──
// ── Watchlist ──

/** 获取自选股列表 */
/** Fetch the watchlist */
// 对应 GET /api/watchlist，返回自选股数组
// Maps to GET /api/watchlist; returns an array of watchlist stocks
export async function fetchWatchlist() {
  return request('/api/watchlist')
}

/** 添加自选股 */
/** Add a watchlist stock */
// 对应 POST /api/watchlist，请求体 { code } 传入股票代码
// Maps to POST /api/watchlist; the body { code } carries the stock code
export async function addWatchlist(code) {
  return request('/api/watchlist', { method: 'POST', data: { code } })
}

/** 移除自选股 */
/** Remove a watchlist stock */
// 对应 DELETE /api/watchlist，请求体 { code } 传入股票代码
// Maps to DELETE /api/watchlist; the body { code } carries the stock code
export async function removeWatchlist(code) {
  return request('/api/watchlist', { method: 'DELETE', data: { code } })
}

// ── 交易动作 ──
// ── Trade actions ──

/** 对某信号执行买入 / 忽略操作 */
/** Perform a buy / ignore action on a signal */
// 对应 POST /api/action，请求体 { code, action }；
// Maps to POST /api/action; the body is { code, action };
// action 取 'buy'（买入）或 'ignore'（忽略）
// action is 'buy' or 'ignore'
export async function actionSignal(code, action) {
  return request('/api/action', { method: 'POST', data: { code, action } })
}

// ── 市场时段追踪（非交易时段仅首次加载） ──
// ── Market session tracking (first load only in non-trading hours) ──
// 原理：后端按市场会话（session）推送数据，前端以模块级变量 _lastSession
// How it works: the backend pushes data per market session; the frontend keeps the last consumed
// 记录最近一次消费的会话期号。当会话号发生变化（进入新的一天或新交易时段）时，
// session number in the module-level _lastSession. When the session number changes (a new day or new trading session),
// 视为“新会话”，用于控制在非交易时段内只做一次数据加载，避免重复请求。
// it counts as a "new session" and limits non-trading hours to a single load, avoiding duplicate requests.

// 模块级会话期号缓存：-1 表示尚未记录任何会话
// Module-level cached session number: -1 means no session recorded yet
let _lastSession = -1

/** 获取上次记录的会话期号 */
/** Get the previously recorded session number */
export function getLastSession() { return _lastSession }

/** 设置当前会话期号 */
/** Set the current session number */
export function setLastSession(s) { _lastSession = s }

/** 判断当前会话是否为不同于上次的新会话 */
/** Whether the given session is new compared with the last one */
// 原理：与会话号不一致则为新会话，并同步更新记录；相同则返回 false
// How it works: a different session number is a new session and updates the record; identical numbers return false
export function isNewSession(session) {
  if (session === _lastSession) return false
  _lastSession = session
  return true
}

/** 判断某会话是否为交易时段（早盘=1，午盘=3） */
/** Whether a session is a trading session (morning=1, afternoon=3) */
// 说明：session 为后端约定的枚举值，1 表示早盘交易时段、3 表示午盘交易时段
// Note: session is a backend-defined enum; 1 = morning trading session, 3 = afternoon trading session
export function isTradingSession(session) {
  return session === 1 || session === 3 // SessionMorningTrade=1, SessionAfternoonTrade=3
}

// ── SSE 服务端推送事件 ──
// ── SSE server push events ──
// 原理：SSE（Server-Sent Events）是单向服务端推送协议，基于浏览器 EventSource 实现长连接。
// How it works: SSE (Server-Sent Events) is a one-way server-push protocol using the browser's EventSource for a long-lived connection.
// 与 WebSocket 不同，SSE 仅由服务端向客户端推送，连接成本低且自带重连，适合行情 / 信号类通知。
// Unlike WebSocket, SSE only pushes server-to-client with low connection cost and built-in reconnection, ideal for quotes / signals.
//  - 建立连接前先读取 token，并以查询参数 ?token=... 带给后端完成鉴权；
//  - the token is read before connecting and sent to the backend as a ?token=... query param for auth;
//  - 收到消息后按 JSON 解析，广播给所有已注册回调（sseCallbacks）；
//  - incoming messages are parsed as JSON and broadcast to all registered callbacks (sseCallbacks);
//  - 连接出错时主动 close 后延时 3 秒重连，避免依赖浏览器自带的指数退避重连。
//  - on error the connection is closed and reconnected after 3s, rather than relying on the browser's exponential backoff.

// 当前 SSE 连接对象（EventSource 实例），null 表示未连接
// The current SSE connection object (an EventSource instance); null means disconnected
let sse = null
// 已注册的 SSE 消息回调列表，新增信号等推送会依次通知所有回调
// List of registered SSE callbacks; pushes such as new signals notify each callback in turn
let sseCallbacks = []
// SSE 连续重连次数，用于退避重连与登录态失效探测（成功收到消息后重置为 0）
let sseRetry = 0

/**
 * 注册 SSE 消息回调，返回取消注册的函数
 * Register an SSE message callback; returns an unsubscribe function
 * @param {Function} fn - 消息处理回调，入参为解析后的消息对象
 * @param {Function} fn - the handler callback receiving the parsed message object
 * @returns {Function} unsubscribe - 调用后将该回调从列表中移除
 * @returns {Function} unsubscribe - removes this callback from the list when invoked
 */
export function onSSE(fn) {
  sseCallbacks.push(fn)
  return () => { sseCallbacks = sseCallbacks.filter(f => f !== fn) }
}

/** 建立与后端的 SSE 长连接，接收实时推送 */
/** Establish the SSE long connection to the backend to receive realtime pushes */
// 说明：
// Notes:
//  - 已有连接或未登录时直接返回（幂等操作，避免重复建连）；
//  - returns immediately if already connected or not logged in (idempotent, avoids duplicate connections);
//  - 连接 URL = baseUrl() + '/api/events?token=' + encodeURIComponent(token)；
//  - connection URL = baseUrl() + '/api/events?token=' + encodeURIComponent(token);
//  - 收到消息时解析 JSON 并依次调用 sseCallbacks 中的回调；
//  - received messages are parsed as JSON and dispatched to each callback in sseCallbacks;
//  - onerror 触发时关闭旧连接，3 秒后重新建立，实现手动重连。
//  - onerror closes the old connection and reconnects after 3 seconds (manual reconnect).
export function connectSSE() {
  if (sse) return
  const token = getToken()
  // 未登录时不建立连接
  // Do not connect when not logged in
  if (!token) return
  // 说明：浏览器 EventSource 会自动携带 Last-Event-ID 头（来自服务端 `id:` 行），
  //       服务端据此实现断线续传（见 handleFixSSE 读取 Last-Event-ID）。
  // Note: the browser EventSource automatically sends the Last-Event-ID header
  //       (from the server's `id:` line), enabling reconnect resume server-side.
  sse = new EventSource(baseUrl() + '/api/events?token=' + encodeURIComponent(token))
  sse.onmessage = (e) => {
    // 成功收到一条消息即重置重连计数（连接已恢复）
    sseRetry = 0
    try {
      const msg = JSON.parse(e.data)
      sseCallbacks.forEach(fn => fn(msg))
    } catch (_) {}
  }
  sse.onerror = () => {
    // 连接断开时先关闭，随后按退避重连
    // Close on disconnect, then reconnect with backoff
    disconnectSSE()
    sseRetry++
    // 退避延迟：3s 起，指数增长并封顶 30s，避免网络异常时无限快速重连风暴
    // Backoff delay: starts at 3s, grows exponentially and caps at 30s to avoid a reconnect storm
    const delay = Math.min(3000 * Math.pow(1.6, sseRetry - 1), 30000)
    // 连续重连多次仍失败时，探测一次登录态：若 token 已失效（401），
    // request() 内部会 clearAuth 并广播 auth:expired，App 层据此回到登录页，
    // 从而终止对过期 token 的无限重连。
    // After many consecutive failures, probe auth once; if the token is expired (401),
    // request() clears auth and dispatches auth:expired so the app returns to login,
    // stopping the infinite reconnect loop on a dead token.
    if (sseRetry === 5) {
      request('/api/status').catch(() => {})
    }
    setTimeout(connectSSE, delay)
  }
}

/** 断开 SSE 长连接 */
/** Disconnect the SSE long connection */
// 说明：关闭连接并将 sse 置空，使下次 connectSSE() 可以重新建立连接
// Note: closes the connection and nulls `sse` so the next connectSSE() can reconnect
export function disconnectSSE() {
  if (sse) { sse.close(); sse = null }
}

// ── LLM 配置 ──
// ── LLM config ──

/** 获取 LLM 配置 */
/** Fetch the LLM configuration */
// 对应 GET /api/config/llm，返回 { api_url, api_key, model } 等配置项
// Maps to GET /api/config/llm; returns { api_url, api_key, model } config items
export async function fetchLLMConfig() {
  return request('/api/config/llm')
}

/** 设置 LLM 配置（API URL / Key / 模型名） */
/** Set the LLM configuration (API URL / Key / model name) */
// 对应 POST /api/config/llm，cfg 为完整配置对象，后端持久化到 config.json
// Maps to POST /api/config/llm; cfg is the full config object, persisted to config.json by the backend
export async function setLLMConfig(cfg) {
  return request('/api/config/llm', { method: 'POST', data: cfg })
}

// ── 战法参数配置 ──
// ── Strategy parameter config ──

/** 获取四个战法参数（dragon/double_bump/n_shape/dragon_return） */
/** Fetch the four strategy parameter sets (dragon/double_bump/n_shape/dragon_return) */
// 对应 GET /api/config/strategy，返回各战法的阈值参数
// Maps to GET /api/config/strategy; returns each strategy's threshold parameters
export async function fetchStrategyConfig() {
  return request('/api/config/strategy')
}

/** 保存四个战法参数并持久化到 config.json */
/** Save the four strategy parameter sets and persist them to config.json */
// 对应 POST /api/config/strategy，cfg 为四个战法的阈值参数对象
// Maps to POST /api/config/strategy; cfg is the threshold parameter object for the four strategies
export async function setStrategyConfig(cfg) {
  return request('/api/config/strategy', { method: 'POST', data: cfg })
}

/** 获取 LLM 诊断调试数据 */
/** Fetch LLM diagnostic/debug data */
// 对应 GET /api/llm-debug，返回最近一次 LLM 决策的输入 / 输出，供 LLM 诊断页分析
// Maps to GET /api/llm-debug; returns the input / output of the latest LLM decision for the debug page
export async function fetchLLMDebug() {
  return request('/api/llm-debug')
}

/** 获取当日全量 LLM/Stage 轮次记录（固化到磁盘，供复盘） */
/** Fetch today's full LLM/Stage round records (persisted to disk, for review) */
// 对应 GET /api/stage-records，返回当日全部轮次快照，用于事后复盘
// Maps to GET /api/stage-records; returns all of today's round snapshots for later review
export async function fetchStageRecords() {
  return request('/api/stage-records')
}

/** 获取当日全量信号批次记录（固化到磁盘，供复盘） */
/** Fetch today's full signal batch records (persisted to disk, for review) */
// 对应 GET /api/signal-logs，返回当日各轮信号批次快照（做多/做空/提醒），用于信号日志弹窗
// Maps to GET /api/signal-logs; returns today's per-round signal batch snapshots (long/short/alerts) for the log modal
export async function fetchSignalLogs() {
  return request('/api/signal-logs')
}

// ── 做空开关 ──
// ── Shorting toggle ──

/** 查询当前做空状态 */
/** Query the current shorting status */
// 对应 GET /api/short/status，返回 { short_enabled } 布尔值
// Maps to GET /api/short/status; returns the boolean { short_enabled }
export async function fetchShortStatus() {
  return request('/api/short/status')
}

/** 切换做空开关 */
/** Toggle the shorting switch */
// 对应 POST /api/short/toggle，请求体 { enabled } 传入目标开关状态，后端持久化
// Maps to POST /api/short/toggle; the body { enabled } carries the target state, persisted by the backend
export async function toggleShort(enabled) {
  return request('/api/short/toggle', { method: 'POST', data: { enabled } })
}

// ── 资讯显示全部开关 ──
// ── "Show all news" toggle ──

/** 查询"资讯显示全部"开关状态 */
/** Query the "show all news" toggle state */
// 对应 GET /api/news/showall，返回 { news_show_all } 布尔值
// Maps to GET /api/news/showall; returns the boolean { news_show_all }
export async function fetchNewsShowAllStatus() {
  return request('/api/news/showall')
}

/** 切换"资讯显示全部"开关 */
/** Toggle the "show all news" switch */
// 对应 POST /api/news/showall，请求体 { enabled }；开启时弱档/中性资讯也出现在 /api/news
// Maps to POST /api/news/showall; body { enabled }; when enabled, weak/neutral news also appears in /api/news
export async function toggleNewsShowAll(enabled) {
  return request('/api/news/showall', { method: 'POST', data: { enabled } })
}

// ── 数据源健康状况 ──
// ── Data source health ──
// 对应 GET /api/data_source_health，返回各数据源健康探测结果
// Maps to GET /api/data_source_health
// 返回 { eastmoney: true|false, sina: true|false, tencent: true|false, ths: true|false }
// (由 DataCoordinator.HealthCheck 返回，包含东财/新浪/腾讯/同花顺四大数据源的健康探测结果)
export async function fetchDataSourceHealth() {
  return request('/api/data_source_health')
}

// 对应 GET /api/news_source_health，返回新闻源健康探测结果
// Maps to GET /api/news_source_health
// 返回 { cainanshe: true|false, kuaixun: true|false }
export async function fetchNewsSourceHealth() {
  return request('/api/news_source_health')
}

// ── 股票咨询（多轮对话）──
// ── Stock consultation (multi-turn chat) ──

/** 发送咨询消息，获取 LLM 多轮对话回复 */
/** Send a consultation message and get the LLM multi-turn reply */
// 对应 POST /api/consult，请求体 { message }；返回 { reply }
// Maps to POST /api/consult; body { message }; returns { reply }
// LLM 推理耗时较长，单独使用 120s 超时避免提前中止
// LLM inference takes a while, so a dedicated 120s timeout avoids premature aborts
export async function consultChat(message) {
  return request('/api/consult', { method: 'POST', data: { message }, timeout: 120000 })
}

/** 获取当日咨询对话历史 */
/** Fetch today's consultation chat history */
// 对应 GET /api/consult/history，返回咨询消息数组
// Maps to GET /api/consult/history; returns an array of consultation messages
export async function fetchConsultHistory() {
  return request('/api/consult/history')
}

/** Clear today's consultation chat history */
// 对应 DELETE /api/consult/history，清空当日咨询对话
// Maps to DELETE /api/consult/history; clears today's consultation messages
export async function clearConsultHistory() {
  return request('/api/consult/history', { method: 'DELETE' })
}

/** 获取专业模式开关状态 */
/** Fetch the pro-mode toggle state */
// 对应 GET /api/consult/pro-mode，返回 { enabled } 布尔值
// Maps to GET /api/consult/pro-mode; returns the boolean { enabled }
export async function fetchConsultProMode() {
  return request('/api/consult/pro-mode')
}

/** 切换专业模式开关 */
/** Toggle pro mode */
// 对应 PUT /api/consult/pro-mode，请求体 { enabled }；开启后咨询会注入全部实时行情，
// Maps to PUT /api/consult/pro-mode; body { enabled }; when enabled, consultations inject full realtime quotes,
// 盘中每 15 分钟限流一次，盘前盘后不限
// throttled to once every 15 minutes intraday, unlimited pre/post market
export async function setConsultProMode(enabled) {
  return request('/api/consult/pro-mode', { method: 'PUT', data: { enabled } })
}

// ── B5 研究候选 ──
// ── B5 research candidates ──

/** 获取研究处理进度（GET /api/research/progress） */
/** Fetch the research processing progress (GET /api/research/progress) */
// 返回 { stocks, ready_stocks, ready_pct, daily_rows, fin_rows, candidates, applied, proposed, db_attached }
// Returns { stocks, ready_stocks, ready_pct, daily_rows, fin_rows, candidates, applied, proposed, db_attached }
export async function fetchResearchProgress() {
  return request('/api/research/progress')
}

/** 获取全部因子元数据（GET /api/research/factors） */
/** Fetch factor metadata (GET /api/research/factors) */
// 返回 { factors: [{ id, name, cat, desc }, ...] }，供自动研究页把因子规则渲染成中文可读文案
// Returns { factors: [{ id, name, cat, desc }, ...] } so the auto-research page can render factor rules in Chinese
export async function fetchResearchFactors() {
  return request('/api/research/factors')
}

/** 获取研究候选列表（可选按状态过滤） */
/** Fetch the research candidate list (optionally filtered by status) */
// 对应 GET /api/research/candidates?status=...，返回 { candidates: [...] }
// Maps to GET /api/research/candidates?status=...; returns { candidates: [...] }
export async function fetchResearchCandidates(status) {
  const q = status ? '?status=' + encodeURIComponent(status) : ''
  return request('/api/research/candidates' + q)
}

/** 审批通过候选并应用权重（POST /api/research/candidates/{id}/approve） */
/** Approve a candidate and apply its weights (POST /api/research/candidates/{id}/approve) */
export async function approveResearchCandidate(id) {
  return request('/api/research/candidates/' + encodeURIComponent(id) + '/approve', { method: 'POST' })
}

/** 驳回候选（POST /api/research/candidates/{id}/reject） */
/** Reject a candidate (POST /api/research/candidates/{id}/reject) */
export async function rejectResearchCandidate(id) {
  return request('/api/research/candidates/' + encodeURIComponent(id) + '/reject', { method: 'POST' })
}

/** 对指定候选跑一次全量回测（POST /api/research/candidates/{id}/backtest，异步） */
/** Run a full backtest on a candidate (POST ..., async) */
export async function backtestResearchCandidate(id) {
  return request('/api/research/candidates/' + encodeURIComponent(id) + '/backtest', { method: 'POST' })
}

/** 查询回测任务状态（GET /api/research/backtest/{id}） */
/** Query a backtest job's status (GET /api/research/backtest/{id}) */
export async function fetchBacktestStatus(id) {
  return request('/api/research/backtest/' + encodeURIComponent(id))
}

// ── 战法库（已应用因子战法管理 + 效果监测）──
// Strategy library: applied factor-strategy management + effectiveness monitoring

/** 获取战法库（GET /api/research/library） */
/** Fetch the strategy library (GET /api/research/library) */
export async function fetchResearchLibrary() {
  return request('/api/research/library')
}

/** 启用/禁用战法库某条（POST /api/research/library/{id}/enable|disable） */
/** Enable/disable a library strategy (POST .../{id}/enable|disable) */
export async function setResearchLibraryEnabled(id, enabled) {
  return request('/api/research/library/' + encodeURIComponent(id) + (enabled ? '/enable' : '/disable'), { method: 'POST' })
}

/** 删除战法库某条（POST /api/research/library/{id}/delete） */
/** Delete a library strategy (POST /api/research/library/{id}/delete) */
export async function deleteResearchLibrary(id) {
  return request('/api/research/library/' + encodeURIComponent(id) + '/delete', { method: 'POST' })
}

/** 重命名战法库某条（POST /api/research/library/{id}/rename，body {name}） */
/** Rename a library strategy (POST /api/research/library/{id}/rename, body {name}) */
export async function renameResearchLibrary(id, name) {
  return request('/api/research/library/' + encodeURIComponent(id) + '/rename', { method: 'POST', data: { name } })
}

/** 查询全量回测全局开关（GET /api/research/backtest-toggle） */
/** Get the global full-backtest toggle (GET /api/research/backtest-toggle) */
export async function fetchBacktestToggle() {
  return request('/api/research/backtest-toggle')
}

/** 设置全量回测全局开关（POST /api/research/backtest-toggle，body {enabled}） */
/** Set the global full-backtest toggle (POST /api/research/backtest-toggle, body {enabled}) */
export async function setBacktestToggle(enabled) {
  return request('/api/research/backtest-toggle', { method: 'POST', data: { enabled } })
}

// ── 用户/账号管理（仅 admin）──
// ── User / account management (admin only) ──

/** 获取全部用户列表与权限位清单 */
/** Fetch the full user list and permission-bit catalog */
// 对应 GET /api/admin/users，返回 { users: [...], perms: [...] }；users 为公开视图（不含密码/令牌）
// Maps to GET /api/admin/users; returns { users: [...], perms: [...] }; users are public views (no password/token)
export async function fetchAdminUsers() {
  return request('/api/admin/users')
}

/** 创建用户（POST /api/admin/users） */
/** Create a user (POST /api/admin/users) */
export async function createAdminUser(data) {
  return request('/api/admin/users', { method: 'POST', data })
}

/** 设置用户角色（POST /api/admin/users/{id}/role） */
/** Set a user's role (POST /api/admin/users/{id}/role) */
export async function setAdminUserRole(id, role) {
  return request('/api/admin/users/' + encodeURIComponent(id) + '/role', { method: 'POST', data: { role } })
}

/** 整体覆盖用户权限位（POST /api/admin/users/{id}/perms） */
/** Replace a user's permission bits (POST /api/admin/users/{id}/perms) */
export async function setAdminUserPerms(id, perms) {
  return request('/api/admin/users/' + encodeURIComponent(id) + '/perms', { method: 'POST', data: { perms } })
}

/** 重置用户密码（POST /api/admin/users/{id}/password） */
/** Reset a user's password (POST /api/admin/users/{id}/password) */
export async function setAdminUserPassword(id, password) {
  return request('/api/admin/users/' + encodeURIComponent(id) + '/password', { method: 'POST', data: { password } })
}

/** 启用/禁用用户（POST /api/admin/users/{id}/enabled） */
/** Enable/disable a user (POST /api/admin/users/{id}/enabled) */
export async function setAdminUserEnabled(id, enabled) {
  return request('/api/admin/users/' + encodeURIComponent(id) + '/enabled', { method: 'POST', data: { enabled } })
}

/** 设置账号有效期（POST /api/admin/users/{id}/expiry，expires_days=0 表示永久） */
/** Set an account expiry (POST /api/admin/users/{id}/expiry; expires_days=0 means permanent) */
export async function setAdminUserExpiry(id, expiresDays) {
  return request('/api/admin/users/' + encodeURIComponent(id) + '/expiry', { method: 'POST', data: { expires_days: expiresDays } })
}

/** 删除用户（DELETE /api/admin/users/{id}，管理员不可删） */
/** Delete a user (DELETE /api/admin/users/{id}; the admin account cannot be deleted) */
export async function deleteAdminUser(id) {
  return request('/api/admin/users/' + encodeURIComponent(id), { method: 'DELETE' })
}

/** 读取指定账号战法参数（GET /api/admin/users/{id}/config/strategy） */
export async function fetchAdminStrategyConfig(id) {
  return request('/api/admin/users/' + encodeURIComponent(id) + '/config/strategy')
}

/** 保存指定账号战法参数（POST /api/admin/users/{id}/config/strategy） */
export async function setAdminStrategyConfig(id, cfg) {
  return request('/api/admin/users/' + encodeURIComponent(id) + '/config/strategy', { method: 'POST', data: cfg })
}

/** 读取指定账号 D1 规则（GET /api/admin/users/{id}/config/d1） */
export async function fetchAdminD1Config(id) {
  return request('/api/admin/users/' + encodeURIComponent(id) + '/config/d1')
}

/** 保存指定账号 D1 规则（POST /api/admin/users/{id}/config/d1） */
export async function setAdminD1Config(id, cfg) {
  return request('/api/admin/users/' + encodeURIComponent(id) + '/config/d1', { method: 'POST', data: cfg })
}

/** 读取指定账号做多/做空开关（GET /api/admin/users/{id}/config/longshort） */
export async function fetchAdminLongShortConfig(id) {
  return request('/api/admin/users/' + encodeURIComponent(id) + '/config/longshort')
}

/** 保存指定账号做多/做空开关（POST /api/admin/users/{id}/config/longshort） */
export async function setAdminLongShortConfig(id, cfg) {
  return request('/api/admin/users/' + encodeURIComponent(id) + '/config/longshort', { method: 'POST', data: cfg })
}

/** 读取指定账号 LLM 配置（GET /api/admin/users/{id}/config/llm） */
export async function fetchAdminLLMConfig(id) {
  return request('/api/admin/users/' + encodeURIComponent(id) + '/config/llm')
}

/** 保存指定账号 LLM 配置（POST /api/admin/users/{id}/config/llm） */
export async function setAdminLLMConfig(id, cfg) {
  return request('/api/admin/users/' + encodeURIComponent(id) + '/config/llm', { method: 'POST', data: cfg })
}


