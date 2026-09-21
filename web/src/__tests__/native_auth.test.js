// ── §NATIVEAUTH（2026-09-22 C批）登录 token 迁原生安全存储：Web 侧单测 ──
// Covers web/src/api/index.js 的 getToken/storeAuth/clearAuth 原生桥（window.AndroidAuth）语义：
// ① 无桥（纯浏览器/旧 APK）：行为与迁移前完全一致，token 仍在 localStorage；
//    No bridge: behavior identical to pre-migration, token stays in localStorage.
// ② 有桥（新版 APK 内嵌 WebView）：token 只进原生加密存储（桥），localStorage 不再落明文；
//    account/role/perms 非敏感仍留 localStorage；clearAuth 双清防残票。
//    With bridge: token goes only to native secure storage, no plaintext copy in localStorage;
//    non-sensitive account/role/perms remain in localStorage; clearAuth wipes both sides.
// ③ 升级首启迁移：桥内为空但 localStorage 有旧版明文 token → getToken() 自动上桥并删本地明文。
//    First launch after APK upgrade: empty bridge + legacy plaintext token → getToken() migrates it up
//    and removes the local plaintext copy.
// 桩实现仿同目录 a5_auth_reconcile.test.js / sse_ticket.test.js 的 import 与 localStorage 用法。
import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import * as api from '../api/index.js'

// 内存版 AndroidAuth 桥替身：三个同步方法 + jest 风格调用记录，语义对齐原生契约
// In-memory stand-in for the AndroidAuth bridge: three sync methods + call records, matching native contract
// —— getToken() 无票返回空串；setToken(str) 非空受理返回 true；clearToken() 返回 true。
// getToken() returns "" when empty; setToken(str) returns true for non-empty; clearToken() returns true.
function makeBridge(initial = '') {
  let store = initial
  const calls = { getToken: 0, setToken: [], clearToken: 0 }
  return {
    calls,
    getToken() { calls.getToken++; return store },
    setToken(t) {
      if (typeof t !== 'string' || t.length === 0) return false
      store = t; calls.setToken.push(t); return true
    },
    clearToken() { calls.clearToken++; store = ''; return true },
  }
}

describe('§NATIVEAUTH 无桥回落 localStorage（纯浏览器/旧 APK）', () => {
  beforeEach(() => { localStorage.clear() })
  afterEach(() => { delete window.AndroidAuth })

  it('storeAuth → localStorage 有 token；getToken 读到；clearAuth 清光', () => {
    expect(window.AndroidAuth).toBeUndefined() // 前置：环境无桥
    api.storeAuth('tok-web', 'alice', '2026-09-22T00:00:00Z', 'admin', ['trading'])
    expect(localStorage.getItem('liangzai_token')).toBe('tok-web')
    expect(api.getToken()).toBe('tok-web')
    expect(api.isLoggedIn()).toBe(true)

    api.clearAuth()
    expect(localStorage.getItem('liangzai_token')).toBeNull()
    expect(localStorage.getItem('liangzai_account')).toBeNull()
    expect(api.getToken()).toBeNull() // 四键全清，无残票
    expect(api.isLoggedIn()).toBe(false)
  })
})

describe('§NATIVEAUTH 有桥：token 走原生加密存储', () => {
  let bridge
  beforeEach(() => {
    localStorage.clear()
    bridge = makeBridge()
    window.AndroidAuth = bridge
  })
  afterEach(() => { delete window.AndroidAuth })

  it('storeAuth → 桥收到 setToken，localStorage 无 liangzai_token，account/role/perms 仍在', () => {
    api.storeAuth('tok-native', 'bob', null, 'user', ['view'])
    expect(bridge.calls.setToken).toEqual(['tok-native']) // ① token 上了桥
    expect(localStorage.getItem('liangzai_token')).toBeNull() // ② 明文副本已清（含历史残留）
    // ③ 非敏感三键维持 localStorage（getAccount/getRole/getPerms 语义不变）
    expect(api.getAccount()).toBe('bob')
    expect(api.getRole()).toBe('user')
    expect(api.getPerms()).toEqual(['view'])
  })

  it('getToken → 走桥读取，不回退明文', () => {
    api.storeAuth('tok-native', 'bob', null, 'user', [])
    bridge.calls.getToken = 0
    expect(api.getToken()).toBe('tok-native')
    expect(bridge.calls.getToken).toBe(1) // 确实经桥读取而非 localStorage
  })

  it('clearAuth → 桥 clearToken 被调（双清防残票）', () => {
    api.storeAuth('tok-native', 'bob', null, 'user', [])
    api.clearAuth()
    expect(bridge.calls.clearToken).toBe(1) // 原生侧清
    expect(bridge.getToken()).toBe('')      // 桥内已空
    expect(localStorage.getItem('liangzai_token')).toBeNull() // 本地侧清
    expect(api.isLoggedIn()).toBe(false)
  })
})

describe('§NATIVEAUTH 升级首启迁移：旧明文 token 自动上桥', () => {
  let bridge
  beforeEach(() => {
    localStorage.clear()
    // 模拟旧版 APK 升级后首次启动：localStorage 里躺着历史明文 token，新桥为空
    localStorage.setItem('liangzai_token', 'legacy')
    bridge = makeBridge('') // 空桥 = 原生侧尚无票
    window.AndroidAuth = bridge
  })
  afterEach(() => { delete window.AndroidAuth })

  it('getToken() 返回 legacy、桥内已存、localStorage 明文已删', () => {
    expect(api.getToken()).toBe('legacy')
    expect(bridge.calls.setToken).toEqual(['legacy']) // 迁移写入原生加密存储
    expect(bridge.getToken()).toBe('legacy')          // 桥内已有
    expect(localStorage.getItem('liangzai_token')).toBeNull() // 明文副本即时清除
    // 迁移后再读直接走桥，不再触发 setToken（幂等，不反复搬家）
    expect(api.getToken()).toBe('legacy')
    expect(bridge.calls.setToken).toEqual(['legacy'])
  })
})
