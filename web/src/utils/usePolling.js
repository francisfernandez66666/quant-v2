// ── §N-09 统一轮询生命周期 hook（2026-10-20）──
// 缺陷原文：Dashboard/Signals/Hotspot 各自写了 visibilitychange，而 App 状态轮询、
// 自选页、持仓页、量化页的 60s 轮询**没有**可见性处理——手机切到后台仍持续打接口，
// 耗流量/电量，token 过期后还在刷 401。
//
// 本 hook 是唯一实现：hidden 清定时器；visible 立即补一次再重启；卸载/失活清理干净。
// 禁止各页再各写一份 visibilitychange（本仓"两套读法必然分叉"的老坑）。
import { useEffect, useRef } from 'react'

/**
 * @param {() => void} fn 每轮执行的函数（内部用 ref 持有最新闭包，避免因依赖变化重建定时器）
 * @param {number} ms 轮询间隔毫秒
 * @param {boolean} enabled 是否启用（false 时不建定时器；页面权限/标签页可据此开关）
 */
export function usePolling(fn, ms, enabled = true) {
  const fnRef = useRef(fn)
  // 每次渲染更新最新闭包；定时器本身不因函数身份变化而重建
  useEffect(() => { fnRef.current = fn }, [fn])

  useEffect(() => {
    if (!enabled || !ms || ms <= 0 || typeof document === 'undefined') return undefined
    let timer = null
    const stop = () => {
      if (timer) { clearInterval(timer); timer = null }
    }
    const tick = () => {
      // 双保险：即使 visibilitychange 事件漏了，隐藏态也不打请求
      if (!document.hidden) fnRef.current()
    }
    const start = () => {
      stop()
      timer = setInterval(tick, ms)
    }
    const onVisibility = () => {
      if (document.hidden) { stop(); return }
      // 恢复可见：立即补一次（后台期间可能错过了 SSE/数据变化），再重启周期
      stop()
      fnRef.current()
      start()
    }
    start()
    document.addEventListener('visibilitychange', onVisibility)
    return () => {
      stop()
      document.removeEventListener('visibilitychange', onVisibility)
    }
  }, [enabled, ms])
}
