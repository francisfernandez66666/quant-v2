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
