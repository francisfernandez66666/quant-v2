// §M-10（2026-09-22 PM 批清扫）轮询「请求代号 + 后到丢弃」守卫。
//
// 缺陷原文：Dashboard/Signals/Positions 的周期轮询（10s/15s、20s、60s）没有请求序列守卫——
// 上一轮请求还在途（后端慢、网络抖动）时下一轮已经发出，两轮响应按到达顺序写 state：
// **旧请求的迟到响应会把新请求刚写好的数据覆盖回去**（数据倒挂，且下一次轮询前一直显示
// 陈旧值）。api 层的 AbortController 只管超时，不能阻止这种交错。
// 用法：每页一个守卫实例（useRef 持有）；发起请求前 `const token = g.begin()`，
// await 返回后 `if (g.isStale(token)) return`——不是最新一轮就整包丢弃，不写任何 state。
// English: §M-10 — per-page stale-response guard. Each poll round stamps a monotonic token
// before the request and drops the whole payload if a newer round has already begun, so a
// slow old response can never overwrite fresher data (the api-layer AbortController only
// bounds timeouts, it does not order concurrent rounds).
// §P2-I（2026-10-06 修复批 波 6）新增 useStaleGuard()：本模块自此要在组件里持有实例，
// 因此引入 React 的 useRef——守卫实例**必须**跨渲染复用，见下方 useStaleGuard 的成因说明。
import { useRef } from 'react'

export function createStaleGuard() {
  let seq = 0
  return {
    /** 开始一轮新请求，返回本轮代号（写 state 前用 isStale 校验）。 */
    begin() {
      seq += 1
      return seq
    },
    /** 该代号是否已被更新的轮次超越（true=迟到旧响应，必须丢弃）。 */
    isStale(token) {
      return token !== seq
    },
  }
}

// §P2-I（2026-10-06 修复批 波 6）统一 hook 入口。
//
// §M-10 落码时只有三页用了这个守卫，其余含轮询/SSE 的页面各自在文件里写
// `const g = useRef(null); if (!g.current) g.current = createStaleGuard()`——
// 于是"用不用守卫"变成了每页一个口头决定，覆盖面只会随新页继续漏（本批开工前实测：
// 10 个有轮询/SSE 的页面里只有 3 个 import 了守卫）。更麻烦的是那两行样板本身也会被
// 抄歪：写成 `useRef(createStaleGuard())` 就是**每次渲染都 new 一个**（代号序列重置，
// isStale 永远 false，守卫恒绿）。
// 本 hook 把这两行唯一的正确写法收成一个调用点，页面侧只剩 `const guard = useStaleGuard()`；
// 门禁的派生扫描锁（§P2-I N1）据此断「凡有轮询/SSE 的页面必须 import 本模块」，
// 而不是再写死两份文件名。
// English: §P2-I — single hook entry so every polling page wires the guard the same way
// (lazy-init inside a ref, never `useRef(createStaleGuard())` which resets the sequence each
// render and makes the guard silently always-pass).
/**
 * 返回一个跨渲染稳定的轮询代号守卫（每个「数据路」各取一个实例）。
 * @returns {{begin: () => number, isStale: (token: number) => boolean}}
 */
export function useStaleGuard() {
  const ref = useRef(null)
  // 惰性初始化：只有首帧为 null 时才构造，后续渲染复用同一实例（代号序列才不会重置）
  if (!ref.current) ref.current = createStaleGuard()
  return ref.current
}
