// ── 测试环境配置 setup.js ──
// 在测试运行前注册 @testing-library/jest-dom 提供的扩展断言
// （如 toBeInTheDocument / toBeDisabled），供所有 *.test.js(x) 用例使用。
import '@testing-library/jest-dom'
import { configure } from '@testing-library/react'

// §FIX-6 并发超时收敛（2026-09-20）：
// 历史 flaky —— 31 个测试文件并行跑 jsdom 时，Testing Library 的默认
// asyncUtilTimeout（1000ms）在 CPU 争抢下偶发超时，表现为「193/194」（失败用例随机器负载漂移）。
// 根因不是逻辑错，而是 1000ms 的等待窗口对并行套件太紧。
// 放宽到 5s：只影响「UI 未按预期更新时等待多久才判失败」，不会掩盖真实缺陷
// ——断言始终不成立时仍会在 5s 后失败（vitest testTimeout 同步放宽，见 vitest.config.js）。
// English: raise Testing Library's async-util timeout to absorb CPU-contention jitter under the
// parallel jsdom suite; a genuinely broken assertion still fails (just after 5s instead of 1s).
configure({ asyncUtilTimeout: 5000 })
