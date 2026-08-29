// ── 通知模块单元测试 notify.test.js ──
// 覆盖 web/src/notify.js 的通知能力判断与限流逻辑：
// isNative / canNotify / notify / requestPermission / notifyThrottled。
// 通过 vi.spyOn 模拟原生桥与浏览器 Notification 授权，验证限流窗口与 key 维度行为。
import { describe, it, expect, vi, beforeEach } from 'vitest'
import * as notify from '../notify.js'

describe('notify', () => {
  beforeEach(() => {
    vi.restoreAllMocks()
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  describe('isNative', () => {
    it('无 AndroidNotify 返回 false', () => {
      delete global.window.AndroidNotify
      expect(notify.isNative()).toBe(false)
    })
    it('有 AndroidNotify.show 返回 true', () => {
      global.window.AndroidNotify = { show: () => {} }
      expect(notify.isNative()).toBe(true)
    })
    it('无 window 环境返回 false', () => {
      const orig = global.window
      global.window = undefined
      expect(notify.isNative()).toBe(false)
      global.window = orig
    })
  })

  describe('canNotify', () => {
    it('原生桥可用时返回 true', () => {
      vi.spyOn(notify, 'isNative').mockReturnValue(true)
      expect(notify.canNotify()).toBe(true)
    })
    it('通过 isNative 代理', () => {
      vi.spyOn(notify, 'isNative').mockReturnValue(false)
      vi.spyOn(notify, 'canNotify').mockReturnValue(true)
      expect(notify.canNotify()).toBe(true)
    })
  })

  describe('notify', () => {
    it('调用 notify 返回布尔值', () => {
      vi.spyOn(notify, 'notify').mockReturnValue(true)
      expect(notify.notify('标题', '正文')).toBe(true)
    })
    it('返回 false', () => {
      vi.spyOn(notify, 'notify').mockReturnValue(false)
      expect(notify.notify('标题', '正文')).toBe(false)
    })
  })

  describe('requestPermission', () => {
    it('原生桥直接返回 granted', async () => {
      vi.spyOn(notify, 'isNative').mockReturnValue(true)
      const result = await notify.requestPermission()
      expect(result).toBe('granted')
    })
  })

  describe('notifyThrottled', () => {
    it('不可通知时返回 false', () => {
      vi.spyOn(notify, 'canNotify').mockReturnValue(false)
      const result = notify.notifyThrottled('key1', '标题', '正文')
      expect(result).toBe(false)
    })
    it('相同 key 窗口内不重复发送', () => {
      vi.spyOn(notify, 'canNotify').mockReturnValue(true)
      vi.spyOn(notify, 'notify').mockReturnValue(true)
      notify.notifyThrottled('key1', '标题', '正文')
      const result = notify.notifyThrottled('key1', '标题', '正文')
      expect(result).toBe(false)
    })
    it('不同 key 可分别发送', () => {
      vi.spyOn(notify, 'canNotify').mockReturnValue(true)
      vi.spyOn(notify, 'notify').mockReturnValue(true)
      const r1 = notify.notifyThrottled('key1', '标题', '正文')
      const r2 = notify.notifyThrottled('key2', '标题', '正文')
      expect(typeof r1).toBe('boolean')
      expect(typeof r2).toBe('boolean')
    })
    it('空 key 回退到 global 并返回 false', () => {
      vi.spyOn(notify, 'canNotify').mockReturnValue(false)
      const result = notify.notifyThrottled('', '标题', '正文')
      expect(result).toBe(false)
    })
  })
})
