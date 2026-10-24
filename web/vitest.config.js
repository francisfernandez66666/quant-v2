// ── 前端单测配置 vitest.config.js ──
// jsdom 环境 + 全局断言，测试文件位于 src/__tests__/**；覆盖率用 v8。
// 并发超时参数（testTimeout/hookTimeout）与 setup.js 的 asyncUtilTimeout 配套，见 §FIX-6。
// English: frontend unit-test config (jsdom, globals, v8 coverage); timeouts pair with §FIX-6.
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: './src/__tests__/setup.js',
    // §FIX-6 并发超时收敛（2026-09-20）：并行跑 31 个 jsdom 文件时，默认 testTimeout=5000ms
    // 与放宽后的 asyncUtilTimeout（见 setup.js，5000ms）叠加易触发假超时。
    // 提高到 20s 给足 headroom；真实逻辑缺陷仍会失败（只是更慢暴露），不掩盖问题。
    // §0927AUDIT-D5（2026-09-28 修复批）：20s 对**冷 transform** 仍不够——09-27 全量审计
    // 实录首跑 import 编译耗时 39.6s，直接把没跑业务逻辑的用例判红（热缓存复跑 16.6s 即绿），
    // 与 pytest 并行时 CPU 争抢还会放大。上调到 60s：超时只可能来自真挂死，不再冒充产品缺陷。
    // English: cold vite transform alone measured 39.6s (>20s), so the old ceiling produced
    // infra flakes that looked like product bugs; 60s leaves room for cold starts while real
    // hangs/logic failures still fail.
    testTimeout: 60000,
    hookTimeout: 60000,
    // §TEST-POOL（2026-10-24 实录）：本机 8GB 内存跑全量 70 个 jsdom 文件时，默认 forks 池给
    // **每个文件**起一个子进程；实测 70 个 worker 全部「Timeout waiting for worker to respond」，
    // 单文件直跑还会被 OS 直接 SIGTERM(137)。读数从"用例失败"变成"池根本起不来"，最容易被
    // 误判成代码坏了（同一天内 `-full` 门禁正是卡在这一步，23 分钟没有任何进展）。
    // 改 threads 池 + 单 worker：内存峰值降到约 1/5，全量 70 文件 / 442 用例 141s 跑完。
    // 需要更高吞吐时用环境变量放大（`VITEST_MAX_WORKERS=4 npm test`），默认值只保"能跑完"。
    // English: §TEST-POOL — the default forks pool spawns one child per test file; on this 8GB box
    // all 70 workers timed out ("Timeout waiting for worker to respond") and single-file runs got
    // SIGTERM(137), so the reading degraded from "tests failed" to "pool never started" (exactly what
    // stalled the `-full` gate for 23 minutes). Threads + one worker cuts peak memory ~5x and runs the
    // whole suite (70 files / 442 tests) in 141s. Override with VITEST_MAX_WORKERS for more throughput.
    pool: 'threads',
    maxWorkers: Number(process.env.VITEST_MAX_WORKERS || 1),
    minWorkers: 1,
    include: ['src/__tests__/**/*.test.{js,jsx}'],
    coverage: {
      provider: 'v8',
      reporter: ['text', 'json', 'html'],
      include: ['src/**/*.{js,jsx}'],
      exclude: ['src/__tests__/**', 'src/main.jsx'],
    },
  },
})