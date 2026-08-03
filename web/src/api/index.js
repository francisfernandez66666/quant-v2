// ── API 封装层 index.js ──
// 提供与后端 REST 接口通信的所有方法，以及认证 / SSE / 会话管理
//
// 职责说明：
// 1. 统一请求封装：自动拼接服务器地址、附加 JWT 认证头、超时中断、401 自动登出；
// 2. 登录态管理：token / 账号 / 服务器地址在 localStorage 的读写与清除；
// 3. 业务接口：信号、状态、消息、持仓、板块热点、行情快照、个股评分、
//    IPO 日历、个股查询、资讯、自选股、交易动作、LLM 配置、战法参数、做空开关等；
// 4. SSE 长连接：订阅后端实时推送（如新信号），断线自动重连；
// 5. 市场会话追踪：记录 session 期号，辅助判断非交易时段的首次加载。
//
// 路径拼接原理：接口函数均传入相对路径（如 '/api/signals'），
// 最终请求地址 = baseUrl()（用户配置的服务器地址）+ 相对路径，
// 使前端代码与具体后端地址解耦，便于切换服务器。

// 全局基础路径前缀（预留，当前为空串；实际前缀来自用户配置的服务器地址）
const BASE = ''

// localStorage 存储键名
// 统一带 'liangzai_' 前缀，避免键名与其他应用冲突
// STORAGE_KEY    存储 JWT 访问令牌
// STORAGE_SERVER 存储用户配置的后端服务器地址
// STORAGE_ACCOUNT 存储当前登录账号名
const STORAGE_KEY = 'liangzai_token'
const STORAGE_SERVER = 'liangzai_server_url'
const STORAGE_ACCOUNT = 'liangzai_account'

// 从 localStorage 读取服务器基础地址
// 说明：所有请求均基于该地址拼接相对路径；未配置时返回空串，表示使用同源相对请求
function baseUrl() {
  return localStorage.getItem(STORAGE_SERVER) || ''
}

// 从 localStorage 读取 JWT 令牌
// 说明：令牌由登录接口写入，后续每个请求都会携带该令牌完成鉴权
function getToken() {
  return localStorage.getItem(STORAGE_KEY)
}

// 将登录成功后返回的 token、账号等信息持久化到 localStorage
// @param {string} token      - JWT 访问令牌
// @param {string} account    - 登录账号名（可空）
// @param {string} expiresAt  - 令牌过期时间（预留参数，当前未使用，用于将来做本地过期校验）
function storeAuth(token, account, expiresAt) {
  localStorage.setItem(STORAGE_KEY, token)
  localStorage.setItem(STORAGE_ACCOUNT, account || '')
}

/**
 * 清除本地存储的认证信息（退出登录时调用）
 * 同时移除 token 与账号，使 isLoggedIn() 立即失效
 */
export function clearAuth() {
  localStorage.removeItem(STORAGE_KEY)
  localStorage.removeItem(STORAGE_ACCOUNT)
}

/**
 * 检查当前是否存在有效的登录令牌
 * 说明：只判断 token 是否存在（非空），不校验其真实有效性；
 *       令牌是否过期由后端返回 401 时统一判定（见 request()）。
 * @returns {boolean} true 表示已登录
 */
export function isLoggedIn() {
  return !!getToken()
}

/**
 * 获取当前登录账号名
 * @returns {string} 账号名，未登录时返回空字符串
 */
export function getAccount() {
  return localStorage.getItem(STORAGE_ACCOUNT) || ''
}

/**
 * 获取持久化的服务器地址
 * @returns {string} 服务器地址，未配置时返回空字符串
 */
export function getStoredServer() {
  return localStorage.getItem(STORAGE_SERVER) || ''
}

/**
 * 持久化保存服务器地址
 * 说明：写入 localStorage，供 baseUrl() 在拼接所有请求时读取
 * @param {string} url - 服务器地址
 */
export function setStoredServer(url) {
  localStorage.setItem(STORAGE_SERVER, url)
}

// ── 通用请求封装 ──

/** 请求超时时间（毫秒），防止慢接口把页面卡在空状态 */
// 说明：超过该时长仍未得到响应，则通过 AbortController 中止请求并抛出“请求超时”
const REQUEST_TIMEOUT = 10000

/**
 * 统一的 HTTP 请求封装，自动附加认证头、处理 401 过期
 * 原理：
 *  - URL = baseUrl() + path，即用户配置的服务器地址拼接相对路径；
 *  - 若本地存在 token，则自动附加 Authorization: Bearer <token> 请求头；
 *  - 使用 AbortController + setTimeout 实现超时中断，超时后抛“请求超时”；
 *  - 响应为 401 时视为令牌过期，自动清除本地登录态并抛出“登录已过期”；
 *  - 默认以 JSON 格式发送 / 接收数据。
 * @param {string} path - API 路径（相对路径）
 * @param {object} [opts] - 可选参数 { method, data, headers }
 *   method:  HTTP 方法，默认 GET
 *   data:    请求体对象，会被 JSON.stringify 序列化后作为 body
 *   headers: 附加请求头，与默认头合并（同名可覆盖默认值）
 * @returns {Promise<object>} 响应 JSON
 */
async function request(path, opts = {}) {
  // 拼接完整请求地址：服务器基础地址 + 相对路径
  const url = baseUrl() + path
  // 默认 JSON 头，允许调用方通过 opts.headers 覆盖 / 追加
  const headers = { 'Content-Type': 'application/json', ...opts.headers }
  const token = getToken()
  // 已登录时附加 Bearer 令牌，供后端鉴权
  if (token) headers['Authorization'] = 'Bearer ' + token

  // 设置请求超时，超时后中止请求
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
    // 区分“超时中止”与真正的网络错误：超时抛出明确的中文提示
    if (e && e.name === 'AbortError') throw new Error('请求超时')
    throw e
  } finally {
    // 无论成功与否都清理定时器，避免计时器泄漏
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
 * 说明：登录接口不使用统一 request() 封装，因为登录时尚未持有 token，
 *       且需要单独处理非 2xx 响应的错误信息提取（后端返回 err 字段）。
 * @param {string} username - 用户名
 * @param {string} password - 密码
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
    const err = await res.json().catch(() => ({}))
    throw new Error(err.error || '登录失败')
  }
  const data = await res.json()
  // 登录成功后持久化 token 与账号
  storeAuth(data.token, data.account, data.expires_at)
  return data
}

// ── 策略信号 ──

/** 获取所有策略信号列表 */
// 对应后端 GET /api/signals，返回策略信号数组（含 code、action、score 等字段）
export async function fetchSignals() {
  return request('/api/signals')
}

// ── 系统状态 ──

/** 获取服务端运行状态（含扫描统计、运行时长等） */
// 对应 GET /api/status，返回 { signal_count, in_trade_time, ... }，
// 顶部状态栏与 15 秒轮询均依赖该接口
export async function fetchStatus() {
  return request('/api/status')
}

// ── 消息提醒 ──

/** 获取所有提醒/告警消息 */
// 对应 GET /api/alerts，返回消息数组；消息中心据此展示，未读数用于导航角标
export async function fetchAlerts() {
  return request('/api/alerts')
}

/** 清空消息中心全部消息 */
// 对应 DELETE /api/alerts，一次性删除所有提醒
export async function clearAlerts() {
  return request('/api/alerts', { method: 'DELETE' })
}

/** 手工删除单条消息 */
// 对应 DELETE /api/alerts/{id}，id 经 encodeURIComponent 编码后拼入路径，
// 防止特殊字符破坏 URL 结构
export async function deleteAlert(id) {
  return request('/api/alerts/' + encodeURIComponent(id), { method: 'DELETE' })
}

// ── 持仓管理 ──

/** 获取当前持仓列表及可用资金 */
// 对应 GET /api/holdings，返回 { holdings: [...], available_cash } 等
export async function fetchHoldings() {
  return request('/api/holdings')
}

/** 更新持仓数据（含可用资金） */
// 对应 POST /api/holdings，data 为完整持仓快照，整体覆盖保存
export async function updateHoldings(data) {
  return request('/api/holdings', { method: 'POST', data })
}

// ── 板块热点 ──

/** 获取热门板块数据 */
// 对应 GET /api/sector/hot，返回当前热门板块及其涨幅等
export async function fetchSectorHot() {
  return request('/api/sector/hot')
}

/** 获取当日热点板块轮次记录（历史快照） */
// 对应 GET /api/sector/hot/records，返回各轮次板块快照，用于复盘
export async function fetchSectorHotRecords() {
  return request('/api/sector/hot/records')
}

// ── 行情快照 ──

/** 获取全市场行情快照 */
// 对应 GET /api/snapshot，返回全市场标的的最新行情
export async function fetchSnapshot() {
  return request('/api/snapshot')
}

/** 获取热门个股快照 */
// 对应 GET /api/snapshot/hot，返回热度较高的个股行情子集
export async function fetchHotSnapshot() {
  return request('/api/snapshot/hot')
}

// ── 个股评分 ──

/** 获取全市场个股多维度评分（N形/龙头/双凸/回头/动量） */
// 对应 GET /api/evaluations，返回个股在各战法维度下的评分结果
export async function fetchEvaluations() {
  return request('/api/evaluations')
}

// ── IPO 日历 ──

// 本地缓存键：用于按天缓存 IPO 日历，减少不必要的后端请求
const IPO_CACHE_KEY = 'ipo_calendar_cache_v1'

/**
 * 获取 IPO 日历数据（按日缓存：同一天内首次调用才请求后端，其余直接读缓存）
 * 原理：
 *  - 以当天日期（YYYY-MM-DD，UTC 日期）作为缓存标识；
 *  - 命中缓存且数据结构合法则直接返回本地数据，避免重复请求；
 *  - 未命中则请求 GET /api/ipo/calendar，成功后把当天日期与数据一并写回 localStorage。
 */
export async function fetchIPOCalendar() {
  const today = new Date().toISOString().slice(0, 10)
  try {
    const raw = localStorage.getItem(IPO_CACHE_KEY)
    if (raw) {
      const d = JSON.parse(raw)
      // 命中当日缓存则直接返回，避免重复请求
      if (d.date === today && Array.isArray(d.data)) return d.data
    }
  } catch (_) {}
  const data = await request('/api/ipo/calendar')
  try {
    // 拉取成功后按当天日期写入缓存
    localStorage.setItem(IPO_CACHE_KEY, JSON.stringify({ date: today, data }))
  } catch (_) {}
  return data
}

// ── 个股查询 ──

/**
 * 根据代码查询个股信息（名称、现价等）
 * 说明：code 经 encodeURIComponent 编码后作为查询参数拼入 URL，
 *       避免股票代码中的特殊字符干扰请求。
 * @param {string} code - 股票代码
 */
export async function fetchStockLookup(code) {
  return request('/api/stock/lookup?code=' + encodeURIComponent(code))
}

// ── 资讯 ──

/**
 * 获取新闻资讯
 * @param {boolean} [all] - 是否获取全部（含历史）资讯；true 时追加 ?all=true 查询参数
 */
export async function fetchNews(all) {
  return request(all ? '/api/news?all=true' : '/api/news')
}

// ── 自选股 ──

/** 获取自选股列表 */
// 对应 GET /api/watchlist，返回自选股数组
export async function fetchWatchlist() {
  return request('/api/watchlist')
}

/** 添加自选股 */
// 对应 POST /api/watchlist，请求体 { code } 传入股票代码
export async function addWatchlist(code) {
  return request('/api/watchlist', { method: 'POST', data: { code } })
}

/** 移除自选股 */
// 对应 DELETE /api/watchlist，请求体 { code } 传入股票代码
export async function removeWatchlist(code) {
  return request('/api/watchlist', { method: 'DELETE', data: { code } })
}

// ── 交易动作 ──

/** 对某信号执行买入 / 忽略操作 */
// 对应 POST /api/action，请求体 { code, action }；
// action 取 'buy'（买入）或 'ignore'（忽略）
export async function actionSignal(code, action) {
  return request('/api/action', { method: 'POST', data: { code, action } })
}

// ── 市场时段追踪（非交易时段仅首次加载） ──
// 原理：后端按市场会话（session）推送数据，前端以模块级变量 _lastSession
// 记录最近一次消费的会话期号。当会话号发生变化（进入新的一天或新交易时段）时，
// 视为“新会话”，用于控制在非交易时段内只做一次数据加载，避免重复请求。

// 模块级会话期号缓存：-1 表示尚未记录任何会话
let _lastSession = -1

/** 获取上次记录的会话期号 */
export function getLastSession() { return _lastSession }

/** 设置当前会话期号 */
export function setLastSession(s) { _lastSession = s }

/** 判断当前会话是否为不同于上次的新会话 */
// 原理：与会话号不一致则为新会话，并同步更新记录；相同则返回 false
export function isNewSession(session) {
  if (session === _lastSession) return false
  _lastSession = session
  return true
}

/** 判断某会话是否为交易时段（早盘=1，午盘=3） */
// 说明：session 为后端约定的枚举值，1 表示早盘交易时段、3 表示午盘交易时段
export function isTradingSession(session) {
  return session === 1 || session === 3 // SessionMorningTrade=1, SessionAfternoonTrade=3
}

// ── SSE 服务端推送事件 ──
// 原理：SSE（Server-Sent Events）是单向服务端推送协议，基于浏览器 EventSource 实现长连接。
// 与 WebSocket 不同，SSE 仅由服务端向客户端推送，连接成本低且自带重连，适合行情 / 信号类通知。
//  - 建立连接前先读取 token，并以查询参数 ?token=... 带给后端完成鉴权；
//  - 收到消息后按 JSON 解析，广播给所有已注册回调（sseCallbacks）；
//  - 连接出错时主动 close 后延时 3 秒重连，避免依赖浏览器自带的指数退避重连。

// 当前 SSE 连接对象（EventSource 实例），null 表示未连接
let sse = null
// 已注册的 SSE 消息回调列表，新增信号等推送会依次通知所有回调
let sseCallbacks = []

/**
 * 注册 SSE 消息回调，返回取消注册的函数
 * @param {Function} fn - 消息处理回调，入参为解析后的消息对象
 * @returns {Function} unsubscribe - 调用后将该回调从列表中移除
 */
export function onSSE(fn) {
  sseCallbacks.push(fn)
  return () => { sseCallbacks = sseCallbacks.filter(f => f !== fn) }
}

/** 建立与后端的 SSE 长连接，接收实时推送 */
// 说明：
//  - 已有连接或未登录时直接返回（幂等操作，避免重复建连）；
//  - 连接 URL = baseUrl() + '/api/events?token=' + encodeURIComponent(token)；
//  - 收到消息时解析 JSON 并依次调用 sseCallbacks 中的回调；
//  - onerror 触发时关闭旧连接，3 秒后重新建立，实现手动重连。
export function connectSSE() {
  if (sse) return
  const token = getToken()
  // 未登录时不建立连接
  if (!token) return
  sse = new EventSource(baseUrl() + '/api/events?token=' + encodeURIComponent(token))
  sse.onmessage = (e) => {
    try {
      const msg = JSON.parse(e.data)
      sseCallbacks.forEach(fn => fn(msg))
    } catch (_) {}
  }
  sse.onerror = () => {
    // 连接断开时先关闭，3 秒后自动重连
    disconnectSSE()
    setTimeout(connectSSE, 3000)
  }
}

/** 断开 SSE 长连接 */
// 说明：关闭连接并将 sse 置空，使下次 connectSSE() 可以重新建立连接
export function disconnectSSE() {
  if (sse) { sse.close(); sse = null }
}

// ── LLM 配置 ──

/** 获取 LLM 配置 */
// 对应 GET /api/config/llm，返回 { api_url, api_key, model } 等配置项
export async function fetchLLMConfig() {
  return request('/api/config/llm')
}

/** 设置 LLM 配置（API URL / Key / 模型名） */
// 对应 POST /api/config/llm，cfg 为完整配置对象，后端持久化到 config.json
export async function setLLMConfig(cfg) {
  return request('/api/config/llm', { method: 'POST', data: cfg })
}

// ── 战法参数配置 ──

/** 获取四个战法参数（dragon/double_bump/n_shape/dragon_return） */
// 对应 GET /api/config/strategy，返回各战法的阈值参数
export async function fetchStrategyConfig() {
  return request('/api/config/strategy')
}

/** 保存四个战法参数并持久化到 config.json */
// 对应 POST /api/config/strategy，cfg 为四个战法的阈值参数对象
export async function setStrategyConfig(cfg) {
  return request('/api/config/strategy', { method: 'POST', data: cfg })
}

/** 获取 LLM 诊断调试数据 */
// 对应 GET /api/llm-debug，返回最近一次 LLM 决策的输入 / 输出，供 LLM 诊断页分析
export async function fetchLLMDebug() {
  return request('/api/llm-debug')
}

/** 获取当日全量 LLM/Stage 轮次记录（固化到磁盘，供复盘） */
// 对应 GET /api/stage-records，返回当日全部轮次快照，用于事后复盘
export async function fetchStageRecords() {
  return request('/api/stage-records')
}

// ── 做空开关 ──

/** 查询当前做空状态 */
// 对应 GET /api/short/status，返回 { short_enabled } 布尔值
export async function fetchShortStatus() {
  return request('/api/short/status')
}

/** 切换做空开关 */
// 对应 POST /api/short/toggle，请求体 { enabled } 传入目标开关状态，后端持久化
export async function toggleShort(enabled) {
  return request('/api/short/toggle', { method: 'POST', data: { enabled } })
}

// ── 资讯显示全部开关 ──

/** 查询"资讯显示全部"开关状态 */
// 对应 GET /api/news/showall，返回 { news_show_all } 布尔值
export async function fetchNewsShowAllStatus() {
  return request('/api/news/showall')
}

/** 切换"资讯显示全部"开关 */
// 对应 POST /api/news/showall，请求体 { enabled }；开启时弱档/中性资讯也出现在 /api/news
export async function toggleNewsShowAll(enabled) {
  return request('/api/news/showall', { method: 'POST', data: { enabled } })
}
