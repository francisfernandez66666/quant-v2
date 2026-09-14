// ── 共享 UI 原语 ──
// 全局 Toast / Notification 封装（基于 TDesign MessagePlugin / NotificationPlugin），
// 替代旧 Vue 版 App 内的 addToast 逻辑，供各页面与 App 壳统一调用。
import React from 'react'
import { MessagePlugin, NotificationPlugin, DialogPlugin } from 'tdesign-react'

/**
 * 通用确认弹窗：返回 Promise<boolean>，用户确认 resolve(true)、关闭/取消 resolve(false)。
 * §P1-9（2026-09-15）：原先 Quant/Paper/MsgCenter/Admin/Research 五处各持一份裸实现，
 * 其中四处缺少 done 守卫——TDesign 的 d.hide() 会同步触发 onClose，若 onConfirm 先
 * d.hide() 再 resolve(true)，onClose 的 resolve(false) 会先一步生效，导致"确认"被当作
 * "取消"（Promise 首次 resolve 即定型）。统一收口到本守卫版本。
 * English: shared resolve-once confirm dialog. d.hide() synchronously fires onClose; without
 * the guard a confirm could resolve false (treated as cancel), so actions would never persist.
 * @param {string} body 弹窗正文
 * @param {string} [header='确认'] 弹窗标题
 * @returns {Promise<boolean>} 用户确认结果
 */
export function confirmDialog(body, header = '确认') {
  return new Promise((resolve) => {
    let done = false
    // resolve 一次性守卫：无论确认/取消/点遮罩先触发，首次结果生效
    const finish = (val) => {
      if (done) return
      done = true
      d.hide()
      resolve(val)
    }
    const d = DialogPlugin.confirm({
      header,
      body,
      theme: 'warning',
      onConfirm: () => finish(true),
      onClose: () => finish(false),
    })
  })
}

/**
 * 弹出轻提示（顶部居中，3s 后自动消失）
 * @param {string} msg - 提示内容
 * @param {'info'|'success'|'warning'|'error'} type - 提示类型
 */
// 轻提示 toast（type: info/success/warning/error）
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
// 右上角系统风格通知条
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
// 通用加载占位卡片
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
// 通用空数据占位卡片
export function Empty({ text = '暂无数据' }) {
  return (
    <div className="card-dark" style={{ textAlign: 'center', color: '#666', padding: 32 }}>
      {text}
    </div>
  )
}
