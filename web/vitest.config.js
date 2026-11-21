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
    // §TEST-POOL（2026-10-24 实录）：本机 8GB 内存跑全量 jsdom 文件时，默认 forks 池给
    // **每个文件**起一个子进程；实测 70 个 worker 全部「Timeout waiting for worker to respond」，
    // 单文件直跑还会被 OS 直接 SIGTERM(137)。读数从"用例失败"变成"池根本起不来"，最容易被
    // 误判成代码坏了（同一天内 `-full` 门禁正是卡在这一步，23 分钟没有任何进展）。
    // 关键教训：根因是 **forks 池按文件起进程**，不是"并发"本身——threads 池共享进程、内存
    // 峰值约为 forks 的 1/5，所以并发度可以安全地往上调。
    // §TEST-POOL-2（2026-11-21）：测试文件已从 70 涨到 115，串行单 worker 让全量耗时线性增长
    // （动辄十几分钟）。改用 threads 池 + 默认 4 worker：并发跑 jsdom 文件，内存仍在 8GB 可承受
    // 范围（threads 不是 fork，不复制整个堆）；实测吞吐约为单 worker 的 3~4 倍。
    // 低内存机器用 `VITEST_MAX_WORKERS=1 npm test` 退回串行；CI/大内存可继续上调。
    // 为什么默认从 1 改 4 而不是反过来：1 是当年 forks OOM 后的应急值，把"能跑完"当成了默认值，
    // 代价是全量测试长期卡十几分钟——这个隐性成本远大于 4 threads 的内存开销。
    // English: §TEST-POOL-2 — root cause was the forks pool (one process per file), not concurrency.
    // Threads share a heap (~1/5 the memory), so raise the default to 4 workers for ~3-4x throughput
    // as the suite grew 70 -> 115 files. Override VITEST_MAX_WORKERS=1 on low-memory boxes.
    pool: 'threads',
    maxWorkers: Number(process.env.VITEST_MAX_WORKERS || 4),
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