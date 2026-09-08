// ── API 层单元测试 api.test.jsx ──
// 覆盖 web/src/api/index.js 的本地认证/权限状态管理与会话追踪逻辑：
// isLoggedIn / clearAuth / getAccount / getRole / getPerms / isAdmin / hasPerm /
// 服务器地址读写，以及 isNewSession / isTradingSession / getLastSession 等会话辅助。
// 每个用例前后清空 localStorage 与 mock，保证相互隔离。
import { describe, it, expect, beforeEach, vi } from 'vitest'
import * as api from '../api/index.js'

describe('api - 认证与权限', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    localStorage.clear()
  })

  describe('isLoggedIn', () => {
    it('无 token 返回 false', () => {
      expect(api.isLoggedIn()).toBe(false)
    })
    it('有 token 返回 true', () => {
      localStorage.setItem('liangzai_token', 'test-token')
      expect(api.isLoggedIn()).toBe(true)
    })
  })

  describe('clearAuth', () => {
    it('清除所有认证信息', () => {
      localStorage.setItem('liangzai_token', 't')
      localStorage.setItem('liangzai_account', 'admin')
      localStorage.setItem('liangzai_role', 'admin')
      localStorage.setItem('liangzai_perms', '[]')
      api.clearAuth()
      expect(api.isLoggedIn()).toBe(false)
      expect(api.getAccount()).toBe('')
      expect(api.getRole()).toBe('user')
    })
  })

  describe('getAccount', () => {
    it('返回存储的账号', () => {
      localStorage.setItem('liangzai_account', 'admin')
      expect(api.getAccount()).toBe('admin')
    })
    it('无账号返回空串', () => {
      expect(api.getAccount()).toBe('')
    })
  })

  describe('getRole', () => {
    it('返回存储的角色', () => {
      localStorage.setItem('liangzai_role', 'admin')
      expect(api.getRole()).toBe('admin')
    })
    it('默认返回 user', () => {
      expect(api.getRole()).toBe('user')
    })
  })

  describe('getPerms', () => {
    it('返回权限位数组', () => {
      localStorage.setItem('liangzai_perms', JSON.stringify(['research_approve']))
      expect(api.getPerms()).toEqual(['research_approve'])
    })
    it('无权限返回空数组', () => {
      expect(api.getPerms()).toEqual([])
    })
  })

  describe('isAdmin', () => {
    it('角色为 admin 时返回 true', () => {
      localStorage.setItem('liangzai_role', 'admin')
      expect(api.isAdmin()).toBe(true)
    })
    it('非 admin 返回 false', () => {
      localStorage.setItem('liangzai_role', 'user')
      expect(api.isAdmin()).toBe(false)
    })
  })

  describe('hasPerm', () => {
    it('admin 隐式拥有所有权限', () => {
      localStorage.setItem('liangzai_role', 'admin')
      expect(api.hasPerm('any_perm')).toBe(true)
    })
    it('拥有指定权限返回 true', () => {
      localStorage.setItem('liangzai_role', 'user')
      localStorage.setItem('liangzai_perms', JSON.stringify(['research_approve']))
      expect(api.hasPerm('research_approve')).toBe(true)
    })
    it('无指定权限返回 false', () => {
      localStorage.setItem('liangzai_role', 'user')
      localStorage.setItem('liangzai_perms', JSON.stringify([]))
      expect(api.hasPerm('research_approve')).toBe(false)
    })
  })

  describe('getStoredServer / setStoredServer', () => {
    it('读写服务器地址', () => {
      api.setStoredServer('https://quant-trading.top')
      expect(api.getStoredServer()).toBe('https://quant-trading.top')
    })
    it('无存储返回空串', () => {
      expect(api.getStoredServer()).toBe('')
    })
  })
})

describe('api - 会话追踪', () => {
  it('isNewSession 不同会话返回 true', () => {
    expect(api.isNewSession(1)).toBe(true)
    expect(api.isNewSession(1)).toBe(false)
    expect(api.isNewSession(2)).toBe(true)
  })

  it('isTradingSession 正确判断交易时段', () => {
    expect(api.isTradingSession(1)).toBe(true)
    expect(api.isTradingSession(3)).toBe(true)
    expect(api.isTradingSession(2)).toBe(false)
    expect(api.isTradingSession(0)).toBe(false)
  })

  it('getLastSession / setLastSession', () => {
    api.setLastSession(5)
    expect(api.getLastSession()).toBe(5)
  })
})

describe('api - 网关执行通道（§QMT-DUAL）', () => {
  // 统一 fetch mock：替换全局 fetch，返回可解析的两段式 Response（status/ok/json）
  function mockFetchOk(payload, status = 200) {
    global.fetch = vi.fn().mockResolvedValue({
      ok: status >= 200 && status < 300,
      status,
      json: async () => payload,
    })
  }

  beforeEach(() => {
    localStorage.clear()
    vi.restoreAllMocks()
  })

  it('fetchQMTBroker 请求 GET /api/qmt/broker 并回传双路径状态', async () => {
    const payload = { ok: true, broker: 'xt', xt_connected: true, queued_connected: false }
    mockFetchOk(payload)
    // 注入 token：request() 会附加 Authorization 头
    localStorage.setItem('liangzai_token', 'test-token')

    const st = await api.fetchQMTBroker()
    expect(st).toEqual(payload)

    const [url, opts] = global.fetch.mock.calls[0]
    expect(url.endsWith('/api/qmt/broker')).toBe(true)
    expect((opts && opts.method) || 'GET').toBe('GET')
    expect(opts.headers.Authorization).toBe('Bearer test-token')
  })

  it('switchQMTBroker 提交 POST /api/qmt/broker 并携带目标通道', async () => {
    const payload = { ok: '1', broker: 'queued' }
    mockFetchOk(payload)

    const res = await api.switchQMTBroker('queued')
    expect(res).toEqual(payload)

    const [url, opts] = global.fetch.mock.calls[0]
    expect(url.endsWith('/api/qmt/broker')).toBe(true)
    expect(opts.method).toBe('POST')
    expect(JSON.parse(opts.body)).toEqual({ broker: 'queued' })
  })

  it('fetchQMTBroker 网关不可达（ok=false）时原样回传，交由调用方展示', async () => {
    const payload = { ok: false, broker: '', err: 'not enabled' }
    mockFetchOk(payload)

    const st = await api.fetchQMTBroker()
    expect(st.ok).toBe(false)
    expect(st.err).toBe('not enabled')
  })
})
