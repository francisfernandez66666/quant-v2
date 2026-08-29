// ── UI 原语单元测试 ui.test.js ──
// 验证 web/src/ui.jsx 的全局提示封装 showToast / showNotify 是否正确调用
// TDesign 的 MessagePlugin / NotificationPlugin，并透传内容与选项（duration / closeBtn）。
// 通过 vi.mock 拦截 tdesign-react 导出，断言调用参数。
import { describe, it, expect, vi } from 'vitest'

vi.mock('tdesign-react', () => ({
  MessagePlugin: { info: vi.fn(), success: vi.fn(), warning: vi.fn(), error: vi.fn() },
  NotificationPlugin: { info: vi.fn() },
}))

import { showToast, showNotify } from '../ui.jsx'
import * as tdesign from 'tdesign-react'

describe('showToast', () => {
  it('调用 MessagePlugin.success', () => {
    showToast('提示内容', 'success')
    expect(tdesign.MessagePlugin.success).toHaveBeenCalledWith({ content: '提示内容', duration: 3000 })
  })

  it('默认类型为 info', () => {
    showToast('提示内容')
    expect(tdesign.MessagePlugin.info).toHaveBeenCalledWith({ content: '提示内容', duration: 3000 })
  })

  it('调用 MessagePlugin.warning', () => {
    showToast('警告内容', 'warning')
    expect(tdesign.MessagePlugin.warning).toHaveBeenCalledWith({ content: '警告内容', duration: 3000 })
  })

  it('调用 MessagePlugin.error', () => {
    showToast('错误内容', 'error')
    expect(tdesign.MessagePlugin.error).toHaveBeenCalledWith({ content: '错误内容', duration: 3000 })
  })
})

describe('showNotify', () => {
  it('调用 NotificationPlugin.info', () => {
    showNotify('标题', '正文')
    expect(tdesign.NotificationPlugin.info).toHaveBeenCalledWith(
      expect.objectContaining({ title: '标题', content: '正文' })
    )
  })

  it('支持自定义 duration', () => {
    showNotify('标题', '正文', { duration: 5000 })
    expect(tdesign.NotificationPlugin.info).toHaveBeenCalledWith(
      expect.objectContaining({ duration: 5000 })
    )
  })

  it('支持 closeBtn 选项', () => {
    showNotify('标题', '正文', { closeBtn: false })
    expect(tdesign.NotificationPlugin.info).toHaveBeenCalledWith(
      expect.objectContaining({ closeBtn: false })
    )
  })
})
