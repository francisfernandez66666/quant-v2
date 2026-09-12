// ── §F5 SSE 事件总线 sseBus.js ──
// 单一 SSE 连接由 App 维护（api.onSSE），本模块把每条推送按 `type` 分发给按类型订阅的页面，
// 让"事件到达即刷新"取代各页各自的 5s 轮询（轮询降为 60s 兜底）。纯逻辑、无 DOM，可单测。
// English: the §F5 SSE event bus. App owns the single connection (api.onSSE) and dispatches each push
// here by `type`; pages subscribe to the types they care about so "event arrives → refresh" replaces
// each page's own 5s polling (polling demoted to a 60s safety net). Pure logic, DOM-free, unit-tested.

// handlers: Map<type, Set<fn>>，'*' 为通配（收所有类型）。
// English: handlers keyed by type; '*' is a wildcard receiving every message.
const handlers = new Map()

function add(type, fn) {
  let set = handlers.get(type)
  if (!set) { set = new Set(); handlers.set(type, set) }
  set.add(fn)
}
function remove(type, fn) {
  const set = handlers.get(type)
  if (!set) return
  set.delete(fn)
  if (set.size === 0) handlers.delete(type)
}

/**
 * 订阅指定类型的 SSE 事件（types 为数组或字符串，含 '*' 表示全部）。返回取消订阅函数。
 * @param {string|string[]} types
 * @param {(msg:object)=>void} fn
 * @returns {() => void}
 */
export function on(types, fn) {
  const arr = Array.isArray(types) ? types : [types]
  arr.forEach((t) => add(t, fn))
  return () => arr.forEach((t) => remove(t, fn))
}

/**
 * 分发一条消息给对应类型订阅者 + '*' 通配订阅者。异常隔离，单个回调抛错不影响其余。
 * @param {object} msg 含 type 字段的已解析 SSE 消息
 * @returns {number} 被调用的回调数量
 */
export function dispatch(msg) {
  if (!msg || typeof msg !== 'object') return 0
  const t = msg.type
  let n = 0
  const call = (fn) => { try { fn(msg); n++ } catch (_) {} }
  const set = t ? handlers.get(t) : null
  if (set) set.forEach(call)
  const wild = handlers.get('*')
  if (wild) wild.forEach(call)
  return n
}

/** 当前订阅的类型数（测试/调试用）。English: number of subscribed types (test/debug). */
export function size() { return handlers.size }

/** 清空全部订阅（测试用）。English: clear all subscriptions (tests). */
export function __reset() { handlers.clear() }
