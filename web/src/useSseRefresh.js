// ── §F5 SSE 驱动刷新钩子 useSseRefresh.js ──
// 把"事件到达即回调 + 低频轮询兜底"收敛成一个钩子：页面声明关心的 SSE 类型与刷新函数，
// 挂载即订阅 sseBus（App 单连接分发）并起一个兜底 interval（默认 60s，取代原先各页 5~15s 高频轮询）。
// 回调始终执行最新版本（ref），interval 与订阅随组件卸载自动清理。
// English: the §F5 SSE-driven refresh hook. Pages declare the SSE types they care about plus a refresh
// fn; on mount it subscribes to sseBus (App owns the single connection) and starts a fallback interval
// (default 60s, replacing the old 5–15s per-page polling). The latest callback always runs (via ref);
// the interval and subscription are cleaned up on unmount.
import { useEffect, useRef } from 'react'
import { on } from './sseBus.js'

/**
 * @param {string|string[]} types 关心的 SSE 事件类型（'*'=全部）
 * @param {() => void} cb 事件到达 / 兜底 interval 触发时执行（通常是页面的 load()）
 * @param {{intervalMs?: number, enabled?: boolean}} [opts] intervalMs<=0 关闭兜底轮询；enabled=false 整体停用
 */
export default function useSseRefresh(types, cb, opts = {}) {
  const { intervalMs = 60000, enabled = true } = opts
  const cbRef = useRef(cb)
  useEffect(() => { cbRef.current = cb }, [cb])

  useEffect(() => {
    if (!enabled) return
    const off = on(types, () => cbRef.current && cbRef.current())
    let timer = null
    if (intervalMs > 0) timer = setInterval(() => cbRef.current && cbRef.current(), intervalMs)
    return () => { off(); if (timer) clearInterval(timer) }
    // types 变更时重订阅（页面切换筛选类型时使用）
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [enabled, intervalMs, JSON.stringify(types)])
}
