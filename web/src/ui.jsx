// ── 共享 UI 原语 ──
// 全局 Toast / Notification 封装（基于 TDesign MessagePlugin / NotificationPlugin），
// 替代旧 Vue 版 App 内的 addToast 逻辑，供各页面与 App 壳统一调用。
import React from 'react'
import { MessagePlugin, NotificationPlugin } from 'tdesign-react'

/**
 * 弹出轻提示（顶部居中，3s 后自动消失）
 * @param {string} msg - 提示内容
 * @param {'info'|'success'|'warning'|'error'} type - 提示类型
 */
export function showToast(msg, type = 'info') {
  const fn = MessagePlugin[type] || MessagePlugin.info
  fn({ content: String(msg), duration: 3000 })
}

/**
 * 弹出系统通知（右上角，可手动关闭，用于关键消息/交易信号）
 * @param {string} title - 通知标题
 * @param {string} body - 通知正文
 * @param {object} opts - NotificationPlugin 额外选项
 */
export function showNotify(title, body, opts = {}) {
  NotificationPlugin.info({
    title: String(title),
    content: String(body),
    duration: opts.duration ?? 4000,
    closeBtn: true,
    ...opts,
  })
}

/**
 * 加载态组件
 * @param {{text?:string}} props
 */
export function Loading({ text = '加载中...' }) {
  return (
    <div className="card-dark" style={{ textAlign: 'center', color: '#888', padding: 32 }}>
      {text}
    </div>
  )
}

/**
 * 空态组件
 * @param {{text?:string}} props
 */
export function Empty({ text = '暂无数据' }) {
  return (
    <div className="card-dark" style={{ textAlign: 'center', color: '#666', padding: 32 }}>
      {text}
    </div>
  )
}
