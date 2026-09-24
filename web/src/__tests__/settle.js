// ── 确定性等待工具 settle()（§DET-TIME，2026-09-24）──
// 为什么要有这个文件：本仓前端用例原先一律用 RTL 的 waitFor / findBy* 等 UI 出现，
// 而它们是按**墙上时钟**轮询的（asyncUtilTimeout 现为 5000ms，见 setup.js §FIX-6 说明）。
// 同机器上别的项目跑测试时（邻居 vitest 负载 15~23），事件循环被抢走，
// "UI 其实已经渲染完、只是没轮到跑"就被判成超时红——09-22~09-24 反复出现，
// 且红名单每次都不一样、单跑必绿。那不是缺陷，是尺子挑错了时钟。
//
// 口径：被测的挂载/事件链全部只 await 已被 mock 成 resolved promise 的接口，
// 所以"等它到达终态"根本不需要计时器——把微任务队列排空有限轮即可确定性到达。
// 断言一个字都不放宽：没渲染就是 getByText 抛错、用例判红（且立刻红，不用等 5 秒）。
//
// 用法：
//   await settle()
//   expect(screen.getByText('退出')).toBeInTheDocument()
//
// 什么时候**不能**用它：被测路径里有真实的 setTimeout/setInterval/防抖（轮询、SSE 重连退避等）。
// 那种用例请照 m10_stale_guard.test.jsx 的先例改用 vi.useFakeTimers() + advanceTimersByTimeAsync()，
// 让"过了多久"由测试自己规定；不要退回真实时钟去"多等一会儿"。
// English: deterministic replacement for wall-clock waitFor — flush the microtask queue instead.
import { act } from '@testing-library/react'

// 默认 12 轮：本仓最厚的挂载链（App.checkAuth → refreshMe → setLoggedIn → 再拉 status/alerts）
// 实测 5 轮内就稳定到达终态，12 轮是给"以后多加一层 await"留的余量，而不是给负载留的。
// 轮数是**确定性**的（与 CPU 快慢无关），所以放大它不会引入新的偶发性。
export async function settle(rounds = 12) {
  for (let i = 0; i < rounds; i++) await act(async () => {})
}
