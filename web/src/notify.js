// 通知工具：APK WebView 里优先走原生桥（window.AndroidNotify），
// 桌面浏览器回退到标准 Web Notification API。
// Notification helper: prefer the native bridge inside the APK WebView,
// fall back to the standard Web Notification API on desktop browsers.
// （原生桥说明：WebView 的 Web Notification API 依赖的 onShowNotification
//   在新版平台已移除，new Notification() 在 Android 上会静默失效，
//   因此 APK 端一律走 MainActivity 注入的 AndroidNotify 桥。）

/**
 * 判断当前是否运行在 APK 原生桥环境
 * 说明：APK 的 MainActivity 会向 WebView 注入 window.AndroidNotify（含 show 方法），
 *       检测到该桥即认为可走原生通道发系统通知；桌面浏览器返回 false。
 * @returns {boolean} true 表示原生桥可用
 */
function isNative() {
  return (
    typeof window !== 'undefined' &&
    typeof window.AndroidNotify !== 'undefined' &&
    typeof window.AndroidNotify.show === 'function'
  )
}

/**
 * 判断当前环境是否允许弹出系统通知
 * 原生桥环境恒为可（Android 通知权限由系统设置控制，真正发送时才校验）；
 * 浏览器环境要求支持 Notification API 且用户已授权（permission === 'granted'）。
 * @returns {boolean} true 表示当前可以发通知
 */
function canNotify() {
  if (isNative()) return true
  return typeof Notification !== 'undefined' && Notification.permission === 'granted'
}

/**
 * 申请系统通知权限
 * 原生桥无需浏览器授权流程，直接视为已授予（granted），由 Android 端自行申请系统权限；
 * 浏览器环境走标准 Notification.requestPermission() 弹授权框；
 * 环境不支持时返回 'unsupported'，调用方据此跳过后续提示。
 * @returns {Promise<string>} 'granted' / 'denied' / 'default' / 'unsupported'
 */
function requestPermission() {
  if (isNative()) return Promise.resolve('granted')
  if (typeof Notification === 'undefined') return Promise.resolve('unsupported')
  return Notification.requestPermission()
}

/**
 * 发送一条系统通知（立即发送，不做限流；限流请用 notifyThrottled）
 * @param {string} title - 通知标题
 * @param {string} body  - 通知正文
 * @returns {boolean} 是否真正发出：
 *   原生桥以 show() 返回布尔为准（false=未获系统通知权限）；
 *   浏览器端 new Notification() 成功即 true，未授权或抛异常为 false。
 */
function notify(title, body) {
  if (isNative()) {
    try {
      // 原生桥 show 返回布尔：true=已发送；false=未获通知权限，前端据此提示用户
      return !!window.AndroidNotify.show(String(title || ''), String(body || ''))
    } catch (_) {
      return false
    }
  }
  if (typeof Notification === 'undefined' || Notification.permission !== 'granted') return false
  try {
    new Notification(title || '', { body: body || '', icon: '' })
    return true
  } catch (_) {
    return false
  }
}

// 按 key 限流的通知窗口表：key 形如 "code@level"（或 "scan"），记录最近一次通知时间戳
const throttledAt = new Map()
// 默认限流窗口（毫秒）：同一 key 在窗口内最多通知一次
const THROTTLE_WINDOW_MS = 60 * 1000

/**
 * 按 key 维度限流发送系统通知（同一 key 在限流窗口内最多弹一次）
 * @param {string} key   - 限流维度键，如 "code@level"（个股@级别）或 "scan"（信号批次）；空值回退 'global'
 * @param {string} title - 通知标题
 * @param {string} body  - 通知正文
 * @returns {boolean} 是否真正发送（false = 窗口内已发过，或未获通知权限）
 */
// 按 key 限流发送系统通知。
// 与全局 60s 去重不同，这里按 "code@level" 维度限流：同一只股票同一级别在窗口内
// 只弹一次，不同股票/级别的提醒互不压制，避免高频 trigger 事件刷屏的同时不漏掉关键个股提醒。
// 返回是否真正发送了通知（true=已发送；false=窗口内已发过或未获权限）。
function notifyThrottled(key, title, body) {
  if (!key) key = 'global'
  if (!canNotify()) return false
  const now = Date.now()
  const last = throttledAt.get(key) || 0
  if (now - last < THROTTLE_WINDOW_MS) return false
  throttledAt.set(key, now)
  // 控制 map 大小，避免长期运行无限增长（超过 1000 键时清空重建）
  if (throttledAt.size > 1000) throttledAt.clear()
  return notify(title, body)
}

export { isNative, canNotify, requestPermission, notify, notifyThrottled }

