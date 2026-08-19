// 通知工具：APK WebView 里优先走原生桥（window.AndroidNotify），
// 桌面浏览器回退到标准 Web Notification API。
// Notification helper: prefer the native bridge inside the APK WebView,
// fall back to the standard Web Notification API on desktop browsers.
// （原生桥说明：WebView 的 Web Notification API 依赖的 onShowNotification
//   在新版平台已移除，new Notification() 在 Android 上会静默失效，
//   因此 APK 端一律走 MainActivity 注入的 AndroidNotify 桥。）

function isNative() {
  return (
    typeof window !== 'undefined' &&
    typeof window.AndroidNotify !== 'undefined' &&
    typeof window.AndroidNotify.show === 'function'
  )
}

function canNotify() {
  if (isNative()) return true
  return typeof Notification !== 'undefined' && Notification.permission === 'granted'
}

function requestPermission() {
  if (isNative()) return Promise.resolve('granted')
  if (typeof Notification === 'undefined') return Promise.resolve('unsupported')
  return Notification.requestPermission()
}

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

