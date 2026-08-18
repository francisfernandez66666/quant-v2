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
      window.AndroidNotify.show(String(title || ''), String(body || ''))
      return true
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

export { isNative, canNotify, requestPermission, notify }
