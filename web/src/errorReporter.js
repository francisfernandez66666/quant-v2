// errorReporter.js — §OBSERVABILITY（2026-10-26）前端运行期错误上报。
//
// 只上报登录后的运行期错误：window.onerror / unhandledrejection / React ErrorBoundary。
// 载荷最小化（message/stack/source/line/col/route/build），同指纹 60s 内只发一次，
// 每分钟最多 10 条；没有 token 时静默跳过（登录页错误不进后端）。
//
// English: minimal, throttled frontend error reporting for logged-in users; no token → no send.
import { getToken as getStoredToken } from './api/index.js'

const MAX_PER_MIN = 10
const DEDUPE_MS = 60_000

function defaultBuild() {
  try {
    return typeof __BUILD_COMMIT__ !== 'undefined' ? String(__BUILD_COMMIT__) : 'dev'
  } catch (_) {
    return 'dev'
  }
}

export function createClientErrorReporter({
  // §NATIVEAUTH：token 必须走 api 存储层（原生桥/加密存储），禁止绕过它直读 localStorage。
  getToken = () => { try { return getStoredToken() || '' } catch (_) { return '' } },
  fetchImpl = (...args) => fetch(...args),
  now = () => Date.now(),
  build = defaultBuild(),
  route = () => { try { return window.location.hash || window.location.pathname || '' } catch (_) { return '' } },
} = {}) {
  const lastSeen = new Map()
  let sentAt = []

  // report 去重/限流后上报一条错误；上报自身失败静默，绝不形成错误风暴。
  function report(error, extra = {}) {
    try {
      const token = getToken()
      if (!token) return
      const err = error instanceof Error ? error : new Error(String(error || 'unknown frontend error'))
      const message = String(err.message || 'unknown frontend error').slice(0, 500)
      const stack = [err.stack || '', extra.componentStack || ''].filter(Boolean).join('\n').slice(0, 4000)
      const source = String(extra.source || '').slice(0, 500)
      const line = Number.isFinite(Number(extra.line)) ? Number(extra.line) : 0
      const col = Number.isFinite(Number(extra.col)) ? Number(extra.col) : 0
      const key = `${message}|${source}|${line}|${col}`
      const t = now()
      if (lastSeen.has(key) && t - lastSeen.get(key) < DEDUPE_MS) return
      sentAt = sentAt.filter((x) => t - x < 60_000)
      if (sentAt.length >= MAX_PER_MIN) return
      lastSeen.set(key, t)
      sentAt.push(t)
      if (lastSeen.size > 100) {
        for (const [k, ts] of lastSeen) { if (t - ts >= DEDUPE_MS) lastSeen.delete(k) }
      }
      fetchImpl('/api/client-errors', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', Authorization: 'Bearer ' + token },
        body: JSON.stringify({ message, stack, source, line, col, route: route(), build }),
      }).catch(() => {})
    } catch (_) {
      // 上报自身绝不再抛错（否则会形成错误风暴）。
    }
  }

  // install 注册全局错误监听；只装一次，StrictMode 双挂载也不会重复上报。
  function install() {
    try {
      if (typeof window === 'undefined' || window.__liangzaiErrorReporterInstalled) return
      window.__liangzaiErrorReporterInstalled = true
      window.addEventListener('error', (e) => {
        report(e && e.error ? e.error : new Error((e && e.message) || 'window error'), {
          source: e && e.filename,
          line: e && e.lineno,
          col: e && e.colno,
        })
      })
      window.addEventListener('unhandledrejection', (e) => {
        report(e && e.reason ? e.reason : new Error('unhandled rejection'))
      })
    } catch (_) {}
  }

  return { report, install }
}

export const clientErrorReporter = createClientErrorReporter()
export const reportClientError = clientErrorReporter.report
export const installGlobalErrorReporting = clientErrorReporter.install
